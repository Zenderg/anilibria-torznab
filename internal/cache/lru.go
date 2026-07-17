// Package cache provides bounded in-memory cache and request-coalescing
// primitives. Cached values are returned as stored; callers own any defensive
// copying required for mutable values such as slices and maps.
package cache

import (
	"container/heap"
	"container/list"
	"sync"
	"time"
)

// LRU is a concurrency-safe, bounded least-recently-used cache. Get promotes a
// live entry to most-recently-used. When capacity is exceeded, Set evicts the
// least-recently-used entry deterministically.
type LRU[K comparable, V any] struct {
	mu         sync.Mutex
	maxEntries int
	now        func() time.Time
	entries    map[K]*list.Element
	recency    list.List
	expiry     expiryHeap[K, V]
}

type entry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
	element   *list.Element
	heapIndex int
}

type expiryHeap[K comparable, V any] []*entry[K, V]

func (items expiryHeap[K, V]) Len() int {
	return len(items)
}

func (items expiryHeap[K, V]) Less(first, second int) bool {
	return items[first].expiresAt.Before(items[second].expiresAt)
}

func (items expiryHeap[K, V]) Swap(first, second int) {
	items[first], items[second] = items[second], items[first]
	items[first].heapIndex = first
	items[second].heapIndex = second
}

func (items *expiryHeap[K, V]) Push(value any) {
	item := value.(*entry[K, V])
	item.heapIndex = len(*items)
	*items = append(*items, item)
}

func (items *expiryHeap[K, V]) Pop() any {
	old := *items
	last := len(old) - 1
	item := old[last]
	old[last] = nil
	item.heapIndex = -1
	*items = old[:last]
	return item
}

// NewLRU creates a cache with the given capacity. It uses the system clock by
// default; an optional clock provides a deterministic test seam. maxEntries
// must be positive. At most one clock may be supplied.
func NewLRU[K comparable, V any](maxEntries int, clocks ...func() time.Time) *LRU[K, V] {
	switch len(clocks) {
	case 0:
		return NewLRUWithClock[K, V](maxEntries, time.Now)
	case 1:
		return NewLRUWithClock[K, V](maxEntries, clocks[0])
	default:
		panic("cache: at most one clock may be supplied")
	}
}

// NewLRUWithClock creates a cache using now to evaluate expiry. It is useful
// for deterministic tests. maxEntries must be positive and now must be non-nil.
func NewLRUWithClock[K comparable, V any](maxEntries int, now func() time.Time) *LRU[K, V] {
	if maxEntries <= 0 {
		panic("cache: maxEntries must be positive")
	}
	if now == nil {
		panic("cache: clock must be non-nil")
	}
	return &LRU[K, V]{
		maxEntries: maxEntries,
		now:        now,
		entries:    make(map[K]*list.Element, maxEntries),
	}
}

// Get returns a live entry and marks it most-recently-used. Expired entries are
// removed lazily and reported as misses.
func (cache *LRU[K, V]) Get(key K) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	element, ok := cache.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	item := element.Value.(*entry[K, V])
	if !cache.now().Before(item.expiresAt) {
		cache.remove(element)
		var zero V
		return zero, false
	}

	cache.recency.MoveToFront(element)
	return item.value, true
}

// Set stores value until ttl elapses. A non-positive TTL removes an existing
// value for key and does not store the new value.
func (cache *LRU[K, V]) Set(key K, value V, ttl time.Duration) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if ttl <= 0 {
		if element, ok := cache.entries[key]; ok {
			cache.remove(element)
		}
		return
	}

	now := cache.now()
	expiresAt := now.Add(ttl)
	if element, ok := cache.entries[key]; ok {
		item := element.Value.(*entry[K, V])
		item.value = value
		item.expiresAt = expiresAt
		heap.Fix(&cache.expiry, item.heapIndex)
		cache.recency.MoveToFront(element)
		return
	}

	item := &entry[K, V]{key: key, value: value, expiresAt: expiresAt}
	element := cache.recency.PushFront(item)
	item.element = element
	cache.entries[key] = element
	heap.Push(&cache.expiry, item)
	if cache.recency.Len() > cache.maxEntries {
		cache.removeExpired(now)
		if cache.recency.Len() > cache.maxEntries {
			cache.remove(cache.recency.Back())
		}
	}
}

// Delete removes key if present.
func (cache *LRU[K, V]) Delete(key K) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.entries[key]; ok {
		cache.remove(element)
	}
}

// Len returns the number of live entries. It also removes all expired entries.
func (cache *LRU[K, V]) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.removeExpired(cache.now())
	return cache.recency.Len()
}

func (cache *LRU[K, V]) remove(element *list.Element) {
	item := element.Value.(*entry[K, V])
	delete(cache.entries, item.key)
	cache.recency.Remove(element)
	heap.Remove(&cache.expiry, item.heapIndex)
}

func (cache *LRU[K, V]) removeExpired(now time.Time) {
	for len(cache.expiry) > 0 && !now.Before(cache.expiry[0].expiresAt) {
		cache.remove(cache.expiry[0].element)
	}
}

// TTLForResult selects negativeTTL for a successful empty result and
// positiveTTL for a successful non-empty result. Errors should not be cached.
func TTLForResult(empty bool, positiveTTL, negativeTTL time.Duration) time.Duration {
	if empty {
		return negativeTTL
	}
	return positiveTTL
}

// TTLFor is a concise alias for TTLForResult.
func TTLFor(empty bool, positiveTTL, negativeTTL time.Duration) time.Duration {
	return TTLForResult(empty, positiveTTL, negativeTTL)
}
