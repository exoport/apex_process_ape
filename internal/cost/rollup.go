package cost

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Rollup is the on-disk shape of <project>/_output/ape/cost-rollup.json.
// Aggregates pipeline runs + chat sessions per name / per date bucket.
// PLAN-5 / C7.
type Rollup struct {
	UpdatedAt time.Time         `json:"updated_at"`
	Pipelines map[string]Bucket `json:"pipelines,omitempty"`
	// Tasks aggregates `ape task` runs, keyed by skill name
	// (manifests under _output/tasks/<skill>/<run-id>/). PLAN-11.
	Tasks map[string]Bucket `json:"tasks,omitempty"`
	Chats Bucket            `json:"chats"`
	ByDay map[string]Totals `json:"by_day,omitempty"` // YYYY-MM-DD → totals
	// PerModel is the project-wide per-model breakdown, keyed by
	// normalized model id (see NormalizeModel). PLAN-10 D5.
	PerModel map[string]Totals `json:"per_model,omitempty"`
}

// Bucket totals one pipeline name (or all chats) over the lifetime
// of the project. Runs is the per-run-id breakdown; PerModel is the
// per-model breakdown summed across the bucket's runs (PLAN-10 D5).
type Bucket struct {
	Totals   Totals            `json:"totals"`
	Runs     map[string]Totals `json:"runs,omitempty"`
	PerModel map[string]Totals `json:"per_model,omitempty"`
}

// RollupPath returns <project>/_output/ape/cost-rollup.json.
func RollupPath(projectRoot string) string {
	return filepath.Join(projectRoot, "_output", "ape", "cost-rollup.json")
}

// LoadRollup reads RollupPath(projectRoot). Returns an empty rollup
// if the file doesn't exist yet.
func LoadRollup(projectRoot string) (*Rollup, error) {
	path := RollupPath(projectRoot)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Rollup{Pipelines: map[string]Bucket{}, Tasks: map[string]Bucket{}, ByDay: map[string]Totals{}}, nil
		}
		return nil, err
	}
	defer f.Close()
	var r Rollup
	bs, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(bs, &r); err != nil {
		// Corrupt file: best-effort restart. The durable record is
		// per-run manifest.yaml / session.yaml — rollup is a cache.
		return &Rollup{Pipelines: map[string]Bucket{}, Tasks: map[string]Bucket{}, ByDay: map[string]Totals{}}, nil
	}
	if r.Pipelines == nil {
		r.Pipelines = map[string]Bucket{}
	}
	if r.Tasks == nil {
		r.Tasks = map[string]Bucket{}
	}
	if r.ByDay == nil {
		r.ByDay = map[string]Totals{}
	}
	if r.Chats.Runs == nil {
		r.Chats.Runs = map[string]Totals{}
	}
	return &r, nil
}

// rollupMu serialises writes to the same project's rollup file. The
// scope is process-local — multi-process write contention is rare
// because writes only fire on exit; we accept the residual race.
var rollupMu sync.Mutex

// SaveRollup atomically writes r to RollupPath(projectRoot).
func SaveRollup(projectRoot string, r *Rollup) error {
	rollupMu.Lock()
	defer rollupMu.Unlock()
	r.UpdatedAt = time.Now().UTC()
	path := RollupPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bs, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o644); err != nil { //nolint:gosec // shared rollup file; world-readable is intentional
		return err
	}
	return os.Rename(tmp, path)
}

// FoldPipelineRun mutates r to include one pipeline run's totals plus
// its per-model breakdown (PLAN-10 D5; perModel may be nil).
func (r *Rollup) FoldPipelineRun(pipelineName, runID string, day time.Time, totals Totals, perModel map[string]Totals) {
	if r.Pipelines == nil {
		r.Pipelines = map[string]Bucket{}
	}
	b := r.Pipelines[pipelineName]
	if b.Runs == nil {
		b.Runs = map[string]Totals{}
	}
	b.Runs[runID] = totals
	b.Totals = sumTotals(b.Totals, totals)
	b.PerModel = sumPerModel(b.PerModel, perModel)
	r.Pipelines[pipelineName] = b
	r.foldDay(day, totals)
	r.PerModel = sumPerModel(r.PerModel, perModel)
}

// FoldTaskRun mutates r to include one `ape task` run's totals,
// keyed by skill name. PLAN-11 / PLAN-10 D5.
func (r *Rollup) FoldTaskRun(skill, runID string, day time.Time, totals Totals, perModel map[string]Totals) {
	if r.Tasks == nil {
		r.Tasks = map[string]Bucket{}
	}
	b := r.Tasks[skill]
	if b.Runs == nil {
		b.Runs = map[string]Totals{}
	}
	b.Runs[runID] = totals
	b.Totals = sumTotals(b.Totals, totals)
	b.PerModel = sumPerModel(b.PerModel, perModel)
	r.Tasks[skill] = b
	r.foldDay(day, totals)
	r.PerModel = sumPerModel(r.PerModel, perModel)
}

// FoldChat mutates r to include one chat session's totals.
func (r *Rollup) FoldChat(chatID string, day time.Time, totals Totals, perModel map[string]Totals) {
	if r.Chats.Runs == nil {
		r.Chats.Runs = map[string]Totals{}
	}
	r.Chats.Runs[chatID] = totals
	r.Chats.Totals = sumTotals(r.Chats.Totals, totals)
	r.Chats.PerModel = sumPerModel(r.Chats.PerModel, perModel)
	r.foldDay(day, totals)
	r.PerModel = sumPerModel(r.PerModel, perModel)
}

func (r *Rollup) foldDay(day time.Time, t Totals) {
	if r.ByDay == nil {
		r.ByDay = map[string]Totals{}
	}
	key := day.UTC().Format("2006-01-02")
	r.ByDay[key] = sumTotals(r.ByDay[key], t)
}

func sumTotals(a, b Totals) Totals {
	return Totals{
		CostUSD:             a.CostUSD + b.CostUSD,
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
		NumTurns:            a.NumTurns + b.NumTurns,
	}
}

// sumPerModel folds src into dst (keyed by normalized model id),
// allocating dst on first use. Returns dst (possibly newly allocated).
func sumPerModel(dst, src map[string]Totals) map[string]Totals {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]Totals, len(src))
	}
	for model, t := range src {
		dst[model] = sumTotals(dst[model], t)
	}
	return dst
}

// SortedDays returns the ByDay keys in ascending order. Useful for
// the `ape costs` human renderer.
func (r *Rollup) SortedDays() []string {
	out := make([]string, 0, len(r.ByDay))
	for k := range r.ByDay {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
