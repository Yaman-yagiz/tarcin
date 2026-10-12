// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
)

// mode is what the keyboard currently drives.
type mode uint8

const (
	modeNormal  mode = iota // panes, through the keymap
	modeCommand             // the ":" command line
	modeSearch              // the chat search ("/" or ctrl+k)
	modeHelp                // the help screen
	modeMembers             // a group's members
	modeReact               // the reaction picker
	modeConfirm             // a yes/no question in the status line
	modeFind                // message search results
	modeDebug               // the debug pane
)

func newInput() textinput.Model {
	in := textinput.New()
	in.SetVirtualCursor(false)
	in.CharLimit = 256
	s := in.Styles()
	s.Focused.Prompt = lipgloss.NewStyle()
	s.Focused.Text = lipgloss.NewStyle()
	s.Blurred.Prompt = lipgloss.NewStyle()
	s.Blurred.Text = lipgloss.NewStyle()
	s.Cursor.Color = nil // the terminal's own cursor color
	in.SetStyles(s)
	return in
}

// startInput opens the command line or the search.
func (m *Model) startInput(md mode) {
	m.mode = md
	m.keys.Reset()
	m.input.Reset()
	m.input.Prompt = ":"
	if md == modeSearch {
		m.input.Prompt = "/"
		m.setFocus(FocusChatList)
	}
	m.input.Focus()
}

func (m *Model) endInput() {
	m.choices = ""
	if m.mode == modeSearch {
		m.chats.SetFilter("")
	}
	if m.forwarding != nil {
		// Back to the message, with the open chat selected again.
		m.forwarding = nil
		if open := m.msgs.Chat(); open != 0 {
			m.chats.Select(open)
		}
		m.setFocus(FocusMessages)
	}
	m.mode = modeNormal
	m.input.Blur()
}

// handleInputKey drives the command line and the search.
func (m *Model) handleInputKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.endInput()
		return nil
	case "enter":
		if m.mode == modeSearch {
			return m.finishSearch()
		}
		line := m.input.Value()
		m.endInput()
		return m.execute(line)
	}
	if m.mode == modeCommand && msg.String() == "tab" {
		if cmd := completeAttach(m.input.Value()); cmd != nil {
			return cmd
		}
		return m.completeContact(m.input.Value())
	}
	m.choices = "" // the line changes: its completion no longer applies
	if m.mode == modeSearch {
		switch msg.String() {
		case "down", "ctrl+n":
			return m.chats.Update(keymap.ActionMsg{Action: keymap.Down})
		case "up", "ctrl+p":
			return m.chats.Update(keymap.ActionMsg{Action: keymap.Up})
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.mode == modeSearch {
		m.chats.SetFilter(m.input.Value())
	}
	return cmd
}

// finishSearch opens the selected match, or forwards to it, and clears
// the filter.
func (m *Model) finishSearch() tea.Cmd {
	id, ok := m.chats.Selected()
	fwd := m.forwarding
	m.endInput()
	switch {
	case !ok:
		return nil
	case fwd != nil:
		return m.forwardTo(fwd, id)
	}
	m.chats.Select(id)
	return m.open(id)
}

// execute runs a command-line command.
func (m *Model) execute(line string) tea.Cmd {
	name, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "":
		return nil
	case "q", "quit":
		return tea.Quit
	case "h", "help":
		m.mode = modeHelp
		m.helpOffset = 0
	case "archived":
		m.showArchived = !m.showArchived
		m.chats.SetShowArchived(m.showArchived)
		if m.showArchived {
			m.notice = "showing archived chats"
		} else {
			m.notice = "archived chats hidden"
		}
	case "reconnect":
		if m.reconnect == nil {
			m.notice = "there is no connection to restart"
			return nil
		}
		m.notice = "reconnecting…"
		cmd := m.reconnect()
		return cmd
	case "search":
		m.startInput(modeSearch)
		m.input.SetValue(arg)
		m.chats.SetFilter(arg)
	case "members":
		return m.openMembers()
	case "react":
		return m.reactCommand(arg)
	case "attach":
		return m.attachCommand(arg)
	case "debug":
		return m.debugCommand()
	case "delete-local":
		return m.deleteLocalCommand()
	case "mute", "unmute", "pin", "unpin", "archive", "unarchive":
		return m.settingCommand(name, arg)
	case "find":
		return m.findCommand(arg, false)
	case "findin":
		return m.findCommand(arg, true)
	case "new":
		return m.startChat(arg)
	case "open":
		if arg == "" {
			m.notice = "usage: :open NAME"
			return nil
		}
		m.chats.SetFilter(arg)
		id, ok := m.chats.Selected()
		m.chats.SetFilter("")
		if !ok {
			m.notice = "no chat matches " + arg
			return nil
		}
		m.chats.Select(id)
		return m.open(id)
	default:
		m.notice = "unknown command: " + name
	}
	return nil
}

// handleHelpKey scrolls or closes the help screen.
func (m *Model) handleHelpKey(msg tea.KeyPressMsg) tea.Cmd {
	page := max(m.layout.Height-2, 1)
	if slices.Contains(m.quitKeys, msg.String()) {
		return tea.Quit
	}
	switch msg.String() {
	case "esc", "q", "?", "enter":
		m.mode = modeNormal
	case "j", "down":
		m.helpOffset++
	case "k", "up":
		m.helpOffset--
	case "ctrl+d", "pgdown", "space":
		m.helpOffset += page / 2
	case "ctrl+u", "pgup":
		m.helpOffset -= page / 2
	case "g", "home":
		m.helpOffset = 0
	case "G", "end":
		m.helpOffset = len(m.help)
	}
	return nil
}
