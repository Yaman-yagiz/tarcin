// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var (
	ts       = time.Unix(1_760_000_000, 0)
	groupJID = types.JID{User: "120363001", Server: types.GroupServer}
	aliJID   = types.JID{User: "101", Server: types.HiddenUserServer}
	aliPN    = types.JID{User: "905551112233", Server: types.DefaultUserServer}
)

func groupMsg(m *waE2E.Message) *events.Message {
	return &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: groupJID, Sender: aliJID, SenderAlt: aliPN, IsGroup: true},
		ID:            "MSG1", Timestamp: ts, PushName: "Ali",
	}, Message: m}
}

func one(t *testing.T, evs []domain.Event) domain.Event {
	t.Helper()
	if len(evs) != 1 {
		t.Fatalf("events = %+v, want one", evs)
	}
	return evs[0]
}

func received(t *testing.T, evs []domain.Event) domain.IncomingMessage {
	t.Helper()
	m, ok := one(t, evs).(domain.MessageReceived)
	if !ok {
		t.Fatalf("event = %T", evs[0])
	}
	return m.Message
}

func TestNormalizeKinds(t *testing.T) {
	a := testAdapter(t)
	for _, c := range []struct {
		name  string
		msg   *waE2E.Message
		kind  domain.MessageKind
		body  string
		media *domain.Media
	}{
		{"text", &waE2E.Message{Conversation: proto.String("merhaba")}, domain.KindText, "merhaba", nil},
		{"extended", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("link https://x")}}, domain.KindText, "link https://x", nil},
		{"image", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("kedi"), Mimetype: proto.String("image/jpeg"),
			FileLength: proto.Uint64(2048), Width: proto.Uint32(640), Height: proto.Uint32(480)}},
			domain.KindImage, "kedi", &domain.Media{MIME: "image/jpeg", Size: 2048, Width: 640, Height: 480}},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Seconds: proto.Uint32(42), Mimetype: proto.String("video/mp4")}},
			domain.KindVideo, "", &domain.Media{MIME: "video/mp4", Duration: 42 * time.Second}},
		{"voice", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Seconds: proto.Uint32(7), PTT: proto.Bool(true)}},
			domain.KindAudio, "", &domain.Media{Duration: 7 * time.Second}},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("rapor.pdf"),
			Caption: proto.String("son hali"), FileLength: proto.Uint64(1 << 20)}},
			domain.KindDocument, "son hali", &domain.Media{FileName: "rapor.pdf", Size: 1 << 20}},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")}},
			domain.KindSticker, "", &domain.Media{MIME: "image/webp"}},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: proto.String("Galata"),
			Address: proto.String("Beyoğlu"), DegreesLatitude: proto.Float64(41.0256)}}, domain.KindLocation, "Galata · Beyoğlu", nil},
		{"coordinates", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(41.0256),
			DegreesLongitude: proto.Float64(28.9741)}}, domain.KindLocation, "41.02560, 28.97410", nil},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ayşe")}}, domain.KindContact, "Ayşe", nil},
		{"poll", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Nerede?"),
			Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Kadıköy")}, {OptionName: proto.String("Beşiktaş")}}}},
			domain.KindPoll, "Nerede?\n• Kadıköy\n• Beşiktaş", nil},
		{"unknown", &waE2E.Message{EventMessage: &waE2E.EventMessage{Name: proto.String("x")}}, domain.KindUnsupported, "", nil},
	} {
		in := received(t, a.normalizeMessage(groupMsg(c.msg), domain.SourceLive))
		if in.Kind != c.kind || in.Body != c.body {
			t.Errorf("%s: kind %v body %q", c.name, in.Kind, in.Body)
		}
		if (c.media == nil) != (in.Media == nil) || (c.media != nil && *in.Media != *c.media) {
			t.Errorf("%s: media %+v, want %+v", c.name, in.Media, c.media)
		}
		if (in.Raw == nil) != (c.kind == domain.KindText) {
			t.Errorf("%s: raw kept = %v; only non-text messages keep it", c.name, in.Raw != nil)
		}
		if in.ChatJID != "120363001@g.us" || in.SenderJID != "101@lid" || in.SenderAlt != "905551112233@s.whatsapp.net" ||
			in.PushName != "Ali" || in.WAID != "MSG1" || !in.Timestamp.Equal(ts) {
			t.Errorf("%s: envelope = %+v", c.name, in)
		}
	}
}

