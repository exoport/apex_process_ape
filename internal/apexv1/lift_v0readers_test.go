package apexv1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D141, from atomix and radix on rc.3–rc.5: tests and guards that read
// the tracker, the ADR folder or the deferred records go red once the lift
// deletes them. They are their own class, each with what it reads; a
// mention in a comment, a document or evidence is not one.
func TestLift_V0StateReaders(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		// atomix's TestKnownUnwiredList: the tracker by its path, from the
		// test's folder.
		"tools/ci/unwired_test.go": "package ci\n\n" +
			"func TestKnownUnwiredList(t *testing.T) {\n" +
			"\tdata, err := os.ReadFile(\"../../development/implementation/sprint-status.yaml\")\n" +
			"\trequire.NoError(t, err)\n\t_ = data\n}\n",
		// atomix's reachability test: the tracker by its base name alone.
		"tools/reachability/reachability_test.go": "package reachability\n\n" +
			"func TestOwnersAreLive(t *testing.T) {\n" +
			"\tp := filepath.Join(repoRoot(t), \"development\", \"implementation\", \"sprint-status.yaml\")\n" +
			"\t_, _ = os.ReadFile(p)\n}\n",
		// radix's adr_release_identifier: the ADR folder, joined to a root.
		"scripts/adr_release_identifier.sh": "#!/bin/sh\n" +
			"ADR_DIR=\"${ROOT}/development/governance/adrs\"\n" +
			"for f in \"$ADR_DIR\"/adr-*.md; do grep -q '^release:' \"$f\" || exit 1; done\n",
		// The deferred records, by a path under their folder.
		"scripts/deferred_count.py": "import os\nprint(len(os.listdir('development/deferred/')))\n",
		// Comment only: not a reader (the lift strips the path from it).
		"internal/greet/tracker.go": "package greet\n\n" +
			"// The tracker (development/implementation/sprint-status.yaml) used to\n" +
			"// list this; see sprint-status.yaml.\n" +
			"var Tracker = 1\n",
		"scripts/notes.sh": "#!/bin/sh\n# The ADRs were in development/governance/adrs/ and the deferred\n# records in development/deferred/.\necho ok\n",
		// A base name more than one deleted file has (index.yaml), and
		// one a kept file has too (README.md): no reader.
		"internal/greet/index_test.go": "package greet\n\n" +
			"func TestIndex(t *testing.T) {\n\t_, _ = os.ReadFile(\"index.yaml\")\n\t_, _ = os.ReadFile(\"README.md\")\n}\n",
		// A document, evidence: not code.
		"docs/tracker.md":           "Read `development/implementation/sprint-status.yaml`.\n",
		"tools/dr/evidence/run.sh":  "#!/bin/sh\ncat development/implementation/sprint-status.yaml\n",
		"tools/report/statuses.txt": "development/implementation/sprint-status.yaml\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "v0 readers")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)

	want := []LiftV0Reader{
		{File: ".github/ci.yml", Reads: []string{"development/implementation/sprint-status.yaml"}},
		{File: "scripts/adr_release_identifier.sh", Reads: []string{"development/governance/adrs"}},
		{File: "scripts/deferred_count.py", Reads: []string{"development/deferred"}},
		{File: "tools/ci/unwired_test.go", Reads: []string{"development/implementation/sprint-status.yaml"}},
		{File: "tools/reachability/reachability_test.go", Reads: []string{"development/implementation/sprint-status.yaml"}},
	}
	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, want, res.V0StateReaders, "not the comment, the shared base names, the document or the evidence")

	res, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.Equal(t, want, res.V0StateReaders)
	data, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	d := string(data)
	sec := d[strings.Index(d, "## Code and tests that read deleted v0 state"):strings.Index(d, "## Code and CI that still read removed paths")]
	for _, line := range []string{
		"- `.github/ci.yml` reads `development/implementation/sprint-status.yaml`\n",
		"- `scripts/adr_release_identifier.sh` reads `development/governance/adrs`\n",
		"- `scripts/deferred_count.py` reads `development/deferred`\n",
		"- `tools/ci/unwired_test.go` reads `development/implementation/sprint-status.yaml`\n",
		"- `tools/reachability/reachability_test.go` reads `development/implementation/sprint-status.yaml`\n",
	} {
		require.Contains(t, sec, line)
	}
	require.Contains(t, sec, "(5 files;")
	require.NotContains(t, sec, "tracker.go")
	require.Less(t, strings.Index(d, "## Code and tests that read deleted v0 state"), strings.Index(d, "## ADRs"), "what can break comes first")
	code := d[strings.Index(d, "## Code and CI that still read removed paths"):strings.Index(d, "## ADRs")]
	require.NotContains(t, code, "unwired_test.go", "a reader is not repeated")

	var report liftReport
	data, err = os.ReadFile(filepath.Join(root, "_apex", "state", LiftReport))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, want, report.V0StateReaders)
	require.Contains(t, string(data), `"v0_state_readers": [`)
}

