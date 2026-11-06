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

// The fence's transient class is NATS being away only (research's note,
// D808+): a lease value that does not decode refuses at once, in a run
// too, rather than wait the whole bound and blame NATS. The control (NATS
// down waits) is TestFenceMilestone_WaitsForNATSInARun.
func TestFenceMilestone_AnUndecodableLeaseRefusesAtOnce(t *testing.T) {
	p, _, js := gateRig(t)
	ctx := t.Context()
	kv, err := js.KeyValue(ctx, lease.LeasesBucket)
	require.NoError(t, err)
	_, err = kv.Put(ctx, "milestone.notes-svc.M2", []byte("not json"))
	require.NoError(t, err)
	start := time.Now()
	err = FenceMilestone(WithRunWait(ctx, time.Minute, io.Discard), p, "M2")
	require.ErrorContains(t, err, "does not decode is not a NATS outage")
	require.False(t, Transient(err))
	require.Less(t, time.Since(start), 5*time.Second, "at once")
}

// FINDING-H5-DRY-1 (eval's dry pass, D808+): an outage longer than the
// lease's TTL let it lapse while the fence waited, and the fence then
// refused "held by nobody (it lapsed)" — the run's renewer would have
// taken it again a tick later, but no renewer runs here (as in the
// fixture) and in a stage it raced the fence's first read. The earlier
// T1s missed it: their outage (about 2 s) was far inside the 30 s TTL. The
// T1: a 1 s TTL, every node down longer than that, the fence in a run
// waits, retakes the lapsed lease under retake's condition, and passes.
// The controls: outside a run the lapsed lease refuses as before; another
// checkout that claimed it meanwhile refuses at once.
func TestFenceMilestone_RetakesALeaseThatLapsedDuringTheWait(t *testing.T) {
	shortLeases(t, time.Hour) // no renewer tick inside the test
	p, c, js := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond

	outage := func() {
		t.Helper()
		for i := range 3 {
			c.Stop(i)
		}
		time.Sleep(2 * MilestoneTTL)
		for i := range 3 {
			c.Restart(i)
		}
	}
	var out syncBuf
	done := make(chan error, 1)
	for i := range 3 {
		c.Stop(i)
	}
	go func() { done <- FenceMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2") }()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "the push of milestone/M2: waiting for NATS") },
		15*time.Second, 50*time.Millisecond)
	time.Sleep(2 * MilestoneTTL)
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "the lapsed lease is taken again and the fence passes")
	case <-time.After(60 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}
	require.Contains(t, out.String(), "its lease lapsed while the fence waited and was taken again")

	// Control: outside a run, a lapsed lease refuses as before.
	outage()
	require.Eventually(t, func() bool {
		err := FenceMilestone(ctx, p, "M2")
		return err != nil && strings.Contains(err.Error(), "it lapsed")
	}, 30*time.Second, 200*time.Millisecond, "outside a run the lapsed lease refuses")

	// Control: another checkout claimed it after it lapsed — refused at
	// once, in a run too.
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	other, err := ls.Claim(ctx, "milestone.notes-svc.M2", "zed/other", time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Release(context.Background(), other) })
	start := time.Now()
	err = FenceMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2")
	require.ErrorContains(t, err, "zed/other")
	require.Less(t, time.Since(start), 5*time.Second, "at once")
}

