package main

import (
	"fmt"
	"image/color"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ─── UI State ────────────────────────────────────────────────

type UI struct {
	theme  *material.Theme
	config *ConfigManager
	ssh    *SSHManager

	// win is the current app window; needed by the custom title bar
	// to perform window actions (minimize/maximize/close).
	win       *app.Window
	maximized bool

	// Custom title bar buttons
	minBtn   widget.Clickable
	maxBtn   widget.Clickable
	closeBtn widget.Clickable

	// Collapsible settings section (merged into the main page)
	settingsExpanded bool
	settingsToggle   widget.Clickable

	// Main page
	toggleBtn widget.Clickable
	testBtn   widget.Clickable

	// Test state
	testResult string
	testOK     bool
	testing    bool
	testMu     sync.Mutex
	// lastShownStatus remembers the last SSH status that was displayed
	// on the main page. When the status transitions to "running" after
	// a "正在连接..." message was set, we clear the test result so the
	// user sees a clean status instead of stale "connecting…" text.
	lastShownStatus string

	// Settings form
	remoteHostEntry   widget.Editor
	remotePortEntry   widget.Editor
	localHostEntry    widget.Editor
	localPortEntry    widget.Editor
	sshUserEntry      widget.Editor
	sshPasswordEntry  widget.Editor
	apiKeyEntry       widget.Editor
	ddnsIntervalEntry widget.Editor
	autoReconnect     widget.Bool
	forwardLocal      widget.Bool

	// Settings scroll
	settingsList   widget.List
	settingsScroll ScrollableState

	// Settings buttons
	saveBtn widget.Clickable

	// Zone-forwarding (域名解析优化) feature. refreshZoneForwardStatusAsync
	// computes the row's content off the UI thread and applies it via
	// queueUIFunc, so these fields are only mutated on the UI thread;
	// zoneBusy guards double clicks while the pkexec dialog is open.
	zoneBtn         widget.Clickable
	zoneBusy        bool
	zoneSupported   bool
	zoneActionLabel string // button caption: 启用 / 更新 / 移除
	zonePending     string // action to run when the button is clicked
	zoneStatusText  string // one-line status shown above the button

	// Animation
	animTick int

	// Port conflict dialog
	showPortConflictDialog bool
	// showNewPortEntry reveals the change-port input only after the
	// user presses 更换端口.
	showNewPortEntry bool
	// overlayBtn covers the whole screen behind the dialog; clicking
	// it dismisses the dialog.
	overlayBtn          widget.Clickable
	portConflictPort    int
	portConflictProcess string
	// conflictBusy is set true while a kill/change/ignore handler is
	// running. We use it to swallow repeat clicks that would otherwise
	// spawn duplicate SSH processes or double-kill the same PID.
	conflictBusy   bool
	newPortEntry   widget.Editor
	killProcessBtn widget.Clickable
	changePortBtn  widget.Clickable

	// Hero status, recomputed each frame by updateHeroStatus.
	heroDotColor   color.NRGBA
	heroStatusText string

	// portChecker overrides CheckPortInUse for tests; nil means use it.
	portChecker func(port int) (bool, string, error)

	// uiCmdQ carries UI mutations queued by background goroutines. The
	// event loop drains them on the UI thread (see drainUICmds) so
	// widget state — editors, clickables, dialog fields — is never
	// touched from a worker goroutine, which would race with the
	// render thread. Gio renders from a single thread; all UI state
	// changes must happen there.
	uiCmdQ  []uiCmd
	uiCmdMu sync.Mutex
}

// uiCmd is a closure queued by background work and run on the UI
// event thread. It lets worker goroutines request UI changes without
// touching widget state directly (which would be a data race).
type uiCmd func()

// queueUIFunc appends f to the pending UI-command queue. Safe to call
// from any goroutine; the event loop drains the queue on the UI thread
// via drainUICmds.
func (ui *UI) queueUIFunc(f uiCmd) {
	ui.uiCmdMu.Lock()
	ui.uiCmdQ = append(ui.uiCmdQ, f)
	ui.uiCmdMu.Unlock()
}

// drainUICmds runs every pending UI command on the current (UI) thread.
// Call it at the start of handleEvents so queued mutations are applied
// before this frame's events and layout.
func (ui *UI) drainUICmds() {
	ui.uiCmdMu.Lock()
	cmds := ui.uiCmdQ
	ui.uiCmdQ = nil
	ui.uiCmdMu.Unlock()
	for _, f := range cmds {
		f()
	}
}

// AttachWindow binds the UI to a (re)created app window. The window is
// recreated every time the user closes to tray, so main.go calls this
// on each new window.
func (ui *UI) AttachWindow(w *app.Window) {
	ui.win = w
}

func NewUI(cfg *ConfigManager, sshMgr *SSHManager) *UI {
	th := material.NewTheme()
	th.Palette = material.Palette{
		Fg:         ColorText,
		Bg:         ColorBg,
		ContrastBg: ColorBlue,
		ContrastFg: ColorOnAccent,
	}

	u := &UI{
		theme:  th,
		config: cfg,
		ssh:    sshMgr,
	}

	u.remoteHostEntry.SingleLine = true
	u.remotePortEntry.SingleLine = true
	u.localHostEntry.SingleLine = true
	u.localPortEntry.SingleLine = true
	u.sshUserEntry.SingleLine = true
	u.sshPasswordEntry.SingleLine = true
	u.apiKeyEntry.SingleLine = true
	u.apiKeyEntry.Mask = '•' // Mask the API key for security
	u.ddnsIntervalEntry.SingleLine = true

	u.forwardLocal.Value = true
	u.autoReconnect.Value = true
	u.localHostEntry.SetText("localhost")
	u.portChecker = CheckPortInUse

	// Initialize settings list for scrolling
	u.settingsList.Axis = layout.Vertical

	u.loadConfigToForm()
	return u
}

func (ui *UI) loadConfigToForm() {
	forwards := ui.config.GetForwards()
	if len(forwards) > 0 {
		fwd := forwards[0]
		ui.remoteHostEntry.SetText(fwd.RemoteHost)
		ui.remotePortEntry.SetText(strconv.Itoa(fwd.RemotePort))
		ui.localHostEntry.SetText(fwd.LocalHost)
		ui.localPortEntry.SetText(strconv.Itoa(fwd.LocalPort))
		ui.sshUserEntry.SetText(fwd.SSHUser)
		ui.sshPasswordEntry.SetText(fwd.SSHPassword)
		ui.autoReconnect.Value = fwd.AutoReconnect
		ui.forwardLocal.Value = fwd.ForwardType == "local"
	}

	// Load API key from config
	ui.apiKeyEntry.SetText(ui.config.GetAPIKey())

	// DDNS probe interval: show the effective value (the built-in
	// default when the config leaves it unset).
	interval := ui.config.GetDDNSCheckInterval()
	if interval <= 0 {
		interval = DefaultDDNSIntervalSeconds
	}
	ui.ddnsIntervalEntry.SetText(strconv.Itoa(interval))
}

func (ui *UI) Layout(gtx layout.Context) layout.Dimensions {
	ui.animTick++

	// Fill background
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, ColorBg)

	// Page content (below the custom title bar).
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(ui.layoutTitleBar),
		layout.Flexed(1, ui.layoutMainPage),
	)

	// Render port conflict dialog as overlay (on top of any page)
	if ui.showPortConflictDialog {
		Log("UI.Layout: rendering port conflict dialog")
		ui.layoutPortConflictDialog(gtx)
	}

	return dims
}

