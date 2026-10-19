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
	sec := d[strings.Index(d, "## Code and tests that read deleted apex-v0 state"):strings.Index(d, "## Code and CI that still read removed paths")]
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
	require.Less(t, strings.Index(d, "## Code and tests that read deleted apex-v0 state"), strings.Index(d, "## ADRs"), "what can break comes first")
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
	require.Contains(t, d, "## Code and tests that read deleted apex-v0 state\n\n")
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

// R25, from mdpdf, radix, tenax and cortex on rc.6: a deleted file's base
// name in an echo, a test's message or a SQL comment reads nothing; the
// same name passed to cat, a reading call or a testify path argument
// does.
func TestLift_V0StateReadersNotByNameInProse(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		// cortex: data-layer.md, a deleted planning document.
		"development/planning/data-layer.md": "# Data layer\n",
		// mdpdf's collect-evidence-bundle.sh, its only member.
		"scripts/collect-evidence-bundle.sh": "#!/bin/sh\n" +
			"echo \"bundle: see team-memory.md for the retrospective\"\n",
		// radix's bench/hot-paths/parser.sh.
		"bench/hot-paths/parser.sh": "#!/bin/bash\nreport() {\n" +
			"    echo \"  \\`ready-for-dev\\` in \\`sprint-status.yaml\\`, none \\`done\\` —\"\n" +
			"    printf '%s\\n' \"(team-memory.md)\"\n}\n",
		// radix's cmd/radix/gen_test.go, tenax's partition_workers_flag_test.go.
		"cmd/radix/gen_test.go": "package main\n\nfunc TestDown(t *testing.T) {\n" +
			"\tif a > b {\n\t\tt.Errorf(\"down: offset %d — the exact drift class §1 used to carry (sprint-status.yaml)\", a)\n\t}\n" +
			"\tt.Fatalf(\"exitCodeUsage = %d, want 2 (team-memory.md stable exit-code map)\", a)\n}\n",
		// cortex's internal/progress/stream_test.go.
		"internal/progress/stream_test.go": "package progress\n\nfunc TestStream(t *testing.T) {\n" +
			"\trequire.Equal(t, 3, n, \"(data-layer.md's own pinned count)\")\n" +
			"\tlog.Printf(\"see data-layer.md\")\n" +
			"\terr := fmt.Errorf(\"pinned by data-layer.md\")\n\t_ = err\n}\n",
		// radix's tools/ci/rls_coverage.sql: every match in a comment.
		"tools/ci/rls_coverage.sql": "-- research/06-deep-review/fix-plan.md §6 item 2.\n" +
			"SELECT 1; -- class development/project-context.md's Guard Empty-Scan Hardening\n" +
			"/* tracked in development/implementation/sprint-status.yaml\n   and team-memory.md */\n" +
			"SELECT '-- not a comment';\n",
		// Readers all the same.
		"scripts/memory.sh":                 "#!/bin/sh\ncat team-memory.md | wc -l\necho \"$(cat sprint-status.yaml)\"\n",
		"internal/progress/layer_test.go":   "package progress\n\nfunc TestLayer(t *testing.T) {\n\trequire.FileExists(t, \"data-layer.md\")\n}\n",
		"tools/dr/read_deferred.sql":        "SELECT pg_read_file('development/deferred/DW-20261001-ab12_lost-result.md'); -- a real read\n",
		"tools/report/status_name_test.py":  "def test_status():\n    assert 'status' in open('sprint-status.yaml').read()\n",
		"internal/progress/joined_layer.go": "package progress\n\nvar layer = root + \"/data-layer.md\"\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "named in prose")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	got := map[string][]string{}
	for _, r := range res.V0StateReaders {
		got[r.File] = r.Reads
	}
	var members []string
	for _, f := range []string{"scripts/collect-evidence-bundle.sh", "bench/hot-paths/parser.sh", "cmd/radix/gen_test.go", "internal/progress/stream_test.go", "tools/ci/rls_coverage.sql"} {
		if _, ok := got[f]; ok {
			members = append(members, f)
		}
	}
	require.Empty(t, members, "named in a message or a SQL comment, read nowhere")
	require.Equal(t, []string{"development/implementation/sprint-status.yaml", "development/team-memory.md"}, got["scripts/memory.sh"])
	require.Equal(t, []string{"development/planning/data-layer.md"}, got["internal/progress/layer_test.go"], "a testify path argument")
	require.Equal(t, []string{"development/deferred/DW-20261001-ab12_lost-result.md"}, got["tools/dr/read_deferred.sql"], "a SQL string is not a comment")
	require.Equal(t, []string{"development/implementation/sprint-status.yaml"}, got["tools/report/status_name_test.py"])
	require.Equal(t, []string{"development/planning/data-layer.md"}, got["internal/progress/joined_layer.go"], "joined to a root")
}

