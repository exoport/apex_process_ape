package member

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
)

// V15-REL-2: a loop start whose first claim met a write between its read
// and its claim (here this checkout's own release, between the read of its
// live key and adopt) refused, outside a run's wait, with the store's raw
// "lease: stale token". It reads again now, and the next read's own word
// is what it says: released by this checkout.
func TestHoldMilestone_AWriteBetweenTheReadAndTheClaimIsReadAgain(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	liveSeam = func(string) {
		liveSeam = nil
		_, err := ReleaseMilestone(ctx, ann, "M1")
		require.NoError(t, err)
	}
	t.Cleanup(func() { liveSeam = nil })

	stop, err := HoldMilestone(ctx, ann, "M1", nil)
	require.Error(t, err)
	require.Nil(t, stop)
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, apexv1.CodeNotLeaseHolder, fe.Code)
	require.ErrorContains(t, err, "released by this checkout")
	require.NotContains(t, err.Error(), "stale token")
}

// V15-REL-2, the bound: a lease written between every read and claim is
// read again holdTries times, then refused in words (still ErrStale, so a
// run's wait reads again later).
func TestHoldMilestone_ALeaseChangingUnderEveryReadIsRefusedInWords(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	other, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer other.close()
	js, err := jetstream.New(other.nc)
	require.NoError(t, err)
	st, err := js.Stream(ctx, "KV_"+lease.LeasesBucket)
	require.NoError(t, err)
	// Read live, then gone before adopt; read gone, then claimed before
	// the claim: every read is out of date by its act.
	reads := 0
	liveSeam = func(k string) {
		reads++
		require.NoError(t, st.Purge(ctx, jetstream.WithPurgeSubject("$KV."+lease.LeasesBucket+"."+k)))
	}
	reclaimSeam = func(k string) {
		reads++
		_, err := other.store.Claim(ctx, k, "bob", time.Minute)
		require.NoError(t, err)
	}
	t.Cleanup(func() { liveSeam, reclaimSeam = nil, nil })

	_, err = HoldMilestone(ctx, ann, "M1", nil)
	require.ErrorIs(t, err, lease.ErrStale)
	require.ErrorContains(t, err, "M1's lease changed between each of 10 reads and this checkout's claim")
	require.NotContains(t, err.Error(), "stale token")
	require.Equal(t, holdTries, reads, "read again holdTries times, then refused")
}
