// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Servers of the JIDs tarcin handles.
const (
	ServerPN         = "s.whatsapp.net" // phone-number user
	ServerLID        = "lid"            // linked identity user
	ServerGroup      = "g.us"           // group chat
	ServerBroadcast  = "broadcast"      // broadcast list or status
	ServerNewsletter = "newsletter"     // channel
)

// JID is a canonical WhatsApp address of the form "user@server", for
// example "123@lid", "905551112233@s.whatsapp.net" or "1203630@g.us".
// Canonical JIDs never carry a device suffix; the protocol adapter strips
// it before creating a JID. The zero value is the empty JID.
type JID string

// ErrInvalidJID is wrapped by every error returned from ParseJID.
var ErrInvalidJID = errors.New("invalid JID")

// ParseJID validates s and returns it as a canonical JID.
func ParseJID(s string) (JID, error) {
	user, server, ok := strings.Cut(s, "@")
	switch {
	case !ok || user == "":
		return "", fmt.Errorf("%w %q: want user@server", ErrInvalidJID, s)
	case strings.ContainsRune(user, ':'):
		return "", fmt.Errorf("%w %q: device JIDs are not canonical", ErrInvalidJID, s)
	case !knownServer(server):
		return "", fmt.Errorf("%w %q: unknown server %q", ErrInvalidJID, s, server)
	}
	for _, r := range user {
		if !validUserRune(r) {
			return "", fmt.Errorf("%w %q: unexpected character %q", ErrInvalidJID, s, r)
		}
	}
	return JID(s), nil
}

func knownServer(server string) bool {
	switch server {
	case ServerPN, ServerLID, ServerGroup, ServerBroadcast, ServerNewsletter:
		return true
	}
	return false
}

func validUserRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '.' || r == '_'
}

// User returns the part before the "@".
func (j JID) User() string {
	user, _, _ := strings.Cut(string(j), "@")
	return user
}

// Server returns the part after the "@".
func (j JID) Server() string {
	_, server, _ := strings.Cut(string(j), "@")
	return server
}

// IsZero reports whether j is the empty JID.
func (j JID) IsZero() bool { return j == "" }

// IsPN reports whether j addresses a user by phone number.
func (j JID) IsPN() bool { return j.Server() == ServerPN }

// IsLID reports whether j addresses a user by linked identity.
func (j JID) IsLID() bool { return j.Server() == ServerLID }

// IsUser reports whether j addresses a single person.
func (j JID) IsUser() bool { return j.IsPN() || j.IsLID() }

// IsGroup reports whether j addresses a group chat.
func (j JID) IsGroup() bool { return j.Server() == ServerGroup }

// String implements fmt.Stringer. Logs must not print full JIDs; use
// the redacting helpers in internal/logging instead.
func (j JID) String() string { return string(j) }
