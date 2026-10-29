package apexv1

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// v1.4 (B1, T5): a check has a TMPDIR of its own, short, removed when it
// ends; its size is recorded.
func TestRunCheck_OwnTmpdir(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	r := RunCheck(ctx(), dir, `echo "$TMPDIR"; head -c 2048 /dev/zero > "$TMPDIR/blob"`, "", 0)
	require.Equal(t, 0, r.Exit, r.Tail)
	require.Len(t, r.Tail, 1)
	tmp := r.Tail[0]
	require.True(t, strings.HasPrefix(tmp, checkTmpRoot+string(os.PathSeparator)), tmp)
	require.LessOrEqual(t, len(tmp), maxCheckTmp, "short enough for a test's unix socket under it (T5)")
	require.NoDirExists(t, tmp, "removed when the check ended")
	require.Equal(t, int64(2048), r.TmpBytes)
	other := RunCheck(ctx(), dir, `echo "$TMPDIR"`, "", 0)
	require.NotEqual(t, tmp, other.Tail[0], "each check its own")
}

func TestTmpRoot(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	base := string(filepath.Separator) + filepath.Join("run", "user", "x")
	require.Equal(t, filepath.Join(base, "ape-"+uid), tmpRoot(base))
	long := "/tmp/claude-1000/-home-someone--dev-a-long-project/scratch"
	if runtime.GOOS == "windows" {
		require.Equal(t, filepath.Join(long, "ape-"+uid), tmpRoot(long), "no unix-socket path limit to keep on Windows: no /tmp")
		return
	}
	require.Equal(t, "/tmp/ape-"+uid, tmpRoot(long), "too long for a socket under it: /tmp")
}

// T3, Q7: COMPOSE_PROJECT_NAME is the check's own, over the person's.
func TestRunCheck_ComposeProject(t *testing.T) {
	needSh(t)
	t.Setenv("COMPOSE_PROJECT_NAME", "devstack")
	dir := filepath.Join(t.TempDir(), "My Project")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	r := RunCheck(ctx(), dir, `echo "$COMPOSE_PROJECT_NAME"`, "", 0)
	require.Equal(t, 0, r.Exit)
	require.Regexp(t, `^apex-my-project-[0-9a-f]{6}$`, r.Tail[0])
	require.Equal(t, r.Tail[0], r.Compose)
}

// T1: a process outside the check's scope that holds its TMPDIR — snap's
// Chromium moves itself into a scope of its own — is ended and named.
func TestRunCheck_TmpdirHolderOutsideTheScopeIsEnded(t *testing.T) {
	needSh(t)
	if _, d, _ := newScope(); d == "" {
		t.Skip("no systemd user manager here")
	}
	run, err := exec.LookPath("systemd-run")
	require.NoError(t, err)
	cmd := "cd \"$TMPDIR\" && setsid " + run + " --user --scope -q --collect --expand-environment=no sleep 305 & sleep 0.5; exit 0"
	r := RunCheck(ctx(), t.TempDir(), cmd, "", 0)
	require.Equal(t, 0, r.Exit, r.Tail)
	var named string
	for _, l := range r.Leftovers {
		if strings.Contains(l, "sleep 305") {
			named = l
		}
	}
	require.Contains(t, named, "held the check's TMPDIR", r.Leftovers)
	pid, _ := strconv.Atoi(strings.Fields(named)[0])
	require.Eventually(t, func() bool { return !processAlive(pid) }, 5*time.Second, 50*time.Millisecond)
}

// A red check whose port another process holds is unrun (port-held),
// never red for the product.
func TestRunCheck_PortHeldOutsideIsUnrun(t *testing.T) {
	needSh(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := addr.Port
	cmd := "echo 'listen tcp 127.0.0.1:" + strconv.Itoa(port) + ": bind: address already in use'; exit 1"
	r := RunCheck(ctx(), t.TempDir(), cmd, "", 0)
	if listenerPID(port) == 0 {
		t.Skip("this platform names no listener")
	}
	require.Equal(t, ExitUnrun, r.Exit, r.Tail)
	require.Equal(t, ReasonUnrun, r.Reason)
	require.Contains(t, r.Detail, "port "+strconv.Itoa(port)+" is held by "+strconv.Itoa(os.Getpid()))
	require.Contains(t, r.Detail, "port-held")

	// A red check that says nothing of a port stays red.
	r = RunCheck(ctx(), t.TempDir(), "echo FAIL; exit 1", "", 0)
	require.Equal(t, 1, r.Exit)
}

func TestTakenPort(t *testing.T) {
	for line, want := range map[string]int{
		"listen tcp :8080: bind: address already in use":                                      8080,
		"Error response from daemon: Bind for 0.0.0.0:5432 failed: port is already allocated": 5432,
		"Error: listen EADDRINUSE: address already in use :::3000":                            3000,
		"OSError: [Errno 98] Address already in use":                                          0,
		"FAIL: got 500 from http://127.0.0.1:8080":                                            0,
	} {
		require.Equal(t, want, takenPort(line), line)
	}
}

// With a container runtime: a container of the check's compose project is
// removed when it ends, its use recorded; any other it created is reported
// and left.
func TestRunCheck_ContainersOfItsComposeProjectAreRemoved(t *testing.T) {
	needSh(t)
	if !dockerAvailable(ctx()) {
		t.Skip("no docker daemon")
	}
	if exec.Command("docker", "image", "inspect", "nginx:alpine").Run() != nil {
		t.Skip("no nginx:alpine image (pulling one is not this test's)")
	}
	name := "ape-test-unlabelled-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	cmd := `docker run -d --label com.docker.compose.project="$COMPOSE_PROJECT_NAME" nginx:alpine sleep 60 >/dev/null && ` +
		`docker run -d --name ` + name + ` nginx:alpine sleep 60 >/dev/null`
	r := RunCheck(ctx(), t.TempDir(), cmd, "", 0)
	require.Equal(t, 0, r.Exit, r.Tail)
	require.Len(t, r.Containers, 2, "%+v", r.Containers)
	byOwner := map[string]ContainerUse{}
	for _, c := range r.Containers {
		byOwner[c.Owner] = c
	}
	require.True(t, byOwner["compose"].Removed)
	require.False(t, byOwner[""].Removed, "another container is reported, never removed")
	require.Equal(t, name, byOwner[""].Name)
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+r.Compose).Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(out)), "the check's project left nothing")
}
