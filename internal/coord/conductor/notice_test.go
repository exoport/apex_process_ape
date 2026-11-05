package conductor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/events"
)

// PLAN-V15-ASSESSOR step 5 (D760+): the conductor (`ape conductor attach`
// runs this) is delivered the coordinator's impact-notice and
// hold-requested like any other event: typed with the delivery mark and
// `ape conductor ack <token>`, and acknowledged by that ack. The payload
// carries the batch's trigger ids (events[]). The controls: a
// hold-requested pauses nothing (the milestone is not paused, and no
// milestone-paused is published however long it waits), while OQ24's
// milestone-paused still pauses it.
func TestConductor_DeliversNoticesAsAckedDeliveries(t *testing.T) {
	_, nc, js := rig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pubs := make(chan coordinator.Publisher, 2)
	go func() {
		_ = coordinator.Run(ctx, nc, coordinator.Config{
			Ensemble: "atomix", ID: "coord-1", TTL: 3 * time.Second, Renew: time.Second, Projects: []string{"taskforge"},
			Assessor: &coordinator.AssessorConfig{Quiet: 200 * time.Millisecond, Ceiling: 2 * time.Second, OnBatch: func(_ context.Context, _ coordinator.Batch, p coordinator.Publisher) { pubs <- p }},
		})
	}()
	sess := &fakeSession{nc: nc, answers: 10, acked: map[string]bool{}}
	go func() { _ = Run(ctx, nc, scaled("ann", "cond-a", sess)) }()
	paused := watch(t, nc, "ape.evt.*.taskforge.project.M2.milestone-paused")
	time.Sleep(time.Second) // the coordinator's and the conductor's consumers are bound

	trigger := "taskforge/c1/change-archived/M2/b-2"
	_, err := js.Publish(ctx, "ape.evt.bob.taskforge.project.M2.change-archived", []byte(`{"key":"b-2"}`), jetstream.WithMsgID(trigger))
	require.NoError(t, err)
	var pub coordinator.Publisher
	select {
	case pub = <-pubs:
	case <-time.After(10 * time.Second):
		t.Fatal("no batch")
	}
	_, _, err = pub.Notices(ctx, "20261103T040000Z-1", "taskforge", []string{trigger},
		[]coordinator.Notice{{Target: "taskforge/M1", Reason: "B-2 changes the pace A documents", Evidence: []string{"docs/configuration.md"}, Hold: true}})
	require.NoError(t, err)

	typedOf := func(event string) string {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		for _, s := range sess.typed {
			if strings.Contains(s, ".coord.20261103T040000Z-1-taskforge-M1."+event) {
				return s
			}
		}
		return ""
	}
	require.Eventually(t, func() bool { return typedOf("impact-notice") != "" && typedOf("hold-requested") != "" }, 15*time.Second, 100*time.Millisecond)
	for _, ev := range []string{"impact-notice", "hold-requested"} {
		s := typedOf(ev)
		require.Contains(t, s, "<ape-delivery ", ev)
		require.Contains(t, s, "ape conductor ack ", ev)
		require.Contains(t, s, `"events":["`+trigger+`"]`, ev)
	}
	t.Logf("delivered: %s", typedOf("impact-notice"))

	cons, err := js.Consumer(ctx, events.EventsStream, "conductor-ann-atomix")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, err := cons.Info(ctx)
		return err == nil && info.NumAckPending == 0 && info.NumPending == 0
	}, 10*time.Second, 100*time.Millisecond, "both deliveries acknowledged")

	time.Sleep(5 * time.Second) // past the scaled pause age (4 s)
	for _, ms := range []string{"M1", "M2"} {
		p, err := Paused(ctx, js, "taskforge", ms, "bob")
		require.NoError(t, err)
		require.False(t, p, "control: a hold request pauses nothing (%s)", ms)
	}
	require.False(t, paused("milestone-paused"))

	_, err = events.Publish(ctx, js, events.Subject("ann", "taskforge", events.KindProject, "M1", "milestone-paused"), "oq24-pause-1", []byte(`{"by":"cond-a","user":"ann"}`))
	require.NoError(t, err)
	p, err := Paused(ctx, js, "taskforge", "M1", "bob")
	require.NoError(t, err)
	require.True(t, p, "control: OQ24's pause still pauses the milestone")
}
