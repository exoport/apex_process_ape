package conductor

import (
	"context"
	"errors"
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
		// V15-RED-1: the run's end says why — no longer a bare "lease lost".
		require.ErrorContains(t, err, "conductor.sam.atomix lapsed: ")
		require.ErrorContains(t, err, "unacknowledged past the lease's TTL")
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
	}, 30*time.Second, 250*time.Millisecond, "the conductor pulls on its durable") // not faster: a CONSUMER.INFO poll every 50 ms while an R3 durable is created disturbs it (FINDING-V12-13-1)
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
// afterOutage bounds each of the outage drills' waits once the nodes are
// back: three nodes replay their stores before they answer, which took
// more than 20 s on GitHub's Windows runners (V8-WIN-5, ~3 runs in 5) and
// once on a loaded Linux ci-local. Each wait logs how long it took.
const afterOutage = 60 * time.Second

// waited logs how long a wait after the outage took, from back.
func waited(t *testing.T, what string, back time.Time) {
	t.Helper()
	t.Logf("V8-WIN-5: %s %v after the nodes were back", what, time.Since(back).Round(100*time.Millisecond))
}

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
		back := time.Now()
		select {
		case err := <-errA:
			t.Fatalf("attach ended during the outage: %v", err)
		default:
		}
		a.hold.Store(false)
		require.Eventually(t, func() bool { return a.ackedWith("held across") }, afterOutage, 100*time.Millisecond, "redelivered to the same conductor after the outage, acknowledged")
		waited(t, "the held event acknowledged", back)
		require.Eventually(t, func() bool {
			_, err := events.Publish(context.Background(), js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 3), []byte(`{"q":"after"}`))
			return err == nil
		}, afterOutage, 200*time.Millisecond)
		waited(t, "the next event published", back)
		require.Eventually(t, func() bool { return a.ackedWith(`"after"`) }, afterOutage, 100*time.Millisecond, "the next event reaches it too, acknowledged")
		waited(t, "the next event acknowledged", back)
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
		}, afterOutage, 100*time.Millisecond, "its lease is held again")
		waited(t, "the lease held again", back)
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
			// The next event by its own payload, not by counting answers: a
			// durable deleted before sweep dropped the first event's entry is
			// bound again from it (at least once, R2C-2), and the fake, which
			// answers a sequence once, leaves its copy pending, so the
			// conductor's own escalation of it is answered too (one CPU: 8
			// in 10 runs counted 3, never 2).
			_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 2), []byte(`{"q":"the next"}`))
			require.NoError(t, err)
			require.Eventually(t, func() bool { return f.ackedWith("the next") }, 5*time.Second, 50*time.Millisecond, "the next event delivered")
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

