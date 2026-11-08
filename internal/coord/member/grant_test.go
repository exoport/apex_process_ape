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

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// enforcedCluster is leaseCluster on an enforcing server (V12): the
// buckets, the streams and epoch 1 declared on its admin credential, as
// `ape nats declare --new-epoch` does on the operator's.
func enforcedCluster(t *testing.T) (*natstest.Enforced, jetstream.JetStream) {
	t.Helper()
	e := natstest.StartEnforced(t)
	nc, err := nats.Connect(e.URL, nats.UserCredentials(e.Admin))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	s, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, s.SetEpoch(ctx, 1))
	return e, js
}

// enforcedCheckout is ann's checkout of config's project, with a
// credential holding exactly cred.MemberGrant for her, the project and its
// ensemble.
func enforcedCheckout(t *testing.T, e *natstest.Enforced, config string) *apexv1.Project {
	t.Helper()
	const user = "ann"
	p := project(t, strings.Replace(config, "nats://127.0.0.1:4222", e.URL, 1), false)
	m, err := Load(project(t, config, true))
	require.NoError(t, err)
	path := e.Mint(t, user, perms(cred.MemberGrant(user, m.Project, m.Ensemble)))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	return p
}

// V12 (C1, D758): a member's verbs work under exactly cred.MemberGrant,
// the API narrowed to the calls they make — claim, gate, fence, the loop's
// hold, the close lease and its fence, pause and resume, events, the
// event watch, notices and a notice ack, release. The server refuses
// nothing (natstest.Enforced fails the test on any refusal). The control:
// another project's member is refused the project's lease keys and its
// events by the server.
func TestMemberGrant_TheMemberVerbsUnderIt(t *testing.T) {
	ctx := context.Background()
	e, js := enforcedCluster(t)
	ann := enforcedCheckout(t, e, block)

	c, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(c.Lease.Holder, "ann/"), c.Lease.Holder)
	require.NoError(t, Gate(ctx, ann, "change start", "M1", time.Now()))
	require.NoError(t, FenceMilestone(ctx, ann, "M1"))
	stop, err := HoldMilestone(ctx, ann, "M1", io.Discard)
	require.NoError(t, err)
	stop()

	release, err := HoldClose(ctx, ann, io.Discard, func() {})
	require.NoError(t, err)
	_, err = FenceClose(ctx, ann, "the close PR's enqueue")
	require.NoError(t, err)
	release()

	_, err = PauseMilestone(ctx, ann, "M1", "a look first", true)
	require.NoError(t, err)
	require.Equal(t, apexv1.CodeCoordMilestonePaused, leaseCode(t, Gate(ctx, ann, "change start", "M1", time.Now())))
	st, err := ReadPauseState(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, st.Paused, "the gate's read, as eval's row 3a reads it")
	require.Equal(t, "ann", st.ReadAs)
	_, err = PauseMilestone(ctx, ann, "M1", "", false)
	require.NoError(t, err)
	require.NoError(t, Gate(ctx, ann, "change start", "M1", time.Now()))

	wake, stopWatch, live := Watch(ctx, ann)
	defer stopWatch()
	require.True(t, live)
	Publish(ctx, ann, apexv1.Event{Seq: 1, At: "2026-11-05T00:00:00Z", Event: apexv1.EventGateGreen, Milestone: "M1"})
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not wake on the published event")
	}
	PublishTrigger(ctx, ann, apexv1.Trigger{Event: apexv1.EventChangeArchived, Milestone: "M1", Change: "c1", Commit: "abc"})

	_, err = events.Publish(ctx, js, events.Subject("coord", "shop", events.KindCoord, "n1", "impact-notice"), "n1/impact-notice",
		[]byte(`{"id":"n1","target":"notes-svc/M1","reason":"r"}`))
	require.NoError(t, err)
	m, err := Load(ann)
	require.NoError(t, err)
	notes, ok := Notices(ctx, &m)
	require.True(t, ok)
	require.NotEmpty(t, notes["M1"], "the notice reads back")
	_, err = AckNotice(ctx, ann, "n1", "acted")
	require.NoError(t, err)

	held, err := ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.True(t, held)

	// The control: carl, a member of another project, may not write this
	// project's leases or publish its events, and the server says so.
	carl := e.Mint(t, "carl", perms(cred.MemberGrant("carl", "cart-svc", "shop")))
	e.Expect(`"$KV.ape-leases.milestone.notes-svc.M1"`)
	e.Expect(`"ape.evt.carl.notes-svc.project.M1.milestone-paused"`)
	errs := make(chan error, 4)
	cnc, err := nats.Connect(e.URL, append(inboxOf(t, carl), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }))...)
	require.NoError(t, err)
	defer cnc.Close()
	for _, s := range []string{"$KV.ape-leases.milestone.notes-svc.M1", "ape.evt.carl.notes-svc.project.M1.milestone-paused"} {
		require.NoError(t, cnc.Publish(s, []byte("{}")))
		select {
		case err := <-errs:
			require.ErrorContains(t, err, s)
		case <-time.After(2 * time.Second):
			t.Fatalf("control: %s was not refused", s)
		}
	}
}

