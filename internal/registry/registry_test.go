package registry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// record writes a record file with the given id.
func (f *fixture) record(familyName, name, id string) {
	f.t.Helper()
	body := fmt.Sprintf("---\nid: %s\ntitle: Record %s\nstatus: accepted\n---\n\n## Context\n\nx\n", id, id)
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir(familyName), name), []byte(body), 0o644))
}

// raw writes an arbitrary file into a family directory.
func (f *fixture) raw(familyName, name, body string) {
	f.t.Helper()
	path := filepath.Join(f.dir(familyName), name)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(f.t, os.WriteFile(path, []byte(body), 0o644))
}

// listIndex writes a list-shaped index from id→file pairs.
func (f *fixture) listIndex(pairs ...[2]string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("generated_at: '20260821120000'\nadrs:\n")
	for _, p := range pairs {
		fmt.Fprintf(&b, "  - id: %s\n    title: Record %s\n    status: accepted\n    file: %s\n", p[0], p[0], p[1])
	}
	f.raw("adrs", IndexFileName, b.String())
}

// mappingIndex writes a mapping-shaped (features) index.
func (f *fixture) mappingIndex(pairs ...[2]string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("generated_at: '20260821120000'\nfeatures:\n")
	for _, p := range pairs {
		fmt.Fprintf(&b, "  %s:\n    title: Record %s\n    status: planned\n    file: %s\n", p[0], p[0], p[1])
	}
	f.raw("features", IndexFileName, b.String())
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
			"    status: accepted\n    components: []\n", id, strings.ToLower(id), id)
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

// TestSync_NeverWritesAFileFieldToCapabilities: a sync that "helpfully"
// added one would produce an index the framework's own schema rejects.
func TestSync_NeverWritesAFileFieldToCapabilities(t *testing.T) {
	f := newFixture(t)
	f.record("capabilities", "cap-1_greeting.md", "CAP-1")
	f.record("capabilities", "cap-2_service.md", "CAP-2")
	f.capabilityIndex("CAP-1") // CAP-2 is an orphan sync must adopt

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"capabilities"}, GeneratedAt: "20260822120000"})
	require.NoError(t, err)
	require.NotEmpty(t, res.Changes)

	body, err := os.ReadFile(filepath.Join(f.dir("capabilities"), IndexFileName))
	require.NoError(t, err)
	require.NotContains(t, string(body), "file:",
		"capabilities locate their record by id + slug; a file: key is schema-invalid")
	require.Empty(t, f.verify("capabilities").Findings)
}

// TestSync_RepairsAnEmptyFileField: an entry whose file: is the empty string
// is unresolved, not resolved. The naive check stats
// filepath.Join(dir, "") — the directory — which always succeeds, so the one
// entry that most needs repointing is the one that gets skipped.
func TestSync_RepairsAnEmptyFileField(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", IndexFileName, "generated_at: '20260821120000'\nadrs:\n"+
		"  - id: ADR-0001\n    status: accepted\n    file: ''\n")

	require.Len(t, findingsOf(f.verify("adrs"), CheckFileUnresolved), 1)

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260822120000"})
	require.NoError(t, err)
	require.NotEmpty(t, res.Changes, "an empty file: is repairable and must be repaired")

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "file: adr-0001_first.md")
	require.Empty(t, f.verify("adrs").Findings)
}

// TestVerify_CleanCorpusHasNoFindings is the false-positive gate, and the
// fixture is the point: everything that is NOT a record sits in the
// directory alongside the records. The real corpus has 54 changelog
// sidecars; a verifier that counts them is one people mute.
func TestVerify_CleanCorpusHasNoFindings(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.record("adrs", "adr-0002_second.md", "ADR-0002")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"}, [2]string{"ADR-0002", "adr-0002_second.md"})

	// Not records, and none of them may produce a finding:
	f.raw("adrs", "README.md", "# ADRs\n\nHow this directory works.\n")
	f.raw("adrs", ".adr-state.yaml", "cursor: 2\n")
	f.raw("adrs", "changelog/adr-0001_first_changelog.yaml", "entries: []\n")
	f.raw("adrs", "notes.txt", "scratch\n")

	report := f.verify("adrs")
	require.Empty(t, report.Findings, "clean corpus: %+v", report.Findings)
	require.Equal(t, 2, report.Families[0].Records, "only the two .md records count")
	require.Equal(t, 2, report.Families[0].Entries)
	require.True(t, report.OK())
}

