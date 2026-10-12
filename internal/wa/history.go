// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// History sync (event-pipeline.md §3.1). whatsmeow downloads a sync on its
// own by default, ignores whether the handler stored it, and deletes it
// from the server right away: quitting tarcin during an import, or a full
// disk, would lose that history for good. tarcin downloads it itself
// (ManualHistorySyncDownload):
//
//  1. The notification arrives as a protocol message. It is stored as a
//     HistoryQueued event before it is acknowledged.
//  2. A worker downloads the sync, writes it in chunks through the bulk
//     queue, each chunk waiting for its commit, and only then deletes it
//     from the server and stores HistoryDone.
//  3. Syncs still queued after a restart are handed back with Resume.
//
// Every write is idempotent, so a sync cut short is simply written again.

// historyChunk is the most messages a bulk submission carries.
const historyChunk = 500

// historyQueue holds the syncs the worker has yet to process.
type historyQueue struct {
	mu   sync.Mutex
	jobs []domain.HistoryQueued
	wake chan struct{}
}

func newHistoryQueue() *historyQueue { return &historyQueue{wake: make(chan struct{}, 1)} }

func (q *historyQueue) add(jobs ...domain.HistoryQueued) {
	q.mu.Lock()
	q.jobs = append(q.jobs, jobs...)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *historyQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

func (q *historyQueue) next() (domain.HistoryQueued, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return domain.HistoryQueued{}, false
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, true
}

// Resume hands back history syncs that were queued before a restart (from
// store.Reader.PendingHistory). Call it before Run.
func (a *Adapter) Resume(pending []domain.HistoryQueued) { a.history.add(pending...) }

// queued returns the HistoryQueued event for a history sync notification.
func queued(id types.MessageID, n *waE2E.HistorySyncNotification) domain.Event {
	raw, err := proto.Marshal(n)
	if err != nil {
		return nil
	}
	return domain.HistoryQueued{ID: id, Notification: raw}
}

// runHistory processes queued syncs until ctx ends.
func (a *Adapter) runHistory(ctx context.Context, sink backend.Sink) {
	for {
		job, ok := a.history.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-a.history.wake:
				continue
			}
		}
		if !a.syncHistory(ctx, sink, job) {
			return // ctx ended; the job stays queued in the store
		}
	}
}

// syncHistory downloads and stores one sync, retrying with backoff until
// it is stored or ctx ends. It reports false when ctx ended first.
func (a *Adapter) syncHistory(ctx context.Context, sink backend.Sink, job domain.HistoryQueued) bool {
	log := a.opts.Log.With("sync", job.ID)
	var n waE2E.HistorySyncNotification
	if err := proto.Unmarshal(job.Notification, &n); err != nil {
		log.Warn("dropped an unreadable history sync", "err", err)
		return a.historyDone(ctx, sink, job.ID) // unreadable: nothing to retry
	}
	for delay := a.opts.HistoryRetry; ; delay = min(2*delay, time.Minute) {
		data, err := a.cli.DownloadHistorySync(ctx, &n, true)
		switch {
		case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410):
			log.Warn("the server no longer has a history sync", "err", err)
			return a.historyDone(ctx, sink, job.ID)
		case err == nil:
			if a.storeHistory(ctx, sink, data) {
				// Safe now: remove it from the server, then from the queue.
				if err := a.cli.DeleteMedia(ctx, whatsmeow.MediaHistory, n.GetDirectPath(), n.GetFileEncSHA256(), n.GetEncHandle()); err != nil {
					log.Info("could not delete a stored history sync from the server", "err", err)
				}
				log.Info("history sync stored", "type", data.GetSyncType().String(), "conversations", len(data.GetConversations()))
				return a.historyDone(ctx, sink, job.ID)
			}
			if ctx.Err() == nil {
				log.Warn("history sync not stored; trying again", "in", delay)
			}
		case ctx.Err() == nil:
			log.Warn("history sync not downloaded; trying again", "in", delay, "err", err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
	}
}

// storeHistory writes the events of a sync in chunks, each waiting for its
// commit. It reports whether all of them were stored.
func (a *Adapter) storeHistory(ctx context.Context, sink backend.Sink, data *waHistorySync.HistorySync) bool {
	for _, chunk := range a.historyEvents(ctx, data) {
		if ctx.Err() != nil || sink.Deliver(ctx, chunk) != nil {
			return false
		}
	}
	return true
}

func (a *Adapter) historyDone(ctx context.Context, sink backend.Sink, id string) bool {
	for {
		if sink.Deliver(ctx, []domain.Event{domain.HistoryDone{ID: id}}) == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(a.opts.HistoryRetry):
		}
	}
}

