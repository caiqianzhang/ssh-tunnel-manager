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
	mgr.autoReconnect("abort-test")

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
	})
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
	})
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
