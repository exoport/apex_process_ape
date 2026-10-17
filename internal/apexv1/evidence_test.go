package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// H23: evidence: archive commits the final verify's output inside the
// archive squash, one file per check.
func TestEvidence_Archive(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "evidence: archive\n"
	p := reviewedWaveFrom(t, files)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	dir := filepath.Join(res.ArchivedTo, "evidence")
	ev := read(t, p, dir+"/01-t1.txt")
	require.Contains(t, ev, "check: T1\nkind: task\ncommand: ./wave.sh\nexit: 0\n")
	require.Contains(t, ev, "output: complete, 5 bytes\n\nwave\n")
	committed := run(t, p.Root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, dir+"/01-t1.txt", "inside the squash")
	entries, err := os.ReadDir(filepath.Join(p.Root, dir))
	require.NoError(t, err)
	st, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Len(t, entries, len(st.Verify.Results), "one file per check")
	clean(t, p, "an archive with evidence")
}

func TestEvidence_OffByDefault(t *testing.T) {
	p := reviewedWaveFrom(t, baseFiles())
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(p.Root, res.ArchivedTo, "evidence"))
}

// Output over the cap keeps its head and tail and names the full log; a
// log that no longer matches its recorded sha256 is not passed off as the
// check's output.
func TestEvidence_FileCapsAndRefusesAStaleLog(t *testing.T) {
	p := fixture(t, baseFiles())
	limit := DefaultCaps().EvidenceLog
	require.Equal(t, 1<<20, limit)
	big := strings.Repeat("h", limit) + strings.Repeat("m", 1000) + strings.Repeat("t", limit)
	write(t, p, "_apex/state/logs/x/T1.log", big)
	sum := sha256.Sum256([]byte(big))
	r := CheckResult{Task: "T1", Cmd: "./big.sh", Exit: 1, Secs: 2, OutputSHA256: hex.EncodeToString(sum[:]), Log: "_apex/state/logs/x/T1.log"}
	ev := p.evidenceFile(&r)
	require.Less(t, len(ev), limit+2048)
	require.Contains(t, ev, "over the 1 MiB cap (caps.evidence_log) — head and tail kept; the full output is _apex/state/logs/x/T1.log (sha256 "+r.OutputSHA256)
	require.Contains(t, ev, "bytes omitted — full output: _apex/state/logs/x/T1.log")
	require.NotContains(t, ev, "mmm")
	require.True(t, strings.HasSuffix(ev, "ttt"))

	r.OutputSHA256 = "0000"
	require.Contains(t, p.evidenceFile(&r), "output: unavailable — the log no longer holds what this check recorded")
	r.Log = "_apex/state/logs/x/gone.log"
	require.Contains(t, p.evidenceFile(&r), "output: unavailable — the log is gone")
}

// A green close writes the gate run's evidence, committed with the close
// record, and carries an accept_red record milestones.json holds.
func TestEvidence_MilestoneClose(t *testing.T) {
	files := suiteFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: ./suite.sh\nevidence: archive\n"
	p := fixture(t, files)
	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "development/archive/milestones/M1/evidence", g.Evidence)
	ev := read(t, p, g.Evidence+"/01-gate-m1.txt")
	require.Contains(t, ev, "command: ./gate.sh\nexit: 0\n")
	committed := run(t, p.Root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, g.Evidence+"/01-gate-m1.txt")
	require.Contains(t, committed, "development/roadmap.md", "with the close record")
	require.Equal(t, "apex: M1 done", subjects(t, p)[0])
	require.Empty(t, p.ArchivedIDs(), "the milestones folder is no archived change")
	clean(t, p, "a close with evidence")

	// accept_red (H22's record) is read generically and carried.
	write(t, p, "_apex/state/milestones.json", `{"M1": {"result": "red", "since": null, "accept_red": {"reason": "flaky rig", "by": "ann", "at": "2026-10-16T00:00:00Z"}}}`)
	_, err = p.writeMilestoneEvidence("M1", g.checks)
	require.NoError(t, err)
	require.Contains(t, read(t, p, g.Evidence+"/accept-red.json"), `"reason": "flaky rig"`)
}
