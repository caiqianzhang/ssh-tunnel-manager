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
		Forward: &ForwardConfig{
			ID: "test-1", Name: "Test Server", RemoteHost: "example.com",
			RemotePort: 8080, LocalHost: "localhost", LocalPort: 80,
			SSHUser: "admin", AutoReconnect: true, MaxRetries: 5, RetryInterval: 5,
		},
	}

	if err := SaveConfig(tmpFile, &original); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loaded, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if loaded.Forward == nil {
		t.Fatalf("expected 1 forward, got none")
	}
	if loaded.Forward.Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got '%s'", loaded.Forward.Name)
	}
	// The current shape must never carry the legacy list.
	if loaded.Forwards != nil {
		t.Errorf("expected legacy Forwards to be dropped, got %d entries", len(loaded.Forwards))
	}
}

// TestLoadMigratesLegacyForwardsList pins the single-forward migration:
// configs written by pre-single-forward builds store a forwards array.
// Loading must keep the first entry as the app's forward and drop the
// rest, and re-saving must persist the current shape.
func TestLoadMigratesLegacyForwardsList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	legacy := `{"forwards":[` +
		`{"id":"first","name":"First","forward_type":"local","remote_host":"a","remote_port":22,"local_host":"l","local_port":2222,"ssh_user":"u","auto_reconnect":true,"max_retries":3,"retry_interval":5},` +
		`{"id":"second","name":"Second","forward_type":"local","remote_host":"b","remote_port":22,"local_host":"l","local_port":3333,"ssh_user":"u","auto_reconnect":true,"max_retries":3,"retry_interval":5}` +
		`],"settings":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	fwd, ok := cm.GetForward()
	if !ok {
		t.Fatal("expected the first legacy forward to survive migration")
	}
	if fwd.ID != "first" {
		t.Errorf("expected the FIRST legacy entry to win, got %q", fwd.ID)
	}

	// Round-trip: the migrated config must be written in the new shape.
	cm2path := filepath.Join(t.TempDir(), "migrated.json")
	cm2 := NewConfigManager(cm2path)
	cm2.RestoreAppState(AppState{Forward: &fwd, Settings: AppSettings{}})
	if err := cm2.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cm2path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"forwards"`) {
		t.Errorf("migrated config still contains the legacy forwards array:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"forward"`) {
		t.Errorf("migrated config missing the forward object:\n%s", raw)
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
	if cfg.Forward != nil {
		t.Errorf("expected no forward in default config, got %+v", cfg.Forward)
	}
}

func TestSetForward(t *testing.T) {
	cm := NewConfigManager("/tmp/test.json")

	fwd := ForwardConfig{
		ID: "test-set", Name: "Set Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: false, MaxRetries: 3, RetryInterval: 10,
	}

	// Unconfigured manager reports the absence explicitly.
	if _, ok := cm.GetForward(); ok {
		t.Fatal("expected no forward before SetForward")
	}

	cm.SetForward(fwd)

	got, ok := cm.GetForward()
	if !ok {
		t.Fatal("expected forward after SetForward")
	}
	if got.ID != "test-set" {
		t.Errorf("expected ID 'test-set', got '%s'", got.ID)
	}

	// SetForward replaces wholesale — there is exactly one rule.
	replacement := fwd
	replacement.ID = "replacement"
	replacement.LocalPort = 3333
	cm.SetForward(replacement)

	got, _ = cm.GetForward()
	if got.ID != "replacement" || got.LocalPort != 3333 {
		t.Errorf("expected SetForward to replace the rule, got %+v", got)
	}
}

func TestConfigManagerLoadSave(t *testing.T) {
	tmpFile := t.TempDir() + "/test_config_manager.json"
	defer os.Remove(tmpFile)

	cm1 := NewConfigManager(tmpFile)
	cm1.SetForward(ForwardConfig{
		ID: "mgr-test", Name: "Manager Test", RemoteHost: "test.com",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "user", AutoReconnect: true, MaxRetries: 3, RetryInterval: 10,
	})

	if err := cm1.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cm2 := NewConfigManager(tmpFile)
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	fwd, ok := cm2.GetForward()
	if !ok {
		t.Fatalf("expected forward after load")
	}
	if fwd.ID != "mgr-test" {
		t.Errorf("expected ID 'mgr-test', got '%s'", fwd.ID)
	}
}

// Bug 16: GetForward must return a copy so external mutation cannot
// bypass the ConfigManager's locking discipline.
func TestGetForwardReturnsCopy(t *testing.T) {
	cm := NewConfigManager("/tmp/test_get_copy.json")
	cm.SetForward(ForwardConfig{
		ID: "x", Name: "original", RemoteHost: "r", RemotePort: 1,
		LocalHost: "l", LocalPort: 2, SSHUser: "u",
	})

	got, ok := cm.GetForward()
	if !ok {
		t.Fatal("expected forward")
	}
	// Mutate the returned value.
	got.Name = "mutated"

	// Re-read; state must be unchanged.
	again, _ := cm.GetForward()
	if again.Name != "original" {
		t.Errorf("GetForward leaked mutation; got Name=%q", again.Name)
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
	cm.SetForward(ForwardConfig{
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
	fwd, ok := cm2.GetForward()
	if !ok {
		t.Fatal("expected to find forward")
	}
	if fwd.ID != "p" {
		t.Fatalf("expected forward ID 'p', got %q", fwd.ID)
	}
	if fwd.SSHPassword != secret {
		t.Errorf("decryption failed: got %q want %q", fwd.SSHPassword, secret)
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
	fwd, ok := cm.GetForward()
	if !ok {
		t.Fatal("expected to find forward")
	}
	if fwd.SSHPassword != secret {
		t.Errorf("legacy plaintext not preserved: got %q want %q", fwd.SSHPassword, secret)
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

// TestDDNSCheckIntervalRoundTrip verifies the settings.ddns_check_interval
// field survives Save → Load through the ConfigManager.
func TestDDNSCheckIntervalRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cm := NewConfigManager(path)
	cm.SetDDNSCheckInterval(7)
	if err := cm.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cm2 := NewConfigManager(path)
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cm2.GetDDNSCheckInterval(); got != 7 {
		t.Errorf("expected ddns_check_interval 7 after round trip, got %d", got)
	}

	// Unset (0) must stay unset — it means "use the built-in default".
	path2 := filepath.Join(t.TempDir(), "empty.json")
	cm3 := NewConfigManager(path2)
	if err := cm3.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	cm4 := NewConfigManager(path2)
	if err := cm4.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cm4.GetDDNSCheckInterval(); got != 0 {
		t.Errorf("expected unset ddns_check_interval to stay 0, got %d", got)
	}
}

// TestDNSResolverRoundTrip verifies the settings.dns_resolver field
// survives Save → Load through the ConfigManager.
func TestDNSResolverRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cm := NewConfigManager(path)
	cm.SetDNSResolver("1.1.1.1")
	if err := cm.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	cm2 := NewConfigManager(path)
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cm2.GetDNSResolver(); got != "1.1.1.1" {
		t.Errorf("expected dns_resolver 1.1.1.1 after round trip, got %q", got)
	}

	// Empty must stay empty — it means "use the system resolver".
	path2 := filepath.Join(t.TempDir(), "empty.json")
	cm3 := NewConfigManager(path2)
	if err := cm3.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	cm4 := NewConfigManager(path2)
	if err := cm4.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cm4.GetDNSResolver(); got != "" {
		t.Errorf("expected unset dns_resolver to stay empty, got %q", got)
	}
}

// TestSaveRefusesWhenReadOnly pins the data-loss protection: when the
// initial Load failed (corrupt file, missing secret key), Save must
// refuse to overwrite the on-disk config with in-memory defaults, and
// must work again once the protection is lifted.
func TestSaveRefusesWhenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"forwards":[{"id":"keep-me","name":"real"}]}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cm := NewConfigManager(path)
	cm.SetReadOnly(true)
	if err := cm.Save(); err == nil {
		t.Fatal("expected Save to refuse while read-only")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("read-only Save modified the file:\n%s", data)
	}

	cm.SetReadOnly(false)
	if err := cm.Save(); err != nil {
		t.Fatalf("Save after lifting protection failed: %v", err)
	}
}
