// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/textfold"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
)

// ChatStartedMsg reports the chat that ":new" found or created. Its row
// may be new to the chat list.
type ChatStartedMsg struct {
	Row chatlist.Row
}

// ContactsMsg lists the names of the contacts that match Query, for
// completing ":new".
type ContactsMsg struct {
	Query string
	Names []string // raw: sanitized here
}

// startChat runs ":new": a contact's name or a phone number.
func (m *Model) startChat(arg string) tea.Cmd {
	switch {
	case m.startChatFn == nil:
		m.notice = "starting chats is not available here"
		return nil
	case arg == "":
		m.notice = "usage: :new NAME or :new +NUMBER"
		return nil
	}
	m.notice = "Starting the chat…"
	return m.startChatFn(arg)
}

// chatStarted shows the chat ":new" found or created.
func (m *Model) chatStarted(msg ChatStartedMsg) tea.Cmd {
	m.notice = ""
	m.chats.Upsert(msg.Row)
	m.chats.Select(msg.Row.ID)
	return m.open(msg.Row.ID)
}

// completeContact returns the command that completes the name of a
// ":new" command line, or nil for any other line.
func (m *Model) completeContact(input string) tea.Cmd {
	arg, ok := strings.CutPrefix(input, "new ")
	if !ok || m.contactsFn == nil {
		return nil
	}
	return m.contactsFn(strings.TrimLeft(arg, " "))
}

// completedContacts applies a name completion, if the command line is
// unchanged: one match is completed, several to the start they share,
// and the matches are listed.
func (m *Model) completedContacts(msg ContactsMsg) {
	input := "new " + msg.Query
	if m.mode != modeCommand || strings.TrimRight(m.input.Value(), " ") != strings.TrimRight(input, " ") {
		return
	}
	names := make([]string, 0, len(msg.Names))
	for _, n := range msg.Names {
		if n = oneLine(n); n != "" {
			names = append(names, n)
		}
	}
	m.choices = ""
	if len(names) == 0 {
		m.choices = "no contact matches"
		return
	}
	// Prefer names that start with what was typed; a name may match in
	// its middle too, as in a surname.
	key := textfold.Fold(msg.Query)
	starting := names[:0:0]
	for _, n := range names {
		if strings.HasPrefix(textfold.Fold(n), key) {
			starting = append(starting, n)
		}
	}
	if len(starting) > 0 {
		names = starting
	}
	completed := names[0]
	for _, n := range names[1:] {
		completed = commonPrefix(completed, n)
	}
	if textfold.Fold(completed) == key || len([]rune(completed)) < len([]rune(msg.Query)) {
		completed = msg.Query // nothing to add, or the names start differently
	}
	m.input.SetValue("new " + completed)
	m.input.CursorEnd()
	if len(names) > 1 {
		shown := names[:min(len(names), maxChoices)]
		m.choices = strings.Join(shown, "  ")
		if len(names) > maxChoices {
			m.choices += "  …"
		}
	}
}

// commonPrefix returns the longest start a and b share, in whole runes.
func commonPrefix(a, b string) string {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return string(ra[:n])
}
