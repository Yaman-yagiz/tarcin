// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestMergeFoldsTheNumberIntoTheLID(t *testing.T) {
	f := newApplyFixture(t)
	byPN := incoming(group, aliPN, "W1", "by number", 1)
	byPN.Message.PushName = "Ali"
	f.apply(
		byPN,
		incoming(group, aliLID, "W2", "by LID", 2),
		domain.ReactionReceived{ChatJID: group, TargetWAID: "W2", SenderJID: aliPN, Emoji: "old", Timestamp: at(3)},
		domain.ReactionReceived{ChatJID: group, TargetWAID: "W2", SenderJID: aliLID, Emoji: "new", Timestamp: at(4)},
		domain.ReactionReceived{ChatJID: group, TargetWAID: "W1", SenderJID: aliPN, Emoji: "pn", Timestamp: at(5)},
		domain.GroupMembersChanged{ChatJID: group, Members: []domain.GroupMember{{JID: aliPN, Role: domain.RoleAdmin}, {JID: aliLID}}},
		domain.ContactUpdated{JID: aliPN, ContactName: "Ali Veli"},
	)
	before := f.identity(aliPN).ID
	ch := f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID})

	ali := f.identity(aliLID)
	if ali.PN != aliPN || ali.ContactName != "Ali Veli" || ali.PushName != "Ali" {
		t.Fatalf("merged identity = %+v", ali)
	}
	if n := f.count("SELECT count(*) FROM identities WHERE is_me = 0"); n != 1 {
		t.Fatalf("%d identities after the merge", n)
	}
	if n := f.count("SELECT count(DISTINCT sender_id) FROM messages"); n != 1 {
		t.Fatal("every message must have the merged sender")
	}
	state := f.state()
	for _, want := range []string{"W2 101@lid new", "W1 101@lid pn", fmt.Sprintf("g1@g.us 101@lid %d", domain.RoleAdmin)} {
		if !strings.Contains(state, want) {
			t.Errorf("missing %q in\n%s", want, state)
		}
	}
	if strings.Contains(state, " old ") {
		t.Error("the older of two reactions by one person must go")
	}
	c := f.chats()[0]
	if _, ok := ch.Reload[c.ID]; !ok {
		t.Error("a chat whose senders changed must reload")
	}
	if _, ok := ch.Identities[before]; !ok {
		t.Error("the merged-away identity must be notified")
	}
	if again := f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID}); !again.Empty() {
		t.Error("a repeated link must change nothing")
	}
}

func TestMergeJoinsDirectChats(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(
		incoming(aliPN, aliPN, "W1", "one", 1),
		incoming(aliPN, aliPN, "W3", "three", 3), // also delivered under the LID
		domain.ChatUpdated{ChatJID: aliPN, Fields: domain.ChatFieldPinned, PinnedAt: at(50)},
		// An edit for W4, which only the LID chat holds.
		domain.MessageEdited{ChatJID: aliPN, TargetWAID: "W4", Body: "four, edited", EditedAt: at(40)},
	)
	f.apply(
		incoming(aliLID, aliLID, "W3", "three", 3),
		incoming(aliLID, aliLID, "W4", "four", 4),
		domain.ReceiptReceived{ChatJID: aliLID, WAIDs: []domain.WAMessageID{"W1"}, Status: domain.StatusRead, Timestamp: at(5)},
	)
	chats := f.chats()
	if len(chats) != 2 {
		t.Fatalf("before the link: %d chats", len(chats))
	}
	pnChat := chats[0].ID
	if chats[0].JID != aliPN {
		pnChat = chats[1].ID
	}
	ch := f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID})

	chats = f.chats()
	if len(chats) != 1 {
		t.Fatalf("after the link: %+v", chats)
	}
	c := chats[0]
	if c.JID != aliLID || !c.PinnedAt.Equal(at(50)) || c.UnreadCount != 3 || !c.LastActivity.Equal(at(4)) {
		t.Fatalf("merged chat = %+v", c)
	}
	got := bodies(f.messages(c.ID))
	if strings.Join(got, "|") != "one|three|four, edited" {
		t.Fatalf("messages = %q", got)
	}
	if _, ok := ch.Removed[pnChat]; !ok {
		t.Fatal("the merged-away chat must be reported as removed")
	}
	if _, ok := ch.Reload[c.ID]; !ok {
		t.Fatal("the merged chat must reload")
	}
	// The receipt for W1 waited in the LID chat for a message of the PN
	// chat; W1 was not from me, so its status is unchanged but the op is
	// consumed.
	if n := f.count("SELECT count(*) FROM pending_ops"); n != 0 {
		t.Fatalf("%d pending ops left", n)
	}
}

