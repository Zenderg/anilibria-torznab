package cache

import (
	"context"
	"sync"
	"time"
)

// Coalescer collapses concurrent loads for equal keys. Its zero value is ready
// to use and derives load contexts from context.Background.
//
// A caller's cancellation only detaches that caller. The shared load continues
// while another waiter remains and is cancelled when the last waiter detaches.
type Coalescer[K comparable, V any] struct {
	mu    sync.Mutex
	base  context.Context
	calls map[K]*call[V]
}

type call[V any] struct {
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	waiters      int
	nextWaiterID uint64
	budgets      map[uint64]waiterBudget
	completed    bool
	value        V
	err          error
}

type waiterBudget struct {
	deadline time.Time
	bounded  bool
}

type coalescedContext[K comparable, V any] struct {
	context.Context
	coalescer *Coalescer[K, V]
	current   *call[V]
}

// NewCoalescer creates a coalescer whose loads are also cancelled when base is
// cancelled. A nil base is treated as context.Background.
func NewCoalescer[K comparable, V any](base context.Context) *Coalescer[K, V] {
	if base == nil {
		base = context.Background()
	}
	return &Coalescer[K, V]{base: base}
}

// Do joins or starts a load for key. Completed values and errors are not
// retained by the coalescer; cache storage remains the caller's responsibility.
func (coalescer *Coalescer[K, V]) Do(
	ctx context.Context,
	key K,
	load func(context.Context) (V, error),
) (V, error) {
	value, err, _ := coalescer.DoShared(ctx, key, load)
	return value, err
}

// DoShared is like Do and additionally reports whether this caller joined an
// existing in-flight load.
func (coalescer *Coalescer[K, V]) DoShared(
	ctx context.Context,
	key K,
	load func(context.Context) (V, error),
) (value V, err error, shared bool) {
	if ctx == nil {
		panic("cache: waiter context must be non-nil")
	}
	if load == nil {
		panic("cache: load function must be non-nil")
	}
	if err := ctx.Err(); err != nil {
		return value, err, false
	}

	coalescer.mu.Lock()
	if coalescer.calls == nil {
		coalescer.calls = make(map[K]*call[V])
	}
	current, exists := coalescer.calls[key]
	if exists {
		shared = true
	} else {
		base := coalescer.base
		if base == nil {
			base = context.Background()
		}
		loadContext, cancel := context.WithCancel(base)
		current = &call[V]{
			cancel:  cancel,
			done:    make(chan struct{}),
			budgets: make(map[uint64]waiterBudget),
		}
		current.ctx = &coalescedContext[K, V]{
			Context:   loadContext,
			coalescer: coalescer,
			current:   current,
		}
		coalescer.calls[key] = current
	}
	waiterID := current.addWaiter(ctx)
	if !exists {
		go coalescer.run(key, current, load)
	}
	coalescer.mu.Unlock()

	select {
	case <-current.done:
		return current.value, current.err, shared
	case <-ctx.Done():
		coalescer.detach(key, current, waiterID)
		return value, ctx.Err(), shared
	}
}

func (current *call[V]) addWaiter(ctx context.Context) uint64 {
	current.nextWaiterID++
	waiterID := current.nextWaiterID
	deadline, bounded := ctx.Deadline()
	current.budgets[waiterID] = waiterBudget{deadline: deadline, bounded: bounded}
	current.waiters++
	return waiterID
}

func (coalescer *Coalescer[K, V]) run(key K, current *call[V], load func(context.Context) (V, error)) {
	value, err := load(current.ctx)

	coalescer.mu.Lock()
	current.value = value
	current.err = err
	current.completed = true
	if coalescer.calls[key] == current {
		delete(coalescer.calls, key)
	}
	close(current.done)
	current.cancel()
	coalescer.mu.Unlock()
}

func (coalescer *Coalescer[K, V]) detach(key K, current *call[V], waiterID uint64) {
	coalescer.mu.Lock()
	defer coalescer.mu.Unlock()

	if current.completed {
		return
	}
	delete(current.budgets, waiterID)
	current.waiters--
	if current.waiters == 0 {
		// Remove before cancelling. A new caller must start fresh rather than
		// joining a load that no caller needs and whose context is cancelled.
		if coalescer.calls[key] == current {
			delete(coalescer.calls, key)
		}
		current.cancel()
	}
}

// EffectiveDeadline reports the load budget implied by all active waiters.
// An unbounded waiter keeps the shared load unbounded; otherwise the latest
// waiter deadline applies, subject to an earlier base-context deadline.
func (ctx *coalescedContext[K, V]) EffectiveDeadline() (time.Time, bool) {
	baseDeadline, baseBounded := ctx.Context.Deadline()

	ctx.coalescer.mu.Lock()
	defer ctx.coalescer.mu.Unlock()

	var latest time.Time
	waitersBounded := len(ctx.current.budgets) > 0
	for _, budget := range ctx.current.budgets {
		if !budget.bounded {
			waitersBounded = false
			break
		}
		if latest.IsZero() || budget.deadline.After(latest) {
			latest = budget.deadline
		}
	}

	switch {
	case baseBounded && (!waitersBounded || baseDeadline.Before(latest)):
		return baseDeadline, true
	case waitersBounded:
		return latest, true
	default:
		return time.Time{}, false
	}
}
