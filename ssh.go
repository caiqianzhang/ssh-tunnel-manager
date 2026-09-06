package main

import (
	"bytes"
	"fmt"
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
	Stderr    bytes.Buffer
	mu        sync.Mutex
	StopCh    chan struct{}
}

// SSHManager manages multiple SSH connections
type SSHManager struct {
	conns map[string]*SSHConn
	mu    sync.RWMutex

	// onStatusChange is invoked whenever a forward's status
	// transitions. Guarded by mu; see SetOnStatusChange.
	onStatusChange func(forwardID, status string)
}

// NewSSHManager creates a new SSHManager
func NewSSHManager() *SSHManager {
	return &SSHManager{
		conns: make(map[string]*SSHConn),
	}
}

// SetOnStatusChange registers a callback fired on every status
// transition (e.g. "connecting" -> "running" -> "disconnected"). The
// callback runs on its own goroutine — implementations must be
// goroutine-safe and must not call back into the manager.
func (m *SSHManager) SetOnStatusChange(fn func(forwardID, status string)) {
	m.mu.Lock()
	m.onStatusChange = fn
	m.mu.Unlock()
}

// notifyStatus dispatches a status transition to the registered
// callback. Safe to call while holding m.mu: the callback is invoked
// from a separate goroutine once the lock has been released.
func (m *SSHManager) notifyStatus(forwardID, status string) {
	go func() {
		m.mu.RLock()
		fn := m.onStatusChange
		m.mu.RUnlock()
		if fn != nil {
			fn(forwardID, status)
		}
	}()
}

// FlushDNS clears the local DNS cache
// On Windows: ipconfig /flushdns
// On Linux: sudo systemd-resolve --flush-caches || sudo service nscd restart
// On macOS: sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder
func FlushDNS() error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("ipconfig", "/flushdns").Run()
	case "darwin":
		// macOS: flush both caches
		if err := exec.Command("sudo", "dscacheutil", "-flushcache").Run(); err != nil {
			return fmt.Errorf("failed to flush DNS (dscacheutil): %v", err)
		}
		if err := exec.Command("sudo", "killall", "-HUP", "mDNSResponder").Run(); err != nil {
			Logf("mDNSResponder restart failed (non-fatal): %v", err)
		}
		return nil
	case "linux":
		// Try systemd-resolve first
		if err := exec.Command("sudo", "systemd-resolve", "--flush-caches").Run(); err == nil {
			return nil
		}
		// Fallback to nscd
		if err := exec.Command("sudo", "service", "nscd", "restart").Run(); err != nil {
			return fmt.Errorf("failed to flush DNS: no supported method worked")
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// ResolveHost resolves a hostname to a list of IP addresses
func ResolveHost(host string) ([]string, error) {
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve host %s: %v", host, err)
	}
	return ips, nil
}

// CheckPortInUse checks if a local port is already in use.
// Returns (inUse, processInfo, err).
// Uses a short timeout so the check is fast when the port is free.
func CheckPortInUse(port int) (bool, string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	// 150ms is long enough to avoid false negatives on loaded systems
	// but short enough to keep the UI responsive.
	conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		// Port is available (connection refused / timeout).
		return false, "", nil
	}
	conn.Close()

	// Port is in use, try to find the process.
	out, err := exec.Command("ss", "-tlnp", fmt.Sprintf("sport = :%d", port)).Output()
	if err != nil {
		return true, "unknown process", nil
	}

	// Parse ss output to find process info.
	output := string(out)
	if strings.Contains(output, "users:") {
		// Extract process info from the first line containing users:.
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			if strings.Contains(line, "users:") {
				start := strings.Index(line, "users:")
				if start >= 0 {
					processInfo := strings.TrimSpace(line[start+6:])
					return true, formatProcessInfo(processInfo), nil
				}
			}
		}
	}

	return true, "未知进程", nil
}

