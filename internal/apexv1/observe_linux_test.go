package apexv1

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseTCPLine(t *testing.T) {
	s, inode, ok := parseTCPLine("   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 123456 1 0 100 0 0 10 0")
	require.True(t, ok)
	require.Equal(t, "123456", inode)
	require.Equal(t, tcpListen, s.state)
	require.Equal(t, 8080, s.localPort)
	s, _, ok = parseTCPLine("   1: 0100007F:A1B2 0100007F:1538 01 00000000:00000000 00:00000000 00000000  1000        0 99 1 0 20 4 30 10 -1")
	require.True(t, ok)
	require.Equal(t, 5432, s.remotePort)
	require.True(t, s.remote.IsLoopback())
	s, _, ok = parseTCPLine("   2: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 77 1 0 100 0 0 10 0")
	require.True(t, ok, "tcp6 ::1")
	require.Equal(t, 8080, s.localPort)
	_, _, ok = parseTCPLine("   3: 0100007F:1F90 00000000:0000 06 00000000:00000000 00:00000000 00000000  1000 0 5 1")
	require.False(t, ok, "TIME_WAIT is not read")
}

// D623, live: a check run as a "go test binary" (python3 linked as
// pkg.test) that listens in-process, one that starts a separate listening
// process, and one that connects to a listener outside the check.
func TestObserved_LiveCheck(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "pkg.test")
	require.NoError(t, os.Symlink(py, bin))
	run := func(script string) *Observed {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "t.py"), []byte(script), 0o644))
		r := RunCheck(context.Background(), dir, "./pkg.test t.py", "", time.Minute)
		require.Equal(t, 0, r.Exit, "%v", r.Tail)
		require.NotNil(t, r.Observed)
		return r.Observed
	}

	o := run("import socket,time\ns=socket.socket(); s.bind(('127.0.0.1',0)); s.listen()\nc=socket.create_connection(s.getsockname()); a,_=s.accept()\ntime.sleep(1.2)\n")
	require.Equal(t, "go", o.Runner)
	require.GreaterOrEqual(t, o.Samples, 2)
	require.Equal(t, 1, o.InProcessListeners)
	require.False(t, o.Needs(), "%+v", o)

	// The child is python3 by its own path: sys.executable would be pkg.test.
	o = run("import subprocess,time\np=subprocess.Popen(['" + py + "','-c','import socket,time\\ns=socket.socket(); s.bind((\"127.0.0.1\",0)); s.listen(); time.sleep(5)'])\ntime.sleep(1.2); p.kill()\n")
	require.True(t, o.Needs(), "%+v", o)
	require.Len(t, o.Services, 1)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	addr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := strconv.Itoa(addr.Port)
	o = run("import socket,time\nc=socket.create_connection(('127.0.0.1'," + port + "))\ntime.sleep(1.2)\n")
	require.Equal(t, []string{"127.0.0.1:" + port}, o.Outside)
	require.True(t, o.Needs())
}
