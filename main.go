package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
)

// Window lifecycle state, shared between the tray's show-request
// goroutine and runAppLoop. A live window is restored via
// Perform(ActionRaise); a show request only reaches runAppLoop (which
// creates a new window) when no window exists.
var (
	winMu    sync.Mutex
	curWin   *app.Window
	winAlive bool

	// appUI is set once the UI is created; it lives for the whole
	// process and is reused across windows.
	uiMu  sync.Mutex
	appUI *UI
)

func setAppUI(ui *UI) {
	uiMu.Lock()
	appUI = ui
	uiMu.Unlock()
}

func currentUI() *UI {
	uiMu.Lock()
	defer uiMu.Unlock()
	return appUI
}

// invalidateWindow requests a repaint if a window is currently alive.
// Safe to call from any goroutine; a no-op in tray mode.
func invalidateWindow() {
	winMu.Lock()
	w, alive := curWin, winAlive
	winMu.Unlock()
	if alive {
		w.Invalidate()
	}
}

func main() {
	// Only one instance may run: a second one would conflict with the
	// tunnels of the first and confuse the user.
	lockFile, err := acquireSingleInstanceLock()
	if err != nil {
		// Another instance owns the lock: poke it to raise its window
		// instead of failing silently — a desktop double-click must
		// never be a no-op. (The wake socket can only be reached while
		// the lock holder is alive, which is exactly this case.)
		if notifyRunningInstance() {
			fmt.Fprintln(os.Stderr, "ssh-tunnel-manager: 已通知正在运行的实例显示窗口")
			return
		}
		fmt.Fprintln(os.Stderr, "ssh-tunnel-manager:", err)
		return
	}
	if lockFile != nil {
		defer lockFile.Close()
	}

	// Initialize logger
	if logPath, err := InitLogger(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init logger: %v\n", err)
	} else {
		fmt.Println("Log file:", logPath)
	}
	defer CloseLogger()

	// Load Baidu Cloud DNS credentials (baidu.key) for fast IP
	// resolution in the DDNS scenario. Disables itself silently
	// if the file is absent.
	InitBaiduDNS()

	// A second launch (e.g. double-clicking the binary again) wakes
	// the running instance instead of starting a competing process.
	// (Handled in the lock-failure branch above — the wake socket can
	// only exist while another instance holds the lock.)

	showCh := make(chan struct{}, 1)
	quitCh := make(chan struct{}, 1)
	showReq := make(chan struct{}, 1)

	listenForShowRequests(showCh)

	configPath, err := configFile()
	if err != nil {
		log.Printf("Failed to resolve config path: %v", err)
	}
	cfg := NewConfigManager(configPath)
	if err := cfg.Load(); err != nil {
		if !isNotExist(err) {
			// The on-disk config is probably fine (corrupt file, missing
			// secret key, ...). Mark the manager read-only: saving from
			// the half-loaded in-memory state would overwrite the file
			// and destroy the user's real config.
			log.Printf("Failed to load config: %v", err)
			Logf("runAppLoop: failed to load config: %v — read-only protection enabled", err)
			cfg.SetReadOnly(true)
		} else {
			Log("config file not found, using defaults")
		}
	}
	sshMgr := NewSSHManager()

	// The DDNS heartbeat period can be tuned from the config file
	// (settings.ddns_check_interval, seconds); 0/unset keeps the
	// built-in default of 15s.
	if s := cfg.GetDDNSCheckInterval(); s > 0 {
		sshMgr.SetDDNSCheckInterval(time.Duration(s) * time.Second)
		Logf("DDNS probe interval set to %ds from config", s)
	}

	// Repaint immediately on tunnel status transitions instead of
	// waiting for the next interaction-driven frame.
	sshMgr.SetOnStatusChange(func(forwardID, status string) {
		invalidateWindow()
	})

	// Tray show-request consumer. A live window — even minimized — is
	// restored via Perform(ActionRaise); with no window alive the
	// request is forwarded to runAppLoop, which creates a new one.
	go func() {
		for range showCh {
			winMu.Lock()
			w, alive := curWin, winAlive
			winMu.Unlock()
			if alive {
				w.Perform(system.ActionRaise)
			} else {
				showReq <- struct{}{}
			}
		}
	}()

	// Tray quit and external signals share one shutdown path. The Once
	// guarantees that a second arrival (tray quit racing SIGTERM)
	// cannot cut the first one's DisconnectAll short with os.Exit.
	var shutdownOnce sync.Once
	shutdown := func(trigger string) {
		shutdownOnce.Do(func() {
			Logf("quit (%s): disconnecting all SSH tunnels", trigger)
			sshMgr.DisconnectAll()
			quitProcess()
		})
	}

	// Quit handling lives on its own goroutine: a quit request can
	// arrive while the window is up — runAppLoop is then inside its
	// window event loop and would not see quitCh until the window is
	// closed — or in tray mode with no window at all. Either way the
	// response is the same: disconnect everything, then exit the
	// process for real (see quitProcess).
	go func() {
		<-quitCh
		shutdown("tray")
	}()

	// External termination (kill / Ctrl+C in a terminal) must behave
	// like a tray quit. Without this, a killed process orphaned its
	// ssh children, which kept holding the local forward port and
	// tripped the port-conflict dialog on the next launch.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, os.Interrupt)
	go func() {
		sig := <-sigCh
		shutdown(fmt.Sprintf("signal %v", sig))
	}()

	// trayConnect connects the first forward from the tray. If the
	// local port is taken it surfaces the conflict dialog instead.
	trayConnect := func() {
		go func() {
			forwards := cfg.GetForwards()
			if len(forwards) == 0 {
				return
			}
			fwd := forwards[0]
			inUse, processInfo, err := CheckPortInUse(fwd.LocalPort)
			if err != nil {
				return
			}
			if inUse {
				if u := currentUI(); u != nil {
					u.ShowPortConflictForAutoConnect(fwd.LocalPort, processInfo)
				}
				select {
				case showCh <- struct{}{}:
				default:
				}
				return
			}
			if err := sshMgr.Connect(fwd); err != nil {
				Logf("trayConnect: connect failed: %v", err)
				// Surface the failure to the user instead of silently
				// dropping it — a failed tray "连接" click left the
				// user staring at a gray "未连接" with no idea why.
				if u := currentUI(); u != nil {
					u.queueUIFunc(func() {
						u.setTestResult(fmt.Sprintf("连接失败: %v", err), false)
					})
				}
			}
		}()
	}

	go RunSystray(TrayCallbacks{
		ShowCh:     showCh,
		QuitCh:     quitCh,
		Status:     func() string { return sshMgr.GetStatus(getFirstForwardID(cfg)) },
		Connect:    trayConnect,
		Disconnect: func() { sshMgr.DisconnectAll() },
	})

	// Start the app event loop. It runs until the process is torn down
	// by quitProcess — app.Main() below blocks forever by design.
	go runAppLoop(showReq, cfg, sshMgr)

	app.Main()
}

