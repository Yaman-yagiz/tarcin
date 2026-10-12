// SPDX-License-Identifier: GPL-3.0-or-later

package fake

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// run holds the state of one Run call. It is owned by the Run goroutine.
type run struct {
	b    *Backend
	sink backend.Sink
	// Content and fault decisions use separate streams, so enabling a
	// fault never changes which messages are generated.
	gen    *synth.Gen
	faults *rand.Rand
	start  time.Time
	recent map[int][]domain.WAMessageID // recent message IDs per chat, for quotes

	generated int     // live messages generated so far
	acc       float64 // fractional messages owed by the rate
	window    []domain.Event
	later     []deferred  // duplicates waiting for later delivery
	retry     []timed     // batches the sink rejected
	script    int         // next script entry
	flapAt    time.Time   // next flap start
	downUntil time.Time   // end of the current flap
	onDemand  map[int]int // older messages given so far per chat
}

type deferred struct {
	afterSeq int
	event    domain.Event
}

// Run implements backend.Backend. It delivers an initial sync, the
// configured history and then live traffic, and returns nil when ctx is
// canceled or, if MaxEvents is set, once every live message and receipt
// has been delivered.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	r := &run{
		b:        b,
		sink:     sink,
		gen:      synth.New(b.cfg.Seed ^ 0x7a1c),
		faults:   rand.New(rand.NewPCG(b.cfg.Seed, 0xd1ce)), //nolint:gosec // fault injection, not security-sensitive
		start:    b.cfg.Clock(),
		recent:   map[int][]domain.WAMessageID{},
		onDemand: map[int]int{},
	}
	defer b.online.Store(false)
	if b.cfg.FlapEvery > 0 {
		r.flapAt = r.start.Add(b.cfg.FlapEvery)
	}

	if b.cfg.Link && !r.link(ctx) {
		return nil
	}
	r.connect(ctx)
	r.deliverDurable(ctx, r.start, r.initialSync())
	for _, batch := range r.history() {
		r.deliverDurable(ctx, r.start, batch)
	}

	ticker := time.NewTicker(b.cfg.Tick)
	defer ticker.Stop()
	last := r.start
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		now := b.cfg.Clock()
		r.step(ctx, now, now.Sub(last))
		last = now
		if r.done() {
			return nil
		}
	}
}

func (r *run) connect(ctx context.Context) {
	r.deliverEphemeral(ctx, domain.ConnectionChanged{State: domain.ConnConnecting})
	r.b.online.Store(true)
	r.deliverEphemeral(ctx, domain.ConnectionChanged{State: domain.ConnOnline})
}

// step advances the simulation by dt.
func (r *run) step(ctx context.Context, now time.Time, dt time.Duration) {
	cfg := &r.b.cfg

	if !r.flapAt.IsZero() && !now.Before(r.flapAt) && r.downUntil.IsZero() {
		r.b.online.Store(false)
		r.deliverEphemeral(ctx, domain.ConnectionChanged{State: domain.ConnDisconnected, Reason: "simulated network flap"})
		r.downUntil = now.Add(cfg.FlapDowntime)
	}
	if !r.downUntil.IsZero() {
		select {
		case <-r.b.reconnect:
			r.downUntil = now
		default:
		}
		if now.Before(r.downUntil) {
			// Offline: traffic accumulates and arrives as a burst on reconnect.
			r.acc += cfg.Rate * dt.Seconds()
			return
		}
		r.downUntil = time.Time{}
		r.flapAt = now.Add(cfg.FlapEvery)
		r.connect(ctx)
	}

	for r.script < len(cfg.Script) && now.Sub(r.start) >= cfg.Script[r.script].At {
		r.deliverDurable(ctx, now, cfg.Script[r.script].Events)
		r.script++
	}

	r.acc += cfg.Rate * dt.Seconds()
	for r.acc >= 1 && (cfg.MaxEvents == 0 || r.generated < cfg.MaxEvents) {
		r.acc--
		r.generated++
		r.live(ctx, now)
	}
	if cfg.MaxEvents > 0 && r.generated >= cfg.MaxEvents {
		r.flushWindow(ctx, now)
		for _, d := range r.later {
			r.deliverDurable(ctx, now, []domain.Event{d.event})
		}
		r.later = nil
	}

	for _, batch := range r.b.takeReceipts(now) {
		r.deliverDurable(ctx, now, batch)
	}
	for drained := false; !drained; {
		select {
		case req := <-r.b.older:
			r.deliverDurable(ctx, now, r.olderHistory(req))
		case jid := <-r.b.presence:
			r.deliverEphemeral(ctx, domain.PresenceChanged{JID: jid, Online: true})
		default:
			drained = true
		}
	}
	retry := r.retry
	r.retry = nil
	for _, t := range retry {
		if t.due.After(now) {
			r.retry = append(r.retry, t)
			continue
		}
		r.deliverDurable(ctx, now, t.events)
	}
}

