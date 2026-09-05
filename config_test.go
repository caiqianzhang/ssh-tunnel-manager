package main

import (
	"os"
	"testing"
)

func TestSaveAndLoadConfig(t *testing.T) {
	tmpFile := t.TempDir() + "/test_config.json"
	defer os.Remove(tmpFile)

	original := AppState{
		Forwards: []ForwardConfig{
			{
				ID: "test-1", Name: "Test Server", RemoteHost: "example.com",
				RemotePort: 8080, LocalHost: "localhost", LocalPort: 80,
				SSHUser: "admin", AutoReconnect: true, MaxRetries: 5, RetryInterval: 5,
			},
		},
	}

	if err := SaveConfig(tmpFile, &original); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loaded, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(loaded.Forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(loaded.Forwards))
	}
	if loaded.Forwards[0].Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got '%s'", loaded.Forwards[0].Name)
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Forwards) != 0 {
		t.Errorf("expected empty forwards, got %d", len(cfg.Forwards))
	}
}

func TestAddForward(t *testing.T) {
	cm := NewConfigManager("/tmp/test.json")

	fwd := ForwardConfig{
		ID: "test-add", Name: "Add Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: false, MaxRetries: 3, RetryInterval: 10,
	}

	cm.AddForward(fwd)

	forwards := cm.GetForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(forwards))
	}
	if forwards[0].ID != "test-add" {
		t.Errorf("expected ID 'test-add', got '%s'", forwards[0].ID)
	}
}

func TestUpdateForward(t *testing.T) {
	cm := NewConfigManager("/tmp/test.json")

	fwd1 := ForwardConfig{
		ID: "test-update", Name: "Original", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: false, MaxRetries: 3, RetryInterval: 10,
	}
	cm.AddForward(fwd1)

	fwd2 := ForwardConfig{
		ID: "test-update", Name: "Updated", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 3333,
		SSHUser: "user", AutoReconnect: true, MaxRetries: 5, RetryInterval: 5,
	}

	updated := cm.UpdateForward("test-update", fwd2)
	if !updated {
		t.Fatal("expected update to succeed")
	}

	forwards := cm.GetForwards()
	if forwards[0].Name != "Updated" {
		t.Errorf("expected name 'Updated', got '%s'", forwards[0].Name)
	}
	if forwards[0].LocalPort != 3333 {
		t.Errorf("expected local port 3333, got %d", forwards[0].LocalPort)
	}
}

func TestDeleteForward(t *testing.T) {
	cm := NewConfigManager("/tmp/test.json")

	fwd := ForwardConfig{
		ID: "test-delete", Name: "Delete Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: false, MaxRetries: 3, RetryInterval: 10,
	}
	cm.AddForward(fwd)

	deleted := cm.DeleteForward("test-delete")
	if !deleted {
		t.Fatal("expected delete to succeed")
	}

	forwards := cm.GetForwards()
	if len(forwards) != 0 {
		t.Errorf("expected 0 forwards after delete, got %d", len(forwards))
	}
}

func TestGetForward(t *testing.T) {
	cm := NewConfigManager("/tmp/test.json")

	fwd := ForwardConfig{
		ID: "test-get", Name: "Get Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: false, MaxRetries: 3, RetryInterval: 10,
	}
	cm.AddForward(fwd)

	got, found := cm.GetForward("test-get")
	if !found {
		t.Fatal("expected to find forward")
	}
	if got.Name != "Get Test" {
		t.Errorf("expected name 'Get Test', got '%s'", got.Name)
	}

	_, found = cm.GetForward("nonexistent")
	if found {
		t.Fatal("expected not to find nonexistent forward")
	}
}

func TestConfigManagerLoadSave(t *testing.T) {
	tmpFile := t.TempDir() + "/test_config_manager.json"
	defer os.Remove(tmpFile)

	cm1 := NewConfigManager(tmpFile)
	fwd := ForwardConfig{
		ID: "mgr-test", Name: "Manager Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: true, MaxRetries: 3, RetryInterval: 10,
	}
	cm1.AddForward(fwd)

	if err := cm1.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cm2 := NewConfigManager(tmpFile)
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	forwards := cm2.GetForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward after load, got %d", len(forwards))
	}
	if forwards[0].ID != "mgr-test" {
		t.Errorf("expected ID 'mgr-test', got '%s'", forwards[0].ID)
	}
}