// formatProcessInfo converts raw `ss -tlnp` process output such as
// (("ssh",pid=410301,fd=5)) into a user-friendly "ssh (PID 410301)".
func formatProcessInfo(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "()")
	name := ""
	if i := strings.Index(raw, `",`); i >= 0 {
		name = strings.Trim(raw[:i], `"`)
	} else if i := strings.Index(raw, ","); i >= 0 {
		name = raw[:i]
	} else {
		return raw
	}
	if pid := extractPIDFromSS(raw); pid != "" {
		return fmt.Sprintf("%s (PID %s)", name, pid)
	}
	return raw
}

// KillProcessByPort kills the process using a specific port
func KillProcessByPort(port int) error {
	Logf("KillProcessByPort: attempting to kill process on port %d", port)

	// Find PID using the port
	out, err := exec.Command("ss", "-tlnp", fmt.Sprintf("sport = :%d", port)).Output()
	if err != nil {
		Logf("KillProcessByPort: ss command failed: %v", err)
		// Try fallback with lsof
		return killProcessByPortFallback(port)
	}

	output := string(out)
	Logf("KillProcessByPort: ss output: %s", output)

	// Parse output to extract PID
	pid := extractPIDFromSS(output)
	if pid == "" {
		Logf("KillProcessByPort: could not extract PID from ss output, trying fallback")
		return killProcessByPortFallback(port)
	}

	Logf("KillProcessByPort: found PID %s, attempting to kill", pid)

	// Try kill without sudo first
	if err := exec.Command("kill", pid).Run(); err != nil {
		Logf("KillProcessByPort: kill failed (no sudo): %v, trying with sudo", err)
		// Try with sudo
		if err := exec.Command("sudo", "kill", pid).Run(); err != nil {
			Logf("KillProcessByPort: sudo kill also failed: %v", err)
			// Try SIGKILL as last resort
			if err := exec.Command("sudo", "kill", "-9", pid).Run(); err != nil {
				return fmt.Errorf("failed to kill process %s (tried kill, sudo kill, sudo kill -9): %v", pid, err)
			}
		}
	}

	Logf("KillProcessByPort: successfully killed process %s", pid)
	return nil
}

// killProcessByPortFallback tries alternative methods to kill a process by port
func killProcessByPortFallback(port int) error {
	Logf("killProcessByPortFallback: trying fuser for port %d", port)

	// Try fuser
	if err := exec.Command("fuser", "-k", fmt.Sprintf("%d/tcp", port)).Run(); err == nil {
		Logf("killProcessByPortFallback: fuser succeeded")
		return nil
	} else {
		Logf("killProcessByPortFallback: fuser failed: %v", err)
	}

	// Try lsof
	out, err := exec.Command("lsof", "-t", "-i", fmt.Sprintf(":%d", port)).Output()
	if err == nil {
		pid := strings.TrimSpace(string(out))
		if pid != "" {
			Logf("killProcessByPortFallback: lsof found PID %s, killing", pid)
			if err := exec.Command("kill", pid).Run(); err == nil {
				return nil
			}
			if err := exec.Command("sudo", "kill", "-9", pid).Run(); err == nil {
				return nil
			}
		}
	} else {
		Logf("killProcessByPortFallback: lsof failed: %v", err)
	}

	return fmt.Errorf("could not kill process on port %d (tried ss, fuser, lsof)", port)
}

// extractPIDFromSS extracts the first PID from ss output.
// Handles multiple ss output formats across Linux distributions:
//   - "pid=12345,fd=5"      (standard ss -tlnp)
//   - "pid=12345,fd=4)"     (no space before closing paren)
//   - "pid=12345)"          (truncated)
func extractPIDFromSS(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		start := strings.Index(line, "pid=")
		if start < 0 {
			continue
		}
		rest := line[start+4:]
		// PID is decimal digits, optionally followed by comma/fd/parens/space.
		end := len(rest)
		for i, r := range rest {
			if r < '0' || r > '9' {
				end = i
				break
			}
		}
		pid := strings.TrimSpace(rest[:end])
		if pid != "" {
			return pid
		}
	}
	return ""
}

