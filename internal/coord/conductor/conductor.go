// Package conductor is APEX v1.5's conductor liveness (stage 0, step 7;
// proposal §3.1 requirements 1–4, OQ22, OQ24, OQ25): the ape process that
// owns a conductor's terminal receives the ensemble's events through the
// durable consumer conductor_<user>_<ens>, types each into the session when
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
	"strconv"
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
	// Log reports what the conductor's ape rides out (V8d); nil: nothing.
	Log func(format string, a ...any)
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
// acknowledge it — the token single-quoted, so a shell keeps its $JS
// (V8g, FINDING-V15-1C-5: run unquoted it reached ape as .ACK.…).
func Format(seq uint64, nonce, subject string, data []byte, ackToken string) string {
	body := strings.Join(strings.Fields(string(data)), " ")
	return fmt.Sprintf("%sseq=\"%d\" nonce=%q> %s %s </ape-delivery> When you have handled this event, run: ape conductor ack '%s'",
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
	r := &runner{cfg: cfg, js: js, pend: map[uint64]*pending{}, lastAck: time.Now(), reconnects: nc.Stats().Reconnects}
	cons, err := r.bind(ctx, 0)
	if err != nil {
		// Nothing was delivered: the lease is let go, so a corrected
		// attach need not wait out its TTL (v1.5 PRE-3).
		_ = ls.Release(context.WithoutCancel(ctx), l)
		return fmt.Errorf("conductor: bind consumer: %w", err)
	}
	r.cons = cons
	// Where the durable stood when bound: its ack floor, not what it
	// delivered (research, D863+): an earlier attach (a crash, a lease taken
	// over) can have left events delivered and unanswered, which this
	// runner does not hold; a rebind after a loss before they come again
	// starts at or below them, not after them.
	r.seen = boundFloor(cons.CachedInfo())
	// Which stream it was bound on: a rebuild makes a new one, whose
	// sequences start again at 1 (R2C-1).
	if st, err := js.Stream(ctx, events.EventsStream); err == nil {
		r.created = st.CachedInfo().Created
	}
	dctx, stop := context.WithCancel(ctx)
	defer stop()
	go r.deliver(dctx)

	t := time.NewTicker(cfg.Renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ls.Release(context.WithoutCancel(ctx), l)
			return nil
		case <-t.C:
			// The connection says an outage too: a request made while it
			// is down waits in its buffer rather than failing, so no error
			// need ever reach keep (V8d).
			if !nc.IsConnected() {
				r.unreachable(errors.New("NATS disconnected"))
				continue
			}
			if n := nc.Stats().Reconnects; n != r.reconnects {
				r.reconnects = n
				r.unreachable(errors.New("NATS reconnected"))
			}
			r.sweep(ctx, r.consumer())
			if l, err = r.keep(ctx, ls, l); err != nil {
				return err
			}
		}
	}
}

type runner struct {
	cfg     Config
	js      jetstream.JetStream
	mu      sync.Mutex
	cons    jetstream.Consumer
	pend    map[uint64]*pending
	lastAck time.Time
	// down: the lease could not be kept at the last tick, or NATS was
	// away since (said once); reconnects is the connection's count.
	down       bool
	reconnects uint64
	// seen is the highest stream sequence typed, or acknowledged as the
	// durable last reported (its ack floor, never past an unanswered
	// delivery: D863+); rebindMu serialises a rebind between deliver and
	// sweep (V12-8).
	seen     uint64
	rebindMu sync.Mutex
	// created is when the events stream the durable was bound on was
	// created (R2C-1: a rebuild re-declares it).
	created time.Time
}

