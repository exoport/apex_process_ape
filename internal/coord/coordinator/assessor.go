package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// TriggerEvent is one assessor trigger (PLAN-V15-ASSESSOR step 1) as the
// coordinator received it: its message id — what an impact notice's
// events[] carries — its subject and payload.
type TriggerEvent struct {
	MsgID   string    `json:"msg_id"` //nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
	Subject string    `json:"subject"`
	Data    []byte    `json:"data"`
	At      time.Time `json:"at"`
	// Seq is its APE_EVENTS sequence: where a replacement resumes (step 8).
	Seq uint64 `json:"seq"`
}

// Batch is a debounced run of triggers: one assessor session's input
// (step 2; Q2).
type Batch struct {
	ID     string         `json:"id"`
	Events []TriggerEvent `json:"events"`
	Opened time.Time      `json:"opened"`
	Closed time.Time      `json:"closed"`
	// Replay: a batch a coordinator recorded and did not finish, run by
	// its replacement (step 8).
	Replay bool `json:"replay,omitempty"`
}

// lastSeq is the highest stream sequence of b's triggers.
func (b *Batch) lastSeq() uint64 {
	var n uint64
	for _, e := range b.Events {
		n = max(n, e.Seq)
	}
	return n
}

// AssessorConfig turns the assessor's batching on (step 2): a batch closes
// once Quiet passed since its last trigger, or Ceiling since it opened,
// and OnBatch gets it, with the Publisher its outcome goes out through
// (step 5). Batches are handed over one at a time, in order, apart from
// the loop that batches: a session takes minutes, and triggers keep
// arriving meanwhile.
type AssessorConfig struct {
	Quiet, Ceiling time.Duration
	OnBatch        func(context.Context, Batch, Publisher)
}

// batchQueue bounds the closed batches waiting for OnBatch.
const batchQueue = 64

// seenKept bounds the batcher's memory of ids it batched.
const seenKept = 4096

// ticksPerQuiet is how often a quiet window checks for a due batch.
const ticksPerQuiet = 4

// batcher is the debounce (step 2): one open batch per ensemble.
type batcher struct {
	quiet, ceiling time.Duration
	cur            *Batch
	last           time.Time
	seen           map[string]bool
	order          []string
	n              int
}

func newBatcher(quiet, ceiling time.Duration) *batcher {
	return &batcher{quiet: quiet, ceiling: ceiling, seen: map[string]bool{}}
}

// add puts e in the open batch, opening one; false for an id it batched
// before (a replay the stream's dedupe window no longer catches).
func (b *batcher) add(e TriggerEvent, now time.Time) bool {
	if e.MsgID != "" {
		if b.seen[e.MsgID] {
			return false
		}
		b.remember(e.MsgID)
	}
	if b.cur == nil {
		b.n++
		b.cur = &Batch{ID: now.UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(b.n), Opened: now}
	}
	e.At = now
	b.cur.Events = append(b.cur.Events, e)
	b.last = now
	return true
}

// remember adds id to the ids batched.
func (b *batcher) remember(id string) {
	if b.seen[id] {
		return
	}
	b.seen[id] = true
	b.order = append(b.order, id)
	if len(b.order) > seenKept {
		delete(b.seen, b.order[0])
		b.order = b.order[1:]
	}
}

// due closes and returns the open batch once quiet passed since its last
// trigger, or ceiling since it opened; nil otherwise.
func (b *batcher) due(now time.Time) *Batch {
	if b.cur == nil {
		return nil
	}
	end := b.last.Add(b.quiet)
	if c := b.cur.Opened.Add(b.ceiling); c.Before(end) {
		end = c
	}
	if now.Before(end) {
		return nil
	}
	out := b.cur
	out.Closed = end
	b.cur = nil
	return out
}

// TriggerEvents are the trigger events the assessor batches.
var TriggerEvents = []string{"plan-approved", "change-archived", "gate-red"}

// The assessor's own records on the stream (step 8), coord kind, in the
// scope <ens>-assessor so the ensemble's conductors (ape.evt.coord.<ens>.>)
// are not delivered them: a closed
// batch (assessor-batch, its triggers with their sequences), its end
// (assessor-done, after OnBatch returned), and, once per ensemble, where
// the assessor began (assessor-started, the stream's last sequence then).
// A coordinator that starts re-runs every recorded batch with no
// assessor-done, and reads the triggers after the newest sequence a
// recorded batch (or the start) covers, so a batch still open at a kill is
// batched again. A notice's per-subject dedupe makes a second session of
// one batch publish nothing twice.
const (
	EventAssessorBatch   = "assessor-batch"
	EventAssessorDone    = "assessor-done"
	EventAssessorStarted = "assessor-started"
	assessorStartedID    = "assessor"
)

// recordScope is the assessor's records' scope token.
func (c *Coordinator) recordScope() string { return c.cfg.Ensemble + "-assessor" }

// record publishes one of the assessor's records once.
func (c *Coordinator) record(ctx context.Context, id, event string, payload any) error {
	_, err := publisher{c}.onceOn(ctx, events.Subject(Owner, c.recordScope(), events.KindCoord, id, event), id, event, payload)
	return err
}

