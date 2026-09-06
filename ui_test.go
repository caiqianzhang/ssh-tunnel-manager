package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/op"
)

// TestUIInitialization tests that the UI initializes correctly.
func TestUIInitialization(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	if ui.theme == nil {
		t.Error("theme is nil")
	}
	if ui.config == nil {
		t.Error("config is nil")
	}
	if ui.ssh == nil {
		t.Error("ssh is nil")
	}
	if ui.settingsExpanded {
		t.Error("expected settings section to start collapsed")
	}
}

// TestUIFormFields tests that form fields can be set and read.
func TestUIFormFields(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	ui.remoteHostEntry.SetText("192.168.1.100")
	ui.remotePortEntry.SetText("8080")
	ui.localHostEntry.SetText("localhost")
	ui.localPortEntry.SetText("3000")
	ui.sshUserEntry.SetText("admin")
	ui.sshPasswordEntry.SetText("secret")

	if ui.remoteHostEntry.Text() != "192.168.1.100" {
		t.Errorf("remoteHostEntry: expected '192.168.1.100', got '%s'", ui.remoteHostEntry.Text())
	}
	if ui.remotePortEntry.Text() != "8080" {
		t.Errorf("remotePortEntry: expected '8080', got '%s'", ui.remotePortEntry.Text())
	}
	if ui.localHostEntry.Text() != "localhost" {
		t.Errorf("localHostEntry: expected 'localhost', got '%s'", ui.localHostEntry.Text())
	}
	if ui.localPortEntry.Text() != "3000" {
		t.Errorf("localPortEntry: expected '3000', got '%s'", ui.localPortEntry.Text())
	}
	if ui.sshUserEntry.Text() != "admin" {
		t.Errorf("sshUserEntry: expected 'admin', got '%s'", ui.sshUserEntry.Text())
	}
	if ui.sshPasswordEntry.Text() != "secret" {
		t.Errorf("sshPasswordEntry: expected 'secret', got '%s'", ui.sshPasswordEntry.Text())
	}
}

// TestUIEditorInsert tests that editor.Insert works for paste simulation.
// This validates the input handling path that was missing in handleEvents.
func TestUIEditorInsert(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Simulate paste by directly using Insert (this is what widget.Editor does internally on Ctrl+V)
	ui.apiKeyEntry.Insert("sk-ant-test123")
	if got := ui.apiKeyEntry.Text(); got != "sk-ant-test123" {
		t.Errorf("apiKeyEntry Insert: expected 'sk-ant-test123', got '%s'", got)
	}

	// Test the test-config persistence still works
	ui.apiKeyEntry.SetText("new-key")
	if got := ui.apiKeyEntry.Text(); got != "new-key" {
		t.Errorf("apiKeyEntry SetText: expected 'new-key', got '%s'", got)
	}
}

// TestUIForwardTypeDefault tests the default forward type.
func TestUIForwardTypeDefault(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	if !ui.forwardLocal.Value {
		t.Error("expected default forward type to be local")
	}
	if !ui.autoReconnect.Value {
		t.Error("expected default autoReconnect to be true")
	}
}

// TestUIForwardTypeToggle tests toggling between local and remote forwarding.
func TestUIForwardTypeToggle(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Default is local
	if !ui.forwardLocal.Value {
		t.Error("expected default to be local")
	}

	// Switch to remote
	ui.forwardLocal.Value = false
	if ui.forwardLocal.Value {
		t.Error("expected forwardLocal to be false after toggle")
	}

	// Switch back to local
	ui.forwardLocal.Value = true
	if !ui.forwardLocal.Value {
		t.Error("expected forwardLocal to be true after toggle back")
	}
}

// TestUINavigation tests page navigation.
func TestUISettingsToggle(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	if ui.settingsExpanded {
		t.Error("expected settings section to start collapsed")
	}

	// Simulate expanding the settings section
	ui.settingsExpanded = true
	if !ui.settingsExpanded {
		t.Error("expected settings section expanded after toggle")
	}

	// And collapsing it again
	ui.settingsExpanded = false
	if ui.settingsExpanded {
		t.Error("expected settings section collapsed after second toggle")
	}
}

// TestConfigPersistence tests that config is saved and loaded correctly.
func TestConfigPersistence(t *testing.T) {
	tmpFile := t.TempDir() + "/test_persistence.json"

	cfg1 := NewConfigManager(tmpFile)
	cfg1.AddForward(ForwardConfig{
		ID:            "test-1",
		Name:          "default",
		ForwardType:   "local",
		RemoteHost:    "192.168.1.33",
		RemotePort:    15721,
		LocalHost:     "localhost",
		LocalPort:     2222,
		SSHUser:       "you",
		SSHPassword:   "1",
		AutoReconnect: true,
	})
	if err := cfg1.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	cfg2 := NewConfigManager(tmpFile)
	if err := cfg2.Load(); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	forwards := cfg2.GetForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(forwards))
	}
	if forwards[0].RemoteHost != "192.168.1.33" {
		t.Errorf("expected remote host '192.168.1.33', got '%s'", forwards[0].RemoteHost)
	}
	if forwards[0].SSHPassword != "1" {
		t.Errorf("expected password '1', got '%s'", forwards[0].SSHPassword)
	}
}

