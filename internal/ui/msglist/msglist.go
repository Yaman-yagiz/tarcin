// SPDX-License-Identifier: GPL-3.0-or-later

// Package msglist is the message view: a virtualized list over a bounded
// window of one chat's history (docs/architecture/ui.md §3).
//
// The window holds at most MaxWindow messages around the viewport and is
// extended a page at a time through History, from tea.Cmd goroutines,
// never from Update or View. The scroll position is an anchor (the
// bottom-most visible message and how many of its lines are hidden below
// the viewport), so loading older pages or receiving new messages never
// makes the view jump. View joins only the cached lines of the visible
// messages.
package msglist

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Window and paging limits.
const (
	// PageSize is the number of messages loaded per request.
	PageSize = 100
	// MaxWindow caps the messages held in memory for the open chat.
	MaxWindow = 600
	// prefetch is how close (in messages) the viewport may get to the
	// window's edge before the next page is requested.
	prefetch = 30
	// cacheSize is the number of rendered messages kept.
	cacheSize = 4096
	// loadTimeout bounds every history request.
	loadTimeout = 10 * time.Second
)

// History loads pages of a chat's messages. Every method returns
// messages in ascending order (oldest first). It is called from tea.Cmd
// goroutines and must be safe for concurrent use.
type History interface {
	// Latest returns the newest limit messages.
	Latest(ctx context.Context, chat domain.ChatID, limit int) ([]domain.Message, error)
	// Before returns up to limit messages older than c.
	Before(ctx context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error)
	// After returns up to limit messages newer than c.
	After(ctx context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error)
}

// Locator is implemented by histories that can load the messages around
// one, for jumping to a quoted message outside the window.
type Locator interface {
	// Around returns up to limit messages around the one with waid,
	// oldest first, or none if it is not stored.
	Around(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID, limit int) ([]domain.Message, error)
}

// Names resolves sender display names from memory. It is called from
// View and must not block.
type Names interface {
	DisplayName(id domain.IdentityID) string
}

// NamesFunc adapts a function to Names.
type NamesFunc func(domain.IdentityID) string

// DisplayName implements Names.
func (f NamesFunc) DisplayName(id domain.IdentityID) string { return f(id) }

type direction uint8

const (
	dirLatest direction = iota
	dirOlder
	dirNewer
	dirAround // the messages around a target, replacing the window
)

// PageMsg carries the result of a history request. It is produced only
// by the commands this package returns.
type PageMsg struct {
	chat   domain.ChatID
	gen    uint64
	dir    direction
	limit  int
	target domain.WAMessageID // dirAround only
	msgs   []domain.Message
	err    error
}

// NoticeMsg is a short message for the status line.
type NoticeMsg struct{ Text string }

// AppendMsg delivers new messages of a chat as they arrive.
type AppendMsg struct {
	Chat domain.ChatID
	Msgs []domain.Message
}

// ReplaceMsg delivers updated messages (edits, receipts, reactions). Each
// update must carry a higher Revision.
type ReplaceMsg struct {
	Chat domain.ChatID
	Msgs []domain.Message
}

// ReloadMsg asks the view to reload a chat from history, e.g. after the
// bridge dropped updates for it.
type ReloadMsg struct {
	Chat domain.ChatID
}

// NamesChangedMsg tells the view that display names changed.
type NamesChangedMsg struct{}

// OldestMsg is sent by the view when the user moves up past the oldest
// stored message of a chat: the root asks the phone for older ones. It
// is sent once per oldest message.
type OldestMsg struct {
	Chat   domain.ChatID
	Oldest domain.Message
}

// MessageActionMsg asks the root model to apply an action to a message
// (reply, react, edit, …).
type MessageActionMsg struct {
	Action  keymap.Action
	Message domain.Message
}

