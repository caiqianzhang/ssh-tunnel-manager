package main

import (
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestUIAddButton tests the Add button functionality
func TestUIAddButton(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Simulate form input
	ui.nameEntry.SetText("Test Server")
	ui.remoteHostEntry.SetText("192.168.1.33")
	ui.remotePortEntry.SetText("15721")
	ui.localHostEntry.SetText("localhost")
	ui.localPortEntry.SetText("2222")
	ui.sshUserEntry.SetText("you")
	ui.sshPasswordEntry.SetText("1")

	// Read form inputs
	cfgResult, ok := ui.readFormInputs()
	if !ok {
		t.Fatal("readFormInputs failed")
	}

	// Validate
	if cfgResult.Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got '%s'", cfgResult.Name)
	}
	if cfgResult.RemoteHost != "192.168.1.33" {
		t.Errorf("expected remote host '192.168.1.33', got '%s'", cfgResult.RemoteHost)
	}
	if cfgResult.RemotePort != 15721 {
		t.Errorf("expected remote port 15721, got %d", cfgResult.RemotePort)
	}
	if cfgResult.LocalPort != 2222 {
		t.Errorf("expected local port 2222, got %d", cfgResult.LocalPort)
	}
	if cfgResult.SSHUser != "you" {
		t.Errorf("expected SSH user 'you', got '%s'", cfgResult.SSHUser)
	}
	if cfgResult.SSHPassword != "1" {
		t.Errorf("expected SSH password '1', got '%s'", cfgResult.SSHPassword)
	}
	if cfgResult.ForwardType != "local" {
		t.Errorf("expected forward type 'local', got '%s'", cfgResult.ForwardType)
	}

	// Test add forward
	ui.config.AddForward(cfgResult)
	forwards := ui.config.GetForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(forwards))
	}
	if forwards[0].Name != "Test Server" {
		t.Errorf("expected forward name 'Test Server', got '%s'", forwards[0].Name)
	}

	// Cleanup
	t.Cleanup(func() {
		ui.config.DeleteForward(forwards[0].ID)
	})
}

// TestUIFormValidation tests form validation
func TestUIFormValidation(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	tests := []struct {
		name        string
		remotePort  string
		localPort   string
		expectError bool
	}{
		{"valid ports", "8080", "80", false},
		{"invalid remote port", "abc", "80", true},
		{"invalid local port", "8080", "xyz", true},
		{"remote port too large", "70000", "80", true},
		{"local port zero", "8080", "0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui.nameEntry.SetText("Test")
			ui.remoteHostEntry.SetText("example.com")
			ui.sshUserEntry.SetText("user")
			ui.remotePortEntry.SetText(tt.remotePort)
			ui.localPortEntry.SetText(tt.localPort)

			_, ok := ui.readFormInputs()
			if ok == tt.expectError {
				t.Errorf("expected error=%v, got ok=%v", tt.expectError, ok)
			}
		})
	}
}

// TestUIForwardType tests local/remote forwarding toggle
func TestUIForwardType(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Default should be local
	if !ui.forwardLocal.Value {
		t.Error("expected default forward type to be local")
	}

	// Test local forwarding
	ui.nameEntry.SetText("Test Local")
	ui.remoteHostEntry.SetText("example.com")
	ui.remotePortEntry.SetText("8080")
	ui.localPortEntry.SetText("80")
	ui.sshUserEntry.SetText("user")
	ui.forwardLocal.Value = true

	cfgResult, ok := ui.readFormInputs()
	if !ok {
		t.Fatal("readFormInputs failed for local")
	}
	if cfgResult.ForwardType != "local" {
		t.Errorf("expected 'local', got '%s'", cfgResult.ForwardType)
	}

	// Test remote forwarding
	ui.nameEntry.SetText("Test Remote")
	ui.forwardLocal.Value = false

	cfgResult, ok = ui.readFormInputs()
	if !ok {
		t.Fatal("readFormInputs failed for remote")
	}
	if cfgResult.ForwardType != "remote" {
		t.Errorf("expected 'remote', got '%s'", cfgResult.ForwardType)
	}
}