func TestNormalizeContextAndFlags(t *testing.T) {
	a := testAdapter(t)
	a.sess.device.ID = &types.JID{User: "905550000000", Server: types.DefaultUserServer}
	a.sess.device.LID = types.JID{User: "100", Server: types.HiddenUserServer}
	ctx := &waE2E.ContextInfo{StanzaID: proto.String("QUOTED"), IsForwarded: proto.Bool(true), Expiration: proto.Uint32(86400),
		MentionedJID: []string{"200@lid", "100@lid"}}
	evt := groupMsg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("@you bak"), ContextInfo: ctx}})
	evt.IsViewOnce = true
	in := received(t, a.normalizeMessage(evt, domain.SourceHistory))
	want := domain.FlagForwarded | domain.FlagViewOnce | domain.FlagEphemeral | domain.FlagMentionsMe
	if in.QuotedWAID != "QUOTED" || in.Flags != want {
		t.Fatalf("quoted %q flags %b, want %b", in.QuotedWAID, in.Flags, want)
	}
	if !slices.Equal(in.Mentions, []domain.JID{"200@lid", "100@lid"}) {
		t.Fatalf("mentions = %v", in.Mentions)
	}
	ctx.MentionedJID = []string{"905551112233@s.whatsapp.net"}
	if in := received(t, a.normalizeMessage(evt, domain.SourceLive)); in.Flags.Has(domain.FlagMentionsMe) {
		t.Fatal("a mention of someone else is not a mention of me")
	}
	ev := a.normalizeMessage(evt, domain.SourceHistory)[0].(domain.MessageReceived)
	if ev.Source != domain.SourceHistory {
		t.Fatal("the source is passed through")
	}
}

func TestNormalizeEditsRevokesReactions(t *testing.T) {
	a := testAdapter(t)
	key := &waCommon.MessageKey{ID: proto.String("TARGET")}
	edit := groupMsg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key,
		TimestampMS: proto.Int64(ts.Add(time.Minute).UnixMilli()), EditedMessage: &waE2E.Message{Conversation: proto.String("düzeltildi")}}})
	if e := one(t, a.normalizeMessage(edit, domain.SourceLive)).(domain.MessageEdited); e.TargetWAID != "TARGET" ||
		e.Body != "düzeltildi" || !e.EditedAt.Equal(ts.Add(time.Minute)) || e.ChatJID != "120363001@g.us" {
		t.Fatalf("edit = %+v", e)
	}
	if e := one(t, a.normalizeMessage(edit, domain.SourceLive)).(domain.MessageEdited); e.Actor != "101@lid" {
		t.Fatalf("an edit names who made it: %+v", e)
	}
	revoke := groupMsg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}})
	if e := one(t, a.normalizeMessage(revoke, domain.SourceLive)).(domain.MessageRevoked); e.TargetWAID != "TARGET" ||
		!e.RevokedAt.Equal(ts) || e.Actor != "101@lid" || e.ByAdmin {
		t.Fatalf("revoke = %+v", e)
	}
	revoke.Info.Edit = types.EditAttributeAdminRevoke
	if e := one(t, a.normalizeMessage(revoke, domain.SourceLive)).(domain.MessageRevoked); !e.ByAdmin {
		t.Fatalf("an admin revoke is marked: %+v", e)
	}
	react := groupMsg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("+1"),
		SenderTimestampMS: proto.Int64(ts.Add(2 * time.Second).UnixMilli())}})
	if e := one(t, a.normalizeMessage(react, domain.SourceLive)).(domain.ReactionReceived); e.TargetWAID != "TARGET" ||
		e.Emoji != "+1" || e.SenderJID != "101@lid" || !e.Timestamp.Equal(ts.Add(2*time.Second)) {
		t.Fatalf("reaction = %+v", e)
	}
	ephemeral := groupMsg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
		EphemeralExpiration: proto.Uint32(604800)}})
	if e := one(t, a.normalizeMessage(ephemeral, domain.SourceLive)).(domain.ChatUpdated); e.EphemeralTimer != 7*24*time.Hour ||
		!e.Fields.Has(domain.ChatFieldEphemeral) {
		t.Fatalf("ephemeral = %+v", e)
	}
	for name, m := range map[string]*waE2E.Message{
		"history notice": {ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum()}},
		"pin":            {PinInChatMessage: &waE2E.PinInChatMessage{Key: key}},
		"nil":            nil,
	} {
		if evs := a.normalizeMessage(groupMsg(m), domain.SourceLive); len(evs) != 0 {
			t.Errorf("%s: %+v", name, evs)
		}
	}
}

func TestNormalizeChats(t *testing.T) {
	a := testAdapter(t)
	status := groupMsg(&waE2E.Message{Conversation: proto.String("story")})
	status.Info.Chat = types.StatusBroadcastJID
	if evs := a.normalizeMessage(status, domain.SourceLive); evs != nil {
		t.Fatal("statuses are not stored")
	}
	list := groupMsg(&waE2E.Message{Conversation: proto.String("to my list")})
	list.Info.Chat = types.JID{User: "1234", Server: types.BroadcastServer}
	if in := received(t, a.normalizeMessage(list, domain.SourceLive)); in.ChatJID != "101@lid" {
		t.Fatalf("a broadcast-list message belongs in the sender's direct chat: %s", in.ChatJID)
	}
	own := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: aliJID, Sender: aliJID, IsFromMe: true},
		ID: "OWN", Timestamp: ts}, Message: &waE2E.Message{Conversation: proto.String("from my phone")}}
	if in := received(t, a.normalizeMessage(own, domain.SourceLive)); in.ChatJID != "101@lid" || !in.FromMe {
		t.Fatalf("own message = %+v", in)
	}
	und := &events.UndecryptableMessage{Info: groupMsg(nil).Info}
	if in := received(t, normalizeUndecryptable(und)); in.Kind != domain.KindUnsupported || in.WAID != "MSG1" {
		t.Fatalf("undecryptable = %+v", in)
	}
	und.Info.Chat = types.StatusBroadcastJID
	if normalizeUndecryptable(und) != nil {
		t.Fatal("undecryptable statuses are not stored")
	}
}

