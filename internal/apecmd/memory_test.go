package apecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/stretchr/testify/require"
)

// writeTeamMemory drops a team-memory.md into the project's
// development_folder and returns its path.
func writeTeamMemory(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, "development")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "team-memory.md"), []byte(body), 0o644))
}

const memoryFixture = `# Team Memory

## Process

### Story sizing — 2026-03-01

Split at 8 tasks.

### Review cadence — 2026-04-12

Review the day it lands.

## Technical

### Migration ordering — 2026-05-02

Migrate before deploy.
`

func TestMemoryIndex_HumanAndJSON(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root, memoryFixture)

	out := runCmd(t, newMemoryIndexCmd(), "--output-format", "json")
	var idx memory.Index
	require.NoError(t, json.Unmarshal([]byte(out), &idx))
	require.Equal(t, 3, idx.Count)
	require.Equal(t, "Process", idx.Entries[0].Section)
	require.Equal(t, "Technical", idx.Entries[2].Section)
	require.Equal(t, idx.TotalBytes, idx.EntryBytes+idx.HeadingBytes+idx.OtherBytes)

	human := runCmd(t, newMemoryIndexCmd())
	require.Contains(t, human, "Story sizing")
	require.Contains(t, human, "3 entries")
}

func TestMemoryShow_Verbatim(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root, memoryFixture)

	out := runCmd(t, newMemoryShowCmd(), "2")
	require.Equal(t, "### Review cadence — 2026-04-12\n\nReview the day it lands.\n\n", out)
}

func TestMemoryShow_MultipleInOrder(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root, memoryFixture)

	out := runCmd(t, newMemoryShowCmd(), "3,1")
	require.Less(t, strings.Index(out, "Migration ordering"), strings.Index(out, "Story sizing"),
		"entries come back in the order asked for")
}

// TestMemoryCheck_ExitZeroByDefault is Decision 2's core contract: the
// verdict is the state field, never the exit code, because the
// framework's "non-zero means HALT" convention would otherwise abort the
// retrospective exactly when compaction is due.
func TestMemoryCheck_ExitZeroByDefault(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root, strings.Repeat("x", 300<<10))

	out := runCmd(t, newMemoryCheckCmd(), "--output-format", "json")
	var c memory.Check
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	require.Equal(t, memory.StateOverHard, c.State)
	// runCmd asserts Execute() returned nil, i.e. no os.Exit was reached.
}

func TestMemoryCheck_States(t *testing.T) {
	cases := []struct {
		name  string
		size  int
		state memory.State
	}{
		{"ok", 1 << 10, memory.StateOK},
		{"over soft", 100 << 10, memory.StateOverSoft},
		{"over hard", 300 << 10, memory.StateOverHard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestProject(t, realProjectConfig)
			writeTeamMemory(t, root, strings.Repeat("x", tc.size))
			out := runCmd(t, newMemoryCheckCmd(), "--output-format", "json")
			var c memory.Check
			require.NoError(t, json.Unmarshal([]byte(out), &c))
			require.Equal(t, tc.state, c.State)
		})
	}
}

func TestMemoryCheck_AbsentFile(t *testing.T) {
	newTestProject(t, realProjectConfig)
	out := runCmd(t, newMemoryCheckCmd())
	require.Contains(t, out, "absent")
}

func TestMemoryCheck_HumanMessages(t *testing.T) {
	root := newTestProject(t, realProjectConfig)

	writeTeamMemory(t, root, strings.Repeat("x", 100<<10))
	out := runCmd(t, newMemoryCheckCmd())
	require.Contains(t, out, "over-soft")
	require.Contains(t, out, "compaction is due")

	writeTeamMemory(t, root, strings.Repeat("x", 300<<10))
	out = runCmd(t, newMemoryCheckCmd())
	require.Contains(t, out, "OVER THE HARD CEILING")
	require.Contains(t, out, "bug report")
}

func TestMemoryCheck_ThresholdOverrides(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root, strings.Repeat("x", 2048))

	out := runCmd(t, newMemoryCheckCmd(), "--soft", "1024", "--hard", "4096", "--output-format", "json")
	var c memory.Check
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	require.Equal(t, memory.StateOverSoft, c.State)
	require.Equal(t, int64(1024), c.SoftBudget)
	require.Equal(t, int64(4096), c.HardCeiling)
}

// TestMemoryCheckShouldFail is the --fail-at policy table: three values
// crossed with all four states.
func TestMemoryCheckShouldFail(t *testing.T) {
	states := []memory.State{
		memory.StateAbsent, memory.StateOK, memory.StateOverSoft, memory.StateOverHard,
	}
	want := map[string][]bool{
		failAtNever: {false, false, false, false},
		failAtSoft:  {false, false, true, true},
		failAtHard:  {false, false, false, true},
	}
	for policy, expected := range want {
		for i, state := range states {
			require.Equal(t, expected[i], memoryCheckShouldFail(state, policy),
				"--fail-at %s with state %s", policy, state)
		}
	}
}

func TestMemoryIndex_FencedHeadingNotCounted(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	writeTeamMemory(t, root,
		"### Real — 2026-01-01\n\nSee:\n\n```md\n### Pasted — 2026-01-02\n```\n\n### Also real — 2026-01-03\n\nx\n")

	out := runCmd(t, newMemoryIndexCmd(), "--output-format", "json")
	var idx memory.Index
	require.NoError(t, json.Unmarshal([]byte(out), &idx))
	require.Equal(t, 2, idx.Count, "a fenced heading is content")
}

func TestHumanBytes(t *testing.T) {
	require.Equal(t, "0 B", humanBytes(0))
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "1.0 KiB", humanBytes(1024))
	require.Equal(t, "421.8 KiB", humanBytes(431_950))
}

func TestThousands(t *testing.T) {
	require.Equal(t, "0", thousands(0))
	require.Equal(t, "999", thousands(999))
	require.Equal(t, "1,000", thousands(1000))
	require.Equal(t, "107,987", thousands(107_987))
	require.Equal(t, "1,234,567", thousands(1_234_567))
}

func TestMemoryCmd_Surface(t *testing.T) {
	cmd := newMemoryCmd()
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, verb := range []string{"index", "show", "check"} {
		require.True(t, names[verb], "ape memory %s missing", verb)
	}
}

// TestMemoryIndex_PastTheReadCap is the whole point of the package: the
// index works on a file the skills can no longer read at all.
func TestMemoryIndex_PastTheReadCap(t *testing.T) {
	root := newTestProject(t, realProjectConfig)
	var b bytes.Buffer
	b.WriteString("# Team Memory\n\n## Process\n\n")
	for i := 1; i <= 180; i++ {
		b.WriteString("### Lesson — 2026-01-01\n\n" + strings.Repeat("prose ", 300) + "\n\n")
	}
	require.Greater(t, b.Len(), memory.ReadCap)
	writeTeamMemory(t, root, b.String())

	out := runCmd(t, newMemoryIndexCmd(), "--output-format", "json")
	var idx memory.Index
	require.NoError(t, json.Unmarshal([]byte(out), &idx))
	require.Equal(t, 180, idx.Count)
}
