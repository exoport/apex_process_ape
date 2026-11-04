package member

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
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// gateRig is a member project on a three-node cluster (Stop/Restart keep
// each node's port and store: an outage), holding M2's lease.
func gateRig(t *testing.T) (*apexv1.Project, *natstest.Cluster, jetstream.JetStream) {
	t.Helper()
	c := natstest.RunCluster(t, 3)
	nc, err := nats.Connect(c.URLs())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))
	require.NoError(t, lease.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", c.URLs(), 1), false)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	_, err = ClaimMilestone(ctx, p, "M2")
	require.NoError(t, err)
	return p, c, js
}

// D800+: a gating run waits for NATS rather than refusing. The T1: NATS
// goes down, the run says it waits for NATS, and it proceeds once NATS is
// back. The control: Gate, the call's check (--wait 0), refuses at once
// with a transient refusal.
func TestGateWait_RidesOutAnOutage(t *testing.T) {
	p, c, _ := gateRig(t)
	ctx := t.Context()
	require.NoError(t, Gate(ctx, p, "change start", "M2", time.Now()))
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond

	for i := range 3 {
		c.Stop(i)
	}
	err := Gate(ctx, p, "change start", "M2", time.Now())
	require.True(t, Transient(err), "control: the call's check refuses at once: %v", err)

	var out syncBuf
	done := make(chan error, 1)
	go func() { done <- GateWait(ctx, p, "change start", "M2", time.Minute, &out) }()
	time.Sleep(1500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned during the outage: %v", err)
	default:
	}
	require.Contains(t, out.String(), "change start: waiting for NATS")
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "proceeds once NATS is back")
	case <-time.After(30 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}
}

// D800+ (research's fail-closed): a pause state that cannot be read is
// never "not paused". The T1: a timed-out read refuses as transient, and
// a run waits for it, saying so, up to its bound. The controls: a clean
// read with no pause proceeds at once; a person's pause refuses at once,
// with no wait.
func TestGateWait_APauseStateThatCannotBeRead(t *testing.T) {
	p, _, js := gateRig(t)
	ctx := t.Context()
	defer func(d, e time.Duration) { pauseCheckTimeout, gateWaitEvery = d, e }(pauseCheckTimeout, gateWaitEvery)
	gateWaitEvery = 100 * time.Millisecond

	pauseCheckTimeout = time.Nanosecond
	err := Gate(ctx, p, "milestone close", "M2", time.Now())
	require.True(t, Transient(err), "a timed-out read is not 'not paused': %v", err)
	require.Contains(t, err.Error(), "pause state could not be read")
	var out syncBuf
	start := time.Now()
	err = GateWait(ctx, p, "milestone close", "M2", 600*time.Millisecond, &out)
	require.True(t, Transient(err), "past the bound the refusal returns")
	require.GreaterOrEqual(t, time.Since(start), 600*time.Millisecond, "it waited")
	require.Contains(t, out.String(), "milestone close: waiting for the pause state")

	pauseCheckTimeout = 2 * time.Second
	start = time.Now()
	require.NoError(t, GateWait(ctx, p, "milestone close", "M2", time.Minute, &out), "control: a clean read, no pause")
	require.Less(t, time.Since(start), 5*time.Second)

	_, err = js.Publish(ctx, "ape.evt.dana.notes-svc.project.M2."+apexv1.EventMilestonePaused, []byte(`{"kind":"person","by":"dana","reason":"r"}`))
	require.NoError(t, err)
	start = time.Now()
	err = GateWait(ctx, p, "milestone close", "M2", time.Minute, &out)
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, apexv1.CodeCoordMilestonePaused, fe.Code, "control: a pause that holds refuses")
	require.Less(t, time.Since(start), 5*time.Second, "at once: a pause is not waited out")
}
