package story

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/registry"
)

// Check names. Three classes and no more: extension-gated key presence,
// type shape, and referential integrity. No enum checks, no `contribution`
// vocabulary, no JSON Schema engine — those are judgment.
const (
	CheckRequiredKeyMissing   = "story.required_key_missing"
	CheckExtKeyMissing        = "story.ext_key_missing"
	CheckOptionalKeyMalformed = "story.optional_key_malformed"
	CheckTypeMismatch         = "story.type_mismatch"
	CheckUnresolvedRef        = "story.unresolved_ref"
	CheckUnparsable           = "story.unparseable"
	// CheckRequirementIDsMissing — the story declares no requirement_ids.
	//
	// REPORT-ONLY, and there is no --fix for it. The value cannot be
	// derived from frontmatter: the ids live in the story's own prose,
	// where two sections can disagree, and picking between them is the
	// judgement `apex-frontmatter-repair` exists to make. This class is
	// how that skill gets a work list — it branches on a check name like
	// its other classes instead of globbing the corpus and guessing,
	// which is the re-derivation its contract forbids.
	//
	// Every story minted before the key existed lacks it, so gating on
	// this would fail entire corpora over a field that is being
	// backfilled. It never decides an exit code.
	CheckRequirementIDsMissing = "story.requirement_ids_missing"
)

// RequiredKeys are the four keys every story carries, matching
// verify-story-frontmatter.py's _REQUIRED_KEYS exactly.
var RequiredKeys = []string{"story_id", "epic", "status", "output_document"}

// optionalKeyFormats mirrors the Python's _OPTIONAL_KEY_FORMATS: a key
// that need not be present, but must match this shape when it is.
// dev_started_at_sha is written by apex-dev-story after creation and is
// absent when the repo had no commits at dev start.
var optionalKeyFormats = map[string]*regexp.Regexp{
	"dev_started_at_sha": regexp.MustCompile(`^[0-9a-f]{7,40}$`),
}

// extRequiredPaths mirrors the Python's _EXT_REQUIRED_PATHS: the
// frontmatter path each extension makes mandatory.
var extRequiredPaths = []struct {
	Ext  string
	Path []string
	On   func(apexcfg.Ext) bool
}{
	{apexcfg.ExtADRs, []string{"governance", "adrs"}, func(e apexcfg.Ext) bool { return e.ADRs }},
	{apexcfg.ExtPatterns, []string{"governance", "patterns"}, func(e apexcfg.Ext) bool { return e.Patterns }},
	{apexcfg.ExtCapabilities, []string{"capabilities"}, func(e apexcfg.Ext) bool { return e.Capabilities }},
	{apexcfg.ExtFeatures, []string{"features"}, func(e apexcfg.Ext) bool { return e.Features }},
}

// Finding is one thing wrong with a story's frontmatter.
type Finding struct {
	Check   string `json:"check"           yaml:"check"`
	Story   string `json:"story,omitempty" yaml:"story,omitempty"`
	Path    string `json:"path"            yaml:"path"`
	Field   string `json:"field,omitempty" yaml:"field,omitempty"`
	Message string `json:"message"         yaml:"message"`
	// Candidates is set only by story.adrs_considered: the recomputed
	// candidate set the message lists, as data.
	Candidates []Candidate `json:"candidates,omitempty" yaml:"candidates,omitempty"`
}

// Report is the payload of a corpus verify.
type Report struct {
	Findings []Finding     `json:"findings" yaml:"findings"`
	Summary  ReportSummary `json:"summary"  yaml:"summary"`
}

// ReportSummary aggregates for a caller that only wants the verdict.
type ReportSummary struct {
	FilesScanned   int            `json:"files_scanned"      yaml:"files_scanned"`
	StoriesChecked int            `json:"stories_checked"    yaml:"stories_checked"`
	Findings       int            `json:"findings"           yaml:"findings"`
	ByCheck        map[string]int `json:"by_check,omitempty" yaml:"by_check,omitempty"`
	// RootMissing reports that implementation_folder does not exist, so
	// a clean verdict over zero files means "nowhere to look" rather
	// than "looked and found nothing wrong".
	RootMissing bool `json:"implementation_folder_missing,omitempty" yaml:"implementation_folder_missing,omitempty"`
}