// bind binds the conductor's durable consumer, creating it when it is not
// there (V12-8: get, then create). One that exists is used as it is, or
// updated to this conductor's filters and ack wait keeping where it
// delivers from (NATS refuses a change of its deliver policy or start
// sequence). A new one starts at stream sequence from, or with the next
// event when from is 0 (the first bind), which also writes where that is,
// the stream's last sequence + 1, into the durable's own config as
// StartSeqMeta (V12-13): a DeliverNew durable on an R3 stream reports ack
// floor 0 until its first ack, so a rebind after a loss before that
// replayed the whole stream. DeliverNew stays the create: one by start
// sequence took ≈ 10 s on R3 while a consumer info for it was in flight,
// and its reply timed out.
func (r *runner) bind(ctx context.Context, from uint64) (jetstream.Consumer, error) {
	want := jetstream.ConsumerConfig{
		Durable:        layout.ConductorConsumer(tok(r.cfg.User), tok(r.cfg.Ensemble)),
		FilterSubjects: Filters(r.cfg.Ensemble, r.cfg.Projects),
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        r.cfg.AckWait,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	}
	c, err := r.js.Consumer(ctx, events.EventsStream, want.Durable)
	if err == nil {
		have := c.CachedInfo().Config
		if slices.Equal(have.FilterSubjects, want.FilterSubjects) && have.AckWait == want.AckWait && have.AckPolicy == want.AckPolicy {
			return c, nil
		}
		want.DeliverPolicy, want.OptStartSeq, want.OptStartTime = have.DeliverPolicy, have.OptStartSeq, have.OptStartTime
		want.Metadata = have.Metadata // its recorded start (V12-13) stays
		return r.js.UpdateConsumer(ctx, events.EventsStream, want)
	}
	if !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil, err
	}
	moved := ""
	if from == 0 {
		from, moved = r.legacyStart(ctx)
	}
	if from > 0 {
		want.DeliverPolicy, want.OptStartSeq = jetstream.DeliverByStartSequencePolicy, from
	} else {
		st, err := r.js.Stream(ctx, events.EventsStream)
		if err != nil {
			return nil, err
		}
		want.Metadata = map[string]string{StartSeqMeta: strconv.FormatUint(st.CachedInfo().State.LastSeq+1, 10)}
	}
	c, err = r.js.CreateConsumer(ctx, events.EventsStream, want)
	if err == nil && moved != "" {
		r.logf("conductor: moved from the durable %s to %s (V12-10), from stream sequence %d; %s is left for an admin to delete\n", moved, want.Durable, from, moved)
	}
	if errors.Is(err, jetstream.ErrConsumerExists) {
		return r.js.Consumer(ctx, events.EventsStream, want.Durable)
	}
	return c, err
}

// legacyStart is where a conductor's first durable under its V12-10 name
// starts when its durable of before V12-10 is there: after that one's
// floor, so what it delivered and was not acknowledged comes again. A
// floor of 0 with nothing pending is a durable that never got an event:
// none (0), and the new one starts fresh. It is only read: two pairs could
// share the old name.
func (r *runner) legacyStart(ctx context.Context) (from uint64, legacy string) {
	legacy = layout.LegacyConductorConsumer(tok(r.cfg.User), tok(r.cfg.Ensemble))
	lc, err := r.js.Consumer(ctx, events.EventsStream, legacy)
	if err != nil {
		return 0, ""
	}
	info := lc.CachedInfo()
	floor := boundFloor(info)
	if floor == 0 && info.NumAckPending == 0 {
		return 0, ""
	}
	return floor + 1, legacy
}

// StartSeqMeta is the conductor durable's metadata key holding the stream
// sequence it was first bound at (V12-13).
const StartSeqMeta = "ape.start-seq"

// boundFloor is where a bound durable stands: its ack floor, or the
// sequence before the start its own config records (StartSeqMeta, or a
// rebind's start sequence) when that is higher, since an R3 durable
// reports floor 0 until its first ack (V12-13). A durable from before
// V12-13 records no start, and has its floor only.
func boundFloor(info *jetstream.ConsumerInfo) uint64 {
	floor := info.AckFloor.Stream
	c := info.Config
	if c.DeliverPolicy == jetstream.DeliverByStartSequencePolicy && c.OptStartSeq > 0 {
		floor = max(floor, c.OptStartSeq-1)
	}
	if v, err := strconv.ParseUint(c.Metadata[StartSeqMeta], 10, 64); err == nil && v > 0 {
		floor = max(floor, v-1)
	}
	return floor
}

