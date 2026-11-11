package member

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// checkoutHolder is the holder p's claims take: <user>/<checkout>.
func checkoutHolder(ctx context.Context, t *testing.T, p *apexv1.Project) string {
	t.Helper()
	s, err := openMilestones(ctx, p)
	require.NoError(t, err)
	defer s.close()
	return s.holder
}

func leaseCode(t *testing.T, err error) string {
	t.Helper()
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe, "%v", err)
	return fe.Code
}

// shortLeases gives the test's milestone and close leases a 1 s TTL,
// renewed every every.
func shortLeases(t *testing.T, every time.Duration) {
	t.Helper()
	shortLeasesTTL(t, time.Second, every)
}

// shortLeasesTTL is shortLeases with the TTL given.
func shortLeasesTTL(t *testing.T, ttl, every time.Duration) {
	t.Helper()
	oldTTL, oldEvery := MilestoneTTL, MilestoneRenewEvery
	MilestoneTTL, MilestoneRenewEvery = ttl, every
	t.Cleanup(func() { MilestoneTTL, MilestoneRenewEvery = oldTTL, oldEvery })
}

// lapseTTL is the TTL of the lease tests that assume a lease outlives the
// steps between its claim and the next check (V15-RED-3): at 1 s a stalled
// Windows runner let ann's claim lapse before bob's, which the test needs
// refused (CI-5GREEN-1 on f1eb84d; first on 70a839a). Every lapse wait is a
// multiple of MilestoneTTL, so it scales with it. The tests that want a
// lapse stay at shortLeases' 1 s (research's classification, D914).
const lapseTTL = 3 * time.Second

// Stage 1a step 4: a milestone has one holder, a checkout; a change of it
// starts only in the holder's checkout; a claim lapses with no run
// renewing it and is taken again by its own checkout, never by another's
// gate; a release ends it.
func TestMilestoneLease_ClaimGateRelease(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 300*time.Millisecond)
	ctx := context.Background()
	now := time.Now()
	url := leaseCluster(t)
	// A trunk project is one person's (D876): the second holder is a second
	// checkout of ann's, which a milestone's lease fences as it would
	// another person's (TestTrunkOwner covers another person).
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "ann")

	c, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	l := c.Lease
	require.Empty(t, c.Branch, "no coordination.github: no milestone branch")
	require.True(t, strings.HasPrefix(l.Holder, "ann/"), l.Holder)
	again, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err, "a checkout's claim of its own milestone keeps it")
	require.Greater(t, again.Lease.Token.Revision, l.Token.Revision)

	_, err = ClaimMilestone(ctx, bob, "M1")
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err))
	require.ErrorContains(t, err, "M1 is held by "+l.Holder)
	_, err = ClaimMilestone(ctx, bob, "M2")
	require.NoError(t, err, "another milestone is free")

	require.NoError(t, Gate(ctx, ann, "change start", "M1", now))
	require.ErrorContains(t, Gate(ctx, bob, "change start", "M1", now), "change start refused: M1 is held by "+l.Holder)
	// D702: the fence before a push of milestone/M1 passes only the holder.
	require.NoError(t, FenceMilestone(ctx, ann, "M1"))
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, FenceMilestone(ctx, bob, "M1")))
	require.NoError(t, Gate(ctx, bob, "change start", "", now), "no milestone, no lease check")

	// Lapsed: nobody renews it. Its own checkout takes it again; bob's
	// gate does not.
	time.Sleep(2 * MilestoneTTL)
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, Gate(ctx, bob, "change new", "M1", now)))
	require.NoError(t, Gate(ctx, ann, "change start", "M1", now), "ann's claim stands: the lapsed lease is taken again")

	held, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, held)
	require.ErrorContains(t, FenceMilestone(ctx, ann, "M1"), "held by nobody", "released: no push")
	err = Gate(ctx, ann, "change start", "M1", now)
	require.ErrorContains(t, err, "holds no lease on M1", "released: no gate takes it again")
	_, err = ClaimMilestone(ctx, bob, "M1")
	require.NoError(t, err, "released: bob may claim it")
}

// The loop's hold: refused without a claim; renewed past the TTL while the
// run lives (the control: the same claim with no hold lapses, above); free
// again once the run stops and the TTL passes.
func TestMilestoneLease_LoopHoldRenews(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	now := time.Now()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "ann") // two checkouts of one person (D876)

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

