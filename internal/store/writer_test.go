// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var errBad = errors.New("bad event")

// harness runs a Writer whose applier inserts each message body as a key
// into table t. Bodies starting with "block" stop the writer inside its
// transaction until release is called; "bad" fails its submission after
// writing; "corrupt" reports a corrupt database; "big" also writes 64 KiB.
type harness struct {
	t       *testing.T
	db      *sql.DB
	w       *Writer
	gate    chan struct{}
	entered chan string

	mu      sync.Mutex
	applied []string
	commits []*Changes
	health  []error
}

func newHarness(t *testing.T, opts WriterOptions) *harness {
	t.Helper()
	h := &harness{t: t, db: openWriter(t), gate: make(chan struct{}), entered: make(chan string, 1)}
	mustExec(t, h.db, "CREATE TABLE t (k TEXT PRIMARY KEY, pad BLOB)")
	opts.Applier = ApplierFunc(h.apply)
	opts.OnCommit = func(c *Changes) {
		h.mu.Lock()
		h.commits = append(h.commits, c)
		h.mu.Unlock()
	}
	opts.OnHealth = func(err error) {
		h.mu.Lock()
		h.health = append(h.health, err)
		h.mu.Unlock()
	}
	w, err := NewWriter(h.db, opts)
	if err != nil {
		t.Fatal(err)
	}
	h.w = w
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	return h
}

func (h *harness) apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *Changes) error {
	for _, ev := range events {
		body := ev.(domain.MessageReceived).Message.Body
		h.mu.Lock()
		h.applied = append(h.applied, body)
		h.mu.Unlock()
		if strings.HasPrefix(body, "block") {
			h.entered <- body
			<-h.gate
		}
		if body == "corrupt" {
			return sqlite3.Error{Code: sqlite3.ErrCorrupt}
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO t (k, pad) VALUES (?1, CASE WHEN ?1 LIKE 'big%' THEN randomblob(65536) END)", body)
		if err != nil {
			return err
		}
		if body == "bad" {
			return errBad
		}
		id, _ := res.LastInsertId()
		ch.AddMessage(&domain.Message{ID: domain.MessageID(id), ChatID: 1, Body: body})
		ch.TouchChat(1)
	}
	return nil
}

// block submits a blocking event and waits until the writer is stuck in
// it. The returned channel yields its result after release.
func (h *harness) block(live bool) <-chan error {
	h.t.Helper()
	done := h.async(context.Background(), live, "block")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		h.t.Fatal("the writer never reached the blocking event")
	}
	return done
}

func (h *harness) release() { close(h.gate) }

func (h *harness) async(ctx context.Context, live bool, bodies ...string) <-chan error {
	done := make(chan error, 1)
	go func() {
		submit := h.w.SubmitBulk
		if live {
			submit = h.w.SubmitLive
		}
		done <- submit(ctx, events(bodies...))
	}()
	return done
}

func events(bodies ...string) []domain.Event {
	evs := make([]domain.Event, len(bodies))
	for i, b := range bodies {
		evs[i] = domain.MessageReceived{Message: domain.IncomingMessage{Body: b}, Source: domain.SourceLive}
	}
	return evs
}

func (h *harness) keys() []string {
	h.t.Helper()
	rows, err := h.db.QueryContext(h.t.Context(), "SELECT k FROM t ORDER BY k")
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

func (h *harness) appliedOrder() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.applied)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("a submission never returned")
		return nil
	}
}

