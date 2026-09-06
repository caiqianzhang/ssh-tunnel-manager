package main

// Main page: status card, action row and the collapsible settings
// section.

import (
	"fmt"
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
//  MAIN PAGE — compact layout
// ═══════════════════════════════════════════════════════════════

func (ui *UI) layoutMainPage(gtx layout.Context) layout.Dimensions {
	return ui.mainContent(gtx)
}

// drawCard paints a rounded card behind content, sized exactly to the
// measured content. Never size card fills from Constraints.Max: inside
// a scrollable List the main axis is unbounded and the fill would

func (ui *UI) mainContent(gtx layout.Context) layout.Dimensions {
	// When the settings section is expanded the content no longer fits
	// the compact window, so the whole page becomes scrollable.
	content := func(gtx layout.Context) layout.Dimensions {
		widgets := []layout.FlexChild{
			// Status card with connection state and forwarding address.
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				ui.updateHeroStatus()
				return ui.renderHeroStatus(gtx)
			}),
		}
		// Free space keeps the action row anchored to the bottom in the
		// compact layout. Inside the scrollable list (expanded) the main
		// axis is unbounded, where a Flexed child would stretch to
		// infinity and push everything after it out of view — so there
		// we use a fixed spacer instead.
		if !ui.settingsExpanded {
			widgets = append(widgets, layout.Flexed(1, layout.Spacer{}.Layout))
		} else {
			widgets = append(widgets, layout.Rigid(layout.Spacer{Height: 10}.Layout))
		}
		widgets = append(widgets,
			layout.Rigid(ui.renderActionRow),
			layout.Rigid(ui.renderTestResult),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(ui.settingsHeader),
		)
		if ui.settingsExpanded {
			widgets = append(widgets,
				layout.Rigid(layout.Spacer{Height: 8}.Layout),
				layout.Rigid(ui.settingsSection),
			)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, widgets...)
	}

	return layout.Inset{Left: 12, Right: 12, Bottom: 12, Top: 2}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			if ui.settingsExpanded {
				return LayoutScrollable(gtx, ui.theme, &ui.settingsScroll, content)
			}
			return content(gtx)
		},
	)
}

// settingsHeader is the clickable collapse/expand bar for the settings
// section, shown at the bottom of the main page.
func (ui *UI) settingsHeader(gtx layout.Context) layout.Dimensions {
	return ui.settingsToggle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return drawCard(gtx, 10, ColorCard, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					chevron := "▸"
					if ui.settingsExpanded {
						chevron = "▾"
					}
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(ui.theme, "连接设置")
							lbl.Color = ColorText
							return lbl.Layout(gtx)
						}),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(ui.theme, chevron)
							lbl.Color = ColorTextSec
							return lbl.Layout(gtx)
						}),
					)
				},
			)
		})
	})
}

// settingsSection is the expanded settings content: forward fields,
// advanced options and the save button.
func (ui *UI) settingsSection(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(ui.settingsCardForward),
		layout.Rigid(layout.Spacer{Height: 12}.Layout),
		layout.Rigid(ui.settingsCardAdvanced),
		layout.Rigid(layout.Spacer{Height: 16}.Layout),
		layout.Rigid(ui.settingsSaveBtn),
		layout.Rigid(layout.Spacer{Height: 20}.Layout),
	)
}

// ─── Hero status state ───────────────────────────────────────

// updateHeroStatus recomputes the hero status each frame.
func (ui *UI) updateHeroStatus() {
	status := ui.ssh.GetStatus(getFirstForwardID(ui.config))

	// Bug 2 / 18: clear stale test results when connection state
	// changes. The "正在连接..." messages used to stick around forever
	// because nobody told the UI when the connection finally succeeded.
	if status != ui.lastShownStatus {
		switch status {
		case "running":
			// Connection became healthy: clear any stale "connecting..."
			// or error message so the user sees a clean status.
			ui.setTestResult("", true)
		case "disconnected", "port_in_use", "auth_failed", "connection_refused", "unreachable":
			// If we were previously running and now we're in a stable
			// error state, surface that to the user too.
			if ui.lastShownStatus == "running" {
				ui.setTestResult(fmt.Sprintf("连接已断开 (%s)", status), false)
			}
		}
		ui.lastShownStatus = status
	}

	switch status {
	case "running":
		ui.heroDotColor = ColorGreen
		ui.heroStatusText = "已连接"
	case "connecting":
		ui.heroDotColor = ColorYellow
		ui.heroStatusText = "连接中..."
	default:
		ui.heroDotColor = ColorGray
		ui.heroStatusText = "未连接"
	}
}

// ─── Hero status (compact tinted card) ───────────────────────

