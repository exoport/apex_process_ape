//go:build unix

package natstest

import (
	"os"
	"os/exec"
	"path/filepath"
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
