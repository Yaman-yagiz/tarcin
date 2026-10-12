// SPDX-License-Identifier: GPL-3.0-or-later

// Package jobs runs bounded background work (docs/architecture/event-pipeline.md
// §3.5): protocol requests such as group metadata, media downloads and
// on-demand history. A pool has a fixed number of workers and a bounded
// queue; jobs with the same key run once at a time, and callers that ask
// for a running job share its result; a token bucket limits how often
// jobs start, to stay within the account-safety limits; every attempt
// has a deadline, and failures are retried with a capped exponential
// backoff with jitter. Jobs are not persisted: each can be derived again
// from the database.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Func is the work of a job. It must return when ctx ends.
type Func func(ctx context.Context) error

var (
	// ErrClosed is returned for jobs submitted to a closed pool, and to
	// waiters of jobs a closing pool did not run.
	ErrClosed = errors.New("jobs: pool closed")
	// ErrFull is returned when the queue has no room.
	ErrFull = errors.New("jobs: queue full")
)

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks an error that retrying cannot fix, such as a 404.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// Options configure a pool. Zero values take the defaults.
type Options struct {
	Name    string        // for the log
	Workers int           // jobs running at once (default 4)
	Queue   int           // jobs waiting for a worker (default 256)
	Rate    float64       // job attempts started per second; 0: no limit
	Burst   int           // attempts that may start at once (default 1)
	Timeout time.Duration // per attempt (default 30 s)
	Retries int           // attempts after the first (default 3; -1: none)
	Backoff Backoff
	Log     *slog.Logger // must redact (internal/logging); default: none
}

// Backoff is the delay before a retry: Base doubled per failed attempt,
// at most Max, varied by ±Jitter of itself.
type Backoff struct {
	Base   time.Duration // default 1 s
	Max    time.Duration // default 1 min
	Jitter float64       // default 0.2; negative: none
}

// Delay returns the delay after the given failed attempt (0 for the
// first).
func (b Backoff) Delay(attempt int) time.Duration {
	d := b.Base
	for range attempt {
		if d >= b.Max/2 {
			d = b.Max
			break
		}
		d *= 2
	}
	d = min(d, b.Max)
	if b.Jitter > 0 {
		f := 1 + b.Jitter*(2*rand.Float64()-1) //nolint:gosec // jitter, not security-sensitive
		d = time.Duration(float64(d) * f)
	}
	return d
}

// Stats are a pool's counters.
type Stats struct {
	Queued, Running int
	// InFlight counts the keys a new submission would join: the jobs
	// queued, running or finishing.
	InFlight int
	// Done and Failed count finished jobs; Retried counts attempts after
	// the first; Shared counts submissions that joined a job with the
	// same key; Dropped counts submissions refused for a full queue.
	Done, Failed, Retried, Shared, Dropped uint64
}

// Pool runs jobs. Create it with New and stop it with Close.
type Pool struct {
	o      Options
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan *flight
	bucket *bucket
	wg     sync.WaitGroup

	mu      sync.Mutex
	flights map[string]*flight // queued or running, by key
	closed  bool

	running                                atomic.Int64
	done, failed, retried, shared, dropped atomic.Uint64
}

// flight is one job and everyone waiting for it.
type flight struct {
	key  string
	fn   Func
	done chan struct{} // closed when err is set
	err  error
}

