package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/lease"
)

type gotBatch struct {
	b   Batch
	pub Publisher
}

// runWith runs coordinator id with OnBatch sending each batch to got; it
// returns once id holds the lease.
func runWith(ctx context.Context, t *testing.T, nc *nats.Conn, ls *lease.Store, id string, quiet time.Duration, got chan gotBatch, block chan struct{}) (stop func()) {
	t.Helper()
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(rctx, nc, Config{Ensemble: "ens", ID: id, TTL: 3 * time.Second, Renew: 500 * time.Millisecond, Projects: []string{"atomix"}, Assessor: &AssessorConfig{
			Quiet: quiet, Ceiling: 10 * quiet,
			OnBatch: func(_ context.Context, b Batch, p Publisher) {
				got <- gotBatch{b, p}
				if block != nil {
					<-block
				}
			},
		}})
	}()
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "coord.ens")
		return err == nil && h == id
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	return func() { cancel(); <-done }
}

func trigger(ctx context.Context, t *testing.T, js jetstream.JetStream, key string) string {
	t.Helper()
	id := "atomix/c/change-archived/M2/" + key
	_, err := js.Publish(ctx, "ape.evt.dana.atomix.project.M2.change-archived", []byte(`{"key":"`+key+`"}`), jetstream.WithMsgID(id))
	require.NoError(t, err)
	return id
}

func next(t *testing.T, got chan gotBatch, within time.Duration) *gotBatch {
	t.Helper()
	select {
	case g := <-got:
		return &g
	case <-time.After(within):
		return nil
	}
}

// PLAN-V15-ASSESSOR step 8: a coordinator killed while a batch's session
// runs leaves the batch recorded and not done; its replacement runs it
// once more (Replay) and publishes its notice once, while the killed one's
// late publish is fenced. A batch still open (not yet closed) at a kill is
// batched again by the replacement, from the recorded sequence. The
// control: a batch whose end is recorded is not run again.
func TestAssessor_ReplacementRunsOpenBatchesOnce(t *testing.T) {
	nc, js, ls := noticeRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	notice := []Notice{{Target: "atomix/M1", Reason: "r", Evidence: []string{"x.go"}}}

	// Killed mid-session.
	got := make(chan gotBatch, 8)
	block := make(chan struct{})
	stop1 := runWith(ctx, t, nc, ls, "c1", 200*time.Millisecond, got, block)
	t1 := trigger(ctx, t, js, "b-1")
	first := next(t, got, 10*time.Second)
	require.NotNil(t, first)
	require.Equal(t, t1, first.b.Events[0].MsgID)
	require.False(t, first.b.Replay)
	stop1() // the session never finished
	close(block)

	got2 := make(chan gotBatch, 8)
	stop2 := runWith(ctx, t, nc, ls, "c2", 200*time.Millisecond, got2, nil)
	again := next(t, got2, 10*time.Second)
	require.NotNil(t, again, "the replacement runs the unfinished batch")
	require.Equal(t, first.b.ID, again.b.ID)
	require.True(t, again.b.Replay)
	published, _, err := again.pub.Notices(ctx, again.b.ID, "atomix", []string{t1}, notice)
	require.NoError(t, err)
	require.Len(t, published, 1)
	_, _, err = first.pub.Notices(ctx, first.b.ID, "atomix", []string{t1}, notice)
	require.ErrorIs(t, err, ErrFenced, "the killed coordinator's late publish is fenced")
	subj := "ape.evt.coord.ens.coord." + first.b.ID + "-atomix-M1.impact-notice"
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, subj), "published once")
	require.Nil(t, next(t, got2, time.Second), "the replayed trigger is not batched again")

	// Open at the kill: c2 sees a trigger, and stops inside the quiet window.
	stop2()
	got3 := make(chan gotBatch, 8)
	stop3 := runWith(ctx, t, nc, ls, "c3", 3*time.Second, got3, nil)
	t2 := trigger(ctx, t, js, "b-2")
	time.Sleep(500 * time.Millisecond)
	stop3()
	require.Nil(t, next(t, got3, 0), "still open when c3 stopped")
	got4 := make(chan gotBatch, 8)
	stop4 := runWith(ctx, t, nc, ls, "c4", 200*time.Millisecond, got4, nil)
	defer stop4()
	reb := next(t, got4, 10*time.Second)
	require.NotNil(t, reb, "the batch open at the kill is batched again")
	require.Len(t, reb.b.Events, 1)
	require.Equal(t, t2, reb.b.Events[0].MsgID)
	require.False(t, reb.b.Replay, "batched anew from the stream, not replayed")

	// The control: c4 recorded both batches done; c5 runs neither.
	time.Sleep(500 * time.Millisecond)
	stop4()
	got5 := make(chan gotBatch, 8)
	stop5 := runWith(ctx, t, nc, ls, "c5", 200*time.Millisecond, got5, nil)
	defer stop5()
	require.Nil(t, next(t, got5, 2*time.Second), "a batch with a recorded end is not run again")
}
