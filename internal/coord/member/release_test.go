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

// V15-REL-1, research's three holes in (b) (D915+).

// lapsedLease claims ms for p and waits, nothing renewing it, until its
// TTL has expired: the store's last record is then the server's marker.
func lapsedLease(ctx context.Context, t *testing.T, p *apexv1.Project, ms string) (*milestones, lease.Lease) {
	t.Helper()
	c, err := ClaimMilestone(ctx, p, ms)
	require.NoError(t, err)
	s, err := openMilestones(ctx, p)
	require.NoError(t, err)
	t.Cleanup(s.close)
	require.Eventually(t, func() bool {
		h, _, err := s.store.Holder(ctx, s.key(ms))
		return err == nil && h == ""
	}, 20*time.Second, 50*time.Millisecond, "lapsed")
	last, err := s.store.LastRecord(ctx, s.key(ms))
	require.NoError(t, err)
	require.True(t, last.Lapsed(), "the store says lapsed: %+v", last)
	return s, c.Lease
}

// Hole A: a release of a lease that had already lapsed wrote no delete,
// so the store still said "lapsed" and a keeper or gate in the release's
// gap took it again. Now the release is written to the store anyway.
func TestReleaseMilestone_ALapsedLeaseIsReleasedInTheStoreToo(t *testing.T) {
	shortLeases(t, time.Hour) // nothing renews it
	ctx := context.Background()
	url := leaseCluster(t)
	ann := checkout(t, url, "ann")
	s, l := lapsedLease(ctx, t, ann, "M1")
	_, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	last, err := s.store.LastRecord(ctx, s.key("M1"))
	require.NoError(t, err)
	require.True(t, last.Released, "the store says released, not lapsed: %+v", last)
	// The gap: the release's book not written yet, as another process sees
	// it between the two steps. reclaim reads the store and refuses.
	require.NoError(t, s.book.Record(s.key("M1"), l, lease.Held))
	_, _, err = s.reclaim(ctx, "M1")
	require.ErrorIs(t, err, errReleased)
	h, _, err := s.store.Holder(ctx, s.key("M1"))
	require.NoError(t, err)
	require.Empty(t, h, "not taken again")
}

// Hole B: a release landing between reclaim's read (a lapse) and its claim
// was taken over by the claim's create. The claim is now a compare-and-
// swap at the record read: it fails, and the next read says released. With
// a keeper running, it logs the release and stops.
func TestReclaim_AReleaseBetweenTheReadAndTheClaimIsNotTakenOver(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	ann := checkout(t, url, "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	other, err := openMilestones(ctx, ann) // the releasing process
	require.NoError(t, err)
	defer other.close()
	key := other.key("M1")
	released := make(chan struct{})
	once := false
	reclaimSeam = func(k string) {
		if k != key || once {
			return
		}
		once = true
		last, err := other.store.LastRecord(ctx, k)
		if err == nil && last.Lapsed() {
			err = other.store.ReleaseLapsed(ctx, k, last)
		}
		if err == nil {
			close(released)
		}
	}
	t.Cleanup(func() { reclaimSeam = nil })
	// Set before the keeper starts, which reads it.
	var log syncBuf
	stop, err := HoldMilestone(ctx, ann, "M1", &log)
	require.NoError(t, err)
	defer stop()
	// The lapse the keeper meets: its key gone with no release on record.
	js, err := jetstream.New(other.nc)
	require.NoError(t, err)
	st, err := js.Stream(ctx, "KV_"+lease.LeasesBucket)
	require.NoError(t, err)
	require.NoError(t, st.Purge(ctx, jetstream.WithPurgeSubject("$KV."+lease.LeasesBucket+"."+key)))

	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatalf("the keeper never tried to take it again; its log:\n%s", log.String())
	}
	logSays(t, &log, "released by this checkout (the milestone closed)")
	require.NotContains(t, log.String(), "taken again", "the release in between is not taken over")
	h, _, err := other.store.Holder(ctx, key)
	require.NoError(t, err)
	require.Empty(t, h)
}

// Hole C: the gate before a change (requireMilestoneLease) took a lapsed
// lease again on the book's word, and in a release's gap the book still
// says claimed. It now refuses, for a held lease released and for a lapsed
// one released.
func TestGate_InAReleasesGapRefusesRatherThanClaims(t *testing.T) {
	shortLeases(t, time.Hour)
	ctx := context.Background()
	url := leaseCluster(t)
	t.Run("held-then-released", func(t *testing.T) {
		ann := checkout(t, url, "ann")
		c, err := ClaimMilestone(ctx, ann, "M1")
		require.NoError(t, err)
		s, err := openMilestones(ctx, ann)
		require.NoError(t, err)
		defer s.close()
		require.NoError(t, s.store.Release(ctx, c.Lease)) // the delete; the book still says claimed
		err = Gate(ctx, ann, "change start", "M1", time.Now())
		require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err))
		h, _, err := s.store.Holder(ctx, s.key("M1"))
		require.NoError(t, err)
		require.Empty(t, h, "the gate did not take it again")
	})
	t.Run("lapsed-then-released", func(t *testing.T) {
		ann := checkout(t, url, "ann")
		s, _ := lapsedLease(ctx, t, ann, "M2")
		last, err := s.store.LastRecord(ctx, s.key("M2"))
		require.NoError(t, err)
		require.NoError(t, s.store.ReleaseLapsed(ctx, s.key("M2"), last)) // the book still says claimed
		err = Gate(ctx, ann, "change start", "M2", time.Now())
		require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err))
		h, _, err := s.store.Holder(ctx, s.key("M2"))
		require.NoError(t, err)
		require.Empty(t, h)
	})
	t.Run("control-a-lapse-is-taken-again", func(t *testing.T) {
		ann := checkout(t, url, "ann")
		lapsedLease(ctx, t, ann, "M3")
		require.NoError(t, Gate(ctx, ann, "change start", "M3", time.Now()), "a lapse between runs, the claim standing: taken again")
	})
}

