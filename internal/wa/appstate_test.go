// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestNormalizeAppState(t *testing.T) {
	contact := normalizeContact(&events.Contact{JID: aliPN, Action: &waSyncAction.ContactAction{
		FullName: proto.String("Ali Veli"), LidJID: proto.String("101@lid")}})
	if len(contact) != 2 || contact[0].(domain.IdentityLinked).LID != "101@lid" ||
		contact[1].(domain.ContactUpdated).ContactName != "Ali Veli" {
		t.Fatalf("contact = %+v", contact)
	}
	byLID := normalizeContact(&events.Contact{JID: aliJID, Action: &waSyncAction.ContactAction{
		FirstName: proto.String("Ali"), PnJID: proto.String("905551112233@s.whatsapp.net")}})
	if len(byLID) != 2 || byLID[0].(domain.IdentityLinked).PN != "905551112233@s.whatsapp.net" ||
		byLID[1].(domain.ContactUpdated).ContactName != "Ali" {
		t.Fatalf("contact by LID = %+v", byLID)
	}
	if evs := normalizeContact(&events.Contact{JID: aliPN, Action: &waSyncAction.ContactAction{}}); len(evs) != 0 {
		t.Fatalf("a contact without a name changes nothing: %+v", evs)
	}
	push := normalizePushName(&events.PushName{JID: aliJID, JIDAlt: aliPN, NewPushName: "ali"})
	if len(push) != 2 || push[1].(domain.ContactUpdated).PushName != "ali" {
		t.Fatalf("push name = %+v", push)
	}
	if b := normalizeBusinessName(&events.BusinessName{JID: aliJID, NewBusinessName: "Ali Ltd"}); b[0].(domain.ContactUpdated).BusinessName != "Ali Ltd" {
		t.Fatalf("business = %+v", b)
	}
	if b := normalizeBusinessName(&events.BusinessName{JID: aliJID}); b != nil {
		t.Fatal("an empty business name changes nothing")
	}

	pin := normalizePin(&events.Pin{JID: groupJID, Timestamp: ts, Action: &waSyncAction.PinAction{Pinned: proto.Bool(true)}})[0].(domain.ChatUpdated)
	if !pin.PinnedAt.Equal(ts) || !pin.Fields.Has(domain.ChatFieldPinned) || pin.ChatJID != "120363001@g.us" {
		t.Fatalf("pin = %+v", pin)
	}
	if unpin := normalizePin(&events.Pin{JID: groupJID, Action: &waSyncAction.PinAction{}})[0].(domain.ChatUpdated); !unpin.PinnedAt.IsZero() {
		t.Fatal("unpin clears the pin")
	}
	for v, want := range map[int64]time.Time{-1: domain.MuteForever, ts.Add(time.Hour).UnixMilli(): ts.Add(time.Hour)} {
		m := normalizeMute(&events.Mute{JID: groupJID, Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(v)}})
		if got := m[0].(domain.ChatUpdated).MutedUntil; !got.Equal(want) {
			t.Errorf("mute end %d = %v, want %v", v, got, want)
		}
	}
	if m := normalizeMute(&events.Mute{JID: groupJID, Action: &waSyncAction.MuteAction{}})[0].(domain.ChatUpdated); !m.MutedUntil.IsZero() {
		t.Fatal("unmute clears the mute")
	}
	if a := normalizeArchive(&events.Archive{JID: groupJID, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})[0].(domain.ChatUpdated); !a.Archived {
		t.Fatal("archive")
	}
	if r := normalizeMarkRead(&events.MarkChatAsRead{JID: groupJID, Timestamp: ts, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)}})[0].(domain.ChatRead); !r.ReadAt.Equal(ts) {
		t.Fatalf("read = %+v", r)
	}
	if u := normalizeMarkRead(&events.MarkChatAsRead{JID: groupJID, Action: &waSyncAction.MarkChatAsReadAction{}})[0].(domain.ChatUpdated); u.UnreadCount != 1 {
		t.Fatalf("marked unread = %+v", u)
	}
	a := testAdapter(t)
	if !a.cli.EmitAppStateEventsOnFullSync {
		t.Fatal("a full sync must arrive as events")
	}
}

// TestAppStateRecovery checks that a collection whose patches no longer
// verify is synced again in full, once per cooldown, and that a failed
// full sync is not retried.
func TestAppStateRecovery(t *testing.T) {
	a, sink := testAdapter(t), &recordingSink{}
	var mu sync.Mutex
	var calls []string
	a.fetchAppState = func(_ context.Context, name appstate.WAPatchName, fullSync, onlyIfNotSynced bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, fmt.Sprintf("%s full=%t only=%t", name, fullSync, onlyIfNotSynced))
		return nil
	}
	mismatch := fmt.Errorf("failed to verify patch v80: %w", appstate.ErrMismatchingLTHash)
	failure := func(name appstate.WAPatchName, full bool) {
		t.Helper()
		if !a.handle(t.Context(), sink, &events.AppStateSyncError{Name: name, FullSync: full, Error: mismatch}) {
			t.Fatal("a sync error must be acknowledged")
		}
		a.resyncs.Wait()
	}
	failure(appstate.WAPatchRegularLow, false)
	failure(appstate.WAPatchRegularLow, false) // within the cooldown
	failure(appstate.WAPatchRegular, true)     // a full sync failed: no loop
	failure(appstate.WAPatchRegular, false)
	want := []string{"regular_low full=true only=false", "regular full=true only=false"}
	if !slices.Equal(calls, want) {
		t.Fatalf("full syncs = %q, want %q", calls, want)
	}
	// After the cooldown, a collection may be synced in full again.
	a.resyncMu.Lock()
	a.resyncedAt[appstate.WAPatchRegularLow] = time.Now().Add(-appStateResyncCooldown)
	a.resyncMu.Unlock()
	failure(appstate.WAPatchRegularLow, false)
	if len(calls) != 3 {
		t.Fatalf("full syncs = %q", calls)
	}
	if len(sink.events) != 0 {
		t.Fatalf("a sync error delivers nothing itself: %v", sink.events)
	}
}
