package vmmstream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreditAcquireImmediate(t *testing.T) {
	w := NewCreditWindow(3)
	got, err := w.Acquire(context.Background(), 2)
	if err != nil || got != 2 {
		t.Fatalf("Acquire(2) = (%d, %v), want (2, nil)", got, err)
	}
	if w.Available() != 1 {
		t.Fatalf("available = %d, want 1", w.Available())
	}
}

func TestCreditAcquireBlocksUntilGrant(t *testing.T) {
	w := NewCreditWindow(0)
	acquired := make(chan int, 1)
	go func() {
		n, _ := w.Acquire(context.Background(), 1)
		acquired <- n
	}()
	// Must not proceed without credit.
	select {
	case <-acquired:
		t.Fatal("Acquire returned before any credit was granted")
	case <-time.After(50 * time.Millisecond):
	}
	w.Grant(1)
	select {
	case n := <-acquired:
		if n != 1 {
			t.Fatalf("acquired %d, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not wake after Grant")
	}
}

func TestCreditAcquireContextCancel(t *testing.T) {
	w := NewCreditWindow(0)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := w.Acquire(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after cancel = %v, want context.Canceled", err)
	}
}

func TestCreditCloseUnblocks(t *testing.T) {
	w := NewCreditWindow(0)
	go func() { time.Sleep(20 * time.Millisecond); w.Close() }()
	if _, err := w.Acquire(context.Background(), 1); !errors.Is(err, ErrCreditClosed) {
		t.Fatalf("Acquire after Close = %v, want ErrCreditClosed", err)
	}
}
