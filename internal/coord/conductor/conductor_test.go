package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// fakeSession is a conductor's terminal: it records what ape typed, and
// acknowledges the first `answers` deliveries it sees (each once), then
// goes silent while staying alive and idle.
type fakeSession struct {
	nc      *nats.Conn
	answers int32
	mu      sync.Mutex
	typed   []string
	acked   map[string]bool
	done    atomic.Int32
	// hold: while set, nothing is acknowledged (a delivery stays pending).
	hold atomic.Bool
	// busy: while set, the session is not idle, so a delivery waits in
	// deliver; waited counts the Idle calls that said so.
	busy   atomic.Bool
	waited atomic.Int32
}

var (
	ackRe = regexp.MustCompile(`ape conductor ack '([^']+)'`) // single-quoted, as a shell takes it (V8g)
	seqRe = regexp.MustCompile(`seq="(\d+)"`)
)

func (f *fakeSession) Idle() bool {
	if f.busy.Load() {
		f.waited.Add(1)
		return false
	}
	return true
}

func (f *fakeSession) Type(text string) error {
	f.mu.Lock()
	f.typed = append(f.typed, text)
	seq := seqRe.FindStringSubmatch(text)[1]
	answer := !f.hold.Load() && !f.acked[seq] && f.done.Load() < f.answers
	if answer {
		f.acked[seq] = true
		f.done.Add(1)
	}
	f.mu.Unlock()
	if answer {
		return Ack(f.nc, ackRe.FindStringSubmatch(text)[1])
	}
	return nil
}

// typedWith reports a delivery whose text holds s.
func (f *fakeSession) typedWith(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.typed {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// ackedWith reports a delivery whose text holds s, acknowledged.
func (f *fakeSession) ackedWith(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.typed {
		if strings.Contains(t, s) && f.acked[seqRe.FindStringSubmatch(t)[1]] {
			return true
		}
	}
	return false
}

func (f *fakeSession) seqs() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, t := range f.typed {
		out[seqRe.FindStringSubmatch(t)[1]]++
	}
	return out
}

func rig(t *testing.T) (string, *nats.Conn, jetstream.JetStream) {
	t.Helper()
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
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

func scaled(user, holder string, s Session) Config {
	return Config{
		User: user, Ensemble: "atomix", Projects: []string{"taskforge"}, Holder: holder, Nonce: "n-" + holder,
		LeaseTTL: 3 * time.Second, Renew: 500 * time.Millisecond, AckWait: time.Second,
		Escalate: 2 * time.Second, Pause: 4 * time.Second, Session: s,
	}
}

func publish(t *testing.T, js jetstream.JetStream, event string, seq int64) {
	t.Helper()
	_, err := events.Publish(context.Background(), js, events.Subject("bob", "taskforge", events.KindProject, "M2", event), events.MessageID("taskforge", "c1", seq), []byte(`{"q":"which port?"}`))
	require.NoError(t, err)
}

func watch(t *testing.T, nc *nats.Conn, subj string) func(suffix string) bool {
	t.Helper()
	var mu sync.Mutex
	var got []string
	sub, err := nc.Subscribe(subj, func(m *nats.Msg) { mu.Lock(); got = append(got, m.Subject); mu.Unlock() })
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return func(suffix string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range got {
			if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
				return true
			}
		}
		return false
	}
}

// Drill 4: a conductor answers once, then goes silent while alive. It keeps
// receiving (redelivery); the question escalates, then its milestone
// pauses; the lease expires (lease-expired); a second conductor of the same
// person takes the ensemble over, receives what was left unacknowledged,
// answers it, and the milestone resumes.
func TestDrill_ConductorAnswersOnceThenGoesSilent(t *testing.T) {
	url, nc, js := rig(t)
	ctx := t.Context()
	seen := watch(t, nc, "ape.evt.>")

	cnc, err := nats.Connect(url)
	require.NoError(t, err)
	defer cnc.Close()
	go func() {
		_ = coordinator.Run(ctx, cnc, coordinator.Config{Ensemble: "atomix", ID: "coord-1", TTL: 3 * time.Second, Renew: time.Second, Projects: []string{"taskforge"}})
	}()

	a := &fakeSession{nc: nc, answers: 1, acked: map[string]bool{}}
	errA := make(chan error, 1)
	go func() { errA <- Run(ctx, nc, scaled("ann", "cond-a", a)) }()
	time.Sleep(300 * time.Millisecond)

	publish(t, js, "block", 1)
	require.Eventually(t, func() bool { return a.done.Load() == 1 }, 5*time.Second, 50*time.Millisecond, "the first is answered")
	publish(t, js, "person", 2)
	require.Eventually(t, func() bool { return a.seqs()["2"] >= 2 }, 6*time.Second, 50*time.Millisecond, "unacknowledged: delivered again")
	require.Eventually(t, func() bool { return seen(".project.M2." + apexv1.EventQuestionEscalated) }, 6*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool { return seen(".project.M2." + apexv1.EventMilestonePaused) }, 6*time.Second, 50*time.Millisecond)
	paused, err := Paused(ctx, js, "taskforge", "M2", "ann") // ann holds M2: her conductor paused it
	require.NoError(t, err)
	require.True(t, paused)

	select {
	case err := <-errA:
		require.ErrorIs(t, err, ErrLost, "a silent conductor stops renewing and loses its lease")
	case <-time.After(10 * time.Second):
		t.Fatal("the silent conductor kept its lease")
	}
	require.Eventually(t, func() bool { return seen(".coord.conductor-ann-atomix.lease-expired") }, 5*time.Second, 50*time.Millisecond)

	b := &fakeSession{nc: nc, answers: 10, acked: map[string]bool{}}
	go func() { _ = Run(ctx, nc, scaled("ann", "cond-b", b)) }()
	require.Eventually(t, func() bool { return b.seqs()["2"] >= 1 }, 6*time.Second, 50*time.Millisecond, "the takeover receives what was left unacknowledged")
	require.Eventually(t, func() bool { return seen(".project.M2." + apexv1.EventMilestoneResumed) }, 6*time.Second, 50*time.Millisecond)
	paused, err = Paused(ctx, js, "taskforge", "M2", "ann")
	require.NoError(t, err)
	require.False(t, paused)
	_, ok := b.seqs()["1"]
	require.False(t, ok, "what the first conductor acknowledged is not delivered again")
}

// D545: a second conductor of the same person is refused while the first
// holds the ensemble; another person's conductor is not.
func TestOneConductorPerPersonAndEnsemble(t *testing.T) {
	_, nc, _ := rig(t)
	ctx := t.Context()
	go func() {
		_ = Run(ctx, nc, scaled("ann", "cond-a", &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}))
	}()
	require.Eventually(t, func() bool {
		js, _ := jetstream.New(nc)
		ls, _ := lease.Open(ctx, js)
		h, _, _ := ls.Holder(ctx, layout.ConductorKey("ann", "atomix"))
		return h == "cond-a"
	}, 5*time.Second, 50*time.Millisecond)
	require.ErrorIs(t, Run(ctx, nc, scaled("ann", "cond-b", &fakeSession{nc: nc, acked: map[string]bool{}})), ErrTaken)
	errBob := make(chan error, 1)
	bctx, cancel := context.WithCancel(ctx)
	go func() {
		errBob <- Run(bctx, nc, scaled("bob", "cond-bob", &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}))
	}()
	time.Sleep(time.Second)
	cancel()
	require.NoError(t, <-errBob, "another person's conductor claims its own key")
}

