// SPDX-License-Identifier: GPL-3.0-or-later

package fake

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// recorder is a Sink that records batches, optionally rejecting some.
type recorder struct {
	mu      sync.Mutex
	batches [][]domain.Event
	reject  func(batch []domain.Event) bool
	notify  chan struct{}
}

func newRecorder() *recorder { return &recorder{notify: make(chan struct{}, 1)} }

func (r *recorder) Deliver(_ context.Context, evs []domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reject != nil && r.reject(evs) {
		return errors.New("rejected")
	}
	r.batches = append(r.batches, slices.Clone(evs))
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}

func (r *recorder) events() []domain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Event
	for _, b := range r.batches {
		out = append(out, b...)
	}
	return out
}

func messages(evs []domain.Event, src domain.Source) []domain.IncomingMessage {
	var out []domain.IncomingMessage
	for _, ev := range evs {
		if m, ok := ev.(domain.MessageReceived); ok && m.Source == src {
			out = append(out, m.Message)
		}
	}
	return out
}

// runUntil runs b until done reports true or five seconds pass.
func runUntil(t *testing.T, b *Backend, rec *recorder, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- b.Run(ctx, rec) }()
	for !done() {
		select {
		case <-rec.notify:
		case err := <-errc:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !done() {
				t.Fatal("Run returned before the condition was met")
			}
			return
		case <-ctx.Done():
			t.Fatal("timed out")
		}
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestInitialSyncAndHistory(t *testing.T) {
	b := New(Config{Seed: 1, Chats: 6, MembersPerGroup: 5, InitialHistory: 120, HistoryChunk: 50})
	rec := newRecorder()
	runUntil(t, b, rec, func() bool {
		return len(messages(rec.events(), domain.SourceHistory)) == 6*120
	})

	evs := rec.events()
	if c, ok := evs[0].(domain.ConnectionChanged); !ok || c.State != domain.ConnConnecting {
		t.Fatalf("first event = %#v, want ConnConnecting", evs[0])
	}
	var links, groups, titles int
	for _, ev := range evs {
		switch e := ev.(type) {
		case domain.IdentityLinked:
			if !e.PN.IsPN() || !e.LID.IsLID() {
				t.Fatalf("bad link %+v", e)
			}
			links++
		case domain.GroupMembersChanged:
			if !e.Full || len(e.Members) != 6 { // 5 members + me
				t.Fatalf("group members = %d (full=%v), want 6", len(e.Members), e.Full)
			}
			groups++
		case domain.ChatUpdated:
			if e.Fields.Has(domain.ChatFieldTitle) {
				titles++
			}
		}
	}
	if links == 0 || groups == 0 || groups != titles {
		t.Fatalf("links=%d groups=%d titles=%d", links, groups, titles)
	}

	for _, batch := range rec.batches {
		if n := len(messages(batch, domain.SourceHistory)); n > 50 {
			t.Fatalf("history batch of %d exceeds chunk size", n)
		}
	}
	hist := messages(evs, domain.SourceHistory)
	seen := map[domain.WAMessageID]bool{}
	last := map[domain.JID]time.Time{}
	for _, m := range hist {
		if seen[m.WAID] {
			t.Fatalf("duplicate WAID %s in history", m.WAID)
		}
		seen[m.WAID] = true
		if m.Timestamp.Before(last[m.ChatJID]) {
			t.Fatalf("history of %s is not oldest-first", m.ChatJID)
		}
		last[m.ChatJID] = m.Timestamp
		if m.Timestamp.Nanosecond() != 0 {
			t.Fatal("timestamps must have one-second resolution")
		}
	}
}

// runToCompletion runs b until Run returns by itself (MaxEvents reached).
func runToCompletion(t *testing.T, b *Backend, rec *recorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Run(ctx, rec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Run did not finish before the timeout")
	}
}

func liveSequence(t *testing.T, cfg Config) []domain.IncomingMessage {
	t.Helper()
	rec := newRecorder()
	runToCompletion(t, New(cfg), rec)
	return messages(rec.events(), domain.SourceLive)
}

func TestLiveTrafficIsDeterministic(t *testing.T) {
	cfg := Config{Seed: 7, Rate: 5000, MaxEvents: 300, Tick: time.Millisecond, HotChatMembers: 200, HotChatShare: 0.5}
	a, b := liveSequence(t, cfg), liveSequence(t, cfg)
	if len(a) != 300 || len(b) != 300 {
		t.Fatalf("got %d and %d live messages, want 300", len(a), len(b))
	}
	hot := New(cfg).Chats()[0]
	var inHot int
	for i := range a {
		x, y := a[i], b[i] // timestamps come from the wall clock and may differ
		if x.WAID != y.WAID || x.Body != y.Body || x.ChatJID != y.ChatJID || x.SenderJID != y.SenderJID {
			t.Fatalf("message %d differs between runs", i)
		}
		if a[i].ChatJID == hot.JID {
			inHot++
		}
	}
	if len(hot.Members) != 200 || inHot < 100 {
		t.Fatalf("hot chat: %d members, %d of 300 messages", len(hot.Members), inHot)
	}
}

func TestDuplicatesAndReordering(t *testing.T) {
	base := Config{Seed: 3, Rate: 5000, MaxEvents: 400, Tick: time.Millisecond}
	ordered := liveSequence(t, base)

	faulty := base
	faulty.DuplicateRate, faulty.ReorderWindow = 0.3, 8
	got := liveSequence(t, faulty)

	count := map[domain.WAMessageID]int{}
	for _, m := range got {
		count[m.WAID]++
	}
	var dups int
	for _, m := range ordered {
		switch count[m.WAID] {
		case 0:
			t.Fatalf("message %s was never delivered", m.WAID)
		case 2:
			dups++
		}
	}
	if len(count) != len(ordered) || dups == 0 {
		t.Fatalf("distinct=%d want %d, duplicates=%d", len(count), len(ordered), dups)
	}
	if slices.EqualFunc(ordered, got[:len(ordered)], func(a, b domain.IncomingMessage) bool { return a.WAID == b.WAID }) {
		t.Fatal("reorder window did not change the delivery order")
	}
}

func TestRedeliveryAfterRejection(t *testing.T) {
	b := New(Config{Seed: 5, Rate: 2000, MaxEvents: 60, Tick: time.Millisecond, RedeliverDelay: 5 * time.Millisecond})
	rec := newRecorder()
	attempts := map[domain.WAMessageID]int{}
	rec.reject = func(batch []domain.Event) bool {
		m, ok := batch[0].(domain.MessageReceived)
		if !ok {
			return false
		}
		attempts[m.Message.WAID]++
		return attempts[m.Message.WAID] == 1 && len(attempts)%3 == 0 // reject every third message once
	}
	runToCompletion(t, b, rec)

	got := messages(rec.events(), domain.SourceLive)
	if len(got) != 60 {
		t.Fatalf("accepted %d messages, want all 60 after redelivery", len(got))
	}
	var retried int
	for _, n := range attempts {
		if n == 2 {
			retried++
		}
	}
	if retried == 0 {
		t.Fatal("no message was redelivered")
	}
}

func TestSendText(t *testing.T) {
	ctx := context.Background()
	b := New(Config{Seed: 9, AutoReceipts: true, Tick: time.Millisecond})
	chat := b.Chats()[0].JID
	if _, err := b.SendText(ctx, backend.SendRequest{ChatJID: chat, WAID: "X", Body: "hi"}); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("send while offline: err = %v, want ErrNotConnected", err)
	}

	rec := newRecorder()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = b.Run(runCtx, rec) }()
	deadline := time.Now().Add(5 * time.Second)
	for !b.online.Load() {
		if time.Now().After(deadline) {
			t.Fatal("never came online")
		}
		time.Sleep(time.Millisecond)
	}

	id := b.NewMessageID()
	res, err := b.SendText(ctx, backend.SendRequest{ChatJID: chat, WAID: id, Body: "merhaba"})
	if err != nil || res.Timestamp.IsZero() {
		t.Fatalf("SendText: %+v, %v", res, err)
	}
	if sent := b.Sent(); len(sent) != 1 || sent[0].WAID != id {
		t.Fatalf("Sent() = %+v", sent)
	}
	if err := b.MarkRead(ctx, chat, chat, []domain.WAMessageID{"A"}, time.Now()); err != nil || len(b.MarkedRead()) != 1 {
		t.Fatalf("MarkRead: %v, %v", err, b.MarkedRead())
	}

	var statuses []domain.MessageStatus
	for len(statuses) < 2 && time.Now().Before(deadline) {
		statuses = statuses[:0]
		for _, ev := range rec.events() {
			if r, ok := ev.(domain.ReceiptReceived); ok && r.WAIDs[0] == id {
				statuses = append(statuses, r.Status)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Equal(statuses, []domain.MessageStatus{domain.StatusDelivered, domain.StatusRead}) {
		t.Fatalf("receipts = %v, want delivered then read", statuses)
	}

	failing := New(Config{Seed: 9, SendFailRate: 1})
	failing.online.Store(true)
	if _, err := failing.SendText(ctx, backend.SendRequest{ChatJID: chat, WAID: "Y"}); !errors.Is(err, ErrSendFailed) {
		t.Fatalf("injected failure: err = %v", err)
	}
}

func TestConnectionFlap(t *testing.T) {
	b := New(Config{Seed: 2, Rate: 200, FlapEvery: 30 * time.Millisecond, FlapDowntime: 20 * time.Millisecond, Tick: time.Millisecond})
	rec := newRecorder()
	var states []domain.ConnectionState
	runUntil(t, b, rec, func() bool {
		states = states[:0]
		for _, ev := range rec.events() {
			if c, ok := ev.(domain.ConnectionChanged); ok {
				states = append(states, c.State)
			}
		}
		return len(states) >= 5
	})
	want := []domain.ConnectionState{domain.ConnConnecting, domain.ConnOnline, domain.ConnDisconnected, domain.ConnConnecting, domain.ConnOnline}
	if !slices.Equal(states[:5], want) {
		t.Fatalf("states = %v, want prefix %v", states, want)
	}
}

func TestScript(t *testing.T) {
	ev := domain.ChatTyping{ChatJID: "1@lid", Typing: true}
	b := New(Config{Seed: 4, Tick: time.Millisecond, Script: []Scripted{
		{At: 5 * time.Millisecond, Events: []domain.Event{ev}},
		{At: 10 * time.Millisecond, Events: []domain.Event{domain.MessageRevoked{ChatJID: "1@lid", TargetWAID: "Z"}}},
	}})
	rec := newRecorder()
	runUntil(t, b, rec, func() bool {
		for _, e := range rec.events() {
			if _, ok := e.(domain.MessageRevoked); ok {
				return true
			}
		}
		return false
	})
	var sawTyping bool
	for _, e := range rec.events() {
		if e == domain.Event(ev) {
			sawTyping = true
		}
		if _, ok := e.(domain.MessageRevoked); ok && !sawTyping {
			t.Fatal("scripted events were delivered out of order")
		}
	}
}

func TestDirectory(t *testing.T) {
	b := New(Config{Seed: 11, Chats: 10, GroupRatio: 0.5, MembersPerGroup: 4})
	var groups int
	for _, c := range b.Chats() {
		switch c.Kind {
		case domain.ChatGroup:
			groups++
			if !c.JID.IsGroup() || len(c.Members) != 4 || c.Title == "" {
				t.Fatalf("bad group %+v", c)
			}
		case domain.ChatDirect:
			if c.JID != c.Peer.LID {
				t.Fatalf("direct chat JID %s is not the peer's LID", c.JID)
			}
		}
	}
	if groups != 5 {
		t.Fatalf("groups = %d, want 5", groups)
	}
	if !b.Me().LID.IsLID() {
		t.Fatal("Me() has no LID")
	}
	if New(Config{Seed: 11, Chats: 10, GroupRatio: 0.5, MembersPerGroup: 4}).Chats()[3].JID != b.Chats()[3].JID {
		t.Fatal("directory is not deterministic")
	}
}

func TestHotChatHistory(t *testing.T) {
	b := New(Config{Seed: 6, Chats: 3, InitialHistory: 10, HotChatHistory: 250, HistoryChunk: 100})
	rec := newRecorder()
	runUntil(t, b, rec, func() bool {
		return len(messages(rec.events(), domain.SourceHistory)) == 250+2*10
	})
	hot := b.Chats()[0].JID
	var n int
	for _, m := range messages(rec.events(), domain.SourceHistory) {
		if m.ChatJID == hot {
			n++
		}
	}
	if n != 250 {
		t.Fatalf("hot chat history = %d, want 250", n)
	}

	// The chats take turns: the small chats are complete before the rest
	// of the hot chat's history arrives.
	rest := 0
	for i, m := range messages(rec.events(), domain.SourceHistory) {
		if m.ChatJID != hot {
			rest = i
		}
	}
	if rest >= 100+2*10 {
		t.Fatalf("the last small-chat message came at position %d, after the hot chat's first batch", rest)
	}
}

func TestLinking(t *testing.T) {
	b := New(Config{Seed: 7, Chats: 2, Link: true, LinkAfter: 30 * time.Millisecond, LinkCodeEvery: 10 * time.Millisecond, Tick: time.Millisecond})
	rec := newRecorder()
	runUntil(t, b, rec, func() bool {
		for _, ev := range rec.events() {
			if _, ok := ev.(domain.ChatUpdated); ok {
				return true
			}
		}
		return false
	})
	evs := rec.events()
	qr, linkedAt, syncAt := 0, -1, -1
	for i, ev := range evs {
		switch e := ev.(type) {
		case domain.LinkProgress:
			if e.State == domain.LinkQR && e.QR != "" {
				qr++
			}
		case domain.AccountLinked:
			if e.LID != b.Me().LID || e.PN != b.Me().PN {
				t.Fatalf("linked as %+v", e)
			}
			linkedAt = i
		case domain.ChatUpdated:
			if syncAt < 0 {
				syncAt = i
			}
		}
	}
	if qr < 2 || linkedAt < 0 || syncAt < linkedAt {
		t.Fatalf("QR codes %d, linked at %d, sync at %d: codes rotate, then the link, then the sync", qr, linkedAt, syncAt)
	}
}

func TestLinkingWithPhoneAndPasskey(t *testing.T) {
	b := New(Config{Seed: 8, Chats: 2, Link: true, LinkAfter: time.Hour, LinkPasskey: true, Tick: time.Millisecond})
	if _, err := b.PairWithPhone(context.Background(), "123"); err == nil {
		t.Fatal("a short number is refused")
	}
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, rec) }()
	state := func(s domain.LinkState) func() bool {
		return func() bool {
			for _, ev := range rec.events() {
				if p, ok := ev.(domain.LinkProgress); ok && p.State == s {
					return true
				}
			}
			return false
		}
	}
	waitFor(t, state(domain.LinkQR))
	if code, err := b.PairWithPhone(ctx, "905551112233"); err != nil || code == "" {
		t.Fatalf("PairWithPhone = %q, %v", code, err)
	}
	waitFor(t, state(domain.LinkPasskey))
	if err := b.SubmitPasskey(ctx, `{"id":"x"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, state(domain.LinkConfirm))
	if err := b.ConfirmLink(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, ev := range rec.events() {
			if _, ok := ev.(domain.AccountLinked); ok {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReconnectEndsAFlap(t *testing.T) {
	b := New(Config{Seed: 9, Chats: 1, FlapEvery: 20 * time.Millisecond, FlapDowntime: time.Hour, Tick: time.Millisecond})
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, rec) }()
	states := func() []domain.ConnectionState {
		var out []domain.ConnectionState
		for _, ev := range rec.events() {
			if c, ok := ev.(domain.ConnectionChanged); ok {
				out = append(out, c.State)
			}
		}
		return out
	}
	waitFor(t, func() bool { return slices.Contains(states(), domain.ConnDisconnected) })
	b.Reconnect()
	waitFor(t, func() bool {
		s := states()
		return s[len(s)-1] == domain.ConnOnline
	})
}

func TestRequestHistory(t *testing.T) {
	b := New(Config{Seed: 3, Chats: 2, OnDemandHistory: 70, Tick: time.Millisecond})
	chat := b.Chats()[1].JID
	if err := b.RequestHistory(t.Context(), chat, backend.HistoryAnchor{}, 50); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("offline: %v", err)
	}
	var mu sync.Mutex
	var older []domain.MessageReceived
	complete := 0
	sink := backend.SinkFunc(func(_ context.Context, evs []domain.Event) error {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range evs {
			switch e := ev.(type) {
			case domain.MessageReceived:
				if e.Message.Timestamp.Before(time.Unix(1, 0).AddDate(30, 0, 0)) {
					older = append(older, e)
				}
			case domain.ChatUpdated:
				if e.HistoryComplete {
					complete++
				}
			}
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, sink) }()
	for !b.online.Load() {
		time.Sleep(time.Millisecond)
	}
	before := time.Unix(1, 0).AddDate(20, 0, 0)
	for range 2 {
		if err := b.RequestHistory(t.Context(), chat, backend.HistoryAnchor{Timestamp: before}, 50); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n, c := len(older), complete
		mu.Unlock()
		if n == 70 && c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d older messages, %d completions", n, c)
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range older {
		if m.Source != domain.SourceHistory || !m.Message.Timestamp.Before(before) {
			t.Fatalf("an older message must be history, before the anchor: %+v", m)
		}
	}
}
