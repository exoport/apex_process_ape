package apexv1

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// selfCheckProject is a project with a proposed M1 whose gate is gate,
// and the files given.
func selfCheckProject(t *testing.T, gate string, extra map[string]string) *Project {
	t.Helper()
	files := baseFiles()
	files["development/roadmap.md"] = "# Roadmap\n\n## M1 — Skeleton\nStatus: proposed\nGate: " + gate + "\nGoal: A greeter.\n"
	maps.Copy(files, extra)
	return fixture(t, files)
}

// D70 B: approve runs the gate once against the current tree; a gate that
// is green, or cannot run, is refused (action replan) and nothing is
// written; a red one is approved with its run recorded.
func TestApprove_GateSelfCheck(t *testing.T) {
	refused := []struct {
		name, gate string
		files      map[string]string
		config     string
		code, want string
	}{
		{
			"green", "./gate.sh",
			map[string]string{"gate.sh": "#!/bin/sh\necho all good\nexit 0\n"},
			"", CodeGateGreen,
			"M1's gate is already green on the current tree (exit 0 in",
		},
		{
			"a missing script", "./scripts/e2e.sh --all", nil, "", CodeGateBroken,
			"it runs scripts/e2e.sh, which does not exist",
		},
		{
			"a missing interpreter script", "python3 scripts/gate.py", nil, "", CodeGateBroken,
			"it runs scripts/gate.py, which does not exist",
		},
		{
			"exit 127", "no-such-tool-xyz --smoke", nil, "", CodeGateBroken,
			"it exited 127 (a command it runs was not found)",
		},
		{
			"exit 126", "./gate.sh",
			map[string]string{"gate.sh": "#!/bin/sh\nexit 1\n"},
			"", CodeGateBroken,
			"it exited 126 (a command it runs is not executable)",
		},
		{
			"a syntax error", "sh gate.sh",
			map[string]string{"gate.sh": "#!/bin/sh\nif then fi\nexit 1\n"},
			"", CodeGateBroken,
			"gate.sh is not valid shell (`bash -n`:",
		},
		{
			"a timeout", "./gate.sh",
			map[string]string{"gate.sh": "#!/bin/sh\necho waiting\nsleep 30\n"},
			"timeouts:\n  gate: 1s\n", CodeGateBroken,
			"it reached its ceiling of 1s (timeouts.gate max)",
		},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			p := selfCheckProject(t, tc.gate, tc.files)
			if tc.name == "exit 126" {
				require.NoError(t, os.Chmod(filepath.Join(p.Root, "gate.sh"), 0o644))
				commitAll(t, p, "not executable")
			}
			if tc.config != "" {
				write(t, p, "_apex/config.yaml", tc.config)
				commitAll(t, p, "config")
			}
			head, roadmap := HeadSHA(ctx(), p.Root), read(t, p, "development/roadmap.md")
			var progress bytes.Buffer
			_, err := p.ApproveMilestone(ctx(), "M1", "ann", &progress)
			require.Equal(t, tc.code, failCode(t, err))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, ActionReplan, Diagnostics(err)[0].Action)
			require.Equal(t, head, HeadSHA(ctx(), p.Root), "nothing committed")
			require.Equal(t, roadmap, read(t, p, "development/roadmap.md"), "nothing written")
			recs, rerr := p.LoadMilestoneRecords()
			require.NoError(t, rerr)
			require.True(t, recs["M1"] == nil || recs["M1"].Approval == nil, "no approval recorded")
			switch tc.name {
			case "green":
				require.ErrorContains(t, err, "Last output (_apex/state/logs/gate-M1-approve.log): all good")
				require.Contains(t, progress.String(), "M1: running the gate once against")
				require.Contains(t, progress.String(), "M1: the gate is GREEN (exit 0")
			case "a timeout":
				require.ErrorContains(t, err, "waiting")
			}
		})
	}

	t.Run("red: approved, the run recorded", func(t *testing.T) {
		p := selfCheckProject(t, "./gate.sh && grep -q wave greet.sh", map[string]string{"gate.sh": "#!/bin/sh\necho 'no wave yet'\nexit 3\n"})
		head := HeadSHA(ctx(), p.Root)
		var progress bytes.Buffer
		m, err := p.ApproveMilestone(ctx(), "M1", "ann", &progress)
		require.NoError(t, err)
		require.Equal(t, MilestoneApproved, m.Status)
		require.Contains(t, progress.String(), "M1: the gate is red (exit 3 in")
		recs, err := p.LoadMilestoneRecords()
		require.NoError(t, err)
		sc := recs["M1"].Approval.SelfCheck
		require.NotNil(t, sc)
		require.Equal(t, 3, sc.Exit)
		require.Equal(t, head, sc.SHA, "the tree it ran against")
		require.Equal(t, "_apex/state/logs/gate-M1-approve.log", sc.Log)
		require.FileExists(t, filepath.Join(p.Root, filepath.FromSlash(sc.Log)))
		require.NotEmpty(t, sc.OutputSHA256)
		require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the self-check's log is ignored state")
	})
}

