// Package conductorwatch is the conductor's watcher (APEX v1.4.2, D664):
// the conductor stays the person's own Claude Code session, and this
// process follows every project's events and wakes it.
//
//   - Each event the conductor acts on (block, person, question,
//     orchestrator-dead/-idle, run-lost, delegated, orders) becomes a
//     delivery, id <project>.<seq>.
//   - When the conductor runs in a tmux pane, the watcher types each
//     delivery into it, marked (apexv1.DeliveryTag and a nonce): the
//     user-prompt hook records it kind "delivery", which never grants a
//     person act. It types only when the pane still runs the registered
//     Claude Code session, the session is idle (no turn running) and its
//     input row is empty — never after half-typed input — and the screen
//     did not change since the last look. The pane's Claude Code session
//     id is recorded at the first look that finds it (the watcher's
//     start) and required before every delivery: when another session
//     takes the pane (a /clear starts one too), the watcher pauses — it
//     logs conductor-stalled in each project (the notifier reaches the
//     person) and types nothing — until `ape conductor watcher --repin`
//     run from that new session pins it, or the watcher is restarted
//     (D665+, D666+ F5).
//   - The conductor acknowledges a delivery (`ape conductor ack <id>`). A
//     delivery not acknowledged within the ack alert (default 15 min) of
//     the event logs conductor-stalled in its project, once, so the
//     project's notifier reaches the person — typed or not: a busy pane, a
//     placeholder in the input row or a paused watcher delays the typing,
//     never the alert (D666+ F1).
//   - Without a pane the watcher only records: the project's notifier
//     already runs on each of those events (notify-only).
package conductorwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/repl"
)

// EventStalled: a delivery went unacknowledged past the ack alert.
const EventStalled = apexv1.EventConductorStalled

// DefaultAckAlert is conductor.ack_alert's default.
const DefaultAckAlert = 15 * time.Minute

// Watched are the events a conductor acts on.
var Watched = []string{
	apexv1.EventBlock, apexv1.EventPerson, apexv1.EventQuestion, apexv1.EventOrchestratorDead,
	apexv1.EventOrchestratorIdle, apexv1.EventRunLost, apexv1.EventDelegated, apexv1.EventOrders,
}

// Delivery is one event for the conductor.
//
//nolint:tagliatelle // ape's state files are snake_case
type Delivery struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	Event       string `json:"event"`
	Ref         string `json:"ref,omitempty"`
	Reason      string `json:"reason,omitempty"`
	At          string `json:"at"`
	DeliveredAt string `json:"delivered_at,omitempty"`
	AckedAt     string `json:"acked_at,omitempty"`
	AlertedAt   string `json:"alerted_at,omitempty"`
	// Unsubmitted: typed, but no Enter of submitEnters submitted it (the
	// row kept the text, no turn started). Nothing more is typed over it;
	// the ack alert reaches the person (1.4.3 PDFA-6).
	Unsubmitted bool `json:"unsubmitted,omitempty"`
}

// State is the watcher's record.
//

type State struct {
	Cursors    map[string]int64 `json:"cursors"`
	Deliveries []Delivery       `json:"deliveries"`
}

// Pane is what the watcher needs of a tmux pane (Tmux in production).
type Pane interface {
	// Capture is the pane's visible text.
	Capture(ctx context.Context) (string, error)
	// Type types text into the input row; Enter presses Enter. Apart, so
	// the watcher can confirm the submit and press Enter again (PDFA-6:
	// after a long text Claude Code can swallow the first Enter).
	Type(ctx context.Context, text string) error
	Enter(ctx context.Context) error
	// Session is the Claude Code session id of the pane's foreground
	// process when that is a session registered on this machine; "" and
	// why it is not otherwise.
	Session(ctx context.Context) (id, why string)
}

// Config is one watcher.
type Config struct {
	// Projects are the project roots it follows.
	Projects []string
	// StateDir holds its state and the acknowledgements.
	StateDir string
	// Pane is the conductor's tmux pane; nil: notify-only.
	Pane     Pane
	Nonce    string
	AckAlert time.Duration
	Poll     time.Duration
	// SubmitSettle is how long a typed delivery settles before Enter, and
	// SubmitWait how long each Enter is given to submit it (1.4.3 PDFA-6;
	// defaults 300 ms and 3 s).
	SubmitSettle, SubmitWait time.Duration
	Now                      func() time.Time
	// Log reports what the watcher did (stderr in production).
	Log func(format string, a ...any)
}

