package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pdfaAgentContextSpec is pdfa's development/specs/agent-context/spec.md
// verbatim (D232, read on mmr7 2026-10-20): the first requirement's two
// scenarios check the ADR-0001 file and the ADR-0031 `ape decision
// standard` removed (seq-08 on rc.7b, 567e23e); the CLAUDE.md scenarios
// name nothing it removed.
const pdfaAgentContextSpec = "# Agent context\n" +
	"Purpose: Claude Code reads the project's agent rules from one home, AGENTS.md.\n\n" +
	"### Requirement: Decisions use the v1 format\n" +
	"The system SHALL record its decisions in the v1 decision format, and SHALL mark ADR-0001's v0\n" +
	"ADR format superseded by exactly one ADR that states the v1 format.\n\n" +
	"#### Scenario: ADR-0001 is superseded by a v1-format ADR\n" +
	"- GIVEN the decisions under `development/decisions/` WHEN the ADRs naming `Supersedes: ADR-0001` are listed THEN there is exactly one, its heading is `# ADR-NNNN: <title>`, ADR-0001's Status is `superseded by` that id, and it has a `## Decision` section and an `Enforced by:` line\n" +
	"- check: f=$(LC_ALL=C grep -l -x -F 'Supersedes: ADR-0001' development/decisions/*.md); [ \"$(printf '%s\\n' \"$f\" | LC_ALL=C grep -c .)\" = 1 ] && id=$(LC_ALL=C sed -n -E '1s/^# (ADR-[0-9]{4}):.*/\\1/p' \"$f\") && [ -n \"$id\" ] && LC_ALL=C grep -q -x -F \"Status: superseded by $id\" development/decisions/0001-use-markdown-adrs.md && LC_ALL=C grep -q -x -F '## Decision' \"$f\" && LC_ALL=C grep -q '^Enforced by: .' \"$f\"\n\n" +
	"#### Scenario: the superseding ADR states the v1 format\n" +
	"- GIVEN ADR-0031 WHEN it is read THEN it names the `development/decisions/NNNN-<slug>.md` layout, `ape decision new <slug> --supersedes ADR-NNNN` and `ape decision accept`, carries no template placeholder and no YAML front matter, and is enforced by `ape project check`\n" +
	"- check: f=development/decisions/0031-use-v1-decision-format.md; LC_ALL=C grep -q -F 'development/decisions/NNNN-<slug>.md' \"$f\" && LC_ALL=C grep -q -F 'ape decision new <slug> --supersedes ADR-NNNN' \"$f\" && LC_ALL=C grep -q -F 'ape decision accept' \"$f\" && ! LC_ALL=C grep -q -F '<!--' \"$f\" && ! LC_ALL=C grep -q -x -F -- '---' \"$f\" && LC_ALL=C grep -q -x -F 'Enforced by: ape project check' \"$f\"\n\n" +
	"### Requirement: CLAUDE.md is AGENTS.md plus Claude-only lines\n" +
	"The system SHALL keep CLAUDE.md to a line `@AGENTS.md` plus only Claude-specific lines: it\n" +
	"repeats no kept rule, states no v0 BMAD process, and names only live paths and make targets.\n\n" +
	"#### Scenario: CLAUDE.md imports AGENTS.md\n" +
	"- GIVEN the repository WHEN CLAUDE.md is read THEN one of its lines is exactly `@AGENTS.md`\n" +
	"- check: LC_ALL=C grep -q -x -F '@AGENTS.md' CLAUDE.md\n\n" +
	"#### Scenario: CLAUDE.md states no v0 BMAD process\n" +
	"- GIVEN the repository WHEN CLAUDE.md is read THEN it does not mention BMAD in any case\n" +
	"- check: ! LC_ALL=C grep -q -i -E 'bmad' CLAUDE.md\n"

const (
	pdfaOld0001 = "development/decisions/0001-use-markdown-adrs.md"
	pdfa0031    = "development/decisions/0031-use-v1-decision-format.md"
	pdfaSpec    = "development/specs/agent-context/spec.md"
)

