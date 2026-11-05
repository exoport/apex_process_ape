package member

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// V8l (1c3's outage #1): a fence inside a gated verb's run waits for NATS
// as its entry does. The T1: every node down, FenceMilestone under
// WithRunWait says it waits for NATS and passes once the nodes are back.
// The controls: without a run wait it refuses at once (transient), and a
// lease another checkout holds refuses at once even inside a run.
func TestFenceMilestone_WaitsForNATSInARun(t *testing.T) {
	p, c, js := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	require.NoError(t, FenceMilestone(ctx, p, "M2"), "held, NATS up: the push goes")

	for i := range 3 {
		c.Stop(i)
	}
	start := time.Now()
	err := FenceMilestone(ctx, p, "M2")
	require.True(t, Transient(err), "control: outside a run it refuses at once: %v", err)
	require.Less(t, time.Since(start), 10*time.Second)

	var out syncBuf
	done := make(chan error, 1)
	go func() { done <- FenceMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2") }()
	time.Sleep(1500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned during the outage: %v", err)
	default:
	}
	require.Contains(t, out.String(), "the push of milestone/M2: waiting for NATS")
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "the fence passes once NATS is back, the lease still this checkout's")
	case <-time.After(30 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}

	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	_, err = ReleaseMilestone(ctx, p, "M2")
	require.NoError(t, err)
	other, err := ls.Claim(ctx, "milestone.notes-svc.M2", "zed/other", time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Release(context.Background(), other) })
	start = time.Now()
	err = FenceMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2")
	require.ErrorContains(t, err, "zed/other", "control: another's lease refuses, in a run too")
	require.False(t, Transient(err))
	require.Less(t, time.Since(start), 5*time.Second, "at once")
}

// V8l for the close PR's enqueue: FenceClose inside a run waits for NATS
// rather than refusing (close.lost, retry). The T1: the close lease held,
// every node down, FenceClose under WithRunWait says it waits and passes
// once the nodes are back. The control: outside a run it refuses at once.
func TestFenceClose_WaitsForNATSInARun(t *testing.T) {
	p, c, _ := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	release, err := HoldClose(ctx, p, io.Discard, nil)
	require.NoError(t, err)
	t.Cleanup(release)
	_, err = FenceClose(ctx, p)
	require.NoError(t, err, "held, NATS up")

	for i := range 3 {
		c.Stop(i)
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = FenceClose(fctx, p)
	cancel()
	require.ErrorContains(t, err, "could not be read before the enqueue", "control: outside a run it refuses")

	var out syncBuf
	done := make(chan error, 1)
	go func() { _, err := FenceClose(WithRunWait(ctx, time.Minute, &out), p); done <- err }()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "the close PR's enqueue: waiting for NATS") }, 20*time.Second, 100*time.Millisecond)
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "passes once NATS is back")
	case <-time.After(60 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}
}

// CI-V8LM check-1 (windows): right after the nodes came back, the fence's
// first try reached NATS before JetStream answered ("lease: bind
// ape-leases: context deadline exceeded") and was refused as "no lease
// buckets". JetStream not answering is NATS not back yet (transient, so a
// run waits); a bucket that is not there still refuses as before.
func TestJetStreamUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadline", fmt.Errorf("lease: bind ape-leases: %w", context.DeadlineExceeded), true},
		{"timeout", fmt.Errorf("x: %w", nats.ErrTimeout), true},
		{"no-responders", fmt.Errorf("x: %w", nats.ErrNoResponders), true},
		{"jetstream-503", fmt.Errorf("x: %w", jetstream.ErrJetStreamNotEnabled), true},
		{"control-bucket-missing", fmt.Errorf("lease: bind ape-leases: %w", jetstream.ErrBucketNotFound), false},
		{"control-other", errors.New("permission denied"), false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, jetStreamUnavailable(tc.err)) })
	}

	// Live: a NATS without JetStream answers the bind with a 503 → transient.
	// The control: JetStream up, the lease buckets never declared → the
	// "no lease buckets" refusal, not transient.
	for _, tc := range []struct {
		name      string
		url       string
		transient bool
	}{
		{"jetstream-not-answering", natstest.RunServer(t), true},
		{"control-no-buckets", natstest.RunJetStreamServer(t), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", tc.url, 1), false)
			creds, _ := natstest.MintStandaloneCreds(t, "dana")
			data, err := os.ReadFile(creds)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
			err = FenceMilestone(t.Context(), p, "M2")
			require.Error(t, err)
			require.Equal(t, tc.transient, Transient(err), "%v", err)
			if !tc.transient {
				require.ErrorContains(t, err, "no lease buckets")
			}
		})
	}
}