// V12-13 (OBS-DRY-2C-2): on an R3 stream a DeliverNew durable reports ack
// floor 0 until its first ack, so a durable lost before the conductor's
// first ack was bound again from stream sequence 1 and the whole history
// was delivered again. Now its first bind records its start in its own
// config, and the rebind starts there: only what came after is delivered.
// On R3 only: on one server the floor is the start, and the bug does not
// show (research, D865+).
func TestDrill_ConductorLostBeforeItsFirstAckReplaysNoHistory(t *testing.T) {
	_, js := outageRig(t)
	ctx := t.Context()
	for seq := int64(1); seq <= 5; seq++ {
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", seq), []byte(`{"q":"history"}`))
		require.NoError(t, err)
	}
	nc, err := nats.Connect(js.Conn().ConnectedUrl(), nats.MaxReconnects(-1))
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
	go func() {
		err := Run(ctx, nc, cfg)
		cfg.Log("Run returned: %v\n", err)
	}()
	name := layout.ConductorConsumer("ann", "atomix")
	var c jetstream.Consumer
	// Bound by the conductor's own line: a poll of the durable while it is
	// created disturbed the create on R3 (FINDING-V12-13-1).
	require.Eventually(t, func() bool { return said("conductor: bound its consumer " + name) }, 30*time.Second, 100*time.Millisecond, "bound")
	c, err = js.Consumer(ctx, events.EventsStream, name)
	require.NoError(t, err)
	cc := c.CachedInfo().Config
	require.Equal(t, "6", cc.Metadata[StartSeqMeta], "the start, after the stream's last event, is in the durable's own config")
	require.Equal(t, jetstream.DeliverNewPolicy, cc.DeliverPolicy, "created DeliverNew, as before")
	// Lost before any ack: nothing was delivered, so nothing was acked. An
	// R3 delete can outlast one request's 5 s under load: tried again.
	require.Eventually(t, func() bool {
		err := js.DeleteConsumer(ctx, events.EventsStream, name)
		return err == nil || errors.Is(err, jetstream.ErrConsumerNotFound)
	}, 30*time.Second, 200*time.Millisecond, "the durable deleted")
	require.Eventually(t, func() bool { return said("conductor: bound its consumer again") }, 15*time.Second, 50*time.Millisecond, "bound again")
	logMu.Lock()
	again := ""
	for _, l := range logged {
		if strings.HasPrefix(l, "conductor: bound its consumer again") {
			again = l
		}
	}
	logMu.Unlock()
	require.Contains(t, again, "from stream sequence 6", "the rebind starts after the history, not at 1")
	_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 6), []byte(`{"q":"the new one"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.ackedWith("the new one") }, 15*time.Second, 50*time.Millisecond, "the new event delivered")
	time.Sleep(time.Second)
	require.False(t, f.typedWith("history"), "no event from before the bind was delivered")
}

// V12-13: a bound durable stands at its ack floor, or before the start its
// own config records when that is higher; one from before V12-13 has its
// floor only.
func TestBoundFloor(t *testing.T) {
	info := func(floor uint64, cfg jetstream.ConsumerConfig) *jetstream.ConsumerInfo {
		return &jetstream.ConsumerInfo{Config: cfg, AckFloor: jetstream.SequenceInfo{Stream: floor}}
	}
	recorded := jetstream.ConsumerConfig{DeliverPolicy: jetstream.DeliverNewPolicy, Metadata: map[string]string{StartSeqMeta: "6"}}
	require.Equal(t, uint64(5), boundFloor(info(0, recorded)), "R3, before the first ack: the recorded start")
	require.Equal(t, uint64(9), boundFloor(info(9, recorded)), "acked past it: the floor")
	require.Equal(t, uint64(0), boundFloor(info(0, jetstream.ConsumerConfig{DeliverPolicy: jetstream.DeliverNewPolicy})), "from before V12-13: the floor only")
	require.Equal(t, uint64(3), boundFloor(info(0, jetstream.ConsumerConfig{DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: 4})), "a rebind's start")
	require.Equal(t, uint64(0), boundFloor(info(0, jetstream.ConsumerConfig{Metadata: map[string]string{StartSeqMeta: "x"}})), "an unreadable record is ignored")
}

// V12-13: an attach that updates the durable (another ack wait) keeps the
// start its first bind recorded, so a later conductor still reads it.
func TestBind_UpdateKeepsTheRecordedStart(t *testing.T) {
	_, _, js := rig(t)
	ctx := t.Context()
	publish(t, js, "question", 1)
	publish(t, js, "question", 2)
	r := &runner{cfg: scaled("ann", "cond-a", nil), js: js}
	c, err := r.bind(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, "3", c.CachedInfo().Config.Metadata[StartSeqMeta])
	r2 := &runner{cfg: scaled("ann", "cond-b", nil), js: js}
	r2.cfg.AckWait = 3 * time.Second
	c, err = r2.bind(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, c.CachedInfo().Config.AckWait, "updated")
	require.Equal(t, "3", c.CachedInfo().Config.Metadata[StartSeqMeta], "and the recorded start kept")
	require.Equal(t, uint64(2), boundFloor(c.CachedInfo()))
}

// V12-10: the first attach under the durable's new name finds the old one
// (conductor-<user>-<ens>) and starts after its floor, so what it delivered
// and was not acknowledged comes again: event 1 was acked there, 2 and 3
// were not. The old durable is deleted after the move (V12-10-2). The
// control: with no old durable, the new one starts fresh, recording its
// start.
func TestConductor_MovesFromTheLegacyDurable(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "control-none"}[legacy], func(t *testing.T) {
			_, nc, js := rig(t)
			ctx := t.Context()
			old := layout.LegacyConductorConsumer("ann", "atomix")
			if legacy {
				lc, err := js.CreateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: old, FilterSubjects: Filters("atomix", []string{"taskforge"}), AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute})
				require.NoError(t, err)
				for i, q := range []string{"one", "two", "three"} {
					_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", int64(i+1)), []byte(`{"q":"`+q+`"}`))
					require.NoError(t, err)
				}
				b, err := lc.Fetch(3, jetstream.FetchMaxWait(5*time.Second))
				require.NoError(t, err)
				n := 0
				for m := range b.Messages() {
					if n == 0 {
						require.NoError(t, m.DoubleAck(ctx))
					}
					n++
				}
				require.Equal(t, 3, n)
				require.Eventually(t, func() bool { i, err := lc.Info(ctx); return err == nil && i.AckFloor.Stream == 1 }, 5*time.Second, 50*time.Millisecond)
			}
			f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
			var logMu sync.Mutex
			var logged []string
			cfg := scaled("ann", "cond-a", f)
			cfg.Log = func(format string, a ...any) {
				logMu.Lock()
				logged = append(logged, fmt.Sprintf(format, a...))
				logMu.Unlock()
			}
			go func() { _ = Run(ctx, nc, cfg) }()
			name := layout.ConductorConsumer("ann", "atomix")
			require.Eventually(t, func() bool {
				logMu.Lock()
				defer logMu.Unlock()
				return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, "conductor: bound its consumer "+name) })
			}, 30*time.Second, 100*time.Millisecond, "bound under the new name")
			moved := func() string {
				logMu.Lock()
				defer logMu.Unlock()
				for _, l := range logged {
					if strings.HasPrefix(l, "conductor: moved from the durable") {
						return l
					}
				}
				return ""
			}
			if !legacy {
				c, err := js.Consumer(ctx, events.EventsStream, name)
				require.NoError(t, err)
				require.NotEmpty(t, c.CachedInfo().Config.Metadata[StartSeqMeta], "a fresh start, recorded")
				require.Empty(t, moved())
				return
			}
			require.Contains(t, moved(), "conductor: moved from the durable "+old+" to "+name+" (V12-10), from stream sequence 2; "+old+" deleted")
			require.Eventually(t, func() bool { return f.ackedWith(`"two"`) && f.ackedWith(`"three"`) }, 10*time.Second, 50*time.Millisecond, "what the old durable left unacknowledged comes again")
			require.False(t, f.typedWith(`"one"`), "what it acknowledged does not")
			_, err := js.Consumer(ctx, events.EventsStream, old)
			require.ErrorIs(t, err, jetstream.ErrConsumerNotFound, "the old durable is deleted (V12-10-2)")
		})
	}
}

// V12-10-1 (research's probe): a production-shaped legacy durable
// (DeliverNew, never delivered anything) and two events published while
// the conductor was down for the upgrade. On R3 its ack floor reads 0 with
// nothing pending (V12-13's condition), so the move started fresh and the
// two were skipped for good; now a legacy with nothing ack-pending starts
// after what it delivered, and they are typed. R1 is the control: there the
// floor is the start, and they were typed before the fix too.
func TestConductor_MoveKeepsWhatWasPublishedWhileDown(t *testing.T) {
	for _, r3 := range []bool{false, true} {
		t.Run(map[bool]string{false: "R1-control", true: "R3"}[r3], func(t *testing.T) {
			var nc *nats.Conn
			var js jetstream.JetStream
			if r3 {
				c, _ := outageRig(t)
				var err error
				nc, err = nats.Connect(c.URLs())
				require.NoError(t, err)
				t.Cleanup(nc.Close)
				js, err = jetstream.New(nc)
				require.NoError(t, err)
			} else {
				_, nc, js = rig(t)
			}
			ctx := t.Context()
			publish(t, js, "h1", 1)
			publish(t, js, "h2", 2)
			old := layout.LegacyConductorConsumer("ann", "atomix")
			_, err := js.CreateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: old, FilterSubjects: Filters("atomix", []string{"taskforge"}), AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute, DeliverPolicy: jetstream.DeliverNewPolicy})
			require.NoError(t, err)
			for i, q := range []string{"three", "four"} {
				_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", int64(i+10)), []byte(`{"q":"`+q+`"}`))
				require.NoError(t, err)
			}
			f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
			var mu sync.Mutex
			var logged []string
			cfg := scaled("ann", "cond-a", f)
			cfg.Log = func(format string, a ...any) {
				mu.Lock()
				logged = append(logged, fmt.Sprintf(format, a...))
				mu.Unlock()
			}
			t.Cleanup(func() {
				if t.Failed() {
					mu.Lock()
					t.Log(strings.Join(logged, ""))
					mu.Unlock()
				}
			})
			go func() { _ = Run(ctx, nc, cfg) }()
			require.Eventually(t, func() bool { return f.ackedWith(`"three"`) && f.ackedWith(`"four"`) }, 15*time.Second, 100*time.Millisecond, "what was published while the conductor was down is typed after the move")
			require.False(t, f.typedWith("which port?"), "and nothing from before the legacy durable")
		})
	}
}

// FINDING-V12-13-1: on R3 a durable's create can lose its reply while the
// durable is made (GitHub's Windows runners: the attach ended "bind
// consumer: context deadline exceeded", 2 runs in 2). The create is tried
// again, which for the same config is the durable made. Here the first
// create makes it and answers with a timeout, as the lost reply did.
func TestBind_TriesACreateWhoseReplyTimedOutAgain(t *testing.T) {
	_, _, js := rig(t)
	ctx := t.Context()
	lost := &lostReply{JetStream: js}
	var logged []string
	r := &runner{cfg: scaled("ann", "cond-a", nil), js: lost}
	r.cfg.Log = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	was := bindCreateWait
	bindCreateWait = 10 * time.Millisecond
	t.Cleanup(func() { bindCreateWait = was })
	c, err := r.bind(ctx, 0)
	require.NoError(t, err, "bound on the second try")
	require.Equal(t, layout.ConductorConsumer("ann", "atomix"), c.CachedInfo().Name)
	require.Equal(t, 2, lost.creates)
	require.Contains(t, strings.Join(logged, ""), "timed out", "and said so")
	require.Contains(t, strings.Join(logged, ""), "trying again (2 of 5)")
}

// lostReply makes the first consumer create and answers it with a timeout.
type lostReply struct {
	jetstream.JetStream
	creates int
}

func (l *lostReply) CreateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	l.creates++
	c, err := l.JetStream.CreateConsumer(ctx, stream, cfg)
	if l.creates == 1 && err == nil {
		return nil, context.DeadlineExceeded
	}
	return c, err
}

// V12-10-2 (OBS-2D-1, research's R3 probe): the move happens once. A later
// attach that finds the new durable gone (an admin's delete while no attach
// ran) moved from the legacy floor again, which never advanced: every event
// since the upgrade was typed a second time, acknowledged ones included.
// The legacy durable is deleted after the move, so that attach starts
// fresh. On R1 and on R3 (where a never-delivered durable reads floor 0).
func TestConductor_MovesFromTheLegacyDurableOnce(t *testing.T) {
	for _, r3 := range []bool{false, true} {
		t.Run(map[bool]string{false: "R1", true: "R3"}[r3], func(t *testing.T) {
			var nc *nats.Conn
			var js jetstream.JetStream
			if r3 {
				c, _ := outageRig(t)
				var err error
				nc, err = nats.Connect(c.URLs())
				require.NoError(t, err)
				t.Cleanup(nc.Close)
				js, err = jetstream.New(nc)
				require.NoError(t, err)
			} else {
				_, nc, js = rig(t)
			}
			ctx := t.Context()
			old := layout.LegacyConductorConsumer("ann", "atomix")
			name := layout.ConductorConsumer("ann", "atomix")
			q := func(s string, i int64) {
				_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", i), []byte(`{"q":"`+s+`"}`))
				require.NoError(t, err)
			}
			q("h1", 1)
			_, err := js.CreateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: old, FilterSubjects: Filters("atomix", []string{"taskforge"}), AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute, DeliverPolicy: jetstream.DeliverNewPolicy})
			require.NoError(t, err)
			q("gap1", 10) // published while the conductor is down for the upgrade
			var mu sync.Mutex
			var logged []string
			attach := func(f *fakeSession) (context.CancelFunc, chan error) {
				cfg := scaled("ann", "cond-a", f)
				cfg.Log = func(format string, a ...any) {
					mu.Lock()
					logged = append(logged, fmt.Sprintf(format, a...))
					mu.Unlock()
				}
				rctx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- Run(rctx, nc, cfg) }()
				return cancel, done
			}
			moves := func() int {
				mu.Lock()
				defer mu.Unlock()
				n := 0
				for _, l := range logged {
					if strings.HasPrefix(l, "conductor: moved from the durable") {
						n++
					}
				}
				return n
			}
			t.Cleanup(func() {
				if t.Failed() {
					mu.Lock()
					t.Log(strings.Join(logged, ""))
					mu.Unlock()
				}
			})

			f1 := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
			stop, done := attach(f1)
			require.Eventually(t, func() bool { return f1.ackedWith(`"gap1"`) }, 15*time.Second, 50*time.Millisecond, "the move types what was published while down")
			q("after1", 11)
			q("after2", 12)
			require.Eventually(t, func() bool { return f1.ackedWith(`"after1"`) && f1.ackedWith(`"after2"`) }, 15*time.Second, 50*time.Millisecond)
			require.Eventually(t, func() bool {
				c, err := js.Consumer(ctx, events.EventsStream, name)
				return err == nil && c.CachedInfo().AckFloor.Stream == 4 && c.CachedInfo().NumAckPending == 0
			}, 10*time.Second, 250*time.Millisecond, "all three acknowledged")
			stop()
			require.NoError(t, <-done)
			require.Equal(t, 1, moves())

			require.NoError(t, js.DeleteConsumer(ctx, events.EventsStream, name), "an admin deletes the new durable while no attach runs")
			f2 := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
			stop, done = attach(f2)
			t.Cleanup(func() { stop(); <-done })
			bound := func() int {
				mu.Lock()
				defer mu.Unlock()
				n := 0
				for _, l := range logged {
					if strings.HasPrefix(l, "conductor: bound its consumer "+name) {
						n++
					}
				}
				return n
			}
			// The log, not the durable's INFO: a tight INFO poll during an
			// attach can lose its create's reply on R3 (FINDING-V12-13-1).
			require.Eventually(t, func() bool { return bound() == 2 }, 15*time.Second, 50*time.Millisecond, "bound again")
			q("after3", 13)
			require.Eventually(t, func() bool { return f2.ackedWith(`"after3"`) }, 15*time.Second, 50*time.Millisecond, "what comes after is typed")
			require.False(t, f2.typedWith(`"gap1"`) || f2.typedWith(`"after1"`) || f2.typedWith(`"after2"`), "what the first attach acknowledged is not typed again")
			require.Equal(t, 1, moves(), "no second move")
		})
	}
}

// V12-10-2: ann in ensemble x-y and ann-x in y shared the legacy name
// conductor-ann-x-y. Only the pair whose filters it holds moves from it and
// deletes it: ann-x, attaching first, starts fresh and leaves ann's
// durable as it is (it neither takes ann's floor, the colliding-pair caveat
// of V12-10-1, nor deletes it); ann then moves from it.
func TestConductor_ACollidingPairNeitherMovesFromNorDeletesTheOthersLegacy(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	old := layout.LegacyConductorConsumer("ann", "x-y")
	require.Equal(t, old, layout.LegacyConductorConsumer("ann-x", "y"))
	q := func(s string, i int64) {
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", i), []byte(`{"q":"`+s+`"}`))
		require.NoError(t, err)
	}
	lc, err := js.CreateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: old, FilterSubjects: Filters("x-y", []string{"taskforge"}), AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute})
	require.NoError(t, err)
	q("one", 1)
	b, err := lc.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	for m := range b.Messages() {
		require.NoError(t, m.DoubleAck(ctx))
	}
	q("two", 2)
	was, err := lc.Info(ctx)
	require.NoError(t, err)
	var mu sync.Mutex
	var logged []string
	attach := func(user, ens string) *fakeSession {
		f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
		cfg := scaled(user, "cond-"+user, f)
		cfg.Ensemble = ens
		cfg.Log = func(format string, a ...any) {
			mu.Lock()
			logged = append(logged, fmt.Sprintf(format, a...))
			mu.Unlock()
		}
		rctx, cancel := context.WithCancel(ctx)
		go func() { _ = Run(rctx, nc, cfg) }()
		t.Cleanup(cancel)
		return f
	}
	saw := func(prefix string) bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(logged, func(l string) bool { return strings.HasPrefix(l, prefix) })
	}
	t.Cleanup(func() {
		if t.Failed() {
			mu.Lock()
			t.Log(strings.Join(logged, ""))
			mu.Unlock()
		}
	})

	attach("ann-x", "y")
	theirs := layout.ConductorConsumer("ann-x", "y")
	require.Eventually(t, func() bool { return saw("conductor: bound its consumer " + theirs) }, 15*time.Second, 50*time.Millisecond)
	require.True(t, saw("conductor: the durable "+old+" filters "), "said why it did not move")
	require.False(t, saw("conductor: moved from the durable"), "ann-x does not take ann's floor")
	c, err := js.Consumer(ctx, events.EventsStream, theirs)
	require.NoError(t, err)
	require.Equal(t, jetstream.DeliverNewPolicy, c.CachedInfo().Config.DeliverPolicy, "a fresh start")
	now, err := lc.Info(ctx)
	require.NoError(t, err, "ann's legacy durable is not deleted")
	require.Equal(t, was.AckFloor, now.AckFloor)
	require.Equal(t, was.Delivered, now.Delivered)

	f := attach("ann", "x-y")
	require.Eventually(t, func() bool { return f.ackedWith(`"two"`) }, 15*time.Second, 50*time.Millisecond, "the control: ann moves from her own")
	require.True(t, saw("conductor: moved from the durable "+old+" to "+layout.ConductorConsumer("ann", "x-y")+" (V12-10), from stream sequence 2; "+old+" deleted"))
	_, err = js.Consumer(ctx, events.EventsStream, old)
	require.ErrorIs(t, err, jetstream.ErrConsumerNotFound)
}

// V12-10-2 (Q-V12102-1): ownership is the ensemble, not the whole filter
// set. A conductor whose --project set changed across the upgrade (it now
// also follows other) still moves from its own legacy durable and deletes
// it; the colliding pair, whose ensemble differs, does not (the test
// above).
func TestConductor_AProjectChangeAcrossTheUpgradeStillMoves(t *testing.T) {
	_, nc, js := rig(t)
	ctx := t.Context()
	old := layout.LegacyConductorConsumer("ann", "atomix")
	lc, err := js.CreateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: old, FilterSubjects: Filters("atomix", []string{"taskforge"}), AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute})
	require.NoError(t, err)
	q := func(s string, i int64) {
		_, err := events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", i), []byte(`{"q":"`+s+`"}`))
		require.NoError(t, err)
	}
	q("one", 1)
	b, err := lc.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	for m := range b.Messages() {
		require.NoError(t, m.DoubleAck(ctx))
	}
	q("two", 2)
	f := &fakeSession{nc: nc, answers: 99, acked: map[string]bool{}}
	var mu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", f)
	cfg.Projects = []string{"taskforge", "other"}
	cfg.Log = func(format string, a ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, a...))
		mu.Unlock()
	}
	t.Cleanup(func() {
		if t.Failed() {
			mu.Lock()
			t.Log(strings.Join(logged, ""))
			mu.Unlock()
		}
	})
	go func() { _ = Run(ctx, nc, cfg) }()
	require.Eventually(t, func() bool { return f.ackedWith(`"two"`) }, 15*time.Second, 50*time.Millisecond, "what the legacy left comes again")
	require.False(t, f.typedWith(`"one"`))
	mu.Lock()
	all := strings.Join(logged, "")
	mu.Unlock()
	require.Contains(t, all, "conductor: moved from the durable "+old+" to "+layout.ConductorConsumer("ann", "atomix")+" (V12-10), from stream sequence 2; "+old+" deleted")
	_, err = js.Consumer(ctx, events.EventsStream, old)
	require.ErrorIs(t, err, jetstream.ErrConsumerNotFound)
}

// OBS-2D-2: the violations that end the first bind at once are those on
// the conductor's own durable (any JetStream API verb on it), and no other.
func TestOnDurable(t *testing.T) {
	d := layout.ConductorConsumer("ann", "atomix")
	v := func(subj string) string {
		return `nats: permissions violation: Permissions Violation for Publish to "` + subj + `"`
	}
	for subj, want := range map[string]bool{
		"$JS.API.CONSUMER.INFO.APE_EVENTS." + d:                                               true,
		"$JS.API.CONSUMER.CREATE.APE_EVENTS." + d:                                             true,
		"$JS.API.CONSUMER.CREATE.APE_EVENTS." + d + ".ape.evt.coord.x.>":                      true,
		"$JS.API.CONSUMER.MSG.NEXT.APE_EVENTS." + d:                                           true,
		"$JS.API.CONSUMER.DELETE.APE_EVENTS." + d:                                             true,
		"$JS.API.CONSUMER.INFO.APE_EVENTS." + layout.LegacyConductorConsumer("ann", "atomix"): false,
		"$JS.API.CONSUMER.INFO.OTHER." + d:                                                    false,
		"$JS.API.CONSUMER.INFO.APE_EVENTS." + d + "x":                                         false,
		"$KV.ape-leases.conductor.ann.atomix":                                                 false,
	} {
		require.Equal(t, want, onDurable(v(subj), d), subj)
	}
}

// OBS-2D-2's watch on a connection two conductors share (as
// TestHeartbeatKeepsOnlyAnAnsweringConductor's do): each watcher sees an
// error while it watches, the handler the connection had keeps getting
// them, and it is the connection's handler again once both have left, in
// either order.
func TestWatchErrors_ASharedConnection(t *testing.T) {
	_, nc, _ := rig(t)
	var mu sync.Mutex
	got := map[string]int{}
	see := func(who string) func(error) {
		return func(error) { mu.Lock(); got[who]++; mu.Unlock() }
	}
	orig := func(*nats.Conn, *nats.Subscription, error) { mu.Lock(); got["orig"]++; mu.Unlock() }
	nc.SetErrorHandler(orig)
	stopA := watchErrors(nc, see("a"))
	stopB := watchErrors(nc, see("b"))
	nc.ErrorHandler()(nc, nil, errors.New("one"))
	stopA()
	nc.ErrorHandler()(nc, nil, errors.New("two"))
	stopB()
	nc.ErrorHandler()(nc, nil, errors.New("three"))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, map[string]int{"a": 1, "b": 2, "orig": 3}, got)
	require.Empty(t, errWatches, "nothing left installed")
}

// V15-RED-1 (research's gated drill, D909): after an outage the lease KV
// answered seconds before the events consumer did, and the conductor
// judged itself silent LeaseTTL after the lease came back, while the event
// it held was still pending and could not yet be delivered again (5/5 on
// 13f183a); when the server's key expiry fell in that stretch the attach
// ended. Here deliver's next fetch is held from 2 s into the outage until
// 5 s after the lease is back — past LeaseTTL (3 s), where the old rule
// judged, and inside the bound LeaseTTL + AckWait + 2 × Renew (D910;
// AckWait 3 s here, so 3 + 3 + 1 = 7 s: the gate lifts 2 s, four renew
// ticks, before it; the bound's own rows are TestAnswering_AfterAnOutage…)
// — and no silence may be judged before the held event is typed again and
// acknowledged.
func TestDrill_ConductorWaitsForItsEventsAfterAnOutage(t *testing.T) {
	c, js := outageRig(t)
	ctx := t.Context()
	anc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(anc.Close)
	a := &fakeSession{nc: anc, answers: 99, acked: map[string]bool{}}
	var logMu sync.Mutex
	var logged []string
	cfg := scaled("ann", "cond-a", a)
	cfg.AckWait = 3 * time.Second // the bound 7 s, past the gate's 5 s
	require.Equal(t, 7*time.Second, cfg.recoveryBound())
	cfg.Log = func(f string, args ...any) {
		logMu.Lock()
		logged = append(logged, time.Now().Format("15:04:05.000 ")+fmt.Sprintf(f, args...))
		logMu.Unlock()
	}
	has := func(x string) bool {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Contains(strings.Join(logged, ""), x)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(strings.Join(logged, ""))
			logMu.Unlock()
		}
	})
	gateDone := make(chan struct{})
	go func() {
		defer close(gateDone)
		for !has("riding it out") && ctx.Err() == nil {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
		g := make(chan struct{})
		testFetchGate.Store(&g)
		for !has("renewed again") && ctx.Err() == nil {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(5 * time.Second)
		testFetchGate.Store(nil)
		close(g)
	}()
	t.Cleanup(func() { <-gateDone; testFetchGate.Store(nil) })
	errA := make(chan error, 1)
	go func() { errA <- Run(ctx, anc, cfg) }()
	bound(t, js)
	publish(t, js, "block", 1)
	require.Eventually(t, func() bool { return a.done.Load() == 1 }, 5*time.Second, 50*time.Millisecond)
	a.hold.Store(true)
	_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 2), []byte(`{"q":"held across"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.typedWith("held across") }, 5*time.Second, 50*time.Millisecond)

	outage(t, c, 3*cfg.LeaseTTL)
	a.hold.Store(false)
	require.Eventually(t, func() bool { return a.ackedWith("held across") }, afterOutage, 100*time.Millisecond, "typed again after the outage, acknowledged")
	require.False(t, has("judged silent"), "no silence judged before the held event could be delivered again")
	select {
	case err := <-errA:
		t.Fatalf("attach ended: %v", err)
	default:
	}
}

