package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// payloadStandard stands in for the framework payload's
// _apex/standards/adr-0001.md: a well-formed accepted ADR-0001 whose text
// is not ape's own copy.
const payloadStandard = `# ADR-0001: Record architecture decisions
Status: accepted
Date: 2026-10-20

## Context
The payload's text of the standard.

## Decision
Decisions are recorded as ADRs.

## Consequences
They are found in one place.

Enforced by: ape project check
`

// installPayload writes payloadStandard as the payload's standard and a
// framework.yaml naming v1.0.1, as `ape framework update` installs them,
// committed: a release that changes the standard.
func installPayload(t *testing.T, p *Project) {
	t.Helper()
	write(t, p, StandardADRPayload, payloadStandard)
	write(t, p, frameworkYAML, "framework:\n  version_tag: v1.0.1\n")
	commitAll(t, p, "chore(apex): update APEX framework to v1.0.1")
}

// initProject is a git repository `ape project init` made, with ape's
// pre-commit hook in place.
func initProject(t *testing.T) *Project {
	t.Helper()
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	asPerson(t)
	run(t, root, "init", "-q", "-b", "main")
	_, err := Init(ctx(), root, false)
	require.NoError(t, err)
	p, err := Open(root)
	require.NoError(t, err)
	return p
}

func standardNotices(p *Project) []Diagnostic { return p.standardADRNotices() }

// Q117, D211: ape's copy is the framework's shipped standard, a
// well-formed accepted ADR-0001 by ape's own decision checks.
func TestStandardADR_EmbeddedIsAWellFormedDecision(t *testing.T) {
	doc := EmbeddedStandardADR()
	require.Empty(t, ValidateDecision(StandardADRFile(), StandardADRFile(), doc))
	require.Equal(t, ADRAccepted, adrStatus(doc))
	require.True(t, strings.HasPrefix(doc, "# ADR-0001: Record architecture decisions\n"))
	require.Equal(t, "4742bb1ffce7ece94a0de4c6642e0a54ee3b419ed566b2452baabbc247e00399", bytesSHA256(doc),
		"the operator-approved text the payload ships (D211): change it only with the payload's")
	require.NotContains(t, doc, "\r", "the standard is written byte for byte: LF only")
}

// Q117: the installed payload's copy wins, with the framework's version;
// one that is not a well-formed accepted ADR-0001 does not.
func TestStandardADR_PayloadPreferred(t *testing.T) {
	root := t.TempDir()
	require.Equal(t, installedStandard{doc: EmbeddedStandardADR(), source: StandardSourceApe}, standardADR(root))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "standards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(StandardADRPayload)), []byte(payloadStandard), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, frameworkYAML), []byte("framework:\n  version_tag: v1.0.0-rc.8\n"), 0o644))
	require.Equal(t, installedStandard{doc: payloadStandard, source: StandardADRPayload, version: "v1.0.0-rc.8"}, standardADR(root))
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(StandardADRPayload)), []byte(strings.Replace(payloadStandard, "## Context\n", "", 1)), 0o644))
	require.Equal(t, StandardSourceApe, standardADR(root).source, "a malformed payload copy is not written")
}

// Q118: init writes the standard as ADR-0001, accepted and recorded by
// "APEX standard (ape project init)" with its version and digest, in
// init's commit; a second init keeps it.
func TestInit_WritesTheStandardADR(t *testing.T) {
	p := initProject(t)
	path := "development/decisions/" + StandardADRFile()
	require.Equal(t, EmbeddedStandardADR(), read(t, p, path))
	rec := mustRecord(t, p.Root, StandardADRID)
	require.Equal(t, "APEX standard (ape project init)", rec.By)
	require.Equal(t, &StandardRecord{FrameworkVersion: "", SHA256: bytesSHA256(EmbeddedStandardADR())}, rec.Standard)
	committed := run(t, p.Root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, path)
	require.Contains(t, committed, "_apex/state/decisions.json")
	clean(t, p, "project init")
	require.NotContains(t, findingMessages(p.Check()), "ADR-0001")
	require.Empty(t, standardNotices(p))
	_, err := p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
	_, err = p.OperatorInterfaceCheck()
	require.NoError(t, err)

	res, err := Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Equal(t, &StandardADRResult{Action: StandardKept, Path: path, Source: StandardSourceApe}, res.StandardADR)
	require.Empty(t, res.Created)
	require.Empty(t, res.Commit)
}

