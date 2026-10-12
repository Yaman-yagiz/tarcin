// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/gen"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// memHistory serves pages from sorted in-memory slices.
type memHistory struct {
	mu    sync.Mutex
	chats map[domain.ChatID][]domain.Message
	calls int
	fail  error
}

func (h *memHistory) get(chat domain.ChatID) ([]domain.Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	return h.chats[chat], h.fail
}

func (h *memHistory) Latest(_ context.Context, chat domain.ChatID, limit int) ([]domain.Message, error) {
	msgs, err := h.get(chat)
	return slices.Clone(msgs[max(len(msgs)-limit, 0):]), err
}

func (h *memHistory) Before(_ context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	msgs, err := h.get(chat)
	i, _ := slices.BinarySearchFunc(msgs, c, func(m domain.Message, c domain.Cursor) int {
		if m.Cursor().Before(c) {
			return -1
		}
		return 1
	})
	return slices.Clone(msgs[max(i-limit, 0):i]), err
}

func (h *memHistory) After(_ context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	msgs, err := h.get(chat)
	i, _ := slices.BinarySearchFunc(msgs, c, func(m domain.Message, c domain.Cursor) int {
		if c.Before(m.Cursor()) {
			return 1
		}
		return -1
	})
	return slices.Clone(msgs[i:min(i+limit, len(msgs))]), err
}

var testNow = gen.DefaultEnd.Add(time.Hour)

// fixture builds a view over a generated chat (ID 1) with n messages.
func fixture(t testing.TB, n, w, h int) (*Model, *memHistory) {
	t.Helper()
	d := gen.Build(gen.Spec{Seed: 5, Chats: 2, Messages: n + 10, HotChatMessages: n, GroupRatio: 1, MembersPerGroup: 12})
	hist := &memHistory{chats: map[domain.ChatID][]domain.Message{}}
	for i := range d.Chats {
		hist.chats[d.Chats[i].ID] = slices.Collect(d.Messages(i))
	}
	names := NamesFunc(func(id domain.IdentityID) string { return d.Identities[id-1].PushName })
	m := New(uikit.NewContext(theme.Default()), hist, names)
	m.now = func() time.Time { return testNow }
	m.loc = time.UTC
	m.Resize(w, h)
	return m, hist
}

// drive runs cmd and every command it leads to, synchronously.
func drive(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			queue = append(queue, m.Update(msg))
		}
	}
}

func act(m *Model, a keymap.Action) { drive(m, m.Update(keymap.ActionMsg{Action: a})) }

func plainLines(m *Model) []string { return strings.Split(ansi.Strip(m.View()), "\n") }

func checkViewport(t *testing.T, m *Model) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.h {
		t.Fatalf("view has %d lines, want %d", len(lines), m.h)
	}
	for i, l := range lines {
		if w := m.ctx.Width.StringWidth(l); w > m.w {
			t.Fatalf("line %d is %d cells wide (> %d): %q", i, w, m.w, ansi.Strip(l))
		}
	}
}

func TestOpenShowsTheNewestMessage(t *testing.T) {
	m, _ := fixture(t, 2500, 80, 20)
	if !strings.Contains(m.View(), "Select a chat") {
		t.Fatal("no empty state before a chat is open")
	}
	cmd := m.Open(1)
	if !strings.Contains(ansi.Strip(m.View()), "Loading") {
		t.Fatal("no loading state")
	}
	drive(m, cmd)
	if !m.follow || !m.atBottom() || !m.newerDone || m.olderDone {
		t.Fatalf("after open: follow=%v bottom=%v newerDone=%v olderDone=%v", m.follow, m.atBottom(), m.newerDone, m.olderDone)
	}
	if len(m.win) != PageSize {
		t.Fatalf("window holds %d messages, want one page", len(m.win))
	}
	last := m.lines(len(m.win) - 1)
	view := strings.Split(m.View(), "\n")
	if view[len(view)-1] != " "+last[len(last)-1] {
		t.Fatalf("the newest message is not at the bottom:\n%q\nvs\n%q", view[len(view)-1], last[len(last)-1])
	}
	checkViewport(t, m)
}

