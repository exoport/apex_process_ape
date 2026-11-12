package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

const (
	ttl   = 3 * time.Second
	renew = time.Second
)

// rig is a JetStream server with the layout declared and epoch 1.
func rig(t *testing.T) (url string, nc *nats.Conn, js jetstream.JetStream) {
	t.Helper()
	url = natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err = jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	return url, nc, js
}

// proc is a coordinator run in-process on its own connection; Stop is a
// kill: the connection drops, nothing is released.
type proc struct {
	cancel  context.CancelFunc
	nc      *nats.Conn
	ens, id string
	done    chan struct{} // Run returned
}

// Stop waits, at most startWait, until the coordinator is past its start
// (Run returned, or its status answers with its id) before the kill:
// nats.go v1.53.1's micro.AddService races a close of its connection
// (its closed-handler iterates the handler map AddService is still
// writing), which a replace right after a start met under -race
// (RACE-RESTARTBUDGET). A real coordinator is a process, killed whole.
func (p *proc) Stop() {
	deadline := time.Now().Add(startWait)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			deadline = time.Time{}
			continue
		default:
		}
		if m, err := p.nc.Request("ape.coord."+p.ens+".status", nil, 250*time.Millisecond); err == nil && strings.Contains(string(m.Data), `"id":"`+p.id+`"`) {
			break
		}
	}
	p.nc.Close()
	p.cancel()
}

// startWait bounds proc.Stop's wait for a coordinator's start.
const startWait = 5 * time.Second

// procs are the coordinators the test supervisors started, newest last.
var procs struct {
	sync.Mutex
	list []*proc
}

func starter(t *testing.T, url string, ids *atomic.Int32, opts ...nats.Option) Starter {
	t.Helper()
	return func(parent context.Context, ens string) (Handle, error) {
		nc, err := nats.Connect(url, opts...)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprintf("coord-%d", ids.Add(1))
		ctx, cancel := context.WithCancel(parent)
		p := &proc{cancel: cancel, nc: nc, ens: ens, id: id, done: make(chan struct{})}
		go func() {
			defer close(p.done)
			_ = coordinator.Run(ctx, nc, coordinator.Config{Ensemble: ens, ID: id, TTL: ttl, Renew: renew})
		}()
		procs.Lock()
		procs.list = append(procs.list, p)
		procs.Unlock()
		return p, nil
	}
}

func runSup(t *testing.T, url, id string, cfg Config) (cancel context.CancelFunc, nc *nats.Conn) {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cfg.ID, cfg.TTL, cfg.Renew = id, ttl, renew
	go func() {
		if err := Run(ctx, nc, cfg); err != nil {
			t.Logf("supervisor %s: %v", id, err)
		}
	}()
	t.Cleanup(func() { cancel(); nc.Close() })
	return cancel, nc
}

func request[T any](t *testing.T, nc *nats.Conn, subj string, body any) T {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	m, err := nc.Request(subj, data, 10*time.Second)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(m.Data, &out), "%s", m.Data)
	return out
}

func coordID(t *testing.T, nc *nats.Conn) string {
	t.Helper()
	m, err := nc.Request("ape.coord.atomix.status", nil, time.Second)
	if err != nil {
		return ""
	}
	var st coordinator.Status
	_ = json.Unmarshal(m.Data, &st)
	return st.ID
}

func waitLeader(t *testing.T, nc *nats.Conn) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := nc.Request("ape.sup.status", nil, 500*time.Millisecond)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "a supervisor leads")
}