// Requirement 2: with no events at all, the supervisor's heartbeat is what
// a silent conductor fails to acknowledge; an answering one keeps its
// lease. (Heartbeats are published here as the supervisor does.)
func TestHeartbeatKeepsOnlyAnAnsweringConductor(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	silent := &fakeSession{nc: nc, answers: 0, acked: map[string]bool{}}
	answering := &fakeSession{nc: nc, answers: 1000, acked: map[string]bool{}}
	errS := make(chan error, 1)
	go func() { errS <- Run(ctx, nc, scaled("sam", "cond-s", silent)) }()
	go func() { _ = Run(ctx, nc, scaled("ann", "cond-a", answering)) }()
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				for _, k := range []string{layout.ConductorKey("sam", "atomix"), layout.ConductorKey("ann", "atomix")} {
					_, _ = events.Publish(context.Background(), js, events.Subject(coordinator.Owner, "atomix", events.KindCoord, k, "heartbeat"), k+time.Now().String(), nil)
				}
			}
		}
	}()
	defer close(stop)
	select {
	case err := <-errS:
		require.ErrorIs(t, err, ErrLost)
	case <-time.After(12 * time.Second):
		t.Fatal("a conductor that never acknowledges its heartbeats kept its lease")
	}
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	h, _, err := ls.Holder(ctx, layout.ConductorKey("ann", "atomix"))
	require.NoError(t, err)
	require.Equal(t, "cond-a", h, "the answering conductor still holds its lease")
}

// Drill 4's control, the v1.4 shape: no ape conductor owns the session (a
// watcher the model armed has stopped). Nothing escalates, nothing pauses.
func TestDrill_ConductorControl_NothingEscalatesWithoutIt(t *testing.T) {
	_, nc, js := rig(t)
	seen := watch(t, nc, "ape.evt.>")
	publish(t, js, "person", 1)
	time.Sleep(5 * time.Second) // past the scaled escalation and pause ages
	require.False(t, seen("."+apexv1.EventQuestionEscalated))
	paused, err := Paused(t.Context(), js, "taskforge", "M2", "ann")
	require.NoError(t, err)
	require.False(t, paused)
}

// v1.5 PRE-3 (a): two checkouts of one project name the project twice.
// One filter each made overlapping consumer filters, which NATS refuses
// (err 10138): attach never bound. Now the project is one filter, and the
// conductor binds and gets its events; the control, two projects, keeps a
// filter for each.
func TestRun_TwoCheckoutsOfOneProjectBind(t *testing.T) {
	require.Equal(t, []string{"ape.evt.coord.atomix.>", "ape.evt.*.atomix.contract.>", "ape.evt.*.taskforge.project.>"},
		Filters("atomix", []string{"taskforge", "taskforge"}))
	require.Equal(t, []string{"ape.evt.coord.atomix.>", "ape.evt.*.atomix.contract.>", "ape.evt.*.taskforge.project.>", "ape.evt.*.billing.project.>"},
		Filters("atomix", []string{"taskforge", "billing"}), "the control: two projects, two filters")

	_, nc, js := rig(t)
	ctx := t.Context()
	f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	cfg := scaled("ann", "cond-a", f)
	cfg.Projects = []string{"taskforge", "taskforge"}
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, nc, cfg) }()
	bound(t, js)
	publish(t, js, "question", 1)
	require.Eventually(t, func() bool {
		select {
		case err := <-errc:
			t.Fatalf("Run returned: %v", err)
		default:
		}
		return len(f.seqs()) == 1
	}, 5*time.Second, 50*time.Millisecond, "bound, and the project's event delivered")
}

