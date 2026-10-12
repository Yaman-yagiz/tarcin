// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// startForward asks for the chat to forward a message to: the chat search
// opens with its own prompt, and Enter forwards instead of opening.
func (m *Model) startForward(msg *domain.Message) {
	switch {
	case m.forwardFn == nil:
		m.notice = "forwarding is not available"
		return
	case !msg.Forwardable():
		m.notice = "This message cannot be forwarded"
		return
	case msg.FromMe && (msg.Status == domain.StatusPending || msg.Status == domain.StatusFailed):
		m.notice = "Only a sent message can be forwarded"
		return
	}
	fwd := *msg
	m.startInput(modeSearch)
	m.forwarding = &fwd
	m.input.Prompt = "forward to /"
}

// forwardTo forwards a message to a chat. The copy shows in that chat at
// once; a failure is reported when the send ends.
func (m *Model) forwardTo(msg *domain.Message, to domain.ChatID) tea.Cmd {
	title, _ := m.chats.Title(to)
	m.notice = "Forwarding to " + title + "…"
	return m.forwardFn(*msg, to)
}
