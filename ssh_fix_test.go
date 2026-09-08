package main

import (
	"runtime"
	"strings"
	"testing"
)

// Bug 26: buildSSHCommand for "remote" forward type must use cfg.LocalHost
// (not hardcoded 127.0.0.1) as the destination, because -R forwards
// from SSH server to client side, where the destination is relative
// to the SSH client.
func TestBuildSSHCommand_RemoteForwardUsesLocalHost(t *testing.T) {
	cfg := ForwardConfig{
		ForwardType: "remote",
		LocalHost:   "192.168.1.5",
		LocalPort:   8080,
		RemotePort:  9000,
		RemoteHost:  "server.example.com",
		SSHUser:     "alice",
	}
	formatted := "ssh " + strings.Join(sshArgs(cfg, ""), " ")
	if !strings.Contains(formatted, "192.168.1.5") {
		t.Errorf("remote forward must include LocalHost as destination; got: %s", formatted)
	}
	if strings.Contains(formatted, "127.0.0.1") {
		t.Errorf("remote forward must NOT use 127.0.0.1 as destination; got: %s", formatted)
	}
}

// Bug 26 (local): for local forwarding, target is relative to the SSH server,
// so 127.0.0.1 (loopback on the server side) is the conventional default.
func TestBuildSSHCommand_LocalForwardKeeps127(t *testing.T) {
	cfg := ForwardConfig{
		ForwardType: "local",
		LocalHost:   "192.168.1.5",
		LocalPort:   8080,
		RemotePort:  9000,
		RemoteHost:  "server.example.com",
		SSHUser:     "alice",
	}
	formatted := "ssh " + strings.Join(sshArgs(cfg, ""), " ")
	if !strings.Contains(formatted, "127.0.0.1") {
		t.Errorf("local forward must use 127.0.0.1 (loopback on server); got: %s", formatted)
	}
}

// Bug 27: extractPIDFromSS must handle the common ss output formats
// across distributions, including ones with no space after the comma.
func TestExtractPIDFromSS(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name: "standard ipv4 with space after pid",
			input: `State  Recv-Q Send-Q Local Address:Port Peer Address:PortProcess
LISTEN 0      128        127.0.0.1:2225      0.0.0.0:*    users:(("ssh",pid=381340,fd=5))`,
			expect: "381340",
		},
		{
			name:   "ipv6 listener with no space after comma",
			input:  `LISTEN 0 128 [::1]:2225 [::]:* users:(("ssh",pid=12345,fd=4))`,
			expect: "12345",
		},
		{
			name:   "no pid at all",
			input:  "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*",
			expect: "",
		},
		{
			name: "multiple listeners, first pid wins",
			input: `LISTEN 0 128 127.0.0.1:2225 0.0.0.0:* users:(("a",pid=111,fd=5))
LISTEN 0 128 [::1]:2225 [::]:* users:(("a",pid=222,fd=4))`,
			expect: "111",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPIDFromSS(tc.input)
			if got != tc.expect {
				t.Errorf("extractPIDFromSS(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

// Bug 12: Disconnect must NOT leak the SSHConn. After Disconnect, the
// SSHManager must not retain the SSHConn, and the *exec.Cmd must be
// detached so that its captured stderr buffer can be GC'd.
func TestDisconnectDoesNotLeakSSHConn(t *testing.T) {
	mgr := NewSSHManager()
	// Inject a fake SSHConn to simulate "currently connected". StopCh
	// must be a real channel (production always creates one in Connect).
	mgr.mu.Lock()
	mgr.conns["leak-test"] = &SSHConn{
		Config: ForwardConfig{ID: "leak-test", Name: "leak"},
		Status: "running",
		StopCh: make(chan struct{}),
	}
	mgr.mu.Unlock()

	// Sanity: it is present.
	if _, ok := mgr.GetConnection("leak-test"); !ok {
		t.Fatal("setup: expected leak-test to be tracked")
	}

	// But there is no real process, so Disconnect will fail to Kill.
	// We only care that the conn is removed from the map regardless.
	_ = mgr.Disconnect("leak-test")

	if _, ok := mgr.GetConnection("leak-test"); ok {
		t.Errorf("Disconnect must remove conn from map (memory leak)")
	}
}

// Bug 12b: double Disconnect on the same id must be safe (second call
// returns an error and does not panic on closed channel).
func TestDoubleDisconnectIsSafe(t *testing.T) {
	mgr := NewSSHManager()
	mgr.mu.Lock()
	mgr.conns["dup"] = &SSHConn{
		Config: ForwardConfig{ID: "dup", Name: "dup"},
		Status: "running",
		StopCh: make(chan struct{}),
	}
	mgr.mu.Unlock()

	if err := mgr.Disconnect("dup"); err != nil {
		t.Fatalf("first Disconnect: %v", err)
	}
	// Second call must not panic.
	if err := mgr.Disconnect("dup"); err == nil {
		t.Errorf("second Disconnect should error (not_found), got nil")
	}
}

// Bug 28: CheckPortInUse must complete fast for unbound ports.
// With a 100ms timeout the function is too slow; we now expect <= 200ms.
// The 200ms ceiling is generous enough to absorb transient system load
// (a localhost TCP RST is typically <1ms) while still catching a
// regression where the dial timeout crept back up.
func TestCheckPortInUseIsFastForFreePort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows TCP loopback differs; skipping timing assertion")
	}
	// Pick a port unlikely to be bound.
	addr := listenFreePort(t)
	if addr == 0 {
		t.Skip("could not allocate a free port")
	}
	start := nowMillis()
	inUse, _, err := CheckPortInUse(addr)
	elapsed := nowMillis() - start
	if err != nil {
		t.Fatalf("CheckPortInUse error: %v", err)
	}
	// The port may have been snatched between listenFreePort (bind+close)
	// and CheckPortInUse; skip rather than fail in that rare case.
	if inUse {
		t.Skipf("port %d was taken between allocation and check; skipping timing assertion", addr)
	}
	if elapsed > 200 {
		t.Errorf("CheckPortInUse took %dms for a free port; expected <= 200ms", elapsed)
	}
}
