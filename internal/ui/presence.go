// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Online status and typing, sent only as far as the privacy settings let
// Options.Presence and Options.Typing exist. Like WhatsApp Web, tarcin is
// online only while the user is active: from a key press until two
// minutes without one or until the terminal loses focus. A terminal left
// open all day does not show the account online all day.
const (
	presenceIdle  = 2 * time.Minute
	presenceCheck = 30 * time.Second
	typingRenew   = 10 * time.Second // WhatsApp shows "typing" for about 25 s
	typingIdle    = 5 * time.Second
)

// presenceCheckMsg makes the model check whether the user went idle.
type presenceCheckMsg struct{}

// typingIdleMsg ends a typing state that was not renewed; gen tells a
// stale one from the current.
type typingIdleMsg struct{ gen int }

// active records a key press: an inactive user becomes online.
func (m *Model) active() tea.Cmd {
	m.lastKey = m.now()
	if m.presenceFn == nil || m.available {
		return nil
	}
	m.available = true
	return tea.Batch(m.presenceFn(true), tea.Tick(presenceCheck, func(time.Time) tea.Msg { return presenceCheckMsg{} }))
}

// checkPresence turns an idle user offline, or checks again later.
func (m *Model) checkPresence() tea.Cmd {
	if !m.available {
		return nil
	}
	if m.now().Sub(m.lastKey) >= presenceIdle {
		return m.goOffline()
	}
	return tea.Tick(presenceCheck, func(time.Time) tea.Msg { return presenceCheckMsg{} })
}

func (m *Model) goOffline() tea.Cmd {
	if !m.available {
		return nil
	}
	m.available = false
	return m.presenceFn(false)
}

// typed follows the composer's text: while it changes in an open chat,
// the chat sees this account typing, renewed every typingRenew, and the
// state ends after typingIdle without a change.
func (m *Model) typed(before string) tea.Cmd {
	chat := m.comp.Chat()
	if m.typingFn == nil || chat == 0 || m.comp.Value() == before {
		return nil
	}
	if m.comp.Value() == "" {
		return m.stopTyping()
	}
	var cmds []tea.Cmd
	if m.composing != chat || m.now().Sub(m.typingSent) >= typingRenew {
		if m.composing != 0 && m.composing != chat {
			cmds = append(cmds, m.typingFn(m.composing, false))
		}
		m.composing, m.typingSent = chat, m.now()
		cmds = append(cmds, m.typingFn(chat, true))
	}
	m.typingGen++
	gen := m.typingGen
	cmds = append(cmds, tea.Tick(typingIdle, func(time.Time) tea.Msg { return typingIdleMsg{gen: gen} }))
	return tea.Batch(cmds...)
}

// stopTyping ends this account's typing state, if one is on.
func (m *Model) stopTyping() tea.Cmd {
	if m.composing == 0 || m.typingFn == nil {
		return nil
	}
	chat := m.composing
	m.composing = 0
	m.typingGen++
	return m.typingFn(chat, false)
}
