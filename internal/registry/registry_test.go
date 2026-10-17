package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

// fixture builds a project with all four families enabled and returns the
// resolved config plus the project root.
type fixture struct {
	t    *testing.T
	root string
	cfg  *apexcfg.Resolved
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	cfgBody := `config_schema_version: "1"
project_name: fx
extensions: [ext-adrs, ext-patterns, ext-features, ext-capabilities]
development_folder: development
implementation_folder: development/implementation
governance_folder: development/governance
functionality_folder: development/functionality
`
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile), []byte(cfgBody), 0o644,
	))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	f := &fixture{t: t, root: root, cfg: cfg}
	for _, family := range Families {
		require.NoError(t, os.MkdirAll(family.Dir(cfg.Paths), 0o755))
	}
	return f
}

func (f *fixture) dir(familyName string) string {
	f.t.Helper()
	family, err := FamilyByName(familyName)
	require.NoError(f.t, err)
	return family.Dir(f.cfg.Paths)
}

// recordFrontmatter is a superset of what any family's index schema
// requires, so an entry sync builds from it is complete for every family —
// and a test that wants an incomplete one writes it by hand.
const recordFrontmatter = "type: technology\ntags: [x]\nversion: v1\ncategory: general\n" +
	"capability: CAP-1\ncomponents: [x]\ncreated_at: \"20260101000000\"\nupdated_at: \"20260101000000\"\n"

// record writes a record file with the given id.
func (f *fixture) record(familyName, name, id string) {
	f.t.Helper()
	body := fmt.Sprintf("---\nid: %s\ntitle: Record %s\nname: Record %s\nstatus: accepted\n%s---\n\n## Context\n\nx\n",
		id, id, id, recordFrontmatter)
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir(familyName), name), []byte(body), 0o644))
}

// raw writes an arbitrary file into a family directory.
func (f *fixture) raw(familyName, name, body string) {
	f.t.Helper()
	path := filepath.Join(f.dir(familyName), name)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(f.t, os.WriteFile(path, []byte(body), 0o644))
}

func (f *fixture) verify(only ...string) *Report {
	f.t.Helper()
	report, err := Verify(f.cfg, VerifyOptions{Only: only})
	require.NoError(f.t, err)
	return report
}

// findingsOf returns the findings of one check, for concise assertions.
func findingsOf(report *Report, check string) []Finding {
	var out []Finding
	for _, f := range report.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// capabilityIndex writes the capability index in its REAL shape: entries
// with no `file:` key, because capability-index-schema.json defines none.
// Every capability test below uses it, so a verifier or a sync that starts
// demanding or writing a file: fails here rather than on someone's project.
func (f *fixture) capabilityIndex(ids ...string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("generated_at: '20260821120000'\ncapabilities:\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "  - id: %s\n    slug: cap-%s\n    name: Capability %s\n"+
			"    status: accepted\n    components: []\n    created_at: '20260101000000'\n"+
			"    updated_at: '20260101000000'\n", id, strings.ToLower(id), id)
	}
	f.raw("capabilities", IndexFileName, b.String())
}

// TestVerify_CapabilitiesNeedNoFileField is the false-positive that shipped:
// capabilities are the one family whose index schema has no `file` property,
// so demanding one reports a finding per entry on every project with the
// extension on — and `ape doctor` then shows registry drift that no sync can
// ever clear.
func TestVerify_CapabilitiesNeedNoFileField(t *testing.T) {
	f := newFixture(t)
	f.record("capabilities", "cap-1_greeting.md", "CAP-1")
	f.record("capabilities", "cap-2_service.md", "CAP-2")
	f.capabilityIndex("CAP-1", "CAP-2")

	report := f.verify("capabilities")
	require.Empty(t, report.Findings, "a schema-conformant capability index is clean: %+v", report.Findings)

	// The other three families DO require one, and still say so.
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", IndexFileName,
		"generated_at: '20260821120000'\nadrs:\n  - id: ADR-0001\n    status: accepted\n")
	require.Len(t, findingsOf(f.verify("adrs"), CheckFileUnresolved), 1,
		"an ADR entry without file: is still a finding")
}

