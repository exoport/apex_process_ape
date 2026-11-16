package rebuild

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func shortDeclare(t *testing.T, try, every, budget time.Duration) {
	t.Helper()
	wasTry, wasEvery, wasBudget := declareTry, declareEvery, declareBudget
	declareTry, declareEvery, declareBudget = try, every, budget
	t.Cleanup(func() { declareTry, declareEvery, declareBudget = wasTry, wasEvery, wasBudget })
}

// V15-DECLARE-1: what is the cluster not answering yet, tried again, and
// what is a refusal, returned at once.
func TestTransient(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a try's deadline (eval's 101 in 133)", fmt.Errorf("lease: declare ape-leases: %w", context.DeadlineExceeded), true},
		{"timeout", nats.ErrTimeout, true},
		{"no responders (D924's read)", fmt.Errorf("lease: read epoch: %w", nats.ErrNoResponders), true},
		{"temporarily unavailable", &jetstream.APIError{Code: 503, ErrorCode: 10008}, true},
		{"not leader", &jetstream.APIError{Code: 500, ErrorCode: 10009}, true},
		// DECLARE-R2: the descriptions nats-server v2.14.5 gives (probed).
		{"no suitable peers, peer offline (a just-started cluster)", &jetstream.APIError{Code: 400, ErrorCode: 10005, Description: "no suitable peers for placement, peer offline"}, true},
		{"no suitable peers (two nodes, replicas 3)", &jetstream.APIError{Code: 400, ErrorCode: 10005, Description: "no suitable peers for placement"}, false},
		{"replicas > 1 on one server", &jetstream.APIError{Code: 500, ErrorCode: 10074, Description: "replicas > 1 not supported in non-clustered mode"}, false},
		{"bad request", &jetstream.APIError{Code: 400, ErrorCode: jetstream.JSErrCodeBadRequest}, false},
		{"permission", nats.ErrPermissionViolation, false},
		{"authorization", nats.ErrAuthorization, false},
		{"JetStream not enabled", jetstream.ErrJetStreamNotEnabled, false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, transient(ctx, tc.err))
		})
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	require.False(t, transient(done, context.DeadlineExceeded), "the caller's own end is not the cluster's")
}

func TestRetry(t *testing.T) {
	shortDeclare(t, 200*time.Millisecond, 10*time.Millisecond, 300*time.Millisecond)
	ctx := context.Background()

	tries := 0
	require.NoError(t, Retry(ctx, "step", func(ctx context.Context) error {
		tries++
		dl, ok := ctx.Deadline()
		require.True(t, ok, "each try has a deadline of its own")
		require.LessOrEqual(t, time.Until(dl), declareTry)
		if tries < 3 {
			return fmt.Errorf("lease: declare ape-leases: %w", context.DeadlineExceeded)
		}
		return nil
	}))
	require.Equal(t, 3, tries, "two slow answers waited out")

	tries = 0
	err := Retry(ctx, "step", func(context.Context) error { tries++; return nats.ErrPermissionViolation })
	require.ErrorIs(t, err, nats.ErrPermissionViolation)
	require.Equal(t, 1, tries, "a refusal is not tried again")

	tries = 0
	start := time.Now()
	err = Retry(ctx, "lease: declare", func(context.Context) error { tries++; return nats.ErrNoResponders })
	require.ErrorIs(t, err, nats.ErrNoResponders)
	require.ErrorContains(t, err, "lease: declare: the cluster did not answer in")
	require.Greater(t, tries, 1)
	require.Less(t, time.Since(start), declareBudget+declareTry, "bounded by the budget")
}

