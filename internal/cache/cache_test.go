package cache

import (
	"testing"
	"time"
)

func TestGetMissesOnAbsentAndExpired(t *testing.T) {
	c := New[string](1024, time.Minute)
	if _, ok := c.Get("nope"); ok {
		t.Error("Get returned an entry that was never stored")
	}

	c.Put("a", "x", 1)
	if v, ok := c.Get("a"); !ok || v != "x" {
		t.Fatalf("Get = %q, %v; want x, true", v, ok)
	}

	c.ExpireAllForTesting()
	if _, ok := c.Get("a"); ok {
		t.Error("Get returned an expired entry")
	}
}

func TestTTLZeroMeansNoExpiry(t *testing.T) {
	c := New[string](1024, 0)
	c.Put("a", "x", 1)
	c.ExpireAllForTesting() // ages past any positive TTL; a zero TTL must ignore it
	if v, ok := c.Get("a"); !ok || v != "x" {
		t.Errorf("Get = %q, %v; a zero TTL must not expire entries", v, ok)
	}
}

func TestLRUEvictsWhenOverBudget(t *testing.T) {
	c := New[string](200, time.Minute)
	c.Put("a", makeStr(120), 120)
	c.Put("b", makeStr(100), 100) // 220 > 200: evicts the LRU entry "a"

	if _, ok := c.Get("a"); ok {
		t.Error("a survived past the budget, want it evicted")
	}
	if v, ok := c.Get("b"); !ok || len(v) != 100 {
		t.Errorf("b = %d bytes, %v; want the 100-byte entry", len(v), ok)
	}

	// Touching b made a would-be re-put of a evict b instead.
	c.Get("b")
	c.Put("a", makeStr(120), 120) // 100+120 > 200: evicts b (now the LRU)
	if _, ok := c.Get("b"); ok {
		t.Error("b survived after a re-put over budget, want it evicted")
	}
	if v, ok := c.Get("a"); !ok || len(v) != 120 {
		t.Errorf("a = %d bytes, %v; want the 120-byte entry", len(v), ok)
	}
}

func TestOversizedEntryIsDropped(t *testing.T) {
	c := New[string](100, time.Minute)
	c.Put("big", makeStr(200), 200)
	if _, ok := c.Get("big"); ok {
		t.Error("an entry larger than the whole budget was kept")
	}
}

func TestPutReplacesExistingWithoutDoubleCounting(t *testing.T) {
	c := New[string](200, time.Minute)
	c.Put("a", makeStr(100), 100)
	c.Put("a", makeStr(50), 50)
	c.Put("b", makeStr(100), 100) // 50+100 = 150 ≤ 200: must not evict b…
	if _, ok := c.Get("b"); !ok {
		t.Error("b was evicted, want it kept (replaced entries must not double-count their size)")
	}
}

func makeStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