func TestViewportAtManySizes(t *testing.T) {
	for _, w := range []int{1, 2, 3, 20, 41, 80, 200} {
		for _, h := range []int{1, 2, 5, 24, 50} {
			m, _ := fixture(t, 400, w, h)
			drive(m, m.Open(1))
			checkViewport(t, m)
			act(m, keymap.PageUp)
			checkViewport(t, m)
		}
	}
}

func TestScrollToTheStartAndBack(t *testing.T) {
	m, hist := fixture(t, 2500, 60, 20)
	m.SetFocused(true)
	drive(m, m.Open(1))
	for steps := 0; !m.olderDone || m.idx[m.anchor] != m.lastFullScreenIndex(); steps++ {
		act(m, keymap.PageUp)
		if len(m.win) > MaxWindow+PageSize {
			t.Fatalf("window grew to %d messages", len(m.win))
		}
		checkViewport(t, m)
		if steps > 5000 {
			t.Fatal("never reached the start of the chat")
		}
	}
	if m.newerDone || m.follow {
		t.Fatal("the window should have dropped the newest messages")
	}
	if top, cut, filled := m.topVisible(); top != 0 || cut != 0 || filled != m.h {
		t.Fatalf("at the start: top=%d cut=%d filled=%d", top, cut, filled)
	}
	if first := plainLines(m)[0]; !strings.Contains(first, "\u2500") {
		t.Fatalf("the first line should be the first day separator, got %q", first)
	}
	if hist.calls < 20 {
		t.Fatalf("only %d history calls for 2500 messages", hist.calls)
	}

	for steps := 0; !m.follow; steps++ {
		act(m, keymap.PageDown)
		checkViewport(t, m)
		if steps > 5000 {
			t.Fatal("never returned to the newest message")
		}
	}
	if !m.newerDone || !m.atBottom() {
		t.Fatal("following without the newest message at the bottom")
	}
}

// lastFullScreenIndex is the anchor index when the first message's top is
// at the top of the viewport.
func (m *Model) lastFullScreenIndex() int {
	total := 0
	for i := range m.win {
		total += m.height(i)
		if total >= m.h {
			return i
		}
	}
	return len(m.win) - 1
}

func TestAnchorIsStableWhenAnOlderPageArrives(t *testing.T) {
	m, _ := fixture(t, 2500, 70, 20)
	drive(m, m.Open(1))
	var fetch tea.Cmd
	for i := 0; fetch == nil && i < 100; i++ {
		fetch = m.Update(keymap.ActionMsg{Action: keymap.PageUp})
	}
	if fetch == nil {
		t.Fatal("scrolling up never requested an older page")
	}
	if top, _, _ := m.topVisible(); top == 0 {
		t.Fatal("the fetch was requested too late: the first message is already visible")
	}
	before := m.View()
	n := len(m.win)
	drive(m, fetch)
	if len(m.win) <= n {
		t.Fatal("the older page did not arrive")
	}
	if after := m.View(); after != before {
		t.Fatalf("the view moved when older messages were prepended:\n%s\n---\n%s", ansi.Strip(before), ansi.Strip(after))
	}
}

func newMessage(prev domain.Message, body string) domain.Message {
	return domain.Message{
		ID: prev.ID + 1000, ChatID: prev.ChatID, WAID: domain.WAMessageID("NEW" + body),
		SenderID: prev.SenderID, Timestamp: prev.Timestamp.Add(time.Minute), Kind: domain.KindText, Body: body,
	}
}

func TestLiveMessages(t *testing.T) {
	m, _ := fixture(t, 500, 80, 20)
	drive(m, m.Open(1))
	last := m.win[len(m.win)-1]

	a := newMessage(last, "first live message")
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{a}})
	if m.anchor != a.ID || !strings.Contains(ansi.Strip(m.View()), "first live message") {
		t.Fatal("a live message is not shown at the bottom while following")
	}
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{a}})
	m.Update(AppendMsg{Chat: 2, Msgs: []domain.Message{newMessage(a, "other chat")}})
	if m.win[len(m.win)-1].ID != a.ID || m.unseen != 0 {
		t.Fatal("duplicates or other chats' messages must be ignored")
	}

	act(m, keymap.PageUp)
	b := newMessage(a, "second live message")
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{b}})
	view := ansi.Strip(m.View())
	if m.follow || m.unseen != 1 || !strings.Contains(view, "1 new message (G)") {
		t.Fatalf("scrolled up: follow=%v unseen=%d\n%s", m.follow, m.unseen, view)
	}
	act(m, keymap.Bottom)
	if !m.follow || m.unseen != 0 || strings.Contains(ansi.Strip(m.View()), "new message") || m.anchor != b.ID {
		t.Fatal("G must return to the newest message and clear the indicator")
	}
}