func coordEvents(t *testing.T, nc *nats.Conn) (*[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	sub, err := nc.Subscribe("ape.evt.coord.>", func(m *nats.Msg) { mu.Lock(); got = append(got, m.Subject); mu.Unlock() })
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return &got, &mu
}

func has(got *[]string, mu *sync.Mutex, suffix string) bool {
	mu.Lock()
	defer mu.Unlock()
	for _, s := range *got {
		if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

// ensure starts a coordinator once; it then serves slots for the ensemble.
func TestEnsureAndSlots(t *testing.T) {
	url, nc, _ := rig(t)
	var ids atomic.Int32
	runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), Verify: true})
	waitLeader(t, nc)
	require.Equal(t, "started", request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})["action"])
	require.Eventually(t, func() bool { return coordID(t, nc) == "coord-1" }, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, "running", request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})["action"])

	sl := request[coordinator.SlotReply](t, nc, "ape.coord.atomix.slot.acquire", coordinator.AcquireRequest{Host: "mmq4", Resource: "port-8080", Capacity: 1, Holder: "ann", TTLSecs: 60})
	require.Equal(t, "slot.mmq4.port-8080.0", sl.Key)
	require.Equal(t, "coord-1", sl.Granted)
	m, err := nc.Request("ape.coord.atomix.slot.acquire", mustJSON(t, coordinator.AcquireRequest{Host: "mmq4", Resource: "port-8080", Capacity: 1, Holder: "bob"}), 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, "409", m.Header.Get("Nats-Service-Error-Code"), "the port is held")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// Drill 1: a killed coordinator is confirmed dead (no answer), revoked and
// replaced; milestone leases are untouched.
func TestDrill_KillACoordinator(t *testing.T) {
	url, nc, js := rig(t)
	ctx := context.Background()
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	m2, err := ls.Claim(ctx, layout.MilestoneKey("taskforge", "M2"), "ann", time.Minute)
	require.NoError(t, err)
	got, mu := coordEvents(t, nc)

	var ids atomic.Int32
	runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), Verify: true})
	waitLeader(t, nc)
	request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})
	require.Eventually(t, func() bool { return coordID(t, nc) == "coord-1" }, 10*time.Second, 50*time.Millisecond)

	// SIGKILL: the coordinator's connection drops, nothing is released.
	procs.Lock()
	procs.list[len(procs.list)-1].Stop()
	procs.Unlock()
	require.Eventually(t, func() bool { return coordID(t, nc) == "" }, 5*time.Second, 50*time.Millisecond)
	v := request[Verdict](t, nc, "ape.sup.health.report", HealthReport{Ensemble: "atomix", Reporter: "ann", Subject: "ape.coord.atomix.slot.acquire", SentAt: time.Now()})
	require.Equal(t, "replaced", v.Action, v.Message)
	require.False(t, v.Checks["ping_answered"])
	require.Eventually(t, func() bool { return coordID(t, nc) == "coord-2" }, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool { return has(got, mu, ".coordinator-replaced") }, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, ls.Fence(ctx, m2.Key, m2.Token), "a milestone lease outlives its coordinator")
}

// Drill 2: a healthy coordinator reported dead by one ape's bad link is
// not killed; with verification off (the control) it is.
func TestDrill_FalseOutage(t *testing.T) {
	for _, verify := range []bool{true, false} {
		t.Run(fmt.Sprintf("verify=%v", verify), func(t *testing.T) {
			url, nc, _ := rig(t)
			got, mu := coordEvents(t, nc)
			var ids atomic.Int32
			runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), Verify: verify})
			waitLeader(t, nc)
			request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})
			require.Eventually(t, func() bool { return coordID(t, nc) == "coord-1" }, 10*time.Second, 50*time.Millisecond)

			v := request[Verdict](t, nc, "ape.sup.health.report", HealthReport{Ensemble: "atomix", Reporter: "ann", What: "timeout"})
			if verify {
				require.Equal(t, "unconfirmed", v.Action)
				require.True(t, v.Checks["lease_fresh"])
				require.True(t, v.Checks["ping_answered"])
				require.Contains(t, v.Message, "your link")
				require.Equal(t, "coord-1", coordID(t, nc), "the healthy coordinator was not touched")
				require.Eventually(t, func() bool { return has(got, mu, ".health-unconfirmed") }, 5*time.Second, 50*time.Millisecond)
			} else {
				require.Equal(t, "replaced", v.Action, "the control: one report kills a healthy coordinator")
				require.Eventually(t, func() bool { return coordID(t, nc) == "coord-2" }, 10*time.Second, 50*time.Millisecond)
			}
		})
	}
}

// The restart budget: past K starts in the window, the supervisor gives up.
func TestRestartBudget(t *testing.T) {
	url, nc, _ := rig(t)
	var ids atomic.Int32
	runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), Verify: false, RestartBudget: 2, RestartWindow: time.Hour})
	waitLeader(t, nc)
	request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})
	v := request[Verdict](t, nc, "ape.sup.health.report", HealthReport{Ensemble: "atomix"})
	require.Equal(t, "replaced", v.Action)
	v = request[Verdict](t, nc, "ape.sup.health.report", HealthReport{Ensemble: "atomix"})
	require.Equal(t, "gave-up", v.Action)
	require.Contains(t, v.Message, "giving up")
}