// Model is the message view. Create it with New.
type Model struct {
	ctx   *uikit.Context
	hist  History
	names Names

	chat    domain.ChatID // zero: no chat open
	gen     uint64        // increments on every open; stale pages are dropped
	loading bool
	err     error

	win    []domain.Message // ascending
	idx    map[domain.MessageID]int
	byWAID map[domain.WAMessageID]int

	olderDone, newerDone       bool // the window reaches the chat's first / newest message
	loadingOlder, loadingNewer bool
	askedOldest                domain.MessageID // the oldest message an OldestMsg was sent for

	anchor   domain.MessageID // bottom-most visible message
	skip     int              // lines of the anchor hidden below the viewport
	follow   bool             // pinned to the newest message
	unseen   int              // messages that arrived below while not following
	selected domain.MessageID

	w, h     int
	focused  bool
	namesRev uint32

	cache   *uikit.LRU[cacheKey, []string]
	now     func() time.Time
	loc     *time.Location
	misses  int // rendered messages that were not cached
	lookups int // rendered messages looked up in the cache
}

// New returns a message view reading from hist.
func New(ctx *uikit.Context, hist History, names Names) *Model {
	return &Model{
		ctx:    ctx,
		hist:   hist,
		names:  names,
		idx:    map[domain.MessageID]int{},
		byWAID: map[domain.WAMessageID]int{},
		cache:  uikit.NewLRU[cacheKey, []string](cacheSize),
		now:    time.Now,
		loc:    time.Local,
	}
}

// SetClock replaces the clock and time zone used for times and day
// separators.
func (m *Model) SetClock(now func() time.Time, loc *time.Location) {
	m.now, m.loc = now, loc
	m.cache.Clear()
}

// SelectedMessage returns the selected message, if any.
func (m *Model) SelectedMessage() (domain.Message, bool) {
	i, ok := m.idx[m.selected]
	if !ok {
		return domain.Message{}, false
	}
	return m.win[i], true
}

// CacheStats returns how many rendered messages were looked up in the
// render cache, and how many of those were not in it, for the debug pane.
func (m *Model) CacheStats() (lookups, misses, window int) { return m.lookups, m.misses, len(m.win) }

// Chat returns the open chat.
func (m *Model) Chat() domain.ChatID { return m.chat }

// Open shows a chat and returns the command that loads its newest page.
func (m *Model) Open(chat domain.ChatID) tea.Cmd {
	m.chat = chat
	m.gen++
	m.loading, m.err = true, nil
	m.win = m.win[:0]
	m.reindex()
	m.olderDone, m.newerDone = false, false
	m.loadingOlder, m.loadingNewer = false, false
	m.askedOldest = 0
	m.anchor, m.skip, m.follow, m.unseen, m.selected = 0, 0, true, 0, 0
	return m.fetch(dirLatest, domain.Cursor{})
}

// OpenAt shows a chat around one of its messages, selected, as a search
// result opens. Without a Locator it opens the newest messages.
func (m *Model) OpenAt(chat domain.ChatID, waid domain.WAMessageID) tea.Cmd {
	loc, ok := m.hist.(Locator)
	if !ok || waid == "" {
		return m.Open(chat)
	}
	m.chat = chat
	m.win = m.win[:0]
	m.reindex()
	m.olderDone, m.newerDone = false, false
	m.askedOldest = 0
	m.anchor, m.skip, m.follow, m.unseen, m.selected = 0, 0, false, 0, 0
	m.err = nil
	return m.fetchAround(loc, waid)
}

// Close shows no chat, as after the open chat was removed. Pages still
// loading for it are dropped when they arrive.
func (m *Model) Close() {
	m.chat = 0
	m.gen++
	m.loading, m.err = false, nil
	m.win = m.win[:0]
	m.reindex()
	m.olderDone, m.newerDone = false, false
	m.loadingOlder, m.loadingNewer = false, false
	m.anchor, m.skip, m.follow, m.unseen, m.selected = 0, 0, true, 0, 0
}

