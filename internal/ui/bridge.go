// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"maps"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
)

// Bridge defaults (docs/architecture/event-pipeline.md §3.3).
const (
	// DefaultFlushInterval is how long the bridge collects events before
	// it hands them to the UI in one message. Key presses do not wait for
	// it. Measured under scenario S5, 50 ms halves the CPU spent re-rendering
	// against 16 ms with the same key latency (make loadtest).
	DefaultFlushInterval = 50 * time.Millisecond
	// DefaultChatCap is how many new or updated messages the bridge keeps
	// per chat between flushes; beyond it the chat is reloaded instead.
	DefaultChatCap = 200
)

// BatchMsg is everything that changed since the previous flush. Message
// lists are in arrival order; for chat rows, typing and the connection
// only the latest state is kept.
type BatchMsg struct {
	Rows     []chatlist.Row
	Appends  map[domain.ChatID][]domain.Message
	Replaces map[domain.ChatID][]domain.Message
	// Reloads lists chats whose updates overflowed the per-chat cap, or
	// that storage asked to reload; their open views must reload.
	Reloads []domain.ChatID
	// Removed lists chats that no longer exist.
	Removed []domain.ChatID
	// Groups lists groups whose members or metadata changed.
	Groups []domain.ChatID
	Typing map[domain.ChatID]bool
	// Presence holds the latest online status of direct chats' peers.
	Presence   map[domain.ChatID]domain.PresenceChanged
	Connection *domain.ConnectionChanged
	// NamesChanged reports that display names changed.
	NamesChanged bool
	// Notice is a short message for the status line, if not empty.
	Notice string
	// Link is the latest step of linking the device, if one arrived.
	Link *domain.LinkProgress
	// Linked reports that the device is now linked to an account.
	Linked bool
}

func (b *BatchMsg) empty() bool {
	return len(b.Rows) == 0 && len(b.Appends) == 0 && len(b.Replaces) == 0 && len(b.Reloads) == 0 &&
		len(b.Removed) == 0 && len(b.Groups) == 0 && len(b.Typing) == 0 && len(b.Presence) == 0 && b.Connection == nil && !b.NamesChanged && b.Notice == "" &&
		b.Link == nil && !b.Linked
}

// BridgeOptions configure a Bridge. Zero values select the defaults.
type BridgeOptions struct {
	FlushInterval time.Duration
	ChatCap       int
}

// Bridge coalesces backend events into at most one UI message per flush
// interval. Its producer methods never block on the UI: they append to
// bounded buffers under a mutex. A single goroutine waits for the first
// event after an idle period, sleeps for the flush interval, then sends
// the batch; only that goroutine waits on the UI (send blocks until the
// Bubble Tea loop receives it). With no events nothing runs.
type Bridge struct {
	send     func(tea.Msg)
	interval time.Duration
	cap      int

	mu      sync.Mutex
	pending pendingBatch
	armed   bool
	closed  bool

	wake chan struct{}
	done chan struct{}
	exit chan struct{}

	flushes   int // batches sent
	overflows int // chats collapsed into reloads
}

type pendingBatch struct {
	rows     map[domain.ChatID]chatlist.Row
	appends  map[domain.ChatID][]domain.Message
	replaces map[domain.ChatID][]domain.Message
	reloads  map[domain.ChatID]bool
	removed  map[domain.ChatID]bool
	groups   map[domain.ChatID]bool
	typing   map[domain.ChatID]bool
	presence map[domain.ChatID]domain.PresenceChanged
	conn     *domain.ConnectionChanged
	names    bool
	notice   string
	link     *domain.LinkProgress
	linked   bool
}

func newPending() pendingBatch {
	return pendingBatch{
		rows:     map[domain.ChatID]chatlist.Row{},
		appends:  map[domain.ChatID][]domain.Message{},
		replaces: map[domain.ChatID][]domain.Message{},
		reloads:  map[domain.ChatID]bool{},
		removed:  map[domain.ChatID]bool{},
		groups:   map[domain.ChatID]bool{},
		typing:   map[domain.ChatID]bool{},
		presence: map[domain.ChatID]domain.PresenceChanged{},
	}
}

// NewBridge starts a bridge that delivers batches with send, normally
// (*tea.Program).Send. Call Close to stop it.
func NewBridge(send func(tea.Msg), opts BridgeOptions) *Bridge {
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultFlushInterval
	}
	if opts.ChatCap <= 0 {
		opts.ChatCap = DefaultChatCap
	}
	b := &Bridge{
		send:     send,
		interval: opts.FlushInterval,
		cap:      opts.ChatCap,
		pending:  newPending(),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		exit:     make(chan struct{}),
	}
	go b.run()
	return b
}

// Messages queues newly arrived messages of a chat.
func (b *Bridge) Messages(chat domain.ChatID, msgs []domain.Message) {
	b.queueMessages(chat, msgs, true)
}

// Updates queues changed messages of a chat (edits, receipts, reactions).
func (b *Bridge) Updates(chat domain.ChatID, msgs []domain.Message) {
	b.queueMessages(chat, msgs, false)
}

