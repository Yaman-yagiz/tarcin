// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime/trace"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/jobs"
	"github.com/Yaman-yagiz/tarcin/internal/logging"
	"github.com/Yaman-yagiz/tarcin/internal/store"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
)

// uiSink is the part of ui.Bridge the pipeline reports to. Its methods
// never block.
type uiSink interface {
	Messages(chat domain.ChatID, msgs []domain.Message)
	Updates(chat domain.ChatID, msgs []domain.Message)
	Reload(chat domain.ChatID)
	RemoveChat(chat domain.ChatID)
	GroupChanged(chat domain.ChatID)
	Chat(row chatlist.Row)
	NamesChanged()
	Notice(text string)
	Typing(chat domain.ChatID, typing bool)
	Presence(chat domain.ChatID, p domain.PresenceChanged)
	Connection(c domain.ConnectionChanged)
	Link(p domain.LinkProgress)
	Linked()
}

// pipelineOptions configure a pipeline.
type pipelineOptions struct {
	MeLID, MePN domain.JID
	SnapshotDir string           // where pre-migration snapshots go; empty: none
	Now         func() time.Time // default time.Now
	// Backend receives the read receipts, if ReadReceipts is set: the
	// privacy setting that lets others see when their messages were read.
	Backend      backend.Backend
	ReadReceipts bool
	// Typing and Presence are the privacy settings that let others see
	// when this account types and when it is online.
	Typing, Presence bool
	// Media configures downloads; nil: none.
	Media *mediaOptions
	Log   *slog.Logger // must redact (internal/logging); default: none
	// wrapApplier, if set, wraps the event applier; tests use it to
	// inject faults.
	wrapApplier func(store.Applier) store.Applier
}

// pipeline is the message database at work (docs/architecture/event-pipeline.md):
// backend events go through the single writer into SQLite, and every
// commit is turned into UI updates by a notifier goroutine that reads
// the changed rows back. The UI reads history and names through it.
type pipeline struct {
	opts   pipelineOptions
	path   string // the database file
	start0 time.Time
	wdb    *sql.DB
	rdb    *sql.DB
	reader *store.Reader
	writer *store.Writer
	names  *names
	ui     uiSink
	log    *slog.Logger
	guard  *guard // reports panics and stops the program
	clock  *serverClock
	stats  func() store.ApplierStats
	jobs   *jobs.Pool // network requests, such as group refreshes
	media  *mediaHandler

	mu      sync.Mutex
	pending store.Changes
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	// expiring runs expireLoop until expireStop closes.
	expiring   sync.WaitGroup
	expireStop chan struct{}

	open atomic.Int64 // the chat the user is reading, for marking new messages read
	away atomic.Bool  // the terminal lost focus: the open chat's new messages stay unread
	// typingMu guards typingEnd: the timers that hide a typing indicator
	// whose end never arrived.
	typingMu  sync.Mutex
	typingEnd map[domain.ChatID]*time.Timer
	// lastOwn is the time, in Unix milliseconds, of the latest change this
	// account made here (a reaction or an edit); see ownTime.
	lastOwn atomic.Int64
	// readMu orders reads: the unread messages a read reports must be
	// those it marks read, or two reads at once would report them twice.
	readMu sync.Mutex
}

// openPipeline opens and migrates the database at path. Call start
// before delivering events.
func openPipeline(ctx context.Context, path string, opts pipelineOptions) (*pipeline, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = logging.Discard()
	}
	wdb, err := store.Open(ctx, path, store.RoleWriter)
	if err != nil {
		return nil, err
	}
	if err := store.CheckFTS5(ctx, wdb); err != nil {
		_ = wdb.Close()
		return nil, err
	}
	if _, _, err := store.Migrate(ctx, wdb, store.Migrations(), store.MigrateOptions{SnapshotDir: opts.SnapshotDir}); err != nil {
		_ = wdb.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	rdb, err := store.Open(ctx, path, store.RoleReader)
	if err != nil {
		_ = wdb.Close()
		return nil, err
	}
	return &pipeline{
		opts: opts, path: path, start0: time.Now(), wdb: wdb, rdb: rdb, reader: store.NewReader(rdb), names: newNames(), log: opts.Log,
		guard: newGuard(opts.Log), clock: &serverClock{now: opts.Now},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		typingEnd: map[domain.ChatID]*time.Timer{},
	}, nil
}

