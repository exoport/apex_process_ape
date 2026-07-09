package apecmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/reporting"
	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	var (
		f      reportFlags
		fields []string
	)
	cmd := &cobra.Command{
		Use:   "log <level> <message>",
		Short: "Publish a structured log record over NATS",
		Long: `Publish one structured log record for the current Claude session on
ape.log.<user>.<project>.<session-id>.<level>.

<level> is one of debug|info|warn|error. Extra structured context is passed
as repeated --field key=value pairs. Centralized-logging consumers subscribe
ape.log.> (or per-user/project subtrees — the subject is the routing key).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.`,
		Example: `  ape log info "migration step 3 complete"
  ape log warn "retrying" --field attempt=2 --field endpoint=api`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(func() error {
				return runLog(cmd.Context(), cmd.OutOrStdout(), &f, args[0], args[1], fields)
			})
		},
	}
	cmd.Flags().StringArrayVar(&fields, "field", nil, "Structured field as key=value (repeatable).")
	addReportFlags(cmd, &f, false)
	return cmd
}

func runLog(ctx context.Context, out io.Writer, f *reportFlags, level, msg string, fields []string) error {
	if !reporting.LevelValid(level) {
		return usageErr(fmt.Errorf("level %q must be one of debug|info|warn|error", level))
	}
	kv, err := parseFields(fields)
	if err != nil {
		return usageErr(err)
	}
	r, ref, err := setupReporter(ctx, f)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Log(ref.SessionID, level, msg, kv); err != nil {
		return failErr(err)
	}
	if f.jsonMode() {
		return emitJSON(out, map[string]any{"ok": true, "session_id": ref.SessionID, "level": level})
	}
	if !f.quiet {
		fmt.Fprintf(out, "✅ %s logged for session %s\n", level, ref.SessionID)
	}
	return nil
}

// parseFields parses repeated key=value flags into a map (empty when none).
// A missing '=' or empty key is a usage error.
func parseFields(fields []string) (map[string]string, error) {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--field %q must be key=value", f)
		}
		out[k] = v
	}
	return out, nil
}