func TestReplace(t *testing.T) {
	m, _ := fixture(t, 200, 80, 20)
	drive(m, m.Open(1))
	msg := m.win[len(m.win)-1]
	msg.Body = "corrected text"
	m.Update(ReplaceMsg{Chat: 1, Msgs: []domain.Message{msg}})
	if strings.Contains(ansi.Strip(m.View()), "corrected text") {
		t.Fatal("an update without a new revision must be ignored")
	}
	msg.Revision++
	msg.EditedAt = msg.Timestamp.Add(time.Minute)
	m.Update(ReplaceMsg{Chat: 1, Msgs: []domain.Message{msg}})
	if !strings.Contains(ansi.Strip(m.View()), "corrected text") {
		t.Fatal("the edited message is not shown")
	}
}

func TestStalePagesAreDropped(t *testing.T) {
	m, _ := fixture(t, 200, 80, 20)
	first := m.Open(1)
	second := m.Open(2)
	drive(m, first)
	if !m.loading {
		t.Fatal("a page for a chat that was replaced must be dropped")
	}
	drive(m, second)
	if m.loading || m.Chat() != 2 || len(m.win) == 0 || m.win[0].ChatID != 2 {
		t.Fatal("the second chat did not load")
	}
}

func TestSelection(t *testing.T) {
	m, _ := fixture(t, 600, 80, 15)
	m.SetFocused(true)
	drive(m, m.Open(1))
	n := len(m.win)
	if m.selected != m.win[n-1].ID {
		t.Fatal("opening a focused chat must select the newest message")
	}
	for range 3 {
		act(m, keymap.Up)
	}
	if m.selected != m.win[n-4].ID {
		t.Fatal("up must move the selection by one message")
	}
	if !strings.Contains(m.View(), "\u258c") {
		t.Fatal("the selected message has no selection bar")
	}
	for range 40 {
		act(m, keymap.Up)
		i := m.idx[m.selected]
		top, cut, _ := m.topVisible()
		if a := m.idx[m.anchor]; i < top || i > a || (i == top && cut > 0 && m.height(i) <= m.h) {
			t.Fatalf("the selection %d left the viewport [%d, %d]", i, top, a)
		}
	}
	sel := m.selected
	cmd := m.Update(keymap.ActionMsg{Action: keymap.Reply})
	if got, ok := cmd().(MessageActionMsg); !ok || got.Action != keymap.Reply || got.Message.ID != sel {
		t.Fatalf("reply produced %#v", cmd())
	}
	m.SetFocused(false)
	if strings.Contains(m.View(), "\u258c") {
		t.Fatal("an unfocused view must not show the selection bar")
	}
}

func TestHostileAndLongContent(t *testing.T) {
	d := gen.Build(gen.Scenarios["S12"])
	hist := &memHistory{chats: map[domain.ChatID][]domain.Message{}}
	for i := range d.Chats {
		hist.chats[d.Chats[i].ID] = slices.Collect(d.Messages(i))
	}
	m := New(uikit.NewContext(theme.Default()), hist, NamesFunc(func(id domain.IdentityID) string { return d.Identities[id-1].PushName }))
	m.Resize(100, 40)
	drive(m, m.Open(1))
	for range 30 {
		act(m, keymap.PageUp)
		plain := ansi.Strip(m.View())
		for _, bad := range []string{"\x1b", "pwned", "]52;", "\u202e", "\u009b"} {
			if strings.Contains(plain, bad) {
				t.Fatalf("hostile content %q reached the screen", bad)
			}
		}
		checkViewport(t, m)
	}
	for i := range m.win {
		if h := m.height(i); h > maxBodyLines+4 {
			t.Fatalf("message %d renders %d lines; long messages must be capped", i, h)
		}
	}
}