// rebind binds the durable again after the cluster lost it (V12-6,
// V12-8): from the lowest sequence still waiting for an acknowledgement,
// so what the lost durable held is delivered again with a fresh token
// (R2B-1), else from after the last delivered, so what was published
// while it was gone is delivered too. Nothing when it is there after all.
// On an events stream a rebuild made anew (R2C-1), from that stream's first
// event, and what was held from the old one is dropped.
func (r *runner) rebind(ctx context.Context) {
	r.rebindMu.Lock()
	defer r.rebindMu.Unlock()
	name := layout.ConductorConsumer(tok(r.cfg.User), tok(r.cfg.Ensemble))
	if _, err := r.js.Consumer(ctx, events.EventsStream, name); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return
	}
	st, err := r.js.Stream(ctx, events.EventsStream)
	if err != nil {
		return // no stream to bind on yet (a rebuild under way): the next sweep tries again
	}
	si := st.CachedInfo()
	r.mu.Lock()
	from := r.seen + 1
	for seq := range r.pend {
		from = min(from, seq)
	}
	// A rebuilt stream (R2C-1): its sequences start again at 1, so a start
	// taken from the old one would be past its end, and the server would
	// deliver nothing until the new stream reached it. Bound from its
	// first event instead; what was held belongs to the old stream.
	rebuilt := (!r.created.IsZero() && !si.Created.Equal(r.created)) || from > si.State.LastSeq+1
	dropped := 0
	if rebuilt {
		from, dropped = max(si.State.FirstSeq, 1), len(r.pend)
		r.pend, r.seen = map[uint64]*pending{}, 0
		r.created = si.Created
	}
	r.mu.Unlock()
	c, err := r.bind(ctx, from)
	if err != nil {
		return
	}
	r.mu.Lock()
	r.cons = c
	r.mu.Unlock()
	if rebuilt {
		r.logf("conductor: the events stream is new (rebuild): bound its consumer from stream sequence %d; %d event(s) held from the old stream dropped\n", from, dropped)
		return
	}
	r.logf("conductor: bound its consumer again, from stream sequence %d\n", from)
}

func (r *runner) consumer() jetstream.Consumer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cons
}

func (r *runner) logf(format string, a ...any) {
	if r.cfg.Log != nil {
		r.cfg.Log(format, a...)
	}
}

// keep is one tick of the conductor lease (OQ22, V8d). A silent
// conductor stops renewing, and once its lease lapsed it has lost the
// ensemble (ErrLost), which another of the person's conductors may then
// take over (D545). An answering one renews it, and rides out NATS being
// unreachable (FINDING-V15-1C-DRILL-1: attach exited on the first failed
// read): tried again at the next tick, as the supervisor does; a lease
// that lapsed meanwhile is claimed again when nobody else holds it, or
// taken up at its new revision when it is still this conductor's. Another
// conductor holding it ends the run (ErrLost).
func (r *runner) keep(ctx context.Context, ls *lease.Store, l lease.Lease) (lease.Lease, error) {
	// While NATS is unreachable nobody could acknowledge anything, so
	// silence is judged only once it answers again (reachable restarts the
	// window).
	if !r.down && !r.answering() {
		// Silent: stop renewing; the lease expires on its own.
		if _, _, err := ls.Holder(ctx, l.Key); err == nil && ls.Fence(ctx, l.Key, l.Token) != nil {
			return l, ErrLost
		}
		return l, nil
	}
	nl, err := ls.Renew(ctx, l)
	if err == nil {
		r.reachable()
		return nl, nil
	}
	if !errors.Is(err, lease.ErrStale) {
		r.unreachable(err)
		return l, nil
	}
	cur, tok, err := ls.Holder(ctx, l.Key)
	switch {
	case err != nil:
		r.unreachable(err)
		return l, nil
	case cur != "" && cur != r.cfg.Holder:
		return l, fmt.Errorf("%w: %s holds %s now", ErrLost, cur, l.Key)
	case cur == r.cfg.Holder:
		// Ours, at a revision this conductor did not see (a renewal whose
		// reply the outage lost).
		nl, err := ls.Renew(ctx, lease.Lease{Key: l.Key, Holder: r.cfg.Holder, Token: tok, TTL: r.cfg.LeaseTTL})
		if err != nil {
			r.unreachable(err)
			return l, nil
		}
		r.reachable()
		return nl, nil
	}
	nl, err = ls.Claim(ctx, l.Key, r.cfg.Holder, r.cfg.LeaseTTL)
	if errors.Is(err, lease.ErrHeld) {
		return l, nil // taken between the read and the claim: read again next tick
	}
	if err != nil {
		r.unreachable(err)
		return l, nil
	}
	r.reachable()
	r.logf("conductor: %s had lapsed during the outage; claimed again\n", l.Key)
	return nl, nil
}

