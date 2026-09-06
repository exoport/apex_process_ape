package story

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"github.com/exoport/apex_process_ape/internal/registry"
	"gopkg.in/yaml.v3"
)

// The two governance gates, both on the story-side verifier.
//
// Neither belongs on `ape registry verify`: that command is declared as
// one skill's work list over registry-index-versus-record drift, and it
// takes no story input at all.
//
// # 9.12b — the adrs_considered recomputation
//
// A story's `governance_pass` declares `adrs_considered` (the tag-match
// candidate count) and `adrs_applicable` (how many of them the digest
// pass judged applicable). Only the FIRST is mechanical, and only the
// first is recomputed here.
//
// `adrs_applicable` is the output of the digest algorithm's step 2,
// which the framework declares to be the producing agent's own inline
// judgement. It is compared as a BOUND and never as an equality: asking
// a deterministic CLI to reproduce a judgement would fire a finding on
// every legitimate keep-or-drop the agent made. So there are exactly two
// findings — `adrs_applicable > adrs_considered`, which is impossible
// rather than debatable, and `adrs_applicable == 0` against a non-zero
// candidate set, which is the self-certifying skip the check exists to
// catch.
//
// # 13.3b — ids resolve at HEAD
//
// An id cited in `governance.adrs` that resolves to no ADR is a gate
// failure. An id that resolves to an ADR whose status is not `accepted`
// is REPORTED AND EXITS 0 — a story may legitimately cite a proposed
// ADR, a reader needs to know, and stopping the run over it would turn a
// note into a halt.

// ADR is the subset of an ADR record these gates read.
type ADR struct {
	ID     string
	Status string
	Type   string
	Tags   []string
	Path   string
}

// Accepted reports whether this ADR is in force.
func (a ADR) Accepted() bool { return strings.EqualFold(a.Status, "accepted") }

// EligibleForGCC reports whether this ADR can produce a compliance
// criterion at all: accepted, and not a pattern record filed among the
// ADRs.
//
// This is the filter the tag-match candidate set uses, and the choice is
// deliberate. `governance-integration.md` applies exactly this filter at
// emission ("Only include ADRs with status: accepted and type != pattern"),
// so an ADR that can never be applicable should not be "considered" — and
// counting one would make the `adrs_applicable == 0` finding fire against
// a candidate the agent was right to drop.
func (a ADR) EligibleForGCC() bool {
	return a.Accepted() && !strings.EqualFold(a.Type, "pattern")
}

// adrCap bounds the read per record. ADR frontmatter never approaches
// this; the cap is what keeps a gate over 500 records a few
// milliseconds rather than a full corpus read.
const adrCap = 8 << 10

// LoadADRs reads the ADR corpus's frontmatter. A missing directory is
// not an error — it means there is no corpus to check against, and the
// caller skips rather than reporting every citation unresolved.
func LoadADRs(dir string) ([]ADR, bool) {
	if dir == "" {
		return nil, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	records, err := registry.ListRecords(dir)
	if err != nil {
		return nil, false
	}
	out := make([]ADR, 0, len(records))
	for _, rec := range records {
		adr, ok := readADR(rec.Path)
		if !ok {
			continue
		}
		out = append(out, adr)
	}
	return out, true
}

func readADR(path string) (ADR, bool) {
	f, err := os.Open(path)
	if err != nil {
		return ADR{}, false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, adrCap)
	n, _ := f.Read(buf)
	fm, _, splitErr := frontmatter.Split(buf[:n])
	if splitErr != nil {
		return ADR{}, false
	}
	var head struct {
		ID     string   `yaml:"id"`
		Status string   `yaml:"status"`
		Type   string   `yaml:"type"`
		Tags   []string `yaml:"tags"`
	}
	if err := yaml.Unmarshal(fm, &head); err != nil {
		return ADR{}, false
	}
	if head.ID == "" {
		return ADR{}, false
	}
	return ADR{
		ID:     head.ID,
		Status: head.Status,
		Type:   head.Type,
		Tags:   head.Tags,
		Path:   filepath.Base(path),
	}, true
}

// TagMatch returns the ADRs whose tags appear in the story body — the
// digest algorithm's step-1 candidate set, recomputed.
//
// Matching is case-insensitive on word boundaries against the body text.
// It is deliberately INCLUSIVE, which is the algorithm's own instruction:
// "extra entries cost little, missed entries are expensive". That skews
// the two findings in the safe direction — a larger candidate set makes
// `adrs_applicable > adrs_considered` harder to trip, and makes the
// self-certifying-skip finding easier to trip, which is the one a human
// should look at.
func TagMatch(adrs []ADR, body string) []ADR {
	lower := strings.ToLower(body)
	var out []ADR
	for _, adr := range adrs {
		if !adr.EligibleForGCC() {
			continue
		}
		if matchesAnyTag(adr.Tags, lower) {
			out = append(out, adr)
		}
	}
	return out
}

func matchesAnyTag(tags []string, lowerBody string) bool {
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if tagPattern(tag).MatchString(lowerBody) {
			return true
		}
	}
	return false
}

