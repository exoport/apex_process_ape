package apecmd

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"gopkg.in/yaml.v3"
)

// stopGateRecorder is the `ape task` side of the APEX v1 stop gate (trial
// decision D1).
//
// ape's own Stop hook tells the driver a turn ended. In a v1 project the
// project's `ape hook stop` may have blocked that same Stop — Claude Code
// then continues the turn with the hook's reason — and a driver that
// trusted the first Stop would tear the session down mid-work. So in a
// project whose settings register the hook, the driver waits for the
// hook's verdict on every Stop and only a non-block completes the step.
// The hook blocks only while exactly one change is in progress and red; on
// every other Stop it allows at once. Every Stop is matched to its own
// verdict, in order.
type stopGateRecorder struct {
	root string
	// appear is how long a Stop waits for its hook run to show up.
	appear time.Duration
	// inline is set when ape task passed the project's hooks in its
	// --settings: the stop hook runs on every Stop whatever settings.json
	// says.
	inline bool

	// gateMu serialises Stops: each is decided in arrival order, so the
	// k-th Stop takes the k-th verdict in the hook's log.
	gateMu sync.Mutex

	mu sync.Mutex
	// consumed holds the StartedAt of every verdict already used.
	consumed map[string]bool
	events   []pipeline.StopGateEvent
}

// stopGateAppear is how long a Stop waits for the hook's record to
// appear before concluding no gate ran for it.
const stopGateAppear = 10 * time.Second

// stopGateWait bounds one Stop's wait on the hook: the hook's own
// timeout (apexv1.StopHookTimeout, 1800 s) plus margin.
const stopGateWait = 35 * time.Minute

func newStopGateRecorder(root string) *stopGateRecorder {
	return &stopGateRecorder{root: root, appear: stopGateAppear, consumed: map[string]bool{}}
}

// bounded adapts gate to sessiondriver.StopGate: each wait ends with the
// run, or after stopGateWait.
func (r *stopGateRecorder) bounded(runCtx context.Context) sessiondriver.StopGate {
	return func(sessionID string, stopAt time.Time) (blocked, ok bool) {
		ctx, cancel := context.WithTimeout(runCtx, stopGateWait)
		defer cancel()
		return r.gate(ctx, sessionID, stopAt)
	}
}

// gate decides one Stop.
func (r *stopGateRecorder) gate(ctx context.Context, sessionID string, stopAt time.Time) (blocked, ok bool) {
	r.gateMu.Lock()
	defer r.gateMu.Unlock()
	if !r.inline && !apexv1.StopGateConfigured(r.root) {
		return false, false
	}
	r.mu.Lock()
	consumed := maps.Clone(r.consumed)
	r.mu.Unlock()
	rec, err := apexv1.WaitStopVerdict(ctx, r.root, sessionID, stopAt, consumed, r.appear)
	if err != nil {
		return false, false
	}
	r.mu.Lock()
	r.consumed[rec.StartedAt] = true
	r.events = append(r.events, pipeline.StopGateEvent{
		At: time.Now().UTC(), Verdict: rec.Verdict, Change: rec.Change, Reason: rec.Reason,
	})
	r.mu.Unlock()
	return rec.Verdict == apexv1.StopBlock, true
}

func (r *stopGateRecorder) record() *pipeline.StopGateRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	out := &pipeline.StopGateRecord{Events: append([]pipeline.StopGateEvent(nil), r.events...)}
	for _, e := range r.events {
		if e.Verdict == apexv1.StopBlock {
			out.Deferrals++
		} else {
			out.Allowed++
		}
	}
	return out
}

// recordStopGate writes the run's stop-gate verdicts into its manifest.
// Best-effort, like recordCommitContract: the run is over and reported.
func recordStopGate(projectRoot, skill, manifestDir string, r *stopGateRecorder) {
	if r == nil {
		return
	}
	rec := r.record()
	if rec == nil {
		return
	}
	patchLatestManifest(projectRoot, skill, manifestDir, func(m *pipeline.Manifest) { m.StopGate = rec })
}

// patchLatestManifest applies change to the latest run manifest of skill
// and writes it back. Best-effort: the run is over and reported.
func patchLatestManifest(projectRoot, skill, manifestDir string, change func(*pipeline.Manifest)) {
	runDir := pipeline.ResolveLatestRunDir(projectRoot, skill, manifestDir)
	if runDir == "" {
		return
	}
	manifestPath := filepath.Join(runDir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var m pipeline.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return
	}
	change(&m)
	out, err := yaml.Marshal(&m)
	if err != nil {
		return
	}
	_ = atomicfile.Write(manifestPath, out)
}