// TestVerify_RediscoversOrphanRecord is the ADR-0050 regression: a record
// on disk with status accepted, cited by 42 story files, absent from
// index.yaml across all 57 commits that touched it.
func TestVerify_RediscoversOrphanRecord(t *testing.T) {
	const total = 64
	f := newFixture(t)
	var pairs [][2]string
	for i := 1; i <= total; i++ {
		id := fmt.Sprintf("ADR-%04d", i)
		name := fmt.Sprintf("adr-%04d_decision.md", i)
		f.record("adrs", name, id)
		if i == 50 {
			continue // ADR-0050 is on disk and never listed
		}
		pairs = append(pairs, [2]string{id, name})
	}
	f.listIndex(pairs...)

	report := f.verify("adrs")
	orphans := findingsOf(report, CheckOrphanRecord)
	require.Len(t, orphans, 1, "exactly one orphan, not a sweep of false positives")
	require.Equal(t, "ADR-0050", orphans[0].ID)
	require.Equal(t, "adr-0050_decision.md", orphans[0].File)
	require.Len(t, report.Findings, 1, "zero false positives on the other 63")
	require.Equal(t, total, report.Families[0].Records)
}

func TestVerify_PhantomEntry(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0002", "adr-0002_gone.md"})

	report := f.verify("adrs")
	phantoms := findingsOf(report, CheckPhantomEntry)
	require.Len(t, phantoms, 1)
	require.Equal(t, "ADR-0002", phantoms[0].ID)
	// The same entry also fails the file: check — a phantom's file cannot
	// resolve either. Both are true and both are reported.
	require.Len(t, findingsOf(report, CheckFileUnresolved), 1)
}

func TestVerify_FileUnresolved(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_renamed.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_old-name.md"})

	report := f.verify("adrs")
	unresolved := findingsOf(report, CheckFileUnresolved)
	require.Len(t, unresolved, 1)
	require.Equal(t, "adr-0001_old-name.md", unresolved[0].File)
	require.Empty(t, findingsOf(report, CheckOrphanRecord), "the id is indexed, only the path is stale")
}

// TestVerify_FileResolvesAgainstIndexOwnDir pins the resolution base: a
// `file:` is relative to the index, not to the project root or the cwd.
func TestVerify_FileResolvesAgainstIndexOwnDir(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", "sub/adr-0002_nested.md", "---\nid: ADR-0002\n---\n\nx\n")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0002", "sub/adr-0002_nested.md"})

	report := f.verify("adrs")
	require.Empty(t, findingsOf(report, CheckFileUnresolved),
		"a subdirectory path relative to the index resolves")
	// The nested record is not a top-level record, so it reads as a
	// phantom on the set comparison — which is correct: records live flat.
	require.Len(t, findingsOf(report, CheckPhantomEntry), 1)
}

func TestVerify_DuplicateIDOnDisk(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.record("adrs", "adr-0001_copy.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"})

	dupes := findingsOf(f.verify("adrs"), CheckDuplicateID)
	require.Len(t, dupes, 1)
	require.Equal(t, "ADR-0001", dupes[0].ID)
	require.Contains(t, dupes[0].Message, "adr-0001_copy.md")
	require.Contains(t, dupes[0].Message, "adr-0001_first.md")
}

func TestVerify_DuplicateIDInIndex(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0001", "adr-0001_first.md"})

	dupes := findingsOf(f.verify("adrs"), CheckDuplicateID)
	require.Len(t, dupes, 1)
	require.Equal(t, IndexFileName, dupes[0].File)
	require.Contains(t, dupes[0].Message, "listed 2 times")
}

