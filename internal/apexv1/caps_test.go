package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseByteSize(t *testing.T) {
	for in, want := range map[string]int{
		"16384": 16384, "16KB": 16 << 10, "16 KB": 16 << 10, "16KiB": 16 << 10, "16kb": 16 << 10,
		"1MiB": 1 << 20, "1MB": 1 << 20, "512B": 512, " 48KB ": 48 << 10,
	} {
		got, err := ParseByteSize(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "0", "-1KB", "16GB", "1.5KB", "KB", "lots", "16 K B"} {
		_, err := ParseByteSize(in)
		require.Error(t, err, in)
	}
}

func TestCaps_DefaultsCeilingsAndParse(t *testing.T) {
	require.Equal(t, Caps{ProjectMD: 16 << 10, AgentsMD: 16 << 10, ChangeMD: 16 << 10, DesignMD: 16 << 10, Spec: 24 << 10, EvidenceLog: 1 << 20, ArchitectureMD: 16 << 10}, DefaultCaps())

	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte("caps:\n  project_md: 32KB\n  spec: 40960\n  evidence_log: 4MiB\n"), &cfg))
	require.Equal(t, Caps{ProjectMD: 32 << 10, Spec: 40 << 10, EvidenceLog: 4 << 20}, cfg.Caps)
	limits := cfg.Caps.Limits()
	require.Equal(t, 32<<10, limits.ProjectMD)
	require.Equal(t, 16<<10, limits.ChangeMD, "unset: the default")
	require.Empty(t, cfg.Caps.OverCeiling())

	// Above its ceiling: parses, is reported, and is measured at the ceiling.
	require.NoError(t, yaml.Unmarshal([]byte("caps:\n  change_md: 64KB\n  evidence_log: 32MiB\n"), &cfg))
	fs := cfg.Caps.OverCeiling()
	require.Len(t, fs, 2)
	require.Contains(t, findingMessages(fs), "caps.change_md: 64 KB is over its 48 KB ceiling (change.md)")
	require.Contains(t, findingMessages(fs), "caps.evidence_log: 32 MiB is over its 16 MiB ceiling")
	for _, f := range ClassifyFindings(fs) {
		require.Equal(t, CodeCapsOverCeiling, f.Code)
		require.Equal(t, ActionPerson, ActionFor(f.Code))
	}
	require.Equal(t, 48<<10, cfg.Caps.Limits().ChangeMD)
	require.Equal(t, 16<<20, cfg.Caps.Limits().EvidenceLog)

	// An unknown key or an unreadable size makes the config unparseable.
	require.ErrorContains(t, yaml.Unmarshal([]byte("caps:\n  project_lines: 150\n"), &Config{}), "caps.project_lines: not a key ape knows")
	require.ErrorContains(t, yaml.Unmarshal([]byte("caps:\n  spec: 2GB\n"), &Config{}), "caps.spec:")
}

// D69: AGENTS.md has a byte cap and no line measurement; project.md has no
// line cap.
func TestCaps_AgentsAndProjectAreBytesOnly(t *testing.T) {
	p := fixture(t, baseFiles())
	agents := baseFiles()["AGENTS.md"]
	write(t, p, "AGENTS.md", agents+strings.Repeat("- x\n", 1000))
	write(t, p, "development/project.md", projectMD+strings.Repeat("- note\n", 500))
	require.Empty(t, p.Check(), "a thousand short lines under the byte caps pass")

	write(t, p, "AGENTS.md", agents+strings.Repeat("x", 16<<10+1-len(agents)))
	got := ClassifyFindings(p.Check())
	require.Len(t, got, 1)
	require.Equal(t, "agents.too-big", got[0].Code)
	require.Contains(t, got[0].Message, "AGENTS.md is 16.0 KB, over the 16 KB cap (caps.agents_md)")

	write(t, p, "_apex/config.yaml", "caps:\n  agents_md: 20KB\n")
	require.Empty(t, p.Check(), "a configured cap is the one measured")
}

// A value above its ceiling is a project check finding, and the verbs that
// enforce a cap refuse until a person fixes it; abandon enforces none.
func TestCaps_OverCeilingRefusesTheCapVerbs(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	write(t, p, "_apex/config.yaml", "caps:\n  spec: 128KB\n")
	commitAll(t, p, "plan: add-wave")

	fs := ClassifyFindings(p.Check())
	n := 0
	for _, f := range fs {
		if f.Code == CodeCapsOverCeiling {
			n++
			require.Equal(t, "_apex/config.yaml", f.File)
		}
	}
	require.Equal(t, 1, n, "reported once, not once per change")
	require.Contains(t, findingMessages(p.CheckChange("add-wave")), "caps.spec: 128 KB is over its 64 KB ceiling")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeCheckFailed, failCode(t, err))
	_, err = p.Abandon(ctx(), "add-wave", "dropped")
	require.NoError(t, err, "abandon measures nothing (D89 Q40)")
}

