// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
	"strings"
	"testing"
)

// schemaDB returns a writer connection with the embedded migrations
// applied.
func schemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openWriter(t)
	if _, _, err := Migrate(t.Context(), db, Migrations(), MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func mustFail(t *testing.T, db *sql.DB, why, query string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query); err == nil {
		t.Errorf("%s: %s succeeded", why, query)
	}
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestSchemaTablesAreStrict(t *testing.T) {
	db := schemaDB(t)
	rows, err := db.QueryContext(t.Context(),
		`SELECT name, strict FROM pragma_table_list WHERE schema = 'main' AND type = 'table'
		 AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'messages_fts%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := 0
	for rows.Next() {
		var name string
		var strict bool
		if err := rows.Scan(&name, &strict); err != nil {
			t.Fatal(err)
		}
		tables++
		if !strict {
			t.Errorf("table %s is not STRICT", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if tables != 10 {
		t.Errorf("%d tables, want 10", tables)
	}
	var ok string
	if err := db.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check = %q, %v", ok, err)
	}
}

// seed inserts an identity, a DM chat with it, and one message.
func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, "INSERT INTO identities (id, lid, updated_at) VALUES (1, '1@lid', 0)")
	mustExec(t, db, "INSERT INTO chats (id, jid, kind, peer_identity_id, updated_at) VALUES (1, '1@lid', 'dm', 1, 0)")
	mustExec(t, db, `INSERT INTO messages (id, chat_id, wa_id, sender_id, from_me, ts, kind, body, status)
		VALUES (1, 1, 'W1', 1, 0, 1000, 'image', 'caption', 1)`)
}

func TestSchemaConstraints(t *testing.T) {
	db := schemaDB(t)
	seed(t, db)
	mustFail(t, db, "STRICT", "INSERT INTO identities (pn, updated_at) VALUES ('2@s.whatsapp.net', 'now')")
	mustFail(t, db, "identity without a JID", "INSERT INTO identities (updated_at) VALUES (0)")
	mustFail(t, db, "duplicate LID", "INSERT INTO identities (lid, updated_at) VALUES ('1@lid', 0)")
	mustFail(t, db, "chat kind", "INSERT INTO chats (jid, kind, updated_at) VALUES ('x@g.us', 'channel', 0)")
	mustFail(t, db, "duplicate wa_id", `INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, status)
		VALUES (1, 'W1', 0, 0, 'text', 1)`)
	mustFail(t, db, "foreign key", `INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, status)
		VALUES (99, 'W2', 0, 0, 'text', 1)`)
	mustFail(t, db, "alias for no identity", "INSERT INTO jid_aliases (jid, identity_id) VALUES ('9@lid', 9)")
	// The same wa_id may exist in two chats.
	mustExec(t, db, "INSERT INTO chats (id, jid, kind, updated_at) VALUES (2, 'g@g.us', 'group', 0)")
	mustExec(t, db, `INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, status) VALUES (2, 'W1', 1, 0, 'text', 0)`)
}

func TestSchemaDeletingAChatCascades(t *testing.T) {
	db := schemaDB(t)
	seed(t, db)
	mustExec(t, db, "INSERT INTO media (message_id, mime, updated_at) VALUES (1, 'image/jpeg', 0)")
	mustExec(t, db, "INSERT INTO reactions VALUES (1, 'W1', 1, '+', 0)")
	mustExec(t, db, "INSERT INTO pending_ops (chat_id, target_wa_id, kind, payload, ts) VALUES (1, 'W9', 'edit', x'00', 0)")
	mustExec(t, db, "INSERT INTO pending_ops (chat_id, target_wa_id, kind, payload, ts) VALUES (1, 'W9', 'edit', x'01', 0)")
	mustExec(t, db, "INSERT INTO group_members VALUES (1, 1, 0, 0)")
	mustExec(t, db, "DELETE FROM chats WHERE id = 1")
	for _, table := range []string{"messages", "media", "reactions", "pending_ops", "group_members"} {
		if n := count(t, db, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("%s: %d rows left after deleting the chat", table, n)
		}
	}
	if n := count(t, db, "SELECT count(*) FROM identities"); n != 1 {
		t.Error("deleting a chat must keep the person")
	}
}

func TestSchemaIDsAreNotReused(t *testing.T) {
	db := schemaDB(t)
	seed(t, db)
	mustExec(t, db, "DELETE FROM chats WHERE id = 1")
	mustExec(t, db, "DELETE FROM identities WHERE id = 1")
	mustExec(t, db, "INSERT INTO identities (lid, updated_at) VALUES ('2@lid', 0)")
	mustExec(t, db, "INSERT INTO chats (jid, kind, updated_at) VALUES ('2@lid', 'dm', 0)")
	mustExec(t, db, `INSERT INTO messages (chat_id, wa_id, from_me, ts, kind, status)
		VALUES ((SELECT id FROM chats), 'W2', 0, 0, 'text', 1)`)
	for _, table := range []string{"identities", "chats", "messages"} {
		if id := count(t, db, "SELECT id FROM "+table); id != 2 {
			t.Errorf("%s: new row got id %d, want 2 (ids must not be reused)", table, id)
		}
	}
}

func TestSchemaFTSContentlessDelete(t *testing.T) {
	db := schemaDB(t)
	mustExec(t, db, "INSERT INTO messages_fts (rowid, body) VALUES (1, 'gunaydin dunya'), (2, 'iyi geceler')")
	if n := count(t, db, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'dunya'"); n != 1 {
		t.Fatalf("match = %d, want 1", n)
	}
	mustExec(t, db, "DELETE FROM messages_fts WHERE rowid = 1")
	// An edit re-indexes with UPDATE. A plain INSERT of an existing rowid
	// would keep the old tokens as well: a contentless table cannot see
	// the conflict.
	mustExec(t, db, "UPDATE messages_fts SET body = 'replaced' WHERE rowid = 2")
	if n := count(t, db, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'dunya OR geceler'"); n != 0 {
		t.Fatalf("deleted and replaced rows still match: %d", n)
	}
	if n := count(t, db, "SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'replaced'"); n != 1 {
		t.Fatal("a replaced row must match its new body")
	}
}

// TestSchemaQueryPlans checks that the query shapes of data-model.md §4
// are served by their index, without a sort.
func TestSchemaQueryPlans(t *testing.T) {
	db := schemaDB(t)
	for _, q := range []struct{ query, index string }{
		{"SELECT id FROM chats WHERE archived = 0 ORDER BY pinned_at DESC, last_activity_ts DESC LIMIT 50", "chats_list"},
		{"SELECT id FROM messages WHERE chat_id = 1 ORDER BY ts DESC, id DESC LIMIT 100", "messages_chat_ts"},
		{"SELECT id FROM messages WHERE chat_id = 1 AND (ts, id) < (5, 5) ORDER BY ts DESC, id DESC LIMIT 100", "messages_chat_ts"},
		{"SELECT id FROM messages WHERE chat_id = 1 AND (ts, id) > (5, 5) ORDER BY ts ASC, id ASC LIMIT 100", "messages_chat_ts"},
		{"SELECT id FROM messages WHERE sender_id = 1", "messages_sender"},
		{"SELECT chat_id FROM group_members WHERE identity_id = 1", "group_members_identity"},
		{"SELECT payload FROM pending_ops WHERE chat_id = 1 AND target_wa_id = 'W1'", "pending_ops_target"},
	} {
		rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+q.query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		p := strings.Join(plan, "; ")
		if !strings.Contains(p, q.index) || strings.Contains(p, "TEMP B-TREE") {
			t.Errorf("%s\n  plan: %s\n  want index %s and no sort", q.query, p, q.index)
		}
	}
}