func TestVerify_RecordUnparsable(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_ok.md", "ADR-0001")
	f.raw("adrs", "adr-0002_broken.md", "# No frontmatter here\n\nbody\n")
	f.raw("adrs", "adr-0003_badyaml.md", "---\nid: [unterminated\n---\n\nbody\n")
	f.raw("adrs", "adr-0004_noid.md", "---\ntitle: Nameless\n---\n\nbody\n")
	f.listIndex([2]string{"ADR-0001", "adr-0001_ok.md"})

	report := f.verify("adrs")
	unparsable := findingsOf(report, CheckRecordUnparsable)
	require.Len(t, unparsable, 3)
	names := []string{unparsable[0].File, unparsable[1].File, unparsable[2].File}
	require.ElementsMatch(t,
		[]string{"adr-0002_broken.md", "adr-0003_badyaml.md", "adr-0004_noid.md"}, names)

	// An unparsable record must not ALSO be reported as an orphan: one
	// defect, one finding.
	require.Empty(t, findingsOf(report, CheckOrphanRecord))
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

// TestVerify_MappingShapedFeaturesIndex covers the outlier shape that
// forked the Python helper in two.
func TestVerify_MappingShapedFeaturesIndex(t *testing.T) {
	f := newFixture(t)
	f.record("features", "feat-1-1_login.md", "FEAT-1-1")
	f.record("features", "feat-1-2_logout.md", "FEAT-1-2")
	f.mappingIndex(
		[2]string{"FEAT-1-1", "feat-1-1_login.md"},
		[2]string{"FEAT-1-2", "feat-1-2_missing.md"},
	)

	report := f.verify("features")
	require.Empty(t, findingsOf(report, CheckOrphanRecord), "both ids are indexed")
	require.Len(t, findingsOf(report, CheckFileUnresolved), 1)
	require.Equal(t, 2, report.Families[0].Entries, "mapping entries are counted")
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

func TestVerify_FindingOrderIsStable(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"ADR-0003", "ADR-0001", "ADR-0002"} {
		f.record("adrs", strings.ToLower(id)+"_x.md", id)
	}
	f.listIndex()

	first := f.verify("adrs")
	second := f.verify("adrs")
	require.Equal(t, first.Findings, second.Findings, "output must not depend on map iteration order")
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

// TestVerify_Performance keeps the check honest at scale: 500 records is
// the scaling check, distinct from the <10ms target asserted on a
// 64-record corpus in the command test.
func TestVerify_Performance(t *testing.T) {
	f := newFixture(t)
	var pairs [][2]string
	for i := 1; i <= 500; i++ {
		id := fmt.Sprintf("ADR-%04d", i)
		name := fmt.Sprintf("adr-%04d_x.md", i)
		f.record("adrs", name, id)
		pairs = append(pairs, [2]string{id, name})
	}
	f.listIndex(pairs...)
	report := f.verify("adrs")
	require.Empty(t, report.Findings)
	require.Equal(t, 500, report.Families[0].Records)
}

// --- Sync (D3) ---

func TestSync_AddsOrphansAndDropsPhantoms(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.record("adrs", "adr-0002_second.md", "ADR-0002")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0009", "adr-0009_ghost.md"})

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260822000000"})
	require.NoError(t, err)
	require.True(t, res.Changed())

	actions := map[string]string{}
	for _, c := range res.Changes {
		actions[c.ID] = c.Action
	}
	require.Equal(t, "add", actions["ADR-0002"])
	require.Equal(t, "remove", actions["ADR-0009"])
	require.True(t, res.Families[0].Written)

	// And the registry now verifies clean.
	require.Empty(t, f.verify("adrs").Findings)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "20260822000000", "generated_at refreshed on mutation")
	require.Contains(t, string(body), "title: Record ADR-0002", "the new entry carries the record's own title")
	require.NotContains(t, string(body), "ADR-0009")
}

// TestSync_CheckWritesNothing is the dry-run contract.
func TestSync_CheckWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex()
	indexPath := filepath.Join(f.dir("adrs"), IndexFileName)
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, Check: true, GeneratedAt: "20260822000000"})
	require.NoError(t, err)
	require.True(t, res.Changed(), "--check still reports the diff")
	require.False(t, res.Families[0].Written)

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "--check must not touch the file")
}