// buildSSHCommand creates an exec.Cmd for an SSH tunnel with the given config.
// Handles both password (via sshpass) and key-based authentication.
//
// Forward semantics:
//   - local  (-L): SSH server-side destination. Default 127.0.0.1 means
//     "loopback on the SSH server". Users rarely need to change this.
//   - remote (-R): SSH client-side destination. Must be the address of
//     a service running on the SSH client machine — typically the user's
//     laptop. We use cfg.LocalHost so the configured value reaches the
//     SSH server (default: localhost/127.0.0.1 on the client).
func buildSSHCommand(cfg ForwardConfig) *exec.Cmd {
	forwardFlag := "-L"
	if cfg.ForwardType == "remote" {
		forwardFlag = "-R"
	}

	forwardTarget := "127.0.0.1"
	if cfg.ForwardType == "remote" {
		// For remote forwards, the destination is interpreted by the SSH
		// server as a client-side address. Use the user-configured value
		// (defaulting to 127.0.0.1 if empty).
		if cfg.LocalHost != "" {
			forwardTarget = cfg.LocalHost
		}
	}

	args := []string{
		forwardFlag, fmt.Sprintf("%d:%s:%d", cfg.LocalPort, forwardTarget, cfg.RemotePort),
		"-N",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=no",
	}

	if cfg.SSHPassword != "" {
		args = append(args, "-o", "PreferredAuthentications=password")
		args = append(args, "-o", "PubkeyAuthentication=no")
	}

	args = append(args, "-l", cfg.SSHUser, cfg.RemoteHost)

	if cfg.SSHPassword != "" {
		args = append([]string{"-p", cfg.SSHPassword, "ssh"}, args...)
		return exec.Command("sshpass", args...)
	}
	return exec.Command("ssh", args...)
}

// Connect starts an SSH tunnel with the given configuration.
//
// DNS resolution happens outside the manager lock so a slow resolver
// cannot block other connections or status queries. The connection is
// reported as "connecting" immediately; monitorProcess transitions it
// to "running" once the SSH handshake has had time to complete.
func (m *SSHManager) Connect(cfg ForwardConfig) error {
	// Check if already connected (short critical section).
	m.mu.Lock()
	if _, exists := m.conns[cfg.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("connection %s already exists", cfg.ID)
	}
	m.mu.Unlock()

	// Resolve host outside the lock.
	ips, err := ResolveHost(cfg.RemoteHost)
	if err != nil {
		return fmt.Errorf("failed to resolve remote host: %v", err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("no IPs found for host %s", cfg.RemoteHost)
	}

	cmd := buildSSHCommand(cfg)

	// Create SSHConn
	sshConn := &SSHConn{
		Config:    cfg,
		Process:   cmd,
		Status:    "connecting",
		CurrentIP: ips[0],
		StopCh:    make(chan struct{}),
	}

	// Capture stderr for diagnostics
	cmd.Stderr = &sshConn.Stderr

	// Start the SSH process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH process: %v", err)
	}

	// Store connection
	m.mu.Lock()
	m.conns[cfg.ID] = sshConn
	m.mu.Unlock()

	// Report "connecting" — monitorProcess will flip this to "running"
	// once the tunnel has had time to establish.
	m.notifyStatus(cfg.ID, "connecting")

	Logf("SSH tunnel started: %s (%s) -> %s:%d via %s (IP: %s)",
		cfg.Name, cfg.ForwardType, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ips[0])

	// Start monitoring goroutine
	go m.monitorProcess(cfg.ID)

	return nil
}