// V15-RED-1's rule, row by row: after an outage an event pending from
// before it is not judged until it is typed again, and that re-type
// restarts the window; a failing read of the durable is not judged
// either; each for at most LeaseTTL + AckWait + 2 × Renew (D910), after
// which a conductor still unable to answer is judged as before (it yields
// its lease).
func TestAnswering_AfterAnOutageAndWhileItsDurableCannotBeRead(t *testing.T) {
	ttl, ack, renew := 3*time.Second, time.Second, 2*time.Second
	bound := ttl + ack + 2*renew
	stale := time.Now().Add(-2 * ttl)
	fresh := func() *runner {
		return &runner{cfg: Config{LeaseTTL: ttl, AckWait: ack, Renew: renew}, pend: map[uint64]*pending{2: {first: stale}}, lastAck: stale}
	}
	shipped := Config{}
	shipped.defaults()
	require.Equal(t, 8*time.Minute, shipped.recoveryBound(), "as shipped: 5 min + 1 min + 2 × 1 min")
	r := fresh()
	require.Equal(t, bound, r.cfg.recoveryBound())

	r = fresh()
	require.False(t, r.answering(), "the control: an event waiting past the TTL is silence")

	r = fresh()
	r.down = true
	r.reachable()
	r.lastAck = stale // the lease came back; the window as the old rule would judge it
	require.True(t, r.answering(), "pending from before the outage, not yet typed again: not judged")
	r.typedSeq(2, "ape.evt.bob.taskforge.project.M2.question")
	require.True(t, r.answering(), "typed again: the window starts there")
	r.lastAck = stale
	require.False(t, r.answering(), "and runs again")

	r = fresh()
	r.recovered, r.retyped = time.Now().Add(-(ttl + ack + time.Second)), map[uint64]bool{}
	require.True(t, r.answering(), "past LeaseTTL + AckWait but inside the two renew ticks: not judged yet (D910)")
	r.recovered = time.Now().Add(-(bound + time.Second))
	require.False(t, r.answering(), "deaf past the bound: judged as before, so it yields its lease")

	r = fresh()
	r.sweep(context.Background(), infoFails{})
	require.True(t, r.answering(), "its durable cannot be read, so no acknowledgement can be seen: not judged")
	require.Len(t, r.pend, 1, "and nothing is dropped on a read that failed")
	r.infoDown = time.Now().Add(-(ttl + ack + time.Second))
	require.True(t, r.answering(), "unreadable past LeaseTTL + AckWait, inside the bound: not judged yet")
	r.infoDown = time.Now().Add(-(bound + time.Second))
	require.False(t, r.answering(), "unreadable past the bound: judged as before")

	r = fresh()
	was := fetchPause.Load()
	fetchPause.Store(int64(time.Millisecond))
	t.Cleanup(func() { fetchPause.Store(was) })
	r.fetchFailed(context.Background(), errors.New("nats: no responders available"))
	require.True(t, r.answering(), "no fetch can deliver it: not judged")
	r.fetchOK()
	require.True(t, r.answering(), "the first good fetch restarts the window")
	r.lastAck = stale
	require.False(t, r.answering(), "and it runs again")
	r.fetchFailed(context.Background(), errors.New("nats: no responders available"))
	r.fetchDown = time.Now().Add(-(ttl + ack + time.Second))
	require.True(t, r.answering(), "fetches failing past LeaseTTL + AckWait, inside the bound: not judged yet")
	r.fetchDown = time.Now().Add(-(bound + time.Second))
	require.False(t, r.answering(), "fetches failing past the bound: judged as before")
}

