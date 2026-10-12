// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"strings"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

func (f *applyFixture) search(query string) []string {
	f.t.Helper()
	msgs, err := f.r.Search(f.t.Context(), query, 0, 50)
	if err != nil {
		f.t.Fatal(err)
	}
	return bodies(msgs)
}

// checkIndexIDs verifies that the index has an entry for exactly the
// messages that have a body and are not revoked.
func (f *applyFixture) checkIndexIDs() {
	f.t.Helper()
	const searchable = "SELECT id FROM messages WHERE body IS NOT NULL AND revoked_at IS NULL"
	if n := f.count("SELECT count(*) FROM (SELECT rowid FROM messages_fts EXCEPT " + searchable + ")"); n != 0 {
		f.t.Fatalf("%d index entries without a searchable message", n)
	}
	if n := f.count("SELECT count(*) FROM (" + searchable + " EXCEPT SELECT rowid FROM messages_fts)"); n != 0 {
		f.t.Fatalf("%d searchable messages without an index entry", n)
	}
}

// checkIndex also verifies that each entry holds its message's body.
func (f *applyFixture) checkIndex() {
	f.t.Helper()
	f.checkIndexIDs()
	rows, err := f.db.QueryContext(f.t.Context(), "SELECT id, body FROM messages WHERE body IS NOT NULL AND revoked_at IS NULL")
	if err != nil {
		f.t.Fatal(err)
	}
	type msg struct {
		id   int64
		body string
	}
	var msgs []msg
	for rows.Next() {
		var m msg
		if err := rows.Scan(&m.id, &m.body); err != nil {
			f.t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	_ = rows.Close()
	for _, m := range msgs {
		phrase := `"` + strings.ReplaceAll(textfold.Fold(m.body), `"`, `""`) + `"`
		var n int
		if err := f.db.QueryRowContext(f.t.Context(),
			"SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ? AND rowid = ?", phrase, m.id).Scan(&n); err != nil {
			f.t.Fatal(err)
		}
		if n != 1 {
			f.t.Fatalf("message %d (%q) is not indexed with its body", m.id, m.body)
		}
	}
}

func TestIndexFollowsTheMessage(t *testing.T) {
	f := newApplyFixture(t)
	img := incoming(group, aliLID, "W2", "", 2)
	img.Message.Kind = domain.KindImage
	f.apply(incoming(group, aliLID, "W1", "Kadınlar matinesi", 1), img)
	if got := f.search("kadin"); len(got) != 1 {
		t.Fatalf("search = %q", got)
	}
	if n := f.count("SELECT count(*) FROM messages_fts"); n != 1 {
		t.Fatalf("%d index entries; a message without text has none", n)
	}
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "İstanbul yolu", EditedAt: at(3)})
	if got := f.search("kadin"); len(got) != 0 {
		t.Fatalf("an edited-away word still matches: %q", got)
	}
	if got := f.search("istanbul"); len(got) != 1 {
		t.Fatalf("the edited text must be searchable: %q", got)
	}
	f.checkIndex()
	f.apply(domain.MessageRevoked{ChatJID: group, TargetWAID: "W1", RevokedAt: at(4)})
	if got := f.search("istanbul"); len(got) != 0 {
		t.Fatalf("a revoked message must not be found: %q", got)
	}
	f.checkIndex()
}

func TestIndexAppliesPendingEdits(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(domain.MessageEdited{ChatJID: group, TargetWAID: "W1", Body: "yeni metin", EditedAt: at(5)})
	f.apply(incoming(group, aliLID, "W1", "eski metin", 1))
	if got := f.search("eski"); len(got) != 0 {
		t.Fatalf("the pre-edit text must not be indexed: %q", got)
	}
	if got := f.search("yeni"); len(got) != 1 {
		t.Fatalf("search = %q", got)
	}
	f.checkIndex()
}

func TestIndexUpgradesAndMerges(t *testing.T) {
	f := newApplyFixture(t)
	placeholder := incoming(aliPN, aliPN, "W1", "", 1)
	placeholder.Message.Kind = domain.KindUnsupported
	f.apply(placeholder)
	f.apply(incoming(aliPN, aliPN, "W1", "çözüldü", 1))
	if got := f.search("cozuldu"); len(got) != 1 {
		t.Fatalf("an upgraded placeholder must be searchable: %q", got)
	}
	f.apply(incoming(aliLID, aliLID, "W1", "çözüldü", 1), domain.IdentityLinked{PN: aliPN, LID: aliLID})
	if got := f.search("cozuldu"); len(got) != 1 {
		t.Fatalf("a merged duplicate must be indexed once: %q", got)
	}
	f.checkIndex()
}

func TestIndexDeleteTrigger(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliLID, "W1", "silinecek", 1))
	mustExec(t, f.db, "DELETE FROM chats")
	if n := f.count("SELECT count(*) FROM messages_fts"); n != 0 {
		t.Fatalf("deleting a chat left %d index entries", n)
	}
}

func TestIndexRebuildsForAnotherFoldVersion(t *testing.T) {
	f := newApplyFixture(t)
	f.apply(incoming(group, aliLID, "W1", "Çağrı geldi", 1))
	// Simulate an index built by an older textfold: stale keys, and a
	// message the old version indexed differently.
	mustExec(t, f.db, "DELETE FROM messages_fts")
	mustExec(t, f.db, "INSERT INTO messages_fts (rowid, body) VALUES (999, 'stale')")
	mustExec(t, f.db, "INSERT INTO sync_state (key, value, updated_at) VALUES ('fts.fold_version', x'30', 0)")

	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Maintain(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := f.search("cagri"); len(got) != 1 {
		t.Fatalf("search after the rebuild = %q", got)
	}
	f.checkIndex()

	tx, err = f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rebuilt, err := rebuildIndexIfStale(t.Context(), tx, 0)
	if err != nil || rebuilt {
		t.Fatalf("an index of the current version must not be rebuilt: %v, %v", rebuilt, err)
	}
}
