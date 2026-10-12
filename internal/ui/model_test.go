// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"context"
	"fmt"
	"slices"
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
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

func TestLayoutWide(t *testing.T) {
	l := computeLayout(120, 40, 3, true)
	if l.Narrow || l.TooSmall || !l.ShowList || !l.ShowChat {
		t.Fatalf("unexpected mode: %+v", l)
	}
	want := Layout{
		Width: 120, Height: 40, ShowList: true, ShowChat: true,
		List:     Rect{X: 0, Y: 0, W: 36, H: 39},
		Header:   Rect{X: 37, Y: 0, W: 83, H: 1},
		Messages: Rect{X: 37, Y: 1, W: 83, H: 34},
		Composer: Rect{X: 37, Y: 36, W: 83, H: 3},
		Status:   Rect{X: 0, Y: 39, W: 120, H: 1},
	}
	if l != want {
		t.Fatalf("layout =\n%+v\nwant\n%+v", l, want)
	}
}

func TestLayoutNarrowAndTiny(t *testing.T) {
	list := computeLayout(60, 20, 1, true)
	if !list.Narrow || !list.ShowList || list.ShowChat || list.List.W != 60 {
		t.Fatalf("narrow list screen: %+v", list)
	}
	chat := computeLayout(60, 20, 1, false)
	if !chat.Narrow || chat.ShowList || !chat.ShowChat || chat.Messages.W != 60 || chat.Header.X != 0 {
		t.Fatalf("narrow chat screen: %+v", chat)
	}
	for _, sz := range [][2]int{{0, 0}, {1, 1}, {19, 30}, {100, 4}} {
		if l := computeLayout(sz[0], sz[1], 1, true); !l.TooSmall {
			t.Errorf("%dx%d should be too small: %+v", sz[0], sz[1], l)
		}
	}
	if l := computeLayout(-5, -5, 1, true); l.Width != 0 || l.Height != 0 {
		t.Errorf("negative sizes must clamp to zero: %+v", l)
	}
}

// TestLayoutInvariants checks every size up to 130x50: regions stay in
// bounds, do not overlap, and the rows and columns add up.
func TestLayoutInvariants(t *testing.T) {
	for w := MinWidth; w <= 130; w++ {
		for h := MinHeight; h <= 50; h++ {
			for _, c := range []int{0, 1, 4, 99} {
				for _, listScreen := range []bool{true, false} {
					l := computeLayout(w, h, c, listScreen)
					checkLayout(t, l)
				}
			}
		}
	}
}

func checkLayout(t *testing.T, l Layout) {
	t.Helper()
	regions := []Rect{l.List, l.Header, l.Messages, l.Composer, l.Status}
	for _, r := range regions {
		if r.Empty() {
			continue
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > l.Width || r.Y+r.H > l.Height {
			t.Fatalf("%dx%d: region %+v out of bounds", l.Width, l.Height, r)
		}
	}
	for i, a := range regions {
		for _, b := range regions[i+1:] {
			if !a.Empty() && !b.Empty() && a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Fatalf("%dx%d: regions %+v and %+v overlap", l.Width, l.Height, a, b)
			}
		}
	}
	body := l.Height - 1
	if l.ShowChat {
		if got := l.Header.H + l.Messages.H + 1 + l.Composer.H; got != body {
			t.Fatalf("%dx%d: chat rows add up to %d, want %d", l.Width, l.Height, got, body)
		}
		if l.Messages.H < 1 || l.Composer.H < 1 || l.Composer.H > MaxComposerHeight {
			t.Fatalf("%dx%d: messages %d, composer %d rows", l.Width, l.Height, l.Messages.H, l.Composer.H)
		}
	}
	if l.ShowList && l.ShowChat && l.List.W+1+l.Header.W != l.Width {
		t.Fatalf("%dx%d: columns add up to %d", l.Width, l.Height, l.List.W+1+l.Header.W)
	}
	if l.Narrow == l.ShowList && l.ShowChat {
		t.Fatalf("%dx%d: narrow mode shows both panes", l.Width, l.Height)
	}
}

// checkFrame asserts the frame has exactly h lines of exactly w cells.
func checkFrame(t *testing.T, frame string, method ansi.Method, w, h int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Fatalf("%dx%d: frame has %d lines", w, h, len(lines))
	}
	for i, line := range lines {
		if got := method.StringWidth(line); got != w {
			t.Fatalf("%dx%d: line %d is %d cells wide: %q", w, h, i, got, ansi.Strip(line))
		}
	}
}

func size(m *Model, w, h int) { m.Update(tea.WindowSizeMsg{Width: w, Height: h}) }

