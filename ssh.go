package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
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
	// DDNSRestart is set by the DDNS heartbeat right before it kills
	// the process because the server IP changed. handleProcessExit
	// consumes the flag (and resets it) so autoReconnect can skip the
	// retry sleep: the API just confirmed the server is up at its new
	// IP, so waiting would only prolong the blackout. Guarded by the
	// manager's mu.
	//
	// DDNSGen identifies the current DDNS-monitor generation. It is
	// bumped in the same critical section that publishes "running"
	// (monitorProcess) and the value is handed to startDDNSMonitor;
	// heartbeats from older generations retire themselves when they
	// observe the bump (without this, a monitor that misses the brief
	// non-running window around a reconnect would leak and keep
	// polling the API forever). Guarded by the manager's mu.
	DDNSGen     uint64
	DDNSRestart bool
	StopCh      chan struct{}
}

// SSHManager manages multiple SSH connections
type SSHManager struct {
	conns map[string]*SSHConn
	mu    sync.RWMutex

	// onStatusChange is invoked whenever a forward's status
	// transitions. Guarded by mu; see SetOnStatusChange.
	onStatusChange func(forwardID, status string)

	// notifyCh serializes onStatusChange delivery: rapid transitions
	// could otherwise reach the callback out of order (a stale
	// "disconnected" arriving after the newest "connecting" would make
	// the UI show a dead tunnel).
	notifyCh chan notifyMsg

	// onTerminalFailure, when set, fires for terminal failures the user
	// must see (auth_failed, retry exhaustion, ...) — transitions where
	// no auto-reconnect follows, so the pending "连接中..." banner would
	// otherwise stick around forever. Guarded by mu.
	onTerminalFailure func(forwardID, status string)

	// commandBuilder creates the exec.Cmd for a forward. Defaults to
	// buildSSHCommand; tests can override it to use a fake SSH binary
	// (e.g. sleep) instead of spawning a real ssh process.
	commandBuilder func(ForwardConfig, string) *exec.Cmd

	// ddnsCheckInterval is the period between Baidu DNS IP checks in
	// startDDNSMonitor. Defaults to DefaultDDNSIntervalSeconds (15s);
	// the config file and tests can override it (via
	// SetDDNSCheckInterval — the field is guarded by mu).
	ddnsCheckInterval time.Duration
}

// notifyMsg is one status transition queued for serialized dispatch.
type notifyMsg struct {
	id     string
	status string
	// terminal marks a failure with no auto-reconnect follow-up — the
	// UI should surface it instead of waiting for a state that never
	// comes.
	terminal bool
}

// NewSSHManager creates a new SSHManager
func NewSSHManager() *SSHManager {
	m := &SSHManager{
		conns: make(map[string]*SSHConn),
		commandBuilder: func(cfg ForwardConfig, host string) *exec.Cmd {
			return buildSSHCommand(cfg, host)
		},
		ddnsCheckInterval: time.Duration(DefaultDDNSIntervalSeconds) * time.Second,
		notifyCh:          make(chan notifyMsg, 64),
	}
	// Serialized dispatcher: callbacks observe transitions in the order
	// they happened, one goroutine for the manager's lifetime.
	go func() {
		for msg := range m.notifyCh {
			m.mu.RLock()
			fn := m.onStatusChange
			tf := m.onTerminalFailure
			m.mu.RUnlock()
			if fn != nil {
				fn(msg.id, msg.status)
			}
			if msg.terminal && tf != nil {
				tf(msg.id, msg.status)
			}
		}
	}()
	return m
}

// SetOnTerminalFailure registers a callback for terminal failures (see
// notifyTerminalStatus). Same contract as SetOnStatusChange.
func (m *SSHManager) SetOnTerminalFailure(fn func(forwardID, status string)) {
	m.mu.Lock()
	m.onTerminalFailure = fn
	m.mu.Unlock()
}

// SetDDNSCheckInterval sets the DDNS heartbeat period. Safe to call
// from any goroutine; a ticker already running finishes its current
// cycle with the old value.
func (m *SSHManager) SetDDNSCheckInterval(d time.Duration) {
	m.mu.Lock()
	m.ddnsCheckInterval = d
	m.mu.Unlock()
}

