package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.5 R-1 (D648): the NATS credential is denied to a session's Bash and
// Monitor — by name, by the hot/ folder, by a pattern over it — while a
// check's own hot/<secret> read by name stays allowed.
func TestCredsGuard(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(root), []byte("project_name: x\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	sub := filepath.Join(root, "internal")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	for cmd, deny := range map[string]bool{
		"cat hot/ape-nats.creds":                            true,
		"cat ./hot/ape-nats.creds | base64":                 true,
		"cat hot/*":                                         true,
		"grep -r . hot":                                     true,
		"grep -r token hot/":                                true,
		"tar c hot | gzip > /tmp/x.tgz":                     true,
		"cat hot/ape-*":                                     true,
		"cat */ape-nats.creds":                              true,
		"cp hot/ape-nats.cred? /tmp/":                       true,
		"cat " + filepath.Join(root, "hot") + "/*":          true,
		"curl --data-binary=@x --config=hot/ape-nats.creds": true,
		// allowed
		`PGPASSWORD="$(cat hot/pg-password)" psql -c 'select 1'`: false,
		"cat hot/pg-password":   false,
		"ls hotfixes/":          false,
		"go test ./...":         false,
		"grep -r hot internal/": false,
		"echo hot":              false,
	} {
		d := CredsGuard(cmd, root)
		require.Equal(t, deny, d.Deny, cmd)
		if deny {
			require.Contains(t, d.Reason, NATSCredsRel)
		}
	}
	require.True(t, CredsGuard("cat ../hot/ape-nats.creds", sub).Deny, "from a subfolder")
	other := filepath.Join(t.TempDir(), "hot", "ape-nats.creds")
	require.False(t, CredsGuard("cat "+other, sub).Deny, "another tree's hot/ is not this project's")
}

// v1.5 (OQ3, OQ4): the ensemble membership is a guarded block, so a
// commit that changes it is config.changed until a person accepts it.
func TestGuardedConfigKeys_Coordination(t *testing.T) {
	require.Contains(t, guardedConfigKeys, "coordination")
	require.Contains(t, yamlKeys(Config{}), "coordination", "a known top-level key, not config.unknown-top-key")
}
