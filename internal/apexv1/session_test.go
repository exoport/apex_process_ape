package apexv1

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelSession(t *testing.T) {
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	require.Empty(t, ModelSession())
	require.NoError(t, requirePerson("--accept-red"))

	t.Setenv("CLAUDECODE", "1")
	require.Equal(t, "CLAUDECODE", ModelSession())
	t.Setenv("APE_SESSION", "task:abc")
	require.Equal(t, "APE_SESSION", ModelSession())
	err := requirePerson("--accept-red")
	var ue *UsageError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, CodePersonOnly, ue.Code)
	require.Contains(t, ue.Msg, "--accept-red is for a person")
}

// D47 Q6: a model never approves a milestone or a fix; nothing is
// written when one tries.
func TestApprovals_PersonOnly(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = "# Roadmap\n\n## M1 — Skeleton\nStatus: proposed\nGate: ./gate.sh\nGoal: A greeter.\n"
	files["gate.sh"] = "#!/bin/sh\nexit 1\n" // red, as a gate is at approval (D70 B)
	p := fixture(t, files)
	addChange(t, p, "fix-typo", strings.Replace(changeDoc("fix-typo", "greeting", "none — a typo", "- T1 fix — check: true"), "milestone: M1", "milestone: none", 1))
	commitAll(t, p, "fix")
	head := HeadSHA(ctx(), p.Root)
	for _, k := range []string{"CLAUDECODE", "APE_SESSION"} {
		t.Setenv(k, "1")
		_, err := p.ApproveMilestone(ctx(), "M1", "ann", nil)
		ue, ok := errors.AsType[*UsageError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, CodePersonOnly, ue.Code)
		require.Contains(t, ue.Msg, "ape milestone approve is for a person")
		_, err = p.ApproveFix(ctx(), "fix-typo", "ann")
		require.ErrorContains(t, err, "ape change approve is for a person")
		t.Setenv(k, "")
	}
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing written")
	_, err := p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)
	_, err = p.ApproveFix(ctx(), "fix-typo", "ann")
	require.NoError(t, err)
}
