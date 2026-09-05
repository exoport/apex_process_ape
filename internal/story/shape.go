package story

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// The story-SHAPE check classes, over a story's body.
//
// These are `--file` mode only, and that is a decision rather than an
// oversight. The corpus scan reads at most FrontmatterCap per file and
// never opens a body — the difference between a 67 KB scan and a 23.5 MB
// one across 465 stories. Every class below is a body check, so wiring
// them into the corpus walk would trade that guarantee away for a report
// nobody gates on. Corpus mode still reports the frontmatter classes
// exactly as it did.
//
// Each class replaces prose the framework carried in a skill file and
// enforced by asking a model to re-read the story. The prose is being
// deleted; these are what make its claims true.
const (
	// CheckSectionMissing — a section the resolved config makes mandatory
	// is absent from the body.
	CheckSectionMissing = "story.section_missing"
	// CheckFileListMarker — a File List entry carries no status marker,
	// or uses the forbidden em-dash-prose form in place of one.
	CheckFileListMarker = "story.file_list_marker"
	// CheckPlaceholderResidue — a template placeholder survived into a
	// story at `status: review`.
	CheckPlaceholderResidue = "story.placeholder_residue"
	// CheckComplianceTableHeader — a compliance table's header row is not
	// the verbatim shape.
	CheckComplianceTableHeader = "story.compliance_table_header"
	// CheckGCCLineForm — a Governance Compliance Criteria line does not
	// match the declared form.
	CheckGCCLineForm = "story.gcc_line_form"
	// CheckADRsConsidered — the declared adrs_applicable does not bind
	// against the recomputed tag-match candidate count.
	CheckADRsConsidered = "story.adrs_considered"
	// CheckADRUnresolved — an id cited in governance.adrs does not
	// resolve to an ADR at HEAD.
	CheckADRUnresolved = "story.adr_unresolved"
	// CheckADRNotAccepted — a cited ADR resolves but is not `accepted`.
	// REPORTED, NOT GATED: exit 0. A story may legitimately cite a
	// proposed ADR; a reader needs to know, and a gate would stop the run.
	CheckADRNotAccepted = "story.adr_not_accepted"
)

// The canonical section headings, exactly as the story template writes
// them.
const (
	SecStory          = "## Story"
	SecAcceptance     = "## Acceptance Criteria"
	SecTasks          = "## Tasks / Subtasks"
	SecDevNotes       = "## Dev Notes"
	SecDevAgentRecord = "## Dev Agent Record"
	SecAgentModel     = "### Agent Model Used"
	SecFileList       = "### File List"
	SecCompletionNote = "### Completion Notes List"
	SecDebugLog       = "### Debug Log References"
	SecChangeLog      = "## Change Log"

	SecGCC          = "### Governance Compliance Criteria"
	SecGovernance   = "## Governance"
	SecADRTable     = "### ADR Compliance Table"
	SecPatternTable = "### Pattern Compliance Table"
	SecFeatureScope = "## Feature Scope"
)

// Placeholder is what the template writes into the Dev Agent Record
// subsections a dev pass is expected to replace.
const Placeholder = "_(populated during dev)_"

// DebugLogTerminal is `### Debug Log References`' terminal convention. It
// is NOT residue: its writer treats "no issues" as a complete answer,
// and reporting it would fail every clean story.
const DebugLogTerminal = "_No issues encountered._"

// StatusReview is the status at which placeholder residue becomes a
// finding. At `ready-for-dev` those placeholders are exactly what the
// template is required to write, so asserting non-placeholder at mint
// would invert the template's own contract.
const StatusReview = "review"

