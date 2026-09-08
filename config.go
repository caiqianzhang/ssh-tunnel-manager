package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ForwardConfig represents a single SSH port forwarding configuration.
//
// SSHPassword is stored on disk as ciphertext (AES-256-GCM, base64) when
// written by Save/AddForward/UpdateForward. It is decrypted transparently
// on read. Empty passwords are passed through unchanged.
type ForwardConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ForwardType   string `json:"forward_type"` // "local" or "remote"
	RemoteHost    string `json:"remote_host"`
	RemotePort    int    `json:"remote_port"`
	LocalHost     string `json:"local_host"`
	LocalPort     int    `json:"local_port"`
	SSHUser       string `json:"ssh_user"`
	SSHPassword   string `json:"ssh_password,omitempty"`
	AutoReconnect bool   `json:"auto_reconnect"`
	MaxRetries    int    `json:"max_retries"`
	RetryInterval int    `json:"retry_interval"`
}

// DefaultDDNSIntervalSeconds is the heartbeat period between Baidu DNS
// IP checks while a tunnel is running. 15s keeps detection well under
// the record TTL (usually 60s) without hammering the API; it can be
// overridden per config (settings.ddns_check_interval, seconds).
const DefaultDDNSIntervalSeconds = 15

// AppState represents the entire application configuration state.
//
// The app manages exactly ONE forwarding rule. Forwards is the legacy
// multi-forward field: it is read for compatibility with older configs
// (the first entry migrates into Forward) and never written back.
type AppState struct {
	Forward  *ForwardConfig `json:"forward,omitempty"`
	Settings AppSettings    `json:"settings"`
	// Forwards is legacy-only; see normalizeLegacy. Always nil after
	// Load/Save.
	Forwards []ForwardConfig `json:"forwards,omitempty"`
}

// normalizeLegacy migrates the legacy multi-forward layout: the first
// entry becomes the app's single forward and the slice is dropped, so
// a config loaded and re-saved by any version of the app comes out in
// the current shape.
func (s *AppState) normalizeLegacy() {
	if s.Forward == nil && len(s.Forwards) > 0 {
		f := s.Forwards[0]
		s.Forward = &f
	}
	s.Forwards = nil
}

// AppSettings holds application-level settings
type AppSettings struct {
	APIKey string `json:"api_key,omitempty"`
	// DDNSCheckInterval is the DDNS heartbeat period in seconds.
	// 0 (unset) means "use DefaultDDNSIntervalSeconds".
	DDNSCheckInterval int `json:"ddns_check_interval,omitempty"`
	// DNSResolver is the DNS server used to resolve DDNS hostnames when
	// the Baidu DNS API is unavailable. Empty means "use the system
	// resolver"; Baidu's public DNS (119.29.29.29) avoids stale local
	// caches (see ssh.ResolveHost).
	DNSResolver string `json:"dns_resolver,omitempty"`
}

// ConfigManager manages configuration persistence and operations
type ConfigManager struct {
	mu       sync.RWMutex
	filePath string
	state    AppState
	// readOnly is set when the existing config could not be loaded
	// (corrupt file, missing secret key). Saving would then overwrite
	// the on-disk config with in-memory defaults — an unrecoverable
	// data-loss chain — so Save refuses while the flag is set.
	readOnly bool
}

// NewConfigManager creates a new ConfigManager with the specified file path
func NewConfigManager(filePath string) *ConfigManager {
	return &ConfigManager{
		filePath: filePath,
		state:    AppState{},
	}
}

// Load loads the configuration from the JSON file. Decrypts the stored
// SSH password if present. Legacy multi-forward configs are migrated
// to the single-forward shape on the way in (first entry wins).
func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	state, err := LoadConfig(cm.filePath)
	if err != nil {
		return err
	}

	// Decrypt the SSH password in place so callers see plaintext. An
	// undecryptable field (lost/corrupt secret.key) must not take the
	// whole config down — drop just that field and let the user
	// re-enter it; the rest of the config stays usable.
	if state.Forward != nil && state.Forward.SSHPassword != "" {
		pt, err := decryptPassword(state.Forward.SSHPassword)
		if err != nil {
			Logf("config: forward (%s) SSH password undecryptable: %v — clearing it, please re-enter",
				state.Forward.Name, err)
			state.Forward.SSHPassword = ""
		} else {
			state.Forward.SSHPassword = pt
		}
	}

	// Decrypt the API key in place as well.
	if state.Settings.APIKey != "" {
		pt, err := decryptPassword(state.Settings.APIKey)
		if err != nil {
			Logf("config: API key undecryptable: %v — clearing it", err)
			state.Settings.APIKey = ""
		} else {
			state.Settings.APIKey = pt
		}
	}

	cm.state = *state
	return nil
}