// OK reports whether the corpus is clean.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

// AdvisoryChecks are the classes that are reported only when a caller
// asks for them, and are otherwise absent from every surface.
//
// # Why a class would ever be opt-in
//
// `story.requirement_ids_missing` fires on every story that has not been
// backfilled — 296 stories on one real project, 483 on another — and it
// stays that way for the whole migration. That is fine for the skill
// whose job is to work the list, and not fine for `ape doctor`, whose
// reds sit on the orchestrator's never-worked-around list: a conducting
// session is forbidden from routing around them. A permanently-red gate
// leaves exactly two outcomes, and both are worse than the finding:
// either the rule is suspended in practice, which teaches operators that
// doctor reds are sometimes ignorable, or every session escalates a red
// nobody can clear.
//
// So the class is invisible to the gate whose job is to be trustworthy,
// and fully visible to every consumer whose job is to work it —
// `apex-frontmatter-repair` asks, a migration entry asks,
// `ape story verify --include-advisory` asks. A work list nobody can see
// would not be a work list; a work list that jams a gate is worse.
func AdvisoryChecks() []string {
	return []string{CheckRequirementIDsMissing}
}

// IsAdvisory reports whether a check is opt-in.
func IsAdvisory(check string) bool {
	return slices.Contains(AdvisoryChecks(), check)
}

// VerifyOptions controls which classes a corpus verify reports.
type VerifyOptions struct {
	// IncludeAdvisory adds the AdvisoryChecks classes to the report.
	// Off by default: see AdvisoryChecks for why the default matters.
	IncludeAdvisory bool
}

// VerifyCorpus checks every story under the implementation folder,
// excluding the advisory classes.
func VerifyCorpus(cfg *apexcfg.Resolved) (*Report, error) {
	return VerifyCorpusWith(cfg, VerifyOptions{})
}

// VerifyCorpusWith is VerifyCorpus with the class selection made
// explicit.
func VerifyCorpusWith(cfg *apexcfg.Resolved, opts VerifyOptions) (*Report, error) {
	root := cfg.Paths.Implementation
	if root == "" {
		return nil, errors.New(apexcfg.MsgImplementationFolderUnset)
	}
	scan, err := ScanHeads(root)
	if err != nil {
		return nil, err
	}
	known, err := loadKnownIDs(cfg)
	if err != nil {
		return nil, err
	}
	// Non-nil so a clean corpus marshals as `"findings": []`, not `null`.
	report := &Report{Findings: []Finding{}}
	report.Summary.RootMissing = scan.RootMissing
	for _, h := range scan.Heads {
		report.Summary.FilesScanned++
		if h.Err != nil {
			// Not every .md under implementation_folder is a story —
			// retros, epic briefs and the deferred-work stub live there
			// too — so a file with no frontmatter is simply not a story,
			// not a finding. A file that HAS frontmatter which does not
			// parse is a finding, because something meant it to be data.
			if isNotARecord(h.Err) {
				continue
			}
			report.Findings = append(report.Findings, Finding{
				Check:   CheckUnparsable,
				Path:    h.Path,
				Message: h.Err.Error(),
			})
			continue
		}
		if !h.IsStory() {
			continue
		}
		report.Summary.StoriesChecked++
		for _, f := range checkStory(h, cfg.Ext, known) {
			if IsAdvisory(f.Check) && !opts.IncludeAdvisory {
				continue
			}
			report.Findings = append(report.Findings, f)
		}
	}
	sortFindings(report.Findings)
	report.Summary.Findings = len(report.Findings)
	if len(report.Findings) > 0 {
		report.Summary.ByCheck = map[string]int{}
		for _, f := range report.Findings {
			report.Summary.ByCheck[f.Check]++
		}
	}
	return report, nil
}

func isNotARecord(err error) bool {
	return strings.Contains(err.Error(), "no YAML frontmatter block")
}