func TestShellSyntax(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		content string
		failed  bool
	}{
		"ok.sh":     {"#!/bin/sh\necho ok\n", false},
		"bad.sh":    {"if then fi\n", true},
		"env-bash":  {"#!/usr/bin/env bash\nif [[ x ]]; then\n", true},
		"python.sh": {"#!/usr/bin/env python3\nif then fi\n", false},
		"plain":     {"if then fi\n", false},
	} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))
		_, failed := shellSyntax(ctx(), path)
		require.Equal(t, tc.failed, failed, name)
	}
}

// RK-5 (Q145): a person fixes a gate after the milestone's work is done;
// the fixed gate is green, so a re-approval is refused (the message names
// --accept-green) and gate and close refuse the changed gate. With
// --accept-green the green run is recorded beside the reason and close
// closes it. On a first approval the flag is a usage error; on a red
// re-approval it is not recorded.
func TestApprove_AcceptGreenOnAReapproval(t *testing.T) {
	p := selfCheckProject(t, "./gate.sh", map[string]string{"gate.sh": "#!/bin/sh\ntest -f done.flag\n"})
	head := HeadSHA(ctx(), p.Root)
	_, err := p.ApproveMilestoneAcceptGreen(ctx(), "M1", "ann", "fixed after the work", nil)
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.accept-green", ue.Code)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "a refused first approval writes nothing")
	_, err = p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)

	// The work is done; then the gate gets a fix (a person or apex-plan).
	write(t, p, "done.flag", "done\n")
	commitAll(t, p, "feat: done")
	write(t, p, "gate.sh", "#!/bin/sh\n# fixed: the check the milestone means\ntest -f done.flag\n")
	commitAll(t, p, "plan: amend the gate")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeGateChanged, failCode(t, err))
	_, err = p.ApproveMilestone(ctx(), "M1", "bob", nil)
	require.Equal(t, CodeGateGreen, failCode(t, err))
	require.ErrorContains(t, err, "a person re-approves with `ape milestone approve M1 --by <who> --accept-green \"<reason>\"`")
	_, err = p.ApproveMilestoneAcceptGreen(ctx(), "M1", "bob", " ", nil)
	ue, ok = errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "usage.reason", ue.Code)

	var progress bytes.Buffer
	m, err := p.ApproveMilestoneAcceptGreen(ctx(), "M1", "bob", "fixed after the work", &progress)
	require.NoError(t, err)
	require.Equal(t, MilestoneApproved, m.Status)
	require.Contains(t, progress.String(), "M1: the gate is GREEN (exit 0")
	require.Contains(t, progress.String(), "re-approving, as accepted (--accept-green)")
	require.Equal(t, "apex: M1 re-approved (gate green: fixed after the work)", subjects(t, p)[0])
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	a := recs["M1"].Approval
	require.Equal(t, 0, a.SelfCheck.Exit)
	require.Equal(t, &AcceptGreen{Reason: "fixed after the work", By: "bob", At: a.At}, a.AcceptGreen)
	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, MilestoneDone, g.Status)

	t.Run("a red re-approval records no accept_green", func(t *testing.T) {
		p := selfCheckProject(t, "./gate.sh", map[string]string{"gate.sh": "#!/bin/sh\ntest -f done.flag\n"})
		_, err := p.ApproveMilestone(ctx(), "M1", "ann", nil)
		require.NoError(t, err)
		write(t, p, "gate.sh", "#!/bin/sh\n# still red\ntest -f done.flag\n")
		commitAll(t, p, "plan: amend the gate")
		_, err = p.ApproveMilestoneAcceptGreen(ctx(), "M1", "bob", "not needed", nil)
		require.NoError(t, err)
		require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
		recs, err := p.LoadMilestoneRecords()
		require.NoError(t, err)
		require.Nil(t, recs["M1"].Approval.AcceptGreen)
		require.Equal(t, 1, recs["M1"].Approval.SelfCheck.Exit)
	})
}
