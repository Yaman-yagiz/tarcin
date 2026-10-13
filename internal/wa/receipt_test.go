// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestNormalizeReceipts(t *testing.T) {
	r := func(kind types.ReceiptType, fromMe bool) *events.Receipt {
		return &events.Receipt{MessageSource: types.MessageSource{Chat: groupJID, Sender: aliJID, IsFromMe: fromMe, IsGroup: true},
			MessageIDs: []types.MessageID{"OUT1", "OUT2"}, Timestamp: ts, Type: kind}
	}
	for kind, want := range map[types.ReceiptType]domain.MessageStatus{
		types.ReceiptTypeDelivered: domain.StatusDelivered,
		types.ReceiptTypeRead:      domain.StatusRead,
		types.ReceiptTypePlayed:    domain.StatusPlayed,
	} {
		ev := one(t, normalizeReceipt(r(kind, false))).(domain.ReceiptReceived)
		if ev.Status != want || len(ev.WAIDs) != 2 || ev.WAIDs[1] != "OUT2" || ev.From != "101@lid" || ev.ChatJID != "120363001@g.us" {
			t.Errorf("%q: %+v", kind, ev)
		}
	}
	// A read on the phone: "read" from this account with read receipts
	// on, "read-self" with them off; both read the chat here.
	for _, kind := range []types.ReceiptType{types.ReceiptTypeRead, types.ReceiptTypePlayed, types.ReceiptTypeReadSelf, types.ReceiptTypePlayedSelf} {
		read, ok := one(t, normalizeReceipt(r(kind, true))).(domain.ChatRead)
		if !ok || !read.ReadAt.Equal(ts) || read.ChatJID != "120363001@g.us" {
			t.Fatalf("%q on another own device must read the chat: %+v", kind, read)
		}
	}
	for _, rc := range []*events.Receipt{r(types.ReceiptTypeRetry, false), r(types.ReceiptTypeSender, true),
		r(types.ReceiptTypeDelivered, true), r(types.ReceiptTypeServerError, false),
		r(types.ReceiptTypeReadSelf, false), r(types.ReceiptTypePlayedSelf, false)} { // someone else cannot read for this account
		if evs := normalizeReceipt(rc); evs != nil {
			t.Errorf("%q from me=%v: %+v", rc.Type, rc.IsFromMe, evs)
		}
	}
	empty := r(types.ReceiptTypeRead, false)
	empty.MessageIDs = nil
	if normalizeReceipt(empty) != nil {
		t.Error("a receipt without messages changes nothing")
	}
}
