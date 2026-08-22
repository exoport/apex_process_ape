package story

import (
	"errors"
	"fmt"
	"os"
	"regexp"
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
}

// OK reports whether the corpus is clean.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

// VerifyCorpus checks every story under the implementation folder.
func VerifyCorpus(cfg *apexcfg.Resolved) (*Report, error) {
	root := cfg.Paths.Implementation
	if root == "" {
		return nil, errors.New("implementation_folder is not configured")
	}
	heads, err := ScanHeads(root)
	if err != nil {
		return nil, err
	}
	known, err := loadKnownIDs(cfg)
	if err != nil {
		return nil, err
	}
	// Non-nil so a clean corpus marshals as `"findings": []`, not `null`.
	report := &Report{Findings: []Finding{}}
	for _, h := range heads {
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
		report.Findings = append(report.Findings, checkStory(h, cfg.Ext, known)...)
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

	findings = append(findings, checkTypes(h, id)...)
	findings = append(findings, checkRefs(h, id, ext, known)...)
	return findings
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
}

// File verdict codes, matching the Python's documented table.
const (
	FileOK = 0
	// FileParseFailure — YAML malformed or the --- delimiters are missing.
	FileParseFailure = 2
	// FileKeyProblem — a required key absent, an extension-conditional key
	// absent, or an optional key present with a malformed value.
	FileKeyProblem = 3
)

// VerifyFile checks one story file against the active extensions. ext is
// the caller's declared set, which for this mode comes from
// --active-extensions rather than the resolved config: the caller already
// has the list, and the check stays reproducible against a file outside
// any project.
func VerifyFile(path string, ext apexcfg.Ext) FileVerdict {
	rel := path
	h := readHead(path, rel)
	if h.Err != nil {
		return FileVerdict{
			Path: rel, Code: FileParseFailure,
			Findings: []Finding{{
				Check: CheckUnparsable, Path: rel, Message: h.Err.Error(),
			}},
		}
	}
	if len(h.Raw) == 0 {
		// `---\n---` or a block of nothing but comments. The Python calls
		// this a parse failure ("YAML block did not produce a mapping", 2)
		// rather than four missing keys (3), and the two codes route
		// differently in the calling prose, so the distinction is kept.
		return FileVerdict{
			Path: rel, Code: FileParseFailure,
			Findings: []Finding{{
				Check: CheckUnparsable, Path: rel,
				Message: "frontmatter block did not produce a mapping",
			}},
		}
	}
	verdict := FileVerdict{Path: rel, Code: FileOK}
	for _, f := range checkStory(h, ext, nil) {
		// Referential integrity needs the corpus, so the per-file gate
		// cannot assert it — exactly as the Python could not. Only the
		// key-shape classes decide this verdict.
		if f.Check == CheckUnresolvedRef {
			continue
		}
		verdict.Findings = append(verdict.Findings, f)
	}
	if len(verdict.Findings) > 0 {
		verdict.Code = FileKeyProblem
	}
	sortFindings(verdict.Findings)
	return verdict
}

// ParseActiveExtensions reads the comma-separated form the Python's
// --active-extensions flag takes ("ext-adrs,ext-features"), including the
// empty string for "standard mode, no extensions".
func ParseActiveExtensions(arg string) apexcfg.Ext {
	var ext apexcfg.Ext
	for part := range strings.SplitSeq(arg, ",") {
		switch strings.TrimSpace(part) {
		case apexcfg.ExtADRs:
			ext.ADRs = true
		case apexcfg.ExtPatterns:
			ext.Patterns = true
		case apexcfg.ExtCapabilities:
			ext.Capabilities = true
		case apexcfg.ExtFeatures:
			ext.Features = true
		}
	}
	return ext
}
