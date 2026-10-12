// SPDX-License-Identifier: GPL-3.0-or-later

package domain

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
// shortened LID. Names are raw and must be sanitized before rendering.
func (i Identity) DisplayName() string {
	switch {
	case i.ContactName != "":
		return i.ContactName
	case i.PushName != "":
		return i.PushName
	case i.BusinessName != "":
		return i.BusinessName
	case !i.PN.IsZero():
		return "+" + i.PN.User()
	case !i.LID.IsZero():
		return "~" + lastN(i.LID.User(), 4)
	}
	return "unknown"
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
