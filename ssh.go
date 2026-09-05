package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SSHConn represents an active SSH connection
type SSHConn struct {
	Config    ForwardConfig
	Process   *exec.Cmd
	Status    string
	CurrentIP string
	mu        sync.Mutex
	StopCh    chan struct{}
}

// SSHManager manages multiple SSH connections
type SSHManager struct {
	conns map[string]*SSHConn
	mu    sync.RWMutex
}

// NewSSHManager creates a new SSHManager
func NewSSHManager() *SSHManager {
	return &SSHManager{
		conns: make(map[string]*SSHConn),
	}
}

// FlushDNS clears the local DNS cache
// On Windows: ipconfig /flushdns
// On Linux: sudo systemd-resolve --flush-caches && sudo service nscd restart
func FlushDNS() error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("ipconfig", "/flushdns")
	case "linux", "darwin":
		// Try systemd-resolve first (Linux)
		cmd = exec.Command("sudo", "systemd-resolve", "--flush-caches")
		if err := cmd.Run(); err != nil {
			// Try nscd if systemd-resolve not available
			cmd = exec.Command("sudo", "service", "nscd", "restart")
			if err := cmd.Run(); err != nil {
				// Both failed, return error
				return fmt.Errorf("failed to flush DNS: %v", err)
			}
			return nil
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	return cmd.Run()
}

// ResolveHost resolves a hostname to a list of IP addresses
func ResolveHost(host string) ([]string, error) {
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve host %s: %v", host, err)
	}
	return ips, nil
}

// Connect starts an SSH tunnel with the given configuration
func (m *SSHManager) Connect(cfg ForwardConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already connected
	if _, exists := m.conns[cfg.ID]; exists {
		return fmt.Errorf("connection %s already exists", cfg.ID)
	}

	// Resolve host to get IP
	ips, err := ResolveHost(cfg.RemoteHost)
	if err != nil {
		return fmt.Errorf("failed to resolve remote host: %v", err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("no IPs found for host %s", cfg.RemoteHost)
	}

	// Create SSH command
	args := []string{
		"-R", fmt.Sprintf("%d:%s:%d", cfg.RemotePort, cfg.LocalHost, cfg.LocalPort),
		"-N",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=no",
	}

	// Use password authentication if provided
	if cfg.SSHPassword != "" {
		args = append(args, "-o", "PreferredAuthentications=password")
		args = append(args, "-o", "PubkeyAuthentication=no")
	}

	args = append(args, "-l", cfg.SSHUser, cfg.RemoteHost)

	var cmd *exec.Cmd
	if cfg.SSHPassword != "" {
		// Use sshpass for password authentication
		args = append([]string{"-p", cfg.SSHPassword, "ssh"}, args...)
		cmd = exec.Command("sshpass", args...)
	} else {
		cmd = exec.Command("ssh", args...)
	}

	// Create SSHConn
	sshConn := &SSHConn{
		Config:    cfg,
		Process:   cmd,
		Status:    "connecting",
		CurrentIP: ips[0],
		StopCh:    make(chan struct{}),
	}

	// Start the SSH process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH process: %v", err)
	}

	// Store connection
	m.conns[cfg.ID] = sshConn
	sshConn.Status = "running"

	log.Printf("SSH tunnel started: %s -> %s:%d via %s (IP: %s)",
		cfg.Name, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ips[0])

	// Start monitoring goroutine
	go m.monitorProcess(cfg.ID)

	return nil
}

// monitorProcess monitors an SSH process and handles auto-reconnect
func (m *SSHManager) monitorProcess(id string) {
	// Take a snapshot under lock
	m.mu.RLock()
	sshConn, exists := m.conns[id]
	m.mu.RUnlock()

	if !exists {
		return
	}

	// Wait for the process to exit (without holding any lock)
	err := sshConn.Process.Wait()

	// Check if we should stop monitoring
	select {
	case <-sshConn.StopCh:
		// Stop was signaled, don't reconnect
		return
	default:
	}

	m.mu.Lock()
	if _, stillExists := m.conns[id]; !stillExists {
		m.mu.Unlock()
		return
	}

	// Process exited
	if err != nil {
		log.Printf("SSH process for %s exited with error: %v", id, err)
	} else {
		log.Printf("SSH process for %s exited normally", id)
	}

	sshConn.Status = "disconnected"
	m.mu.Unlock()

	// Check if auto-reconnect is enabled
	if sshConn.Config.AutoReconnect {
		log.Printf("Auto-reconnect enabled for %s, attempting reconnect...", id)
		go m.autoReconnect(id)
	}
}

