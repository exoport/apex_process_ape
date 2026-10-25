package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteLiveText(t *testing.T) {
	md, n := rewriteLiveMarkdown("#### Scenario: a\n- check (live): ./a.sh\n- check ( live ):x\n" +
		"- T1 serve — check (live): ./svc.sh\n- T2 keep — regression (live): make e2e\n" +
		"```\n- check (live): an example\n```\n- check: ./plain.sh (live)\n")
	require.Equal(t, 4, n)
	require.Equal(t, "#### Scenario: a\n- check (full): ./a.sh\n- check (full):x\n"+
		"- T1 serve — check (full): ./svc.sh\n- T2 keep — regression (full): make e2e\n"+
		"```\n- check (live): an example\n```\n- check: ./plain.sh (live)\n", md, "a fenced example and a command's own words stay")

	gate, n := rewriteLiveGate("#!/bin/sh\n# scenario (live): [greeting] Serve / served\n  ## scenario (live): x\necho '# scenario (live): y'\n")
	require.Equal(t, 2, n)
	require.Equal(t, "#!/bin/sh\n# scenario (full): [greeting] Serve / served\n  ## scenario (full): x\necho '# scenario (live): y'\n", gate)
}

// v1.4 (D526): `ape project rewrite-live` rewrites the specs, a draft
// change and an open gate's comment in one commit; the commit is no
// spec.edited and the rewritten approved gate file no gate drift; --check
// then passes. A started change is left, and a commit that borrows the
// subject for another spec edit is still spec.edited.
func TestRewriteLive(t *testing.T) {
	files := baseFiles()
	files["development/specs/greeting/spec.md"] = liveSpec
	files["gate.sh"] = "#!/bin/sh\n# scenario (live): [greeting] Serve / served\nexit 1\n"
	p := fixture(t, files)
	// The v1 epoch: provenance judges the commits after it.
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", strings.Replace(addedWave, "- check: ./wave.sh", "- check (live): ./wave.sh", 1),
		"- T1 add wave — check (live): ./wave.sh\n"))
	addChange(t, p, "add-bow", changeDoc("add-bow", "greeting", "none — a test\n", "- T1 bow — check (live): ./bow.sh\n"))
	require.NoError(t, SaveState(p.StateFile("add-bow"), &State{ID: "add-bow", Status: StatusInProgress, BaseSHA: "x"}))
	commitAll(t, p, "plan")
	require.NotEmpty(t, p.liveNotices(), "(live) warns")

	res, err := p.RewriteLiveCheck()
	require.Equal(t, CodeLiveLeft, failCode(t, err))
	require.Len(t, res.Rewritten, 3)
	require.Len(t, res.Left, 1)
	require.Equal(t, "development/changes/add-bow/change.md", res.Left[0].Path)

	res, err = p.RewriteLive(ctx())
	require.NoError(t, err)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, []string{"M1"}, res.Approvals)
	require.Equal(t, RewriteLiveSubject, subjects(t, p)[0])
	require.Contains(t, read(t, p, "development/specs/greeting/spec.md"), "- check (full): ./svc.sh")
	require.Contains(t, read(t, p, "gate.sh"), "# scenario (full): [greeting] Serve / served")
	require.Contains(t, read(t, p, "development/changes/add-wave/change.md"), "— check (full): ./wave.sh")
	require.Contains(t, read(t, p, "development/changes/add-bow/change.md"), "— check (live): ./bow.sh", "a started change is left")
	clean(t, p, "rewrite-live")

	_, err = p.RewriteLiveCheck()
	require.NoError(t, err, "nothing left: the migration's check passes")
	codes := strings.Join(codesOf(p.Check()), " ")
	require.NotContains(t, codes, "spec.edited")
	require.NotContains(t, codes, CodeGateChanged)
	require.NoError(t, p.GatePreflight(ctx(), "M1"), "the approved gate still runs")

	again, err := p.RewriteLive(ctx())
	require.NoError(t, err)
	require.Empty(t, again.Commit, "idempotent")

	// The subject is no licence: another spec edit under it is judged.
	write(t, p, "development/specs/greeting/spec.md", read(t, p, "development/specs/greeting/spec.md")+"\n")
	run(t, p.Root, "commit", "-qam", RewriteLiveSubject)
	require.Contains(t, strings.Join(codesOf(p.Check()), " "), "spec.edited")
}

// v1.4 (D526): an archive merges a delta's (live) check as (full).
func TestMergeDelta_LiveIsWrittenFull(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", strings.Replace(addedWave, "- check: ./wave.sh", "- check (live): ./wave.sh", 1),
		"- T1 add wave — check: ./wave.sh\n"))
	ups, conflicts := p.MergeDelta(p.mustChange(t, "add-wave"))
	require.Empty(t, conflicts)
	require.Len(t, ups, 1)
	require.Contains(t, ups[0].content, "- check (full): ./wave.sh")
	require.NotContains(t, ups[0].content, "(live)")
}
