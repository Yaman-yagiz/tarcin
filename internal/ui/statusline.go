// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"strings"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// modeName is the label of the mode badge.
func (m *Model) modeName() string {
	switch m.mode {
	case modeCommand:
		return "COMMAND"
	case modeSearch:
		if m.forwarding != nil {
			return "FORWARD"
		}
		return "SEARCH"
	case modeMembers:
		return "MEMBERS"
	case modeReact:
		return "REACT"
	case modeConfirm:
		return "CONFIRM"
	case modeFind:
		return "FIND"
	case modeDebug:
		return "DEBUG"
	case modeHelp:
		return "HELP"
	}
	if m.focus == FocusComposer {
		return "INSERT"
	}
	return "NORMAL"
}

func connectionLabel(c domain.ConnectionChanged) string {
	label := c.State.String()
	if c.State == domain.ConnDisconnected {
		label = "offline"
	}
	if c.Reason != "" {
		label += ": " + sanitize.Line(c.Reason)
	}
	if c.State == domain.ConnBanned && !c.Until.IsZero() {
		label += " (until " + c.Until.Local().Format("2 Jan 15:04") + ")"
	}
	return label
}

// statusLine renders the bottom line: the mode badge, then either the
// command-line input or the connection, chat, pending keys and the
// latest notice, with the help hint on the right.
func (m *Model) statusLine(w int) string {
	t := m.ctx.Theme
	method := m.ctx.Width
	badge := t.StatusMode.Render(" " + m.modeName() + " ")
	rest := max(w-method.StringWidth(badge), 0)

	if m.mode == modeCommand || m.mode == modeSearch {
		width := max(rest-2-method.StringWidth(m.input.Prompt), 1)
		// A completion's choices follow the command line while it is open.
		var choices string
		if m.mode == modeCommand && m.choices != "" {
			need := method.StringWidth(m.input.Value()) + 1
			if room := width - need - 2; room >= 10 {
				width = need
				choices = "  " + t.Muted.Render(uikit.Truncate(method, m.choices, room, "…"))
			}
		}
		m.input.SetWidth(width)
		return uikit.Fit(method, badge+" "+m.input.View()+choices, w)
	}
	if m.mode == modeReact {
		return uikit.Fit(method, badge+" "+uikit.Display(method, reactPrompt()), w)
	}
	if m.mode == modeConfirm {
		return uikit.Fit(method, badge+" "+t.Warning.Render(m.confirmPrompt()), w)
	}

	conn := connectionLabel(m.conn)
	if m.link != nil && m.link.Welcoming() {
		conn = "not linked" // nothing connects before the notice is accepted
	}
	if m.conn.State.Terminal() {
		// tarcin stopped connecting: the user must act.
		conn = t.Warning.Render(conn)
	}
	segs := []string{conn}
	if m.msgs.Chat() != 0 {
		segs = append(segs, uikit.Display(method, m.header))
	}
	if p := m.keys.Pending(); len(p) > 0 {
		segs = append(segs, strings.Join(p, " ")+"…")
	}
	if m.notice != "" {
		segs = append(segs, "! "+sanitize.Line(m.notice)) // notices may quote names from the network
	}
	right := " ? help "
	if hint := m.selectedMediaHint(); hint != "" {
		right = " " + hint + " · ? help "
	}
	left := " " + strings.Join(segs, " │ ")
	gap := rest - method.StringWidth(left) - method.StringWidth(right)
	var body string
	if gap >= 1 {
		body = left + strings.Repeat(" ", gap) + right
	} else {
		body = uikit.Truncate(method, left, max(rest-1, 0), "…") + " "
	}
	return uikit.Fit(method, badge+t.Status.Render(uikit.Fit(method, body, rest)), w)
}

// selectedMediaHint names the keys that open and save the selected
// message's media, while the messages have focus.
func (m *Model) selectedMediaHint() string {
	if m.focus != FocusMessages || m.mediaHint == "" || m.openMediaFn == nil {
		return ""
	}
	msg, ok := m.msgs.SelectedMessage()
	if !ok || !msg.Kind.HasMedia() || msg.IsRevoked() || msg.Flags.Has(domain.FlagViewOnce) {
		return ""
	}
	return m.mediaHint
}
