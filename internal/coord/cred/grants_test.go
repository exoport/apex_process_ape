package cred_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func perms(g aped.Grant) natstest.Perms {
	return natstest.Perms{PubAllow: g.PubAllow, PubDeny: g.PubDeny, SubAllow: g.SubAllow, SubDeny: g.SubDeny, AllowResponses: g.AllowResponses}
}

func connect(t *testing.T, url, creds string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, nats.UserCredentials(creds))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
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

	supCreds := e.Mint(t, "sup-a", perms(cred.SupervisorGrant()))
	coordCreds := e.Mint(t, "coordinator", perms(cred.CoordinatorGrant("atomix")))
	annCreds := e.Mint(t, "ann", perms(cred.MemberGrant("ann", "taskforge", "atomix")))
	bobCreds := e.Mint(t, "bob", perms(cred.MemberGrant("bob", "taskforge", "atomix")))

	batches := make(chan struct{}, 4)
	var ids atomic.Int32
	var procMu sync.Mutex
	var procs []*coordProc
	start := func(parent context.Context, ens string) (supervisor.Handle, error) {
		nc, err := nats.Connect(e.URL, nats.UserCredentials(coordCreds))
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
	defer stopSup()
	go func() {
		_ = supervisor.Run(sctx, snc, supervisor.Config{ID: "sup-a", Start: start, Verify: true, TTL: 3 * time.Second, Renew: time.Second, HeartbeatEvery: 500 * time.Millisecond})
	}()

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