// v1.5 PRE-3 (b): a conductor that fails before its first delivery (here
// the consumer cannot bind: no events stream) lets its lease go, so a
// corrected attach claims it at once instead of waiting out the TTL. The
// control: a conductor that bound holds it, and a second is refused.
func TestRun_ReleasesTheLeaseOnABindError(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, lease.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	cfg := scaled("ann", "cond-a", &fakeSession{nc: nc, acked: map[string]bool{}})
	cfg.LeaseTTL = time.Minute
	err = Run(ctx, nc, cfg)
	require.ErrorContains(t, err, "bind consumer")
	h, _, err := ls.Holder(ctx, layout.ConductorKey("ann", "atomix"))
	require.NoError(t, err)
	require.Empty(t, h, "released on the bind error")
	l, err := ls.Claim(ctx, layout.ConductorKey("ann", "atomix"), "cond-b", time.Minute)
	require.NoError(t, err, "a corrected attach claims it at once")
	require.NoError(t, ls.Release(ctx, l))

	require.NoError(t, events.Declare(ctx, js, 1))
	go func() { _ = Run(ctx, nc, cfg) }()
	require.Eventually(t, func() bool {
		h, _, _ := ls.Holder(ctx, layout.ConductorKey("ann", "atomix"))
		return h == "cond-a"
	}, 5*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	require.ErrorIs(t, Run(ctx, nc, scaled("ann", "cond-b", &fakeSession{nc: nc, acked: map[string]bool{}})), ErrTaken, "the control: a bound conductor keeps its lease")
}

// V8g (FINDING-V15-1C-5): the delivery's ack command, run as given through
// a real shell, reaches ape with the whole token. Unquoted, the shell
// expanded $JS away and ape got .ACK.…; a conductor session ran it so.
// The token is now single-quoted. The controls: the old unquoted form, and
// the token in double quotes, both reach ape stripped — what the refusal
// in `ape conductor ack` is for.
func TestFormat_TheAckSurvivesARealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a POSIX shell's quoting; the conductor's pane runs one (tmux), never Windows'")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	bin := t.TempDir()
	argv := filepath.Join(t.TempDir(), "argv")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ape"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\n"), 0o755))
	token := "$JS.ACK.APE_EVENTS.ape-conductor-ann-shop.1.466.27.1762198506000000000.3"
	got := func(cmdline string) []string {
		t.Helper()
		c := exec.Command(sh, "-c", cmdline)
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "JS=")
		out, err := c.CombinedOutput()
		require.NoError(t, err, string(out))
		data, err := os.ReadFile(argv)
		require.NoError(t, err)
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	line := Format(466, "n1", "ape.evt.coord.shop.heartbeat", []byte(`{}`), token)
	_, cmdline, ok := strings.Cut(line, "When you have handled this event, run: ")
	require.True(t, ok, line)
	require.Equal(t, []string{"conductor", "ack", token}, got(cmdline), "as given: the whole token")

	stripped := strings.TrimPrefix(token, "$JS")
	require.Equal(t, []string{"conductor", "ack", stripped}, got("ape conductor ack "+token), "the control: unquoted, $JS is expanded away")
	require.Equal(t, []string{"conductor", "ack", stripped}, got(`ape conductor ack "`+token+`"`), "the control: double quotes expand it too")
}

// outageRig is rig on a three-node cluster whose nodes a test can stop and
// restart on their data, with the buckets and stream at R3.
func outageRig(t *testing.T) (*natstest.Cluster, jetstream.JetStream) {
	t.Helper()
	c := natstest.RunCluster(t, 3)
	nc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 3))
	require.NoError(t, slots.Declare(ctx, js, 3))
	require.NoError(t, events.Declare(ctx, js, 3))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	return c, js
}

// bound waits for ann's conductor to be pulling on its durable. The
// durable delivers new events only, so one published before it starts is
// never delivered (V8-FLAKE-2: a fixed 500 ms was short of a slow
// runner's bind). Its existing is not enough on a cluster: the consumer
// is answered for once assigned, and fixes where "new" starts only when
// its leader runs; a pull waiting on it says the leader runs.
func bound(t *testing.T, js jetstream.JetStream) {
	t.Helper()
	name := layout.ConductorConsumer("ann", "atomix")
	require.Eventually(t, func() bool {
		c, err := js.Consumer(context.Background(), events.EventsStream, name)
		if err != nil {
			return false
		}
		info, err := c.Info(context.Background())
		return err == nil && info.NumWaiting > 0
	}, 30*time.Second, 50*time.Millisecond, "the conductor pulls on its durable")
}

func outage(t *testing.T, c *natstest.Cluster, d time.Duration) {
	t.Helper()
	for i := range c.Servers {
		c.Stop(i)
	}
	time.Sleep(d)
	for i := range c.Servers {
		c.Restart(i)
	}
	c.WaitMetaLeader(30 * time.Second)
}

