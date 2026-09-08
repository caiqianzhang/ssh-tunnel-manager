package main

// Settings section content: forward fields, advanced options, save
// button and the shared field/button/scrollable drawing helpers.

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ═══════════════════════════════════════════════════════════════
//  SETTINGS PAGE
// ═══════════════════════════════════════════════════════════════

func (ui *UI) settingsCardForward(gtx layout.Context) layout.Dimensions {
	fields := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(ui.settingsField("远程主机", &ui.remoteHostEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("远程端口", &ui.remotePortEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("本地主机", &ui.localHostEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("本地端口", &ui.localPortEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("SSH 用户", &ui.sshUserEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("SSH 密码", &ui.sshPasswordEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("API Key (x-api-key)", &ui.apiKeyEntry)),
		)
	}
	return fields(gtx)
}

func (ui *UI) settingsCardAdvanced(gtx layout.Context) layout.Dimensions {
	checkbox := func(b *widget.Bool, label string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			cb := material.CheckBox(ui.theme, b, label)
			cb.TextSize = unit.Sp(13) // match the field-label captions
			cb.Color = ColorText
			cb.Size = unit.Dp(22)
			cb.Font.Weight = font.Normal
			return cb.Layout(gtx)
		}
	}
	return ui.drawSection(gtx, "高级选项", func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(ui.settingsField("DDNS 探测周期(秒)", &ui.ddnsIntervalEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsField("DNS 解析服务器", &ui.dnsResolverEntry)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(checkbox(&ui.autoReconnect, "自动重连")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(checkbox(&ui.forwardLocal, "本地转发 (-L)")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.zoneForwardRow),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(ui.versionRow),
		)
	})
}

// versionRow shows the build version (injected via -ldflags at build
// time; "dev" for hand-built binaries).
func (ui *UI) versionRow(gtx layout.Context) layout.Dimensions {
	lbl := material.Caption(ui.theme, "版本 "+appVersion)
	lbl.Color = ColorGray
	lbl.TextSize = unit.Sp(11)
	return lbl.Layout(gtx)
}

func (ui *UI) settingsSaveBtn(gtx layout.Context) layout.Dimensions {
	return ui.saveBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return ui.drawRoundedBtn(gtx, &ui.saveBtn, "保存设置", ColorBlue, ColorOnAccent)
	})
}

// ═══════════════════════════════════════════════════════════════
//  SHARED DRAWING HELPERS
// ═══════════════════════════════════════════════════════════════

// drawSection renders a titled section: a small secondary-color label
// followed by the content, with no card surface (fields themselves are
// dark filled inputs).
func (ui *UI) drawSection(gtx layout.Context, title string, content layout.Widget) layout.Dimensions {
	return layout.Inset{Top: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Caption(ui.theme, title)
				lbl.Color = ColorTextSec
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(content),
		)
	})
}

// settingsField renders a label above a dark filled input.
func (ui *UI) settingsField(label string, editor *widget.Editor) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Caption(ui.theme, label)
				lbl.Color = ColorTextSec
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.filledEditor(gtx, editor)
			}),
		)
	}
}

// filledEditor renders a widget.Editor inside a dark rounded-rect fill.
// Paste (Ctrl+V) is handled by widget.Editor itself when focused.
func (ui *UI) filledEditor(gtx layout.Context, editor *widget.Editor) layout.Dimensions {
	// Fixed input height. Under a layout.Rigid Flex child the parent
	// offers Max = all remaining vertical space, and the Expanded
	// background below absorbs all of it — which blew the settings page
	// fields into full-screen boxes and pushed the conflict-dialog
	// buttons out of their clipped card. Pin the height instead.
	h := gtx.Dp(38)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		// Input background.
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Max
			defer clip.UniformRRect(image.Rectangle{Max: sz}, 6).Push(gtx.Ops).Pop()
			paint.Fill(gtx.Ops, ColorInputBg)
			return layout.Dimensions{Size: sz}
		}),
		// Editor content with comfortable padding.
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 9, Bottom: 9, Left: 10, Right: 10}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					ed := material.Editor(ui.theme, editor, "")
					ed.HintColor = ColorGray
					ed.TextSize = unit.Sp(14)
					return ed.Layout(gtx)
				},
			)
		}),
	)
}

