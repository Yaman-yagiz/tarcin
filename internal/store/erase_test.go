// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// TestDeletedContentLeavesTheFiles: once a chat is deleted, its text is
// in neither the database file nor the WAL, as itself or as the folded
// words of the full-text index (security.md §3).
func TestDeletedContentLeavesTheFiles(t *testing.T) {
	f := newApplyFixture(t)
	var path string
	if err := f.db.QueryRowContext(t.Context(), "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f.db, WriterOptions{Applier: f.a})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	var events []domain.Event
	// Interleaved with another chat's, so that the index's segments hold
	// words of both.
	for i := range 300 {
		events = append(events, incoming(group, aliLID, fmt.Sprintf("W%d", i), fmt.Sprintf("Kanarya CANARYXQZ %d", i), i+1),
			incoming(ayLID, ayLID, fmt.Sprintf("K%d", i), fmt.Sprintf("kept message %d", i), i+1))
	}
	// In thirty transactions: the index holds many segments.
	for chunk := range slices.Chunk(events, 20) {
		if err := w.Deliver(t.Context(), chunk); err != nil {
			t.Fatal(err)
		}
	}
	leaks := func() []string {
		var found []string
		for _, file := range []string{path, path + "-wal"} {
			data, err := os.ReadFile(file) //nolint:gosec // the test's database
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, canary := range []string{"CANARYXQZ", "canaryxqz", "kanarya"} {
				if bytes.Contains(data, []byte(canary)) {
					found = append(found, fmt.Sprintf("%q in %s", canary, file[len(file)-10:]))
				}
			}
		}
		return found
	}
	// The search finds the text and the index's words before the
	// deletion, so that it means something after it.
	var busy, frames, done int
	if err := f.db.QueryRowContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil || busy != 0 {
		t.Fatalf("checkpoint: busy %d, %v", busy, err)
	}
	if found := leaks(); len(found) != 3 {
		t.Fatalf("before the deletion the files hold %v; the search does not work", found)
	}
	if err := w.Deliver(t.Context(), []domain.Event{domain.ChatDeleted{ChatJID: group, Before: at(1000)}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(leaks()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if found := leaks(); len(found) > 0 {
		t.Fatalf("deleted text left on disk: %v", found)
	}
	if n := f.count("SELECT count(*) FROM messages"); n != 300 {
		t.Fatalf("%d messages left, want the other chat's", n)
	}
}
