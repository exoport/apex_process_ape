package cred_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func perms(g aped.Grant) natstest.Perms {
	return natstest.Perms{PubAllow: g.PubAllow, PubDeny: g.PubDeny, SubAllow: g.SubAllow, SubDeny: g.SubDeny, AllowResponses: g.AllowResponses}
}

// connect is a connection as ape makes one (natsconn.Connect): the
// credential, and replies to its user's own inboxes.
func connect(t *testing.T, url, creds string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, inboxOf(t, creds)...)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// inboxOf is a credential's connect options, with its user's inbox prefix.
func inboxOf(t *testing.T, creds string) []nats.Option {
	t.Helper()
	id, err := natsconn.DecodeIdentity(creds)
	require.NoError(t, err)
	return []nats.Option{nats.UserCredentials(creds), nats.CustomInboxPrefix(natsconn.InboxPrefix(id.SubjectToken))}
}

// acker is a conductor's terminal that acknowledges every delivery, with
// `ape conductor ack`'s call, on the conductor's own connection.
type acker struct {
	nc    *nats.Conn
	mu    sync.Mutex
	typed []string
}

var ackRe = regexp.MustCompile(`ape conductor ack '([^']+)'`)

func (a *acker) Idle() bool { return true }
func (a *acker) Type(text string) error {
	a.mu.Lock()
	a.typed = append(a.typed, text)
	a.mu.Unlock()
	return conductor.Ack(a.nc, ackRe.FindStringSubmatch(text)[1])
}

