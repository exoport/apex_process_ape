package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"gopkg.in/yaml.v3"
)

// stopGateRecorder is the `ape task` side of the APEX v1 stop gate (trial
// decision D1).
//
// ape's own Stop hook tells the driver a turn ended. In a v1 project the
// project's `ape hook stop` may have blocked that same Stop — Claude Code
// then continues the turn with the hook's reason — and a driver that
// trusted the first Stop would tear the session down mid-work. So, when
// exactly one change is in progress, the driver waits for the hook's
// verdict and only a non-block completes the step.
type stopGateRecorder struct {
	root string

	mu sync.Mutex
	// consumed is the StartedAt of the last verdict used, so a record
	// left by an earlier Stop is never read as this one's.
	consumed string
	events   []pipeline.StopGateEvent
}

// stopGateAppear is how long a Stop waits for the hook's record to
// appear before concluding no gate ran for it.
const stopGateAppear = 10 * time.Second

func newStopGateRecorder(root string) *stopGateRecorder {
	return &stopGateRecorder{root: root}
}

// gate is a sessiondriver.StopGate.
func (r *stopGateRecorder) gate(ctx context.Context, sessionID string, stopAt time.Time) (blocked, ok bool) {
	if !apexv1.StopGateApplies(r.root) {
		return false, false
	}
	r.mu.Lock()
	consumed := r.consumed
	r.mu.Unlock()
	rec, err := apexv1.WaitStopVerdict(ctx, r.root, sessionID, stopAt, consumed, stopGateAppear)
	if err != nil {
		return false, false
	}
	r.mu.Lock()
	r.consumed = rec.StartedAt
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
	m.StopGate = rec
	out, err := yaml.Marshal(&m)
	if err != nil {
		return
	}
	_ = atomicfile.Write(manifestPath, out)
}