// buildCmd creates the exec.Cmd for a forward using the configured
// commandBuilder. Tests can substitute a fake SSH binary (e.g. sleep)
// instead of spawning a real ssh process.
func (m *SSHManager) buildCmd(cfg ForwardConfig, host string) *exec.Cmd {
	if m.commandBuilder != nil {
		return m.commandBuilder(cfg, host)
	}
	return buildSSHCommand(cfg, host)
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

// notifyStatus queues a status transition for serialized dispatch to
// the registered callback. Safe to call while holding m.mu: the
// callback is invoked from the dispatcher goroutine.
func (m *SSHManager) notifyStatus(forwardID, status string) {
	m.notifyStatusKind(forwardID, status, false)
}

// notifyTerminalStatus queues a terminal failure for dispatch: it fires
// both onStatusChange and onTerminalFailure, letting the UI surface the
// failure immediately (no auto-reconnect will follow).
func (m *SSHManager) notifyTerminalStatus(forwardID, status string) {
	m.notifyStatusKind(forwardID, status, true)
}

func (m *SSHManager) notifyStatusKind(forwardID, status string, terminal bool) {
	select {
	case m.notifyCh <- notifyMsg{id: forwardID, status: status, terminal: terminal}:
	default:
		// Queue full (pathologically slow consumer): drop the oldest
		// entry and retry once — the newest state is the one that
		// matters for the UI.
		select {
		case <-m.notifyCh:
		default:
		}
		select {
		case m.notifyCh <- notifyMsg{id: forwardID, status: status, terminal: terminal}:
		default:
		}
	}
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
		// Total 5s budget: none of these may hang the caller when they
		// prompt for a password or are missing (e.g. inside a test
		// runner or a headless service).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// resolvectl first, without sudo: systemd ships a polkit rule
		// that lets active desktop sessions flush the cache, while the
		// sudo variants below always fail in a GUI app (no tty for the
		// password prompt).
		if err := exec.CommandContext(ctx, "resolvectl", "flush-caches").Run(); err == nil {
			return nil
		}
		if err := exec.CommandContext(ctx, "sudo", "systemd-resolve", "--flush-caches").Run(); err == nil {
			return nil
		}
		// Fallback to nscd
		if err := exec.CommandContext(ctx, "sudo", "service", "nscd", "restart").Run(); err != nil {
			return fmt.Errorf("failed to flush DNS: no supported method worked")
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// dnsResolver is the DNS server used by ResolveHost when the Baidu DNS
// fast-path is unavailable. It defaults to Baidu's public DNS
// (119.29.29.29) rather than the local system resolver: for a domain
// managed on Baidu Cloud DNS the local cache is exactly what we are
// trying to bypass, and Baidu's own resolver is the natural
// authoritative choice. Override via SetDNSResolver (set from the
// settings page).
var dnsResolver = "119.29.29.29"

// SetDNSResolver configures the resolver used by ResolveHost. Pass an
// empty string to fall back to the local system resolver.
func SetDNSResolver(server string) {
	dnsResolver = server
}

// ResolveHost resolves a hostname to a list of IP addresses.
//
// When a DNS server is configured it queries that server directly,
// bypassing the local system resolver (and its stale cache) — this is
// the DDNS fallback path. When no server is configured it uses the
// ordinary system resolver.
func ResolveHost(host string) ([]string, error) {
	if dnsResolver == "" {
		ips, err := net.LookupHost(host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve host %s: %v", host, err)
		}
		return ips, nil
	}

	resolver := &net.Resolver{
		StrictErrors: true,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", dnsResolver+":53")
		},
	}
	ips, err := resolver.LookupHost(context.Background(), host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve host %s via %s: %v", host, dnsResolver, err)
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
		// Don't silently swallow the ss error — it usually means
		// ss is missing or the port is held by a process we can't
		// inspect (e.g. a kernel socket). Log it so the failure is
		// diagnosable.
		Logf("CheckPortInUse: ss failed for port %d: %v", port, err)
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

	// Unprivileged kill first; escalate through pkexec (graphical polkit
	// prompt) for processes owned by root/other users. Plain `sudo` is
	// useless in a GUI app: there is no terminal for the password prompt,
	// so both sudo attempts always failed.
	if err := exec.Command("kill", pid).Run(); err != nil {
		Logf("KillProcessByPort: plain kill failed: %v, escalating via pkexec", err)
		if err := exec.Command("pkexec", "kill", pid).Run(); err != nil {
			Logf("KillProcessByPort: pkexec kill failed: %v, trying SIGKILL", err)
			if err := exec.Command("pkexec", "kill", "-9", pid).Run(); err != nil {
				return fmt.Errorf("结束进程 %s 失败（已尝试 kill 与授权强杀）: %v", pid, err)
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
			if err := exec.Command("pkexec", "kill", "-9", pid).Run(); err == nil {
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

// sshArgs returns the ssh argument list for a tunnel. host is the
// address ssh connects to — normally the IP resolved by
// resolveConnectTarget — and falls back to cfg.RemoteHost when empty.
// Shared by buildSSHCommand (which wraps it in sshpass when a password
// is configured) and by tests asserting the command line.
//
// Forward semantics:
//   - local  (-L): SSH server-side destination. Default 127.0.0.1 means
//     "loopback on the SSH server". Users rarely need to change this.
//   - remote (-R): SSH client-side destination. Must be the address of
//     a service running on the SSH client machine — typically the user's
//     laptop. We use cfg.LocalHost so the configured value reaches the
//     SSH server (default: localhost/127.0.0.1 on the client).
func sshArgs(cfg ForwardConfig, host string) []string {
	if host == "" {
		host = cfg.RemoteHost
	}

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
		// Bound the TCP connect phase: without it an unreachable host
		// (SYN dropped) keeps ssh alive for the kernel's full retry
		// cycle (~2 min on Linux) while monitorProcess's 1.5s
		// confirmation window has already marked the tunnel "running".
		"-o", "ConnectTimeout=10",
		// accept-new accepts hosts not yet in known_hosts but refuses
		// to replace an existing key — far safer than
		// StrictHostKeyChecking=no, which disables host verification
		// entirely and leaves the tunnel open to a MITM downgrade.
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHostsPath(),
	}

	if cfg.SSHPassword != "" {
		args = append(args, "-o", "PreferredAuthentications=password")
		args = append(args, "-o", "PubkeyAuthentication=no")
	}

	args = append(args, "-l", cfg.SSHUser, host)
	return args
}

// buildSSHCommand creates the exec.Cmd for an SSH tunnel. host is the
// connect target resolved by resolveConnectTarget (usually an IP);
// passing the IP instead of the hostname is what makes the DDNS
// fast-path work — ssh re-resolving the hostname itself would use the
// local resolver cache, which can be up to one record TTL stale.
func buildSSHCommand(cfg ForwardConfig, host string) *exec.Cmd {
	args := sshArgs(cfg, host)

	if cfg.SSHPassword != "" {
		// Never pass the password on the command line: every local
		// user can read it via `ps` or /proc/<pid>/cmdline. Instead
		// feed it to sshpass through file descriptor 3 (see
		// attachPasswordPipe). sshpass -d 3 reads the password from
		// that fd and never exposes it in the process title.
		args = append([]string{"-d", "3", "ssh"}, args...)
		return exec.Command("sshpass", args...)
	}
	return exec.Command("ssh", args...)
}

// attachPasswordPipe sets up a pipe carrying the SSH password to
// sshpass via file descriptor 3, so the credential never appears in
// the process command line (ps, /proc/<pid>/cmdline). Returns the
// read end, which the CALLER must close right after a successful
// Start: os/exec does not close caller-provided ExtraFiles, and an
// unclosed read end would leak one fd per connection until GC
// finalizers run. If Start fails, close it immediately instead.
func attachPasswordPipe(cmd *exec.Cmd, password string) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create password pipe: %w", err)
	}
	if _, err := io.WriteString(w, password); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("write password to pipe: %w", err)
	}
	// Close the write end before Start so the child sees EOF after
	// reading the password. The pipe buffer holds the data until the
	// child reads it.
	w.Close()
	cmd.ExtraFiles = []*os.File{r}
	return r, nil
}

// resolveConnectTarget decides what the SSH process should actually
// connect to. When Baidu Cloud DNS credentials are configured and the
// host is a Baidu-managed zone, the authoritative record is fetched
// straight from the API: the local resolver may hold a stale record
// for up to one record TTL (DDNS records often use TTL 60), which
// routes a fresh tunnel to a dead or wrong server right after an IP
// change. On a successful API lookup the OS DNS cache is flushed
// (best effort) so other consumers resolve fresh again. Everything
// else — integration disabled, IP-literal host, API error, unknown
// zone — falls back to ResolveHost, which queries settings.dns_resolver
// directly instead of the local system resolver.
//
// Both paths return the RESOLVED IP as the connect host: letting ssh
// re-resolve the hostname would send it back through the local system
// resolver — the exact stale cache this function exists to bypass.
//
// Returns (connectHost, resolvedIP, error).
func resolveConnectTarget(remoteHost string) (host, ip string, err error) {
	if realIP, ok := ResolveRealIP(remoteHost); ok {
		if ferr := FlushDNS(); ferr != nil {
			Logf("resolveConnectTarget: DNS cache flush failed (non-fatal): %v", ferr)
		}
		return realIP, realIP, nil
	}

	ips, rerr := ResolveHost(remoteHost)
	if rerr != nil {
		return "", "", rerr
	}
	if len(ips) == 0 {
		return "", "", fmt.Errorf("no IPs found for host %s", remoteHost)
	}
	return ips[0], ips[0], nil
}

// Connect starts an SSH tunnel with the given configuration.
//
// Connect-target resolution happens outside the manager lock so a slow
// resolver (or the Baidu DNS API round trip) cannot block other
// connections or status queries. The connection is reported as
// "connecting" immediately; monitorProcess transitions it to "running"
// once the SSH handshake has had time to complete.
func (m *SSHManager) Connect(cfg ForwardConfig) error {
	// Check if already connected (short critical section).
	m.mu.Lock()
	if _, exists := m.conns[cfg.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("connection %s already exists", cfg.ID)
	}
	m.mu.Unlock()

	// Resolve the connect target outside the lock: Baidu DNS API first
	// (authoritative, bypasses a stale local cache), local DNS as
	// fallback.
	hostToUse, ip, err := resolveConnectTarget(cfg.RemoteHost)
	if err != nil {
		return fmt.Errorf("failed to resolve remote host: %v", err)
	}

	cmd := m.buildCmd(cfg, hostToUse)

	// If the config carries a password, feed it to sshpass through a
	// pipe (fd 3) rather than the command line.
	var pwRead *os.File
	if cfg.SSHPassword != "" {
		var err error
		pwRead, err = attachPasswordPipe(cmd, cfg.SSHPassword)
		if err != nil {
			return fmt.Errorf("failed to set up password pipe: %v", err)
		}
	}

	// Create SSHConn
	sshConn := &SSHConn{
		Config:    cfg,
		Process:   cmd,
		Status:    "connecting",
		CurrentIP: ip,
		StopCh:    make(chan struct{}),
	}

	// Capture stderr for diagnostics
	cmd.Stderr = &sshConn.Stderr

	// Start the SSH process
	if err := cmd.Start(); err != nil {
		if pwRead != nil {
			pwRead.Close()
		}
		return fmt.Errorf("failed to start SSH process: %v", err)
	}
	if pwRead != nil {
		// The child holds its own dup of the pipe now; our copy would
		// otherwise linger until GC finalizers run (one leaked fd per
		// password-authenticated connection).
		pwRead.Close()
	}

	// Store connection. Re-check existence under the lock first: another
	// Connect for the same ID may have stored while we were resolving the
	// host and starting the process. Without this check both calls pass
	// the earlier existence test and the loser's tunnel is orphaned — a
	// running SSH process with no monitor and no StopCh, which also
	// fights the winner over the same local port.
	m.mu.Lock()
	if _, exists := m.conns[cfg.ID]; exists {
		m.mu.Unlock()
		if sshConn.Process != nil && sshConn.Process.Process != nil {
			_ = sshConn.Process.Process.Kill()
		}
		_ = sshConn.Process.Wait()
		return fmt.Errorf("connection %s already exists", cfg.ID)
	}
	m.conns[cfg.ID] = sshConn
	m.mu.Unlock()

	// Report "connecting" — monitorProcess will flip this to "running"
	// once the tunnel has had time to establish.
	m.notifyStatus(cfg.ID, "connecting")

	Logf("SSH tunnel started: %s (%s) -> %s:%d via %s (IP: %s)",
		cfg.Name, cfg.ForwardType, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ip)

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
		// Process exited during the confirmation window. If the user
		// disconnected while we were waiting, don't classify the exit
		// or attempt a reconnect.
		select {
		case <-sshConn.StopCh:
			return
		default:
		}
		m.handleProcessExit(id, err, sshConn)
		return
	case <-time.After(confirmWindow):
		// Process survived the window — mark as running.
		var gen uint64
		m.mu.Lock()
		if _, stillExists := m.conns[id]; stillExists {
			sshConn.Status = "running"
			// Claim the DDNS monitor generation in the SAME critical
			// section that publishes "running": heartbeats from older
			// generations retire at their next check, which closes the
			// window where an old monitor could act on a stale
			// snapshot taken before the reconnect.
			sshConn.DDNSGen++
			gen = sshConn.DDNSGen
			m.notifyStatus(id, "running")
		}
		m.mu.Unlock()
		if gen == 0 {
			// The conn was disconnected during the confirm window —
			// nothing to monitor.
			return
		}

		// Start the DDNS heartbeat: if the server's IP changes while the
		// tunnel is up (the DDNS scenario), the old tunnel becomes a
		// black hole — new connections through the local port reach a
		// dead or wrong server, but the SSH process keeps running.
		//
		// The heartbeat periodically queries Baidu DNS for the current
		// IP and compares it to the IP the tunnel is actually connected
		// to. On a mismatch it kills the SSH process, which routes
		// through the existing handleProcessExit → autoReconnect path
		// (which itself re-queries Baidu DNS for the fresh IP).
		m.startDDNSMonitor(id, sshConn, sshConn.Config.RemoteHost, gen)
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

	// Process exited. handleProcessExit acquires m.mu itself and
	// checks whether the connection is still tracked before doing
	// anything, so we do not need to hold the lock here.
	m.handleProcessExit(id, err, sshConn)
}

// freshIPs returns the current IP set for host from the freshest
// available source: the Baidu DNS API when credentials are configured,
// otherwise a direct query of the configured DNS resolver — the same
// resolution order resolveConnectTarget uses. The fallback keeps the
// DDNS heartbeat working on hosts without baidu.key: without it a
// server IP change would go unnoticed until the SSH keepalive (3 min)
// or a TCP timeout killed a tunnel that was already forwarding into a
// black hole. ok is false when neither source can answer; the heartbeat
// skips that cycle and retries on the next tick.
func freshIPs(host string) ([]string, bool) {
	if ip, ok := ResolveRealIP(host); ok {
		return []string{ip}, true
	}
	ips, err := ResolveHost(host)
	if err != nil || len(ips) == 0 {
		return nil, false
	}
	return ips, true
}

// startDDNSMonitor launches the DDNS heartbeat for monitor generation
// gen (claimed by the caller in the same critical section that
// publishes "running" — see monitorProcess).
//
// Every interval it fetches the domain's current IP set (Baidu DNS API
// when configured, the direct DNS query otherwise — see freshIPs) and
// compares it to the IP the tunnel is actually connected to; when that
// IP is no longer in the set it marks DDNSRestart and kills the SSH
// process, which routes through the existing handleProcessExit →
// autoReconnect path (that path re-resolves, so the new tunnel lands
// on the fresh IP). The set-membership check (not first-element
// equality) keeps multi-record hosts from flapping when the resolver
// rotates record order.
//
// The goroutine exits when StopCh closes (Disconnect), when the
// connection is no longer "running", or when a newer generation
// claims the connection — it never outlives the tunnel it monitors.
func (m *SSHManager) startDDNSMonitor(id string, sshConn *SSHConn, remoteHost string, gen uint64) {
	m.mu.RLock()
	interval := m.ddnsCheckInterval
	m.mu.RUnlock()
	if interval <= 0 {
		interval = time.Duration(DefaultDDNSIntervalSeconds) * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-sshConn.StopCh:
				return
			case <-ticker.C:
				// Snapshot under the lock: gen, status and the IP the
				// tunnel is currently connected to.
				m.mu.RLock()
				genOK := sshConn.DDNSGen == gen
				status := sshConn.Status
				currentIP := sshConn.CurrentIP
				m.mu.RUnlock()
				// A newer monitor took over (reconnect happened): this
				// one is obsolete, stop polling.
				if !genOK || status != "running" {
					return
				}

				ips, ok := freshIPs(remoteHost)
				if !ok {
					continue
				}

				// Re-check AFTER the (up to 10s) network call: the
				// earlier snapshot may be stale — a reconnect may have
				// published a new process/IP, or a newer monitor may
				// have taken over. Acting on the stale snapshot could
				// kill a healthy tunnel.
				m.mu.Lock()
				if sshConn.DDNSGen != gen || sshConn.Status != "running" || sshConn.CurrentIP != currentIP {
					m.mu.Unlock()
					return
				}
				// Membership, not first-element equality: a host with
				// several A/AAAA records must not flap when the
				// resolver rotates their order.
				if !slices.Contains(ips, currentIP) {
					Logf("DDNS heartbeat: IP changed %s → %s for %s, restarting tunnel",
						currentIP, ips[0], id)
					// Mark the restart as DDNS-triggered BEFORE killing
					// the process: handleProcessExit consumes this flag
					// to skip the retry sleep (the fresh IP is already
					// confirmed authoritative, so there is nothing to
					// wait for).
					sshConn.DDNSRestart = true
					proc := sshConn.Process
					m.mu.Unlock()
					if proc != nil && proc.Process != nil {
						_ = proc.Process.Kill()
					}
					return
				}
				m.mu.Unlock()
				Logf("DDNS heartbeat: IP OK %s for %s", currentIP, id)
			}
		}
	}()
}

// handleProcessExit classifies the exit error, updates status, and
// triggers auto-reconnect when appropriate. It acquires m.mu itself;
// callers must NOT hold it.
func (m *SSHManager) handleProcessExit(id string, err error, sshConn *SSHConn) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// The connection may have been removed (e.g. by Disconnect) while
	// we were waiting for the process to exit; nothing to do then.
	if _, ok := m.conns[id]; !ok {
		return
	}

	if err != nil {
		Logf("SSH process for %s exited with error: %v", id, err)
	} else {
		Logf("SSH process for %s exited normally", id)
	}

	stderr := sshConn.Stderr.String()

	// Classify against the FULL stderr: ssh prints banners/kex noise
	// first, and truncating before classification could cut the needle
	// ("Permission denied", "Address already in use") out of the haystack
	// — a missed auth failure used to keep retrying and hammering the
	// server's password limit.
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

	// Log at most 200 bytes of stderr: it can contain credential prompts
	// or other sensitive output, and the log file may be world-readable.
	if stderr != "" {
		if len(stderr) > 200 {
			stderr = stderr[:200] + "..."
		}
		Logf("SSH stderr for %s: %s", id, stderr)
	}

	sshConn.Status = status

	// A reconnect follows only when classification allows it AND the
	// config enables it. Only then is the failure transient for the
	// user (the "连接中..." banner applies); otherwise the transition is
	// terminal and must be surfaced right away — a first-connect failure
	// in particular used to leave the banner stuck on "连接中...".
	reconnectComing := shouldReconnect && sshConn.Config.AutoReconnect
	if reconnectComing {
		m.notifyStatus(id, status)
	} else {
		m.notifyTerminalStatus(id, status)
	}

	// Check if auto-reconnect is enabled. Consume the DDNSRestart flag
	// here (under the lock) so the flag belongs to exactly one restart.
	if reconnectComing {
		ddnsTriggered := sshConn.DDNSRestart
		sshConn.DDNSRestart = false
		if ddnsTriggered {
			Logf("Auto-reconnect for %s was triggered by a DDNS IP change — skipping the initial retry wait", id)
		}
		go m.autoReconnect(id, ddnsTriggered)
	} else if !shouldReconnect {
		Logf("Auto-reconnect skipped for %s due to error type: %s", id, status)
	} else {
		Logf("Auto-reconnect disabled in config for %s", id)
	}
}

// autoReconnect attempts to reconnect an SSH connection.
//
// ddnsTriggered marks a restart initiated by the DDNS heartbeat (the
// server IP changed while the tunnel was up). The first attempt of such
// a restart skips the retry sleep and the explicit DNS flush: the
// Baidu API has just confirmed the server is alive at its new IP, and
// resolveConnectTarget flushes the OS cache itself after a successful
// lookup. Later attempts (and plain failure reconnects) keep the usual
// wait — whatever went wrong may need time to heal.
func (m *SSHManager) autoReconnect(id string, ddnsTriggered bool) {
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

		// The first attempt of a DDNS-triggered restart skips the
		// flush + wait: nothing needs to heal, the new IP is already
		// authoritative and confirmed reachable.
		if !(ddnsTriggered && i == 0) {
			// Flush DNS
			if err := FlushDNS(); err != nil {
				Logf("DNS flush failed: %v", err)
			}

			// Wait before retry
			time.Sleep(time.Duration(retryInterval) * time.Second)
		}

		// Check StopCh before doing anything expensive that leads up to
		// starting a process: if the user disconnected while we were
		// sleeping/resolving, do not spawn an orphan tunnel.
		select {
		case <-sshConn.StopCh:
			Logf("autoReconnect: StopCh signaled before start, aborting for %s", id)
			return
		default:
		}

		// Resolve the connect target: in the DDNS scenario the server IP
		// may have changed while the tunnel was down, but DNS hasn't
		// propagated yet (bounded by the record TTL). The Baidu DNS API
		// returns the current IP immediately; ordinary DNS is the
		// fallback.
		hostToUse, ip, err := resolveConnectTarget(cfg.RemoteHost)
		if err != nil {
			Logf("Failed to resolve host %s: %v", cfg.RemoteHost, err)
			continue
		}

		cmd := m.buildCmd(cfg, hostToUse)

		// Clear stderr buffer and set up capture
		sshConn.Stderr.Reset()
		cmd.Stderr = &sshConn.Stderr

		// Feed the password through a pipe (fd 3) rather than the
		// command line, same as Connect.
		var pwRead *os.File
		if cfg.SSHPassword != "" {
			var err error
			pwRead, err = attachPasswordPipe(cmd, cfg.SSHPassword)
			if err != nil {
				Logf("autoReconnect: password pipe failed: %v", err)
				continue
			}
		}

		// Start SSH process
		if err := cmd.Start(); err != nil {
			if pwRead != nil {
				pwRead.Close()
			}
			Logf("Failed to start SSH process on attempt %d: %v", i+1, err)
			continue
		}
		if pwRead != nil {
			// Same fd-hygiene as Connect: the child owns the pipe now.
			pwRead.Close()
		}

		// Start succeeded — publish the new process and status. Re-check
		// under the lock, and by IDENTITY: restartWithConfig (save
		// settings while a tunnel exists) swaps in a brand-new conn
		// under the same ID while this in-flight reconnect was starting.
		// An existence-only check would publish into the replaced slot —
		// mutating a dead conn, attaching a second monitor to the new
		// tunnel and emitting a spurious "connecting".
		m.mu.Lock()
		if cur, exists := m.conns[id]; !exists || cur != sshConn {
			m.mu.Unlock()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			Logf("autoReconnect: conn %s removed or replaced during start, aborting", id)
			return
		}
		sshConn.Process = cmd
		sshConn.Status = "connecting"
		m.notifyStatus(id, "connecting")
		sshConn.CurrentIP = ip
		m.mu.Unlock()

		Logf("SSH tunnel reconnected: %s -> %s:%d via %s (IP: %s)",
			cfg.Name, cfg.RemoteHost, cfg.RemotePort, cfg.SSHUser, ip)

		// Start monitoring again
		go m.monitorProcess(id)
		return
	}

	Logf("Auto-reconnect failed for %s after %d attempts", id, maxRetries)

	// Surface the failure to the UI: notify the status transition so
	// the render loop repaints with the disconnected state. Without
	// this the user sees a stale "connecting" or last-known status.
	// Terminal: retry budget exhausted, the UI must show the failure.
	m.mu.Lock()
	if c, ok := m.conns[id]; ok {
		c.Status = "disconnected"
		m.notifyTerminalStatus(id, "disconnected")
	}
	m.mu.Unlock()
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

	// NOTE: we deliberately do NOT call sshConn.Stderr.Reset() here.
	// The exec.Cmd started a copy goroutine that reads the child's
	// stderr pipe and writes into sshConn.Stderr; that goroutine is
	// still running until the process is reaped. Resetting the buffer
	// concurrently with that goroutine's writes is a data race
	// (confirmed under -race). The buffer is owned by this SSHConn,
	// which is about to be dropped from the map and GC'd, so clearing
	// it serves no purpose. monitorProcess's Wait goroutine is the
	// one that reaps the process and drains the copy goroutine.
	sshConn.Status = "stopped"
	m.notifyStatus(id, "stopped")
	delete(m.conns, id)

	Logf("SSH tunnel stopped: %s", sshConn.Config.Name)
	return nil
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