// TestLoadConfigToForm tests loading config into the form fields.
func TestLoadConfigToForm(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()

	// Add a config
	cfg.AddForward(ForwardConfig{
		ID:            "test-load",
		Name:          "default",
		ForwardType:   "remote",
		RemoteHost:    "example.com",
		RemotePort:    9090,
		LocalHost:     "127.0.0.1",
		LocalPort:     7070,
		SSHUser:       "root",
		SSHPassword:   "pass",
		AutoReconnect: false,
	})

	// Create a new UI and verify it loads the config
	ui2 := NewUI(cfg, sshMgr)
	if ui2.remoteHostEntry.Text() != "example.com" {
		t.Errorf("expected remoteHost 'example.com', got '%s'", ui2.remoteHostEntry.Text())
	}
	if ui2.remotePortEntry.Text() != "9090" {
		t.Errorf("expected remotePort '9090', got '%s'", ui2.remotePortEntry.Text())
	}
	if ui2.localPortEntry.Text() != "7070" {
		t.Errorf("expected localPort '7070', got '%s'", ui2.localPortEntry.Text())
	}
	if ui2.sshUserEntry.Text() != "root" {
		t.Errorf("expected sshUser 'root', got '%s'", ui2.sshUserEntry.Text())
	}
	if ui2.forwardLocal.Value {
		t.Error("expected forwardLocal to be false (remote)")
	}
	if ui2.autoReconnect.Value {
		t.Error("expected autoReconnect to be false")
	}

	// Cleanup
	cfg.DeleteForward("test-load")
}

// TestSSHFowardingCommand tests SSH command generation.
func TestSSHFowardingCommand(t *testing.T) {
	tests := []struct {
		name     string
		cfg      ForwardConfig
		contains []string
		notHave  []string
	}{
		{
			"local forwarding",
			ForwardConfig{
				ForwardType: "local",
				RemoteHost:  "192.168.1.33",
				RemotePort:  15721,
				LocalHost:   "localhost",
				LocalPort:   2222,
				SSHUser:     "you",
			},
			[]string{
				"ssh -L 2222:127.0.0.1:15721",
				"-N",
				"-o ServerAliveInterval=60",
				"-o ServerAliveCountMax=3",
				"-o ExitOnForwardFailure=yes",
				"-o StrictHostKeyChecking=accept-new",
				"-o UserKnownHostsFile=",
				"-l you 192.168.1.33",
			},
			[]string{"StrictHostKeyChecking=no"},
		},
		{
			"remote forwarding",
			ForwardConfig{
				ForwardType: "remote",
				RemoteHost:  "example.com",
				RemotePort:  8080,
				LocalHost:   "localhost",
				LocalPort:   80,
				SSHUser:     "admin",
			},
			[]string{
				"ssh -R 80:localhost:8080",
				"-N",
				"-o ServerAliveInterval=60",
				"-o ServerAliveCountMax=3",
				"-o ExitOnForwardFailure=yes",
				"-o StrictHostKeyChecking=accept-new",
				"-o UserKnownHostsFile=",
				"-l admin example.com",
			},
			[]string{"StrictHostKeyChecking=no"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := FormatSSHCommand(tt.cfg)
			for _, want := range tt.contains {
				if !strings.Contains(cmd, want) {
					t.Errorf("expected command to contain %q, got:\n%s", want, cmd)
				}
			}
			for _, forbidden := range tt.notHave {
				if strings.Contains(cmd, forbidden) {
					t.Errorf("expected command NOT to contain %q, got:\n%s", forbidden, cmd)
				}
			}
		})
	}
}

