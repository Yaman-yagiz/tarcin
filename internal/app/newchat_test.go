// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
)

func TestPhoneNumber(t *testing.T) {
	for _, tt := range []struct {
		in, digits string
		isNumber   bool
		err        string
	}{
		{"+90 555 123 45 67", "905551234567", true, ""},
		{"+1 (415) 555-0100", "14155550100", true, ""},
		{"0090.555.123.45.67", "905551234567", true, ""},
		{"905551234567", "905551234567", true, ""},
		{"0555 123 45 67", "", true, "country code"},
		{"+12 34", "", true, "international format"},
		{"+1234567890123456", "", true, "international format"},
		{"Ayşe", "", false, ""},
		{"Ali 2", "", false, ""},
		{"+", "", false, ""},
		{"", "", false, ""},
	} {
		digits, isNumber, err := phoneNumber(tt.in)
		if digits != tt.digits || isNumber != tt.isNumber || (err == nil) != (tt.err == "") ||
			(err != nil && !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("phoneNumber(%q) = %q, %t, %v; want %q, %t, %q", tt.in, digits, isNumber, err, tt.digits, tt.isNumber, tt.err)
		}
	}
}

// numberBackend resolves the numbers it knows to LIDs.
type numberBackend struct {
	backend.Backend // never called
	known           map[string]domain.JID
	asked           []string
}

func (b *numberBackend) ResolveNumber(_ context.Context, phone string) (domain.JID, []domain.Event, error) {
	b.asked = append(b.asked, phone)
	lid, ok := b.known[phone]
	if !ok {
		return "", nil, backend.ErrNotOnWhatsApp
	}
	return lid, []domain.Event{domain.IdentityLinked{PN: domain.JID(phone + "@s.whatsapp.net"), LID: lid}}, nil
}

func TestStartChat(t *testing.T) {
	nb := &numberBackend{known: map[string]domain.JID{"905551112233": "77@lid"}}
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: "1@lid", Backend: nb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close(context.Background()) })
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	if err := p.Deliver(t.Context(), []domain.Event{
		domain.ContactUpdated{JID: "10@lid", ContactName: "Ayşe Kaya"},
		domain.ContactUpdated{JID: "11@lid", ContactName: "Ayşe Kaya Demir"},
		domain.ContactUpdated{JID: "12@lid", ContactName: "Mert Şahin"},
	}); err != nil {
		t.Fatal(err)
	}
	start := func(query string) (ui.ChatStartedMsg, error) {
		t.Helper()
		switch msg := p.startChat(query)().(type) {
		case ui.ChatStartedMsg:
			return msg, nil
		case composer.ErrorMsg:
			return ui.ChatStartedMsg{}, msg.Err
		default:
			t.Fatalf("startChat(%q) = %#v", query, msg)
		}
		return ui.ChatStartedMsg{}, nil
	}

	// A contact without a chat: the chat is created, titled with the name.
	got, err := start("mert")
	if err != nil || got.Row.Title != "Mert Şahin" || got.Row.ID == 0 {
		t.Fatalf("mert = %+v, %v", got, err)
	}
	if id, ok, _ := p.reader.ChatID(t.Context(), "12@lid"); !ok || id != got.Row.ID {
		t.Fatalf("the chat with Mert is %d, %t; the row says %d", id, ok, got.Row.ID)
	}
	// Again: the same chat, not a second one.
	again, err := start("Mert Şahin")
	if err != nil || again.Row.ID != got.Row.ID {
		t.Fatalf("again = %+v, %v", again, err)
	}
	// A name that is a whole contact name picks it among longer ones.
	if got, err := start("ayse kaya"); err != nil || got.Row.Title != "Ayşe Kaya" {
		t.Fatalf("exact = %+v, %v", got, err)
	}
	if _, err := start("ayş"); err == nil || !strings.Contains(err.Error(), "Ayşe Kaya, Ayşe Kaya Demir") {
		t.Fatalf("an ambiguous name must list the matches: %v", err)
	}
	if _, err := start("zeynep"); err == nil || !strings.Contains(err.Error(), "no contact matches") {
		t.Fatalf("no match: %v", err)
	}

	// A number on WhatsApp: asked once, linked to its LID, chat by LID.
	byNumber, err := start("+90 555 111 22 33")
	if err != nil || byNumber.Row.Title != "+905551112233" {
		t.Fatalf("number = %+v, %v", byNumber, err)
	}
	if id, ok, _ := p.reader.ChatID(t.Context(), "905551112233@s.whatsapp.net"); !ok || id != byNumber.Row.ID {
		t.Fatal("the number and its LID must address the same chat")
	}
	if c, _ := p.chatOf(t.Context(), byNumber.Row.ID); c.JID != "77@lid" {
		t.Fatalf("the chat is keyed by %s, want the LID", c.JID)
	}
	if _, err := start("+90 555 999 99 99"); err == nil || !strings.Contains(err.Error(), "+905559999999 is not on WhatsApp") {
		t.Fatalf("unknown number: %v", err)
	}
	if len(nb.asked) != 2 {
		t.Fatalf("asked the server %d times for 2 numbers", len(nb.asked))
	}
	if _, err := start("0555 111 22 33"); err == nil || len(nb.asked) != 2 {
		t.Fatalf("a number without its country code must not be looked up: %v", err)
	}

	// Completion lists the contacts that match.
	msg, ok := p.contacts("ay")().(ui.ContactsMsg)
	if !ok || msg.Query != "ay" || strings.Join(msg.Names, "|") != "Ayşe Kaya|Ayşe Kaya Demir" {
		t.Fatalf("contacts = %#v", msg)
	}
}

func TestStartChatByNumberNeedsABackend(t *testing.T) {
	p, _ := newTestPipeline(t, fake.Person{LID: "1@lid"})
	msg, ok := p.startChat("+90 555 111 22 33")().(composer.ErrorMsg)
	if !ok || !strings.Contains(msg.Err.Error(), "needs a WhatsApp connection") {
		t.Fatalf("got %#v", msg)
	}
}