func TestGroupingAndDays(t *testing.T) {
	day1 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	msgs := []domain.Message{
		{ID: 0, ChatID: 1, SenderID: 3, Timestamp: time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC), Kind: domain.KindText, Body: "last week"},
		{ID: 1, ChatID: 1, SenderID: 2, Timestamp: day1, Kind: domain.KindText, Body: "first"},
		{ID: 2, ChatID: 1, SenderID: 2, Timestamp: day1.Add(time.Minute), Kind: domain.KindText, Body: "grouped"},
		{ID: 3, ChatID: 1, SenderID: 3, Timestamp: day1.Add(2 * time.Minute), Kind: domain.KindText, Body: "other sender"},
		{ID: 4, ChatID: 1, SenderID: 3, Timestamp: day1.Add(30 * time.Minute), Kind: domain.KindText, Body: "later"},
		{ID: 5, ChatID: 1, FromMe: true, SenderID: 1, Timestamp: day1.Add(24 * time.Hour), Kind: domain.KindText, Body: "next day", Status: domain.StatusRead},
		{ID: 6, ChatID: 1, SenderID: 2, Timestamp: day1.Add(25 * time.Hour), Kind: domain.KindImage, Media: &domain.Media{Size: 1 << 21}},
	}
	hist := &memHistory{chats: map[domain.ChatID][]domain.Message{1: msgs}}
	names := NamesFunc(func(id domain.IdentityID) string { return map[domain.IdentityID]string{2: "Ayşe", 3: "Mehmet"}[id] })
	m := New(uikit.NewContext(theme.Plain()), hist, names)
	m.now, m.loc = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }, time.UTC
	m.Resize(60, 30)
	drive(m, m.Open(1))
	got := strings.Join(plainLines(m), "\n")
	for _, want := range []string{"Monday, 5 October 2026", "Yesterday", "Ayşe  09:00", "Mehmet  09:02", "Mehmet  09:30", "Today", "You  09:00", "next day \u2714\u2714", "[image \u00b7 2.0 MB]"} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Ayşe  09:01") != 0 {
		t.Error("a message within the group gap must not repeat the header")
	}
}

func TestRenderCache(t *testing.T) {
	m, _ := fixture(t, 3000, 80, 30)
	drive(m, m.Open(1))
	m.View()
	misses := m.misses
	m.View()
	if m.misses != misses {
		t.Fatalf("an unchanged frame rendered %d messages", m.misses-misses)
	}
	m.scrollUp(1)
	m.View()
	if m.misses-misses > 1 {
		t.Fatalf("scrolling one line rendered %d messages", m.misses-misses)
	}
}

func TestErrorAndReload(t *testing.T) {
	m, hist := fixture(t, 300, 80, 20)
	hist.fail = errors.New("disk on fire")
	drive(m, m.Open(1))
	if !strings.Contains(ansi.Strip(m.View()), "Could not load messages: disk on fire") {
		t.Fatalf("no error state: %q", ansi.Strip(m.View()))
	}
	hist.fail = nil
	drive(m, m.Update(ReloadMsg{Chat: 1}))
	if m.err != nil || len(m.win) == 0 {
		t.Fatal("reload did not recover")
	}
}

func TestResizeKeepsTheAnchor(t *testing.T) {
	m, _ := fixture(t, 1000, 80, 20)
	drive(m, m.Open(1))
	for range 5 {
		act(m, keymap.PageUp)
	}
	anchor := m.anchor
	m.Resize(120, 30)
	if m.anchor != anchor {
		t.Fatal("resizing moved the anchor to another message")
	}
	checkViewport(t, m)
}

func TestNamesChanged(t *testing.T) {
	m, _ := fixture(t, 100, 80, 20)
	drive(m, m.Open(1))
	m.View()
	misses := m.misses
	m.Update(NamesChangedMsg{})
	m.View()
	if m.misses == misses {
		t.Fatal("a name change must re-render visible messages")
	}
}

