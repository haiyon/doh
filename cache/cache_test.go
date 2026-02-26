package cache

import (
	"testing"
	"time"
)

func TestSetGetCopiesValue(t *testing.T) {
	c := New(time.Minute)
	t.Cleanup(c.Close)

	input := []byte{1, 2, 3}
	c.Set("k", input)
	input[0] = 9

	got, ok := c.Get("k")
	if !ok {
		t.Fatalf("expected cached value")
	}
	if got[0] != 1 {
		t.Fatalf("cache value was mutated by caller, got=%v", got)
	}

	got[1] = 7
	again, ok := c.Get("k")
	if !ok {
		t.Fatalf("expected cached value on second read")
	}
	if again[1] != 2 {
		t.Fatalf("cache exposed internal slice, got=%v", again)
	}
}

func TestExpiredEntryIsRemovedOnGet(t *testing.T) {
	c := New(20 * time.Millisecond)
	t.Cleanup(c.Close)

	c.Set("k", []byte{1})
	time.Sleep(40 * time.Millisecond)

	if _, ok := c.Get("k"); ok {
		t.Fatalf("expected expired entry to be absent")
	}

	c.mu.RLock()
	_, exists := c.items["k"]
	c.mu.RUnlock()
	if exists {
		t.Fatalf("expected expired entry to be deleted")
	}
}

func TestSetWithTTLOverridesDefault(t *testing.T) {
	c := New(time.Minute)
	t.Cleanup(c.Close)

	c.SetWithTTL("k", []byte{1}, 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)

	if _, ok := c.Get("k"); ok {
		t.Fatalf("expected entry to expire with custom TTL")
	}
}

func TestSetWithTTLZeroFallsBackToDefault(t *testing.T) {
	c := New(time.Minute)
	t.Cleanup(c.Close)

	c.SetWithTTL("k", []byte{1}, 0)
	if _, ok := c.Get("k"); !ok {
		t.Fatalf("expected entry to be present with default TTL fallback")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c := New(time.Minute)
	c.Close()
	c.Close()
}