// New starts a pool.
func New(o Options) *Pool {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Queue <= 0 {
		o.Queue = 256
	}
	if o.Burst <= 0 {
		o.Burst = 1
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	switch {
	case o.Retries < 0:
		o.Retries = 0
	case o.Retries == 0:
		o.Retries = 3
	}
	if o.Backoff.Base <= 0 {
		o.Backoff.Base = time.Second
	}
	if o.Backoff.Max <= 0 {
		o.Backoff.Max = time.Minute
	}
	if o.Backoff.Jitter == 0 {
		o.Backoff.Jitter = 0.2
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		o: o, ctx: ctx, cancel: cancel, queue: make(chan *flight, o.Queue),
		flights: map[string]*flight{},
	}
	if o.Rate > 0 {
		p.bucket = newBucket(o.Rate, o.Burst, time.Now)
	}
	for range o.Workers {
		p.wg.Go(p.work)
	}
	return p
}

// Submit queues fn under key without waiting. If a job with that key is
// queued or running, fn is not queued: that job's result serves both.
// The error is ErrFull or ErrClosed when fn could not be queued.
func (p *Pool) Submit(key string, fn Func) error {
	_, err := p.join(key, fn)
	return err
}

// Do runs fn under key and waits for its result, or for ctx. A caller
// that finds a job with the same key queued or running waits for that
// one instead. The job runs under the pool's deadline, not ctx: when ctx
// ends first, Do returns, and the job goes on for its other waiters.
func (p *Pool) Do(ctx context.Context, key string, fn Func) error {
	f, err := p.join(key, fn)
	if err != nil {
		return err
	}
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) join(key string, fn Func) (*flight, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrClosed
	}
	if f, ok := p.flights[key]; ok {
		p.shared.Add(1)
		return f, nil
	}
	f := &flight{key: key, fn: fn, done: make(chan struct{})}
	select {
	case p.queue <- f:
	default:
		p.dropped.Add(1)
		return nil, ErrFull
	}
	p.flights[key] = f
	return f, nil
}

func (p *Pool) work() {
	for f := range p.queue {
		p.running.Add(1)
		err := p.attempts(f)
		p.running.Add(-1)
		p.finish(f, err)
	}
}

// attempts runs a job until it succeeds, fails permanently, runs out of
// retries or the pool closes.
func (p *Pool) attempts(f *flight) error {
	for attempt := 0; ; attempt++ {
		if err := p.ctx.Err(); err != nil {
			return ErrClosed
		}
		if p.bucket != nil {
			if err := p.bucket.wait(p.ctx); err != nil {
				return ErrClosed
			}
		}
		if attempt > 0 {
			p.retried.Add(1)
		}
		err := p.run(f)
		switch {
		case err == nil:
			return nil
		case p.ctx.Err() != nil:
			return ErrClosed
		case IsPermanent(err) || attempt >= p.o.Retries:
			return err
		}
		delay := p.o.Backoff.Delay(attempt)
		p.o.Log.Info("job failed; trying again", "pool", p.o.Name, "job", f.key, "in", delay, "err", err)
		t := time.NewTimer(delay)
		select {
		case <-p.ctx.Done():
			t.Stop()
			return ErrClosed
		case <-t.C:
		}
	}
}

// run makes one attempt under the attempt deadline. A panic is the job's
// failure, not the pool's.
func (p *Pool) run(f *flight) (err error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.o.Timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(fmt.Errorf("jobs: %s panicked: %s", f.key, panicText(r)))
		}
	}()
	return f.fn(ctx)
}

func (p *Pool) finish(f *flight, err error) {
	p.mu.Lock()
	delete(p.flights, f.key)
	p.mu.Unlock()
	f.err = err
	close(f.done)
	switch {
	case err == nil:
		p.done.Add(1)
	case errors.Is(err, ErrClosed):
	default:
		p.failed.Add(1)
		p.o.Log.Warn("job failed", "pool", p.o.Name, "job", f.key, "err", err)
	}
}

// Stats returns the pool's counters.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	inFlight := len(p.flights)
	p.mu.Unlock()
	return Stats{
		Queued: len(p.queue), Running: int(p.running.Load()), InFlight: inFlight,
		Done: p.done.Load(), Failed: p.failed.Load(), Retried: p.retried.Load(),
		Shared: p.shared.Load(), Dropped: p.dropped.Load(),
	}
}

// Close stops the pool: new jobs are refused, running ones are canceled,
// and queued ones end with ErrClosed. It returns when every worker has
// stopped.
func (p *Pool) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	p.cancel()
	p.wg.Wait()
}

// panicText describes a recovered panic without its value, which may hold
// message content: a runtime error's text, or the value's type.
func panicText(r any) string {
	if re, ok := r.(runtime.Error); ok {
		return re.Error()
	}
	return fmt.Sprintf("%T", r)
}