// Save saves the current configuration to the JSON file. Encrypts the
// SSHPassword and APIKey fields before writing. Refuses to write when
// the manager is in read-only protection (see SetReadOnly).
func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.readOnly {
		return fmt.Errorf("配置加载失败，已进入只读保护；修复或删除配置文件后重启应用再保存（详见日志）")
	}

	// Build an encrypted copy so we don't mutate in-memory state.
	snapshot := AppState{Settings: cm.state.Settings}
	if cm.state.Forward != nil {
		f := *cm.state.Forward
		if f.SSHPassword != "" {
			ct, err := encryptPassword(f.SSHPassword)
			if err != nil {
				return fmt.Errorf("encrypt forward password: %w", err)
			}
			f.SSHPassword = ct
		}
		snapshot.Forward = &f
	}
	// Encrypt the API key too — it is a live credential that should
	// not sit in plaintext next to the (already encrypted) password.
	if snapshot.Settings.APIKey != "" {
		ct, err := encryptPassword(snapshot.Settings.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		snapshot.Settings.APIKey = ct
	}

	return SaveConfig(cm.filePath, &snapshot)
}

// GetForward returns the configured forwarding rule. ok is false when
// no forward is configured (fresh install, config without a forward).
func (cm *ConfigManager) GetForward() (ForwardConfig, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.state.Forward == nil {
		return ForwardConfig{}, false
	}
	return *cm.state.Forward, true
}

// SetForward replaces the configured forwarding rule wholesale. The
// caller owns identity: reuse the existing ID when updating a
// configured forward, generate one for a fresh config.
func (cm *ConfigManager) SetForward(f ForwardConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.Forward = &f
}

// LoadConfig loads the application state from a JSON file, migrating
// the legacy multi-forward layout to the single-forward shape.
func LoadConfig(filePath string) (*AppState, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	state := &AppState{}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(state); err != nil {
		return nil, err
	}
	state.normalizeLegacy()

	return state, nil
}

// SaveConfig saves the application state to a JSON file.
//
// The state is written to a temporary file in the same directory, fsync'd,
// and renamed into place. A crash or a concurrent writer mid-encode can
// never leave a half-written config.json behind. The temp file is created
// with 0600 permissions because the config holds encrypted passwords.
func SaveConfig(filePath string, state *AppState) error {
	// Never persist the legacy shape, whatever the caller passed in.
	state.normalizeLegacy()

	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".ssh-tunnel-manager-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		tmp.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("rename temp config: %w", err)
	}
	return nil
}

// DefaultConfig returns a default empty configuration
func DefaultConfig() AppState {
	return AppState{}
}

// SetReadOnly toggles read-only protection (see Save). Used by main
// when the initial Load fails: the on-disk config is likely fine, the
// app just could not read it, so overwriting must be prevented.
func (cm *ConfigManager) SetReadOnly(ro bool) {
	cm.mu.Lock()
	cm.readOnly = ro
	cm.mu.Unlock()
}

// IsReadOnly reports whether read-only protection is active.
func (cm *ConfigManager) IsReadOnly() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.readOnly
}

// RestoreAppState replaces the in-memory state wholesale — the UI uses
// it to roll back speculative mutations when Save fails.
func (cm *ConfigManager) RestoreAppState(state AppState) {
	cm.mu.Lock()
	cm.state = state
	cm.mu.Unlock()
}

// GetAPIKey returns the configured API key
func (cm *ConfigManager) GetAPIKey() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.state.Settings.APIKey
}

// SetAPIKey sets the API key
func (cm *ConfigManager) SetAPIKey(key string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.state.Settings.APIKey = key
}

// GetDDNSCheckInterval returns the configured DDNS heartbeat period in
// seconds. 0 means unset — callers should fall back to
// DefaultDDNSIntervalSeconds.
func (cm *ConfigManager) GetDDNSCheckInterval() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.state.Settings.DDNSCheckInterval
}

// SetDDNSCheckInterval stores the DDNS heartbeat period in seconds.
// Values <= 0 mean "use the default".
func (cm *ConfigManager) SetDDNSCheckInterval(seconds int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.state.Settings.DDNSCheckInterval = seconds
}

// GetDNSResolver returns the DNS server used for DDNS hostname
// resolution when the Baidu DNS API is unavailable. Empty means "use
// the system resolver".
func (cm *ConfigManager) GetDNSResolver() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.state.Settings.DNSResolver
}

// SetDNSResolver stores the DNS server used for DDNS hostname
// resolution. Pass an empty string to fall back to the system resolver.
func (cm *ConfigManager) SetDNSResolver(server string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.state.Settings.DNSResolver = server
}