// D709: one close of a project at a time — the close lease is held across
// the close, renewed past its TTL, kept by the same checkout run again,
// refused to another (coord.close-busy), and free once released.
func TestHoldClose(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "bob")

	release, err := HoldClose(ctx, ann, io.Discard, nil)
	require.NoError(t, err)
	time.Sleep(3 * MilestoneTTL)
	_, err = HoldClose(ctx, bob, io.Discard, nil)
	require.Equal(t, apexv1.CodeCloseBusy, leaseCode(t, err), "renewed while ann's close runs")
	require.ErrorContains(t, err, "ann/")
	again, err := HoldClose(ctx, ann, io.Discard, nil)
	require.NoError(t, err, "the same checkout's close run again keeps it")
	again()
	release()

	r2, err := HoldClose(ctx, bob, io.Discard, nil)
	require.NoError(t, err, "released: bob's close takes it")
	r2()
	require.Empty(t, mustHolder(t, url, "close.notes-svc"), "released at the end")
}

func mustHolder(t *testing.T, url, key string) string {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	s, err := lease.Open(context.Background(), js)
	require.NoError(t, err)
	h, _, err := s.Holder(context.Background(), key)
	require.NoError(t, err)
	return h
}

// FINDING-V15-1A-11: a NATS outage longer than the TTL made a run's renewal
// stale, and its keeper quit, so its close later found the lease lapsed and
// refused to push. Now the keeper takes a lapsed lease again while this
// checkout's claim stands. The control: another member claimed the lapsed
// lease first, and the keeper stops, naming them.
func TestMilestoneLease_LoopHoldRetakesALapsedLease(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	lapse := func(t *testing.T, p *apexv1.Project, ms string) {
		t.Helper()
		s, err := openMilestones(ctx, p)
		require.NoError(t, err)
		defer s.close()
		js, err := jetstream.New(s.nc)
		require.NoError(t, err)
		kv, err := js.KeyValue(ctx, lease.LeasesBucket)
		require.NoError(t, err)
		require.NoError(t, kv.Delete(ctx, s.key(ms)))
	}
	holder := func(t *testing.T, p *apexv1.Project) string {
		t.Helper()
		s, err := openMilestones(ctx, p)
		require.NoError(t, err)
		defer s.close()
		cur, _, err := s.store.Holder(ctx, s.key("M1"))
		require.NoError(t, err)
		return cur
	}

	t.Run("retaken", func(t *testing.T) {
		ann, bob := checkout(t, url, "ann"), checkout(t, url, "ann") // two checkouts of one person (D876)
		_, err := ClaimMilestone(ctx, ann, "M1")
		require.NoError(t, err)
		var log syncBuf
		stop, err := HoldMilestone(ctx, ann, "M1", &log)
		require.NoError(t, err)
		defer stop()
		annID := holder(t, ann)
		lapse(t, ann, "M1")
		require.Eventually(t, func() bool { return strings.Contains(log.String(), "was taken again") }, 10*time.Second, 50*time.Millisecond, log.String())
		require.Equal(t, annID, holder(t, ann))
		time.Sleep(3 * MilestoneTTL)
		_, err = ClaimMilestone(ctx, bob, "M1")
		require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err), "renewed again after the re-take")
		require.NoError(t, FenceMilestone(ctx, ann, "M1"), "a close's fenced push goes through")
	})
	t.Run("control-taken-by-another", func(t *testing.T) {
		ann, bob := checkout(t, url, "ann"), checkout(t, url, "ann")
		bobID := checkoutHolder(ctx, t, bob)
		_, err := ClaimMilestone(ctx, ann, "M2")
		require.NoError(t, err)
		var log syncBuf
		stop, err := HoldMilestone(ctx, ann, "M2", &log)
		require.NoError(t, err)
		defer stop()
		lapse(t, ann, "M2")
		_, err = ClaimMilestone(ctx, bob, "M2")
		require.NoError(t, err, "bob takes the lapsed lease first")
		require.Eventually(t, func() bool { return strings.Contains(log.String(), "holds it now") }, 10*time.Second, 50*time.Millisecond, log.String())
		require.Contains(t, log.String(), bobID)
		require.ErrorContains(t, FenceMilestone(ctx, ann, "M2"), bobID, "ann's first checkout does not push")
	})
}

