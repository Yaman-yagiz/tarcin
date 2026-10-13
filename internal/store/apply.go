// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// PendingOpsRetention is how long an edit, revoke or receipt waits for a
// target message that has not arrived.
const PendingOpsRetention = 30 * 24 * time.Hour

// ApplierOptions configure an EventApplier.
type ApplierOptions struct {
	// MeLID and MePN are this account's own JIDs. Messages sent by this
	// account are attributed to the identity they resolve to. Either may
	// be empty; with both empty, such messages have no sender.
	MeLID, MePN domain.JID
	// Now returns the current time (default time.Now).
	Now func() time.Time
}

// ApplierStats are cumulative EventApplier counters.
type ApplierStats struct {
	Skipped          uint64 // malformed events that were dropped
	SenderMismatches uint64 // redeliveries whose sender differed from the stored one
	Refused          uint64 // edits and revokes by someone who may not make them
}

// EventApplier writes domain events to the message database as
// idempotent upserts keyed by natural IDs, so that duplicated and
// reordered events give the same end state (guarantee G2 in
// docs/architecture/event-pipeline.md):
//
//   - A message is inserted once per (chat, WhatsApp ID). A redelivery
//     can only improve the stored row: the status moves forward, a
//     missing raw payload or media field is filled in, and a placeholder
//     for an undecryptable message is replaced by its content.
//   - Edits, revokes and receipts for a message that has not arrived wait
//     in pending_ops and are applied when it is inserted.
//   - The newest edit wins, a revoke wins over every edit, and the newest
//     reaction of each person wins; removed reactions are kept as
//     tombstones so that a late, older reaction cannot revive them.
//
// A malformed event (no ID, an unknown kind of JID) is skipped and
// counted rather than failed: failing would make the server redeliver it
// forever. It implements Applier and Maintainer.
type EventApplier struct {
	opts           ApplierOptions
	skipped        atomic.Uint64
	senderMismatch atomic.Uint64
	refused        atomic.Uint64
}

// NewEventApplier returns an EventApplier.
func NewEventApplier(opts ApplierOptions) *EventApplier {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &EventApplier{opts: opts}
}

// Stats returns the applier's counters.
func (a *EventApplier) Stats() ApplierStats {
	return ApplierStats{Skipped: a.skipped.Load(), SenderMismatches: a.senderMismatch.Load(), Refused: a.refused.Load()}
}

// errSkip marks a malformed event. Handlers return it before writing.
var errSkip = errors.New("malformed event")

// applyTx is the state of one Apply call. Its caches live no longer than
// the call, which runs in one savepoint.
//
// A run of consecutive messages, such as a history chunk, defers what it
// changes in each chat's row and the read-back of its messages for the UI
// to the end of the run (settle): a chunk of 500 messages of one chat
// updates the chat once, and reads back none of them when there are more
// than Changes keeps.
type applyTx struct {
	ctx     context.Context
	tx      *sql.Tx
	ch      *Changes
	a       *EventApplier
	now     int64
	aliases map[domain.JID]domain.IdentityID
	chats   map[domain.JID]chatRow
	meID    domain.IdentityID

	activity map[domain.ChatID]*activity
	touched  []domain.ChatID                // the chats in activity, in the order first seen
	pending  map[domain.ChatID]bool         // whether operations wait in a chat
	waiting  map[domain.ChatID]int          // how many, once counted in this transaction
	named    map[domain.IdentityID]struct{} // identities a message offered a push name for
}

// activity is what a run of messages changes in one chat.
type activity struct {
	last   domain.MessageID // the newest message inserted, the later one of a tie; 0 if none
	ts     int64            // its time
	unread int
	notes  []note // messages to read back for the UI, in order
}

type note struct {
	id    domain.MessageID
	added bool
}

type chatRow struct {
	id       domain.ChatID
	kind     string
	peer     domain.IdentityID
	lastRead int64
}

// Apply implements Applier.
func (a *EventApplier) Apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *Changes) error {
	t := &applyTx{
		ctx: ctx, tx: tx, ch: ch, a: a, now: a.opts.Now().UnixMilli(),
		aliases: map[domain.JID]domain.IdentityID{}, chats: map[domain.JID]chatRow{},
		activity: map[domain.ChatID]*activity{}, pending: map[domain.ChatID]bool{}, named: map[domain.IdentityID]struct{}{},
		waiting: map[domain.ChatID]int{},
	}
	for _, ev := range events {
		if _, ok := ev.(domain.MessageReceived); !ok {
			// Other events may read what the messages before them changed.
			if err := t.settle(); err != nil {
				return err
			}
		}
		var err error
		switch e := ev.(type) {
		case domain.MessageReceived:
			err = t.message(&e)
		case domain.ReceiptReceived:
			err = t.receiptEvent(&e)
		case domain.MessageSent:
			err = t.messageSent(&e)
		case domain.MessageEdited:
			err = t.editEvent(&e)
		case domain.MessageRevoked:
			err = t.revokeEvent(&e)
		case domain.ReactionReceived:
			err = t.reaction(&e)
		case domain.ChatUpdated:
			err = t.chatUpdated(&e)
		case domain.ContactUpdated:
			err = t.contact(&e)
		case domain.IdentityLinked:
			err = t.link(e.PN, e.LID)
		case domain.GroupMembersChanged:
			err = t.groupMembers(&e)
		case domain.GroupMetaUpdated:
			err = t.groupMeta(&e)
		case domain.MediaChanged:
			err = t.mediaChanged(&e)
		case domain.MediaEvicted:
			err = t.mediaEvicted(&e)
		case domain.MessagesExpired:
			err = t.messagesExpired(&e)
		case domain.ChatDeleted:
			err = t.chatEmptied(e.ChatJID, e.Before, true)
		case domain.ChatCleared:
			err = t.chatEmptied(e.ChatJID, e.Before, false)
		case domain.ChatRead:
			err = t.chatRead(&e)
		case domain.AccountLinked:
			err = t.accountLinked(&e)
		case domain.HistoryQueued:
			err = t.historyQueued(&e)
		case domain.HistoryDone:
			err = t.historyDone(&e)
		case domain.SendsInterrupted:
			err = t.sendsInterrupted(&e)
		}
		if errors.Is(err, errSkip) {
			a.skipped.Add(1)
			continue
		}
		if err != nil {
			return err
		}
	}
	return t.settle()
}