// V8d (FINDING-V15-1C-DRILL-1): in 1c's 90 s outage attach logged "lease
// lost: lease: read epoch: context deadline exceeded" and exited; nothing
// restarted it, and the conductor went deaf. Now it rides the outage out,
// as the supervisor does: every node down past the lease's TTL, then back
// on its data, and the same conductor still runs, holds its lease and gets
// the next event. The control: another conductor of the person took the
// lapsed lease before this one was back, and it stops (ErrLost).
func TestDrill_ConductorRidesOutANATSOutage(t *testing.T) {
	t.Run("rides-it-out", func(t *testing.T) {
		c, js := outageRig(t)
		ctx := t.Context()
		anc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
		require.NoError(t, err)
		t.Cleanup(anc.Close)
		a := &fakeSession{nc: anc, answers: 99, acked: map[string]bool{}}
		var logMu sync.Mutex
		var logged []string
		cfg := scaled("ann", "cond-a", a)
		cfg.Log = func(f string, args ...any) {
			logMu.Lock()
			logged = append(logged, time.Now().Format("15:04:05.000 ")+fmt.Sprintf(f, args...))
			logMu.Unlock()
		}
		t.Cleanup(func() {
			if t.Failed() {
				logMu.Lock()
				t.Log(strings.Join(logged, ""))
				logMu.Unlock()
			}
		})
		errA := make(chan error, 1)
		go func() { errA <- Run(ctx, anc, cfg) }()
		bound(t, js)
		publish(t, js, "block", 1)
		require.Eventually(t, func() bool { return a.done.Load() == 1 }, 5*time.Second, 50*time.Millisecond, "delivered and acknowledged before the outage")

		// A delivery pending across the outage, as a heartbeat is in
		// production: no acknowledgement could be made while NATS was
		// down, so the conductor is not judged silent for it.
		a.hold.Store(true)
		_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 2), []byte(`{"q":"held across"}`))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return a.typedWith("held across") }, 5*time.Second, 50*time.Millisecond, "delivered, not yet acknowledged")

		outage(t, c, 3*cfg.LeaseTTL)
		select {
		case err := <-errA:
			t.Fatalf("attach ended during the outage: %v", err)
		default:
		}
		a.hold.Store(false)
		require.Eventually(t, func() bool { return a.ackedWith("held across") }, 20*time.Second, 100*time.Millisecond, "redelivered to the same conductor after the outage, acknowledged")
		require.Eventually(t, func() bool {
			_, err := events.Publish(context.Background(), js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 3), []byte(`{"q":"after"}`))
			return err == nil
		}, 30*time.Second, 200*time.Millisecond)
		require.Eventually(t, func() bool { return a.ackedWith(`"after"`) }, 20*time.Second, 100*time.Millisecond, "the next event reaches it too, acknowledged")
		select {
		case err := <-errA:
			t.Fatalf("attach ended after the outage: %v", err)
		default:
		}
		// Each read bounded, the wait as long as the drill's other waits
		// after the outage: an unbounded read on a cluster just back can
		// spend most of a short window (Windows CI, run 41949504519).
		require.Eventually(t, func() bool {
			rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			ls, err := lease.Open(rctx, js)
			if err != nil {
				return false
			}
			h, _, err := ls.Holder(rctx, layout.ConductorKey("ann", "atomix"))
			return err == nil && h == "cond-a"
		}, 30*time.Second, 100*time.Millisecond, "its lease is held again")
		logMu.Lock()
		defer logMu.Unlock()
		all := strings.Join(logged, "")
		require.True(t, strings.Contains(all, "riding it out") || strings.Contains(all, "claimed again"), "the outage is said: %q", all)
	})
	t.Run("another-conductor-took-it", func(t *testing.T) {
		c, js := outageRig(t)
		ctx := t.Context()
		// This conductor comes back slowly, after the other claimed.
		anc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(8*time.Second))
		require.NoError(t, err)
		t.Cleanup(anc.Close)
		a := &fakeSession{nc: anc, answers: 99, acked: map[string]bool{}}
		cfg := scaled("ann", "cond-a", a)
		errA := make(chan error, 1)
		go func() { errA <- Run(ctx, anc, cfg) }()
		bound(t, js)
		publish(t, js, "block", 1)
		require.Eventually(t, func() bool { return a.done.Load() == 1 }, 10*time.Second, 50*time.Millisecond, "bound and delivering before the outage")

		outage(t, c, 3*cfg.LeaseTTL)
		bnc, err := nats.Connect(c.URLs())
		require.NoError(t, err)
		t.Cleanup(bnc.Close)
		bjs, err := jetstream.New(bnc)
		require.NoError(t, err)
		// The lease lapsed during the outage (a server may keep an expired
		// key a while after a restart, so the lapse is made deterministic)
		// and another conductor of the person takes the ensemble over.
		require.Eventually(t, func() bool {
			ls, err := lease.Open(ctx, bjs)
			if err != nil {
				return false
			}
			key := layout.ConductorKey("ann", "atomix")
			h, tok, err := ls.Holder(ctx, key)
			if err != nil {
				return false
			}
			if h != "" {
				_ = ls.Release(ctx, lease.Lease{Key: key, Holder: h, Token: tok})
			}
			_, err = ls.Claim(ctx, key, "cond-b", time.Minute)
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "another conductor takes the lapsed lease")
		select {
		case err := <-errA:
			require.ErrorIs(t, err, ErrLost)
			require.ErrorContains(t, err, "cond-b")
		case <-time.After(30 * time.Second):
			t.Fatal("attach kept running though another conductor holds the ensemble")
		}
	})
}

// FINDING-V15-1C2-1: a person's pause is the person's to lift. The
// conductor acknowledges the pause's own delivery and a later delivery of
// the same milestone, and the milestone stays paused, with no
// milestone-resumed published. The control is Drill 4: a conductor's own
// pause is resumed by the answer, its predecessor's included.
func TestRun_APersonsPauseSurvivesTheConductorsAcks(t *testing.T) {
	url, nc, js := rig(t)
	ctx := t.Context()
	seen := watch(t, nc, "ape.evt.>")
	cnc, err := nats.Connect(url)
	require.NoError(t, err)
	defer cnc.Close()
	go func() {
		_ = coordinator.Run(ctx, cnc, coordinator.Config{Ensemble: "atomix", ID: "coord-1", TTL: 3 * time.Second, Renew: time.Second, Projects: []string{"taskforge"}})
	}()
	a := &fakeSession{nc: nc, answers: 10, acked: map[string]bool{}}
	go func() { _ = Run(ctx, nc, scaled("ann", "cond-a", a)) }()
	time.Sleep(300 * time.Millisecond)

	data := fmt.Sprintf(`{"kind":%q,"by":"bob","user":"bob","reason":"close contention"}`, PausePerson)
	_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", apexv1.EventMilestonePaused), "bob/pause/M2", []byte(data))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.ackedWith(apexv1.EventMilestonePaused) }, 5*time.Second, 50*time.Millisecond, "the pause is delivered and acknowledged")
	publish(t, js, "block", 1)
	require.Eventually(t, func() bool { return a.ackedWith(".M2.block") }, 5*time.Second, 50*time.Millisecond, "a later M2 delivery is acknowledged")

	require.Never(t, func() bool { return seen(".project.M2." + apexv1.EventMilestoneResumed) }, 3*time.Second, 100*time.Millisecond,
		"the conductor's acks do not lift a person's pause")
	paused, err := Paused(ctx, js, "taskforge", "M2", "bob")
	require.NoError(t, err)
	require.True(t, paused)
}

