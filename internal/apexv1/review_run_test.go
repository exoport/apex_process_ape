package apexv1

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D18 read-only rule: in a review session (APE_REVIEW=<id>) the edit hook
// lets the session write only its review.md inside the project.
func TestPreTool_ReviewSessionIsReadOnly(t *testing.T) {
	p := fixture(t, baseFiles())
	t.Setenv(EnvReview, "add-wave")
	outside := filepath.Join(t.TempDir(), "copy", "greet.sh")
	for path, deny := range map[string]bool{
		"development/changes/add-wave/review.md":  false,
		outside:                                   false,
		"greet.sh":                                true,
		"development/changes/add-wave/change.md":  true,
		"development/changes/other-one/review.md": true,
		"development/project.md":                  true,
	} {
		d := PreTool(editPayload(t, p.Root, "Write", path))
		require.Equal(t, deny, d.Deny, path)
		if deny {
			require.Contains(t, d.Reason, "read-only review session for change add-wave")
		}
	}
	t.Setenv(EnvReview, "")
	require.False(t, PreTool(editPayload(t, p.Root, "Write", "greet.sh")).Deny, "outside a review session nothing changes")
}

func TestPrepareReviewRun(t *testing.T) {
	p := verifiedWave(t)
	_, err := p.PrepareReviewRun(ctx(), "add-wave")
	require.NoError(t, err)

	t.Setenv(EnvReview, "x")
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	var ue *UsageError
	require.ErrorAs(t, err, &ue, "depth 1")
	t.Setenv(EnvReview, "")

	write(t, p, "wave.sh", "#!/bin/sh\necho wave2\n")
	commitAll(t, p, "feat: more")
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	require.ErrorContains(t, err, "code changed since the last verify")
}

// The post-run assertion: review.md and the state ape commits are the only
// traces a review session may leave.
func TestSettleReviewRun(t *testing.T) {
	t.Run("clean session keeps its verdict", func(t *testing.T) {
		p := verifiedWave(t)
		run := &ReviewRun{Change: "add-wave", StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), DetachedID: "d1"}
		commitAll(t, p, "review: add-wave") // review.md, written by verifiedWave
		_, err := p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
		require.NoError(t, err)
		out, err := p.SettleReviewRun(ctx(), run)
		require.NoError(t, err)
		require.Empty(t, out.Violations)
		require.Equal(t, "pass", out.Verdict)
		require.Equal(t, StatusReviewed, out.Status)
	})
	t.Run("an uncommitted stray file voids it, verify still holds", func(t *testing.T) {
		p := verifiedWave(t)
		run := &ReviewRun{Change: "add-wave", StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), DetachedID: "d2"}
		_, err := p.Review(ctx(), "add-wave", "block", "development/changes/add-wave/review.md")
		require.NoError(t, err)
		write(t, p, "notes.txt", "scratch\n")
		out, err := p.SettleReviewRun(ctx(), run)
		require.NoError(t, err)
		require.True(t, out.Voided)
		require.Equal(t, []string{"left notes.txt"}, out.Violations)
		st := state(t, p, "add-wave")
		require.Equal(t, StatusVerified, st.Status, "the code under the verify did not move")
		require.Nil(t, st.Review)
		require.Equal(t, "block", st.ReviewVoided[0].Verdict)
	})
	t.Run("no verdict recorded after the run started", func(t *testing.T) {
		p := verifiedWave(t)
		run := &ReviewRun{Change: "add-wave", StartedAt: "2099-01-01T00:00:00Z", HeadSHA: HeadSHA(ctx(), p.Root)}
		commitAll(t, p, "review: add-wave")
		out, err := p.SettleReviewRun(ctx(), run)
		require.NoError(t, err)
		require.Empty(t, out.Verdict)
		require.False(t, out.Voided)
	})
}

func TestDecideReviewLaunch(t *testing.T) {
	cfg := SkillModel{Model: "claude-haiku-5-5", Effort: "low"}
	cfgModelOnly := SkillModel{Model: "claude-haiku-5-5"}
	for name, tc := range map[string]struct {
		flagModel, flagEffort string
		cfg                   SkillModel
		pModel, pEffort       string
		want                  ReviewLaunch
	}{
		"parent":          {pModel: "sonnet", pEffort: "high", want: ReviewLaunch{"sonnet", LaunchFromParent, "high", LaunchFromParent}},
		"parent, table":   {pModel: "sonnet", want: ReviewLaunch{"sonnet", LaunchFromParent, "", LaunchFromDefault}},
		"config wins":     {cfg: cfg, pModel: "sonnet", pEffort: "high", want: ReviewLaunch{"claude-haiku-5-5", LaunchFromConfig, "low", LaunchFromConfig}},
		"config model":    {cfg: cfgModelOnly, pModel: "sonnet", pEffort: "high", want: ReviewLaunch{"claude-haiku-5-5", LaunchFromConfig, "", LaunchFromDefault}},
		"flags win":       {flagModel: "opus", flagEffort: "max", cfg: cfg, pModel: "sonnet", want: ReviewLaunch{"opus", LaunchFromFlag, "max", LaunchFromFlag}},
		"flag model only": {flagModel: "opus", cfg: cfgModelOnly, pEffort: "high", want: ReviewLaunch{"opus", LaunchFromFlag, "high", LaunchFromParent}},
		"nothing":         {want: ReviewLaunch{"", LaunchFromDefault, "", LaunchFromDefault}},
	} {
		require.Equal(t, tc.want, DecideReviewLaunch(tc.flagModel, tc.flagEffort, tc.cfg, tc.pModel, tc.pEffort), name)
	}
}

func TestConfig_ModelsBothForms(t *testing.T) {
	root := t.TempDir()
	write := func(s string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(s), 0o644))
	}
	write("models:\n  apex-review: sonnet\n  apex-apply: {model: opus, effort: high}\n")
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	require.Equal(t, SkillModel{Model: "sonnet"}, cfg.Models["apex-review"])
	require.Equal(t, SkillModel{Model: "opus", Effort: "high"}, cfg.Models["apex-apply"])
}

// D51 Q16: archive refuses a reviewed change whose review went stale
// (review.stale, action review); review --run then verifies it again
// first and reviews it as a verified change. A fresh review is refused.
func TestReviewRun_StaleReviewReverifies(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeWrongStatus, fe.Code)
	require.Contains(t, fe.Msg, "is reviewed and its review is fresh")
	res, err := p.ReverifyStaleReview(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, res, "a fresh review: nothing verified")
	require.Equal(t, StatusReviewed, state(t, p, "add-wave").Status)

	// A person commits an evidence file on the change's branch.
	write(t, p, "evidence/screen.txt", "looks right\n")
	commitAll(t, p, "evidence")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "review.stale", fe.Code)
	require.Equal(t, ActionReview, ActionFor(fe.Code))
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	require.ErrorContains(t, err, "its review is stale")

	head := HeadSHA(ctx(), p.Root)
	res, err = p.ReverifyStaleReview(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Green)
	require.Equal(t, StatusVerified, res.Status)
	require.Equal(t, "apex: add-wave verified", subjects(t, p)[0])
	require.NotEqual(t, head, HeadSHA(ctx(), p.Root))
	_, err = p.PrepareReviewRun(ctx(), "add-wave")
	require.NoError(t, err, "now a verified change, reviewed as any")

	// A red re-verify returns its error.
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "break")
	res, err = p.ReverifyStaleReview(ctx(), "add-wave", RunOptions{})
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "verify.red", fe.Code)
	require.False(t, res.Green)
	require.Equal(t, StatusInProgress, state(t, p, "add-wave").Status)
}