// start starts the writer and the notifier, which first loads every chat
// and name into the UI.
func (p *pipeline) start(sink uiSink) error {
	p.ui = sink
	events := store.NewEventApplier(store.ApplierOptions{MeLID: p.opts.MeLID, MePN: p.opts.MePN, Now: p.clock.Now})
	p.stats = events.Stats
	var applier store.Applier = &guardedApplier{inner: events, g: p.guard}
	if p.opts.wrapApplier != nil {
		applier = p.opts.wrapApplier(applier)
	}
	w, err := store.NewWriter(p.wdb, store.WriterOptions{Applier: applier, OnCommit: p.onCommit, OnHealth: p.onHealth})
	if err != nil {
		return err
	}
	p.writer = w
	// Protocol requests: a few at once, about one a second, so that no
	// burst of them looks like automation (security.md §7).
	p.jobs = jobs.New(jobs.Options{Name: "network", Workers: 4, Rate: 1, Burst: 3, Log: p.log})
	if p.opts.Media != nil {
		p.media = newMediaHandler(p, *p.opts.Media)
		sweep, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p.sweepCache(sweep)
		cancel()
	}
	p.expireStop = make(chan struct{})
	p.expiring.Go(func() {
		defer p.guard.protect("pipeline.expire")
		p.expireLoop()
	})
	go func() {
		defer p.guard.protect("pipeline.notify")
		p.notify()
	}()
	// Own messages still pending from the last run were interrupted.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.SubmitLive(ctx, []domain.Event{domain.SendsInterrupted{Before: p.clock.Now()}}); err != nil {
		return fmt.Errorf("marking interrupted sends: %w", err)
	}
	return nil
}

// close drains the writer within ctx, stops the notifier and closes the
// database.
func (p *pipeline) close(ctx context.Context) error {
	var err error
	if p.jobs != nil {
		p.jobs.Close() // before the writer: jobs store what they fetch
		j := p.jobs.Stats()
		p.log.Info("network jobs closed", "done", j.Done, "failed", j.Failed, "retried", j.Retried, "dropped", j.Dropped)
	}
	if p.media != nil {
		p.media.close()
		j := p.media.pool.Stats()
		p.log.Info("media jobs closed", "done", j.Done, "failed", j.Failed, "dropped", j.Dropped)
	}
	p.typingMu.Lock()
	for _, t := range p.typingEnd {
		t.Stop()
	}
	p.typingMu.Unlock()
	if p.writer != nil {
		close(p.expireStop)
		p.expiring.Wait() // before the writer: it deletes through it
		err = p.writer.Close(ctx)
		close(p.stop)
		<-p.done
		s, w := p.stats(), p.writer.Stats()
		p.log.Info("message store closed", "transactions", w.Transactions, "events", w.Events, "failed", w.Failed,
			"canceled", w.Canceled, "busy_retries", w.BusyRetries, "max_queue_wait", w.MaxQueueWait,
			"skipped", s.Skipped, "sender_mismatches", s.SenderMismatches, "refused", s.Refused)
	}
	return errors.Join(err, p.rdb.Close(), p.wdb.Close())
}

// Deliver implements backend.Sink: typing, connection changes and link
// steps go straight to the UI, everything else through the writer. The
// link screen closes once the account is stored.
func (p *pipeline) Deliver(ctx context.Context, events []domain.Event) error {
	defer trace.StartRegion(ctx, "ingest").End()
	arrived := p.opts.Now()
	linked := false
	for _, ev := range events {
		switch e := ev.(type) {
		case domain.ChatTyping:
			if id, ok, err := p.reader.ChatID(ctx, e.ChatJID); err == nil && ok {
				p.typingChanged(id, e.Typing)
			}
		case domain.PresenceChanged:
			if id, ok, err := p.reader.ChatID(ctx, e.JID); err == nil && ok {
				p.ui.Presence(id, e)
			}
		case domain.ConnectionChanged:
			p.ui.Connection(e)
		case domain.LinkProgress:
			p.ui.Link(e)
		case domain.AccountLinked:
			linked = true
		case domain.MessageReceived:
			if e.Source == domain.SourceLive && !e.Message.FromMe {
				p.clock.observeLive(e.Message.Timestamp, arrived)
			}
		}
	}
	if err := p.writer.Deliver(ctx, events); err != nil {
		return err
	}
	if linked {
		p.ui.Linked()
	}
	for _, ev := range events {
		// A change to a group whose full member list is missing or old.
		if g, ok := ev.(domain.GroupMembersChanged); ok && !g.Full {
			p.refreshGroup(g.ChatJID)
		}
	}
	return nil
}

// removeFiles deletes the cache files a commit left unused, unless a
// later one uses the same content again.
func (p *pipeline) removeFiles(ctx context.Context, names []string) {
	if p.media == nil {
		return
	}
	for _, name := range names {
		path, ok := p.media.o.cachePath(name)
		if used, err := p.reader.MediaInUse(ctx, name); ok && err == nil && !used {
			_ = os.Remove(path) //nolint:gosec // a stored name that cachePath checked
		}
	}
}

// sweepCache deletes the cache files no stored message uses: those a
// commit left unused when tarcin stopped before deleting them. It runs at
// start, before any download could put a file in place.
func (p *pipeline) sweepCache(ctx context.Context) {
	if p.media == nil {
		return
	}
	used, err := p.reader.LocalNames(ctx)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(p.media.o.Dir)
	for _, e := range entries {
		if name := e.Name(); e.Type().IsRegular() && !isMediaTemp(name) && !used[name] {
			_ = os.Remove(filepath.Join(p.media.o.Dir, name)) //nolint:gosec // a name from the cache's own listing
		}
	}
}

