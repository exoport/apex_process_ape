package apecmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// allExtensionsConfig enables all four families so the registry commands
// have something to look at.
const allExtensionsConfig = `config_schema_version: "1"
project_name: fx
extensions: [ext-adrs, ext-patterns, ext-features, ext-capabilities]
development_folder: development
implementation_folder: development/implementation
governance_folder: development/governance
functionality_folder: development/functionality
`

// seedADRCorpus writes n ADR records and an index listing all but the
// ids in skip — the ADR-0050 shape.
func seedADRCorpus(t *testing.T, root string, n int, skip ...int) string {
	t.Helper()
	dir := filepath.Join(root, "development", "governance", "adrs")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "changelog"), 0o755))
	skipped := map[int]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var index strings.Builder
	index.WriteString("generated_at: '20260821120000'\nadrs:\n")
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("ADR-%04d", i)
		name := fmt.Sprintf("adr-%04d_decision.md", i)
		body := fmt.Sprintf("---\nid: %s\ntitle: Decision %s\nstatus: accepted\n---\n\n## Context\n\nx\n", id, id)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
		// Every record gets a changelog sidecar, as the real corpus does:
		// 54 of them, none of which may count as a record.
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "changelog", fmt.Sprintf("adr-%04d_decision_changelog.yaml", i)),
			[]byte("entries: []\n"), 0o644,
		))
		if skipped[i] {
			continue
		}
		fmt.Fprintf(&index, "  - id: %s\n    title: Decision %s\n    status: accepted\n    file: %s\n", id, id, name)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index.String()), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# ADRs\n"), 0o644))
	return dir
}

func runCmd(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())
	return buf.String()
}

// TestRegistryVerify_RediscoversADR0050 is the D2 acceptance criterion:
// the orphan is found, the other 63 produce nothing, and it is fast.
func TestRegistryVerify_RediscoversADR0050(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 64, 50)

	start := time.Now()
	out := runCmd(t, newRegistryVerifyCmd(), "--family", "adrs", "--output-format", "json")
	elapsed := time.Since(start)

	var report registry.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Findings, 1, "one orphan, zero false positives on the other 63")
	require.Equal(t, registry.CheckOrphanRecord, report.Findings[0].Check)
	require.Equal(t, "ADR-0050", report.Findings[0].ID)
	require.Equal(t, 64, report.Families[0].Records,
		"64 records — the 64 changelog sidecars and the README are not records")

	// The <10ms target is on this 64-record corpus. Generous ceiling so
	// the assertion tracks an algorithmic regression, not machine noise.
	require.Less(t, elapsed, 500*time.Millisecond, "verify took %s", elapsed)
}

// TestRegistryVerify_ExitZeroWithFindings is the contract that makes
// these commands safe to call from a review path.
func TestRegistryVerify_ExitZeroWithFindings(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 3, 2)

	// Execute() returning nil is the in-process equivalent of exit 0 —
	// runCmd asserts NoError, and no os.Exit is reached without --strict.
	out := runCmd(t, newRegistryVerifyCmd(), "--family", "adrs")
	require.Contains(t, out, "1 finding(s)")
	require.Contains(t, out, "registry.orphan_record")
}

func TestRegistryVerify_HumanCleanSaysSo(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 3)

	out := runCmd(t, newRegistryVerifyCmd(), "--family", "adrs")
	require.Contains(t, out, "no findings")
	require.Contains(t, out, "3 record(s), 3 entries")
}

func TestRegistryVerify_AllFamilies(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 2)

	out := runCmd(t, newRegistryVerifyCmd(), "--all", "--output-format", "json")
	var report registry.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Families, 4)
	require.Equal(t, 3, report.Summary.Skipped, "three families have no directory yet")
}

// TestFamilyVerify_PerFamilyNoun covers `ape adr verify` reaching the
// same code as the fan-out.
func TestFamilyVerify_PerFamilyNoun(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 4, 3)

	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)
	out := runCmd(t, newFamilyVerifyCmd(family), "--output-format", "json")

	var report registry.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Findings, 1)
	require.Equal(t, "ADR-0003", report.Findings[0].ID)
}

// TestADRCmd_PluralAliasAndVerbs asserts the surface D0 promises: the
// plural alias resolves, and `validate` survives as a hidden pointer.
func TestADRCmd_PluralAliasAndVerbs(t *testing.T) {
	cmd := newADRCmd()
	require.Contains(t, cmd.Aliases, "adrs", "the plural alias is registered")

	byName := map[string]*cobra.Command{}
	for _, sub := range cmd.Commands() {
		byName[sub.Name()] = sub
	}
	for _, verb := range []string{"list", "new", "verify", "sync", "update", "validate"} {
		require.Contains(t, byName, verb, "ape adr %s must exist", verb)
	}
	require.True(t, byName["validate"].Hidden, "validate is a hidden pointer, not a documented verb")
	require.False(t, byName["verify"].Hidden)
	require.Contains(t, byName["validate"].Long, "Deprecated")
}

func TestPatternCmd_SurfaceAndNoStub(t *testing.T) {
	cmd := newPatternCmd()
	require.Contains(t, cmd.Aliases, "patterns")
	byName := map[string]*cobra.Command{}
	for _, sub := range cmd.Commands() {
		byName[sub.Name()] = sub
	}
	require.Contains(t, byName, "sync")
	require.False(t, byName["sync"].Hidden, "the stub became the real verb")
	out := byName["sync"].Long
	require.NotContains(t, out, "not yet implemented")
}

