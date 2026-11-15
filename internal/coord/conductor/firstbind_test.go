package conductor

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// R-8a: the first bind read the stream's last sequence and then created
// its durable DeliverNew, so an event published between the two was past
// the durable's start and never delivered (it was below the recorded
// start, so no floor was wrong: nothing delivered it). The durable is
// created by start sequence now, at the start it records, and that event
// is its first delivery.
func TestBind_AnEventPublishedBetweenTheReadAndTheCreateIsDelivered(t *testing.T) {
	_, _, js := rig(t)
	ctx := t.Context()
	publish(t, js, "question", 1) // history, before the read: not delivered
	testFirstBindHook = func() { publish(t, js, "question", 2) }
	t.Cleanup(func() { testFirstBindHook = nil })

	r := &runner{cfg: scaled("ann", "cond-a", nil), js: js}
	c, err := r.bind(ctx, 0)
	require.NoError(t, err)
	testFirstBindHook = nil

	b, err := c.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	var seqs []uint64
	for m := range b.Messages() {
		md, err := m.Metadata()
		require.NoError(t, err)
		seqs = append(seqs, md.Sequence.Stream)
	}
	require.NoError(t, b.Error())
	require.Equal(t, []uint64{2}, seqs, "the event published in the gap is delivered, and the history is not")
	require.Equal(t, uint64(1), boundFloor(c.CachedInfo()), "it stands after the history")
}