// OQ9: two supervisors, one leads; when the leader dies without releasing,
// the standby takes over once sup.leader expires.
func TestTwoSupervisors(t *testing.T) {
	url, nc, _ := rig(t)
	var ids atomic.Int32
	cancelA, ncA := runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), Verify: true})
	waitLeader(t, nc)
	runSup(t, url, "sup-b", Config{Start: starter(t, url, &ids), Verify: true})
	who := func() string {
		m, err := nc.Request("ape.sup.status", nil, 500*time.Millisecond)
		if err != nil {
			return ""
		}
		var st map[string]string
		_ = json.Unmarshal(m.Data, &st)
		return st["id"]
	}
	require.Equal(t, "sup-a", who())
	time.Sleep(2 * renew)
	require.Equal(t, "sup-a", who(), "the standby does not serve while the leader renews")
	ncA.Close() // the leader's host dies: no release
	cancelA()
	require.Eventually(t, func() bool { return who() == "sup-b" }, 3*ttl, 100*time.Millisecond, "the standby takes over on expiry")
}

// §3.1 requirement 2: the leader sends every held conductor lease a
// heartbeat, kind coord, its id naming the lease.
func TestHeartbeat(t *testing.T) {
	url, nc, js := rig(t)
	got, mu := coordEvents(t, nc)
	ls, err := lease.Open(context.Background(), js)
	require.NoError(t, err)
	_, err = ls.Claim(context.Background(), layout.ConductorKey("ann", "atomix"), "cond-a", time.Minute)
	require.NoError(t, err)
	var ids atomic.Int32
	runSup(t, url, "sup-a", Config{Start: starter(t, url, &ids), HeartbeatEvery: 300 * time.Millisecond})
	require.Eventually(t, func() bool { return has(got, mu, "atomix.coord.conductor-ann-atomix.heartbeat") }, 5*time.Second, 50*time.Millisecond)
}

