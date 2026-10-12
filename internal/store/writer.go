// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/trace"
	"sync"
	"sync/atomic"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Writer errors.
var (
	// ErrWriterClosed is returned for submissions after Close, and for
	// those still queued when Close gave up draining.
	ErrWriterClosed = errors.New("store: writer closed")
	// ErrDiskFull means a transaction failed with SQLITE_FULL. The writer
	// keeps running and recovers by itself once there is space.
	ErrDiskFull = errors.New("store: disk full")
	// ErrCorrupt means SQLite reported a corrupt database. The writer
	// stops writing: tarcin never runs blind repairs.
	ErrCorrupt = errors.New("store: database is corrupt")
)

// Applier writes a batch of events inside a writer transaction and
// records what changed. It must be deterministic: after a busy retry the
// writer applies the same events again in a new transaction.
//
// ctx is the writer's own context, never the submitter's: canceling a
// submission must not interrupt a transaction, because SQLite rolls back
// the whole transaction when a write statement is interrupted.
type Applier interface {
	Apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *Changes) error
}

// Maintainer is implemented by appliers with periodic upkeep. The writer
// runs Maintain in a transaction of its own at start and hourly, before
// PRAGMA optimize. Failures are ignored: upkeep is retried an hour later.
type Maintainer interface {
	Maintain(ctx context.Context, tx *sql.Tx) error
}

// ApplierFunc adapts a function to the Applier interface.
type ApplierFunc func(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *Changes) error

// Apply implements Applier.
func (f ApplierFunc) Apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *Changes) error {
	return f(ctx, tx, events, ch)
}

// WriterOptions configure a Writer. Zero values select the defaults.
type WriterOptions struct {
	// Applier writes the events. Required.
	Applier Applier
	// OnCommit receives what each commit changed, if anything did. It
	// runs on the writer goroutine and must not block; the receiver owns
	// the value.
	OnCommit func(*Changes)
	// OnHealth reports health changes: ErrDiskFull when writes start
	// failing for lack of space, nil when they succeed again, and
	// ErrCorrupt when the writer stops. It runs on the writer goroutine
	// and must not block.
	OnHealth func(error)

	LiveQueue      int           // capacity of the live queue (1024)
	BulkQueue      int           // capacity of the bulk queue (16)
	MaxBatchEvents int           // events per live transaction (256)
	MaxBatchTime   time.Duration // time spent filling a live transaction (10 ms)
	BusyRetry      time.Duration // how long a busy transaction is retried (5 s, the busy timeout)
	OptimizeEvery  time.Duration // interval of upkeep and PRAGMA optimize (1 h)
}

func (o *WriterOptions) defaults() {
	setDefault(&o.LiveQueue, 1024)
	setDefault(&o.BulkQueue, 16)
	setDefault(&o.MaxBatchEvents, 256)
	setDefault(&o.MaxBatchTime, 10*time.Millisecond)
	setDefault(&o.BusyRetry, 5*time.Second)
	setDefault(&o.OptimizeEvery, time.Hour)
}

func setDefault[T int | time.Duration](v *T, def T) {
	if *v <= 0 {
		*v = def
	}
}

// WriterStats are cumulative writer counters.
type WriterStats struct {
	Transactions uint64        // committed transactions
	Committed    uint64        // submissions committed
	Events       uint64        // events committed
	Failed       uint64        // submissions that failed (not counting cancellations)
	Canceled     uint64        // submissions canceled before they were applied
	BusyRetries  uint64        // transactions retried after SQLITE_BUSY
	MaxQueueWait time.Duration // longest time a submission waited in a queue
}

type request struct {
	ctx    context.Context
	events []domain.Event
	queued time.Time
	done   chan error // buffered: the writer never blocks on a submitter
}

// Writer is the only writer of the message database
// (docs/architecture/event-pipeline.md §3.2). Submissions go to a live
// or a bulk queue. The writer goroutine drains the live queue first and
// takes at most one bulk chunk between live transactions, so that a
// history sync cannot delay new messages by more than one chunk.
//
// Live submissions are group-committed: requests that queue up while a
// transaction is open join it, up to MaxBatchEvents events or
// MaxBatchTime. The writer never waits to fill a batch. Each submission
// runs in its own savepoint, so a submission that fails (bad data, a
// constraint) is rolled back alone while the others commit. Failures of
// the transaction itself (disk full, I/O errors, busy) fail all of them.
type Writer struct {
	db   *sql.DB
	opts WriterOptions
	live chan *request
	bulk chan *request

	mu     sync.RWMutex // guards closed against concurrent submissions
	closed bool
	stop   chan struct{} // closed by Close: drain the queues and exit
	abort  chan struct{} // closed when Close gives up draining
	exited chan struct{}

	broken atomic.Pointer[error] // set once the database is corrupt

	// Owned by the writer goroutine.
	health  error
	reqs    []*request
	errs    []error
	scratch Changes
	erased  bool // a commit deleted content: empty the WAL when idle

	transactions, committed, events, failed, canceled, busyRetries atomic.Uint64
	maxQueueWait                                                   atomic.Int64
}

