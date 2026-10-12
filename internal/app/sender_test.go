// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
)

// sendBackend records sends, with a latency, and fails the IDs in fail
// once each.
type sendBackend struct {
	latency time.Duration

	mu       sync.Mutex
	n        int
	sent     []backend.SendRequest
	inFlight int
	maxIn    int
	fail     map[domain.WAMessageID]bool
	block    chan struct{}
}

func (b *sendBackend) Run(ctx context.Context, _ backend.Sink) error { <-ctx.Done(); return nil }

func (b *sendBackend) NewMessageID() domain.WAMessageID {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n++
	return domain.WAMessageID(fmt.Sprintf("OUT%03d", b.n))
}

func (b *sendBackend) MarkRead(context.Context, domain.JID, domain.JID, []domain.WAMessageID, time.Time) error {
	return nil
}

func (b *sendBackend) SendText(ctx context.Context, req backend.SendRequest) (backend.SendResult, error) {
	b.mu.Lock()
	b.inFlight++
	b.maxIn = max(b.maxIn, b.inFlight)
	fail := b.fail[req.WAID]
	delete(b.fail, req.WAID)
	block := b.block
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	select {
	case <-time.After(b.latency):
	case <-ctx.Done():
		return backend.SendResult{}, ctx.Err()
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return backend.SendResult{}, ctx.Err()
		}
	}
	if fail {
		return backend.SendResult{}, errors.New("network down")
	}
	b.mu.Lock()
	b.sent = append(b.sent, req)
	b.mu.Unlock()
	return backend.SendResult{Timestamp: time.Now()}, nil
}

var fakeMe = fake.Person{LID: "900@lid", PN: "905550000000@s.whatsapp.net"}

// chatsFor creates n group chats and returns their IDs.
func chatsFor(t *testing.T, p *pipeline, n int) []domain.ChatID {
	t.Helper()
	var evs []domain.Event
	for i := range n {
		evs = append(evs, domain.ChatUpdated{ChatJID: domain.JID(fmt.Sprintf("g%d@g.us", i)), Fields: domain.ChatFieldTitle, Title: fmt.Sprint("g", i)})
	}
	if err := p.Deliver(t.Context(), evs); err != nil {
		t.Fatal(err)
	}
	var ids []domain.ChatID
	for i := range n {
		id, ok, err := p.reader.ChatID(t.Context(), domain.JID(fmt.Sprintf("g%d@g.us", i)))
		if err != nil || !ok {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestSenderKeepsOrderAndLimitsConcurrency(t *testing.T) {
	p, _ := newTestPipeline(t, fakeMe)
	b := &sendBackend{latency: 15 * time.Millisecond}
	s := newSender(b, p)
	defer s.close()
	chats := chatsFor(t, p, 6)
	send := p.send(s, b)
	for i := range 4 {
		for _, c := range chats {
			send(ui.Outgoing{Chat: c, Text: fmt.Sprintf("%d-%d", c, i)})
		}
	}
	waitUntil(t, "every message to be sent", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.sent) == 24
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxIn > maxSends || b.maxIn < 2 {
		t.Fatalf("%d sends at once, want 2..%d", b.maxIn, maxSends)
	}
	byChat := map[domain.JID][]string{}
	for _, r := range b.sent {
		byChat[r.ChatJID] = append(byChat[r.ChatJID], r.Body)
	}
	for jid, bodies := range byChat {
		if !slices.IsSortedFunc(bodies, strings.Compare) || len(bodies) != 4 {
			t.Fatalf("chat %s got %v: messages must keep their order", jid, bodies)
		}
	}
}

func TestSenderFailureAndRetry(t *testing.T) {
	p, r := newTestPipeline(t, fakeMe)
	b := &sendBackend{fail: map[domain.WAMessageID]bool{"OUT001": true}}
	s := newSender(b, p)
	defer s.close()
	chat := chatsFor(t, p, 1)[0]
	p.send(s, b)(ui.Outgoing{Chat: chat, Text: "merhaba"})
	status := func() domain.MessageStatus {
		msgs, _ := p.reader.Latest(t.Context(), chat, 1)
		if len(msgs) == 0 {
			return 255
		}
		return msgs[0].Status
	}
	waitUntil(t, "the failure", func() bool { return status() == domain.StatusFailed })
	waitUntil(t, "a notice that says how to retry", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.notices, func(n string) bool { return strings.Contains(n, "ctrl+r") })
	})
	msgs, _ := p.reader.Latest(t.Context(), chat, 1)
	retry := p.retry(s)
	notFailed := msgs[0]
	notFailed.Status = domain.StatusSent
	if retry(notFailed) != nil || len(b.sent) != 0 {
		t.Fatal("only a failed message is sent again")
	}
	retry(msgs[0])
	waitUntil(t, "the retry", func() bool { return status() == domain.StatusSent })
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 || b.sent[0].WAID != "OUT001" {
		t.Fatalf("sent %+v: a retry keeps the message ID, so it shows once", b.sent)
	}
	var n int
	_ = p.rdb.QueryRowContext(t.Context(), "SELECT count(*) FROM messages").Scan(&n)
	if n != 1 {
		t.Fatalf("%d messages after a retry", n)
	}
}

func TestSenderCloseKeepsQueuedMessages(t *testing.T) {
	p, _ := newTestPipeline(t, fakeMe)
	b := &sendBackend{block: make(chan struct{})} // never completes
	s := newSender(b, p)
	chat := chatsFor(t, p, 1)[0]
	for i := range 3 {
		p.send(s, b)(ui.Outgoing{Chat: chat, Text: fmt.Sprint("m", i)})
	}
	waitUntil(t, "the first send to start", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.inFlight == 1
	})
	s.close()
	msgs, _ := p.reader.Latest(t.Context(), chat, 10)
	if len(msgs) != 3 {
		t.Fatalf("%d messages kept, want 3: closing must lose none", len(msgs))
	}
	for _, m := range msgs {
		if m.Status != domain.StatusFailed {
			t.Fatalf("%q is %v; unsent messages become failed, to retry", m.Body, m.Status)
		}
	}
}

