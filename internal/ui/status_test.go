// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

func statusOf(m *Model) string {
	lines := strings.Split(m.View().Content, "\n")
	return ansi.Strip(lines[len(lines)-1])
}

func typeKeys(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func modelWithChats(t *testing.T) *Model {
	t.Helper()
	m := New(Options{History: emptyHistory{}})
	size(m, 100, 30)
	now := time.Now()
	archived := chatlist.Row{ID: 3, Title: "Eski grup", LastActivity: now.Add(-time.Hour), Archived: true}
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "Aile", LastActivity: now},
		{ID: 2, Title: "Ayşe Yılmaz", LastActivity: now.Add(-time.Minute)},
		archived,
	}})
	return m
}

func TestStatusLine(t *testing.T) {
	m := modelWithChats(t)
	st := statusOf(m)
	for _, want := range []string{"NORMAL", "connecting", "? help"} {
		if !strings.Contains(st, want) {
			t.Errorf("status %q lacks %q", st, want)
		}
	}
	if w := m.ctx.Width.StringWidth(strings.Split(m.View().Content, "\n")[29]); w != 100 {
		t.Fatalf("status line is %d cells wide", w)
	}
	m.Update(BatchMsg{Connection: &domain.ConnectionChanged{State: domain.ConnDisconnected, Reason: "no network"}})
	if st := statusOf(m); !strings.Contains(st, "offline: no network") {
		t.Fatalf("status %q lacks the connection state", st)
	}

	_, open := m.Update(key("enter")) // open "Aile"
	m.Update(open())
	m.Update(key("g"))
	if st := statusOf(m); !strings.Contains(st, "Aile") || !strings.Contains(st, "g…") {
		t.Fatalf("status %q lacks the chat or the pending key", st)
	}
	m.Update(key("g"))
	m.Update(key("i"))
	if st := statusOf(m); !strings.Contains(st, "INSERT") {
		t.Fatalf("status %q is not in insert mode", st)
	}

	m.Update(composer.ErrorMsg{Err: errorString("set $EDITOR")})
	if st := statusOf(m); !strings.Contains(st, "! set $EDITOR") {
		t.Fatalf("status %q lacks the notice", st)
	}
	m.Update(key("x"))
	if strings.Contains(statusOf(m), "set $EDITOR") {
		t.Fatal("a notice must clear on the next key")
	}
	m.Update(key("esc"))
	m.Update(msglist.MessageActionMsg{Action: keymap.Action("frobnicate")})
	if st := statusOf(m); !strings.Contains(st, "frobnicate is not available yet") {
		t.Fatalf("status %q lacks the not-available notice", st)
	}
	size(m, 20, 5) // narrow status lines are truncated, never wrapped
	checkFrame(t, m.View().Content, m.ctx.Width, 20, 5)
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestHelpScreen(t *testing.T) {
	km, err := keymap.New(map[string]map[string][]string{"global": {"quit": {"ctrl+w"}}})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{History: emptyHistory{}, Keymap: km})
	size(m, 90, 200) // tall enough for the whole help
	m.Update(key("?"))
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"tarcin keys", "Everywhere", "ctrl+w", "quit", "g g, home", "top", "Composer", "ctrl+x ctrl+e", ":open NAME", "HELP"} {
		if !strings.Contains(view, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if strings.Contains(view, "ctrl+q") {
		t.Error("the help must show the configured keys, not the defaults")
	}
	size(m, 90, 20)
	checkFrame(t, m.View().Content, m.ctx.Width, 90, 20)
	m.Update(key("j"))
	m.Update(key("j"))
	if m.helpOffset != 2 {
		t.Fatalf("help offset = %d after two j", m.helpOffset)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m.View()
	if m.helpOffset != len(m.help)-19 {
		t.Fatalf("end did not scroll to the bottom: %d", m.helpOffset)
	}
	m.Update(key("esc"))
	if m.mode != modeNormal || !strings.Contains(statusOf(m), "NORMAL") {
		t.Fatal("esc must close the help")
	}
}

func TestCommandLine(t *testing.T) {
	m := modelWithChats(t)
	m.Update(key(":"))
	if !strings.Contains(statusOf(m), "COMMAND") || !strings.Contains(statusOf(m), ":") {
		t.Fatalf("status %q does not show the command line", statusOf(m))
	}
	typeKeys(m, "open ayse")
	if v := m.View(); v.Cursor == nil || v.Cursor.Y != 29 {
		t.Fatalf("the cursor must be on the command line, got %+v", v.Cursor)
	}
	_, cmd := m.Update(key("enter"))
	if cmd == nil || m.header != "Ayşe Yılmaz" || m.Focus() != FocusMessages {
		t.Fatalf(":open ayse opened %q (focus %v)", m.header, m.Focus())
	}

	m.Update(key(":"))
	typeKeys(m, "archived")
	m.Update(key("enter"))
	if m.chats.Len() != 3 || !strings.Contains(statusOf(m), "showing archived chats") {
		t.Fatalf(":archived: %d chats shown, status %q", m.chats.Len(), statusOf(m))
	}

	m.Update(key(":"))
	typeKeys(m, "fly away")
	m.Update(key("enter"))
	if !strings.Contains(statusOf(m), "unknown command: fly") {
		t.Fatalf("status %q", statusOf(m))
	}

	m.Update(key(":"))
	typeKeys(m, "open nobody")
	m.Update(key("enter"))
	if !strings.Contains(statusOf(m), "no chat matches nobody") {
		t.Fatalf("status %q", statusOf(m))
	}

	m.Update(key(":"))
	typeKeys(m, "help")
	m.Update(key("enter"))
	if m.mode != modeHelp {
		t.Fatal(":help must open the help")
	}
	m.Update(key("q"))

	m.Update(key(":"))
	typeKeys(m, "quit")
	m.Update(key("esc"))
	if m.mode != modeNormal {
		t.Fatal("esc must cancel the command line")
	}
	m.Update(key(":"))
	typeKeys(m, "q")
	if _, cmd := m.Update(key("enter")); cmd == nil {
		t.Fatal(":q did not quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal(":q must quit")
	}
}

func TestSearch(t *testing.T) {
	m := modelWithChats(t)
	m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if m.mode != modeSearch || !strings.Contains(statusOf(m), "SEARCH") {
		t.Fatal("ctrl+k must start the search")
	}
	typeKeys(m, "a")
	if m.chats.Len() != 2 {
		t.Fatalf("filter 'a' shows %d chats, want 2", m.chats.Len())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if id, _ := m.chats.Selected(); id != 2 {
		t.Fatalf("down selected %d, want 2", id)
	}
	m.Update(key("esc"))
	if m.chats.Len() != 2 || m.mode != modeNormal { // archived stays hidden
		t.Fatalf("esc must clear the filter: %d chats", m.chats.Len())
	}

	m.Update(key("/"))
	typeKeys(m, "yilmaz")
	if m.chats.Len() != 1 {
		t.Fatalf("filter 'yilmaz' shows %d chats", m.chats.Len())
	}
	_, cmd := m.Update(key("enter"))
	if cmd == nil || m.header != "Ayşe Yılmaz" || m.chats.Len() != 2 {
		t.Fatalf("enter opened %q; %d chats shown afterwards", m.header, m.chats.Len())
	}

	m.Update(key("/"))
	typeKeys(m, "zzz")
	if _, cmd := m.Update(key("enter")); cmd != nil {
		t.Fatal("enter without a match must do nothing")
	}

	m.Update(key(":"))
	typeKeys(m, "search aile")
	m.Update(key("enter"))
	if m.mode != modeSearch || m.chats.Len() != 1 {
		t.Fatalf(":search must start a filtered search (mode %v, %d chats)", m.mode, m.chats.Len())
	}
}

func TestConnectionStatesAndReconnect(t *testing.T) {
	reconnects := 0
	m := New(Options{History: emptyHistory{}, Reconnect: func() tea.Cmd { reconnects++; return nil }})
	size(m, 140, 30)
	until := time.Date(2026, 10, 12, 9, 30, 0, 0, time.Local)
	m.Update(BatchMsg{Connection: &domain.ConnectionChanged{State: domain.ConnBanned, Reason: "too many messages to strangers", Until: until}})
	line := ansi.Strip(m.View().Content)
	if !strings.Contains(line, "temporarily banned: too many messages to strangers (until 12 Oct 09:30)") {
		t.Fatalf("status line:\n%s", line)
	}
	m.Update(BatchMsg{Connection: &domain.ConnectionChanged{State: domain.ConnStreamReplace, Reason: "another session took over"}})
	if !strings.Contains(ansi.Strip(m.View().Content), "replaced by another client: another session took over") {
		t.Fatal("a replaced session must say so")
	}
	m.Update(key(":"))
	for _, r := range "reconnect" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if reconnects != 1 || !strings.Contains(ansi.Strip(m.View().Content), "reconnecting") {
		t.Fatalf("reconnects = %d", reconnects)
	}
	plain := New(Options{History: emptyHistory{}})
	size(plain, 140, 30)
	plain.Update(key(":"))
	for _, r := range "reconnect" {
		plain.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	plain.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(plain.View().Content), "no connection to restart") {
		t.Fatal("without a backend that reconnects, :reconnect says so")
	}
}

type memberSource struct{ reads int }

func (s *memberSource) GroupMeta(context.Context, domain.ChatID) (domain.GroupMeta, bool, error) {
	return domain.GroupMeta{Topic: "Bayram"}, true, nil
}

func (s *memberSource) GroupMemberCount(context.Context, domain.ChatID) (int, error) { return 2, nil }

func (s *memberSource) GroupMembers(context.Context, domain.ChatID, int, int) ([]domain.GroupMemberRow, error) {
	s.reads++
	return []domain.GroupMemberRow{{Identity: 1, Role: domain.RoleSuperAdmin}, {Identity: 2}}, nil
}

// runCmd executes cmd and feeds what it returns back to m.
func runCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runCmd(m, c)
		}
	case nil:
	default:
		_, next := m.Update(msg)
		runCmd(m, next)
	}
}

func TestMembersView(t *testing.T) {
	src := &memberSource{}
	m := New(Options{History: emptyHistory{}, Members: src,
		Names: msglist.NamesFunc(func(id domain.IdentityID) string { return [...]string{"", "Ali", "Ayşe"}[id] })})
	size(m, 100, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "Aile", LastActivity: time.Now(), Group: true},
		{ID: 2, Title: "Ayşe Yılmaz", LastActivity: time.Now().Add(-time.Minute)},
	}})
	command := func(line string) tea.Cmd {
		m.Update(key(":"))
		typeKeys(m, line)
		_, cmd := m.Update(key("enter"))
		return cmd
	}
	runCmd(m, command("members"))
	if !strings.Contains(statusOf(m), "open a group to see its members") {
		t.Fatalf("no chat open: %q", statusOf(m))
	}
	runCmd(m, command("open ayse"))
	runCmd(m, command("members"))
	if !strings.Contains(statusOf(m), "open a group") {
		t.Fatalf("a direct chat has no members view: %q", statusOf(m))
	}
	runCmd(m, command("open aile"))
	runCmd(m, command("members"))
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Aile · 2 members", "Bayram", "Ali  owner", "Ayşe", "MEMBERS"} {
		if !strings.Contains(view, want) {
			t.Fatalf("members view lacks %q:\n%s", want, view)
		}
	}
	m.Update(key("j"))
	if _, cmd := m.Update(BatchMsg{Groups: []domain.ChatID{1}}); cmd == nil {
		t.Fatal("a change of the group shown reloads it")
	} else {
		runCmd(m, cmd)
	}
	if src.reads != 2 {
		t.Fatalf("reads = %d", src.reads)
	}
	m.Update(key("esc"))
	if m.mode != modeNormal || strings.Contains(ansi.Strip(m.View().Content), "2 members") {
		t.Fatal("esc closes the members view")
	}
	runCmd(m, command("members"))
	m.Update(BatchMsg{Removed: []domain.ChatID{1}})
	if m.mode != modeNormal {
		t.Fatal("a removed group closes its members view")
	}
	if _, cmd := New(Options{History: emptyHistory{}}).Update(BatchMsg{Groups: []domain.ChatID{1}}); cmd != nil {
		t.Fatal("without a members view, group changes do nothing")
	}
	plain := New(Options{History: emptyHistory{}})
	size(plain, 100, 30)
	plain.Update(key(":"))
	typeKeys(plain, "members")
	plain.Update(key("enter"))
	if !strings.Contains(statusOf(plain), "not available") {
		t.Fatalf("status %q", statusOf(plain))
	}
}