// pdfaProject is pdfa as the rc.7 lift and its repair left it, before
// seq-08: ADR-0001 the converted apex-v0 governance standard, superseded
// by the PROPOSED ADR-0031 that restated the format, and the lasting
// agent-context spec that checks both.
func pdfaProject(t *testing.T) *Project {
	t.Helper()
	files := baseFiles()
	files["AGENTS.md"] += "\n" + OperatorLine + "\n"
	files[pdfaOld0001] = "# ADR-0001: Use Markdown Architectural Decision Records\nStatus: superseded by ADR-0031\nDate: 2026-10-11\n" +
		"Lifted from: ADR-0001, development/governance/adrs/adr-0001_use-markdown-adrs.md at apex-v0-final\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: review\n"
	files[pdfa0031] = "# ADR-0031: Record decisions in the v1 ADR format\nStatus: proposed\nDate: 2026-10-19\nSupersedes: ADR-0001\n\n" +
		"## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: ape project check\n"
	files[pdfaSpec] = pdfaAgentContextSpec
	p := fixture(t, files)
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	recs := p.loadDecisionRecords()
	recs[StandardADRID] = DecisionRecord{AcceptedAt: Stamp(), By: LiftBy, BodySHA256: adrBodyDigest(files[pdfaOld0001])}
	require.NoError(t, p.saveDecisionRecords(recs))
	commitAll(t, p, "rc.7 lift and its repair (M1)")
	return p
}

const (
	pdfaScenario1 = "ADR-0001 is superseded by a v1-format ADR"
	pdfaScenario2 = "the superseding ADR states the v1 format"
	pdfaReq       = "Decisions use the v1 format"
)

func staleOf(ss []StaleScenario) map[string][]string {
	out := map[string][]string{}
	for _, s := range ss {
		out[s.Scenario] = s.Names
	}
	return out
}

// staleNotices are project check's spec.check-names-removed notices.
func staleNotices(p *Project) []Diagnostic {
	var out []Diagnostic
	for _, n := range p.Notices() {
		if n.Code == CodeSpecCheckNamesRemoved {
			out = append(out, n)
		}
	}
	return out
}

// Q130–Q132 on pdfa's shape: `ape decision standard` records what it
// removed, lists the two scenarios that check it (--check: before
// writing), leaves the spec alone, and project check shows one notice
// spec.check-names-removed (action replan) per scenario until a change
// retires them. Fails on rc.7c: no record, no list, no notice.
func TestStaleScenarios_PdfaSeq08(t *testing.T) {
	p := pdfaProject(t)
	require.Empty(t, p.StaleScenarios(), "before seq-08 nothing is removed")
	require.Empty(t, staleNotices(p))

	want := map[string][]string{
		pdfaScenario1: {pdfaOld0001, standardSupersedes},
		pdfaScenario2: {pdfa0031},
	}
	res, err := p.DecisionStandardCheck(ctx())
	require.Equal(t, CodeStandardPending, ErrorCode(err))
	require.Equal(t, want, staleOf(res.StaleScenarios), "--check lists what the write would leave stale")

	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	clean(t, p, "decision standard")
	require.Equal(t, want, staleOf(res.StaleScenarios))
	require.Equal(t, StaleScenario{
		Spec: pdfaSpec, Requirement: pdfaReq, Scenario: pdfaScenario1, Line: 8,
		Names: want[pdfaScenario1], ref: "agent-context/" + pdfaReq + "/" + pdfaScenario1,
	}, res.StaleScenarios[0])

	// Q130: the record.
	rec := mustRecord(t, p.Root, StandardADRID)
	require.NotNil(t, rec.Standard.Removed)
	require.Equal(t, []StandardRemoval{{Path: pdfaOld0001, ID: "ADR-0001"}, {Path: pdfa0031, ID: "ADR-0031"}}, *rec.Standard.Removed)
	// Q132: the spec is not in the commit, and unchanged.
	require.NotContains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "specs/")
	require.Equal(t, pdfaAgentContextSpec, read(t, p, pdfaSpec))

	ns := staleNotices(p)
	require.Len(t, ns, 2)
	require.Equal(t, ActionReplan, ns[0].Action)
	require.Equal(t, SeverityNotice, ns[0].Severity)
	require.Equal(t, pdfaSpec, ns[0].File)
	require.Equal(t, 8, ns[0].Line)
	require.Contains(t, ns[0].Message, `scenario "`+pdfaScenario1+`" of requirement "`+pdfaReq+`": its check names `+pdfaOld0001+", Supersedes: ADR-0001")
	require.Contains(t, ns[0].Message, "v1.0.0_seq-08")
	require.Contains(t, ns[0].Fix, `REMOVED delta of requirement "`+pdfaReq+`" cites the migration v1.0.0_seq-08`)
	require.Contains(t, ns[0].Fix, "never recreate the removed records or supersede ADR-0001 again")
	require.Contains(t, ns[1].Message, pdfa0031)

	// Idempotent: kept, still listed.
	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardKept, res.Action)
	require.Len(t, res.StaleScenarios, 2)

	// A change that REMOVES the requirement retires both, as its archive
	// leaves the spec: the notice is gone.
	head, _, _ := strings.Cut(pdfaAgentContextSpec, "### Requirement: Decisions")
	_, tail, _ := strings.Cut(pdfaAgentContextSpec, "### Requirement: CLAUDE.md")
	write(t, p, pdfaSpec, head+"### Requirement: CLAUDE.md"+tail)
	commitAll(t, p, "archive: retire the seq-08 scenarios")
	require.Empty(t, p.StaleScenarios())
	require.Empty(t, staleNotices(p))
}

