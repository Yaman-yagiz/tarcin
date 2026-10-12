// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// oneApplyMatchesOneByOne applies events in one Apply call and, on a
// second database, one event per transaction, and requires the same end
// state: deferring the work of a run of messages changes nothing.
func oneApplyMatchesOneByOne(t *testing.T, events ...domain.Event) {
	t.Helper()
	whole, single := newApplyFixture(t), newApplyFixture(t)
	whole.apply(events...)
	for _, ev := range events {
		single.apply(ev)
	}
	if got, want := whole.state(), single.state(); got != want {
		t.Fatalf("in one Apply:\n%s\none event at a time:\n%s", got, want)
	}
	// state compares the last message's time; in the same order, a tie
	// must pick the same message too.
	if got, want := whole.lastMessages(), single.lastMessages(); got != want {
		t.Fatalf("last messages in one Apply: %s; one event at a time: %s", got, want)
	}
}

func (f *applyFixture) lastMessages() string {
	f.t.Helper()
	var out string
	chats := f.chats()
	for i := range chats {
		out += fmt.Sprintf("%s=%s ", chats[i].JID, f.waid(chats[i].LastMessageID))
	}
	return out
}

// waid returns the WhatsApp ID of a stored message.
func (f *applyFixture) waid(id domain.MessageID) string {
	f.t.Helper()
	var waid string
	if err := f.db.QueryRowContext(f.t.Context(), "SELECT wa_id FROM messages WHERE id = ?", id).Scan(&waid); err != nil {
		f.t.Fatal(err)
	}
	return waid
}

func history(chat, sender domain.JID, waid, body string, sec int) domain.MessageReceived {
	e := incoming(chat, sender, waid, body, sec)
	e.Source = domain.SourceHistory
	return e
}

func TestApplyRunMatchesOneByOne(t *testing.T) {
	named := incoming(group, aliLID, "G3", "named", 30)
	named.Message.PushName = "Ali"
	renamed := incoming(group, aliLID, "G4", "renamed", 31)
	renamed.Message.PushName = "Ali B."
	linking := incoming(group, aliLID, "G5", "linking", 40)
	linking.Message.SenderAlt = aliPN
	upgraded := incoming(group, ayLID, "G6", "late content", 41)
	placeholder := upgraded
	placeholder.Message.Kind, placeholder.Message.Body = domain.KindUnsupported, ""
	for _, tc := range []struct {
		name   string
		events []domain.Event
	}{
		{"history in two chats, out of order, with a tie", []domain.Event{
			history(group, aliLID, "G1", "first", 20),
			history(group, ayLID, "G2", "tie a", 25),
			history(aliPN, aliPN, "P1", "by number", 22),
			history(group, ayLID, "G2b", "tie b", 25),
			history(group, aliLID, "G0", "older", 10),
		}},
		{"an operation that waits within the call", []domain.Event{
			history(group, aliLID, "G1", "first", 20),
			domain.MessageEdited{ChatJID: group, TargetWAID: "G2", Body: "edited", EditedAt: at(60)},
			history(group, aliLID, "G0", "older", 10),
			incoming(group, aliLID, "G2", "original", 50),
		}},
		{"unread messages around a read", []domain.Event{
			named, renamed,
			domain.ChatRead{ChatJID: group, ReadAt: at(30)},
			incoming(group, ayLID, "G7", "after the read", 50),
			domain.ReceiptReceived{ChatJID: group, WAIDs: []domain.WAMessageID{"G7"}, From: ayLID, Status: domain.StatusRead, Timestamp: at(61)},
		}},
		{"direct chats merged within a run", []domain.Event{
			history(aliPN, aliPN, "P1", "by number", 22),
			incoming(aliLID, aliLID, "L1", "by LID", 35),
			incoming(aliPN, aliPN, "P2", "by number, unread", 36),
			linking,
			incoming(aliLID, aliLID, "L2", "after the merge", 45),
			incoming(aliPN, aliPN, "P3", "by number again", 46),
		}},
		{"operations moved by a merge within the call", []domain.Event{
			domain.MessageRevoked{ChatJID: aliPN, TargetWAID: "L1", RevokedAt: at(50)},
			incoming(aliLID, aliLID, "L1", "by LID", 35),
			domain.IdentityLinked{PN: aliPN, LID: aliLID},
		}},
		{"a placeholder and its content in one run", []domain.Event{
			placeholder, upgraded,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { oneApplyMatchesOneByOne(t, tc.events...) })
	}
}

func TestApplyRunReadsBackItsMessages(t *testing.T) {
	f := newApplyFixture(t)
	placeholder := incoming(group, aliLID, "W2", "", 2)
	placeholder.Message.Kind = domain.KindUnsupported
	ch := f.apply(incoming(group, aliLID, "W1", "one", 1), placeholder, incoming(group, aliLID, "W2", "two", 2))
	id := f.chats()[0].ID
	added := ch.Added[id]
	if len(added) != 2 || added[0].WAID != "W1" || added[1].WAID != "W2" || added[1].Body != "two" {
		t.Fatalf("added = %+v; a run reports its messages in order, as they are at its end", added)
	}
	if len(ch.Updated[id]) != 0 || len(ch.Reload) != 0 {
		t.Fatalf("changes = %+v", ch)
	}
	if c := f.chats()[0]; c.UnreadCount != 2 || f.waid(c.LastMessageID) != "W2" {
		t.Fatalf("chat = %+v", c)
	}
}

func TestApplyLongRunReloadsItsChat(t *testing.T) {
	f := newApplyFixture(t)
	var events []domain.Event
	for i := range MaxChangedMessages + 1 {
		events = append(events, history(group, aliLID, fmt.Sprintf("H%d", i), "m", i+1))
	}
	ch := f.apply(events...)
	id := f.chats()[0].ID
	if _, ok := ch.Reload[id]; !ok || len(ch.Added[id]) != 0 {
		t.Fatalf("%d added, reload %v; more than Changes keeps reloads the chat", len(ch.Added[id]), ch.Reload)
	}
	if _, ok := ch.Chats[id]; !ok {
		t.Fatal("the chat-list entry changed")
	}
	if c := f.chats()[0]; f.waid(c.LastMessageID) != fmt.Sprintf("H%d", MaxChangedMessages) {
		t.Fatalf("chat = %+v", c)
	}
}

func TestApplyPendingOpsAcrossRuns(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W2", Body: "edited", EditedAt: at(9)})
	// The chat has a pending edit: a later chunk must still apply it.
	f.apply(history(group, aliLID, "W1", "one", 1), history(group, aliLID, "W2", "two", 2))
	msgs := f.messages(f.chats()[0].ID)
	if len(msgs) != 2 || msgs[1].Body != "edited" {
		t.Fatalf("messages = %+v", msgs)
	}
	if n := f.count("SELECT count(*) FROM pending_ops"); n != 0 {
		t.Fatalf("%d pending operations left", n)
	}
}

// TestDeletionsUnlinkTheirFiles: a message deleted for everyone, or a
// cleared chat, names the cache files no other message uses.
func TestDeletionsUnlinkTheirFiles(t *testing.T) {
	f := newApplyFixture(t)
	image := func(waid string, sec int) domain.MessageReceived {
		e := incoming(group, aliLID, waid, "", sec)
		e.Message.Kind, e.Message.Media = domain.KindImage, &domain.Media{MIME: "image/jpeg"}
		return e
	}
	local := func(waid, name string) domain.MediaChanged {
		return domain.MediaChanged{ChatJID: group, TargetWAID: domain.WAMessageID(waid), State: domain.MediaLocal, LocalName: name}
	}
	f.apply(image("A", 1), image("B", 2), image("C", 3), local("A", "a.jpg"), local("B", "shared.jpg"), local("C", "shared.jpg"))
	ch := f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "A", RevokedAt: at(10), Actor: aliLID})
	if !slices.Equal(ch.Unlinked, []string{"a.jpg"}) {
		t.Fatalf("unlinked = %v after a revoke", ch.Unlinked)
	}
	if ch := f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "B", RevokedAt: at(11), Actor: aliLID}); len(ch.Unlinked) != 0 {
		t.Fatalf("unlinked = %v; C still uses the file", ch.Unlinked)
	}
	if ch := f.apply(domain.ChatCleared{ChatJID: group, Before: at(100)}); !slices.Equal(ch.Unlinked, []string{"shared.jpg"}) {
		t.Fatalf("unlinked = %v after clearing the chat", ch.Unlinked)
	}
}

