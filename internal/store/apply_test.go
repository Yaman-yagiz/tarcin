// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

const (
	mePN   domain.JID = "900@s.whatsapp.net"
	meLID  domain.JID = "100@lid"
	aliPN  domain.JID = "901@s.whatsapp.net"
	aliLID domain.JID = "101@lid"
	ayPN   domain.JID = "902@s.whatsapp.net"
	ayLID  domain.JID = "102@lid"
	group  domain.JID = "g1@g.us"
)

var epoch = time.UnixMilli(1_700_000_000_000)

func at(sec int) time.Time { return epoch.Add(time.Duration(sec) * time.Second) }

type applyFixture struct {
	t  *testing.T
	db *sql.DB
	r  *Reader
	a  *EventApplier
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	db, r := readFixture(t)
	return &applyFixture{t: t, db: db, r: r, a: NewEventApplier(ApplierOptions{
		MeLID: meLID, MePN: mePN, Now: func() time.Time { return at(1000) },
	})}
}

// apply runs events through the applier in one transaction.
func (f *applyFixture) apply(events ...domain.Event) *Changes {
	f.t.Helper()
	tx, err := f.db.BeginTx(f.t.Context(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	ch := &Changes{}
	if err := f.a.Apply(f.t.Context(), tx, events, ch); err != nil {
		_ = tx.Rollback()
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
	return ch
}

func incoming(chat, sender domain.JID, waid, body string, sec int) domain.MessageReceived {
	return domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
		ChatJID: chat, SenderJID: sender, WAID: domain.WAMessageID(waid), Timestamp: at(sec),
		Kind: domain.KindText, Body: body,
	}}
}

func (f *applyFixture) chats() []domain.Chat {
	f.t.Helper()
	chats, err := f.r.Chats(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return chats
}

func (f *applyFixture) messages(chat domain.ChatID) []domain.Message {
	f.t.Helper()
	msgs, err := f.r.Latest(f.t.Context(), chat, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	return msgs
}

func (f *applyFixture) identity(jid domain.JID) domain.Identity {
	f.t.Helper()
	ids, err := f.r.Identities(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	for _, id := range ids {
		if id.LID == jid || id.PN == jid {
			return id
		}
	}
	f.t.Fatalf("no identity for %s", jid)
	return domain.Identity{}
}

func (f *applyFixture) count(query string) int { return count(f.t, f.db, query) }

// state is the database content without local IDs or revisions, for
// comparing the outcomes of different event orders.
func (f *applyFixture) state() string {
	f.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT coalesce(lid, '-'), coalesce(pn, '-'), coalesce(contact_name, '-'), coalesce(push_name, '-'),
			coalesce(business_name, '-'), is_me FROM identities ORDER BY 1, 2`,
		`SELECT a.jid, coalesce(i.lid, i.pn) FROM jid_aliases a JOIN identities i ON i.id = a.identity_id ORDER BY 1`,
		`SELECT c.jid, c.kind, coalesce(c.title, '-'), c.last_activity_ts, c.unread_count, coalesce(c.pinned_at, 0),
			c.archived, coalesce(c.muted_until, 0), coalesce((SELECT ts FROM messages WHERE id = c.last_message_id), 0)
			FROM chats c ORDER BY 1`,
		`SELECT c.jid, m.wa_id, coalesce(i.lid, i.pn, '-'), m.from_me, m.ts, m.kind, coalesce(m.body, '-'),
			m.status, coalesce(m.edited_at, 0), coalesce(m.revoked_at, 0), m.flags, m.raw IS NOT NULL, m.unread,
			coalesce(md.mime, '-'), coalesce(md.size, 0)
			FROM messages m JOIN chats c ON c.id = m.chat_id LEFT JOIN identities i ON i.id = m.sender_id
			LEFT JOIN media md ON md.message_id = m.id ORDER BY 1, 2`,
		`SELECT c.jid, r.target_wa_id, coalesce(i.lid, i.pn), r.emoji, r.ts
			FROM reactions r JOIN chats c ON c.id = r.chat_id JOIN identities i ON i.id = r.sender_id ORDER BY 1, 2, 3`,
		`SELECT c.jid, coalesce(i.lid, i.pn), g.role FROM group_members g JOIN chats c ON c.id = g.chat_id
			JOIN identities i ON i.id = g.identity_id ORDER BY 1, 2`,
		`SELECT count(*) FROM pending_ops`,
	} {
		rows, err := f.db.QueryContext(f.t.Context(), q)
		if err != nil {
			f.t.Fatalf("%s: %v", q, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				f.t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		_ = rows.Close()
		b.WriteString("--\n")
	}
	return b.String()
}

// permutations calls fn with every order of events.
func permutations(events []domain.Event, fn func([]domain.Event)) {
	var rec func(int)
	rec = func(k int) {
		if k == len(events) {
			fn(slices.Clone(events))
			return
		}
		for i := k; i < len(events); i++ {
			events[k], events[i] = events[i], events[k]
			rec(k + 1)
			events[k], events[i] = events[i], events[k]
		}
	}
	rec(0)
}

// orderIndependent applies every order of events, one event per
// transaction, and requires the same end state each time.
func orderIndependent(t *testing.T, events ...domain.Event) string {
	t.Helper()
	var want string
	n := 0
	permutations(events, func(order []domain.Event) {
		f := newApplyFixture(t)
		for _, ev := range order {
			f.apply(ev)
		}
		got := f.state()
		if n == 0 {
			want = got
		} else if got != want {
			t.Fatalf("order %d gives a different state:\n%s\nwant:\n%s", n, got, want)
		}
		n++
	})
	return want
}

func TestApplyNewDirectMessage(t *testing.T) {
	f := newApplyFixture(t)
	e := incoming(aliLID, aliLID, "W1", "merhaba", 10)
	e.Message.PushName = "Ali"
	ch := f.apply(e)

	chats := f.chats()
	if len(chats) != 1 {
		t.Fatalf("chats = %+v", chats)
	}
	c := chats[0]
	ali := f.identity(aliLID)
	if c.JID != aliLID || c.Kind != domain.ChatDirect || c.PeerID != ali.ID || c.UnreadCount != 1 ||
		!c.LastActivity.Equal(at(10)) || c.LastMessageID == 0 {
		t.Fatalf("chat = %+v", c)
	}
	if ali.PushName != "Ali" {
		t.Fatalf("push name = %q", ali.PushName)
	}
	msgs := f.messages(c.ID)
	if len(msgs) != 1 || msgs[0].SenderID != ali.ID || msgs[0].Body != "merhaba" || msgs[0].Status != domain.StatusDelivered {
		t.Fatalf("messages = %+v", msgs)
	}
	if len(ch.Added[c.ID]) != 1 || ch.Added[c.ID][0].ID != msgs[0].ID {
		t.Fatalf("changes = %+v", ch)
	}
	if _, ok := ch.Chats[c.ID]; !ok {
		t.Fatal("a new message must touch its chat")
	}
	if _, ok := ch.Identities[ali.ID]; !ok {
		t.Fatal("a new person must be reported, so the UI learns the name")
	}

	// A redelivery changes nothing and notifies nothing.
	if ch := f.apply(e); !ch.Empty() {
		t.Fatalf("redelivery changes = %+v", ch)
	}
	if n := f.count("SELECT count(*) FROM messages"); n != 1 {
		t.Fatalf("%d messages after a redelivery", n)
	}
	if c := f.chats()[0]; c.UnreadCount != 1 {
		t.Fatal("a redelivery must not count as unread again")
	}
}

func TestApplyOwnMessages(t *testing.T) {
	f := newApplyFixture(t)
	other := incoming(aliLID, meLID, "W1", "from my phone", 10)
	other.Message.FromMe = true
	self := incoming(aliLID, meLID, "W2", "from tarcin", 11)
	self.Message.FromMe, self.Source = true, domain.SourceSelf
	f.apply(other, self)
	me := f.identity(meLID)
	if !me.IsMe || me.PN != mePN {
		t.Fatalf("me = %+v", me)
	}
	c := f.chats()[0]
	msgs := f.messages(c.ID)
	if msgs[0].Status != domain.StatusSent || msgs[1].Status != domain.StatusPending ||
		msgs[0].SenderID != me.ID || !msgs[1].FromMe {
		t.Fatalf("messages = %+v", msgs)
	}
	if c.UnreadCount != 0 {
		t.Fatal("own messages are never unread")
	}
}

func TestApplyAccountLinked(t *testing.T) {
	db, r := readFixture(t)
	f := &applyFixture{t: t, db: db, r: r, a: NewEventApplier(ApplierOptions{Now: func() time.Time { return at(1000) }})}
	early := incoming(group, meLID, "W0", "before the link is known", 1)
	early.Message.FromMe = true
	f.apply(early)
	if m := f.messages(f.chats()[0].ID)[0]; m.SenderID != 0 {
		t.Fatal("without a known account, own messages have no sender")
	}
	ch := f.apply(domain.AccountLinked{PN: mePN, LID: meLID, BusinessName: "Me Ltd"})
	me := f.identity(meLID)
	if !me.IsMe || me.PN != mePN || me.BusinessName != "Me Ltd" {
		t.Fatalf("me = %+v", me)
	}
	if _, ok := ch.Identities[me.ID]; !ok {
		t.Fatal("the account's identity must be reported")
	}
	// Later own messages find the account through the database.
	own := incoming(group, meLID, "W1", "mine", 2)
	own.Message.FromMe = true
	f.apply(own)
	msgs := f.messages(f.chats()[0].ID)
	if msgs[1].SenderID != me.ID {
		t.Fatalf("own message sender = %d, want %d", msgs[1].SenderID, me.ID)
	}
	f.apply(domain.AccountLinked{})
	if s := f.a.Stats(); s.Skipped != 1 {
		t.Fatalf("an empty link must be skipped: %+v", s)
	}
	// Linking another account to the same database: the old one is
	// someone else now.
	ch = f.apply(domain.AccountLinked{PN: aliPN, LID: aliLID})
	if old := f.identity(meLID); old.IsMe {
		t.Fatal("the previous account must no longer be mine")
	}
	if _, ok := ch.Identities[me.ID]; !ok {
		t.Fatal("the previous account's identity must be reported")
	}
	if !f.identity(aliLID).IsMe {
		t.Fatal("the new account must be mine")
	}
}

func TestApplyHistoryIsNotUnread(t *testing.T) {
	f := newApplyFixture(t)
	old := incoming(group, aliLID, "H1", "old", 5)
	old.Source = domain.SourceHistory
	f.apply(old, domain.ChatUpdated{ChatJID: group, Fields: domain.ChatFieldUnread | domain.ChatFieldTitle, UnreadCount: 4, Title: "Aile"})
	c := f.chats()[0]
	if c.UnreadCount != 4 || c.Title != "Aile" || c.Kind != domain.ChatGroup {
		t.Fatalf("chat = %+v; history uses the conversation's own unread count", c)
	}
	sys := incoming(group, aliLID, "S1", "Ali joined", 6)
	sys.Message.Kind = domain.KindSystem
	f.apply(sys)
	if c := f.chats()[0]; c.UnreadCount != 4 {
		t.Fatal("system messages are never unread")
	}
}

func TestApplyPNAndLIDShareOneDirectChat(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(aliPN, aliPN, "W1", "by number", 10))
	if c := f.chats()[0]; c.JID != aliPN {
		t.Fatalf("chat = %+v", c)
	}
	ch := f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID})
	f.apply(incoming(aliLID, aliLID, "W2", "by LID", 11), incoming(aliPN, aliPN, "W3", "by number again", 12))
	chats := f.chats()
	if len(chats) != 1 || chats[0].JID != aliLID {
		t.Fatalf("chats = %+v; one person must have one direct chat keyed by the LID", chats)
	}
	if _, ok := ch.Chats[chats[0].ID]; !ok {
		t.Fatal("re-keying a chat must touch it")
	}
	if n := len(f.messages(chats[0].ID)); n != 3 {
		t.Fatalf("%d messages in the chat, want 3", n)
	}
	if ali := f.identity(aliLID); ali.PN != aliPN {
		t.Fatalf("identity = %+v", ali)
	}
	if n := f.count("SELECT count(*) FROM identities WHERE is_me = 0"); n != 1 {
		t.Fatalf("%d identities for one person", n)
	}
}

func TestApplySenderAltLinks(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliPN, "W1", "a", 1))
	e := incoming(group, aliLID, "W2", "b", 2)
	e.Message.SenderAlt = aliPN
	f.apply(e)
	if n := f.count("SELECT count(*) FROM identities WHERE is_me = 0"); n != 1 {
		t.Fatalf("SenderAlt must link the two forms: %d identities", n)
	}
	msgs := f.messages(f.chats()[0].ID)
	if msgs[0].SenderID != msgs[1].SenderID {
		t.Fatal("both messages must have one sender")
	}
}

func TestApplyRecycledNumberMovesToNewPerson(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID}, domain.ContactUpdated{JID: aliLID, ContactName: "Ali"})
	f.apply(domain.IdentityLinked{PN: aliPN, LID: ayLID})
	ali, newOwner := f.identity(aliLID), f.identity(ayLID)
	if ali.PN != "" || newOwner.PN != aliPN || ali.ID == newOwner.ID || ali.ContactName != "Ali" {
		t.Fatalf("old %+v, new %+v", ali, newOwner)
	}
	f.apply(incoming(aliPN, aliPN, "W1", "hi", 1))
	if c := f.chats()[0]; c.JID != ayLID {
		t.Fatalf("a message to the recycled number belongs to its new owner: %+v", c)
	}
}

func TestApplyEditsAndRevokesInAnyOrder(t *testing.T) {
	msg := incoming(group, aliLID, "W1", "v0", 10)
	edit1 := domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "v1", EditedAt: at(20)}
	edit2 := domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "v2", EditedAt: at(30)}
	state := orderIndependent(t, msg, edit1, edit2)
	if !strings.Contains(state, " v2 ") || strings.Contains(state, " v1 ") {
		t.Fatalf("the newest edit must win:\n%s", state)
	}
	revoke := domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(25)}
	state = orderIndependent(t, msg, edit1, revoke, edit2)
	if strings.Contains(state, " v") {
		t.Fatalf("a revoke must remove the content for good:\n%s", state)
	}
	if !strings.Contains(state, "\n0\n--") {
		t.Fatalf("pending operations must be consumed:\n%s", state)
	}
}

func TestApplyOnlyTheSenderMayEditOrRevoke(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID}, incoming(group, aliLID, "W1", "orijinal", 1),
		domain.GroupMembersChanged{ChatJID: group, Members: []domain.GroupMember{{JID: ayLID, Role: domain.RoleAdmin}, {JID: "103@lid"}}})
	body := func() string { return f.messages(f.chats()[0].ID)[0].Body }

	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "sahte", EditedAt: at(2), Actor: ayLID})
	f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(3), Actor: "103@lid", ByAdmin: true})
	f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(3), Actor: "999@lid"})
	if got := body(); got != "orijinal" {
		t.Fatalf("someone else changed the message: %q", got)
	}
	if s := f.a.Stats(); s.Refused != 3 {
		t.Fatalf("refused = %d", s.Refused)
	}
	// The sender may edit, by either address form.
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "düzeltildi", EditedAt: at(4), Actor: aliPN})
	if got := body(); got != "düzeltildi" {
		t.Fatalf("body = %q", got)
	}
	// A group admin may delete it.
	f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(5), Actor: ayLID, ByAdmin: true})
	if m := f.messages(f.chats()[0].ID)[0]; !m.IsRevoked() {
		t.Fatal("an admin's revoke must apply")
	}

	// The check also holds for an edit that arrives before its message.
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W2", Body: "sahte", EditedAt: at(7), Actor: ayLID})
	f.apply(incoming(group, aliLID, "W2", "ikinci", 6))
	for _, m := range f.messages(f.chats()[0].ID) {
		if m.WAID == "W2" && m.Body != "ikinci" {
			t.Fatalf("a queued forged edit applied: %q", m.Body)
		}
	}
}

func TestApplyReceiptsInAnyOrder(t *testing.T) {
	sent := incoming(group, meLID, "W1", "hi", 10)
	sent.Message.FromMe = true
	delivered := domain.ReceiptReceived{ChatJID: group, WAIDs: []domain.WAMessageID{"W1"}, From: aliLID, Status: domain.StatusDelivered, Timestamp: at(11)}
	read := domain.ReceiptReceived{ChatJID: group, WAIDs: []domain.WAMessageID{"W1", "W404"}, From: aliLID, Status: domain.StatusRead, Timestamp: at(12)}
	state := orderIndependent(t, sent, delivered, read)
	if !strings.Contains(state, fmt.Sprintf(" text hi %d ", domain.StatusRead)) {
		t.Fatalf("status must end as read:\n%s", state)
	}
	// The receipt for the unknown W404 keeps waiting.
	if !strings.Contains(state, "\n1\n--") {
		t.Fatalf("want one pending receipt:\n%s", state)
	}
}

func TestApplyReactionsInAnyOrder(t *testing.T) {
	msg := incoming(group, aliLID, "W1", "hi", 10)
	set := domain.ReactionReceived{ChatJID: group, TargetWAID: "W1", SenderJID: ayLID, Emoji: "+1", Timestamp: at(20)}
	remove := domain.ReactionReceived{ChatJID: group, TargetWAID: "W1", SenderJID: ayLID, Timestamp: at(30)}
	other := domain.ReactionReceived{ChatJID: group, TargetWAID: "W1", SenderJID: aliLID, Emoji: "<3", Timestamp: at(25)}
	state := orderIndependent(t, msg, set, remove, other)
	if strings.Contains(state, "+1") || !strings.Contains(state, "<3") {
		t.Fatalf("a newer removal must win over an older reaction:\n%s", state)
	}

	f := newApplyFixture(t)
	f.apply(msg)
	ch := f.apply(set)
	c := f.chats()[0]
	if len(ch.Updated[c.ID]) != 1 || ch.Updated[c.ID][0].Revision == 0 {
		t.Fatalf("a reaction must update its message's revision: %+v", ch)
	}
	if ch := f.apply(set); !ch.Empty() {
		t.Fatal("a repeated reaction changes nothing")
	}
}

func TestApplyRedeliveryImprovesTheRow(t *testing.T) {
	f := newApplyFixture(t)
	placeholder := incoming(group, aliLID, "W1", "", 10)
	placeholder.Message.Kind = domain.KindUnsupported
	f.apply(placeholder)
	decrypted := incoming(group, aliLID, "W1", "a cat", 10)
	decrypted.Message.Kind = domain.KindImage
	decrypted.Message.Raw = []byte{1, 2, 3}
	decrypted.Message.Media = &domain.Media{MIME: "image/jpeg", Width: 640}
	ch := f.apply(decrypted)
	c := f.chats()[0]
	m := f.messages(c.ID)[0]
	if m.Kind != domain.KindImage || m.Body != "a cat" || m.Media == nil || m.Media.Width != 640 || m.Revision == 0 {
		t.Fatalf("message = %+v", m)
	}
	if len(ch.Updated[c.ID]) != 1 {
		t.Fatalf("changes = %+v", ch)
	}

	// Media fields are filled in, the local download state is kept.
	mustExec(t, f.db, "UPDATE media SET state = 2")
	decrypted.Message.Media = &domain.Media{MIME: "image/png", Width: 1, Size: 4096}
	f.apply(decrypted)
	m = f.messages(c.ID)[0]
	if m.Media.MIME != "image/jpeg" || m.Media.Width != 640 || m.Media.Size != 4096 || m.Media.State != domain.MediaLocal {
		t.Fatalf("media = %+v", m.Media)
	}

	// A revoked message stays empty whatever is redelivered.
	f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(11)})
	f.apply(decrypted)
	m = f.messages(c.ID)[0]
	if !m.IsRevoked() || m.Body != "" || m.Media != nil || f.count("SELECT count(*) FROM messages WHERE raw IS NOT NULL") != 0 {
		t.Fatalf("revoked message = %+v", m)
	}
}

// TestApplyRedeliveryFromAnotherSenderIsRefused: a message ID is visible
// to every member, so someone else's message with the ID of a placeholder
// (a view-once message never reaches a linked device) must not fill it.
func TestApplyRedeliveryFromAnotherSenderIsRefused(t *testing.T) {
	f := newApplyFixture(t)
	viewOnce := incoming(group, aliLID, "W1", "", 10)
	viewOnce.Message.Kind = domain.KindUnsupported
	f.apply(viewOnce)
	forged := incoming(group, "666@lid", "W1", "transfer the money", 11)
	if ch := f.apply(forged); len(ch.Updated) != 0 || len(ch.Added) != 0 {
		t.Fatalf("a message from another sender changed the row: %+v", ch)
	}
	c, _, _ := f.r.ChatID(t.Context(), group)
	if m := f.messages(c)[0]; m.Kind != domain.KindUnsupported || m.Body != "" || m.SenderID != f.identity(aliLID).ID {
		t.Fatalf("placeholder = %+v", m)
	}

	// In a direct chat, the peer cannot fill this account's own placeholder.
	own := incoming(aliLID, meLID, "W2", "", 12)
	own.Message.FromMe, own.Message.Kind = true, domain.KindUnsupported
	f.apply(own)
	f.apply(incoming(aliLID, aliLID, "W2", "I agree to pay", 13))
	dm, _, _ := f.r.ChatID(t.Context(), aliLID)
	if m := f.messages(dm)[0]; !m.FromMe || m.Body != "" || m.Kind != domain.KindUnsupported {
		t.Fatalf("own placeholder = %+v", m)
	}
	if got := f.a.Stats().SenderMismatches; got != 2 {
		t.Fatalf("%d mismatches counted, want 2", got)
	}
}

func TestApplyPlainTextKeepsNoRaw(t *testing.T) {
	f := newApplyFixture(t)
	e := incoming(group, aliLID, "W1", "text", 1)
	e.Message.Raw = []byte{1}
	f.apply(e)
	if f.count("SELECT count(*) FROM messages WHERE raw IS NOT NULL") != 0 {
		t.Fatal("text messages must not store the protocol payload")
	}
}

func TestApplyChatUpdatedFields(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.ChatUpdated{ChatJID: group, Fields: domain.ChatFieldTitle | domain.ChatFieldPinned | domain.ChatFieldMuted |
		domain.ChatFieldEphemeral, Title: "Aile", PinnedAt: at(5), MutedUntil: domain.MuteForever, EphemeralTimer: 7 * 24 * time.Hour})
	f.apply(domain.ChatUpdated{ChatJID: group, Fields: domain.ChatFieldArchived, Archived: true, Title: "ignored"})
	c := f.chats()[0]
	if c.Title != "Aile" || !c.PinnedAt.Equal(at(5)) || !c.MutedUntil.Equal(domain.MuteForever) ||
		c.EphemeralTimer != 7*24*time.Hour || !c.Archived {
		t.Fatalf("chat = %+v", c)
	}
	f.apply(domain.ChatUpdated{ChatJID: group, Fields: domain.ChatFieldPinned | domain.ChatFieldMuted | domain.ChatFieldEphemeral})
	if c := f.chats()[0]; c.IsPinned() || !c.MutedUntil.IsZero() || c.EphemeralTimer != 0 || c.Title != "Aile" {
		t.Fatalf("cleared fields: %+v", c)
	}
}

func TestApplyContactUpdated(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(aliLID, aliLID, "W1", "hi", 1))
	ch := f.apply(domain.ContactUpdated{JID: aliLID, ContactName: "Ali Veli", PushName: "ali"})
	ali := f.identity(aliLID)
	c := f.chats()[0]
	if ali.ContactName != "Ali Veli" || ali.PushName != "ali" {
		t.Fatalf("identity = %+v", ali)
	}
	if _, ok := ch.Identities[ali.ID]; !ok {
		t.Fatal("name changes must be notified")
	}
	if _, ok := ch.Chats[c.ID]; !ok {
		t.Fatal("a direct chat's title follows its peer's name")
	}
	if ch := f.apply(domain.ContactUpdated{JID: aliLID, PushName: "ali"}); !ch.Empty() {
		t.Fatal("an unchanged name must not be notified")
	}
	f.apply(domain.ContactUpdated{JID: aliLID, BusinessName: "Ali Ltd"})
	if ali := f.identity(aliLID); ali.ContactName != "Ali Veli" || ali.BusinessName != "Ali Ltd" {
		t.Fatalf("empty fields must leave names alone: %+v", ali)
	}

	// A name from a message only fills a gap.
	e := incoming(aliLID, aliLID, "W2", "hi", 2)
	e.Message.PushName = "stale"
	f.apply(e)
	if ali := f.identity(aliLID); ali.PushName != "ali" {
		t.Fatalf("push name = %q", ali.PushName)
	}
	// My own push name is not taken from contacts.
	own := incoming(aliLID, meLID, "W3", "hi", 3)
	own.Message.FromMe = true
	f.apply(own, domain.ContactUpdated{JID: meLID, PushName: "someone"})
	if me := f.identity(meLID); me.PushName != "" {
		t.Fatalf("me = %+v", me)
	}
}

func TestApplyGroupMembers(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.GroupMembersChanged{ChatJID: group, Members: []domain.GroupMember{
		{JID: aliLID, AltJID: aliPN, Role: domain.RoleAdmin},
		{JID: ayPN},
	}})
	f.apply(domain.GroupMembersChanged{ChatJID: group, Full: true, Members: []domain.GroupMember{
		{JID: aliPN, Role: domain.RoleSuperAdmin},
		{JID: meLID},
	}})
	state := f.state()
	for _, want := range []string{
		fmt.Sprintf("g1@g.us 101@lid %d", domain.RoleSuperAdmin),
		"g1@g.us 100@lid 0",
	} {
		if !strings.Contains(state, want) {
			t.Errorf("missing member %q in\n%s", want, state)
		}
	}
	if strings.Contains(state, "g1@g.us 902@s.whatsapp.net") {
		t.Error("a full list must remove members not in it")
	}
	if n := f.count("SELECT participant_count FROM group_meta"); n != 2 {
		t.Errorf("participant_count = %d", n)
	}
	// Someone leaves, by either address form; an unknown leaver is ignored.
	f.apply(domain.GroupMembersChanged{ChatJID: group, Left: []domain.JID{aliPN, "999@lid"}})
	if state := f.state(); strings.Contains(state, "g1@g.us 101@lid") || !strings.Contains(state, "g1@g.us 100@lid") {
		t.Errorf("after Ali left:\n%s", state)
	}
}

func TestApplyGroupMetaAndMemberPages(t *testing.T) {
	f := newApplyFixture(t)
	ch := f.apply(domain.GroupMetaUpdated{ChatJID: group, Fields: domain.GroupFieldTopic | domain.GroupFieldOwner | domain.GroupFieldAnnounce,
		Topic: "Weekend plans", Owner: aliLID, AnnounceOnly: true})
	chat := f.chats()[0].ID
	if _, ok := ch.Groups[chat]; !ok {
		t.Fatal("a metadata change must be reported")
	}
	meta, ok, err := f.r.GroupMeta(t.Context(), chat)
	if err != nil || !ok || meta.Topic != "Weekend plans" || meta.Owner != f.identity(aliLID).ID || !meta.AnnounceOnly || meta.Locked {
		t.Fatalf("meta = %+v, %v, %v", meta, ok, err)
	}
	if !meta.FetchedAt.IsZero() {
		t.Fatal("metadata alone is not a fetched member list")
	}
	// Fields not set stay; a full member list sets the fetch time.
	f.apply(domain.GroupMetaUpdated{ChatJID: group, Fields: domain.GroupFieldLocked, Locked: true},
		domain.AccountLinked{PN: mePN, LID: meLID}, domain.ContactUpdated{JID: aliLID, PushName: "Çiğdem"}, domain.ContactUpdated{JID: ayLID, ContactName: "Ayşe"},
		domain.GroupMembersChanged{ChatJID: group, Full: true, Members: []domain.GroupMember{
			{JID: ayLID}, {JID: "103@lid"}, {JID: aliLID, Role: domain.RoleAdmin}, {JID: meLID}, {JID: "104@lid", Role: domain.RoleSuperAdmin},
		}})
	meta, _, _ = f.r.GroupMeta(t.Context(), chat)
	if meta.Topic != "Weekend plans" || !meta.Locked || !meta.AnnounceOnly || meta.ParticipantCount != 5 || meta.FetchedAt.IsZero() {
		t.Fatalf("meta = %+v", meta)
	}
	if n, err := f.r.GroupMemberCount(t.Context(), chat); err != nil || n != 5 {
		t.Fatalf("count = %d, %v", n, err)
	}
	var order []domain.JID
	for offset := 0; ; offset += 2 {
		page, err := f.r.GroupMembers(t.Context(), chat, offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page {
			ids, _ := f.r.IdentitiesByID(t.Context(), []domain.IdentityID{m.Identity})
			order = append(order, ids[0].LID)
		}
		if len(page) < 2 {
			break
		}
	}
	// Me; the owner; the admin; named members by name; the unnamed.
	if want := []domain.JID{meLID, "104@lid", aliLID, ayLID, "103@lid"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	// Turkish letters sort among their Latin ones: Çiğdem after Ayşe and
	// before Zeynep, as a plain member.
	f.apply(domain.ContactUpdated{JID: "105@lid", PushName: "Zeynep"}, domain.GroupMembersChanged{ChatJID: group, Members: []domain.GroupMember{
		{JID: aliLID}, {JID: "105@lid"},
	}})
	order = order[:0]
	page, _ := f.r.GroupMembers(t.Context(), chat, 0, 10)
	for _, m := range page {
		ids, _ := f.r.IdentitiesByID(t.Context(), []domain.IdentityID{m.Identity})
		order = append(order, ids[0].LID)
	}
	if want := []domain.JID{meLID, "104@lid", ayLID, aliLID, "105@lid", "103@lid"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if _, ok, _ := f.r.GroupMeta(t.Context(), 999); ok {
		t.Fatal("an unknown group has no metadata")
	}
	f.apply(domain.GroupMetaUpdated{ChatJID: group}, domain.GroupMetaUpdated{ChatJID: aliLID, Fields: domain.GroupFieldTopic})
	if s := f.a.Stats(); s.Skipped != 2 {
		t.Fatalf("empty or non-group metadata must be skipped: %+v", s)
	}
}

func TestApplyHistoryQueue(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.HistoryQueued{ID: "N1", Notification: []byte{1}}, domain.HistoryQueued{ID: "N2", Notification: []byte{2}})
	f.apply(domain.HistoryQueued{ID: "N1", Notification: []byte{9}}) // a redelivery keeps the first
	pending, err := f.r.PendingHistory(t.Context())
	if err != nil || len(pending) != 2 || pending[0].ID != "N1" || pending[0].Notification[0] != 1 || pending[1].ID != "N2" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	f.apply(domain.HistoryDone{ID: "N1"}, domain.HistoryDone{ID: "unknown"})
	pending, _ = f.r.PendingHistory(t.Context())
	if len(pending) != 1 || pending[0].ID != "N2" {
		t.Fatalf("pending = %+v", pending)
	}
	f.apply(domain.HistoryQueued{}, domain.HistoryDone{})
	if s := f.a.Stats(); s.Skipped != 2 {
		t.Fatalf("stats = %+v", s)
	}
	f.apply(domain.ChatUpdated{ChatJID: group, Fields: domain.ChatFieldHistoryComplete, HistoryComplete: true})
	if c := f.chats()[0]; !c.HistoryComplete {
		t.Fatal("history_complete must be set")
	}
}

func TestApplySendsInterrupted(t *testing.T) {
	f := newApplyFixture(t)
	pending := incoming(group, meLID, "P1", "yarım kaldı", 10)
	pending.Message.FromMe, pending.Source = true, domain.SourceSelf
	later := incoming(group, meLID, "P2", "şimdi yazıldı", 30)
	later.Message.FromMe, later.Source = true, domain.SourceSelf
	f.apply(pending, later)
	ch := f.apply(domain.SendsInterrupted{Before: at(20)})
	msgs := f.messages(f.chats()[0].ID)
	if msgs[0].Status != domain.StatusFailed || msgs[1].Status != domain.StatusPending {
		t.Fatalf("statuses = %v, %v", msgs[0].Status, msgs[1].Status)
	}
	if len(ch.Updated[f.chats()[0].ID]) != 1 {
		t.Fatal("the failed message must be notified")
	}
	// A retry that succeeds moves it on.
	f.apply(domain.ReceiptReceived{ChatJID: group, WAIDs: []domain.WAMessageID{"P1"}, Status: domain.StatusSent, Timestamp: at(40)})
	if m := f.messages(f.chats()[0].ID)[0]; m.Status != domain.StatusSent {
		t.Fatalf("after a retry: %v", m.Status)
	}
}

func TestApplyChatRead(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliLID, "W1", "old", 10), incoming(group, aliLID, "W2", "new", 30))
	ch := f.apply(domain.ChatRead{ChatJID: group, ReadAt: at(20)})
	c := f.chats()[0]
	if c.UnreadCount != 1 || !c.LastReadAt.Equal(at(20)) {
		t.Fatalf("chat = %+v; a message after the read stays unread", c)
	}
	if _, ok := ch.Chats[c.ID]; !ok {
		t.Fatal("a read must touch the chat")
	}
	if ch := f.apply(domain.ChatRead{ChatJID: group, ReadAt: at(15)}); !ch.Empty() {
		t.Fatal("an older read changes nothing")
	}
	f.apply(incoming(group, aliLID, "W0", "older than the read", 5))
	if c := f.chats()[0]; c.UnreadCount != 1 {
		t.Fatalf("a message older than the read is not unread: %d", c.UnreadCount)
	}
	refs, err := f.r.UnreadIncoming(t.Context(), f.chats()[0].ID, 10)
	if err != nil || len(refs) != 1 || refs[0].WAID != "W2" || refs[0].Sender != aliLID {
		t.Fatalf("unread refs = %+v, %v", refs, err)
	}
	f.apply(domain.ChatRead{ChatJID: group, ReadAt: at(40)})
	if c := f.chats()[0]; c.UnreadCount != 0 {
		t.Fatalf("unread = %d", c.UnreadCount)
	}
	if refs, _ := f.r.UnreadIncoming(t.Context(), f.chats()[0].ID, 10); len(refs) != 0 {
		t.Fatalf("after the read: %+v", refs)
	}
	// Reads and messages commute.
	read := domain.ChatRead{ChatJID: group, ReadAt: at(20)}
	orderIndependent(t, incoming(group, aliLID, "W1", "a", 10), incoming(group, aliLID, "W2", "b", 30), read)
}

func TestReaderChatID(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliLID, "W1", "hi", 1), incoming(aliPN, aliPN, "W2", "hi", 1),
		domain.IdentityLinked{PN: aliPN, LID: aliLID})
	chats := f.chats()
	byJID := map[domain.JID]domain.ChatID{}
	for _, c := range chats {
		byJID[c.JID] = c.ID
	}
	for jid, want := range map[domain.JID]domain.ChatID{group: byJID[group], aliPN: byJID[aliLID], aliLID: byJID[aliLID]} {
		got, ok, err := f.r.ChatID(t.Context(), jid)
		if err != nil || !ok || got != want {
			t.Errorf("ChatID(%s) = %d, %v, %v; want %d", jid, got, ok, err, want)
		}
	}
	if _, ok, err := f.r.ChatID(t.Context(), ayLID); ok || err != nil {
		t.Errorf("an unknown person has no chat: %v, %v", ok, err)
	}
}

func TestApplySkipsMalformedEvents(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(
		incoming(group, aliLID, "", "no id", 1),
		incoming("x@unknown.server", aliLID, "W1", "bad chat", 1),
		domain.ReceiptReceived{ChatJID: group},
		domain.MessageEdited{ChatJID: group},
		domain.ReactionReceived{ChatJID: group, TargetWAID: "W1"},
		domain.ContactUpdated{JID: group},
		domain.GroupMembersChanged{ChatJID: aliLID},
		domain.ChatUpdated{ChatJID: "nouser"},
		incoming(group, aliLID, "W2", "fine", 2),
		domain.ChatTyping{ChatJID: group},
	)
	if s := f.a.Stats(); s.Skipped != 8 {
		t.Fatalf("skipped = %d, want 8", s.Skipped)
	}
	if n := f.count("SELECT count(*) FROM messages"); n != 1 {
		t.Fatalf("valid events must still apply: %d messages", n)
	}
}

func TestApplyCountsSenderMismatches(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliLID, "W1", "hi", 1))
	f.apply(incoming(group, ayLID, "W1", "hi", 1))
	if s := f.a.Stats(); s.SenderMismatches != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestApplyMaintainPrunesOldPendingOps(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "OLD", Body: "x", EditedAt: at(1000).Add(-31 * 24 * time.Hour)})
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "NEW", Body: "x", EditedAt: at(999)})
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Maintain(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var left string
	if err := f.db.QueryRowContext(t.Context(), "SELECT group_concat(target_wa_id) FROM pending_ops").Scan(&left); err != nil || left != "NEW" {
		t.Fatalf("pending ops left: %q, %v", left, err)
	}
}

// TestApplyPendingOpsAreBounded: what waits for a target that never
// comes cannot grow without bound, whatever times its senders claim.
func TestApplyPendingOpsAreBounded(t *testing.T) {
	f := newApplyFixture(t)
	future := domain.MessageEdited{ChatJID: group, TargetWAID: "FUTURE", Body: "x", EditedAt: at(1000).AddDate(5, 0, 0)}
	f.apply(future)
	f.apply(future) // redelivered
	if n := f.count(fmt.Sprintf("SELECT count(*) FROM pending_ops WHERE target_wa_id = 'FUTURE' AND ts <= %d", at(1000).UnixMilli())); n != 1 {
		t.Fatalf("%d rows for one future edit; want one, timed now at the latest", n)
	}
	var evs []domain.Event
	for i := range maxPendingPerChat + 50 {
		evs = append(evs, domain.MessageEdited{ChatJID: group, TargetWAID: domain.WAMessageID(fmt.Sprint("T", i)), Body: "x", EditedAt: at(10)})
	}
	f.apply(evs...)
	if n := f.count("SELECT count(*) FROM pending_ops"); n != maxPendingPerChat {
		t.Fatalf("%d operations wait in one chat, want at most %d", n, maxPendingPerChat)
	}

	old := at(1000).Add(-31 * 24 * time.Hour)
	f.apply(incoming(group, aliLID, "HERE", "hi", 1))
	f.apply(domain.ReactionReceived{ChatJID: group, TargetWAID: "HERE", SenderJID: aliLID, Emoji: "x", Timestamp: old})
	f.apply(domain.ReactionReceived{ChatJID: group, TargetWAID: "GONE", SenderJID: aliLID, Emoji: "x", Timestamp: old})
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Maintain(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var left string
	if err := f.db.QueryRowContext(t.Context(), "SELECT group_concat(target_wa_id) FROM reactions").Scan(&left); err != nil || left != "HERE" {
		t.Fatalf("reactions left: %q, %v; a reaction to a message that never came goes after the retention", left, err)
	}
}

func TestEventApplierThroughTheWriter(t *testing.T) {
	f := newApplyFixture(t)
	commits := make(chan *Changes, 4)
	w, err := NewWriter(f.db, WriterOptions{Applier: f.a, OnCommit: func(c *Changes) { commits <- c }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	if err := w.Deliver(t.Context(), []domain.Event{
		incoming(aliLID, aliLID, "W1", "one", 1),
		domain.ChatTyping{ChatJID: aliLID, SenderJID: aliLID, Typing: true},
	}); err != nil {
		t.Fatal(err)
	}
	c := <-commits
	if len(c.Added) != 1 || len(c.Chats) != 1 {
		t.Fatalf("changes = %+v", c)
	}
	if n := f.count("SELECT count(*) FROM messages"); n != 1 {
		t.Fatal("the message must be stored")
	}
}

func TestReadReactions(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.AccountLinked{PN: mePN, LID: meLID}, incoming(group, aliLID, "W1", "toplantı saat 5", 1))
	react := func(who domain.JID, emoji string, sec int) domain.Event {
		return domain.ReactionReceived{ChatJID: group, TargetWAID: "W1", SenderJID: who, Emoji: emoji, Timestamp: at(sec)}
	}
	f.apply(react(aliLID, "❤", 2), react(meLID, "❤", 3), react(ayLID, "\U0001F44D", 4), react("103@lid", "\U0001F602", 5), react("103@lid", "", 6))
	msgs := f.messages(f.chats()[0].ID)
	want := []domain.ReactionCount{{Emoji: "❤", Count: 2, Mine: true}, {Emoji: "\U0001F44D", Count: 1}}
	if !slices.Equal(msgs[0].Reactions, want) {
		t.Fatalf("reactions = %+v, want %+v (a removed one is gone)", msgs[0].Reactions, want)
	}
	f.apply(incoming(group, aliLID, "W2", "yok", 7))
	if msgs := f.messages(f.chats()[0].ID); msgs[1].Reactions != nil {
		t.Fatalf("a message without reactions has none: %+v", msgs[1].Reactions)
	}
	if _, err := parseReactions(`[["x", 1]]`); err == nil {
		t.Fatal("a malformed reaction must be an error")
	}
}

func TestReadMentions(t *testing.T) {
	f := newApplyFixture(t)
	m := incoming(group, aliLID, "W1", "@902 @101 bak", 1)
	m.Message.Mentions = []domain.JID{ayPN, aliLID, ayPN, "bad"}
	f.apply(m)
	msg := f.messages(f.chats()[0].ID)[0]
	// Someone unknown is mentioned by address only: a mention creates no
	// identity, so a sender cannot fill the store with them.
	want := []domain.Mention{{Token: "@902", Identity: 0}, {Token: "@101", Identity: f.identity(aliLID).ID}}
	if !slices.Equal(msg.Mentions, want) {
		t.Fatalf("mentions = %+v, want %+v", msg.Mentions, want)
	}
	if n := f.count("SELECT count(*) FROM jid_aliases WHERE jid = '902@s.whatsapp.net'"); n != 0 {
		t.Fatal("a mention must not create an identity")
	}
	// Once Ayşe's number is known and merged with her LID, the mention
	// names her.
	f.apply(domain.IdentityLinked{PN: ayPN, LID: ayLID})
	msg = f.messages(f.chats()[0].ID)[0]
	if survivor := f.identity(ayLID); msg.Mentions[0].Identity != survivor.ID || survivor.ID == 0 {
		t.Fatalf("mention after the merge = %+v, survivor %d", msg.Mentions[0], survivor.ID)
	}
	f.apply(incoming(group, aliLID, "W2", "none", 2))
	if msgs := f.messages(f.chats()[0].ID); msgs[1].Mentions != nil {
		t.Fatalf("no mentions: %+v", msgs[1].Mentions)
	}
	many := incoming(group, aliLID, "W3", "@all", 3)
	for i := range maxMentions + 500 {
		many.Message.Mentions = append(many.Message.Mentions, domain.JID(fmt.Sprintf("%d@lid", 5000+i%(maxMentions+100))))
	}
	f.apply(many)
	if msgs := f.messages(f.chats()[0].ID); len(msgs[2].Mentions) != maxMentions {
		t.Fatalf("%d mentions kept, want at most %d", len(msgs[2].Mentions), maxMentions)
	}
	if _, err := parseMentions(`[["x"]]`); err == nil {
		t.Fatal("a malformed mention must be an error")
	}
}

func TestMediaStates(t *testing.T) {
	f := newApplyFixture(t)
	img := incoming(group, aliLID, "IMG", "bir kedi", 1)
	img.Message.Kind, img.Message.Raw = domain.KindImage, []byte{1, 2, 3}
	img.Message.Media = &domain.Media{MIME: "image/jpeg", Size: 1234}
	f.apply(img, incoming(group, aliLID, "TXT", "metin", 2))
	chat := f.chats()[0].ID
	src, ok, err := f.r.MediaSource(t.Context(), chat, "IMG")
	if err != nil || !ok || !slices.Equal(src.Raw, []byte{1, 2, 3}) || src.Media.MIME != "image/jpeg" || src.Media.State != domain.MediaRemote || src.LocalName != "" {
		t.Fatalf("source = %+v, %v, %v", src, ok, err)
	}
	if _, ok, _ := f.r.MediaSource(t.Context(), chat, "TXT"); ok {
		t.Fatal("a text message has no media")
	}
	rev := f.messages(chat)[0].Revision
	ch := f.apply(domain.MediaChanged{ChatJID: group, TargetWAID: "IMG", State: domain.MediaDownloading})
	if m := f.messages(chat)[0]; m.Media.State != domain.MediaDownloading || m.Revision == rev || len(ch.Updated[chat]) != 1 {
		t.Fatalf("downloading: %+v", m.Media)
	}
	f.apply(domain.MediaChanged{ChatJID: group, TargetWAID: "IMG", State: domain.MediaLocal, SHA256: []byte{9}, LocalName: "ab.jpg"})
	src, _, _ = f.r.MediaSource(t.Context(), chat, "IMG")
	if src.Media.State != domain.MediaLocal || src.LocalName != "ab.jpg" || !slices.Equal(src.SHA256, []byte{9}) {
		t.Fatalf("local: %+v", src)
	}
	rev = f.messages(chat)[0].Revision
	ch = f.apply(domain.MediaChanged{ChatJID: group, TargetWAID: "IMG", State: domain.MediaLocal, SHA256: []byte{9}, LocalName: "ab.jpg"})
	if f.messages(chat)[0].Revision != rev || len(ch.Updated) != 0 {
		t.Fatal("the same state again changes nothing")
	}
	f.apply(domain.MediaEvicted{LocalName: "ab.jpg"})
	src, _, _ = f.r.MediaSource(t.Context(), chat, "IMG")
	if src.Media.State != domain.MediaRemote || src.LocalName != "" || !slices.Equal(src.SHA256, []byte{9}) {
		t.Fatalf("evicted: %+v (the hash stays)", src)
	}
	f.apply(domain.MediaChanged{ChatJID: group, TargetWAID: "TXT", State: domain.MediaLocal, LocalName: "x"},
		domain.MediaChanged{ChatJID: group, TargetWAID: "NONE", State: domain.MediaFailed},
		domain.MediaChanged{ChatJID: group, TargetWAID: "IMG", State: domain.MediaLocal},
		domain.MediaEvicted{})
	if s := f.a.Stats(); s.Skipped != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestMessagesExpire(t *testing.T) {
	f := newApplyFixture(t)
	gone := incoming(group, aliLID, "GONE", "kaybolacak", 1)
	gone.Message.Expiration = time.Hour
	stays := incoming(group, aliLID, "STAYS", "kalıcı", 2)
	later := incoming(group, aliLID, "LATER", "sonra", 3)
	later.Message.Expiration = 48 * time.Hour
	img := incoming(group, aliLID, "IMG", "", 4)
	img.Message.Expiration, img.Message.Kind, img.Message.Media = time.Hour, domain.KindImage, &domain.Media{MIME: "image/jpeg"}
	f.apply(gone, stays, later, img,
		domain.ReactionReceived{ChatJID: group, TargetWAID: "GONE", SenderJID: ayLID, Emoji: "x", Timestamp: at(5)},
		domain.MediaChanged{ChatJID: group, TargetWAID: "IMG", State: domain.MediaLocal, LocalName: "img.jpg"})
	chat := f.chats()[0]
	if chat.UnreadCount != 4 {
		t.Fatalf("unread = %d", chat.UnreadCount)
	}
	soon := at(2).Add(2 * time.Hour)
	ch := f.apply(domain.MessagesExpired{Now: soon})
	if !slices.Equal(ch.Unlinked, []string{"img.jpg"}) {
		t.Fatalf("unlinked = %v; the commit names the files it leaves unused", ch.Unlinked)
	}
	var left []domain.WAMessageID
	for _, m := range f.messages(chat.ID) {
		left = append(left, m.WAID)
	}
	if !slices.Equal(left, []domain.WAMessageID{"STAYS", "LATER"}) {
		t.Fatalf("left %v", left)
	}
	if _, ok := ch.Reload[chat.ID]; !ok || f.chats()[0].UnreadCount != 2 {
		t.Fatalf("the chat reloads and recounts: unread %d", f.chats()[0].UnreadCount)
	}
	if n := f.count("SELECT count(*) FROM reactions"); n != 0 {
		t.Fatalf("an expired message's reactions go: %d", n)
	}
	if got := f.search("kaybolacak"); len(got) != 0 {
		t.Fatalf("and its index entry: %v", got)
	}
	if ch := f.apply(domain.MessagesExpired{Now: soon}); len(ch.Reload) != 0 {
		t.Fatal("expiring again changes nothing")
	}
}

func TestChatDeletedAndCleared(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.IdentityLinked{PN: aliPN, LID: aliLID},
		incoming(group, aliLID, "A", "eski", 1), incoming(group, aliLID, "B", "eski 2", 2), incoming(aliLID, aliLID, "D", "dm", 3),
		domain.ReactionReceived{ChatJID: group, TargetWAID: "A", SenderJID: ayLID, Emoji: "x", Timestamp: at(4)})
	var g, dm domain.ChatID
	for _, c := range f.chats() {
		if c.JID == group {
			g = c.ID
		} else {
			dm = c.ID
		}
	}
	// Cleared up to B's time: both go, the chat stays, its counts follow.
	ch := f.apply(domain.ChatCleared{ChatJID: group, Before: at(2)})
	if msgs := f.messages(g); len(msgs) != 0 {
		t.Fatalf("left %d", len(msgs))
	}
	if _, ok := ch.Reload[g]; !ok || len(f.chats()) != 2 || f.count("SELECT count(*) FROM reactions") != 0 {
		t.Fatal("a cleared chat stays, reloaded, without its reactions")
	}
	for _, c := range f.chats() {
		if c.ID == g && (c.UnreadCount != 0 || c.LastMessageID != 0) {
			t.Fatalf("cleared chat = %+v", c)
		}
	}
	// A deletion keeps what came after it, and the chat with it.
	f.apply(incoming(aliLID, aliLID, "E", "sonra", 9))
	f.apply(domain.ChatDeleted{ChatJID: aliPN, Before: at(5)}) // by its other address form
	if msgs := f.messages(dm); len(msgs) != 1 || msgs[0].WAID != "E" {
		t.Fatalf("left %+v", msgs)
	}
	ch = f.apply(domain.ChatDeleted{ChatJID: aliLID, Before: at(10)})
	if _, ok := ch.Removed[dm]; !ok || len(f.chats()) != 1 {
		t.Fatal("a deleted chat with nothing newer goes")
	}
	if got := f.search("sonra"); len(got) != 0 {
		t.Fatalf("index entries go: %v", got)
	}
	f.apply(domain.ChatDeleted{ChatJID: group}, domain.ChatCleared{ChatJID: "bad"})
	if s := f.a.Stats(); s.Skipped != 2 {
		t.Fatalf("stats = %+v", s)
	}
}
