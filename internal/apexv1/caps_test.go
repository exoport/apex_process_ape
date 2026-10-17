package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
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
	require.Equal(t, Caps{ProjectMD: 16 << 10, AgentsMD: 16 << 10, ChangeMD: 16 << 10, DesignMD: 16 << 10, Spec: 24 << 10, EvidenceLog: 1 << 20}, DefaultCaps())

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
	write(t, p, "AGENTS.md", "# AGENTS.md\n"+strings.Repeat("- x\n", 1000))
	write(t, p, "development/project.md", projectMD+strings.Repeat("- note\n", 500))
	require.Empty(t, p.Check(), "a thousand short lines under the byte caps pass")

	write(t, p, "AGENTS.md", strings.Repeat("x", 16<<10+1))
	got := ClassifyFindings(p.Check())
	require.Len(t, got, 1)
	require.Equal(t, "agents.too-big", got[0].Code)
	require.Contains(t, got[0].Message, "AGENTS.md is 16.0 KB, over the 16 KB cap (caps.agents_md)")

	write(t, p, "_apex/config.yaml", "caps:\n  agents_md: 20KB\n")
	require.Empty(t, p.Check(), "a configured cap is the one measured")
}

// A value above its ceiling is a project check finding, and the verbs that
// enforce a cap refuse until a person fixes it.
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
	require.Equal(t, CodeCapsOverCeiling, failCode(t, err))
	require.DirExists(t, p.ChangeDir("add-wave"))
}

// change.md's cap (16 KB by default) holds at check and start, and at
// archive and abandon too (D69).
func TestCaps_ChangeMDAtArchiveAndAbandon(t *testing.T) {
	t.Run("archive", func(t *testing.T) {
		p := reviewedWaveFrom(t, baseFiles())
		path := "development/changes/add-wave/change.md"
		doc := read(t, p, path)
		// ## Notes is append-only after start: it grew past the cap.
		write(t, p, path, doc+strings.Repeat("a note that grew after the plan froze\n", 450))
		_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.Equal(t, "change.too-big", failCode(t, err))
		require.ErrorContains(t, err, "over the 16 KB cap (caps.change_md)")
		require.DirExists(t, p.ChangeDir("add-wave"), "nothing was archived")

		write(t, p, path, doc+"a short note\n")
		_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.NoError(t, err, "trimmed under the cap, it archives")
	})
	t.Run("abandon", func(t *testing.T) {
		p := fixture(t, baseFiles())
		big := waveChange("add-wave") + strings.Repeat("n", 16<<10)
		addChange(t, p, "add-wave", big)
		commitAll(t, p, "plan: add-wave")
		require.Contains(t, findingMessages(p.CheckChange("add-wave")), "over the 16 KB cap (caps.change_md) — split this change")
		_, err := p.Abandon(ctx(), "add-wave", "too big")
		require.Equal(t, "change.too-big", failCode(t, err))
		require.DirExists(t, p.ChangeDir("add-wave"))

		write(t, p, "development/changes/add-wave/change.md", waveChange("add-wave"))
		commitAll(t, p, "trim")
		_, err = p.Abandon(ctx(), "add-wave", "too big")
		require.NoError(t, err)
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