// ═══════════════════════════════════════════════════════════════
//  EVENT HANDLING
// ═══════════════════════════════════════════════════════════════

func (ui *UI) handleEvents(gtx layout.Context) {
	// Apply any UI mutations queued by background goroutines on the UI
	// thread before processing this frame's events.
	ui.drainUICmds()

	// Custom title bar window controls.
	if ui.win != nil {
		if _, ok := ui.minBtn.Update(gtx); ok {
			Log("titlebar: minimize clicked")
			ui.win.Perform(system.ActionMinimize)
		}
		if _, ok := ui.maxBtn.Update(gtx); ok {
			Logf("titlebar: maximize clicked (maximized=%v)", ui.maximized)
			if ui.maximized {
				ui.win.Perform(system.ActionUnmaximize)
			} else {
				ui.win.Perform(system.ActionMaximize)
			}
			ui.maximized = !ui.maximized
		}
		if _, ok := ui.closeBtn.Update(gtx); ok {
			ui.win.Perform(system.ActionClose)
		}
	}

	// Process editor events FIRST so they can receive focus and input
	// (including paste via Ctrl+V)
	ui.remoteHostEntry.Update(gtx)
	ui.remotePortEntry.Update(gtx)
	ui.localHostEntry.Update(gtx)
	ui.localPortEntry.Update(gtx)
	ui.sshUserEntry.Update(gtx)
	ui.sshPasswordEntry.Update(gtx)
	ui.apiKeyEntry.Update(gtx)
	ui.newPortEntry.Update(gtx)

	// Snapshot testing under the lock: testAPI writes it from a
	// goroutine, so reading it here without the lock would race.
	ui.testMu.Lock()
	testing := ui.testing
	ui.testMu.Unlock()

	// Paste is handled entirely by widget.Editor itself (Ctrl+V issues
	// clipboard.ReadCmd and the editor consumes the DataEvent for its
	// own tag). Per-field paste buttons were removed as redundant.

	if _, ok := ui.settingsToggle.Update(gtx); ok {
		ui.settingsExpanded = !ui.settingsExpanded
		if ui.settingsExpanded {
			// Always reveal the top of the page (status card + action
			// row) after expanding — a stale scroll offset would land
			// the view in the middle of the settings fields.
			ui.settingsScroll.list.Position.First = 0
			ui.settingsScroll.list.Position.Offset = 0
		}
		// Window size is fixed (see main.go app.Size). We do NOT
		// resize at runtime: win.Option(app.Size) is async — the WM
		// processes it after this frame's Layout runs, so the
		// material.List would compute its clip from stale
		// constraints and cut off the status card/buttons. The
		// settings section is scrollable inside the fixed window,
		// which avoids the clip/resize race entirely.
	}
	if _, ok := ui.toggleBtn.Update(gtx); ok {
		ui.toggleConnection()
	}
	if _, ok := ui.testBtn.Update(gtx); ok {
		// Ignore repeat clicks while a test is already running: the
		// ghostBtn only grays out the label, it does not stop the
		// widget from delivering clicks, so without this guard a
		// rapid double-click spawns two concurrent testAPI calls.
		if !testing {
			go ui.testAPI()
		}
	}
	if _, ok := ui.saveBtn.Update(gtx); ok {
		ui.saveSettings()
	}

	// Zone-forwarding feature button (域名解析优化).
	if _, ok := ui.zoneBtn.Update(gtx); ok {
		ui.startZoneForwardAction()
	}

	// Handle port conflict dialog buttons. Bug 22: ignore repeat clicks
	// while a handler is running, so we don't double-kill or spawn two
	// SSH processes. conflictBusy is held until the handler's async
	// connect finishes (the handler queues its own release via
	// queueUIFunc), so it covers the whole critical section.
	if ui.showPortConflictDialog && !ui.conflictBusy {
		if _, ok := ui.killProcessBtn.Update(gtx); ok {
			// conflictBusy stays true until handleKillProcess's async
			// connect completes (it queues conflictBusy=false itself).
			ui.conflictBusy = true
			ui.handleKillProcess()
		}
		if _, ok := ui.changePortBtn.Update(gtx); ok {
			if !ui.showNewPortEntry {
				// First press: reveal the new-port input only.
				ui.showNewPortEntry = true
				return
			}
			// Second press: change the port and connect. conflictBusy
			// stays true until handleChangePort's async connect
			// completes (it queues conflictBusy=false itself).
			ui.conflictBusy = true
			ui.handleChangePort()
		}
		if _, ok := ui.overlayBtn.Update(gtx); ok {
			ui.dismissConflictDialog()
		}
	}
}