// deleteLocal returns the ui.Options.DeleteLocal function: it deletes a
// chat with its messages and media from this computer only; the phone and
// the server keep them.
func (p *pipeline) deleteLocal() func(domain.ChatID) tea.Cmd {
	return func(chat domain.ChatID) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			jid, err := p.chatJID(ctx, chat)
			if err != nil {
				return ui.BatchMsg{Notice: "Not deleted: " + err.Error()}
			}
			// The commit names the cache files it leaves unused, and
			// publish deletes them.
			if err := p.writer.SubmitLive(ctx, []domain.Event{domain.ChatDeleted{ChatJID: jid, Before: p.clock.Now()}}); err != nil {
				return ui.BatchMsg{Notice: "Not deleted: " + err.Error()}
			}
			return ui.BatchMsg{Notice: "Deleted from this computer; your phone keeps the chat"}
		}
	}
}

// metrics returns the ui.Options.Metrics function: the counters of the
// store, the applier, the job pools and the bridge, for the debug pane.
func (p *pipeline) metrics(bridge func() (flushes, overflows int)) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg {
			if p.writer == nil {
				return nil
			}
			w := p.writer.Stats()
			live, bulk := p.writer.Queued()
			size := int64(0)
			for _, f := range []string{p.path, p.path + "-wal"} {
				if info, err := os.Stat(f); err == nil {
					size += info.Size()
				}
			}
			n := func(v uint64) string { return strconv.FormatUint(v, 10) }
			sections := []ui.MetricSection{{Title: "Store", Items: [][2]string{
				{"queued (live, bulk)", strconv.Itoa(live) + ", " + strconv.Itoa(bulk)},
				{"transactions", n(w.Transactions)}, {"events", n(w.Events)},
				{"failed submissions", n(w.Failed)}, {"busy retries", n(w.BusyRetries)},
				{"longest queue wait", w.MaxQueueWait.Round(time.Microsecond).String()},
				{"database size", fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))},
			}}}
			if p.stats != nil {
				a := p.stats()
				sections = append(sections, ui.MetricSection{Title: "Applier", Items: [][2]string{
					{"skipped events", n(a.Skipped)}, {"sender mismatches", n(a.SenderMismatches)}, {"refused changes", n(a.Refused)},
				}})
			}
			pool := func(title string, s jobs.Stats) ui.MetricSection {
				return ui.MetricSection{Title: title, Items: [][2]string{
					{"queued, running", strconv.Itoa(s.Queued) + ", " + strconv.Itoa(s.Running)},
					{"done, failed", n(s.Done) + ", " + n(s.Failed)},
					{"retried, shared, dropped", n(s.Retried) + ", " + n(s.Shared) + ", " + n(s.Dropped)},
				}}
			}
			if p.jobs != nil {
				sections = append(sections, pool("Network jobs", p.jobs.Stats()))
			}
			if p.media != nil {
				sections = append(sections, pool("Media jobs", p.media.pool.Stats()))
			}
			if bridge != nil {
				f, o := bridge()
				sections = append(sections, ui.MetricSection{Title: "Bridge", Items: [][2]string{
					{"batches", strconv.Itoa(f)}, {"chats reloaded instead", strconv.Itoa(o)},
					{"uptime", time.Since(p.start0).Round(time.Second).String()},
				}})
			}
			return ui.MetricsMsg{Sections: sections}
		}
	}
}

// expireEvery is how often disappearing messages whose time is up are
// deleted; they also are at start.
const expireEvery = 5 * time.Minute

// expireLoop deletes expired disappearing messages until stop closes.
func (p *pipeline) expireLoop() {
	t := time.NewTicker(expireEvery)
	defer t.Stop()
	for {
		p.expire()
		select {
		case <-p.expireStop:
			return
		case <-t.C:
		}
	}
}

// expire deletes the disappearing messages whose time is up, and then
// their cached media files: a deleted message leaves nothing behind.
func (p *pipeline) expire() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The commit names the cache files it leaves unused, and publish
	// deletes them.
	if err := p.writer.SubmitBulk(ctx, []domain.Event{domain.MessagesExpired{Now: p.clock.Now()}}); err != nil {
		p.log.Warn("deleting expired messages", "err", err)
	}
}

// typingTimeout is how long a typing indicator shows without news: one
// whose end was lost must not stay forever.
var typingTimeout = 25 * time.Second

// typingChanged shows or hides a chat's typing indicator, hiding it after
// typingTimeout unless it is renewed.
func (p *pipeline) typingChanged(chat domain.ChatID, typing bool) {
	p.ui.Typing(chat, typing)
	p.typingMu.Lock()
	defer p.typingMu.Unlock()
	if t := p.typingEnd[chat]; t != nil {
		t.Stop()
		delete(p.typingEnd, chat)
	}
	if !typing {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(typingTimeout, func() {
		p.typingMu.Lock()
		current := p.typingEnd[chat] == t
		if current {
			delete(p.typingEnd, chat)
		}
		p.typingMu.Unlock()
		if current {
			p.ui.Typing(chat, false)
		}
	})
	p.typingEnd[chat] = t
}

// typing returns the ui.Options.Typing function, if the privacy setting
// allows it: it tells a chat whether this account is typing.
func (p *pipeline) typing() func(domain.ChatID, bool) tea.Cmd {
	pr, ok := p.opts.Backend.(backend.Presencer)
	if !ok || !p.opts.Typing {
		return nil
	}
	return func(chat domain.ChatID, composing bool) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if jid, err := p.chatJID(ctx, chat); err == nil {
				_ = pr.SendTyping(ctx, jid, composing) // a lost one costs a moment of "typing…"
			}
			return nil
		}
	}
}