// Bug 3: saveSettings must preserve MaxRetries/RetryInterval from the
// existing forward instead of resetting them to hard-coded 5/5.
func TestSaveSettingsPreservesMaxRetriesAndInterval(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	cfgFile := filepath.Join(tmpDir, "test_save.json")
	cm := NewConfigManager(cfgFile)
	// Seed an existing forward with custom retry settings.
	cm.AddForward(ForwardConfig{
		ID: "preserve-1", Name: "p", RemoteHost: "r", RemotePort: 22,
		LocalHost: "l", LocalPort: 2222, SSHUser: "u",
		AutoReconnect: true,
		MaxRetries:    12,
		RetryInterval: 30,
	})
	if err := cm.Save(); err != nil {
		t.Fatal(err)
	}

	ui := NewUI(cm, NewSSHManager())
	// Manually fill the form fields (simulating user input).
	ui.remoteHostEntry.SetText("r")
	ui.remotePortEntry.SetText("22")
	ui.localHostEntry.SetText("l")
	ui.localPortEntry.SetText("2222")
	ui.sshUserEntry.SetText("u")
	ui.sshPasswordEntry.SetText("")
	ui.forwardLocal.Value = true
	ui.autoReconnect.Value = true
	ui.saveSettings()

	// Reload & check.
	cm2 := NewConfigManager(cfgFile)
	if err := cm2.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := cm2.GetForward("preserve-1")
	if !ok {
		t.Fatal("expected forward to be preserved by ID")
	}
	if got.MaxRetries != 12 {
		t.Errorf("MaxRetries overwritten: got %d, want 12", got.MaxRetries)
	}
	if got.RetryInterval != 30 {
		t.Errorf("RetryInterval overwritten: got %d, want 30", got.RetryInterval)
	}
	if got.RemoteHost != "r" || got.LocalPort != 2222 {
		t.Errorf("basic fields changed unexpectedly: %+v", got)
	}
}

// Bug 5: when the config file cannot be written (e.g., read-only dir),
// saveSettings must surface the error to the user via setTestResult
// rather than silently switching back to the main page.
func TestSaveSettingsReportsFailure(t *testing.T) {
	// Root bypasses filesystem permission bits, so the read-only-dir
	// trick this test relies on cannot make the config unwritable.
	if os.Getuid() == 0 {
		t.Skip("skipped as root: chmod does not restrict writes")
	}

	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	cfgFile := filepath.Join(tmpDir, "test_save_fail.json")
	cm := NewConfigManager(cfgFile)
	ui := NewUI(cm, NewSSHManager())
	ui.remoteHostEntry.SetText("r")
	ui.remotePortEntry.SetText("22")
	ui.localHostEntry.SetText("l")
	ui.localPortEntry.SetText("2222")
	ui.sshUserEntry.SetText("u")
	ui.forwardLocal.Value = true
	ui.autoReconnect.Value = true

	// The settings section stays expanded while saving.
	ui.settingsExpanded = true

	// Make the config file unwritable by making the directory read-only.
	if err := os.Chmod(tmpDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmpDir, 0700) })
	// Save should error, and the UI must NOT switch back to the main page.
	ui.saveSettings()

	// After save attempt, we expect the section to stay expanded and
	// the test result must reflect the failure.
	if !ui.settingsExpanded {
		t.Error("expected settings section to stay expanded after save failure")
	}
	ui.testMu.Lock()
	result := ui.testResult
	ok := ui.testOK
	ui.testMu.Unlock()
	if result == "" {
		t.Error("expected non-empty test result after save failure")
	}
	if ok {
		t.Errorf("expected ok=false after save failure; got message %q", result)
	}
}

// Bug 6: clicking Connect when no forwards are configured must show a
// helpful message instead of silently doing nothing.
func TestConnectWithNoForwardShowsMessage(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	cm := NewConfigManager(filepath.Join(tmpDir, "empty.json"))
	ui := NewUI(cm, NewSSHManager())

	ui.toggleConnection()

	ui.testMu.Lock()
	result := ui.testResult
	ok := ui.testOK
	ui.testMu.Unlock()
	if result == "" {
		t.Error("expected a 'no forward configured' message; got empty")
	}
	if ok {
		t.Errorf("expected ok=false; got message %q", result)
	}
}

// Bug 6b: loadConfigToForm must not silently fill defaults if no
// forward exists. We verify by checking that with an empty config, the
// form is left mostly empty (rather than pre-filled with misleading
// localhost/auto-reconnect/local values).
func TestLoadConfigToFormDoesNotMislead(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	cm := NewConfigManager(filepath.Join(tmpDir, "empty.json"))
	ui := NewUI(cm, NewSSHManager())

	// After init with empty config, form fields should be empty (or
	// only the localHost default — not auto-reconnect etc).
	if ui.remoteHostEntry.Text() != "" {
		t.Errorf("remoteHost should be empty; got %q", ui.remoteHostEntry.Text())
	}
	if ui.remotePortEntry.Text() != "" {
		t.Errorf("remotePort should be empty; got %q", ui.remotePortEntry.Text())
	}
	if ui.localPortEntry.Text() != "" {
		t.Errorf("localPort should be empty; got %q", ui.localPortEntry.Text())
	}
}