// DerivedSections returns the section set a story must carry, given the
// resolved extension flags.
//
// The set is a function of RESOLVED CONFIG ALONE. No writer stamps a
// story type into frontmatter today — the template's block carries
// story_id, epic, status, review_count and output_document and nothing
// else — so there is no type input to read, and no `sections:`
// declaration ships to compare against. `## UX Specification` is
// therefore NOT a member: the template calls it a frontend-story
// section, which is precisely the type-dependent judgement no key
// records.
//
// The signature takes the type as a string so a later `story_type:` key
// can NARROW the set without changing the exit contract. An unknown or
// empty type yields the config-derived set, which is today's every case.
func DerivedSections(ext apexcfg.Ext, storyType string) []string {
	// storyType is accepted and deliberately unused: see the doc comment.
	// A later writer narrows here, and no caller changes.
	_ = storyType

	sections := []string{
		SecStory,
		SecAcceptance,
		SecTasks,
		SecDevNotes,
		SecDevAgentRecord,
		SecAgentModel,
		SecFileList,
		SecCompletionNote,
		SecDebugLog,
		SecChangeLog,
	}
	if ext.ADRs || ext.Patterns {
		// Both governance headers are emitted unconditionally when either
		// extension is on, even when no rules survive: an empty section is
		// accepted, a missing header is not. That is the always-emit
		// invariant, and the derived set is what preserves it.
		sections = append(sections, SecGCC, SecGovernance)
	}
	if ext.ADRs {
		sections = append(sections, SecADRTable)
	}
	if ext.Patterns {
		sections = append(sections, SecPatternTable)
	}
	if ext.Features {
		// A member on ext_features ALONE, always — never conditioned on a
		// feature having matched. When zero features apply the section
		// carries the `_No features apply to this story._` sentinel, which
		// is "no matches" and not a missing section.
		sections = append(sections, SecFeatureScope)
	}
	return sections
}

// Body is a story read in full: frontmatter plus the text after it.
//
// Only `--file` mode builds one. The corpus walk keeps reading heads.
type Body struct {
	Path string
	Raw  map[string]any
	// Text is the body verbatim, fences and all.
	Text string
	// Prose is Text with every fenced code block removed. Every
	// STRUCTURAL class reads this rather than Text — see stripFences for
	// why a fenced example is not a story's own content.
	Prose string
	Err   error
}

// ReadBody reads a whole story file.
func ReadBody(path string) Body {
	b := Body{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Err = err
		return b
	}
	fm, rest, splitErr := frontmatter.Split(data)
	if splitErr != nil {
		b.Err = splitErr
		return b
	}
	var raw map[string]any
	if unmarshalErr := yaml.Unmarshal(fm, &raw); unmarshalErr != nil {
		b.Err = fmt.Errorf("frontmatter is not valid YAML: %w", unmarshalErr)
		return b
	}
	if raw == nil {
		raw = map[string]any{}
	}
	b.Raw = raw
	b.Text = string(rest)
	b.Prose = stripFences(b.Text)
	return b
}