func TestLinkMovesARecycledNumber(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID}, domain.ContactUpdated{JID: ayLID, ContactName: "Ayşe"})
	f.apply(domain.IdentityLinked{PN: aliPN, LID: ayLID})
	ali, ay := f.identity(aliLID), f.identity(ayLID)
	if ali.PN != "" || ay.PN != aliPN || ay.ContactName != "Ayşe" || ali.ID == ay.ID {
		t.Fatalf("ali %+v, ayşe %+v", ali, ay)
	}
}

func TestChangesRemoveChat(t *testing.T) {
	var c Changes
	c.AddMessage(&domain.Message{ID: 1, ChatID: 3})
	c.TouchChat(3)
	c.RemoveChat(3)
	c.TouchChat(3)
	c.ReloadChat(3)
	c.UpdateMessage(&domain.Message{ID: 2, ChatID: 3})
	if len(c.Added) != 0 || len(c.Chats) != 0 || len(c.Reload) != 0 || len(c.Updated) != 0 || len(c.Removed) != 1 {
		t.Fatalf("a removed chat must record nothing else: %+v", c)
	}
	var merged Changes
	merged.TouchChat(3)
	merged.Merge(&c)
	if _, ok := merged.Chats[3]; ok || len(merged.Removed) != 1 {
		t.Fatalf("merge = %+v", merged)
	}
}

// person is someone the scenario generator writes about.
type person struct {
	pn, lid domain.JID
	push    string
	role    domain.GroupRole
}

