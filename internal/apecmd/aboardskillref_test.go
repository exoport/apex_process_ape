package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/stretchr/testify/require"
)

// aboardSkillProject builds a directory isProjectRoot accepts, carrying a
// generated-reference file stamped with the given hash (empty = no reference,
// which is the state of almost every project).
func aboardSkillProject(t *testing.T, stamp string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	if stamp != "" {
		ref := aboard.Root(root).SkillReference()
		require.NoError(t, os.MkdirAll(filepath.Dir(ref), 0o755))
		require.NoError(t, os.WriteFile(ref,
			[]byte("# aboard reference\n\ncapsHash: "+stamp+"\n\ngenerated, do not edit\n"), 0o600))
	}
	return root
}

func runAboardSkillRef(t *testing.T, root string) CheckResult {
	t.Helper()
	return checkAboardSkillReference(context.Background(), doctorEnv{ProjectRoot: root})
}

// servedCapsHash is what THIS binary's mounted board describes. Read from the
// library rather than written down: a literal here would have to be edited on
// every aboard bump, and the one it drifted from is the value under test.
func servedCapsHash(t *testing.T) string {
	t.Helper()
	rep := aboard.Status(context.Background(), aboard.Root(t.TempDir()), "", aboard.WebFS())
	require.NotEmpty(t, rep.CapsHash, "the mounted board must describe a surface")
	return rep.CapsHash
}

func TestAboardSkillRef_NoProjectRootIsInfo(t *testing.T) {
	t.Parallel()
	res := checkAboardSkillReference(context.Background(), doctorEnv{})
	require.Equal(t, StatusInfo, res.Status)
}

// A bare temp dir is not a project root, so there is nothing to judge.
func TestAboardSkillRef_NonProjectIsInfo(t *testing.T) {
	t.Parallel()
	res := runAboardSkillRef(t, t.TempDir())
	require.Equal(t, StatusInfo, res.Status)
}

// Most projects never copy the skill. Absent is not drift.
func TestAboardSkillRef_AbsentReferenceIsASkip(t *testing.T) {
	t.Parallel()
	res := runAboardSkillRef(t, aboardSkillProject(t, ""))
	require.Equal(t, StatusSkip, res.Status)
	require.Contains(t, res.Message, "no .claude/skills/aboard reference")
}

func TestAboardSkillRef_MatchingStampPasses(t *testing.T) {
	t.Parallel()
	res := runAboardSkillRef(t, aboardSkillProject(t, servedCapsHash(t)))
	require.Equal(t, StatusOK, res.Status)
	require.Contains(t, res.Message, "current")
}

// The case the check exists for: a copied reference describing a board this
// binary no longer serves. A warn, not a fail — a stale reference degrades an
// agent's writes, it does not stop the project working.
func TestAboardSkillRef_StaleStampWarns(t *testing.T) {
	t.Parallel()
	res := runAboardSkillRef(t, aboardSkillProject(t, "deadbeef"))
	require.Equal(t, StatusWarn, res.Status)
	require.Contains(t, res.Message, "deadbeef")
	require.Contains(t, res.Message, servedCapsHash(t))
	require.NotEmpty(t, res.FixCommand)
}

// The check must never be the thing that breaks doctor. A reference too short
// for the stamp scan panicked inside aboard.stampedHash up to and including
// v0.1.1 — `strings.Split(body, "\n")[:6]` on a file with fewer than six lines
// — and it took `ape aboard status` down the same way, so ape v0.0.55 shipped
// the crash. aboard v0.1.2 bounds both that slice and the `strings.Fields`
// index beside it; this test is what stops a future dependency bump from
// quietly reintroducing either, in the one command that must survive a project
// where something is already wrong.
func TestAboardSkillRef_ShortReferenceDoesNotPanic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	ref := aboard.Root(root).SkillReference()
	require.NoError(t, os.MkdirAll(filepath.Dir(ref), 0o755))
	require.NoError(t, os.WriteFile(ref, []byte("a\nb\n"), 0o600))

	require.NotPanics(t, func() {
		require.Equal(t, StatusSkip, runAboardSkillRef(t, root).Status)
	})
}

// The check is registered. A probe nothing runs is worse than no probe: it
// reads as coverage.
func TestAboardSkillRef_IsRegistered(t *testing.T) {
	t.Parallel()
	var found bool
	for _, c := range allChecks {
		if c.Name == "aboard.skill_reference" {
			found = true
			require.False(t, c.Required, "drift is a warn, not a required gate")
		}
	}
	require.True(t, found, "aboard.skill_reference must be in allChecks")
}