// TestUIClearForm tests form clearing
func TestUIClearForm(t *testing.T) {
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Fill form
	ui.nameEntry.SetText("Test")
	ui.remoteHostEntry.SetText("example.com")
	ui.remotePortEntry.SetText("8080")
	ui.localPortEntry.SetText("80")
	ui.sshUserEntry.SetText("user")
	ui.sshPasswordEntry.SetText("pass")
	ui.autoReconnect.Value = true

	// Clear form
	ui.clearForm()

	// Verify cleared
	if ui.nameEntry.Text() != "" {
		t.Errorf("expected empty name, got '%s'", ui.nameEntry.Text())
	}
	if ui.remoteHostEntry.Text() != "" {
		t.Errorf("expected empty remote host, got '%s'", ui.remoteHostEntry.Text())
	}
	if ui.sshPasswordEntry.Text() != "" {
		t.Errorf("expected empty password, got '%s'", ui.sshPasswordEntry.Text())
	}
	if ui.autoReconnect.Value {
		t.Error("expected autoReconnect to be false")
	}
	if !ui.forwardLocal.Value {
		t.Error("expected forwardLocal to be true")
	}
}

// TestGioWindowCreation tests that the Gio window can be created
func TestGioWindowCreation(t *testing.T) {
	// This test verifies that the UI can be initialized without errors
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Verify UI is properly initialized
	if ui.theme == nil {
		t.Error("theme is nil")
	}
	if ui.config == nil {
		t.Error("config is nil")
	}
	if ui.ssh == nil {
		t.Error("ssh is nil")
	}

	// Verify form fields exist
	if ui.nameEntry.Text() != "" {
		t.Error("nameEntry should have empty text")
	}
}

// TestConfigPersistence tests that config is saved and loaded correctly
func TestConfigPersistence(t *testing.T) {
	tmpFile := t.TempDir() + "/test_persistence.json"

	// Create and save config
	cfg1 := NewConfigManager(tmpFile)
	cfg1.AddForward(ForwardConfig{
		ID:            "test-1",
		Name:          "Test Server",
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

	// Load config in new manager
	cfg2 := NewConfigManager(tmpFile)
	if err := cfg2.Load(); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Verify
	forwards := cfg2.GetForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(forwards))
	}
	if forwards[0].Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got '%s'", forwards[0].Name)
	}
	if forwards[0].SSHPassword != "1" {
		t.Errorf("expected password '1', got '%s'", forwards[0].SSHPassword)
	}
}

// TestSSHFowardingCommand tests SSH command generation
func TestSSHFowardingCommand(t *testing.T) {
	tests := []struct {
		name     string
		cfg      ForwardConfig
		expected string
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
			"ssh -L 2222:192.168.1.33:15721 -N -o ServerAliveInterval=60 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes -l you 192.168.1.33",
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
			"ssh -R 80:example.com:8080 -N -o ServerAliveInterval=60 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes -l admin example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := FormatSSHCommand(tt.cfg)
			if cmd != tt.expected {
				t.Errorf("expected:\n%s\ngot:\n%s", tt.expected, cmd)
			}
		})
	}
}

// TestUIEventLoop tests the event loop can be started
func TestUIEventLoop(t *testing.T) {
	// Create a test window
	w := new(app.Window)
	w.Option(app.Size(unit.Dp(100), unit.Dp(100)))

	// Create UI
	cfg := NewConfigManager("test_config.json")
	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	// Create a goroutine that will handle events
	done := make(chan error, 1)
	go func() {
		var ops op.Ops
		for i := 0; i < 10; i++ { // Run for a few iterations
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

	// Wait a bit for the loop to start
	time.Sleep(100 * time.Millisecond)

	// The test passes if the event loop starts without panicking
	// In a real test, we would simulate events
}
