// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var _ backend.Forwarder = (*Adapter)(nil)

// Forward implements backend.Forwarder. A message with media goes out as
// it came in, so the recipient downloads the same encrypted file and
// nothing is uploaded again; only its context changes.
func (a *Adapter) Forward(ctx context.Context, req backend.ForwardRequest) (backend.SendResult, []byte, error) {
	to, err := toJID(req.ChatJID)
	if err != nil {
		return backend.SendResult{}, nil, err
	}
	if !a.cli.IsConnected() {
		return backend.SendResult{}, nil, backend.ErrNotConnected
	}
	msg, err := forwardMessage(&req)
	if err != nil {
		return backend.SendResult{}, nil, err
	}
	resp, err := a.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(req.WAID)})
	if err != nil {
		return backend.SendResult{}, nil, fmt.Errorf("sending: %w", err)
	}
	if req.Kind == domain.KindText {
		return backend.SendResult{Timestamp: resp.Timestamp}, nil, nil // text keeps no protocol message
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return backend.SendResult{Timestamp: resp.Timestamp}, nil, nil
	}
	return backend.SendResult{Timestamp: resp.Timestamp}, raw, nil
}

// errNotForwardable is returned for a message whose content cannot be
// sent again.
var errNotForwardable = errors.New("this kind of message cannot be forwarded")

// forwardMessage builds the copy of a message to forward: its content,
// with a context that marks it forwarded and counts how often it was.
// The original's quote and mentions are not carried over.
func forwardMessage(req *backend.ForwardRequest) (*waE2E.Message, error) {
	forwarded := func(old *waE2E.ContextInfo, again bool) *waE2E.ContextInfo {
		score := old.GetForwardingScore()
		if score == 0 && (again || old.GetIsForwarded()) {
			score = 1 // forwarded before, but not counted
		}
		return &waE2E.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(score + 1),
			Expiration: expiration(req.Expiration)}
	}
	if req.Kind == domain.KindText {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(req.Body),
			ContextInfo: forwarded(nil, req.Forwarded)}}, nil
	}
	var src waE2E.Message
	if len(req.Raw) == 0 || proto.Unmarshal(req.Raw, &src) != nil {
		return nil, errors.New("this message's content is not stored here")
	}
	out := &waE2E.Message{}
	switch {
	case src.ImageMessage != nil:
		out.ImageMessage = src.ImageMessage
		out.ImageMessage.ContextInfo = forwarded(src.ImageMessage.ContextInfo, false)
	case src.VideoMessage != nil:
		out.VideoMessage = src.VideoMessage
		out.VideoMessage.ContextInfo = forwarded(src.VideoMessage.ContextInfo, false)
	case src.PtvMessage != nil:
		out.PtvMessage = src.PtvMessage
		out.PtvMessage.ContextInfo = forwarded(src.PtvMessage.ContextInfo, false)
	case src.AudioMessage != nil:
		out.AudioMessage = src.AudioMessage
		out.AudioMessage.ContextInfo = forwarded(src.AudioMessage.ContextInfo, false)
	case src.DocumentMessage != nil:
		out.DocumentMessage = src.DocumentMessage
		out.DocumentMessage.ContextInfo = forwarded(src.DocumentMessage.ContextInfo, false)
	case src.StickerMessage != nil:
		out.StickerMessage = src.StickerMessage
		out.StickerMessage.ContextInfo = forwarded(src.StickerMessage.ContextInfo, false)
	case src.LocationMessage != nil:
		out.LocationMessage = src.LocationMessage
		out.LocationMessage.ContextInfo = forwarded(src.LocationMessage.ContextInfo, false)
	case src.ContactMessage != nil:
		out.ContactMessage = src.ContactMessage
		out.ContactMessage.ContextInfo = forwarded(src.ContactMessage.ContextInfo, false)
	case src.ContactsArrayMessage != nil:
		out.ContactsArrayMessage = src.ContactsArrayMessage
		out.ContactsArrayMessage.ContextInfo = forwarded(src.ContactsArrayMessage.ContextInfo, false)
	default:
		return nil, errNotForwardable
	}
	return out, nil
}