// checkStory runs the three classes over one story.
func checkStory(h Head, ext apexcfg.Ext, known map[string]map[string]bool) []Finding {
	var findings []Finding
	id := h.StoryID()

	// Class 1a: the four unconditional keys. Present-but-empty counts as
	// absent, exactly as verify-story-frontmatter.py's `val is None or val
	// == ""` does — a `story_id: ""` is what a half-completed write leaves
	// behind, and it is precisely the failure a post-write gate exists to
	// catch. Note `0` is NOT empty: `epic: 0` is a real value.
	for _, key := range RequiredKeys {
		v, ok := h.Raw[key]
		if !ok || v == nil || fmt.Sprintf("%v", v) == "" {
			findings = append(findings, Finding{
				Check: CheckRequiredKeyMissing, Story: id, Path: h.Path, Field: key,
				Message: "required key is absent or empty",
			})
		}
	}

	// Class 1b: extension-gated keys.
	for _, req := range extRequiredPaths {
		if !req.On(ext) {
			continue
		}
		if _, ok := lookupPath(h.Raw, req.Path); !ok {
			findings = append(findings, Finding{
				Check: CheckExtKeyMissing, Story: id, Path: h.Path,
				Field:   strings.Join(req.Path, "."),
				Message: req.Ext + " is active, so this key is required",
			})
		}
	}

	// Class 1c: optional keys that must match a shape when present.
	for key, pattern := range optionalKeyFormats {
		v, ok := h.Raw[key]
		if !ok || v == nil {
			continue
		}
		if !pattern.MatchString(fmt.Sprintf("%v", v)) {
			findings = append(findings, Finding{
				Check: CheckOptionalKeyMalformed, Story: id, Path: h.Path, Field: key,
				Message: fmt.Sprintf("value %q does not match %s", fmt.Sprintf("%v", v), pattern),
			})
		}
	}

	// Class 1d: requirement_ids, report-only. Absent and present-but-empty
	// are the same answer — a story that claims no requirement covers
	// none, and the release record's coverage table reads `unknown` for
	// it either way.
	if !hasRequirementIDs(h.Raw) {
		findings = append(findings, Finding{
			Check: CheckRequirementIDsMissing, Story: id, Path: h.Path,
			Field:   "requirement_ids",
			Message: "story declares no requirement_ids — the release record's coverage table reads it as unknown (report-only; the ids live in the story's prose and backfilling them is a judgement)",
		})
	}

	findings = append(findings, checkTypes(h, id)...)
	findings = append(findings, checkRefs(h, id, ext, known)...)
	return findings
}

// hasRequirementIDs reports whether the story declares at least one
// requirement id. A present-but-empty list is treated as absent: it
// asserts nothing the coverage table can use.
func hasRequirementIDs(raw map[string]any) bool {
	v, ok := raw["requirement_ids"]
	if !ok || v == nil {
		return false
	}
	switch typed := v.(type) {
	case []any:
		for _, item := range typed {
			if item != nil && strings.TrimSpace(fmt.Sprintf("%v", item)) != "" {
				return true
			}
		}
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return true
	}
}

// checkTypes is class 2, as plain Go type switches.
//
// `features` items must be objects, not bare strings: three skills emit
// `{id, contribution}` while one governance doc specified a flat list, so
// the corpus carries both. `depends_on` items must be strings, not YAML
// floats: `- 53.1` decodes as float64, and reporting it is the whole
// point — coercing it would hide a story key that has silently become a
// number.
func checkTypes(h Head, id string) []Finding {
	var findings []Finding

	if items, ok := h.Raw["features"].([]any); ok {
		for i, item := range items {
			if _, isMap := item.(map[string]any); isMap {
				continue
			}
			findings = append(findings, Finding{
				Check: CheckTypeMismatch, Story: id, Path: h.Path,
				Field:   fmt.Sprintf("features[%d]", i),
				Message: fmt.Sprintf("expected an object, got %s (%v)", typeName(item), item),
			})
		}
	}

	if items, ok := h.Raw["depends_on"].([]any); ok {
		for i, item := range items {
			if _, isString := item.(string); isString {
				continue
			}
			findings = append(findings, Finding{
				Check: CheckTypeMismatch, Story: id, Path: h.Path,
				Field:   fmt.Sprintf("depends_on[%d]", i),
				Message: fmt.Sprintf("expected a string, got %s (%v) — quote it", typeName(item), item),
			})
		}
	}
	return findings
}

func typeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case int, int64:
		return "int"
	case float64, float32:
		return "float"
	case bool:
		return "bool"
	case map[string]any:
		return "object"
	case []any:
		return "list"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// checkRefs is class 3: every id a story cites must resolve to a record