// presence returns the ui.Options.Presence function, if the privacy
// setting allows it: it tells contacts whether this account is online.
func (p *pipeline) presence() func(bool) tea.Cmd {
	pr, ok := p.opts.Backend.(backend.Presencer)
	if !ok || !p.opts.Presence {
		return nil
	}
	return func(available bool) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := pr.SetAvailable(ctx, available); err != nil {
				p.log.Info("online status not sent", "available", available, "err", err)
			}
			return nil
		}
	}
}

// offline tells contacts this account is no longer online, at shutdown,
// if the privacy setting let them see it online.
func (p *pipeline) offline() {
	if pr, ok := p.opts.Backend.(backend.Presencer); ok && p.opts.Presence {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = pr.SetAvailable(ctx, false)
	}
}

// followPresence asks for the online status of a direct chat's peer, in
// the background, if the privacy setting shares this account's.
func (p *pipeline) followPresence(chat domain.JID) {
	pr, ok := p.opts.Backend.(backend.Presencer)
	if !ok || !p.opts.Presence || !chat.IsUser() || p.jobs == nil {
		return
	}
	_ = p.jobs.Submit("presence.subscribe "+string(chat), func(ctx context.Context) error {
		return pr.SubscribePresence(ctx, chat)
	})
}

// groupRefreshAge is how old a group's stored member list may get before
// it is fetched again (event-pipeline.md §3.5).
const groupRefreshAge = 24 * time.Hour

// refreshGroup fetches a group's state in the background if its stored
// member list is missing or older than groupRefreshAge. It does no I/O
// itself and never blocks; a full queue drops the request, which the
// next trigger makes again.
func (p *pipeline) refreshGroup(group domain.JID) {
	f, ok := p.opts.Backend.(backend.GroupFetcher)
	if !ok || p.jobs == nil || !group.IsGroup() {
		return
	}
	_ = p.jobs.Submit("group.refresh "+string(group), func(ctx context.Context) error {
		if id, found, err := p.reader.ChatID(ctx, group); err != nil {
			return err
		} else if found {
			meta, ok, err := p.reader.GroupMeta(ctx, id)
			if err != nil {
				return err
			}
			if ok && !meta.FetchedAt.IsZero() && p.opts.Now().Sub(meta.FetchedAt) < groupRefreshAge {
				return nil
			}
		}
		evs, err := f.FetchGroup(ctx, group)
		switch {
		case errors.Is(err, backend.ErrNoGroup):
			return jobs.Permanent(err)
		case err != nil:
			return err
		}
		return p.writer.SubmitLive(ctx, evs)
	})
}

// onCommit collects a commit's changes for the notifier. It runs on the
// writer goroutine and must not block.
func (p *pipeline) onCommit(c *store.Changes) {
	p.mu.Lock()
	p.pending.Merge(c)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pipeline) onHealth(err error) {
	if err == nil {
		p.log.Info("the message database accepts writes again")
	} else {
		p.log.Error("the message database stopped accepting writes", "err", err)
	}
	switch {
	case err == nil:
		p.ui.Notice("Saving messages again")
	case errors.Is(err, store.ErrCorrupt):
		p.ui.Notice("The message database is damaged; new messages are not saved")
	case errors.Is(err, store.ErrDiskFull):
		p.ui.Notice("Disk full: new messages are not saved until there is space")
	}
}

func (p *pipeline) notify() {
	defer close(p.done)
	p.loadAll()
	for {
		select {
		case <-p.wake:
		case <-p.stop:
			return
		}
		p.mu.Lock()
		c := p.pending
		p.pending = store.Changes{}
		p.mu.Unlock()
		p.publish(&c)
	}
}

// loadAll shows every stored chat and name: the UI is usable from the
// cache before the backend connects (budget B1).
func (p *pipeline) loadAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids, err := p.reader.Identities(ctx)
	if err == nil {
		p.names.set(ids, nil)
	}
	chats, err := p.reader.Chats(ctx)
	if err != nil {
		p.log.Error("loading the chats", "err", err)
		p.ui.Notice("Could not load the chats: " + err.Error())
		return
	}
	for i := range chats {
		p.ui.Chat(p.row(&chats[i]))
	}
}

// publish turns a commit's changes into UI updates. Names go first, so
// rows and messages that show them find them.
func (p *pipeline) publish(c *store.Changes) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.removeFiles(ctx, c.Unlinked)
	if len(c.Identities) > 0 {
		asked := slices.Collect(maps.Keys(c.Identities))
		if found, err := p.reader.IdentitiesByID(ctx, asked); err == nil {
			p.names.set(found, asked)
			p.ui.NamesChanged()
		} else {
			p.log.Warn("reading changed names", "err", err)
		}
	}
	for chat, msgs := range c.Added {
		p.ui.Messages(chat, msgs)
	}
	for chat, msgs := range c.Updated {
		p.ui.Updates(chat, msgs)
	}
	for chat := range c.Reload {
		p.ui.Reload(chat)
	}
	for chat := range c.Removed {
		p.ui.RemoveChat(chat)
	}
	for chat := range c.Groups {
		p.ui.GroupChanged(chat)
	}
	if len(c.Chats) > 0 {
		if chats, err := p.reader.ChatsByID(ctx, slices.Collect(maps.Keys(c.Chats))); err == nil {
			for i := range chats {
				p.ui.Chat(p.row(&chats[i]))
			}
		} else {
			p.log.Warn("reading changed chats", "err", err)
		}
	}
	p.readOpenChat(ctx, c)
}