// whyGrown is change.md with 16 KB more in ## Why, which may change
// after start: over the default cap.
func whyGrown(doc string) string {
	const why = "## Why\nBecause the greeter must change.\n"
	return strings.Replace(doc, why, why+strings.Repeat("w", 16<<10)+"\n", 1)
}

func TestPlanBytes(t *testing.T) {
	plan := "---\nid: x\n---\n# X\n## Why\nwhy\n## Tasks\n- T1 a — check: true\n"
	notes := "## Notes\nsome notes\n### a sub-heading in the notes\nmore\n"
	for name, tc := range map[string]struct {
		doc  string
		want int
	}{
		"no notes":              {plan, len(plan)},
		"notes last":            {plan + notes, len(plan)},
		"notes in the middle":   {strings.Replace(plan, "## Tasks", notes+"## Tasks", 1), len(plan)},
		"notes up to an h1":     {plan + notes + "# Appendix\nkept\n", len(plan) + len("# Appendix\nkept\n")},
		"case-insensitive":      {plan + "## NOTES\nn\n", len(plan)},
		"no trailing newline":   {plan + "## Notes\nn", len(plan)},
		"notes in a code block": {plan + "```\n## Notes\nnot a heading\n```\n", len(plan + "```\n## Notes\nnot a heading\n```\n")},
		"an h3 notes counts":    {plan + "### Notes\nn\n", len(plan + "### Notes\nn\n")},
	} {
		require.Equal(t, tc.want, ParseChange("change.md", tc.doc).PlanBytes(), name)
	}
	// Bytes, not runes; CRLF lines are measured as they are on disk.
	crlf := strings.ReplaceAll(plan+"## Why\n— ünïcode\n", "\n", "\r\n")
	require.Equal(t, len(crlf), ParseChange("change.md", crlf+"## Notes\r\nn\r\n").PlanBytes())
	require.Equal(t, len(crlf), ParseChange("change.md", crlf).PlanBytes())
}

// change.md's cap (16 KB by default) measures the plan — change.md less
// ## Notes (D89) — at check and start, and at archive too (D69); the whole
// file is held to the 48 KB ceiling. Abandon measures nothing (Q40).
func TestCaps_ChangeMDMeasuresThePlan(t *testing.T) {
	t.Run("check and start", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", whyGrown(waveChange("add-wave")))
		commitAll(t, p, "plan: add-wave")
		fs := ClassifyFindings(p.CheckChange("add-wave"))
		require.Contains(t, findingMessages(fs), "without ## Notes, over the 16 KB cap (caps.change_md) — split this change")
		require.Equal(t, "change.too-big", fs[0].Code)
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.Equal(t, CodeCheckFailed, failCode(t, err), "start runs the check")
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok)
		require.Contains(t, findingMessages(fe.Findings), "without ## Notes, over the 16 KB cap")

		// The same bytes in ## Notes: under the cap, start goes ahead.
		write(t, p, "development/changes/add-wave/change.md", waveChange("add-wave")+strings.Repeat("a working note\n", 2000))
		commitAll(t, p, "notes")
		require.Empty(t, p.CheckChange("add-wave"))
		_, err = p.Start(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
	})
	t.Run("the file ceiling, whatever the cap", func(t *testing.T) {
		p := fixture(t, baseFiles())
		write(t, p, "_apex/config.yaml", "caps:\n  change_md: 48KB\n")
		addChange(t, p, "add-wave", waveChange("add-wave")+strings.Repeat("n", 48<<10))
		commitAll(t, p, "plan: add-wave")
		fs := ClassifyFindings(p.CheckChange("add-wave"))
		require.Len(t, fs, 1)
		require.Equal(t, CodeChangeOverCeiling, fs[0].Code)
		require.Contains(t, fs[0].Message, "in all, over the 48 KB file ceiling — ## Notes is for working notes only: move bulk to the change's assets/")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.Equal(t, CodeCheckFailed, failCode(t, err))
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok)
		require.Contains(t, findingMessages(fe.Findings), "over the 48 KB file ceiling")
	})
	t.Run("archive", func(t *testing.T) {
		p := reviewedWaveFrom(t, baseFiles())
		path := "development/changes/add-wave/change.md"
		doc := read(t, p, path)
		// ## Why may change after start: it grew past the cap.
		write(t, p, path, whyGrown(doc))
		_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.Equal(t, "change.too-big", failCode(t, err))
		require.ErrorContains(t, err, "without ## Notes, over the 16 KB cap (caps.change_md)")
		require.ErrorContains(t, err, "move what ## Why gained since start to ## Notes, commit, then archive again")
		require.DirExists(t, p.ChangeDir("add-wave"), "nothing was archived")
		require.Equal(t, whyGrown(doc), read(t, p, path), "archive never edits change.md")

		// Notes past the ceiling: refused.
		write(t, p, path, doc+strings.Repeat("a note that grew after the plan froze\n", 1400))
		_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.Equal(t, CodeChangeOverCeiling, failCode(t, err))
		require.ErrorContains(t, err, "move bulk to the change's assets/, commit, then archive again")

		// Notes past the cap but under the ceiling: archived as used.
		grown := doc + strings.Repeat("a note that grew after the plan froze\n", 450)
		write(t, p, path, grown)
		res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.NoError(t, err, "## Notes does not count against the cap")
		require.Equal(t, grown, read(t, p, res.ArchivedTo+"/change.md"), "the folder moves exactly as used")
	})
	t.Run("abandon never refuses for size", func(t *testing.T) {
		p := fixture(t, baseFiles())
		big := whyGrown(waveChange("add-wave")) + strings.Repeat("n", 48<<10)
		addChange(t, p, "add-wave", big)
		commitAll(t, p, "plan: add-wave")
		res, err := p.Abandon(ctx(), "add-wave", "too big")
		require.NoError(t, err, "over the cap and the ceiling, it is moved out of the way")
		require.Equal(t, big, read(t, p, res.ArchivedTo+"/change.md"), "as used, never trimmed")
	})
}

