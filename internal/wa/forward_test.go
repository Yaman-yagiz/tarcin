// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestForwardText(t *testing.T) {
	msg, err := forwardMessage(&backend.ForwardRequest{Kind: domain.KindText, Body: "selam", Expiration: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	x := msg.GetExtendedTextMessage()
	ctx := x.GetContextInfo()
	if x.GetText() != "selam" || !ctx.GetIsForwarded() || ctx.GetForwardingScore() != 1 || ctx.GetExpiration() != 86400 {
		t.Fatalf("message = %v", msg)
	}
	again, err := forwardMessage(&backend.ForwardRequest{Kind: domain.KindText, Body: "selam", Forwarded: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetExtendedTextMessage().GetContextInfo().GetForwardingScore() != 2 {
		t.Fatal("a forwarded text forwarded again counts twice")
	}
}

func TestForwardMediaKeepsTheFile(t *testing.T) {
	src := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("photo"), URL: proto.String("https://mmg.example/x"), DirectPath: proto.String("/x"),
		MediaKey: []byte("key"), FileSHA256: []byte("sha"), FileEncSHA256: []byte("enc"), FileLength: proto.Uint64(10),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("QUOTED"), MentionedJID: []string{"1@lid"},
			IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(4)},
	}}
	raw, err := proto.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := forwardMessage(&backend.ForwardRequest{Kind: domain.KindImage, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	img := msg.GetImageMessage()
	if img.GetURL() != "https://mmg.example/x" || string(img.GetMediaKey()) != "key" || img.GetCaption() != "photo" {
		t.Fatalf("image = %v; the file's location and key go with the copy", img)
	}
	ctx := img.GetContextInfo()
	if !ctx.GetIsForwarded() || ctx.GetForwardingScore() != 5 || ctx.GetStanzaID() != "" || len(ctx.GetMentionedJID()) != 0 {
		t.Fatalf("context = %v; the copy counts one more forward and drops the quote and mentions", ctx)
	}
}

func TestForwardRefusesWhatItCannotSend(t *testing.T) {
	if _, err := forwardMessage(&backend.ForwardRequest{Kind: domain.KindImage}); err == nil {
		t.Fatal("media without its stored message cannot be forwarded")
	}
	raw, err := proto.Marshal(&waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forwardMessage(&backend.ForwardRequest{Kind: domain.KindLocation, Raw: raw}); !errors.Is(err, errNotForwardable) {
		t.Fatalf("err = %v; a live location is not forwarded", err)
	}
}