// readOpenChat marks new messages of the chat on screen as read.
func (p *pipeline) readOpenChat(ctx context.Context, c *store.Changes) {
	open := domain.ChatID(p.open.Load())
	if open == 0 || p.away.Load() {
		return
	}
	// The read covers the messages this commit added, not the moment it
	// runs: a message that arrives after the user left stays unread.
	var at time.Time
	_, reload := c.Reload[open]
	if reload {
		at = p.clock.Now()
	}
	incoming := reload
	for i := range c.Added[open] {
		if m := &c.Added[open][i]; !m.FromMe {
			incoming = true
			at = maxTime(at, m.Timestamp)
		}
	}
	if incoming {
		if err := p.markRead(ctx, open, at); err != nil {
			p.log.Warn("marking the open chat read", "err", err)
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// row builds a chat's chat-list row. A direct chat is titled with its
// peer's current name.
func (p *pipeline) row(c *domain.Chat) chatlist.Row {
	title := c.Title
	if c.Kind == domain.ChatDirect {
		title = p.names.DisplayName(c.PeerID)
	}
	if title == "" {
		title = c.JID.User()
	}
	return chatlist.Row{
		ID: c.ID, Title: title, LastActivity: c.LastActivity, Unread: c.UnreadCount,
		PinnedAt: c.PinnedAt, Muted: c.IsMuted(p.clock.Now()), Archived: c.Archived, Group: c.Kind == domain.ChatGroup,
		Disappearing: c.EphemeralTimer,
	}
}

func (p *pipeline) chatJID(ctx context.Context, chat domain.ChatID) (domain.JID, error) {
	c, err := p.chatOf(ctx, chat)
	return c.JID, err
}

func (p *pipeline) chatOf(ctx context.Context, chat domain.ChatID) (domain.Chat, error) {
	chats, err := p.reader.ChatsByID(ctx, []domain.ChatID{chat})
	if err != nil {
		return domain.Chat{}, err
	}
	if len(chats) == 0 {
		return domain.Chat{}, fmt.Errorf("chat %d no longer exists", chat)
	}
	return chats[0], nil
}

func (p *pipeline) markRead(ctx context.Context, chat domain.ChatID, at time.Time) error {
	p.readMu.Lock()
	defer p.readMu.Unlock()
	jid, err := p.chatJID(ctx, chat)
	if err != nil {
		return err
	}
	var unread []store.UnreadRef
	if p.opts.ReadReceipts && p.opts.Backend != nil {
		unread, _ = p.reader.UnreadIncoming(ctx, chat, readReceiptLimit)
	}
	if err := p.writer.SubmitLive(ctx, []domain.Event{domain.ChatRead{ChatJID: jid, ReadAt: at}}); err != nil {
		return err
	}
	if len(unread) > 0 {
		// The read is stored; the receipts may outlive the caller.
		go func() {
			defer p.guard.protect("pipeline.receipts")
			p.sendReadReceipts(context.WithoutCancel(ctx), jid, unread, at)
		}()
	}
	return nil
}

// readReceiptLimit bounds the read receipts sent for one read.
const readReceiptLimit = 500

// sendReadReceipts tells the senders that their messages were read, one
// request per sender, as groups require. A failure costs only the blue
// ticks on their side.
func (p *pipeline) sendReadReceipts(ctx context.Context, chat domain.JID, unread []store.UnreadRef, at time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bySender := map[domain.JID][]domain.WAMessageID{}
	var order []domain.JID
	for _, u := range unread {
		if _, ok := bySender[u.Sender]; !ok {
			order = append(order, u.Sender)
		}
		bySender[u.Sender] = append(bySender[u.Sender], u.WAID)
	}
	for _, sender := range order {
		to := sender
		if !chat.IsGroup() {
			to = "" // in a direct chat the chat is the sender
		}
		if err := p.opts.Backend.MarkRead(ctx, chat, to, bySender[sender], at); err != nil {
			p.log.Info("read receipt not sent", "messages", len(bySender[sender]), "err", err)
		}
	}
}

// attention is the ui.Options.Attention function. While the user is away
// (the terminal lost focus), the open chat's new messages stay unread and
// send no read receipts; when the user is back, the chat is read, as the
// official clients do when their window gets focus again.
func (p *pipeline) attention(watching bool) tea.Cmd {
	p.away.Store(!watching)
	chat := domain.ChatID(p.open.Load())
	if !watching || chat == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.markRead(ctx, chat, p.clock.Now()); err != nil {
			return composer.ErrorMsg{Err: fmt.Errorf("marking the chat read: %w", err)}
		}
		return nil
	}
}

// read returns the ui.Options.Read function: the chat is read now, and
// its new messages are read as they arrive while it stays open and the
// user is not away.
func (p *pipeline) read(chat domain.ChatID) tea.Cmd {
	p.open.Store(int64(chat))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.markRead(ctx, chat, p.clock.Now()); err != nil {
			return composer.ErrorMsg{Err: fmt.Errorf("marking the chat read: %w", err)}
		}
		if jid, err := p.chatJID(ctx, chat); err == nil {
			p.refreshGroup(jid)   // an opened group shows current members
			p.followPresence(jid) // an opened direct chat shows if the peer is online
		}
		return nil
	}
}

// ownTime returns the time of a reaction or edit this account makes now:
// the server's estimated clock in milliseconds, but always after the
// previous one, since the latest change wins and two in one millisecond
// would otherwise tie.
func (p *pipeline) ownTime() time.Time {
	for {
		last := p.lastOwn.Load()
		next := max(p.clock.Now().UnixMilli(), last+1)
		if p.lastOwn.CompareAndSwap(last, next) {
			return time.UnixMilli(next)
		}
	}
}

// openMedia and saveMedia return the ui.Options functions for media, or
// nil without media handling.
func (p *pipeline) openMedia() func(domain.Message) tea.Cmd {
	if p.opts.Media == nil {
		return nil
	}
	return func(msg domain.Message) tea.Cmd { return p.media.openMedia(msg) }
}

// openLink returns the ui.Options.OpenLink function: the web address
// goes to the media opener (xdg-open by default), which hands it to the
// browser.
func (p *pipeline) openLink() func(string) tea.Cmd {
	if p.opts.Media == nil {
		return nil
	}
	return p.media.openLink
}

func (p *pipeline) saveMedia() func(domain.Message) tea.Cmd {
	if p.opts.Media == nil {
		return nil
	}
	return func(msg domain.Message) tea.Cmd { return p.media.saveMedia(msg) }
}

// maxPinned is how many chats WhatsApp lets an account pin.
const maxPinned = 3

// chatSetting returns the ui.Options.ChatSetting function: it changes a
// chat's mute, pin or archive setting through the account's app state
// and stores it at once, as the phone will.
func (p *pipeline) chatSetting() func(domain.ChatID, ui.ChatSetting) tea.Cmd {
	cs, ok := p.opts.Backend.(backend.ChatSettings)
	if !ok {
		return nil
	}
	return func(chat domain.ChatID, s ui.ChatSetting) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			jid, err := p.chatJID(ctx, chat)
			if err != nil {
				return ui.BatchMsg{Notice: "Not changed: " + err.Error()}
			}
			ev := domain.ChatUpdated{ChatJID: jid}
			var done string
			now := p.clock.Now()
			switch s.What {
			case "mute", "unmute":
				until, label := time.Time{}, "Unmuted"
				if s.What == "mute" {
					until, label = domain.MuteForever, "Muted"
					if s.MuteFor > 0 {
						until, label = now.Add(s.MuteFor), "Muted until "+now.Add(s.MuteFor).Local().Format("2 Jan 15:04")
					}
				}
				err, done = cs.MuteChat(ctx, jid, until), label
				ev.Fields, ev.MutedUntil = domain.ChatFieldMuted, until
			case "pin", "unpin":
				pin := s.What == "pin"
				if pin {
					if n, err := p.reader.PinnedCount(ctx); err == nil && n >= maxPinned {
						return ui.BatchMsg{Notice: "At most three chats can be pinned: unpin one first"}
					}
					ev.PinnedAt, done = now, "Pinned"
				} else {
					done = "Unpinned"
				}
				err = cs.PinChat(ctx, jid, pin)
				ev.Fields = domain.ChatFieldPinned
			case "archive", "unarchive":
				ev.Archived = s.What == "archive"
				err = cs.ArchiveChat(ctx, jid, ev.Archived)
				ev.Fields, done = domain.ChatFieldArchived, "Unarchived"
				if ev.Archived {
					ev.Fields |= domain.ChatFieldPinned // archiving unpins
					done = "Archived (:archived shows archived chats)"
				}
			default:
				return nil
			}
			if err != nil {
				p.log.Warn("chat setting not changed", "setting", s.What, "err", err)
				return ui.BatchMsg{Notice: "Not changed: " + err.Error()}
			}
			if err := p.writer.SubmitLive(ctx, []domain.Event{ev}); err != nil {
				return ui.BatchMsg{Notice: "Changed, but not saved: " + err.Error()}
			}
			return ui.BatchMsg{Notice: done}
		}
	}
}