// FINDING-V15-1C2-1, widened (D797+): a person's pause and a conductor's
// are read apart. A person pauses M2, a conductor pauses it too (OQ24) and
// then resumes its own pause: M2 is still the person's, paused. The
// control: the person's own resume lifts it.
func TestPaused_APersonsAndAConductorsPauseAreReadApart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []struct{ event, kind string }
		paused bool
	}{
		{"conductor-resume-leaves-the-persons-pause", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, PausePerson}, {apexv1.EventMilestonePaused, ""}, {apexv1.EventMilestoneResumed, ""},
		}, true},
		{"control-the-persons-resume-lifts-it", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, PausePerson}, {apexv1.EventMilestoneResumed, PausePerson},
		}, false},
		{"a-persons-resume-lifts-a-conductors-pause-too", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, PausePerson}, {apexv1.EventMilestonePaused, ""}, {apexv1.EventMilestoneResumed, PausePerson},
		}, false},
		{"a-conductor-pause-after-the-persons-resume-holds", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, PausePerson}, {apexv1.EventMilestoneResumed, PausePerson}, {apexv1.EventMilestonePaused, ""},
		}, true},
		{"conductor-pause-then-person-pause-then-conductor-resume", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, ""}, {apexv1.EventMilestonePaused, PausePerson}, {apexv1.EventMilestoneResumed, ""},
		}, true},
		{"control-a-conductors-own-pause-and-resume", []struct{ event, kind string }{
			{apexv1.EventMilestonePaused, ""}, {apexv1.EventMilestoneResumed, ""},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, js := rig(t)
			ctx := t.Context()
			for i, e := range tc.events {
				data := fmt.Sprintf(`{"kind":%q,"by":"x","user":"bob"}`, e.kind)
				_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", e.event), fmt.Sprintf("e%d", i), []byte(data))
				require.NoError(t, err)
			}
			paused, err := Paused(ctx, js, "taskforge", "M2", "bob")
			require.NoError(t, err)
			require.Equal(t, tc.paused, paused)
		})
	}
}

// D800+: the pause read is bounded. A person's pause behind more events
// than the first window is still found (the window widens); a history past
// pauseReadMax fails the read, which a gate does not take as "not paused".
func TestPaused_TheReadIsBounded(t *testing.T) {
	_, _, js := rig(t)
	ctx := t.Context()
	pub := func(event, kind string, id string) {
		t.Helper()
		data := fmt.Sprintf(`{"kind":%q,"by":"x","user":"bob"}`, kind)
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", event), id, []byte(data))
		require.NoError(t, err)
	}
	pub(apexv1.EventMilestonePaused, PausePerson, "p")
	for i := range 300 {
		publish(t, js, "block", int64(i))
	}
	pub(apexv1.EventMilestonePaused, "", "c")
	paused, why, err := PausedWith(ctx, js, "taskforge", "M2", "bob")
	require.NoError(t, err)
	require.True(t, paused)
	require.Equal(t, PausePerson, why.Kind, "the person's pause, 300 events back, is found")

	for i := range pauseReadMax + 8 {
		ev := apexv1.EventMilestonePaused
		if i%2 == 1 {
			ev = apexv1.EventMilestoneResumed
		}
		data := `{"by":"x","user":"bob"}`
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M3", ev), fmt.Sprintf("m3-%d", i), []byte(data))
		require.NoError(t, err)
	}
	_, err = Paused(ctx, js, "taskforge", "M3", "bob")
	require.ErrorIs(t, err, errPauseHistory, "a conductor-only history past the bound is not read as 'no person pause'")
}

