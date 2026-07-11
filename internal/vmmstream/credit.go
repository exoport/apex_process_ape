package vmmstream

import (
	"context"
	"errors"
	"sync"
)

// ErrCreditClosed is returned by Acquire once the window is closed.
var ErrCreditClosed = errors.New("vmmstream: credit window closed")

// CreditWindow is the flow-control budget between a producer and a consumer,
// counted in data frames. The producer must Acquire a credit before publishing
// a frame; the consumer Grants a credit as it drains one — so frames in flight
// (published, not yet consumed) never exceed the initial credit, and a fast
// producer can never outrun the consumer into a NATS slow-consumer drop
// (PLAN-18 D2). One producer calls Acquire; Grant/Close are safe from any
// goroutine.
type CreditWindow struct {
	mu     sync.Mutex
	cond   *sync.Cond
	avail  int
	closed bool
}

// NewCreditWindow starts a window with initial frames of credit.
func NewCreditWindow(initial int) *CreditWindow {
	w := &CreditWindow{avail: initial}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// Grant adds n credits and wakes a waiting Acquire. n<=0 is a no-op.
func (w *CreditWindow) Grant(n int) {
	if n <= 0 {
		return
	}
	w.mu.Lock()
	w.avail += n
	w.mu.Unlock()
	w.cond.Broadcast()
}

// Acquire blocks until n credits are available (consuming them), ctx is done, or
// the window is closed. It returns n on success, or 0 + an error otherwise.
func (w *CreditWindow) Acquire(ctx context.Context, n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	// sync.Cond has no ctx-aware wait: a watcher broadcasts on cancellation so a
	// blocked Wait re-checks ctx.Err(). done stops the watcher when Acquire returns.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			w.cond.Broadcast()
		case <-done:
		}
	}()

	w.mu.Lock()
	defer w.mu.Unlock()
	for w.avail < n && !w.closed && ctx.Err() == nil {
		w.cond.Wait()
	}
	switch {
	case w.closed:
		return 0, ErrCreditClosed
	case ctx.Err() != nil:
		return 0, ctx.Err()
	default:
		w.avail -= n
		return n, nil
	}
}

// Close unblocks every waiting Acquire with ErrCreditClosed.
func (w *CreditWindow) Close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.cond.Broadcast()
}

// Available reports the current credit (for tests/diagnostics).
func (w *CreditWindow) Available() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.avail
}
