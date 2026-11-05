package cred

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// c1Open says C1 is unfixed (D757, research's threat review): the member
// grant's "$JS.API.>" let a member act on streams by name — purge by
// filter, delete a message, delete the stream — reaching messages its
// publish permissions deny. While true the test shows each bypass; V12
// (the grant narrowed to the API calls a member makes) flipped it, and
// the test requires every one refused.
const c1Open = false

// C1's T1 (D757): ann, a member of taskforge, may not publish to
// $KV.ape-leases.sup.leader or as the coordinator (the control, refused
// by the server). Through the JetStream API she tries to remove the
// supervisor's leader lease and the coordinator's event anyway: a filtered
// purge, a message delete, and deleting APE_EVENTS. An admin connection then
// reads what is left.
func TestMemberGrant_C1_JetStreamAPIReachesOutsideItsSubjects(t *testing.T) {
	url, acct := natstest.StartOperatorJetStream(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPath, _ := natstest.MintCreds(t, acct, "admin", nil, nil)
	admin, err := nats.Connect(url, nats.UserCredentials(adminPath))
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	ajs, err := jetstream.New(admin)
	require.NoError(t, err)
	require.NoError(t, lease.Declare(ctx, ajs, 1))
	require.NoError(t, events.Declare(ctx, ajs, 1))
	kv, err := ajs.KeyValue(ctx, lease.LeasesBucket)
	require.NoError(t, err)
	leaderRev, err := kv.Put(ctx, "sup.leader", []byte(`{"holder":"sup-a"}`))
	require.NoError(t, err)
	coordSubj := "ape.evt.coord.atomix.coord.x.lease-expired"
	_, err = ajs.Publish(ctx, coordSubj, []byte(`{"key":"milestone.taskforge.M2"}`))
	require.NoError(t, err)

	g := MemberGrant("ann", "taskforge", "atomix")
	annPath, _ := natstest.MintCreds(t, acct, "ann", g.PubAllow, g.SubAllow)
	errs := make(chan error, 16)
	ann, err := nats.Connect(url, nats.UserCredentials(annPath), nats.CustomInboxPrefix(natsconn.InboxPrefix("ann")), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }))
	require.NoError(t, err)
	t.Cleanup(ann.Close)
	js, err := jetstream.New(ann)
	require.NoError(t, err)

	// The control: her direct write to the leader's key is refused.
	require.NoError(t, ann.Publish("$KV.ape-leases.sup.leader", []byte("x")))
	require.NoError(t, ann.Flush())
	select {
	case e := <-errs:
		require.ErrorContains(t, e, "sup.leader", "the server refuses the direct write")
	case <-time.After(2 * time.Second):
		t.Fatal("control: the direct write to sup.leader was not refused")
	}

	// Each attempt gets its own deadline: a refused API request is never
	// answered, so it ends at its deadline.
	try := func(f func(context.Context) error) error {
		tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return f(tctx)
	}
	leases, err := js.Stream(ctx, "KV_"+lease.LeasesBucket)
	require.NoError(t, err, "she can bind the leases' stream by name")
	purgeLeader := try(func(ctx context.Context) error {
		return leases.Purge(ctx, jetstream.WithPurgeSubject("$KV."+lease.LeasesBucket+".sup.leader"))
	})
	deleteLeader := try(func(ctx context.Context) error { return leases.DeleteMsg(ctx, leaderRev) })
	evs, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	purgeCoord := try(func(ctx context.Context) error { return evs.Purge(ctx, jetstream.WithPurgeSubject(coordSubj)) })
	deleteStream := try(func(ctx context.Context) error { return js.DeleteStream(ctx, events.EventsStream) })
	t.Logf("C1 attempts by ann: purge sup.leader: %v; delete its message: %v; purge the coordinator's event: %v; delete APE_EVENTS: %v",
		purgeLeader, deleteLeader, purgeCoord, deleteStream)

	_, leaderErr := kv.Get(ctx, "sup.leader")
	_, streamErr := ajs.Stream(ctx, events.EventsStream)
	t.Logf("C1 after: sup.leader read: %v; APE_EVENTS: %v", leaderErr, streamErr)
	if c1Open {
		require.NoError(t, purgeLeader, "C1 open: the filtered purge goes through")
		require.Error(t, leaderErr, "C1 open: the supervisor's leader lease is gone")
		require.NoError(t, deleteStream, "C1 open: the event stream can be deleted")
		require.Error(t, streamErr)
		return
	}
	for _, e := range []error{purgeLeader, deleteLeader, purgeCoord, deleteStream} {
		require.Error(t, e, "a member's JetStream API must not reach outside its subjects")
	}
	require.NoError(t, leaderErr, "the leader lease is intact")
	require.NoError(t, streamErr, "APE_EVENTS is intact")
	// Refused by her permissions, not merely unanswered: the server names
	// each API subject.
	var refused []string
	for len(errs) > 0 {
		refused = append(refused, (<-errs).Error())
	}
	joined := strings.Join(refused, "\n")
	for _, api := range []string{
		"$JS.API.STREAM.PURGE.KV_" + lease.LeasesBucket,
		"$JS.API.STREAM.MSG.DELETE.KV_" + lease.LeasesBucket,
		"$JS.API.STREAM.PURGE." + events.EventsStream,
		"$JS.API.STREAM.DELETE." + events.EventsStream,
	} {
		require.Contains(t, joined, api, "the server refuses %s", api)
	}
}