// TestAllocationsDoNotGrowWithHistory is budget B5: frame cost depends on
// what is visible, not on how long the chat is.
func TestAllocationsDoNotGrowWithHistory(t *testing.T) {
	allocs := func(n int) float64 {
		m, _ := fixture(t, n, 120, 40)
		drive(m, m.Open(1))
		for range 20 {
			act(m, keymap.PageUp)
		}
		m.View()
		return testing.AllocsPerRun(50, func() { _ = m.View() })
	}
	small, large := allocs(10_000), allocs(100_000)
	if large > small+2 {
		t.Fatalf("View allocates %.0f times with 100k messages vs %.0f with 10k", large, small)
	}
}

// TestScrollAllocationsDoNotGrowWithHistory extends budget B5 to the
// scroll path: a page up and down costs the same with 10k and 100k
// messages.
func TestScrollAllocationsDoNotGrowWithHistory(t *testing.T) {
	allocs := func(n int) float64 {
		m, _ := fixture(t, n, 120, 40)
		drive(m, m.Open(1))
		for range 20 {
			act(m, keymap.PageUp)
		}
		m.View()
		return testing.AllocsPerRun(50, func() {
			m.scrollUp(39)
			_ = m.View()
			m.scrollDown(39)
			_ = m.View()
		})
	}
	small, large := allocs(10_000), allocs(100_000)
	if large > small*1.5+4 {
		t.Fatalf("a scroll allocates %.0f times with 100k messages vs %.0f with 10k", large, small)
	}
}

// benchSizes are the history sizes from scenarios S1 and S2.
var benchSizes = []struct {
	name string
	n    int
}{{"10k", 10_000}, {"100k", 100_000}}

func BenchmarkView(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			m, _ := fixture(b, sz.n, 200, 50)
			drive(m, m.Open(1))
			m.View()
			b.ReportAllocs()
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}

// BenchmarkScroll pages through history and back, rendering each frame;
// pages that are not in the window are loaded through History.
func BenchmarkScroll(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			m, _ := fixture(b, sz.n, 200, 50)
			drive(m, m.Open(1))
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				a := keymap.PageUp
				if i/10%2 == 1 {
					a = keymap.PageDown
				}
				i++
				drive(m, m.Update(keymap.ActionMsg{Action: a}))
				_ = m.View()
			}
		})
	}
}

func TestRenderKinds(t *testing.T) {
	ts := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	msg := func(id int, kind domain.MessageKind, body string, media *domain.Media) domain.Message {
		return domain.Message{ID: domain.MessageID(id), ChatID: 1, WAID: domain.WAMessageID(fmt.Sprintf("W%d", id)), SenderID: 2,
			Timestamp: ts.Add(time.Duration(id) * 10 * time.Minute), Kind: kind, Body: body, Media: media}
	}
	msgs := []domain.Message{
		msg(0, domain.KindSystem, "Ali joined", nil),
		msg(1, domain.KindVideo, "", &domain.Media{Duration: 75 * time.Second, Size: 3 << 20}),
		msg(2, domain.KindAudio, "", &domain.Media{Duration: 42 * time.Second}),
		msg(3, domain.KindDocument, "", &domain.Media{FileName: "rapor\x1b[31m.pdf", Size: 340 << 10}),
		msg(4, domain.KindSticker, "", nil),
		msg(5, domain.KindLocation, "", nil),
		msg(6, domain.KindContact, "", nil),
		msg(7, domain.KindPoll, "", nil),
		msg(8, domain.KindUnsupported, "", nil),
		msg(9, domain.KindText, "forwarded text", nil),
		msg(10, domain.KindText, "reply text", nil),
		msg(11, domain.KindText, "gone", nil),
		msg(12, domain.KindText, "failed one", nil),
		msg(13, domain.KindText, "pending one", nil),
		msg(14, domain.KindText, strings.Repeat("line\n", 100), nil),
		msg(15, domain.KindText, "reply to missing", nil),
	}
	msgs[9].Flags = domain.FlagForwarded
	msgs[10].QuotedWAID = msgs[9].WAID
	msgs[11].RevokedAt = ts
	msgs[12].FromMe, msgs[12].Status = true, domain.StatusFailed
	msgs[13].FromMe, msgs[13].Status = true, domain.StatusPending
	msgs[15].QuotedWAID = "NOPE"
	hist := &memHistory{chats: map[domain.ChatID][]domain.Message{1: msgs}}
	m := New(uikit.NewContext(theme.Default()), hist, nil)
	m.now, m.loc = func() time.Time { return ts }, time.UTC
	m.Resize(70, 200)
	drive(m, m.Open(1))
	got := ansi.Strip(m.View())
	for _, want := range []string{
		"\u2014 Ali joined \u2014", "[video \u00b7 1:15 \u00b7 3.0 MB]", "[voice message \u00b7 0:42]",
		"[document \u00b7 rapor.pdf \u00b7 340 KB]", "[sticker]", "[location]", "[contact card]", "[poll]",
		"[unsupported message]", "\u21aa forwarded", "\u21a9 unknown: forwarded text", "This message was deleted",
		"failed one \u2717 not sent", "pending one \u25f7", "long message: press v", "\u21a9 quoted message",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	if strings.Contains(got, "[31m") {
		t.Error("an escape sequence in a file name reached the screen")
	}
	if humanSize(512) != "512 B" {
		t.Error("humanSize of bytes is wrong")
	}
}

// TestSelectionStaysWhileMessagesArrive: new messages keep a reader's
// selection, and only a selection on the newest message follows them.
func TestSelectionStaysWhileMessagesArrive(t *testing.T) {
	m, _ := fixture(t, 300, 80, 30)
	m.SetFocused(true)
	drive(m, m.Open(1))
	last := m.win[len(m.win)-1]
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{newMessage(last, "one")}})
	if m.selected != m.win[len(m.win)-1].ID {
		t.Fatal("a selection on the newest message must follow new messages")
	}
	act(m, keymap.Up)
	sel := m.selected
	next := newMessage(m.win[len(m.win)-1], "two")
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{next}})
	if m.selected != sel {
		t.Fatal("new messages moved the selection of a user reading older messages")
	}
	act(m, keymap.Down)
	act(m, keymap.Down)
	if m.selected != next.ID {
		t.Fatal("moving down must reach the newest message")
	}
}