func TestFeatureAndCapabilityCmds(t *testing.T) {
	for _, tc := range []struct {
		cmd   *cobra.Command
		alias string
	}{
		{newFeatureCmd(), "features"},
		{newCapabilityCmd(), "capabilities"},
	} {
		t.Run(tc.cmd.Name(), func(t *testing.T) {
			require.Contains(t, tc.cmd.Aliases, tc.alias)
			byName := map[string]bool{}
			for _, sub := range tc.cmd.Commands() {
				byName[sub.Name()] = true
			}
			for _, verb := range []string{"list", "verify", "sync", "update"} {
				require.True(t, byName[verb], "%s %s missing", tc.cmd.Name(), verb)
			}
		})
	}
}

// TestSyncCmd_LegacySpellingIsHiddenAndDelegates keeps the verb-first
// spelling working for one release while the noun-first form is canonical.
func TestSyncCmd_LegacySpellingIsHiddenAndDelegates(t *testing.T) {
	cmd := newSyncCmd()
	require.True(t, cmd.Hidden, "`ape sync` is retired to a hidden pointer")
	for _, sub := range cmd.Commands() {
		require.True(t, sub.Hidden)
		require.Contains(t, sub.Short, "Deprecated")
	}

	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 2, 2)
	out := runCmd(t, newSyncCmd(), "adrs", "--check")
	require.Contains(t, out, "deprecated")
	require.Contains(t, out, "would apply")
	require.Contains(t, out, "ADR-0002")
}

func TestFamilySync_RepairsAndVerifiesClean(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	dir := seedADRCorpus(t, root, 5, 4)
	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)

	out := runCmd(t, newFamilySyncCmd(family))
	require.Contains(t, out, "applied 1 change(s)")
	require.Contains(t, out, "add")

	verified := runCmd(t, newFamilyVerifyCmd(family), "--output-format", "json")
	var report registry.Report
	require.NoError(t, json.Unmarshal([]byte(verified), &report))
	require.Empty(t, report.Findings, "sync is the repair for verify's findings")

	// And the index is still readable YAML with the right entry count.
	body, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	require.Equal(t, 5, strings.Count(string(body), "  - id: "))
}

func TestFamilySync_CheckWritesNothing(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	dir := seedADRCorpus(t, root, 3, 3)
	indexPath := filepath.Join(dir, "index.yaml")
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)
	out := runCmd(t, newFamilySyncCmd(family), "--check")
	require.Contains(t, out, "would apply")

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestFamilySync_AlreadyInSync(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 3)
	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)
	out := runCmd(t, newFamilySyncCmd(family))
	require.Contains(t, out, "already in sync")
}

// TestFamilyUpdate_StdinAndOutputLine mirrors the message shape
// render-index-update.py printed, so the calling prose reads the same.
func TestFamilyUpdate_StdinAndOutputLine(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	dir := seedADRCorpus(t, root, 2)
	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)

	cmd := newFamilyUpdateCmd(family)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetIn(strings.NewReader(`{"ADR-0001": {"status": "superseded"}}`))
	cmd.SetArgs([]string{"--updates", "-", "--generated-at", "20260822010203"})
	require.NoError(t, cmd.Execute())

	require.Contains(t, buf.String(), "1 entry, 1 field delta(s)")
	require.Contains(t, buf.String(), "round-trip parse verified")

	body, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(body), "status: superseded")
	require.Contains(t, string(body), "generated_at: '20260822010203'")
}

func TestFamilyUpdate_FromFile(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	dir := seedADRCorpus(t, root, 2)
	updates := filepath.Join(t.TempDir(), "u.json")
	require.NoError(t, os.WriteFile(updates,
		[]byte(`{"ADR-0002": {"status": "rejected", "v": 4}}`), 0o644))

	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)
	out := runCmd(t, newFamilyUpdateCmd(family), "--updates", updates)
	require.Contains(t, out, "2 field delta(s)")

	body, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(body), "status: rejected")
	require.Contains(t, string(body), "v: '4'", "a numeric-looking field stays a string")
}

// TestFamilyUpdate_UnknownIDLeavesFileByteIdentical is the fail-fast
// assertion the plan calls for.
func TestFamilyUpdate_UnknownIDLeavesFileByteIdentical(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	dir := seedADRCorpus(t, root, 2)
	indexPath := filepath.Join(dir, "index.yaml")
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)
	cmd := newFamilyUpdateCmd(family)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(`{"ADR-9999": {"status": "x"}}`))
	cmd.SetArgs([]string{"--updates", "-"})
	err = cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "ADR-9999")

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestFamilyList(t *testing.T) {
	root := newTestProject(t, allExtensionsConfig)
	seedADRCorpus(t, root, 2)
	family, err := registry.FamilyByName("adrs")
	require.NoError(t, err)

	out := runCmd(t, newFamilyListCmd(family), "--output-format", "json")
	var rows []map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 2)
	require.Equal(t, "ADR-0001", rows[0]["id"])
	require.Equal(t, "accepted", rows[0]["status"])
}

func TestReadUpdatesInput(t *testing.T) {
	data, err := readUpdatesInput(strings.NewReader("{}"), "-")
	require.NoError(t, err)
	require.Equal(t, "{}", string(data))

	path := filepath.Join(t.TempDir(), "u.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"a":{}}`), 0o644))
	data, err = readUpdatesInput(strings.NewReader(""), path)
	require.NoError(t, err)
	require.JSONEq(t, `{"a":{}}`, string(data))

	_, err = readUpdatesInput(strings.NewReader(""), filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestPluralY(t *testing.T) {
	require.Equal(t, "y", pluralY(1))
	require.Equal(t, "ies", pluralY(0))
	require.Equal(t, "ies", pluralY(2))
}