func TestSync_CleanRegistryIsANoOp(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"})
	indexPath := filepath.Join(f.dir("adrs"), IndexFileName)
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260822000000"})
	require.NoError(t, err)
	require.False(t, res.Changed())
	require.False(t, res.Families[0].Written, "nothing to do means nothing written")

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "generated_at must NOT move when nothing changed")
}

// TestSync_RepairsRenamedRecordRatherThanRecreating: a renamed file keeps
// its entry (and its authored fields) instead of being dropped and
// re-added with only what frontmatter states.
func TestSync_RepairsRenamedRecord(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_new-name.md", "ADR-0001")
	f.raw("adrs", IndexFileName, `generated_at: '20260101000000'
adrs:
  - id: ADR-0001
    title: A Carefully Authored Title
    status: accepted
    file: adr-0001_old-name.md
`)

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)
	require.Equal(t, "fix-file", res.Changes[0].Action)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "adr-0001_new-name.md")
	require.Contains(t, string(body), "A Carefully Authored Title",
		"the authored title survives — sync repairs, it does not re-author")
}

func TestSync_CreatesIndexFromScratch(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0002_b.md", "ADR-0002")
	f.record("adrs", "adr-0001_a.md", "ADR-0001")

	_, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260822000000"})
	require.NoError(t, err)
	require.Empty(t, f.verify("adrs").Findings)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	// Name order, so a first-ever sync is reproducible.
	require.Less(t, bytes.Index(body, []byte("ADR-0001")), bytes.Index(body, []byte("ADR-0002")))
}

func TestSync_MappingShapedFeatures(t *testing.T) {
	f := newFixture(t)
	f.record("features", "feat-1-1_a.md", "FEAT-1-1")
	f.mappingIndex([2]string{"FEAT-9-9", "feat-9-9_ghost.md"})

	_, err := Sync(f.cfg, SyncOptions{Only: []string{"features"}, GeneratedAt: "20260822000000"})
	require.NoError(t, err)
	require.Empty(t, f.verify("features").Findings)

	body, err := os.ReadFile(filepath.Join(f.dir("features"), IndexFileName))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(body, &doc))
	features, ok := doc["features"].(map[string]any)
	require.True(t, ok, "features stays mapping-shaped, not converted to a list")
	require.Contains(t, features, "FEAT-1-1")
	require.NotContains(t, features, "FEAT-9-9")
}

// --- Update (D3) ---

func TestUpdate_AppliesDeltasAndRefreshesGeneratedAt(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_a.md", "ADR-0001")
	f.record("adrs", "adr-0002_b.md", "ADR-0002")
	f.listIndex([2]string{"ADR-0001", "adr-0001_a.md"},
		[2]string{"ADR-0002", "adr-0002_b.md"})

	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	res, err := Update(f.cfg, family, map[string]map[string]string{
		"ADR-0001": {"status": "superseded", "updated_at": "20260822010203"},
	}, "20260822010203")
	require.NoError(t, err)
	require.Equal(t, 1, res.Entries)
	require.Equal(t, 2, res.Fields)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "status: superseded")
	require.Contains(t, string(body), "updated_at:")
	require.Contains(t, string(body), "generated_at: '20260822010203'")
	require.Contains(t, string(body), "status: accepted", "the other entry is untouched")
}

// TestUpdate_UnknownIDFailsBeforeAnyWrite is the fail-fast contract, and
// the assertion is byte-identity: the file must be exactly as it was.
func TestUpdate_UnknownIDFailsBeforeAnyWrite(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_a.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_a.md"})
	indexPath := filepath.Join(f.dir("adrs"), IndexFileName)
	before, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]string{
		"ADR-0001": {"status": "superseded"},
		"ADR-9999": {"status": "superseded"},
	}, "20260822010203")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ADR-9999")
	require.Contains(t, err.Error(), "unknown adrs IDs")

	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, before, after,
		"an unknown id must fail before ANY write — creating entries is the authoring skill's job")
}

