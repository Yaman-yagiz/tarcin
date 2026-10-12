// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// normalizeReceipt turns a receipt into a status change of own messages,
// or, for a read on another of this account's devices, into a read of the
// chat. Retry, server-error and similar receipts are whatsmeow's business.
func normalizeReceipt(e *events.Receipt) []domain.Event {
	chat := fromJID(e.Chat)
	if e.Chat == types.StatusBroadcastJID || len(e.MessageIDs) == 0 {
		return nil
	}
	var status domain.MessageStatus
	switch e.Type {
	case types.ReceiptTypeReadSelf, types.ReceiptTypePlayedSelf:
		return []domain.Event{domain.ChatRead{ChatJID: chat, ReadAt: e.Timestamp}}
	case types.ReceiptTypeDelivered:
		status = domain.StatusDelivered
	case types.ReceiptTypeRead:
		status = domain.StatusRead
	case types.ReceiptTypePlayed:
		status = domain.StatusPlayed
	default:
		return nil
	}
	if e.IsFromMe {
		return nil // an own device received or read its own copy
	}
	ids := make([]domain.WAMessageID, len(e.MessageIDs))
	for i, id := range e.MessageIDs {
		ids[i] = domain.WAMessageID(id)
	}
	return []domain.Event{domain.ReceiptReceived{ChatJID: chat, WAIDs: ids, From: fromJID(e.Sender), Status: status, Timestamp: e.Timestamp}}
}
