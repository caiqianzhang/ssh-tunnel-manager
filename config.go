package main

import (
	"encoding/json"
	"fmt"
	"os"
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

// AppState represents the entire application configuration state
type AppState struct {
	Forwards []ForwardConfig `json:"forwards"`
	Settings AppSettings     `json:"settings"`
}

// AppSettings holds application-level settings
type AppSettings struct {
	APIKey string `json:"api_key,omitempty"`
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

	cm.state = *state
	return nil
}

// Save saves the current configuration to the JSON file. Encrypts
// SSHPassword fields before writing.
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

// SaveConfig saves the application state to a JSON file
func SaveConfig(filePath string, state *AppState) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(state)
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