func TestUpdate_MissingIndexIsAnError(t *testing.T) {
	f := newFixture(t)
	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]string{"ADR-1": {"status": "x"}}, "")
	require.Error(t, err)
}

func TestUpdate_MappingShaped(t *testing.T) {
	f := newFixture(t)
	f.record("features", "feat-1-1_a.md", "FEAT-1-1")
	f.mappingIndex([2]string{"FEAT-1-1", "feat-1-1_a.md"})

	family, err := FamilyByName("features")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]string{
		"FEAT-1-1": {"status": "delivered"},
	}, "20260822010203")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(f.dir("features"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "status: delivered")
}

// TestUpdate_PreservesKeyOrderAndComments is where the Go version beats
// the script it retires: PyYAML drops comments on a round trip, yaml.v3
// keeps them.
func TestUpdate_PreservesKeyOrderAndComments(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_a.md", "ADR-0001")
	f.raw("adrs", IndexFileName, `# Generated by apex-adr-update. Do not hand-edit.
generated_at: '20260101000000'
adrs:
  # the first decision
  - id: ADR-0001
    title: First
    status: accepted
    file: adr-0001_a.md
`)

	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]string{
		"ADR-0001": {"status": "superseded"},
	}, "20260822010203")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "# Generated by apex-adr-update")
	require.Contains(t, text, "# the first decision")
	require.Less(t, strings.Index(text, "generated_at"), strings.Index(text, "adrs:"),
		"top-level key order preserved")
	require.Less(t, strings.Index(text, "title: First"), strings.Index(text, "file:"),
		"entry key order preserved")
}

// TestScalarQuoting mirrors the Python representer: a value YAML would
// re-read as a number, bool or null has to come back as a string.
func TestScalarQuoting(t *testing.T) {
	for _, value := range []string{"0001", "123", "1.5", "true", "false", "no", "yes", "null", "~", ""} {
		t.Run(value, func(t *testing.T) {
			node := scalar(value)
			out, err := yaml.Marshal(map[string]*yaml.Node{"k": node})
			require.NoError(t, err)
			var back map[string]any
			require.NoError(t, yaml.Unmarshal(out, &back))
			require.Equal(t, value, back["k"], "value must round-trip as a string: %s", string(out))
		})
	}
	require.False(t, needsQuoting("accepted"))
	require.False(t, needsQuoting("adr-0001_x.md"))
}

func TestParseUpdates(t *testing.T) {
	got, err := ParseUpdates([]byte(`{"ADR-0001": {"status": "accepted", "v": 4}}`))
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]string{
		"ADR-0001": {"status": "accepted", "v": "4"},
	}, got, "values are coerced to strings — an index field is text")

	_, err = ParseUpdates([]byte(`["not", "an", "object"]`))
	require.Error(t, err)
}

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

// TestSync_WithholdsRemovalOfAnUnreadableRecordsEntry is the data-loss gate.
//
// A record with no frontmatter claims no id, so it is absent from the
// on-disk id set and its index entry looks exactly like a phantom. Removing
// it deletes the only copy of the id, title, type, status and dates that
// would repair the record — turning a missing-header defect into an
// unrecoverable one, on a file that was sitting right there the whole time.
// This fired in the field: an ADR whose header had been lost was reported as
// `phantom_entry` + `record_unparseable`, and the remediation the framework's
// own preflight recommends is this command.
func TestSync_WithholdsRemovalOfAnUnreadableRecordsEntry(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", "adr-0002_headerless.md", "# ADR-0002 — no frontmatter block\n\ntext\n")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0002", "adr-0002_headerless.md"})

	// Precondition: the pair of findings that appear together in the field.
	report := f.verify("adrs")
	require.Len(t, findingsOf(report, CheckPhantomEntry), 1)
	require.Len(t, findingsOf(report, CheckRecordUnparsable), 1)

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260901000000"})
	require.NoError(t, err)

	for _, c := range res.Changes {
		require.NotEqual(t, "remove", c.Action, "sync must never remove an unreadable record's entry")
	}
	require.True(t, res.Withheld())
	require.Len(t, res.Blocked, 1)
	require.Equal(t, "ADR-0002", res.Blocked[0].ID)
	require.Contains(t, res.Blocked[0].Reason, "adr-0002_headerless.md")

	// The metadata that repairs the record must still be on disk.
	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), IndexFileName))
	require.NoError(t, err)
	require.Contains(t, string(body), "ADR-0002")
	require.Contains(t, string(body), "Record ADR-0002")
}

