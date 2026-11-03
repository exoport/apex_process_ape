package member

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-ASSESSOR step 5: `ape status --json` shows each milestone's
// impact notices (milestones[].notices), read from the coord stream by
// target. The control: a notice to another project's milestone of the
// same name is not shown.
func TestReadNotices_ByTargetMilestone(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, events.Declare(ctx, js, 1))
	pub := func(id, body string) {
		_, err := events.Publish(ctx, js, events.Subject("coord", "ens", events.KindCoord, id, "impact-notice"), id, []byte(body))
		require.NoError(t, err)
	}
	pub("b-1-atomix-M1", `{"id":"b-1-atomix-M1","target":"atomix/M1","reason":"r1","evidence":["a.go"],"hold":true,"at":"2026-11-03T03:00:00Z"}`)
	pub("b-1-other-M1", `{"id":"b-1-other-M1","target":"other/M1","reason":"r2","evidence":["b.go"],"at":"2026-11-03T03:00:00Z"}`)
	got, ok := ReadNotices(ctx, js, "ens", "atomix")
	require.True(t, ok)
	require.Equal(t, map[string][]apexv1.NoticeLine{"M1": {{ID: "b-1-atomix-M1", Reason: "r1", Evidence: []string{"a.go"}, Hold: true, At: "2026-11-03T03:00:00Z"}}}, got)
}
