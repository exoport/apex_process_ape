// Package conductor is APEX v1.5's conductor liveness (stage 0, step 7;
// proposal §3.1 requirements 1–4, OQ22, OQ24, OQ25): the ape process that
// owns a conductor's terminal receives the ensemble's events through the
// durable consumer conductor-<user>-<ens>, types each into the session when
// it is idle, marked as ape's, and renews the conductor lease only while
// the conductor acknowledges what it is sent. An event left unacknowledged
// is delivered again; past the escalation age it is escalated, past the
// pause age its milestone pauses.
package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
)

// Defaults (OQ22, OQ24).
const (
	DefaultEscalate = 30 * time.Minute
	DefaultPause    = 2 * time.Hour
	DefaultAckWait  = time.Minute
)

// Session is the conductor's terminal as ape drives it.
type Session interface {
	// Idle reports whether the session waits for input (nothing typed is
	// lost in a turn in progress).
	Idle() bool
	// Type submits text as one prompt.
	Type(text string) error
}

// Config is one conductor's ape.
type Config struct {
	User, Ensemble string
	// Projects are the ensemble's members, whose project events it gets.
	Projects []string
	// Holder names this conductor instance (the lease holder).
	Holder string
	// Nonce marks this process's deliveries.
	Nonce string
	// LeaseTTL (5 min) and Renew time the conductor lease; AckWait is the
	// redelivery interval; Escalate and Pause the question ages.
	LeaseTTL, Renew, AckWait, Escalate, Pause time.Duration
	Session                                   Session
}

func (c *Config) defaults() {
	if c.LeaseTTL == 0 {
		c.LeaseTTL = layout.ConductorLeaseTTL
	}
	if c.Renew == 0 {
		c.Renew = c.LeaseTTL / 5
	}
	if c.AckWait == 0 {
		c.AckWait = DefaultAckWait
	}
	if c.Escalate == 0 {
		c.Escalate, c.Pause = DefaultEscalate, DefaultPause
	}
}

// ErrTaken: another conductor of this person holds the ensemble (D545).
var ErrTaken = errors.New("conductor: another of your conductors holds this ensemble")

// ErrLost: the conductor lease expired or moved on.
var ErrLost = errors.New("conductor: lease lost")

// Filters are the subjects a conductor of ens receives: the coordinator's
// events (heartbeats included), the ensemble's contracts, and each member
// project's events. A project named twice — two checkouts of one
// project — is one filter (v1.5 PRE-3): NATS refuses overlapping filters.
func Filters(ens string, projects []string) []string {
	out := make([]string, 0, 2+len(projects))
	out = append(out, "ape.evt.coord."+tok(ens)+".>", "ape.evt.*."+tok(ens)+".contract.>")
	for _, p := range projects {
		if f := "ape.evt.*." + tok(p) + ".project.>"; !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Format is a delivery as ape types it, one line (a typed newline would
// submit early): the marker the user-prompt hook reads (apexv1.DeliveryTag;
// such a prompt never grants a person act), the event, and how to
// acknowledge it.
func Format(seq uint64, nonce, subject string, data []byte, ackToken string) string {
	body := strings.Join(strings.Fields(string(data)), " ")
	return fmt.Sprintf("%sseq=\"%d\" nonce=%q> %s %s </ape-delivery> When you have handled this event, run: ape conductor ack %s",
		apexv1.DeliveryTag, seq, nonce, subject, body, ackToken)
}

// Ack acknowledges a delivery by its token (the event's ack subject): what
// `ape conductor ack <token>` does, from any process.
func Ack(nc *nats.Conn, token string) error {
	if !strings.HasPrefix(token, "$JS.ACK.") {
		return fmt.Errorf("conductor: %q is not an ack token", token)
	}
	_, err := nc.Request(token, []byte("+ACK"), 5*time.Second)
	if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) {
		// An ack needs no reply; a plain publish is what the server reads.
		return nc.Publish(token, []byte("+ACK"))
	}
	return err
}

type pending struct {
	first     time.Time
	project   string
	milestone string
	escalated bool
	paused    bool
}

// Run claims conductor.<user>.<ens>, binds the conductor's consumer, and
// delivers until ctx ends or the lease is lost.
func Run(ctx context.Context, nc *nats.Conn, cfg Config) error {
	cfg.defaults()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	ls, err := lease.Open(ctx, js)
	if err != nil {
		return err
	}
	l, err := ls.Claim(ctx, layout.ConductorKey(tok(cfg.User), tok(cfg.Ensemble)), cfg.Holder, cfg.LeaseTTL)
	if errors.Is(err, lease.ErrHeld) {
		return ErrTaken
	}
	if err != nil {
		return err
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{
		Durable:        layout.ConductorConsumer(tok(cfg.User), tok(cfg.Ensemble)),
		FilterSubjects: Filters(cfg.Ensemble, cfg.Projects),
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        cfg.AckWait,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		// Nothing was delivered: the lease is let go, so a corrected
		// attach need not wait out its TTL (v1.5 PRE-3).
		_ = ls.Release(context.WithoutCancel(ctx), l)
		return fmt.Errorf("conductor: bind consumer: %w", err)
	}

	r := &runner{cfg: cfg, js: js, pend: map[uint64]*pending{}, lastAck: time.Now()}
	dctx, stop := context.WithCancel(ctx)
	defer stop()
	go r.deliver(dctx, cons)

	t := time.NewTicker(cfg.Renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ls.Release(context.WithoutCancel(ctx), l)
			return nil
		case <-t.C:
			r.sweep(ctx, cons)
			if !r.answering() {
				// Silent: stop renewing; the lease expires on its own.
				if _, _, err := ls.Holder(ctx, l.Key); err == nil && !r.stillHeld(ctx, ls, l) {
					return ErrLost
				}
				continue
			}
			nl, err := ls.Renew(ctx, l)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrLost, err)
			}
			l = nl
		}
	}
}