// stripFences removes fenced code blocks from a body.
//
// # Why every structural class needs this
//
// A fenced block is an EXAMPLE of a shape, not an instance of it. The
// story template's `### File List` carries its canonical entry shape
// inside a ```markdown fence:
//
//   - `path/to/file.ext` (marker) — short note
//
// That line is written verbatim into every minted story, and
// `apex-dev-story` often leaves it in place. Read as an entry it reports
// `(marker)` as an unknown marker — which is how this turned into 51 of
// 60 failing fixture stories, 140 of one real project's 483 and 2 of
// another's 296. The validator would have been reporting the template
// against itself.
//
// The same trap exists for every other body class: a fenced `## Story`
// in Dev Notes must not satisfy the derived section set, a fenced GCC
// line must not be checked for its separator, a fenced compliance table
// must not be read as the story's own, and a fenced placeholder is not
// residue. So the strip happens once, here, and every structural class
// reads Prose.
//
// Tag matching deliberately still reads Text: the digest algorithm's
// step 1 is instructed to be inclusive ("extra entries cost little,
// missed entries are expensive"), and a tag mentioned inside an example
// is still the story talking about that subject.
//
// Fence rules follow CommonMark closely enough for real documents: an
// opening fence is three or more backticks or tildes, indented at most
// three spaces, optionally followed by an info string; the block ends at
// a fence of the SAME character that is at least as long and carries no
// info string, or at end of document. Matching the character and length
// is what lets a ```` ``` ```` example sit inside a ```“ ```` ```“ block.
func stripFences(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	var (
		inFence bool
		char    byte
		width   int
	)
	for _, line := range lines {
		marker, markerChar, markerWidth, info := fenceMarker(line)
		switch {
		case !inFence && marker:
			inFence, char, width = true, markerChar, markerWidth
			// The opening fence line goes too — it is not story prose.
		case inFence && marker && markerChar == char && markerWidth >= width && info == "":
			inFence = false
		case inFence:
			// Inside the block: dropped.
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// fenceMarker reports whether line opens or closes a fence, with the
// fence character, its run length, and any info string.
func fenceMarker(line string) (isFence bool, char byte, width int, info string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		// More than three spaces of indent is an indented code block, not
		// a fence.
		return false, 0, 0, ""
	}
	if len(trimmed) < 3 {
		return false, 0, 0, ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return false, 0, 0, ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return false, 0, 0, ""
	}
	rest := strings.TrimSpace(trimmed[n:])
	if c == '`' && strings.Contains(rest, "`") {
		// A backtick fence's info string may not contain a backtick;
		// that shape is inline code, not a fence.
		return false, 0, 0, ""
	}
	return true, c, n, rest
}

// Status returns the story's declared status, lowercased.
func (b Body) Status() string {
	v, ok := b.Raw["status"]
	if !ok || v == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
}

// StoryType returns the declared story type. Always empty today — no
// writer stamps one — and read here so the derived-section call site
// does not have to change when one does.
func (b Body) StoryType() string {
	v, ok := b.Raw["story_type"]
	if !ok || v == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
}

// headingRe matches an ATX heading and captures its level and text.
var headingRe = regexp.MustCompile(`(?m)^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)

// wsRun collapses internal whitespace so `## Tasks  /  Subtasks` and
// `## Tasks / Subtasks` are the same heading. Spacing is formatting, and
// Prettier rewrites it; the heading's identity is its words.
var wsRun = regexp.MustCompile(`[ \t]+`)

// headings returns the body's headings, normalised to `### Text` form.
func headings(text string) []string {
	matches := headingRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1]+" "+wsRun.ReplaceAllString(strings.TrimSpace(m[2]), " "))
	}
	return out
}

// CheckSections reports each derived section the body does not carry.
func CheckSections(b Body, ext apexcfg.Ext, id string) []Finding {
	present := map[string]bool{}
	for _, h := range headings(b.Prose) {
		present[h] = true
	}
	var findings []Finding
	for _, want := range DerivedSections(ext, b.StoryType()) {
		if present[want] {
			continue
		}
		findings = append(findings, Finding{
			Check: CheckSectionMissing, Story: id, Path: b.Path, Field: want,
			Message: fmt.Sprintf(
				"the derived section set requires %s and the body does not carry it "+
					"(an empty section is accepted; a missing header is not)", want,
			),
		})
	}
	return findings
}