// identityJID returns a person's canonical JID, the LID if known, or ""
// if they are not stored.
func (p *pipeline) identityJID(ctx context.Context, id domain.IdentityID) domain.JID {
	ids, err := p.reader.IdentitiesByID(ctx, []domain.IdentityID{id})
	if err != nil || len(ids) == 0 {
		return ""
	}
	if !ids[0].LID.IsZero() {
		return ids[0].LID
	}
	return ids[0].PN
}

// meJID returns this account's canonical JID: from the options, or, for
// an account linked in this run, from the store.
func (p *pipeline) meJID(ctx context.Context) domain.JID {
	switch {
	case !p.opts.MeLID.IsZero():
		return p.opts.MeLID
	case !p.opts.MePN.IsZero():
		return p.opts.MePN
	}
	me, ok, err := p.reader.Me(ctx)
	if err != nil || !ok {
		return ""
	}
	if !me.LID.IsZero() {
		return me.LID
	}
	return me.PN
}

// react returns the ui.Options.React function: it sends a reaction to a
// message, or removes this account's with an empty emoji, and then
// stores it, so that it shows at once.
func (p *pipeline) react() func(domain.Message, string) tea.Cmd {
	r, ok := p.opts.Backend.(backend.Reacter)
	if !ok {
		return nil
	}
	return func(msg domain.Message, emoji string) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			chat, err := p.chatJID(ctx, msg.ChatID)
			if err != nil {
				return ui.BatchMsg{Notice: "Reaction not sent: " + err.Error()}
			}
			var sender domain.JID
			if !msg.FromMe {
				sender = p.identityJID(ctx, msg.SenderID)
			}
			// The reaction carries the sender's clock in milliseconds
			// (BuildReaction), and so does the copy stored here: a quick
			// change of mind within a second still wins.
			at := p.ownTime()
			if _, err := r.SendReaction(ctx, chat, msg.WAID, sender, emoji); err != nil {
				p.log.Warn("reaction not sent", "err", err)
				return ui.BatchMsg{Notice: "Reaction not sent: " + err.Error()}
			}
			me := p.meJID(ctx)
			if me.IsZero() {
				return nil // shown once the phone's echo arrives
			}
			ev := domain.ReactionReceived{ChatJID: chat, TargetWAID: msg.WAID, SenderJID: me, Emoji: emoji, Timestamp: at}
			if err := p.writer.SubmitLive(ctx, []domain.Event{ev}); err != nil {
				return ui.BatchMsg{Notice: "Reaction sent, but not saved: " + err.Error()}
			}
			return nil
		}
	}
}