// unreachable says once per outage that the lease could not be kept.
func (r *runner) unreachable(err error) {
	if !r.down {
		r.down = true
		r.logf("conductor: the conductor lease could not be kept (%v); riding it out, trying again every %s\n", err, r.cfg.Renew)
	}
}

// reachable ends an outage: the conductor's answer window starts again
// from now, since it could acknowledge nothing while NATS was down.
func (r *runner) reachable() {
	if r.down {
		r.down = false
		r.mu.Lock()
		r.lastAck = time.Now()
		r.mu.Unlock()
		r.logf("conductor: the conductor lease is renewed again\n")
	}
}

// deliver types each event when the session is idle; a redelivery of an
// event still pending is typed again. A fetch that fails (NATS down) is
// tried again after a pause, and a consumer gone from the cluster is
// bound again (V8d, V12-6).
//
// Fetch only publishes the pull: the server's answer to it — 409 Consumer
// Deleted, or 503 No Responders for a consumer that no longer exists —
// reaches only batch.Error() (nats.go v1.53.1), so that is read too. Read
// nowhere, a deleted durable left attach deaf, pulling ≈ 6,650 times a
// second (FINDING-2A-3).
func (r *runner) deliver(ctx context.Context) {
	for ctx.Err() == nil {
		batch, err := r.consumer().Fetch(1, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			r.fetchFailed(ctx, err)
			continue
		}
		cons := r.consumer()
		for m := range batch.Messages() {
			md, err := m.Metadata()
			if err != nil {
				continue
			}
			for !r.cfg.Session.Idle() && ctx.Err() == nil {
				time.Sleep(100 * time.Millisecond)
			}
			// Bound again while it waited for a busy session (FINDING-DRY-2C-1,
			// D863+): this copy's token is the lost durable's, and the new one
			// delivers the event again with a live one; typed, the session
			// would see it twice and answer the dead copy.
			if r.consumer() != cons {
				continue
			}
			reply := m.Reply()
			_ = r.cfg.Session.Type(Format(md.Sequence.Stream, r.cfg.Nonce, m.Subject(), m.Data(), reply))
			r.mu.Lock()
			r.seen = max(r.seen, md.Sequence.Stream)
			if _, ok := r.pend[md.Sequence.Stream]; !ok {
				proj, ms := projectOf(m.Subject())
				r.pend[md.Sequence.Stream] = &pending{first: time.Now(), project: proj, milestone: ms}
			}
			r.mu.Unlock()
		}
		if err := batch.Error(); err != nil {
			r.fetchFailed(ctx, err)
		}
	}
}

