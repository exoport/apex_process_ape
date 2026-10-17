package story

import (
	"github.com/exoport/apex_process_ape/internal/mdscan"
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

// DebugLogTerminal is `### Debug Log References`' terminal convention. It
// is NOT residue: its writer treats "no issues" as a complete answer,
// and reporting it would fail every clean story.
const DebugLogTerminal = "_No issues encountered._"

// StatusReview is the status at which placeholder residue becomes a
// finding. At `ready-for-dev` those placeholders are exactly what the
// template is required to write, so asserting non-placeholder at mint
// would invert the template's own contract.
const StatusReview = "review"

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

// The markdown primitives now live in internal/mdscan, so the spec
// graph reads a document exactly the way this package does rather than
// reimplementing it slightly differently.
//
// Kept as package-level aliases rather than rewritten call sites: every
// use below — and every test, including the ones that call the
// unexported name directly — is textually unchanged, which is what makes
// the move provably behaviour-preserving. The rationale comments moved
// with the code; see mdscan for why each has the shape it does.
// No headingRe alias: both of its users — the heading walk and the
// section slicer — moved with it, so the name has no caller left here.
var (
	stripFences = mdscan.StripFences
)
