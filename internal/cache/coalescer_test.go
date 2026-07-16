package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalescerCollapsesSameKey(t *testing.T) {
	coalescer := NewCoalescer[string, int](context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32
	load := func(context.Context) (int, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return 42, nil
	}

	type result struct {
		value  int
		err    error
		shared bool
	}
	results := make(chan result, 2)
	go func() {
		value, err, shared := coalescer.DoShared(context.Background(), "key", load)
		results <- result{value: value, err: err, shared: shared}
	}()
	<-started
	go func() {
		value, err, shared := coalescer.DoShared(context.Background(), "key", load)
		results <- result{value: value, err: err, shared: shared}
	}()

	waitFor(t, func() bool {
		coalescer.mu.Lock()
		defer coalescer.mu.Unlock()
		return coalescer.calls["key"].waiters == 2
	})
	close(release)

	sharedCount := 0
	for index := 0; index < 2; index++ {
		got := <-results
		if got.err != nil || got.value != 42 {
			t.Errorf("Do() = %d, %v", got.value, got.err)
		}
		if got.shared {
			sharedCount++
		}
	}
	if loads.Load() != 1 {
		t.Errorf("loads = %d", loads.Load())
	}
	if sharedCount != 1 {
		t.Errorf("shared results = %d, want 1", sharedCount)
	}
}

func TestCoalescerFirstCancellationDoesNotCancelSibling(t *testing.T) {
	coalescer := NewCoalescer[string, int](context.Background())
	loaderStarted := make(chan struct{})
	loaderCancelled := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		close(loaderStarted)
		select {
		case <-release:
			return 7, nil
		case <-ctx.Done():
			close(loaderCancelled)
			return 0, ctx.Err()
		}
	}

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := coalescer.Do(firstContext, "key", load)
		firstResult <- err
	}()
	<-loaderStarted

	siblingResult := make(chan struct {
		value int
		err   error
	}, 1)
	go func() {
		value, err := coalescer.Do(context.Background(), "key", load)
		siblingResult <- struct {
			value int
			err   error
		}{value: value, err: err}
	}()
	waitFor(t, func() bool {
		coalescer.mu.Lock()
		defer coalescer.mu.Unlock()
		return coalescer.calls["key"].waiters == 2
	})

	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}
	select {
	case <-loaderCancelled:
		t.Fatal("first waiter cancelled the shared load")
	default:
	}

	close(release)
	got := <-siblingResult
	if got.err != nil || got.value != 7 {
		t.Fatalf("sibling = %d, %v", got.value, got.err)
	}
}

func TestCoalescerLastCancellationCancelsLoad(t *testing.T) {
	coalescer := NewCoalescer[string, int](context.Background())
	loaderStarted := make(chan struct{})
	loaderCancelled := make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		close(loaderStarted)
		<-ctx.Done()
		close(loaderCancelled)
		return 0, ctx.Err()
	}

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() {
		_, err := coalescer.Do(waiterContext, "key", load)
		waiterResult <- err
	}()
	<-loaderStarted
	cancelWaiter()

	if err := <-waiterResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v", err)
	}
	select {
	case <-loaderCancelled:
	case <-time.After(time.Second):
		t.Fatal("last waiter did not cancel the load")
	}
}

func TestCoalescerStartsFreshLoadAfterAllWaitersDetach(t *testing.T) {
	coalescer := NewCoalescer[string, int](context.Background())
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	var loads atomic.Int32
	load := func(ctx context.Context) (int, error) {
		attempt := loads.Add(1)
		if attempt == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			return 0, ctx.Err()
		}
		return 9, nil
	}

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := coalescer.Do(firstContext, "key", load)
		firstResult <- err
	}()
	<-firstStarted
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}

	value, err, shared := coalescer.DoShared(context.Background(), "key", load)
	if err != nil || value != 9 || shared {
		t.Fatalf("fresh Do() = %d, %v, shared %v", value, err, shared)
	}
	if loads.Load() != 2 {
		t.Fatalf("loads = %d", loads.Load())
	}
	select {
	case <-firstCancelled:
	case <-time.After(time.Second):
		t.Fatal("abandoned first load did not observe cancellation")
	}
}

func TestCoalescerDoesNotMergeDistinctKeys(t *testing.T) {
	coalescer := NewCoalescer[string, string](context.Background())
	var loads atomic.Int32
	var workers sync.WaitGroup
	results := make(chan string, 2)
	for _, key := range []string{"search:1", "torrents:1"} {
		key := key
		workers.Add(1)
		go func() {
			defer workers.Done()
			value, err := coalescer.Do(context.Background(), key, func(context.Context) (string, error) {
				loads.Add(1)
				return key, nil
			})
			if err != nil {
				t.Errorf("Do(%q) error = %v", key, err)
			}
			results <- value
		}()
	}
	workers.Wait()
	close(results)
	if loads.Load() != 2 {
		t.Fatalf("loads = %d", loads.Load())
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}