// NewWriter starts the writer goroutine on db, which must be opened with
// RoleWriter. Close stops it; closing db is up to the caller.
func NewWriter(db *sql.DB, opts WriterOptions) (*Writer, error) {
	if opts.Applier == nil {
		return nil, errors.New("store: WriterOptions.Applier is required")
	}
	opts.defaults()
	w := &Writer{
		db:     db,
		opts:   opts,
		live:   make(chan *request, opts.LiveQueue),
		bulk:   make(chan *request, opts.BulkQueue),
		stop:   make(chan struct{}),
		abort:  make(chan struct{}),
		exited: make(chan struct{}),
	}
	go w.run()
	return w, nil
}

// SubmitLive queues live events and waits until they are committed. If
// the queue is full it waits for room, which pushes back on the
// protocol: decrypting slower is better than dropping messages.
//
// A nil error means the events are durable. If ctx ends first, SubmitLive
// returns its error and the events may or may not be committed later;
// since every write is idempotent, the protocol may then redeliver them.
// The writer may read events until it has dealt with the submission, so
// the caller must not modify the slice.
func (w *Writer) SubmitLive(ctx context.Context, events []domain.Event) error {
	return w.submit(ctx, w.live, events)
}

// SubmitBulk queues one history chunk, applied as one transaction, and
// waits until it is committed. See SubmitLive.
func (w *Writer) SubmitBulk(ctx context.Context, events []domain.Event) error {
	return w.submit(ctx, w.bulk, events)
}

// Deliver implements backend.Sink. It drops ephemeral events, which are
// never persisted, and routes batches that carry history to the bulk
// queue.
func (w *Writer) Deliver(ctx context.Context, events []domain.Event) error {
	durable := events
	for i, ev := range events {
		if !backend.Durable(ev) {
			durable = make([]domain.Event, 0, len(events))
			durable = append(durable, events[:i]...)
			for _, ev := range events[i+1:] {
				if backend.Durable(ev) {
					durable = append(durable, ev)
				}
			}
			break
		}
	}
	if isHistory(durable) {
		return w.SubmitBulk(ctx, durable)
	}
	return w.SubmitLive(ctx, durable)
}

func isHistory(events []domain.Event) bool {
	for _, ev := range events {
		if m, ok := ev.(domain.MessageReceived); ok && m.Source == domain.SourceHistory {
			return true
		}
	}
	return false
}

func (w *Writer) submit(ctx context.Context, q chan *request, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := w.brokenErr(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := &request{ctx: ctx, events: events, queued: time.Now(), done: make(chan error, 1)}
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return ErrWriterClosed
	}
	select {
	case q <- r:
		w.mu.RUnlock()
	case <-ctx.Done():
		w.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting submissions, commits what is queued and stops the
// writer. If ctx ends first, the remaining submissions fail with
// ErrWriterClosed and Close returns once the current transaction is
// done. Close runs PRAGMA optimize before returning.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.exited
		return nil
	}
	w.closed = true
	close(w.stop)
	w.mu.Unlock()
	select {
	case <-w.exited:
		return nil
	case <-ctx.Done():
		close(w.abort)
		<-w.exited
		return fmt.Errorf("draining the writer: %w", ctx.Err())
	}
}

// Queued returns how many submissions wait in the live and bulk queues.
func (w *Writer) Queued() (live, bulk int) { return len(w.live), len(w.bulk) }

// Stats returns the writer's counters.
func (w *Writer) Stats() WriterStats {
	return WriterStats{
		Transactions: w.transactions.Load(),
		Committed:    w.committed.Load(),
		Events:       w.events.Load(),
		Failed:       w.failed.Load(),
		Canceled:     w.canceled.Load(),
		BusyRetries:  w.busyRetries.Load(),
		MaxQueueWait: time.Duration(w.maxQueueWait.Load()),
	}
}

func (w *Writer) brokenErr() error {
	if p := w.broken.Load(); p != nil {
		return *p
	}
	return nil
}

func (w *Writer) aborted() bool {
	select {
	case <-w.abort:
		return true
	default:
		return false
	}
}