func runAppLoop(showReq chan struct{}, cfg *ConfigManager, sshMgr *SSHManager) {
	Log("runAppLoop: starting")

	var ui *UI
	needWindow := true

	for {
		if needWindow {
			Log("runAppLoop: creating new window")
			w := new(app.Window)
			w.Option(app.Title("SSH 隧道管理器"))
			w.Option(app.Size(280, 420))
			// Custom decorations: we draw our own minimal title bar.
			w.Option(app.Decorated(false))

			if ui == nil {
				Log("runAppLoop: creating UI for the first time")
				ui = NewUI(cfg, sshMgr)
				// Compute the 域名解析优化 row (DNS lookups) off the
				// UI thread once the config is loaded.
				ui.refreshZoneForwardStatusAsync()
				setAppUI(ui)
				// Auto-connect on first launch
				go autoConnect(cfg, sshMgr, ui)
			}
			ui.AttachWindow(w)

			winMu.Lock()
			curWin, winAlive = w, true
			winMu.Unlock()

			var ops op.Ops
			windowDestroyed := false
			for !windowDestroyed {
				e := w.Event()
				switch e := e.(type) {
				case app.DestroyEvent:
					Log("runAppLoop: window destroyed event received")
					windowDestroyed = true
				case app.FrameEvent:
					gtx := app.NewContext(&ops, e)
					ui.handleEvents(gtx)
					ui.Layout(gtx)
					e.Frame(gtx.Ops)
				}
			}

			winMu.Lock()
			winAlive = false
			// The OS window state (maximized or not) is lost when
			// the window is destroyed, but ui.maximized still
			// reflects the last title-bar toggle. Reset it so the
			// restored window's max button shows the correct icon.
			if ui != nil {
				ui.maximized = false
			}
			winMu.Unlock()

			Log("runAppLoop: window closed, waiting in system tray")
			needWindow = false
		}

		// Window is gone: wait for a show signal. Quitting is handled
		// by the dedicated quit goroutine in main and exits the whole
		// process, so it never reaches this loop.
		select {
		case <-showReq:
			Log("runAppLoop: restoring window from tray")
			needWindow = true
		}
	}
}

