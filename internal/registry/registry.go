// Package registry verifies and maintains the four APEX record families
// — ADRs, patterns, features and capabilities — each of which is a
// directory of Markdown records plus an `index.yaml` that lists them.
//
// The scope is deliberately four checks and no more (PLAN-25 D2): set
// equality between directory and index in both directions, every index
// `file:` resolving on disk, duplicate ids, and whether a record parses
// as frontmatter at all. No schema validation, no field drift, no tag
// comparison, no `updated_at` comparison — those are judgment, and a
// verifier that wanders into them stops being trustworthy.
//
// What it replaces did none of this: `runMarkdownDirValidate` read the
// directory, printed "OK: <file>" for every `.md`, and returned nil
// without ever calling os.Open.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
)

// Check names travel in findings and are a stable contract for anything
// that filters them.
const (
	CheckOrphanRecord     = "registry.orphan_record"
	CheckPhantomEntry     = "registry.phantom_entry"
	CheckFileUnresolved   = "registry.file_unresolved"
	CheckDuplicateID      = "registry.duplicate_id"
	CheckRecordUnparsable = "registry.record_unparseable"
	// CheckIndexMissing is the degenerate case of the set-equality check,
	// not a fifth check: one side of the comparison is absent entirely.
	// Reporting it once beats emitting an orphan per record, which would
	// bury the one fact that matters under 64 lines of noise.
	CheckIndexMissing = "registry.index_missing"
)

// Shape discriminates the two index layouts the framework uses. The
// difference is the whole reason `render-index-update.py` forked into two
// divergent copies; here it is one descriptor field.
type Shape string

const (
	// ShapeList is `adrs: [ {id: …}, … ]` — a sequence of mappings.
	ShapeList Shape = "list"
	// ShapeMapping is `features: {FEAT-1: {…}}` — id-keyed mappings.
	ShapeMapping Shape = "mapping"
)

// Family describes one record family: where its records live, what its
// index calls them, which shape that index has, whether its entries name
// their record file, and which extension flag gates it.
type Family struct {
	// Name is the plural family name and the index's top-level list key.
	Name string
	// Singular is the command noun (`adr`, `pattern`, …).
	Singular string
	Shape    Shape
	// HasFileField records whether this family's index schema defines a
	// `file:` key at all.
	//
	// Three of the four require one; capabilities do not. `capability-index-
	// schema.json` lists exactly id, slug, name, status, components,
	// related_epics, related_capabilities, created_at and updated_at — there
	// is no `file` property to populate, because a capability record's
	// filename is derived from its id and slug. Demanding one anyway emits a
	// registry.file_unresolved per capability on every project that has the
	// extension on, which is a false positive that never clears and which
	// `ape doctor` reports as registry drift forever.
	HasFileField bool
	dir          func(apexcfg.Paths) string
	ext          func(apexcfg.Ext) bool
}

// Dir returns the family's record directory, or "" when the folder it
// depends on is not configured.
func (f Family) Dir(p apexcfg.Paths) string { return f.dir(p) }

// Enabled reports whether the family's extension flag is set.
func (f Family) Enabled(e apexcfg.Ext) bool { return f.ext(e) }

// Families is the registry of registries, in a fixed order so `--all`
// output is deterministic.
var Families = []Family{
	{
		Name: "adrs", Singular: "adr", Shape: ShapeList, HasFileField: true,
		dir: func(p apexcfg.Paths) string { return p.ADRs },
		ext: func(e apexcfg.Ext) bool { return e.ADRs },
	},
	{
		Name: "patterns", Singular: "pattern", Shape: ShapeList, HasFileField: true,
		dir: func(p apexcfg.Paths) string { return p.Patterns },
		ext: func(e apexcfg.Ext) bool { return e.Patterns },
	},
	{
		// features is the mapping-shaped outlier.
		Name: "features", Singular: "feature", Shape: ShapeMapping, HasFileField: true,
		dir: func(p apexcfg.Paths) string { return p.Features },
		ext: func(e apexcfg.Ext) bool { return e.Features },
	},
	{
		// capabilities is the no-file-field outlier: its schema defines no
		// `file` property, so the record is located by id + slug.
		Name: "capabilities", Singular: "capability", Shape: ShapeList, HasFileField: false,
		dir: func(p apexcfg.Paths) string { return p.Capabilities },
		ext: func(e apexcfg.Ext) bool { return e.Capabilities },
	},
}

// FamilyByName resolves a family from either its plural name or its
// singular noun, so `ape adr verify` and `ape registry verify --family
// adrs` reach the same descriptor.
func FamilyByName(name string) (Family, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, f := range Families {
		if want == f.Name || want == f.Singular {
			return f, nil
		}
	}
	return Family{}, fmt.Errorf("unknown record family %q (known: %s)", name, strings.Join(FamilyNames(), ", "))
}

// FamilyNames lists the plural family names.
func FamilyNames() []string {
	names := make([]string, 0, len(Families))
	for _, f := range Families {
		names = append(names, f.Name)
	}
	return names
}

// Finding is one thing wrong with a registry. Findings are data, not
// errors: they travel in the payload and only `--strict` turns their
// presence into a non-zero exit.
type Finding struct {
	Check   string `json:"check"          yaml:"check"`
	Family  string `json:"family"         yaml:"family"`
	ID      string `json:"id,omitempty"   yaml:"id,omitempty"`
	File    string `json:"file,omitempty" yaml:"file,omitempty"`
	Message string `json:"message"        yaml:"message"`
}

// FamilyResult is the per-family outcome, including the counts that make
// a "clean" verdict meaningful — zero findings over zero records is not
// the same answer as zero findings over 64.
type FamilyResult struct {
	Family    string `json:"family"           yaml:"family"`
	Dir       string `json:"dir,omitempty"    yaml:"dir,omitempty"`
	Skipped   bool   `json:"skipped"          yaml:"skipped"`
	Reason    string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Records   int    `json:"records"          yaml:"records"`
	Entries   int    `json:"entries"          yaml:"entries"`
	IndexPath string `json:"index,omitempty"  yaml:"index,omitempty"`
}

// Report is the payload of `ape registry verify` and of each family's own
// `verify`.
type Report struct {
	Families []FamilyResult `json:"families" yaml:"families"`
	Findings []Finding      `json:"findings" yaml:"findings"`
	Summary  Summary        `json:"summary"  yaml:"summary"`
}

// Summary aggregates for a caller that only wants the verdict.
type Summary struct {
	Families int            `json:"families"           yaml:"families"`
	Skipped  int            `json:"skipped"            yaml:"skipped"`
	Records  int            `json:"records"            yaml:"records"`
	Findings int            `json:"findings"           yaml:"findings"`
	ByCheck  map[string]int `json:"by_check,omitempty" yaml:"by_check,omitempty"`
}

// OK reports whether the run found nothing wrong.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

func (r *Report) tally() {
	r.Summary = Summary{Families: len(r.Families), Findings: len(r.Findings)}
	for _, f := range r.Families {
		if f.Skipped {
			r.Summary.Skipped++
		}
		r.Summary.Records += f.Records
	}
	if len(r.Findings) > 0 {
		r.Summary.ByCheck = make(map[string]int, len(r.Findings))
		for _, f := range r.Findings {
			r.Summary.ByCheck[f.Check]++
		}
	}
}

// sortFindings orders findings so output is stable across runs: by
// family, then check, then id, then file.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.File < b.File
	})
}