func (w *Writer) run() {
	defer close(w.exited)
	ctx := context.Background()
	w.maintain(ctx)
	w.optimize(ctx, "PRAGMA optimize = 0x10002")
	optimize := time.NewTicker(w.opts.OptimizeEvery)
	defer optimize.Stop()
	for {
		select {
		case r := <-w.live:
			w.liveTx(ctx, r)
			w.nextBulk(ctx)
			continue
		default:
		}
		if w.nextBulk(ctx) {
			continue
		}
		if w.erased {
			w.emptyWAL(ctx)
		}
		select {
		case r := <-w.live:
			w.liveTx(ctx, r)
			w.nextBulk(ctx)
		case r := <-w.bulk:
			w.bulkTx(ctx, r)
		case <-optimize.C:
			w.maintain(ctx)
			w.optimize(ctx, "PRAGMA optimize")
		case <-w.stop:
			// Close set closed before closing stop, so nothing new can
			// be queued: drain what is left, live first.
			for {
				select {
				case r := <-w.live:
					w.liveTx(ctx, r)
					continue
				default:
				}
				select {
				case r := <-w.bulk:
					w.bulkTx(ctx, r)
					continue
				default:
				}
				break
			}
			if !w.aborted() {
				if w.erased {
					w.emptyWAL(ctx)
				}
				w.optimize(ctx, "PRAGMA optimize")
			}
			return
		}
	}
}

// emptyWAL checkpoints the WAL and truncates it to nothing. With
// secure_delete, a deletion overwrites the rows in the database file,
// but the WAL keeps them until it is checkpointed and its frames are
// overwritten. The checkpoint waits briefly for readers that still see
// an older snapshot; if they hold it longer, it runs again after the next
// transaction or upkeep, so a live message never waits for it.
func (w *Writer) emptyWAL(ctx context.Context) {
	conn, err := w.db.Conn(ctx)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 100"); err != nil {
		return
	}
	defer func() { _, _ = conn.ExecContext(ctx, busyTimeout) }()
	var busy, frames, done int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err == nil && busy == 0 {
		w.erased = false
	}
}

// nextBulk commits one queued bulk chunk, if there is one.
func (w *Writer) nextBulk(ctx context.Context) bool {
	select {
	case r := <-w.bulk:
		w.bulkTx(ctx, r)
		return true
	default:
		return false
	}
}

func (w *Writer) liveTx(ctx context.Context, first *request) {
	w.reqs = append(w.reqs[:0], first)
	w.transaction(ctx, true)
}

func (w *Writer) bulkTx(ctx context.Context, r *request) {
	w.reqs = append(w.reqs[:0], r)
	w.transaction(ctx, false)
}

// transaction commits w.reqs, joined by more live requests if collect is
// set, retrying while the database is busy, and releases the submitters.
func (w *Writer) transaction(ctx context.Context, collect bool) {
	defer func() {
		clear(w.reqs)
		w.reqs = w.reqs[:0]
	}()
	if err := w.brokenErr(); err != nil {
		w.release(nil, err)
		return
	}
	if w.aborted() {
		w.release(nil, ErrWriterClosed)
		return
	}
	defer trace.StartRegion(ctx, "db.write").End()
	start := time.Now()
	for attempt := 0; ; attempt++ {
		changes, err := w.attempt(ctx, collect && attempt == 0)
		if isBusy(err) && time.Since(start) < w.opts.BusyRetry && !w.aborted() {
			w.busyRetries.Add(1)
			w.backoff(attempt)
			continue
		}
		w.release(changes, classify(err))
		return
	}
}