func TestViewFillsTheScreen(t *testing.T) {
	for _, th := range []*theme.Theme{theme.Default(), theme.Plain()} {
		m := New(Options{Theme: th, History: emptyHistory{}})
		for _, sz := range [][2]int{{120, 40}, {80, 24}, {79, 24}, {200, 60}, {20, 5}, {19, 4}, {3, 1}} {
			size(m, sz[0], sz[1])
			checkFrame(t, m.View().Content, m.ctx.Width, sz[0], sz[1])
		}
	}
	m := New(Options{History: emptyHistory{}})
	if m.View().Content != "" {
		t.Fatal("before the first WindowSizeMsg the view must be empty")
	}
	if v := m.View(); !v.AltScreen || !v.ReportFocus {
		t.Fatal("the view must use the alternate screen and report focus")
	}
}

func TestViewWithWideCharacters(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	m.list = newPlaceholder(m.ctx, strings.Repeat("田中👨\u200d👩\u200d👧", 20))
	m.header = "Aile 🇹🇷 " + strings.Repeat("ş", 300)
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		m.ctx.Width = method
		for w := 80; w <= 90; w++ {
			size(m, w, 10)
			checkFrame(t, m.View().Content, method, w, 10)
		}
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+q":
		return tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// emptyHistory is a History without messages.
type emptyHistory struct{}

func (emptyHistory) Latest(context.Context, domain.ChatID, int) ([]domain.Message, error) {
	return nil, nil
}

func (emptyHistory) Before(context.Context, domain.ChatID, domain.Cursor, int) ([]domain.Message, error) {
	return nil, nil
}

func (emptyHistory) After(context.Context, domain.ChatID, domain.Cursor, int) ([]domain.Message, error) {
	return nil, nil
}

// recorder is a pane that records the messages routed to it.
type recorder struct {
	placeholder
	msgs []tea.Msg
}

func (r *recorder) Update(msg tea.Msg) tea.Cmd {
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *recorder) last() tea.Msg {
	if len(r.msgs) == 0 {
		return nil
	}
	return r.msgs[len(r.msgs)-1]
}

func recordingModel(t *testing.T) (*Model, *recorder, *recorder, *recorder) {
	t.Helper()
	m := New(Options{History: emptyHistory{}})
	list := &recorder{placeholder: placeholder{ctx: m.ctx, title: "Chats"}}
	msgs := &recorder{placeholder: placeholder{ctx: m.ctx, title: "Messages"}}
	comp := &recorder{placeholder: placeholder{ctx: m.ctx, title: "Compose"}}
	m.list, m.messages, m.composer = list, msgs, comp
	m.applyFocus()
	size(m, 120, 40)
	return m, list, msgs, comp
}

func TestKeyRouting(t *testing.T) {
	m, list, msgs, comp := recordingModel(t)

	m.Update(key("j"))
	if got := list.last(); got != (keymap.ActionMsg{Action: keymap.Down}) {
		t.Fatalf("j in the chat list delivered %#v, want ActionMsg{down}", got)
	}
	m.Update(key("enter"))
	if got := list.last(); got != (keymap.ActionMsg{Action: keymap.Open}) {
		t.Fatalf("enter in the chat list delivered %#v", got)
	}

	m.Update(key("tab")) // messages
	m.Update(key("g"))
	if p := m.PendingKeys(); len(p) != 1 || p[0] != "g" {
		t.Fatalf("pending keys = %q, want [g]", p)
	}
	if len(msgs.msgs) != 0 {
		t.Fatal("a pending key must not reach the pane")
	}
	m.Update(key("q"))
	if got := msgs.last(); got != (keymap.ActionMsg{Action: keymap.JumpQuoted}) {
		t.Fatalf("gq delivered %#v", got)
	}

	m.Update(key("i"))
	if m.Focus() != FocusComposer {
		t.Fatalf("i must focus the composer, focus is %v", m.Focus())
	}
	m.Update(key("?"))
	if got, ok := comp.last().(tea.KeyPressMsg); !ok || got.String() != "?" {
		t.Fatalf("? in the composer delivered %#v, want the raw key", comp.last())
	}
	m.Update(key("enter"))
	if got := comp.last(); got != (keymap.ActionMsg{Action: keymap.Send}) {
		t.Fatalf("enter in the composer delivered %#v", got)
	}
	m.Update(key("esc"))
	if m.Focus() != FocusMessages {
		t.Fatalf("esc must return to the messages, focus is %v", m.Focus())
	}
}

func TestFocusChangeDropsPendingKeys(t *testing.T) {
	m, _, _, _ := recordingModel(t)
	m.Update(key("tab"))
	m.Update(key("g"))
	m.Update(key("tab"))
	if len(m.PendingKeys()) != 0 {
		t.Fatalf("pending keys survived a focus change: %q", m.PendingKeys())
	}
}

func TestFocusCycle(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 120, 40)
	want := []Focus{FocusMessages, FocusComposer, FocusChatList, FocusMessages}
	for _, f := range want {
		m.Update(key("tab"))
		if m.Focus() != f {
			t.Fatalf("after tab: focus %v, want %v", m.Focus(), f)
		}
	}
	m.Update(key("shift+tab"))
	if m.Focus() != FocusChatList {
		t.Fatalf("after shift+tab: focus %v", m.Focus())
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "No chats") {
		t.Fatal("the empty chat list is not shown")
	}
}