// V12c (D806+, research's D6 after V8j): a person's pause or resume counts
// only from the claim holder's user token. The T1: bob, a member of the
// project who does not hold M2, publishes a person pause — ann, the
// holder, is not paused by it; ann's own pause holds, and bob's person
// resume does not lift it; ann's resume does. V12-2: the same for the
// conductor's kind, which is only the payload's word — bob's kind-less
// pause does not hold ann, and his kind-less resume does not lift ann's
// conductor's pause (the control: ann's conductor's own pause holds).
func TestPaused_APersonsPauseCountsOnlyFromTheClaimHolder(t *testing.T) {
	_, _, js := rig(t)
	ctx := t.Context()
	n := 0
	pub := func(user, event, kind string) {
		t.Helper()
		n++
		data := fmt.Sprintf(`{"kind":%q,"by":%q,"user":%q}`, kind, user, user)
		_, err := events.Publish(ctx, js, events.Subject(user, "taskforge", events.KindProject, "M2", event), fmt.Sprintf("e%d", n), []byte(data))
		require.NoError(t, err)
	}
	paused := func() bool {
		t.Helper()
		p, err := Paused(ctx, js, "taskforge", "M2", "ann")
		require.NoError(t, err)
		return p
	}
	pub("bob", apexv1.EventMilestonePaused, PausePerson)
	require.False(t, paused(), "bob does not hold M2: his person pause does not count")
	p, err := Paused(ctx, js, "taskforge", "M2", "bob")
	require.NoError(t, err)
	require.True(t, p, "read for bob as the holder, it would")

	pub("ann", apexv1.EventMilestonePaused, PausePerson)
	require.True(t, paused(), "the holder's own pause holds")
	pub("bob", apexv1.EventMilestoneResumed, PausePerson)
	require.True(t, paused(), "a non-holder's resume does not lift it")
	pub("ann", apexv1.EventMilestoneResumed, PausePerson)
	require.False(t, paused(), "the holder's resume does")

	pub("bob", apexv1.EventMilestonePaused, "")
	require.False(t, paused(), "V12-2: bob's pause labelled as a conductor's does not hold the holder either")
	pub("ann", apexv1.EventMilestonePaused, "")
	require.True(t, paused(), "control: the holder's conductor's pause holds")
	pub("bob", apexv1.EventMilestoneResumed, "")
	require.True(t, paused(), "V12-2: bob's kind-less resume does not lift it")
	pub("ann", apexv1.EventMilestoneResumed, "")
	require.False(t, paused(), "the holder's conductor's resume does")
}

// V12-6 (FINDING-2A-3): a durable deleted while attach is idle. Fetch only
// publishes the pull, and the server's 409/503 reaches batch.Error() alone,
// which deliver never read: attach stayed deaf and pulled ≈ 6,650 times a
// second. Now it binds again within seconds, says so, gets the next event,
// and pulls about once a second throughout. The control: the same idle
// attach with its durable left alone pulls at that rate too and binds
// nothing.
func TestDrill_ConductorBindsADeletedDurableAgainWhileIdle(t *testing.T) {
	for _, del := range []bool{true, false} {
		t.Run(map[bool]string{true: "deleted", false: "control-left-alone"}[del], func(t *testing.T) {
			url, _, js := rig(t)
			ctx := t.Context()
			// attach's own connection: its outbound count is its pulls,
			// renewals and sweeps. A subscriber on the pull subject would
			// count them too, but it answers the pull (no 503), which
			// hides the spin it is there to measure.
			nc, err := nats.Connect(url)
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
			var logMu sync.Mutex
			var logged []string
			cfg := scaled("ann", "cond-a", f)
			cfg.Log = func(format string, a ...any) {
				logMu.Lock()
				logged = append(logged, fmt.Sprintf(format, a...))
				logMu.Unlock()
			}
			rebound := func() bool {
				logMu.Lock()
				defer logMu.Unlock()
				return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer again") })
			}
			go func() { _ = Run(ctx, nc, cfg) }()
			name := layout.ConductorConsumer("ann", "atomix")
			require.Eventually(t, func() bool { _, err := js.Consumer(ctx, events.EventsStream, name); return err == nil }, 5*time.Second, 50*time.Millisecond, "bound")
			publish(t, js, "question", 1)
			require.Eventually(t, func() bool { return f.done.Load() == 1 }, 5*time.Second, 50*time.Millisecond, "delivered before")

			if del {
				require.NoError(t, js.DeleteConsumer(ctx, events.EventsStream, name))
			}
			before := nc.Stats().OutMsgs
			time.Sleep(3 * time.Second)
			n := nc.Stats().OutMsgs - before
			t.Logf("attach's messages out in the 3 s after: %d", n)
			// The control's rate is ≈ 30 (a pull a second, a renewal and a
			// sweep each 500 ms); the spin was thousands.
			require.LessOrEqual(t, n, uint64(60), "a bounded pull rate")
			if !del {
				require.False(t, rebound(), "nothing deleted, nothing bound again")
				return
			}
			require.Eventually(t, rebound, 5*time.Second, 50*time.Millisecond, "bound again, and said so")
			_, err = js.Consumer(ctx, events.EventsStream, name)
			require.NoError(t, err, "the durable exists again")
			publish(t, js, "question", 2)
			require.Eventually(t, func() bool { return f.done.Load() == 2 }, 5*time.Second, 50*time.Millisecond, "the next event delivered")
		})
	}
}

