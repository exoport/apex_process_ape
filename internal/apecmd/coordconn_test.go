package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// V12b: --nats-creds is honoured in a member project without --nats-url
// (it was dropped for hot/ape-nats.creds), so a supervisor can give the
// coordinators it starts their own credential. The T1: the project's
// credential is one the server rejects; with --nats-creds naming a good
// one the connection opens. The control: without it, the project's is
// used, and refused.
func TestCoordFlags_NATSCredsInAMemberProject(t *testing.T) {
	url, acct := natstest.StartOperator(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "state"), 0o755))
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), []byte("project_name: notes\ncoordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: "+url+"\n"), 0o644))
	rejected, _ := natstest.MintStandaloneCreds(t, "stranger")
	data, err := os.ReadFile(rejected)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	good, _ := natstest.MintCreds(t, acct, "coordinator", nil, nil)

	nc, m, err := (&coordFlags{cwd: root, creds: good}).connect(context.Background(), "t")
	require.NoError(t, err, "--nats-creds is used in a member project")
	require.NotNil(t, m)
	require.Equal(t, good, m.Creds)
	nc.Close()

	_, _, err = (&coordFlags{cwd: root}).connect(context.Background(), "t")
	require.Error(t, err, "control: the project's own credential, rejected")
}
