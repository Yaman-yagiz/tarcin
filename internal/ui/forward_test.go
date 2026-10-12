// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

func TestForwardFlow(t *testing.T) {
	type forward struct {
		waid domain.WAMessageID
		to   domain.ChatID
	}
	var got []forward
	m := New(Options{History: emptyHistory{}, Forward: func(msg domain.Message, to domain.ChatID) tea.Cmd {
		got = append(got, forward{msg.WAID, to})
		return nil
	}})
	size(m, 120, 30)
	now := time.Now()
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "Aile", LastActivity: now},
		{ID: 2, Title: "Ayşe Yılmaz", LastActivity: now.Add(-time.Minute)},
	}})
	_, open := m.Update(key("enter"))
	m.Update(open())
	target := domain.Message{ID: 1, ChatID: 1, WAID: "W1", SenderID: 7, Kind: domain.KindText, Body: "hi"}

	m.Update(msglist.MessageActionMsg{Action: keymap.Forward, Message: target})
	if st := statusOf(m); m.mode != modeSearch || !strings.Contains(st, "FORWARD") || !strings.Contains(st, "forward to /") {
		t.Fatalf("mode %v, status %q; f asks for a chat", m.mode, st)
	}
	typeKeys(m, "yilmaz")
	m.Update(key("enter"))
	if want := []forward{{"W1", 2}}; !slices.Equal(got, want) {
		t.Fatalf("forwarded %v, want %v", got, want)
	}
	if m.mode != modeNormal || m.Focus() != FocusMessages || m.msgs.Chat() != 1 {
		t.Fatalf("mode %v, focus %v, chat %d; forwarding keeps the open chat", m.mode, m.Focus(), m.msgs.Chat())
	}
	if id, _ := m.chats.Selected(); id != 1 || m.chats.Len() != 2 {
		t.Fatalf("selected %d of %d chats; the filter goes and the open chat stays selected", id, m.chats.Len())
	}
	if st := statusOf(m); !strings.Contains(st, "Forwarding to Ayşe Yılmaz") {
		t.Fatalf("status %q", st)
	}

	m.Update(msglist.MessageActionMsg{Action: keymap.Forward, Message: target})
	m.Update(key("esc"))
	if len(got) != 1 || m.mode != modeNormal || m.Focus() != FocusMessages {
		t.Fatalf("esc cancels: %v, mode %v, focus %v", got, m.mode, m.Focus())
	}

	for _, msg := range []domain.Message{
		{ID: 2, ChatID: 1, WAID: "W2", Kind: domain.KindText, RevokedAt: now},
		{ID: 3, ChatID: 1, WAID: "W3", Kind: domain.KindImage, Flags: domain.FlagViewOnce},
		{ID: 4, ChatID: 1, WAID: "W4", Kind: domain.KindPoll},
		{ID: 5, ChatID: 1, WAID: "W5", Kind: domain.KindText, FromMe: true, Status: domain.StatusFailed},
	} {
		m.Update(msglist.MessageActionMsg{Action: keymap.Forward, Message: msg})
		if m.mode != modeNormal || !strings.Contains(statusOf(m), "forwarded") {
			t.Fatalf("%s: mode %v, status %q; it cannot be forwarded", msg.WAID, m.mode, statusOf(m))
		}
	}

	off := New(Options{History: emptyHistory{}})
	size(off, 120, 30)
	off.Update(msglist.MessageActionMsg{Action: keymap.Forward, Message: target})
	if off.mode != modeNormal || !strings.Contains(statusOf(off), "not available") {
		t.Fatalf("status %q", statusOf(off))
	}
}
