// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"sync"
	"time"
)

// bucket is a token bucket: it holds up to burst tokens, refilled at rate
// per second, and each start takes one.
type bucket struct {
	rate, burst float64
	now         func() time.Time

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newBucket(rate float64, burst int, now func() time.Time) *bucket {
	return &bucket{rate: rate, burst: float64(burst), now: now, tokens: float64(burst), last: now()}
}

// reserve takes a token and returns how long to wait until it is due.
func (b *bucket) reserve() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed.Seconds()*b.rate)
		b.last = now
	}
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// unreserve gives back a token that was not used.
func (b *bucket) unreserve() {
	b.mu.Lock()
	b.tokens = min(b.burst, b.tokens+1)
	b.mu.Unlock()
}

// wait takes a token, waiting until it is due or ctx ends.
func (b *bucket) wait(ctx context.Context) error {
	d := b.reserve()
	if d == 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		b.unreserve()
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
