package cache

import (
	"context"
	"sync"
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
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	waiters   int
	completed bool
	value     V
	err       error
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
		current.waiters++
		shared = true
	} else {
		base := coalescer.base
		if base == nil {
			base = context.Background()
		}
		loadContext, cancel := context.WithCancel(base)
		current = &call[V]{
			ctx:     loadContext,
			cancel:  cancel,
			done:    make(chan struct{}),
			waiters: 1,
		}
		coalescer.calls[key] = current
		go coalescer.run(key, current, load)
	}
	coalescer.mu.Unlock()

	select {
	case <-current.done:
		return current.value, current.err, shared
	case <-ctx.Done():
		coalescer.detach(key, current)
		return value, ctx.Err(), shared
	}
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

func (coalescer *Coalescer[K, V]) detach(key K, current *call[V]) {
	coalescer.mu.Lock()
	defer coalescer.mu.Unlock()

	if current.completed {
		return
	}
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