func (c *Config) defaults() {
	if c.AckAlert == 0 {
		c.AckAlert = DefaultAckAlert
	}
	if c.Poll == 0 {
		c.Poll = 2 * time.Second
	}
	if c.SubmitSettle == 0 {
		c.SubmitSettle = DefaultSubmitSettle
	}
	if c.SubmitWait == 0 {
		c.SubmitWait = DefaultSubmitWait
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = func(string, ...any) {}
	}
}

func statePath(dir string) string { return filepath.Join(dir, "watcher.json") }
func ackDir(dir string) string    { return filepath.Join(dir, "acks") }

// LoadState reads the watcher's record (empty when none).
func LoadState(dir string) (*State, error) {
	s := &State{Cursors: map[string]int64{}, Deliveries: []Delivery{}}
	data, err := os.ReadFile(statePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Cursors == nil {
		s.Cursors = map[string]int64{}
	}
	return s, nil
}

func saveState(dir string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return atomicfile.Write(statePath(dir), append(data, '\n'))
}

// Ack records the conductor's acknowledgement of a delivery.
func Ack(dir, id string, now time.Time) error {
	if strings.ContainsAny(id, `/\`) || strings.TrimSpace(id) == "" {
		return fmt.Errorf("conductorwatch: %q is not a delivery id", id)
	}
	if err := os.MkdirAll(ackDir(dir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ackDir(dir), id), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// maxMarkText bounds a delivery's text: one line, under Claude Code's
// paste collapse with room for the mark and the ack.
const maxMarkText = 400

// Mark is a delivery as typed: one line, apexv1.DeliveryTag first.
func Mark(d Delivery, nonce string) string {
	text := strings.Join(strings.Fields(fmt.Sprintf("%s %s %s — %s", filepath.Base(d.Project), d.Event, d.Ref, d.Reason)), " ")
	if r := []rune(text); len(r) > maxMarkText {
		text = string(r[:maxMarkText-1]) + "…"
	}
	return fmt.Sprintf(`%sseq=%q nonce=%q> %s — ack: ape conductor ack %s </ape-delivery>`, apexv1.DeliveryTag, d.ID, nonce, text, d.ID)
}

// Watcher is a running watcher's per-look memory.
type Watcher struct {
	c    Config
	gate *Gate
}

// New is a watcher for c.
func New(c Config) *Watcher {
	c.defaults()
	return &Watcher{c: c, gate: &Gate{Pane: c.Pane, StateDir: c.StateDir, Log: c.Log}}
}

// Run looks every Poll until ctx ends.
func (w *Watcher) Run(ctx context.Context) error {
	t := time.NewTicker(w.c.Poll)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil {
			w.c.Log("watcher: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick is one look: new events become deliveries, acks are read, one
// delivery is typed when the pane allows it, and overdue ones alert.
func (w *Watcher) Tick(ctx context.Context) error {
	s, err := LoadState(w.c.StateDir)
	if err != nil {
		return err
	}
	now := w.c.Now()
	projects := map[string]*apexv1.Project{}
	for _, root := range w.c.Projects {
		p, err := apexv1.Open(root)
		if err != nil {
			continue
		}
		projects[root] = p
		cur := s.Cursors[root]
		evs := p.EventsSince(cur)
		for i := range evs {
			ev := &evs[i]
			s.Cursors[root] = max(s.Cursors[root], ev.Seq)
			if !slices.Contains(Watched, ev.Event) {
				continue
			}
			ref := ev.Change
			if ref == "" {
				ref = ev.Milestone
			}
			reason := ev.Reason
			if ev.Action != "" {
				reason = strings.TrimSpace(ev.Action + "; " + reason)
			}
			s.Deliveries = append(s.Deliveries, Delivery{
				ID: apexv1.PersonSlug(filepath.Base(root)) + "." + strconv.FormatInt(ev.Seq, 10), Project: root,
				Event: ev.Event, Ref: ref, Reason: reason, At: now.UTC().Format(time.RFC3339),
			})
		}
	}
	for i := range s.Deliveries {
		d := &s.Deliveries[i]
		if d.AckedAt == "" {
			if data, err := os.ReadFile(filepath.Join(ackDir(w.c.StateDir), d.ID)); err == nil {
				d.AckedAt = strings.TrimSpace(string(data))
			}
		}
	}
	w.deliver(ctx, s, now, projects)
	for i := range s.Deliveries {
		d := &s.Deliveries[i]
		if d.AckedAt != "" || d.AlertedAt != "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, d.At)
		if err != nil || now.Sub(at) < w.c.AckAlert {
			continue
		}
		reason := fmt.Sprintf("the conductor has not acknowledged %s (%s) since %s", d.ID, d.Event, d.At)
		if d.DeliveredAt == "" {
			why := w.gate.Why()
			if why == "" {
				why = "no conductor pane (notify-only)"
			}
			reason += "; it was never typed into the conductor's pane: " + why
		}
		if p := projects[d.Project]; p != nil {
			p.Emit(ctx, apexv1.Event{Event: EventStalled, Action: "ape conductor ack " + d.ID, Reason: reason}, nil)
		}
		d.AlertedAt = now.UTC().Format(time.RFC3339)
		w.c.Log("watcher: %s: the person is notified\n", reason)
	}
	return saveState(w.c.StateDir, s)
}

// deliver types the oldest undelivered delivery when the gate is open
// (gate.go): the pinned session's pane, idle, input row empty, steady. A
// session that just took the pane pauses the gate and notifies each
// project once.
func (w *Watcher) deliver(ctx context.Context, s *State, now time.Time, projects map[string]*apexv1.Project) {
	if w.c.Pane == nil {
		return
	}
	ok, pausedNow := w.gate.Session(ctx)
	if pausedNow != "" {
		w.notifyPaused(ctx, projects)
	}
	if !ok {
		return
	}
	i := slices.IndexFunc(s.Deliveries, func(d Delivery) bool { return d.DeliveredAt == "" && d.AckedAt == "" })
	if i < 0 || !w.gate.Ready(ctx) {
		return
	}
	d := &s.Deliveries[i]
	if err := w.c.Pane.Type(ctx, Mark(*d, w.c.Nonce)); err != nil {
		w.c.Log("watcher: typing %s: %v\n", d.ID, err)
		return
	}
	d.DeliveredAt = now.UTC().Format(time.RFC3339)
	w.gate.Typed()
	if !Submit(ctx, w.c.Pane, w.c.SubmitSettle, w.c.SubmitWait) {
		d.Unsubmitted = true
		w.c.Log("watcher: %s was typed but not submitted after %d Enters (the input row kept it, no turn started): nothing more is typed over it; the ack alert reaches the person\n", d.ID, submitEnters)
	}
}

// submitEnters bounds the Enters pressed for one delivery.
const submitEnters = 3

// DefaultSubmitSettle and DefaultSubmitWait are Submit's settle and
// per-Enter wait when a caller sets none.
const (
	DefaultSubmitSettle = 300 * time.Millisecond
	DefaultSubmitWait   = 3 * time.Second
)

// Submit presses Enter until the text just typed into pane is submitted —
// the input row empty, or a turn running — at most submitEnters times,
// after letting it settle, and gives each Enter wait to take (1.4.3
// PDFA-6: on Claude Code 2.1.311 the Enter after a ≈ 500-character typed
// delivery was swallowed, the delivery sat in the row, and the next ones
// saw a busy row). It never clears the row. Every path that types into a
// conductor's pane submits through it: the watcher and attach's
// PaneSession (v1.5 PRE-4).
func Submit(ctx context.Context, pane Pane, settle, wait time.Duration) bool {
	sleep(ctx, settle)
	for range submitEnters {
		if err := pane.Enter(ctx); err != nil {
			return false
		}
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			if screen, err := pane.Capture(ctx); err == nil && Submitted(screen) {
				return true
			}
			sleep(ctx, wait/submitLooks)
		}
	}
	return false
}

// submitLooks is how many times submit looks within one SubmitWait.
const submitLooks = 15

// Submitted is the pane after a delivery's Enter took: a turn running, or
// the input row empty again.
func Submitted(pane string) bool {
	return strings.Contains(pane, "esc to interrupt") || repl.InputEmpty(pane)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// notifyPaused logs conductor-stalled in each project once per session
// that took the pane, so its notifier reaches the person; the watcher
// goes on recording, typing nothing, until a --repin from that session
// pins it (D666+ F5).
func (w *Watcher) notifyPaused(ctx context.Context, projects map[string]*apexv1.Project) {
	reason := w.gate.PausedReason()
	for _, root := range w.c.Projects {
		if p := projects[root]; p != nil {
			p.Emit(ctx, apexv1.Event{Event: EventStalled, Action: "ape conductor watcher --repin", Reason: reason}, nil)
		}
	}
}

// Idle is the conductor pane's idle rule (D664, D664+): no turn running
// ("esc to interrupt" absent) and an empty input row — never after a
// half-typed line. The caller adds the third condition: the screen did
// not change between two looks one poll apart.
func Idle(pane string) bool {
	return !strings.Contains(pane, "esc to interrupt") && repl.InputEmpty(pane)
}