// TestSync_WithholdsEveryRemovalWhileAnyRecordIsUnreadable is the wider half
// of the rule. An entry whose file: does not name the unreadable record is
// still not a provable phantom: the unreadable record may claim that very id,
// and nothing can read it to find out.
func TestSync_WithholdsEveryRemovalWhileAnyRecordIsUnreadable(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", "adr-0002_headerless.md", "# ADR-0002 — no frontmatter block\n\ntext\n")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0009", "adr-0009_ghost.md"})

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260901000000"})
	require.NoError(t, err)

	for _, c := range res.Changes {
		require.NotEqual(t, "remove", c.Action)
	}
	require.Len(t, res.Blocked, 1)
	require.Equal(t, "ADR-0009", res.Blocked[0].ID)
	require.Contains(t, res.Blocked[0].Reason, "record_unparseable")
}

// TestSync_StillDropsAProvablePhantom guards the other direction: the
// withholding rule must not disarm the removal it was narrowing. With every
// record readable, an entry no record claims is a phantom and goes.
func TestSync_StillDropsAProvablePhantom(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"},
		[2]string{"ADR-0009", "adr-0009_ghost.md"})

	res, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260901000000"})
	require.NoError(t, err)
	require.False(t, res.Withheld())

	actions := map[string]string{}
	for _, c := range res.Changes {
		actions[c.ID] = c.Action
	}
	require.Equal(t, "remove", actions["ADR-0009"])
	require.Empty(t, f.verify("adrs").Findings)
}

