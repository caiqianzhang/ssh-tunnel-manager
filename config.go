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

// AppState represents the entire application configuration state
type AppState struct {
	Forwards []ForwardConfig `json:"forwards"`
	Settings AppSettings     `json:"settings"`
}

// AppSettings holds application-level settings
type AppSettings struct {
	APIKey string `json:"api_key,omitempty"`
	// DDNSCheckInterval is the DDNS heartbeat period in seconds.
	// 0 (unset) means "use DefaultDDNSIntervalSeconds".
	DDNSCheckInterval int `json:"ddns_check_interval,omitempty"`
}

// ConfigManager manages configuration persistence and operations
type ConfigManager struct {
	mu       sync.RWMutex
	filePath string
	state    AppState
}

// NewConfigManager creates a new ConfigManager with the specified file path
func NewConfigManager(filePath string) *ConfigManager {
	return &ConfigManager{
		filePath: filePath,
		state:    AppState{Forwards: []ForwardConfig{}},
	}
}

// Load loads the configuration from the JSON file. Decrypts any
// SSHPassword fields that are stored in ciphertext form.
func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	state, err := LoadConfig(cm.filePath)
	if err != nil {
		return err
	}

	// Decrypt passwords in place so callers see plaintext.
	for i := range state.Forwards {
		if state.Forwards[i].SSHPassword != "" {
			pt, err := decryptPassword(state.Forwards[i].SSHPassword)
			if err != nil {
				return fmt.Errorf("decrypt forward[%d] password: %w", i, err)
			}
			state.Forwards[i].SSHPassword = pt
		}
	}

	// Decrypt the API key in place as well.
	if state.Settings.APIKey != "" {
		pt, err := decryptPassword(state.Settings.APIKey)
		if err != nil {
			return fmt.Errorf("decrypt api key: %w", err)
		}
		state.Settings.APIKey = pt
	}

	cm.state = *state
	return nil
}

// Save saves the current configuration to the JSON file. Encrypts
// SSHPassword and APIKey fields before writing.
func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Build an encrypted copy so we don't mutate in-memory state.
	snapshot := AppState{
		Forwards: make([]ForwardConfig, len(cm.state.Forwards)),
		Settings: cm.state.Settings,
	}
	for i, f := range cm.state.Forwards {
		snapshot.Forwards[i] = f
		if f.SSHPassword != "" {
			ct, err := encryptPassword(f.SSHPassword)
			if err != nil {
				return fmt.Errorf("encrypt forward[%d] password: %w", i, err)
			}
			snapshot.Forwards[i].SSHPassword = ct
		}
	}
	// Encrypt the API key too — it is a live credential that should
	// not sit in plaintext next to the (already encrypted) passwords.
	if snapshot.Settings.APIKey != "" {
		ct, err := encryptPassword(snapshot.Settings.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		snapshot.Settings.APIKey = ct
	}

	return SaveConfig(cm.filePath, &snapshot)
}

// GetForwards returns a deep copy of all forwarding configurations.
// Returning a copy prevents callers from mutating internal state and
// bypassing the ConfigManager's locking discipline.
func (cm *ConfigManager) GetForwards() []ForwardConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	out := make([]ForwardConfig, len(cm.state.Forwards))
	copy(out, cm.state.Forwards)
	return out
}

// AddForward adds a new forwarding configuration
func (cm *ConfigManager) AddForward(f ForwardConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.state.Forwards = append(cm.state.Forwards, f)
}

// UpdateForward updates an existing forwarding configuration by ID
func (cm *ConfigManager) UpdateForward(id string, f ForwardConfig) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, fwd := range cm.state.Forwards {
		if fwd.ID == id {
			cm.state.Forwards[i] = f
			return true
		}
	}
	return false
}

// DeleteForward removes a forwarding configuration by ID
func (cm *ConfigManager) DeleteForward(id string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, fwd := range cm.state.Forwards {
		if fwd.ID == id {
			cm.state.Forwards = append(cm.state.Forwards[:i], cm.state.Forwards[i+1:]...)
			return true
		}
	}
	return false
}

// GetForward retrieves a specific forwarding configuration by ID
func (cm *ConfigManager) GetForward(id string) (ForwardConfig, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, fwd := range cm.state.Forwards {
		if fwd.ID == id {
			return fwd, true
		}
	}
	return ForwardConfig{}, false
}

// LoadConfig loads the application state from a JSON file
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

	return state, nil
}

// SaveConfig saves the application state to a JSON file.
//
// The state is written to a temporary file in the same directory, fsync'd,
// and renamed into place. A crash or a concurrent writer mid-encode can
// never leave a half-written config.json behind. The temp file is created
// with 0600 permissions because the config holds encrypted passwords.
func SaveConfig(filePath string, state *AppState) error {
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
	return AppState{
		Forwards: []ForwardConfig{},
		Settings: AppSettings{},
	}
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