func (a *acker) saw(s string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.typed {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// coordProc is a coordinator the supervisor started, in-process on its
// own connection with the coordinator's credential; Stop is a kill.
type coordProc struct {
	cancel context.CancelFunc
	nc     *nats.Conn
}

func (p *coordProc) Stop() { p.nc.Close(); p.cancel() }

// V12/V12b (C1, C2; D758, D806+): the ensemble runs with each credential
// kind holding exactly its own allow-list — the supervisor
// cred.SupervisorGrant, the coordinator it starts cred.CoordinatorGrant,
// the members cred.MemberGrant, ann's conductor on her member credential
// — on an enforcing server that refuses nothing (natstest.Enforced fails
// the test on any refusal). It walks what each kind does: ensure and a
// coordinator start, its lease and status, a slot, the assessor's batch
// and notices, a lease the coordinator sees expire, the conductor's
// lease, deliveries, acks and heartbeats, a health report verified, and a
// killed coordinator replaced (the supervisor clears its lease).
func TestGrants_TheEnsembleUnderItsOwnAllowLists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	e := natstest.StartEnforced(t)
	admin := connect(t, e.URL, e.Admin)
	ajs, err := jetstream.New(admin)
	require.NoError(t, err)
	require.NoError(t, lease.Declare(ctx, ajs, 1))
	require.NoError(t, slots.Declare(ctx, ajs, 1))
	require.NoError(t, events.Declare(ctx, ajs, 1))
	als, err := lease.Open(ctx, ajs)
	require.NoError(t, err)
	require.NoError(t, als.SetEpoch(ctx, 1))
	var seenMu sync.Mutex
	var seen []string
	sub, err := admin.Subscribe("ape.>", func(m *nats.Msg) { seenMu.Lock(); seen = append(seen, m.Subject); seenMu.Unlock() })
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	saw := func(suffix string) bool {
		seenMu.Lock()
		defer seenMu.Unlock()
		for _, s := range seen {
			if strings.HasSuffix(s, suffix) {
				return true
			}
		}
		return false
	}

	supCreds := e.Mint(t, "sup-a", perms(cred.SupervisorGrant("sup-a")))
	coordCreds := e.Mint(t, "coordinator", perms(cred.CoordinatorGrant("coordinator", "atomix")))
	annCreds := e.Mint(t, "ann", perms(cred.MemberGrant("ann", "taskforge", "atomix")))
	bobCreds := e.Mint(t, "bob", perms(cred.MemberGrant("bob", "taskforge", "atomix")))

	batches := make(chan struct{}, 4)
	var ids atomic.Int32
	var procMu sync.Mutex
	var procs []*coordProc
	start := func(parent context.Context, ens string) (supervisor.Handle, error) {
		nc, err := nats.Connect(e.URL, inboxOf(t, coordCreds)...)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprintf("coord-%d", ids.Add(1))
		cctx, cancel := context.WithCancel(parent)
		go func() {
			_ = coordinator.Run(cctx, nc, coordinator.Config{
				Ensemble: ens, ID: id, TTL: 3 * time.Second, Renew: time.Second, Projects: []string{"taskforge"},
				Assessor: &coordinator.AssessorConfig{
					Quiet: 300 * time.Millisecond, Ceiling: 3 * time.Second,
					OnBatch: func(ctx context.Context, b coordinator.Batch, p coordinator.Publisher) {
						if _, err := p.Published(ctx, b.ID, "taskforge"); err != nil {
							t.Errorf("assessor Published: %v", err)
						}
						if _, _, err := p.Notices(ctx, b.ID, "taskforge", []string{b.Events[0].MsgID},
							[]coordinator.Notice{{Target: "taskforge/M2", Reason: "r", Evidence: []string{"e"}, Hold: true}}); err != nil {
							t.Errorf("assessor Notices: %v", err)
						}
						if err := p.Metrics(b.ID, "taskforge", coordinator.AssessMetrics{Batch: b.ID, Project: "taskforge", Ensemble: "atomix"}); err != nil {
							t.Errorf("assessor Metrics: %v", err)
						}
						batches <- struct{}{}
					},
				},
			})
		}()
		p := &coordProc{cancel: cancel, nc: nc}
		procMu.Lock()
		procs = append(procs, p)
		procMu.Unlock()
		return p, nil
	}
	snc := connect(t, e.URL, supCreds)
	sctx, stopSup := context.WithCancel(ctx)
	supDone := make(chan struct{})
	go func() {
		defer close(supDone)
		_ = supervisor.Run(sctx, snc, supervisor.Config{ID: "sup-a", Start: start, Verify: true, TTL: 3 * time.Second, Renew: time.Second, HeartbeatEvery: 500 * time.Millisecond})
	}()
	// The supervisor and its coordinators end before anything the test
	// made goes (cleanups run last first): a supervisor still running
	// could start a coordinator from a credential already removed.
	t.Cleanup(func() {
		stopSup()
		<-supDone
		procMu.Lock()
		defer procMu.Unlock()
		for _, p := range procs {
			p.Stop()
		}
	})

	// ann's ape: `ape supervisor ensure|status`, on her member credential.
	ann := connect(t, e.URL, annCreds)
	request := func(subj string, body any) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		var m *nats.Msg
		require.Eventually(t, func() bool {
			m, err = ann.Request(subj, data, time.Second)
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "%s answers", subj)
		return m.Data
	}
	coordID := func() string {
		m, err := ann.Request("ape.coord.atomix.status", nil, time.Second)
		if err != nil {
			return ""
		}
		var st coordinator.Status
		_ = json.Unmarshal(m.Data, &st)
		return st.ID
	}
	request("ape.sup.status", nil)
	once := func(subj string, body any) string {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		m, err := ann.Request(subj, data, 20*time.Second)
		require.NoError(t, err, subj)
		return string(m.Data)
	}
	require.Contains(t, once("ape.sup.ensure", supervisor.EnsureRequest{Ensemble: "atomix"}), "started")
	require.Eventually(t, func() bool { return coordID() == "coord-1" }, 15*time.Second, 100*time.Millisecond)

	// The coordinator grants a slot (its own request, as a gate's ape
	// asks it today: on the admin's connection, since no member verb
	// does).
	m, err := admin.Request("ape.coord.atomix.slot.acquire", []byte(`{"host":"h","resource":"port-8080","capacity":1,"holder":"ann","ttl_secs":60}`), 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, string(m.Data), "slot.h.port-8080.0")

	// ann's conductor, on her member credential.
	cnc := connect(t, e.URL, annCreds)
	a := &acker{nc: cnc}
	go func() {
		_ = conductor.Run(ctx, cnc, conductor.Config{
			User: "ann", Ensemble: "atomix", Projects: []string{"taskforge"}, Holder: "cond-a", Nonce: "n",
			LeaseTTL: 3 * time.Second, Renew: 500 * time.Millisecond, AckWait: time.Second,
			Escalate: 2 * time.Second, Pause: 4 * time.Second, Session: a,
		})
	}()
	require.Eventually(t, func() bool {
		h, _, err := als.Holder(ctx, layout.ConductorKey("ann", "atomix"))
		return err == nil && h == "cond-a"
	}, 10*time.Second, 100*time.Millisecond, "the conductor holds its lease")
	time.Sleep(300 * time.Millisecond) // its consumer is bound

	// bob, a member: a question the conductor delivers and ann acks; a
	// trigger the assessor batches; a milestone lease left to expire.
	bob := connect(t, e.URL, bobCreds)
	bjs, err := jetstream.New(bob)
	require.NoError(t, err)
	_, err = events.Publish(ctx, bjs, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), "taskforge/b/1", []byte(`{"q":"which port?"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.saw("project.M2.question") }, 10*time.Second, 50*time.Millisecond, "delivered")
	_, err = events.Publish(ctx, bjs, events.Subject("bob", "taskforge", events.KindProject, "M2", "change-archived"), "taskforge/b/2", []byte(`{"change":"c1"}`))
	require.NoError(t, err)
	select {
	case <-batches:
	case <-time.After(15 * time.Second):
		t.Fatal("the assessor's batch never closed")
	}
	require.Eventually(t, func() bool { return a.saw("impact-notice") }, 10*time.Second, 50*time.Millisecond, "the notice reaches the conductor")
	bls, err := lease.Open(ctx, bjs)
	require.NoError(t, err)
	_, err = bls.Claim(ctx, layout.MilestoneKey("taskforge", "M3"), "bob/c", time.Second)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return saw(".lease-expired") }, 15*time.Second, 100*time.Millisecond, "the coordinator sees it expire")
	require.Eventually(t, func() bool { return a.saw(".heartbeat") }, 10*time.Second, 50*time.Millisecond, "the supervisor's heartbeat is delivered")

	// A health report about a healthy coordinator is not believed.
	require.Contains(t, once("ape.sup.health.report", supervisor.HealthReport{Ensemble: "atomix", Reporter: "ann", Subject: "ape.coord.atomix.status", SentAt: time.Now()}), "unconfirmed")
	// A killed one is confirmed and replaced; the supervisor clears its
	// lease.
	procMu.Lock()
	procs[len(procs)-1].Stop()
	procMu.Unlock()
	require.Eventually(t, func() bool { return coordID() == "" }, 5*time.Second, 50*time.Millisecond)
	require.Contains(t, once("ape.sup.health.report", supervisor.HealthReport{Ensemble: "atomix", Reporter: "ann", Subject: "ape.coord.atomix.status", SentAt: time.Now()}), "replaced")
	require.Eventually(t, func() bool { return coordID() == "coord-2" }, 15*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return saw(".coordinator-replaced") }, 5*time.Second, 50*time.Millisecond)
}

// streamAdmin is the JetStream API no credential kind may call: a stream's
// create, update, delete, purge, message delete, snapshot and restore on
// every coordination stream, a new stream, and the account- and
// cluster-wide verbs.
func streamAdmin() []string {
	var out []string
	for _, s := range []string{"APE_EVENTS", "APE_METRICS", "KV_ape-leases", "KV_ape-meta", "KV_ape-slots"} {
		for _, v := range []string{"CREATE", "UPDATE", "DELETE", "PURGE", "MSG.DELETE", "SNAPSHOT", "RESTORE"} {
			out = append(out, "$JS.API.STREAM."+v+"."+s)
		}
	}
	return append(out, "$JS.API.STREAM.CREATE.NEW", "$JS.API.ACCOUNT.PURGE.APP", "$JS.API.META.LEADER.STEPDOWN", "$JS.API.SERVER.REMOVE",
		"$JS.API.STREAM.PEER.REMOVE.APE_EVENTS", "$JS.API.STREAM.LEADER.STEPDOWN.APE_EVENTS")
}

// V12-4 (research's REVIEW-V12): every grant is no wider than its kind
// needs — the harness's refusal mode. For each kind, the server refuses
// the stream administration C1 used and its siblings (stream create and
// update included), the subscriptions another person's replies or the App
// service's requests arrive on (V12-1: a member read another's App token
// on _INBOX.>, and the warden's secrets request on ape.sup.ghapp.>), and
// publishing as another user or kind.
func TestGrants_NoWiderThanTheKindNeeds(t *testing.T) {
	e := natstest.StartEnforced(t)
	opt := func(creds string) []nats.Option { return inboxOf(t, creds)[1:] }
	others := []string{"_INBOX.>", "_INBOX.bob.>", "ape.>", "ape.sup.ghapp.>", "ape.sup.ghapp.pusher.bob", "ape.sup.ghapp.warden.*", "ape.sup.ghapp.warden.op"}

	ann := e.Mint(t, "ann", perms(cred.MemberGrant("ann", "taskforge", "atomix")))
	e.RequireRefused(t, ann, opt(ann), append(streamAdmin(),
		"ape.evt.bob.taskforge.project.M1.milestone-paused", "ape.evt.coord.atomix.coord.x.impact-notice", "ape.evt.ann.other.project.M1.gate-green",
		"$KV.ape-leases.sup.leader", "$KV.ape-leases.coord.atomix", "$KV.ape-leases.milestone.other.M1", "$KV.ape-leases.close.other",
		"$KV.ape-leases.conductor.bob.atomix", "$KV.ape-meta.epoch", "$KV.ape-slots.slot.h.p.0",
		"ape.sup.ghapp.pusher.bob", "ape.sup.ghapp.warden.bob", "_INBOX.bob.x", "$JS.ACK.APE_EVENTS.conductor_bob_atomix.1.1.1.1.0",
	), append(others, "ape.evt.*.other.project.>", "ape.evt.coord.>", "ape.coord.atomix.>", "ape.sup.ensure", "$SRV.>"))

	coord := e.Mint(t, "coordinator", perms(cred.CoordinatorGrant("coordinator", "atomix")))
	e.RequireRefused(t, coord, opt(coord), append(streamAdmin(),
		"ape.evt.ann.taskforge.project.M1.milestone-resumed", "ape.evt.coord.other.coord.x.impact-notice",
		"$KV.ape-leases.sup.leader", "$KV.ape-leases.coord.other", "$KV.ape-leases.milestone.taskforge.M1", "$KV.ape-meta.epoch",
		"ape.sup.ghapp.pusher.ann", "ape.sup.health.report",
	), append(others, "ape.evt.>", "ape.sup.>", "ape.coord.other.>"))

	sup := e.Mint(t, "sup-a", perms(cred.SupervisorGrant("sup-a")))
	e.RequireRefused(t, sup, opt(sup), append(streamAdmin(),
		"ape.evt.ann.taskforge.project.M1.milestone-resumed", "$KV.ape-leases.milestone.taskforge.M1", "$KV.ape-leases.conductor.ann.atomix",
		"$KV.ape-meta.epoch", "ape.sup.ghapp.pusher.ann", "ape.coord.atomix.slot.acquire",
	), append(others, "ape.evt.>", "ape.coord.>"))
}

// V12-3 (research's REVIEW-V12; R2, 2a rows 5a/5b): a person's credential
// reaches no consumer but its own conductor's durable, by exact name, so
// bob can neither delete ann's durable (5a: her attach deaf and spinning,
// FINDING-2A-3) nor re-create it with a filter that matches nothing (5b:
// deaf, silently), nor read it, nor make an ephemeral consumer on the
// stream; the server refuses each. The control: ann's own conductor, on
// her member credential, binds, delivers, and binds again after the
// operator deletes the durable, with no refusal (Enforced fails the test
// on any it did not expect), and her notices read by gets alone.
func TestGrants_V12_3_AnotherPersonsDurable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e := natstest.StartEnforced(t)
	admin := connect(t, e.URL, e.Admin)
	ajs, err := jetstream.New(admin)
	require.NoError(t, err)
	require.NoError(t, lease.Declare(ctx, ajs, 1))
	require.NoError(t, events.Declare(ctx, ajs, 1))
	als, err := lease.Open(ctx, ajs)
	require.NoError(t, err)
	require.NoError(t, als.SetEpoch(ctx, 1))
	durable := layout.ConductorConsumer("ann", "atomix")

	annCreds := e.Mint(t, "ann", perms(cred.MemberGrant("ann", "taskforge", "atomix")))
	cnc := connect(t, e.URL, annCreds)
	a := &acker{nc: cnc}
	var logMu sync.Mutex
	var logged []string
	go func() {
		_ = conductor.Run(ctx, cnc, conductor.Config{
			User: "ann", Ensemble: "atomix", Projects: []string{"taskforge"}, Holder: "cond-a", Nonce: "n",
			LeaseTTL: 3 * time.Second, Renew: 500 * time.Millisecond, AckWait: time.Second,
			Escalate: time.Minute, Pause: 2 * time.Minute, Session: a,
			Log: func(f string, args ...any) {
				logMu.Lock()
				logged = append(logged, fmt.Sprintf(f, args...))
				logMu.Unlock()
			},
		})
	}()
	require.Eventually(t, func() bool { _, err := ajs.Consumer(ctx, events.EventsStream, durable); return err == nil }, 10*time.Second, 50*time.Millisecond, "ann's conductor binds its durable on her credential")
	before, err := ajs.Consumer(ctx, events.EventsStream, durable)
	require.NoError(t, err)
	created := before.CachedInfo().Created

	bobCreds := e.Mint(t, "bob", perms(cred.MemberGrant("bob", "taskforge", "atomix")))
	bob := connect(t, e.URL, bobCreds)
	bjs, err := jetstream.New(bob)
	require.NoError(t, err)
	_, err = events.Publish(ctx, bjs, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), "taskforge/b/1", []byte(`{"q":"one"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.saw("project.M2.question") }, 10*time.Second, 50*time.Millisecond, "delivered to ann")

	// bob, through the API as the nats CLI would call it (5a, 5b), each
	// refused on the server's own line; ann's durable unchanged.
	try := func(what string, call func(context.Context) error, line string) {
		t.Helper()
		e.Expect(line)
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
		defer ccancel()
		err := call(cctx)
		t.Logf("V12-3: bob %s: %v", what, err)
		require.Error(t, err, "bob %s", what)
		require.Eventually(t, func() bool { return strings.Contains(strings.Join(e.Violations(), "\n"), line) }, 5*time.Second, 50*time.Millisecond, "the server refuses bob's %s", what)
	}
	try("deletes ann's durable", func(c context.Context) error { return bjs.DeleteConsumer(c, events.EventsStream, durable) },
		`Publish Violation - Subject "$JS.API.CONSUMER.DELETE.`+events.EventsStream+`.`+durable+`"`)
	try("re-creates it with a filter that matches nothing", func(c context.Context) error {
		_, err := bjs.CreateOrUpdateConsumer(c, events.EventsStream, jetstream.ConsumerConfig{
			Durable: durable, FilterSubjects: []string{"ape.evt.nobody.>", "ape.evt.none.>"}, AckPolicy: jetstream.AckExplicitPolicy,
		})
		return err
	}, `Publish Violation - Subject "$JS.API.CONSUMER.CREATE.`+events.EventsStream+`.`+durable+`"`)
	try("re-creates it on one filter", func(c context.Context) error {
		_, err := bjs.CreateOrUpdateConsumer(c, events.EventsStream, jetstream.ConsumerConfig{
			Durable: durable, FilterSubject: "ape.evt.nobody.>", AckPolicy: jetstream.AckExplicitPolicy,
		})
		return err
	}, `Publish Violation - Subject "$JS.API.CONSUMER.CREATE.`+events.EventsStream+`.`+durable+`.ape.evt.nobody.>"`)
	try("reads its info", func(c context.Context) error { _, err := bjs.Consumer(c, events.EventsStream, durable); return err },
		`Publish Violation - Subject "$JS.API.CONSUMER.INFO.`+events.EventsStream+`.`+durable+`"`)
	try("pulls from it", func(_ context.Context) error {
		_, err := bob.Request("$JS.API.CONSUMER.MSG.NEXT."+events.EventsStream+"."+durable, []byte(`{"batch":1,"expires":500000000}`), time.Second)
		return err
	}, `Publish Violation - Subject "$JS.API.CONSUMER.MSG.NEXT.`+events.EventsStream+`.`+durable+`"`)
	try("makes an ordered consumer on the stream", func(c context.Context) error {
		oc, err := bjs.OrderedConsumer(c, events.EventsStream, jetstream.OrderedConsumerConfig{FilterSubjects: []string{"ape.evt.>"}})
		if err != nil {
			return err
		}
		_, err = oc.Fetch(1, jetstream.FetchMaxWait(time.Second))
		return err
	}, `Publish Violation - Subject "$JS.API.CONSUMER.CREATE.`+events.EventsStream+`.`)
	after, err := ajs.Consumer(ctx, events.EventsStream, durable)
	require.NoError(t, err)
	require.Equal(t, created, after.CachedInfo().Created, "ann's durable is the one her conductor made")
	require.Equal(t, conductor.Filters("atomix", []string{"taskforge"}), after.CachedInfo().Config.FilterSubjects)

	// The control: the operator deletes it; ann's conductor binds it again
	// on her own credential and gets the next event.
	require.NoError(t, ajs.DeleteConsumer(ctx, events.EventsStream, durable))
	require.Eventually(t, func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer again") })
	}, 10*time.Second, 50*time.Millisecond, "ann's conductor binds its durable again")
	_, err = events.Publish(ctx, bjs, events.Subject("bob", "taskforge", events.KindProject, "M3", "question"), "taskforge/b/2", []byte(`{"q":"two"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.saw("project.M3.question") }, 10*time.Second, 50*time.Millisecond, "the next event delivered")

	// And a person's read of the stream needs no consumer: ann's notice
	// acks, by gets.
	ajsAnn, err := jetstream.New(cnc)
	require.NoError(t, err)
	for i := range 3 {
		_, err = events.Publish(ctx, ajsAnn, events.Subject("ann", "taskforge", events.KindProject, "M1", apexv1.EventNoticeAcked), fmt.Sprintf("taskforge/a/%d", i), fmt.Appendf(nil, `{"id":"n%d"}`, i))
		require.NoError(t, err)
	}
	acks, ok := member.ReadAcks(ctx, ajsAnn, "taskforge")
	require.True(t, ok)
	require.Len(t, acks, 3)
}

// cred names the App service's subjects itself (so ghapp's tests can
// import it); they must be ghapp's.
func TestGrants_AppSubjectsAreGHApps(t *testing.T) {
	g := cred.GHAppGrant("svc")
	require.Contains(t, g.SubAllow, ghapp.TokenSubject+".*")
	require.Contains(t, g.SubAllow, ghapp.SetupSubject+".*")
	m := cred.MemberGrant("ann", "taskforge", "atomix")
	require.Contains(t, m.PubAllow, ghapp.TokenSubject+".ann")
	require.Contains(t, m.PubAllow, ghapp.SetupSubject+".ann")
}

// V12-10: under the old name conductor-<user>-<ens>, ann in ensemble x-y
// and ann-x in ensemble y shared one durable, conductor-ann-x-y, and each
// credential reached the other's. Now each has its own
// (conductor_ann_x-y, conductor_ann-x_y), and the server refuses ann-x
// every subject of ann's: create, info, pull and ack.
func TestGrants_V12_10_CollidingPairsHaveTheirOwnDurables(t *testing.T) {
	require.Equal(t, layout.LegacyConductorConsumer("ann", "x-y"), layout.LegacyConductorConsumer("ann-x", "y"), "the old names collide")
	ann, annX := layout.ConductorConsumer("ann", "x-y"), layout.ConductorConsumer("ann-x", "y")
	require.NotEqual(t, ann, annX)
	e := natstest.StartEnforced(t)
	creds := e.Mint(t, "ann-x", perms(cred.MemberGrant("ann-x", "taskforge", "y")))
	pre := "$JS.API.CONSUMER."
	e.RequireRefused(t, creds, inboxOf(t, creds)[1:], []string{
		pre + "CREATE." + events.EventsStream + "." + ann,
		pre + "INFO." + events.EventsStream + "." + ann,
		pre + "MSG.NEXT." + events.EventsStream + "." + ann,
		"$JS.ACK." + events.EventsStream + "." + ann + ".1.1.1.1.0",
		pre + "CREATE." + events.EventsStream + "." + layout.LegacyConductorConsumer("ann-x", "y"),
	}, nil)
	g := cred.MemberGrant("ann-x", "taskforge", "y")
	require.Contains(t, g.PubAllow, pre+"CREATE."+events.EventsStream+"."+annX, "the control: its own durable")
	require.Contains(t, g.PubAllow, pre+"INFO."+events.EventsStream+"."+layout.LegacyConductorConsumer("ann-x", "y"), "and the old name, read only")
}
