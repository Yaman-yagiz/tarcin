// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"math"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// App state is what the phone keeps in sync across devices: contact
// names, and per-chat pin, mute, archive and read state. The client sets
// EmitAppStateEventsOnFullSync, so a full sync arrives as one event per
// item, like any later change, and both take the same path here.

func normalizeContact(e *events.Contact) []domain.Event {
	a := e.Action
	name := a.GetFullName()
	if name == "" {
		name = a.GetFirstName()
	}
	var evs []domain.Event
	pn, lid := parse(a.GetPnJID()), parse(a.GetLidJID())
	for _, l := range []domain.Event{link(e.JID, lid), link(e.JID, pn), link(pn, lid)} {
		if l != nil {
			evs = append(evs, l)
			break
		}
	}
	if name != "" {
		evs = append(evs, domain.ContactUpdated{JID: fromJID(e.JID), ContactName: name})
	}
	return evs
}

func parse(s string) types.JID {
	if s == "" {
		return types.EmptyJID
	}
	j, err := types.ParseJID(s)
	if err != nil {
		return types.EmptyJID
	}
	return j
}

func normalizePushName(e *events.PushName) []domain.Event {
	var evs []domain.Event
	if l := link(e.JID, e.JIDAlt); l != nil {
		evs = append(evs, l)
	}
	if e.NewPushName != "" {
		evs = append(evs, domain.ContactUpdated{JID: fromJID(e.JID), PushName: e.NewPushName})
	}
	return evs
}

func normalizeBusinessName(e *events.BusinessName) []domain.Event {
	if e.NewBusinessName == "" {
		return nil
	}
	return []domain.Event{domain.ContactUpdated{JID: fromJID(e.JID), BusinessName: e.NewBusinessName}}
}

func normalizePin(e *events.Pin) []domain.Event {
	u := domain.ChatUpdated{ChatJID: fromJID(e.JID), Fields: domain.ChatFieldPinned}
	if e.Action.GetPinned() {
		u.PinnedAt = e.Timestamp
	}
	return []domain.Event{u}
}

func normalizeMute(e *events.Mute) []domain.Event {
	u := domain.ChatUpdated{ChatJID: fromJID(e.JID), Fields: domain.ChatFieldMuted}
	if e.Action.GetMuted() {
		u.MutedUntil = muteUntil(e.Action.GetMuteEndTimestamp())
	}
	return []domain.Event{u}
}

// muteUntil converts a mute end of an app state action: milliseconds, and
// -1 (or anything not ahead of 1970) for muted without an end.
func muteUntil(v int64) time.Time {
	if v <= 0 {
		return domain.MuteForever
	}
	return muteEnd(uint64(min(v, math.MaxInt64)))
}

func normalizeArchive(e *events.Archive) []domain.Event {
	return []domain.Event{domain.ChatUpdated{ChatJID: fromJID(e.JID), Fields: domain.ChatFieldArchived, Archived: e.Action.GetArchived()}}
}

// normalizeMarkRead turns a chat marked read on another device into a
// read, and one marked unread into an unread count of one, the dot the
// phone shows.
func normalizeMarkRead(e *events.MarkChatAsRead) []domain.Event {
	if e.Action.GetRead() {
		return []domain.Event{domain.ChatRead{ChatJID: fromJID(e.JID), ReadAt: e.Timestamp}}
	}
	return []domain.Event{domain.ChatUpdated{ChatJID: fromJID(e.JID), Fields: domain.ChatFieldUnread, UnreadCount: 1}}
}

// appStateResyncCooldown is how long after a full sync of a collection
// another failure of it is only logged, so that a collection the server
// cannot sync in full either does not keep the connection busy.
const appStateResyncCooldown = 10 * time.Minute

// appStateResyncTimeout bounds one full sync of a collection.
const appStateResyncTimeout = 2 * time.Minute

// recoverAppState handles a collection whose patches no longer verify,
// for example with a mismatching LTHash after a missed patch. whatsmeow
// only reports it, and the collection then stops syncing. tarcin drops
// the local copy and syncs the collection in full from the server, off
// the event handler. The full sync delivers every item again, which the
// store applies idempotently. A full sync that fails is only logged.
func (a *Adapter) recoverAppState(ctx context.Context, e *events.AppStateSyncError) {
	if e.FullSync {
		a.opts.Log.Warn("full app state sync failed", "collection", string(e.Name), "err", e.Error)
		return
	}
	now := time.Now()
	a.resyncMu.Lock()
	last, seen := a.resyncedAt[e.Name]
	if seen && now.Sub(last) < appStateResyncCooldown {
		a.resyncMu.Unlock()
		a.opts.Log.Warn("app state out of step again; not syncing it in full yet", "collection", string(e.Name), "err", e.Error)
		return
	}
	a.resyncedAt[e.Name] = now
	a.resyncMu.Unlock()
	a.opts.Log.Info("app state out of step; syncing it again in full", "collection", string(e.Name), "err", e.Error)
	a.resyncs.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, appStateResyncTimeout)
		defer cancel()
		if err := a.fetchAppState(ctx, e.Name, true, false); err != nil {
			a.opts.Log.Warn("full app state sync failed", "collection", string(e.Name), "err", err)
			return
		}
		a.opts.Log.Info("app state synced again in full", "collection", string(e.Name))
	})
}