// olderHistory answers a history request: up to count messages before
// the request's time, an hour apart, and the end of the chat's history
// once OnDemandHistory messages were given.
func (r *run) olderHistory(req olderRequest) []domain.Event {
	i := slices.IndexFunc(r.b.chats, func(c Chat) bool { return c.JID == req.chat })
	if i < 0 {
		return nil
	}
	n := min(req.count, r.b.cfg.OnDemandHistory-r.onDemand[i])
	var evs []domain.Event
	ts := req.before
	for range max(n, 0) {
		ts = ts.Add(-time.Hour)
		evs = append(evs, r.message(i, ts.Truncate(time.Second), domain.SourceHistory))
	}
	r.onDemand[i] += max(n, 0)
	if r.onDemand[i] >= r.b.cfg.OnDemandHistory {
		evs = append(evs, domain.ChatUpdated{ChatJID: req.chat, Fields: domain.ChatFieldHistoryComplete, HistoryComplete: true})
	}
	return evs
}

// live generates one live message (and possibly a typing indicator and a
// duplicate) and delivers it through the reorder window.
func (r *run) live(ctx context.Context, now time.Time) {
	cfg := &r.b.cfg
	chat := r.pickChat()
	ev := r.message(chat, now.Truncate(time.Second), domain.SourceLive)
	if r.faults.Float64() < cfg.TypingRate {
		m := ev.Message
		r.deliverEphemeral(ctx, domain.ChatTyping{ChatJID: m.ChatJID, SenderJID: m.SenderJID, Typing: true})
	}
	r.window = append(r.window, ev)
	if r.faults.Float64() < cfg.DuplicateRate {
		r.later = append(r.later, deferred{afterSeq: r.generated + 1 + r.faults.IntN(10), event: ev})
	}
	if len(r.window) >= max(cfg.ReorderWindow, 1) {
		r.flushWindow(ctx, now)
	}
	keep := r.later[:0]
	for _, d := range r.later {
		if d.afterSeq <= r.generated {
			r.deliverDurable(ctx, now, []domain.Event{d.event})
			continue
		}
		keep = append(keep, d)
	}
	r.later = keep
}

func (r *run) flushWindow(ctx context.Context, now time.Time) {
	if len(r.window) > 1 {
		r.faults.Shuffle(len(r.window), func(i, j int) { r.window[i], r.window[j] = r.window[j], r.window[i] })
	}
	for _, ev := range r.window {
		// Live messages arrive one at a time, as from whatsmeow.
		r.deliverDurable(ctx, now, []domain.Event{ev})
	}
	r.window = r.window[:0]
}

func (r *run) done() bool {
	return r.b.cfg.MaxEvents > 0 && r.generated >= r.b.cfg.MaxEvents &&
		len(r.window) == 0 && len(r.later) == 0 && len(r.retry) == 0 && !r.b.pendingReceipts()
}

// deliverDurable delivers a batch and schedules a redelivery if the sink
// rejects it, as the server would.
func (r *run) deliverDurable(ctx context.Context, now time.Time, events []domain.Event) {
	if len(events) == 0 || ctx.Err() != nil {
		return
	}
	if err := r.sink.Deliver(ctx, events); err != nil && ctx.Err() == nil {
		r.retry = append(r.retry, timed{due: now.Add(r.b.cfg.RedeliverDelay), events: events})
	}
}

func (r *run) deliverEphemeral(ctx context.Context, ev domain.Event) {
	if ctx.Err() == nil {
		_ = r.sink.Deliver(ctx, []domain.Event{ev}) // ephemeral: losing one is harmless
	}
}

// initialSync describes the account: names, PN/LID links, chat metadata
// and group participants, as an app-state and history bootstrap would.
func (r *run) initialSync() []domain.Event {
	b := r.b
	var evs []domain.Event
	seen := map[domain.JID]bool{}
	addPerson := func(p Person) {
		if seen[p.LID] {
			return
		}
		seen[p.LID] = true
		evs = append(evs,
			domain.IdentityLinked{PN: p.PN, LID: p.LID},
			domain.ContactUpdated{JID: p.LID, PushName: p.Name})
	}
	addPerson(b.me)
	for _, c := range b.chats {
		switch c.Kind {
		case domain.ChatDirect:
			addPerson(c.Peer)
			evs = append(evs, domain.ChatUpdated{ChatJID: c.JID, Fields: domain.ChatFieldKind, Kind: c.Kind})
		case domain.ChatGroup:
			for _, m := range c.Members {
				addPerson(m)
			}
			evs = append(evs,
				domain.ChatUpdated{ChatJID: c.JID, Fields: domain.ChatFieldKind | domain.ChatFieldTitle, Kind: c.Kind, Title: c.Title},
				b.groupMembers(&c))
		}
	}
	return evs
}

