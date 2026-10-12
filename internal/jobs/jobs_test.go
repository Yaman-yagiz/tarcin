// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fast = Backoff{Base: time.Millisecond, Max: 4 * time.Millisecond, Jitter: -1}

func TestWorkersBoundConcurrency(t *testing.T) {
	p := New(Options{Workers: 3, Backoff: fast})
	defer p.Close()
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		err := p.Submit(fmt.Sprint(i), func(context.Context) error {
			defer wg.Done()
			n := running.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak concurrency %d, want 3", got)
	}
	waitFor(t, func() bool { return p.Stats().Done == 20 })
}

func TestSameKeyRunsOnce(t *testing.T) {
	p := New(Options{Workers: 2, Backoff: fast})
	defer p.Close()
	release := make(chan struct{})
	var runs atomic.Int32
	job := func(context.Context) error {
		runs.Add(1)
		<-release
		return errors.New("no such group")
	}
	errs := make(chan error, 3)
	for range 3 {
		go func() { errs <- p.Do(context.Background(), "group.refresh(g1)", job) }()
	}
	waitFor(t, func() bool { return p.Stats().Shared == 2 })
	if err := p.Submit("group.refresh(g1)", job); err != nil {
		t.Fatal(err)
	}
	close(release)
	for range 3 {
		if err := <-errs; err == nil || err.Error() != "no such group" {
			t.Fatalf("every waiter gets the result: %v", err)
		}
	}
	if n := runs.Load(); n != 4 { // 1 attempt + 3 retries, shared by all
		t.Fatalf("runs = %d", n)
	}
	// Once finished, the key can run again.
	if err := p.Do(context.Background(), "group.refresh(g1)", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestDoReturnsWhenTheCallerGivesUp(t *testing.T) {
	p := New(Options{Workers: 1})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	if err := p.Do(ctx, "slow", func(context.Context) error { <-release; return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do = %v", err)
	}
}

func TestRetriesAndPermanentErrors(t *testing.T) {
	p := New(Options{Workers: 1, Retries: 3, Backoff: fast})
	defer p.Close()
	var runs atomic.Int32
	err := p.Do(context.Background(), "flaky", func(context.Context) error {
		if runs.Add(1) < 3 {
			return errors.New("timeout")
		}
		return nil
	})
	if err != nil || runs.Load() != 3 {
		t.Fatalf("flaky: %v after %d runs", err, runs.Load())
	}
	runs.Store(0)
	gone := errors.New("404")
	err = p.Do(context.Background(), "gone", func(context.Context) error { runs.Add(1); return Permanent(gone) })
	if !errors.Is(err, gone) || !IsPermanent(err) || runs.Load() != 1 {
		t.Fatalf("permanent: %v after %d runs", err, runs.Load())
	}
	if s := p.Stats(); s.Done != 1 || s.Failed != 1 || s.Retried != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must be nil")
	}
	none := New(Options{Retries: -1})
	defer none.Close()
	runs.Store(0)
	_ = none.Do(context.Background(), "once", func(context.Context) error { runs.Add(1); return errors.New("x") })
	if runs.Load() != 1 {
		t.Fatalf("Retries -1 runs %d times", runs.Load())
	}
}

func TestAttemptDeadlineAndPanics(t *testing.T) {
	p := New(Options{Workers: 1, Timeout: 20 * time.Millisecond, Retries: -1})
	defer p.Close()
	start := time.Now()
	err := p.Do(context.Background(), "hang", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("hang: %v after %v", err, time.Since(start))
	}
	err = p.Do(context.Background(), "bug", func(context.Context) error { panic("nil map") })
	if !IsPermanent(err) {
		t.Fatalf("a panic is a permanent failure: %v", err)
	}
	if err := p.Do(context.Background(), "after", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("the pool must survive a panic: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	p := New(Options{Workers: 4, Rate: 50, Burst: 2})
	defer p.Close()
	start := time.Now()
	var wg sync.WaitGroup
	for i := range 7 {
		wg.Go(func() { _ = p.Do(context.Background(), fmt.Sprint(i), func(context.Context) error { return nil }) })
	}
	wg.Wait()
	// Two start at once, five more at 50 per second: about 100 ms.
	if d := time.Since(start); d < 80*time.Millisecond || d > 2*time.Second {
		t.Fatalf("7 jobs took %v", d)
	}
}

func TestBucket(t *testing.T) {
	now := time.Unix(0, 0)
	b := newBucket(10, 2, func() time.Time { return now })
	for i := range 2 {
		if d := b.reserve(); d != 0 {
			t.Fatalf("start %d of the burst waits %v", i, d)
		}
	}
	if d := b.reserve(); d != 100*time.Millisecond {
		t.Fatalf("third start waits %v", d)
	}
	b.unreserve()
	now = now.Add(time.Hour)
	got := []time.Duration{b.reserve(), b.reserve(), b.reserve()}
	if got[0] != 0 || got[1] != 0 || got[2] == 0 {
		t.Fatalf("tokens refill up to the burst only: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v", err)
	}
}

func TestBackoffDelay(t *testing.T) {
	b := Backoff{Base: time.Second, Max: 10 * time.Second}
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second} {
		if got := b.Delay(attempt); got != want {
			t.Errorf("Delay(%d) = %v, want %v", attempt, got, want)
		}
	}
	if got := b.Delay(1000); got != 10*time.Second {
		t.Fatalf("a huge attempt must not overflow: %v", got)
	}
	b.Jitter = 0.2
	for range 100 {
		if d := b.Delay(2); d < 3200*time.Millisecond || d > 4800*time.Millisecond {
			t.Fatalf("jittered delay %v outside ±20%% of 4 s", d)
		}
	}
}

func TestQueueFullAndClose(t *testing.T) {
	p := New(Options{Workers: 1, Queue: 1})
	release := make(chan struct{})
	started := make(chan struct{})
	if err := p.Submit("a", func(context.Context) error { close(started); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := p.Submit("b", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit("c", func(context.Context) error { return nil }); !errors.Is(err, ErrFull) {
		t.Fatalf("a full queue: %v", err)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- p.Do(context.Background(), "b", nil) }() // joins the queued b
	waitFor(t, func() bool { return p.Stats().Shared == 1 })
	close(release)
	go p.Close()
	if err := <-waiter; err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("a queued job ends or is closed: %v", err)
	}
	waitFor(t, func() bool { return errors.Is(p.Submit("d", func(context.Context) error { return nil }), ErrClosed) })
	p.Close() // twice is fine
	if s := p.Stats(); s.Dropped != 1 || s.Running != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestCloseCancelsRunningJobs(t *testing.T) {
	p := New(Options{Workers: 1, Timeout: time.Hour})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.Do(context.Background(), "long", func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	<-started
	p.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("Do = %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