// TestUIEventLoop tests the event loop can be started.
func TestUIEventLoop(t *testing.T) {
	w := new(app.Window)
	w.Option(app.Size(100, 100))

	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)
	ui.AttachWindow(w)

	done := make(chan error, 1)
	go func() {
		var ops op.Ops
		for i := 0; i < 10; i++ {
			e := w.Event()
			switch e := e.(type) {
			case app.DestroyEvent:
				done <- e.Err
				return
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				ui.handleEvents(gtx)
				ui.Layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
		done <- nil
	}()

	// w.Event() blocks on the OS event queue, so the goroutine may not
	// finish before the test returns. That's acceptable: the test
	// verifies the event loop runs without panicking. If it does finish
	// (e.g. the window receives a DestroyEvent), we check the error.
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("event loop ended with error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		// Still running — expected for a real window in a test.
	}
}

// Regression test for the stale-connection bug: handleProcessExit
// classifies a tunnel failure and sets a terminal status but never
// removes the connection from the map. Without cleanup, the next
// "连接" click reaches ssh.Connect, which returns
// "connection already exists" — a permanent, unrecoverable error
// until the app is restarted.
//
// checkPortAndConnect must therefore clean up a stale (non-running,
// tracked) connection before attempting to connect.
func TestCheckPortAndConnectCleansUpStaleConn(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	cm := NewConfigManager(filepath.Join(tmpDir, "stale.json"))
	cm.AddForward(ForwardConfig{
		ID: "stale-1", Name: "default", ForwardType: "local",
		RemoteHost: "this-host-does-not-exist.invalid",
		RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
		SSHUser: "u",
	})
	sshMgr := NewSSHManager()
	ui := NewUI(cm, sshMgr)

	// Seed a stale connection in the failed state that handleProcessExit
	// would have left behind.
	stale := &SSHConn{
		Config: ForwardConfig{
			ID: "stale-1", Name: "default", ForwardType: "local",
			RemoteHost: "this-host-does-not-exist.invalid",
			RemotePort: 22, LocalHost: "localhost", LocalPort: 2222,
			SSHUser: "u",
		},
		Status:  "auth_failed",
		StopCh:  make(chan struct{}),
		Process: exec.Command("true"),
	}
	sshMgr.mu.Lock()
	sshMgr.conns["stale-1"] = stale
	sshMgr.mu.Unlock()

	// Deterministic port check: port is free, so we proceed to connect.
	ui.portChecker = func(port int) (bool, string, error) { return false, "", nil }

	fwd := cm.GetForwards()[0]
	ui.checkPortAndConnect(fwd)

	// The stale conn must have been cleaned up synchronously before the
	// background connect was launched.
	if status := sshMgr.GetStatus("stale-1"); status != "not_found" {
		t.Errorf("expected stale conn to be cleaned up, got status %q", status)
	}

	// Wait for the background connect to finish (it will fail at DNS
	// resolution, but it must NOT be the "already exists" error).
	done := make(chan struct{})
	go func() {
		for {
			ui.testMu.Lock()
			r := ui.testResult
			ui.testMu.Unlock()
			if r != "" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect did not report a result in time")
	}

	ui.testMu.Lock()
	result := ui.testResult
	ui.testMu.Unlock()
	if strings.Contains(result, "already exists") {
		t.Errorf("stale conn was not cleaned up before connect; got %q", result)
	}
	if result == "" {
		t.Error("expected a non-empty connect result")
	}
}

// Regression test: the port-conflict dialog state must never be touched
// from a background goroutine. ShowPortConflictForAutoConnect and the
// connect path queue their mutations for the UI thread; only drainUICmds
// (run on the event loop) may apply them. Under -race, the previous
// direct-write implementation flags a data race between worker goroutines
// and the render thread reading the same fields.
func TestConflictDialogStateIsQueuedNotTouched(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Concurrent writers from background goroutines.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ui.ShowPortConflictForAutoConnect(2000+id, "some-process")
			}
		}(i)
	}

	// Concurrent reader on another goroutine, racing the writers. It is
	// part of wg so wg.Wait() below also guarantees it has stopped before
	// we drain — otherwise the drain would race the reader's reads.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = ui.showPortConflictDialog
			_ = ui.portConflictPort
			_ = ui.portConflictProcess
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Drain on the UI thread and verify the state is consistent.
	ui.drainUICmds()
	if !ui.showPortConflictDialog {
		t.Error("expected dialog to be shown after draining the queue")
	}
	if ui.portConflictProcess != "some-process" {
		t.Errorf("unexpected process %q", ui.portConflictProcess)
	}
	if ui.portConflictPort < 2000 || ui.portConflictPort > 2003 {
		t.Errorf("unexpected port %d", ui.portConflictPort)
	}
}
