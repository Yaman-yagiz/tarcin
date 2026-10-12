// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/trace"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

// Reader runs the read queries (docs/architecture/data-model.md §4) on a
// pool opened with RoleReader. It is safe for concurrent use. Message
// pages use keyset pagination on (ts, id) and never OFFSET, so every page
// costs the same however deep in history it is.
type Reader struct {
	db *sql.DB
}

// NewReader returns a Reader on db.
func NewReader(db *sql.DB) *Reader { return &Reader{db: db} }

const chatColumns = `id, jid, kind, peer_identity_id, title, last_message_id, last_activity_ts,
	unread_count, last_read_ts, pinned_at, archived, muted_until, ephemeral_secs, history_complete`

// Chats returns every chat in chat-list order: pinned chats first (most
// recently pinned first), then by latest activity, then by ID, as
// domain.Chat.ListBefore orders them. Archived chats are included; the
// UI filters them.
func (r *Reader) Chats(ctx context.Context) ([]domain.Chat, error) {
	return r.chats(ctx, `SELECT `+chatColumns+` FROM chats
		ORDER BY pinned_at DESC, last_activity_ts DESC, id DESC`)
}

// ChatsByID returns the chats with the given IDs that exist, in chat-list
// order.
func (r *Reader) ChatsByID(ctx context.Context, ids []domain.ChatID) ([]domain.Chat, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return r.chats(ctx, `SELECT `+chatColumns+` FROM chats
		WHERE id IN (SELECT value FROM json_each(?))
		ORDER BY pinned_at DESC, last_activity_ts DESC, id DESC`, idList(ids))
}

func (r *Reader) chats(ctx context.Context, query string, args ...any) ([]domain.Chat, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading chats: %w", err)
	}
	defer rows.Close()
	var out []domain.Chat
	for rows.Next() {
		var (
			c                              domain.Chat
			jid, kind                      string
			peer, lastMsg, pinned, muted   sql.NullInt64
			ephemeral                      sql.NullInt64
			title                          sql.NullString
			lastActivity, unread, lastRead int64
			archived, historyComplete      bool
		)
		if err := rows.Scan(&c.ID, &jid, &kind, &peer, &title, &lastMsg, &lastActivity,
			&unread, &lastRead, &pinned, &archived, &muted, &ephemeral, &historyComplete); err != nil {
			return nil, fmt.Errorf("reading chats: %w", err)
		}
		c.JID = domain.JID(jid)
		c.Kind = chatKinds[kind]
		c.PeerID = domain.IdentityID(peer.Int64)
		c.Title = title.String
		c.LastMessageID = domain.MessageID(lastMsg.Int64)
		c.LastActivity = fromMs(lastActivity)
		c.UnreadCount = int(unread)
		c.LastReadAt = fromMs(lastRead)
		c.PinnedAt = fromNullMs(pinned)
		c.Archived = archived
		c.MutedUntil = mutedFromSQL(muted)
		c.EphemeralTimer = time.Duration(ephemeral.Int64) * time.Second
		c.HistoryComplete = historyComplete
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading chats: %w", err)
	}
	return out, nil
}

// ChatID returns the chat a JID addresses: a group or channel by its JID,
// a direct chat through the identity of the person, whichever address
// form the JID uses.
func (r *Reader) ChatID(ctx context.Context, jid domain.JID) (domain.ChatID, bool, error) {
	query := "SELECT id FROM chats WHERE jid = ?"
	if jid.IsUser() {
		query = `SELECT c.id FROM jid_aliases a JOIN identities i ON i.id = a.identity_id
			JOIN chats c ON c.jid = coalesce(i.lid, i.pn) AND c.kind = 'dm' WHERE a.jid = ?`
	}
	var id domain.ChatID
	err := r.db.QueryRowContext(ctx, query, jid).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("finding the chat of %s: %w", jid, err)
	}
	return id, true, nil
}

// UnreadRef is an unread incoming message, for a read receipt.
type UnreadRef struct {
	WAID   domain.WAMessageID
	Sender domain.JID // the sender's canonical JID; empty if unknown
}