// monitorProcess monitors an SSH process and handles auto-reconnect.
//
// The process goes through a confirmation window after Start(): if it
// survives long enough the SSH handshake has completed and the tunnel
// is active, so we transition "connecting" -> "running". If it exits
// during the window the failure is reported immediately.
func (m *SSHManager) monitorProcess(id string) {
	// Take a snapshot under lock
	m.mu.RLock()
	sshConn, exists := m.conns[id]
	m.mu.RUnlock()

	if !exists {
		return
	}

	// Wait for the process to exit. We use a goroutine + channel so we
	// can race the exit against a confirmation timeout.
	exited := make(chan error, 1)
	go func() {
		exited <- sshConn.Process.Wait()
	}()

	// Confirmation window: give the SSH handshake time to complete.
	// If the process is still alive after this, the tunnel is up.
	const confirmWindow = 1500 * time.Millisecond
	select {
	case <-sshConn.StopCh:
		// Disconnect was called during the window.
		<-exited // consume the Wait goroutine to avoid a leak
		return
	case err := <-exited:
		// Process exited during the confirmation window.
		m.handleProcessExit(id, err, sshConn)
		return
	case <-time.After(confirmWindow):
		// Process survived the window — mark as running.
		m.mu.Lock()
		if _, stillExists := m.conns[id]; stillExists {
			sshConn.Status = "running"
			m.notifyStatus(id, "running")
		}
		m.mu.Unlock()
	}

	// Process is confirmed running; now wait for it to actually exit.
	err := <-exited

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
	m.handleProcessExit(id, err, sshConn)
}

// handleProcessExit classifies the exit error, updates status, and
// triggers auto-reconnect when appropriate. Caller must hold m.mu.
func (m *SSHManager) handleProcessExit(id string, err error, sshConn *SSHConn) {
	if err != nil {
		Logf("SSH process for %s exited with error: %v", id, err)
	} else {
		Logf("SSH process for %s exited normally", id)
	}

	// Log stderr output for diagnostics
	stderr := sshConn.Stderr.String()
	if stderr != "" {
		Logf("SSH stderr for %s: %s", id, stderr)
	}

	// Check for specific error types
	status := "disconnected"
	shouldReconnect := true

	if strings.Contains(stderr, "Address already in use") {
		status = "port_in_use"
		shouldReconnect = false
		Logf("Port conflict detected for %s - local port %d is already in use", id, sshConn.Config.LocalPort)
	} else if strings.Contains(stderr, "Connection refused") {
		status = "connection_refused"
		Logf("Connection refused for %s - remote SSH server may not be running", id)
	} else if strings.Contains(stderr, "Permission denied") {
		status = "auth_failed"
		shouldReconnect = false
		Logf("Authentication failed for %s - check username/password", id)
	} else if strings.Contains(stderr, "No route to host") {
		status = "unreachable"
		Logf("Host unreachable for %s - check network connectivity", id)
	}

	sshConn.Status = status
	m.notifyStatus(id, status)
	m.mu.Unlock()

	// Check if auto-reconnect is enabled
	if shouldReconnect && sshConn.Config.AutoReconnect {
		Logf("Auto-reconnect enabled for %s, attempting reconnect...", id)
		go m.autoReconnect(id)
	} else if !shouldReconnect {
		Logf("Auto-reconnect skipped for %s due to error type: %s", id, status)
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
		Logf("Auto-reconnect attempt %d/%d for %s", i+1, maxRetries, id)

		// Flush DNS
		if err := FlushDNS(); err != nil {
			Logf("DNS flush failed: %v", err)
		}

		// Wait before retry
		time.Sleep(time.Duration(retryInterval) * time.Second)

		// Try to resolve and reconnect
		ips, err := ResolveHost(cfg.RemoteHost)
		if err != nil {
			Logf("Failed to resolve host %s: %v", cfg.RemoteHost, err)
			continue
		}

		if len(ips) == 0 {
			Logf("No IPs found for host %s", cfg.RemoteHost)
			continue
		}

		cmd := buildSSHCommand(cfg)

		// Clear stderr buffer and set up capture
		sshConn.Stderr.Reset()
		cmd.Stderr = &sshConn.Stderr

		// Check StopCh before doing anything that starts a process: if
		// the user disconnected while we were sleeping/resolving, do not
		// spawn an orphan tunnel.
		select {
		case <-sshConn.StopCh:
			Logf("autoReconnect: StopCh signaled before start, aborting for %s", id)
			return
		default:
		}

		// Start SSH process
		if err := cmd.Start(); err != nil {
			Logf("Failed to start SSH process on attempt %d: %v", i+1, err)
			continue
		}

		// Start succeeded — publish the new process and status.
		m.mu.Lock()
		sshConn.Process = cmd
		sshConn.Status = "connecting"
		m.notifyStatus(id, "connecting")
		sshConn.CurrentIP = ips[0]
		m.mu.Unlock()

		Logf("SSH tunnel reconnected: %s -> %s:%d via %s (IP: %s)",
			cfg.Name, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ips[0])

		// Start monitoring again
		go m.monitorProcess(id)
		return
	}

	Logf("Auto-reconnect failed for %s after %d attempts", id, maxRetries)
}

