// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
)

// Notification is a desktop notification for a new message. Its texts are
// sanitized.
type Notification struct {
	Chat   domain.ChatID
	Title  string // the chat's title
	Sender string // the sender's name, in a group; empty in a direct chat
	Text   string // a preview of the message
}

// notifyWindow is how recent a message must be to notify: older ones are
// a backlog, delivered after tarcin was offline.
const notifyWindow = 2 * time.Minute

// Terminal focus, as focus reporting (DECSET 1004) tells it.
const (
	focusUnknown int8 = iota
	focusIn
	focusOut
)

// setTermFocus records the terminal's focus and tells Attention when the
// user leaves or comes back. Focus that was never reported counts as
// present, so the first FocusMsg changes nothing.
func (m *Model) setTermFocus(f int8) tea.Cmd {
	was := m.termFocus
	if f == focusIn && was == focusUnknown {
		m.termFocus = focusIn
		return nil
	}
	m.termFocus = f
	if was == f || m.attentionFn == nil {
		return nil
	}
	return m.attentionFn(f == focusIn)
}

// notifications returns the commands that notify about a batch's new
// messages: only live, unread, recent messages from others, in chats that
// are not muted, and only while the terminal does not have focus. While
// focus is unknown, the open chat does not notify.
func (m *Model) notifications(b BatchMsg) tea.Cmd {
	if m.notifyFn == nil || m.termFocus == focusIn {
		return nil
	}
	now := m.now()
	var cmds []tea.Cmd
	for chat, msgs := range b.Appends {
		if m.chats.IsMuted(chat) || (m.termFocus == focusUnknown && chat == m.msgs.Chat()) {
			continue
		}
		var latest *domain.Message
		for i := range msgs {
			msg := &msgs[i]
			if msg.FromMe || !msg.Unread || msg.Kind == domain.KindSystem || now.Sub(msg.Timestamp) > notifyWindow {
				continue
			}
			if latest == nil || latest.Timestamp.Before(msg.Timestamp) {
				latest = msg
			}
		}
		if latest == nil {
			continue
		}
		title, _ := m.chats.Title(chat)
		n := Notification{Chat: chat, Title: title, Text: preview(latest)}
		if m.chats.IsGroup(chat) && m.names != nil {
			n.Sender = oneLine(m.names.DisplayName(latest.SenderID))
		}
		cmds = append(cmds, m.notifyFn(n))
	}
	return tea.Batch(cmds...)
}

// preview is a message's text for a notification: its body, or what it
// holds.
func preview(msg *domain.Message) string {
	text := oneLine(msg.Body)
	if text == "" {
		text = "[" + msg.Kind.String() + "]"
	}
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	return text
}

func oneLine(s string) string { return strings.Join(strings.Fields(sanitize.Line(s)), " ") }
