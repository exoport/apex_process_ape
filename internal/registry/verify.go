package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// Record is one record file on disk.
type Record struct {
	Name string // base name, e.g. adr-0050_something.md
	Path string
	// ID is the `id:` claimed by the frontmatter, empty when unparsable.
	ID string
	// ParseErr is non-nil when the file has no readable frontmatter.
	ParseErr error
}

// recordCap bounds how much of a record is read to find its id. Record
// frontmatter never approaches this; the cap is what keeps a verify over
// 500 records a few milliseconds rather than a full corpus read.
const recordCap = 8 << 10

// ListRecords returns the record files in dir. A record is a top-level
// `.md` file that is not a dotfile and not README.md — which is what
// keeps `changelog/` sidecars, `index.yaml`, `.feat-state.yaml` and a
// directory README from being counted as records. Those false positives
// are the difference between a verifier people trust and one they mute.
func ListRecords(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := make([]Record, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "README.md") {
			continue
		}
		rec := Record{Name: name, Path: filepath.Join(dir, name)}
		rec.ID, rec.ParseErr = readRecordID(rec.Path)
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// readRecordID reads at most recordCap bytes and returns the frontmatter
// `id:`. An unparsable record yields an error rather than a guess.
func readRecordID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, recordCap)
	n, err := f.Read(buf)
	if n == 0 && err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	fm, _, err := frontmatter.Split(buf[:n])
	if err != nil {
		return "", err
	}
	var head struct {
		ID any `yaml:"id"`
	}
	if err := yaml.Unmarshal(fm, &head); err != nil {
		return "", fmt.Errorf("frontmatter is not valid YAML: %w", err)
	}
	if head.ID == nil {
		return "", nil
	}
	return fmt.Sprintf("%v", head.ID), nil
}

// VerifyOptions selects what to verify.
type VerifyOptions struct {
	// Only limits the run to these families (plural or singular names).
	// Empty means every family.
	Only []string
	// IgnoreExt verifies a family even when its ext_* flag is false. The
	// commands do not set this; it exists so a family can be checked in
	// isolation by a test or an operator debugging a flag.
	IgnoreExt bool
}

// Verify runs the four checks over the selected families.
func Verify(cfg *apexcfg.Resolved, opts VerifyOptions) (*Report, error) {
	families, err := selectFamilies(opts.Only)
	if err != nil {
		return nil, err
	}
	report := &Report{}
	for _, family := range families {
		result, findings := verifyFamily(cfg, family, opts.IgnoreExt)
		report.Families = append(report.Families, result)
		report.Findings = append(report.Findings, findings...)
	}
	sortFindings(report.Findings)
	report.tally()
	return report, nil
}

func selectFamilies(only []string) ([]Family, error) {
	if len(only) == 0 {
		return Families, nil
	}
	seen := map[string]bool{}
	var out []Family
	for _, name := range only {
		for part := range strings.SplitSeq(name, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			family, err := FamilyByName(part)
			if err != nil {
				return nil, err
			}
			if !seen[family.Name] {
				seen[family.Name] = true
				out = append(out, family)
			}
		}
	}
	if len(out) == 0 {
		return Families, nil
	}
	return out, nil
}

// cohesive sequence; splitting them would scatter the family's state
// across helpers that all need the same three slices.
func verifyFamily(cfg *apexcfg.Resolved, family Family, ignoreExt bool) (FamilyResult, []Finding) {
	result := FamilyResult{Family: family.Name}
	dir := family.Dir(cfg.Paths)

	switch {
	case !ignoreExt && !family.Enabled(cfg.Ext):
		result.Skipped = true
		result.Reason = "ext_" + family.Name + " is false"
		return result, nil
	case dir == "":
		result.Skipped = true
		result.Reason = "the folder this family lives under is not configured"
		return result, nil
	}
	result.Dir = dir

	if !dirExists(dir) {
		result.Skipped = true
		result.Reason = "directory does not exist"
		return result, nil
	}

	records, err := ListRecords(dir)
	if err != nil {
		return result, []Finding{{
			Check:   CheckRecordUnparsable,
			Family:  family.Name,
			File:    dir,
			Message: err.Error(),
		}}
	}
	result.Records = len(records)

	idx, err := LoadIndex(dir, family)
	if err != nil {
		return result, []Finding{{
			Check:   CheckIndexMissing,
			Family:  family.Name,
			File:    filepath.Join(dir, IndexFileName),
			Message: err.Error(),
		}}
	}
	result.IndexPath = idx.Path

	findings := make([]Finding, 0, len(records))

	// Check 4 first: a record that does not parse cannot participate in
	// the set comparison, so reporting it before the id-keyed checks
	// keeps one defect from producing three findings.
	parsable := make([]Record, 0, len(records))
	for _, rec := range records {
		switch {
		case rec.ParseErr != nil:
			findings = append(findings, Finding{
				Check:   CheckRecordUnparsable,
				Family:  family.Name,
				File:    rec.Name,
				Message: rec.ParseErr.Error(),
			})
		case rec.ID == "":
			findings = append(findings, Finding{
				Check:   CheckRecordUnparsable,
				Family:  family.Name,
				File:    rec.Name,
				Message: "frontmatter has no id field",
			})
		default:
			parsable = append(parsable, rec)
		}
	}

	// The degenerate set-equality case: no index at all. One finding, not
	// one orphan per record.
	if idx.Missing {
		if len(records) > 0 {
			findings = append(findings, Finding{
				Check:   CheckIndexMissing,
				Family:  family.Name,
				File:    IndexFileName,
				Message: fmt.Sprintf("%d record(s) on disk and no index.yaml — run `ape %s sync`", len(records), family.Singular),
			})
		}
		return result, findings
	}

	entries, badEntries := idx.Entries()
	result.Entries = len(entries)
	for _, msg := range badEntries {
		findings = append(findings, Finding{
			Check:   CheckRecordUnparsable,
			Family:  family.Name,
			File:    IndexFileName,
			Message: msg,
		})
	}

	findings = append(findings, checkDuplicates(family, parsable, entries)...)
	findings = append(findings, checkSetEquality(family, parsable, entries)...)
	findings = append(findings, checkFilesResolve(family, dir, entries)...)

	return result, findings
}