// V12-8 with R2B-1 and DRY-2B-1: a durable deleted while the conductor
// holds an unacknowledged event. V12-6's rebind re-created it DeliverNew,
// whose ack floor is the stream's last sequence: the held event was never
// delivered again, sweep took it as acknowledged (and could resume a
// milestone the conductor had paused for it), and an event published while
// the durable was gone was never delivered at all. Now the rebind starts at
// the lowest sequence still waiting, so: the held event is delivered again
// with a new token, which acknowledges it; a conductor's pause for it
// stands until then; the gap's event is delivered. And the loss is noticed
// by sweep while the delivery is held for a busy session, not only at the
// next fetch.
func TestDrill_ConductorKeepsWhatALostDurableHeld(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	seen := watch(t, nc, "ape.evt.>")
	f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	f.hold.Store(true) // the conductor does not answer yet
	var logMu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", f)
	cfg.Escalate, cfg.Pause = time.Second, 2*time.Second
	// The answers are held for longer than scaled's lease TTL: under OQ22
	// that conductor is silent and lets its lease lapse (lease lost, Run
	// returns; drill 4's subject), whenever the held event's redelivery is
	// not answered first. Not this drill's question.
	cfg.LeaseTTL = 30 * time.Second
	cfg.Log = func(format string, a ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, a...))
		logMu.Unlock()
	}
	rebound := func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer again") })
	}
	errc := make(chan error, 1)
	go func() {
		err := Run(ctx, nc, cfg)
		cfg.Log("Run returned: %v\n", err) // in the failure's log: a wait that never ends says why
		errc <- err
	}()
	name := layout.ConductorConsumer("ann", "atomix")
	require.Eventually(t, func() bool { _, err := js.Consumer(ctx, events.EventsStream, name); return err == nil }, 5*time.Second, 50*time.Millisecond)
	t.Cleanup(func() {
		if t.Failed() {
			f.mu.Lock()
			for _, x := range f.typed {
				t.Log(x[:min(len(x), 160)])
			}
			t.Logf("acked %v done %d", f.acked, f.done.Load())
			f.mu.Unlock()
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})

	publish(t, js, "question", 1)
	require.Eventually(t, func() bool { return f.typedWith("which port?") }, 5*time.Second, 50*time.Millisecond, "delivered, held unanswered")
	require.Eventually(t, func() bool { return seen(".M2.milestone-paused") }, 10*time.Second, 50*time.Millisecond, "the question waited past the pause age: its milestone is paused")
	first := f.seqs()

	// The session turns busy: the held event's next redelivery waits in
	// deliver, which fetches nothing until it is typed.
	f.busy.Store(true)
	require.Eventually(t, func() bool { return f.waited.Load() > 0 }, 5*time.Second, 20*time.Millisecond, "a redelivery waits for the busy session")
	require.NoError(t, js.DeleteConsumer(ctx, events.EventsStream, name))
	require.Eventually(t, rebound, 5*time.Second, 50*time.Millisecond, "bound again by sweep, with the delivery still held (DRY-2B-1)")
	f.busy.Store(false)
	_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M3", "question"), events.MessageID("taskforge", "c1", 2), []byte(`{"q":"in the gap"}`))
	require.NoError(t, err)
	time.Sleep(2 * time.Second) // sweeps after the rebind
	require.False(t, seen(".M2.milestone-resumed"), "no answer came: the conductor's pause stands (R2B-1: the re-created durable's floor is not an answer)")

	f.hold.Store(false)
	require.Eventually(t, func() bool { return f.ackedWith("which port?") }, 15*time.Second, 50*time.Millisecond, "the held event delivered again, and its new token acknowledges it")
	require.Eventually(t, func() bool { return f.ackedWith("in the gap") }, 15*time.Second, 50*time.Millisecond, "the event published while the durable was gone is delivered")
	require.Eventually(t, func() bool { return seen(".M2.milestone-resumed") }, 15*time.Second, 50*time.Millisecond, "answered now: the conductor's pause is lifted")
	require.GreaterOrEqual(t, f.seqs()[firstSeq(first)], 2, "the held event was typed again after the rebind")
	select {
	case err := <-errc:
		t.Fatalf("Run returned: %v", err)
	default:
	}
}

func firstSeq(m map[string]int) string {
	for k := range m {
		return k
	}
	return ""
}

