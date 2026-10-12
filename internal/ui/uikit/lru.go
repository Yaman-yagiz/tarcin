// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

// LRU is a fixed-capacity cache that evicts the least recently used
// entry. It is not safe for concurrent use; UI caches are owned by the
// Bubble Tea goroutine.
type LRU[K comparable, V any] struct {
	capacity   int
	items      map[K]*lruEntry[K, V]
	head, tail *lruEntry[K, V] // head is the most recently used
	evictions  int
}

type lruEntry[K comparable, V any] struct {
	key        K
	value      V
	prev, next *lruEntry[K, V]
}

// NewLRU returns a cache holding up to capacity entries (at least one).
func NewLRU[K comparable, V any](capacity int) *LRU[K, V] {
	capacity = max(capacity, 1)
	return &LRU[K, V]{capacity: capacity, items: make(map[K]*lruEntry[K, V], capacity)}
}

// Get returns the value for key and marks it as recently used.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	e, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.moveToFront(e)
	return e.value, true
}

// Put stores value for key, evicting the least recently used entry when
// the cache is full.
func (c *LRU[K, V]) Put(key K, value V) {
	if e, ok := c.items[key]; ok {
		e.value = value
		c.moveToFront(e)
		return
	}
	if len(c.items) >= c.capacity {
		old := c.tail
		c.unlink(old)
		delete(c.items, old.key)
		c.evictions++
	}
	e := &lruEntry[K, V]{key: key, value: value}
	c.items[key] = e
	c.pushFront(e)
}

// Len returns the number of entries.
func (c *LRU[K, V]) Len() int { return len(c.items) }

// Evictions returns how many entries were evicted so far, for metrics.
func (c *LRU[K, V]) Evictions() int { return c.evictions }

// Clear removes every entry.
func (c *LRU[K, V]) Clear() {
	clear(c.items)
	c.head, c.tail = nil, nil
}

func (c *LRU[K, V]) moveToFront(e *lruEntry[K, V]) {
	if c.head == e {
		return
	}
	c.unlink(e)
	c.pushFront(e)
}

func (c *LRU[K, V]) pushFront(e *lruEntry[K, V]) {
	e.prev, e.next = nil, c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *LRU[K, V]) unlink(e *lruEntry[K, V]) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
}
