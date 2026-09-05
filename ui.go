package main

import (
	"fmt"
	"image/color"
	"os"
	"strconv"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// UI manages the graphical user interface and user interactions.
type UI struct {
	theme  *material.Theme
	config *ConfigManager
	ssh    *SSHManager

	// Form fields
	nameEntry       widget.Editor
	remoteHostEntry widget.Editor
	remotePortEntry widget.Editor
	localHostEntry  widget.Editor
	localPortEntry  widget.Editor
	sshUserEntry    widget.Editor
	autoReconnect   widget.Bool

	// Buttons
	addBtn        widget.Clickable
	editBtn       widget.Clickable
	deleteBtn     widget.Clickable
	connectBtn    widget.Clickable
	disconnectBtn widget.Clickable

	// List state
	forwardList widget.List
	selectedIdx int
}

// NewUI creates and initializes a new UI instance.
func NewUI(cfg *ConfigManager, sshMgr *SSHManager) *UI {
	th := material.NewTheme()
	th.Palette = material.Palette{
		Fg:         color.NRGBA{R: 0, G: 0, B: 0, A: 255},
		Bg:         color.NRGBA{R: 240, G: 240, B: 240, A: 255},
		ContrastBg: color.NRGBA{R: 50, G: 100, B: 200, A: 255},
		ContrastFg: color.NRGBA{R: 255, G: 255, B: 255, A: 255},
	}

	u := &UI{
		theme:  th,
		config: cfg,
		ssh:    sshMgr,
	}
	u.nameEntry.SingleLine = true
	u.remoteHostEntry.SingleLine = true
	u.remotePortEntry.SingleLine = true
	u.localHostEntry.SingleLine = true
	u.localPortEntry.SingleLine = true
	u.sshUserEntry.SingleLine = true
	return u
}

// Layout renders the complete UI and handles events.
func (ui *UI) Layout(gtx layout.Context) layout.Dimensions {
	ui.handleEvents(gtx)
	return ui.buildLayout(gtx)
}

func (ui *UI) buildLayout(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(ui.renderHeader),
		layout.Rigid(ui.renderForm),
		layout.Rigid(ui.renderCRUDButtons),
		layout.Rigid(ui.renderRuleListHeader),
		layout.Rigid(ui.renderRuleList),
		layout.Rigid(ui.renderControlButtons),
	)
}

func (ui *UI) renderHeader(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: 20, Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.H5(ui.theme, "SSH Tunnel Manager").Layout(gtx)
	})
}

func (ui *UI) renderForm(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Left: 20, Right: 20, Top: 10, Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: 8}.Layout(gtx,
			layout.Rigid(ui.renderEditorField(gtx, "Name:", &ui.nameEntry)),
			layout.Rigid(ui.renderEditorField(gtx, "Remote Host:", &ui.remoteHostEntry)),
			layout.Rigid(ui.renderEditorField(gtx, "Remote Port:", &ui.remotePortEntry)),
			layout.Rigid(ui.renderEditorField(gtx, "Local Host:", &ui.localHostEntry)),
			layout.Rigid(ui.renderEditorField(gtx, "Local Port:", &ui.localPortEntry)),
			layout.Rigid(ui.renderEditorField(gtx, "SSH User:", &ui.sshUserEntry)),
			layout.Rigid(ui.renderCheckBox("Auto Reconnect:", &ui.autoReconnect)),
		)
	})
}

func (ui *UI) renderEditorField(gtx layout.Context, label string, editor *widget.Editor) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: 10}.Layout(gtx, material.Body1(ui.theme, label).Layout)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return material.Editor(ui.theme, editor, "").Layout(gtx)
			}),
		)
	}
}

func (ui *UI) renderCheckBox(label string, boolState *widget.Bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 8}.Layout(gtx, material.CheckBox(ui.theme, boolState, label).Layout)
	}
}

func (ui *UI) renderCRUDButtons(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Left: 20, Right: 20, Top: 10, Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Spacing: 10}.Layout(gtx,
			layout.Rigid(material.Button(ui.theme, &ui.addBtn, "Add").Layout),
			layout.Rigid(material.Button(ui.theme, &ui.editBtn, "Edit").Layout),
			layout.Rigid(material.Button(ui.theme, &ui.deleteBtn, "Delete").Layout),
		)
	})
}

func (ui *UI) renderRuleListHeader(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Left: 20, Right: 20, Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.Subtitle1(ui.theme, "Forwarding Rules:").Layout(gtx)
	})
}