// V15-DECLARE-1 (D924): --new-epoch read the cluster's epoch straight
// after the creates, and a read the cluster did not answer counted as no
// epoch, so the next one was the local copy's + 1, at or below the
// cluster's. The read is tried again now, and the epoch follows the
// cluster's.
func TestNewEpoch_AnUnansweredReadIsNotNoEpoch(t *testing.T) {
	shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
	ctx := context.Background()
	js, ls := cluster(t, natstest.RunJetStreamServer(t))
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 5))
	unanswered := 1
	readEpoch = func(ctx context.Context, ls *lease.Store) (uint64, error) {
		if unanswered > 0 {
			unanswered--
			return 0, fmt.Errorf("lease: read epoch: %w", nats.ErrNoResponders)
		}
		return ls.Epoch(ctx)
	}
	t.Cleanup(func() {
		readEpoch = func(ctx context.Context, ls *lease.Store) (uint64, error) { return ls.Epoch(ctx) }
	})

	e, err := NewEpoch(ctx, js, ls, EpochFile(filepath.Join(t.TempDir(), "epoch")), window)
	require.NoError(t, err)
	require.Equal(t, uint64(6), e, "the cluster's 5, read again, not the empty local copy's 0")
	cur, err := ls.Epoch(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(6), cur)
}

// A write of the epoch whose answer was lost, tried again, finds it landed
// and does not move it a second time.
func TestNewEpoch_AWriteThatLandedIsNotWrittenTwice(t *testing.T) {
	shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
	ctx := context.Background()
	js, ls := cluster(t, natstest.RunJetStreamServer(t))
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 2))
	writes := 0
	writeEpoch = func(ctx context.Context, ls *lease.Store, e uint64) error {
		writes++
		if err := ls.SetEpoch(ctx, e); err != nil || writes > 1 {
			return err
		}
		return fmt.Errorf("lease: write epoch: %w", context.DeadlineExceeded) // landed; the answer lost
	}
	t.Cleanup(func() {
		writeEpoch = func(ctx context.Context, ls *lease.Store, e uint64) error { return ls.SetEpoch(ctx, e) }
	})

	e, err := NewEpoch(ctx, js, ls, EpochFile(filepath.Join(t.TempDir(), "epoch")), window)
	require.NoError(t, err)
	require.Equal(t, uint64(3), e)
	require.Equal(t, 1, writes, "the second try read it landed and wrote nothing")
	cur, err := ls.Epoch(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), cur)
}

// slowFirst is a cluster whose first n bucket creates are not answered in
// time (the create may have landed or not: these did not).
type slowFirst struct {
	jetstream.JetStream
	n int
}

func (s *slowFirst) CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	if s.n > 0 {
		s.n--
		return nil, fmt.Errorf("nats: %w", context.DeadlineExceeded)
	}
	return s.JetStream.CreateOrUpdateKeyValue(ctx, cfg)
}

// V15-DECLARE-1: `ape nats declare` on a just-started cluster failed its
// first try about three times in four, on its first create. A create the
// cluster did not answer in time is tried again, and the declare lands.
func TestDeclare_ASlowFirstCreateIsWaitedOut(t *testing.T) {
	shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
	ctx := context.Background()
	nc, err := nats.Connect(natstest.RunJetStreamServer(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	slow := &slowFirst{JetStream: js, n: 2}
	require.NoError(t, Declare(ctx, slow, 1))
	require.Zero(t, slow.n, "both slow answers were met")
	for _, b := range append([]string{lease.LeasesBucket, lease.MetaBucket}, kvBuckets...) {
		_, err := js.KeyValue(ctx, b)
		require.NoError(t, err, "%s declared", b)
	}
}

// DECLARE-R1 (research's probe, on f23a325): the write lands with its
// answer lost; the retry's own read is then not answered, and the step
// wrote again, so SetEpoch's own read (answered) refused the epoch it had
// written ("epoch 3 is not above the current 3"). An unanswered read is
// now tried again, and the landed write is found.
func TestNewEpoch_AnUnansweredReadAfterALandedWriteIsNotAWrite(t *testing.T) {
	shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
	ctx := context.Background()
	js, ls := cluster(t, natstest.RunJetStreamServer(t))
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 2))
	reads, writes := 0, 0
	readEpoch = func(ctx context.Context, ls *lease.Store) (uint64, error) {
		reads++
		if reads == 3 { // the read step (1), write try 1 (2), write try 2 (3)
			return 0, fmt.Errorf("lease: read epoch: %w", nats.ErrNoResponders)
		}
		return ls.Epoch(ctx)
	}
	writeEpoch = func(ctx context.Context, ls *lease.Store, e uint64) error {
		writes++
		if err := ls.SetEpoch(ctx, e); err != nil || writes > 1 {
			return err
		}
		return fmt.Errorf("lease: write epoch: %w", context.DeadlineExceeded)
	}
	t.Cleanup(func() {
		readEpoch = func(ctx context.Context, ls *lease.Store) (uint64, error) { return ls.Epoch(ctx) }
		writeEpoch = func(ctx context.Context, ls *lease.Store, e uint64) error { return ls.SetEpoch(ctx, e) }
	})

	e, err := NewEpoch(ctx, js, ls, EpochFile(filepath.Join(t.TempDir(), "epoch")), window)
	require.NoError(t, err, "an epoch that landed is not refused")
	require.Equal(t, uint64(3), e)
	require.Equal(t, 1, writes, "written once: the unanswered read was read again, not written over")
	require.Equal(t, 4, reads)
}