func (m *Model) fetch(dir direction, c domain.Cursor) tea.Cmd {
	hist, chat, gen := m.hist, m.chat, m.gen
	switch dir {
	case dirOlder:
		m.loadingOlder = true
	case dirNewer:
		m.loadingNewer = true
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var msgs []domain.Message
		var err error
		switch dir {
		case dirLatest:
			msgs, err = hist.Latest(ctx, chat, PageSize)
		case dirOlder:
			msgs, err = hist.Before(ctx, chat, c, PageSize)
		case dirNewer:
			msgs, err = hist.After(ctx, chat, c, PageSize)
		}
		return PageMsg{chat: chat, gen: gen, dir: dir, limit: PageSize, msgs: msgs, err: err}
	}
}

// fetchAround loads the window around a message, for a jump to it.
func (m *Model) fetchAround(loc Locator, target domain.WAMessageID) tea.Cmd {
	chat := m.chat
	m.gen++ // pages on their way belong to the old window
	m.loading, m.loadingOlder, m.loadingNewer = true, false, false
	gen := m.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msgs, err := loc.Around(ctx, chat, target, PageSize)
		return PageMsg{chat: chat, gen: gen, dir: dirAround, limit: PageSize, target: target, msgs: msgs, err: err}
	}
}

// jumpQuoted selects the message the selected one quotes, loading the
// window around it if it is not in the window.
func (m *Model) jumpQuoted() tea.Cmd {
	i, ok := m.idx[m.selected]
	if !ok {
		return nil
	}
	notice := func(text string) tea.Cmd { return func() tea.Msg { return NoticeMsg{Text: text} } }
	msg := &m.win[i]
	if msg.QuotedWAID == "" {
		return notice("This message does not quote another")
	}
	if j, ok := m.byWAID[msg.QuotedWAID]; ok {
		m.selected = m.win[j].ID
		m.ensureVisible(j)
		return m.maybeLoad()
	}
	if msg.Quote == nil {
		return notice("The quoted message is not stored on this computer")
	}
	loc, ok := m.hist.(Locator)
	if !ok {
		return notice("The quoted message is not loaded")
	}
	return m.fetchAround(loc, msg.QuotedWAID)
}

// Resize implements uikit.Pane.
func (m *Model) Resize(w, h int) {
	m.w, m.h = w, h
	m.clampAnchor()
}

// SetFocused implements uikit.Pane.
func (m *Model) SetFocused(focused bool) {
	m.focused = focused
	if focused && m.selected == 0 && len(m.win) > 0 {
		m.selected = m.anchor
	}
}

// Update implements uikit.Pane.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case PageMsg:
		return m.page(msg)
	case AppendMsg:
		if msg.Chat == m.chat {
			m.append(msg.Msgs)
		}
		return nil
	case ReplaceMsg:
		if msg.Chat == m.chat {
			m.replace(msg.Msgs)
		}
		return nil
	case ReloadMsg:
		if msg.Chat == m.chat && m.chat != 0 {
			return m.Open(m.chat)
		}
		return nil
	case NamesChangedMsg:
		m.namesRev++
		return nil
	case keymap.ActionMsg:
		return m.action(msg.Action)
	}
	return nil
}

func (m *Model) page(p PageMsg) tea.Cmd {
	if p.chat != m.chat || p.gen != m.gen {
		return nil // a response for a chat or an open that is gone
	}
	switch p.dir {
	case dirLatest, dirAround:
		m.loading = false
	case dirOlder:
		m.loadingOlder = false
	case dirNewer:
		m.loadingNewer = false
	}
	if p.err != nil {
		m.err = p.err
		return nil
	}
	m.err = nil
	short := len(p.msgs) < p.limit
	switch p.dir {
	case dirLatest:
		m.win = append(m.win[:0], p.msgs...)
		m.olderDone, m.newerDone = short, true
		m.reindex()
		if n := len(m.win); n > 0 {
			m.anchor, m.skip, m.follow = m.win[n-1].ID, 0, true
			if m.focused {
				m.selected = m.anchor
			}
		}
	case dirOlder:
		m.win = slices.Concat(m.fresh(p.msgs), m.win)
		m.olderDone = short
		m.reindex()
		m.evictNewest()
	case dirAround:
		if len(p.msgs) == 0 {
			// The message went (deleted locally): show the newest instead.
			return m.Open(m.chat)
		}
		m.win = append(m.win[:0], p.msgs...)
		m.olderDone, m.newerDone, m.follow, m.unseen = false, false, false, 0
		m.reindex()
		if j, ok := m.byWAID[p.target]; ok {
			m.selected = m.win[j].ID
			m.showFromTop(j)
		}
	case dirNewer:
		m.win = append(m.win, m.fresh(p.msgs)...)
		m.newerDone = short
		m.reindex()
		m.evictOldest()
		if m.newerDone && m.atBottom() {
			m.follow, m.unseen = true, 0
		}
	}
	m.clampAnchor()
	return m.maybeLoad()
}

