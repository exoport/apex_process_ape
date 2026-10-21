package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fillADR replaces the skeleton's comment placeholders with prose.
func fillADR(t *testing.T, p *Project, path string) {
	t.Helper()
	doc := read(t, p, path)
	for _, hint := range []string{"<!-- the forces at play -->", "<!-- what we decided -->", "<!-- what becomes easier or harder -->"} {
		doc = strings.Replace(doc, hint, "Some prose.", 1)
	}
	write(t, p, path, doc)
	commitAll(t, p, "docs: fill "+path)
}

// Backlog item 7: accept fixes an ADR's text; a different decision
// supersedes it, writing both Status lines.
func TestDecision_AcceptAndSupersede(t *testing.T) {
	p := fixture(t, baseFiles())
	first, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	fillADR(t, p, first.Path)

	acc, err := p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.NoError(t, err)
	require.Equal(t, ADRAccepted, acc.Status)
	require.Contains(t, read(t, p, first.Path), "Status: accepted")
	require.Equal(t, "apex: ADR-0001 accepted", subjects(t, p)[0])
	require.Contains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "_apex/state/decisions.json")
	clean(t, p, "decision accept")
	require.Empty(t, p.Check())

	_, err = p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.ErrorContains(t, err, "only a proposed decision is accepted")

	// Editing the accepted text is caught; restoring it clears the finding.
	doc := read(t, p, first.Path)
	write(t, p, first.Path, strings.Replace(doc, "Some prose.", "Different prose.", 1))
	require.Contains(t, findingMessages(p.Check()), "ADR-0001 was accepted and its text has changed since")
	write(t, p, first.Path, doc)
	require.Empty(t, p.Check())

	// Supersede: both Status lines, one commit, project check still green.
	second, err := p.NewDecision(ctx(), "use-bash", "Use bash", "ADR-0001")
	require.NoError(t, err)
	require.Equal(t, "ADR-0002", second.ID)
	require.Equal(t, "ADR-0001", second.Supersedes)
	require.Contains(t, read(t, p, second.Path), "Supersedes: ADR-0001")
	require.Contains(t, read(t, p, first.Path), "Status: superseded by ADR-0002")
	require.Equal(t, "apex: ADR-0002 proposed, supersedes ADR-0001", subjects(t, p)[0])
	require.ElementsMatch(t, []string{first.Path, second.Path},
		strings.Split(run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "\n"))
	clean(t, p, "decision new --supersedes")
	require.Empty(t, findingMessages(p.Check()), "a superseded ADR's Status change is not an edit")

	_, err = p.NewDecision(ctx(), "use-zsh", "", "ADR-0001")
	require.ErrorContains(t, err, "only a proposed or accepted decision can be superseded")
	_, err = p.NewDecision(ctx(), "use-fish", "", "ADR-0099")
	require.ErrorContains(t, err, "no decision ADR-0099")
}

// An ADR accepted by hand (no ape record) is held to the text git first
// committed it accepted with.
func TestDecision_HandAcceptedIsHeldToItsFirstAcceptedCommit(t *testing.T) {
	p := fixture(t, baseFiles())
	d, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	fillADR(t, p, d.Path)
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Status: proposed", "Status: accepted", 1))
	commitAll(t, p, "docs: accept ADR-0001 by hand")
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, CodeDecisionUnrecorded, fs[0].Code, "accepted by hand is not recorded (H2)")
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Some prose.", "Edited.", 1))
	commitAll(t, p, "docs: edit an accepted ADR")
	require.Contains(t, findingMessages(p.Check()), "was accepted and its text has changed since")
	_, err = p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.ErrorContains(t, err, "restore it, then record it", "an edited ADR is not recorded as it is")

	// Restored, a person records it as it is.
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Edited.", "Some prose.", 1))
	commitAll(t, p, "docs: restore ADR-0001")
	res, err := p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.NoError(t, err)
	require.Equal(t, "apex: ADR-0001 accepted (recorded)", subjects(t, p)[0])
	require.NotEmpty(t, res.Commit)
	require.Empty(t, p.Check())
	require.Equal(t, "ann", p.loadDecisionRecords()["ADR-0001"].By)
}

