// SPDX-License-Identifier: GPL-3.0-or-later

package chatlist

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

var now = time.Date(2026, 10, 11, 15, 30, 0, 0, time.UTC)

func newList(t *testing.T, w, h int, rows ...Row) *Model {
	t.Helper()
	m := New(uikit.NewContext(theme.Default()))
	m.now = func() time.Time { return now }
	m.loc = time.UTC
	m.Set(rows)
	m.Resize(w, h)
	return m
}

func row(id, minsAgo int) Row {
	return Row{ID: domain.ChatID(id), Title: fmt.Sprintf("Chat %d", id), LastActivity: now.Add(-time.Duration(minsAgo) * time.Minute)}
}

func ids(m *Model) []domain.ChatID {
	m.rebuild()
	var out []domain.ChatID
	for _, r := range m.visible {
		out = append(out, r.ID)
	}
	return out
}

func act(m *Model, a keymap.Action) { m.Update(keymap.ActionMsg{Action: a}) }

func TestOrder(t *testing.T) {
	pinned := row(9, 500)
	pinned.PinnedAt = now.Add(-time.Hour)
	pinnedLater := row(8, 900)
	pinnedLater.PinnedAt = now
	m := newList(t, 40, 10, row(1, 10), row(2, 5), row(3, 10), pinned, pinnedLater)
	want := []domain.ChatID{8, 9, 2, 3, 1} // pins (latest first), activity, then higher ID
	if got := ids(m); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestOrderMatchesDomain cross-checks the list order with
// domain.Chat.ListBefore over random rows and updates.
func TestOrderMatchesDomain(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data
	m := newList(t, 40, 10)
	for range 2000 {
		x := Row{ID: domain.ChatID(1 + r.IntN(200)), LastActivity: now.Add(-time.Duration(r.IntN(50)) * time.Minute)}
		if r.IntN(10) == 0 {
			x.PinnedAt = now.Add(-time.Duration(r.IntN(5)) * time.Hour)
		}
		if r.IntN(20) == 0 {
			m.Remove(x.ID)
			continue
		}
		m.Upsert(x)
	}
	for i := 1; i < len(m.rows); i++ {
		a, b := m.rows[i-1], m.rows[i]
		ca := domain.Chat{ID: a.ID, LastActivity: a.LastActivity, PinnedAt: a.PinnedAt}
		cb := domain.Chat{ID: b.ID, LastActivity: b.LastActivity, PinnedAt: b.PinnedAt}
		if !ca.ListBefore(&cb) {
			t.Fatalf("rows %d and %d are out of domain order", a.ID, b.ID)
		}
	}
	if len(m.rows) != len(m.byID) {
		t.Fatalf("%d rows but %d indexed", len(m.rows), len(m.byID))
	}
}

func TestCursorFollowsTheChat(t *testing.T) {
	m := newList(t, 40, 3, row(1, 1), row(2, 2), row(3, 3), row(4, 4), row(5, 5))
	act(m, keymap.Down)
	act(m, keymap.Down)
	if id, _ := m.Selected(); id != 3 {
		t.Fatalf("selected %d, want 3", id)
	}
	// A new message moves chat 5 to the top; the cursor stays on chat 3.
	r := row(5, 0)
	r.Unread = 1
	m.Upsert(r)
	if id, _ := m.Selected(); id != 3 {
		t.Fatalf("after an update elsewhere the cursor moved to %d", id)
	}
	if got := ids(m); got[0] != 5 {
		t.Fatalf("updated chat did not move to the top: %v", got)
	}
	// The selected chat itself moves to the top and stays selected.
	m.Upsert(row(3, -1))
	if id, _ := m.Selected(); id != 3 || m.pos[3] != 0 || m.offset != 0 {
		t.Fatalf("selected chat %d at %d, offset %d", id, m.pos[3], m.offset)
	}
}

func TestNavigation(t *testing.T) {
	var rows []Row
	for i := 1; i <= 30; i++ {
		rows = append(rows, row(i, i))
	}
	m := newList(t, 40, 10, rows...)
	steps := []struct {
		a    keymap.Action
		want domain.ChatID
	}{
		{keymap.Up, 1}, // already at the top
		{keymap.Down, 2},
		{keymap.HalfPageDown, 7},
		{keymap.PageDown, 17},
		{keymap.Bottom, 30},
		{keymap.Down, 30}, // already at the bottom
		{keymap.PageUp, 20},
		{keymap.HalfPageUp, 15},
		{keymap.Top, 1},
	}
	for _, s := range steps {
		act(m, s.a)
		id, _ := m.Selected()
		if id != s.want {
			t.Fatalf("after %s: selected %d, want %d", s.a, id, s.want)
		}
		idx := m.pos[id]
		if idx < m.offset || idx >= m.offset+m.h {
			t.Fatalf("after %s: cursor %d is off screen (offset %d)", s.a, idx, m.offset)
		}
	}
}

func TestScrollMargin(t *testing.T) {
	var rows []Row
	for i := 1; i <= 20; i++ {
		rows = append(rows, row(i, i))
	}
	m := newList(t, 40, 6, rows...)
	for range 6 {
		act(m, keymap.Down)
	}
	// Cursor index 6 with a margin of two keeps rows 7 and 8 visible below.
	if m.offset != 3 {
		t.Fatalf("offset = %d, want 3", m.offset)
	}
	act(m, keymap.Bottom)
	if m.offset != 14 {
		t.Fatalf("offset at the bottom = %d, want 14", m.offset)
	}
}

func TestOpenNeedsEnter(t *testing.T) {
	m := newList(t, 40, 5, row(1, 1), row(2, 2))
	if cmd := m.Update(keymap.ActionMsg{Action: keymap.Down}); cmd != nil {
		t.Fatal("moving the cursor must not open a chat")
	}
	cmd := m.Update(keymap.ActionMsg{Action: keymap.Open})
	if cmd == nil {
		t.Fatal("open returned no command")
	}
	if got := cmd(); got != (OpenMsg{ID: 2}) {
		t.Fatalf("open emitted %#v", got)
	}
	empty := newList(t, 40, 5)
	if cmd := empty.Update(keymap.ActionMsg{Action: keymap.Open}); cmd != nil {
		t.Fatal("an empty list must not open anything")
	}
}

func TestArchiveAndFilter(t *testing.T) {
	archived := row(3, 3)
	archived.Archived = true
	ayse := row(4, 4)
	ayse.Title = "Ayşe Yılmaz"
	m := newList(t, 40, 10, row(1, 1), row(2, 2), archived, ayse)
	if got := ids(m); slices.Contains(got, 3) {
		t.Fatalf("archived chat is shown: %v", got)
	}
	m.SetShowArchived(true)
	if got := ids(m); !slices.Contains(got, 3) {
		t.Fatalf("archived chat is hidden after SetShowArchived: %v", got)
	}
	m.Select(2)
	m.SetFilter("ayse yil")
	if got := ids(m); !slices.Equal(got, []domain.ChatID{4}) {
		t.Fatalf("filter = %v, want [4]", got)
	}
	if id, _ := m.Selected(); id != 4 {
		t.Fatalf("selection must move into the filtered rows, got %d", id)
	}
	m.SetFilter("nobody")
	if m.Len() != 0 || !strings.Contains(ansi.Strip(m.View()), "No matching chats") {
		t.Fatalf("empty filter result renders %q", ansi.Strip(m.View()))
	}
	if _, ok := m.Selected(); ok {
		t.Fatal("nothing can be selected in an empty list")
	}
}

func TestTimeLabel(t *testing.T) {
	tests := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-5 * time.Minute), "15:25"},
		{time.Date(2026, 10, 11, 0, 1, 0, 0, time.UTC), "00:01"},
		{time.Date(2026, 10, 10, 23, 59, 0, 0, time.UTC), "Sat"},
		{now.Add(-5 * 24 * time.Hour), "Tue"},
		{now.Add(-8 * 24 * time.Hour), "03 Oct"},
		{time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC), "2025"},
		{time.Time{}, ""},
	}
	for _, tt := range tests {
		if got := timeLabel(tt.at, now, time.UTC); got != tt.want {
			t.Errorf("timeLabel(%v) = %q, want %q", tt.at, got, tt.want)
		}
	}
	// 22:00 UTC on Saturday is 01:00 on Sunday in Turkey, the same day as now.
	trt := time.FixedZone("TRT", 3*3600)
	if got := timeLabel(time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC), now, trt); got != "01:00" {
		t.Errorf("timeLabel in TRT = %q, want 01:00", got)
	}
}