func TestWriterCommitsAndNotifies(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	if err := h.w.SubmitLive(t.Context(), events("a", "b")); err != nil {
		t.Fatal(err)
	}
	if got := h.keys(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("keys = %v", got)
	}
	h.mu.Lock()
	commits := h.commits
	h.mu.Unlock()
	if len(commits) != 1 || len(commits[0].Added[1]) != 2 || commits[0].Added[1][1].Body != "b" {
		t.Fatalf("commit notifications = %+v", commits)
	}
	if err := h.w.SubmitLive(t.Context(), nil); err != nil {
		t.Fatal("an empty submission must succeed without a transaction")
	}
	if s := h.w.Stats(); s.Transactions != 1 || s.Committed != 1 || s.Events != 2 || s.Failed != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWriterGroupCommits(t *testing.T) {
	h := newHarness(t, WriterOptions{MaxBatchTime: time.Minute})
	first := h.block(true)
	const n = 600
	var dones []<-chan error
	for i := range n {
		dones = append(dones, h.async(t.Context(), true, fmt.Sprintf("k%03d", i)))
	}
	waitFor(t, "the queue to fill", func() bool { return len(h.w.live) == n })
	h.release()
	for _, d := range append(dones, first) {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	// 601 events in transactions of at most 256 events.
	if s := h.w.Stats(); s.Transactions != 3 || s.Committed != n+1 || s.Events != n+1 {
		t.Fatalf("stats = %+v, want 3 transactions", s)
	}
	if len(h.keys()) != n+1 {
		t.Fatal("rows missing")
	}
}

// TestWriterBatchesBehindASlowSubmission: the fill window starts after
// the first submission, so the ones queued behind a slow one (a slow
// disk) still share its transaction.
func TestWriterBatchesBehindASlowSubmission(t *testing.T) {
	h := newHarness(t, WriterOptions{}) // the default 10 ms window
	first := h.block(true)
	var queued []<-chan error
	for i := range 5 {
		queued = append(queued, h.async(t.Context(), true, fmt.Sprintf("q%d", i)))
	}
	waitFor(t, "the queue", func() bool { return len(h.w.live) == 5 })
	time.Sleep(30 * time.Millisecond) // longer than the window
	h.release()
	for _, d := range append(queued, first) {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	if s := h.w.Stats(); s.Transactions != 1 || s.Committed != 6 {
		t.Fatalf("stats = %+v, want one transaction", s)
	}
}

func TestWriterIsolatesFailingSubmissions(t *testing.T) {
	h := newHarness(t, WriterOptions{MaxBatchTime: time.Minute})
	first := h.block(true)
	x := h.async(t.Context(), true, "x")
	waitFor(t, "x to queue", func() bool { return len(h.w.live) == 1 })
	bad := h.async(t.Context(), true, "y", "bad")
	waitFor(t, "bad to queue", func() bool { return len(h.w.live) == 2 })
	dup := h.async(t.Context(), true, "x")
	waitFor(t, "dup to queue", func() bool { return len(h.w.live) == 3 })
	z := h.async(t.Context(), true, "z")
	waitFor(t, "z to queue", func() bool { return len(h.w.live) == 4 })
	h.release()

	if err := result(t, bad); !errors.Is(err, errBad) {
		t.Fatalf("bad: err = %v", err)
	}
	if err := result(t, dup); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate key: err = %v", err)
	}
	for _, d := range []<-chan error{first, x, z} {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	// "y" was written by the failing submission and must be rolled back
	// with it.
	if got := h.keys(); !slices.Equal(got, []string{"block", "x", "z"}) {
		t.Fatalf("keys = %v", got)
	}
	s := h.w.Stats()
	if s.Transactions != 1 || s.Committed != 3 || s.Failed != 2 {
		t.Fatalf("stats = %+v", s)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var bodies []string
	for _, m := range h.commits[0].Added[1] {
		bodies = append(bodies, m.Body)
	}
	if !slices.Equal(bodies, []string{"block", "x", "z"}) {
		t.Fatalf("notified %v; failed submissions must not be notified", bodies)
	}
}

func TestWriterPrefersLiveOverBulk(t *testing.T) {
	h := newHarness(t, WriterOptions{MaxBatchTime: time.Minute})
	first := h.block(false)
	b1 := h.async(t.Context(), false, "b1")
	waitFor(t, "b1", func() bool { return len(h.w.bulk) == 1 })
	b2 := h.async(t.Context(), false, "b2")
	waitFor(t, "b2", func() bool { return len(h.w.bulk) == 2 })
	l1 := h.async(t.Context(), true, "l1")
	waitFor(t, "l1", func() bool { return len(h.w.live) == 1 })
	l2 := h.async(t.Context(), true, "l2")
	waitFor(t, "l2", func() bool { return len(h.w.live) == 2 })
	h.release()
	for _, d := range []<-chan error{first, b1, b2, l1, l2} {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"block", "l1", "l2", "b1", "b2"}
	if got := h.appliedOrder(); !slices.Equal(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
	// The bulk chunks never join a live transaction.
	if s := h.w.Stats(); s.Transactions != 4 {
		t.Fatalf("stats = %+v, want 4 transactions", s)
	}
}

func TestWriterSkipsCanceledSubmissions(t *testing.T) {
	h := newHarness(t, WriterOptions{MaxBatchTime: time.Minute})
	blockCtx, cancelBlock := context.WithCancel(t.Context())
	first := h.async(blockCtx, true, "block")
	<-h.entered
	ctx, cancel := context.WithCancel(t.Context())
	queued := h.async(ctx, true, "c")
	waitFor(t, "c to queue", func() bool { return len(h.w.live) == 1 })
	cancel()
	if err := result(t, queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Canceling a submission that is being applied does not interrupt
	// the transaction: it commits, and only the caller stops waiting.
	cancelBlock()
	if err := result(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	h.release()
	if err := h.w.SubmitLive(t.Context(), events("after")); err != nil {
		t.Fatal(err)
	}
	if got := h.keys(); !slices.Equal(got, []string{"after", "block"}) {
		t.Fatalf("keys = %v", got)
	}
	if !slices.Equal(h.appliedOrder(), []string{"block", "after"}) {
		t.Fatalf("a canceled submission was applied: %v", h.appliedOrder())
	}
	if s := h.w.Stats(); s.Canceled != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWriterRetriesWhileBusy(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	mustExec(t, h.db, "PRAGMA busy_timeout = 10")
	other, err := sql.Open(DriverName, mustDSN(t, h))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.BeginTx(t.Context(), nil) // BEGIN IMMEDIATE takes the write lock
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO t (k) VALUES ('other')"); err != nil {
		t.Fatal(err)
	}
	done := h.async(t.Context(), true, "mine")
	waitFor(t, "a busy retry", func() bool { return h.w.Stats().BusyRetries > 0 })
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := result(t, done); err != nil {
		t.Fatalf("a busy transaction must be retried: %v", err)
	}
	if got := h.keys(); !slices.Equal(got, []string{"mine", "other"}) {
		t.Fatalf("keys = %v", got)
	}
}

func TestWriterGivesUpWhenBusyTooLong(t *testing.T) {
	h := newHarness(t, WriterOptions{BusyRetry: 50 * time.Millisecond})
	mustExec(t, h.db, "PRAGMA busy_timeout = 10")
	other, err := sql.Open(DriverName, mustDSN(t, h))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = h.w.SubmitLive(t.Context(), events("mine"))
	if !isBusy(err) {
		t.Fatalf("err = %v, want SQLITE_BUSY", err)
	}
}

// mustDSN returns a writer DSN for the harness database file.
func mustDSN(t *testing.T, h *harness) string {
	t.Helper()
	var file string
	if err := h.db.QueryRowContext(t.Context(), "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	dsn, err := DSN(file, RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestWriterDiskFull(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	var pages int
	if err := h.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, fmt.Sprintf("PRAGMA max_page_count = %d", pages+4))
	err := h.w.SubmitLive(t.Context(), events("small", "big1"))
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("err = %v, want ErrDiskFull", err)
	}
	if got := h.keys(); len(got) != 0 {
		t.Fatalf("a failed transaction left %v", got)
	}
	if err := h.w.SubmitLive(t.Context(), events("big2")); !errors.Is(err, ErrDiskFull) {
		t.Fatalf("err = %v, want ErrDiskFull", err)
	}
	mustExec(t, h.db, "PRAGMA max_page_count = 1000000")
	if err := h.w.SubmitLive(t.Context(), events("big3")); err != nil {
		t.Fatalf("the writer must recover once there is space: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.health) != 2 || !errors.Is(h.health[0], ErrDiskFull) || h.health[1] != nil {
		t.Fatalf("health = %v, want [disk full, nil]", h.health)
	}
}

func TestWriterStopsOnCorruption(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	if err := h.w.SubmitLive(t.Context(), events("corrupt")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if err := h.w.SubmitLive(t.Context(), events("after")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v: a corrupt database must not be written", err)
	}
	if got := h.appliedOrder(); !slices.Equal(got, []string{"corrupt"}) {
		t.Fatalf("applied %v", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.health) != 1 || !errors.Is(h.health[0], ErrCorrupt) {
		t.Fatalf("health = %v", h.health)
	}
}

func TestWriterCloseDrains(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	first := h.block(true)
	queued := []<-chan error{h.async(t.Context(), true, "l"), h.async(t.Context(), false, "b")}
	waitFor(t, "the queues", func() bool { return len(h.w.live) == 1 && len(h.w.bulk) == 1 })
	closed := make(chan error, 1)
	go func() { closed <- h.w.Close(context.Background()) }()
	waitFor(t, "Close to start", func() bool {
		h.w.mu.RLock()
		defer h.w.mu.RUnlock()
		return h.w.closed
	})
	if err := h.w.SubmitLive(t.Context(), events("late")); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("err = %v, want ErrWriterClosed", err)
	}
	h.release()
	for _, d := range append(queued, first) {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := result(t, closed); err != nil {
		t.Fatal(err)
	}
	if got := h.keys(); !slices.Equal(got, []string{"b", "block", "l"}) {
		t.Fatalf("keys = %v", got)
	}
	if err := h.w.Close(context.Background()); err != nil {
		t.Fatal("Close must be idempotent")
	}
}

func TestWriterCloseGivesUpAtDeadline(t *testing.T) {
	h := newHarness(t, WriterOptions{MaxBatchTime: time.Minute})
	first := h.block(true)
	queued := []<-chan error{h.async(t.Context(), true, "l"), h.async(t.Context(), false, "b")}
	waitFor(t, "the queues", func() bool { return len(h.w.live) == 1 && len(h.w.bulk) == 1 })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- h.w.Close(ctx) }()
	waitFor(t, "the abort", h.w.aborted)
	h.release()
	if err := result(t, first); err != nil {
		t.Fatalf("the transaction in progress must still commit: %v", err)
	}
	for _, d := range queued {
		if err := result(t, d); !errors.Is(err, ErrWriterClosed) {
			t.Fatalf("err = %v, want ErrWriterClosed", err)
		}
	}
	if err := result(t, closed); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v", err)
	}
	if got := h.keys(); !slices.Equal(got, []string{"block"}) {
		t.Fatalf("keys = %v", got)
	}
}

func TestWriterDeliverRoutes(t *testing.T) {
	h := newHarness(t, WriterOptions{})
	mixed := []domain.Event{
		domain.ChatTyping{Typing: true},
		domain.MessageReceived{Message: domain.IncomingMessage{Body: "live"}, Source: domain.SourceLive},
		domain.ConnectionChanged{State: domain.ConnOnline},
	}
	if err := h.w.Deliver(t.Context(), mixed); err != nil {
		t.Fatal(err)
	}
	if err := h.w.Deliver(t.Context(), mixed[:1]); err != nil {
		t.Fatal("a batch of ephemeral events must succeed without a write")
	}
	first := h.block(true)
	history := []domain.Event{
		domain.MessageReceived{Message: domain.IncomingMessage{Body: "old"}, Source: domain.SourceHistory},
	}
	done := make(chan error, 1)
	go func() { done <- h.w.Deliver(t.Context(), history) }()
	waitFor(t, "history to reach the bulk queue", func() bool { return len(h.w.bulk) == 1 })
	h.release()
	for _, d := range []<-chan error{first, done} {
		if err := result(t, d); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.keys(); !slices.Equal(got, []string{"block", "live", "old"}) {
		t.Fatalf("keys = %v", got)
	}
}

func TestNewWriterNeedsAnApplier(t *testing.T) {
	if _, err := NewWriter(openWriter(t), WriterOptions{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestChanges(t *testing.T) {
	var c Changes
	if !c.Empty() {
		t.Fatal("zero Changes must be empty")
	}
	c.AddMessage(&domain.Message{ID: 1, ChatID: 7, Body: "v1"})
	c.UpdateMessage(&domain.Message{ID: 1, ChatID: 7, Body: "v2"})
	c.UpdateMessage(&domain.Message{ID: 2, ChatID: 7, Status: domain.StatusRead})
	if len(c.Added[7]) != 1 || c.Added[7][0].Body != "v2" || len(c.Updated[7]) != 1 {
		t.Fatalf("an update of a message added in the same commit must replace it: %+v", c)
	}
	for i := range MaxChangedMessages {
		c.AddMessage(&domain.Message{ID: domain.MessageID(100 + i), ChatID: 8})
	}
	c.AddMessage(&domain.Message{ID: 1000, ChatID: 8})
	if _, ok := c.Reload[8]; !ok || len(c.Added[8]) != 0 {
		t.Fatal("a chat over the cap must become a reload marker")
	}
	c.UpdateMessage(&domain.Message{ID: 100, ChatID: 8})
	if len(c.Updated[8]) != 0 {
		t.Fatal("a reloading chat must not collect messages again")
	}

	var merged Changes
	merged.AddMessage(&domain.Message{ID: 2, ChatID: 7})
	merged.Merge(&c)
	if len(merged.Added[7]) != 2 || len(merged.Updated[7]) != 0 {
		t.Fatalf("merge: %+v", merged.Added[7])
	}
	if _, ok := merged.Reload[8]; !ok {
		t.Fatal("merge must keep reload markers")
	}
	c.reset()
	if !c.Empty() {
		t.Fatal("reset must empty")
	}
}

type maintainingApplier struct {
	Applier
	calls chan struct{}
}

func (m maintainingApplier) Maintain(context.Context, *sql.Tx) error {
	m.calls <- struct{}{}
	return nil
}

func TestWriterRunsMaintenanceAtStart(t *testing.T) {
	m := maintainingApplier{Applier: ApplierFunc(func(context.Context, *sql.Tx, []domain.Event, *Changes) error { return nil }),
		calls: make(chan struct{}, 1)}
	w, err := NewWriter(openWriter(t), WriterOptions{Applier: m})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	select {
	case <-m.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("Maintain was not called at start")
	}
}