// checkPortAndConnect checks if a port is in use and either shows the
// conflict dialog or connects. The connect path runs in a goroutine so
// DNS resolution and SSH start cannot freeze the UI; results arrive
// later via setTestResult / showPortConflictDialog.
func (ui *UI) checkPortAndConnect(fwd ForwardConfig) {
	// Fast path: already running — disconnect synchronously.
	status := ui.ssh.GetStatus(fwd.ID)
	if status == "running" {
		ui.ssh.Disconnect(fwd.ID)
		return
	}

	// If a previous connection is still tracked (failed, disconnected,
	// mid-handshake, etc.) clean it up first. handleProcessExit never
	// removes the conn from the map — it only sets a terminal status —
	// so without this the user gets a permanent
	// "connection already exists" after any tunnel failure, and can
	// only recover by restarting the app.
	if status != "not_found" {
		ui.ssh.Disconnect(fwd.ID)
	}

	// Show "connecting" immediately so the user gets feedback while
	// the port check + SSH start happen in the background.
	ui.setTestResult("连接中...", true)

	go func() {
		inUse, processInfo, err := ui.portChecker(fwd.LocalPort)
		if err != nil {
			fmt.Println("Port check error:", err)
			ui.setTestResult("端口检查失败", false)
			return
		}

		if inUse {
			// Show port conflict dialog. Queue it for the UI thread:
			// setting widget.Editor state from a worker goroutine
			// would race with the render loop.
			ui.queueUIFunc(func() {
				ui.showConflictDialogInternal(fwd.LocalPort, processInfo, false)
			})
			return
		}

		// Port is available, connect
		if err := ui.ssh.Connect(fwd); err != nil {
			fmt.Println("Connect error:", err)
			ui.setTestResult(fmt.Sprintf("连接失败: %v", err), false)
		}
	}()
}