// Disconnect stops an SSH connection and releases its resources so they
// can be garbage-collected. Closing the *exec.Cmd's Stderr writer is
// critical: ssh.go attaches `&sshConn.Stderr` to the cmd, which keeps
// the bytes.Buffer alive for the lifetime of the cmd. Without explicit
// detachment, every disconnect leaks one SSHConn + buffer.
func (m *SSHManager) Disconnect(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sshConn, exists := m.conns[id]
	if !exists {
		return fmt.Errorf("connection %s not found", id)
	}

	// Signal stop (idempotent: only close if not already closed).
	select {
	case <-sshConn.StopCh:
		// Already closed.
	default:
		close(sshConn.StopCh)
	}

	// Kill the process.
	if sshConn.Process != nil && sshConn.Process.Process != nil {
		if err := sshConn.Process.Process.Kill(); err != nil {
			// Process might have already exited.
			Logf("Failed to kill process for %s (may have already exited): %v", id, err)
		}
	}

	// Detach stderr buffer so the underlying cmd does not retain a
	// reference to our bytes.Buffer (which would prevent GC).
	if sshConn.Process != nil {
		sshConn.Process.Stderr = nil
	}

	// Clear the buffer too (defensive).
	sshConn.Stderr.Reset()
	sshConn.Status = "stopped"
	m.notifyStatus(id, "stopped")
	delete(m.conns, id)

	Logf("SSH tunnel stopped: %s", sshConn.Config.Name)
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
			Logf("Failed to disconnect %s: %v", id, err)
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

// FormatSSHCommand returns the SSH command that would be executed for a given config.
// Useful for debugging and testing. NOTE: the returned string omits the
// sshpass wrapper and password argument — it shows the underlying ssh
// invocation only, so it never leaks credentials into logs or test output.
func FormatSSHCommand(cfg ForwardConfig) string {
	forwardFlag := "-L"
	if cfg.ForwardType == "remote" {
		forwardFlag = "-R"
	}

	forwardTarget := "127.0.0.1"
	if cfg.ForwardType == "remote" {
		if cfg.LocalHost != "" {
			forwardTarget = cfg.LocalHost
		}
	}

	args := []string{
		forwardFlag, fmt.Sprintf("%d:%s:%d", cfg.LocalPort, forwardTarget, cfg.RemotePort),
		"-N",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=no",
		"-l", cfg.SSHUser,
		cfg.RemoteHost,
	}
	return "ssh " + strings.Join(args, " ")
}

// FormatSSHCommandForType is identical to FormatSSHCommand but exists
// as a separately-named export for tests so callers cannot accidentally
// share formatting bugs.
func FormatSSHCommandForType(cfg ForwardConfig) string {
	return FormatSSHCommand(cfg)
}