// FINDING-V15-1A-10: the 1a run's outage (all three nodes down past the
// claim's deadline) ended the supervisor, and its coordinator with it; and
// nothing brought a coordinator back. Now a failed claim is tried again
// (a), and the leader restarts a coordinator whose lease is stale and
// whose ping goes unanswered (b): after the outage the supervisor still
// runs and the ensemble has a coordinator again, unasked.
func TestDrill_SupervisorRidesOutANATSOutage(t *testing.T) {
	c := natstest.RunCluster(t, 3)
	url := c.URLs()
	nc, err := nats.Connect(url, nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	// The R3 setup under a deadline, as the coordinator's rigs: without one
	// each call took nats.go's 5 s default, and an R3 stream create on a
	// loaded Windows runner outlasted it ("declare APE_METRICS: context
	// deadline exceeded", CI-5GREEN-3 on 083b036).
	dctx, dcancel := context.WithTimeout(ctx, 60*time.Second)
	defer dcancel()
	require.NoError(t, lease.Declare(dctx, js, 3))
	require.NoError(t, slots.Declare(dctx, js, 3))
	require.NoError(t, events.Declare(dctx, js, 3))
	ls, err := lease.Open(dctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(dctx, 1))

	snc, err := nats.Connect(url, nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(snc.Close)
	var ids atomic.Int32
	var logMu sync.Mutex
	var logged []string
	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(sctx, snc, Config{
			// The coordinators' connections do not reconnect: the outage
			// ends coord-1 for good (a coordinator rides out an outage it
			// can come back from, V8n), so (b) is the supervisor's restart.
			ID: "sup-a", Start: starter(t, url, &ids, nats.NoReconnect()), Verify: true, TTL: ttl, Renew: renew, HealthDeadline: 500 * time.Millisecond,
			Logf: func(f string, a ...any) { logMu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); logMu.Unlock() },
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitLeader(t, nc)
	require.Equal(t, "started", request[map[string]string](t, nc, "ape.sup.ensure", EnsureRequest{Ensemble: "atomix"})["action"])
	require.Eventually(t, func() bool { return coordID(t, nc) == "coord-1" }, 10*time.Second, 50*time.Millisecond)

	// The outage: every node down, well past the claim's deadline and the
	// leases' TTL, then back on their data.
	for i := range c.Servers {
		c.Stop(i)
	}
	time.Sleep(3 * ttl)
	select {
	case err := <-done:
		t.Fatalf("the supervisor ended during the outage: %v", err)
	default:
	}
	for i := range c.Servers {
		c.Restart(i)
	}
	c.WaitMetaLeader(30 * time.Second)

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("(a) the supervisor ended after the outage: %v", err)
		default:
		}
		if id := coordID(t, nc); id != "" && id != "coord-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("(b) the supervisor runs, but no coordinator came back: nothing started one unasked")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The supervisor logs the restart once replace returns, which can be
	// after the new coordinator already answers: wait for the line.
	require.Eventually(t, func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		for _, l := range logged {
			if strings.Contains(l, "coordinator was gone (lease stale, ping unanswered); restarted") {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the restart is logged")
}

type termHandle struct{ stopped, terminated *bool }

func (h termHandle) Stop()      { *h.stopped = true }
func (h termHandle) Terminate() { *h.terminated = true }

type stopHandle struct{ stopped *bool }

func (h stopHandle) Stop() { *h.stopped = true }

// D736: the supervisor's exit ends each coordinator gracefully when its
// handle can (Terminate: it releases its lease); a handle that cannot is
// stopped as before (the control).
func TestStopAll_TerminatesWhenItCan(t *testing.T) {
	var s1, t1, s2 bool
	s := &Supervisor{handles: map[string]Handle{"a": termHandle{&s1, &t1}, "b": stopHandle{&s2}}}
	s.stopAll()
	require.True(t, t1, "terminated")
	require.False(t, s1, "not killed")
	require.True(t, s2, "the control: a plain handle is stopped")
	require.Empty(t, s.handles)
}

// FINDING-2A-DRY-2: a leader that loses sup.leader in a NATS outage stops
// its service while the connection is reconnecting. nats.go then leaks the
// endpoints' subscriptions on the server (a drain while reconnecting sends
// no UNSUB, the reconnect re-subscribes them, and the client forgets them),
// so after it leads again each ape.sup.* subject has a dead queue member and
// about half the requests go unanswered. After the outage every request is
// answered (the control: the same count before it).
func TestDrill_ServiceAnswersEveryRequestAfterAnOutage(t *testing.T) {
	c := natstest.RunCluster(t, 3)
	url := c.URLs()
	snc, err := nats.Connect(url, nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(snc.Close)
	js, err := jetstream.New(snc)
	require.NoError(t, err)
	ctx := context.Background()
	// The R3 setup under a deadline, as the coordinator's rigs: without one
	// each call took nats.go's 5 s default, and an R3 stream create on a
	// loaded Windows runner outlasted it ("declare APE_METRICS: context
	// deadline exceeded", CI-5GREEN-3 on 083b036).
	dctx, dcancel := context.WithTimeout(ctx, 60*time.Second)
	defer dcancel()
	require.NoError(t, lease.Declare(dctx, js, 3))
	ls, err := lease.Open(dctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(dctx, 1))

	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(sctx, snc, Config{ID: "sup-a", TTL: ttl, Renew: renew}) }()
	t.Cleanup(func() { cancel(); <-done })

	answered := func() int {
		nc, err := nats.Connect(c.URLs())
		require.NoError(t, err)
		defer nc.Close()
		n := 0
		for range 20 {
			if _, err := nc.Request("ape.sup.status", nil, time.Second); err == nil {
				n++
			}
		}
		return n
	}
	waitLeader(t, snc)
	require.Equal(t, 20, answered(), "the control: every request answered before the outage")

	// The outage outlasts the renewal, so the leader stops serving while
	// its connection is reconnecting.
	for i := range c.Servers {
		c.Stop(i)
	}
	time.Sleep(3 * ttl)
	for i := range c.Servers {
		c.Restart(i)
	}
	c.WaitMetaLeader(30 * time.Second)
	require.Eventually(t, func() bool {
		_, err := snc.Request("ape.sup.status", nil, 500*time.Millisecond)
		return err == nil
	}, 60*time.Second, 200*time.Millisecond, "it leads again")
	require.Equal(t, 20, answered(), "every request answered after the outage")
}
