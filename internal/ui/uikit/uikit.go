// SPDX-License-Identifier: GPL-3.0-or-later

// Package uikit holds what the root model and the UI components share:
// the render context, the Pane interface and helpers that fit styled
// lines to an exact width.
package uikit

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

// Context is shared by the root model and every pane. The root updates it
// when the terminal's width method or the theme changes.
type Context struct {
	Theme *theme.Theme
	// Width is the width method the renderer uses. It starts as wcwidth
	// and switches to grapheme widths when the terminal reports Unicode
	// core mode (2027), exactly when Bubble Tea's renderer does.
	Width ansi.Method
}

// NewContext returns a context with the given theme and wcwidth widths.
func NewContext(t *theme.Theme) *Context {
	return &Context{Theme: t, Width: ansi.WcWidth}
}

// Pane is a rectangular component of the screen. Panes are mutated in
// place by the root model's Update and never perform I/O; work that does
// is returned as a tea.Cmd.
type Pane interface {
	// Resize sets the pane's size in cells.
	Resize(width, height int)
	// SetFocused tells the pane whether it receives key input.
	SetFocused(focused bool)
	// Update handles a message routed to the pane.
	Update(msg tea.Msg) tea.Cmd
	// View renders at most height lines. The root fits each line to the
	// pane's width, so panes may return shorter lines.
	View() string
}

// FitLines splits view into exactly h lines of exactly w cells: longer
// lines are truncated, shorter ones padded, missing ones blank.
func FitLines(method ansi.Method, view string, w, h int) []string {
	out := make([]string, 0, max(h, 0))
	if w > 0 {
		for line := range strings.SplitSeq(view, "\n") {
			if len(out) == h {
				break
			}
			out = append(out, Fit(method, line, w))
		}
	}
	blank := strings.Repeat(" ", max(w, 0))
	for len(out) < h {
		out = append(out, blank)
	}
	return out
}

// Fit truncates or pads one (possibly styled) line to exactly w cells.
func Fit(method ansi.Method, line string, w int) string {
	lw := method.StringWidth(line)
	if lw > w {
		line = Truncate(method, line, w, "")
		lw = method.StringWidth(line) // a wide character may leave a gap
	}
	if lw < w {
		line += strings.Repeat(" ", w-lw)
	}
	return line
}
