// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
	"errors"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Identity resolution (docs/architecture/data-model.md §2,
// docs/adr/0009-lid-canonical-identity.md). The writer keeps these
// invariants:
//
//   - every non-NULL identities.lid and identities.pn has a jid_aliases
//     row pointing to that identity;
//   - a direct chat's jid is the canonical JID (the LID when known) of its
//     peer identity.

// alias looks a JID up in jid_aliases.
func (t *applyTx) alias(jid domain.JID) (domain.IdentityID, bool, error) {
	if id, ok := t.aliases[jid]; ok {
		return id, true, nil
	}
	var id domain.IdentityID
	err := t.queryRow("SELECT identity_id FROM jid_aliases WHERE jid = ?", jid).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	t.aliases[jid] = id
	return id, true, nil
}

// resolve returns the identity of a user JID, creating a provisional one
// under the JID as given when it is new.
func (t *applyTx) resolve(jid domain.JID) (domain.IdentityID, error) {
	if id, ok, err := t.alias(jid); err != nil || ok {
		return id, err
	}
	var id domain.IdentityID
	var err error
	if jid.IsLID() {
		err = t.queryRow("INSERT INTO identities (lid, updated_at) VALUES (?, ?) RETURNING id", jid, t.now).Scan(&id)
	} else {
		err = t.queryRow("INSERT INTO identities (pn, updated_at) VALUES (?, ?) RETURNING id", jid, t.now).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	t.ch.TouchIdentity(id) // a new person: the UI learns the name
	return id, t.addAlias(jid, id)
}

func (t *applyTx) addAlias(jid domain.JID, id domain.IdentityID) error {
	if _, err := t.exec(`INSERT INTO jid_aliases (jid, identity_id) VALUES (?, ?)
		ON CONFLICT (jid) DO UPDATE SET identity_id = excluded.identity_id`, jid, id); err != nil {
		return err
	}
	t.aliases[jid] = id
	return nil
}

// canonical returns an identity's canonical JID: its LID when known.
func (t *applyTx) canonical(id domain.IdentityID) (domain.JID, error) {
	var jid string
	err := t.queryRow("SELECT coalesce(lid, pn) FROM identities WHERE id = ?", id).Scan(&jid)
	return domain.JID(jid), err
}

// me returns this account's identity: the one the configured JIDs
// resolve to, or else the one an AccountLinked event marked. It returns
// 0 when neither is known.
func (t *applyTx) me() (domain.IdentityID, error) {
	o := &t.a.opts
	if t.meID != 0 {
		return t.meID, nil
	}
	if o.MeLID.IsZero() && o.MePN.IsZero() {
		err := t.queryRow("SELECT id FROM identities WHERE is_me = 1 ORDER BY id LIMIT 1").Scan(&t.meID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return t.meID, err
	}
	return t.markMe(o.MePN, o.MeLID)
}

// markMe links this account's JIDs and marks their identity as mine.
func (t *applyTx) markMe(pn, lid domain.JID) (domain.IdentityID, error) {
	if err := t.link(pn, lid); err != nil {
		return 0, err
	}
	jid := lid
	if jid.IsZero() {
		jid = pn
	}
	id, err := t.resolve(jid)
	if err != nil {
		return 0, err
	}
	res, err := t.exec("UPDATE identities SET is_me = 1 WHERE id = ? AND is_me = 0", id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		t.ch.TouchIdentity(id) // shown as "You" from now on
	}
	t.meID = id
	return id, nil
}

// linkPair links two address forms of one person, in either order.
func (t *applyTx) linkPair(a, b domain.JID) error {
	switch {
	case a.IsPN() && b.IsLID():
		return t.link(a, b)
	case a.IsLID() && b.IsPN():
		return t.link(b, a)
	}
	return nil
}

// link records that a phone-number JID and a LID belong to one person.
func (t *applyTx) link(pn, lid domain.JID) error {
	if !pn.IsPN() || !lid.IsLID() {
		return nil
	}
	idP, okP, err := t.alias(pn)
	if err != nil {
		return err
	}
	idL, okL, err := t.alias(lid)
	if err != nil {
		return err
	}
	switch {
	case okP && okL && idP == idL:
		return nil
	case !okP && !okL:
		var id domain.IdentityID
		if err := t.queryRow("INSERT INTO identities (lid, pn, updated_at) VALUES (?, ?, ?) RETURNING id",
			lid, pn, t.now).Scan(&id); err != nil {
			return err
		}
		t.ch.TouchIdentity(id)
		if err := t.addAlias(lid, id); err != nil {
			return err
		}
		return t.addAlias(pn, id)
	case okL && !okP:
		// A number new to us for a known person, or their new number.
		if _, err := t.exec("UPDATE identities SET pn = ?, updated_at = ? WHERE id = ?", pn, t.now, idL); err != nil {
			return err
		}
		t.ch.TouchIdentity(idL)
		return t.addAlias(pn, idL)
	case okP && !okL:
		return t.learnLID(idP, pn, lid)
	}
	// Both are known, as two identities.
	var otherLID sql.NullString
	if err := t.queryRow("SELECT lid FROM identities WHERE id = ?", idP).Scan(&otherLID); err != nil {
		return err
	}
	if otherLID.Valid {
		// The number belonged to someone else, who keeps their LID.
		return t.movePN(idP, idL, pn)
	}
	return t.merge(idL, idP, pn, lid)
}

// movePN gives the number pn to identity to, taking it from identity
// from.
func (t *applyTx) movePN(from, to domain.IdentityID, pn domain.JID) error {
	if _, err := t.exec("UPDATE identities SET pn = NULL, updated_at = ? WHERE id = ? AND pn = ?", t.now, from, pn); err != nil {
		return err
	}
	if _, err := t.exec("UPDATE identities SET pn = ?, updated_at = ? WHERE id = ?", pn, t.now, to); err != nil {
		return err
	}
	t.ch.TouchIdentity(from)
	t.ch.TouchIdentity(to)
	t.forget() // a chat cached under pn belongs to the old owner
	return t.addAlias(pn, to)
}

// merge folds identity from, known only by the number pn, into identity
// into, the same person's LID identity (data-model.md §2). Everything that
// refers to from moves to into, and the two direct chats become one.
func (t *applyTx) merge(into, from domain.IdentityID, pn, lid domain.JID) error {
	// Merging recounts and moves chats: what a run of messages deferred
	// must be in them first.
	if err := t.settle(); err != nil {
		return err
	}
	var contact, push, business sql.NullString
	var isMe bool
	if err := t.queryRow("SELECT contact_name, push_name, business_name, is_me FROM identities WHERE id = ?", from).
		Scan(&contact, &push, &business, &isMe); err != nil {
		return err
	}
	// Chats showing from's messages must reload: their senders change.
	if err := t.reloadChats("SELECT DISTINCT chat_id FROM messages WHERE sender_id = ?", from); err != nil {
		return err
	}
	for _, q := range []string{
		"UPDATE messages SET sender_id = ?1 WHERE sender_id = ?2",
		// Of two reactions by the person to one message, keep the newer.
		`DELETE FROM reactions WHERE sender_id = ?2 AND EXISTS (SELECT 1 FROM reactions s
			WHERE s.sender_id = ?1 AND s.chat_id = reactions.chat_id AND s.target_wa_id = reactions.target_wa_id
			AND (s.ts > reactions.ts OR (s.ts = reactions.ts AND s.emoji >= reactions.emoji)))`,
		`DELETE FROM reactions WHERE sender_id = ?1 AND EXISTS (SELECT 1 FROM reactions r
			WHERE r.sender_id = ?2 AND r.chat_id = reactions.chat_id AND r.target_wa_id = reactions.target_wa_id)`,
		"UPDATE reactions SET sender_id = ?1 WHERE sender_id = ?2",
		// Of two memberships in one group, keep the higher role.
		`UPDATE group_members SET role = max(role, (SELECT g.role FROM group_members g
			WHERE g.identity_id = ?2 AND g.chat_id = group_members.chat_id))
			WHERE identity_id = ?1 AND chat_id IN (SELECT chat_id FROM group_members WHERE identity_id = ?2)`,
		"DELETE FROM group_members WHERE identity_id = ?2 AND chat_id IN (SELECT chat_id FROM group_members WHERE identity_id = ?1)",
		"UPDATE group_members SET identity_id = ?1 WHERE identity_id = ?2",
		"UPDATE group_meta SET owner_id = ?1 WHERE owner_id = ?2",
		// Of two receipts by the person for one message, keep the further.
		`UPDATE receipts SET status = max(status, (SELECT r.status FROM receipts r
			WHERE r.identity_id = ?2 AND r.message_id = receipts.message_id))
			WHERE identity_id = ?1 AND message_id IN (SELECT message_id FROM receipts WHERE identity_id = ?2)`,
		"DELETE FROM receipts WHERE identity_id = ?2 AND message_id IN (SELECT message_id FROM receipts WHERE identity_id = ?1)",
		"UPDATE receipts SET identity_id = ?1 WHERE identity_id = ?2",
		"UPDATE jid_aliases SET identity_id = ?1 WHERE identity_id = ?2",
	} {
		if _, err := t.exec(q, into, from); err != nil {
			return err
		}
	}

	// The direct chats: from's is keyed by pn, into's by lid.
	fromChat, okFrom, err := t.directChat(pn)
	if err != nil {
		return err
	}
	intoChat, okInto, err := t.directChat(lid)
	if err != nil {
		return err
	}
	switch {
	case okFrom && okInto:
		if err := t.mergeChats(intoChat, fromChat); err != nil {
			return err
		}
	case okFrom:
		if _, err := t.exec("UPDATE chats SET jid = ?, peer_identity_id = ?, updated_at = ? WHERE id = ?",
			lid, into, t.now, fromChat); err != nil {
			return err
		}
		t.ch.TouchChat(fromChat)
	}

	if _, err := t.exec("DELETE FROM identities WHERE id = ?", from); err != nil {
		return err
	}
	if _, err := t.exec(`UPDATE identities SET pn = ?, contact_name = coalesce(contact_name, ?),
		push_name = coalesce(push_name, ?), business_name = coalesce(business_name, ?),
		is_me = max(is_me, ?), updated_at = ? WHERE id = ?`,
		pn, contact, push, business, isMe, t.now, into); err != nil {
		return err
	}
	t.ch.TouchIdentity(into)
	t.ch.TouchIdentity(from) // gone: the UI drops it from its name cache
	t.forget()
	return nil
}

// forget drops the per-call caches after identities or chats changed.
func (t *applyTx) forget() {
	clear(t.aliases)
	clear(t.chats)
	clear(t.pending)
	clear(t.named)
	t.meID = 0
}

func (t *applyTx) directChat(jid domain.JID) (domain.ChatID, bool, error) {
	var id domain.ChatID
	err := t.queryRow("SELECT id FROM chats WHERE jid = ? AND kind = 'dm'", jid).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return id, true, nil
}

// reloadChats marks the chats a query returns for a UI reload.
func (t *applyTx) reloadChats(query string, args ...any) error {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id domain.ChatID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		t.ch.ReloadChat(id)
	}
	return rows.Err()
}

// duplicate is a message of the chat being merged away that the
// surviving chat also holds.
type duplicate struct {
	keep, drop      domain.MessageID
	waid            domain.WAMessageID
	status          int64
	kind            string
	body, quoted    sql.NullString
	flags           int64
	raw             []byte
	edited, revoked sql.NullInt64
	keepKind        string
	keepRevoked     bool
	unread          bool
}

// mergeChats moves the direct chat from into the direct chat into, as if
// every event had gone to into. Copies of one message in both are merged
// with the rules that make events order-independent.
func (t *applyTx) mergeChats(into, from domain.ChatID) error {
	// The unread count beyond the flagged messages came from a history
	// sync; it is kept, while the flagged part is recounted after the
	// merge.
	var syncedUnread int64
	if err := t.queryRow(`SELECT coalesce(sum(max(0, unread_count -
		(SELECT count(*) FROM messages WHERE chat_id = chats.id AND unread = 1))), 0)
		FROM chats WHERE id IN (?, ?)`, into, from).Scan(&syncedUnread); err != nil {
		return err
	}
	dups, err := t.duplicates(into, from)
	if err != nil {
		return err
	}
	for i := range dups {
		if err := t.mergeCopy(&dups[i]); err != nil {
			return err
		}
	}
	for _, q := range []string{
		"UPDATE messages SET chat_id = ?1 WHERE chat_id = ?2",
		`DELETE FROM reactions WHERE chat_id = ?2 AND EXISTS (SELECT 1 FROM reactions s
			WHERE s.chat_id = ?1 AND s.sender_id = reactions.sender_id AND s.target_wa_id = reactions.target_wa_id
			AND (s.ts > reactions.ts OR (s.ts = reactions.ts AND s.emoji >= reactions.emoji)))`,
		`DELETE FROM reactions WHERE chat_id = ?1 AND EXISTS (SELECT 1 FROM reactions r
			WHERE r.chat_id = ?2 AND r.sender_id = reactions.sender_id AND r.target_wa_id = reactions.target_wa_id)`,
		"UPDATE reactions SET chat_id = ?1 WHERE chat_id = ?2",
		"UPDATE pending_ops SET chat_id = ?1 WHERE chat_id = ?2",
	} {
		if _, err := t.exec(q, into, from); err != nil {
			return err
		}
	}
	delete(t.pending, into) // it may have from's operations now
	// Operations that waited in one chat for a message of the other.
	if err := t.applyAllPending(into); err != nil {
		return err
	}
	if err := t.mergeChatRows(into, from, syncedUnread); err != nil {
		return err
	}
	t.ch.RemoveChat(from)
	t.ch.ReloadChat(into)
	t.ch.TouchChat(into)
	return nil
}

func (t *applyTx) duplicates(into, from domain.ChatID) ([]duplicate, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT a.id, b.id, b.wa_id, b.status, b.kind, b.body, b.quoted_wa_id,
		b.flags, b.raw, b.edited_at, b.revoked_at, a.kind, a.revoked_at IS NOT NULL, b.unread
		FROM messages b JOIN messages a ON a.chat_id = ? AND a.wa_id = b.wa_id
		WHERE b.chat_id = ?`, into, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []duplicate
	for rows.Next() {
		var d duplicate
		if err := rows.Scan(&d.keep, &d.drop, &d.waid, &d.status, &d.kind, &d.body, &d.quoted, &d.flags, &d.raw,
			&d.edited, &d.revoked, &d.keepKind, &d.keepRevoked, &d.unread); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// mergeCopy folds the dropped copy of a message into the kept one.
func (t *applyTx) mergeCopy(d *duplicate) error {
	unsupported := domain.KindUnsupported.String()
	if d.keepKind == unsupported && d.kind != unsupported && !d.keepRevoked {
		if _, err := t.exec(`UPDATE messages SET kind = ?, body = ?, quoted_wa_id = ?, flags = ?, raw = ?,
			edited_at = ?, revision = revision + 1 WHERE id = ?`,
			d.kind, d.body, d.quoted, d.flags, d.raw, d.edited, d.keep); err != nil {
			return err
		}
		if err := t.reindex(d.keep, d.body.String); err != nil {
			return err
		}
	} else if d.edited.Valid {
		if _, err := t.edit(d.keep, d.body.String, d.edited.Int64); err != nil {
			return err
		}
	}
	if d.revoked.Valid {
		if _, err := t.revoke(d.keep, d.revoked.Int64); err != nil {
			return err
		}
	}
	if err := t.status(d.keep, domain.MessageStatus(u8(d.status))); err != nil {
		return err
	}
	for _, q := range []string{
		"UPDATE messages SET raw = coalesce(raw, ?3) WHERE id = ?1 AND revoked_at IS NULL",
		"UPDATE messages SET unread = max(unread, ?4) WHERE id = ?1",
		`INSERT INTO media (message_id, mime, size, file_name, width, height, duration_ms, sha256, state, local_name, updated_at)
			SELECT ?1, mime, size, file_name, width, height, duration_ms, sha256, state, local_name, updated_at
			FROM media WHERE message_id = ?2 AND (SELECT revoked_at FROM messages WHERE id = ?1) IS NULL
			ON CONFLICT (message_id) DO UPDATE SET
				mime = coalesce(media.mime, excluded.mime), size = coalesce(media.size, excluded.size),
				file_name = coalesce(media.file_name, excluded.file_name), width = coalesce(media.width, excluded.width),
				height = coalesce(media.height, excluded.height),
				duration_ms = coalesce(media.duration_ms, excluded.duration_ms),
				sha256 = coalesce(media.sha256, excluded.sha256),
				-- A downloaded copy wins over any other state.
				state = CASE WHEN media.state != 2 AND excluded.state = 2 THEN 2 ELSE media.state END,
				local_name = CASE WHEN media.state != 2 AND excluded.state = 2 THEN excluded.local_name
					ELSE media.local_name END`,
		"DELETE FROM messages WHERE id = ?2",
	} {
		if _, err := t.exec(q, d.keep, d.drop, d.raw, d.unread); err != nil {
			return err
		}
	}
	return nil
}

// applyAllPending applies a chat's pending operations whose target is
// now stored.
func (t *applyTx) applyAllPending(chat domain.ChatID) error {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT DISTINCT p.target_wa_id, m.id FROM pending_ops p
		JOIN messages m ON m.chat_id = p.chat_id AND m.wa_id = p.target_wa_id WHERE p.chat_id = ?`, chat)
	if err != nil {
		return err
	}
	type target struct {
		waid domain.WAMessageID
		id   domain.MessageID
	}
	var targets []target
	for rows.Next() {
		var tg target
		if err := rows.Scan(&tg.waid, &tg.id); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, tg)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, tg := range targets {
		if err := t.applyOps(chat, tg.waid, tg.id); err != nil {
			return err
		}
	}
	return nil
}

// mergeChatRows combines the chat-list fields of two direct chats into
// into and deletes from. A field set in either chat stays set, so the
// result does not depend on which chat an update reached first.
func (t *applyTx) mergeChatRows(into, from domain.ChatID, syncedUnread int64) error {
	type row struct {
		unread, lastRead int64
		pinned, muted    sql.NullInt64
		ephemeral        sql.NullInt64
		archived, done   bool
		title            sql.NullString
	}
	read := func(id domain.ChatID) (row, error) {
		var r row
		err := t.queryRow(`SELECT unread_count, last_read_ts, pinned_at, muted_until, ephemeral_secs, archived,
			history_complete, title FROM chats WHERE id = ?`, id).
			Scan(&r.unread, &r.lastRead, &r.pinned, &r.muted, &r.ephemeral, &r.archived, &r.done, &r.title)
		return r, err
	}
	a, err := read(into)
	if err != nil {
		return err
	}
	b, err := read(from)
	if err != nil {
		return err
	}
	if _, err := t.exec("DELETE FROM chats WHERE id = ?", from); err != nil {
		return err
	}
	muted := maxNull(a.muted, b.muted)
	if (a.muted.Valid && a.muted.Int64 == mutedForever) || (b.muted.Valid && b.muted.Int64 == mutedForever) {
		muted = sql.NullInt64{Int64: mutedForever, Valid: true}
	}
	title := a.title
	if !title.Valid {
		title = b.title
	}
	lastRead := max(a.lastRead, b.lastRead)
	// A message both chats counted is one message now, and the later of
	// the two reads covers both chats.
	if _, err := t.exec("UPDATE messages SET unread = 0 WHERE chat_id = ? AND unread = 1 AND ts <= ?", into, lastRead); err != nil {
		return err
	}
	_, err = t.exec(`UPDATE chats SET
		unread_count = ?1 + (SELECT count(*) FROM messages WHERE chat_id = ?9 AND unread = 1),
		last_read_ts = ?2, pinned_at = ?3, muted_until = ?4, ephemeral_secs = ?5, archived = ?6,
		history_complete = ?7, title = ?8,
		last_activity_ts = coalesce((SELECT max(ts) FROM messages WHERE chat_id = ?9), 0),
		last_message_id = (SELECT id FROM messages WHERE chat_id = ?9 ORDER BY ts DESC, id DESC LIMIT 1),
		updated_at = ?10
		WHERE id = ?9`,
		syncedUnread, lastRead, maxNull(a.pinned, b.pinned), muted, maxNull(a.ephemeral, b.ephemeral),
		a.archived || b.archived, a.done || b.done, title, into, t.now)
	return err
}

func maxNull(a, b sql.NullInt64) sql.NullInt64 {
	switch {
	case !a.Valid:
		return b
	case !b.Valid:
		return a
	}
	return sql.NullInt64{Int64: max(a.Int64, b.Int64), Valid: true}
}

// learnLID handles a new LID for the identity known by pn.
func (t *applyTx) learnLID(id domain.IdentityID, pn, lid domain.JID) error {
	var current sql.NullString
	if err := t.queryRow("SELECT lid FROM identities WHERE id = ?", id).Scan(&current); err != nil {
		return err
	}
	if current.Valid {
		// The number now belongs to someone else (numbers are recycled):
		// it moves to a new identity, and the old person keeps their LID.
		if _, err := t.exec("UPDATE identities SET pn = NULL, updated_at = ? WHERE id = ?", t.now, id); err != nil {
			return err
		}
		t.ch.TouchIdentity(id)
		t.forget()
		var moved domain.IdentityID
		if err := t.queryRow("INSERT INTO identities (lid, pn, updated_at) VALUES (?, ?, ?) RETURNING id",
			lid, pn, t.now).Scan(&moved); err != nil {
			return err
		}
		t.ch.TouchIdentity(moved)
		if err := t.addAlias(lid, moved); err != nil {
			return err
		}
		return t.addAlias(pn, moved)
	}
	if _, err := t.exec("UPDATE identities SET lid = ?, updated_at = ? WHERE id = ?", lid, t.now, id); err != nil {
		return err
	}
	if err := t.addAlias(lid, id); err != nil {
		return err
	}
	t.ch.TouchIdentity(id)
	// The canonical JID changed from the number to the LID.
	var chat domain.ChatID
	err := t.queryRow("UPDATE chats SET jid = ?, updated_at = ? WHERE jid = ? AND kind = 'dm' RETURNING id",
		lid, t.now, pn).Scan(&chat)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	t.forget()
	t.ch.TouchChat(chat)
	return nil
}