// drawRoundedBtn renders a flat filled rounded-rect button.
func (ui *UI) drawRoundedBtn(gtx layout.Context, btn *widget.Clickable, text string, bg, fg color.NRGBA) layout.Dimensions {
	const btnH = 36

	w := gtx.Constraints.Max.X
	if w < 200 {
		w = 200
	}
	h := gtx.Dp(btnH)
	gtx.Constraints = layout.Exact(image.Pt(w, h))

	if btn.Hovered() {
		bg = ColorBlueDark
	}

	defer clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, bg)

	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Body2(ui.theme, text)
		lbl.Color = fg
		lbl.Font.Weight = font.Bold
		return lbl.Layout(gtx)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// ─── Scrollable Container with Visible Scrollbar ─────────────────

// ScrollableState holds the state for a scrollable container with scrollbar.
type ScrollableState struct {
	list widget.List
}

// LayoutScrollable lays out a scrollable container using material.List with a customized
// visible scrollbar. The built-in Scrollbar widget handles drag-to-scroll correctly.
func LayoutScrollable(gtx layout.Context, theme *material.Theme, state *ScrollableState, content func(gtx layout.Context) layout.Dimensions) layout.Dimensions {
	// Configure list for vertical scrolling
	state.list.Axis = layout.Vertical

	// Create a list style with a custom visible scrollbar
	listStyle := material.List(theme, &state.list)

	// Customize the scrollbar to make it visible
	listStyle.Track.MajorPadding = unit.Dp(2)
	listStyle.Track.MinorPadding = unit.Dp(2)
	listStyle.Track.Color = color.NRGBA{R: 0xF5, G: 0xF7, B: 0xFA, A: 14} // Faint track

	listStyle.Indicator.MajorMinLen = unit.Dp(30)
	listStyle.Indicator.MinorWidth = unit.Dp(6)
	listStyle.Indicator.Color = color.NRGBA{R: 0x8B, G: 0x94, B: 0xA7, A: 110}      // Visible gray
	listStyle.Indicator.HoverColor = color.NRGBA{R: 0x8B, G: 0x94, B: 0xA7, A: 170} // Brighter on hover
	listStyle.Indicator.CornerRadius = unit.Dp(3)

	// Layout the list with the content
	return listStyle.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		return content(gtx)
	})
}

// zoneForwardRow renders the 域名解析优化 feature row. Returns zero
// size on systems without systemd-resolved so the row disappears
// entirely.
//
// Button prominence follows the state: 启用/更新 (an action the user
// asked for) gets the prominent filled button; the up-to-date state
// gets no filled button at all — just a demoted 移除 text link, since
// removing is a rare troubleshooting action and a big button invited
// accidental clicks. The pkexec dialog doubles as its confirmation.
func (ui *UI) zoneForwardRow(gtx layout.Context) layout.Dimensions {
	if !ui.zoneSupported {
		return layout.Dimensions{}
	}
	status := func(gtx layout.Context) layout.Dimensions {
		lbl := material.Body2(ui.theme, "域名解析优化："+ui.zoneStatusText)
		lbl.Color = ColorTextSec
		lbl.TextSize = unit.Sp(12)
		return lbl.Layout(gtx)
	}
	caption := ui.zoneActionLabel
	if ui.zoneBusy {
		caption = "执行中…"
	}

	// 未配置域名 / 查询失败: nothing actionable — status line only.
	// Falling through to the button branch here would render a wide,
	// caption-less blue button that silently ignores clicks.
	if ui.zonePending == "" {
		return status(gtx)
	}

	// Up to date: status line with a demoted 移除 text link.
	if ui.zonePending == zoneActionRemove {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, status),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.zoneBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					fg := ColorTextSec
					if ui.zoneBtn.Hovered() {
						fg = ColorDanger
					}
					lbl := material.Body2(ui.theme, caption)
					lbl.Color = fg
					lbl.TextSize = unit.Sp(12)
					return lbl.Layout(gtx)
				})
			}),
		)
	}

	// 启用 / 更新: the action the user wants gets the prominent button.
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(status),
		layout.Rigid(layout.Spacer{Height: 8}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return ui.drawRoundedBtn(gtx, &ui.zoneBtn, caption, ColorBlue, ColorOnAccent)
		}),
	)
}
