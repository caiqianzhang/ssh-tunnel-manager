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
