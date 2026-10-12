// SPDX-License-Identifier: GPL-3.0-or-later

package backend

import (
	"context"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestDurable(t *testing.T) {
	durable := []domain.Event{
		domain.MessageReceived{}, domain.ReceiptReceived{}, domain.ReactionReceived{},
		domain.MessageEdited{}, domain.MessageRevoked{}, domain.IdentityLinked{},
		domain.ContactUpdated{}, domain.ChatUpdated{}, domain.GroupMembersChanged{},
		domain.ChatRead{}, domain.AccountLinked{},
	}
	for _, ev := range durable {
		if !Durable(ev) {
			t.Errorf("%T must be durable", ev)
		}
	}
	for _, ev := range []domain.Event{domain.ChatTyping{}, domain.ConnectionChanged{}, domain.LinkProgress{}} {
		if Durable(ev) {
			t.Errorf("%T must be ephemeral", ev)
		}
	}
}

func TestSinkFunc(t *testing.T) {
	var got int
	var s Sink = SinkFunc(func(_ context.Context, evs []domain.Event) error {
		got = len(evs)
		return nil
	})
	if err := s.Deliver(context.Background(), []domain.Event{domain.ChatTyping{}, domain.ChatTyping{}}); err != nil || got != 2 {
		t.Fatalf("Deliver: err=%v got=%d", err, got)
	}
}
