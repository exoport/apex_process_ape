package apecmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/reporting"
	"github.com/spf13/cobra"
)

func newMetricsCmd() *cobra.Command {
	var (
		f     reportFlags
		runID string
	)
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Scan and publish this session's usage metrics over NATS",
		Long: `Scan the resolved Claude session set (main + sub-agents) and publish a
usage snapshot on ape.metrics.<user>.<project>.<session-id>.

The payload carries per-model token counts (with the ephemeral 5m/1h cache
split), turn count, first/last turn timestamps, and the Claude Code version
— everything needed to reprice against Claude Code API rates at any later
moment (per_model tokens × the price table = cost_usd).

--run-id <id> instead publishes a completed run's manifest totals (a reader
over the run's manifest.yaml), with run_id populated. Republishing is
idempotent; consumers key on (session_id, ts).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or the session/run was unresolvable.`,
		Example: `  ape metrics
  ape metrics --output-format json
  ape metrics --run-id 20260709-abc123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReport(func() error {
				return runMetrics(cmd.Context(), cmd.OutOrStdout(), &f, runID)
			})
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "Publish a completed run's manifest totals instead of a live session scan.")
	addReportFlags(cmd, &f, false)
	return cmd
}

func runMetrics(ctx context.Context, out io.Writer, f *reportFlags, runID string) error {
	r, ref, err := setupReporter(ctx, f)
	if err != nil {
		return err
	}
	defer r.Close()

	payload := reporting.MetricsPayload{}
	sessionID := ref.SessionID
	if runID != "" {
		project, _ := f.projectRoot()
		p, sid, err := metricsFromRun(project, runID)
		if err != nil {
			return usageErr(err)
		}
		payload = p
		if sid != "" {
			sessionID = sid
		}
	} else {
		if ref.Transcript == "" {
			return usageErr(fmt.Errorf("session %s has no transcript on disk; pass --transcript or --run-id", ref.SessionID))
		}
		files := cost.SessionFiles(ref.Transcript, time.Time{})
		paths := make([]string, 0, len(files))
		for _, sf := range files {
			paths = append(paths, sf.Path)
		}
		payload = reporting.BuildMetrics(cost.ScanPaths(paths), "")
	}

	if err := r.Metrics(sessionID, payload); err != nil {
		return failErr(err)
	}
	if f.jsonMode() {
		return emitJSON(out, map[string]any{
			"ok":               true,
			"session_id":       sessionID,
			"num_turns":        payload.NumTurns,
			"duration_seconds": payload.DurationSeconds,
			"per_model":        payload.PerModel,
			"run_id":           payload.RunID,
		})
	}
	if !f.quiet {
		fmt.Fprintf(out, "✅ metrics published for session %s — %d turn(s), %d model(s)\n",
			sessionID, payload.NumTurns, len(payload.PerModel))
	}
	return nil
}

// metricsFromRun builds a metrics payload from a completed run's manifest
// (--run-id mode). The run dir is located under <project>/_output/*/*/<id>.
func metricsFromRun(projectRoot, runID string) (reporting.MetricsPayload, string, error) {
	matches, _ := filepath.Glob(filepath.Join(projectRoot, "_output", "*", "*", runID, "manifest.yaml"))
	if len(matches) == 0 {
		return reporting.MetricsPayload{}, "", fmt.Errorf("no manifest found for run %q under %s/_output", runID, projectRoot)
	}
	m, err := pipeline.LoadManifest(filepath.Dir(matches[0]))
	if err != nil {
		return reporting.MetricsPayload{}, "", fmt.Errorf("load manifest for run %q: %w", runID, err)
	}
	perModel := make(map[string]eventing.ModelMetrics, len(m.Totals.ModelUsage))
	for model, u := range m.Totals.ModelUsage {
		perModel[model] = eventing.ModelMetrics{
			CostUSD:              u.CostUSD,
			InputTokens:          u.TokensInput,
			OutputTokens:         u.TokensOutput,
			CacheReadInputTokens: u.TokensCacheRead,
			CacheCreation5m:      u.TokensCacheCreation5m,
			CacheCreation1h:      u.TokensCacheCreation1h,
			Turns:                u.NumTurns,
		}
	}
	return reporting.MetricsPayload{
		NumTurns: m.Totals.NumTurns,
		PerModel: perModel,
		RunID:    runID,
	}, firstSessionID(m), nil
}

// firstSessionID returns the manifest's first recorded per-session id, so
// the --run-id metrics land on a meaningful session subject. Empty when the
// manifest records none (the caller keeps the resolved session id).
func firstSessionID(m pipeline.Manifest) string {
	for i := range m.Stages {
		for j := range m.Stages[i].Steps {
			for _, s := range m.Stages[i].Steps[j].Sessions {
				if s.SessionID != "" {
					return s.SessionID
				}
			}
		}
	}
	return ""
}