// section returns the text under heading, up to the next heading of the
// same or shallower level. Empty when the heading is absent.
func section(text, heading string) string {
	level := strings.Count(strings.SplitN(heading, " ", 2)[0], "#")
	lines := strings.Split(text, "\n")
	var (
		out      []string
		inside   bool
		wantNorm = heading
	)
	for _, line := range lines {
		m := headingRe.FindStringSubmatch(line)
		if m != nil {
			got := m[1] + " " + wsRun.ReplaceAllString(strings.TrimSpace(m[2]), " ")
			if inside && len(m[1]) <= level {
				break
			}
			if got == wantNorm {
				inside = true
				continue
			}
		}
		if inside {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// FileListMarkers is the marker vocabulary, exactly as the template's
// table declares it.
//
// ALL FIVE, not the three a dev pass writes. `(planned)` and
// `(deferred)` are apex-lift-project's — "predicted by lift; not yet
// implemented" and "listed in lift; punted to a later story" — and
// rejecting them would report a finding on every story that skill has
// ever produced.
var FileListMarkers = []string{"created", "modified", "deleted", "planned", "deferred"}

// fileListEntryRe matches a File List bullet and captures the path plus
// what follows it.
//
// The backticks are OPTIONAL on purpose. The canonical entry wraps the
// path in them, but an entry that forgot to is still an entry, and
// skipping it here would let the commonest malformed line — a bare path
// with no marker at all — pass unexamined. Missing backticks are not
// this class's finding (the class is the marker vocabulary); being
// unable to see the entry would be.
var fileListEntryRe = regexp.MustCompile(
	"^[-*][ \t]+(?:`([^`]+)`|([^\\s`]+))[ \t]*(.*)$",
)

// markerRe matches the canonical marker position: parens immediately
// after the backticked path.
var markerRe = regexp.MustCompile(`^\(([a-z]+)\)`)

// emDashProseRe matches the forbidden marker substitute: an em-dash
// annotation standing in for the marker, as in "— created: the thing".
var emDashProseRe = regexp.MustCompile(`^[—–-]+[ \t]*(created|modified|deleted|added|removed|changed)\b`)

// CheckFileList reports File List entries whose marker is missing or
// written in the forbidden em-dash-prose form.
func CheckFileList(b Body, id string) []Finding {
	body := section(b.Prose, SecFileList)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var findings []Finding
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, Placeholder) {
			// The template's own placeholder is not an entry. It is
			// CheckPlaceholders' finding at status: review, and nothing at
			// all before then.
			continue
		}
		m := fileListEntryRe.FindStringSubmatch(trimmed)
		if m == nil {
			// Not an entry: prose, a blank line, or a table row. The class
			// is about entries that exist.
			continue
		}
		// Group 1 is the backticked path, group 2 the bare one; exactly
		// one is set.
		path := m[1]
		if path == "" {
			path = m[2]
		}
		rest := strings.TrimSpace(m[3])
		if marker := markerRe.FindStringSubmatch(rest); marker != nil {
			if !validMarker(marker[1]) {
				findings = append(findings, Finding{
					Check: CheckFileListMarker, Story: id, Path: b.Path, Field: path,
					Message: fmt.Sprintf("marker (%s) is not one of %s",
						marker[1], strings.Join(FileListMarkers, ", ")),
				})
			}
			continue
		}
		if emDashProseRe.MatchString(rest) {
			findings = append(findings, Finding{
				Check: CheckFileListMarker, Story: id, Path: b.Path, Field: path,
				Message: "the em-dash-prose form is forbidden as a marker substitute — " +
					"write one marker in parens immediately after the path, then any " +
					"em-dash annotation after it",
			})
			continue
		}
		findings = append(findings, Finding{
			Check: CheckFileListMarker, Story: id, Path: b.Path, Field: path,
			Message: fmt.Sprintf(
				"entry carries no status marker — exactly one of (%s) belongs in parens "+
					"immediately after the path",
				strings.Join(FileListMarkers, ") ("),
			),
		})
	}
	return findings
}

func validMarker(m string) bool {
	return slices.Contains(FileListMarkers, m)
}

// CheckPlaceholders reports template placeholders surviving at
// `status: review`.
//
// Scoped to three subsections. `### Debug Log References` is excluded on
// purpose: its `_No issues encountered._` is a terminal convention its
// writer emits as a complete answer, not a placeholder awaiting one.
func CheckPlaceholders(b Body, id string) []Finding {
	if b.Status() != StatusReview {
		return nil
	}
	var findings []Finding
	for _, heading := range []string{SecAgentModel, SecFileList, SecCompletionNote} {
		if !strings.Contains(section(b.Prose, heading), Placeholder) {
			continue
		}
		findings = append(findings, Finding{
			Check: CheckPlaceholderResidue, Story: id, Path: b.Path, Field: heading,
			Message: fmt.Sprintf(
				"%s still carries the %s placeholder at status: review — "+
					"the dev pass is what replaces it", heading, Placeholder,
			),
		})
	}
	return findings
}

// The compliance-table header rows, verbatim.
const (
	ADRTableHeader     = "| ADR | Why it applies | Key constraints |"
	PatternTableHeader = "| Pattern | Why it applies | Key constraints |"
)

// CheckComplianceTables reports a compliance table whose header row is
// not the declared shape.
//
// Only the HEADER is checked. The rows carry authored prose, and
// asserting anything about their content would be asserting a judgement.
func CheckComplianceTables(b Body, ext apexcfg.Ext, id string) []Finding {
	var findings []Finding
	tables := []struct {
		on      bool
		heading string
		want    string
	}{
		{ext.ADRs, SecADRTable, ADRTableHeader},
		{ext.Patterns, SecPatternTable, PatternTableHeader},
	}
	for _, tbl := range tables {
		if !tbl.on {
			continue
		}
		body := section(b.Prose, tbl.heading)
		if strings.TrimSpace(body) == "" {
			// A missing section is CheckSections' finding, not this one —
			// reporting both would name one defect twice.
			continue
		}
		header := firstTableRow(body)
		if header == "" {
			findings = append(findings, Finding{
				Check: CheckComplianceTableHeader, Story: id, Path: b.Path, Field: tbl.heading,
				Message: fmt.Sprintf("no table found under %s — the header row %q is "+
					"emitted even when zero rules survive", tbl.heading, tbl.want),
			})
			continue
		}
		if normaliseRow(header) != normaliseRow(tbl.want) {
			findings = append(findings, Finding{
				Check: CheckComplianceTableHeader, Story: id, Path: b.Path, Field: tbl.heading,
				Message: fmt.Sprintf("header row is %q, want %q", header, tbl.want),
			})
		}
	}
	return findings
}

// firstTableRow returns the first pipe-delimited row in body.
func firstTableRow(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			return trimmed
		}
	}
	return ""
}

