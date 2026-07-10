package service

import (
	"errors"
	"sync"
	"testing"
)

// TestAdmitMatrix asserts all six cells of the D3 admission table. Each
// case sets up a key's state by admitting a prior job, then asserts the
// next request's outcome.
func TestAdmitMatrix(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(a *Admissions) // brings the key to the state under test
		reqExcl     bool
		wantErr     error // nil = accept
		wantHeldGot KeyStatus
	}{
		{
			name:        "exclusive into free → accept, held-exclusive",
			setup:       func(*Admissions) {},
			reqExcl:     true,
			wantErr:     nil,
			wantHeldGot: KeyStatus{Exclusive: true, Count: 1},
		},
		{
			name:        "nonexclusive into free → accept, held-shared",
			setup:       func(*Admissions) {},
			reqExcl:     false,
			wantErr:     nil,
			wantHeldGot: KeyStatus{Exclusive: false, Count: 1},
		},
		{
			name:    "exclusive into held-exclusive → BUSY_EXCLUSIVE",
			setup:   func(a *Admissions) { mustAdmit(t, a, true) },
			reqExcl: true,
			wantErr: ErrBusyExclusive,
		},
		{
			name:    "nonexclusive into held-exclusive → BUSY_EXCLUSIVE",
			setup:   func(a *Admissions) { mustAdmit(t, a, true) },
			reqExcl: false,
			wantErr: ErrBusyExclusive,
		},
		{
			name:    "exclusive into held-shared → BUSY_KEY",
			setup:   func(a *Admissions) { mustAdmit(t, a, false) },
			reqExcl: true,
			wantErr: ErrBusyKey,
		},
		{
			name:        "nonexclusive into held-shared → accept, count++",
			setup:       func(a *Admissions) { mustAdmit(t, a, false) },
			reqExcl:     false,
			wantErr:     nil,
			wantHeldGot: KeyStatus{Exclusive: false, Count: 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAdmissions()
			tc.setup(a)
			rel, err := a.Admit("k", tc.reqExcl)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Admit err = %v, want %v", err, tc.wantErr)
				}
				if rel != nil {
					t.Fatal("rejected Admit must return a nil release")
				}
				return
			}
			if err != nil {
				t.Fatalf("Admit err = %v, want accept", err)
			}
			if rel == nil {
				t.Fatal("accepted Admit must return a non-nil release")
			}
			if got := a.Snapshot()["k"]; got != tc.wantHeldGot {
				t.Fatalf("held state = %+v, want %+v", got, tc.wantHeldGot)
			}
		})
	}
}

// TestReleaseFreesKey verifies that releasing the last job on a key frees
// it (so a later exclusive request succeeds) and that release is
// idempotent (a double call cannot underflow the count).
func TestReleaseFreesKey(t *testing.T) {
	a := NewAdmissions()

	rel, err := a.Admit("k", true)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if _, err := a.Admit("k", true); !errors.Is(err, ErrBusyExclusive) {
		t.Fatalf("second exclusive Admit err = %v, want ErrBusyExclusive", err)
	}

	rel()
	rel() // idempotent: must not underflow or panic
	if held := a.HeldKeys(); len(held) != 0 {
		t.Fatalf("key should be free after release, held = %v", held)
	}
	if _, err := a.Admit("k", true); err != nil {
		t.Fatalf("Admit after release err = %v, want accept", err)
	}
}

// TestSharedRefcount verifies that a held-shared key releases only when the
// last nonexclusive job leaves, and that an exclusive request is rejected
// until then.
func TestSharedRefcount(t *testing.T) {
	a := NewAdmissions()
	r1, _ := a.Admit("k", false)
	r2, _ := a.Admit("k", false)
	if got := a.Snapshot()["k"]; got.Count != 2 || got.Exclusive {
		t.Fatalf("held = %+v, want shared count 2", got)
	}

	r1()
	if _, err := a.Admit("k", true); !errors.Is(err, ErrBusyKey) {
		t.Fatalf("exclusive Admit with one shared job left err = %v, want ErrBusyKey", err)
	}
	r2()
	if _, err := a.Admit("k", true); err != nil {
		t.Fatalf("exclusive Admit after all shared jobs left err = %v, want accept", err)
	}
}

// TestKeysIndependent verifies keys don't interfere: an exclusive job on
// "chore" leaves "" free for anything.
func TestKeysIndependent(t *testing.T) {
	a := NewAdmissions()
	if _, err := a.Admit("chore", true); err != nil {
		t.Fatalf("Admit chore: %v", err)
	}
	if _, err := a.Admit("", true); err != nil {
		t.Fatalf("exclusive Admit on the default key should be unaffected by chore, got %v", err)
	}
	if _, err := a.Admit("", false); !errors.Is(err, ErrBusyExclusive) {
		t.Fatalf("nonexclusive into held-exclusive default key err = %v, want ErrBusyExclusive", err)
	}
}

// TestAdmitConcurrent stresses the controller under the race detector:
// many goroutines contend for one key; exactly one exclusive winner may
// hold it at a time, and after all releases the key is free.
func TestAdmitConcurrent(t *testing.T) {
	a := NewAdmissions()
	const workers = 64

	var wg sync.WaitGroup
	var accepted int64
	var mu sync.Mutex
	for range workers {
		wg.Go(func() {
			rel, err := a.Admit("k", true)
			if err != nil {
				return // rejected: someone else holds it
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			rel()
		})
	}
	wg.Wait()

	// At least one worker won; the exact count is timing-dependent (a
	// winner may release before the next contends), but the invariant is
	// that the key is free once everyone has released.
	if accepted == 0 {
		t.Fatal("no worker was admitted")
	}
	if held := a.HeldKeys(); len(held) != 0 {
		t.Fatalf("key not free after all releases: %v", held)
	}
}

// mustAdmit brings key "k" into the state under test for the matrix setup.
func mustAdmit(t *testing.T, a *Admissions, exclusive bool) {
	t.Helper()
	if _, err := a.Admit("k", exclusive); err != nil {
		t.Fatalf("setup Admit(excl=%v): %v", exclusive, err)
	}
}
