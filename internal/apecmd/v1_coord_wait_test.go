package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// v1.5 stage 1a: ape run wait in a member project wakes on the run-end
// the supervisor publishes once the exit is recorded, not on its poll. With
// the poll and the level read both an hour, it returns within seconds of
// the event. The control: the same project without its block, and the
// same hour's poll, still reports the run running when its 2 s wait ends.
func TestRunWait_WakesOnPublishedRunEnd(t *testing.T) {
	poll, level := runWaitPoll, runWaitLevelEvery
	runWaitPoll, runWaitLevelEvery = time.Hour, time.Hour
	t.Cleanup(func() { runWaitPoll, runWaitLevelEvery = poll, level })

	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	root := v1Repo(t)
	hpath := handlePath(root, "r1")
	require.NoError(t, os.MkdirAll(filepath.Dir(hpath), 0o755))
	running := `{"id":"r1","argv":["ape","task","apex-foo"],"dir":"` + root + `","root":"` + root + `","log":"x","stdout":"y","started_at":"2026-10-31T12:00:00Z"}` // no supervisor_pid yet: going, never lost
	require.NoError(t, os.WriteFile(hpath, []byte(running), 0o644))

	stdout, _, err := v1Exec(t, newRunCmd, "wait", "r1", "--wait", "2s", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), "no block: an hour's poll sees nothing in 2 s\n%s", stdout)

	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
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
		time.Sleep(time.Second)
		ended := strings.Replace(running, `"started_at"`, `"exit_code":0,"started_at"`, 1)
		_ = os.WriteFile(hpath, []byte(ended), 0o644)
		p.Emit(ctx, apexv1.Event{Event: apexv1.EventRunEnd, Run: "r1"}, nil)
	}()
	start := time.Now()
	stdout, _, err = v1Exec(t, newRunCmd, "wait", "r1", "--wait", "30s", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Less(t, time.Since(start), 10*time.Second, "woke on the published run-end, not on a poll")
	require.Contains(t, stdout, "run r1 ended with exit 0")
}