// Q118 with setup before init (D162): the installed payload's copy.
func TestInit_WritesThePayloadStandard(t *testing.T) {
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	run(t, root, "init", "-q", "-b", "main")
	p := &Project{Root: root}
	write(t, p, StandardADRPayload, payloadStandard)
	write(t, p, frameworkYAML, "framework:\n  version_tag: v1.0.0-rc.8\n")
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	require.Equal(t, StandardADRPayload, res.StandardADR.Source)
	require.Equal(t, payloadStandard, read(t, p, "development/decisions/"+StandardADRFile()))
	require.Equal(t, "v1.0.0-rc.8", mustRecord(t, root, StandardADRID).Standard.FrameworkVersion)
}

// Q118: a project's own ADR-0001 is left alone and reported.
func TestInit_LeavesTheProjectsOwnADR0001(t *testing.T) {
	p := fixture(t, baseFiles())
	own, err := p.NewDecision(ctx(), "use-sqlite", "Use SQLite", "")
	require.NoError(t, err)
	doc := read(t, p, own.Path)
	res, err := Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Equal(t, StandardDiffers, res.StandardADR.Action)
	require.Equal(t, own.Path, res.StandardADR.Path)
	require.Equal(t, doc, read(t, p, own.Path))
	require.NoFileExists(t, filepath.Join(p.Root, "development", "decisions", StandardADRFile()))
	require.Equal(t, []string{CodeStandardDiffers}, noticeCodesOf(standardNotices(p)))
	require.Contains(t, standardNotices(p)[0].Message, "the project's own decision")

	// ape decision standard does not replace it.
	sres, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardDiffers, sres.Action)
	require.Empty(t, sres.Commit)
	_, err = p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
}

// D206 Q123: a framework release that changes the standard — ADR-0001 is
// stale (a notice) until `ape project adr-standard` replaces it, in its
// own commit; decision.accepted-edited accepts that replacement.
func TestADRStandard_UpdateReplacesAndCommits(t *testing.T) {
	p := initProject(t)
	path := "development/decisions/" + StandardADRFile()
	installPayload(t, p)

	ns := standardNotices(p)
	require.Equal(t, []string{CodeStandardStale}, noticeCodesOf(ns))
	require.Contains(t, ns[0].Message, "that is not the installed standard (v1.0.1)")
	res, err := p.DecisionStandardCheck(ctx())
	require.Equal(t, CodeStandardPending, ErrorCode(err))
	require.Equal(t, StandardReplaced, res.Action)
	require.Equal(t, EmbeddedStandardADR(), read(t, p, path), "--check writes nothing")

	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardReplaced, res.Action)
	require.Equal(t, path, res.Replaces)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, "apex: ADR-0001 standard v1.0.1", subjects(t, p)[0])
	require.ElementsMatch(t, []string{path, "_apex/state/decisions.json"},
		strings.Split(run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "\n"))
	clean(t, p, "decision standard")
	require.Equal(t, payloadStandard, read(t, p, path))
	rec := mustRecord(t, p.Root, StandardADRID)
	require.Equal(t, "APEX standard (ape decision standard)", rec.By)
	require.Equal(t, &StandardRecord{FrameworkVersion: "v1.0.1", SHA256: bytesSHA256(payloadStandard)}, rec.Standard)
	require.NotContains(t, findingMessages(p.Check()), "ADR-0001", "the sanctioned replacement is no accepted-edited")
	require.Empty(t, standardNotices(p))

	// Idempotent.
	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardKept, res.Action)
	require.Empty(t, res.Commit)
	_, err = p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
}

