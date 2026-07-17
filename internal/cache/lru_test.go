package cache

import (
	"sync"
	"testing"
	"time"
)

func TestLRUEvictionIsDeterministic(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewLRUWithClock[string, int](2, func() time.Time { return now })
	cache.Set("a", 1, time.Minute)
	cache.Set("b", 2, time.Minute)

	if _, ok := cache.Get("a"); !ok {
		t.Fatal("Get(a) missed")
	}
	cache.Set("c", 3, time.Minute)

	if _, ok := cache.Get("b"); ok {
		t.Error("least-recently-used b was not evicted")
	}
	if got, ok := cache.Get("a"); !ok || got != 1 {
		t.Errorf("Get(a) = %v, %v", got, ok)
	}
	if got, ok := cache.Get("c"); !ok || got != 3 {
		t.Errorf("Get(c) = %v, %v", got, ok)
	}
}

func TestLRUExpiresEntriesAndUsesExactBoundary(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewLRUWithClock[string, string](2, func() time.Time { return now })
	cache.Set("key", "value", time.Minute)

	now = now.Add(time.Minute - time.Nanosecond)
	if _, ok := cache.Get("key"); !ok {
		t.Fatal("entry expired before its deadline")
	}
	now = now.Add(time.Nanosecond)
	if _, ok := cache.Get("key"); ok {
		t.Fatal("entry remained live at its expiry deadline")
	}
	if got := cache.Len(); got != 0 {
		t.Errorf("Len() = %d", got)
	}
}

func TestLRUEvictionPrefersExpiredEntryOverLiveLRU(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewLRUWithClock[string, int](2, func() time.Time { return now })
	cache.Set("live", 1, time.Hour)
	cache.Set("short", 2, 2*time.Second)

	now = now.Add(2 * time.Second)
	cache.Set("new", 3, time.Hour)

	if got, ok := cache.Get("live"); !ok || got != 1 {
		t.Fatalf("Get(live) = %v, %v", got, ok)
	}
	if got, ok := cache.Get("new"); !ok || got != 3 {
		t.Fatalf("Get(new) = %v, %v", got, ok)
	}
	if _, ok := cache.Get("short"); ok {
		t.Fatal("expired short entry remained cached")
	}
}

func TestLRUUpdateAndDelete(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewLRUWithClock[string, int](1, func() time.Time { return now })
	cache.Set("key", 1, time.Minute)
	cache.Set("key", 2, 2*time.Minute)
	if got, ok := cache.Get("key"); !ok || got != 2 {
		t.Fatalf("Get(key) = %v, %v", got, ok)
	}
	cache.Set("key", 3, 0)
	if _, ok := cache.Get("key"); ok {
		t.Fatal("Set with zero TTL did not remove key")
	}
	cache.Set("key", 4, time.Minute)
	cache.Delete("key")
	if _, ok := cache.Get("key"); ok {
		t.Fatal("Delete did not remove key")
	}
}

func TestLRUIsConcurrencySafe(t *testing.T) {
	cache := NewLRU[int, int](16)
	var workers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 1000; index++ {
				key := (worker + index) % 32
				cache.Set(key, index, time.Minute)
				cache.Get(key)
				if index%11 == 0 {
					cache.Delete((key + 1) % 32)
				}
			}
		}(worker)
	}
	workers.Wait()
	if got := cache.Len(); got > 16 {
		t.Fatalf("Len() = %d, exceeds capacity", got)
	}
}

func TestTTLForResult(t *testing.T) {
	if got := TTLForResult(false, 10*time.Minute, time.Minute); got != 10*time.Minute {
		t.Errorf("positive TTL = %s", got)
	}
	if got := TTLForResult(true, 10*time.Minute, time.Minute); got != time.Minute {
		t.Errorf("negative TTL = %s", got)
	}
}