func TestHandlerDeliversMessages(t *testing.T) {
	a, sink := testAdapter(t), &recordingSink{}
	if !a.handle(t.Context(), sink, groupMsg(&waE2E.Message{Conversation: proto.String("hi")})) || len(sink.events) != 2 {
		t.Fatalf("events = %+v; the sender's link, then the message", sink.events)
	}
	if !a.handle(t.Context(), sink, groupMsg(&waE2E.Message{PinInChatMessage: &waE2E.PinInChatMessage{}})) || len(sink.events) != 2 {
		t.Fatal("a message with nothing to store is acknowledged without a delivery")
	}
	sink.fail = true
	if a.handle(t.Context(), sink, groupMsg(&waE2E.Message{Conversation: proto.String("hi")})) {
		t.Fatal("a message that was not stored must not be acknowledged")
	}
	if !strings.Contains(received(t, a.normalizeMessage(groupMsg(&waE2E.Message{Conversation: proto.String("ş")}), 1)).Body, "ş") {
		t.Fatal("text is kept as it is")
	}
}

func TestTextMessage(t *testing.T) {
	plain, err := textMessage(&backend.SendRequest{Body: "merhaba"})
	if err != nil || plain.GetConversation() != "merhaba" || plain.ExtendedTextMessage != nil {
		t.Fatalf("plain = %v, %v", plain, err)
	}
	reply, err := textMessage(&backend.SendRequest{Body: "evet", QuotedWAID: "3EB0Q", QuotedSender: "101@lid", QuotedText: "geliyor musun?"})
	if err != nil {
		t.Fatal(err)
	}
	x := reply.GetExtendedTextMessage()
	if x.GetText() != "evet" || x.GetContextInfo().GetStanzaID() != "3EB0Q" || x.GetContextInfo().GetParticipant() != "101@lid" ||
		x.GetContextInfo().GetQuotedMessage().GetConversation() != "geliyor musun?" {
		t.Fatalf("reply = %v", reply)
	}
	// The reply normalises back to the same quote.
	if c := content(reply); c.body != "evet" || c.ctx.GetStanzaID() != "3EB0Q" {
		t.Fatalf("content = %+v", c)
	}
	bare, err := textMessage(&backend.SendRequest{Body: "x", QuotedWAID: "Q"})
	if err != nil || bare.GetExtendedTextMessage().GetContextInfo().Participant != nil {
		t.Fatalf("an unknown quote names only its ID: %v, %v", bare, err)
	}
	if _, err := textMessage(&backend.SendRequest{Body: "x", QuotedWAID: "Q", QuotedSender: "bad"}); err == nil {
		t.Fatal("an invalid sender must be rejected")
	}
}

func TestTextMessageMentions(t *testing.T) {
	msg, err := textMessage(&backend.SendRequest{Body: "@101 bak", Mentions: []domain.JID{"101@lid"}})
	if err != nil {
		t.Fatal(err)
	}
	x := msg.GetExtendedTextMessage()
	if x.GetText() != "@101 bak" || !slices.Equal(x.GetContextInfo().GetMentionedJID(), []string{"101@lid"}) || x.GetContextInfo().StanzaID != nil {
		t.Fatalf("message = %v", msg)
	}
	if m := mentioned(x.GetContextInfo()); !slices.Equal(m, []domain.JID{"101@lid"}) {
		t.Fatalf("mentions read back = %v", m)
	}
	if _, err := textMessage(&backend.SendRequest{Body: "x", Mentions: []domain.JID{"bad"}}); err == nil {
		t.Fatal("an invalid mention must be rejected")
	}
}

func TestDisappearingTimers(t *testing.T) {
	a := testAdapter(t)
	ctx := &waE2E.ContextInfo{Expiration: proto.Uint32(86400)}
	evt := groupMsg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("x"), ContextInfo: ctx}})
	if in := received(t, a.normalizeMessage(evt, domain.SourceLive)); in.Expiration != 24*time.Hour || !in.Flags.Has(domain.FlagEphemeral) {
		t.Fatalf("expiration %v, flags %b", in.Expiration, in.Flags)
	}
	msg, err := textMessage(&backend.SendRequest{Body: "x", Expiration: 7 * 24 * time.Hour})
	if err != nil || msg.GetExtendedTextMessage().GetContextInfo().GetExpiration() != 604800 {
		t.Fatalf("send = %v, %v", msg, err)
	}
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}
	setExpiration(img, time.Hour)
	if img.GetImageMessage().GetContextInfo().GetExpiration() != 3600 {
		t.Fatalf("media = %v", img)
	}
}