func (ui *UI) renderRuleList(gtx layout.Context) layout.Dimensions {
	forwards := ui.config.GetForwards()

	if len(forwards) == 0 {
		return layout.Inset{Left: 20, Top: 10, Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return material.Body2(ui.theme, "No forwarding rules configured.").Layout(gtx)
		})
	}

	return layout.Inset{Left: 20, Right: 20, Top: 5, Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(ui.theme, &ui.forwardList).Layout(gtx, len(forwards), func(gtx layout.Context, index int) layout.Dimensions {
			fwd := forwards[index]
			isSelected := index == ui.selectedIdx
			status := ui.ssh.GetStatus(fwd.ID)

			text := fmt.Sprintf("%s -> %s:%d via %s@%s [%s]",
				fwd.Name, fwd.RemoteHost, fwd.RemotePort, fwd.SSHUser, fwd.LocalHost, status)

			widget := material.Body1(ui.theme, text)
			if isSelected {
				widget.Color = color.NRGBA{R: 50, G: 100, B: 200, A: 255}
			}

			return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, widget.Layout)
		})
	})
}

func (ui *UI) renderControlButtons(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Left: 20, Right: 20, Top: 10, Bottom: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Spacing: 15}.Layout(gtx,
			layout.Rigid(material.Button(ui.theme, &ui.connectBtn, "Connect").Layout),
			layout.Rigid(material.Button(ui.theme, &ui.disconnectBtn, "Disconnect").Layout),
		)
	})
}

// handleEvents processes user interactions.
func (ui *UI) handleEvents(gtx layout.Context) {
	for ui.addBtn.Clicked(gtx) {
		ui.addForward()
	}
	for ui.editBtn.Clicked(gtx) {
		ui.editForward()
	}
	for ui.deleteBtn.Clicked(gtx) {
		ui.deleteForward()
	}
	for ui.connectBtn.Clicked(gtx) {
		ui.connectForward()
	}
	for ui.disconnectBtn.Clicked(gtx) {
		ui.disconnectForward()
	}
}

func (ui *UI) getSelectedForward() *ForwardConfig {
	forwards := ui.config.GetForwards()
	if ui.selectedIdx >= 0 && ui.selectedIdx < len(forwards) {
		return &forwards[ui.selectedIdx]
	}
	return nil
}

func (ui *UI) readFormInputs() (ForwardConfig, bool) {
	remotePort, err := strconv.Atoi(ui.remotePortEntry.Text())
	if err != nil {
		return ForwardConfig{}, false
	}
	localPort, err := strconv.Atoi(ui.localPortEntry.Text())
	if err != nil {
		return ForwardConfig{}, false
	}

	return ForwardConfig{
		ID:            fmt.Sprintf("fwd_%d", time.Now().UnixNano()),
		Name:          ui.nameEntry.Text(),
		RemoteHost:    ui.remoteHostEntry.Text(),
		RemotePort:    remotePort,
		LocalHost:     ui.localHostEntry.Text(),
		LocalPort:     localPort,
		SSHUser:       ui.sshUserEntry.Text(),
		AutoReconnect: ui.autoReconnect.Value,
	}, true
}

func (ui *UI) addForward() {
	cfg, ok := ui.readFormInputs()
	if !ok {
		return
	}
	ui.config.AddForward(cfg)
	if err := ui.config.Save(); err != nil {
		fmt.Println("Error saving config:", err)
	}
	ui.clearForm()
}

func (ui *UI) editForward() {
	cfg, ok := ui.readFormInputs()
	if !ok {
		return
	}
	fwd := ui.getSelectedForward()
	if fwd != nil {
		cfg.ID = fwd.ID
		ui.config.UpdateForward(fwd.ID, cfg)
		if err := ui.config.Save(); err != nil {
			fmt.Println("Error saving config:", err)
		}
	}
}

func (ui *UI) deleteForward() {
	fwd := ui.getSelectedForward()
	if fwd != nil {
		ui.config.DeleteForward(fwd.ID)
		if err := ui.config.Save(); err != nil {
			fmt.Println("Error saving config:", err)
		}
		ui.selectedIdx = -1
	}
}

func (ui *UI) connectForward() {
	fwd := ui.getSelectedForward()
	if fwd != nil {
		if err := ui.ssh.Connect(*fwd); err != nil {
			fmt.Println("Error connecting:", err)
		}
	}
}

func (ui *UI) disconnectForward() {
	fwd := ui.getSelectedForward()
	if fwd != nil {
		if err := ui.ssh.Disconnect(fwd.ID); err != nil {
			fmt.Println("Error disconnecting:", err)
		}
	}
}

func (ui *UI) clearForm() {
	ui.nameEntry.SetText("")
	ui.remoteHostEntry.SetText("")
	ui.remotePortEntry.SetText("")
	ui.localHostEntry.SetText("")
	ui.localPortEntry.SetText("")
	ui.sshUserEntry.SetText("")
	ui.autoReconnect.Value = false
}

// RunUI is the entry point called from main.go.
func RunUI(w *app.Window) error {
	cfg := NewConfigManager("config.json")
	if err := cfg.Load(); err != nil {
		// Ignore file not found error on first run
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	sshMgr := NewSSHManager()
	ui := NewUI(cfg, sshMgr)

	var ops op.Ops
	for {
		e := w.Event()
		switch e := e.(type) {
		case app.DestroyEvent:
			sshMgr.DisconnectAll()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			ui.Layout(gtx)
			e.Frame(&ops)
		}
	}
}