func (ui *UI) toggleConnection() {
	forwards := ui.config.GetForwards()
	if len(forwards) == 0 {
		// Bug 6: be explicit when the user tries to connect with no
		// configured forward. Previously the click silently did nothing.
		ui.setTestResult("✗ 未配置转发规则，请先在设置中添加", false)
		return
	}
	fwd := forwards[0]
	ui.checkPortAndConnect(fwd)
}

// ShowPortConflictForAutoConnect schedules the port conflict dialog to
// be shown. It may be called from a goroutine (e.g. autoConnect at
// startup); the actual dialog state is applied on the UI thread via
// queueUIFunc so widget state is never touched from a worker goroutine.
func (ui *UI) ShowPortConflictForAutoConnect(port int, processInfo string) {
	Logf("UI.ShowPortConflictForAutoConnect: port=%d, process=%s", port, processInfo)
	ui.queueUIFunc(func() {
		ui.showConflictDialogInternal(port, processInfo, false)
	})
	Log("UI.ShowPortConflictForAutoConnect: dialog state queued")
}

// showConflictDialogInternal sets the port-conflict dialog state. It must
// run on the UI thread because it touches widget.Editor state.
func (ui *UI) showConflictDialogInternal(port int, process string, showNewPort bool) {
	ui.showPortConflictDialog = true
	ui.showNewPortEntry = showNewPort
	ui.portConflictPort = port
	ui.portConflictProcess = process
	ui.newPortEntry.SetText(strconv.Itoa(port))
}

// handleKillProcess kills the process using the conflicting port and then attempts to connect.
func (ui *UI) handleKillProcess() {
	Log("UI.handleKillProcess: user clicked kill process button")
	forwards := ui.config.GetForwards()
	if len(forwards) == 0 {
		Log("UI.handleKillProcess: no forwards configured")
		ui.showPortConflictDialog = false
		ui.conflictBusy = false
		return
	}
	fwd := forwards[0]
	Logf("UI.handleKillProcess: attempting to kill process on port %d", fwd.LocalPort)

	// Kill the process
	if err := KillProcessByPort(fwd.LocalPort); err != nil {
		Logf("UI.handleKillProcess: kill failed: %v", err)
		ui.setTestResult(fmt.Sprintf("结束进程失败: %v", err), false)
		ui.showPortConflictDialog = false
		ui.conflictBusy = false
		return
	}

	Logf("UI.handleKillProcess: process killed, attempting to connect to port %d", fwd.LocalPort)
	ui.setTestResult("进程已结束，正在连接...", true)
	ui.showPortConflictDialog = false

	// Try to connect asynchronously so the UI does not freeze during
	// DNS resolution / SSH start. conflictBusy stays true until the
	// connect finishes so repeat clicks cannot spawn a second tunnel;
	// the release is queued for the UI thread via queueUIFunc.
	go func() {
		if err := ui.ssh.Connect(fwd); err != nil {
			Logf("UI.handleKillProcess: connect failed after kill: %v", err)
			ui.queueUIFunc(func() { ui.setTestResult(fmt.Sprintf("连接失败: %v", err), false) })
		}
		ui.queueUIFunc(func() { ui.conflictBusy = false })
	}()
}

