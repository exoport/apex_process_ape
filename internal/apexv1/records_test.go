package apexv1

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.5 stage 1a step 9b (section 5.4): in a coordinated project a
// milestone's records are made on main and reach it through record PRs
// in main's queue; a record that did not reach its PR is sent by the next
// record verb; the claim record is a commit with no change on the
// remote's main; once merged, the local main fast-forwards to the queue's
// merge.
func TestRecordPRs(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	remoteMain := run(t, bare, "rev-parse", "main")

	// From milestone/M1: the record is made on main all the same.
	run(t, p.Root, "switch", "-q", "milestone/M1")
	id, err := p.NewMilestoneWith(ctx(), "Second", "./gate.sh", "", MilestoneBudgets{})
	require.NoError(t, err)
	require.Equal(t, "M2", id)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	tip := run(t, p.Root, "rev-parse", "main")
	require.Equal(t, "apex: M2 proposed", run(t, p.Root, "log", "-1", "--format=%s", tip))
	r := p.PendingRecord("M2")
	require.NotNil(t, r)
	require.Equal(t, "apex/record-M2-proposed-"+short(tip)+"→main", f.opened[0])
	require.Equal(t, tip, run(t, bare, "rev-parse", r.Head))
	require.Equal(t, remoteMain, run(t, bare, "rev-parse", "main"), "main moves only through its queue")

	// A record whose PR fails is sent by the next record verb.
	f.openErr = errors.New("HTTP 502")
	_, err = p.NewMilestoneWith(ctx(), "Third", "./gate.sh", "", MilestoneBudgets{})
	require.Equal(t, CodeIntegrationPR, failCode(t, err))
	f.openErr = nil
	m3 := run(t, p.Root, "rev-parse", "main")
	require.NoError(t, p.prepareRecordOnMain(ctx(), "test"))
	require.Contains(t, f.opened, "apex/record-M3-proposed-"+short(m3)+"→main")
	n := len(f.opened)
	require.NoError(t, p.prepareRecordOnMain(ctx(), "test"))
	require.Len(t, f.opened, n, "sent once")

	// The claim record changes nothing and touches no local branch.
	c, err := p.RecordClaim(ctx(), "M1", "dana/abc123")
	require.NoError(t, err)
	require.Equal(t, "apex: M1 claimed by dana/abc123", run(t, bare, "log", "-1", "--format=%s", c.Head))
	require.Equal(t, run(t, bare, "rev-parse", "main^{tree}"), run(t, bare, "rev-parse", c.Head+"^{tree}"))
	require.Equal(t, m3, run(t, p.Root, "rev-parse", "main"))

	// The queue merged M3's record (it carries M2's): main fast-forwards.
	mergeOnRemote(t, bare, "main", "apex/record-M3-proposed-"+short(m3))
	require.NoError(t, p.prepareRecordOnMain(ctx(), "test"))
	require.Equal(t, run(t, bare, "rev-parse", "main"), run(t, p.Root, "rev-parse", "main"))
	require.True(t, strings.HasPrefix(run(t, p.Root, "log", "-1", "--format=%s", "main"), "Merge"))
}

func TestRecordPRs_UncoordinatedSendsNone(t *testing.T) {
	p := fixture(t, baseFiles())
	withOrigin(t, p)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	_, err := p.NewMilestoneWith(ctx(), "Second", "./gate.sh", "", MilestoneBudgets{})
	require.NoError(t, err)
	c, err := p.RecordClaim(ctx(), "M1", "dana/abc123")
	require.NoError(t, err)
	require.Nil(t, c)
	require.Empty(t, f.opened)
}