// Hole D: retake's branch for this checkout's own key adopted it with a
// create, which nats.go writes again over a delete, so a release landing
// after retake's read was taken over and the book said claimed again. It
// now renews at the token read: the release stands, and the next tick
// reads it.
func TestRetake_AReleaseAfterReadingItsOwnKeyIsNotTakenOver(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	s, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer s.close()
	retakeOwnSeam = func(string) {
		retakeOwnSeam = nil
		held, err := ReleaseMilestone(ctx, ann, "M1")
		require.NoError(t, err)
		require.True(t, held)
	}
	t.Cleanup(func() { retakeOwnSeam = nil })
	var log syncBuf
	l, keep := s.retake(ctx, "M1", lease.ErrStale, &log)
	require.Nil(t, l, "no lease kept; log: %s", log.String())
	require.True(t, keep, "the next tick reads it again")
	h, _, err := s.store.Holder(ctx, s.key("M1"))
	require.NoError(t, err)
	require.Empty(t, h, "the release is not taken over")
	claimed, err := s.claimed("M1")
	require.NoError(t, err)
	require.False(t, claimed, "the book says released")
	// The next tick: the store says released, the keeper stops.
	l, keep = s.retake(ctx, "M1", lease.ErrStale, &log)
	require.Nil(t, l)
	require.False(t, keep)
	require.Contains(t, log.String(), "released by this checkout")
}

// Hole D, reclaim's branch for a live key: a release landing after the
// read (live) was taken over by claimFresh's create. adopt reads again
// and finds it gone.
func TestReclaim_AReleaseAfterReadingALiveKeyIsNotTakenOver(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	s, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer s.close()
	liveSeam = func(string) {
		liveSeam = nil
		_, err := ReleaseMilestone(ctx, ann, "M1")
		require.NoError(t, err)
	}
	t.Cleanup(func() { liveSeam = nil })
	_, fresh, err := s.reclaim(ctx, "M1")
	require.ErrorIs(t, err, lease.ErrStale, "read again")
	require.False(t, fresh)
	h, _, err := s.store.Holder(ctx, s.key("M1"))
	require.NoError(t, err)
	require.Empty(t, h, "the release is not taken over")
	_, _, err = s.reclaim(ctx, "M1")
	require.ErrorIs(t, err, errReleased, "and read again it says released")
}

// Hole E: releasing a lapsed lease while this checkout's own keeper takes
// the lapse again (its compare-and-swap at the same record wins) ignored
// the release's ErrStale: the book said released, the store held by this
// checkout. The release now reads again and deletes the keeper's lease.
func TestReleaseMilestone_ALapseItsOwnKeeperTookAgainIsReleased(t *testing.T) {
	shortLeasesTTL(t, 2*time.Second, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	lapsedLease(ctx, t, ann, "M1")
	keeper, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer keeper.close()
	shortLeasesTTL(t, time.Minute, time.Hour) // the keeper's lease outlives the check
	releaseLapsedSeam = func(string) {
		releaseLapsedSeam = nil
		_, fresh, err := keeper.reclaim(ctx, "M1")
		require.NoError(t, err)
		require.True(t, fresh)
	}
	t.Cleanup(func() { releaseLapsedSeam = nil })
	held, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, held, "the keeper's lease, read again, was this checkout's to release")
	h, _, err := keeper.store.Holder(ctx, keeper.key("M1"))
	require.NoError(t, err)
	require.Empty(t, h, "after a release this checkout holds no lease on M1")
	last, err := keeper.store.LastRecord(ctx, keeper.key("M1"))
	require.NoError(t, err)
	require.True(t, last.Released, "the store says released: %+v", last)
}

// Hole F: a release of a held lease read the token once, so a renewal by
// one of this checkout's keepers in between (a close inside a loop's run
// has two) made the delete stale and the lease stayed held. It now reads
// again and releases at the new token.
func TestReleaseMilestone_ARenewalBetweenTheReadAndTheDeleteIsReadAgain(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	keeper, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer keeper.close()
	renewals := 0
	releaseHeldSeam = func(string) {
		if renewals == 3 { // more than one write in between is read again too
			return
		}
		renewals++
		_, err := keeper.adopt(ctx, "M1")
		require.NoError(t, err)
	}
	t.Cleanup(func() { releaseHeldSeam = nil })
	held, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, 3, renewals)
	h, _, err := keeper.store.Holder(ctx, keeper.key("M1"))
	require.NoError(t, err)
	require.Empty(t, h, "after a release this checkout holds no lease on M1")
}

// The read again is bounded: a keeper that renews between every read and
// delete ends it after releaseTries, with the stale error, not a loop.
func TestReleaseMilestone_ReadsAgainAtMostReleaseTries(t *testing.T) {
	shortLeasesTTL(t, time.Minute, time.Hour)
	ctx := context.Background()
	ann := checkout(t, leaseCluster(t), "ann")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	keeper, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer keeper.close()
	renewals := 0
	releaseHeldSeam = func(string) {
		renewals++
		_, err := keeper.adopt(ctx, "M1")
		require.NoError(t, err)
	}
	t.Cleanup(func() { releaseHeldSeam = nil })
	_, err = ReleaseMilestone(ctx, ann, "M1")
	require.ErrorIs(t, err, lease.ErrStale)
	require.Equal(t, releaseTries, renewals)
}