// scenario generates a random event sequence over three people who use
// both address forms, with split direct chats, links learned in several
// ways, receipts, edits, revokes, reactions, names and group members.
// Every field is set at most once per person or chat, so that the
// canonical end state is well defined.
func scenario(rng *rand.Rand) []domain.Event {
	people := []person{
		{aliPN, aliLID, "Ali", domain.RoleAdmin},
		{ayPN, ayLID, "Ayşe", domain.RoleMember},
		{"903@s.whatsapp.net", "103@lid", "Can", domain.RoleSuperAdmin},
	}
	groups := []domain.JID{"g1@g.us", "g2@g.us"}
	form := func(p person) domain.JID {
		if rng.IntN(2) == 0 {
			return p.pn
		}
		return p.lid
	}
	other := func(p person, jid domain.JID) domain.JID {
		if jid == p.pn {
			return p.lid
		}
		return p.pn
	}
	type sent struct {
		chats  []domain.JID // the chat JIDs it was delivered to
		fromMe bool
		waid   domain.WAMessageID
		ts     int
	}
	var evs []domain.Event
	var msgs []sent
	for i := range 24 {
		waid := domain.WAMessageID(fmt.Sprintf("M%02d", i))
		ts := 100 + i*10
		p := people[rng.IntN(len(people))]
		in := domain.IncomingMessage{WAID: waid, Timestamp: at(ts), Kind: domain.KindText, Body: fmt.Sprintf("body %d", i)}
		ev := domain.MessageReceived{Source: domain.SourceLive}
		s := sent{waid: waid, ts: ts}
		if rng.IntN(2) == 0 { // direct chat
			in.ChatJID = form(p)
			in.FromMe = rng.IntN(3) == 0
			if in.FromMe {
				in.SenderJID = meLID
			} else {
				in.SenderJID, in.PushName = form(p), p.push
			}
			s.chats, s.fromMe = []domain.JID{in.ChatJID}, in.FromMe
			ev.Message = in
			evs = append(evs, ev)
			if rng.IntN(4) == 0 { // the same message under the other form
				dup := ev
				dup.Message.ChatJID = other(p, in.ChatJID)
				s.chats = append(s.chats, dup.Message.ChatJID)
				evs = append(evs, dup)
			}
		} else {
			in.ChatJID = groups[rng.IntN(len(groups))]
			in.SenderJID, in.PushName = form(p), p.push
			if rng.IntN(3) == 0 {
				in.SenderAlt = other(p, in.SenderJID)
			}
			if rng.IntN(4) == 0 {
				ev.Source = domain.SourceHistory
			}
			s.chats = []domain.JID{in.ChatJID}
			ev.Message = in
			evs = append(evs, ev)
		}
		msgs = append(msgs, s)
	}
	pick := func(s sent) domain.JID { return s.chats[rng.IntN(len(s.chats))] }
	for i, s := range msgs {
		if s.fromMe {
			for k := rng.IntN(3); k > 0; k-- {
				evs = append(evs, domain.ReceiptReceived{ChatJID: pick(s), WAIDs: []domain.WAMessageID{s.waid},
					Status: []domain.MessageStatus{domain.StatusDelivered, domain.StatusRead}[rng.IntN(2)], Timestamp: at(s.ts + 1)})
			}
		}
		// Edits and revokes of one message have distinct times: a tie
		// between two different edits has no defined winner.
		for k := range rng.IntN(3) {
			evs = append(evs, domain.MessageEdited{ChatJID: pick(s), TargetWAID: s.waid,
				Body: fmt.Sprintf("edit %d.%d", i, k), EditedAt: at(s.ts + 2 + k)})
		}
		if rng.IntN(6) == 0 {
			for k := range 1 + rng.IntN(2) {
				evs = append(evs, domain.MessageRevoked{ChatJID: pick(s), TargetWAID: s.waid, RevokedAt: at(s.ts + 3 + k)})
			}
		}
		for k := rng.IntN(4); k > 0; k-- {
			p := people[rng.IntN(len(people))]
			emoji := fmt.Sprintf("e%d", rng.IntN(3))
			if rng.IntN(4) == 0 {
				emoji = "" // a removal
			}
			evs = append(evs, domain.ReactionReceived{ChatJID: pick(s), TargetWAID: s.waid, SenderJID: form(p),
				Emoji: emoji, Timestamp: at(s.ts + 3 + rng.IntN(4))})
		}
	}
	for _, p := range people {
		if rng.IntN(3) != 0 {
			evs = append(evs, domain.ContactUpdated{JID: form(p), ContactName: p.push + " (contact)"})
		}
		if rng.IntN(3) == 0 {
			evs = append(evs, domain.ChatUpdated{ChatJID: form(p), Fields: domain.ChatFieldPinned, PinnedAt: at(5000)})
		}
		for _, g := range groups {
			if rng.IntN(2) == 0 {
				m := domain.GroupMember{JID: form(p), Role: p.role}
				if rng.IntN(2) == 0 {
					m.AltJID = other(p, m.JID)
				}
				evs = append(evs, domain.GroupMembersChanged{ChatJID: g, Members: []domain.GroupMember{m}})
			}
		}
		if rng.IntN(5) < 3 {
			evs = append(evs, domain.IdentityLinked{PN: p.pn, LID: p.lid})
		}
	}
	for _, g := range groups {
		evs = append(evs, domain.ChatUpdated{ChatJID: g, Fields: domain.ChatFieldTitle, Title: "group " + g.User()})
		if rng.IntN(2) == 0 {
			evs = append(evs, domain.ChatRead{ChatJID: g, ReadAt: at(100 + rng.IntN(240))})
		}
	}
	for _, p := range people {
		if rng.IntN(2) == 0 {
			evs = append(evs, domain.ChatRead{ChatJID: form(p), ReadAt: at(100 + rng.IntN(240))})
		}
	}
	return evs
}

