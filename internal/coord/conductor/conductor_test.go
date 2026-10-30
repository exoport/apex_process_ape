package conductor

import (
	"context"
	"regexp"
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
}

var (
	ackRe = regexp.MustCompile(`ape conductor ack (\S+)`)
	seqRe = regexp.MustCompile(`seq="(\d+)"`)
)

func (f *fakeSession) Idle() bool { return true }
func (f *fakeSession) Type(text string) error {
	f.mu.Lock()
	f.typed = append(f.typed, text)
	seq := seqRe.FindStringSubmatch(text)[1]
	answer := !f.acked[seq] && f.done.Load() < f.answers
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
