// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	tstore "github.com/Yaman-yagiz/tarcin/internal/store"
)

func dmFrom(sender types.JID, id string) *events.Message {
	return &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: sender, Sender: sender}, ID: id, Timestamp: ts,
	}, Message: &waE2E.Message{Conversation: proto.String("selam")}}
}

func TestResolveFromTheLIDMap(t *testing.T) {
	a := testAdapter(t)
	if err := a.sess.container.LIDMap.PutLIDMapping(t.Context(), aliJID, aliPN); err != nil {
		t.Fatal(err)
	}
	evt := dmFrom(aliPN, "A1")
	evs := a.resolveMessage(t.Context(), &evt.Info, a.normalizeMessage(evt, domain.SourceLive))
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if l := evs[0].(domain.IdentityLinked); l.PN != "905551112233@s.whatsapp.net" || l.LID != "101@lid" {
		t.Fatalf("the chat link must come first: %+v", evs[0])
	}
	if m := evs[1].(domain.MessageReceived); m.Message.SenderAlt != "101@lid" {
		t.Fatalf("SenderAlt from the LID map: %+v", m.Message)
	}

	// An unknown person stays as delivered.
	stranger := types.JID{User: "905559990000", Server: types.DefaultUserServer}
	evt = dmFrom(stranger, "A2")
	if evs := a.resolveMessage(t.Context(), &evt.Info, a.normalizeMessage(evt, domain.SourceLive)); len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}

	// An own message names the peer's other form in RecipientAlt.
	own := dmFrom(stranger, "A3")
	own.Info.IsFromMe, own.Info.RecipientAlt = true, types.JID{User: "777", Server: types.HiddenUserServer}
	evs = a.resolveMessage(t.Context(), &own.Info, a.normalizeMessage(own, domain.SourceLive))
	if l, ok := evs[0].(domain.IdentityLinked); !ok || l.LID != "777@lid" || l.PN != "905559990000@s.whatsapp.net" {
		t.Fatalf("events = %+v", evs)
	}

	// In a group the chat is no person, but the sender gets its other form.
	g := groupMsg(&waE2E.Message{Conversation: proto.String("x")})
	g.Info.SenderAlt = types.EmptyJID
	evs = a.resolveMessage(t.Context(), &g.Info, a.normalizeMessage(g, domain.SourceLive))
	if len(evs) != 2 || evs[1].(domain.MessageReceived).Message.SenderAlt != "905551112233@s.whatsapp.net" {
		t.Fatalf("events = %+v", evs)
	}
	if l := evs[0].(domain.IdentityLinked); l.LID != "101@lid" {
		t.Fatalf("the sender's link comes first: %+v", evs[0])
	}
}