// TestSendReply: a reply is stored with its quote and sent with the
// quoted message's sender and text, also when it is sent again.
func TestSendReply(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, GroupRatio: 1, MembersPerGroup: 3, InitialHistory: 3, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	var chat domain.Chat
	waitUntil(t, "the history", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		for _, c := range chats {
			if len(allMessages(t, p, c.ID)) == 3 {
				chat = c
				return true
			}
		}
		return false
	})
	var question domain.Message
	for _, m := range allMessages(t, p, chat.ID) {
		if !m.FromMe {
			question = m
		}
	}
	if question.ID == 0 {
		t.Skip("the seed gave only own messages")
	}
	s := newSender(fb, p)
	defer s.close()
	p.send(s, fb)(ui.Outgoing{Chat: chat.ID, Text: "evet", Quoted: question.WAID})
	waitUntil(t, "the reply", func() bool { return len(fb.Sent()) == 1 })
	req := fb.Sent()[0]
	ids, _ := p.reader.IdentitiesByID(t.Context(), []domain.IdentityID{question.SenderID})
	if req.QuotedWAID != question.WAID || req.QuotedText != question.Body || req.QuotedSender != ids[0].LID {
		t.Fatalf("request = %+v, want a reply to %s by %s", req, question.WAID, ids[0].LID)
	}
	waitUntil(t, "the stored reply", func() bool {
		page, _ := p.reader.Latest(t.Context(), chat.ID, 1)
		return len(page) == 1 && page[0].Body == "evet" && page[0].QuotedWAID == question.WAID &&
			page[0].Quote != nil && page[0].Quote.Body == question.Body
	})
}

// TestSendMentions: a message's mentions are sent and stored, so its
// tokens show as names.
func TestSendMentions(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 3, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	g := fb.Chats()[0]
	var chat domain.ChatID
	waitUntil(t, "the group", func() bool {
		id, ok, _ := p.reader.ChatID(t.Context(), g.JID)
		chat = id
		return ok
	})
	cands := p.mentionable(chat)().(ui.MentionCandidatesMsg)
	if len(cands.Candidates) != 3 || cands.Candidates[0].JID.IsZero() {
		t.Fatalf("candidates = %+v (me excluded)", cands.Candidates)
	}
	who := g.Members[0]
	s := newSender(fb, p)
	defer s.close()
	p.send(s, fb)(ui.Outgoing{Chat: chat, Text: "@" + who.LID.User() + " bak", Mentions: []domain.JID{who.LID}})
	waitUntil(t, "the send", func() bool { return len(fb.Sent()) == 1 })
	if req := fb.Sent()[0]; !slices.Equal(req.Mentions, []domain.JID{who.LID}) {
		t.Fatalf("request = %+v", req)
	}
	page, _ := p.reader.Latest(t.Context(), chat, 1)
	if len(page) != 1 || len(page[0].Mentions) != 1 || page[0].Mentions[0].Identity == 0 {
		t.Fatalf("stored = %+v", page)
	}
}
