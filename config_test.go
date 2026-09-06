package main

import (
	"os"
	"path/filepath"
	"strings"
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

// Bug 16: GetForwards must return a deep copy so external mutation
// cannot bypass the ConfigManager's locking/validation.
func TestGetForwardsReturnsDeepCopy(t *testing.T) {
	cm := NewConfigManager("/tmp/test_deep_copy.json")
	cm.AddForward(ForwardConfig{
		ID: "x", Name: "original", RemoteHost: "r", RemotePort: 1,
		LocalHost: "l", LocalPort: 2, SSHUser: "u",
	})

	got := cm.GetForwards()
	if len(got) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(got))
	}
	// Mutate the returned slice & element.
	got[0].Name = "mutated"
	got = append(got, ForwardConfig{ID: "y"})

	// Re-read; state must be unchanged.
	again := cm.GetForwards()
	if again[0].Name != "original" {
		t.Errorf("GetForwards leaked mutation; got Name=%q", again[0].Name)
	}
	if len(again) != 1 {
		t.Errorf("GetForwards leaked appended element; got %d forwards", len(again))
	}
}

// Bug 16: GetForward must also return a copy (by value, since
// ForwardConfig contains no pointer fields, value semantics already
// achieve this — but the test guards the contract).
func TestGetForwardReturnsCopy(t *testing.T) {
	cm := NewConfigManager("/tmp/test_get_copy.json")
	cm.AddForward(ForwardConfig{
		ID: "g", Name: "n", RemoteHost: "r", RemotePort: 1,
		LocalHost: "l", LocalPort: 2, SSHUser: "u",
	})
	got, ok := cm.GetForward("g")
	if !ok {
		t.Fatal("expected found")
	}
	got.Name = "mutated"
	again, _ := cm.GetForward("g")
	if again.Name != "n" {
		t.Errorf("GetForward returned aliased value; got Name=%q", again.Name)
	}
}

// Bug 4: passwords must be encrypted on Save and decrypted on Load.
// Raw file bytes must NOT contain the plaintext password.
func TestConfigEncryptsPasswordOnDisk(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const secret = "my-SSH-password-123"
	cfgFile := filepath.Join(tmp, "test_enc.json")
	cm := NewConfigManager(cfgFile)
	cm.AddForward(ForwardConfig{
		ID: "p", Name: "pw", RemoteHost: "r", RemotePort: 22,
		LocalHost: "l", LocalPort: 2222, SSHUser: "u",
		SSHPassword: secret,
	})
	if err := cm.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("config file contains plaintext password:\n%s", string(raw))
	}

	// Reload and verify decryption.
	cm2 := NewConfigManager(cfgFile)
	if err := cm2.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := cm2.GetForward("p")
	if !ok {
		t.Fatal("expected to find forward")
	}
	if got.SSHPassword != secret {
		t.Errorf("decryption failed: got %q want %q", got.SSHPassword, secret)
	}
}

// Bug 4b: pre-existing plaintext config files must still load.
func TestConfigLoadsLegacyPlaintextPassword(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const secret = "legacy-plain-pw"
	cfgFile := filepath.Join(tmp, "test_legacy.json")
	// Write raw legacy-style JSON.
	legacy := `{"forwards":[{"id":"l","name":"l","forward_type":"local","remote_host":"r","remote_port":22,"local_host":"l","local_port":2222,"ssh_user":"u","ssh_password":"` + secret + `","auto_reconnect":false,"max_retries":3,"retry_interval":5}],"settings":{}}`
	if err := os.WriteFile(cfgFile, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	cm := NewConfigManager(cfgFile)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := cm.GetForward("l")
	if !ok {
		t.Fatal("expected to find forward")
	}
	if got.SSHPassword != secret {
		t.Errorf("legacy plaintext not preserved: got %q want %q", got.SSHPassword, secret)
	}
}

// TestConfigEncryptsAPIKeyOnDisk verifies the API key is encrypted on
// disk (like SSH passwords) and round-trips through Save/Load. The
// API key is a live credential and was previously stored in plaintext
// alongside the config.
func TestConfigEncryptsAPIKeyOnDisk(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const apiKey = "sk-ant-live-key-123"
	cfgFile := filepath.Join(tmp, "test_apikey.json")
	cm := NewConfigManager(cfgFile)
	cm.SetAPIKey(apiKey)
	if err := cm.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), apiKey) {
		t.Errorf("config file contains plaintext API key:\n%s", string(raw))
	}

	// Round-trip: Load must decrypt back to the original value.
	cm2 := NewConfigManager(cfgFile)
	if err := cm2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := cm2.GetAPIKey(); got != apiKey {
		t.Errorf("API key round-trip failed: got %q want %q", got, apiKey)
	}
}

// TestConfigLoadsLegacyPlaintextAPIKey verifies a pre-encryption API
// key (raw plaintext in the JSON) still loads without error.
func TestConfigLoadsLegacyPlaintextAPIKey(t *testing.T) {
	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(oldWd)

	const apiKey = "legacy-plain-key"
	cfgFile := filepath.Join(tmp, "test_legacy_key.json")
	legacy := `{"forwards":[],"settings":{"api_key":"` + apiKey + `"}}`
	if err := os.WriteFile(cfgFile, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	cm := NewConfigManager(cfgFile)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cm.GetAPIKey(); got != apiKey {
		t.Errorf("legacy plaintext API key not preserved: got %q want %q", got, apiKey)
	}
}