// peerOffline is a cluster whose first n bucket creates are refused as a
// just-started cluster refuses them, a node not yet seen online.
type peerOffline struct {
	jetstream.JetStream
	n int
}

func (s *peerOffline) CreateOrUpdateKeyValue(ctx context.Context, cfg jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	if s.n > 0 {
		s.n--
		return nil, &jetstream.APIError{Code: 400, ErrorCode: 10005, Description: "no suitable peers for placement, peer offline"}
	}
	return s.JetStream.CreateOrUpdateKeyValue(ctx, cfg)
}

// DECLARE-R2 (FINDING-151-D1): on rc.1, 3 first declares in 40 on a
// just-started R3 cluster failed at once with 10005 "no suitable peers for
// placement, peer offline". It is waited out now, as a timeout is.
func TestDeclare_APeerNotYetOnlineIsWaitedOut(t *testing.T) {
	shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
	ctx := context.Background()
	nc, err := nats.Connect(natstest.RunJetStreamServer(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	slow := &peerOffline{JetStream: js, n: 2}
	require.NoError(t, Declare(ctx, slow, 1))
	require.Zero(t, slow.n, "both refusals were waited out")
}

// DECLARE-R2, the refusals from a real nats-server (v2.14.5), so a server
// that words them differently fails here: a two-node cluster asked for
// --replicas 3 says no suitable peers with no reason (refused at once, in
// nats-server's words); one unclustered server says 10074. The "peer
// offline" reason (selectPeerError, jetstream_cluster.go) was probed with two
// of three nodes down, but whether the cluster answers it or 10008 depends on
// which node led the meta group, so the text is pinned above, not here.
func TestTransient_RealServerTexts(t *testing.T) {
	ctx := context.Background()
	create := func(t *testing.T, url string) error {
		t.Helper()
		nc, err := nats.Connect(url)
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err = js.CreateOrUpdateKeyValue(cctx, jetstream.KeyValueConfig{Bucket: "probe", Replicas: 3, History: 1})
		return err
	}
	t.Run("two nodes, replicas 3", func(t *testing.T) {
		c := natstest.RunCluster(t, 2)
		c.WaitMetaLeader(30 * time.Second)
		shortDeclare(t, time.Second, 10*time.Millisecond, 5*time.Second)
		nc, err := nats.Connect(c.URLs())
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		require.NoError(t, err)
		start := time.Now()
		err = Declare(ctx, js, 3)
		require.ErrorContains(t, err, "no suitable peers for placement")
		require.NotContains(t, err.Error(), "peer offline")
		require.NotContains(t, err.Error(), "did not answer", "refused at once, not waited out")
		require.Less(t, time.Since(start), 2*time.Second)
	})
	t.Run("one server, replicas 3", func(t *testing.T) {
		err := create(t, natstest.RunJetStreamServer(t))
		var api *jetstream.APIError
		require.ErrorAs(t, err, &api)
		require.Equal(t, jetstream.ErrorCode(10074), api.ErrorCode)
		require.False(t, transient(ctx, err))
	})
}
