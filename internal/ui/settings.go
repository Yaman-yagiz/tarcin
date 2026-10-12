// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"cmp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ChatSetting is a change of a chat's settings: What is "mute",
// "unmute", "pin", "unpin", "archive" or "unarchive"; MuteFor is how long
// a mute lasts, zero for always.
type ChatSetting struct {
	What    string
	MuteFor time.Duration
}

// muteDurations are the mutes WhatsApp offers, by their :mute argument.
var muteDurations = map[string]time.Duration{"8h": 8 * time.Hour, "1w": 7 * 24 * time.Hour, "always": 0}

// settingCommand runs :mute [8h|1w|always], :unmute, :pin, :unpin,
// :archive and :unarchive on the chat selected in the list, or the open
// one when the list does not have focus.
func (m *Model) settingCommand(what, arg string) tea.Cmd {
	chat := m.msgs.Chat()
	if m.focus == FocusChatList || chat == 0 {
		chat, _ = m.chats.Selected()
	}
	switch {
	case m.settingFn == nil:
		m.notice = "chat settings are not available"
		return nil
	case chat == 0:
		m.notice = "select a chat first"
		return nil
	}
	s := ChatSetting{What: what}
	if what == "mute" {
		d, ok := muteDurations[cmp.Or(strings.TrimSpace(arg), "8h")]
		if !ok {
			var err error
			if d, err = time.ParseDuration(arg); err != nil || d <= 0 {
				m.notice = "usage: :mute [8h|1w|always]"
				return nil
			}
		}
		s.MuteFor = d
	}
	cmd := m.settingFn(chat, s)
	return cmd
}

// nextUnread selects and opens the next (dir 1) or previous (-1) chat
// with unread messages, from the selected one, in list order.
func (m *Model) nextUnread(dir int) tea.Cmd {
	from, _ := m.chats.Selected()
	id, ok := m.chats.NextUnread(from, dir)
	if !ok {
		m.notice = "no unread chats"
		return nil
	}
	m.chats.Select(id)
	return m.open(id)
}
