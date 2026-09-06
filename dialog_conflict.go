package main

// Port conflict modal dialog.

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"
)

// ═══════════════════════════════════════════════════════════════
//  PORT CONFLICT DIALOG
// ═══════════════════════════════════════════════════════════════

func (ui *UI) layoutPortConflictDialog(gtx layout.Context) layout.Dimensions {
	// Semi-transparent overlay + dialog card, centered by a Stack.
	// (Previously the card was offset by hand-computed pixel insets
	// passed as unit.Dp, which misplaced it on HiDPI displays.)
	dialogWidth := gtx.Dp(256)

	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		// Overlay dims everything behind the dialog; clicking it
		// dismisses the dialog without taking any action.
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
			paint.Fill(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 128})
			return ui.overlayBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		// Dialog card.
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			// Fixed width, height wraps the content.
			gtx.Constraints.Min = image.Pt(dialogWidth, 0)
			gtx.Constraints.Max = image.Pt(dialogWidth, gtx.Constraints.Max.Y)

			// Card content: sizes itself to the content.
			return drawCard(gtx, 12, ColorCard, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 18, Bottom: 18, Left: 18, Right: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						// Title
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body2(ui.theme, "端口冲突")
							lbl.Color = ColorDanger
							lbl.Font.Weight = font.Bold
							return lbl.Layout(gtx)
						}),
						layout.Rigid(layout.Spacer{Height: 8}.Layout),
						// Message
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							msg := fmt.Sprintf("端口 %d 已被占用:\n%s", ui.portConflictPort, ui.portConflictProcess)
							lbl := material.Body2(ui.theme, msg)
							lbl.Color = ColorText
							return lbl.Layout(gtx)
						}),
						// New port input — hidden until 更换端口 is pressed.
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if !ui.showNewPortEntry {
								return layout.Dimensions{}
							}
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(layout.Spacer{Height: 10}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Caption(ui.theme, "新端口号:")
									lbl.Color = ColorTextSec
									return lbl.Layout(gtx)
								}),
								layout.Rigid(layout.Spacer{Height: 6}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return ui.filledEditor(gtx, &ui.newPortEntry)
								}),
							)
						}),
						layout.Rigid(layout.Spacer{Height: 16}.Layout),
						// Buttons
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Spacing: 8}.Layout(gtx,
								// Kill process button (destructive, filled)
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return ui.killProcessBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return ui.actionBtn(gtx, "结束进程", ColorDanger, ColorTextInv, ui.conflictBusy)
									})
								}),
								// Change port button (primary, filled)
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return ui.changePortBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return ui.actionBtn(gtx, "更换端口", ColorBlue, ColorOnAccent, ui.conflictBusy)
									})
								}),
							)
						}),
					)
				})
			})
		}),
	)
}
