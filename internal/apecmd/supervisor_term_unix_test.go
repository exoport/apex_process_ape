//go:build !windows

package apecmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D736: a supervisor's exit asks its coordinators to stop (SIGTERM), so
// each releases coord.<ens> on its clean exit and a successor's
// coordinator claims it at once, not one TTL later. A stand-in coordinator
// marks its clean exit: Terminate lets it run; the control, Stop (the
// replacement of one judged dead), kills it, so no mark; one that ignores
// TERM is killed after the grace.
func TestCoordProcess_TerminateLetsItRelease(t *testing.T) {
	start := func(t *testing.T, script string) (coordProcess, string) {
		t.Helper()
		mark := filepath.Join(t.TempDir(), "released")
		c := exec.Command("sh", "-c", script)
		c.Env = append(os.Environ(), "MARK="+mark)
		require.NoError(t, c.Start())
		time.Sleep(200 * time.Millisecond) // the trap is set
		return coordProcess{cmd: c}, mark
	}
	const releasing = `trap 'touch "$MARK"; exit 0' TERM; while :; do sleep 0.05; done`

	p, mark := start(t, releasing)
	p.Terminate()
	require.FileExists(t, mark, "asked to stop: it released and exited cleanly")

	p, mark = start(t, releasing)
	p.Stop()
	require.NoFileExists(t, mark, "the control: Stop kills, nothing released")

	old := coordTermGrace
	coordTermGrace = 300 * time.Millisecond
	t.Cleanup(func() { coordTermGrace = old })
	p, mark = start(t, `trap '' TERM; while :; do sleep 0.05; done`)
	begin := time.Now()
	p.Terminate()
	require.Less(t, time.Since(begin), 3*time.Second, "one that ignores TERM is killed after the grace")
	require.NoFileExists(t, mark)
}