// FINDING-H5-DRY-1 for the close PR's enqueue: the close lease lapses
// during an outage longer than its TTL with its keeper's tick not yet due;
// FenceClose in a run retakes it (counted, so the close re-checks main)
// and passes. The control: another checkout's close took it — refused.
func TestFenceClose_RetakesALeaseThatLapsedDuringTheWait(t *testing.T) {
	shortLeases(t, time.Hour)
	p, c, js := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	release, err := HoldClose(ctx, p, io.Discard, nil)
	require.NoError(t, err)
	t.Cleanup(release)

	var out syncBuf
	done := make(chan error, 1)
	for i := range 3 {
		c.Stop(i)
	}
	retakes := make(chan int, 1)
	go func() {
		n, err := FenceClose(WithRunWait(ctx, time.Minute, &out), p)
		retakes <- n
		done <- err
	}()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "the close PR's enqueue: waiting for NATS") }, 20*time.Second, 100*time.Millisecond)
	time.Sleep(2 * MilestoneTTL) // lapsed by the clock (LiveHolder's At+TTL) whatever the server still has
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "the lapsed close lease is taken again and the fence passes")
		// Taken again when the server had expired the key (counted: the
		// close re-checks main), or renewed at its own revision when the
		// server had not yet (a Windows runner, CI-V8LM2 check-3): nobody
		// could have held it between, so nothing to re-check.
		require.LessOrEqual(t, <-retakes, 1)
	case <-time.After(60 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}

	// Control: another close took it.
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "close.notes-svc")
		return err == nil && h == ""
	}, 15*time.Second, 100*time.Millisecond, "the retaken lease lapses again (1 s TTL, no keeper tick)")
	other, err := ls.Claim(ctx, "close.notes-svc", "zed/other", time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Release(context.Background(), other) })
	_, err = FenceClose(WithRunWait(ctx, time.Minute, &out), p)
	require.ErrorContains(t, err, "zed/other")
}

// FINDING-H5-DRY-1 with a renewer running (Q-H5-4, D808+): a keeper of the
// same checkout (a loop's, or the run's own hold) renews M's lease while
// the fence waits out an outage longer than the TTL. After NATS is back
// either may retake it first; the other adopts it (the same holder,
// renewed at the current revision), so the fence passes and the keeper
// goes on renewing — they never refuse each other.
func TestFenceMilestone_ALapseWithAKeeperRunning(t *testing.T) {
	shortLeases(t, 300*time.Millisecond)
	p, c, js := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	var keeperLog syncBuf
	stop, err := HoldMilestone(ctx, p, "M2", &keeperLog)
	require.NoError(t, err)
	defer stop()

	var out syncBuf
	done := make(chan error, 1)
	for i := range 3 {
		c.Stop(i)
	}
	go func() { done <- FenceMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2") }()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "the push of milestone/M2: waiting for NATS") },
		15*time.Second, 50*time.Millisecond)
	time.Sleep(3 * MilestoneTTL)
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "the fence passes beside the keeper")
	case <-time.After(60 * time.Second):
		t.Fatal("still waiting after NATS came back")
	}
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	_, before, err := ls.Holder(ctx, "milestone.notes-svc.M2")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		h, tok, err := ls.Holder(ctx, "milestone.notes-svc.M2")
		return err == nil && strings.HasPrefix(h, "dana/") && tok.Revision > before.Revision
	}, 10*time.Second, 100*time.Millisecond, "the keeper goes on renewing it")
	require.NotContains(t, keeperLog.String(), "was lost", "the keeper never lost it to the fence: %s", keeperLog.String())
	require.NoError(t, FenceMilestone(ctx, p, "M2"), "and it is still this checkout's")
}

