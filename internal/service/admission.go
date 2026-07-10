// Package service implements the `ape service` NATS-micro job daemon
// (PLAN-14): it receives pipeline/task/command/script jobs as JSON
// request/reply over the `ape.svc.<name>.<project-slug>.<endpoint>`
// subject group, admits or rejects them with keyed exclusivity, and
// spawns each accepted job as an `ape` child process whose own PLAN-13
// events carry the daemon-injected job id.
//
// The package is layered so the subtle parts are pure and unit-tested in
// isolation: the admission state machine (this file), the project
// allowlist (allowlist.go), the in-memory job registry (registry.go), and
// the typed argv builder (spawn.go). The micro wiring (daemon.go, run.go)
// composes them against a real *nats.Conn. All NATS diagnostics go to
// stderr; the request/reply payloads are the only thing on the wire.
package service

import (
	"errors"
	"sort"
	"sync"
)

// Admission sentinel errors. The daemon maps each to the stable PLAN-14
// req.Error code of the same shape (docs/reference/events.md):
//
//	ErrBusyExclusive → BUSY_EXCLUSIVE
//	ErrBusyKey       → BUSY_KEY
//
// They are returned by Admit and never wrap anything, so callers match
// with errors.Is.
var (
	// ErrBusyExclusive: the key already holds an exclusive job, so no
	// further job (exclusive or not) may join it.
	ErrBusyExclusive = errors.New("service: exclusivity key held by an exclusive job")
	// ErrBusyKey: the key holds one or more nonexclusive jobs and the
	// caller asked for an exclusive slot, which cannot coexist with them.
	ErrBusyKey = errors.New("service: exclusivity key held by nonexclusive jobs")
)

// keyState is the admission state of a single exclusivity key. A key is
// either absent from the map (free), held-exclusive (exclusive==true,
// count==1), or held-shared (exclusive==false, count>=1). count is
// decremented as jobs release; the entry is deleted when it reaches zero,
// so "free" and "count 0" are the same observable state.
type keyState struct {
	exclusive bool
	count     int
}

// KeyStatus is the exported view of one held key, for the daemon `status`
// endpoint's held_keys map.
type KeyStatus struct {
	Exclusive bool `json:"exclusive"`
	Count     int  `json:"count"`
}

// Admissions is the D3 keyed-exclusivity admission controller. Jobs are
// EXCLUSIVE BY DEFAULT and bound to an optional key (default ""); keys are
// independent (an exclusive "chore" job coexists with anything under "").
// Nonexclusive concurrency within a key is unlimited. Admission is
// accept-or-reject — never queued — so a rejected caller simply retries.
//
// The zero value is not usable; construct with NewAdmissions. All methods
// are safe for concurrent use.
type Admissions struct {
	mu   sync.Mutex
	keys map[string]*keyState
}

// NewAdmissions returns an empty admission controller (every key free).
func NewAdmissions() *Admissions {
	return &Admissions{keys: make(map[string]*keyState)}
}

// Admit applies the D3 admission matrix for a request against key with the
// requested exclusivity. On success it returns a release func the caller
// must invoke exactly once when the job ends (any outcome) to free its
// slot; release is idempotent. On rejection it returns a nil release and
// one of ErrBusyExclusive / ErrBusyKey.
//
//	request ↓ / key state → | free           | held-exclusive   | held-shared
//	exclusive (default)     | accept (→excl) | ErrBusyExclusive | ErrBusyKey
//	nonexclusive: true      | accept (→shrd) | ErrBusyExclusive | accept (count++)
func (a *Admissions) Admit(key string, exclusive bool) (release func(), err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	st, held := a.keys[key]
	if held {
		if st.exclusive {
			return nil, ErrBusyExclusive
		}
		// held-shared: only an exclusive request conflicts.
		if exclusive {
			return nil, ErrBusyKey
		}
		st.count++
		return a.releaseFunc(key), nil
	}

	// free: accept and take the key in the requested mode.
	a.keys[key] = &keyState{exclusive: exclusive, count: 1}
	return a.releaseFunc(key), nil
}

// releaseFunc builds an idempotent release closure bound to key. It
// decrements the key's job count and frees the key when it hits zero. Bound
// to the key (not a job id) because the count uniquely tracks occupancy —
// an over-release (double call) is absorbed by sync.Once, and a release
// after the key was already freed is a no-op.
func (a *Admissions) releaseFunc(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			st, ok := a.keys[key]
			if !ok {
				return
			}
			st.count--
			if st.count <= 0 {
				delete(a.keys, key)
			}
		})
	}
}

// Snapshot returns a copy of the currently held keys for the `status`
// endpoint. Free keys are absent from the result.
func (a *Admissions) Snapshot() map[string]KeyStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]KeyStatus, len(a.keys))
	for k, st := range a.keys {
		out[k] = KeyStatus{Exclusive: st.exclusive, Count: st.count}
	}
	return out
}

// HeldKeys returns the sorted list of currently held keys (test/diagnostic
// helper).
func (a *Admissions) HeldKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]string, 0, len(a.keys))
	for k := range a.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