// handleChangePort changes the local port to the user-specified value and attempts to connect.
func (ui *UI) handleChangePort() {
	forwards := ui.config.GetForwards()
	if len(forwards) == 0 {
		ui.showPortConflictDialog = false
		ui.conflictBusy = false
		return
	}
	fwd := forwards[0]

	// Parse new port
	newPortStr := strings.TrimSpace(ui.newPortEntry.Text())
	newPort, err := strconv.Atoi(newPortStr)
	if err != nil || newPort < 1 || newPort > 65535 {
		ui.setTestResult("无效的端口号", false)
		ui.conflictBusy = false
		return
	}

	// Update config with new port
	fwd.LocalPort = newPort
	ui.config.UpdateForward(fwd.ID, fwd)
	if err := ui.config.Save(); err != nil {
		ui.setTestResult(fmt.Sprintf("保存配置失败: %v", err), false)
		ui.showPortConflictDialog = false
		ui.conflictBusy = false
		return
	}

	ui.setTestResult(fmt.Sprintf("端口已更改为 %d，正在连接...", newPort), true)
	ui.showPortConflictDialog = false

	// Try to connect with new port asynchronously. conflictBusy stays
	// true until the connect finishes so repeat clicks cannot spawn a
	// second tunnel; the release is queued for the UI thread.
	go func() {
		if err := ui.ssh.Connect(fwd); err != nil {
			ui.queueUIFunc(func() { ui.setTestResult(fmt.Sprintf("连接失败: %v", err), false) })
		}
		ui.queueUIFunc(func() { ui.conflictBusy = false })
	}()
}

// dismissConflictDialog closes the port-conflict dialog without
// taking any action. To retry the connection afterwards, press 连接
// on the main page (it re-runs the conflict check first).
func (ui *UI) dismissConflictDialog() {
	ui.showPortConflictDialog = false
	ui.showNewPortEntry = false
}

// testAPI sends a Claude API request to the local forwarded port to verify connectivity.
func (ui *UI) testAPI() {
	ui.testMu.Lock()
	ui.testing = true
	ui.testResult = "测试中..."
	ui.testOK = false
	ui.testMu.Unlock()

	forwards := ui.config.GetForwards()
	if len(forwards) == 0 {
		ui.setTestResult("未配置转发规则", false)
		return
	}

	fwd := forwards[0]
	addr := net.JoinHostPort(fwd.LocalHost, strconv.Itoa(fwd.LocalPort))

	// Step 1: Check if SSH tunnel is running
	status := ui.ssh.GetStatus(fwd.ID)
	if status != "running" {
		ui.setTestResult("✗ SSH隧道未连接，请先连接", false)
		return
	}

	// Step 2: TCP dial to check port reachability
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		ui.setTestResult(fmt.Sprintf("✗ 端口不可达: %v", err), false)
		return
	}
	conn.Close()

	// Step 3: Claude API POST request
	url := fmt.Sprintf("http://%s/v1/messages", addr)
	body := `{"model":"claude-sonnet-4-20250514","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`

	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		ui.setTestResult(fmt.Sprintf("✗ 构建请求失败: %v", err), false)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := ui.config.GetAPIKey(); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		ui.setTestResult(fmt.Sprintf("✗ 请求失败: %v", err), false)
		return
	}
	defer resp.Body.Close()

	// Read response body for diagnostics
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	bodyStr := strings.TrimSpace(string(bodyBytes))
	if bodyStr != "" {
		Logf("testAPI: HTTP %d, body: %s", resp.StatusCode, bodyStr)
	} else {
		Logf("testAPI: HTTP %d, no body", resp.StatusCode)
	}

	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	var msg string
	if ok {
		msg = fmt.Sprintf("✓ 接口可用 (HTTP %d)", resp.StatusCode)
	} else {
		// Show HTTP status + first 200 chars of body for diagnosis
		short := bodyStr
		if len(short) > 200 {
			short = short[:200] + "..."
		}
		if short == "" {
			msg = fmt.Sprintf("✗ 请求失败 (HTTP %d)", resp.StatusCode)
		} else {
			msg = fmt.Sprintf("✗ HTTP %d: %s", resp.StatusCode, short)
		}
	}
	ui.setTestResult(msg, ok)
}

