// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"fmt"

	"go.mau.fi/whatsmeow/types"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// toJID converts a domain JID to whatsmeow's. The JID is checked with
// domain.ParseJID first: whatsmeow's parser accepts nearly anything.
func toJID(j domain.JID) (types.JID, error) {
	if _, err := domain.ParseJID(string(j)); err != nil {
		return types.JID{}, err
	}
	jid, err := types.ParseJID(string(j))
	if err != nil {
		return types.JID{}, fmt.Errorf("%w: %q", domain.ErrInvalidJID, j)
	}
	return jid, nil
}

// fromJID converts whatsmeow's JID to a domain JID. Device and agent parts
// are dropped: tarcin addresses people and chats, not their devices.
func fromJID(j types.JID) domain.JID {
	if j.IsEmpty() {
		return ""
	}
	return domain.JID(j.ToNonAD().String())
}