// TestVerify_IndexMissingIsOneFinding is the degenerate set-equality
// case. 64 orphan findings would be technically correct and useless.
func TestVerify_IndexMissingIsOneFinding(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 12; i++ {
		f.record("adrs", fmt.Sprintf("adr-%04d_x.md", i), fmt.Sprintf("ADR-%04d", i))
	}

	report := f.verify("adrs")
	require.Len(t, report.Findings, 1, "one finding, not one per record")
	require.Equal(t, CheckIndexMissing, report.Findings[0].Check)
	require.Contains(t, report.Findings[0].Message, "12 record(s)")
	require.Contains(t, report.Findings[0].Message, "ape adr sync")
	require.Empty(t, findingsOf(report, CheckOrphanRecord))
}

func TestVerify_IndexMissingAndNoRecordsIsClean(t *testing.T) {
	f := newFixture(t)
	report := f.verify("adrs")
	require.Empty(t, report.Findings, "an empty family is not a problem")
}

func TestVerify_ExtDisabledFamilyIsSkippedNotFailed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile),
		[]byte("project_name: x\nextensions: []\ngovernance_folder: development/governance\n"), 0o644))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	adrDir := filepath.Join(root, "development", "governance", "adrs")
	require.NoError(t, os.MkdirAll(adrDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(adrDir, "adr-0001_x.md"),
		[]byte("---\nid: ADR-0001\n---\n\nx\n"), 0o644))

	report, err := Verify(cfg, VerifyOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Empty(t, report.Findings, "a disabled family is skipped, not failed")
	require.True(t, report.Families[0].Skipped)
	require.Contains(t, report.Families[0].Reason, "ext_adrs")
	require.Equal(t, 1, report.Summary.Skipped)
}

func TestVerify_AllFourFamilies(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_a.md", "ADR-0001")
	f.record("patterns", "pat-0001_p.md", "PAT-0001")
	f.record("features", "feat-1-1_f.md", "FEAT-1-1")
	f.record("capabilities", "cap-1_c.md", "CAP-1")

	report := f.verify()
	require.Len(t, report.Families, 4)
	require.Equal(t, []string{"adrs", "patterns", "features", "capabilities"}, FamilyNames())
	// Every family has a record and no index: four index_missing findings.
	require.Len(t, findingsOf(report, CheckIndexMissing), 4)
	require.Equal(t, 4, report.Summary.Records)
	require.Equal(t, map[string]int{CheckIndexMissing: 4}, report.Summary.ByCheck)
}

func TestFamilyByName(t *testing.T) {
	for _, name := range []string{"adr", "adrs", "ADRS", " patterns "} {
		_, err := FamilyByName(name)
		require.NoError(t, err, name)
	}
	_, err := FamilyByName("nonsense")
	require.Error(t, err)
	require.Contains(t, err.Error(), "capabilities", "the message lists the known families")
}

func TestVerify_UnknownFamilyIsAnError(t *testing.T) {
	f := newFixture(t)
	_, err := Verify(f.cfg, VerifyOptions{Only: []string{"stories"}})
	require.Error(t, err)
}

// --- Sync (D3) ---

// --- Update (D3) ---

func TestLoadIndex_NotAMapping(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", IndexFileName, "- just\n- a\n- list\n")
	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	_, err = LoadIndex(f.dir("adrs"), family)
	require.ErrorIs(t, err, ErrIndexNotMapping)
}

func TestLoadIndex_EmptyFileIsAnEmptyIndex(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", IndexFileName, "")
	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	idx, err := LoadIndex(f.dir("adrs"), family)
	require.NoError(t, err)
	require.False(t, idx.Missing)
	entries, bad := idx.Entries()
	require.Empty(t, entries)
	require.Empty(t, bad)
}

func TestEntries_ReportsMalformedEntries(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", IndexFileName, `adrs:
  - id: ADR-0001
    file: a.md
  - "just a string"
  - title: no id here
`)
	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	idx, err := LoadIndex(f.dir("adrs"), family)
	require.NoError(t, err)
	entries, bad := idx.Entries()
	require.Len(t, entries, 1)
	require.Len(t, bad, 2, "a non-mapping entry and a mapping with no id are both reported")
}

func TestListRecords_MissingDirIsAnError(t *testing.T) {
	_, err := ListRecords(filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
}