func (ui *UI) renderHeroStatus(gtx layout.Context) layout.Dimensions {
	forwards := ui.config.GetForwards()
	addr := "未配置转发规则"
	if len(forwards) > 0 {
		fwd := forwards[0]
		addr = fmt.Sprintf("%s:%d → %s:%d", fwd.LocalHost, fwd.LocalPort, fwd.RemoteHost, fwd.RemotePort)
	}

	// Card tinted with a translucent version of the status color.
	tint := ui.heroDotColor
	tint.A = 28

	return drawCard(gtx, 12, tint, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 11, Bottom: 11, Left: 14, Right: 14}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Spacing: 8}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								dot := 10
								defer clip.UniformRRect(image.Rect(0, 0, dot, dot), dot/2).Push(gtx.Ops).Pop()
								paint.Fill(gtx.Ops, ui.heroDotColor)
								return layout.Dimensions{Size: image.Pt(dot, dot)}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Body2(ui.theme, ui.heroStatusText)
								lbl.TextSize = unit.Sp(15)
								lbl.Color = ui.heroDotColor
								return lbl.Layout(gtx)
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: 5}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(ui.theme, addr)
						lbl.Color = ColorTextSec
						return lbl.Layout(gtx)
					}),
				)
			},
		)
	})
}

// ─── Action row: connect + test buttons ──────────────────────

func (ui *UI) renderActionRow(gtx layout.Context) layout.Dimensions {
	// ui.testing is written by testAPI under testMu; snapshot it here
	// so the render thread never reads it without the lock (would race
	// with testAPI's writes).
	ui.testMu.Lock()
	testing := ui.testing
	ui.testMu.Unlock()

	return layout.Flex{Axis: layout.Horizontal, Spacing: 8}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return ui.toggleBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				status := ui.ssh.GetStatus(getFirstForwardID(ui.config))
				running := status == "running"
				text := "连接"
				bg, fg := ColorBlue, ColorOnAccent
				if running {
					text = "断开"
					bg, fg = ColorDanger, ColorTextInv
				}
				if ui.toggleBtn.Hovered() {
					if running {
						bg = ColorDangerDark
					} else {
						bg = ColorBlueDark
					}
				}
				return ui.actionBtn(gtx, text, bg, fg, false)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return ui.ghostBtn(gtx, &ui.testBtn, "测试", testing)
		}),
	)
}

// actionBtn renders a flat filled pill button with rounded corners.
func (ui *UI) actionBtn(gtx layout.Context, text string, bg, fg color.NRGBA, disabled bool) layout.Dimensions {
	const btnH = 36

	w := gtx.Constraints.Max.X
	if w < 80 {
		w = 80
	}
	h := gtx.Dp(btnH)

	// Exact constraints so layout.Center truly centers the label on
	// both axes regardless of the incoming constraints.
	gtx.Constraints = layout.Exact(image.Pt(w, h))

	fill, txt := bg, fg
	if disabled {
		fill = ColorGrayBg
		txt = ColorGray
	}

	defer clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, fill)

	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Body2(ui.theme, text)
		lbl.Color = txt
		lbl.Font.Weight = font.Bold
		return lbl.Layout(gtx)
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// ghostBtn renders an outlined, transparent "secondary action" button.
func (ui *UI) ghostBtn(gtx layout.Context, btn *widget.Clickable, text string, disabled bool) layout.Dimensions {
	const btnH = 36

	return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		if w < 80 {
			w = 80
		}
		h := gtx.Dp(btnH)
		gtx.Constraints = layout.Exact(image.Pt(w, h))

		inner := ColorBg
		if btn.Hovered() {
			inner = ColorInputBg
		}

		// 1px outline: outer rect in border color, inner in background.
		defer clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, ColorBorder)
		defer clip.UniformRRect(image.Rect(1, 1, w-1, h-1), h/2-1).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, inner)

		txt := ColorText
		if disabled {
			txt = ColorGray
		}
		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(ui.theme, text)
			lbl.Color = txt
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		})
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// ─── Test result ─────────────────────────────────────────────

func (ui *UI) renderTestResult(gtx layout.Context) layout.Dimensions {
	ui.testMu.Lock()
	result := ui.testResult
	ok := ui.testOK
	ui.testMu.Unlock()

	if result == "" {
		return layout.Dimensions{}
	}

	bgColor := ColorGreenBg
	txtColor := ColorGreen
	if !ok {
		bgColor = ColorDangerBg
		txtColor = ColorDanger
	}

	return layout.Inset{Top: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return drawCard(gtx, 8, bgColor, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					lbl := material.Caption(ui.theme, result)
					lbl.Color = txtColor
					return lbl.Layout(gtx)
				},
			)
		})
	})
}