// TestMessageSentTakesTheServerTime: a sent own message moves to the time
// the server stamped it with, before a reply that arrived meanwhile, and
// keeps its disappearing timer from there.
func TestMessageSentTakesTheServerTime(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.AccountLinked{PN: mePN, LID: meLID})
	own := incoming(group, meLID, "OWN", "mine", 200) // stamped by a clock 2 minutes ahead
	own.Source, own.Message.FromMe, own.Message.Expiration = domain.SourceSelf, true, time.Hour
	f.apply(own, incoming(group, aliLID, "REPLY", "reply", 90))
	chat := f.chats()[0]
	if msgs := f.messages(chat.ID); msgs[0].WAID != "REPLY" {
		t.Fatalf("before: %v", msgs)
	}
	ch := f.apply(domain.MessageSent{ChatJID: group, WAID: "OWN", At: at(80)})
	msgs := f.messages(chat.ID)
	if msgs[0].WAID != "OWN" || !msgs[0].Timestamp.Equal(at(80)) || msgs[0].Status != domain.StatusSent {
		t.Fatalf("after: %+v", msgs)
	}
	if _, ok := ch.Reload[chat.ID]; !ok {
		t.Fatal("moving past another message reloads the chat")
	}
	if n := f.count("SELECT count(*) FROM messages WHERE wa_id = 'OWN' AND expires_at = " + fmt.Sprint(at(80).Add(time.Hour).UnixMilli())); n != 1 {
		t.Fatal("the timer runs from the server's time")
	}
	if c := f.chats()[0]; f.waid(c.LastMessageID) != "REPLY" {
		t.Fatalf("the chat's last message is the reply, the newest: %s", f.waid(c.LastMessageID))
	}
	before := f.state()
	f.apply(domain.MessageSent{ChatJID: group, WAID: "OWN", At: at(80)})
	if f.state() != before {
		t.Fatal("applying it again changes nothing")
	}
	if ch := f.apply(domain.MessageSent{ChatJID: group, WAID: "REPLY", At: at(1)}); len(ch.Reload) != 0 {
		t.Fatal("someone else's message is not moved")
	}
}