// Q130: a later run of the verb (a release that changes the standard)
// keeps what earlier runs removed, and a run that removed nothing records
// [] — so a project with the record is never read by Q133's evidence.
func TestStandardRemovals_CarriedOver(t *testing.T) {
	p := pdfaProject(t)
	_, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	installPayload(t, p)
	res, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardReplaced, res.Action)
	require.Equal(t, []StandardRemoval{{Path: pdfaOld0001, ID: "ADR-0001"}, {Path: pdfa0031, ID: "ADR-0031"}}, *mustRecord(t, p.Root, StandardADRID).Standard.Removed)
	require.Len(t, res.StaleScenarios, 2)

	q := initProject(t)
	installPayload(t, q)
	_, err = q.DecisionStandard(ctx())
	require.NoError(t, err)
	rm := mustRecord(t, q.Root, StandardADRID).Standard.Removed
	require.NotNil(t, rm)
	require.Empty(t, *rm)
	require.Contains(t, read(t, q, "_apex/state/decisions.json"), `"removed": []`)
}

// Q133: pdfa's seq-08 ran on rc.7b, which recorded nothing. With no
// record, on a project whose ADR-0001 is the standard `ape decision
// standard` wrote, a check naming a decisions/ file that does not exist,
// or an ADR-0001 file name that is not the standard's, is stale — so the
// notice appears on pdfa with no re-run. Not on a project init or the
// lift wrote the standard for.
func TestStaleScenarios_LegacyRecord(t *testing.T) {
	p := pdfaProject(t)
	_, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	recs := p.loadDecisionRecords()
	rec := recs[StandardADRID]
	rec.Standard.Removed = nil // as rc.7b wrote it
	recs[StandardADRID] = rec
	require.NoError(t, p.saveDecisionRecords(recs))
	require.NotContains(t, read(t, p, "_apex/state/decisions.json"), "removed")
	commitAll(t, p, "as rc.7b left it")

	require.Equal(t, map[string][]string{pdfaScenario1: {pdfaOld0001}, pdfaScenario2: {pdfa0031}}, staleOf(p.StaleScenarios()))
	require.Len(t, staleNotices(p), 2)

	// An existing decisions/ file and the standard's own name are not
	// evidence; a bare ADR-0001 file name that is not the standard's is.
	write(t, p, pdfaSpec, pdfaAgentContextSpec+"\n### Requirement: Other\nThe system SHALL.\n\n#### Scenario: existing\n- GIVEN x WHEN y THEN z\n"+
		"- check: test -f development/decisions/"+StandardADRFile()+" && grep -q x development/decisions/0001-record-architecture-decisions.md\n\n"+
		"#### Scenario: bare name\n- GIVEN x WHEN y THEN z\n- check: cd development/decisions && test -f 0001-use-markdown-adrs.md\n")
	commitAll(t, p, "more")
	got := staleOf(p.StaleScenarios())
	require.NotContains(t, got, "existing")
	require.Equal(t, []string{"0001-use-markdown-adrs.md"}, got["bare name"])

	// init's standard: no Q133 reading, whatever the spec names.
	q := initProject(t)
	write(t, q, pdfaSpec, pdfaAgentContextSpec)
	commitAll(t, q, "spec")
	require.Empty(t, q.StaleScenarios())
}