// fetchFailed handles a fetch's error, returned or in its batch: a
// consumer the server says is gone is bound again, once JetStream answers
// that it is (so an outage's 503s are not taken for a deletion); then,
// whatever the error, a pause before the next pull.
func (r *runner) fetchFailed(ctx context.Context, err error) {
	if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, nats.ErrNoResponders) {
		r.rebind(ctx)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}

// sweep drops what was acknowledged (the consumer's ack floor and its
// pending set say so), records the last acknowledgement, and escalates or
// pauses what has waited too long. A durable the cluster no longer has is
// bound again here too (DRY-2B-1): deliver meets it only at its next
// fetch, which waits while a delivery is held for a busy session.
func (r *runner) sweep(ctx context.Context, cons jetstream.Consumer) {
	info, err := cons.Info(ctx)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		r.rebind(ctx)
		return
	}
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	floor := info.AckFloor.Stream
	// The ack floor, not what was delivered (FINDING-DRY-2C-1, D863+):
	// delivered events this runner never typed (one fetched for a busy
	// session, another attach's) are not answers, and a rebind must start
	// at or below them.
	r.seen = max(r.seen, floor)
	for seq, p := range r.pend {
		// Acknowledged: at or below the ack floor, or delivered with
		// nothing waiting. Not yet delivered by this durable (one bound
		// again after a loss starts below it, V12-8/R2B-1) is not an
		// answer: its floor is not the old durable's.
		if seq > floor && (info.NumAckPending > 0 || seq > info.Delivered.Stream) {
			continue
		}
		delete(r.pend, seq)
		r.lastAck = time.Now()
		// Resume what this answer unblocks, whichever conductor paused it:
		// one that took the ensemble over answers its predecessor's
		// question. A person's pause is the person's to lift
		// (FINDING-V15-1C2-1): the resume published here is a conductor's,
		// and lifts a conductor's pause only.
		if p.project != "" && conductorPausedNow(ctx, r.js, p.project, p.milestone, r.cfg.User) {
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

// conductorPausedNow reports a milestone a conductor's pause (OQ24)
// holds, whatever a person's pause says.
func conductorPausedNow(ctx context.Context, js jetstream.JetStream, project, milestone, user string) bool {
	st, err := readPauses(ctx, js, project, milestone, user)
	return err == nil && st.conductor != nil
}

// Paused reports whether a milestone is paused: a person's pause (`ape
// milestone pause`) newer than the person's newest resume, or a
// conductor's (OQ24) newer than any newer resume, a conductor's or a
// person's. The two are read apart (FINDING-V15-1C2-1, D797+): a
// conductor's resume never lifts a person's pause, and a person's resume
// lifts both, so a person can clear a pause whose conductor is gone
// (Q-V8J-1). change new and change start refuse on it (step 9 wires them).
//
// A pause or resume counts only from holder, the subject token of the user
// whose checkout holds the milestone's claim (V12c, D6): a person's pause
// is that person's act (OQ6), and the holder's own conductor runs on that
// user's credential, so its pauses carry the same token. The server binds
// an event's owner token to its publisher's credential (cred.MemberGrant),
// so another member of the project can neither pause the milestone nor
// lift its holder's pause, a person's or a conductor's — whatever kind its
// payload claims (V12-2). Another person's conductor pausing a milestone
// needs a conductor credential of its own (R5).
func Paused(ctx context.Context, js jetstream.JetStream, project, milestone, holder string) (bool, error) {
	paused, _, err := PausedWith(ctx, js, project, milestone, holder)
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

// PausedWith is Paused with the pause that holds it: the person's when
// both hold, since only the person lifts it.
func PausedWith(ctx context.Context, js jetstream.JetStream, project, milestone, holder string) (bool, Pause, error) {
	st, err := readPauses(ctx, js, project, milestone, holder)
	if err != nil {
		return false, Pause{}, err
	}
	switch {
	case st.person != nil:
		return true, *st.person, nil
	case st.conductor != nil:
		return true, *st.conductor, nil
	}
	return false, Pause{}, nil
}

// pauses is a milestone's standing pauses, a person's and a conductor's,
// each nil when its newest resume is newer.
type pauses struct{ person, conductor *Pause }

// pauseReadMax bounds how many pause and resume events one read takes in
// (D800+): past it the read fails, and a gate refuses rather than guess.
const pauseReadMax = 512

// errPauseHistory is a milestone whose pause history the bound cannot
// read.
var errPauseHistory = errors.New("more pause and resume events than one read takes")

// readPauses finds each kind's newest pause or resume event: a person's
// events carry kind "person", a conductor's carry none; a conductor's
// resume lifts a conductor's pause only, a person's lifts both (D797+,
// Q-V8J-1). Only the newest event of a kind matters (and the newest person
// resume, which is newer than a conductor event in the window or older than
// all of it), so it reads back from the newest in a widening window of
// stream sequences, and stops once both kinds are seen or the window
// reaches the stream's start. An event whose owner token is not holder is
// passed over, of either kind ("": none is): the kind is the payload's,
// which any publisher writes (V12-2).
func readPauses(ctx context.Context, js jetstream.JetStream, project, milestone, holder string) (pauses, error) {
	s, err := js.Stream(ctx, events.EventsStream)
	if err != nil {
		return pauses{}, err
	}
	subjects := map[string]bool{} // subject → it is a pause
	var top uint64
	for _, event := range []string{apexv1.EventMilestonePaused, apexv1.EventMilestoneResumed} {
		subj := "ape.evt.*." + tok(project) + ".project." + milestone + "." + event
		m, err := s.GetLastMsgForSubject(ctx, subj)
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			continue
		}
		if err != nil {
			return pauses{}, err
		}
		subjects[subj] = event == apexv1.EventMilestonePaused
		top = max(top, m.Sequence)
	}
	if top == 0 {
		return pauses{}, nil
	}
	type newest struct {
		seq    uint64
		paused bool
		p      Pause
	}
	for span := uint64(256); ; span *= 8 {
		from := uint64(1)
		if top > span {
			from = top - span
		}
		var person, cond *newest
		var personResume uint64
		read := 0
		for subj, paused := range subjects {
			for seq := from; seq <= top; {
				m, err := s.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(subj))
				if errors.Is(err, jetstream.ErrMsgNotFound) {
					break
				}
				if err != nil {
					return pauses{}, err
				}
				read++
				if read > pauseReadMax {
					return pauses{}, fmt.Errorf("%s %s: %w (%d)", project, milestone, errPauseHistory, pauseReadMax)
				}
				var p Pause
				_ = json.Unmarshal(m.Data, &p)
				e := &newest{seq: m.Sequence, paused: paused, p: p}
				if holder != "" && ownerOf(m.Subject) != tok(holder) {
					seq = m.Sequence + 1
					continue
				}
				if p.Kind == PausePerson {
					if person == nil || e.seq > person.seq {
						person = e
					}
					if !paused {
						personResume = max(personResume, e.seq)
					}
				} else if cond == nil || e.seq > cond.seq {
					cond = e
				}
				seq = m.Sequence + 1
			}
		}
		if person != nil && cond != nil || from == 1 {
			var st pauses
			if person != nil && person.paused {
				st.person = &person.p
			}
			if cond != nil && cond.paused && cond.seq > personResume {
				st.conductor = &cond.p
			}
			return st, nil
		}
	}
}

// ownerOf is an event subject's owner token (ape.evt.<owner>.…).
func ownerOf(subject string) string {
	rest, ok := strings.CutPrefix(subject, "ape.evt.")
	if !ok {
		return ""
	}
	owner, _, _ := strings.Cut(rest, ".")
	return owner
}

func tok(s string) string {
	return strings.ToLower(strings.NewReplacer(".", "-", "*", "-", ">", "-", " ", "-").Replace(s))
}