// inboxOf is a credential's connect options as natsconn.Connect sets
// them: the credential, and its user's inbox prefix.
func inboxOf(t *testing.T, creds string) []nats.Option {
	t.Helper()
	id, err := natsconn.DecodeIdentity(creds)
	require.NoError(t, err)
	return []nats.Option{nats.UserCredentials(creds), nats.CustomInboxPrefix(natsconn.InboxPrefix(id.SubjectToken))}
}

// perms is a grant as natstest mints it.
func perms(g aped.Grant) natstest.Perms {
	return natstest.Perms{PubAllow: g.PubAllow, PubDeny: g.PubDeny, SubAllow: g.SubAllow, SubDeny: g.SubDeny, AllowResponses: g.AllowResponses}
}

// V12c on an enforcing server: bob, a member of the same project who does
// not hold M1, publishes a person pause of M1 under his own token (his
// grant allows it) — ann's gate, the holder's, is not paused by it. He
// cannot publish one as ann: the server refuses the subject. The control:
// ann's own pause refuses her gate.
func TestMemberGrant_APersonPauseByAnotherMemberDoesNotHold(t *testing.T) {
	ctx := context.Background()
	e, _ := enforcedCluster(t)
	ann := enforcedCheckout(t, e, block)
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)

	bobCreds := e.Mint(t, "bob", perms(cred.MemberGrant("bob", "notes-svc", "shop")))
	errs := make(chan error, 4)
	bob, err := nats.Connect(e.URL, append(inboxOf(t, bobCreds), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }))...)
	require.NoError(t, err)
	defer bob.Close()
	bjs, err := jetstream.New(bob)
	require.NoError(t, err)
	_, err = events.Publish(ctx, bjs, events.Subject("bob", "notes-svc", events.KindProject, "M1", apexv1.EventMilestonePaused), "bob/pause/M1",
		[]byte(`{"kind":"person","by":"bob","user":"bob","reason":"mine now"}`))
	require.NoError(t, err, "his own subject: allowed")
	require.NoError(t, Gate(ctx, ann, "change start", "M1", time.Now()), "a non-holder's person pause does not hold the holder")

	forged := events.Subject("ann", "notes-svc", events.KindProject, "M1", apexv1.EventMilestonePaused)
	e.Expect(`"` + forged + `"`)
	require.NoError(t, bob.Publish(forged, []byte(`{"kind":"person","by":"ann","user":"ann"}`)))
	select {
	case err := <-errs:
		require.ErrorContains(t, err, forged, "the server refuses bob publishing as ann")
	case <-time.After(2 * time.Second):
		t.Fatal("bob's forged pause was not refused")
	}

	_, err = PauseMilestone(ctx, ann, "M1", "a look", true)
	require.NoError(t, err)
	require.Equal(t, apexv1.CodeCoordMilestonePaused, leaseCode(t, Gate(ctx, ann, "change start", "M1", time.Now())), "control: the holder's pause holds")
}