// syncBuf is a goroutine-safe log.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// FINDING-V15-1A-11, research's close-lease finding: a close's lease that
// lapses mid-close (a NATS outage) is taken again when nobody holds it —
// counted, so the close checks main again — and its fence passes. The
// second-claimant case: another close took the lapsed lease first; the
// first close's keeper cancels it (onLost) and its fence right before the
// enqueue refuses coord.close-lost, so it enqueues nothing.
func TestHoldClose_LapseRetakenOrLostToASecondClose(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	lapse := func(t *testing.T, h *closeHold) {
		t.Helper()
		js, err := jetstream.New(h.s.nc)
		require.NoError(t, err)
		kv, err := js.KeyValue(ctx, lease.LeasesBucket)
		require.NoError(t, err)
		require.NoError(t, kv.Delete(ctx, h.key))
	}

	t.Run("control-retaken", func(t *testing.T) {
		ann := checkout(t, url, "ann")
		var log syncBuf
		var lost atomic.Bool
		release, err := HoldClose(ctx, ann, &log, func() { lost.Store(true) })
		require.NoError(t, err)
		defer release()
		h := activeClose.Load()
		lapse(t, h)
		require.Eventually(t, func() bool { return strings.Contains(log.String(), "was taken again") }, 10*time.Second, 50*time.Millisecond, log.String())
		n, err := h.fence(ctx, "the close PR's enqueue")
		require.NoError(t, err, "nobody else holds it: the close goes on")
		require.Equal(t, 1, n, "one re-take: the close checks main again before it enqueues")
		require.False(t, lost.Load())
	})
	t.Run("second-claimant", func(t *testing.T) {
		ann, bob := checkout(t, url, "ann3"), checkout(t, url, "bob3")
		// A project of their own, so control-retaken's lease is not in the way.
		for _, p := range []*apexv1.Project{ann, bob} {
			cfg, err := os.ReadFile(apexv1.ConfigPath(p.Root))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(apexv1.ConfigPath(p.Root), []byte(strings.Replace(string(cfg), "Notes Svc", "Cart Svc", 1)), 0o644))
		}
		var log syncBuf
		var lost atomic.Bool
		release, err := HoldClose(ctx, ann, &log, func() { lost.Store(true) })
		require.NoError(t, err)
		defer release()
		h := activeClose.Load()
		lapse(t, h)
		releaseBob, err := HoldClose(ctx, bob, io.Discard, nil)
		require.NoError(t, err, "bob's close takes the lapsed lease")
		defer releaseBob()
		require.Eventually(t, lost.Load, 10*time.Second, 50*time.Millisecond, "ann's close is cancelled: %s", log.String())
		require.Contains(t, log.String(), "bob3/")
		_, err = h.fence(ctx, "the close PR's enqueue")
		require.Equal(t, apexv1.CodeCloseLost, leaseCode(t, err), "ann enqueues nothing")
		require.ErrorContains(t, err, "bob3/")
	})
}

// D732 / FINDING-V15-1A-11, research's defence in depth: a lease value
// carries its TTL, and a fence treats now > at+ttl−ExpirySkew as not held,
// whatever the server still has — the fence fails safe if the server's
// expiry lags. A value written 40 s ago with a 30 s TTL, still on the
// server, refuses the push and names the skew margin; the control, the
// same holder written now, passes and logs its one fence line.
func TestFenceMilestone_ClientSideExpiry(t *testing.T) {
	ctx := context.Background()
	url := leaseCluster(t)
	ann := checkout(t, url, "ann4")
	for _, p := range []*apexv1.Project{ann} {
		cfg, err := os.ReadFile(apexv1.ConfigPath(p.Root))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(apexv1.ConfigPath(p.Root), []byte(strings.Replace(string(cfg), "Notes Svc", "Fence Svc", 1)), 0o644))
	}
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	s, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer s.close()
	js, err := jetstream.New(s.nc)
	require.NoError(t, err)
	kv, err := js.KeyValue(ctx, lease.LeasesBucket)
	require.NoError(t, err)
	put := func(at time.Time) {
		t.Helper()
		v, err := json.Marshal(lease.Value{Holder: s.holder, At: at, TTLSecs: 30})
		require.NoError(t, err)
		_, err = kv.Put(ctx, s.key("M1"), v)
		require.NoError(t, err)
	}
	var log syncBuf
	old := FenceLog
	FenceLog = &log
	t.Cleanup(func() { FenceLog = old })

	put(time.Now().Add(-40 * time.Second))
	err = FenceMilestone(ctx, ann, "M1")
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err), "past at+ttl: not held, though the server has it")
	require.ErrorContains(t, err, "at+ttl has passed")
	require.ErrorContains(t, err, lease.ExpirySkew.String())
	require.Empty(t, log.String(), "no push, no fence line")

	put(time.Now())     // the control: written now
	apexv1.TakeFences() // only this push's line below (the collector is process-wide)
	require.NoError(t, FenceMilestone(ctx, ann, "M1"))
	require.Contains(t, log.String(), "ape: fence: milestone/M1: pushing under "+s.key("M1")+", held by this checkout (epoch 1, revision ")
	require.Equal(t, 1, strings.Count(log.String(), "\n"), "one line per fenced push")
	// B1: the same line is noted for the run's result.json.
	noted := apexv1.TakeFences()
	require.Len(t, noted, 1)
	require.Equal(t, strings.TrimSuffix(log.String(), "\n"), noted[0])
}

