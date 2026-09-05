package story

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/stretchr/testify/require"
)

// govProject builds a project with an ADR corpus, so the `--file` gate
// has something to walk up to and resolve.
func govProject(t *testing.T) *apexcfg.Resolved {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, apexcfg.DirName), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, apexcfg.DirName, apexcfg.BaseFile),
		[]byte("config_schema_version: \"1\"\nproject_name: gov\nextensions: [ext-adrs]\n"+
			"development_folder: development\n"+
			"implementation_folder: development/implementation\n"+
			"governance_folder: development/governance\n"), 0o644,
	))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cfg.Paths.Implementation, 0o755))
	require.NoError(t, os.MkdirAll(cfg.Paths.ADRs, 0o755))
	return cfg
}

// writeADR writes an accepted architectural ADR. The two fields a case
// might vary — status and type — have their own helper below, so the
// common case stays a one-liner.
func writeADR(t *testing.T, dir, id string, tags ...string) {
	t.Helper()
	writeADRAs(t, dir, id, "accepted", "architectural", tags...)
}

func writeADRAs(t *testing.T, dir, id, status, adrType string, tags ...string) {
	t.Helper()
	body := "---\nid: " + id + "\ntitle: \"" + id + "\"\nstatus: " + status +
		"\ntype: " + adrType + "\ntags: [" + strings.Join(tags, ", ") + "]\n---\n\n# " + id + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "adr-"+id+".md"), []byte(body), 0o644))
}

// storyWithGovernance writes a story carrying the governance sections
// plus whatever extra frontmatter the case needs.
func storyWithGovernance(t *testing.T, cfg *apexcfg.Resolved, extraFM, extraBody string) string {
	t.Helper()
	fm := "---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n" +
		"governance:\n  adrs: []\n" + extraFM + "---\n"
	return writeStory(t, cfg.Paths.Implementation, "1-1.md", fm+governanceBody+extraBody)
}

// --- 9.12b, the adrs_considered recomputation --------------------------

// TestGovernanceCounts_SelfCertifyingSkip is the finding the check exists
// to catch: the pass declared that none of a non-empty candidate set
// applies.
func TestGovernanceCounts_SelfCertifyingSkip(t *testing.T) {
	cfg := govProject(t)
	writeADR(t, cfg.Paths.ADRs, "ADR-0001", "wiring")

	path := storyWithGovernance(t, cfg,
		"governance_pass:\n  ran_at: \"20260903120000\"\n  skill: apex-story-governance\n"+
			"  adrs_considered: 1\n  adrs_applicable: 0\n",
		"\nThe story is about wiring the thing.\n")

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)

	var found bool
	for _, f := range verdict.Findings {
		if f.Check != CheckADRsConsidered {
			continue
		}
		found = true
		require.Contains(t, f.Message, "adrs_applicable")
		require.Contains(t, f.Message, "adrs_considered")
		require.NotContains(t, f.Message, "applicability mismatch",
			"the message must never call this an applicability mismatch")
	}
	require.True(t, found, "findings: %+v", verdict.Findings)
}

func TestGovernanceCounts_ApplicableExceedsConsidered(t *testing.T) {
	cfg := govProject(t)
	writeADR(t, cfg.Paths.ADRs, "ADR-0001", "wiring")

	path := storyWithGovernance(t, cfg,
		"governance_pass:\n  adrs_applicable: 4\n",
		"\nThe story is about wiring the thing.\n")

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckADRsConsidered)
}

// TestGovernanceCounts_JudgementIsNotRecomputed is the guard against the
// tempting wrong check: a pass that kept some candidates and dropped
// others is doing its job, and firing on it would fire on every story.
func TestGovernanceCounts_JudgementIsNotRecomputed(t *testing.T) {
	cfg := govProject(t)
	writeADR(t, cfg.Paths.ADRs, "ADR-0001", "wiring")
	writeADR(t, cfg.Paths.ADRs, "ADR-0002", "wiring")
	writeADR(t, cfg.Paths.ADRs, "ADR-0003", "wiring")

	// Three candidates, one judged applicable. A legitimate keep-or-drop.
	path := storyWithGovernance(t, cfg,
		"governance_pass:\n  adrs_applicable: 1\n",
		"\nThe story is about wiring the thing.\n")

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileOK, verdict.Code, "findings: %+v", verdict.Findings)
}

// TestGovernanceCounts_NoDeclarationIsNotAFinding — governance_pass is a
// new key, and every story minted before it would otherwise report one.
func TestGovernanceCounts_NoDeclarationIsNotAFinding(t *testing.T) {
	cfg := govProject(t)
	writeADR(t, cfg.Paths.ADRs, "ADR-0001", "wiring")

	path := storyWithGovernance(t, cfg, "", "\nAbout wiring the thing.\n")
	require.Equal(t, FileOK, VerifyFile(path, apexcfg.Ext{ADRs: true}).Code)
}

// TestTagMatch_CandidateSetExcludesIneligibleADRs documents the decision
// PLAN-26 records: the candidate set is the tag match over ADRs that can
// produce a GCC line at all. Counting a proposed or pattern-typed record
// would make the self-certifying-skip finding fire against a candidate
// the agent was right to drop.
func TestTagMatch_CandidateSetExcludesIneligibleADRs(t *testing.T) {
	adrs := []ADR{
		{ID: "ADR-0001", Status: "accepted", Type: "architectural", Tags: []string{"wiring"}},
		{ID: "ADR-0002", Status: "proposed", Type: "architectural", Tags: []string{"wiring"}},
		{ID: "ADR-0003", Status: "accepted", Type: "pattern", Tags: []string{"wiring"}},
	}
	matched := TagMatch(adrs, "the story is about wiring the thing")
	require.Len(t, matched, 1)
	require.Equal(t, "ADR-0001", matched[0].ID)
}