// fresh drops messages already in the window.
func (m *Model) fresh(msgs []domain.Message) []domain.Message {
	out := msgs[:0:0]
	for i := range msgs {
		if _, ok := m.idx[msgs[i].ID]; !ok {
			out = append(out, msgs[i])
		}
	}
	return out
}

// append adds stored messages. Newer ones than the window are appended
// if the window reaches the newest message, and otherwise only counted:
// they load when the user scrolls down. Older ones, from a history sync,
// go in their place within the window, or before it if the window
// reaches the first message; otherwise paging up finds them.
func (m *Model) append(msgs []domain.Message) {
	// The selection follows new messages only if it is on the newest one;
	// a user reading older messages keeps their place.
	selNewest := m.selected == 0 || (len(m.win) > 0 && m.selected == m.win[len(m.win)-1].ID)
	newer, inserted := 0, 0
	seen := make(map[domain.MessageID]bool, len(msgs))
	for k := range msgs {
		msg := &msgs[k]
		if _, ok := m.idx[msg.ID]; ok || seen[msg.ID] {
			continue
		}
		seen[msg.ID] = true
		c := msg.Cursor()
		n := len(m.win)
		switch {
		case n == 0 || m.win[n-1].Cursor().Before(c):
			newer++
			if m.newerDone {
				m.win = append(m.win, *msg) // usually the newest: O(1)
			}
			continue
		case c.Before(m.win[0].Cursor()) && !m.olderDone:
			continue
		}
		inserted++
		i, _ := slices.BinarySearchFunc(m.win, c, func(a domain.Message, c domain.Cursor) int {
			switch ac := a.Cursor(); {
			case ac.Before(c):
				return -1
			case c.Before(ac):
				return 1
			}
			return 0
		})
		m.win = slices.Insert(m.win, i, *msg)
	}
	if newer == 0 && inserted == 0 {
		return
	}
	m.reindex() // once per batch, not per message
	if newer == 0 {
		m.clampAnchor() // older messages: the anchor keeps the view in place
		return
	}
	added := newer
	if m.follow && m.newerDone {
		m.anchor, m.skip = m.win[len(m.win)-1].ID, 0
		if m.focused && selNewest {
			m.selected = m.anchor
		}
		m.evictOldest()
		m.fixSelection()
		return
	}
	m.unseen += added
}

func (m *Model) replace(msgs []domain.Message) {
	for k := range msgs {
		if i, ok := m.idx[msgs[k].ID]; ok && msgs[k].Revision != m.win[i].Revision {
			m.win[i] = msgs[k]
		}
	}
	m.reindex() // a WhatsApp ID may have been filled in
	m.clampAnchor()
}

func (m *Model) reindex() {
	clear(m.idx)
	clear(m.byWAID)
	for i := range m.win {
		m.idx[m.win[i].ID] = i
		if id := m.win[i].WAID; id != "" {
			m.byWAID[id] = i
		}
	}
}

// evictNewest trims the window after an older page, keeping a screen of
// messages below the anchor.
func (m *Model) evictNewest() {
	excess := len(m.win) - MaxWindow
	if excess <= 0 {
		return
	}
	a := m.idx[m.anchor]
	keep := max(a+1+m.h, len(m.win)-excess)
	if keep >= len(m.win) {
		return
	}
	m.win = m.win[:keep]
	m.newerDone, m.follow = false, false
	m.reindex()
	m.fixSelection()
}