// A check that asserts the removed name's absence is not stale; a name
// inside a longer one is not the name.
func TestStaleScenarios_NamedIn(t *testing.T) {
	needles := []needle{{text: pdfa0031}, {text: "0031-use-v1-decision-format.md"}, {text: "ADR-0031", word: true}, {text: standardSupersedes, word: true}}
	for cmd, want := range map[string][]string{
		"! test -e " + pdfa0031:                                      nil,
		"test ! -e " + pdfa0031:                                      nil,
		"[ ! -f " + pdfa0031 + " ]":                                  nil,
		"[[ ! -f " + pdfa0031 + " ]]":                                nil,
		"! [ -e " + pdfa0031 + " ]":                                  nil,
		"test -f a && ! test -e " + pdfa0031 + " && echo ok":         nil,
		"! LC_ALL=C grep -q -x -F 'Supersedes: ADR-0001' d/*.md":     nil,
		"! { grep -l 'Supersedes: ADR-0001' d/*.md | grep -q .; }":   nil,
		"! grep -rqw ADR-0031 docs/":                                 nil,
		"test -f " + pdfa0031:                                        {pdfa0031, "0031-use-v1-decision-format.md"},
		"! test -e x && cat " + pdfa0031:                             {pdfa0031, "0031-use-v1-decision-format.md"},
		"f=$(grep -l 'Supersedes: ADR-0001' d/*.md); test -n \"$f\"": {standardSupersedes},
		"[ \"$(grep -c 'Supersedes: ADR-0001' d/*.md)\" = 1 ]":       {standardSupersedes},
		"grep -q ADR-0031 notes.md":                                  {"ADR-0031"},
		"grep -q ADR-00310 notes.md":                                 nil,
		"grep -q 'Supersedes: ADR-00010' x":                          nil,
		"test -f d/x0031-use-v1-decision-format.md":                  nil,
		"test -f d/0031-use-v1-decision-format.md.bak":               nil,
		"echo '! test -e' " + pdfa0031:                               {pdfa0031, "0031-use-v1-decision-format.md"},
	} {
		require.Equal(t, want, namedIn(cmd, needles), cmd)
	}
}

// Q131: a red scenario of the lasting spec that checks what the verb
// removed carries the pointer on the gate's and verify's red line
// (stale_names); a change's own delta scenario and a green one do not.
// Fails on rc.7c: no such field.
func TestStaleScenarios_GateAndVerifyPointer(t *testing.T) {
	p := pdfaProject(t)
	_, err := p.DecisionStandard(ctx())
	require.NoError(t, err)

	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	byRef := map[string]CheckResult{}
	for _, r := range g.Run.SpecFailed {
		byRef[r.Task] = r
	}
	r1 := byRef["agent-context/"+pdfaReq+"/"+pdfaScenario1]
	require.Equal(t, []string{pdfaOld0001, standardSupersedes}, r1.StaleNames)
	require.Contains(t, r1.StalePointer(), "which `ape decision standard` (the framework's migration v1.0.0_seq-08, APEX's standard ADR-0001) removed")
	require.Contains(t, r1.StalePointer(), " — retire it with a change whose REMOVED delta")
	require.Equal(t, []string{pdfa0031}, byRef["agent-context/"+pdfaReq+"/"+pdfaScenario2].StaleNames)
	require.Contains(t, g.Run.Why(), pdfaScenario2+" (checks what ape decision standard removed: retire it, never re-supersede ADR-0001)")

	// verify of a change touching agent-context runs its scenarios.
	addChange(t, p, "add-note", changeDoc("add-note", "agent-context", "### ADDED [agent-context] Requirement: Notes\nThe system SHALL keep notes.\n#### Scenario: notes\n- GIVEN x WHEN y THEN z\n- check: test -f "+pdfa0031+"\n",
		"- T1 note — check: test -f notes.txt\n"))
	commitAll(t, p, "plan: add-note")
	_, err = p.Start(ctx(), "add-note", RunOptions{})
	require.NoError(t, err)
	write(t, p, "notes.txt", "n\n")
	commitAll(t, p, "feat: notes")
	v, err := p.Verify(ctx(), "add-note", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green)
	marked := map[string][]string{}
	for _, r := range v.Results {
		if len(r.StaleNames) > 0 {
			marked[r.Task] = r.StaleNames
		}
	}
	require.Equal(t, map[string][]string{
		"agent-context/" + pdfaReq + "/" + pdfaScenario1: {pdfaOld0001, standardSupersedes},
		"agent-context/" + pdfaReq + "/" + pdfaScenario2: {pdfa0031},
	}, marked, "the change's own delta scenario is its own, not marked")
}