// tagPattern builds a word-boundary matcher for one tag. Compiled per
// call rather than cached: the corpus is a few hundred records and this
// runs once per `--file` invocation, so a cache would be state to get
// wrong for no measurable gain.
func tagPattern(tag string) *regexp.Regexp {
	// \b does not fire next to a hyphen or a dot, which most tags carry
	// ("error-handling", "gopkg.in"), so the boundary is spelled out as
	// "not preceded/followed by a word character".
	quoted := regexp.QuoteMeta(tag)
	return regexp.MustCompile(`(^|[^\w])` + quoted + `($|[^\w])`)
}

// GovernancePass is the story's declared digest-pass record.
type GovernancePass struct {
	// Applicable is `adrs_applicable`, and Declared reports whether the
	// story stated one at all. A story with no governance_pass block is
	// not a finding: the key is new, and every story minted before it
	// would otherwise report one.
	Applicable int
	Declared   bool
}

// ReadGovernancePass pulls the declared counts out of frontmatter.
func ReadGovernancePass(raw map[string]any) GovernancePass {
	block, ok := lookupPath(raw, []string{"governance_pass"})
	if !ok {
		return GovernancePass{}
	}
	m, ok := block.(map[string]any)
	if !ok {
		return GovernancePass{}
	}
	v, ok := m["adrs_applicable"]
	if !ok || v == nil {
		return GovernancePass{}
	}
	n, ok := asInt(v)
	if !ok {
		return GovernancePass{}
	}
	return GovernancePass{Applicable: n, Declared: true}
}

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		// A YAML `0` decodes as int, but a hand-edited `0.0` does not.
		// Accepting it costs nothing and refusing it would report a
		// missing declaration that is plainly there.
		return int(typed), typed == float64(int(typed))
	default:
		return 0, false
	}
}

// CheckGovernanceCounts is 9.12b: recompute the candidate count and bind
// the declared applicable count against it.
func CheckGovernanceCounts(b Body, adrs []ADR, id string) []Finding {
	pass := ReadGovernancePass(b.Raw)
	if !pass.Declared {
		return nil
	}
	considered := len(TagMatch(adrs, b.Text))

	switch {
	case pass.Applicable > considered:
		return []Finding{{
			Check: CheckADRsConsidered, Story: id, Path: b.Path, Field: "governance_pass",
			Message: fmt.Sprintf(
				"adrs_applicable is %d but the recomputed adrs_considered is %d — "+
					"more ADRs were judged applicable than were ever candidates",
				pass.Applicable, considered,
			),
		}}
	case pass.Applicable == 0 && considered > 0:
		return []Finding{{
			Check: CheckADRsConsidered, Story: id, Path: b.Path, Field: "governance_pass",
			Message: fmt.Sprintf(
				"adrs_applicable is 0 but the recomputed adrs_considered is %d — "+
					"the digest pass certified that none of %d candidate ADRs applies, "+
					"which is the one judgement it cannot make silently",
				considered, considered,
			),
		}}
	}
	return nil
}