func TestLoadAndOpenChat(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 120, 40)
	now := time.Now()
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "Aile", LastActivity: now},
		{ID: 2, Title: "\x1b]0;x\x07İş", LastActivity: now.Add(-time.Minute)},
	}})
	m.Update(chatlist.UpdateMsg{Rows: []chatlist.Row{{ID: 3, Title: "Yeni", LastActivity: now.Add(time.Minute), Unread: 2}}})
	if !strings.Contains(ansi.Strip(m.View().Content), "Yeni") {
		t.Fatal("an upserted chat is not shown")
	}
	m.Update(key("j"))
	m.Update(key("j"))
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter did not open the chat")
	}
	_, load := m.Update(cmd())
	if load == nil {
		t.Fatal("opening a chat must load its messages")
	}
	m.Update(load())
	if !strings.Contains(ansi.Strip(m.View().Content), "No messages yet") {
		t.Fatal("the loaded (empty) chat is not shown")
	}
	if m.Focus() != FocusMessages {
		t.Fatalf("opening a chat must focus its messages, focus is %v", m.Focus())
	}
	if m.header != "İş" {
		t.Fatalf("header = %q, want the sanitized title", m.header)
	}
	m.Update(chatlist.OpenMsg{ID: 99}) // unknown chats are ignored
	if m.header != "İş" {
		t.Fatal("opening an unknown chat changed the header")
	}
}

func TestNarrowModeFollowsFocus(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 60, 20)
	if l := m.Layout(); !l.ShowList || l.ShowChat {
		t.Fatalf("narrow start: %+v", l)
	}
	m.Update(key("tab"))
	if l := m.Layout(); l.ShowList || !l.ShowChat {
		t.Fatalf("narrow after tab: %+v", l)
	}
	checkFrame(t, m.View().Content, m.ctx.Width, 60, 20)
	m.Update(key("shift+tab"))
	if l := m.Layout(); !l.ShowList {
		t.Fatalf("narrow back to list: %+v", l)
	}
}

func TestUnicodeCoreSwitchesWidthMethod(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 100, 30)
	if m.ctx.Width != ansi.WcWidth {
		t.Fatal("must start with wcwidth like the renderer")
	}
	m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeNotRecognized})
	if m.ctx.Width != ansi.WcWidth {
		t.Fatal("an unsupported mode must not switch the method")
	}
	m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
	if m.ctx.Width != ansi.GraphemeWidth {
		t.Fatal("a supported Unicode core mode must switch to grapheme widths")
	}
	checkFrame(t, m.View().Content, ansi.GraphemeWidth, 100, 30)
}

func TestQuit(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	_, cmd := m.Update(key("ctrl+q"))
	if cmd == nil {
		t.Fatal("ctrl+q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+q must quit")
	}
	if _, cmd := m.Update(key("x")); cmd != nil {
		t.Fatal("an unbound key routed to a placeholder must not return a command")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("ctrl+c is not bound to quit; the composer may use it")
	}
}

func BenchmarkView(b *testing.B) {
	m := New(Options{History: emptyHistory{}})
	size(m, 200, 50)
	for b.Loop() {
		_ = m.View()
	}
}

func TestComposeAndSend(t *testing.T) {
	type sent struct {
		chat domain.ChatID
		text string
	}
	var got []sent
	m := New(Options{History: emptyHistory{}, Send: func(out Outgoing) tea.Cmd {
		got = append(got, sent{out.Chat, out.Text})
		return nil
	}})
	size(m, 100, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Aile", LastActivity: time.Now()}}})
	_, open := m.Update(key("enter"))
	m.Update(open())

	m.Update(tea.PasteMsg{Content: "ignored"}) // messages are focused, not the composer
	m.Update(key("i"))
	for _, r := range "merhaba" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}) // newline
	m.Update(tea.PasteMsg{Content: "ikinci satır"})
	if h := m.Layout().Composer.H; h != 2 {
		t.Fatalf("composer height = %d, want 2 after a newline", h)
	}
	v := m.View()
	if v.Cursor == nil || v.Cursor.X < m.Layout().Composer.X || v.Cursor.Y < m.Layout().Composer.Y {
		t.Fatalf("cursor %+v is not inside the composer %+v", v.Cursor, m.Layout().Composer)
	}
	checkFrame(t, v.Content, m.ctx.Width, 100, 30)

	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter in the composer did not send")
	}
	m.Update(cmd())
	if len(got) != 1 || got[0].chat != 4 || got[0].text != "merhaba\nikinci satır" {
		t.Fatalf("sent %+v", got)
	}
	if h := m.Layout().Composer.H; h != 1 {
		t.Fatalf("composer height = %d after sending, want 1", h)
	}
	m.Update(key("esc"))
	if m.View().Cursor != nil {
		t.Fatal("the cursor must hide when the composer loses focus")
	}
}

