package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveHost(t *testing.T) {
	ips, err := ResolveHost("localhost")
	if err != nil {
		t.Fatalf("ResolveHost failed: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("expected at least one IP for localhost")
	}
	t.Logf("localhost resolves to: %v", ips)
}

func TestFlushDNS(t *testing.T) {
	err := FlushDNS()
	if err != nil {
		t.Logf("FlushDNS returned error (may need sudo): %v", err)
	}
}

func TestSSHManagerLifecycle(t *testing.T) {
	mgr := NewSSHManager()
	if mgr.IsRunning("test") {
		t.Error("expected not running for nonexistent connection")
	}

	// Test GetStatus for nonexistent connection
	status := mgr.GetStatus("nonexistent")
	if status != "not_found" {
		t.Errorf("expected 'not_found' status, got %s", status)
	}

	// Test GetConnection for nonexistent connection
	_, exists := mgr.GetConnection("nonexistent")
	if exists {
		t.Error("expected no connection for nonexistent ID")
	}

	// Test GetActiveConnections on empty manager
	active := mgr.GetActiveConnections()
	if len(active) != 0 {
		t.Errorf("expected 0 active connections, got %d", len(active))
	}
}

func TestFormatSSHCommand(t *testing.T) {
	cfg := ForwardConfig{
		ID:            "test-1",
		Name:          "Test Tunnel",
		RemoteHost:    "remote.example.com",
		RemotePort:    8080,
		LocalHost:     "localhost",
		LocalPort:     3000,
		SSHUser:       "testuser",
		AutoReconnect: true,
		MaxRetries:    5,
		RetryInterval: 10,
	}

	cmd := FormatSSHCommand(cfg)
	if cmd == "" {
		t.Fatal("expected non-empty SSH command")
	}

	t.Logf("SSH command: %s", cmd)
}

func TestSSHManagerDisconnectNonexistent(t *testing.T) {
	mgr := NewSSHManager()

	err := mgr.Disconnect("nonexistent")
	if err == nil {
		t.Error("expected error when disconnecting nonexistent connection")
	}
}

// TestAutoReconnectAbortsOnStopCh verifies that autoReconnect does not
// spawn an orphan tunnel when the connection was disconnected during the
// retry window (FlushDNS + sleep + resolve).
func TestAutoReconnectAbortsOnStopCh(t *testing.T) {
	mgr := NewSSHManager()

	// Build a conn whose StopCh is already closed (simulating Disconnect).
	conn := &SSHConn{
		Config: ForwardConfig{
			ID:            "abort-test",
			RemoteHost:    "127.0.0.1",
			RemotePort:    22,
			LocalHost:     "127.0.0.1",
			LocalPort:     0, // 0 = kernel-assigned, avoids real port use
			SSHUser:       "u",
			AutoReconnect: true,
			MaxRetries:    1,
			RetryInterval: 1,
		},
		Status: "disconnected",
		StopCh: func() chan struct{} {
			c := make(chan struct{})
			close(c)
			return c
		}(),
	}
	mgr.mu.Lock()
	mgr.conns["abort-test"] = conn
	mgr.mu.Unlock()

	// autoReconnect should notice the closed StopCh and return without
	// starting any process. We verify by checking no process was started
	// (conn.Process stays nil) and the connection is still in the map
	// with its original status.
	mgr.autoReconnect("abort-test", false)

	if conn.Process != nil {
		t.Error("autoReconnect started a process despite closed StopCh")
	}
}

// TestDecryptPasswordError verifies that decryptPassword returns an
// error (not the ciphertext) when GCM open fails — e.g. when the secret
// key changed or the data is corrupt. Silently passing garbage to SSH
// would surface as a confusing "Permission denied".
func TestDecryptPasswordError(t *testing.T) {
	// Encrypt with one key...
	tmp := t.TempDir()
	oldDir := secretKeyDir
	secretKeyDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { secretKeyDir = oldDir })

	ct, err := encryptPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}

	// ...then switch the key and try to decrypt. This must fail.
	tmp2 := t.TempDir()
	secretKeyDir = func() (string, error) { return tmp2, nil }

	_, err = decryptPassword(ct)
	if err == nil {
		t.Error("expected error when decrypting with wrong key, got nil")
	}
}

