package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestApplyConfigSettingsSyncsRuntime pins the startup wiring: the DDNS
// heartbeat period must reach the SSH manager and the fallback DNS
// resolver must reach the ssh package var BEFORE the first (auto-)
// connect. Previously the resolver was only applied by a save from the
// settings UI, so a value hand-edited into config.json was silently
// ignored on startup.
func TestApplyConfigSettingsSyncsRuntime(t *testing.T) {
	oldResolver := dnsResolver
	t.Cleanup(func() { dnsResolver = oldResolver })

	path := filepath.Join(t.TempDir(), "config.json")
	cfgJSON := `{"forward":{"id":"f","name":"d","forward_type":"local","remote_host":"r","remote_port":22,"local_host":"l","local_port":2222,"ssh_user":"u"},"settings":{"ddns_check_interval":7,"dns_resolver":"1.1.1.1"}}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	mgr := NewSSHManager()

	applyConfigSettings(cm, mgr)

	if dnsResolver != "1.1.1.1" {
		t.Errorf("expected config resolver 1.1.1.1 to be applied, got %q", dnsResolver)
	}
	if mgr.ddnsCheckInterval != 7*time.Second {
		t.Errorf("expected DDNS interval 7s, got %v", mgr.ddnsCheckInterval)
	}

	// An empty resolver means "use the system resolver" and must pass
	// through as-is, not be replaced by the built-in default; an unset
	// DDNS interval must be left alone (no override, no reset).
	emptyPath := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(emptyPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cm2 := NewConfigManager(emptyPath)
	if err := cm2.Load(); err != nil {
		t.Fatal(err)
	}
	fresh := NewSSHManager()
	applyConfigSettings(cm2, fresh)
	if dnsResolver != "" {
		t.Errorf("expected empty config resolver to select the system resolver, got %q", dnsResolver)
	}
	if fresh.ddnsCheckInterval != time.Duration(DefaultDDNSIntervalSeconds)*time.Second {
		t.Errorf("unset DDNS interval must keep the built-in default, got %v", fresh.ddnsCheckInterval)
	}
}

// TestQuitProcessExitsAndCleansUp pins the tray-quit contract: the
// process must actually terminate (app.Main blocks forever by design,
// so the old quit path left a zombie in the taskbar that also ate
// relaunch attempts) and remove the show-request socket.
//
// os.Exit never returns, so the assertion runs in a self-re-exec'd
// subprocess: the child calls quitProcess and must die with exit code 0
// before reaching the unreachable marker exit; the parent then checks
// the exit code and the socket file.
func TestQuitProcessExitsAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only: uses the XDG_RUNTIME_DIR socket path")
	}

	// Redirect the socket path into a throwaway dir so the test never
	// touches a real instance's socket. The parent pre-creates a fake
	// socket file there; the child's quitProcess must delete it.
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "ssh-tunnel-manager.sock")
	if err := os.WriteFile(sock, []byte("listener"), 0600); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	if os.Getenv("STTM_TEST_QUIT_PROCESS") == "1" {
		// Child: reached only via the re-exec below.
		quitProcess() // must exit(0)
		os.Exit(7)    // unreachable if quitProcess works
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestQuitProcessExitsAndCleansUp$")
	// Duplicate env keys have unspecified precedence, so strip any
	// inherited XDG_RUNTIME_DIR before pointing it at our temp dir.
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env,
		"XDG_RUNTIME_DIR="+tmp,
		"STTM_TEST_QUIT_PROCESS=1",
	)

	err := cmd.Run()
	// A nil error means the child exited 0 — exactly what quitProcess
	// must do. Any error is a start failure or a non-zero exit: code 7
	// specifically means quitProcess returned and the marker was hit.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 7 {
			t.Fatal("quitProcess returned without exiting; marker exit reached")
		}
		t.Fatalf("quitProcess did not exit cleanly: %v", err)
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("show-request socket still present after quitProcess: %v", err)
	}
}