func TestAppendBatchWithDuplicatesAndOlderMessages(t *testing.T) {
	m, _ := fixture(t, 200, 80, 30)
	drive(m, m.Open(1))
	last := m.win[len(m.win)-1]
	a := newMessage(last, "a")
	b := newMessage(a, "b")
	old := m.win[len(m.win)-3]
	older := domain.Message{ID: old.ID + 5000, ChatID: 1, Timestamp: old.Timestamp, Kind: domain.KindText, Body: "late history"}
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{a, b, a, older}})
	if m.win[len(m.win)-1].ID != b.ID || m.win[len(m.win)-2].ID != a.ID {
		t.Fatal("new messages must be appended in order")
	}
	count := 0
	for i := range m.win {
		if m.win[i].ID == a.ID {
			count++
		}
		if i > 0 && !m.win[i-1].Cursor().Before(m.win[i].Cursor()) {
			t.Fatalf("window out of order at %d", i)
		}
	}
	if count != 1 {
		t.Fatalf("a duplicate inside one batch was stored %d times", count)
	}
	if _, ok := m.idx[older.ID]; !ok {
		t.Fatal("an older message must be inserted in place")
	}
}

// driveCollect is drive, returning the OldestMsgs the view sent.
func driveCollect(m *Model, cmd tea.Cmd) []OldestMsg {
	var out []OldestMsg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case OldestMsg:
			out = append(out, msg)
		default:
			queue = append(queue, m.Update(msg))
		}
	}
	return out
}

