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
// centered on the FULL bar width (matching native title bars) and the
// window control buttons top-right. The platform title bar is disabled
// via app.Decorated(false).
func (ui *UI) layoutTitleBar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(titleBarHeight)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))

	// Bar background.
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, ColorCard)

	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		// Layer 1: the whole bar is the drag surface. The ActionMove op
		// must attach to its own registered input area, hence the
		// event.Op here. While a modal dialog is open the drag area is
		// disabled: otherwise clicks landing on dialog controls that
		// overlap this region get swallowed as window-move gestures.
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			if !ui.showPortConflictDialog {
				event.Op(gtx.Ops, dragTag)
				system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
			}
			return layout.Dimensions{Size: gtx.Constraints.Max}
		}),
		// Layer 2: window control buttons, pinned right. Declared after
		// the drag layer, so their clickable areas take precedence over
		// the drag area beneath them.
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(ui.titleBarBtn(&ui.minBtn, "–", false)),
						layout.Rigid(layout.Spacer{Width: 4}.Layout),
						layout.Rigid(ui.titleBarBtn(&ui.maxBtn, "□", false)),
						layout.Rigid(layout.Spacer{Width: 4}.Layout),
						layout.Rigid(ui.titleBarBtn(&ui.closeBtn, "✕", true)),
					)
				})
			})
		}),
		// Layer 3: the caption, centered by the Stack on both axes. It
		// registers no input ops, so presses over the text still reach
		// the drag surface below.
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(ui.theme, "SSH 隧道管理器")
			lbl.Color = ColorTextSec
			lbl.Font.Weight = font.Normal
			return lbl.Layout(gtx)
		}),
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
			// Mask this area from the bar-wide ActionMove surface: the
			// drag area is an ANCESTOR of this clip (the bar clip spans
			// the whole Stack), and Gio's hit-testing only prunes
			// sibling subtrees — without a non-move action claimed here,
			// pressing the button starts a window drag on every
			// platform. Matches upstream material.Decorations, which
			// adds an ActionInputOp per decoration button.
			system.ActionInputOp(system.ActionRaise).Add(gtx.Ops)
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
