// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseJID(t *testing.T) {
	valid := []struct {
		in             string
		user, server   string
		pn, lid, group bool
	}{
		{in: "905551112233@s.whatsapp.net", user: "905551112233", server: ServerPN, pn: true},
		{in: "123456789012345@lid", user: "123456789012345", server: ServerLID, lid: true},
		{in: "120363012345678901@g.us", user: "120363012345678901", server: ServerGroup, group: true},
		{in: "905551112233-1600000000@g.us", user: "905551112233-1600000000", server: ServerGroup, group: true},
		{in: "status@broadcast", user: "status", server: ServerBroadcast},
		{in: "120363999@newsletter", user: "120363999", server: ServerNewsletter},
	}
	for _, tt := range valid {
		j, err := ParseJID(tt.in)
		if err != nil {
			t.Errorf("ParseJID(%q) error: %v", tt.in, err)
			continue
		}
		if j.User() != tt.user || j.Server() != tt.server {
			t.Errorf("ParseJID(%q) = user %q server %q", tt.in, j.User(), j.Server())
		}
		if j.IsPN() != tt.pn || j.IsLID() != tt.lid || j.IsGroup() != tt.group {
			t.Errorf("ParseJID(%q): IsPN=%v IsLID=%v IsGroup=%v", tt.in, j.IsPN(), j.IsLID(), j.IsGroup())
		}
		if j.IsUser() != (tt.pn || tt.lid) {
			t.Errorf("ParseJID(%q).IsUser() = %v", tt.in, j.IsUser())
		}
		if j.String() != tt.in {
			t.Errorf("String() = %q, want %q", j.String(), tt.in)
		}
	}

	invalid := []string{
		"",
		"@s.whatsapp.net",
		"905551112233",
		"905551112233@",
		"905551112233@example.com",
		"905551112233:12@s.whatsapp.net", // device JID
		"90555 1112233@s.whatsapp.net",
		"9055\x1b[31m@s.whatsapp.net", // escape sequence
		"a@b@lid",
	}
	for _, in := range invalid {
		if j, err := ParseJID(in); err == nil {
			t.Errorf("ParseJID(%q) = %q, want error", in, j)
		} else if !errors.Is(err, ErrInvalidJID) {
			t.Errorf("ParseJID(%q) error %v does not wrap ErrInvalidJID", in, err)
		}
	}
}

func TestZeroJID(t *testing.T) {
	var j JID
	if !j.IsZero() || j.IsUser() || j.IsGroup() || j.User() != "" || j.Server() != "" {
		t.Fatalf("zero JID misbehaves: %+v", j)
	}
}

func FuzzParseJID(f *testing.F) {
	for _, s := range []string{"905551112233@s.whatsapp.net", "1@lid", "1-2@g.us", "x:1@lid", "@", "a@b", "\x00@lid"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		j, err := ParseJID(s)
		if err != nil {
			return
		}
		if string(j) != s {
			t.Fatalf("ParseJID(%q) changed the input to %q", s, j)
		}
		if j.User() == "" || !knownServer(j.Server()) {
			t.Fatalf("accepted JID %q has empty user or unknown server", s)
		}
		if strings.ContainsAny(j.User(), ":@ \x1b") {
			t.Fatalf("accepted JID %q has a forbidden character in the user part", s)
		}
	})
}