// assessorRecovery is what the stream says a starting assessor owes.
type assessorRecovery struct {
	pending []Batch
	from    uint64
	seen    []string
	n       int
}

// recoverAssessor reads the ensemble's assessor records, and records the
// start when there is none.
func (c *Coordinator) recoverAssessor(ctx context.Context) (assessorRecovery, error) {
	var rec assessorRecovery
	pre := "ape.evt." + natsconn.SubjectToken(Owner) + "." + natsconn.SubjectToken(c.recordScope()) + "." + events.KindCoord + ".*."
	done := map[string]bool{}
	if !events.ReadAll(ctx, c.js, pre+EventAssessorDone, func(data []byte) {
		var d struct {
			Batch string `json:"batch"`
		}
		if json.Unmarshal(data, &d) == nil {
			done[d.Batch] = true
		}
	}) {
		return rec, errors.New("reading assessor-done")
	}
	var batches []Batch
	if !events.ReadAll(ctx, c.js, pre+EventAssessorBatch, func(data []byte) {
		var b Batch
		if json.Unmarshal(data, &b) == nil && b.ID != "" {
			batches = append(batches, b)
		}
	}) {
		return rec, errors.New("reading assessor-batch")
	}
	for i := range batches {
		b := &batches[i]
		rec.from = max(rec.from, b.lastSeq())
		for _, e := range b.Events {
			rec.seen = append(rec.seen, e.MsgID)
		}
		if _, nstr, ok := strings.Cut(b.ID, "-"); ok {
			if n, err := strconv.Atoi(nstr); err == nil {
				rec.n = max(rec.n, n)
			}
		}
		if !done[b.ID] {
			b.Replay = true
			rec.pending = append(rec.pending, *b)
		}
	}
	st, err := c.js.Stream(ctx, events.EventsStream)
	if err != nil {
		return rec, err
	}
	startedSubj := events.Subject(Owner, c.recordScope(), events.KindCoord, assessorStartedID, EventAssessorStarted)
	if m, err := st.GetLastMsgForSubject(ctx, startedSubj); err == nil {
		var started struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(m.Data, &started)
		rec.from = max(rec.from, started.Seq)
	} else if errors.Is(err, jetstream.ErrMsgNotFound) {
		info, err := st.Info(ctx)
		if err != nil {
			return rec, err
		}
		if err := c.record(ctx, assessorStartedID, EventAssessorStarted, map[string]any{"seq": info.State.LastSeq, "by": c.cfg.ID, "at": time.Now().UTC().Format(time.RFC3339)}); err != nil {
			return rec, err
		}
		rec.from = max(rec.from, info.State.LastSeq)
	} else {
		return rec, err
	}
	return rec, nil
}

// runAssessor batches the ensemble's projects' triggers and hands each
// closed batch to OnBatch, after the batches a predecessor left unfinished
// (step 8), until ctx ends. A batch is recorded before it is run, and
// marked done after.
func (c *Coordinator) runAssessor(ctx context.Context) {
	a := c.cfg.Assessor
	filters := make([]string, 0, len(TriggerEvents)*len(c.cfg.Projects))
	for _, p := range c.cfg.Projects {
		for _, ev := range TriggerEvents {
			filters = append(filters, "ape.evt.*."+natsconn.SubjectToken(p)+".project.*."+ev)
		}
	}
	if len(filters) == 0 || a.OnBatch == nil {
		return
	}
	rec, err := c.recoverAssessor(ctx)
	if err != nil {
		return
	}
	cons, err := c.js.OrderedConsumer(ctx, events.EventsStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: filters, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: rec.from + 1,
	})
	if err != nil {
		return
	}
	in := make(chan TriggerEvent, 64)
	go func() {
		for ctx.Err() == nil {
			batch, err := cons.Fetch(16, jetstream.FetchMaxWait(time.Second))
			if err != nil {
				continue
			}
			for m := range batch.Messages() {
				e := TriggerEvent{MsgID: m.Headers().Get(jetstream.MsgIDHeader), Subject: m.Subject(), Data: m.Data()}
				if md, err := m.Metadata(); err == nil {
					e.Seq = md.Sequence.Stream
				}
				in <- e
			}
		}
	}()
	pub := publisher{c}
	closed := make(chan Batch, batchQueue+len(rec.pending))
	for _, b := range rec.pending {
		closed <- b
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-closed:
				a.OnBatch(ctx, b, pub)
				if ctx.Err() == nil {
					_ = c.record(ctx, b.ID, EventAssessorDone, map[string]any{"batch": b.ID, "by": c.cfg.ID, "at": time.Now().UTC().Format(time.RFC3339)})
				}
			}
		}
	}()
	b := newBatcher(a.Quiet, a.Ceiling)
	b.n = rec.n
	for _, id := range rec.seen {
		b.remember(id)
	}
	// Checked four times a quiet window, at least once a second.
	tick := min(a.Quiet/ticksPerQuiet, time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-in:
			b.add(e, time.Now())
		case now := <-t.C:
			if done := b.due(now); done != nil {
				// Recorded first: a coordinator that loses its lease
				// records nothing and runs nothing.
				if err := c.record(ctx, done.ID, EventAssessorBatch, done); err != nil {
					continue
				}
				closed <- *done
			}
		}
	}
}
