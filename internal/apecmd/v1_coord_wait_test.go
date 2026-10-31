package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// Plan step 4 through the CLI: with the poll and the level recheck both an
// hour, ape status --wait --until event in a member project returns as
// soon as an event is logged (it was published, and the wait woke on it).
// The control: the same project without its block times out (75).
func TestStatusWait_WakesOnPublishedEvent(t *testing.T) {
	poll, level := statusWaitPoll, statusWaitLevelEvery
	statusWaitPoll, statusWaitLevelEvery = time.Hour, time.Hour
	t.Cleanup(func() { statusWaitPoll, statusWaitLevelEvery = poll, level })

	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	root := v1Repo(t)
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	stdout, _, err := v1Exec(t, newStatusCmd, "--wait", "2s", "--until", "event", "--cwd", root)
	require.Equal(t, 75, exitCodeOf(t, err), "no block: an hour's poll sees nothing in 2 s\n%s", stdout)

	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	p, err := apexv1.Open(root)
	require.NoError(t, err)

	go func() {
		time.Sleep(500 * time.Millisecond)
		p.Emit(ctx, apexv1.Event{Event: apexv1.EventGateGreen, Milestone: "M1"}, nil)
	}()
	start := time.Now()
	stdout, _, err = v1Exec(t, newStatusCmd, "--wait", "30s", "--until", "event", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Less(t, time.Since(start), 10*time.Second, "woke on the event, not on a poll")
	require.Contains(t, stdout, "gate-green")
}