func (b *Bridge) queueMessages(chat domain.ChatID, msgs []domain.Message, appended bool) {
	if len(msgs) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	p := &b.pending
	if !p.reloads[chat] && !p.removed[chat] {
		if appended {
			p.appends[chat] = append(p.appends[chat], msgs...)
		} else {
			p.replaces[chat] = append(p.replaces[chat], msgs...)
		}
		if len(p.appends[chat])+len(p.replaces[chat]) > b.cap {
			// Too much for one frame: drop the details, the UI reloads the
			// chat from storage, which is the source of truth (G6).
			delete(p.appends, chat)
			delete(p.replaces, chat)
			p.reloads[chat] = true
			b.overflows++
		}
	}
	b.arm()
}

// Chat queues the latest state of a chat list row.
func (b *Bridge) Chat(row chatlist.Row) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending.removed[row.ID] {
		return
	}
	b.pending.rows[row.ID] = row
	b.arm()
}

// Reload asks the UI to reload a chat's open view from storage, for
// changes too many to list.
func (b *Bridge) Reload(chat domain.ChatID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending.removed[chat] {
		return
	}
	delete(b.pending.appends, chat)
	delete(b.pending.replaces, chat)
	b.pending.reloads[chat] = true
	b.arm()
}

// RemoveChat queues the removal of a chat that no longer exists. Chat
// IDs are never reused, so later events for it are dropped.
func (b *Bridge) RemoveChat(chat domain.ChatID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	p := &b.pending
	delete(p.rows, chat)
	delete(p.appends, chat)
	delete(p.replaces, chat)
	delete(p.reloads, chat)
	delete(p.groups, chat)
	delete(p.typing, chat)
	p.removed[chat] = true
	b.arm()
}

// GroupChanged tells the UI that a group's members or metadata changed.
func (b *Bridge) GroupChanged(chat domain.ChatID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending.removed[chat] {
		return
	}
	b.pending.groups[chat] = true
	b.arm()
}

// NamesChanged tells the UI that display names changed.
func (b *Bridge) NamesChanged() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.pending.names = true
	b.arm()
}

// Link queues a step of linking the device; the latest wins.
func (b *Bridge) Link(p domain.LinkProgress) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.pending.link = &p
	b.arm()
}

// Linked reports that the device is now linked to an account.
func (b *Bridge) Linked() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.pending.linked = true
	b.arm()
}

// Notice queues a short message for the status line; the latest wins.
func (b *Bridge) Notice(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || text == "" {
		return
	}
	b.pending.notice = text
	b.arm()
}

// Presence queues the online status of a direct chat's peer; the latest
// wins.
func (b *Bridge) Presence(chat domain.ChatID, p domain.PresenceChanged) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending.removed[chat] {
		return
	}
	b.pending.presence[chat] = p
	b.arm()
}

// Typing queues a chat's typing indicator; the latest state wins.
func (b *Bridge) Typing(chat domain.ChatID, typing bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending.removed[chat] {
		return
	}
	b.pending.typing[chat] = typing
	b.arm()
}

// Connection queues the connection state; the latest state wins.
func (b *Bridge) Connection(c domain.ConnectionChanged) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.pending.conn = &c
	b.arm()
}

// arm wakes the flusher for the first event after an idle period. The
// caller holds b.mu.
func (b *Bridge) arm() {
	if b.armed {
		return
	}
	b.armed = true
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Close stops the bridge. Queued events that were not flushed are
// dropped; the UI is shutting down.
func (b *Bridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	close(b.done)
	<-b.exit
}

// Stats returns the number of batches sent and chats collapsed into
// reloads, for the debug metrics.
func (b *Bridge) Stats() (flushes, overflows int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.flushes, b.overflows
}

func (b *Bridge) run() {
	defer close(b.exit)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-b.wake:
		case <-b.done:
			return
		}
		timer.Reset(b.interval)
		select {
		case <-timer.C:
		case <-b.done:
			timer.Stop()
			return
		}
		if batch := b.take(); !batch.empty() {
			b.mu.Lock()
			b.flushes++
			b.mu.Unlock()
			b.send(batch)
		}
	}
}

// take swaps out the pending events and builds the batch.
func (b *Bridge) take() BatchMsg {
	b.mu.Lock()
	p := b.pending
	b.pending = newPending()
	b.armed = false
	b.mu.Unlock()

	batch := BatchMsg{}
	batch.Rows = slices.Collect(maps.Values(p.rows))
	if len(p.appends) > 0 {
		batch.Appends = p.appends
	}
	if len(p.replaces) > 0 {
		batch.Replaces = p.replaces
	}
	for chat := range p.reloads {
		batch.Reloads = append(batch.Reloads, chat)
	}
	for chat := range p.removed {
		batch.Removed = append(batch.Removed, chat)
	}
	for chat := range p.groups {
		batch.Groups = append(batch.Groups, chat)
	}
	if len(p.typing) > 0 {
		batch.Typing = p.typing
	}
	if len(p.presence) > 0 {
		batch.Presence = p.presence
	}
	batch.Connection = p.conn
	batch.NamesChanged = p.names
	batch.Notice = p.notice
	batch.Link = p.link
	batch.Linked = p.linked
	return batch
}