// caps.evidence_log caps one check's output in its evidence file.
func TestCaps_EvidenceLogConfigured(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "_apex/config.yaml", "caps:\n  evidence_log: 1KB\n")
	out := strings.Repeat("h", 2000) + strings.Repeat("t", 2000)
	write(t, p, "_apex/state/logs/x/T1.log", out)
	sum := sha256.Sum256([]byte(out))
	r := CheckResult{Task: "T1", Cmd: "x", Exit: 1, OutputSHA256: hex.EncodeToString(sum[:]), Log: "_apex/state/logs/x/T1.log"}
	ev := p.evidenceFile(&r)
	require.Contains(t, ev, "output: 4000 bytes, over the 1 KB cap (caps.evidence_log)")
	require.Less(t, len(ev), 1024+1024)
}

// D84: architecture.md is optional; when present it is under
// caps.architecture_md (default 16 KB, ceiling 48 KB).
func TestCaps_ArchitectureMD(t *testing.T) {
	require.Equal(t, 16<<10, DefaultCaps().ArchitectureMD)
	p := fixture(t, baseFiles())
	require.Equal(t, filepath.Join(p.Root, "development", "architecture.md"), p.ArchitectureMD())
	require.Empty(t, p.Check(), "absent: nothing to measure")

	write(t, p, "development/architecture.md", "# Architecture\n"+strings.Repeat("x", 8<<10))
	require.Empty(t, p.Check(), "under the cap")

	write(t, p, "development/architecture.md", strings.Repeat("x", 16<<10+1))
	got := ClassifyFindings(p.Check())
	require.Len(t, got, 1)
	require.Equal(t, "architecture.too-big", got[0].Code)
	require.Equal(t, ActionPerson, ActionFor(got[0].Code))
	require.Equal(t, "development/architecture.md", got[0].File)
	require.Equal(t, "architecture.md is 16.0 KB, over the 16 KB cap (caps.architecture_md) — move detail to specs/, decisions/ or docs/explanation/", got[0].Message)

	write(t, p, "_apex/config.yaml", "caps:\n  architecture_md: 20KB\n")
	require.Empty(t, p.Check(), "a configured cap is the one measured")

	write(t, p, "_apex/config.yaml", "caps:\n  architecture_md: 64KB\n")
	require.Contains(t, findingMessages(p.Check()), "caps.architecture_md: 64 KB is over its 48 KB ceiling (architecture.md)")
	require.Equal(t, 48<<10, p.caps().ArchitectureMD, "measured at its ceiling meanwhile")
}
