// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Normalisation turns whatsmeow's message events into domain events
// (docs/architecture/event-pipeline.md §3.1). It is pure: no network
// calls, no store lookups, so it can run inside an event handler. Text is
// kept raw; it is sanitized when it is rendered.

// normalizeMessage converts a message event. Edits, revokes and reactions
// travel as messages too and become their own events. Protocol messages
// tarcin has no use for, status updates and a message without an ID,
// which cannot be stored, give no events.
func (a *Adapter) normalizeMessage(evt *events.Message, src domain.Source) []domain.Event {
	info := &evt.Info
	if info.Chat == types.StatusBroadcastJID || evt.Message == nil || info.ID == "" {
		return nil // statuses are not stored
	}
	chat := chatOf(&info.MessageSource)
	msg := evt.Message
	if pm := msg.GetProtocolMessage(); pm != nil {
		return a.protocol(chat, info, pm)
	}
	if r := msg.GetReactionMessage(); r != nil {
		return []domain.Event{reaction(chat, info, r)}
	}
	if msg.GetPinInChatMessage() != nil || msg.GetKeepInChatMessage() != nil || msg.GetPollUpdateMessage() != nil ||
		msg.GetEncReactionMessage() != nil {
		return nil // not shown yet; the message they refer to stays as it is
	}
	c := content(msg)
	in := domain.IncomingMessage{
		ChatJID:    chat,
		SenderJID:  fromJID(info.Sender),
		SenderAlt:  fromJID(info.SenderAlt),
		PushName:   info.PushName,
		WAID:       domain.WAMessageID(info.ID),
		FromMe:     info.IsFromMe,
		Timestamp:  info.Timestamp,
		Kind:       c.kind,
		Body:       c.body,
		QuotedWAID: domain.WAMessageID(c.ctx.GetStanzaID()),
		Flags:      a.flags(evt, c.ctx),
		Media:      c.media,
		Mentions:   mentioned(c.ctx),
		Expiration: time.Duration(c.ctx.GetExpiration()) * time.Second,
	}
	if c.kind != domain.KindText {
		// Kept for media downloads and for kinds a later version
		// understands; text keeps none (data-model.md §3).
		in.Raw, _ = proto.Marshal(msg)
	}
	return []domain.Event{domain.MessageReceived{Message: in, Source: src}}
}

// chatOf returns the chat a message belongs to. A message sent to a
// broadcast list shows in the direct chat with its sender.
func chatOf(src *types.MessageSource) domain.JID {
	if src.IsIncomingBroadcast() && !src.IsFromMe {
		return fromJID(src.Sender)
	}
	return fromJID(src.Chat)
}

func (a *Adapter) protocol(chat domain.JID, info *types.MessageInfo, pm *waE2E.ProtocolMessage) []domain.Event {
	target := domain.WAMessageID(pm.GetKey().GetID())
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		return []domain.Event{domain.MessageRevoked{ChatJID: chat, TargetWAID: target, RevokedAt: info.Timestamp,
			Actor: fromJID(info.Sender), ByAdmin: info.Edit == types.EditAttributeAdminRevoke}}
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		at := info.Timestamp
		if ms := pm.GetTimestampMS(); ms > 0 {
			at = time.UnixMilli(ms)
		}
		return []domain.Event{domain.MessageEdited{ChatJID: chat, TargetWAID: target,
			Body: content(pm.GetEditedMessage()).body, EditedAt: at, Actor: fromJID(info.Sender)}}
	case waE2E.ProtocolMessage_EPHEMERAL_SETTING:
		return []domain.Event{domain.ChatUpdated{ChatJID: chat, Fields: domain.ChatFieldEphemeral,
			EphemeralTimer: time.Duration(pm.GetEphemeralExpiration()) * time.Second}}
	}
	return nil // history notifications, key shares and the like are whatsmeow's business
}

func reaction(chat domain.JID, info *types.MessageInfo, r *waE2E.ReactionMessage) domain.Event {
	at := info.Timestamp
	if ms := r.GetSenderTimestampMS(); ms > 0 {
		at = time.UnixMilli(ms)
	}
	return domain.ReactionReceived{ChatJID: chat, TargetWAID: domain.WAMessageID(r.GetKey().GetID()),
		SenderJID: fromJID(info.Sender), Emoji: r.GetText(), Timestamp: at}
}

// flags derives the message flags: forwarded, view once, ephemeral and a
// mention of this account.
func (a *Adapter) flags(evt *events.Message, ctx *waE2E.ContextInfo) domain.MessageFlags {
	var f domain.MessageFlags
	if ctx.GetIsForwarded() {
		f |= domain.FlagForwarded
	}
	if evt.IsViewOnce {
		f |= domain.FlagViewOnce
	}
	if evt.IsEphemeral || ctx.GetExpiration() > 0 {
		f |= domain.FlagEphemeral
	}
	if mentions := ctx.GetMentionedJID(); len(mentions) > 0 {
		pn, lid := a.sess.Account()
		if (!pn.IsZero() && slices.Contains(mentions, string(pn))) || (!lid.IsZero() && slices.Contains(mentions, string(lid))) {
			f |= domain.FlagMentionsMe
		}
	}
	return f
}

// mentioned returns the people a message mentions, skipping JIDs that do
// not parse.
func mentioned(ctx *waE2E.ContextInfo) []domain.JID {
	var out []domain.JID
	for _, s := range ctx.GetMentionedJID() {
		if jid, err := types.ParseJID(s); err == nil && !jid.IsEmpty() {
			out = append(out, fromJID(jid))
		}
	}
	return out
}

