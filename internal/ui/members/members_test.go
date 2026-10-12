// SPDX-License-Identifier: GPL-3.0-or-later

package members

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

type source struct {
	rows  []domain.GroupMemberRow
	meta  domain.GroupMeta
	reads atomic.Int32
	fail  error
}

func (s *source) GroupMeta(context.Context, domain.ChatID) (domain.GroupMeta, bool, error) {
	return s.meta, true, nil
}

func (s *source) GroupMemberCount(context.Context, domain.ChatID) (int, error) {
	return len(s.rows), nil
}

func (s *source) GroupMembers(_ context.Context, _ domain.ChatID, offset, limit int) ([]domain.GroupMemberRow, error) {
	s.reads.Add(1)
	if s.fail != nil {
		return nil, s.fail
	}
	end := min(offset+limit, len(s.rows))
	if offset >= end {
		return nil, nil
	}
	return s.rows[offset:end], nil
}

func group(n int) *source {
	s := &source{meta: domain.GroupMeta{Topic: "Weekend\nplans", AnnounceOnly: true}}
	for i := range n {
		role := domain.RoleMember
		if i == 0 {
			role = domain.RoleSuperAdmin
		}
		s.rows = append(s.rows, domain.GroupMemberRow{Identity: domain.IdentityID(i + 1), Role: role})
	}
	return s
}

var names = msglist.NamesFunc(func(id domain.IdentityID) string {
	if id == 2 {
		return "Ali\u202e evil" // a bidi override must not reach the terminal
	}
	return fmt.Sprintf("Member %d", id)
})

// run executes cmd and feeds every message it produces back to m.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case nil:
	default:
		run(m, m.Update(msg))
	}
}

func view(m *Model) string { return ansi.Strip(m.View()) }

func TestOpenShowsTheFirstPage(t *testing.T) {
	src := group(450)
	m := New(uikit.NewContext(theme.Default()), src, names)
	m.Resize(60, 13) // 10 rows below the header
	run(m, m.Open(7, "Aile"))
	v := view(m)
	for _, want := range []string{"Aile · 450 members", "Weekend plans · only admins send", "Member 1  owner", "Member 10"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "\u202e") || !strings.Contains(v, "Ali") {
		t.Fatalf("names must be sanitized:\n%q", v)
	}
	if strings.Contains(v, "Member 11") || len(m.rows) != PageSize || m.Chat() != 7 {
		t.Fatalf("one page loaded, ten rows shown: %d rows\n%s", len(m.rows), v)
	}
}

func TestScrollingLoadsPages(t *testing.T) {
	src := group(450)
	m := New(uikit.NewContext(theme.Default()), src, names)
	m.Resize(60, 13)
	run(m, m.Open(7, "Aile"))
	for range 19 {
		run(m, m.Update(keymap.ActionMsg{Action: keymap.PageDown}))
	}
	if len(m.rows) != 400 {
		t.Fatalf("near the end of a page, the next one loads: %d rows", len(m.rows))
	}
	run(m, m.Update(keymap.ActionMsg{Action: keymap.Bottom}))
	if len(m.rows) != 450 || !m.end || m.sel != 449 || !strings.Contains(view(m), "Member 450") {
		t.Fatalf("bottom loads to the end: %d rows, sel %d", len(m.rows), m.sel)
	}
	run(m, m.Update(keymap.ActionMsg{Action: keymap.Top}))
	if m.sel != 0 || !strings.Contains(view(m), "Member 1  owner") {
		t.Fatal("top returns to the first member")
	}
	reads := src.reads.Load()
	for range 5 {
		run(m, m.Update(keymap.ActionMsg{Action: keymap.Down}))
	}
	if src.reads.Load() != reads {
		t.Fatal("once everything is loaded, moving reads nothing")
	}
}

func TestBottomOfAGroupWithoutACount(t *testing.T) {
	src := group(450)
	m := New(uikit.NewContext(theme.Default()), src, names)
	m.Resize(60, 13)
	run(m, m.Open(7, "Aile"))
	m.count = 0 // the count is unknown: the end shows by a short page
	m.Update(keymap.ActionMsg{Action: keymap.Bottom})
	if !m.wantBottom {
		t.Fatal("bottom before the end keeps loading")
	}
}

func TestReloadKeepsTheSelectionAndDropsStalePages(t *testing.T) {
	src := group(450)
	m := New(uikit.NewContext(theme.Default()), src, names)
	m.Resize(60, 13)
	run(m, m.Open(7, "Aile"))
	for range 25 {
		m.Update(keymap.ActionMsg{Action: keymap.Down})
	}
	stale := m.load() // a page asked for before the reload
	src.rows = src.rows[:300]
	run(m, m.Reload())
	if m.sel != 25 || len(m.rows) != PageSize {
		t.Fatalf("after a reload: sel %d, %d rows", m.sel, len(m.rows))
	}
	if stale != nil {
		run(m, stale)
	}
	if len(m.rows) != PageSize {
		t.Fatalf("a page from before the reload must be dropped: %d rows", len(m.rows))
	}
	run(m, m.Open(8, "Other"))
	if m.sel != 0 || m.Chat() != 8 {
		t.Fatal("opening another group starts over")
	}
	if (&Model{}).Reload() != nil {
		t.Fatal("nothing to reload without a group")
	}
}

func TestErrorsAndEmptyGroups(t *testing.T) {
	src := group(0)
	m := New(uikit.NewContext(theme.Plain()), src, names)
	m.Resize(60, 13)
	run(m, m.Open(7, "Empty"))
	if v := view(m); !strings.Contains(v, "No members are known yet") {
		t.Fatalf("empty group:\n%s", v)
	}
	src.fail = errors.New("database is locked")
	run(m, m.Open(7, "Empty"))
	if v := view(m); !strings.Contains(v, "Could not read the members: database is locked") {
		t.Fatalf("error:\n%s", v)
	}
	m.Resize(0, 0)
	if m.View() != "" {
		t.Fatal("no size, no view")
	}
}