// R31, from radix, tenax and mdpdf on rc.6c: R25's message rule covers a
// whole removed path too — in an echo, a test's message, a message string
// a call opens on one line and carries on the next — while a whole path
// cat reads stays a reader.
func TestLift_V0StateReadersNotByPathInProse(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		"development/planning/data-architecture.md": "# Data architecture\n",
		// radix's bench/hot-paths/parser.sh.
		"bench/hot-paths/parser.sh": "#!/bin/bash\nreport() {\n" +
			"    echo \"(development/planning/data-architecture.md §4 sizes the hot paths)\"\n}\n",
		// radix's internal/archtest/exit_test.go.
		"internal/archtest/exit_test.go": "package archtest\n\nfunc TestExit(t *testing.T) {\n" +
			"\tif got != 2 {\n\t\tt.Errorf(\"exitCodeUsage = %d, want 2 (the stable map in development/project-context.md)\", got)\n\t}\n}\n",
		// tenax's test/integration/remote_call_redrive_test.go.
		"test/integration/remote_call_redrive_test.go": "package integration\n\nfunc TestRedrive(t *testing.T) {\n" +
			"\tt.Fatalf(\"redrive: want the ledger row development/implementation/sprint-status.yaml pins, got %v\", err)\n}\n",
		// tenax's obs_test.go: a message carried onto the next line.
		"test/integration/obs_test.go": "package integration\n\nfunc TestObs(t *testing.T) {\n" +
			"\tt.Errorf(\n\t\t\"obs: %d spans, want %d (development/team-memory.md G12)\",\n\t\tgot, want)\n}\n",
		// mdpdf's collect-evidence-bundle.sh.
		"scripts/collect-evidence-bundle.sh": "#!/bin/sh\n" +
			"echo \"bundle: the retrospective was development/team-memory.md\"\n",
		// A reader all the same.
		"scripts/reader.sh": "#!/bin/sh\ncat development/implementation/sprint-status.yaml | wc -l\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "paths in prose")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	got := map[string][]string{}
	for _, r := range res.V0StateReaders {
		got[r.File] = r.Reads
	}
	var members []string
	for _, f := range []string{"bench/hot-paths/parser.sh", "internal/archtest/exit_test.go", "test/integration/remote_call_redrive_test.go", "test/integration/obs_test.go", "scripts/collect-evidence-bundle.sh"} {
		if _, ok := got[f]; ok {
			members = append(members, f)
		}
	}
	require.Empty(t, members, "a whole path in a message reads nothing")
	require.Equal(t, []string{"development/implementation/sprint-status.yaml"}, got["scripts/reader.sh"])
}