// evictOldest trims the window after a newer page or live messages,
// keeping a screen of messages above the viewport.
func (m *Model) evictOldest() {
	excess := len(m.win) - MaxWindow
	if excess <= 0 {
		return
	}
	top, _, _ := m.topVisible()
	drop := min(excess, max(top-m.h, 0))
	if drop <= 0 {
		return
	}
	m.win = slices.Delete(m.win, 0, drop)
	m.olderDone = false
	m.reindex()
	m.fixSelection()
}

func (m *Model) fixSelection() {
	if _, ok := m.idx[m.selected]; !ok {
		m.selected = 0
		if m.focused {
			m.selected = m.anchor
		}
	}
}

// maybeLoad requests the next page when the viewport nears an edge of
// the window that is not the edge of the chat.
func (m *Model) maybeLoad() tea.Cmd {
	if len(m.win) == 0 || m.loading {
		return nil
	}
	var cmds []tea.Cmd
	top, _, _ := m.topVisible()
	if !m.olderDone && !m.loadingOlder && top <= prefetch {
		cmds = append(cmds, m.fetch(dirOlder, m.win[0].Cursor()))
	}
	if a := m.idx[m.anchor]; !m.newerDone && !m.loadingNewer && a >= len(m.win)-1-prefetch {
		cmds = append(cmds, m.fetch(dirNewer, m.win[len(m.win)-1].Cursor()))
	}
	return tea.Batch(cmds...)
}

func (m *Model) action(a keymap.Action) tea.Cmd {
	if len(m.win) == 0 {
		return nil
	}
	half := max(m.h/2, 1)
	page := max(m.h-1, 1)
	switch a {
	case keymap.Down:
		m.moveSelection(1)
	case keymap.Up:
		m.moveSelection(-1)
	case keymap.HalfPageDown:
		m.scrollDown(half)
		m.selectVisible(true)
	case keymap.HalfPageUp:
		m.scrollUp(half)
		m.selectVisible(false)
	case keymap.PageDown:
		m.scrollDown(page)
		m.selectVisible(true)
	case keymap.PageUp:
		m.scrollUp(page)
		m.selectVisible(false)
	case keymap.Top:
		m.selected = m.win[0].ID
		m.showFromTop(0)
	case keymap.Bottom:
		if !m.newerDone {
			return m.Open(m.chat) // jump to the newest messages
		}
		m.anchor, m.skip, m.follow, m.unseen = m.win[len(m.win)-1].ID, 0, true, 0
		m.selected = m.anchor
	case keymap.JumpQuoted:
		return m.jumpQuoted()
	case keymap.Reply, keymap.React, keymap.Edit, keymap.Delete, keymap.Copy,
		keymap.OpenMedia, keymap.SaveMedia, keymap.ViewPager, keymap.Retry, keymap.Forward:
		if i, ok := m.idx[m.selected]; ok {
			msg := m.win[i]
			return func() tea.Msg { return MessageActionMsg{Action: a, Message: msg} }
		}
		return nil
	default:
		return nil
	}
	cmd := m.maybeLoad()
	switch a {
	case keymap.Up, keymap.HalfPageUp, keymap.PageUp, keymap.Top:
		return tea.Batch(cmd, m.askOlder())
	}
	return cmd
}

// askOlder sends an OldestMsg when the user moves up while the chat's
// oldest stored message is in view, once per oldest message: opening a
// short chat asks the phone for nothing.
func (m *Model) askOlder() tea.Cmd {
	if !m.olderDone || len(m.win) == 0 || m.askedOldest == m.win[0].ID {
		return nil
	}
	if top, _, _ := m.topVisible(); top != 0 {
		return nil
	}
	m.askedOldest = m.win[0].ID
	chat, oldest := m.chat, m.win[0]
	return func() tea.Msg { return OldestMsg{Chat: chat, Oldest: oldest} }
}
