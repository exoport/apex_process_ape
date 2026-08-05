package aped

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// Sandbox-aware cost reporting (PLAN-24 D3).
//
// Work done inside a workspace was invisible to `ape`'s reporting, and the
// reason turned out to be narrower than it looked: the transcripts were already
// on the host the whole time. A workspace's guest $HOME (/sandbox/home) is a
// read-write bind from the per-workspace staging dir this daemon composed, so
// every session a Claude inside a workspace writes lands under
// <state-dir>/homes/<name>/.claude/projects/… as it happens. Nothing had to be
// shipped anywhere. Nothing was collecting them.
//
// Why this runs in the DAEMON and not in the operator's `ape`: the staging homes
// are 0700 and owned by the front's user, and the workspace registry is 0600 and
// owned by root. Neither is readable by a member of the `ape` group, which is
// what an operator is. Relaxing those modes to let a reporting command read them
// would widen access to composed homes — which hold each workspace's credential
// copy — to buy a rollup. The daemon already holds both, so it does the scan and
// returns the numbers.
//
// It needs no agent, no telemetry wire, and no network into the guest. It is a
// filesystem read of files the node already owns.

// costScanTimeout bounds a whole report. Scanning is local file I/O over
// transcripts that grow with use, and a node with many long-lived workspaces
// should degrade to a partial answer rather than hold a request open past the
// client's own timeout — which would surface as a NATS timeout with no
// indication of why.
const costScanTimeout = 60 * time.Second

// CostReporter serves the costs verb. It is an interface on the VMM so a node
// with no state dir (or a test) can report UNSUPPORTED rather than scan nothing
// and call it zero.
type CostReporter interface {
	Costs(ctx context.Context, id string) (workspace.CostsReply, error)
}

// WorkspaceCosts scans workspace staging homes and rolls their Claude usage up
// per workspace.
type WorkspaceCosts struct {
	// StateDir is the aped state dir the staging homes live under.
	StateDir string
	// Backend enumerates the workspaces to report on — the same registry-backed
	// List every other verb uses, so the report covers exactly the workspaces the
	// node admits to having.
	Backend workspace.Backend
}

var _ CostReporter = (*WorkspaceCosts)(nil)

// Costs reports usage for one workspace (id set) or for all of them.
//
// A workspace whose home cannot be read yields a row carrying that error rather
// than failing the whole report: one broken staging dir must not hide the cost
// of every other workspace on the node. A workspace with no transcripts yields a
// zero row, which is the honest answer for one nobody has worked in.
func (w *WorkspaceCosts) Costs(ctx context.Context, id string) (workspace.CostsReply, error) {
	if strings.TrimSpace(w.StateDir) == "" {
		return workspace.CostsReply{}, fmt.Errorf("%w: this node records no workspace state dir", workspace.ErrUnsupported)
	}
	if w.Backend == nil {
		return workspace.CostsReply{}, fmt.Errorf("%w: this node has no workspace backend", workspace.ErrUnsupported)
	}

	list, err := w.Backend.List(ctx)
	if err != nil {
		return workspace.CostsReply{}, err
	}
	names := make([]string, 0, len(list))
	for i := range list {
		names = append(names, list[i].Name)
	}
	if want := strings.TrimSpace(id); want != "" {
		if !slices.Contains(names, want) {
			return workspace.CostsReply{}, fmt.Errorf("%w: %s", workspace.ErrNotFound, want)
		}
		names = []string{want}
	}
	sort.Strings(names)

	ctx, cancel := context.WithTimeout(ctx, costScanTimeout)
	defer cancel()

	reply := workspace.CostsReply{V: workspace.WireVersion, Workspaces: make([]workspace.Cost, 0, len(names))}
	for _, name := range names {
		if ctx.Err() != nil {
			// Say so rather than silently truncating: a short report that looks
			// complete is worse than one that names what it dropped.
			reply.Workspaces = append(reply.Workspaces, workspace.Cost{
				Name:  name,
				Error: "not scanned: the cost scan ran out of time",
			})
			continue
		}
		row := w.scanOne(name)
		reply.Workspaces = append(reply.Workspaces, row)
		reply.Totals = addUsage(reply.Totals, row.Totals)
	}
	return reply, nil
}

// scanOne rolls up one workspace's staging home.
//
// The staging path is DERIVED (StagingDirFor) rather than read back off the
// registry record, because that is how the resolver computed it in the first
// place — one function owns the layout, so a report cannot look somewhere a
// create never wrote. The registry's own staging_dir field records the same
// value; deriving it keeps this from depending on a field the executor happens
// to populate.
func (w *WorkspaceCosts) scanOne(name string) workspace.Cost {
	home := sandbox.StagingDirFor(w.StateDir, name)
	if _, err := os.Stat(home); err != nil {
		// A workspace created before composed homes, or one whose home was reaped
		// out of band. Reported, not fatal, and not silently zero.
		return workspace.Cost{Name: name, Error: "no composed home at " + home}
	}
	res := cost.ScanHome(home)
	row := workspace.Cost{
		Name:            name,
		Sessions:        res.Sessions,
		Totals:          usageFromCost(res.Totals),
		UnpricedModels:  sortedKeys(res.UnpricedModels),
		EstimatedModels: sortedKeys(res.EstimatedModels),
	}
	if len(res.ByModel) > 0 {
		row.PerModel = make(map[string]workspace.UsageTotals, len(res.ByModel))
		for model, t := range res.ByModel {
			row.PerModel[model] = usageFromCost(t)
		}
	}
	if !res.FirstTurnAt.IsZero() {
		row.FirstTurnAt = res.FirstTurnAt.UTC().Format(time.RFC3339)
	}
	if !res.LastTurnAt.IsZero() {
		row.LastTurnAt = res.LastTurnAt.UTC().Format(time.RFC3339)
	}
	return row
}

// usageFromCost projects the cost package's totals onto the wire shape.
func usageFromCost(t cost.Totals) workspace.UsageTotals {
	return workspace.UsageTotals{
		CostUSD:             t.CostUSD,
		InputTokens:         t.InputTokens,
		OutputTokens:        t.OutputTokens,
		CacheReadTokens:     t.CacheReadTokens,
		CacheCreationTokens: t.CacheCreationTokens,
		NumTurns:            t.NumTurns,
	}
}

// addUsage sums two usage buckets.
func addUsage(a, b workspace.UsageTotals) workspace.UsageTotals {
	return workspace.UsageTotals{
		CostUSD:             a.CostUSD + b.CostUSD,
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
		NumTurns:            a.NumTurns + b.NumTurns,
	}
}

// sortedKeys returns a count map's keys in ascending order (stable output).
func sortedKeys(m map[string]int) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