func (ui *UI) setTestResult(msg string, ok bool) {
	ui.testMu.Lock()
	ui.testResult = msg
	ui.testOK = ok
	ui.testing = false
	ui.testMu.Unlock()
}

func (ui *UI) saveSettings() {
	remoteHost := strings.TrimSpace(ui.remoteHostEntry.Text())
	remotePort, _ := strconv.Atoi(strings.TrimSpace(ui.remotePortEntry.Text()))
	localHost := strings.TrimSpace(ui.localHostEntry.Text())
	localPort, _ := strconv.Atoi(strings.TrimSpace(ui.localPortEntry.Text()))
	sshUser := strings.TrimSpace(ui.sshUserEntry.Text())
	sshPassword := ui.sshPasswordEntry.Text()

	// Validate required fields. Bug 5: any validation failure must keep
	// the user on the settings page (don't switch pages on error).
	if remoteHost == "" {
		ui.setTestResult("✗ 远程主机不能为空", false)
		return
	}
	if sshUser == "" {
		ui.setTestResult("✗ SSH 用户不能为空", false)
		return
	}
	if remotePort < 1 || remotePort > 65535 {
		ui.setTestResult("✗ 远程端口无效 (1-65535)", false)
		return
	}
	if localPort < 1 || localPort > 65535 {
		ui.setTestResult("✗ 本地端口无效 (1-65535)", false)
		return
	}

	if localHost == "" {
		localHost = "localhost"
	}

	forwardType := "local"
	if !ui.forwardLocal.Value {
		forwardType = "remote"
	}

	forwards := ui.config.GetForwards()
	var id string
	var existing ForwardConfig
	hadExisting := false
	if len(forwards) > 0 {
		id = forwards[0].ID
		existing = forwards[0]
		hadExisting = true
		ui.config.DeleteForward(id)
	} else {
		id = fmt.Sprintf("fwd_%d", time.Now().UnixNano())
	}

	// Bug 3: preserve MaxRetries/RetryInterval from the existing
	// forward, otherwise default to 5/5. We also preserve SSHPassword
	// when the user left the field empty (avoids wiping a stored pw
	// because the form is unmasked).
	maxRetries := 5
	retryInterval := 5
	if hadExisting {
		if existing.MaxRetries > 0 {
			maxRetries = existing.MaxRetries
		}
		if existing.RetryInterval > 0 {
			retryInterval = existing.RetryInterval
		}
		// Only preserve the stored password if the user did not type
		// anything in the password field (which is masked, so we can't
		// tell what they intended; an explicit empty form means "keep
		// what was there"). If they typed something, use the new value.
		if sshPassword == "" {
			sshPassword = existing.SSHPassword
		}
	}

	ui.config.AddForward(ForwardConfig{
		ID:            id,
		Name:          "default",
		ForwardType:   forwardType,
		RemoteHost:    remoteHost,
		RemotePort:    remotePort,
		LocalHost:     localHost,
		LocalPort:     localPort,
		SSHUser:       sshUser,
		SSHPassword:   sshPassword,
		AutoReconnect: ui.autoReconnect.Value,
		MaxRetries:    maxRetries,
		RetryInterval: retryInterval,
	})

	// Save API key
	ui.config.SetAPIKey(strings.TrimSpace(ui.apiKeyEntry.Text()))

	// Save the DDNS probe interval. Empty or invalid input keeps the
	// default (0 = unset); negative values are clamped.
	ddnsInterval, _ := strconv.Atoi(strings.TrimSpace(ui.ddnsIntervalEntry.Text()))
	if ddnsInterval < 0 {
		ddnsInterval = 0
	}
	ui.config.SetDDNSCheckInterval(ddnsInterval)
	// Live-apply to the manager: tunnels started or reconnected from
	// now on use the new period (a heartbeat already ticking finishes
	// its current cycle first).
	if ddnsInterval > 0 {
		ui.ssh.ddnsCheckInterval = time.Duration(ddnsInterval) * time.Second
	}

	// Bug 5: report save failure to the user and keep them on the
	// settings page so they can retry. Success transitions to main.
	if err := ui.config.Save(); err != nil {
		fmt.Println("Error saving config:", err)
		Logf("UI.saveSettings: save error: %v", err)
		ui.setTestResult(fmt.Sprintf("✗ 保存失败: %v", err), false)
		return
	}
	Log("UI.saveSettings: config saved successfully")
	ui.setTestResult("✓ 设置已保存", true)
	ui.settingsExpanded = false
	// The forward's domain may have changed — recompute the
	// 域名解析优化 row against the new zone.
	ui.refreshZoneForwardStatusAsync()
}

