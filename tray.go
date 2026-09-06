package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"time"

	"fyne.io/systray"
)

// TrayCallbacks wires the tray to the running application.
type TrayCallbacks struct {
	// Status returns the current forward status, e.g. "running".
	Status func() string
	// Connect connects the first configured forward (run in its own
	// goroutine by the tray).
	Connect func()
	// Disconnect stops all tunnels.
	Disconnect func()
	// ShowCh requests that the main window be shown/restored.
	ShowCh chan struct{}
	// QuitCh requests full application exit.
	QuitCh chan struct{}
}

// RunSystray initializes the system tray: an icon that reflects the
// connection state, a live status line, "show window" and "quit".
//
// showCh signals the main app to show/restore the window. quitCh
// signals the main app to exit completely.
func RunSystray(cb TrayCallbacks) {
	systray.Run(func() {
		systray.SetTooltip("SSH 隧道管理器")

		// Live status line (disabled — informational only).
		mStatus := systray.AddMenuItem("状态获取中…", "")
		mStatus.Disable()
		systray.AddSeparator()

		mShow := systray.AddMenuItem("显示窗口", "显示或还原主窗口")
		mToggle := systray.AddMenuItem("连接", "连接或断开隧道")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "退出程序")

		// Keep icon + status line + tooltip in sync with the tunnel.
		go func() {
			last := ""
			for {
				st := "disconnected"
				if cb.Status != nil {
					st = cb.Status()
				}
				if st != last {
					last = st
					systray.SetIcon(trayIconPNG(st))
					systray.SetTitle(trayStatusText(st))
					systray.SetTooltip("SSH 隧道管理器 — " + trayStatusText(st))
					mStatus.SetTitle("状态: " + trayStatusText(st))
					if st == "running" {
						mToggle.SetTitle("断开")
					} else {
						mToggle.SetTitle("连接")
					}
				}
				time.Sleep(1500 * time.Millisecond)
			}
		}()

		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					select {
					case cb.ShowCh <- struct{}{}:
					default:
					}
				case <-mToggle.ClickedCh:
					switch {
					case cb.Status != nil && cb.Status() == "running" && cb.Disconnect != nil:
						cb.Disconnect()
					case cb.Connect != nil:
						cb.Connect()
					}
				case <-mQuit.ClickedCh:
					signalQuit(cb.QuitCh)
					systray.Quit()
					return
				}
			}
		}()
	}, func() {
		// onExit — the main loop in main.go owns shutdown.
	})
}

// trayStatusText maps a raw status into a user-facing label.
func trayStatusText(st string) string {
	switch st {
	case "running":
		return "已连接"
	case "connecting":
		return "连接中…"
	}
	return "未连接"
}

// trayIconPNG renders a 16px status dot (green = connected, yellow =
// connecting, gray = down) as PNG bytes for the tray icon.
func trayIconPNG(st string) []byte {
	var c color.NRGBA
	switch st {
	case "running":
		c = color.NRGBA{R: 0x34, G: 0xD3, B: 0x99, A: 255}
	case "connecting":
		c = color.NRGBA{R: 0xFB, G: 0xBF, B: 0x24, A: 255}
	default:
		c = color.NRGBA{R: 0x6B, G: 0x72, B: 0x80, A: 255}
	}
	return circlePNG(c)
}

func circlePNG(c color.NRGBA) []byte {
	const size = 16
	const cx, cy, r = 7.5, 7.5, 6.2
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Sqrt(dx*dx + dy*dy)
			switch {
			case d <= r-1:
				img.SetNRGBA(x, y, c)
			case d <= r:
				a := uint8(float64(c.A) * (r - d))
				img.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: a})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// signalQuit performs a non-blocking send on quitCh. If quitCh is
// already buffered (because the consumer hasn't read yet) the second
// send is dropped — which is exactly what we want: a single quit is
// enough.
func signalQuit(quitCh chan struct{}) {
	select {
	case quitCh <- struct{}{}:
	default:
		// Already pending; ignore.
	}
}