func TestReadsByName(t *testing.T) {
	for line, want := range map[string]bool{
		`cat NAME.md`:                                 true,
		`test -f NAME.md && echo ok`:                  true,
		`[ -f NAME.md ]`:                              true,
		`data, _ := os.ReadFile("NAME.md")`:           true,
		`p := filepath.Join(root, "dev", "NAME.md")`:  true,
		`with open('NAME.md') as f:`:                  true,
		`files = glob.glob("NAME.md")`:                true,
		`echo "$(cat NAME.md)"`:                       true,
		`var p = "NAME.md"`:                           true,
		`require.FileExists(t, "NAME.md")`:            true,
		`echo "see NAME.md"`:                          false,
		`echo "NAME.md"`:                              false,
		`printf '%s\n' 'NAME.md'`:                     false,
		`t.Errorf("want 2 (NAME.md exit-code map)")`:  false,
		`t.Logf("NAME.md")`:                           false,
		`return fmt.Errorf("pinned by NAME.md")`:      false,
		`g.log.Warn("ledgered in NAME.md")`:           false,
		`require.Equal(t, 3, n, "(NAME.md's count)")`: false,
		`msg := "update NAME.md first"`:               false,
		`for f in NAME.md; do`:                        false,
	} {
		code := "x\n" + line + "\n"
		var tok pathTok
		for _, tk := range pathTokenSpans(code) {
			if tk.s == "NAME.md" {
				tok = tk
			}
		}
		require.NotEmpty(t, tok.s, line)
		require.Equal(t, want, readsAt(code, tok, true, callFrames(code)), line)
	}
}

// R31: a whole removed path in a message's text reads nothing — on its
// line, or in a message string a call opened on an earlier line — and
// one a reading call or command takes still reads.
func TestReadsAt_WholePath(t *testing.T) {
	const p = "development/x/y.md"
	for code, want := range map[string]bool{
		"cat " + p:                                        true,
		"data, _ := os.ReadFile(\"" + p + "\")":           true,
		"var p = \"" + p + "\"":                           true,
		"msg := \"update " + p + " first\"":               false, // R32: a sentence
		"for f in " + p + "; do":                          true,
		"require.FileExists(t, \"" + p + "\")":            true,
		"echo hi > " + p:                                  true,
		"echo hi | tee " + p:                              true,
		"while read l; do :; done < " + p:                 true,
		"print(len(os.listdir('" + p + "')))":             true,
		"t.Errorf(\"%s\", os.ReadFile(\"" + p + "\"))":    true,
		"echo \"$(cat " + p + ")\"":                       true,
		"echo \"(" + p + " is the plan)\"":                false,
		"printf '%s\\n' '" + p + "'":                      false,
		"t.Errorf(\"want 2 (" + p + ")\")":                false,
		"t.Fatalf(\"" + p + "\")":                         false,
		"log.Printf(\"see " + p + "\")":                   false,
		"console.log(`see " + p + "`)":                    false,
		"require.Equal(t, 3, n, \"(" + p + "'s count)\")": false,
		// A message string a call opened on an earlier line.
		"t.Errorf(\n\t\"down: (" + p + ")\",\n\ta)":                false,
		"t.Errorf(\"want %d, got %d \"+\n\t\"(" + p + ")\", a, b)": false,
		"t.Fatalf(`first line\n" + p + " is gone\n`)":              false,
		"echo \"first line\n" + p + " second\"":                    false,
		"logger.Error(\n  fmt.Sprintf(\"%s\", \"" + p + "\"))":     false,
		"log.Printf(\n\t\"%s\",\n\tos.ReadFile(\"" + p + "\"))":    true,
		"os.ReadFile(\n\t\"" + p + "\")":                           true,
		"t.Errorf(\"x\")\nf(\n\t\"" + p + "\")":                    true,
		"echo first\n" + p:                                         true,
		"echo first \\\n  " + p:                                    false,
		"subprocess.run(\n    \"cat " + p + "\", shell=True)":      true,
		// R34 (tenax check-changelog-unreleased-reconciliation.sh:176): a
		// path in a $( … ) inside a " string is unquoted there, and git log
		// reads it.
		"w=\"$(git log \"${newest_tag}\"..HEAD --name-only -- " + p + " 2>/dev/null \\": true,
		"echo start; git log --oneline -- " + p:                                         true,
		"echo start; ls " + p:                                                           true,
		"echo \"$(date) see " + p + " now\"":                                            false,
		"echo \"n=$(grep -c x " + p + ")\"":                                             true,
	} {
		code = "x\n" + code + "\n"
		var tok pathTok
		for _, tk := range pathTokenSpans(code) {
			if tk.s == p {
				tok = tk
			}
		}
		require.NotEmpty(t, tok.s, code)
		require.Equal(t, want, readsAt(code, tok, false, callFrames(code)), code)
	}
}

