package pipeline //nolint:testpackage // white-box tests unexported resolveCommitOutcome

import (
	"context"
	"errors"
	"testing"
)

// TestResolveCommitOutcome_SkipStates pins the skip-state decision table
// (PLAN-6 / C2 conditions 1–4) without touching git. These are the cases
// that used to be covered by programmatic integration tests; the
// skipped-step-failed case in particular is no longer reachable from an
// interactive integration run (a failed step breaks the stage loop before
// performStepCommit), so this unit test is its coverage.
func TestResolveCommitOutcome_SkipStates(t *testing.T) {
	tests := []struct {
		name       string
		opts       RunOptions
		plan       StageCommitPlan
		stepRunErr error
		want       CommitStatus
	}{
		{
			name:       "step-cancelled",
			stepRunErr: context.Canceled,
			want:       CommitStatusSkippedCancelled,
		},
		{
			name:       "step-deadline-exceeded",
			stepRunErr: context.DeadlineExceeded,
			want:       CommitStatusSkippedCancelled,
		},
		{
			name:       "step-failed",
			stepRunErr: errors.New("step blew up"),
			want:       CommitStatusSkippedStepFailed,
		},
		{
			name: "no-commit-flag",
			opts: RunOptions{NoCommit: true},
			want: CommitStatusSkippedByFlag,
		},
		{
			name: "suppressed-by-spec",
			plan: StageCommitPlan{Suppressed: true},
			want: CommitStatusSkippedBySpec,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// isLastStep=false; git is never reached for skip states.
			status, _, sha, _, commitErr := resolveCommitOutcome(
				context.Background(), tc.opts, tc.plan, 1, false, tc.stepRunErr,
			)
			if status != tc.want {
				t.Errorf("status = %q, want %q", status, tc.want)
			}
			if sha != "" {
				t.Errorf("skip state must not record a sha, got %q", sha)
			}
			if commitErr != nil {
				t.Errorf("skip state must not return a commit error, got %v", commitErr)
			}
		})
	}
}
