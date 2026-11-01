package member

import (
	"context"
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
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// leaseCluster is a JetStream server with the lease buckets declared and
// epoch 1 open, as `ape nats declare --new-epoch` leaves them.
func leaseCluster(t *testing.T) string {
	t.Helper()
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	s, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, s.SetEpoch(ctx, 1))
	return url
}

// checkout is a member project on url whose credential's user is user.
func checkout(t *testing.T, url, user string) *apexv1.Project {
	t.Helper()
	p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", url, 1), false)
	creds, _ := natstest.MintStandaloneCreds(t, user)
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	return p
}

func leaseCode(t *testing.T, err error) string {
	t.Helper()
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe, "%v", err)
	return fe.Code
}

func shortLeases(t *testing.T, ttl, every time.Duration) {
	t.Helper()
	oldTTL, oldEvery := MilestoneTTL, MilestoneRenewEvery
	MilestoneTTL, MilestoneRenewEvery = ttl, every
	t.Cleanup(func() { MilestoneTTL, MilestoneRenewEvery = oldTTL, oldEvery })
}

// Stage 1a step 4: a milestone has one holder, a checkout; a change of it
// starts only in the holder's checkout; a claim lapses with no run
// renewing it and is taken again by its own checkout, never by another's
// gate; a release ends it.
func TestMilestoneLease_ClaimGateRelease(t *testing.T) {
	shortLeases(t, time.Second, 300*time.Millisecond)
	ctx := context.Background()
	now := time.Now()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "bob")

	l, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(l.Holder, "ann/"), l.Holder)
	again, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err, "a checkout's claim of its own milestone keeps it")
	require.Greater(t, again.Token.Revision, l.Token.Revision)

	_, err = ClaimMilestone(ctx, bob, "M1")
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err))
	require.ErrorContains(t, err, "M1 is held by "+l.Holder)
	_, err = ClaimMilestone(ctx, bob, "M2")
	require.NoError(t, err, "another milestone is free")

	require.NoError(t, Gate(ctx, ann, "change start", "M1", now))
	require.ErrorContains(t, Gate(ctx, bob, "change start", "M1", now), "change start refused: M1 is held by "+l.Holder)
	require.NoError(t, Gate(ctx, bob, "change start", "", now), "no milestone, no lease check")

	// Lapsed: nobody renews it. Its own checkout takes it again; bob's
	// gate does not.
	time.Sleep(2 * MilestoneTTL)
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, Gate(ctx, bob, "change new", "M1", now)))
	require.NoError(t, Gate(ctx, ann, "change start", "M1", now), "ann's claim stands: the lapsed lease is taken again")

	held, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, held)
	err = Gate(ctx, ann, "change start", "M1", now)
	require.ErrorContains(t, err, "holds no lease on M1", "released: no gate takes it again")
	_, err = ClaimMilestone(ctx, bob, "M1")
	require.NoError(t, err, "released: bob may claim it")
}

// The loop's hold: refused without a claim; renewed past the TTL while the
// run lives (the control: the same claim with no hold lapses, above); free
// again once the run stops and the TTL passes.
func TestMilestoneLease_LoopHoldRenews(t *testing.T) {
	shortLeases(t, time.Second, 250*time.Millisecond)
	ctx := context.Background()
	now := time.Now()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "bob")

	_, err := HoldMilestone(ctx, ann, "M1", io.Discard)
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err))
	require.ErrorContains(t, err, "ape milestone claim M1")

	_, err = ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	time.Sleep(2 * MilestoneTTL) // lapsed before the run: the hold takes it again
	stop, err := HoldMilestone(ctx, ann, "M1", io.Discard)
	require.NoError(t, err)
	time.Sleep(3 * MilestoneTTL)
	_, err = ClaimMilestone(ctx, bob, "M1")
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err), "renewed while the run lives")
	require.NoError(t, Gate(ctx, ann, "change start", "M1", now))
	stop()

	time.Sleep(2 * MilestoneTTL)
	_, err = ClaimMilestone(ctx, bob, "M1")
	require.NoError(t, err, "the run stopped renewing: the lease lapsed and another member may claim it")
	_, err = HoldMilestone(ctx, ann, "M1", io.Discard)
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err), "ann's next run refuses, never takes it back")
}