// D732: a run's keeper whose lease this checkout released (a close, at its
// merge) says so — "released by this checkout", not "lost".
func TestMilestoneLease_ReleasedByThisCheckoutIsNoLoss(t *testing.T) {
	shortLeases(t, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	ann := checkout(t, url, "ann5")
	cfg, err := os.ReadFile(apexv1.ConfigPath(ann.Root))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(ann.Root), []byte(strings.Replace(string(cfg), "Notes Svc", "Release Svc", 1)), 0o644))
	_, err = ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	var log syncBuf
	stop, err := HoldMilestone(ctx, ann, "M1", &log)
	require.NoError(t, err)
	defer stop()
	_, err = ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(log.String(), "released by this checkout (the milestone closed)") }, 10*time.Second, 50*time.Millisecond, log.String())
	require.NotContains(t, log.String(), "was lost")
}

// V8e (FINDING-V15-1C-3): a close that found another checkout's close in
// progress was refused at once (coord.close-busy, retry), and a loop's
// retry met the refusal for the whole first close. Now the close waits:
// it says whose close it waits for, and takes close.<project> once that
// close releases it. The controls: max 0 is refused at once, as a close
// called with --wait 0 is (CloseBusy); past max the refusal comes back;
// the checkout holding it is not busy to itself.
func TestWaitClose_WaitsForTheOtherClose(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	oldPoll := CloseWaitPoll
	CloseWaitPoll = 100 * time.Millisecond
	t.Cleanup(func() { CloseWaitPoll = oldPoll })
	ctx := context.Background()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "bob")

	release, err := HoldClose(ctx, ann, io.Discard, nil)
	require.NoError(t, err)
	require.NoError(t, CloseBusy(ctx, ann), "ann's own close is not busy to ann")
	require.Equal(t, apexv1.CodeCloseBusy, leaseCode(t, CloseBusy(ctx, bob)), "--wait 0: refused at once")

	start := time.Now()
	_, err = WaitClose(ctx, bob, io.Discard, nil, 0)
	require.Equal(t, apexv1.CodeCloseBusy, leaseCode(t, err), "max 0: refused at once")
	require.Less(t, time.Since(start), time.Second)

	start = time.Now()
	_, err = WaitClose(ctx, bob, io.Discard, nil, 400*time.Millisecond)
	require.Equal(t, apexv1.CodeCloseBusy, leaseCode(t, err), "past max: refused")
	require.GreaterOrEqual(t, time.Since(start), 400*time.Millisecond)

	var mu sync.Mutex
	var log strings.Builder
	type held struct {
		release func()
		err     error
	}
	got := make(chan held, 1)
	go func() {
		r, err := WaitClose(ctx, bob, writerFunc(func(b []byte) { mu.Lock(); log.Write(b); mu.Unlock() }), nil, 30*time.Second)
		got <- held{r, err}
	}()
	time.Sleep(3 * MilestoneTTL) // ann's close runs on, renewed
	select {
	case h := <-got:
		t.Fatalf("bob's close did not wait: %v", h.err)
	default:
	}
	mu.Lock()
	require.Contains(t, log.String(), "waiting for ann/", "it says whose close it waits for")
	require.Equal(t, 1, strings.Count(log.String(), "waiting for"), "once per holder")
	mu.Unlock()
	release()
	select {
	case h := <-got:
		require.NoError(t, h.err, "ann's close ended: bob's close goes on")
		require.Contains(t, mustHolder(t, url, "close.notes-svc"), "bob/")
		h.release()
	case <-time.After(5 * time.Second):
		t.Fatal("bob's close still waits after ann's close released close.<project>")
	}
}

type writerFunc func([]byte)

func (w writerFunc) Write(b []byte) (int, error) { w(b); return len(b), nil }