// D141: the readers are kept whole as the candidates are, past the 16 KB
// cap; past the 48 KB ceiling they go to counts, saying so, before any
// candidate is cut.
func TestLift_V0StateReadersCountedPastTheCeiling(t *testing.T) {
	extra := map[string]string{}
	for i := range 300 {
		extra[fmt.Sprintf("tools/verify/reader_with_a_long_name_%03d.sh", i)] = "#!/bin/sh\ncat development/implementation/sprint-status.yaml\n"
	}
	d, n := liftWithCandidatesAnd(t, 150, 220, extra)
	require.LessOrEqual(t, len(d), liftDispositionCeiling)
	require.Contains(t, d, "## Code and tests that read deleted v0 state\n\n")
	require.Contains(t, d, "(301 files;", "the 300 and the fixture's .github/ci.yml")
	require.Contains(t, d, "Counted, not listed, past this file's 48 KB ceiling: every file and what it reads is in `_apex/state/lift-disposition.json` (v0_state_readers)")
	require.Contains(t, d, "- `development/implementation/sprint-status.yaml` — 301 files\n")
	require.NotContains(t, d, "reader_with_a_long_name_000.sh")
	require.NotContains(t, d, "more candidate(s)", "the readers go to counts before a candidate is cut")
	require.Contains(t, d, fmt.Sprintf("| %d | ", n))

	// Under the ceiling, whole, though the file is past the cap.
	extra = map[string]string{}
	for i := range 40 {
		extra[fmt.Sprintf("tools/verify/reader%02d.sh", i)] = "#!/bin/sh\ncat development/implementation/sprint-status.yaml\n"
	}
	d, _ = liftWithCandidatesAnd(t, 78, 220, extra)
	require.Greater(t, len(d), liftDispositionCap)
	require.NotContains(t, d, "Counted, not listed")
	for i := range 40 {
		require.Contains(t, d, fmt.Sprintf("- `tools/verify/reader%02d.sh` reads `development/implementation/sprint-status.yaml`\n", i))
	}
}

func TestPathTokens(t *testing.T) {
	for src, want := range map[string][]string{
		`os.ReadFile("../../development/x.yaml")`:            {"os.ReadFile", "../../development/x.yaml"},
		`ADR_DIR="${ROOT}/development/governance/adrs"`:      {"development/governance/adrs"},
		`cat $ROOT/development/implementation/s.yaml`:        {"development/implementation/s.yaml"},
		`cat $(IMPL)/sprint-status.yaml`:                     {"sprint-status.yaml"},
		`root + "/development/deferred"`:                     {"development/deferred"},
		`ls /development/abs`:                                {"/development/abs"},
		`filepath.Join(root, "development", "status.yaml")`:  {"filepath.Join", "status.yaml"},
		`x := development`:                                   nil,
		`cat testdata/development/implementation/s.yaml # x`: {"testdata/development/implementation/s.yaml"},
	} {
		require.Equal(t, want, pathTokens(src), src)
	}
}