// Maintain implements Maintainer: it prunes pending operations whose
// target never arrived, and rebuilds the full-text index if it was built
// with another textfold version.
func (a *EventApplier) Maintain(ctx context.Context, tx *sql.Tx) error {
	now := a.opts.Now()
	cutoff := now.Add(-PendingOpsRetention).UnixMilli()
	if _, err := tx.ExecContext(ctx, "DELETE FROM pending_ops WHERE ts < ?", cutoff); err != nil {
		return err
	}
	// Reactions to messages that never arrived, after the same wait.
	if _, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE ts < ? AND NOT EXISTS (
		SELECT 1 FROM messages m WHERE m.chat_id = reactions.chat_id AND m.wa_id = reactions.target_wa_id)`, cutoff); err != nil {
		return err
	}
	_, err := rebuildIndexIfStale(ctx, tx, now.UnixMilli())
	return err
}

func (t *applyTx) exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

func (t *applyTx) queryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

// ms converts an event time, using the current time for a missing one.
func (t *applyTx) ms(at time.Time) int64 {
	if at.IsZero() {
		return t.now
	}
	return at.UnixMilli()
}

// chatKindOf derives a chat's kind from its JID.
func chatKindOf(jid domain.JID) (string, bool) {
	if jid.User() == "" {
		return "", false
	}
	switch jid.Server() {
	case domain.ServerPN, domain.ServerLID:
		return "dm", true
	case domain.ServerGroup:
		return "group", true
	case domain.ServerBroadcast:
		return "broadcast", true
	case domain.ServerNewsletter:
		return "newsletter", true
	}
	return "", false
}

// chat returns the chat of a chat JID, creating it when it is new. A
// direct chat is found through its peer's identity, so the PN and the
// LID of one person lead to the same chat.
func (t *applyTx) chat(jid domain.JID) (chatRow, error) {
	if c, ok := t.chats[jid]; ok {
		return c, nil
	}
	kind, ok := chatKindOf(jid)
	if !ok {
		return chatRow{}, errSkip
	}
	key := jid
	var peer domain.IdentityID
	if kind == "dm" {
		var err error
		if peer, err = t.resolve(jid); err != nil {
			return chatRow{}, err
		}
		if key, err = t.canonical(peer); err != nil {
			return chatRow{}, err
		}
	}
	c := chatRow{kind: kind, peer: peer}
	var storedPeer sql.NullInt64
	err := t.queryRow("SELECT id, kind, peer_identity_id, last_read_ts FROM chats WHERE jid = ?", key).
		Scan(&c.id, &c.kind, &storedPeer, &c.lastRead)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var peerArg any
		if peer != 0 {
			peerArg = peer
		}
		if err := t.queryRow("INSERT INTO chats (jid, kind, peer_identity_id, updated_at) VALUES (?, ?, ?, ?) RETURNING id",
			key, kind, peerArg, t.now).Scan(&c.id); err != nil {
			return chatRow{}, err
		}
		t.ch.TouchChat(c.id)
	case err != nil:
		return chatRow{}, err
	default:
		c.peer = domain.IdentityID(storedPeer.Int64)
	}
	t.chats[jid] = c
	return c, nil
}

// messageID finds a message by its WhatsApp ID.
func (t *applyTx) messageID(chat domain.ChatID, waid domain.WAMessageID) (domain.MessageID, bool, error) {
	var id domain.MessageID
	err := t.queryRow("SELECT id FROM messages WHERE chat_id = ? AND wa_id = ?", chat, waid).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return id, true, nil
}

// note defers notify for a message of a run until the run settles.
func (t *applyTx) note(chat domain.ChatID, id domain.MessageID, added bool) {
	a := t.activityOf(chat)
	a.notes = append(a.notes, note{id: id, added: added})
}

func (t *applyTx) activityOf(chat domain.ChatID) *activity {
	a, ok := t.activity[chat]
	if !ok {
		a = &activity{}
		t.activity[chat] = a
		t.touched = append(t.touched, chat)
	}
	return a
}

// settle ends a run of messages: it writes what they changed in their
// chats' rows and records them for the UI.
func (t *applyTx) settle() error {
	for _, chat := range t.touched {
		a := t.activity[chat]
		if a.last != 0 {
			if _, err := t.exec(`UPDATE chats SET
				last_message_id = CASE WHEN ?1 >= last_activity_ts THEN ?2 ELSE last_message_id END,
				last_activity_ts = max(last_activity_ts, ?1),
				unread_count = unread_count + ?3,
				updated_at = ?4
				WHERE id = ?5`, a.ts, a.last, a.unread, t.now, chat); err != nil {
				return err
			}
		}
		if err := t.readBack(chat, a.notes); err != nil {
			return err
		}
	}
	clear(t.activity)
	t.touched = t.touched[:0]
	clear(t.named)
	return nil
}

// readBack records the messages of a chat's notes for the UI, as notify
// does one by one; when they are more than Changes keeps, it marks the
// chat for a reload without reading them.
func (t *applyTx) readBack(chat domain.ChatID, notes []note) error {
	if _, ok := t.ch.Reload[chat]; ok || len(notes) == 0 {
		return nil
	}
	fresh := map[domain.MessageID]struct{}{}
	for _, n := range notes {
		if _, ok := t.ch.where[n.id]; !ok {
			fresh[n.id] = struct{}{}
		}
	}
	if len(t.ch.Added[chat])+len(t.ch.Updated[chat])+len(fresh) > MaxChangedMessages {
		t.ch.ReloadChat(chat)
		return nil
	}
	for _, n := range notes {
		if err := t.notify(chat, n.id, n.added); err != nil {
			return err
		}
	}
	return nil
}

// notify records a new or changed message for the UI. Chats already
// marked for a reload need no rows.
func (t *applyTx) notify(chat domain.ChatID, id domain.MessageID, added bool) error {
	if _, ok := t.ch.Reload[chat]; ok {
		return nil
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+messageColumns+` FROM `+messageTables+` WHERE m.id = ?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	m, err := scanMessage(rows)
	if err != nil {
		return err
	}
	if added {
		t.ch.AddMessage(&m)
	} else {
		t.ch.UpdateMessage(&m)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at.UnixMilli()
}

func (t *applyTx) sender(in *domain.IncomingMessage, chat chatRow) (any, error) {
	switch {
	case in.FromMe:
		id, err := t.me()
		if err != nil || id == 0 {
			return nil, err
		}
		return id, nil
	case in.SenderJID.IsUser():
		id, err := t.resolve(in.SenderJID)
		if err != nil {
			return nil, err
		}
		if _, offered := t.named[id]; in.PushName != "" && !offered {
			// A name from a message only fills a gap: messages arrive out
			// of order, and ContactUpdated carries the current name. Of a
			// run of messages, the first that names the sender fills it.
			t.named[id] = struct{}{}
			res, err := t.exec(`UPDATE identities SET push_name = ?, updated_at = ?
				WHERE id = ? AND push_name IS NULL AND is_me = 0`, in.PushName, t.now, id)
			if err != nil {
				return nil, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				t.ch.TouchIdentity(id)
			}
		}
		return id, nil
	case chat.peer != 0:
		return chat.peer, nil
	}
	return nil, nil
}

func (t *applyTx) message(e *domain.MessageReceived) error {
	in := &e.Message
	if in.WAID == "" {
		return errSkip
	}
	if _, ok := chatKindOf(in.ChatJID); !ok {
		return errSkip
	}
	if err := t.linkPair(in.SenderJID, in.SenderAlt); err != nil {
		return err
	}
	chat, err := t.chat(in.ChatJID)
	if err != nil {
		return err
	}
	sender, err := t.sender(in, chat)
	if err != nil {
		return err
	}
	kind := in.Kind
	if kind == 0 {
		kind = domain.KindUnsupported
	}
	status := domain.StatusDelivered
	switch {
	case in.FromMe && e.Source == domain.SourceSelf:
		status = domain.StatusPending
	case in.FromMe:
		status = domain.StatusSent
	}
	var raw any
	if kind != domain.KindText && len(in.Raw) > 0 {
		raw = in.Raw // plain text keeps no protocol payload
	}
	ts := t.ms(in.Timestamp)

	unread := 0
	if !in.FromMe && e.Source == domain.SourceLive && ts > chat.lastRead && kind != domain.KindSystem {
		unread = 1
	}
	mentions, err := t.mentions(in.Mentions)
	if err != nil {
		return err
	}
	var expires any
	if in.Expiration > 0 {
		expires = ts + in.Expiration.Milliseconds()
	}
	var id domain.MessageID
	err = t.queryRow(`INSERT INTO messages
		(chat_id, wa_id, sender_id, from_me, ts, kind, body, quoted_wa_id, status, flags, raw, unread, mentions, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat_id, wa_id) DO NOTHING RETURNING id`,
		chat.id, in.WAID, sender, in.FromMe, ts, kind.String(), nullString(in.Body),
		nullString(string(in.QuotedWAID)), status, in.Flags, raw, unread, mentions, expires).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return t.redelivered(chat, in, kind, sender, status, raw, mentions)
	}
	if err != nil {
		return err
	}
	if in.Media != nil && kind.HasMedia() {
		if _, err := t.media(id, in.Media); err != nil {
			return err
		}
	}
	if err := t.indexNew(id, in.Body); err != nil {
		return err
	}
	if err := t.applyPending(chat.id, in.WAID, id); err != nil {
		return err
	}
	a := t.activityOf(chat.id)
	if a.last == 0 || ts >= a.ts {
		a.last, a.ts = id, ts
	}
	a.unread += unread
	t.ch.TouchChat(chat.id)
	t.note(chat.id, id, true)
	return nil
}

