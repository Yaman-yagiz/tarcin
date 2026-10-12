// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
)

func TestServerClockFromSends(t *testing.T) {
	local := time.Unix(1_800_000_000, 0)
	c := &serverClock{now: func() time.Time { return local }}
	if c.Offset() != 0 || !c.Now().Equal(local) {
		t.Fatal("without samples the clock is the computer's")
	}
	// The computer is two minutes ahead; the server stamps in seconds.
	c.observeSend(local, local.Add(200*time.Millisecond), local.Add(-2*time.Minute).Truncate(time.Second))
	if off := c.Offset(); off < -2*time.Minute-time.Second || off > -2*time.Minute+time.Second {
		t.Fatalf("offset %v, want about -2m", off)
	}
	small := &serverClock{now: func() time.Time { return local }}
	small.observeSend(local, local.Add(100*time.Millisecond), local.Add(time.Second))
	if small.Offset() != 0 {
		t.Fatalf("offset %v: under %v is not corrected", small.Offset(), clockThreshold)
	}
	c.observeSend(local, local.Add(time.Minute), local)
	if off := c.Offset(); off > -time.Minute {
		t.Fatalf("offset %v: a slow send must not count", off)
	}
}

func TestServerClockFromLiveMessages(t *testing.T) {
	local := time.Unix(1_800_000_000, 0)
	c := &serverClock{now: func() time.Time { return local }}
	// The computer is 90 s behind: messages arrive stamped in its future.
	c.observeLive(local.Add(89*time.Second), local)
	c.observeLive(local.Add(-time.Hour), local) // from an offline queue: no bound
	if off := c.Offset(); off != 89*time.Second {
		t.Fatalf("offset %v, want the largest bound, 89s", off)
	}
	c.observeLive(local.Add(48*time.Hour), local) // a bad stamp
	if off := c.Offset(); off != 89*time.Second {
		t.Fatalf("offset %v after an implausible stamp", off)
	}
	// A computer ahead: live messages, fresh or late, only bound the
	// offset from below, and never lower it; a send does.
	ahead := &serverClock{now: func() time.Time { return local }}
	ahead.observeLive(local.Add(-2*time.Minute), local)
	ahead.observeLive(local.Add(-3*time.Hour), local)
	if off := ahead.Offset(); off != 0 {
		t.Fatalf("offset %v: live messages must not move the clock back", off)
	}
	ahead.observeSend(local, local.Add(100*time.Millisecond), local.Add(-2*time.Minute))
	if off := ahead.Offset(); off > -time.Minute {
		t.Fatalf("offset %v after a send", off)
	}
}

// TestRepliesFollowOwnMessagesWhenTheClockIsAhead is the case of a
// computer two minutes ahead of WhatsApp: a reply to a message must still
// come after it.
func TestRepliesFollowOwnMessagesWhenTheClockIsAhead(t *testing.T) {
	ahead := func() time.Time { return time.Now().Add(2 * time.Minute) }
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: fakeMe.LID, MePN: fakeMe.PN, Now: ahead})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	b := &sendBackend{} // stamps with the real time, as the server does
	s := newSender(b, p)
	defer s.close()
	chat := chatsFor(t, p, 1)[0]
	send := p.send(s, b)
	latest := func() []domain.Message {
		page, _ := p.reader.Latest(t.Context(), chat, 10)
		return page
	}

	send(ui.Outgoing{Chat: chat, Text: "first"})
	waitUntil(t, "the first to be sent", func() bool { m := latest(); return len(m) == 1 && m[0].Status == domain.StatusSent })
	if d := time.Until(latest()[0].Timestamp); d > 2*time.Second || d < -2*time.Second {
		t.Fatalf("a sent message keeps the server's time; it is %v off", d)
	}
	reply := domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: "g0@g.us",
		SenderJID: "901@lid", WAID: "REPLY", Timestamp: time.Now(), Kind: domain.KindText, Body: "reply"}}
	if err := p.Deliver(t.Context(), []domain.Event{reply}); err != nil {
		t.Fatal(err)
	}
	send(ui.Outgoing{Chat: chat, Text: "second"})
	waitUntil(t, "the second to be sent", func() bool {
		for _, m := range latest() {
			if m.Body == "second" {
				return m.Status == domain.StatusSent
			}
		}
		return false
	})
	var order []string
	for _, m := range latest() {
		order = append(order, m.Body)
	}
	if want := []string{"first", "reply", "second"}; order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order %v, want %v", order, want)
	}
	if off := p.clock.Offset(); off > -time.Minute {
		t.Fatalf("the clock learned an offset of %v, want about -2m", off)
	}
}
