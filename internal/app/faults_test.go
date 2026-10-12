// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/store"
)

// liveWAIDs runs a fake backend to its end and returns the IDs of the live
// messages it generated, to compare with what was stored.
func liveWAIDs(cfg fake.Config) []domain.WAMessageID {
	fb := fake.New(cfg)
	var ids []domain.WAMessageID
	var mu sync.Mutex
	sink := func(_ context.Context, events []domain.Event) error {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range events {
			if m, ok := ev.(domain.MessageReceived); ok && m.Source == domain.SourceLive && !slices.Contains(ids, m.Message.WAID) {
				ids = append(ids, m.Message.WAID)
			}
		}
		return nil
	}
	_ = fb.Run(context.Background(), sinkFunc(sink))
	return ids
}

type sinkFunc func(context.Context, []domain.Event) error

func (f sinkFunc) Deliver(ctx context.Context, events []domain.Event) error { return f(ctx, events) }

func storedWAIDs(t *testing.T, p *pipeline) map[domain.WAMessageID]bool {
	t.Helper()
	rows, err := p.rdb.QueryContext(t.Context(), "SELECT wa_id FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[domain.WAMessageID]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[domain.WAMessageID(id)] = true
	}
	return out
}

// TestDiskFullLosesNothing is the full-disk half of scenario S11: while
// the database cannot grow, deliveries fail, so the backend keeps the
// events and delivers them again (G1); the UI shows why. Once there is
// space, everything is stored.
func TestDiskFullLosesNothing(t *testing.T) {
	cfg := fake.Config{Seed: 11, Chats: 4, InitialHistory: 20, Rate: 400, MaxEvents: 150,
		RedeliverDelay: 20 * time.Millisecond, Tick: time.Millisecond}
	want := liveWAIDs(cfg)

	fb := fake.New(cfg)
	p, r := newTestPipeline(t, fb.Me())
	// Fill the database first, then stop it from growing.
	var pages int
	if err := p.wdb.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := p.wdb.ExecContext(t.Context(), fmt.Sprintf("PRAGMA max_page_count = %d", pages)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- fb.Run(context.Background(), p) }()
	waitUntil(t, "the disk-full notice", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.notices, func(n string) bool { return len(n) > 9 && n[:9] == "Disk full" })
	})
	time.Sleep(200 * time.Millisecond) // deliveries keep failing meanwhile
	if _, err := p.wdb.ExecContext(t.Context(), "PRAGMA max_page_count = 1073741823"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stored := storedWAIDs(t, p)
	for _, id := range want {
		if !stored[id] {
			t.Fatalf("live message %s was lost while the disk was full", id)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.notices, "Saving messages again") {
		t.Fatalf("notices = %q", r.notices)
	}
}

// slowCommits delays every transaction once, like a slow fsync.
type slowCommits struct {
	store.Applier
	delay time.Duration
	mu    sync.Mutex
	last  *sql.Tx
}

func (s *slowCommits) Apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *store.Changes) error {
	s.mu.Lock()
	first := tx != s.last
	s.last = tx
	s.mu.Unlock()
	if first {
		time.Sleep(s.delay)
	}
	return s.Applier.Apply(ctx, tx, events, ch)
}

// TestSlowDiskPushesBack is the slow-disk half of scenario S11: with
// 15 ms per transaction and traffic far beyond that, the queue pushes
// back on the backend instead of dropping anything, group commit batches
// the submitters that wait, and every message is stored.
func TestSlowDiskPushesBack(t *testing.T) {
	cfg := fake.Config{Seed: 12, Chats: 5, InitialHistory: 10, Rate: 3000, MaxEvents: 150, DuplicateRate: 0.1, Tick: time.Millisecond}
	want := liveWAIDs(cfg)
	fb := fake.New(cfg)
	me := fb.Me()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{
		MeLID: me.LID, MePN: me.PN,
		wrapApplier: func(a store.Applier) store.Applier { return &slowCommits{Applier: a, delay: 15 * time.Millisecond} },
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRecordingUI()
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()

	// Several submitters at once, as whatsmeow's handler goroutines.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := fake.New(cfg).Run(context.Background(), p); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	stored := storedWAIDs(t, p)
	for _, id := range want {
		if !stored[id] {
			t.Fatalf("live message %s was lost on a slow disk", id)
		}
	}
	s := p.writer.Stats()
	if s.Failed != 0 {
		t.Fatalf("writer stats %+v", s)
	}
	if s.Committed <= s.Transactions {
		t.Fatalf("no batching under a slow disk: %+v", s)
	}
	t.Logf("slow disk: %d submissions in %d transactions, longest queue wait %v", s.Committed, s.Transactions, s.MaxQueueWait)
}