// quitProcess performs the final teardown and exits the process.
//
// app.Main() blocks forever on desktop platforms by design (see
// third_party/gio/app/app.go), so there is no graceful return path
// through main(): without an explicit exit the process lingers after a
// tray quit — tunnels disconnected and tray icon gone, but still
// holding the single-instance lock and the show-request socket, still
// showing up in the taskbar, and quietly eating relaunch attempts (a
// new process wakes the zombie instead of starting).
func quitProcess() {
	// Give the tray implementation a moment to unregister the icon —
	// systray.Quit has been called by the time we get here, and exiting
	// immediately can leave a ghost icon behind until the next hover.
	time.Sleep(150 * time.Millisecond)
	// The show-request listener is never closed during the process
	// lifetime; remove its socket so nothing references a dead process.
	// The flock-based lock file needs no cleanup: the OS releases it
	// when we exit.
	removeRuntimeSocket()
	CloseLogger()
	os.Exit(0)
}

func autoConnect(cfg *ConfigManager, sshMgr *SSHManager, ui *UI) {
	Log("autoConnect: starting")
	forwards := cfg.GetForwards()
	if len(forwards) == 0 {
		Log("autoConnect: no forwards configured, skipping")
		return
	}

	fwd := forwards[0]
	Logf("autoConnect: attempting to connect forward '%s' (local port %d)",
		fwd.Name, fwd.LocalPort)

	// Check if port is in use before connecting
	inUse, processInfo, err := CheckPortInUse(fwd.LocalPort)
	if err != nil {
		Logf("autoConnect: port check error for port %d: %v", fwd.LocalPort, err)
		return
	}

	if inUse {
		Logf("autoConnect: port %d is in use by '%s', showing conflict dialog",
			fwd.LocalPort, processInfo)
		// Show port conflict dialog via UI
		ui.ShowPortConflictForAutoConnect(fwd.LocalPort, processInfo)
		Log("autoConnect: port conflict dialog state set")
		return
	}

	Logf("autoConnect: port %d is available, connecting...", fwd.LocalPort)
	// Port is available, connect
	if err := sshMgr.Connect(fwd); err != nil {
		Logf("autoConnect: connect error: %v", err)
		// Surface the failure to the user rather than logging it and
		// leaving them with a gray "未连接" and no explanation.
		ui.queueUIFunc(func() {
			ui.setTestResult(fmt.Sprintf("连接失败: %v", err), false)
		})
	} else {
		Logf("autoConnect: successfully started SSH tunnel for '%s'", fwd.Name)
	}
}

func isNotExist(err error) bool {
	return os.IsNotExist(err)
}