// The lift's records (by "lift") make a converted ADR recorded, and hold
// it to its text from then on.
func TestRecordLiftedDecisions(t *testing.T) {
	p := fixture(t, baseFiles())
	d, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	fillADR(t, p, d.Path)
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Status: proposed", "Status: accepted", 1))
	commitAll(t, p, "lift")
	require.Contains(t, findingMessages(p.Check()), "ADR-0001 is accepted, but ape has no record")
	require.Error(t, p.RecordLiftedDecisions([]string{"ADR-0009"}))
	require.NoError(t, p.RecordLiftedDecisions([]string{"ADR-0001"}))
	require.Equal(t, LiftBy, p.loadDecisionRecords()["ADR-0001"].By)
	require.Contains(t, run(t, p.Root, "status", "--porcelain"), "decisions.json", "written, not committed")
	commitAll(t, p, "lift records")
	require.Empty(t, p.Check())
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Some prose.", "Edited.", 1))
	require.Contains(t, findingMessages(p.Check()), "was accepted and its text has changed since")
	d2, err := p.NewDecision(ctx(), "use-awk", "", "")
	require.NoError(t, err)
	require.ErrorContains(t, p.RecordLiftedDecisions([]string{d2.ID}), "not accepted")
}

func TestDecision_SupersedesMustNameAnADR(t *testing.T) {
	p := fixture(t, baseFiles())
	d, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	fillADR(t, p, d.Path)
	write(t, p, d.Path, strings.Replace(read(t, p, d.Path), "Date:", "Supersedes: ADR-0042\nDate:", 1))
	require.Contains(t, findingMessages(p.Check()), "Supersedes: ADR-0042 names no decision")
}

// Q155: decision accept refuses the skeleton `decision new` wrote, and
// project check notices an ADR accepted with it before that refusal — its
// text is fixed, so the remedy is a new ADR.
func TestDecision_PlaceholdersAreNotADecision(t *testing.T) {
	p := fixture(t, baseFiles())
	d, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	require.NotContains(t, noticeCodesOf(p.Notices()), CodeDecisionPlaceholderAccepted, "a proposed skeleton is a draft")

	_, err = p.AcceptDecision(ctx(), "ADR-0001", "ann")
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, CodeDecisionPlaceholder, fe.Code)
	require.Contains(t, fe.Msg, "ADR-0001 still holds the skeleton's placeholder <!-- the forces at play -->, <!-- what we decided -->, <!-- what becomes easier or harder -->")
	require.Contains(t, read(t, p, d.Path), "Status: proposed", "nothing written")

	// One section left as the skeleton is still refused.
	doc := read(t, p, d.Path)
	write(t, p, d.Path, strings.Replace(strings.Replace(doc, "<!-- the forces at play -->", "Prose.", 1), "<!-- what we decided -->", "Prose.", 1))
	commitAll(t, p, "docs: two of three")
	_, err = p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.ErrorContains(t, err, "placeholder <!-- what becomes easier or harder -->")

	// Accepted by hand before the refusal: a notice, never a finding.
	write(t, p, d.Path, strings.Replace(doc, "Status: proposed", "Status: accepted", 1))
	commitAll(t, p, "docs: accepted by hand")
	var got []Diagnostic
	for _, n := range p.Notices() {
		if n.Code == CodeDecisionPlaceholderAccepted {
			got = append(got, n)
		}
	}
	require.Len(t, got, 1)
	require.Equal(t, SeverityNotice, got[0].Severity)
	require.Contains(t, got[0].Message, "ADR-0001 was accepted with the skeleton's placeholder")
	require.Contains(t, got[0].Fix, "ape decision new <slug> --supersedes ADR-0001")
	require.NotContains(t, findingMessages(p.Check()), "placeholder")
}
