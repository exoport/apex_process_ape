// Package views holds the per-connection rolling state and stage view
// helpers used by the SSE renderer. PLAN-5 / C8.
package views

import "sync"

// State is the broker-side view of pipeline progress. The broker
// updates it as ape publishes events; the SSE handler reads it on
// every new client connection to emit a fresh pipeline-init.
type State struct {
	mu     sync.Mutex
	stages map[string]*Stage
	order  []string // ordered slugs as they appeared

	awaitPending bool
}

func NewState() *State {
	return &State{stages: make(map[string]*Stage)}
}

// UpsertStage creates or updates a stage by slug.
func (s *State) UpsertStage(slug string, fn func(*Stage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stages[slug]
	if !ok {
		st = &Stage{Slug: slug, Name: slug}
		s.stages[slug] = st
		s.order = append(s.order, slug)
	}
	fn(st)
}

// Stages returns a snapshot in original order.
func (s *State) Stages() []*Stage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Stage, 0, len(s.order))
	for _, slug := range s.order {
		out = append(out, s.stages[slug])
	}
	return out
}

// SetAwaitPending records whether a decision-gate is currently open
// so a reconnect can render the correct decision-gate state.
func (s *State) SetAwaitPending(b bool) {
	s.mu.Lock()
	s.awaitPending = b
	s.mu.Unlock()
}

func (s *State) AwaitPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.awaitPending
}

// Reset clears all stages and resets await state. Used on a fresh
// pipeline-init.
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stages = make(map[string]*Stage)
	s.order = nil
	s.awaitPending = false
}