// checkDuplicates is check 3: duplicate ids, in the index and on disk.
func checkDuplicates(family Family, records []Record, entries []Entry) []Finding {
	var findings []Finding
	onDisk := map[string][]string{}
	for _, rec := range records {
		onDisk[rec.ID] = append(onDisk[rec.ID], rec.Name)
	}
	for _, id := range sortedMapKeys(onDisk) {
		if files := onDisk[id]; len(files) > 1 {
			findings = append(findings, Finding{
				Check:   CheckDuplicateID,
				Family:  family.Name,
				ID:      id,
				Message: "claimed by " + strings.Join(files, ", "),
			})
		}
	}
	inIndex := map[string]int{}
	for _, e := range entries {
		inIndex[e.ID]++
	}
	for _, id := range sortedMapKeys(inIndex) {
		if inIndex[id] > 1 {
			findings = append(findings, Finding{
				Check:   CheckDuplicateID,
				Family:  family.Name,
				ID:      id,
				File:    IndexFileName,
				Message: fmt.Sprintf("listed %d times in the index", inIndex[id]),
			})
		}
	}
	return findings
}

// checkSetEquality is check 1, in both directions. The orphan direction
// is the one that matters in practice: ADR-0050 sat on disk with
// `status: accepted`, cited by 42 story files, and absent from index.yaml
// across all 57 commits that touched it.
func checkSetEquality(family Family, records []Record, entries []Entry) []Finding {
	var findings []Finding
	indexed := map[string]bool{}
	for _, e := range entries {
		indexed[e.ID] = true
	}
	for _, rec := range records {
		if !indexed[rec.ID] {
			findings = append(findings, Finding{
				Check:   CheckOrphanRecord,
				Family:  family.Name,
				ID:      rec.ID,
				File:    rec.Name,
				Message: "on disk but absent from index.yaml",
			})
		}
	}
	onDisk := map[string]bool{}
	for _, rec := range records {
		onDisk[rec.ID] = true
	}
	for _, e := range entries {
		if !onDisk[e.ID] {
			findings = append(findings, Finding{
				Check:   CheckPhantomEntry,
				Family:  family.Name,
				ID:      e.ID,
				File:    IndexFileName,
				Message: "listed in index.yaml but no record on disk claims that id",
			})
		}
	}
	return findings
}

// checkFilesResolve is check 2: every index `file:` must stat, resolved
// against the index's own directory.
func checkFilesResolve(family Family, dir string, entries []Entry) []Finding {
	var findings []Finding
	for _, e := range entries {
		if e.File == "" {
			findings = append(findings, Finding{
				Check:   CheckFileUnresolved,
				Family:  family.Name,
				ID:      e.ID,
				File:    IndexFileName,
				Message: "index entry has no file field",
			})
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(e.File))
		if _, err := os.Stat(target); err != nil {
			findings = append(findings, Finding{
				Check:   CheckFileUnresolved,
				Family:  family.Name,
				ID:      e.ID,
				File:    e.File,
				Message: "index file: does not resolve against " + dir,
			})
		}
	}
	return findings
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

// dirExists reports whether path is an existing directory. A stat error
// of any kind means "not usable", which is the only distinction the
// callers make — they skip the family either way.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