func TestRowRendering(t *testing.T) {
	unread := row(1, 5)
	unread.Title, unread.Unread = "Aile", 120
	muted := row(2, 60*24*2)
	muted.Title, muted.Unread, muted.Muted = "Komşular", 3, true
	pinned := row(3, 1)
	pinned.Title, pinned.PinnedAt = "İş", now
	hostile := row(4, 10)
	hostile.Title = "\x1b]0;pwned\x07Evil\u202eName\nNext"
	m := newList(t, 40, 10, unread, muted, pinned, hostile)
	m.SetFocused(true)

	lines := strings.Split(m.View(), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	for i, l := range lines {
		if w := m.ctx.Width.StringWidth(l); w > 40 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"İş", "\u25b4", "Aile", "99+", "15:25", "Komşular", "~ 3", "Fri", "EvilName Next"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendering lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "pwned") || strings.Contains(plain, "\u202e") {
		t.Errorf("unsanitized title reached the screen:\n%q", plain)
	}
	// The selected row (the pinned chat, first in order) spans the width.
	if w := m.ctx.Width.StringWidth(lines[0]); w != 40 {
		t.Errorf("selected row is %d cells wide, want 40", w)
	}
}

func TestNarrowRowsKeepTheCount(t *testing.T) {
	r := row(1, 5)
	r.Title, r.Unread, r.Muted, r.PinnedAt = strings.Repeat("Çok uzun grup adı ", 5), 7, true, now
	for w := 1; w <= 30; w++ {
		m := newList(t, w, 3, r)
		line := m.View()
		if got := m.ctx.Width.StringWidth(line); got > w {
			t.Fatalf("width %d: line is %d cells", w, got)
		}
		if w >= 12 && !strings.Contains(ansi.Strip(line), "7") {
			t.Fatalf("width %d: the unread count was dropped: %q", w, ansi.Strip(line))
		}
	}
}

func TestRenderCache(t *testing.T) {
	var rows []Row
	for i := 1; i <= 100; i++ {
		rows = append(rows, row(i, i))
	}
	m := newList(t, 40, 10, rows...)
	m.View()
	first := m.misses
	if first != 10 {
		t.Fatalf("first frame rendered %d rows, want only the 10 visible", first)
	}
	m.View()
	if m.misses != first {
		t.Fatalf("an unchanged frame re-rendered %d rows", m.misses-first)
	}
	r := row(5, 5)
	r.Unread = 2
	m.Upsert(r)
	m.View()
	if m.misses != first+1 {
		t.Fatalf("one updated row caused %d re-renders", m.misses-first)
	}
	act(m, keymap.Down) // the selection moves: two rows change
	m.View()
	if m.misses != first+3 {
		t.Fatalf("moving the cursor caused %d re-renders, want 2", m.misses-first-1)
	}
}

func TestEmptyAndZeroSize(t *testing.T) {
	m := newList(t, 0, 0, row(1, 1))
	if m.View() != "" {
		t.Fatal("a zero-size list must render nothing")
	}
	e := newList(t, 30, 5)
	if !strings.Contains(ansi.Strip(e.View()), "No chats") {
		t.Fatalf("empty list renders %q", ansi.Strip(e.View()))
	}
	e.Remove(42) // removing an unknown chat is a no-op
	if cmd := e.Update(keymap.ActionMsg{Action: keymap.Down}); cmd != nil {
		t.Fatal("navigation in an empty list returned a command")
	}
}

func BenchmarkView(b *testing.B) {
	m := New(uikit.NewContext(theme.Default()))
	var rows []Row
	for i := 1; i <= 5000; i++ {
		rows = append(rows, Row{ID: domain.ChatID(i), Title: fmt.Sprintf("Grup %d Çağrı", i), LastActivity: now.Add(-time.Duration(i) * time.Minute), Unread: i % 7})
	}
	m.Set(rows)
	m.Resize(40, 50)
	b.Run("cached", func(b *testing.B) {
		for b.Loop() {
			_ = m.View()
		}
	})
	b.Run("scrolling", func(b *testing.B) {
		for b.Loop() {
			m.Update(keymap.ActionMsg{Action: keymap.PageDown})
			_ = m.View()
		}
	})
}

func BenchmarkUpsert(b *testing.B) {
	m := New(uikit.NewContext(theme.Default()))
	var rows []Row
	for i := 1; i <= 5000; i++ {
		rows = append(rows, Row{ID: domain.ChatID(i), LastActivity: now.Add(-time.Duration(i) * time.Minute)})
	}
	m.Set(rows)
	i := 0
	for b.Loop() {
		i++
		m.Upsert(Row{ID: domain.ChatID(1 + i%5000), LastActivity: now.Add(time.Duration(i) * time.Second)})
	}
}

func TestMessages(t *testing.T) {
	m := newList(t, 40, 10)
	m.Update(LoadMsg{Rows: []Row{row(1, 1), row(2, 2)}})
	m.Update(UpdateMsg{Rows: []Row{row(3, 0), row(1, -5)}})
	if got := ids(m); !slices.Equal(got, []domain.ChatID{1, 3, 2}) {
		t.Fatalf("order after messages = %v", got)
	}
	if title, ok := m.Title(3); !ok || title != "Chat 3" {
		t.Fatalf("Title(3) = %q, %v", title, ok)
	}
	if _, ok := m.Title(42); ok {
		t.Fatal("Title of an unknown chat must report false")
	}
	if cmd := m.Update(struct{}{}); cmd != nil {
		t.Fatal("unknown messages must be ignored")
	}
}

func TestSetTyping(t *testing.T) {
	m := newList(t, 40, 5, row(1, 1), row(2, 2))
	m.SetTyping(2, true)
	if !strings.Contains(m.View(), symTyping) {
		t.Fatal("typing indicator not shown")
	}
	m.SetTyping(2, false)
	m.SetTyping(99, true) // unknown chats are ignored
	if strings.Contains(m.View(), symTyping) {
		t.Fatal("typing indicator not cleared")
	}
	if got := ids(m); !slices.Equal(got, []domain.ChatID{1, 2}) {
		t.Fatalf("typing must not reorder rows: %v", got)
	}
}

func TestTimeLabelCacheFollowsTheDay(t *testing.T) {
	m := newList(t, 40, 5, row(1, 5))
	if !strings.Contains(ansi.Strip(m.View()), "15:25") {
		t.Fatalf("today's label missing: %q", ansi.Strip(m.View()))
	}
	m.now = func() time.Time { return now.Add(24 * time.Hour) }
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Sun") {
		t.Fatalf("after midnight the label must become the weekday: %q", got)
	}
	m.Upsert(row(1, -24*60+1)) // new activity "today" in the advanced clock
	if got := ansi.Strip(m.View()); strings.Contains(got, "Sun") {
		t.Fatalf("new activity must refresh the label: %q", got)
	}
}
