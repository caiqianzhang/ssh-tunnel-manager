package main

import (
	"fmt"
	"log"
	"os"
	"sync"

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

func main() {
	// Only one instance may run: a second one would conflict with the
	// tunnels of the first and confuse the user.
	lockFile, err := acquireSingleInstanceLock()
	if err != nil {
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

	// A second launch (e.g. double-clicking the binary again) wakes
	// the running instance instead of starting a competing process.
	if notifyRunningInstance() {
		fmt.Fprintln(os.Stderr, "ssh-tunnel-manager: 已通知正在运行的实例显示窗口")
		return
	}

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
			log.Printf("Failed to load config: %v", err)
			Logf("runAppLoop: failed to load config: %v", err)
		} else {
			Log("config file not found, using defaults")
		}
	}
	sshMgr := NewSSHManager()

	// Repaint immediately on tunnel status transitions instead of
	// waiting for the next interaction-driven frame.
	sshMgr.SetOnStatusChange(func(forwardID, status string) {
		winMu.Lock()
		w, alive := curWin, winAlive
		winMu.Unlock()
		if alive {
			w.Invalidate()
		}
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
			sshMgr.Connect(fwd)
		}()
	}

	go RunSystray(TrayCallbacks{
		ShowCh:     showCh,
		QuitCh:     quitCh,
		Status:     func() string { return sshMgr.GetStatus(getFirstForwardID(cfg)) },
		Connect:    trayConnect,
		Disconnect: func() { sshMgr.DisconnectAll() },
	})

	// Start the app event loop (blocks until quit)
	go runAppLoop(showReq, quitCh, cfg, sshMgr)

	app.Main()
}

func runAppLoop(showReq, quitCh chan struct{}, cfg *ConfigManager, sshMgr *SSHManager) {
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
			winMu.Unlock()

			Log("runAppLoop: window closed, waiting in system tray")
			needWindow = false
		}

		// Window is gone: wait for show or quit signal.
		select {
		case <-showReq:
			Log("runAppLoop: restoring window from tray")
			needWindow = true
		case <-quitCh:
			Log("runAppLoop: quitting, disconnecting all SSH tunnels")
			sshMgr.DisconnectAll()
			return
		}
	}
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
	} else {
		Logf("autoConnect: successfully started SSH tunnel for '%s'", fwd.Name)
	}
}

func isNotExist(err error) bool {
	return os.IsNotExist(err)
}