// edit returns the ui.Options.Edit function: it sends a new text for
// this account's message, within domain.EditWindow, and stores it.
func (p *pipeline) edit() func(domain.Message, string) tea.Cmd {
	e, ok := p.opts.Backend.(backend.Editor)
	if !ok {
		return nil
	}
	return func(msg domain.Message, body string) tea.Cmd {
		return func() tea.Msg {
			switch {
			case !msg.FromMe || msg.Kind != domain.KindText || msg.IsRevoked():
				return ui.BatchMsg{Notice: "Only your own text messages can be edited"}
			case p.clock.Now().Sub(msg.Timestamp) > domain.EditWindow:
				return ui.BatchMsg{Notice: "A message can be edited for 20 minutes after it is sent"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			chat, err := p.chatJID(ctx, msg.ChatID)
			if err == nil {
				err = e.EditText(ctx, chat, msg.WAID, body)
			}
			if err != nil {
				p.log.Warn("edit not sent", "err", err)
				return ui.BatchMsg{Notice: "Edit not sent: " + err.Error()}
			}
			ev := domain.MessageEdited{ChatJID: chat, TargetWAID: msg.WAID, Body: body, EditedAt: p.ownTime(), Actor: p.meJID(ctx)}
			if err := p.writer.SubmitLive(ctx, []domain.Event{ev}); err != nil {
				return ui.BatchMsg{Notice: "Edit sent, but not saved: " + err.Error()}
			}
			return nil
		}
	}
}

// revoke returns the ui.Options.Delete function: it deletes a message for
// everyone, this account's own within domain.RevokeWindow or, as a group
// admin, someone else's, and stores the deletion.
func (p *pipeline) revoke() func(domain.Message) tea.Cmd {
	e, ok := p.opts.Backend.(backend.Editor)
	if !ok {
		return nil
	}
	return func(msg domain.Message) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if msg.IsRevoked() || msg.WAID == "" {
				return nil
			}
			chat, err := p.chatJID(ctx, msg.ChatID)
			if err != nil {
				return ui.BatchMsg{Notice: "Not deleted: " + err.Error()}
			}
			var sender domain.JID
			switch {
			case msg.FromMe && p.clock.Now().Sub(msg.Timestamp) > domain.RevokeWindow:
				return ui.BatchMsg{Notice: "A message can be deleted for everyone for two days after it is sent"}
			case !msg.FromMe:
				if !chat.IsGroup() || !p.isAdmin(ctx, msg.ChatID) {
					return ui.BatchMsg{Notice: "Only your own messages, or as a group admin anyone's, can be deleted"}
				}
				sender = p.identityJID(ctx, msg.SenderID)
			}
			if err := e.Revoke(ctx, chat, msg.WAID, sender); err != nil {
				p.log.Warn("deletion not sent", "err", err)
				return ui.BatchMsg{Notice: "Not deleted: " + err.Error()}
			}
			ev := domain.MessageRevoked{ChatJID: chat, TargetWAID: msg.WAID, RevokedAt: p.clock.Now(), Actor: p.meJID(ctx), ByAdmin: !msg.FromMe}
			if err := p.writer.SubmitLive(ctx, []domain.Event{ev}); err != nil {
				return ui.BatchMsg{Notice: "Deleted, but not saved: " + err.Error()}
			}
			return nil
		}
	}
}

