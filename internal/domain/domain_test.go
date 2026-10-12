// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"slices"
	"testing"
	"time"
)

func TestDisplayNamePriority(t *testing.T) {
	pn := JID("905551112233@s.whatsapp.net")
	lid := JID("123456789@lid")
	tests := []struct {
		name string
		id   Identity
		want string
	}{
		{"contact name wins", Identity{ContactName: "Ayşe", PushName: "ayse", BusinessName: "Shop", PN: pn, LID: lid}, "Ayşe"},
		{"push name", Identity{PushName: "ayse", BusinessName: "Shop", PN: pn}, "ayse"},
		{"business name", Identity{BusinessName: "Shop", PN: pn}, "Shop"},
		{"phone number", Identity{PN: pn, LID: lid}, "+905551112233"},
		{"short LID", Identity{LID: lid}, "~6789"},
		{"very short LID", Identity{LID: JID("12@lid")}, "~12"},
		{"nothing known", Identity{}, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.id.DisplayName(); got != tt.want {
			t.Errorf("%s: DisplayName() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCanonicalJID(t *testing.T) {
	pn, lid := JID("905551112233@s.whatsapp.net"), JID("1@lid")
	if got := (Identity{PN: pn, LID: lid}).CanonicalJID(); got != lid {
		t.Errorf("with LID: got %q, want %q", got, lid)
	}
	if got := (Identity{PN: pn}).CanonicalJID(); got != pn {
		t.Errorf("without LID: got %q, want %q", got, pn)
	}
}

func TestChatListOrder(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	chats := []*Chat{
		{ID: 1, LastActivity: base},
		{ID: 2, LastActivity: base.Add(time.Minute)},
		{ID: 3, LastActivity: base.Add(-time.Hour), PinnedAt: base.Add(-time.Hour)},
		{ID: 4, LastActivity: base, PinnedAt: base},
		{ID: 5, LastActivity: base}, // ties with 1 on activity: higher ID first
	}
	slices.SortFunc(chats, func(a, b *Chat) int {
		switch {
		case a.ListBefore(b):
			return -1
		case b.ListBefore(a):
			return 1
		}
		return 0
	})
	var got []ChatID
	for _, c := range chats {
		got = append(got, c.ID)
	}
	want := []ChatID{4, 3, 2, 5, 1}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestChatMute(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		until time.Time
		want  bool
	}{
		{time.Time{}, false},
		{now.Add(-time.Second), false},
		{now.Add(time.Hour), true},
		{MuteForever, true},
	} {
		c := Chat{MutedUntil: tt.until}
		if got := c.IsMuted(now); got != tt.want {
			t.Errorf("MutedUntil %v: IsMuted = %v, want %v", tt.until, got, tt.want)
		}
	}
}

var allStatuses = []MessageStatus{StatusPending, StatusSent, StatusDelivered, StatusRead, StatusPlayed, StatusFailed}

func TestStatusAdvance(t *testing.T) {
	tests := []struct{ from, next, want MessageStatus }{
		{StatusPending, StatusSent, StatusSent},
		{StatusSent, StatusDelivered, StatusDelivered},
		{StatusRead, StatusDelivered, StatusRead}, // late receipt
		{StatusDelivered, StatusDelivered, StatusDelivered},
		{StatusPending, StatusFailed, StatusFailed},
		{StatusSent, StatusFailed, StatusSent}, // failure after confirmation is ignored
		{StatusFailed, StatusSent, StatusSent}, // server confirmation supersedes failure
		{StatusFailed, StatusPending, StatusFailed},
	}
	for _, tt := range tests {
		if got := tt.from.Advance(tt.next); got != tt.want {
			t.Errorf("%v.Advance(%v) = %v, want %v", tt.from, tt.next, got, tt.want)
		}
	}
}

// TestStatusAdvanceNeverRegresses checks every pair: once confirmed by
// the server, a status never moves backwards, whatever arrives later.
func TestStatusAdvanceNeverRegresses(t *testing.T) {
	rank := func(s MessageStatus) int {
		if s == StatusFailed {
			return 0 // a failure ranks with pending
		}
		return int(s)
	}
	for _, from := range allStatuses {
		for _, next := range allStatuses {
			got := from.Advance(next)
			if rank(got) < rank(from) {
				t.Errorf("%v.Advance(%v) = %v regresses", from, next, got)
			}
			if got.Advance(next) != got {
				t.Errorf("Advance is not idempotent for %v then %v", from, next)
			}
		}
	}
}

func TestCursorOrder(t *testing.T) {
	t0 := time.Unix(1_760_000_000, 0)
	a := Cursor{Time: t0, ID: 10}
	b := Cursor{Time: t0, ID: 11}                 // same second, inserted later
	c := Cursor{Time: t0.Add(time.Second), ID: 2} // newer, but lower ID (history import)
	if !a.Before(b) || b.Before(a) {
		t.Error("equal timestamps must be ordered by ID")
	}
	if !b.Before(c) || c.Before(b) {
		t.Error("timestamps must take precedence over IDs")
	}
	if a.Before(a) {
		t.Error("Before must be strict")
	}
	m := Message{ID: 10, Timestamp: t0}
	if m.Cursor() != a {
		t.Errorf("Message.Cursor() = %+v, want %+v", m.Cursor(), a)
	}
}

func TestKindsAndFlags(t *testing.T) {
	if !KindImage.HasMedia() || KindText.HasMedia() || KindSystem.HasMedia() {
		t.Error("HasMedia classification is wrong")
	}
	if KindPoll.String() != "poll" || MessageKind(0).String() != "unknown" || MessageKind(200).String() != "unknown" {
		t.Error("MessageKind.String is wrong")
	}
	f := FlagForwarded | FlagMentionsMe
	if !f.Has(FlagForwarded) || !f.Has(FlagForwarded|FlagMentionsMe) || f.Has(FlagViewOnce) {
		t.Error("MessageFlags.Has is wrong")
	}
	if ChatGroup.String() != "group" || ChatKind(0).String() != "unknown" {
		t.Error("ChatKind.String is wrong")
	}
	for _, s := range allStatuses {
		if s.String() == "unknown" {
			t.Errorf("status %d has no name", s)
		}
	}
}

func TestChatFields(t *testing.T) {
	f := ChatFieldTitle | ChatFieldMuted
	if !f.Has(ChatFieldTitle) || !f.Has(ChatFieldTitle|ChatFieldMuted) || f.Has(ChatFieldPinned) {
		t.Fatal("ChatFields.Has is wrong")
	}
}

func TestConnectionStates(t *testing.T) {
	terminal := map[ConnectionState]bool{
		ConnDisconnected: false, ConnConnecting: false, ConnOnline: false,
		ConnLoggedOut: true, ConnStreamReplace: true, ConnBanned: true, ConnOutdated: true,
	}
	for s, want := range terminal {
		if s.Terminal() != want {
			t.Errorf("%v.Terminal() = %v, want %v", s, s.Terminal(), want)
		}
		if s.String() == "unknown" {
			t.Errorf("state %d has no name", s)
		}
	}
}

// The event set is closed: every event type implements Event.
var _ = []Event{
	MessageReceived{}, ReceiptReceived{}, ReactionReceived{}, MessageEdited{},
	MessageRevoked{}, IdentityLinked{}, ContactUpdated{}, ChatUpdated{}, GroupMembersChanged{},
	ChatTyping{}, ConnectionChanged{},
}
