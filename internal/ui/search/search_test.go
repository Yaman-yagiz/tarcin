// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

type source struct {
	msgs  []domain.Message
	err   error
	query string
	chat  domain.ChatID
}

func (s *source) Search(_ context.Context, q string, chat domain.ChatID, _ int) ([]domain.Message, error) {
	s.query, s.chat = q, chat
	return s.msgs, s.err
}

func run(m *Model, cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if r, ok := msg.(ResultsMsg); ok {
		m.Update(r)
	}
	return msg
}

func TestSearchView(t *testing.T) {
	at := time.Date(2026, 10, 11, 14, 2, 0, 0, time.UTC)
	src := &source{msgs: []domain.Message{
		{ChatID: 1, WAID: "A", SenderID: 7, Body: strings.Repeat("önce ", 20) + "Toplantı yarın", Timestamp: at},
		{ChatID: 2, WAID: "B", FromMe: true, Body: "toplantı\u202e iptal", Timestamp: at},
	}}
	titles := func(id domain.ChatID) string { return map[domain.ChatID]string{1: "Aile", 2: "İş"}[id] }
	m := New(uikit.NewContext(theme.Default()), src, msglist.NamesFunc(func(domain.IdentityID) string { return "Ali" }), titles)
	m.SetLocation(time.UTC)
	m.Resize(100, 10)
	run(m, m.Open("toplantı", 0))
	v := ansi.Strip(m.View())
	for _, want := range []string{"Search “toplantı” in all chats", "2 results", "Aile · Ali  11 Oct 14:02  …", "Toplantı yarın", "İş · You", "toplantı iptal"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "\u202e") || src.chat != 0 {
		t.Fatal("results are sanitized; the search is global")
	}
	m.Update(keymap.ActionMsg{Action: keymap.Down})
	open, ok := run(m, m.Update(keymap.ActionMsg{Action: keymap.Open})).(OpenMsg)
	if !ok || open.Chat != 2 || open.WAID != "B" {
		t.Fatalf("open = %+v", open)
	}
	run(m, m.Open("yok", 2))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "in İş") || src.chat != 2 {
		t.Fatalf("a chat's search:\n%s", v)
	}
	src.msgs, src.err = nil, errors.New("database is locked")
	run(m, m.Open("x", 0))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "The search failed: database is locked") {
		t.Fatalf("error:\n%s", v)
	}
	if run(m, m.Update(keymap.ActionMsg{Action: keymap.Open})) != nil {
		t.Fatal("nothing to open without results")
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet("kısa toplantı", "toplantı", 30); got != "kısa toplantı" {
		t.Fatalf("a near match keeps the start: %q", got)
	}
	long := strings.Repeat("x", 50) + " match"
	if got := snippet(long, "MATCH", 10); !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "match") {
		t.Fatalf("snippet = %q", got)
	}
	if got := snippet("abc", "", 1); got != "abc" {
		t.Fatalf("snippet = %q", got)
	}
}

// TestResultTextIsBounded: a huge body costs one bounded pass when the
// results arrive, not one per frame, and its row text stays short.
func TestResultTextIsBounded(t *testing.T) {
	body := "kahve " + strings.Repeat("çok uzun bir mesaj ", 100_000)
	got := resultText(body, "kahve")
	if n := len([]rune(got)); n == 0 || n > resultRunes || !strings.HasPrefix(got, "kahve") {
		t.Fatalf("result text: %d runes, starts %q", n, got[:min(len(got), 20)])
	}
	got = resultText(strings.Repeat("x ", 40)+"kahve\x1b]0;title\x07", "kahve")
	if !strings.HasPrefix(got, "…") || !strings.Contains(got, "kahve") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("result text = %q", got)
	}
}