// history returns InitialHistory messages per chat in batches of up to
// HistoryChunk messages of one chat, oldest first within each chat. The
// chats take turns, one batch each, as in a history sync that brings every
// conversation before the long tail of a busy one: small chats are
// complete after the first round.
func (r *run) history() [][]domain.Event {
	cfg := &r.b.cfg
	if cfg.InitialHistory <= 0 && cfg.HotChatHistory <= 0 {
		return nil
	}
	perChat := make([][][]domain.Event, len(r.b.chats))
	for i := range r.b.chats {
		n := cfg.InitialHistory
		if i == 0 && cfg.HotChatHistory > 0 {
			n = cfg.HotChatHistory
		}
		if n <= 0 {
			continue
		}
		step := cfg.HistorySpan / time.Duration(n)
		ts := r.start.Add(-cfg.HistorySpan)
		var batch []domain.Event
		for range n {
			ts = ts.Add(step)
			batch = append(batch, r.message(i, ts.Truncate(time.Second), domain.SourceHistory))
			if len(batch) == cfg.HistoryChunk {
				perChat[i] = append(perChat[i], batch)
				batch = nil
			}
		}
		if len(batch) > 0 {
			perChat[i] = append(perChat[i], batch)
		}
	}
	var batches [][]domain.Event
	for round := 0; ; round++ {
		added := false
		for _, chunks := range perChat {
			if round < len(chunks) {
				batches = append(batches, chunks[round])
				added = true
			}
		}
		if !added {
			return batches
		}
	}
}

func (r *run) pickChat() int {
	cfg := &r.b.cfg
	if cfg.HotChatMembers > 0 && r.gen.Rand().Float64() < cfg.HotChatShare {
		return 0
	}
	return r.gen.Rand().IntN(len(r.b.chats))
}

// message generates a message event in chat i.
func (r *run) message(i int, ts time.Time, src domain.Source) domain.MessageReceived {
	b, g := r.b, r.gen
	rnd := g.Rand()
	c := b.chats[i]

	sender := c.Peer
	if c.Kind == domain.ChatGroup {
		sender = c.Members[rnd.IntN(len(c.Members))]
	}
	fromMe := rnd.IntN(100) < 8
	if fromMe {
		sender = b.me
	}
	chatJID, senderJID, alt := c.JID, sender.LID, sender.PN
	if r.faults.Float64() < b.cfg.PNAddressingRate {
		senderJID, alt = sender.PN, sender.LID
		if c.Kind == domain.ChatDirect {
			chatJID = c.Peer.PN
		}
	}

	m := domain.IncomingMessage{
		ChatJID:   chatJID,
		SenderJID: senderJID,
		SenderAlt: alt,
		PushName:  sender.Name,
		WAID:      g.WAID(),
		FromMe:    fromMe,
		Timestamp: ts,
		Kind:      domain.KindText,
		Body:      g.Message(),
	}
	if rec := r.recent[i]; len(rec) > 0 && rnd.IntN(10) == 0 {
		m.QuotedWAID = rec[rnd.IntN(len(rec))]
	}
	switch p := rnd.IntN(100); {
	case p < 5:
		m.Kind = domain.KindImage
		m.Media = &domain.Media{MIME: "image/jpeg", Size: 20_000 + rnd.Int64N(3_000_000), Width: 1280, Height: 960}
		if rnd.IntN(2) == 0 {
			m.Body = ""
		}
	case p < 6:
		m.Kind = domain.KindDocument
		m.Media = &domain.Media{MIME: "application/pdf", Size: 50_000 + rnd.Int64N(9_000_000), FileName: "rapor-" + string(m.WAID[16:]) + ".pdf"}
		m.Body = ""
	case p < 7:
		m.Kind = domain.KindUnsupported
		m.Body = ""
		m.Raw = []byte{0x0a, 0x00}
	}

	r.recent[i] = append(r.recent[i], m.WAID)
	if len(r.recent[i]) > 20 {
		r.recent[i] = r.recent[i][1:]
	}
	return domain.MessageReceived{Message: m, Source: src}
}
