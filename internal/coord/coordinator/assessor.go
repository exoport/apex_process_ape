package coordinator

import (
	"context"
	"strconv"
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
}

// Batch is a debounced run of triggers: one assessor session's input
// (step 2; Q2).
type Batch struct {
	ID             string         `json:"id"`
	Events         []TriggerEvent `json:"events"`
	Opened, Closed time.Time
}

// AssessorConfig turns the assessor's batching on (step 2): a batch closes
// once Quiet passed since its last trigger, or Ceiling since it opened,
// and OnBatch gets it.
type AssessorConfig struct {
	Quiet, Ceiling time.Duration
	OnBatch        func(context.Context, Batch)
}

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
		b.seen[e.MsgID] = true
		b.order = append(b.order, e.MsgID)
		if len(b.order) > seenKept {
			delete(b.seen, b.order[0])
			b.order = b.order[1:]
		}
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

// runAssessor batches the ensemble's projects' triggers (new ones: a
// replacement's open batches are step 8's) and hands each closed batch to
// OnBatch, until ctx ends.
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
	cons, err := c.js.OrderedConsumer(ctx, events.EventsStream, jetstream.OrderedConsumerConfig{FilterSubjects: filters, DeliverPolicy: jetstream.DeliverNewPolicy})
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
				in <- TriggerEvent{MsgID: m.Headers().Get(jetstream.MsgIDHeader), Subject: m.Subject(), Data: m.Data()}
			}
		}
	}()
	b := newBatcher(a.Quiet, a.Ceiling)
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
				a.OnBatch(ctx, *done)
			}
		}
	}
}