// R2C-1: a rebuild re-declares APE_EVENTS, so its sequences start again at
// 1. A rebind from the old stream's sequences (min pending, or after the
// last seen) is past the new stream's end: the server keeps an explicit
// start sequence, so nothing was delivered until the new stream reached
// it, while the old pending entries escalated. Now the conductor binds the
// new stream from its first event, and drops what it held of the old one.
func TestDrill_ConductorHearsARebuiltEventsStream(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	seen := watch(t, nc, "ape.evt.>")
	f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	var logMu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", f)
	cfg.LeaseTTL = 30 * time.Second // a held answer is not silence here (drill 4's subject)
	cfg.Escalate, cfg.Pause = 4*time.Second, time.Minute
	cfg.Log = func(format string, a ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, a...))
		logMu.Unlock()
	}
	said := func(prefix string) bool {
		logMu.Lock()
		defer logMu.Unlock()
		return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, prefix) })
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	go func() { _ = Run(ctx, nc, cfg) }()
	bound(t, js)
	for i := range int64(5) {
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 10+i), []byte(fmt.Sprintf(`{"q":"old %d"}`, i)))
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return f.done.Load() == 5 }, 10*time.Second, 50*time.Millisecond, "five old events answered")
	// Their acknowledgements reach the ack floor and a few sweeps drop them,
	// so what is pending at the rebuild is the held event alone, at the old
	// stream's sequence 6: a start the new stream does not reach for a while.
	require.Eventually(t, func() bool {
		c, err := js.Consumer(ctx, events.EventsStream, layout.ConductorConsumer("ann", "atomix"))
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		return err == nil && info.AckFloor.Stream == 5
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(4 * cfg.Renew)
	f.hold.Store(true)
	publish(t, js, "question", 1) // held: pending on the old stream, at its sequence 6
	require.Eventually(t, func() bool { return f.typedWith("which port?") }, 5*time.Second, 50*time.Millisecond)

	// The rebuild: the stream deleted (its durables with it) and declared again.
	require.NoError(t, js.DeleteStream(ctx, events.EventsStream))
	require.NoError(t, events.Declare(ctx, js, 1))
	// The fake keys its acknowledgements by sequence, which the new stream
	// numbers again from 1: forget the old stream's.
	f.mu.Lock()
	f.acked = map[string]bool{}
	f.mu.Unlock()
	f.hold.Store(false)
	start := time.Now()
	require.Eventually(t, func() bool { return said("conductor: the events stream is new (rebuild)") }, 10*time.Second, 50*time.Millisecond, "the rebind sees a new stream")
	_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M3", "question"), events.MessageID("taskforge", "c2", 1), []byte(`{"q":"after the rebuild"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.ackedWith("after the rebuild") }, 10*time.Second, 50*time.Millisecond, "the new stream's next event is delivered within seconds")
	time.Sleep(max(0, cfg.Escalate+time.Second-time.Since(start)))
	require.False(t, seen(".M2.question-escalated"), "what was held of the old stream does not escalate after the rebuild")
}

// FINDING-DRY-2C-1: an event fetched while the session is busy waits in
// deliver, not yet typed, while sweep raises seen to it (the durable says
// it delivered it). A durable lost then was bound again from seen+1, past
// it; typed later with the lost durable's token, the event was taken as
// answered. Now it is held from the fetch: the rebind starts at or below
// it, the stale copy is not typed, and the new durable delivers it again
// with a live token, which acknowledges it on the server.
func TestDrill_ConductorKeepsAnEventFetchedForABusySession(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	var logMu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", f)
	cfg.LeaseTTL = 30 * time.Second // a busy session is not silence here (drill 4's subject)
	cfg.Escalate, cfg.Pause = time.Minute, 2*time.Minute
	cfg.Log = func(format string, a ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, a...))
		logMu.Unlock()
	}
	reboundFrom := func() (uint64, bool) {
		logMu.Lock()
		defer logMu.Unlock()
		for _, l := range logged {
			var n uint64
			if _, err := fmt.Sscanf(l, "conductor: bound its consumer again, from stream sequence %d", &n); err == nil {
				return n, true
			}
		}
		return 0, false
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	go func() { _ = Run(ctx, nc, cfg) }()
	bound(t, js)
	name := layout.ConductorConsumer("ann", "atomix")

	f.busy.Store(true)
	publish(t, js, "question", 1)
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	seq := st.CachedInfo().State.LastSeq
	require.Eventually(t, func() bool { return f.waited.Load() > 0 }, 5*time.Second, 20*time.Millisecond, "fetched, waiting for the busy session")
	time.Sleep(4 * cfg.Renew) // sweeps raise seen to the delivered event
	require.False(t, f.typedWith("which port?"), "not typed yet")

	require.NoError(t, js.DeleteConsumer(ctx, events.EventsStream, name))
	require.Eventually(t, func() bool { _, ok := reboundFrom(); return ok }, 10*time.Second, 50*time.Millisecond, "bound again by sweep")
	from, _ := reboundFrom()
	require.LessOrEqual(t, from, seq, "the rebind starts at or below the event the busy session held")

	f.busy.Store(false)
	require.Eventually(t, func() bool {
		c, err := js.Consumer(ctx, events.EventsStream, name)
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		return err == nil && info.AckFloor.Stream >= seq
	}, 15*time.Second, 50*time.Millisecond, "delivered again and acknowledged with a live token, on the server")
	time.Sleep(2 * cfg.AckWait)
	require.Equal(t, 1, f.seqs()[strconv.FormatUint(seq, 10)], "typed once: the copy with the lost durable's token is not typed (D863+)")
}

// D863+ (research): at attach the conductor took seen from what its
// durable had delivered. An earlier attach that crashed (or whose lease
// was taken over) can leave events delivered and unanswered; the new one
// holds none of them, and a durable lost before they came again was bound
// past them. Now seen starts at the durable's ack floor.
func TestDrill_ConductorAttachedAfterAnotherKeepsWhatItLeft(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	name := layout.ConductorConsumer("ann", "atomix")
	long := func(cfg Config) Config {
		cfg.LeaseTTL, cfg.AckWait = 30*time.Second, 30*time.Second // nothing comes again on its own here
		cfg.Escalate, cfg.Pause = time.Minute, 2*time.Minute
		return cfg
	}
	// The first attach: three events delivered, none answered, then gone.
	a := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	a.hold.Store(true)
	actx, stopA := context.WithCancel(ctx)
	doneA := make(chan struct{})
	go func() { defer close(doneA); _ = Run(actx, nc, long(scaled("ann", "cond-a", a))) }()
	bound(t, js)
	for i := range int64(3) {
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 20+i), []byte(fmt.Sprintf(`{"q":"left %d"}`, i)))
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return len(a.seqs()) == 3 }, 10*time.Second, 50*time.Millisecond, "three delivered to the first attach")
	stopA()
	<-doneA

	// The second attach binds the same durable, then loses it before
	// anything comes again.
	b := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	var logMu sync.Mutex
	var logged []string
	cfg := long(scaled("ann", "cond-b", b))
	cfg.Log = func(format string, args ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	go func() { _ = Run(ctx, nc, cfg) }()
	require.Eventually(t, func() bool {
		c, err := js.Consumer(ctx, events.EventsStream, name)
		if err != nil {
			return false
		}
		info, err := c.Info(ctx)
		return err == nil && info.NumWaiting > 0 && info.NumAckPending == 3
	}, 10*time.Second, 50*time.Millisecond, "the second attach pulls on the durable the first left three unanswered on")
	time.Sleep(4 * cfg.Renew) // sweeps read the durable: three delivered, none acknowledged
	require.NoError(t, js.DeleteConsumer(ctx, events.EventsStream, name))
	require.Eventually(t, func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer again") })
	}, 10*time.Second, 50*time.Millisecond, "bound again")
	logMu.Lock()
	again := slices.IndexFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer again") })
	line := logged[again]
	logMu.Unlock()
	require.Equal(t, "conductor: bound its consumer again, from stream sequence 1\n", line, "from the first unanswered event, not after what the first attach was delivered")
	require.Eventually(t, func() bool { return b.ackedWith("left 0") && b.ackedWith("left 1") && b.ackedWith("left 2") }, 15*time.Second, 50*time.Millisecond,
		"what the first attach left is delivered again and answered")
}