// messageContent is what a message shows.
type messageContent struct {
	kind  domain.MessageKind
	body  string // text, caption or a short description
	media *domain.Media
	ctx   *waE2E.ContextInfo
}

// content extracts kind, text and media of a message. Unknown kinds get
// an unsupported placeholder; their payload is kept by the caller.
func content(m *waE2E.Message) messageContent {
	switch {
	case m == nil:
		return messageContent{kind: domain.KindUnsupported}
	case m.Conversation != nil:
		return messageContent{kind: domain.KindText, body: m.GetConversation()}
	case m.ExtendedTextMessage != nil:
		x := m.GetExtendedTextMessage()
		return messageContent{kind: domain.KindText, body: x.GetText(), ctx: x.GetContextInfo()}
	case m.ImageMessage != nil:
		x := m.GetImageMessage()
		return messageContent{kind: domain.KindImage, body: x.GetCaption(), ctx: x.GetContextInfo(), media: &domain.Media{
			MIME: x.GetMimetype(), Size: size(x.GetFileLength()), Width: int(x.GetWidth()), Height: int(x.GetHeight()),
		}}
	case m.VideoMessage != nil || m.PtvMessage != nil:
		x := m.GetVideoMessage()
		if x == nil {
			x = m.GetPtvMessage()
		}
		return messageContent{kind: domain.KindVideo, body: x.GetCaption(), ctx: x.GetContextInfo(), media: &domain.Media{
			MIME: x.GetMimetype(), Size: size(x.GetFileLength()), Width: int(x.GetWidth()), Height: int(x.GetHeight()),
			Duration: time.Duration(x.GetSeconds()) * time.Second,
		}}
	case m.AudioMessage != nil:
		x := m.GetAudioMessage()
		return messageContent{kind: domain.KindAudio, ctx: x.GetContextInfo(), media: &domain.Media{
			MIME: x.GetMimetype(), Size: size(x.GetFileLength()), Duration: time.Duration(x.GetSeconds()) * time.Second,
		}}
	case m.DocumentMessage != nil:
		x := m.GetDocumentMessage()
		name := x.GetFileName()
		if name == "" {
			name = x.GetTitle()
		}
		return messageContent{kind: domain.KindDocument, body: x.GetCaption(), ctx: x.GetContextInfo(), media: &domain.Media{
			MIME: x.GetMimetype(), Size: size(x.GetFileLength()), FileName: name,
		}}
	case m.StickerMessage != nil:
		x := m.GetStickerMessage()
		return messageContent{kind: domain.KindSticker, ctx: x.GetContextInfo(), media: &domain.Media{
			MIME: x.GetMimetype(), Size: size(x.GetFileLength()), Width: int(x.GetWidth()), Height: int(x.GetHeight()),
		}}
	case m.LocationMessage != nil:
		x := m.GetLocationMessage()
		return messageContent{kind: domain.KindLocation, ctx: x.GetContextInfo(),
			body: place(x.GetName(), x.GetAddress(), x.GetDegreesLatitude(), x.GetDegreesLongitude())}
	case m.LiveLocationMessage != nil:
		x := m.GetLiveLocationMessage()
		body := "Live location"
		if c := x.GetCaption(); c != "" {
			body += ": " + c
		}
		return messageContent{kind: domain.KindLocation, body: body, ctx: x.GetContextInfo()}
	case m.ContactMessage != nil:
		x := m.GetContactMessage()
		return messageContent{kind: domain.KindContact, body: x.GetDisplayName(), ctx: x.GetContextInfo()}
	case m.ContactsArrayMessage != nil:
		x := m.GetContactsArrayMessage()
		names := make([]string, 0, len(x.GetContacts()))
		for _, c := range x.GetContacts() {
			names = append(names, c.GetDisplayName())
		}
		return messageContent{kind: domain.KindContact, body: strings.Join(names, ", "), ctx: x.GetContextInfo()}
	}
	if p := poll(m); p != nil {
		lines := []string{p.GetName()}
		for _, o := range p.GetOptions() {
			lines = append(lines, "• "+o.GetOptionName())
		}
		return messageContent{kind: domain.KindPoll, body: strings.Join(lines, "\n"), ctx: p.GetContextInfo()}
	}
	return messageContent{kind: domain.KindUnsupported}
}

func poll(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{
		m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(), m.GetPollCreationMessageV5(),
	} {
		if p != nil {
			return p
		}
	}
	return nil
}

func place(name, address string, lat, lon float64) string {
	parts := make([]string, 0, 2)
	for _, p := range []string{name, address} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%.5f, %.5f", lat, lon)
	}
	return strings.Join(parts, " · ")
}

// size converts a protocol file length, clamping values a hostile sender
// could make overflow.
func size(n uint64) int64 {
	if n > 1<<62 {
		return 1 << 62
	}
	return int64(n)
}

// normalizeUndecryptable turns a message that could not be decrypted
// into a placeholder. whatsmeow asks the sender (and the phone) for it
// again; when it arrives, the placeholder is replaced (data-model.md §3).
func normalizeUndecryptable(evt *events.UndecryptableMessage) []domain.Event {
	info := &evt.Info
	if info.Chat == types.StatusBroadcastJID {
		return nil
	}
	return []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
		ChatJID: chatOf(&info.MessageSource), SenderJID: fromJID(info.Sender), SenderAlt: fromJID(info.SenderAlt),
		PushName: info.PushName, WAID: domain.WAMessageID(info.ID), FromMe: info.IsFromMe,
		Timestamp: info.Timestamp, Kind: domain.KindUnsupported,
	}}}
}