// ═══════════════════════════════════════════════════════════════
//  HELPERS
// ═══════════════════════════════════════════════════════════════

func getFirstForwardID(cfg *ConfigManager) string {
	forwards := cfg.GetForwards()
	if len(forwards) > 0 {
		return forwards[0].ID
	}
	return ""
}

// ═══════════════════════════════════════════════════════════════
//  ZONE FORWARDING (域名解析优化)
// ═══════════════════════════════════════════════════════════════

// refreshZoneForwardStatusAsync recomputes the zone-forwarding row off
// the UI thread: a file compare for the install state, plus NS/IP
// lookups for the current forward's domain. Results land on the UI
// thread via queueUIFunc. No-op on unsupported systems.
func (ui *UI) refreshZoneForwardStatusAsync() {
	forwards := ui.config.GetForwards()
	go func() {
		if !zoneForwardSupported() {
			return
		}
		var zone string
		if len(forwards) > 0 {
			host := forwards[0].RemoteHost
			if host != "" && net.ParseIP(host) == nil {
				zone, _ = deriveZoneAndSub(host)
			}
		}
		var text, action, pending string
		switch {
		case zone == "" || !strings.Contains(zone, "."):
			text = "未配置域名"
		default:
			expected, err := expectedZoneForwardContent(zone)
			if err != nil {
				// Details for the log; the UI row only has room for a
				// short label (the window is 280px wide).
				Logf("zone forward: %s: %v", zone, err)
				text = "查询失败"
				break
			}
			installed, upToDate := zoneForwardStatus(expected)
			switch {
			case !installed:
				text = "未启用"
				action, pending = "启用", zoneActionInstall
			case !upToDate:
				text = "有更新"
				action, pending = "更新", zoneActionInstall
			default:
				text = "已启用"
				action, pending = "移除", zoneActionRemove
			}
		}
		ui.queueUIFunc(func() {
			ui.zoneSupported = true
			ui.zoneStatusText = text
			ui.zoneActionLabel = action
			ui.zonePending = pending
		})
	}()
}

// startZoneForwardAction launches the pending zone action (install,
// update or remove) in the background. Runs on the UI thread; the heavy
// work (DNS lookups + the pkexec authentication dialog) happens in a
// goroutine whose result is applied via queueUIFunc.
func (ui *UI) startZoneForwardAction() {
	if ui.zoneBusy || ui.zonePending == "" {
		return
	}
	ui.zoneBusy = true
	action := ui.zonePending
	go func() {
		err := func() error {
			forwards := ui.config.GetForwards()
			if len(forwards) == 0 {
				return fmt.Errorf("未配置转发规则")
			}
			host := forwards[0].RemoteHost
			if host == "" || net.ParseIP(host) != nil {
				return fmt.Errorf("远端地址无需解析优化")
			}
			zone, _ := deriveZoneAndSub(host)
			if !strings.Contains(zone, ".") {
				return fmt.Errorf("%q 不是可优化的域名", host)
			}
			if action == zoneActionRemove {
				return removeZoneForward()
			}
			content, err := expectedZoneForwardContent(zone)
			if err != nil {
				return err
			}
			return applyZoneForward(content)
		}()
		ui.queueUIFunc(func() {
			ui.zoneBusy = false
			if err != nil {
				ui.setTestResult("✗ 域名解析优化："+err.Error(), false)
			} else {
				ui.setTestResult("✓ 域名解析优化设置已更新", true)
			}
			ui.refreshZoneForwardStatusAsync()
		})
	}()
}