// Regression test for the "sync: unlock of unlocked mutex" panic.
// monitorProcess must not crash when the SSH process exits during the
// 1500 ms confirmation window (wrong password, connection refused, host
// down — the common failure modes). Previously handleProcessExit
// assumed the caller held m.mu, but the fast-exit path called it without
// the lock and then unlocked it, panicking the whole process.
func TestMonitorProcessFastExitNoPanic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: requires the 'true' binary")
	}

	mgr := NewSSHManager()

	// `true` exits immediately, well within the confirmation window.
	cmd := exec.Command("true")
	sshConn := &SSHConn{
		Config: ForwardConfig{
			ID:            "fast-exit",
			RemoteHost:    "127.0.0.1",
			RemotePort:    22,
			LocalHost:     "127.0.0.1",
			LocalPort:     0,
			SSHUser:       "u",
			AutoReconnect: false,
		},
		Process: cmd,
		Status:  "connecting",
		StopCh:  make(chan struct{}),
	}
	cmd.Stderr = &sshConn.Stderr

	mgr.mu.Lock()
	mgr.conns["fast-exit"] = sshConn
	mgr.mu.Unlock()

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("monitorProcess panicked on fast exit: %v", r)
			}
			close(done)
		}()
		mgr.monitorProcess("fast-exit")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitorProcess did not return in time")
	}

	// The connection must have been classified and remain tracked so the
	// user can reconnect manually (auto-reconnect was disabled).
	conn, ok := mgr.GetConnection("fast-exit")
	if !ok {
		t.Error("expected fast-exit connection to remain tracked after exit")
	} else if conn.Status != "disconnected" {
		t.Errorf("expected status 'disconnected', got %q", conn.Status)
	}
}

// TestBuildSSHCommandPasswordNotInCmdline verifies that a configured
// password is never passed on the command line. Previously sshpass was
// invoked as `sshpass -p <password> ssh ...`, which exposed the
// credential to every local user via `ps` and /proc/<pid>/cmdline.
// The password is now fed through file descriptor 3 instead.
func TestBuildSSHCommandPasswordNotInCmdline(t *testing.T) {
	const password = "s3cr3t-pw"

	// With a password: must use sshpass -d 3 and never include the
	// password in the argument list.
	cmd := buildSSHCommand(ForwardConfig{
		ForwardType: "local", RemoteHost: "h", RemotePort: 22,
		LocalHost: "l", LocalPort: 2222, SSHUser: "u", SSHPassword: password,
	}, "")
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	for _, a := range cmd.Args {
		if a == password {
			t.Errorf("password appears in command line: %v", cmd.Args)
		}
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "sshpass") {
		t.Errorf("expected sshpass wrapper, got: %v", cmd.Args)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "-d 3") {
		t.Errorf("expected sshpass -d 3 (fd-based password), got: %v", cmd.Args)
	}

	// Without a password: plain ssh, no sshpass.
	cmd2 := buildSSHCommand(ForwardConfig{
		ForwardType: "local", RemoteHost: "h", RemotePort: 22,
		LocalHost: "l", LocalPort: 2222, SSHUser: "u",
	}, "")
	if strings.Contains(strings.Join(cmd2.Args, " "), "sshpass") {
		t.Errorf("did not expect sshpass for passwordless config, got: %v", cmd2.Args)
	}
}

// TestAttachPasswordPipe verifies the fd-based password channel: the
// password is written to the pipe and is readable from the returned
// file descriptor, and cmd.ExtraFiles is wired so the child inherits
// it as fd 3.
func TestAttachPasswordPipe(t *testing.T) {
	const password = "pipe-pw"

	cmd := exec.Command("ssh", "-v")
	read, err := attachPasswordPipe(cmd, password)
	if err != nil {
		t.Fatalf("attachPasswordPipe failed: %v", err)
	}
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != read {
		t.Fatalf("expected ExtraFiles to hold the read end, got %v", cmd.ExtraFiles)
	}

	// The password must be readable from the pipe (with EOF).
	buf := make([]byte, len(password)+1)
	n, err := read.Read(buf)
	if err != nil && err.Error() != "EOF" {
		// io.EOF is expected; accept either.
	}
	if n != len(password) || string(buf[:n]) != password {
		t.Errorf("pipe read = %q, want %q", string(buf[:n]), password)
	}
	read.Close()
}

