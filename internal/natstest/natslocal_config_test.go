//go:build unix

package natstest

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D814: natslocal.sh's NATSLOCAL_INCLUDE puts an operator's resolver.conf
// into every node's config (an enforcing cluster beside the first, with
// its own ports, dir and cluster name). The control: without it, the
// configs are as before (no include, cluster ape-local).
func TestNatslocal_Include(t *testing.T) {
	script, err := filepath.Abs("../../deploy/natslocal/natslocal.sh")
	require.NoError(t, err)
	inc := filepath.Join(t.TempDir(), "resolver.conf")
	require.NoError(t, os.WriteFile(inc, []byte("operator: x\n"), 0o600))
	run := func(env ...string) string {
		t.Helper()
		dir := t.TempDir()
		c := exec.Command("bash", script, "config")
		c.Env = append(os.Environ(), append([]string{"NATSLOCAL_DIR=" + dir, "NATSLOCAL_CLIENT_BASE=34221", "NATSLOCAL_ROUTE_BASE=36221"}, env...)...)
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s", out)
		conf, err := os.ReadFile(filepath.Join(dir, "n2.conf"))
		require.NoError(t, err)
		return string(conf)
	}
	conf := run("NATSLOCAL_INCLUDE="+inc, "NATSLOCAL_CLUSTER=ape-2a")
	require.Contains(t, conf, "include include.conf")
	require.Contains(t, conf, "name: ape-2a")
	require.Contains(t, conf, "listen: 127.0.0.1:34223")
	require.Contains(t, conf, "nats://127.0.0.1:36222")
	plain := run()
	require.False(t, strings.Contains(plain, "include"), "control: no include")
	require.Contains(t, plain, "name: ape-local")
}

// PORT-EPHEMERAL (HOLD-2C-O1): a port inside the ephemeral range can be
// taken by an outgoing connection while its node is down, and the restart
// failed to bind. natslocal.sh refuses such a port unless it is reserved;
// NATSLOCAL_ALLOW_EPHEMERAL=1 goes on with a warning. The range and the
// reserved list come from files here, so the test is the host's no matter
// its own settings. The control: bases below the range pass.
func TestNatslocal_RefusesEphemeralPorts(t *testing.T) {
	script, err := filepath.Abs("../../deploy/natslocal/natslocal.sh")
	require.NoError(t, err)
	dir := t.TempDir()
	rng, res := filepath.Join(dir, "range"), filepath.Join(dir, "reserved")
	require.NoError(t, os.WriteFile(rng, []byte("20000\t30000\n"), 0o600))
	check := func(reserved string, env ...string) (string, error) {
		t.Helper()
		require.NoError(t, os.WriteFile(res, []byte(reserved+"\n"), 0o600))
		c := exec.Command("bash", script, "check-ports")
		c.Env = append(os.Environ(), append([]string{"NATSLOCAL_DIR=" + t.TempDir(), "NATSLOCAL_PORT_RANGE_FILE=" + rng, "NATSLOCAL_RESERVED_PORTS_FILE=" + res}, env...)...)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	in := []string{"NATSLOCAL_CLIENT_BASE=24221", "NATSLOCAL_ROUTE_BASE=26221"}
	out, err := check("", in...)
	require.Error(t, err, out)
	require.Contains(t, out, "port(s) 24222 26222 24223 26223 24224 26224 inside the ephemeral range 20000-30000")
	out, err = check("24222-24224,26222,26223,26224", in...)
	require.NoError(t, err, "reserved: %s", out)
	out, err = check("24222-24224", in...)
	require.Error(t, err, out)
	require.Contains(t, out, "port(s) 26222 26223 26224 inside", "only the unreserved ones")
	out, err = check("", append(in, "NATSLOCAL_ALLOW_EPHEMERAL=1")...)
	require.NoError(t, err, out)
	require.Contains(t, out, "going on: NATSLOCAL_ALLOW_EPHEMERAL=1")
	out, err = check("", "NATSLOCAL_CLIENT_BASE=14221", "NATSLOCAL_ROUTE_BASE=16221")
	require.NoError(t, err, "control: the defaults sit below the range: %s", out)
	require.Empty(t, out)
}

// PORT-EPHEMERAL: `start N` reported nothing when its node could not bind
// (exit 0, the FTL line only in the node's log). Now it waits for the node
// to answer or exit, and an exit is reported with the node's own line,
// exit 1. The control: the same start on a free port answers, exit 0.
func TestNatslocal_StartReportsABindFailure(t *testing.T) {
	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("no nats-server on PATH")
	}
	script, err := filepath.Abs("../../deploy/natslocal/natslocal.sh")
	require.NoError(t, err)
	free := freeTCPPort(t)
	var lc net.ListenConfig
	held, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	heldPort := held.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a TCP listener's address
	run := func(clientBase int, verb ...string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		env := append(os.Environ(), "NATSLOCAL_DIR="+dir, "NATS_SERVER="+bin, "NATSLOCAL_ALLOW_EPHEMERAL=1",
			"NATSLOCAL_CLIENT_BASE="+strconv.Itoa(clientBase-1), "NATSLOCAL_ROUTE_BASE="+strconv.Itoa(freeTCPPort(t)-1))
		var out []byte
		for _, v := range [][]string{{"config"}, verb} {
			c := exec.Command("bash", append([]string{script}, v...)...)
			c.Env = env
			out, err = c.CombinedOutput()
			if err != nil {
				break
			}
		}
		stop := exec.Command("bash", script, "stop", "1")
		stop.Env = env
		_ = stop.Run()
		return string(out), err
	}
	out, err := run(heldPort, "start", "1")
	require.Error(t, err, out)
	require.Contains(t, out, "natslocal: n1 did not start:")
	require.Contains(t, out, "address already in use")
	out, err = run(free, "start", "1")
	require.NoError(t, err, "control: a free port: %s", out)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a TCP listener's address
}
