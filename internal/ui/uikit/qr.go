// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// qrQuiet is the margin of light modules around a QR code. The standard
// asks for four; on a screen, where the code is drawn on its own white
// background, phone cameras find it with two, which lets it fit smaller
// terminals.
const qrQuiet = 2

// QR renders payload as a QR code with half blocks: each text row holds
// two rows of modules, so the code is about as tall as it is wide in a
// terminal. The quiet zone is included. The lines must be drawn dark on
// light; the caller styles them, because a terminal's own colors may be
// the other way round.
func QR(payload string) ([]string, error) {
	code, err := qr.Encode(payload, qr.L)
	if err != nil {
		return nil, fmt.Errorf("encoding the QR code: %w", err)
	}
	n := code.Size + 2*qrQuiet
	dark := func(x, y int) bool { return code.Black(x-qrQuiet, y-qrQuiet) } // out of range is light
	lines := make([]string, 0, (n+1)/2)
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		b.Grow(n * 3)
		for x := range n {
			switch top, bottom := dark(x, y), dark(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		lines = append(lines, b.String())
	}
	return lines, nil
}
