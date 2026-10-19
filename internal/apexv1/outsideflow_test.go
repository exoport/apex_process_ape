package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func outsideSubjects(r *StatusReport) []string {
	out := make([]string, 0, len(r.OutsideFlow))
	for _, o := range r.OutsideFlow {
		out = append(out, o.Subject)
	}
	return out
}

// H7: base-branch commits that touch code with no Change: trailer and no
// apex: subject are listed, since the v1 epoch and then since the last
// archive squash; planning-only commits, ape's records and attributed
// commits are not.
func TestStatus_OutsideFlow(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	require.Empty(t, p.Status().OutsideFlow, "no v1 epoch commit: nothing is judged")

	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1 !\n")
	commitAll(t, p, "hotfix: before the epoch") // exempt (D47 Q5)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1\n")
	commitAll(t, p, "hotfix: greet")
	write(t, p, "development/project.md", projectMD+"\nMore.\n")
	commitAll(t, p, "docs: planning only")
	write(t, p, "notes.txt", "x\n")
	commitAll(t, p, "apex: ape's own record")
	write(t, p, "notes.txt", "y\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "fix: attributed\n\nChange: add-wave")

	r := p.Status()
	require.Equal(t, []string{"hotfix: greet"}, outsideSubjects(r))
	require.Equal(t, []string{"greet.sh"}, r.OutsideFlow[0].Files)

	run(t, p.Root, "switch", "-q", "change/add-wave")
	require.Equal(t, []string{"hotfix: greet"}, outsideSubjects(p.Status()), "on a change branch: its base")

	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Empty(t, p.Status().OutsideFlow, "the archive squash is the new lower bound")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi $1\n")
	commitAll(t, p, "quick: tweak")
	require.Equal(t, []string{"quick: tweak"}, outsideSubjects(p.Status()))
}

// framework Q32: apex-release's `release: <version>` commit, trailer
// `Release:`, touching only the changelog is not outside the flow; one that
// touches more, or a changelog commit without the trailer, is.
func TestStatus_OutsideFlowReleaseCommit(t *testing.T) {
	p := reviewedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")

	write(t, p, "CHANGELOG.md", "# Changelog\n\n## v0.1.0\n- wave\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "release: v0.1.0\n\nRelease: v0.1.0")
	require.Empty(t, p.Status().OutsideFlow, "the release commit touches only the changelog")

	write(t, p, "CHANGELOG.md", "# Changelog\n\n## v0.1.1\n")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hi\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "release: v0.1.1\n\nRelease: v0.1.1")
	write(t, p, "CHANGELOG.md", "# Changelog\n\n## v0.1.2\n")
	commitAll(t, p, "docs: changelog by hand")
	require.Equal(t, []string{"docs: changelog by hand", "release: v0.1.1"}, outsideSubjects(p.Status()))
}

// D162-6: ape framework setup|update's install commit (trailer
// `Framework-Version:`) touching only what an install writes is not outside
// the flow; the trailer on a commit that also touches code exempts nothing,
// and an install-shaped commit without the trailer is listed.
func TestStatus_OutsideFlowFrameworkInstall(t *testing.T) {
	p := reviewedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")

	write(t, p, ".claude/skills/apex-plan/SKILL.md", "# plan\n")
	write(t, p, ".gitignore", "*.log\n")
	write(t, p, "CLAUDE.md", "@AGENTS.md\n")
	write(t, p, "_apex/framework.yaml", "version: v1.0.0-rc.6\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "chore(framework): install APEX framework v1.0.0-rc.6\n\nFramework-Version: v1.0.0-rc.6\nGenerator: ape framework setup")
	require.Empty(t, p.Status().OutsideFlow, "the install commit touches only framework files")

	write(t, p, ".claude/skills/apex-plan/SKILL.md", "# plan 2\n")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hey\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "chore(framework): update APEX framework to v1.0.0-rc.7\n\nFramework-Version: v1.0.0-rc.7")
	write(t, p, ".claude/skills/apex-plan/SKILL.md", "# plan 3\n")
	commitAll(t, p, "chore: skills by hand")
	require.Equal(t, []string{"chore: skills by hand", "chore(framework): update APEX framework to v1.0.0-rc.7"}, outsideSubjects(p.Status()))
}