// V12: a member asks the App service for a token (push, read, pr) under
// exactly cred.MemberGrant, as its own user; the server refuses nothing.
// The control: it may not ask as another user.
func TestMemberGrant_TheAppTokenUnderIt(t *testing.T) {
	e, _ := enforcedCluster(t)
	admin, err := nats.Connect(e.URL, nats.UserCredentials(e.Admin))
	require.NoError(t, err)
	defer admin.Close()
	_, err = admin.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) { _ = m.Respond([]byte(`{"ok":true,"token":"ghs_x"}`)) })
	require.NoError(t, err)
	require.NoError(t, admin.Flush())
	ann := enforcedCheckout(t, e, queued(block)+"  github:\n    repo: shop/notes\n    app: true\n")
	m, err := Load(ann)
	require.NoError(t, err)
	for _, purpose := range []string{"push", "read", "pr"} {
		tok, err := m.GitHubTokenFor(context.Background(), purpose)
		require.NoError(t, err, purpose)
		require.Equal(t, "ghs_x", tok)
	}

	e.Expect(`"` + ghapp.TokenSubject + `.bob"`)
	nc, err := nats.Connect(e.URL, inboxOf(t, m.Creds)...)
	require.NoError(t, err)
	defer nc.Close()
	actx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = ghapp.Ask(actx, nc, ghapp.TokenSubject, "bob", ghapp.Request{Repo: "shop/notes", Purpose: "push"})
	require.Error(t, err, "control: not as another user")
}

// V12d (R1): a call the grant denies is never answered, so it read as NATS
// not answering and a run waited out its bound for a grant gap. Now the
// server's violation is seen on the connection: the fence, and the gate's
// pause read, refuse at once with coord.membership. The control: the full
// grant claims, gates and fences (TestMemberGrant_TheMemberVerbsUnderIt).
func TestMemberGrant_AGrantGapRefusesRatherThanWaits(t *testing.T) {
	ctx := context.Background()
	e, _ := enforcedCluster(t)
	ann := enforcedCheckout(t, e, block)
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)

	// The same checkout, re-minted without one verb the read needs.
	narrowed := func(drop string) {
		t.Helper()
		g := cred.MemberGrant("ann", "notes-svc", "shop")
		var pub []string
		for _, s := range g.PubAllow {
			if !strings.HasPrefix(s, drop) {
				pub = append(pub, s)
			}
		}
		g.PubAllow = pub
		data, err := os.ReadFile(e.Mint(t, "ann", perms(g)))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(ann.Root, "hot", "ape-nats.creds"), data, 0o600))
	}
	run := WithRunWait(ctx, time.Minute, io.Discard)

	narrowed("$JS.API.DIRECT.GET.KV_ape-leases")
	e.Expect("$JS.API.DIRECT.GET.KV_ape-leases")
	start := time.Now()
	err = FenceMilestone(run, ann, "M1")
	require.Equal(t, apexv1.CodeCoordMembership, leaseCode(t, err), "%v", err)
	require.ErrorContains(t, err, "the server refused this credential's call")
	require.Less(t, time.Since(start), 20*time.Second, "refused, not waited out (the bound is a minute)")

	narrowed("$JS.API.STREAM.MSG.GET.APE_EVENTS")
	e.Expect("$JS.API.STREAM.MSG.GET.APE_EVENTS")
	e.Expect("$JS.API.DIRECT.GET.APE_EVENTS")
	start = time.Now()
	err = GateWait(ctx, ann, "change start", "M1", time.Minute, io.Discard)
	require.Equal(t, apexv1.CodeCoordMembership, leaseCode(t, err), "%v", err)
	require.ErrorContains(t, err, "pause state")
	require.Less(t, time.Since(start), 20*time.Second, "the gate refuses a grant gap, not waits")
}