// UnreadIncoming returns up to limit unread incoming messages of chat,
// oldest first.
func (r *Reader) UnreadIncoming(ctx context.Context, chat domain.ChatID, limit int) ([]UnreadRef, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT m.wa_id, coalesce(i.lid, i.pn, '') FROM messages m
		LEFT JOIN identities i ON i.id = m.sender_id
		WHERE m.chat_id = ? AND m.unread = 1 ORDER BY m.ts, m.id LIMIT ?`, chat, limit)
	if err != nil {
		return nil, fmt.Errorf("reading unread messages: %w", err)
	}
	defer rows.Close()
	var out []UnreadRef
	for rows.Next() {
		var u UnreadRef
		if err := rows.Scan(&u.WAID, &u.Sender); err != nil {
			return nil, fmt.Errorf("reading unread messages: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PendingHistory returns the history syncs that were queued and not done
// yet, oldest first, for the backend to process after a restart.
func (r *Reader) PendingHistory(ctx context.Context) ([]domain.HistoryQueued, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT substr(key, ?), value FROM sync_state
		WHERE key >= ? AND key < ? ORDER BY updated_at, key`,
		len(historyPrefix)+1, historyPrefix, historyPrefix+"\xff")
	if err != nil {
		return nil, fmt.Errorf("reading queued history syncs: %w", err)
	}
	defer rows.Close()
	var out []domain.HistoryQueued
	for rows.Next() {
		var h domain.HistoryQueued
		if err := rows.Scan(&h.ID, &h.Notification); err != nil {
			return nil, fmt.Errorf("reading queued history syncs: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GroupMeta returns what is stored about a group besides its title and
// members; ok is false if nothing is.
func (r *Reader) GroupMeta(ctx context.Context, chat domain.ChatID) (meta domain.GroupMeta, ok bool, err error) {
	var fetched int64
	var owner sql.NullInt64
	err = r.db.QueryRowContext(ctx, `SELECT coalesce(topic, ''), owner_id, announce_only, locked,
		coalesce(participant_count, 0), fetched_at FROM group_meta WHERE chat_id = ?`, chat).
		Scan(&meta.Topic, &owner, &meta.AnnounceOnly, &meta.Locked, &meta.ParticipantCount, &fetched)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.GroupMeta{}, false, nil
	case err != nil:
		return domain.GroupMeta{}, false, fmt.Errorf("reading the group's metadata: %w", err)
	}
	meta.Owner = domain.IdentityID(owner.Int64)
	if fetched > 0 {
		meta.FetchedAt = time.UnixMilli(fetched)
	}
	return meta, true, nil
}

// GroupRole returns a person's role in a group; ok is false if they are
// not a stored member.
func (r *Reader) GroupRole(ctx context.Context, chat domain.ChatID, who domain.IdentityID) (role domain.GroupRole, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, "SELECT role FROM group_members WHERE chat_id = ? AND identity_id = ?", chat, who).Scan(&role)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("reading a group role: %w", err)
	}
	return role, true, nil
}

// GroupMemberCount returns how many members of a group are stored.
func (r *Reader) GroupMemberCount(ctx context.Context, chat domain.ChatID) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM group_members WHERE chat_id = ?", chat).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting the group's members: %w", err)
	}
	return n, nil
}