// D206: a hand edit of the standard is still decision.accepted-edited; the
// recorded shipped digest is the one change that passes, whatever the
// body digest says.
func TestADRStandard_HandEditRefused(t *testing.T) {
	p := initProject(t)
	path := "development/decisions/" + StandardADRFile()
	write(t, p, path, strings.Replace(EmbeddedStandardADR(), "Anyone, person or model,", "Anyone", 1))
	commitAll(t, p, "docs: tweak the standard")
	require.Contains(t, findingMessages(p.Check()), "ADR-0001 was accepted and its text has changed since")

	// The shipped digest ape recorded passes even with a stale body digest.
	write(t, p, path, EmbeddedStandardADR())
	recs := p.loadDecisionRecords()
	rec := recs[StandardADRID]
	rec.BodySHA256 = "stale"
	recs[StandardADRID] = rec
	require.NoError(t, p.saveDecisionRecords(recs))
	require.NotContains(t, findingMessages(p.Check()), "ADR-0001 was accepted")
	require.True(t, standardADRUnchanged(StandardADRID, EmbeddedStandardADR(), rec))
	require.False(t, standardADRUnchanged("ADR-0002", EmbeddedStandardADR(), rec), "only ADR-0001 is the standard")
}

// D206: an accepted ADR that supersedes ADR-0001 opts the project out:
// the verb does nothing and says so; a proposed one does not opt out.
func TestADRStandard_OptOut(t *testing.T) {
	p := initProject(t)
	installPayload(t, p)
	own, err := p.NewDecision(ctx(), "our-adr-format", "Our ADR format", StandardADRID)
	require.NoError(t, err)
	res, err := p.DecisionStandardCheck(ctx())
	require.Equal(t, CodeStandardPending, ErrorCode(err), "a proposed supersede does not opt out")
	require.Equal(t, StandardReplaced, res.Action)

	fillADR(t, p, own.Path)
	_, err = p.AcceptDecision(ctx(), own.ID, "ann")
	require.NoError(t, err)
	head := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardOptedOut, res.Action)
	require.Equal(t, []string{"ADR-0002"}, res.OptedOutBy)
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"), "nothing is committed")
	_, err = p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
	ns := standardNotices(p)
	require.Equal(t, []string{CodeStandardSuperseded}, noticeCodesOf(ns))
	require.Contains(t, ns[0].Message, "ADR-0002 is accepted and supersedes ADR-0001")
}

// Q120 on a project an rc.7 lift made: its ADR-0001 is the apex-v0
// governance standard, superseded by a proposed ADR that only restated
// the format (pdfa's ADR-0031). `ape decision standard` (seq-08, D210)
// replaces ADR-0001 and removes the proposed one, listing where it is
// still cited, in its own commit. seq-07 is frozen: it leaves ADR-0001.
func TestDecisionStandard_ReplacesTheLiftedV0Standard(t *testing.T) {
	p := rc7LiftedProject(t, ADRProposed)
	_, err := p.OperatorInterfaceCheck()
	require.NoError(t, err, "seq-07 is frozen as rc.7 built it (D210): ADR-0001 is not its")
	require.Equal(t, []string{CodeStandardStale}, noticeCodesOf(standardNotices(p)))
	res, err := p.DecisionStandardCheck(ctx())
	require.Equal(t, CodeStandardPending, ErrorCode(err))
	require.Contains(t, err.Error(), "development/decisions/0001-use-markdown-adrs.md is not the installed standard")
	require.Contains(t, err.Error(), "the proposed ADR-0003 (development/decisions/0003-restate-the-format.md), which supersedes it, is to remove")
	require.Equal(t, StandardReplaced, res.Action)

	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, "apex: ADR-0001 standard ape's own copy", subjects(t, p)[0])
	clean(t, p, "decision standard")
	require.Equal(t, []RemovedADR{{ID: "ADR-0003", Path: "development/decisions/0003-restate-the-format.md", Citations: []string{"docs/notes.md:1"}}}, res.Removed)
	require.Equal(t, "development/decisions/0001-use-markdown-adrs.md", res.Replaces)
	committed := run(t, p.Root, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{
		"D\tdevelopment/decisions/0001-use-markdown-adrs.md", "D\tdevelopment/decisions/0003-restate-the-format.md",
		"A\tdevelopment/decisions/" + StandardADRFile(), "M\t_apex/state/decisions.json",
	} {
		require.Contains(t, committed, want)
	}
	require.Equal(t, EmbeddedStandardADR(), read(t, p, "development/decisions/"+StandardADRFile()))
	require.Equal(t, "See ADR-0003.\n", read(t, p, "docs/notes.md"), "citations are listed, not rewritten")
	require.Equal(t, StandardBy("ape decision standard"), mustRecord(t, p.Root, StandardADRID).By)
	require.NotContains(t, findingMessages(p.Check()), "ADR-0001", "the one sanctioned replacement")
	require.Empty(t, standardNotices(p))

	// Idempotent: nothing left.
	_, err = p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardKept, res.Action)
	require.Empty(t, res.Commit)
}