// TestOlderOnDemand: moving up past the oldest stored message asks for
// older ones, once per oldest message; when they arrive they go before
// the window without counting as new, and the next move up asks again.
func TestOlderOnDemand(t *testing.T) {
	m, hist := fixture(t, 150, 80, 20)
	m.SetFocused(true)
	if asked := driveCollect(m, m.Open(1)); len(asked) != 0 {
		t.Fatal("opening a chat asks for nothing")
	}
	var asked []OldestMsg
	for range 40 {
		asked = append(asked, driveCollect(m, m.Update(keymap.ActionMsg{Action: keymap.PageUp}))...)
	}
	first := hist.chats[1][0]
	if len(asked) != 1 || asked[0].Chat != 1 || asked[0].Oldest.ID != first.ID {
		t.Fatalf("asked = %+v, want the oldest message once", asked)
	}
	// The phone's answer arrives through the store as new rows.
	var older []domain.Message
	for i := range 3 {
		older = append(older, domain.Message{ID: domain.MessageID(90000 + i), ChatID: 1, SenderID: first.SenderID, Kind: domain.KindText,
			Body: fmt.Sprint("older ", i), Timestamp: first.Timestamp.Add(time.Duration(i-3) * time.Hour)})
	}
	unseen := m.unseen
	m.Update(AppendMsg{Chat: 1, Msgs: older})
	if m.win[0].ID != older[0].ID || m.unseen != unseen {
		t.Fatalf("older messages go before the window without counting as new: first %d, unseen %d", m.win[0].ID, m.unseen)
	}
	asked = nil
	for range 5 {
		asked = append(asked, driveCollect(m, m.Update(keymap.ActionMsg{Action: keymap.PageUp}))...)
	}
	if len(asked) != 1 || asked[0].Oldest.ID != older[0].ID {
		t.Fatalf("asked = %+v, want the new oldest", asked)
	}
}

// TestOlderMessagesOutsideTheWindow: while the window does not reach the
// first message, an older message is left for paging to find, and none
// counts as new.
func TestOlderMessagesOutsideTheWindow(t *testing.T) {
	m, hist := fixture(t, 2000, 80, 20)
	drive(m, m.Open(1))
	if m.olderDone {
		t.Fatal("the fixture's window must not reach the first message")
	}
	n := len(m.win)
	m.Update(AppendMsg{Chat: 1, Msgs: []domain.Message{{ID: 99999, ChatID: 1, SenderID: hist.chats[1][0].SenderID, Kind: domain.KindText,
		Timestamp: hist.chats[1][0].Timestamp.Add(-time.Hour)}}})
	if len(m.win) != n || m.unseen != 0 {
		t.Fatalf("window %d → %d, unseen %d", n, len(m.win), m.unseen)
	}
}

// locHistory is a memHistory that can load around a message.
type locHistory struct{ *memHistory }

func (h locHistory) Around(_ context.Context, chat domain.ChatID, waid domain.WAMessageID, limit int) ([]domain.Message, error) {
	msgs, err := h.get(chat)
	i := slices.IndexFunc(msgs, func(m domain.Message) bool { return m.WAID == waid })
	if i < 0 || err != nil {
		return nil, err
	}
	return slices.Clone(msgs[max(i-limit/2, 0):min(i+limit-limit/2, len(msgs))]), nil
}

func TestJumpToQuoted(t *testing.T) {
	m, hist := fixture(t, 3000, 80, 20)
	m.hist = locHistory{hist}
	msgs := hist.chats[1]
	near, far := msgs[len(msgs)-10], msgs[100]
	last := &msgs[len(msgs)-1]
	last.QuotedWAID = near.WAID
	msgs[len(msgs)-2].QuotedWAID = far.WAID
	msgs[len(msgs)-2].Quote = &domain.Quote{SenderID: far.SenderID, Kind: far.Kind, Body: "far away"}
	msgs[len(msgs)-3].QuotedWAID = "NOT-STORED"
	msgs[len(msgs)-4].QuotedWAID = ""
	m.SetFocused(true)
	drive(m, m.Open(1))

	// In the window: a jump selects it.
	collect := func(a keymap.Action) []tea.Msg {
		var out []tea.Msg
		queue := []tea.Cmd{m.Update(keymap.ActionMsg{Action: a})}
		for len(queue) > 0 {
			c := queue[0]
			queue = queue[1:]
			if c == nil {
				continue
			}
			switch msg := c().(type) {
			case tea.BatchMsg:
				queue = append(queue, msg...)
			case nil:
			case NoticeMsg:
				out = append(out, msg)
			default:
				queue = append(queue, m.Update(msg))
			}
		}
		return out
	}
	collect(keymap.JumpQuoted)
	if m.selected != near.ID {
		t.Fatalf("selected %d, want the quoted %d", m.selected, near.ID)
	}
	// The rendered quote of a reply outside the window uses the preview.
	act(m, keymap.Bottom)
	act(m, keymap.Up)
	if !slices.ContainsFunc(plainLines(m), func(l string) bool { return strings.Contains(l, "↩") && strings.Contains(l, "far away") }) {
		t.Fatalf("the preview of an unloaded quote must show:\n%s", strings.Join(plainLines(m), "\n"))
	}
	// Outside the window: the window moves around it.
	collect(keymap.JumpQuoted)
	if m.selected != far.ID || m.newerDone || m.follow {
		t.Fatalf("selected %d, want %d (newerDone %v)", m.selected, far.ID, m.newerDone)
	}
	if !slices.ContainsFunc(plainLines(m), func(l string) bool { return strings.Contains(l, far.Body[:min(10, len(far.Body))]) }) {
		t.Fatalf("the quoted message must be on screen:\n%s", strings.Join(plainLines(m), "\n"))
	}
	// Back to the newest, and a quote that is not stored.
	act(m, keymap.Bottom)
	act(m, keymap.Up)
	act(m, keymap.Up)
	if n := collect(keymap.JumpQuoted); len(n) != 1 || !strings.Contains(n[0].(NoticeMsg).Text, "not stored") {
		t.Fatalf("notices = %v", n)
	}
	act(m, keymap.Up)
	if n := collect(keymap.JumpQuoted); len(n) != 1 || !strings.Contains(n[0].(NoticeMsg).Text, "does not quote") {
		t.Fatalf("notices = %v", n)
	}
}

