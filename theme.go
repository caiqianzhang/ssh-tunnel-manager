package main

// Shared visual constants and card painting helpers.

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// ─── Dark Color Palette (blue-cyan accent) ───────────────────

var (
	// Backgrounds — deep charcoal with blue undertone
	ColorBg      = color.NRGBA{R: 0x14, G: 0x17, B: 0x1D, A: 255}
	ColorCard    = color.NRGBA{R: 0x1D, G: 0x21, B: 0x2A, A: 255}
	ColorInputBg = color.NRGBA{R: 0x24, G: 0x2A, B: 0x35, A: 255}
	ColorBorder  = color.NRGBA{R: 0x2C, G: 0x33, B: 0x3F, A: 255}

	// Text
	ColorText     = color.NRGBA{R: 0xE6, G: 0xEA, B: 0xF2, A: 255}
	ColorTextSec  = color.NRGBA{R: 0x9A, G: 0xA4, B: 0xB5, A: 255}
	ColorTextInv  = color.NRGBA{R: 0xF5, G: 0xF7, B: 0xFA, A: 255} // text on dark/colored fills
	ColorOnAccent = color.NRGBA{R: 0x0E, G: 0x12, B: 0x18, A: 255} // dark text on accent fill

	// Accent — blue-cyan
	ColorBlue     = color.NRGBA{R: 0x38, G: 0xBD, B: 0xF8, A: 255}
	ColorBlueDark = color.NRGBA{R: 0x0E, G: 0xA5, B: 0xE9, A: 255}

	// Status (tuned for dark backgrounds)
	ColorGreen      = color.NRGBA{R: 0x34, G: 0xD3, B: 0x99, A: 255}
	ColorGreenBg    = color.NRGBA{R: 0x34, G: 0xD3, B: 0x99, A: 31}
	ColorYellow     = color.NRGBA{R: 0xFB, G: 0xBF, B: 0x24, A: 255}
	ColorYellowBg   = color.NRGBA{R: 0xFB, G: 0xBF, B: 0x24, A: 31}
	ColorGray       = color.NRGBA{R: 0x6B, G: 0x72, B: 0x80, A: 255}
	ColorGrayBg     = color.NRGBA{R: 0x6B, G: 0x72, B: 0x80, A: 26}
	ColorDanger     = color.NRGBA{R: 0xF8, G: 0x71, B: 0x71, A: 255}
	ColorDangerDark = color.NRGBA{R: 0xE1, G: 0x5B, B: 0x5B, A: 255}
	ColorDangerBg   = color.NRGBA{R: 0xF8, G: 0x71, B: 0x71, A: 31}

	// Misc
)

func drawCard(gtx layout.Context, radius int, bg color.NRGBA, content layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := content(gtx)
	c := macro.Stop()
	defer clip.UniformRRect(image.Rectangle{Max: dims.Size}, radius).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, bg)
	c.Add(gtx.Ops)
	return dims
}
