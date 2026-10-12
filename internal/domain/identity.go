// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "unicode"

// IdentityID is the local, stable ID of a person (a row in identities).
type IdentityID int64

// Identity is one person. The LID is canonical once known; the phone
// number is an optional attribute that may be missing or redacted (see
// docs/adr/0009-lid-canonical-identity.md).
type Identity struct {
	ID           IdentityID
	LID          JID // "…@lid", empty until known
	PN           JID // "…@s.whatsapp.net", empty if unknown
	ContactName  string
	PushName     string
	BusinessName string
	IsMe         bool
}

// CanonicalJID returns the LID when known and the phone-number JID
// otherwise.
func (i Identity) CanonicalJID() JID {
	if !i.LID.IsZero() {
		return i.LID
	}
	return i.PN
}

// DisplayName returns the name to show for the identity, in priority
// order: contact name, push name, business name, phone number, and a
// shortened LID. A name the person chose for themselves starts with "~",
// as in the official clients, so that it cannot pass for a contact's
// name, "You" or a phone number; a name that shows nothing is skipped.
// Names are raw and must be sanitized before rendering.
func (i Identity) DisplayName() string {
	switch {
	case visible(i.ContactName):
		return i.ContactName
	case visible(i.PushName):
		return "~" + i.PushName
	case visible(i.BusinessName):
		return "~" + i.BusinessName
	case !i.PN.IsZero():
		return "+" + i.PN.User()
	case !i.LID.IsZero():
		return "~" + lastN(i.LID.User(), 4)
	}
	return "unknown"
}

// visible reports whether s shows anything: a letter, digit, symbol or
// punctuation mark other than the blank look-alikes (the braille blank
// and the Hangul fillers) that make a name look empty.
func visible(s string) bool {
	for _, r := range s {
		switch r {
		case '\u2800', '\u115f', '\u1160', '\u3164', '\uffa0':
			continue
		}
		if unicode.In(r, unicode.L, unicode.N, unicode.S, unicode.P) {
			return true
		}
	}
	return false
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