// messagesExpired deletes the disappearing messages whose time is up:
// their rows, their index entries, media and reactions, and recounts the
// chats they were in. Chats that lost their last message are reloaded.
func (t *applyTx) messagesExpired(e *domain.MessagesExpired) error {
	files, err := t.localNames(localFiles+"m.expires_at IS NOT NULL AND m.expires_at <= ?", t.ms(e.Now))
	if err != nil {
		return err
	}
	rows, err := t.tx.QueryContext(t.ctx, `DELETE FROM messages WHERE expires_at IS NOT NULL AND expires_at <= ?
		RETURNING chat_id, wa_id, unread`, t.ms(e.Now))
	if err != nil {
		return err
	}
	type gone struct {
		chat   domain.ChatID
		waid   string
		unread bool
	}
	var all []gone
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.chat, &g.waid, &g.unread); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, g)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(all) > 0 {
		t.ch.Erased = true
	}
	if err := t.unlinkUnused(files); err != nil {
		return err
	}
	chats := map[domain.ChatID]int{}
	for _, g := range all {
		if _, err := t.exec("DELETE FROM reactions WHERE chat_id = ? AND target_wa_id = ?", g.chat, g.waid); err != nil {
			return err
		}
		if g.unread {
			chats[g.chat]++
		} else if _, ok := chats[g.chat]; !ok {
			chats[g.chat] = 0
		}
	}
	for chat, unread := range chats {
		if _, err := t.exec(`UPDATE chats SET unread_count = max(unread_count - ?1, 0),
			last_message_id = (SELECT id FROM messages WHERE chat_id = ?2 ORDER BY ts DESC, id DESC LIMIT 1),
			updated_at = ?3 WHERE id = ?2`, unread, chat, t.now); err != nil {
			return err
		}
		t.ch.ReloadChat(chat)
		t.ch.TouchChat(chat)
	}
	return nil
}

// chatEmptied deletes a chat's messages up to before, with their index
// entries, media and reactions; a deleted chat with nothing newer left
// goes too.
func (t *applyTx) chatEmptied(jid domain.JID, before time.Time, deleteChat bool) error {
	if _, ok := chatKindOf(jid); !ok || before.IsZero() {
		return errSkip
	}
	chat, err := t.chat(jid)
	if err != nil {
		return err
	}
	cut := before.UnixMilli()
	files, err := t.localNames(localFiles+"m.chat_id = ? AND m.ts <= ?", chat.id, cut)
	if err != nil {
		return err
	}
	if _, err := t.exec(`DELETE FROM reactions WHERE chat_id = ?1
		AND target_wa_id IN (SELECT wa_id FROM messages WHERE chat_id = ?1 AND ts <= ?2)`, chat.id, cut); err != nil {
		return err
	}
	res, err := t.exec("DELETE FROM messages WHERE chat_id = ? AND ts <= ?", chat.id, cut)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		t.ch.Erased = true
	}
	if err := t.unlinkUnused(files); err != nil {
		return err
	}
	var left int
	if err := t.queryRow("SELECT count(*) FROM messages WHERE chat_id = ?", chat.id).Scan(&left); err != nil {
		return err
	}
	if deleteChat && left == 0 {
		if _, err := t.exec("DELETE FROM chats WHERE id = ?", chat.id); err != nil {
			return err
		}
		delete(t.chats, jid)
		t.ch.RemoveChat(chat.id)
		return nil
	}
	if _, err := t.exec(`UPDATE chats SET
		unread_count = (SELECT count(*) FROM messages WHERE chat_id = ?1 AND unread = 1),
		last_message_id = (SELECT id FROM messages WHERE chat_id = ?1 ORDER BY ts DESC, id DESC LIMIT 1),
		updated_at = ?2 WHERE id = ?1`, chat.id, t.now); err != nil {
		return err
	}
	t.ch.ReloadChat(chat.id)
	t.ch.TouchChat(chat.id)
	return nil
}

// localFiles selects the cache files of messages m; a query appends its
// condition on m.
const localFiles = `SELECT DISTINCT md.local_name FROM media md JOIN messages m ON m.id = md.message_id
	WHERE md.local_name IS NOT NULL AND `

