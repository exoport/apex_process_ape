package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
}

var (
	ackRe = regexp.MustCompile(`ape conductor ack '([^']+)'`) // single-quoted, as a shell takes it (V8g)
	seqRe = regexp.MustCompile(`seq="(\d+)"`)
)

func (f *fakeSession) Idle() bool { return true }
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
	paused, err := Paused(ctx, js, "taskforge", "M2")
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
	paused, err = Paused(ctx, js, "taskforge", "M2")
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
	paused, err := Paused(t.Context(), js, "taskforge", "M2")
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
	time.Sleep(500 * time.Millisecond) // the consumer bound: DeliverNew
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
		time.Sleep(500 * time.Millisecond)
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
		require.Eventually(t, func() bool {
			ls, err := lease.Open(ctx, js)
			if err != nil {
				return false
			}
			h, _, err := ls.Holder(ctx, layout.ConductorKey("ann", "atomix"))
			return err == nil && h == "cond-a"
		}, 10*time.Second, 100*time.Millisecond, "its lease is held again")
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
		time.Sleep(500 * time.Millisecond)
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