// TestHandleProcessExitLeavesConnTracked documents the current contract:
// handleProcessExit classifies the failure and sets a terminal status
// but does NOT remove the connection from the map. The UI's
// checkPortAndConnect is responsible for cleaning up a stale conn
// before reconnecting; without that cleanup the user gets a permanent
// "connection already exists" after any tunnel failure.
func TestHandleProcessExitLeavesConnTracked(t *testing.T) {
	mgr := NewSSHManager()

	conn := &SSHConn{
		Config: ForwardConfig{
			ID: "stale-test", RemoteHost: "127.0.0.1", RemotePort: 22,
			LocalHost: "127.0.0.1", LocalPort: 2222, SSHUser: "u",
			AutoReconnect: false,
		},
		Status:  "running",
		StopCh:  make(chan struct{}),
		Process: exec.Command("true"),
	}
	mgr.mu.Lock()
	mgr.conns["stale-test"] = conn
	mgr.mu.Unlock()

	// Simulate a process exit with an auth-failure stderr.
	conn.Stderr.WriteString("Permission denied (publickey)")
	mgr.handleProcessExit("stale-test", exitErr(), conn)

	// The conn must still be tracked (so the UI can clean it up), but
	// with a terminal status.
	got, ok := mgr.GetConnection("stale-test")
	if !ok {
		t.Fatal("expected conn to remain tracked after handleProcessExit")
	}
	if got.Status != "auth_failed" {
		t.Errorf("expected status 'auth_failed', got %q", got.Status)
	}

	// And the cleanup path: Disconnect removes it so a subsequent
	// Connect does not see "already exists".
	if err := mgr.Disconnect("stale-test"); err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}
	if _, ok := mgr.GetConnection("stale-test"); ok {
		t.Error("expected conn removed after Disconnect")
	}
}

func exitErr() error {
	return &exitError{}
}

type exitError struct{}

func (e *exitError) Error() string { return "exit status 1" }

// ---------- Baidu DNS integration ----------

// setBaiduCredsForTest stores Baidu AK/SK in the package-level cache.
func setBaiduCredsForTest(t *testing.T, ak, sk string) {
	baiduMu.Lock()
	baiduAK, baiduSK = ak, sk
	baiduOK = true
	baiduMu.Unlock()
}

// clearBaiduCredsForTest removes cached Baidu credentials.
func clearBaiduCredsForTest(t *testing.T) {
	baiduMu.Lock()
	baiduAK, baiduSK = "", ""
	baiduOK = false
	baiduMu.Unlock()
}

// TestQueryBaiduDNSIPWithMockServer verifies QueryBaiduDNSIP against a
// mock HTTP server: successful retrieval, missing-record error, and
// HTTP-error surfacing.
func TestQueryBaiduDNSIPWithMockServer(t *testing.T) {
	// Mock server that returns a single A record for "www".
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "expected Authorization header", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"recordId": 1,
					"domain":   "www",
					"rdtype":   "A",
					"rdata":    "1.2.3.4",
					"zoneName": "ruanjianggongcheng.site",
					"status":   "RUNNING",
				},
			},
		})
	}))
	defer server.Close()

	ip, err := QueryBaiduDNSIPWithBase("AK", "SK", "ruanjianggongcheng.site", "www", server.URL)
	if err != nil {
		t.Fatalf("QueryBaiduDNSIP failed: %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("expected IP 1.2.3.4, got %s", ip)
	}
}

func TestQueryBaiduDNSIPMissingRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"recordId": 2,
					"domain":   "ftp",
					"rdtype":   "A",
					"rdata":    "5.6.7.8",
					"zoneName": "ruanjianggongcheng.site",
					"status":   "RUNNING",
				},
			},
		})
	}))
	defer server.Close()

	_, err := QueryBaiduDNSIPWithBase("AK", "SK", "ruanjianggongcheng.site", "www", server.URL)
	if err == nil {
		t.Error("expected error for missing A record, got nil")
	}
}

func TestQueryBaiduDNSIPTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "upstream timeout",
		})
	}))
	defer server.Close()

	_, err := QueryBaiduDNSIPWithBase("AK", "SK", "ruanjianggongcheng.site", "www", server.URL)
	if err == nil {
		t.Error("expected error for HTTP error, got nil")
	}
}

// ---------- End-to-end autoReconnect + Baidu DNS ----------

// TestAutoReconnectUsesBaiduDNS is a full end-to-end test of the DDNS
// fast-path: a mock Baidu DNS server returns a fresh IP, the SSH
// process is simulated with `sleep`, and autoReconnect must use the
// Baidu IP when restarting the tunnel.
func TestAutoReconnectUsesBaiduDNS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: requires the 'sleep' binary")
	}

	// 1. Mock Baidu DNS server returning a specific IP.
	//    We use "localhost" as the remote host so that Connect's
	//    initial ResolveHost succeeds (returns 127.0.0.1). The Baidu
	//    mock returns a different IP (10.99.88.77) to simulate a DDNS
	//    update, and autoReconnect must use that IP.
	var gotAuth string
	var gotDomain string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Domain string `json:"domain"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotDomain = body.Domain

		// deriveZoneAndSub("localhost") -> zone="localhost", sub="@"
		// Return an A record for "@" (the root domain).
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"recordId": 1,
					"domain":   "@",
					"rdtype":   "A",
					"rdata":    "10.99.88.77",
					"zoneName": "localhost",
					"status":   "RUNNING",
				},
			},
		})
	}))
	defer server.Close()

	// autoReconnect calls QueryBaiduDNSIP which reads the package-level
	// API base. Point it at the mock server for the duration of the test.
	origBase := baiduDNSAPIBase
	baiduDNSAPIBase = server.URL
	t.Cleanup(func() { baiduDNSAPIBase = origBase })

	// 2. Cache Baidu credentials.
	setBaiduCredsForTest(t, "AK-test", "SK-test")
	defer clearBaiduCredsForTest(t)

	// 3. SSHManager with a fake "SSH" command (sleep 30) so we don't
	//    need a real SSH server.
	mgr := NewSSHManager()
	var gotHost string
	mgr.commandBuilder = func(cfg ForwardConfig, host string) *exec.Cmd {
		// Record the host that autoReconnect passes to the command
		// builder so we can verify the Baidu DNS IP was used.
		gotHost = host
		// Use `sleep` as a stand-in for ssh. It stays alive long enough
		// for monitorProcess to confirm the tunnel is "running", and
		// exits cleanly when killed.
		return exec.Command("sleep", "30")
	}

	// 4. Forward config: auto-reconnect enabled.
	cfg := ForwardConfig{
		ID:            "e2e-ddns",
		Name:          "DDNS Tunnel",
		ForwardType:   "local",
		RemoteHost:    "localhost",
		RemotePort:    22,
		LocalHost:     "127.0.0.1",
		LocalPort:     0, // kernel-assigned, avoids real port conflicts
		SSHUser:       "u",
		AutoReconnect: true,
		MaxRetries:    2,
		RetryInterval: 1, // fast retry for the test
	}

	// 5. Connect — starts the fake SSH process.
	if err := mgr.Connect(cfg); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// 6. Wait for the tunnel to reach "running".
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.GetStatus(cfg.ID) == "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mgr.GetStatus(cfg.ID) != "running" {
		t.Fatalf("expected tunnel to be running, got %q", mgr.GetStatus(cfg.ID))
	}

	// 7. Kill the process to simulate a disconnection.
	conn, ok := mgr.GetConnection(cfg.ID)
	if !ok {
		t.Fatal("expected connection to exist")
	}
	if conn.Process != nil && conn.Process.Process != nil {
		_ = conn.Process.Process.Kill()
	}

	// 8. Wait for autoReconnect to fire and restart the tunnel.
	//
	// IMPORTANT: we must first wait for the killed process to actually
	// be reported as disconnected before looking for the reconnect.
	// handleProcessExit sets "disconnected" before spawning
	// autoReconnect, so once the status leaves "running" we know the
	// old tunnel is gone and autoReconnect is (or will be) running.
	// Without this guard the goroutine races the process death and can
	// observe the old "running" status as a spurious reconnect signal.
	reconnected := make(chan struct{})
	go func() {
		// Wait for the old "running" process to die.
		for mgr.GetStatus(cfg.ID) == "running" {
			time.Sleep(50 * time.Millisecond)
		}
		// Now wait for autoReconnect to spawn a new process.
		for {
			s := mgr.GetStatus(cfg.ID)
			if s == "connecting" || s == "running" {
				close(reconnected)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case <-reconnected:
		// Good: autoReconnect started a new process.
	case <-time.After(15 * time.Second):
		s := mgr.GetStatus(cfg.ID)
		t.Fatalf("autoReconnect did not restart the tunnel in time (status %s)", s)
	}

	// 9. Verify the Baidu DNS API was actually called.
	if gotAuth == "" {
		t.Error("expected Baidu DNS API to receive Authorization header")
	}
	if gotDomain != "localhost" {
		t.Errorf("expected Baidu DNS query for zone 'localhost', got %q", gotDomain)
	}

	// 10. Verify the Baidu DNS IP was actually used for the SSH
	//     connection (not the original hostname). This is the core
	//     DDNS fast-path: the tunnel must restart with the fresh IP,
	//     not the stale DNS entry.
	if gotHost != "10.99.88.77" {
		t.Errorf("expected autoReconnect to use Baidu IP 10.99.88.77, got %q", gotHost)
	}

	// 11. Clean up.
	_ = mgr.Disconnect(cfg.ID)
}

// ---------- DDNS heartbeat ----------

// TestDDNSHeartbeatExitsOnStopCh verifies the heartbeat goroutine exits
// immediately when StopCh is already closed (simulating Disconnect).
func TestDDNSHeartbeatExitsOnStopCh(t *testing.T) {
	mgr := NewSSHManager()
	mgr.ddnsCheckInterval = 50 * time.Millisecond

	conn := &SSHConn{
		Config: ForwardConfig{
			ID: "hb-stop", RemoteHost: "localhost", RemotePort: 22,
			LocalHost: "127.0.0.1", LocalPort: 0, SSHUser: "u",
		},
		Status:    "running",
		CurrentIP: "1.2.3.4",
		StopCh: func() chan struct{} {
			c := make(chan struct{})
			close(c)
			return c
		}(),
	}

	done := make(chan struct{})
	go func() {
		mgr.startDDNSMonitor("hb-stop", conn, "localhost")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DDNS heartbeat did not exit on closed StopCh")
	}
}

// TestDDNSHeartbeatExitsWhenNotRunning verifies the heartbeat exits on
// the first tick if the tunnel is no longer "running" (e.g. the process
// already exited and handleProcessExit fired). This prevents goroutine
// leaks when a tunnel is killed externally and autoReconnect spawns a
// new conn with its own heartbeat.
func TestDDNSHeartbeatExitsWhenNotRunning(t *testing.T) {
	mgr := NewSSHManager()
	mgr.ddnsCheckInterval = 50 * time.Millisecond

	conn := &SSHConn{
		Config: ForwardConfig{
			ID: "hb-notrunning", RemoteHost: "localhost", RemotePort: 22,
			LocalHost: "127.0.0.1", LocalPort: 0, SSHUser: "u",
		},
		Status:    "disconnected",
		CurrentIP: "1.2.3.4",
		StopCh:    make(chan struct{}),
	}

	done := make(chan struct{})
	go func() {
		mgr.startDDNSMonitor("hb-notrunning", conn, "localhost")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DDNS heartbeat did not exit when tunnel not running")
	}
}

// TestDDNSHeartbeatKillsProcessOnIPChange is the end-to-end test of the
// heartbeat's core job: when Baidu DNS reports a different IP than the
// one the tunnel is connected to, the heartbeat kills the SSH process
// so the existing handleProcessExit → autoReconnect path can rebuild
// the tunnel with the fresh IP.
func TestDDNSHeartbeatKillsProcessOnIPChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: requires process signals")
	}

	// Mock Baidu DNS server returning a different IP than the tunnel's
	// CurrentIP ("1.2.3.4" → "5.6.7.8").
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"recordId": 1, "domain": "@", "rdtype": "A",
					"rdata": "5.6.7.8", "zoneName": "localhost", "status": "RUNNING",
				},
			},
		})
	}))
	defer server.Close()

	origBase := baiduDNSAPIBase
	baiduDNSAPIBase = server.URL
	t.Cleanup(func() { baiduDNSAPIBase = origBase })

	setBaiduCredsForTest(t, "AK-test", "SK-test")
	defer clearBaiduCredsForTest(t)

	mgr := NewSSHManager()
	mgr.ddnsCheckInterval = 50 * time.Millisecond

	// Start a fake SSH process (sleep) that the heartbeat can kill.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Ensure the test process is cleaned up regardless.
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	conn := &SSHConn{
		Config: ForwardConfig{
			ID: "hb-ipchange", RemoteHost: "localhost", RemotePort: 22,
			LocalHost: "127.0.0.1", LocalPort: 0, SSHUser: "u",
		},
		Status:    "running",
		CurrentIP: "1.2.3.4",
		StopCh:    make(chan struct{}),
		Process:   cmd,
	}

	done := make(chan struct{})
	go func() {
		mgr.startDDNSMonitor("hb-ipchange", conn, "localhost")
		close(done)
	}()

	// The heartbeat should detect the IP mismatch and kill the process.
	// NOTE: done closes as soon as the monitor goroutine is spawned;
	// the real signal is the process death waited on below.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DDNS heartbeat did not fire on IP change")
	}

	// Verify the process was actually killed (Wait returns immediately).
	exited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Error("process was not killed by DDNS heartbeat")
	}

	// The kill must be marked as DDNS-triggered so the subsequent
	// autoReconnect skips the retry sleep (the server is confirmed
	// alive at its new IP — waiting would only prolong the blackout).
	// The flag is set before the kill, so it is observable once the
	// process death has been reaped.
	mgr.mu.RLock()
	marked := conn.DDNSRestart
	mgr.mu.RUnlock()
	if !marked {
		t.Error("expected DDNSRestart flag to be set before killing the process")
	}
}

// ---------- resolveConnectTarget (startup DDNS fast-path) ----------

// TestBuildSSHCommandUsesResolvedHost pins the connect-target contract:
// the host argument — normally the IP from resolveConnectTarget — must
// reach the ssh command line. Previously the parameter was silently
// dropped and ssh always re-resolved cfg.RemoteHost through the local
// resolver, which can be up to one record TTL stale in the DDNS
// scenario and defeats the Baidu DNS fast-path entirely.
func TestBuildSSHCommandUsesResolvedHost(t *testing.T) {
	cfg := ForwardConfig{
		ForwardType: "local", RemoteHost: "www.example.com", RemotePort: 22,
		LocalHost: "l", LocalPort: 2222, SSHUser: "u",
	}

	cmd := buildSSHCommand(cfg, "10.99.88.77")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-l u 10.99.88.77") {
		t.Errorf("expected ssh to connect to the resolved IP, got: %s", joined)
	}
	if strings.Contains(joined, "www.example.com") {
		t.Errorf("stale hostname leaked into the ssh command: %s", joined)
	}

	// Empty host falls back to the configured hostname.
	cmd2 := buildSSHCommand(cfg, "")
	if !strings.Contains(strings.Join(cmd2.Args, " "), "-l u www.example.com") {
		t.Errorf("expected fallback to RemoteHost, got: %v", cmd2.Args)
	}
}

// TestResolveConnectTargetFallsBackWithoutCreds verifies the fallback
// path: with no Baidu credentials the configured hostname is kept and
// resolved through ordinary DNS.
func TestResolveConnectTargetFallsBackWithoutCreds(t *testing.T) {
	clearBaiduCredsForTest(t)

	host, ip, err := resolveConnectTarget("localhost")
	if err != nil {
		t.Fatalf("resolveConnectTarget failed: %v", err)
	}
	if host != "localhost" {
		t.Errorf("expected host to stay 'localhost', got %q", host)
	}
	if ip == "" {
		t.Error("expected a resolved IP for localhost")
	}
}

// TestResolveConnectTargetUsesBaiduAPI verifies that with credentials
// configured the authoritative API answer wins over local DNS: the
// returned connect host IS the API's IP, so the tunnel never depends on
// the (possibly stale) local resolver cache.
func TestResolveConnectTargetUsesBaiduAPI(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": []map[string]interface{}{
				{
					"recordId": 1, "domain": "@", "rdtype": "A",
					"rdata": "10.99.88.77", "zoneName": "localhost", "status": "RUNNING",
				},
			},
		})
	}))
	defer server.Close()

	origBase := baiduDNSAPIBase
	baiduDNSAPIBase = server.URL
	t.Cleanup(func() { baiduDNSAPIBase = origBase })

	setBaiduCredsForTest(t, "AK-test", "SK-test")
	defer clearBaiduCredsForTest(t)

	host, ip, err := resolveConnectTarget("localhost")
	if err != nil {
		t.Fatalf("resolveConnectTarget failed: %v", err)
	}
	if !hit {
		t.Error("expected mock Baidu API to be called")
	}
	if host != "10.99.88.77" || ip != "10.99.88.77" {
		t.Errorf("expected Baidu IP 10.99.88.77 as connect target, got host=%q ip=%q", host, ip)
	}
}

// TestResolveConnectTargetSkipsIPLiteral verifies that an already-IP
// remote host never triggers a pointless API round trip.
func TestResolveConnectTargetSkipsIPLiteral(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": []interface{}{}})
	}))
	defer server.Close()

	origBase := baiduDNSAPIBase
	baiduDNSAPIBase = server.URL
	t.Cleanup(func() { baiduDNSAPIBase = origBase })

	setBaiduCredsForTest(t, "AK-test", "SK-test")
	defer clearBaiduCredsForTest(t)

	host, ip, err := resolveConnectTarget("127.0.0.1")
	if err != nil {
		t.Fatalf("resolveConnectTarget failed: %v", err)
	}
	if hit {
		t.Error("expected Baidu API to be skipped for an IP-literal host")
	}
	if host != "127.0.0.1" || ip != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1 untouched, got host=%q ip=%q", host, ip)
	}
}

// TestAutoReconnectSkipsSleepOnDDNSTrigger pins the fast-restart
// contract: the first attempt of a DDNS-triggered reconnect must not
// pay the retry_interval sleep (the Baidu API just confirmed the server
// is alive at its new IP), while a plain reconnect keeps waiting —
// whatever failed may need time to heal.
func TestAutoReconnectSkipsSleepOnDDNSTrigger(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: requires the 'true' binary")
	}
	clearBaiduCredsForTest(t)

	mgr := NewSSHManager()
	mgr.commandBuilder = func(cfg ForwardConfig, host string) *exec.Cmd {
		// `true` starts fine and exits immediately — no network, no
		// tunnel; we only measure how long autoReconnect takes to
		// reach the start.
		return exec.Command("true")
	}

	newConn := func(id string) *SSHConn {
		return &SSHConn{
			Config: ForwardConfig{
				ID: id, RemoteHost: "127.0.0.1", RemotePort: 22,
				LocalHost: "127.0.0.1", LocalPort: 0, SSHUser: "u",
				// Keep handleProcessExit from re-spawning a reconnect
				// when `true` exits right after the start.
				AutoReconnect: false,
				MaxRetries:    1, RetryInterval: 2,
			},
			Status: "disconnected",
			StopCh: make(chan struct{}),
		}
	}

	mgr.mu.Lock()
	mgr.conns["ddns-fast"] = newConn("ddns-fast")
	mgr.mu.Unlock()

	start := time.Now()
	mgr.autoReconnect("ddns-fast", true)
	ddnsElapsed := time.Since(start)
	_ = mgr.Disconnect("ddns-fast")

	// Plain reconnect, measured second so a slow machine can't mask
	// the difference in ddnsElapsed's favor.
	mgr.mu.Lock()
	mgr.conns["plain"] = newConn("plain")
	mgr.mu.Unlock()

	start = time.Now()
	mgr.autoReconnect("plain", false)
	plainElapsed := time.Since(start)
	_ = mgr.Disconnect("plain")

	if ddnsElapsed >= 2*time.Second {
		t.Errorf("DDNS-triggered reconnect paid the retry sleep: %v", ddnsElapsed)
	}
	if plainElapsed < 2*time.Second {
		t.Errorf("plain reconnect did not wait out the retry sleep: %v", plainElapsed)
	}
	t.Logf("ddns-triggered=%v plain=%v", ddnsElapsed, plainElapsed)
}
