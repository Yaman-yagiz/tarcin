// SPDX-License-Identifier: GPL-3.0-or-later

// Package chatlist is the chat list pane: conversations (never contacts)
// in list order, rendered as a virtualized list. Only visible rows are
// rendered, each from a per-row cache, and an update moves one row
// instead of rebuilding the list (docs/architecture/ui.md §4).
package chatlist

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Row is the chat list's view of one chat. Text fields are raw; the list
// sanitizes them.
type Row struct {
	ID           domain.ChatID
	Title        string
	LastActivity time.Time
	Unread       int
	PinnedAt     time.Time // zero: not pinned
	Muted        bool
	Archived     bool
	Typing       bool
	Group        bool          // a group, which has a members view
	Disappearing time.Duration // the disappearing-message timer; 0: off

	rev    uint32 // bumped on every update; part of the render-cache key
	title  string // sanitized title
	folded string // search key of the title
}

// LoadMsg replaces every row of the chat list.
type LoadMsg struct {
	Rows []Row
}

// UpdateMsg inserts or updates rows of the chat list.
type UpdateMsg struct {
	Rows []Row
}

// OpenMsg asks the root model to open a chat. The chat list emits it when
// the user presses Enter; moving the cursor never opens a chat.
type OpenMsg struct {
	ID domain.ChatID
}

// Model is the chat list. Create it with New.
type Model struct {
	ctx *uikit.Context

	rows    []*Row // every chat, in list order
	byID    map[domain.ChatID]*Row
	visible []*Row // rows shown: archive and filter applied
	pos     map[domain.ChatID]int
	dirty   bool // visible must be rebuilt

	cursor  domain.ChatID // selected chat; zero when the list is empty
	offset  int           // index in visible of the first row on screen
	w, h    int
	focused bool

	showArchived bool
	filter       textfold.Query

	now    func() time.Time
	loc    *time.Location
	cache  map[domain.ChatID]*cached
	labels map[domain.ChatID]labelEntry
	// misses counts rendered rows that were not in the cache, lookups all.
	misses, lookups int
}

// New returns an empty chat list.
func New(ctx *uikit.Context) *Model {
	return &Model{
		ctx:    ctx,
		byID:   map[domain.ChatID]*Row{},
		pos:    map[domain.ChatID]int{},
		now:    time.Now,
		loc:    time.Local,
		cache:  map[domain.ChatID]*cached{},
		labels: map[domain.ChatID]labelEntry{},
	}
}

// before reports whether a sorts before b: pinned chats first (most
// recently pinned first), then by latest activity, then by ID. It agrees
// with domain.Chat.ListBefore.
func before(a, b *Row) bool {
	ap, bp := !a.PinnedAt.IsZero(), !b.PinnedAt.IsZero()
	switch {
	case ap != bp:
		return ap
	case !a.PinnedAt.Equal(b.PinnedAt):
		return a.PinnedAt.After(b.PinnedAt)
	case !a.LastActivity.Equal(b.LastActivity):
		return a.LastActivity.After(b.LastActivity)
	}
	return a.ID > b.ID
}

func compare(a, b *Row) int {
	switch {
	case a.ID == b.ID:
		return 0
	case before(a, b):
		return -1
	}
	return 1
}

// Set replaces all rows, e.g. on the initial load.
func (m *Model) Set(rows []Row) {
	m.rows = m.rows[:0]
	clear(m.byID)
	clear(m.cache)
	for i := range rows {
		r := prepare(rows[i], 0)
		m.rows = append(m.rows, r)
		m.byID[r.ID] = r
	}
	slices.SortFunc(m.rows, compare)
	m.dirty = true
}

// Upsert inserts a row or replaces the row with the same ID, moving it to
// its new position. It costs a binary search and one slice move.
func (m *Model) Upsert(row Row) {
	if old, ok := m.byID[row.ID]; ok {
		i, found := slices.BinarySearchFunc(m.rows, old, compare)
		if !found || m.rows[i] != old {
			panic("chatlist: rows out of order")
		}
		m.rows = slices.Delete(m.rows, i, i+1)
		row = *prepare(row, old.rev+1)
		*old = row
	} else {
		m.byID[row.ID] = prepare(row, 0)
	}
	r := m.byID[row.ID]
	i, _ := slices.BinarySearchFunc(m.rows, r, compare)
	m.rows = slices.Insert(m.rows, i, r)
	m.dirty = true
}

// NextUnread returns the next (dir 1) or previous (-1) shown chat with
// unread messages after from, in list order, wrapping around.
func (m *Model) NextUnread(from domain.ChatID, dir int) (domain.ChatID, bool) {
	m.rebuild()
	n := len(m.visible)
	if n == 0 {
		return 0, false
	}
	start, ok := m.pos[from]
	if !ok {
		start = -1
		if dir < 0 {
			start = n
		}
	}
	for k := 1; k <= n; k++ {
		i := ((start+dir*k)%n + n) % n
		if r := m.visible[i]; r.Unread > 0 && r.ID != from {
			return r.ID, true
		}
	}
	return 0, false
}

// Disappearing returns a chat's disappearing-message timer.
func (m *Model) Disappearing(id domain.ChatID) time.Duration {
	if r, ok := m.byID[id]; ok {
		return r.Disappearing
	}
	return 0
}

// CacheStats returns how many rows were looked up in the render cache and
// missed it, and how many rows there are, for the debug pane.
func (m *Model) CacheStats() (lookups, misses, rows int) { return m.lookups, m.misses, len(m.rows) }

// IsMuted reports whether a chat is muted.
func (m *Model) IsMuted(id domain.ChatID) bool {
	r, ok := m.byID[id]
	return ok && r.Muted
}