// isAdmin reports whether this account is an admin of a group.
func (p *pipeline) isAdmin(ctx context.Context, chat domain.ChatID) bool {
	me, ok, err := p.reader.Me(ctx)
	if err != nil || !ok {
		return false
	}
	role, ok, err := p.reader.GroupRole(ctx, chat, me.ID)
	return err == nil && ok && role >= domain.RoleAdmin
}

// maxMentionable bounds the members loaded for mention completion; it is
// above WhatsApp's group size limit.
const maxMentionable = 5000

// mentionable is the ui.Options.Mentionable function: the members of a
// group but this account, by their current names, with their canonical
// JIDs, for completing mentions.
func (p *pipeline) mentionable(chat domain.ChatID) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := p.reader.GroupMembers(ctx, chat, 0, maxMentionable)
		if err != nil {
			return ui.MentionCandidatesMsg{Chat: chat}
		}
		ids := make([]domain.IdentityID, len(rows))
		for i, r := range rows {
			ids[i] = r.Identity
		}
		people, err := p.reader.IdentitiesByID(ctx, ids)
		if err != nil {
			return ui.MentionCandidatesMsg{Chat: chat}
		}
		out := make([]ui.MentionCandidate, 0, len(people))
		for i := range people {
			who := &people[i]
			if who.IsMe || (!who.LID.IsZero() && who.LID == p.opts.MeLID) || (!who.PN.IsZero() && who.PN == p.opts.MePN) {
				continue
			}
			jid := who.LID
			if jid.IsZero() {
				jid = who.PN
			}
			out = append(out, ui.MentionCandidate{Name: displayName(who), JID: jid})
		}
		return ui.MentionCandidatesMsg{Chat: chat, Candidates: out}
	}
}

// onDemandCount is how many older messages one request asks the phone
// for.
const onDemandCount = 50

// older returns the ui.Options.Older function: when the oldest stored
// message of a chat comes into view, the phone is asked for older ones
// through the job pool, unless the chat's history is complete.
func (p *pipeline) older() func(domain.ChatID, domain.Message) tea.Cmd {
	req, ok := p.opts.Backend.(backend.HistoryRequester)
	if !ok {
		return nil
	}
	return func(chat domain.ChatID, oldest domain.Message) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			chats, err := p.reader.ChatsByID(ctx, []domain.ChatID{chat})
			if err != nil || len(chats) == 0 || oldest.WAID == "" {
				return nil
			}
			c := chats[0]
			if c.HistoryComplete {
				return ui.BatchMsg{Notice: "This is the start of the chat"}
			}
			before := backend.HistoryAnchor{WAID: oldest.WAID, FromMe: oldest.FromMe, Timestamp: oldest.Timestamp}
			err = p.jobs.Submit("history.request "+string(c.JID)+" "+string(oldest.WAID), func(ctx context.Context) error {
				return req.RequestHistory(ctx, c.JID, before, onDemandCount)
			})
			if err != nil {
				return ui.BatchMsg{Notice: "Older messages cannot be asked for now"}
			}
			return ui.BatchMsg{Notice: "Asking your phone for older messages…"}
		}
	}
}

// send returns the ui.Options.Send function: the message joins its
// chat's queue at once, in the order of typing (see sender.go).
func (p *pipeline) send(s *sender, b backend.Backend) func(ui.Outgoing) tea.Cmd {
	return func(out ui.Outgoing) tea.Cmd {
		s.enqueue(sendJob{chat: out.Chat, waid: b.NewMessageID(), body: out.Text, quoted: out.Quoted, mentions: out.Mentions, at: p.clock.Now()})
		return nil
	}
}

// forward returns the ui.Options.Forward function: the copy joins the
// target chat's queue like a message typed there. Nil if the backend
// cannot forward.
func (p *pipeline) forward(s *sender, b backend.Backend) func(domain.Message, domain.ChatID) tea.Cmd {
	if _, ok := b.(backend.Forwarder); !ok {
		return nil
	}
	return func(m domain.Message, to domain.ChatID) tea.Cmd {
		s.enqueue(sendJob{chat: to, waid: b.NewMessageID(), at: p.clock.Now(), forward: &forwardSource{chat: m.ChatID, waid: m.WAID}})
		return nil
	}
}

// retry returns the ui.Options.Retry function: a failed own text message
// is sent again with its own ID, so a recipient who did get it the first
// time sees it once. A failed forward is forwarded again.
func (p *pipeline) retry(s *sender) func(domain.Message) tea.Cmd {
	return func(m domain.Message) tea.Cmd {
		forward := m.Flags.Has(domain.FlagForwarded)
		if !m.FromMe || m.Status != domain.StatusFailed || (m.Kind != domain.KindText && !m.Kind.HasMedia() && !forward) {
			return nil
		}
		j := sendJob{chat: m.ChatID, waid: m.WAID, body: m.Body, quoted: m.QuotedWAID, kind: m.Kind, at: m.Timestamp, retry: true}
		if forward {
			j.forward = &forwardSource{chat: m.ChatID, waid: m.WAID}
		}
		s.enqueue(j)
		p.ui.Notice("Sending again…")
		return nil
	}
}
