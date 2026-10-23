package apecmd

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// procCmdline reads /proc/<pid>/cmdline as ps prints args: the
// NUL-separated argv joined by spaces.
func TestProcCmdline(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "123"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "123", "cmdline"),
		[]byte("/usr/local/bin/ape\x00run\x00supervise\x00--handle\x00/p/_apex/state/runs/detached/abc.json\x00"), 0o644))
	args, err := procCmdline(root, 123)
	require.NoError(t, err)
	require.Equal(t, "/usr/local/bin/ape run supervise --handle /p/_apex/state/runs/detached/abc.json", args)
	require.Equal(t, supervisorOurs, supervisorArgs(args, "abc"))
	require.Equal(t, supervisorNotOurs, supervisorArgs(args, "abd"), "another run's supervisor")

	_, err = procCmdline(root, 124)
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "125"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "125", "cmdline"), nil, 0o644))
	args, err = procCmdline(root, 125)
	require.NoError(t, err)
	require.Equal(t, supervisorNotOurs, supervisorArgs(args, "abc"), "an empty command line (a zombie) is not ours")

	require.Equal(t, supervisorNotOurs, supervisorArgs("sleep 60", "abc"))
	require.Equal(t, supervisorNotOurs, supervisorArgs("vim /p/_apex/state/runs/detached/abc.json", "abc"))
}

// On Linux the probe reads /proc and never runs ps: a broken ps changes
// nothing. A live pid /proc cannot show falls back to ps.
func TestProbeSupervisor_ReadsProc(t *testing.T) {
	_, path := detachedProject(t, "live", 0)
	h, err := readHandle(path)
	require.NoError(t, err)
	ctx := context.Background()

	psStub(t, "echo 'run supervise --handle /x/detached/live.json'; exit 1")
	require.Equal(t, supervisorOurs, probeSupervisor(ctx, h), "read from /proc, ps unused")
	require.Equal(t, supervisorNotOurs, probeSupervisor(ctx, &detachedHandle{ID: "live", SupervisorPID: stranger(t)}), "a reused pid")
	require.Equal(t, supervisorNotOurs, probeSupervisor(ctx, &detachedHandle{ID: "live", SupervisorPID: reapedPID(t)}), "a pid that is gone")
	require.Equal(t, supervisorNotOurs, probeSupervisor(ctx, &detachedHandle{ID: "live"}), "no pid")

	// A zombie: ended, not yet reaped. Its /proc entry is there, its
	// command line empty.
	z := exec.Command("sh", "-c", "exit 0", "run supervise --handle", path)
	require.NoError(t, z.Start())
	t.Cleanup(func() { _ = z.Wait() })
	require.Eventually(t, func() bool {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(z.Process.Pid) + "/stat")
		_, rest, _ := strings.Cut(string(data), ") ")
		return err == nil && strings.HasPrefix(rest, "Z")
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, supervisorNotOurs, probeSupervisor(ctx, &detachedHandle{ID: "live", SupervisorPID: z.Process.Pid}), "a zombie")

	// /proc unreadable for a live pid: ps decides, and its failure is an
	// unknown, not a loss.
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	require.Equal(t, supervisorUnknown, probeSupervisor(ctx, h), "the failing ps stub")
	t.Setenv("PATH", strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[1])
	require.Equal(t, supervisorOurs, probeSupervisor(ctx, h), "the real ps")
}
