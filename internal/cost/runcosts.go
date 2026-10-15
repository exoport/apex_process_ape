package cost

import (
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
)

// RunCost is one manifest-bearing run's cost: what `ape status` sums as
// spend. Read-only — unlike RebuildRollup it writes no cache.
type RunCost struct {
	Kind      string    `json:"kind"` // runlog.KindPipeline or runlog.KindTask
	Name      string    `json:"name"` // pipeline name or task skill
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	CostUSD   float64   `json:"cost_usd"`
}

// RunCosts lists every pipeline and task run's cost under the project's
// run roots. Unreadable manifests are skipped, as the rollup does.
func RunCosts(projectRoot string) ([]RunCost, error) {
	var out []RunCost
	for _, tree := range runlog.RunRoots(projectRoot) {
		if tree.Kind != runlog.KindPipeline && tree.Kind != runlog.KindTask {
			continue
		}
		kind := tree.Kind
		err := walkManifestTree(tree.Path, func(name, runID string, day time.Time, totals Totals, _ map[string]Totals) {
			out = append(out, RunCost{Kind: kind, Name: name, RunID: runID, StartedAt: day, CostUSD: totals.CostUSD})
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