// Q120 + D206: an ACCEPTED supersede of the lifted ADR-0001 opts the
// project out: the verb leaves both and reports it.
func TestDecisionStandard_AcceptedSupersedeOptsOut(t *testing.T) {
	p := rc7LiftedProject(t, ADRAccepted)
	require.Equal(t, []string{CodeStandardSuperseded}, noticeCodesOf(standardNotices(p)))
	res, err := p.DecisionStandardCheck(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardOptedOut, res.Action)
	require.Equal(t, []string{"ADR-0003"}, res.OptedOutBy)
	head := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Empty(t, res.Commit)
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"))
	require.FileExists(t, filepath.Join(p.Root, "development", "decisions", "0003-restate-the-format.md"))
}

// A proposed supersede of a standard ape wrote is a person's pending
// opt-out: an update of the standard replaces ADR-0001 and leaves it.
func TestDecisionStandard_KeepsAProposedOptOut(t *testing.T) {
	p := initProject(t)
	prop, err := p.NewDecision(ctx(), "our-format", "Our format", StandardADRID)
	require.NoError(t, err)
	installPayload(t, p)
	res, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Equal(t, StandardReplaced, res.Action)
	require.Empty(t, res.Removed)
	require.FileExists(t, filepath.Join(p.Root, filepath.FromSlash(prop.Path)))
}

// rc7LiftedProject is a project the rc.7 lift made, seq-07
// done: ADR-0001 the converted apex-v0 governance standard, recorded by
// the lift and superseded by ADR-0003 (status as given), ADR-0002 the
// project's own.
func rc7LiftedProject(t *testing.T, status string) *Project {
	t.Helper()
	files := baseFiles()
	files["AGENTS.md"] += "\n" + OperatorLine + "\n"
	files["development/decisions/0001-use-markdown-adrs.md"] = "# ADR-0001: Use Markdown Architectural Decision Records\nStatus: superseded by ADR-0003\nDate: 2026-03-19\n" +
		"Lifted from: ADR-0001, development/governance/adrs/adr-0001_use-markdown-adrs.md at apex-v0-final\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: review\n"
	files["development/decisions/0002-use-sh.md"] = "# ADR-0002: Use sh\nStatus: accepted\nDate: 2026-03-20\n" +
		"Lifted from: ADR-0002, development/governance/adrs/adr-0002_use-sh.md at apex-v0-final\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: review\n"
	files["development/decisions/0003-restate-the-format.md"] = "# ADR-0003: Restate the format\nStatus: " + status + "\nDate: 2026-10-18\nSupersedes: ADR-0001\n\n" +
		"## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: review\n"
	files["docs/notes.md"] = "See ADR-0003.\n"
	p := fixture(t, files)
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	recs := p.loadDecisionRecords()
	for _, id := range []string{"ADR-0001", "ADR-0002"} {
		path, _ := p.adrFile(id)
		recs[id] = DecisionRecord{AcceptedAt: Stamp(), By: LiftBy, BodySHA256: adrBodyDigest(read(t, p, p.Rel(path)))}
	}
	if status == ADRAccepted {
		recs["ADR-0003"] = DecisionRecord{AcceptedAt: Stamp(), By: "ann", BodySHA256: adrBodyDigest(files["development/decisions/0003-restate-the-format.md"])}
	}
	require.NoError(t, p.saveDecisionRecords(recs))
	commitAll(t, p, "rc.7 lift and seq-07")
	return p
}

