// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"strings"

	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
)

// actionHelp describes every action for the help screen.
var actionHelp = map[keymap.Action]string{
	keymap.Quit:         "quit",
	keymap.Help:         "show this help",
	keymap.Command:      "command line",
	keymap.Search:       "search chats",
	keymap.FocusNext:    "next pane",
	keymap.FocusPrev:    "previous pane",
	keymap.NextUnread:   "next unread chat",
	keymap.PrevUnread:   "previous unread chat",
	keymap.Down:         "down",
	keymap.Up:           "up",
	keymap.Top:          "top",
	keymap.Bottom:       "bottom (newest)",
	keymap.HalfPageDown: "half a page down",
	keymap.HalfPageUp:   "half a page up",
	keymap.PageDown:     "page down",
	keymap.PageUp:       "page up",
	keymap.Open:         "open chat",
	keymap.ComposeMsg:   "write a message",
	keymap.Reply:        "reply",
	keymap.React:        "react",
	keymap.Forward:      "forward",
	keymap.Edit:         "edit your message",
	keymap.Delete:       "delete",
	keymap.Copy:         "copy text",
	keymap.OpenMedia:    "open media",
	keymap.SaveMedia:    "save media",
	keymap.JumpQuoted:   "jump to quoted message",
	keymap.OpenLink:     "open a link (again: the next)",
	keymap.ViewPager:    "read in $PAGER",
	keymap.Retry:        "send a failed message again",
	keymap.Send:         "send",
	keymap.Newline:      "new line",
	keymap.EditExternal: "edit in $EDITOR",
	keymap.NormalMode:   "leave the composer",
}

var contextHelp = map[keymap.Context]string{
	keymap.Global:     "Everywhere",
	keymap.Navigation: "Chat list and messages",
	keymap.ChatList:   "Chat list",
	keymap.Messages:   "Messages",
	keymap.Compose:    "Composer",
}

// commandHelp lists the command-line commands.
var commandHelp = [][2]string{
	{":quit, :q", "quit"},
	{":open NAME", "open the first chat whose name matches"},
	{":new NAME, :new +NUMBER", "start a chat with a contact (tab completes) or a phone number"},
	{":search TEXT", "filter the chat list (also / or ctrl+k)"},
	{":archived", "show or hide archived chats"},
	{":members", "show the members of the open group"},
	{":react EMOJI", "react to the selected message (without an emoji: pick one)"},
	{":attach PATH", "send a file to the open chat, the composer's text as caption (tab completes)"},
	{":mute [8h|1w|always]", "mute the chat (8 hours by default); :unmute"},
	{":pin, :unpin", "pin the chat to the top (at most three)"},
	{":archive, :unarchive", "archive the chat (:archived shows archived chats)"},
	{":delete-local", "delete the chat and its messages from this computer only"},
	{":debug", "show counters: queues, caches, frames, goroutines"},
	{":find WORDS", "search the messages of all chats"},
	{":findin WORDS", "search the messages of the open chat"},
	{":reconnect", "connect to WhatsApp again now"},
	{":help", "show this help"},
}

// helpLines builds the help text from the effective keymap, so changes
// made in the configuration show up here.
func helpLines(km *keymap.Keymap) []string {
	type entry struct {
		action keymap.Action
		keys   []string
	}
	byContext := map[keymap.Context][]entry{}
	for _, b := range km.Bindings() {
		list := byContext[b.Context]
		if n := len(list); n > 0 && list[n-1].action == b.Action {
			list[n-1].keys = append(list[n-1].keys, b.String())
		} else {
			list = append(list, entry{action: b.Action, keys: []string{b.String()}})
		}
		byContext[b.Context] = list
	}

	lines := []string{"tarcin keys — esc or q closes this screen", ""}
	row := func(keys, what string) string { return fmt.Sprintf("  %-22s %s", keys, what) }
	for _, c := range keymap.Contexts {
		if len(byContext[c]) == 0 {
			continue
		}
		lines = append(lines, contextHelp[c])
		for _, e := range byContext[c] {
			lines = append(lines, row(strings.Join(e.keys, ", "), actionHelp[e.action]))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Commands")
	for _, c := range commandHelp {
		lines = append(lines, row(c[0], c[1]))
	}
	return lines
}

// helpView renders the visible part of the help screen.
func (m *Model) helpView(h int) string {
	t := m.ctx.Theme
	m.helpOffset = min(max(m.helpOffset, 0), max(len(m.help)-h, 0))
	end := min(m.helpOffset+h, len(m.help))
	var b strings.Builder
	for i := m.helpOffset; i < end; i++ {
		line := m.help[i]
		switch {
		case i == 0:
			line = t.Accent.Bold(true).Render(line)
		case line != "" && !strings.HasPrefix(line, " "):
			line = t.Header.Render(line) // section title
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