// normaliseRow makes a table row comparable regardless of the column
// padding a formatter applies. Prettier aligns these tables to their
// widest cell, so a byte comparison would fail every formatted story.
func normaliseRow(row string) string {
	cells := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, wsRun.ReplaceAllString(strings.TrimSpace(c), " "))
	}
	return "|" + strings.Join(out, "|") + "|"
}

// gccLineRe is the declared form:
//
//   - [ ] **[ID]** {instruction} -- {scope}
//
// The checkbox may be ticked, and review may append an N/A reason, so
// the state is captured loosely and the ID and separator strictly.
var gccLineRe = regexp.MustCompile(`^[-*][ \t]+\[[ xX]\][ \t]+\*\*\[([A-Z]+-\d+)\]\*\*[ \t]+(.*)$`)

// gccIDRe is the id vocabulary: ADR, PAT and PATLOC only.
var gccIDRe = regexp.MustCompile(`^(ADR|PAT|PATLOC)-\d+$`)

// naPrefixRe matches review's N/A form, which carries a reason instead
// of a scope tail.
var naPrefixRe = regexp.MustCompile(`^N/A:[ \t]*\S`)

// CheckGCCLines reports Governance Compliance Criteria lines that do not
// match the declared form.
func CheckGCCLines(b Body, id string) []Finding {
	body := section(b.Prose, SecGCC)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var findings []Finding
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [") && !strings.HasPrefix(trimmed, "* [") {
			continue // prose, a blank line, or the empty-section case
		}
		m := gccLineRe.FindStringSubmatch(trimmed)
		if m == nil {
			findings = append(findings, Finding{
				Check: CheckGCCLineForm, Story: id, Path: b.Path, Field: trimmed,
				Message: "does not match the declared form " +
					"`- [ ] **[ID]** {instruction} -- {scope}`",
			})
			continue
		}
		ruleID, rest := m[1], strings.TrimSpace(m[2])
		if !gccIDRe.MatchString(ruleID) {
			findings = append(findings, Finding{
				Check: CheckGCCLineForm, Story: id, Path: b.Path, Field: ruleID,
				Message: "id must be ADR-NNNN, PAT-NNNN or PATLOC-NNNN — " +
					"PATCAN ids are never valid here",
			})
			continue
		}
		if naPrefixRe.MatchString(rest) {
			// Review's `- [x] N/A: <reason>` form carries a reason where
			// the scope tail would be. It is a legal marking of the same
			// line, not a malformed one.
			continue
		}
		if !strings.Contains(rest, " -- ") {
			msg := "the scope tail must be separated by ` -- ` (double hyphen)"
			if strings.ContainsAny(rest, "—–") {
				msg = "the scope tail is separated by an em-dash; the declared " +
					"separator is ` -- ` (double hyphen)"
			}
			findings = append(findings, Finding{
				Check: CheckGCCLineForm, Story: id, Path: b.Path, Field: ruleID,
				Message: msg,
			})
		}
	}
	return findings
}
