package aped

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
)

// The idle reaper (PLAN-24 D7).
//
// PLAN-22 deliberately did NOT ship one, and the reason was not effort: the only
// signal available was `last_used_at`, which records exec/attach/start — someone
// reaching IN — so a workspace running a three-hour build with nobody attached
// looks idle. Shipping a reaper on that signal would have stopped working
// machines. Reporting the number and letting a human decide was the honest
// alternative, and the note in PLAN-22 says so.
//
// What changed is the signal, not the appetite. D6's in-guest agent reports what
// the guest is actually doing, so "idle" can mean idle.
//
// # The rule that decides the shape
//
// The agent is best-effort. A workspace whose agent never started emits nothing,
// and "emits nothing" is indistinguishable from "idle" unless this code is
// written to distinguish it.
//
//	NEVER SEEN A HEARTBEAT  →  UNKNOWN  →  DO NOT REAP
//
// Not "idle". Reading silence as idleness would stop every workspace with a
// broken or absent agent at the threshold — which is a worse failure than the
// one the reaper exists to fix, and a silent one. D6's supervised launch makes
// that rare; it does not make it impossible, and this must not depend on
// impossibility.
//
// # Stop, never destroy
//
// A wrong stop costs ~30 seconds and a `start`. A wrong destroy costs work.
// Disk is reclaimed by a human.

// Reaper timing defaults.
const (
	// DefaultReaperInterval is how often the reaper evaluates workspaces. Coarse
	// relative to the threshold it enforces: an idle workspace being stopped five
	// minutes late costs nothing, and polling harder would only add containerd
	// round-trips.
	DefaultReaperInterval = 5 * time.Minute
	// reaperGrace is how long after a heartbeat's own interval a workspace is still
	// considered to be reporting. Without it, an agent that missed one publish
	// would look like an agent that stopped.
	reaperGrace = 3 * sandbox.DefaultHeartbeatInterval
)

// ReaperConfig configures the idle reaper.
type ReaperConfig struct {
	// Backend lists and stops workspaces. Required.
	Backend workspace.Backend
	// Conn is a TELEMETRY-account connection subscribed to the per-VM heartbeat
	// subjects. Required — with no heartbeats nothing is ever known, so a reaper
	// without one would be a reaper that can only be wrong.
	Conn *nats.Conn
	// IdleAfter is the node-wide threshold. Zero or negative disables the reaper
	// entirely, which is the default posture for a node that has not opted in.
	IdleAfter time.Duration
	// Interval is the evaluation period; zero → DefaultReaperInterval.
	Interval time.Duration
	// Stderr receives the reaper's decisions; nil → os.Stderr. Every stop is
	// logged with the evidence that triggered it — a stop with no explanation is
	// indistinguishable from a crash to whoever finds the workspace down.
	Stderr io.Writer
	// now is an injectable clock for tests.
	now func() time.Time
}

// Reaper stops workspaces that the in-guest agent reports as idle.
type Reaper struct {
	cfg ReaperConfig

	mu    sync.Mutex
	seen  map[string]heartbeatState // vm token → what we know
	subCh *nats.Subscription
}

// heartbeatState is what the reaper remembers about one workspace.
//
// LastBusy is separate from LastSeen on purpose: a workspace reporting steadily
// at 0% is KNOWN idle (reapable), while one that has stopped reporting is
// UNKNOWN (not reapable). Collapsing them into a single "last heartbeat" would
// make a dead agent look like a busy one — or, worse, the other way round.
type heartbeatState struct {
	LastSeen time.Time
	LastBusy time.Time
	Last     sandbox.Heartbeat
}

// NewReaper builds a reaper. It subscribes nothing until Run.
func NewReaper(cfg ReaperConfig) *Reaper {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultReaperInterval
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Reaper{cfg: cfg, seen: map[string]heartbeatState{}}
}

// Run subscribes to the heartbeat subjects and evaluates workspaces on the
// configured interval until ctx is cancelled.
//
// It returns immediately (having done nothing) when the reaper is disabled or
// unconfigured. That is not a silent failure: RunFront prints which of the two
// it is at startup, so a node that is not auto-stopping anything says so.
func (r *Reaper) Run(ctx context.Context) error {
	if r.cfg.IdleAfter <= 0 || r.cfg.Backend == nil || r.cfg.Conn == nil {
		return nil
	}
	// Every VM's heartbeat. The token position is a single-token wildcard because
	// NATS wildcards are whole-token — a literal "vm-*" segment would match
	// nothing. Within the isolated TELEMETRY account the only publishers are per-VM
	// credentials, so this carries VM telemetry and nothing else.
	sub, err := r.cfg.Conn.Subscribe(sandbox.HeartbeatSubjectRoot+".*."+sandbox.HeartbeatSubjectSuffix, r.onHeartbeat)
	if err != nil {
		return fmt.Errorf("aped reaper: subscribe heartbeats: %w", err)
	}
	r.subCh = sub
	defer func() { _ = sub.Unsubscribe() }()

	tick := time.NewTicker(r.cfg.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.evaluate(ctx)
		}
	}
}

