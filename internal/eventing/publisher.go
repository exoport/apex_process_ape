// Package eventing publishes structured JSON progress events over an
// optional NATS connection (PLAN-13 D2). Every pipeline/task/command run
// can stream a run/stage/step lifecycle a remote consumer follows live.
//
// It is strictly fire-and-forget: publishing never blocks or fails a run.
// Events ride a buffered channel drained by a single goroutine; on overflow
// they are dropped with a counter (reported once at Close). A nil *Publisher
// (returned by New when NATS is off) makes every method a no-op, so callers
// need no conditionals. All diagnostics go to stderr — never stdout, which
// carries the `ape task --output-format json` envelope the eval parses.
package eventing

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/nats-io/nats.go"
)

// defaultBuffer is the publish channel depth. Generous enough that a normal
// run never overflows; a runaway producer drops rather than backs up.
const defaultBuffer = 1024

// now is overridable in tests for deterministic timestamps.
var now = func() time.Time { return time.Now().UTC() }

// Options configures a Publisher.
type Options struct {
	// Identity is the decoded NATS credential identity (natsconn.Identity).
	// Its SubjectToken is the <user> subject segment; Name/PublicKey fill
	// the payload user block.
	Identity natsconn.Identity
	// Project is the project root; it is slugged into the <project> segment.
	Project string
	// Kind is the <kind> segment (pipeline/task/command/script/session/svc).
	Kind Kind
	// ID is the <id> segment (run/command/session/job id). The caller applies
	// any APE_JOB_ID override before constructing the Publisher.
	ID string
	// Prefix overrides the subject root (default DefaultPrefix "ape.evt").
	Prefix string
	// Buffer overrides the publish channel depth (default defaultBuffer).
	Buffer int
}

// Publisher serializes payloads and publishes them on the event taxonomy.
// The zero use is via New; a nil *Publisher is a valid no-op.
type Publisher struct {
	nc      *nats.Conn
	prefix  string
	user    string
	userBlk User
	project string
	kind    Kind
	id      string

	ch      chan message
	wg      sync.WaitGroup
	dropped atomic.Int64
	pubErrs atomic.Int64

	// mu guards closed + the send on ch. Emit holds it read-locked while
	// enqueuing; Close write-locks before closing ch, so a late Emit (e.g.
	// a trailing hook from the still-running bridge after Run returns) can
	// never send on a closed channel.
	mu     sync.RWMutex
	closed bool
}

type message struct {
	subject string
	data    []byte
}

// New constructs a Publisher on nc. Returns nil when nc is nil (NATS off),
// so every method is a safe no-op and callers need no branching.
func New(nc *nats.Conn, opts Options) *Publisher {
	if nc == nil {
		return nil
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	buf := opts.Buffer
	if buf <= 0 {
		buf = defaultBuffer
	}
	user := opts.Identity.SubjectToken
	if user == "" {
		user = "anonymous"
	}
	p := &Publisher{
		nc:      nc,
		prefix:  prefix,
		user:    token(user),
		userBlk: User{Name: opts.Identity.Name, PublicKey: opts.Identity.Subject},
		project: ProjectSlug(opts.Project),
		kind:    opts.Kind,
		id:      token(opts.ID),
		ch:      make(chan message, buf),
	}
	p.wg.Add(1)
	go p.loop()
	return p
}

func (p *Publisher) loop() {
	defer p.wg.Done()
	for m := range p.ch {
		if err := p.nc.Publish(m.subject, m.data); err != nil {
			p.pubErrs.Add(1)
		}
	}
}

// Emit publishes a caller-named event with arbitrary extra payload fields
// merged into the versioned envelope. It underpins the typed lifecycle
// methods and is the seam PLAN-17 session events use.
func (p *Publisher) Emit(event string, extra map[string]any) {
	if p == nil || p.nc == nil {
		return
	}
	subject := p.subject(event)
	data := p.encode(event, extra)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		p.dropped.Add(1)
		return
	}
	select {
	case p.ch <- message{subject: subject, data: data}:
	default:
		p.dropped.Add(1)
	}
}

// encode builds the versioned envelope. Common fields are always present;
// extra fields (which may include session_id) are merged at the top level.
func (p *Publisher) encode(event string, extra map[string]any) []byte {
	m := map[string]any{
		"v":       SchemaVersion,
		"ts":      now().Format(time.RFC3339Nano),
		"user":    p.userBlk,
		"project": p.project,
		"event":   event,
	}
	if p.id != "" {
		m["run_id"] = p.id
	}
	maps.Copy(m, extra)
	data, err := json.Marshal(m)
	if err != nil {
		// A payload that won't marshal is a programming error, not a run
		// error: report to stderr and drop.
		fmt.Fprintf(os.Stderr, "⚠ eventing: marshal %s: %v\n", event, err)
		return nil
	}
	return data
}

// Close stops the publisher, flushing queued events to the server, and
// reports any dropped/errored events once on stderr. Safe on a nil
// Publisher and idempotent. The caller still owns the *nats.Conn and should
// Drain it afterward.
func (p *Publisher) Close() {
	if p == nil || p.nc == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.ch)
	p.mu.Unlock()

	p.wg.Wait()
	_ = p.nc.Flush()
	if d := p.dropped.Load(); d > 0 {
		fmt.Fprintf(os.Stderr, "⚠ eventing: %d event(s) dropped (publish buffer overflow)\n", d)
	}
	if e := p.pubErrs.Load(); e > 0 {
		fmt.Fprintf(os.Stderr, "⚠ eventing: %d event(s) failed to publish\n", e)
	}
}

// Dropped reports how many events were dropped on overflow (test/observability).
func (p *Publisher) Dropped() int64 {
	if p == nil {
		return 0
	}
	return p.dropped.Load()
}