// Q119: a v0 project whose records hold no governance standard gets the
// standard inserted as ADR-0001, every record renumbered from 0002; the
// standard record, when there is one, is not converted, and its
// citations go to the standard.
func TestLift_StandardADR(t *testing.T) {
	t.Run("no governance standard", func(t *testing.T) {
		root := liftFixture(t, false)
		p := &Project{Root: root}
		// The project's own first decision, as tenax's ADR-0001 is.
		require.NoError(t, os.Remove(filepath.Join(root, "development", "governance", "adrs", "adr-0001_use-markdown-adrs.md")))
		write(t, p, "development/governance/adrs/adr-0001_nats-as-the-substrate.md", "---\nid: ADR-0001\nstatus: accepted\ntitle: NATS as the substrate\n---\n\nBody.\n")
		tagLift(t, root)
		res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
		require.NoError(t, err)
		require.Equal(t, StandardCreated, res.StandardADR.Action)
		require.Empty(t, res.StandardADR.Replaces)
		pairs := make([]string, 0, len(res.ADRs))
		for i := range res.ADRs {
			pairs = append(pairs, res.ADRs[i].Old+">"+res.ADRs[i].New)
		}
		require.Equal(t, []string{"ADR-0001>ADR-0002", "ADR-0002>ADR-0003", "ADR-0004>ADR-0004", "ADR-0006>ADR-0005", "ADR-0007>ADR-0006"}, pairs)
		require.FileExists(t, filepath.Join(root, "development", "decisions", "0002-nats-as-the-substrate.md"))
		require.Contains(t, run(t, root, "log", "-1", "--format=%B"), "- writes APEX's standard ADR-0001 (Record architecture decisions)\n- converts 5 ADR(s) into development/decisions/, renumbered from ADR-0002\n")
	})
	t.Run("the governance standard replaced", func(t *testing.T) {
		root := liftFixture(t, false)
		p := &Project{Root: root}
		write(t, p, "docs/adrs.md", "Our format is ADR-0001: development/governance/adrs/adr-0001_use-markdown-adrs.md.\n")
		tagLift(t, root)
		res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
		require.NoError(t, err)
		require.Equal(t, StandardReplaced, res.StandardADR.Action)
		require.Equal(t, "Our format is ADR-0001: development/decisions/"+StandardADRFile()+".\n", read(t, p, "docs/adrs.md"))
		msg := run(t, root, "log", "-1", "--format=%B")
		require.Contains(t, msg, "- writes APEX's standard ADR-0001 (Record architecture decisions), in place of the apex-v0 governance standard ADR-0001, "+
			"development/governance/adrs/adr-0001_use-markdown-adrs.md, which is not converted (its text stays at apex-v0-final)\n")
		require.Contains(t, msg, "  ADR-0001 -> ADR-0001 (standard)  "+StandardADRFile()+"\n")
		disp := read(t, p, "development/"+LiftDisposition)
		require.Contains(t, disp, "[ADR-0001](decisions/"+StandardADRFile()+") is APEX's standard ADR, Record architecture decisions (ape's own copy: no framework payload was installed), written by the lift, in place of")
		lp, err := Open(root)
		require.NoError(t, err)
		require.NotContains(t, findingMessages(lp.Check()), "ADR-0001")
		require.Empty(t, lp.standardADRNotices())
		_, err = lp.DecisionStandardCheck(ctx())
		require.NoError(t, err, "a project the rc.7b lift made has no standard due")
	})
	t.Run("a copy named as the canon file, without output_document", func(t *testing.T) {
		root := liftFixture(t, false)
		p := &Project{Root: root}
		write(t, p, "development/governance/adrs/adr-0001_use-markdown-adrs.md", "---\nid: ADR-0001\nstatus: accepted\ntitle: Use Markdown ADRs\n---\n\nBody.\n")
		tagLift(t, root)
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, StandardReplaced, res.StandardADR.Action)
	})
	t.Run("dry run shows the shift and writes nothing", func(t *testing.T) {
		root := liftFixture(t, true)
		res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
		require.NoError(t, err)
		require.Equal(t, StandardReplaced, res.StandardADR.Action)
		require.Equal(t, "ADR-0002", res.ADRs[0].New)
		require.NoDirExists(t, filepath.Join(root, "development", "decisions"))
	})
}

// tagLift commits the fixture's tree, tags it apex-v0-final and pushes
// the tag.
func tagLift(t *testing.T, root string) {
	t.Helper()
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "v0 changes")
	run(t, root, "tag", "-a", LiftTag, "-m", "apex-v0 final")
	run(t, root, "push", "-q", "origin", LiftTag)
}