func TestReplyFlow(t *testing.T) {
	var quoted domain.WAMessageID
	m := New(Options{History: emptyHistory{}, Names: msglist.NamesFunc(func(domain.IdentityID) string { return "Ali" }),
		Send: func(out Outgoing) tea.Cmd { quoted = out.Quoted; return nil }})
	size(m, 100, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Aile", LastActivity: time.Now()}}})
	_, open := m.Update(key("enter"))
	m.Update(open())
	question := domain.Message{ID: 1, ChatID: 4, WAID: "Q1", SenderID: 7, Kind: domain.KindText, Body: "geliyor musun?"}
	m.Update(msglist.MessageActionMsg{Action: keymap.Reply, Message: question})
	if m.Focus() != FocusComposer || !strings.Contains(ansi.Strip(m.View().Content), "↩ Ali: geliyor musun?") {
		t.Fatalf("focus %v\n%s", m.Focus(), ansi.Strip(m.View().Content))
	}
	if h := m.Layout().Composer.H; h != 2 {
		t.Fatalf("composer height = %d with the reply line", h)
	}
	m.Update(key("esc"))
	if m.Focus() != FocusComposer || m.comp.Replying() {
		t.Fatal("the first esc drops the reply and stays in the composer")
	}
	m.Update(key("esc"))
	if m.Focus() != FocusMessages {
		t.Fatal("the second esc leaves the composer")
	}
	m.Update(msglist.MessageActionMsg{Action: keymap.Reply, Message: question})
	for _, r := range "evet" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if quoted != "Q1" {
		t.Fatalf("sent as a reply to %q", quoted)
	}
	deleted := question
	deleted.RevokedAt = time.Now()
	m.Update(msglist.MessageActionMsg{Action: keymap.Reply, Message: deleted})
	if !strings.Contains(statusOf(m), "cannot be answered") {
		t.Fatalf("status %q", statusOf(m))
	}
	m.Update(msglist.NoticeMsg{Text: "The quoted message is not stored on this computer"})
	if !strings.Contains(statusOf(m), "not stored") {
		t.Fatalf("status %q", statusOf(m))
	}
}

func TestReactionPicker(t *testing.T) {
	type reaction struct {
		waid  domain.WAMessageID
		emoji string
	}
	var got []reaction
	m := New(Options{History: emptyHistory{}, React: func(msg domain.Message, emoji string) tea.Cmd {
		got = append(got, reaction{msg.WAID, emoji})
		return nil
	}})
	size(m, 120, 30)
	target := domain.Message{ID: 1, ChatID: 4, WAID: "W1", SenderID: 7, Kind: domain.KindText, Body: "hi"}
	m.Update(msglist.MessageActionMsg{Action: keymap.React, Message: target})
	if st := statusOf(m); !strings.Contains(st, "REACT") || !strings.Contains(st, "0 remove") {
		t.Fatalf("status %q", st)
	}
	m.Update(key("2"))
	m.Update(msglist.MessageActionMsg{Action: keymap.React, Message: target})
	m.Update(key("0"))
	m.Update(msglist.MessageActionMsg{Action: keymap.React, Message: target})
	m.Update(key("esc"))
	if want := []reaction{{"W1", "\u2764\ufe0f"}, {"W1", ""}}; !slices.Equal(got, want) {
		t.Fatalf("reactions = %q, want %q", got, want)
	}
	if m.mode != modeNormal {
		t.Fatal("esc cancels the picker")
	}
	failed := domain.Message{ID: 2, WAID: "W2", FromMe: true, Status: domain.StatusFailed}
	m.Update(msglist.MessageActionMsg{Action: keymap.React, Message: failed})
	if m.mode == modeReact || !strings.Contains(statusOf(m), "cannot get a reaction") {
		t.Fatalf("a failed message gets none: %q", statusOf(m))
	}
	m.Update(key(":"))
	typeKeys(m, "react x")
	m.Update(key("enter"))
	if !strings.Contains(statusOf(m), "select a message") {
		t.Fatalf("status %q", statusOf(m))
	}
	off := New(Options{History: emptyHistory{}})
	size(off, 120, 30)
	off.Update(msglist.MessageActionMsg{Action: keymap.React, Message: target})
	if !strings.Contains(statusOf(off), "not available") {
		t.Fatalf("status %q", statusOf(off))
	}
}

func TestEditAndDeleteFlow(t *testing.T) {
	var edited []string
	var deleted []domain.WAMessageID
	m := New(Options{History: emptyHistory{}, Send: func(Outgoing) tea.Cmd { return nil },
		Edit: func(msg domain.Message, text string) tea.Cmd {
			edited = append(edited, string(msg.WAID)+"="+text)
			return nil
		},
		Delete: func(msg domain.Message) tea.Cmd { deleted = append(deleted, msg.WAID); return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Aile", LastActivity: time.Now()}}})
	_, open := m.Update(key("enter"))
	m.Update(open())
	own := domain.Message{ID: 1, ChatID: 4, WAID: "W1", FromMe: true, Kind: domain.KindText, Body: "selm", Timestamp: time.Now()}
	m.Update(msglist.MessageActionMsg{Action: keymap.Edit, Message: own})
	if m.Focus() != FocusComposer || !m.comp.Editing() || m.comp.Value() != "selm" {
		t.Fatalf("focus %v, editing %v, %q", m.Focus(), m.comp.Editing(), m.comp.Value())
	}
	m.Update(key("backspace"))
	m.Update(key("backspace"))
	for _, r := range "lam" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if len(edited) != 1 || edited[0] != "W1=selam" {
		t.Fatalf("edited = %q", edited)
	}
	m.Update(msglist.MessageActionMsg{Action: keymap.Edit, Message: own})
	m.Update(key("esc"))
	if m.comp.Editing() || m.Focus() != FocusComposer {
		t.Fatal("the first esc cancels the edit")
	}
	old := own
	old.Timestamp = time.Now().Add(-time.Hour)
	m.Update(msglist.MessageActionMsg{Action: keymap.Edit, Message: old})
	if m.comp.Editing() || !strings.Contains(statusOf(m), "20 minutes") {
		t.Fatalf("an old message is not edited: %q", statusOf(m))
	}
	theirs := domain.Message{ID: 2, ChatID: 4, WAID: "W2", SenderID: 7, Kind: domain.KindText, Body: "hi"}
	m.Update(msglist.MessageActionMsg{Action: keymap.Edit, Message: theirs})
	if !strings.Contains(statusOf(m), "Only your own") {
		t.Fatalf("status %q", statusOf(m))
	}

	m.Update(msglist.MessageActionMsg{Action: keymap.Delete, Message: own})
	if st := statusOf(m); !strings.Contains(st, "CONFIRM") || !strings.Contains(st, "Delete this message for everyone? y/n") {
		t.Fatalf("status %q", st)
	}
	m.Update(key("n"))
	m.Update(msglist.MessageActionMsg{Action: keymap.Delete, Message: theirs})
	if !strings.Contains(statusOf(m), "as a group admin") {
		t.Fatalf("status %q", statusOf(m))
	}
	_, cmd = m.Update(key("y"))
	if cmd != nil {
		m.Update(cmd())
	}
	if !slices.Equal(deleted, []domain.WAMessageID{"W2"}) {
		t.Fatalf("deleted = %q", deleted)
	}
}

func TestMentionSuggestions(t *testing.T) {
	var sent Outgoing
	loads := 0
	m := New(Options{History: emptyHistory{}, Send: func(out Outgoing) tea.Cmd { sent = out; return nil },
		Mentionable: func(chat domain.ChatID) tea.Cmd {
			loads++
			return func() tea.Msg {
				return MentionCandidatesMsg{Chat: chat, Candidates: []MentionCandidate{
					{Name: "Zeynep Ay", JID: "103@lid"}, {Name: "Ayşe Yılmaz", JID: "102@lid"}, {Name: "Ali", JID: "101@lid"}, {Name: "", JID: "x@lid"},
				}}
			}
		}})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Aile", LastActivity: time.Now(), Group: true},
		{ID: 5, Title: "Ali", LastActivity: time.Now().Add(-time.Minute)}}})
	_, open := m.Update(key("enter"))
	m.Update(open())
	m.Update(key("i"))
	typing := func(s string) {
		for _, r := range s {
			_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			runCmd(m, cmd)
		}
	}
	typing("selam @ay")
	view := ansi.Strip(m.View().Content)
	if loads != 1 || !strings.Contains(view, "tab @Ayşe Yılmaz @Zeynep Ay") {
		t.Fatalf("loads %d, view:\n%s", loads, view)
	}
	m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // the second
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Focus() != FocusComposer || m.comp.Value() != "selam @Zeynep Ay " {
		t.Fatalf("tab completes the picked name: %q (focus %v)", m.comp.Value(), m.Focus())
	}
	typing("@x")
	if strings.Contains(ansi.Strip(m.View().Content), "tab @") {
		t.Fatal("no match, no suggestions")
	}
	m.Update(key("backspace"))
	m.Update(key("backspace"))
	typing("@a")
	m.Update(key("esc"))
	if strings.Contains(ansi.Strip(m.View().Content), "tab @") || m.Focus() != FocusComposer {
		t.Fatal("esc closes the suggestions and stays in the composer")
	}
	typing("l")
	if !strings.Contains(ansi.Strip(m.View().Content), "tab @Ali") {
		t.Fatal("typing on offers them again")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd := m.Update(key("enter"))
	m.Update(cmd())
	if sent.Text != "selam @103 @101 " && sent.Text != "selam @103 @101" {
		t.Fatalf("sent %q", sent.Text)
	}
	if !slices.Equal(sent.Mentions, []domain.JID{"103@lid", "101@lid"}) || loads != 1 {
		t.Fatalf("mentions %v, loads %d", sent.Mentions, loads)
	}
}

func TestPresenceAndTyping(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var avail []bool
	type typed struct {
		chat      domain.ChatID
		composing bool
	}
	var typing []typed
	m := New(Options{History: emptyHistory{}, Clock: func() time.Time { return now }, Location: time.UTC,
		Send:     func(Outgoing) tea.Cmd { return nil },
		Presence: func(a bool) tea.Cmd { avail = append(avail, a); return nil },
		Typing:   func(c domain.ChatID, on bool) tea.Cmd { typing = append(typing, typed{c, on}); return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Ali", LastActivity: now}}})
	m.Update(key("j")) // a key: online; the commands it returns hold ticks, so none is run here
	m.Update(chatlist.OpenMsg{ID: 4})
	if !slices.Equal(avail, []bool{true}) {
		t.Fatalf("a key makes the user online once: %v", avail)
	}
	m.Update(key("i"))
	m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	now = now.Add(3 * time.Second)
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if !slices.Equal(typing, []typed{{4, true}}) {
		t.Fatalf("typing = %v, want one composing within the renewal time", typing)
	}
	m.Update(typingIdleMsg{gen: m.typingGen - 1}) // stale
	m.Update(typingIdleMsg{gen: m.typingGen})
	if !slices.Equal(typing, []typed{{4, true}, {4, false}}) {
		t.Fatalf("typing = %v, want paused after the idle time", typing)
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.Update(composer.SendMsg{Chat: 4, Text: "abc"})
	if !slices.Equal(typing, []typed{{4, true}, {4, false}, {4, true}, {4, false}}) {
		t.Fatalf("typing = %v, want paused on send", typing)
	}
	// The header shows the peer.
	m.Update(BatchMsg{Presence: map[domain.ChatID]domain.PresenceChanged{4: {Online: true}}})
	if !strings.Contains(ansi.Strip(m.View().Content), "Ali · online") {
		t.Fatalf("header:\n%s", ansi.Strip(m.View().Content))
	}
	m.Update(BatchMsg{Presence: map[domain.ChatID]domain.PresenceChanged{4: {LastSeen: now.Add(-time.Hour)}}})
	if !strings.Contains(ansi.Strip(m.View().Content), "Ali · last seen today 11:00") {
		t.Fatalf("header:\n%s", ansi.Strip(m.View().Content))
	}
	m.Update(BatchMsg{Typing: map[domain.ChatID]bool{4: true}})
	if !strings.Contains(ansi.Strip(m.View().Content), "Ali · typing…") {
		t.Fatalf("header:\n%s", ansi.Strip(m.View().Content))
	}
	// Idle for two minutes, and focus loss, end the online status.
	now = now.Add(time.Minute)
	m.Update(presenceCheckMsg{})
	now = now.Add(2 * time.Minute)
	m.Update(presenceCheckMsg{})
	if !slices.Equal(avail, []bool{true, false}) {
		t.Fatalf("avail = %v, want offline when idle", avail)
	}
	m.Update(key("x"))
	m.Update(tea.BlurMsg{})
	if !slices.Equal(avail, []bool{true, false, true, false}) {
		t.Fatalf("avail = %v", avail)
	}
	if got := lastSeen(now.AddDate(0, 0, -1), now, time.UTC); !strings.HasPrefix(got, "yesterday") {
		t.Fatalf("lastSeen = %q", got)
	}
	if got := lastSeen(now.AddDate(0, 0, -5), now, time.UTC); got != now.AddDate(0, 0, -5).Format("2 Jan 15:04") {
		t.Fatalf("lastSeen = %q", got)
	}
}

func TestNotifications(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var got []Notification
	m := New(Options{History: emptyHistory{}, Clock: func() time.Time { return now }, Location: time.UTC,
		Names:  msglist.NamesFunc(func(domain.IdentityID) string { return "Ali" }),
		Notify: func(n Notification) tea.Cmd { got = append(got, n); return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "Aile", LastActivity: now, Group: true},
		{ID: 2, Title: "Ayşe", LastActivity: now},
		{ID: 3, Title: "Sessiz", LastActivity: now, Muted: true},
	}})
	msg := func(chat domain.ChatID, id domain.MessageID, body string, age time.Duration, unread bool) domain.Message {
		return domain.Message{ID: id, ChatID: chat, SenderID: 7, Kind: domain.KindText, Body: body, Timestamp: now.Add(-age), Unread: unread}
	}
	batch := BatchMsg{Appends: map[domain.ChatID][]domain.Message{
		1: {msg(1, 1, "eski", time.Minute, true), msg(1, 2, "yeni\nsatır", 0, true)},
		2: {msg(2, 3, "geçmiş", 0, false), msg(2, 4, "dünkü", time.Hour, true)},
		3: {msg(3, 5, "sessiz", 0, true)},
	}}
	m.Update(batch)
	if len(got) != 1 || got[0].Chat != 1 || got[0].Text != "yeni satır" || got[0].Sender != "Ali" || got[0].Title != "Aile" {
		t.Fatalf("notifications = %+v", got)
	}
	// While focus is unknown, the open chat does not notify.
	m.Update(chatlist.OpenMsg{ID: 1})
	got = nil
	m.Update(BatchMsg{Appends: map[domain.ChatID][]domain.Message{1: {msg(1, 6, "x", 0, true)}, 2: {msg(2, 7, "", 0, true)}}})
	if len(got) != 1 || got[0].Chat != 2 || got[0].Text != "[text]" || got[0].Sender != "" {
		t.Fatalf("notifications = %+v", got)
	}
	got = nil
	m.Update(tea.FocusMsg{})
	m.Update(BatchMsg{Appends: map[domain.ChatID][]domain.Message{2: {msg(2, 8, "x", 0, true)}}})
	if len(got) != 0 {
		t.Fatal("a focused terminal shows no notifications")
	}
	m.Update(tea.BlurMsg{})
	m.Update(BatchMsg{Appends: map[domain.ChatID][]domain.Message{1: {msg(1, 9, "x", 0, true)}}})
	if len(got) != 1 {
		t.Fatal("without focus, even the open chat notifies")
	}
	own := msg(1, 10, "mine", 0, true)
	own.FromMe = true
	got = nil
	m.Update(BatchMsg{Appends: map[domain.ChatID][]domain.Message{1: {own}}})
	if len(got) != 0 {
		t.Fatal("own messages never notify")
	}
}

type findSource struct{ msgs []domain.Message }

func (f findSource) Search(context.Context, string, domain.ChatID, int) ([]domain.Message, error) {
	return f.msgs, nil
}

func TestFindFlow(t *testing.T) {
	src := findSource{msgs: []domain.Message{{ChatID: 2, WAID: "W9", Body: "kahve?", Timestamp: time.Now()}}}
	m := New(Options{History: emptyHistory{}, Search: src})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Aile", LastActivity: time.Now()}, {ID: 2, Title: "Ayşe", LastActivity: time.Now()}}})
	command := func(line string) {
		m.Update(key(":"))
		typeKeys(m, line)
		_, cmd := m.Update(key("enter"))
		runCmd(m, cmd)
	}
	command("findin kahve")
	if !strings.Contains(statusOf(m), "open a chat to search") {
		t.Fatalf("status %q", statusOf(m))
	}
	command("find")
	if !strings.Contains(statusOf(m), "usage: :find") {
		t.Fatalf("status %q", statusOf(m))
	}
	command("find kahve")
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "Search “kahve” in all chats") || !strings.Contains(statusOf(m), "FIND") {
		t.Fatalf("view:\n%s", v)
	}
	_, cmd := m.Update(key("enter"))
	runCmd(m, cmd)
	if m.mode != modeNormal || m.header != "Ayşe" || m.msgs.Chat() != 2 || m.Focus() != FocusMessages {
		t.Fatalf("the result opens its chat: mode %v header %q chat %d", m.mode, m.header, m.msgs.Chat())
	}
	command("find x")
	m.Update(key("esc"))
	if m.mode != modeNormal {
		t.Fatal("esc closes the results")
	}
	off := New(Options{History: emptyHistory{}})
	size(off, 120, 30)
	off.Update(key(":"))
	typeKeys(off, "find x")
	off.Update(key("enter"))
	if !strings.Contains(statusOf(off), "not available") {
		t.Fatalf("status %q", statusOf(off))
	}
}

func TestUnreadNavigationAndSettings(t *testing.T) {
	var got []string
	m := New(Options{History: emptyHistory{}, ChatSetting: func(chat domain.ChatID, s ChatSetting) tea.Cmd {
		got = append(got, fmt.Sprint(chat, " ", s.What, " ", s.MuteFor))
		return nil
	}})
	size(m, 120, 30)
	now := time.Now()
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{
		{ID: 1, Title: "A", LastActivity: now, Unread: 2},
		{ID: 2, Title: "B", LastActivity: now.Add(-time.Minute)},
		{ID: 3, Title: "C", LastActivity: now.Add(-2 * time.Minute), Unread: 1},
	}})
	press := func(keys ...string) {
		for _, k := range keys {
			_, cmd := m.Update(key(k))
			runCmd(m, cmd)
		}
	}
	press("]", "u")
	if m.msgs.Chat() != 3 {
		t.Fatalf("]u opens the next unread chat: %d", m.msgs.Chat())
	}
	m.Update(key("tab"))
	m.Update(key("tab")) // back to the chat list
	press("]", "u")
	if m.msgs.Chat() != 1 {
		t.Fatalf("]u wraps around: %d", m.msgs.Chat())
	}
	press("[", "u")
	if m.msgs.Chat() != 3 {
		t.Fatalf("[u goes back: %d", m.msgs.Chat())
	}
	command := func(line string) {
		m.Update(key(":"))
		typeKeys(m, line)
		_, cmd := m.Update(key("enter"))
		runCmd(m, cmd)
	}
	command("mute 1w")
	command("mute")
	command("mute always")
	command("pin")
	command("unarchive")
	command("mute soon")
	want := []string{"3 mute 168h0m0s", "3 mute 8h0m0s", "3 mute 0s", "3 pin 0s", "3 unarchive 0s"}
	if !slices.Equal(got, want) {
		t.Fatalf("settings = %q, want %q", got, want)
	}
	if !strings.Contains(statusOf(m), "usage: :mute") {
		t.Fatalf("status %q", statusOf(m))
	}
	read := New(Options{History: emptyHistory{}})
	size(read, 120, 30)
	read.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "A", LastActivity: now}}})
	read.Update(key("]"))
	read.Update(key("u"))
	if !strings.Contains(statusOf(read), "no unread chats") {
		t.Fatalf("status %q", statusOf(read))
	}
}

