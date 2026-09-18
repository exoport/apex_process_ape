package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// The run this exists for: a framework eval capture ended after 3h44m
// with `status: failed`, `steps: []` and totals of zero. The idle backstop
// had fired exactly as designed, 60 minutes after the parent's last hook,
// and nothing on disk said so — the artifact was indistinguishable from a
// step that did nothing at all.
func TestNewTerminationRecord_IdleCarriesTheNumbersAndTheDiagnostic(t *testing.T) {
	t.Parallel()

	err := &sessiondriver.IdleTimeoutError{
		Label:      "interactive step",
		Idle:       60 * time.Minute,
		Window:     60 * time.Minute,
		LastSource: "hook",
		Diagnostic: "last progress hook 1h0m0s ago (hook 1h0m0s ago; transcript none for 3h44m; pty n/a); child pid 4242 alive",
	}

	rec := newTerminationRecord(fmt.Errorf("stage apex-story-batch-dev: %w", err))
	require.NotNil(t, rec)
	require.Equal(t, TerminationIdle, rec.Kind)
	require.InDelta(t, 3600.0, rec.IdleSecs, 0.001)
	require.InDelta(t, 3600.0, rec.WindowSecs, 0.001)
	require.Equal(t, "hook", rec.LastSource)
	require.Contains(t, rec.Diagnostic, "pty n/a",
		"the diagnostic is the whole point of persisting this: `pty n/a` is what separates "+
			"'ape was not watching PTY on this path' from 'claude stopped drawing'")
	require.Contains(t, rec.Message, "idle for 1h0m0s")
}

// Wrapped, because that is how these errors actually arrive: the stage
// runner wraps the driver's error before the runner sees it. errors.As
// unwraps; a type assertion would not, and would silently downgrade every
// real termination to the generic kind.
func TestNewTerminationRecord_ClassifiesThroughWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		wantKind string
	}{
		{
			name:     "max duration",
			err:      fmt.Errorf("stage x: %w", &sessiondriver.MaxDurationError{Elapsed: 4 * time.Hour, Max: 3 * time.Hour}),
			wantKind: TerminationMaxDuration,
		},
		{
			name:     "terminal api error",
			err:      fmt.Errorf("stage x: %w", &sessiondriver.TerminalAPIError{Message: "API Error 529", Quiet: 90 * time.Second}),
			wantKind: TerminationAPIError,
		},
		{
			name:     "context cancelled",
			err:      fmt.Errorf("stage x: %w", context.Canceled),
			wantKind: TerminationCancelled,
		},
		{
			name:     "anything else",
			err:      errors.New("spawn claude: exec format error"),
			wantKind: TerminationError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := newTerminationRecord(tc.err)
			require.NotNil(t, rec)
			require.Equal(t, tc.wantKind, rec.Kind)
			require.NotEmpty(t, rec.Message)
		})
	}
}

// An idle termination reaches some callers as a cancelled context, because
// cancelling the run is HOW the backstop stops it. Reporting that as a
// plain cancellation would throw away the reason, which is the one thing
// this record exists to keep.
func TestNewTerminationRecord_IdleBeatsCancelled(t *testing.T) {
	t.Parallel()

	idle := &sessiondriver.IdleTimeoutError{Idle: time.Hour, Window: time.Hour, Diagnostic: "d"}
	err := fmt.Errorf("%w: %w", context.Canceled, idle)

	require.ErrorIs(t, err, context.Canceled, "precondition: this error IS a cancelled context")
	require.Equal(t, TerminationIdle, newTerminationRecord(err).Kind,
		"a cancelled context that carries an idle timeout must report the idle timeout")
}

func TestNewTerminationRecord_CleanRunCarriesNothing(t *testing.T) {
	t.Parallel()
	require.Nil(t, newTerminationRecord(nil),
		"a completed run's status says everything; an empty record only invites a reader to find meaning in it")
}

// The field is additive: a manifest written without one must not grow an
// empty `termination:` key, or every completed run's artifact changes
// shape for readers that already parse it.
func TestManifest_TerminationIsOmittedWhenAbsent(t *testing.T) {
	t.Parallel()

	out, err := yaml.Marshal(&Manifest{SchemaVersion: 2, Status: StatusCompleted})
	require.NoError(t, err)
	require.NotContains(t, string(out), "termination")
}

func TestRenderReport_TerminationExplainsTheZeros(t *testing.T) {
	t.Parallel()

	m := &Manifest{
		SchemaVersion: 2,
		Pipeline:      Ref{Name: "apex-story-batch-dev"},
		Status:        StatusFailed,
		DurationSecs:  13443,
		Stages:        []StageRecord{{Index: 1, Name: "apex-story-batch-dev", Status: StatusFailed}},
		Termination: &TerminationRecord{
			Kind:       TerminationIdle,
			Message:    "interactive step idle for 1h0m0s without progress (window 1h0m0s)",
			Diagnostic: "last progress hook 1h0m0s ago (hook 1h0m0s ago; transcript none for 3h44m; pty n/a)",
			LastSource: "hook",
			IdleSecs:   3600,
			WindowSecs: 3600,
		},
	}

	got := renderReport(m)

	require.Contains(t, got, "## Why this run ended")
	require.Contains(t, got, "**idle_timeout**")
	require.Contains(t, got, "last progress from `hook`")
	require.Contains(t, got, "pty n/a")
	require.Less(t, strings.Index(got, "## Why this run ended"), strings.Index(got, "## Totals"),
		"the reason must precede the totals: a reader who meets `cost $0.0000 / steps run 0` first "+
			"concludes the run did nothing, which is exactly the misreading this section prevents")
}

func TestRenderReport_NoTerminationSectionOnACleanRun(t *testing.T) {
	t.Parallel()

	got := renderReport(&Manifest{SchemaVersion: 2, Status: StatusCompleted})
	require.NotContains(t, got, "Why this run ended")
}