// historyEvents converts a sync into chunks of events: links between
// address forms and push names first, then each conversation's settings
// and members with its messages, at most historyChunk messages a chunk.
func (a *Adapter) historyEvents(ctx context.Context, data *waHistorySync.HistorySync) [][]domain.Event {
	var chunks [][]domain.Event
	var cur []domain.Event
	messages := 0
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, cur)
			cur, messages = nil, 0
		}
	}
	for _, m := range data.GetPhoneNumberToLidMappings() {
		pn, err1 := types.ParseJID(m.GetPnJID())
		lid, err2 := types.ParseJID(m.GetLidJID())
		if err1 == nil && err2 == nil {
			if l := link(pn, lid); l != nil {
				cur = append(cur, l)
			}
		}
	}
	for _, p := range data.GetPushnames() {
		if jid, err := types.ParseJID(p.GetID()); err == nil && p.GetPushname() != "" && p.GetPushname() != "-" {
			cur = append(cur, domain.ContactUpdated{JID: fromJID(jid), PushName: p.GetPushname()})
		}
	}
	flush()
	onDemand := data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND
	for _, conv := range data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || chat == types.StatusBroadcastJID {
			continue
		}
		switch {
		case !onDemand:
			cur = append(cur, conversation(chat, conv)...)
		case conv.GetEndOfHistoryTransfer() || len(conv.GetMessages()) == 0:
			// An answer to a request for older messages carries no current
			// chat state, only the messages; none means there are no more.
			cur = append(cur, domain.ChatUpdated{ChatJID: fromJID(chat), Fields: domain.ChatFieldHistoryComplete, HistoryComplete: true})
		}
		for _, hm := range conv.GetMessages() {
			evt, err := a.cli.ParseWebMessage(chat, hm.GetMessage())
			if err != nil {
				continue
			}
			evs := a.normalizeMessage(evt, domain.SourceHistory)
			if len(evs) == 0 {
				continue
			}
			cur = append(cur, a.resolveMessage(ctx, &evt.Info, evs)...)
			messages++
			if messages >= historyChunk {
				flush()
			}
		}
		flush()
	}
	return chunks
}

// conversation describes a chat of a history sync: its unread count, pin,
// archive, mute and disappearing settings, the group's name and members,
// and whether the phone has older messages.
func conversation(chat types.JID, c *waHistorySync.Conversation) []domain.Event {
	u := domain.ChatUpdated{ChatJID: fromJID(chat),
		Fields: domain.ChatFieldUnread | domain.ChatFieldArchived | domain.ChatFieldPinned |
			domain.ChatFieldMuted | domain.ChatFieldEphemeral,
		UnreadCount:    int(min(c.GetUnreadCount(), math.MaxInt32)),
		Archived:       c.GetArchived(),
		EphemeralTimer: time.Duration(c.GetEphemeralExpiration()) * time.Second,
		MutedUntil:     muteEnd(c.GetMuteEndTime()),
	}
	if p := c.GetPinned(); p > 0 {
		u.PinnedAt = time.Unix(int64(p), 0)
	}
	if c.GetEndOfHistoryTransfer() {
		u.Fields |= domain.ChatFieldHistoryComplete
		u.HistoryComplete = true
	}
	evs := []domain.Event{}
	if chat.Server == types.GroupServer {
		if name := c.GetName(); name != "" {
			u.Fields |= domain.ChatFieldTitle
			u.Title = name
		}
		if parts := c.GetParticipant(); len(parts) > 0 {
			m := domain.GroupMembersChanged{ChatJID: fromJID(chat), Full: true}
			for _, p := range parts {
				jid, err := types.ParseJID(p.GetUserJID())
				if err != nil {
					continue
				}
				m.Members = append(m.Members, domain.GroupMember{JID: fromJID(jid), Role: role(
					p.GetRank() == waHistorySync.GroupParticipant_ADMIN, p.GetRank() == waHistorySync.GroupParticipant_SUPERADMIN)})
			}
			evs = append(evs, m)
		}
	}
	return append([]domain.Event{u}, evs...)
}

// muteEnd converts a conversation's mute end. Seconds and milliseconds
// both occur; an end beyond the year 2100 means muted forever.
func muteEnd(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	var t time.Time
	if v > 1e11 {
		t = time.UnixMilli(int64(min(v, math.MaxInt64/2)))
	} else {
		t = time.Unix(int64(v), 0)
	}
	if t.Year() > 2100 {
		return domain.MuteForever
	}
	return t
}
