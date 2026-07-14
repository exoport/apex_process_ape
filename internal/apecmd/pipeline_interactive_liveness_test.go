package apecmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// TestInteractiveCore_ChildLivenessProbeWired verifies the PLAN-19 D4 pipeline
// wiring: OnStepStart installs the Driver's child-liveness probe from
// InteractiveStepInfo.SessionName, so a step-termination diagnostic reports
// concrete process liveness instead of "child liveness unknown". Without a
// SessionName (non-interactive callers) the diagnostic stays "unknown". The
// probe is diagnostic-only — it never influences the idle/max-duration
// decision.
func TestInteractiveCore_ChildLivenessProbeWired(t *testing.T) {
	newCore := func() *interactiveCore {
		c := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
		// Tiny idle window so the silent step trips the backstop fast
		// (the Driver floors the poll at ~1s, so this returns in ~1s).
		c.driver.SetIdleTimeout(300 * time.Millisecond)
		return c
	}

	t.Run("session name installs the liveness probe", func(t *testing.T) {
		c := newCore()
		// A session that does not exist: the probe still fires and reports
		// the (absent) process as exited — the point is it is no longer
		// "unknown", proving OnStepStart wired the probe.
		c.OnStepStart(pipeline.InteractiveStepInfo{
			Stage: "s", StepIdx: 0, Skill: "k",
			SessionName: "ape-no-such-session-plan19",
		})
		err := c.WaitStepDone(t.Context(), "s", 0)

		var ite *sessiondriver.IdleTimeoutError
		require.ErrorAs(t, err, &ite, "silent step must trip the idle backstop")
		require.Contains(t, ite.Diagnostic, "child pid",
			"a SessionName must install the child-liveness probe")
		require.NotContains(t, ite.Diagnostic, "child liveness unknown")
	})

	t.Run("no session name leaves liveness unknown", func(t *testing.T) {
		c := newCore()
		c.OnStepStart(pipeline.InteractiveStepInfo{Stage: "s", StepIdx: 0, Skill: "k"})
		err := c.WaitStepDone(t.Context(), "s", 0)

		var ite *sessiondriver.IdleTimeoutError
		require.ErrorAs(t, err, &ite)
		require.Contains(t, ite.Diagnostic, "child liveness unknown",
			"no SessionName means no probe, so liveness stays unknown")
	})
}