// on disk, via the registry loader.
func checkRefs(h Head, id string, ext apexcfg.Ext, known map[string]map[string]bool) []Finding {
	var findings []Finding
	refs := []struct {
		family string
		path   []string
		on     bool
	}{
		{"adrs", []string{"governance", "adrs"}, ext.ADRs},
		{"patterns", []string{"governance", "patterns"}, ext.Patterns},
		{"features", []string{"features"}, ext.Features},
		{"capabilities", []string{"capabilities"}, ext.Capabilities},
	}
	for _, ref := range refs {
		if !ref.on {
			continue
		}
		ids, ok := known[ref.family]
		if !ok {
			// The family's records are unreadable or absent; the registry
			// verifier owns that. Not this check's business to guess.
			continue
		}
		value, present := lookupPath(h.Raw, ref.path)
		if !present {
			continue
		}
		for _, cited := range citedIDs(value) {
			if !ids[cited] {
				findings = append(findings, Finding{
					Check: CheckUnresolvedRef, Story: id, Path: h.Path,
					Field:   strings.Join(ref.path, "."),
					Message: fmt.Sprintf("%s does not resolve to a %s record on disk", cited, ref.family),
				})
			}
		}
	}
	return findings
}

// citedIDs pulls ids out of the shapes a story may use: a flat list of
// strings, a list of `{id: …}` objects, or a single scalar.
func citedIDs(value any) []string {
	var out []string
	switch v := value.(type) {
	case string:
		if v != "" {
			out = append(out, v)
		}
	case []any:
		for _, item := range v {
			switch it := item.(type) {
			case string:
				if it != "" {
					out = append(out, it)
				}
			case map[string]any:
				if id, ok := it["id"]; ok && id != nil {
					out = append(out, fmt.Sprintf("%v", id))
				}
			}
		}
	case map[string]any:
		for key := range v {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// lookupPath walks a dotted frontmatter path.
func lookupPath(raw map[string]any, path []string) (any, bool) {
	var current any = raw
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[key]
		if !ok || current == nil {
			return nil, false
		}
	}
	return current, true
}

// loadKnownIDs collects the record ids on disk per family, for class 3.
// A family whose directory is missing is omitted rather than treated as
// empty — an absent directory would otherwise make every citation look
// unresolved, which is the registry verifier's finding, not ours.
func loadKnownIDs(cfg *apexcfg.Resolved) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, family := range registry.Families {
		if !family.Enabled(cfg.Ext) {
			continue
		}
		dir := family.Dir(cfg.Paths)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		records, err := registry.ListRecords(dir)
		if err != nil {
			return nil, err
		}
		ids := make(map[string]bool, len(records))
		for _, rec := range records {
			if rec.ID != "" {
				ids[rec.ID] = true
			}
		}
		out[family.Name] = ids
	}
	return out, nil
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Field < b.Field
	})
}

// FileVerdict is the exit-code-shaped result of a single-file check.
//
// The codes are verify-story-frontmatter.py's, preserved exactly so the
// calling prose in apex-create-story, apex-story-amend and
// apex-lift-project changes only the command name. This mode is a GATE —
// a non-zero exit is the signal — unlike the corpus mode, which is a
// report. None of its three callers is a C1-protected review skill, so a
// non-zero exit there is safe.
type FileVerdict struct {
	Path     string    `json:"path"     yaml:"path"`
	Code     int       `json:"code"     yaml:"code"`
	Findings []Finding `json:"findings" yaml:"findings"`
	// Flagged carries findings that are REPORTED WITHOUT GATING — today,
	// a cited ADR whose status is not `accepted`. They are kept out of
	// Findings so the code stays 0, and emitted separately so they are
	// not lost: a note nobody can see is a note that does not exist.
	Flagged []Finding `json:"flagged,omitempty" yaml:"flagged,omitempty"`
	// SkippedChecks names classes that could not run and why. A skipped
	// class is never a pass — a caller must be able to tell "asserted and
	// clean" from "could not assert".
	SkippedChecks []SkippedCheck `json:"skipped_checks,omitempty" yaml:"skipped_checks,omitempty"`
}

// SkippedCheck is one class that did not run.
type SkippedCheck struct {
	Check  string `json:"check"  yaml:"check"`
	Reason string `json:"reason" yaml:"reason"`
}
