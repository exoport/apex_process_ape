package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// eventTokenRe validates the caller-chosen <event> token: it becomes a
// subject segment, so restrict it to a safe slug (PLAN-17 D3).
var eventTokenRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func newEventCmd() *cobra.Command {
	var (
		f       reportFlags
		payload string
	)
	cmd := &cobra.Command{
		Use:   "event <event> [--payload <json>|@file|-]",
		Short: "Publish a session progress event over NATS",
		Long: `Publish a caller-named progress event for the current Claude session on
ape.evt.<user>.<project>.session.<session-id>.<event>.

The <event> token is caller-chosen (validated [a-z0-9-]+). --payload is
arbitrary JSON, given inline, as @file, or "-" for stdin; it rides the
versioned envelope under "payload" alongside the decoded user identity and
the resolved session id.

The session is resolved as: --session-id → --transcript → APE_SESSION_ID →
the newest transcript for the current project.

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.`,
		Example: `  ape event status --payload '{"phase":"implement","pct":60}'
  ape event build-green
  echo '{"pr":42}' | ape event pr-opened --payload -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(func() error {
				return runEvent(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), &f, args[0], payload)
			})
		},
	}
	cmd.Flags().StringVar(&payload, "payload", "", `Event payload as JSON: inline, @file, or "-" for stdin.`)
	addReportFlags(cmd, &f, true)
	return cmd
}

// runEvent is the testable core: no os.Exit, returns exitErrors.
func runEvent(ctx context.Context, out io.Writer, stdin io.Reader, f *reportFlags, event, payloadSpec string) error {
	if !eventTokenRe.MatchString(event) {
		return usageErr(fmt.Errorf("event token %q must match [a-z0-9-]+", event))
	}
	raw, err := readPayload(payloadSpec, stdin)
	if err != nil {
		return usageErr(err)
	}
	r, ref, err := setupReporter(ctx, f)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Event(ref.SessionID, event, raw); err != nil {
		return failErr(err)
	}
	if f.jsonMode() {
		return emitJSON(out, map[string]any{"ok": true, "session_id": ref.SessionID, "event": event})
	}
	if !f.quiet {
		fmt.Fprintf(out, "✅ event %q published for session %s\n", event, ref.SessionID)
	}
	return nil
}

// readPayload resolves an event payload spec: "" → none, "@path" → file
// contents, "-" → stdin, else the literal string. The result is validated
// as JSON so a malformed payload fails fast (exit 2) rather than shipping
// a non-JSON blob into the envelope's "payload" field.
func readPayload(spec string, stdin io.Reader) (json.RawMessage, error) {
	if spec == "" {
		return nil, nil
	}
	var data []byte
	switch {
	case spec == "-":
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("read --payload from stdin: %w", err)
		}
		data = b
	case strings.HasPrefix(spec, "@"):
		b, err := os.ReadFile(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("read --payload file: %w", err)
		}
		data = b
	default:
		data = []byte(spec)
	}
	if !json.Valid(data) {
		return nil, errors.New("--payload is not valid JSON")
	}
	return json.RawMessage(data), nil
}

// emitJSON writes v as indented JSON (the result object on stdout).
func emitJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