// GroupMembers returns a page of a group's members in the order the
// members view shows them: this account, then the owners, admins and
// members, each by name, and people without a name last, by number.
func (r *Reader) GroupMembers(ctx context.Context, chat domain.ChatID, offset, limit int) ([]domain.GroupMemberRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT g.identity_id, g.role FROM group_members g
		JOIN identities i ON i.id = g.identity_id
		WHERE g.chat_id = ?
		ORDER BY i.is_me DESC, g.role DESC,
			coalesce(nullif(i.contact_name, ''), nullif(i.push_name, ''), nullif(i.business_name, '')) IS NULL,
			textfold(coalesce(nullif(i.contact_name, ''), nullif(i.push_name, ''), nullif(i.business_name, ''), '')),
			coalesce(i.pn, i.lid), g.identity_id
		LIMIT ? OFFSET ?`, chat, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("reading the group's members: %w", err)
	}
	defer rows.Close()
	var out []domain.GroupMemberRow
	for rows.Next() {
		var m domain.GroupMemberRow
		if err := rows.Scan(&m.Identity, &m.Role); err != nil {
			return nil, fmt.Errorf("reading the group's members: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const identityColumns = `id, lid, pn, contact_name, push_name, business_name, is_me`

// MediaInUse reports whether a stored message still uses a cache file: a
// file holds one content, which several messages may share.
func (r *Reader) MediaInUse(ctx context.Context, name string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM media WHERE local_name = ?", name).Scan(&n); err != nil {
		return false, fmt.Errorf("checking a cache file: %w", err)
	}
	return n > 0, nil
}

// LocalNames returns every cache file a stored message uses.
func (r *Reader) LocalNames(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT local_name FROM media WHERE local_name IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("reading the cache files: %w", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading the cache files: %w", err)
		}
		names[name] = true
	}
	return names, rows.Err()
}

// PinnedCount returns how many chats are pinned.
func (r *Reader) PinnedCount(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM chats WHERE pinned_at IS NOT NULL").Scan(&n); err != nil {
		return 0, fmt.Errorf("counting pinned chats: %w", err)
	}
	return n, nil
}

// Me returns this account's identity, once it is known.
func (r *Reader) Me(ctx context.Context) (domain.Identity, bool, error) {
	ids, err := r.identities(ctx, `SELECT `+identityColumns+` FROM identities WHERE is_me = 1 ORDER BY id LIMIT 1`)
	if err != nil || len(ids) == 0 {
		return domain.Identity{}, false, err
	}
	return ids[0], true, nil
}

// Identities returns every known person, for the display-name cache.
func (r *Reader) Identities(ctx context.Context) ([]domain.Identity, error) {
	return r.identities(ctx, `SELECT `+identityColumns+` FROM identities ORDER BY id`)
}

// IdentitiesByID returns the people with the given IDs that exist.
func (r *Reader) IdentitiesByID(ctx context.Context, ids []domain.IdentityID) ([]domain.Identity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return r.identities(ctx, `SELECT `+identityColumns+` FROM identities
		WHERE id IN (SELECT value FROM json_each(?)) ORDER BY id`, idList(ids))
}

func (r *Reader) identities(ctx context.Context, query string, args ...any) ([]domain.Identity, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading identities: %w", err)
	}
	defer rows.Close()
	var out []domain.Identity
	for rows.Next() {
		var (
			id                               domain.Identity
			lid, pn, contact, push, business sql.NullString
		)
		if err := rows.Scan(&id.ID, &lid, &pn, &contact, &push, &business, &id.IsMe); err != nil {
			return nil, fmt.Errorf("reading identities: %w", err)
		}
		id.LID, id.PN = domain.JID(lid.String), domain.JID(pn.String)
		id.ContactName, id.PushName, id.BusinessName = contact.String, push.String, business.String
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading identities: %w", err)
	}
	return out, nil
}

const messageColumns = `m.id, m.chat_id, m.wa_id, m.sender_id, m.from_me, m.ts, m.kind, m.body,
	m.quoted_wa_id, m.status, m.edited_at, m.revoked_at, m.flags, m.revision, m.unread,
	md.mime, md.size, md.file_name, md.width, md.height, md.duration_ms, md.state,
	q.id, q.sender_id, q.from_me, q.kind, q.body, q.revoked_at IS NOT NULL,
	(SELECT json_group_array(json_array(emoji, n, mine)) FROM (
		SELECT r.emoji, count(*) AS n, max(i.is_me) AS mine, min(r.ts) AS first FROM reactions r
		JOIN identities i ON i.id = r.sender_id
		WHERE r.chat_id = m.chat_id AND r.target_wa_id = m.wa_id AND r.emoji != ''
		GROUP BY r.emoji ORDER BY n DESC, first)),
	(SELECT json_group_array(json_array(j.value, coalesce(a.identity_id, 0)))
		FROM json_each(m.mentions) j LEFT JOIN jid_aliases a ON a.jid = j.value)`

// quoteJoin finds the quoted message of a reply in the same chat, through
// the (chat_id, wa_id) key.
const quoteJoin = `LEFT JOIN messages q ON q.chat_id = m.chat_id AND q.wa_id = m.quoted_wa_id`

const messageTables = `messages m LEFT JOIN media md ON md.message_id = m.id ` + quoteJoin

// Latest returns the newest limit messages of chat, oldest first.
func (r *Reader) Latest(ctx context.Context, chat domain.ChatID, limit int) ([]domain.Message, error) {
	msgs, err := r.messages(ctx, `SELECT `+messageColumns+` FROM `+messageTables+`
		WHERE m.chat_id = ? ORDER BY m.ts DESC, m.id DESC LIMIT ?`, chat, limit)
	slices.Reverse(msgs)
	return msgs, err
}

// Before returns up to limit messages of chat older than c, oldest
// first.
func (r *Reader) Before(ctx context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	msgs, err := r.messages(ctx, `SELECT `+messageColumns+` FROM `+messageTables+`
		WHERE m.chat_id = ? AND (m.ts, m.id) < (?, ?)
		ORDER BY m.ts DESC, m.id DESC LIMIT ?`, chat, c.Time.UnixMilli(), c.ID, limit)
	slices.Reverse(msgs)
	return msgs, err
}

// After returns up to limit messages of chat newer than c, oldest first.
func (r *Reader) After(ctx context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	return r.messages(ctx, `SELECT `+messageColumns+` FROM `+messageTables+`
		WHERE m.chat_id = ? AND (m.ts, m.id) > (?, ?)
		ORDER BY m.ts ASC, m.id ASC LIMIT ?`, chat, c.Time.UnixMilli(), c.ID, limit)
}

// MessageByWAID returns a chat's message with a WhatsApp ID; ok is false
// if it is not stored.
func (r *Reader) MessageByWAID(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID) (domain.Message, bool, error) {
	msgs, err := r.messages(ctx, `SELECT `+messageColumns+` FROM `+messageTables+`
		WHERE m.chat_id = ? AND m.wa_id = ?`, chat, waid)
	if err != nil || len(msgs) == 0 {
		return domain.Message{}, false, err
	}
	return msgs[0], true, nil
}

// Around returns up to limit messages of chat around the one with a
// WhatsApp ID, oldest first: half before it, it, and the rest after. It
// returns none if that message is not stored.
func (r *Reader) Around(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID, limit int) ([]domain.Message, error) {
	target, ok, err := r.MessageByWAID(ctx, chat, waid)
	if err != nil || !ok || limit <= 0 {
		return nil, err
	}
	before, err := r.Before(ctx, chat, target.Cursor(), limit/2)
	if err != nil {
		return nil, err
	}
	after, err := r.After(ctx, chat, target.Cursor(), max(limit-limit/2-1, 0))
	if err != nil {
		return nil, err
	}
	return slices.Concat(before, []domain.Message{target}, after), nil
}

// MediaSource is what a download of a message's media needs.
type MediaSource struct {
	Raw       []byte // the stored protocol message, with the media key
	Media     domain.Media
	SHA256    []byte // the content's hash, once downloaded
	LocalName string // the cache file, if downloaded
}

// Raw returns the stored protocol message of a chat message: nil for a
// text message, which keeps none, or a message that is not stored.
func (r *Reader) Raw(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID) ([]byte, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx, "SELECT raw FROM messages WHERE chat_id = ? AND wa_id = ?", chat, waid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// MediaSource returns a chat message's media and the details to download
// it; ok is false if the message or its media is not stored.
func (r *Reader) MediaSource(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID) (src MediaSource, ok bool, err error) {
	var mime, name, local sql.NullString
	var size sql.NullInt64
	var state int64
	err = r.db.QueryRowContext(ctx, `SELECT m.raw, md.mime, md.size, md.file_name, md.state, md.sha256, md.local_name
		FROM messages m JOIN media md ON md.message_id = m.id WHERE m.chat_id = ? AND m.wa_id = ?`, chat, waid).
		Scan(&src.Raw, &mime, &size, &name, &state, &src.SHA256, &local)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return MediaSource{}, false, nil
	case err != nil:
		return MediaSource{}, false, fmt.Errorf("reading the media: %w", err)
	}
	src.Media = domain.Media{MIME: mime.String, Size: size.Int64, FileName: name.String, State: domain.MediaState(u8(state))}
	src.LocalName = local.String
	return src, true, nil
}

// Search returns up to limit messages whose text contains every word of
// query, newest first, in all chats or only in chat if it is not zero.
// Matching is by word prefix on textfold-folded text: "kadin" finds
// "Kadınlar", but "adin" does not find "kadin". A query without letters
// or digits matches nothing.
func (r *Reader) Search(ctx context.Context, query string, chat domain.ChatID, limit int) ([]domain.Message, error) {
	match := ftsMatch(query)
	if match == "" {
		return nil, nil
	}
	return r.messages(ctx, `SELECT `+messageColumns+`
		FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
		LEFT JOIN media md ON md.message_id = m.id `+quoteJoin+`
		WHERE messages_fts MATCH ? AND (? = 0 OR m.chat_id = ?)
		ORDER BY m.ts DESC, m.id DESC LIMIT ?`, match, chat, chat, limit)
}

// ftsMatch turns a user query into an FTS5 expression: each folded word
// becomes a quoted prefix term, so no input can inject FTS5 syntax.
func ftsMatch(query string) string {
	var terms []string
	for _, w := range strings.Fields(textfold.Fold(query)) {
		if !strings.ContainsFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " ")
}

func (r *Reader) messages(ctx context.Context, query string, args ...any) ([]domain.Message, error) {
	defer trace.StartRegion(ctx, "db.read").End()
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("reading messages: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading messages: %w", err)
	}
	return out, nil
}

// parseReactions reads the [[emoji, count, mine], …] array of the
// reactions column.
func parseReactions(s string) ([]domain.ReactionCount, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]domain.ReactionCount, 0, len(rows))
	for _, row := range rows {
		if len(row) != 3 {
			return nil, fmt.Errorf("a reaction has %d fields", len(row))
		}
		var r domain.ReactionCount
		var mine int
		if err := errors.Join(json.Unmarshal(row[0], &r.Emoji), json.Unmarshal(row[1], &r.Count), json.Unmarshal(row[2], &mine)); err != nil {
			return nil, err
		}
		r.Mine = mine != 0
		out = append(out, r)
	}
	return out, nil
}

// parseMentions reads the [[jid, identity], …] array of the mentions
// column.
func parseMentions(s string) ([]domain.Mention, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]domain.Mention, 0, len(rows))
	for _, row := range rows {
		if len(row) != 2 {
			return nil, fmt.Errorf("a mention has %d fields", len(row))
		}
		var jid string
		var id domain.IdentityID
		if err := errors.Join(json.Unmarshal(row[0], &jid), json.Unmarshal(row[1], &id)); err != nil {
			return nil, err
		}
		out = append(out, domain.Mention{Token: "@" + domain.JID(jid).User(), Identity: id})
	}
	return out, nil
}

func scanMessage(rows *sql.Rows) (domain.Message, error) {
	var (
		m                                domain.Message
		waID, kind                       string
		sender, edited, revoked          sql.NullInt64
		body, quoted                     sql.NullString
		ts, status, flags, revision      int64
		mime, fileName                   sql.NullString
		size, width, height, dur, mstate sql.NullInt64
		qID, qSender                     sql.NullInt64
		qFromMe, qRevoked                sql.NullBool
		qKind, qBody                     sql.NullString
		reactions, mentions              sql.NullString
	)
	if err := rows.Scan(&m.ID, &m.ChatID, &waID, &sender, &m.FromMe, &ts, &kind, &body,
		&quoted, &status, &edited, &revoked, &flags, &revision, &m.Unread,
		&mime, &size, &fileName, &width, &height, &dur, &mstate,
		&qID, &qSender, &qFromMe, &qKind, &qBody, &qRevoked, &reactions, &mentions); err != nil {
		return m, err
	}
	if mentions.Valid && mentions.String != "[]" {
		ms, err := parseMentions(mentions.String)
		if err != nil {
			return m, fmt.Errorf("message %d's mentions: %w", m.ID, err)
		}
		m.Mentions = ms
	}
	if reactions.Valid && reactions.String != "[]" {
		r, err := parseReactions(reactions.String)
		if err != nil {
			return m, fmt.Errorf("message %d's reactions: %w", m.ID, err)
		}
		m.Reactions = r
	}
	if qID.Valid {
		m.Quote = &domain.Quote{SenderID: domain.IdentityID(qSender.Int64), FromMe: qFromMe.Bool,
			Kind: messageKindFromSQL(qKind.String), Body: qBody.String, Revoked: qRevoked.Bool}
	}
	m.WAID = domain.WAMessageID(waID)
	m.SenderID = domain.IdentityID(sender.Int64)
	m.Timestamp = fromMs(ts)
	m.Kind = messageKindFromSQL(kind)
	m.Body = body.String
	m.QuotedWAID = domain.WAMessageID(quoted.String)
	m.Status = domain.MessageStatus(u8(status))
	m.EditedAt = fromNullMs(edited)
	m.RevokedAt = fromNullMs(revoked)
	m.Flags = domain.MessageFlags(u32(flags))
	m.Revision = u32(revision)
	if mstate.Valid {
		m.Media = &domain.Media{
			MIME:     mime.String,
			Size:     size.Int64,
			FileName: fileName.String,
			Width:    int(width.Int64),
			Height:   int(height.Int64),
			Duration: time.Duration(dur.Int64) * time.Millisecond,
			State:    domain.MediaState(u8(mstate.Int64)),
		}
	}
	return m, nil
}