// autoReconnect attempts to reconnect an SSH connection
func (m *SSHManager) autoReconnect(id string) {
	m.mu.RLock()
	sshConn, exists := m.conns[id]
	m.mu.RUnlock()

	if !exists {
		return
	}

	cfg := sshConn.Config
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	retryInterval := cfg.RetryInterval
	if retryInterval <= 0 {
		retryInterval = 5
	}

	for i := 0; i < maxRetries; i++ {
		log.Printf("Auto-reconnect attempt %d/%d for %s", i+1, maxRetries, id)

		// Flush DNS
		if err := FlushDNS(); err != nil {
			log.Printf("DNS flush failed: %v", err)
		}

		// Wait before retry
		time.Sleep(time.Duration(retryInterval) * time.Second)

		// Try to resolve and reconnect
		ips, err := ResolveHost(cfg.RemoteHost)
		if err != nil {
			log.Printf("Failed to resolve host %s: %v", cfg.RemoteHost, err)
			continue
		}

		if len(ips) == 0 {
			log.Printf("No IPs found for host %s", cfg.RemoteHost)
			continue
		}

		// Create SSH command
		args := []string{
			"-R", fmt.Sprintf("%d:%s:%d", cfg.RemotePort, cfg.LocalHost, cfg.LocalPort),
			"-N",
			"-o", "ServerAliveInterval=60",
			"-o", "ServerAliveCountMax=3",
			"-o", "ExitOnForwardFailure=yes",
			"-l", cfg.SSHUser,
			cfg.RemoteHost,
		}

		cmd := exec.Command("ssh", args...)

		// Update connection
		m.mu.Lock()
		sshConn.Process = cmd
		sshConn.Status = "connecting"
		sshConn.CurrentIP = ips[0]
		m.mu.Unlock()

		// Start SSH process
		if err := cmd.Start(); err != nil {
			log.Printf("Failed to start SSH process on attempt %d: %v", i+1, err)
			continue
		}

		m.mu.Lock()
		sshConn.Status = "running"
		m.mu.Unlock()

		log.Printf("SSH tunnel reconnected: %s -> %s:%d via %s (IP: %s)",
			cfg.Name, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ips[0])

		// Start monitoring again
		go m.monitorProcess(id)
		return
	}

	log.Printf("Auto-reconnect failed for %s after %d attempts", id, maxRetries)
}

// Disconnect stops an SSH connection
func (m *SSHManager) Disconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sshConn, exists := m.conns[id]
	if !exists {
		return fmt.Errorf("connection %s not found", id)
	}

	// Signal stop
	close(sshConn.StopCh)

	// Kill the process
	if sshConn.Process != nil && sshConn.Process.Process != nil {
		if err := sshConn.Process.Process.Kill(); err != nil {
			// Process might have already exited
			log.Printf("Failed to kill process for %s (may have already exited): %v", id, err)
		}
	}

	sshConn.Status = "stopped"
	delete(m.conns, id)

	log.Printf("SSH tunnel stopped: %s", sshConn.Config.Name)
	return nil
}

// IsRunning checks if a connection is running
func (m *SSHManager) IsRunning(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sshConn, exists := m.conns[id]
	if !exists {
		return false
	}

	return sshConn.Status == "running"
}

// GetStatus returns the status of a connection
func (m *SSHManager) GetStatus(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sshConn, exists := m.conns[id]
	if !exists {
		return "not_found"
	}

	return sshConn.Status
}

// DisconnectAll stops all SSH connections
func (m *SSHManager) DisconnectAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		if err := m.Disconnect(id); err != nil {
			log.Printf("Failed to disconnect %s: %v", id, err)
		}
	}
}

// GetConnection returns the SSHConn for a given ID
func (m *SSHManager) GetConnection(id string) (*SSHConn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sshConn, exists := m.conns[id]
	return sshConn, exists
}

// GetActiveConnections returns a list of all active connection IDs
func (m *SSHManager) GetActiveConnections() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	return ids
}

// FormatSSHCommand returns the SSH command that would be executed for a given config
// Useful for debugging and testing
func FormatSSHCommand(cfg ForwardConfig) string {
	args := []string{
		"-R", fmt.Sprintf("%d:%s:%d", cfg.RemotePort, cfg.LocalHost, cfg.LocalPort),
		"-N",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-l", cfg.SSHUser,
		cfg.RemoteHost,
	}
	return "ssh " + strings.Join(args, " ")
}