type runner struct {
	cfg     Config
	js      jetstream.JetStream
	mu      sync.Mutex
	pend    map[uint64]*pending
	lastAck time.Time
}

// deliver types each event when the session is idle; a redelivery of an
// event still pending is typed again.
func (r *runner) deliver(ctx context.Context, cons jetstream.Consumer) {
	for ctx.Err() == nil {
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			continue
		}
		for m := range batch.Messages() {
			md, err := m.Metadata()
			if err != nil {
				continue
			}
			for !r.cfg.Session.Idle() && ctx.Err() == nil {
				time.Sleep(100 * time.Millisecond)
			}
			reply := m.Reply()
			_ = r.cfg.Session.Type(Format(md.Sequence.Stream, r.cfg.Nonce, m.Subject(), m.Data(), reply))
			r.mu.Lock()
			if _, ok := r.pend[md.Sequence.Stream]; !ok {
				proj, ms := projectOf(m.Subject())
				r.pend[md.Sequence.Stream] = &pending{first: time.Now(), project: proj, milestone: ms}
			}
			r.mu.Unlock()
		}
	}
}

// sweep drops what was acknowledged (the consumer's ack floor and its
// pending set say so), records the last acknowledgement, and escalates or
// pauses what has waited too long.
func (r *runner) sweep(ctx context.Context, cons jetstream.Consumer) {
	info, err := cons.Info(ctx)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	floor := info.AckFloor.Stream
	for seq, p := range r.pend {
		if seq > floor && info.NumAckPending > 0 {
			continue
		}
		delete(r.pend, seq)
		r.lastAck = time.Now()
		// Resume what this answer unblocks, whoever paused it: a conductor
		// that took the ensemble over answers its predecessor's question.
		if p.paused || p.project != "" && pausedNow(ctx, r.js, p.project, p.milestone) {
			r.publish(ctx, p, apexv1.EventMilestoneResumed)
		}
	}
	for _, p := range r.pend {
		age := time.Since(p.first)
		if !p.escalated && age > r.cfg.Escalate {
			p.escalated = true
			r.publish(ctx, p, apexv1.EventQuestionEscalated)
		}
		if !p.paused && age > r.cfg.Pause {
			p.paused = true
			r.publish(ctx, p, apexv1.EventMilestonePaused)
		}
	}
}

// answering is the renewal rule (OQ22): nothing is waiting, or the
// conductor acknowledged something within the lease's TTL.
func (r *runner) answering() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pend) == 0 {
		return true
	}
	return time.Since(r.lastAck) < r.cfg.LeaseTTL
}

func (r *runner) stillHeld(ctx context.Context, ls *lease.Store, l lease.Lease) bool {
	return ls.Fence(ctx, l.Key, l.Token) == nil
}

func (r *runner) publish(ctx context.Context, p *pending, event string) {
	if p.project == "" {
		return // not a project event: a heartbeat or a contract's
	}
	data, err := json.Marshal(map[string]string{"by": r.cfg.Holder, "user": r.cfg.User})
	if err != nil {
		return
	}
	subj := events.Subject(r.cfg.User, p.project, events.KindProject, p.milestone, event)
	_, _ = events.Publish(ctx, r.js, subj, r.cfg.Holder+"/"+event+"/"+p.project+"/"+p.milestone+"/"+p.first.Format(time.RFC3339Nano), data)
}

// projectOf reads a project event's project and milestone from its
// subject; "" for other kinds.
func projectOf(subject string) (project, milestone string) {
	parts := strings.Split(subject, ".")
	if len(parts) == 7 && parts[4] == events.KindProject {
		return parts[3], parts[5]
	}
	return "", ""
}

func pausedNow(ctx context.Context, js jetstream.JetStream, project, milestone string) bool {
	p, err := Paused(ctx, js, project, milestone)
	return err == nil && p
}

// Paused reports whether a milestone is paused: its newest milestone-paused
// event is newer than its newest milestone-resumed (OQ24, or a person's
// `ape milestone pause`). change new and change start refuse on it (step
// 9 wires them).
func Paused(ctx context.Context, js jetstream.JetStream, project, milestone string) (bool, error) {
	paused, _, err := PausedWith(ctx, js, project, milestone)
	return paused, err
}

// Pause is a milestone-paused event's payload: the conductor's (OQ24: by,
// user) or a person's (kind "person", with the reason).
type Pause struct {
	Kind   string `json:"kind,omitempty"`
	By     string `json:"by"`
	User   string `json:"user,omitempty"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at,omitempty"`
}

// PausePerson is a person's pause's Kind.
const PausePerson = "person"

// PausedWith is Paused with the newest pause's payload.
func PausedWith(ctx context.Context, js jetstream.JetStream, project, milestone string) (bool, Pause, error) {
	s, err := js.Stream(ctx, events.EventsStream)
	if err != nil {
		return false, Pause{}, err
	}
	last := func(event string) *jetstream.RawStreamMsg {
		m, err := s.GetLastMsgForSubject(ctx, "ape.evt.*."+tok(project)+".project."+milestone+"."+event)
		if err != nil {
			return nil
		}
		return m
	}
	p, r := last(apexv1.EventMilestonePaused), last(apexv1.EventMilestoneResumed)
	if p == nil || r != nil && r.Sequence > p.Sequence {
		return false, Pause{}, nil
	}
	var pause Pause
	_ = json.Unmarshal(p.Data, &pause)
	return true, pause, nil
}

func tok(s string) string {
	return strings.ToLower(strings.NewReplacer(".", "-", "*", "-", ">", "-", " ", "-").Replace(s))
}
