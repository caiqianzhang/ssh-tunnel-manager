package main

// Custom window decorations: the platform title bar is disabled via
// app.Decorated(false) in main.go, this file draws the replacement.

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ─── Custom title bar ─────────────────────────────────────────

const titleBarHeight = 32

// dragTag identifies the title bar drag area for ActionMove input ops.
var dragTag = new(int)

// layoutTitleBar draws the custom window decorations: the app title
// centered in the bar (the whole bar doubles as the drag handle) and
// the window control buttons top-right. The platform title bar is
// disabled via app.Decorated(false).
func (ui *UI) layoutTitleBar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(titleBarHeight)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))

	// Bar background.
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, ColorCard)

	return layout.Inset{Left: 10, Right: 6}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				// Title cell doubles as a drag handle: ActionMove
				// makes the platform move the window on press-drag.
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
					// The ActionMove op must attach to its own
					// registered input area, hence the event.Op here.
					// While a modal dialog is open the drag area is
					// disabled: otherwise clicks landing on dialog
					// controls that overlap this region get swallowed
					// as window-move gestures.
					if !ui.showPortConflictDialog {
						event.Op(gtx.Ops, dragTag)
						system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
					}
					lbl := material.Caption(ui.theme, "SSH 隧道管理器")
					lbl.Color = ColorTextSec
					lbl.Font.Weight = font.Normal
					// Centered in the bar on both axes, matching the
					// look of native title bars.
					return layout.Center.Layout(gtx, lbl.Layout)
				}),
				layout.Rigid(ui.titleBarBtn(&ui.minBtn, "–", false)),
				layout.Rigid(layout.Spacer{Width: 4}.Layout),
				layout.Rigid(ui.titleBarBtn(&ui.maxBtn, "□", false)),
				layout.Rigid(layout.Spacer{Width: 4}.Layout),
				layout.Rigid(ui.titleBarBtn(&ui.closeBtn, "✕", true)),
			)
		},
	)
}

// titleBarBtn draws one window-control button with hover feedback.
func (ui *UI) titleBarBtn(btn *widget.Clickable, glyph string, danger bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Dp(28)
			gtx.Constraints = layout.Exact(image.Pt(sz, sz))

			bg := ColorInputBg
			fg := ColorTextSec
			if danger {
				fg = ColorDanger
				if btn.Hovered() {
					bg, fg = ColorDanger, ColorTextInv
				}
			} else if btn.Hovered() {
				bg = ColorBorder
				fg = ColorText
			}

			defer clip.UniformRRect(image.Rect(0, 0, sz, sz), 8).Push(gtx.Ops).Pop()
			paint.Fill(gtx.Ops, bg)
			layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body2(ui.theme, glyph)
				lbl.Color = fg
				lbl.Font.Weight = font.Normal
				return lbl.Layout(gtx)
			})
			return layout.Dimensions{Size: image.Pt(sz, sz)}
		})
	}
}
