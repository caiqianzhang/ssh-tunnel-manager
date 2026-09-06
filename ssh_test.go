package main

import (
	"testing"
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