// V8l-2 (FINDING-2A-4): a close waiting for another checkout's close
// connects afresh each poll, and returned the dial's error at once — M18's
// close ended exit 1 one second into 2a's o2. The T1: bob's close waits
// behind ann's, every node goes down for longer than a poll, and inside a
// run it says it waits for NATS, goes on waiting for ann's close, and
// takes the lease once ann's ends. The control: outside a run the same
// outage refuses at once, transient (the no-outage control is
// TestWaitClose_WaitsForTheOtherClose).
func TestWaitClose_WaitsForNATSInARun(t *testing.T) {
	_, c, _ := gateRig(t)
	ctx := t.Context()
	defer func(d, e time.Duration) { gateWaitEvery, CloseWaitPoll = d, e }(gateWaitEvery, CloseWaitPoll)
	gateWaitEvery, CloseWaitPoll = 200*time.Millisecond, 200*time.Millisecond
	ann, bob := checkout(t, c.URLs(), "ann"), checkout(t, c.URLs(), "bob")
	release, err := HoldClose(ctx, ann, io.Discard, nil)
	require.NoError(t, err)

	var out syncBuf
	got := make(chan error, 1)
	var bobRelease func()
	go func() {
		r, err := WaitClose(WithRunWait(ctx, time.Minute, &out), bob, &out, nil, time.Minute)
		bobRelease = r
		got <- err
	}()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "waiting for ann/") }, 10*time.Second, 50*time.Millisecond, "bob waits for ann's close")

	for i := range 3 {
		c.Stop(i)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = WaitClose(cctx, bob, io.Discard, nil, time.Minute)
	cancel()
	require.True(t, Transient(err), "control: outside a run it refuses at once: %v", err)
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "the close lease: waiting for NATS") }, 10*time.Second, 50*time.Millisecond, "inside the run it says it waits for NATS")
	time.Sleep(2 * time.Second) // ten polls
	select {
	case err := <-got:
		t.Fatalf("bob's close returned during the outage: %v", err)
	default:
	}
	for i := range 3 {
		c.Restart(i)
	}
	time.Sleep(time.Second)
	select {
	case err := <-got:
		t.Fatalf("bob's close returned before ann's ended: %v", err)
	default:
	}
	release()
	select {
	case err := <-got:
		require.NoError(t, err, "ann's close ended: bob's takes the lease")
		require.Contains(t, mustHolder(t, c.URLs(), "close.notes-svc"), "bob/")
		bobRelease()
	case <-time.After(60 * time.Second):
		t.Fatalf("bob's close still waits after ann's ended:\n%s", out.String())
	}
}

// V8l-2's sweep (research, D822+): HoldMilestone, at a gated run's start
// (a close, a change's start or archive, a take-in), dialled once and
// refused. The T1: every node down, it waits inside a run and holds the
// lease once they are back. The control: outside a run it refuses at once.
func TestHoldMilestone_WaitsForNATSInARun(t *testing.T) {
	p, c, _ := gateRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	for i := range 3 {
		c.Stop(i)
	}
	_, err := HoldMilestone(ctx, p, "M2", io.Discard)
	require.True(t, Transient(err), "control: outside a run it refuses at once: %v", err)

	var out syncBuf
	type held struct {
		stop func()
		err  error
	}
	got := make(chan held, 1)
	go func() {
		s, err := HoldMilestone(WithRunWait(ctx, time.Minute, &out), p, "M2", &out)
		got <- held{s, err}
	}()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "M2's lease for the run: waiting for NATS") }, 10*time.Second, 50*time.Millisecond)
	for i := range 3 {
		c.Restart(i)
	}
	select {
	case h := <-got:
		require.NoError(t, h.err, "holds once NATS is back")
		h.stop()
	case <-time.After(60 * time.Second):
		t.Fatalf("still waiting after NATS came back:\n%s", out.String())
	}
}

// downErr: an error NATS being away explains is the transient refusal a
// run waits out; a refusal by the server is the grant gap, never waited;
// anything else is unchanged.
func TestDownErr(t *testing.T) {
	s := &milestones{root: t.TempDir(), denied: &denials{}}
	require.True(t, Transient(s.downErr("x", fmt.Errorf("claim: %w", nats.ErrNoResponders))))
	require.True(t, Transient(s.downErr("x", fmt.Errorf("claim: %w", nats.ErrConnectionClosed))))
	other := errors.New("lease value does not decode")
	require.Equal(t, other, s.downErr("x", other), "control: not NATS away")
	require.NoError(t, s.downErr("x", nil))
	s.denied.err = errors.New(`Permissions Violation for Publish to "$KV.ape-leases.close.x"`)
	err := s.downErr("x", fmt.Errorf("claim: %w", context.DeadlineExceeded))
	require.False(t, Transient(err), "a denied call's timeout is the grant gap: %v", err)
	require.ErrorContains(t, err, "the server refused this credential's call")
}
