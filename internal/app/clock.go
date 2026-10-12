// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"sync"
	"time"
)

// clockThreshold is the smallest difference from the server's clock that
// is corrected: WhatsApp stamps messages in whole seconds.
const clockThreshold = 2 * time.Second

// serverClock estimates WhatsApp's clock from this computer's. A computer
// whose clock is off would stamp its own messages before or after the
// replies to them, and misjudge which messages arrived after a chat was
// read. Server stamps are whole seconds and come from two sources:
//
//   - a send: the server stamped the message between the request and the
//     answer, so the offset is about the stamp, plus half a second, less
//     the middle of the two;
//   - a live message: the server stamped it before it arrived, so the
//     offset is at least the stamp less the arrival. Such a bound only
//     ever raises the estimate: a message delivered late, from an offline
//     queue, gives a bound far below the offset and must not move it.
//
// Without a send, a clock that is ahead is therefore not corrected; the
// first send corrects it.
type serverClock struct {
	now func() time.Time

	mu       sync.Mutex
	base     time.Duration // the latest send's estimate, or 0
	floor    time.Duration // the largest bound live messages gave
	hasFloor bool
}

// maxOffset bounds believable offsets; anything larger is a bad stamp.
const maxOffset = 24 * time.Hour

func plausible(d time.Duration) bool { return d > -maxOffset && d < maxOffset }

// Now returns the estimated server time.
func (c *serverClock) Now() time.Time { return c.now().Add(c.Offset()) }

// Offset returns the server's clock less this computer's, or 0 while it
// is under clockThreshold or unknown.
func (c *serverClock) Offset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	off := c.base
	if c.hasFloor && c.floor > off {
		off = c.floor
	}
	if off > -clockThreshold && off < clockThreshold {
		return 0
	}
	return off
}

// observeSend learns from a send that started and ended at the local
// times start and done and that the server stamped at stamp.
func (c *serverClock) observeSend(start, done, stamp time.Time) {
	if stamp.IsZero() || done.Before(start) || done.Sub(start) > 30*time.Second {
		return
	}
	off := stamp.Add(500 * time.Millisecond).Sub(start.Add(done.Sub(start) / 2))
	if !plausible(off) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base = off
}

// observeLive learns from a live message the server stamped at stamp and
// that arrived at the local time arrival.
func (c *serverClock) observeLive(stamp, arrival time.Time) {
	lower := stamp.Sub(arrival)
	if stamp.IsZero() || !plausible(lower) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasFloor || lower > c.floor {
		c.floor, c.hasFloor = lower, true
	}
}
