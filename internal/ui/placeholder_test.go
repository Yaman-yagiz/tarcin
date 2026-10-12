// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// placeholder is a minimal pane for tests.
type placeholder struct {
	ctx     *uikit.Context
	title   string
	w, h    int
	focused bool
}

func newPlaceholder(ctx *uikit.Context, title string) *placeholder {
	return &placeholder{ctx: ctx, title: title}
}

func (p *placeholder) Resize(w, h int)         { p.w, p.h = w, h }
func (p *placeholder) SetFocused(focused bool) { p.focused = focused }
func (p *placeholder) Update(tea.Msg) tea.Cmd  { return nil }
func (p *placeholder) View() string {
	if p.h <= 0 {
		return ""
	}
	if p.focused {
		return p.ctx.Theme.Accent.Render("\u25b6 " + p.title)
	}
	return p.ctx.Theme.Muted.Render("  " + p.title)
}