// onHeartbeat records one heartbeat.
//
// The workspace token comes from the SUBJECT, not from the payload: the NATS
// server authorized the subject against the publisher's per-VM credential, so it
// is attested. The payload's own Workspace field is a claim by the guest, and a
// compromised guest could set it to anything — including another workspace's
// token, which would let it get that workspace stopped.
func (r *Reaper) onHeartbeat(m *nats.Msg) {
	token, ok := sandbox.WorkspaceFromHeartbeatSubject(m.Subject)
	if !ok {
		return
	}
	var h sandbox.Heartbeat
	if err := json.Unmarshal(m.Data, &h); err != nil {
		return
	}
	now := r.cfg.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.seen[token]
	st.LastSeen = now
	st.Last = h
	if h.Busy {
		st.LastBusy = now
	}
	r.seen[token] = st
}

// evaluate walks the node's workspaces and stops the ones known to be idle.
func (r *Reaper) evaluate(ctx context.Context) {
	list, err := r.cfg.Backend.List(ctx)
	if err != nil {
		fmt.Fprintf(r.cfg.Stderr, "! aped reaper: could not list workspaces: %v\n", err)
		return
	}
	for i := range list {
		ws := &list[i]
		decision := r.decide(ws)
		if !decision.reap {
			continue
		}
		// Confirm it is actually RUNNING before stopping it. The registry says a
		// workspace exists, not that it holds RAM, and stopping an already-stopped
		// workspace would log a reap that reclaimed nothing.
		st, ierr := r.cfg.Backend.Inspect(ctx, ws.Name)
		if ierr != nil || st.State != workspace.StateRunning {
			continue
		}
		if serr := r.cfg.Backend.Stop(ctx, ws.Name); serr != nil {
			fmt.Fprintf(r.cfg.Stderr, "! aped reaper: could not stop %s: %v\n", ws.Name, serr)
			continue
		}
		fmt.Fprintf(r.cfg.Stderr, "⇣ aped reaper: stopped %s — %s (start it again with `ape sandbox start %s`)\n",
			ws.Name, decision.why, ws.Name)
		r.forget(VMToken(ws.Name))
	}
}

// reapDecision is one workspace's verdict plus the evidence for it.
type reapDecision struct {
	reap bool
	why  string
}

// decide reports whether a workspace should be stopped, and why.
//
// The three outcomes that are NOT a reap matter as much as the one that is:
//
//   - exempt: the workspace (or the node) turned idle-stop off.
//   - unknown: no heartbeat has EVER been seen, or the agent has gone quiet.
//     Silence is not idleness.
//   - busy: the guest reported work inside the threshold.
func (r *Reaper) decide(ws *workspace.Workspace) reapDecision {
	after, enabled := sandbox.ResolveIdleStop(ws.IdleStop, r.cfg.IdleAfter)
	if !enabled {
		return reapDecision{}
	}
	now := r.cfg.now()

	r.mu.Lock()
	st, known := r.seen[VMToken(ws.Name)]
	r.mu.Unlock()

	// THE RULE. A workspace we have never heard from is unknown, not idle.
	if !known || st.LastSeen.IsZero() {
		return reapDecision{}
	}
	// An agent that has stopped reporting is also unknown: it may have crashed
	// while its workspace kept working. Treating a gap as idleness would stop a
	// busy workspace whose agent died — the same failure as never having seen one,
	// arrived at differently.
	if now.Sub(st.LastSeen) > reaperGrace {
		return reapDecision{}
	}
	// Known to be reporting. If it has never reported BUSY, idleness is measured
	// from the first heartbeat: a workspace that has been up and quiet for four
	// hours is idle, and requiring a busy sample first would exempt it forever.
	idleSince := st.LastBusy
	if idleSince.IsZero() {
		idleSince = st.LastSeen.Add(-r.observedFor(ws))
	}
	idleFor := now.Sub(idleSince)
	if idleFor < after {
		return reapDecision{}
	}
	return reapDecision{
		reap: true,
		why: fmt.Sprintf("idle for %s (threshold %s); last heartbeat %s ago: %s",
			idleFor.Round(time.Minute), after,
			now.Sub(st.LastSeen).Round(time.Second), st.Last.Summary()),
	}
}

// observedFor estimates how long a workspace has been observable, so a
// never-busy workspace's idle clock starts when it started rather than at the
// last heartbeat.
//
// The agent's own uptime is the right measure and it carries it: it resets on
// every stop/start, which is exactly the boundary that should reset the clock. A
// heartbeat without one (an older agent) falls back to zero, which makes the
// workspace look freshly idle and delays its reap by one threshold — the safe
// direction.
func (r *Reaper) observedFor(ws *workspace.Workspace) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.seen[VMToken(ws.Name)]
	if st.Last.UptimeSeconds <= 0 {
		return 0
	}
	return time.Duration(st.Last.UptimeSeconds) * time.Second
}

// forget drops a workspace's heartbeat state after it is stopped, so a `start`
// begins from unknown rather than from the idleness that got it stopped — which
// would otherwise stop it again at the next tick, before its agent had said a
// word.
func (r *Reaper) forget(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.seen, token)
}

// Observed reports what the reaper currently knows about a workspace
// (diagnostics + tests).
func (r *Reaper) Observed(name string) (lastSeen, lastBusy time.Time, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.seen[VMToken(name)]
	return st.LastSeen, st.LastBusy, ok
}