// TestTagMatch_WordBoundaries — a tag must match a word, not a substring,
// or "go" would match "going" and every ADR would be a candidate for
// every story.
func TestTagMatch_WordBoundaries(t *testing.T) {
	adrs := []ADR{
		{ID: "ADR-0001", Status: "accepted", Type: "architectural", Tags: []string{"go"}},
		{
			ID: "ADR-0002", Status: "accepted", Type: "architectural",
			Tags: []string{"error-handling"},
		},
	}
	require.Empty(t, TagMatch(adrs, "we are going to the shops"))
	require.Len(t, TagMatch(adrs, "written in Go, obviously"), 1,
		"matching is case-insensitive")
	require.Len(t, TagMatch(adrs, "the error-handling story"), 1,
		"a hyphenated tag matches as a phrase")
}

// --- 13.3b, ids resolve at HEAD ----------------------------------------

func TestADRRefs_UnresolvedIDGates(t *testing.T) {
	cfg := govProject(t)
	writeADR(t, cfg.Paths.ADRs, "ADR-0001", "wiring")

	path := writeStory(t, cfg.Paths.Implementation, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: [ADR-9999]\n---\n"+governanceBody)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileShapeProblem, verdict.Code)
	require.Contains(t, checkNames(verdict.Findings), CheckADRUnresolved)
}

// TestADRRefs_ProposedADRIsFlaggedNotGated is the distinction that is the
// point: a different outcome from a different fact.
func TestADRRefs_ProposedADRIsFlaggedNotGated(t *testing.T) {
	cfg := govProject(t)
	writeADRAs(t, cfg.Paths.ADRs, "ADR-0001", "proposed", "architectural", "wiring")

	path := writeStory(t, cfg.Paths.Implementation, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: [ADR-0001]\n---\n"+governanceBody)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileOK, verdict.Code,
		"a story may legitimately cite a proposed ADR — gating would turn a note into a halt")
	// Filtered rather than counted: Flagged also carries the report-only
	// story.requirement_ids_missing, which fires on any story without the
	// key and has nothing to do with ADR status.
	var notAccepted []Finding
	for _, f := range verdict.Flagged {
		if f.Check == CheckADRNotAccepted {
			notAccepted = append(notAccepted, f)
		}
	}
	require.Len(t, notAccepted, 1)
	require.Contains(t, notAccepted[0].Message, "proposed")
}

// TestGovernanceClasses_SkipOutsideAProject is the property the `--file`
// mode is required to keep: it works against a story outside any
// project. The two governance classes skip, every other class still
// gates, and the skip is REPORTED rather than passed over in silence.
func TestGovernanceClasses_SkipOutsideAProject(t *testing.T) {
	dir := t.TempDir()
	path := writeStory(t, dir, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: [ADR-9999]\n"+
			"governance_pass:\n  adrs_applicable: 0\n---\n"+governanceBody)

	verdict := VerifyFile(path, apexcfg.Ext{ADRs: true})
	require.Equal(t, FileOK, verdict.Code)
	require.Len(t, verdict.SkippedChecks, 2)
	names := []string{verdict.SkippedChecks[0].Check, verdict.SkippedChecks[1].Check}
	require.Contains(t, names, CheckADRsConsidered)
	require.Contains(t, names, CheckADRUnresolved)
	for _, s := range verdict.SkippedChecks {
		require.NotEmpty(t, s.Reason, "a skip that says nothing reads as a pass")
	}
}

// TestGovernanceClasses_SkipWhenExtADRsIsOff — the classes are gated on
// the extension, like every other governance-shaped check, AND the
// non-run is reported.
//
// The reporting half is not decoration. This branch is reached two ways
// that look identical from outside: a project that genuinely does not use
// ADRs, and a caller that forgot `--active-extensions`. Measured on an
// otherwise-clean story citing a `proposed` ADR, `--file` exits 0 with
// the flag and 0 without it, and only the first run produces the flagged
// finding — so an acceptance block asserting "exit 0, flagged" passes on
// the second while measuring nothing. The skip is what tells the two
// apart.
func TestGovernanceClasses_SkipWhenExtADRsIsOff(t *testing.T) {
	cfg := govProject(t)
	path := writeStory(t, cfg.Paths.Implementation, "1-1.md",
		"---\nstory_id: 1-1\nepic: 1\nstatus: done\noutput_document: x.md\n"+
			"governance:\n  adrs: [ADR-9999]\n---\n"+conformingBody)

	verdict := VerifyFile(path, apexcfg.Ext{})
	require.Equal(t, FileOK, verdict.Code)
	require.Len(t, verdict.SkippedChecks, 2)
	names := []string{verdict.SkippedChecks[0].Check, verdict.SkippedChecks[1].Check}
	require.Contains(t, names, CheckADRsConsidered)
	require.Contains(t, names, CheckADRUnresolved)
	for _, s := range verdict.SkippedChecks {
		require.Contains(t, s.Reason, "ext-adrs is not active",
			"a class that did not run must say why, or it reads as one that passed")
	}
}