// IsTyping reports whether someone types in a chat.
func (m *Model) IsTyping(id domain.ChatID) bool {
	r, ok := m.byID[id]
	return ok && r.Typing
}

// SetTyping shows or hides the typing indicator of a chat. It does not
// change the row's position.
func (m *Model) SetTyping(id domain.ChatID, typing bool) {
	if r, ok := m.byID[id]; ok && r.Typing != typing {
		r.Typing = typing
		r.rev++
	}
}

// Remove deletes a row.
func (m *Model) Remove(id domain.ChatID) {
	old, ok := m.byID[id]
	if !ok {
		return
	}
	i, _ := slices.BinarySearchFunc(m.rows, old, compare)
	m.rows = slices.Delete(m.rows, i, i+1)
	delete(m.byID, id)
	delete(m.cache, id)
	delete(m.labels, id)
	m.dirty = true
}

func prepare(r Row, rev uint32) *Row {
	r.rev = rev
	r.title = sanitize.Line(r.Title)
	r.folded = textfold.Fold(r.title)
	return &r
}

// SetClock replaces the clock and time zone used for time labels.
func (m *Model) SetClock(now func() time.Time, loc *time.Location) {
	m.now, m.loc = now, loc
	clear(m.labels)
	clear(m.cache)
}

// Len returns the number of rows shown.
func (m *Model) Len() int {
	m.rebuild()
	return len(m.visible)
}

// Selected returns the selected chat.
func (m *Model) Selected() (domain.ChatID, bool) {
	m.rebuild()
	return m.cursor, m.cursor != 0
}

// Select moves the cursor to a chat, if it is shown.
func (m *Model) Select(id domain.ChatID) {
	m.rebuild()
	if _, ok := m.pos[id]; ok {
		m.cursor = id
		m.scrollToCursor()
	}
}

// SetFilter shows only chats whose title matches q.
func (m *Model) SetFilter(q string) {
	m.filter = textfold.NewQuery(q)
	m.dirty = true
}

// SetShowArchived shows or hides archived chats.
func (m *Model) SetShowArchived(show bool) {
	m.showArchived = show
	m.dirty = true
}

// rebuild recomputes the visible rows after changes and keeps the cursor
// on the same chat when possible.
func (m *Model) rebuild() {
	if !m.dirty {
		return
	}
	m.dirty = false
	m.visible = m.visible[:0]
	clear(m.pos)
	for _, r := range m.rows {
		if (r.Archived && !m.showArchived) || !m.filter.MatchFolded(r.folded) {
			continue
		}
		m.pos[r.ID] = len(m.visible)
		m.visible = append(m.visible, r)
	}
	if _, ok := m.pos[m.cursor]; !ok {
		m.cursor = 0
		if len(m.visible) > 0 {
			m.cursor = m.visible[0].ID
			m.offset = 0
		}
	}
	// Reordering can move the selected chat; keep it on screen.
	m.scrollToCursor()
	m.clampOffset()
}

// Resize implements uikit.Pane.
func (m *Model) Resize(w, h int) {
	m.w, m.h = w, h
	m.rebuild()
	m.scrollToCursor()
}

// SetFocused implements uikit.Pane.
func (m *Model) SetFocused(focused bool) { m.focused = focused }

// Title returns the sanitized title of a chat.
func (m *Model) Title(id domain.ChatID) (string, bool) {
	r, ok := m.byID[id]
	if !ok {
		return "", false
	}
	return r.title, true
}

// IsGroup reports whether a chat is a group.
func (m *Model) IsGroup(id domain.ChatID) bool {
	r, ok := m.byID[id]
	return ok && r.Group
}

// Update implements uikit.Pane.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	var am keymap.ActionMsg
	switch msg := msg.(type) {
	case LoadMsg:
		m.Set(msg.Rows)
		return nil
	case UpdateMsg:
		for i := range msg.Rows {
			m.Upsert(msg.Rows[i])
		}
		return nil
	case keymap.ActionMsg:
		am = msg
	default:
		return nil
	}
	m.rebuild()
	if len(m.visible) == 0 {
		return nil
	}
	idx := m.pos[m.cursor]
	page := max(m.h, 1)
	switch am.Action {
	case keymap.Down:
		idx++
	case keymap.Up:
		idx--
	case keymap.Top:
		idx = 0
	case keymap.Bottom:
		idx = len(m.visible) - 1
	case keymap.HalfPageDown:
		idx += max(page/2, 1)
	case keymap.HalfPageUp:
		idx -= max(page/2, 1)
	case keymap.PageDown:
		idx += page
	case keymap.PageUp:
		idx -= page
	case keymap.Open:
		id := m.cursor
		return func() tea.Msg { return OpenMsg{ID: id} }
	default:
		return nil
	}
	idx = min(max(idx, 0), len(m.visible)-1)
	m.cursor = m.visible[idx].ID
	m.scrollToCursor()
	return nil
}

// scrollToCursor keeps the cursor on screen with a margin of up to two
// rows above and below it.
func (m *Model) scrollToCursor() {
	idx, ok := m.pos[m.cursor]
	if !ok || m.h <= 0 {
		return
	}
	margin := min(2, (m.h-1)/2)
	if idx < m.offset+margin {
		m.offset = idx - margin
	}
	if idx > m.offset+m.h-1-margin {
		m.offset = idx - (m.h - 1 - margin)
	}
	m.clampOffset()
}

func (m *Model) clampOffset() {
	m.offset = min(m.offset, max(len(m.visible)-m.h, 0))
	m.offset = max(m.offset, 0)
}