func TestReactionLine(t *testing.T) {
	m, hist := fixture(t, 20, 80, 20)
	msgs := hist.chats[1]
	msgs[len(msgs)-1].Reactions = []domain.ReactionCount{{Emoji: "\U0001F602", Count: 3}, {Emoji: "\u202e!", Count: 1, Mine: true}}
	drive(m, m.Open(1))
	view := strings.Join(plainLines(m), "\n")
	if !strings.Contains(view, "\U0001F602 3") || !strings.Contains(view, "! 1") || strings.Contains(view, "\u202e") {
		t.Fatalf("reactions must show, sanitized:\n%s", view)
	}
	before := m.height(len(m.win) - 1)
	m.Update(ReplaceMsg{Chat: 1, Msgs: []domain.Message{func() domain.Message {
		x := m.win[len(m.win)-1]
		x.Reactions, x.Revision = nil, x.Revision+1
		return x
	}()}})
	if m.height(len(m.win)-1) != before-1 {
		t.Fatal("a message without reactions has no reactions line")
	}
}

func TestMentionsShowNames(t *testing.T) {
	m, hist := fixture(t, 20, 80, 20)
	msgs := hist.chats[1]
	last := &msgs[len(msgs)-1]
	last.Body, last.Kind, last.QuotedWAID = "@905 bak, @9051 değil, @777 bilinmiyor", domain.KindText, ""
	last.Mentions = []domain.Mention{{Token: "@905", Identity: last.SenderID}, {Token: "@777"}}
	last.Flags |= domain.FlagMentionsMe
	drive(m, m.Open(1))
	name := m.names.DisplayName(last.SenderID)
	view := strings.Join(plainLines(m), "\n")
	for _, want := range []string{"@" + name + " bak", "@9051 değil", "@777 bilinmiyor", "@ mentions you"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if got := replaceToken("@1 @12 @1", "@1", "X"); got != "X @12 X" {
		t.Fatalf("replaceToken = %q", got)
	}
}

func TestEphemeralAndViewOnceRendering(t *testing.T) {
	m, hist := fixture(t, 20, 80, 20)
	msgs := hist.chats[1]
	last := &msgs[len(msgs)-1]
	last.Kind, last.Body, last.QuotedWAID = domain.KindImage, "", ""
	last.Media = &domain.Media{MIME: "image/jpeg"}
	last.Flags = domain.FlagViewOnce | domain.FlagEphemeral
	drive(m, m.Open(1))
	view := strings.Join(plainLines(m), "\n")
	if !strings.Contains(view, "[view once image · open it on your phone]") || !strings.Contains(view, "⏱") {
		t.Fatalf("view:\n%s", view)
	}
}