// TestRestoreHeaders_RebuildsAHeaderlessRecord is the non-destructive answer
// to the phantom_entry + record_unparseable pair. Everything written must
// come off the index entry, sequence values included: Entry.Fields keeps
// only scalars, so a `tags:` list is the case that catches a rebuild reading
// the wrong side of the loader.
func TestRestoreHeaders_RebuildsAHeaderlessRecord(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.raw("adrs", "adr-0002_headerless.md", "# ADR-0002 — no header\n\nbody text\n")
	f.raw("adrs", IndexFileName, "generated_at: '20260821120000'\nadrs:\n"+
		"  - id: ADR-0001\n    title: Record ADR-0001\n    status: accepted\n    file: adr-0001_first.md\n"+
		"  - id: ADR-0002\n    slug: headerless\n    file: adr-0002_headerless.md\n"+
		"    title: The one with no header\n    type: process\n    status: accepted\n"+
		"    tags:\n      - data-architecture\n")

	res, err := RestoreHeaders(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)
	require.Equal(t, "ADR-0002", res.Changes[0].ID)

	body, err := os.ReadFile(filepath.Join(f.dir("adrs"), "adr-0002_headerless.md"))
	require.NoError(t, err)
	text := string(body)
	require.True(t, strings.HasPrefix(text, "---\n"))
	require.Contains(t, text, "id: ADR-0002")
	require.Contains(t, text, "title: The one with no header")
	require.Contains(t, text, "- data-architecture", "sequence values must survive the rebuild")
	require.NotContains(t, text, "slug:", "slug is index bookkeeping, not record frontmatter")
	require.NotContains(t, text, "file: adr-0002", "file is index bookkeeping, not record frontmatter")
	require.Contains(t, text, "# ADR-0002 — no header", "the original body is kept, untouched")
	require.Contains(t, text, "body text")

	// Both findings dissolve, and sync then has nothing to withhold.
	require.Empty(t, f.verify("adrs").Findings)
	sync, err := Sync(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260901000000"})
	require.NoError(t, err)
	require.False(t, sync.Withheld())
	require.False(t, sync.Changed())
}

// TestRestoreHeaders_RefusesARecordThatHasAHeader is the blast radius. A
// frontmatter block that fails to parse was authored by someone; overwriting
// it from an index to satisfy a checker destroys their work. Only a record
// with no block at all is this command's business.
func TestRestoreHeaders_RefusesARecordThatHasAHeader(t *testing.T) {
	f := newFixture(t)
	broken := "---\nid: ADR-0002\ntitle: [unclosed\n---\n\nbody\n"
	f.raw("adrs", "adr-0002_broken.md", broken)
	f.listIndex([2]string{"ADR-0002", "adr-0002_broken.md"})

	res, err := RestoreHeaders(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Empty(t, res.Changes)
	require.Len(t, res.Skipped, 1)
	require.Contains(t, res.Skipped[0].Reason, "repair it by hand")

	after, err := os.ReadFile(filepath.Join(f.dir("adrs"), "adr-0002_broken.md"))
	require.NoError(t, err)
	require.Equal(t, broken, string(after), "an authored header must survive byte-for-byte")
}

// TestRestoreHeaders_CheckWritesNothing is the dry-run contract.
func TestRestoreHeaders_CheckWritesNothing(t *testing.T) {
	f := newFixture(t)
	before := "# ADR-0002 — no header\n"
	f.raw("adrs", "adr-0002_headerless.md", before)
	f.listIndex([2]string{"ADR-0002", "adr-0002_headerless.md"})

	res, err := RestoreHeaders(f.cfg, SyncOptions{Only: []string{"adrs"}, Check: true})
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)

	after, err := os.ReadFile(filepath.Join(f.dir("adrs"), "adr-0002_headerless.md"))
	require.NoError(t, err)
	require.Equal(t, before, string(after))
}

// TestRestoreHeaders_SkipsWhenNothingDescribesTheRecord: with no index entry
// naming the file there is no source for an id or a title, and inventing one
// is what this whole command exists to avoid.
func TestRestoreHeaders_SkipsWhenNothingDescribesTheRecord(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0002_headerless.md", "# ADR-0002 — no header\n")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"})

	res, err := RestoreHeaders(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Empty(t, res.Changes)
	require.Len(t, res.Skipped, 1)
	require.Contains(t, res.Skipped[0].Reason, "no index entry names this file")
}

// TestRestoreHeaders_OutputDocumentIsRelativeToTheProjectRoot pins the one
// value in a restored header that is derived rather than copied.
//
// Everything else comes verbatim off the index entry; `output_document` is
// computed from the family directory, and it used to be computed by
// searching the absolute path for a segment literally named "development".
// That segment is not a constant — `development_folder` is a config
// variable — so a project that renamed it got its ABSOLUTE directory
// written into a committed record, and a project living under a
// coincidental `/…/development/…` ancestor got a path rooted at the wrong
// place. Every fixture in this file uses the default name, which is why
// neither showed up.
func TestRestoreHeaders_OutputDocumentIsRelativeToTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	// Every folder renamed away from "development".
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile), []byte(`config_schema_version: "1"
project_name: fx
extensions: [ext-adrs]
development_folder: workspace
implementation_folder: workspace/implementation
governance_folder: workspace/governance
functionality_folder: workspace/functionality
`), 0o644))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)

	family, err := FamilyByName("adrs")
	require.NoError(t, err)
	dir := family.Dir(cfg.Paths)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "adr-0002_headerless.md"),
		[]byte("# ADR-0002 — no header\n\nbody\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, IndexFileName),
		[]byte("generated_at: '20260821120000'\nadrs:\n"+
			"  - id: ADR-0002\n    title: No header\n    status: accepted\n"+
			"    file: adr-0002_headerless.md\n"), 0o644))

	res, err := RestoreHeaders(cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)

	body, err := os.ReadFile(filepath.Join(dir, "adr-0002_headerless.md"))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "output_document: workspace/governance/adrs/adr-0002_headerless.md",
		"output_document must be relative to the project root, whatever the folders are called")
	require.NotContains(t, text, root,
		"an absolute, machine-specific path must never be written into a record")
}