// attempt runs one transaction over w.reqs. It returns the merged
// changes, or the error that rolled the transaction back. Per-submission
// errors are left in w.errs.
func (w *Writer) attempt(ctx context.Context, collect bool) (*Changes, error) {
	w.errs = w.errs[:0]
	tx, err := w.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE: the writer DSN sets _txlock
	if err != nil {
		return nil, err
	}
	changes := &Changes{}
	events := 0
	for _, r := range w.reqs {
		if err := w.applyOne(ctx, tx, r, changes); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		events += len(r.events)
	}
	// The fill window starts now, not with the transaction: when the
	// first submission was slow (a slow disk), the ones that queued up
	// meanwhile are the ones worth batching.
	fill := time.Now()
	for collect && events < w.opts.MaxBatchEvents && time.Since(fill) < w.opts.MaxBatchTime && !w.aborted() {
		var r *request
		select {
		case r = <-w.live:
		default:
		}
		if r == nil {
			break
		}
		w.reqs = append(w.reqs, r)
		if err := w.applyOne(ctx, tx, r, changes); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		events += len(r.events)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changes, nil
}

// applyOne applies one submission in a savepoint and records its outcome
// in w.errs. It returns an error only when the whole transaction failed.
func (w *Writer) applyOne(ctx context.Context, tx *sql.Tx, r *request, changes *Changes) error {
	if wait := int64(time.Since(r.queued)); wait > w.maxQueueWait.Load() {
		w.maxQueueWait.Store(wait) // only the writer goroutine stores
	}
	if err := r.ctx.Err(); err != nil {
		w.errs = append(w.errs, err)
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT submission"); err != nil {
		return err
	}
	w.scratch.reset()
	err := w.opts.Applier.Apply(ctx, tx, r.events, &w.scratch)
	if err == nil {
		if _, err := tx.ExecContext(ctx, "RELEASE submission"); err != nil {
			return err
		}
		changes.Merge(&w.scratch)
		w.errs = append(w.errs, nil)
		return nil
	}
	if fatal(err) {
		return err
	}
	if _, rerr := tx.ExecContext(ctx, "ROLLBACK TO submission"); rerr != nil {
		return rerr
	}
	if _, rerr := tx.ExecContext(ctx, "RELEASE submission"); rerr != nil {
		return rerr
	}
	w.errs = append(w.errs, err)
	return nil
}

// release publishes the transaction's outcome, the changes and the
// health state, and then answers every submission: with txErr if the
// transaction failed, or with its own outcome. Publishing first means a
// submitter that sees success also sees the health it implies.
func (w *Writer) release(changes *Changes, txErr error) {
	switch {
	case txErr == nil:
		w.transactions.Add(1)
		w.setHealth(nil)
		if changes != nil && changes.Erased {
			w.erased = true
		}
		if changes != nil && !changes.Empty() && w.opts.OnCommit != nil {
			w.opts.OnCommit(changes)
		}
	case errors.Is(txErr, ErrCorrupt):
		if w.broken.CompareAndSwap(nil, &txErr) {
			w.setHealth(txErr)
		}
	case errors.Is(txErr, ErrDiskFull):
		w.setHealth(txErr)
	}
	for i, r := range w.reqs {
		err := txErr
		if err == nil {
			err = w.errs[i]
		}
		switch {
		case err == nil:
			w.committed.Add(1)
			w.events.Add(uint64(len(r.events)))
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			w.canceled.Add(1)
		default:
			w.failed.Add(1)
		}
		r.done <- err
	}
}

func (w *Writer) setHealth(err error) {
	if healthState(w.health) == healthState(err) {
		return
	}
	w.health = err
	if w.opts.OnHealth != nil {
		w.opts.OnHealth(err)
	}
}

// healthState maps a health error to its state: 0 healthy, 1 disk full,
// 2 corrupt.
func healthState(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrCorrupt):
		return 2
	}
	return 1
}

func (w *Writer) backoff(attempt int) {
	d := min(10*time.Millisecond<<attempt, 200*time.Millisecond)
	d = d/2 + rand.N(d) //nolint:gosec // jitter, not security
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-w.abort:
	}
}

func (w *Writer) maintain(ctx context.Context) {
	m, ok := w.opts.Applier.(Maintainer)
	if !ok || w.brokenErr() != nil {
		return
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	if err := m.Maintain(ctx, tx); err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

func (w *Writer) optimize(ctx context.Context, pragma string) {
	// Statistics only: a failure costs query plans, not data.
	_, _ = w.db.ExecContext(ctx, pragma)
}

func sqliteCode(err error) (sqlite3.ErrNo, bool) {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.Code, true
	}
	return 0, false
}

func isBusy(err error) bool {
	code, ok := sqliteCode(err)
	return ok && (code == sqlite3.ErrBusy || code == sqlite3.ErrLocked)
}

// fatal reports whether err ends the whole transaction rather than one
// submission. After these errors SQLite may already have rolled the
// transaction back.
func fatal(err error) bool {
	code, ok := sqliteCode(err)
	if !ok {
		return false
	}
	switch code {
	case sqlite3.ErrBusy, sqlite3.ErrLocked, sqlite3.ErrFull, sqlite3.ErrIoErr, sqlite3.ErrNomem,
		sqlite3.ErrCorrupt, sqlite3.ErrNotADB, sqlite3.ErrReadonly, sqlite3.ErrCantOpen,
		sqlite3.ErrInterrupt, sqlite3.ErrProtocol:
		return true
	}
	return false
}

// classify wraps transaction errors that change the writer's health.
func classify(err error) error {
	code, ok := sqliteCode(err)
	switch {
	case !ok:
		return err
	case code == sqlite3.ErrFull:
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	case code == sqlite3.ErrCorrupt || code == sqlite3.ErrNotADB:
		return fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return err
}