// checkInvariants verifies what the writer promises about identities,
// chats and pending operations (identity.go).
func (f *applyFixture) checkInvariants() {
	f.t.Helper()
	for what, q := range map[string]string{
		"an identity column without its alias": `SELECT count(*) FROM identities i WHERE
			(i.lid IS NOT NULL AND NOT EXISTS (SELECT 1 FROM jid_aliases WHERE jid = i.lid AND identity_id = i.id))
			OR (i.pn IS NOT NULL AND NOT EXISTS (SELECT 1 FROM jid_aliases WHERE jid = i.pn AND identity_id = i.id))`,
		"a direct chat not keyed by its peer's canonical JID": `SELECT count(*) FROM chats c
			LEFT JOIN identities i ON i.id = c.peer_identity_id
			WHERE c.kind = 'dm' AND (i.id IS NULL OR c.jid != coalesce(i.lid, i.pn))`,
		"a pending operation whose target is stored": `SELECT count(*) FROM pending_ops p
			JOIN messages m ON m.chat_id = p.chat_id AND m.wa_id = p.target_wa_id`,
		"a foreign key violation": "SELECT count(*) FROM pragma_foreign_key_check",
		"a chat whose last message is not its newest": `SELECT count(*) FROM chats c WHERE c.last_activity_ts !=
			coalesce((SELECT max(ts) FROM messages WHERE chat_id = c.id), c.last_activity_ts)`,
	} {
		if n := f.count(q); n != 0 {
			f.t.Fatalf("%d × %s", n, what)
		}
	}
}

// applyInBatches applies events in random batches, one transaction each.
func (f *applyFixture) applyInBatches(rng *rand.Rand, events []domain.Event) {
	for len(events) > 0 {
		n := min(1+rng.IntN(8), len(events))
		f.apply(events[:n]...)
		events = events[n:]
	}
}

// TestPropertyAnyOrderGivesTheCanonicalState checks guarantee G2: any
// shuffled, duplicated and differently batched delivery of a scenario
// ends in the state of the canonical delivery.
func TestPropertyAnyOrderGivesTheCanonicalState(t *testing.T) {
	seeds, variants := 40, 6
	switch {
	case testing.Short():
		seeds, variants = 8, 3
	case raceEnabled:
		seeds = 10 // make property runs many more without the race detector
	}
	if n, err := strconv.Atoi(os.Getenv("TARCIN_PROPERTY_SEEDS")); err == nil && n > 0 {
		seeds = n
	}
	for seed := range seeds {
		// A subtest per seed deletes its databases when it ends: they live
		// in the temporary directory, often a tmpfs.
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(seed), 0x7a7c1)) //nolint:gosec // reproducible test data
			events := scenario(rng)

			canonical := newApplyFixture(t)
			for _, ev := range events {
				canonical.apply(ev)
			}
			want := canonical.state()
			canonical.checkInvariants()
			canonical.checkIndex()

			for v := range variants {
				shuffled := append([]domain.Event(nil), events...)
				for _, ev := range events {
					if rng.IntN(5) == 0 {
						shuffled = append(shuffled, ev) // duplicate delivery
					}
				}
				rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
				f := newApplyFixture(t)
				f.applyInBatches(rng, shuffled)
				if got := f.state(); got != want {
					t.Fatalf("variant %d: state differs\n%s", v, diffLines(want, got))
				}
				f.checkInvariants()
				f.checkIndexIDs() // the bodies are checked on the canonical state
			}
		})
		if t.Failed() {
			return
		}
	}
}

// diffLines lists the lines only in want or only in got.
func diffLines(want, got string) string {
	in := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if g[l] < w[l] {
			fmt.Fprintf(&b, "- %s\n", l)
			g[l]++
		}
	}
	w2 := in(want)
	for _, l := range strings.Split(got, "\n") {
		if w2[l] == 0 {
			fmt.Fprintf(&b, "+ %s\n", l)
		} else {
			w2[l]--
		}
	}
	return b.String()
}