// localNames returns the cache files a localFiles query selects: read
// before a deletion, for unlinkUnused after it.
func (t *applyTx) localNames(query string, args ...any) ([]string, error) {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// unlinkUnused records the files among names that no stored message uses
// any more: a file holds one content, which several messages may share.
func (t *applyTx) unlinkUnused(names []string) error {
	for _, name := range names {
		var used bool
		if err := t.queryRow("SELECT EXISTS (SELECT 1 FROM media WHERE local_name = ?)", name).Scan(&used); err != nil {
			return err
		}
		if !used {
			t.ch.Unlink(name)
		}
	}
	return nil
}

// mentions returns the JSON array of the mentioned JIDs to store, or nil
// for none, and makes sure each is a known person, so that a name
// learned later shows in the mention.
// maxMentions bounds the people one message mentions: a group has at
// most 1024 members, and a sender must not make a message costly.
const maxMentions = 1024

// mentions returns the JSON list of the people a message mentions, at
// most maxMentions. Reads find them through jid_aliases, so a mention of
// someone unknown creates no identity.
func (t *applyTx) mentions(jids []domain.JID) (any, error) {
	var keep []string
	seen := make(map[domain.JID]bool, min(len(jids), maxMentions))
	for _, j := range jids {
		if len(keep) == maxMentions {
			break
		}
		if !j.IsUser() || seen[j] {
			continue
		}
		seen[j] = true
		keep = append(keep, string(j))
	}
	if len(keep) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(keep)
	return string(b), err
}

// redelivered merges a message that is already stored.
func (t *applyTx) redelivered(chat chatRow, in *domain.IncomingMessage, kind domain.MessageKind,
	sender any, status domain.MessageStatus, raw, mentions any,
) error {
	var (
		id                      domain.MessageID
		storedSender            sql.NullInt64
		storedStatus            int64
		storedKind              string
		fromMe, hasRaw, revoked bool
	)
	if err := t.queryRow(`SELECT id, sender_id, from_me, status, kind, raw IS NOT NULL, revoked_at IS NOT NULL
		FROM messages WHERE chat_id = ? AND wa_id = ?`, chat.id, in.WAID).
		Scan(&id, &storedSender, &fromMe, &storedStatus, &storedKind, &hasRaw, &revoked); err != nil {
		return err
	}
	// The same ID from someone else is another message: every member of
	// a chat sees a message's ID, so a placeholder (a view-once message,
	// say) must never take content from a different sender.
	s, known := sender.(domain.IdentityID)
	if fromMe != in.FromMe || known && storedSender.Valid && domain.IdentityID(storedSender.Int64) != s {
		t.a.senderMismatch.Add(1) // counted, never logged with content
		return nil
	}
	changed := false
	cur := domain.MessageStatus(u8(storedStatus))
	next := cur.Advance(status)
	// A revoked message never gets content back.
	upgrade := storedKind == domain.KindUnsupported.String() && kind != domain.KindUnsupported && !revoked
	fillRaw := !hasRaw && raw != nil && !revoked
	switch {
	case upgrade:
		if _, err := t.exec(`UPDATE messages SET kind = ?, body = ?, quoted_wa_id = ?, flags = ?, raw = ?,
			mentions = ?, status = ?, revision = revision + 1 WHERE id = ?`,
			kind.String(), nullString(in.Body), nullString(string(in.QuotedWAID)), in.Flags, raw, mentions, next, id); err != nil {
			return err
		}
		if err := t.reindex(id, in.Body); err != nil {
			return err
		}
		changed = true
	case next != cur || fillRaw:
		if !fillRaw {
			raw = nil
		}
		if _, err := t.exec(`UPDATE messages SET status = ?, raw = coalesce(raw, ?), revision = revision + 1
			WHERE id = ?`, next, raw, id); err != nil {
			return err
		}
		changed = true
	}
	if in.Media != nil && kind.HasMedia() && !revoked {
		filled, err := t.media(id, in.Media)
		if err != nil {
			return err
		}
		if filled && !changed {
			if _, err := t.exec("UPDATE messages SET revision = revision + 1 WHERE id = ?", id); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		t.note(chat.id, id, false)
	}
	return nil
}

// media stores a media descriptor, or fills in the fields a stored one
// lacks. The download state is local and never overwritten. It reports
// whether anything changed.
func (t *applyTx) media(id domain.MessageID, m *domain.Media) (bool, error) {
	res, err := t.exec(`INSERT INTO media (message_id, mime, size, file_name, width, height, duration_ms, state, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (message_id) DO UPDATE SET
			mime = coalesce(media.mime, excluded.mime),
			size = coalesce(media.size, excluded.size),
			file_name = coalesce(media.file_name, excluded.file_name),
			width = coalesce(media.width, excluded.width),
			height = coalesce(media.height, excluded.height),
			duration_ms = coalesce(media.duration_ms, excluded.duration_ms),
			updated_at = excluded.updated_at
		WHERE (media.mime IS NULL AND excluded.mime IS NOT NULL)
			OR (media.size IS NULL AND excluded.size IS NOT NULL)
			OR (media.file_name IS NULL AND excluded.file_name IS NOT NULL)
			OR (media.width IS NULL AND excluded.width IS NOT NULL)
			OR (media.height IS NULL AND excluded.height IS NOT NULL)
			OR (media.duration_ms IS NULL AND excluded.duration_ms IS NOT NULL)`,
		id, nullString(m.MIME), nullInt(m.Size), nullString(m.FileName), nullInt(int64(m.Width)),
		nullInt(int64(m.Height)), nullInt(m.Duration.Milliseconds()), m.State, t.now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// mediaChanged records the local state of a message's media. Media that
// is not stored, or a message that has none, is skipped: a download is
// only started for media the store describes.
func (t *applyTx) mediaChanged(e *domain.MediaChanged) error {
	if e.TargetWAID == "" || (e.State == domain.MediaLocal && e.LocalName == "") {
		return errSkip
	}
	if _, ok := chatKindOf(e.ChatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	id, ok, err := t.messageID(chat.id, e.TargetWAID)
	if err != nil || !ok {
		return err
	}
	var local, sum any
	if e.State == domain.MediaLocal {
		local, sum = e.LocalName, e.SHA256
	}
	res, err := t.exec(`UPDATE media SET state = ?, local_name = ?, sha256 = coalesce(?, sha256), updated_at = ?
		WHERE message_id = ? AND (state IS NOT ? OR local_name IS NOT ?)`, e.State, local, sum, t.now, id, e.State, local)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if _, err := t.exec("UPDATE messages SET revision = revision + 1 WHERE id = ?", id); err != nil {
		return err
	}
	return t.notify(chat.id, id, false)
}

// mediaEvicted marks the media a deleted cache file held as remote.
func (t *applyTx) mediaEvicted(e *domain.MediaEvicted) error {
	if e.LocalName == "" {
		return errSkip
	}
	rows, err := t.tx.QueryContext(t.ctx, `UPDATE media SET state = ?, local_name = NULL, updated_at = ?
		WHERE local_name = ? RETURNING message_id`, domain.MediaRemote, t.now, e.LocalName)
	if err != nil {
		return err
	}
	var ids []domain.MessageID
	for rows.Next() {
		var id domain.MessageID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		var chat domain.ChatID
		if err := t.queryRow("UPDATE messages SET revision = revision + 1 WHERE id = ? RETURNING chat_id", id).Scan(&chat); err != nil {
			return err
		}
		if err := t.notify(chat, id, false); err != nil {
			return err
		}
	}
	return nil
}

// Pending operations wait for their target message.
const (
	opReceipt = "receipt"
	opEdit    = "edit"
	opRevoke  = "revoke"
)

type pendingOp struct {
	Status  domain.MessageStatus `json:"status,omitempty"`
	Body    string               `json:"body,omitempty"`
	At      int64                `json:"at,omitempty"`
	Actor   domain.JID           `json:"actor,omitempty"`
	ByAdmin bool                 `json:"by_admin,omitempty"`
}

// maxPendingPerChat bounds the operations that wait in one chat for
// targets that have not arrived; beyond it, new ones are dropped.
const maxPendingPerChat = 10_000

// addPending stores an operation until its target arrives. Its time is
// at most now, so that the retention prune removes it even if the sender
// claimed a later time; a repeated operation is stored once.
func (t *applyTx) addPending(chat domain.ChatID, target domain.WAMessageID, kind string, op pendingOp, ts int64) error {
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	n, ok := t.waiting[chat]
	if !ok {
		if err := t.queryRow("SELECT count(*) FROM pending_ops WHERE chat_id = ?", chat).Scan(&n); err != nil {
			return err
		}
	}
	if n >= maxPendingPerChat {
		t.waiting[chat] = n
		t.a.skipped.Add(1)
		return nil
	}
	res, err := t.exec(`INSERT INTO pending_ops (chat_id, target_wa_id, kind, payload, ts)
		SELECT ?1, ?2, ?3, ?4, ?5 WHERE NOT EXISTS (SELECT 1 FROM pending_ops
			WHERE chat_id = ?1 AND target_wa_id = ?2 AND kind = ?3 AND payload = ?4)`,
		chat, target, kind, payload, min(ts, t.now))
	if err != nil {
		return err
	}
	if added, _ := res.RowsAffected(); added > 0 {
		n++
	}
	t.waiting[chat] = n
	t.pending[chat] = true
	return nil
}

// hasPending reports whether operations wait in a chat: a run of messages
// looks for its messages' operations only in chats that have some.
func (t *applyTx) hasPending(chat domain.ChatID) (bool, error) {
	if p, ok := t.pending[chat]; ok {
		return p, nil
	}
	var p bool
	if err := t.queryRow("SELECT EXISTS (SELECT 1 FROM pending_ops WHERE chat_id = ?)", chat).Scan(&p); err != nil {
		return false, err
	}
	t.pending[chat] = p
	return p, nil
}

// applyPending applies and deletes the operations that waited for a new
// message.
func (t *applyTx) applyPending(chat domain.ChatID, waid domain.WAMessageID, id domain.MessageID) error {
	if p, err := t.hasPending(chat); err != nil || !p {
		return err
	}
	return t.applyOps(chat, waid, id)
}

// applyOps applies and deletes the operations that wait for a stored
// message.
func (t *applyTx) applyOps(chat domain.ChatID, waid domain.WAMessageID, id domain.MessageID) error {
	type stored struct {
		kind    string
		payload []byte
	}
	rows, err := t.tx.QueryContext(t.ctx, `SELECT kind, payload FROM pending_ops
		WHERE chat_id = ? AND target_wa_id = ? ORDER BY ts, id`, chat, waid)
	if err != nil {
		return err
	}
	var ops []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.kind, &s.payload); err != nil {
			_ = rows.Close()
			return err
		}
		ops = append(ops, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil || len(ops) == 0 {
		return err
	}
	for _, s := range ops {
		var op pendingOp
		if json.Unmarshal(s.payload, &op) != nil {
			continue // written by a newer tarcin
		}
		if s.kind != opReceipt {
			ok, err := t.mayChange(chat, id, op.Actor, op.ByAdmin)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
		}
		switch s.kind {
		case opReceipt:
			_, err = t.receipt(id, op.Actor, op.Status)
		case opEdit:
			_, err = t.edit(id, op.Body, op.At)
		case opRevoke:
			_, err = t.revoke(id, op.At)
		}
		if err != nil {
			return err
		}
	}
	_, err = t.exec("DELETE FROM pending_ops WHERE chat_id = ? AND target_wa_id = ?", chat, waid)
	return err
}

// receipt advances a message's status.
// receipt applies a receipt from a recipient of an own message. In a
// direct chat it moves the message's status on; in a group the message
// is delivered, read or played once every member is, as the phone shows
// it. A receipt for someone else's message changes nothing.
func (t *applyTx) receipt(id domain.MessageID, from domain.JID, status domain.MessageStatus) (bool, error) {
	var (
		stored int64
		fromMe bool
		kind   string
		chat   domain.ChatID
	)
	if err := t.queryRow(`SELECT m.status, m.from_me, c.kind, m.chat_id FROM messages m
		JOIN chats c ON c.id = m.chat_id WHERE m.id = ?`, id).Scan(&stored, &fromMe, &kind, &chat); err != nil {
		return false, err
	}
	if !fromMe {
		return false, nil
	}
	// Sent and failed come from this device's sender: only what the
	// recipients report is counted per member.
	if kind == "group" && status >= domain.StatusDelivered && status <= domain.StatusPlayed {
		var err error
		if status, err = t.groupReceipt(id, chat, from, status); err != nil {
			return false, err
		}
	}
	return t.advance(id, domain.MessageStatus(u8(stored)), status)
}

// status moves a message's own status on: the server accepted it, or a
// duplicate of it went further.
func (t *applyTx) status(id domain.MessageID, status domain.MessageStatus) error {
	var stored int64
	if err := t.queryRow("SELECT status FROM messages WHERE id = ?", id).Scan(&stored); err != nil {
		return err
	}
	_, err := t.advance(id, domain.MessageStatus(u8(stored)), status)
	return err
}

// advance moves a message's status on from cur to status, never back.
func (t *applyTx) advance(id domain.MessageID, cur, status domain.MessageStatus) (bool, error) {
	next := cur.Advance(status)
	if next == cur {
		return false, nil
	}
	_, err := t.exec("UPDATE messages SET status = ?, revision = revision + 1 WHERE id = ?", next, id)
	return err == nil, err
}

// groupReceipt records a member's receipt for an own group message and
// returns the status every member has reached: delivered, read or
// played once all of them are, else sent. While the members are not
// known yet, a receipt counts as delivered at most: a message is never
// shown read by everyone on one member's word.
func (t *applyTx) groupReceipt(id domain.MessageID, chat domain.ChatID, from domain.JID, status domain.MessageStatus) (domain.MessageStatus, error) {
	if from.IsUser() {
		who, err := t.resolve(from)
		if err != nil {
			return 0, err
		}
		if _, err := t.exec(`INSERT INTO receipts (message_id, identity_id, status) VALUES (?1, ?2, ?3)
			ON CONFLICT (message_id, identity_id) DO UPDATE SET status = max(status, excluded.status)`,
			id, who, status); err != nil {
			return 0, err
		}
	}
	agg, known, err := t.groupStatus(id, chat)
	if err != nil || known {
		return agg, err
	}
	return min(status, domain.StatusDelivered), nil
}

// groupStatus returns the status every member of a group has reached for
// an own message: delivered, read or played once all of them are, else
// sent; known is false while the members are not.
func (t *applyTx) groupStatus(id domain.MessageID, chat domain.ChatID) (status domain.MessageStatus, known bool, err error) {
	var members, delivered, read, played int
	if err := t.queryRow(`SELECT count(*), coalesce(sum(r.status >= ?3), 0), coalesce(sum(r.status >= ?4), 0),
			coalesce(sum(r.status >= ?5), 0)
		FROM group_members gm JOIN identities i ON i.id = gm.identity_id AND NOT i.is_me
		LEFT JOIN receipts r ON r.message_id = ?1 AND r.identity_id = gm.identity_id
		WHERE gm.chat_id = ?2`, id, chat, domain.StatusDelivered, domain.StatusRead, domain.StatusPlayed).
		Scan(&members, &delivered, &read, &played); err != nil {
		return 0, false, err
	}
	switch {
	case members == 0:
		return 0, false, nil
	case played == members:
		return domain.StatusPlayed, true, nil
	case read == members:
		return domain.StatusRead, true, nil
	case delivered == members:
		return domain.StatusDelivered, true, nil
	}
	return domain.StatusSent, true, nil
}

// edit replaces a message's body unless it is revoked or already has a
// newer edit.
func (t *applyTx) edit(id domain.MessageID, body string, at int64) (bool, error) {
	res, err := t.exec(`UPDATE messages SET body = ?, edited_at = ?, revision = revision + 1
		WHERE id = ? AND revoked_at IS NULL AND (edited_at IS NULL OR edited_at < ?)`,
		nullString(body), at, id, at)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	return true, t.reindex(id, body)
}

// revoke deletes a message's content for everyone. The row stays as a
// placeholder. Its edit time goes too: otherwise it would depend on
// whether an edit arrived before or after the revoke. Of several revokes
// the earliest time is kept.
func (t *applyTx) revoke(id domain.MessageID, at int64) (bool, error) {
	res, err := t.exec(`UPDATE messages SET revoked_at = ?1, body = NULL, raw = NULL, edited_at = NULL,
		revision = revision + 1 WHERE id = ?2 AND (revoked_at IS NULL OR revoked_at > ?1)`, at, id)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	files, err := t.localNames(localFiles+"m.id = ?", id)
	if err != nil {
		return false, err
	}
	if _, err := t.exec("DELETE FROM media WHERE message_id = ?", id); err != nil {
		return false, err
	}
	if err := t.unlinkUnused(files); err != nil {
		return false, err
	}
	t.ch.Erased = true
	return true, t.unindex(id)
}

// targetOp applies op to a stored message, or queues it until the
// message arrives.
func (t *applyTx) targetOp(chatJID domain.JID, target domain.WAMessageID, kind string, op pendingOp, ts int64,
	apply func(domain.MessageID) (bool, error),
) error {
	if target == "" {
		return errSkip
	}
	if _, ok := chatKindOf(chatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(chatJID)
	if err != nil {
		return err
	}
	id, ok, err := t.messageID(chat.id, target)
	if err != nil {
		return err
	}
	if !ok {
		return t.addPending(chat.id, target, kind, op, ts)
	}
	changed, err := apply(id)
	if err != nil || !changed {
		return err
	}
	if kind != opReceipt {
		t.ch.TouchChat(chat.id) // the chat-list preview may show this message
	}
	return t.notify(chat.id, id, false)
}

func (t *applyTx) receiptEvent(e *domain.ReceiptReceived) error {
	if _, ok := chatKindOf(e.ChatJID); !ok || len(e.WAIDs) == 0 {
		return errSkip
	}
	ts := t.ms(e.Timestamp)
	for _, waid := range e.WAIDs {
		if waid == "" {
			continue
		}
		err := t.targetOp(e.ChatJID, waid, opReceipt, pendingOp{Status: e.Status, Actor: e.From}, ts,
			func(id domain.MessageID) (bool, error) { return t.receipt(id, e.From, e.Status) })
		if err != nil {
			return err
		}
	}
	return nil
}

// messageSent records that the server accepted an own message: its
// status moves to sent, and its time to the server's stamp, by which
// every client orders messages. A clock that is off would otherwise
// place the replies to it before it. A disappearing message keeps its
// timer from the new time. If the move passes another message, the chat
// reloads; else the message is updated in place.
func (t *applyTx) messageSent(e *domain.MessageSent) error {
	if e.WAID == "" || e.At.IsZero() {
		return errSkip
	}
	if _, ok := chatKindOf(e.ChatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	var id domain.MessageID
	var old int64
	err = t.queryRow("SELECT id, ts FROM messages WHERE chat_id = ? AND wa_id = ? AND from_me = 1", chat.id, e.WAID).Scan(&id, &old)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // not stored here: nothing to move
	case err != nil:
		return err
	}
	if err := t.status(id, domain.StatusSent); err != nil {
		return err
	}
	at := e.At.UnixMilli()
	if at == old {
		return t.notify(chat.id, id, false)
	}
	var passes bool
	if err := t.queryRow(`SELECT EXISTS (SELECT 1 FROM messages WHERE chat_id = ?1 AND id != ?2 AND ts BETWEEN min(?3, ?4) AND max(?3, ?4))`,
		chat.id, id, old, at).Scan(&passes); err != nil {
		return err
	}
	if _, err := t.exec(`UPDATE messages SET ts = ?1, expires_at = expires_at - ?2 + ?1, revision = revision + 1 WHERE id = ?3`,
		at, old, id); err != nil {
		return err
	}
	if _, err := t.exec(`UPDATE chats SET
		last_activity_ts = coalesce((SELECT max(ts) FROM messages WHERE chat_id = ?1), 0),
		last_message_id = (SELECT id FROM messages WHERE chat_id = ?1 ORDER BY ts DESC, id DESC LIMIT 1),
		updated_at = ?2 WHERE id = ?1`, chat.id, t.now); err != nil {
		return err
	}
	t.ch.TouchChat(chat.id)
	if passes {
		t.ch.ReloadChat(chat.id)
		return nil
	}
	return t.notify(chat.id, id, false)
}

func (t *applyTx) editEvent(e *domain.MessageEdited) error {
	at := t.ms(e.EditedAt)
	return t.targetOp(e.ChatJID, e.TargetWAID, opEdit, pendingOp{Body: e.Body, At: at, Actor: e.Actor}, at,
		func(id domain.MessageID) (bool, error) {
			if ok, err := t.mayChangeIn(e.ChatJID, id, e.Actor, false); err != nil || !ok {
				return false, err
			}
			return t.edit(id, e.Body, at)
		})
}

func (t *applyTx) revokeEvent(e *domain.MessageRevoked) error {
	at := t.ms(e.RevokedAt)
	return t.targetOp(e.ChatJID, e.TargetWAID, opRevoke, pendingOp{At: at, Actor: e.Actor, ByAdmin: e.ByAdmin}, at,
		func(id domain.MessageID) (bool, error) {
			if ok, err := t.mayChangeIn(e.ChatJID, id, e.Actor, e.ByAdmin); err != nil || !ok {
				return false, err
			}
			return t.revoke(id, at)
		})
}

func (t *applyTx) mayChangeIn(chatJID domain.JID, id domain.MessageID, actor domain.JID, byAdmin bool) (bool, error) {
	chat, err := t.chat(chatJID)
	if err != nil {
		return false, err
	}
	return t.mayChange(chat.id, id, actor, byAdmin)
}

// mayChange reports whether actor may edit or delete message id: its
// sender may, and so may an admin of the group with a revoke marked as an
// admin's. The protocol is end-to-end encrypted, so the server cannot
// check this; a client must, or any member could rewrite or hide other
// people's messages. An empty actor is tarcin itself.
func (t *applyTx) mayChange(chat domain.ChatID, id domain.MessageID, actor domain.JID, byAdmin bool) (bool, error) {
	if actor.IsZero() {
		return true, nil
	}
	who, known, err := t.alias(actor)
	if err != nil {
		return false, err
	}
	if known {
		var sender sql.NullInt64
		if err := t.queryRow("SELECT sender_id FROM messages WHERE id = ?", id).Scan(&sender); err != nil {
			return false, err
		}
		if sender.Valid && domain.IdentityID(sender.Int64) == who {
			return true, nil
		}
		if byAdmin {
			var role int
			err := t.queryRow("SELECT role FROM group_members WHERE chat_id = ? AND identity_id = ?", chat, who).Scan(&role)
			switch {
			case err == nil && role >= int(domain.RoleAdmin):
				return true, nil
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				return false, err
			}
		}
	}
	t.a.refused.Add(1)
	return false, nil
}

func (t *applyTx) reaction(e *domain.ReactionReceived) error {
	if e.TargetWAID == "" || !e.SenderJID.IsUser() {
		return errSkip
	}
	if _, ok := chatKindOf(e.ChatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	sender, err := t.resolve(e.SenderJID)
	if err != nil {
		return err
	}
	// The newest reaction wins; equal times are broken by the emoji so
	// that the outcome does not depend on arrival order.
	res, err := t.exec(`INSERT INTO reactions (chat_id, target_wa_id, sender_id, emoji, ts) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (chat_id, target_wa_id, sender_id) DO UPDATE SET emoji = excluded.emoji, ts = excluded.ts
		WHERE excluded.ts > reactions.ts OR (excluded.ts = reactions.ts AND excluded.emoji > reactions.emoji)`,
		chat.id, e.TargetWAID, sender, e.Emoji, t.ms(e.Timestamp))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	id, ok, err := t.messageID(chat.id, e.TargetWAID)
	if err != nil || !ok {
		return err
	}
	if _, err := t.exec("UPDATE messages SET revision = revision + 1 WHERE id = ?", id); err != nil {
		return err
	}
	return t.notify(chat.id, id, false)
}

func (t *applyTx) chatUpdated(e *domain.ChatUpdated) error {
	if _, ok := chatKindOf(e.ChatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	// ChatFieldKind is not applied: the kind follows from the JID.
	for _, u := range []struct {
		field domain.ChatFields
		query string
		arg   any
	}{
		{domain.ChatFieldTitle, "UPDATE chats SET title = ?, updated_at = ? WHERE id = ?", nullString(e.Title)},
		{domain.ChatFieldPinned, "UPDATE chats SET pinned_at = ?, updated_at = ? WHERE id = ?", nullTime(e.PinnedAt)},
		{domain.ChatFieldArchived, "UPDATE chats SET archived = ?, updated_at = ? WHERE id = ?", e.Archived},
		{domain.ChatFieldMuted, "UPDATE chats SET muted_until = ?, updated_at = ? WHERE id = ?", mutedArg(e.MutedUntil)},
		{domain.ChatFieldEphemeral, "UPDATE chats SET ephemeral_secs = ?, updated_at = ? WHERE id = ?", nullInt(int64(e.EphemeralTimer / time.Second))},
		{domain.ChatFieldUnread, "UPDATE chats SET unread_count = ?, updated_at = ? WHERE id = ?", max(e.UnreadCount, 0)},
		{domain.ChatFieldHistoryComplete, "UPDATE chats SET history_complete = ?, updated_at = ? WHERE id = ?", e.HistoryComplete},
	} {
		if !e.Fields.Has(u.field) {
			continue
		}
		if _, err := t.exec(u.query, u.arg, t.now, chat.id); err != nil {
			return err
		}
	}
	t.ch.TouchChat(chat.id)
	return nil
}

func mutedArg(until time.Time) any {
	switch {
	case until.IsZero():
		return nil
	case !until.Before(domain.MuteForever):
		return mutedForever
	}
	return until.UnixMilli()
}

// sendsInterrupted marks own messages that were still pending when tarcin
// stopped as failed.
func (t *applyTx) sendsInterrupted(e *domain.SendsInterrupted) error {
	rows, err := t.tx.QueryContext(t.ctx, `UPDATE messages SET status = ?, revision = revision + 1
		WHERE from_me = 1 AND status = ? AND ts < ? RETURNING id, chat_id`,
		domain.StatusFailed, domain.StatusPending, t.ms(e.Before))
	if err != nil {
		return err
	}
	type changed struct {
		id   domain.MessageID
		chat domain.ChatID
	}
	var all []changed
	for rows.Next() {
		var c changed
		if err := rows.Scan(&c.id, &c.chat); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range all {
		if err := t.notify(c.chat, c.id, false); err != nil {
			return err
		}
	}
	return nil
}

// historyPrefix is the sync_state key prefix of queued history syncs.
const historyPrefix = "history.pending/"

func (t *applyTx) historyQueued(e *domain.HistoryQueued) error {
	if e.ID == "" || len(e.Notification) == 0 {
		return errSkip
	}
	_, err := t.exec(`INSERT INTO sync_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO NOTHING`, historyPrefix+e.ID, e.Notification, t.now)
	return err
}

func (t *applyTx) historyDone(e *domain.HistoryDone) error {
	if e.ID == "" {
		return errSkip
	}
	_, err := t.exec("DELETE FROM sync_state WHERE key = ?", historyPrefix+e.ID)
	return err
}

// accountLinked marks the linked account's identity as mine, so that its
// messages from other devices show as "You".
func (t *applyTx) accountLinked(e *domain.AccountLinked) error {
	if !e.LID.IsLID() && !e.PN.IsPN() {
		return errSkip
	}
	id, err := t.markMe(e.PN, e.LID)
	if err != nil {
		return err
	}
	// A database kept from another account: that account is someone
	// else now.
	rows, err := t.tx.QueryContext(t.ctx, "UPDATE identities SET is_me = 0 WHERE is_me = 1 AND id != ? RETURNING id", id)
	if err != nil {
		return err
	}
	var others []domain.IdentityID
	for rows.Next() {
		var other domain.IdentityID
		if err := rows.Scan(&other); err != nil {
			_ = rows.Close()
			return err
		}
		others = append(others, other)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, other := range others {
		t.ch.TouchIdentity(other)
	}
	if e.BusinessName != "" {
		_, err = t.exec("UPDATE identities SET business_name = coalesce(business_name, ?) WHERE id = ?", e.BusinessName, t.meID)
	}
	return err
}

// chatRead marks a chat read up to the read time: the messages up to it
// are no longer unread, and the count becomes that of the unread
// messages after it, which also drops the count a history sync set.
// Messages newer than the read stay unread whichever arrived first; an
// older read changes nothing.
func (t *applyTx) chatRead(e *domain.ChatRead) error {
	if _, ok := chatKindOf(e.ChatJID); !ok {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	at := t.ms(e.ReadAt)
	if at <= chat.lastRead {
		return nil
	}
	clear(t.chats) // cached rows hold the old read time
	if _, err := t.exec("UPDATE messages SET unread = 0 WHERE chat_id = ? AND unread = 1 AND ts <= ?", chat.id, at); err != nil {
		return err
	}
	if _, err := t.exec(`UPDATE chats SET last_read_ts = ?2, updated_at = ?3,
		unread_count = (SELECT count(*) FROM messages WHERE chat_id = ?1 AND unread = 1)
		WHERE id = ?1`, chat.id, at, t.now); err != nil {
		return err
	}
	t.ch.TouchChat(chat.id)
	return nil
}

func (t *applyTx) contact(e *domain.ContactUpdated) error {
	if !e.JID.IsUser() {
		return errSkip
	}
	id, err := t.resolve(e.JID)
	if err != nil {
		return err
	}
	res, err := t.exec(`UPDATE identities SET
			contact_name = coalesce(?2, contact_name),
			push_name = CASE WHEN is_me THEN push_name ELSE coalesce(?3, push_name) END,
			business_name = coalesce(?4, business_name),
			updated_at = ?5
		WHERE id = ?1 AND (contact_name IS NOT coalesce(?2, contact_name)
			OR (is_me = 0 AND push_name IS NOT coalesce(?3, push_name))
			OR business_name IS NOT coalesce(?4, business_name))`,
		id, nullString(e.ContactName), nullString(e.PushName), nullString(e.BusinessName), t.now)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	t.ch.TouchIdentity(id)
	// A direct chat's title is its peer's name.
	jid, err := t.canonical(id)
	if err != nil {
		return err
	}
	var chat domain.ChatID
	err = t.queryRow("SELECT id FROM chats WHERE jid = ? AND kind = 'dm'", jid).Scan(&chat)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	t.ch.TouchChat(chat)
	return nil
}

func (t *applyTx) groupMembers(e *domain.GroupMembersChanged) error {
	if !e.ChatJID.IsGroup() || e.ChatJID.User() == "" {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	// Link first: a link can merge identities resolved earlier.
	for _, m := range e.Members {
		if err := t.linkPair(m.JID, m.AltJID); err != nil {
			return err
		}
	}
	ids := make([]domain.IdentityID, 0, len(e.Members))
	for _, m := range e.Members {
		if !m.JID.IsUser() {
			continue
		}
		id, err := t.resolve(m.JID)
		if err != nil {
			return err
		}
		if _, err := t.exec(`INSERT INTO group_members (chat_id, identity_id, role, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (chat_id, identity_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at
			WHERE role IS NOT excluded.role`, chat.id, id, m.Role, t.now); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for _, jid := range e.Left {
		id, ok, err := t.alias(jid)
		if err != nil {
			return err
		}
		if ok {
			if _, err := t.exec("DELETE FROM group_members WHERE chat_id = ? AND identity_id = ?", chat.id, id); err != nil {
				return err
			}
		}
	}
	t.ch.TouchGroup(chat.id)
	if e.Full {
		if _, err := t.exec(`DELETE FROM group_members WHERE chat_id = ?
			AND identity_id NOT IN (SELECT value FROM json_each(?))`, chat.id, idList(ids)); err != nil {
			return err
		}
		if _, err := t.exec(`INSERT INTO group_meta (chat_id, participant_count, fetched_at) VALUES (?, ?, ?)
			ON CONFLICT (chat_id) DO UPDATE SET participant_count = excluded.participant_count, fetched_at = excluded.fetched_at`,
			chat.id, len(ids), t.now); err != nil {
			return err
		}
	}
	return t.recountReceipts(chat.id)
}

// recountLimit bounds the own messages a member change recounts.
const recountLimit = 500

// recountReceipts counts the receipts of a group's newest own messages
// that are not read yet against its members again, after they changed:
// receipts may have come before the members were known, and a member
// who left no longer holds a message back.
func (t *applyTx) recountReceipts(chat domain.ChatID) error {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT m.id, m.status FROM messages m
		WHERE m.chat_id = ? AND m.from_me AND m.status < ?
			AND EXISTS (SELECT 1 FROM receipts r WHERE r.message_id = m.id)
		ORDER BY m.id DESC LIMIT ?`, chat, domain.StatusRead, recountLimit)
	if err != nil {
		return err
	}
	type own struct {
		id     domain.MessageID
		status int64
	}
	var msgs []own
	for rows.Next() {
		var o own
		if err := rows.Scan(&o.id, &o.status); err != nil {
			_ = rows.Close()
			return err
		}
		msgs = append(msgs, o)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, o := range msgs {
		status, known, err := t.groupStatus(o.id, chat)
		if err != nil {
			return err
		}
		if !known {
			continue
		}
		changed, err := t.advance(o.id, domain.MessageStatus(u8(o.status)), status)
		if err != nil {
			return err
		}
		if changed {
			if err := t.notify(chat, o.id, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// groupMeta stores a group's description and settings. A group known
// only from them has never had its full member list stored: its
// fetched_at is 0.
func (t *applyTx) groupMeta(e *domain.GroupMetaUpdated) error {
	if !e.ChatJID.IsGroup() || e.ChatJID.User() == "" || e.Fields == 0 {
		return errSkip
	}
	chat, err := t.chat(e.ChatJID)
	if err != nil {
		return err
	}
	if _, err := t.exec("INSERT INTO group_meta (chat_id, fetched_at) VALUES (?, 0) ON CONFLICT (chat_id) DO NOTHING", chat.id); err != nil {
		return err
	}
	set := func(column string, v any) error {
		_, err := t.exec("UPDATE group_meta SET "+column+" = ? WHERE chat_id = ?", v, chat.id) // column: a constant below
		return err
	}
	if e.Fields&domain.GroupFieldTopic != 0 {
		if err := set("topic", nullString(e.Topic)); err != nil {
			return err
		}
	}
	if e.Fields&domain.GroupFieldOwner != 0 && e.Owner.IsUser() {
		owner, err := t.resolve(e.Owner)
		if err != nil {
			return err
		}
		if err := set("owner_id", owner); err != nil {
			return err
		}
	}
	if e.Fields&domain.GroupFieldAnnounce != 0 {
		if err := set("announce_only", e.AnnounceOnly); err != nil {
			return err
		}
	}
	if e.Fields&domain.GroupFieldLocked != 0 {
		if err := set("locked", e.Locked); err != nil {
			return err
		}
	}
	t.ch.TouchGroup(chat.id)
	return nil
}