func TestDisappearingAndViewOnce(t *testing.T) {
	opened := 0
	m := New(Options{History: emptyHistory{}, OpenMedia: func(domain.Message) tea.Cmd { opened++; return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Aile", LastActivity: time.Now(), Disappearing: 24 * time.Hour}}})
	m.Update(chatlist.OpenMsg{ID: 1})
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "Aile · messages disappear after 1 day") {
		t.Fatalf("header:\n%s", v)
	}
	once := domain.Message{ID: 1, WAID: "W", Kind: domain.KindImage, Flags: domain.FlagViewOnce, Media: &domain.Media{}}
	m.Update(msglist.MessageActionMsg{Action: keymap.OpenMedia, Message: once})
	if opened != 0 || !strings.Contains(statusOf(m), "only on your phone") {
		t.Fatalf("view once: opened %d, status %q", opened, statusOf(m))
	}
	for d, want := range map[time.Duration]string{time.Hour: "1 hour", 7 * 24 * time.Hour: "7 days", 90 * time.Minute: "90 minutes"} {
		if got := timerLabel(d); got != want {
			t.Errorf("timerLabel(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestDeleteLocalAsks(t *testing.T) {
	var deleted []domain.ChatID
	m := New(Options{History: emptyHistory{}, DeleteLocal: func(chat domain.ChatID) tea.Cmd { deleted = append(deleted, chat); return nil }})
	size(m, 140, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 5, Title: "Eski grup", LastActivity: time.Now()}}})
	command := func(line string) {
		m.Update(key(":"))
		typeKeys(m, line)
		_, cmd := m.Update(key("enter"))
		runCmd(m, cmd)
	}
	command("delete-local")
	if st := statusOf(m); !strings.Contains(st, "Delete Eski grup and its messages from this computer? Your phone keeps them. y/n") {
		t.Fatalf("status %q", st)
	}
	m.Update(key("n"))
	if len(deleted) != 0 || !strings.Contains(statusOf(m), "nothing changed") {
		t.Fatal("n keeps the chat")
	}
	command("delete-local")
	m.Update(key("y"))
	if !slices.Equal(deleted, []domain.ChatID{5}) {
		t.Fatalf("deleted = %v", deleted)
	}
}

func TestDebugPane(t *testing.T) {
	asked := 0
	m := New(Options{History: emptyHistory{}, Metrics: func() tea.Cmd {
		asked++
		return func() tea.Msg {
			return MetricsMsg{Sections: []MetricSection{{Title: "Store", Items: [][2]string{{"transactions", "42"}}}}}
		}
	}})
	size(m, 100, 40)
	m.Update(key(":"))
	typeKeys(m, "debug")
	_, cmd := m.Update(key("enter"))
	// The command batches the metrics with a one-second tick: run only the
	// metrics.
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			if msg, ok := c().(MetricsMsg); ok {
				m.Update(msg)
				break
			}
		}
	}
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"tarcin debug", "Store", "transactions             42", "UI", "frames", "message cache", "Process", "goroutines", "DEBUG"} {
		if !strings.Contains(v, want) {
			t.Fatalf("debug pane lacks %q:\n%s", want, v)
		}
	}
	if _, cmd := m.Update(debugTickMsg{}); cmd == nil || asked != 2 {
		t.Fatalf("a tick refreshes the pane while it shows: asked %d", asked)
	}
	m.Update(key("esc"))
	if _, cmd := m.Update(debugTickMsg{}); m.mode != modeNormal || cmd != nil {
		t.Fatal("esc closes the pane, and ticks stop")
	}
	if got := hitRate(0, 0); got != "–" {
		t.Fatalf("hitRate = %q", got)
	}
	if got := hitRate(200, 10); got != "95.0% hits" {
		t.Fatalf("hitRate = %q", got)
	}
}
