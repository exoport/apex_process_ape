package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// The v1.5 coordination verbs through the CLI on one JetStream server:
// declare twice (idempotent) and open an epoch; a member checkout replays
// its events twice and each is stored once; with no supervisor a health
// report exits 1; a malformed ack token exits 1; outside a member project
// with no URL, a verb exits 2.
func TestCoordVerbs(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	epochFile := filepath.Join(t.TempDir(), "sup.epoch")

	for range 2 {
		stdout, _, err := v1Exec(t, newNatsCmd, "declare", "--nats-url", url, "--replicas", "1")
		require.NoError(t, err)
		require.Contains(t, stdout, "declared the coordination layout")
	}
	stdout, _, err := v1Exec(t, newNatsCmd, "declare", "--nats-url", url, "--replicas", "1", "--new-epoch")
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	stdout, _, err = v1Exec(t, newNatsCmd, "declare", "--nats-url", url, "--replicas", "1", "--new-epoch", "--epoch-file", epochFile, "--window", "1s")
	require.NoError(t, err)
	require.Contains(t, stdout, "epoch 1; re-claim window open for 1s")
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	e, err := ls.Epoch(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), e)

	root := v1Repo(t)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	for _, ev := range []string{apexv1.EventGateGreen, apexv1.EventMilestoneDone} {
		p.Emit(ctx, apexv1.Event{Event: ev, Milestone: "M1"}, nil)
	}
	n := len(p.EventsSince(0))
	require.Positive(t, n)
	for range 2 {
		stdout, _, err = v1Exec(t, newNatsCmd, "replay", "--cwd", root)
		require.NoError(t, err, stdout)
		require.Contains(t, stdout, "replayed ")
	}
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(n), info.State.Msgs, "each event stored once over two replays")

	_, _, err = v1Exec(t, newHealthCmd, "report", "--nats-url", url, "--ensemble", "shop", "--subject", "ape.coord.shop.status", "--what", "test")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	_, _, err = v1Exec(t, newConductorCmd, "ack", "not-a-token", "--nats-url", url)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))

	t.Setenv("APE_NATS_URL", "")
	_, _, err = v1Exec(t, newConductorCmd, "ack", "$JS.ACK.x", "--cwd", t.TempDir())
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.ErrorContains(t, err, apexv1.CodeCoordMembership)
}