// CheckADRRefs is 13.3b: every id cited in governance.adrs resolves at
// HEAD, and one that resolves to a non-accepted ADR is flagged.
func CheckADRRefs(b Body, adrs []ADR, id string) []Finding {
	value, present := lookupPath(b.Raw, []string{"governance", "adrs"})
	if !present {
		return nil
	}
	byID := make(map[string]ADR, len(adrs))
	for _, adr := range adrs {
		byID[adr.ID] = adr
	}
	var findings []Finding
	for _, cited := range citedIDs(value) {
		adr, ok := byID[cited]
		if !ok {
			findings = append(findings, Finding{
				Check: CheckADRUnresolved, Story: id, Path: b.Path, Field: "governance.adrs",
				Message: cited + " does not resolve to an ADR at HEAD",
			})
			continue
		}
		if !adr.Accepted() {
			findings = append(findings, Finding{
				Check: CheckADRNotAccepted, Story: id, Path: b.Path, Field: "governance.adrs",
				Message: fmt.Sprintf(
					"%s resolves to an ADR whose status is %q, not accepted — reported, not gated",
					cited, adr.Status,
				),
			})
		}
	}
	return findings
}

// GovernanceCorpus is the ADR corpus a `--file` run resolved, and
// whether it found one at all.
type GovernanceCorpus struct {
	ADRs []ADR
	// Resolved reports that a project was found and its ADR directory
	// read. False means the two governance classes SKIPPED — which the
	// verdict reports rather than passing over, because "we could not
	// look" is not "we looked and it was clean".
	Resolved bool
	// Root is the project root that was found, for the report.
	Root string
}

// ResolveProjectExt reads the active extensions from the project a story
// sits in, walking up from the story's own path.
//
// `--file` mode ALREADY does this walk — ResolveGovernanceCorpus below is
// the same three lines — and then ignored the project's own `extensions`,
// requiring the caller to restate them via `--active-extensions`.
//
// Deriving them is not a convenience. It removes a RESTATEMENT: the
// project's `_apex/config.yaml` already says which extensions are on, and
// asking every caller to retype it is the two-shapes-for-one-fact family,
// with one shape reachable only by remembering.
//
// Measured, against the framework at v0.16.0-in-progress. Fourteen skills
// reference `ape story verify`; three pass `--active-extensions`; eight
// use corpus mode, which always took its extensions from the resolved
// project and was never affected. **Three call `--file` with no flag** —
// `apex-orchestrator`, `apex-review-story` and `apex-story-governance` —
// so on every invocation the two ADR classes silently did not run and the
// caller read `is valid`. `apex-review-story`'s is the structural
// pre-review gate four other steps cite as their verdict source.
//
// At corpus scale the same omission hid more: the framework's own sweep
// passed no flag, so 982 of 982 stories skipped both classes across seven
// sweeps and were reported clean — 925 of them in fixtures that DO enable
// `ext-adrs`, i.e. 94% of the corpus under-checked and quoted.
//
// ok is false when no project resolves — a story outside any project,
// which `--file` is required to keep working for. The caller then has
// nothing to derive from and the governance classes skip, as before.
func ResolveProjectExt(storyPath string) (ext apexcfg.Ext, ok bool) {
	abs, err := filepath.Abs(storyPath)
	if err != nil {
		return apexcfg.Ext{}, false
	}
	root, err := apexcfg.Find(filepath.Dir(abs))
	if err != nil {
		return apexcfg.Ext{}, false
	}
	cfg, err := apexcfg.ResolveAt(root, nil)
	if err != nil {
		return apexcfg.Ext{}, false
	}
	return cfg.Ext, true
}

// ResolveGovernanceCorpus walks up from the story's own path to find a
// project, then loads its ADRs.
//
// The walk is what lets `--file` mode assert anything about the corpus
// at all. It stays OPTIONAL by design: the mode is required to keep
// working against a story outside any project — exactly as the Python it
// replaces did — so a failed walk yields Resolved=false and the two
// governance classes skip, while every other class still gates.
func ResolveGovernanceCorpus(storyPath string) GovernanceCorpus {
	abs, err := filepath.Abs(storyPath)
	if err != nil {
		return GovernanceCorpus{}
	}
	root, err := apexcfg.Find(filepath.Dir(abs))
	if err != nil {
		return GovernanceCorpus{}
	}
	cfg, err := apexcfg.ResolveAt(root, nil)
	if err != nil {
		return GovernanceCorpus{}
	}
	adrs, ok := LoadADRs(cfg.Paths.ADRs)
	if !ok {
		return GovernanceCorpus{Root: root}
	}
	return GovernanceCorpus{ADRs: adrs, Resolved: true, Root: root}
}