// R32, from tenax on rc.6d: a path inside a sentence in a quoted string
// reads nothing, whatever call takes it — a project's own skip helper
// included — while a quoted string that is the path, one joined to a
// root, one that is itself a reading command, and an unquoted path still
// read.
func TestReadsAt_PathInQuotedProse(t *testing.T) {
	const p = "development/deferred/"
	for code, want := range map[string]bool{
		"quarantinedFlake(t, \"… (see closing condition above and " + p + ").\")": false,
		"skipUnless(t, `waiting on " + p + " to land`)":                           false,
		"loadFixture(t, \"see " + p + " for why\")":                               false,
		"skipf(t, \"drifts under load (see " + p + ")\")":                         false,
		"run(\"python3 -c 'import os; os.listdir(\\\"" + p + "\\\")'\")":          true,
		"helper('the records in " + p + " moved')":                                false,
		"x := \"a\\\" " + p + "\"":                                                false,
		"f(\"it's in " + p + "\")":                                                false,
		"os.ReadFile(\"" + p + "x.md\")":                                          true,
		"filepath.Join(root, \"" + p + "\")":                                      true,
		"quarantinedFlake(t, \"" + p + "\")":                                      true,
		"loadFixture(t, \"$ROOT/" + p + " for why\")":                             true,
		"exec.Command(\"sh\", \"-c\", \"cat " + p + "x.md\")":                     true,
		"subprocess.run(\"sort < " + p + "x.md\", shell=True)":                    true,
		"run(\"echo hi\"); cat " + p + "x.md":                                     true,
		"grep -r 'see' " + p:                                                      true,
		// The R32 residual (tenax invocation_list_52_1_test.go:131): the
		// sentence's last +-joined string is the path and ")." — no path.
		"quarantinedFlake(t, \"list: flaky (see closing condition above and \"+\n\t\t\"" + p + ").\")": false,
		"x := \"" + p + ").\"": false,
		"x := \"" + p + "\"":   true,
		"x := \"" + p + "/\"":  true,
		"x := \"" + p + ",\"":  false,
		// tenax's check-header-timestamp-monotonicity.sh: a path and a field.
		"  \"" + p + "x.yaml:updated_at\"":             true,
		"p := fmt.Sprintf(\"" + p + "%s.md\", id)":     true,
		"p = f\"" + p + "{rid}.md\"":                   true,
		"files = sorted(glob.glob(\"" + p + "*.md\"))": true,
		"os.ReadFile(\"" + p + ").\")":                 true,
		"grep -E '^" + p + "adr-[0-9]{4}_.*\\.md$' x":  true,
	} {
		code = "x\n" + code + "\n"
		var tok pathTok
		for _, tk := range pathTokenSpans(code) {
			if strings.HasPrefix(tk.s, p) {
				tok = tk
			}
		}
		require.NotEmpty(t, tok.s, code)
		require.Equal(t, want, readsAt(code, tok, false, callFrames(code)), code)
	}
}