func TestNormalizeGroups(t *testing.T) {
	a := testAdapter(t)
	if err := a.sess.container.LIDMap.PutLIDMapping(t.Context(), aliJID, aliPN); err != nil {
		t.Fatal(err)
	}
	joined := &events.JoinedGroup{GroupInfo: types.GroupInfo{JID: groupJID, OwnerJID: aliJID,
		GroupName: types.GroupName{Name: "Aile"}, GroupEphemeral: types.GroupEphemeral{IsEphemeral: true, DisappearingTimer: 86400},
		GroupTopic: types.GroupTopic{Topic: "Bayram"}, GroupAnnounce: types.GroupAnnounce{IsAnnounce: true},
		Participants: []types.GroupParticipant{
			{JID: aliJID, LID: aliJID, PhoneNumber: aliPN, IsSuperAdmin: true},
			{JID: types.JID{User: "905552223344", Server: types.DefaultUserServer}, PhoneNumber: types.JID{User: "905552223344", Server: types.DefaultUserServer},
				LID: types.JID{User: "202", Server: types.HiddenUserServer}, IsAdmin: true},
		}}}
	evs := normalizeJoinedGroup(joined)
	if c := evs[0].(domain.ChatUpdated); c.Title != "Aile" || c.EphemeralTimer != 24*time.Hour {
		t.Fatalf("chat = %+v", c)
	}
	if meta := evs[1].(domain.GroupMetaUpdated); meta.Topic != "Bayram" || !meta.AnnounceOnly || meta.Locked ||
		meta.Owner != "101@lid" || meta.Fields != domain.GroupFieldTopic|domain.GroupFieldOwner|domain.GroupFieldAnnounce|domain.GroupFieldLocked {
		t.Fatalf("meta = %+v", meta)
	}
	m := evs[2].(domain.GroupMembersChanged)
	if !m.Full || len(m.Members) != 2 || m.Members[0].AltJID != "905551112233@s.whatsapp.net" ||
		m.Members[0].Role != domain.RoleSuperAdmin || m.Members[1].AltJID != "202@lid" || m.Members[1].Role != domain.RoleAdmin {
		t.Fatalf("members = %+v", m)
	}

	change := &events.GroupInfo{JID: groupJID, Name: &types.GroupName{Name: "Aile 2"},
		Ephemeral: &types.GroupEphemeral{IsEphemeral: false},
		Join:      []types.JID{aliJID}, Leave: []types.JID{{User: "303", Server: types.HiddenUserServer}},
		Promote: []types.JID{aliJID}, Topic: &types.GroupTopic{TopicDeleted: true}, Locked: &types.GroupLocked{IsLocked: true}}
	evs = a.normalizeGroupInfo(t.Context(), change)
	if len(evs) != 4 || evs[0].(domain.ChatUpdated).Title != "Aile 2" || evs[1].(domain.ChatUpdated).EphemeralTimer != 0 {
		t.Fatalf("events = %+v", evs)
	}
	if meta := evs[2].(domain.GroupMetaUpdated); meta.Fields != domain.GroupFieldTopic|domain.GroupFieldLocked || meta.Topic != "" || !meta.Locked {
		t.Fatalf("meta = %+v", meta)
	}
	mc := evs[3].(domain.GroupMembersChanged)
	if mc.Full || len(mc.Members) != 2 || mc.Members[0].AltJID != "905551112233@s.whatsapp.net" ||
		mc.Members[1].Role != domain.RoleAdmin || len(mc.Left) != 1 || mc.Left[0] != "303@lid" {
		t.Fatalf("members = %+v", mc)
	}
	if evs := a.normalizeGroupInfo(t.Context(), &events.GroupInfo{JID: groupJID}); len(evs) != 0 {
		t.Fatalf("a change tarcin does not track gives nothing: %+v", evs)
	}
}

// TestOnePersonOneChat: messages from a person's number and LID end in
// one direct chat once whatsmeow knows the mapping.
func TestOnePersonOneChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tarcin.db")
	db, err := tstore.Open(t.Context(), path, tstore.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := tstore.Migrate(t.Context(), db, tstore.Migrations(), tstore.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	w, err := tstore.NewWriter(db, tstore.WriterOptions{Applier: tstore.NewEventApplier(tstore.ApplierOptions{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	a := testAdapter(t)
	if !a.handle(t.Context(), w, dmFrom(aliPN, "B1")) { // the mapping is not known yet
		t.Fatal("not stored")
	}
	if err := a.sess.container.LIDMap.PutLIDMapping(t.Context(), aliJID, aliPN); err != nil {
		t.Fatal(err)
	}
	if !a.handle(t.Context(), w, dmFrom(aliJID, "B2")) || !a.handle(t.Context(), w, dmFrom(aliPN, "B3")) {
		t.Fatal("not stored")
	}
	var chats, msgs int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM chats").Scan(&chats); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM messages").Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if chats != 1 || msgs != 3 {
		t.Fatalf("%d chats and %d messages, want one chat with three", chats, msgs)
	}
}

func TestFetchGroupNeedsAConnection(t *testing.T) {
	a := testAdapter(t)
	if _, err := a.FetchGroup(t.Context(), "120363001234567890@g.us"); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("FetchGroup offline = %v", err)
	}
	if _, err := a.FetchGroup(t.Context(), "bad"); err == nil {
		t.Fatal("an invalid JID must be rejected")
	}
}
