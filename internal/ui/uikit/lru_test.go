// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"math/rand/v2"
	"testing"
)

func TestLRU(t *testing.T) {
	c := NewLRU[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %d, %v", v, ok)
	}
	c.Put("c", 3) // evicts b, the least recently used
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if c.Len() != 2 || c.Evictions() != 1 {
		t.Fatalf("Len = %d, Evictions = %d", c.Len(), c.Evictions())
	}
	c.Put("a", 10) // update in place
	if v, _ := c.Get("a"); v != 10 || c.Len() != 2 {
		t.Fatalf("update failed: a=%d len=%d", v, c.Len())
	}
	c.Clear()
	if c.Len() != 0 {
		t.Fatal("Clear left entries")
	}
	c.Put("x", 1)
	if v, ok := c.Get("x"); !ok || v != 1 {
		t.Fatal("the cache is unusable after Clear")
	}
	if NewLRU[int, int](0).capacity != 1 {
		t.Fatal("capacity must be at least one")
	}
}

// TestLRUAgainstModel compares the cache with a simple reference model
// under random operations.
func TestLRUAgainstModel(t *testing.T) {
	const capacity = 8
	c := NewLRU[int, int](capacity)
	var order []int // most recent last
	values := map[int]int{}
	touch := func(k int) {
		for i, x := range order {
			if x == k {
				order = append(order[:i], order[i+1:]...)
				break
			}
		}
		order = append(order, k)
	}
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // deterministic test data
	for i := range 10_000 {
		k := r.IntN(20)
		if r.IntN(2) == 0 {
			c.Put(k, i)
			if _, ok := values[k]; !ok && len(values) == capacity {
				oldest := order[0]
				order = order[1:]
				delete(values, oldest)
			}
			values[k] = i
			touch(k)
			continue
		}
		v, ok := c.Get(k)
		want, wantOK := values[k]
		if ok != wantOK || v != want {
			t.Fatalf("op %d: Get(%d) = %d, %v; want %d, %v", i, k, v, ok, want, wantOK)
		}
		if ok {
			touch(k)
		}
	}
	if c.Len() != len(values) {
		t.Fatalf("Len = %d, want %d", c.Len(), len(values))
	}
}