// R32 end to end: tenax's obs_test.go passes a sentence naming the
// deferred folder to its own skip helper — not a reader; the same folder
// read or path-joined is.
func TestLift_V0StateReadersNotByPathInQuotedSentence(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		"test/integration/obs_test.go": "package integration\n\nfunc TestObs(t *testing.T) {\n" +
			"\tquarantinedFlake(t, \"obs: spans drift under load (see closing condition above and development/deferred/).\")\n}\n",
		"test/integration/invocation_list_52_1_test.go": "package integration\n\nfunc TestList(t *testing.T) {\n" +
			"\tquarantinedFlake(t,\n\t\t\"list: flaky (see closing condition above and development/deferred/).\")\n}\n",
		// tenax's real one (rc.6e): the sentence split across +-joined
		// strings, the last of them "development/deferred/).".
		"test/integration/invocation_list_52_2_test.go": "package integration\n\nfunc TestList(t *testing.T) {\n" +
			"\tquarantinedFlake(t, \"list: flaky under load \"+\n\t\t\"(see closing condition above and \"+\n\t\t\"development/deferred/).\")\n}\n",
		// R34: tenax's changelog check reads the ADR folder with git log
		// in a $( … ), and its adr- files with grep.
		"scripts/check-changelog.sh": "#!/bin/sh\n" +
			"window_adr_files=\"$(git log \"${newest_tag}\"..HEAD --name-only -- development/governance/adrs/ 2>/dev/null \\\n" +
			"    | grep -E '^development/governance/adrs/adr-[0-9]{4}_.*\\.md$' || true)\"\n",
		"tools/dr/read_test.go": "package dr\n\nfunc TestRead(t *testing.T) {\n" +
			"\t_, _ = os.ReadFile(\"development/deferred/x.md\")\n}\n",
		"tools/dr/join_test.go": "package dr\n\nfunc TestJoin(t *testing.T) {\n" +
			"\t_ = filepath.Join(root, \"development/deferred\")\n}\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "a path in a sentence")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	got := map[string][]string{}
	for _, r := range res.V0StateReaders {
		got[r.File] = r.Reads
	}
	require.NotContains(t, got, "test/integration/obs_test.go", "a sentence passed to a project's helper")
	require.NotContains(t, got, "test/integration/invocation_list_52_1_test.go", "the same, on the next line")
	require.NotContains(t, got, "test/integration/invocation_list_52_2_test.go", "the same, split across +-joined strings")
	require.Equal(t, []string{"development/governance/adrs", "development/governance/adrs/adr-"}, got["scripts/check-changelog.sh"], "R34: the folder git log reads stays")
	require.Equal(t, []string{"development/deferred/x.md"}, got["tools/dr/read_test.go"])
	require.Equal(t, []string{"development/deferred"}, got["tools/dr/join_test.go"])
}

func TestSQLCodeText(t *testing.T) {
	src := "SELECT 'a -- b', \"c/*d\" FROM t; -- x.md\n/* y.md\n z.md */ SELECT 1 /* w */;\n"
	got := codeText("q.SQL", src)
	require.Len(t, got, len(src))
	require.Equal(t, strings.Count(src, "\n"), strings.Count(got, "\n"))
	require.Equal(t, []string{"SELECT", "'a", "--", "b',", "\"c/*d\"", "FROM", "t;", "SELECT", "1", ";"}, strings.Fields(got))
}

// R26: a removed folder only at a path-segment boundary — radix's
// `mkdir -p development/planning-artifacts` names no development/planning.
func TestLift_RemovedPathAtSegmentBoundary(t *testing.T) {
	root := liftFixture(t, true)
	p := filepath.Join(root, "Makefile.common")
	require.NoError(t, os.WriteFile(p, []byte("soft-update:\n\t@mkdir -p development/planning-artifacts\n\t@ls development/planning.\n\t@ls \"development/planning\"\n"), 0o644))
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "planning artifacts")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)

	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	var lines []int
	for _, r := range res.DanglingRefs {
		if r.File == "Makefile.common" {
			require.Equal(t, "development/planning", r.Text)
			lines = append(lines, r.Line)
		}
	}
	require.Equal(t, []int{3, 4}, lines, "not development/planning-artifacts")
	for _, l := range []string{"development/planning-artifacts", "development/planning_x", "development/planning.md"} {
		require.False(t, atSegmentEnd(l, len("development/planning")), l)
	}
	for _, l := range []string{"development/planning", "development/planning/", "development/planning.", "development/planning\"", "development/planning)", "development/planning's"} {
		require.True(t, atSegmentEnd(l, len("development/planning")), l)
	}
}
