// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// checkEvents fails on an event that breaks what normalisation promises
// the store: a message has an ID, a chat and a kind, and text keeps no
// protocol message.
func checkEvents(t *testing.T, evs []domain.Event) {
	t.Helper()
	for _, ev := range evs {
		m, ok := ev.(domain.MessageReceived)
		if !ok {
			continue
		}
		switch in := m.Message; {
		case in.WAID == "" || in.ChatJID.IsZero():
			t.Fatalf("a message without an ID or a chat: %+v", in)
		case in.Kind == 0:
			t.Fatalf("a message without a kind: %+v", in)
		case in.Kind == domain.KindText && len(in.Raw) > 0:
			t.Fatal("a text message keeps its protocol message")
		case in.Media != nil && !in.Kind.HasMedia():
			t.Fatalf("media on a %s message", in.Kind)
		}
	}
}

func messageSeeds() []*waE2E.Message {
	ctx := &waE2E.ContextInfo{StanzaID: proto.String("Q"), MentionedJID: []string{"101@lid", "bad jid"}, IsForwarded: proto.Bool(true)}
	return []*waE2E.Message{
		{Conversation: proto.String("selam")},
		{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("@101 bak"), ContextInfo: ctx}},
		{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(1 << 63)}},
		{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("../../etc/passwd"), Mimetype: proto.String("application/pdf")}},
		{ReactionMessage: &waE2E.ReactionMessage{Key: &waCommon.MessageKey{ID: proto.String("T")}, Text: proto.String("👍")}},
		{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("T")}}},
		{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("T")},
			EditedMessage: &waE2E.Message{Conversation: proto.String("yeni")}}},
		{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("kaybolan")}}},
		{ViewOnceMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}}},
		{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(41), DegreesLongitude: proto.Float64(29), Name: proto.String("\x1b[2J")}},
		{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("?"), Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("a")}}}},
	}
}

// FuzzNormalizeMessage normalises any protocol message, as whatsmeow
// delivers it after unwrapping, in a direct chat or a group.
func FuzzNormalizeMessage(f *testing.F) {
	for _, m := range messageSeeds() {
		b, err := proto.Marshal(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b, false, false)
		f.Add(b, true, true)
	}
	a := testAdapter(f)
	f.Fuzz(func(t *testing.T, data []byte, fromMe, group bool) {
		var msg waE2E.Message
		if proto.Unmarshal(data, &msg) != nil {
			return
		}
		chat := aliPN
		if group {
			chat = groupJID
		}
		evt := &events.Message{Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: aliPN, IsFromMe: fromMe, IsGroup: group},
			ID:            "FUZZ", Timestamp: ts,
		}, Message: &msg}
		checkEvents(t, a.normalizeMessage(evt.UnwrapRaw(), domain.SourceLive))
	})
}

// FuzzHistory turns any history sync into store events: whatsmeow parses
// each web message, and the adapter normalises it and the chats' state.
func FuzzHistory(f *testing.F) {
	var msgs []*waHistorySync.HistorySyncMsg
	for i, m := range messageSeeds() {
		id := string(rune('A' + i))
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{RemoteJID: proto.String(groupJID.String()), ID: proto.String(id), Participant: proto.String(aliPN.String())},
			Message:          m,
			MessageTimestamp: proto.Uint64(1_760_000_000), // ts
		}})
	}
	for _, typ := range []waHistorySync.HistorySync_HistorySyncType{waHistorySync.HistorySync_INITIAL_BOOTSTRAP, waHistorySync.HistorySync_ON_DEMAND} {
		b, err := proto.Marshal(&waHistorySync.HistorySync{
			SyncType: typ.Enum(),
			Conversations: []*waHistorySync.Conversation{
				{ID: proto.String(groupJID.String()), Name: proto.String("Aile"), UnreadCount: proto.Uint32(3), Messages: msgs,
					MuteEndTime: proto.Uint64(1 << 62), EphemeralExpiration: proto.Uint32(86400)},
				{ID: proto.String("not a jid")},
				{ID: proto.String(aliPN.String()), EndOfHistoryTransfer: proto.Bool(true)},
			},
			Pushnames: []*waHistorySync.Pushname{{ID: proto.String(aliPN.String()), Pushname: proto.String("Ali")}, {ID: proto.String("x"), Pushname: proto.String("-")}},
			PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
				{PnJID: proto.String(aliPN.String()), LidJID: proto.String(aliJID.String())},
				{PnJID: proto.String(aliJID.String()), LidJID: proto.String(aliPN.String())},
			},
		})
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	a := testAdapter(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var h waHistorySync.HistorySync
		if proto.Unmarshal(data, &h) != nil {
			return
		}
		for _, chunk := range a.historyEvents(t.Context(), &h) {
			if len(chunk) == 0 {
				t.Fatal("an empty chunk")
			}
			checkEvents(t, chunk)
		}
	})
}