// D909+: a conductor that stays deaf after an outage (its events consumer
// never delivers again) is not kept forever by the suppression: past
// the bound, LeaseTTL + AckWait + 2 × Renew (D910; 3 + 1 + 1 = 5 s here),
// it is judged as before, its lease lapses one TTL later and Run ends with
// the lapsed line — ≈ 8 s after the lease came back, never before the
// bound.
func TestDrill_ADeafConductorYieldsWithinTheBound(t *testing.T) {
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
	has := func(x string) bool {
		logMu.Lock()
		defer logMu.Unlock()
		return strings.Contains(strings.Join(logged, ""), x)
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
	a.hold.Store(true)
	_, err = events.Publish(ctx, js, events.Subject("bob", "taskforge", events.KindProject, "M2", "question"), events.MessageID("taskforge", "c1", 2), []byte(`{"q":"held across"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.typedWith("held across") }, 5*time.Second, 50*time.Millisecond)
	g := make(chan struct{}) // never opened: deliver never fetches again
	testFetchGate.Store(&g)
	t.Cleanup(func() { testFetchGate.Store(nil); close(g) })
	outage(t, c, 3*cfg.LeaseTTL)
	var back time.Time
	require.Eventually(t, func() bool {
		if has("renewed again") && back.IsZero() {
			back = time.Now()
		}
		return !back.IsZero()
	}, afterOutage, 50*time.Millisecond)
	select {
	case err := <-errA:
		require.ErrorIs(t, err, ErrLost)
		require.ErrorContains(t, err, "lapsed: ")
		took, bnd := time.Since(back), cfg.recoveryBound()
		t.Logf("D910: yielded %v after the lease came back (bound %v + the lease's TTL %v)", took.Round(100*time.Millisecond), bnd, cfg.LeaseTTL)
		require.Greater(t, took, bnd, "not before the bound")
		require.Less(t, took, bnd+cfg.LeaseTTL+5*time.Second, "within the bound, then the lease's lapse")
	case <-time.After(afterOutage):
		t.Fatal("a deaf conductor kept its lease past the bound")
	}
	require.True(t, has("judged silent"), "and said so")
}

// infoFails is a durable whose INFO fails as a cluster just back can.
type infoFails struct{ jetstream.Consumer }

func (infoFails) Info(context.Context) (*jetstream.ConsumerInfo, error) {
	return nil, context.DeadlineExceeded
}
