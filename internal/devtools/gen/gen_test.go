// SPDX-License-Identifier: GPL-3.0-or-later

package gen

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestScenarioShapes(t *testing.T) {
	for _, name := range []string{"S1", "S2", "S4", "S12"} {
		t.Run(name, func(t *testing.T) {
			spec := Scenarios[name]
			d := Build(spec)
			if len(d.Chats) != spec.Chats {
				t.Fatalf("chats = %d, want %d", len(d.Chats), spec.Chats)
			}
			total := 0
			for i := range d.Chats {
				total += d.MessageCount(i)
			}
			if total != spec.Messages || d.TotalMessages() != spec.Messages {
				t.Fatalf("messages = %d, want %d", total, spec.Messages)
			}
			if spec.HotChatMessages > 0 && d.MessageCount(0) != spec.HotChatMessages {
				t.Fatalf("hot chat has %d messages, want %d", d.MessageCount(0), spec.HotChatMessages)
			}
			if spec.HotGroupMembers > 0 {
				if d.Chats[0].Kind != domain.ChatGroup || len(d.Members[1]) != spec.HotGroupMembers+1 {
					t.Fatalf("hot group: kind %v, %d members", d.Chats[0].Kind, len(d.Members[1]))
				}
			}
			for i, id := range d.Identities {
				if id.ID != domain.IdentityID(i+1) || !id.LID.IsLID() {
					t.Fatalf("identity %d is malformed: %+v", i, id)
				}
			}
			if !d.Me.IsMe || d.Identities[0] != d.Me {
				t.Fatal("Me must be identity 1")
			}
		})
	}
}

func TestMessagesAreOrderedAndConsistent(t *testing.T) {
	d := Build(Scenarios["S1"])
	var prevID domain.MessageID
	var ties int
	for i, chat := range d.Chats {
		var prev *domain.Message
		n := 0
		for m := range d.Messages(i) {
			n++
			if m.ID != prevID+1 {
				t.Fatalf("message IDs are not consecutive: %d after %d", m.ID, prevID)
			}
			prevID = m.ID
			if m.ChatID != chat.ID {
				t.Fatalf("message in chat %d has ChatID %d", chat.ID, m.ChatID)
			}
			if prev != nil {
				if !prev.Cursor().Before(m.Cursor()) {
					t.Fatalf("chat %d is not strictly ordered", chat.ID)
				}
				if prev.Timestamp.Equal(m.Timestamp) {
					ties++
				}
			}
			if m.Timestamp.Nanosecond() != 0 {
				t.Fatal("timestamps must have one-second resolution")
			}
			if m.FromMe != (m.SenderID == d.Me.ID) {
				t.Fatal("FromMe and SenderID disagree")
			}
			if chat.Kind == domain.ChatGroup && !slices.Contains(d.Members[chat.ID], m.SenderID) {
				t.Fatalf("sender %d is not a member of group %d", m.SenderID, chat.ID)
			}
			p := m
			prev = &p
		}
		if n != d.MessageCount(i) {
			t.Fatalf("chat %d streamed %d messages, want %d", i, n, d.MessageCount(i))
		}
		if n > 0 && (chat.LastMessageID != prevID || !chat.LastActivity.Equal(prev.Timestamp)) {
			t.Fatalf("chat %d metadata does not match its newest message", i)
		}
	}
	if ties == 0 {
		t.Fatal("no same-second messages were generated; tie-breaking would go untested")
	}
}

func TestDeterministic(t *testing.T) {
	a, b := Build(Scenarios["S1"]), Build(Scenarios["S1"])
	if !slices.Equal(a.Chats, b.Chats) || !slices.Equal(a.Identities, b.Identities) {
		t.Fatal("chats or identities differ between builds")
	}
	next, stop := iterPull(b.All())
	defer stop()
	for m := range a.All() {
		n, ok := next()
		if !ok || m.WAID != n.WAID || m.Body != n.Body || !m.Timestamp.Equal(n.Timestamp) {
			t.Fatal("message streams differ between builds")
		}
	}
	// Streaming one chat twice gives the same result.
	first := slices.Collect(a.Messages(3))
	again := slices.Collect(a.Messages(3))
	if len(first) == 0 || first[len(first)-1].WAID != again[len(again)-1].WAID {
		t.Fatal("re-streaming a chat is not deterministic")
	}
}

func TestStressContent(t *testing.T) {
	d := Build(Scenarios["S12"])
	var hostile, huge, invalid int
	for m := range d.All() {
		if strings.ContainsAny(m.Body, "\x1b\u202e\u200b") {
			hostile++
		}
		if len(m.Body) > 1<<19 {
			huge++
		}
		if !utf8.ValidString(m.Body) {
			invalid++
		}
	}
	if hostile == 0 || huge == 0 || invalid == 0 {
		t.Fatalf("S12 lacks hostile content: escapes=%d huge=%d invalidUTF8=%d", hostile, huge, invalid)
	}

	var rtl, long int
	for _, id := range Build(Scenarios["S4"]).Identities {
		if strings.ContainsAny(id.PushName, "דמ") {
			rtl++
		}
		if utf8.RuneCountInString(id.PushName) > 100 {
			long++
		}
	}
	if rtl == 0 || long == 0 {
		t.Fatalf("S4 lacks stress names: rtl=%d long=%d", rtl, long)
	}
}

func TestDistributeExact(t *testing.T) {
	for _, spec := range []Spec{
		{Chats: 1, Messages: 10},
		{Chats: 7, Messages: 1000},
		{Chats: 3, Messages: 2, HotChatMessages: 1},
		{Chats: 50, Messages: 49},
	} {
		counts := distribute(spec)
		sum := 0
		for _, c := range counts {
			sum += c
		}
		if sum != spec.Messages {
			t.Errorf("distribute(%+v) sums to %d", spec, sum)
		}
	}
}

func TestInconsistentSpecPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	Build(Spec{Chats: 2, Messages: 1, HotChatMessages: 5})
}

func iterPull(seq func(func(domain.Message) bool)) (func() (domain.Message, bool), func()) {
	ch := make(chan domain.Message)
	done := make(chan struct{})
	go func() {
		defer close(ch)
		seq(func(m domain.Message) bool {
			select {
			case ch <- m:
				return true
			case <-done:
				return false
			}
		})
	}()
	next := func() (domain.Message, bool) { m, ok := <-ch; return m, ok }
	return next, func() { close(done) }
}

func BenchmarkGenerateS2(b *testing.B) {
	spec := Scenarios["S2"]
	for b.Loop() {
		d := Build(spec)
		n := 0
		for range d.All() {
			n++
		}
		if n != spec.Messages {
			b.Fatalf("generated %d messages", n)
		}
	}
}
