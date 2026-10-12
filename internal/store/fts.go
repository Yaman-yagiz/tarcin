// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

// The full-text index (docs/architecture/data-model.md §3) is a
// contentless-delete FTS5 table holding textfold.Fold of each message
// body under the message's ID. The writer maintains it in the same
// transaction as the message; the messages_fts_delete trigger removes the
// entries of deleted messages.

// ftsVersionKey is the sync_state key of the textfold version the index
// was built with.
const ftsVersionKey = "fts.fold_version"

// indexNew indexes the body of a message that was just inserted.
func (t *applyTx) indexNew(id domain.MessageID, body string) error {
	if body == "" {
		return nil
	}
	_, err := t.exec("INSERT INTO messages_fts (rowid, body) VALUES (?, ?)", id, textfold.Fold(body))
	return err
}

// reindex replaces a message's index entry after its body changed. A
// plain INSERT would keep the old words searchable: a contentless table
// cannot see that the rowid is taken.
func (t *applyTx) reindex(id domain.MessageID, body string) error {
	if err := t.unindex(id); err != nil {
		return err
	}
	return t.indexNew(id, body)
}

func (t *applyTx) unindex(id domain.MessageID) error {
	_, err := t.exec("DELETE FROM messages_fts WHERE rowid = ?", id)
	return err
}

// rebuildBatch is how many messages a rebuild reads at a time.
const rebuildBatch = 1000

// rebuildIndexIfStale rebuilds the index when it was built with another
// textfold version (or never), so that stored keys and queries fold
// alike. It reports whether it rebuilt.
func rebuildIndexIfStale(ctx context.Context, tx *sql.Tx, now int64) (bool, error) {
	want := textfold.Version()
	var have []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", ftsVersionKey).Scan(&have)
	switch {
	case err == nil && string(have) == want:
		return false, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM messages_fts"); err != nil {
		return false, err
	}
	type entry struct {
		id   domain.MessageID
		body string
	}
	batch := make([]entry, 0, rebuildBatch)
	var last domain.MessageID
	for {
		batch = batch[:0]
		rows, err := tx.QueryContext(ctx, `SELECT id, body FROM messages
			WHERE id > ? AND body IS NOT NULL AND revoked_at IS NULL ORDER BY id LIMIT ?`, last, rebuildBatch)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.id, &e.body); err != nil {
				_ = rows.Close()
				return false, err
			}
			batch = append(batch, e)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		for _, e := range batch {
			if _, err := tx.ExecContext(ctx, "INSERT INTO messages_fts (rowid, body) VALUES (?, ?)",
				e.id, textfold.Fold(e.body)); err != nil {
				return false, err
			}
		}
		if len(batch) < rebuildBatch {
			break
		}
		last = batch[len(batch)-1].id
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sync_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		ftsVersionKey, []byte(want), now)
	return true, err
}
