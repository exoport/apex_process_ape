package apexv1

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// W3 (v1.1): the project's event log. Every event ape notifies about is
// first appended, one JSON line, to _apex/state/events.jsonl (ignored by
// the state whitelist, created on demand), and only then is the project's
// `notify:` command run — so a notifier that fails, or none at all, never
// loses an event, and a waiter (`ape status --wait`) reads the log instead
// of watching processes.
//
//   - seq is strictly increasing per project: it is taken under an
//     exclusive lock on _apex/state/events.lock, because concurrent ape
//     processes are normal (a detached verify beside the loop).
//   - At EventsRollBytes the log is renamed events.1.jsonl (the one kept)
//     before the next line is written, and seq carries on from the rolled
//     file's last line.
//   - A line is {seq, at, event, milestone, change, run, exit, reason,
//     action}, every field present ("" or null when it does not apply),
//     plus skill for an event about an ape task run.

// The events (APE_EVENT). stop and block are H20's; the rest W3's.
const (
	EventStop  = NotifyStop
	EventBlock = NotifyBlock
	// EventPerson: the project waits on a person act it did not wait on
	// before; action is the phrase the person types (PersonActions).
	EventPerson = "person"
	// EventGateGreen: a milestone's gate turned green (red, or never
	// run, before).
	EventGateGreen = "gate-green"
	// EventMilestoneDone: ape closed a milestone.
	EventMilestoneDone = "milestone-done"
	// EventRunEnd: an ape task run ended, attached or detached; exit is
	// its exit code.
	EventRunEnd = "run-end"
	// EventRunLost: a detached run's supervisor is gone without recording
	// an exit; logged once per run, by the first ape command to see it.
	EventRunLost = "run-lost"
)

// EventNames are every event, in the order the docs list them.
var EventNames = []string{EventPerson, EventGateGreen, EventMilestoneDone, EventRunEnd, EventRunLost, EventStop, EventBlock}

// EventsRollBytes is the size at which events.jsonl is rolled (10 MB).
var EventsRollBytes int64 = 10 << 20

// Event is one line of events.jsonl.
type Event struct {
	Seq       int64  `json:"seq"`
	At        string `json:"at"`
	Event     string `json:"event"`
	Milestone string `json:"milestone"`
	Change    string `json:"change"`
	Run       string `json:"run"`
	Exit      *int   `json:"exit"`
	Reason    string `json:"reason"`
	Action    string `json:"action"`
	Skill     string `json:"skill,omitempty"`
}

// EventsPath is _apex/state/events.jsonl.
func (p *Project) EventsPath() string { return filepath.Join(p.StateDir(), "events.jsonl") }

// EventsRolledPath is the one rolled log kept, events.1.jsonl.
func (p *Project) EventsRolledPath() string { return filepath.Join(p.StateDir(), "events.1.jsonl") }

func (p *Project) eventsLockPath() string { return filepath.Join(p.StateDir(), "events.lock") }

// eventsOn reports whether the project keeps an event log: an APEX v1
// project (never a v0 one, nor a directory ape would make look v1 by
// creating _apex/state/ in it).
func (p *Project) eventsOn() bool { return p.IsV1() && !IsV0Project(p.Root) }

// Emit appends ev to the event log, then runs the project's notifier with
// it; it returns ev as logged (its seq and at). warn gets the one line a
// failure is worth (nil: stderr). A log that cannot be written is that
// line, and the notifier still runs; neither ever changes an outcome.
func (p *Project) Emit(ctx context.Context, ev Event, warn io.Writer) Event {
	if warn == nil {
		warn = os.Stderr
	}
	if p.eventsOn() {
		var logged []Event
		err := p.withEventsLock(func() error {
			var err error
			logged, err = p.appendEventsLocked([]Event{ev})
			return err
		})
		if err != nil {
			fmt.Fprintf(warn, "events (%s): %v — not logged\n", ev.Event, err)
		} else {
			ev = logged[0]
		}
	}
	p.runNotifier(ctx, ev, warn)
	return ev
}

// withEventsLock runs fn holding the event log's lock.
func (p *Project) withEventsLock(fn func() error) error {
	p.ensureState()
	unlock, err := lockFileExclusive(p.eventsLockPath())
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// appendEventsLocked stamps evs with the next seqs and appends them, rolling
// the log first when it has reached EventsRollBytes. The caller holds the
// lock.
func (p *Project) appendEventsLocked(evs []Event) ([]Event, error) {
	seq := p.lastSeq()
	path := p.EventsPath()
	if st, err := os.Stat(path); err == nil && st.Size() >= EventsRollBytes {
		if err := os.Rename(path, p.EventsRolledPath()); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	// A line a crash cut short is ended, so the next one stays whole.
	if st, err := f.Stat(); err == nil && st.Size() > 0 && !endsWithNewline(path, st.Size()) {
		buf.WriteByte('\n')
	}
	out := make([]Event, len(evs))
	for i := range evs {
		ev := evs[i]
		seq++
		ev.Seq, ev.At = seq, Stamp()
		line, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		out[i] = ev
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	return out, nil
}

func endsWithNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, size-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// lastSeq is the newest seq logged: the current log's last whole line,
// else the rolled log's, else 0.
func (p *Project) lastSeq() int64 {
	if seq, ok := lastSeqIn(p.EventsPath()); ok {
		return seq
	}
	seq, _ := lastSeqIn(p.EventsRolledPath())
	return seq
}

// LastEventSeq is the newest seq in the project's event log, 0 when it has
// none: what a waiter passes as the next --since. Read without the lock: a
// line being written is not yet whole, and the one before it counts.
func (p *Project) LastEventSeq() int64 { return p.lastSeq() }

// lastSeqIn reads path's last line that parses, from its tail.
func lastSeqIn(path string) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return 0, false
	}
	for chunk := int64(64 << 10); ; chunk *= 8 {
		off := max(0, st.Size()-chunk)
		buf := make([]byte, st.Size()-off)
		if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return 0, false
		}
		lines := bytes.Split(buf, []byte{'\n'})
		// The first piece of a chunk that does not start the file may be a
		// line's tail: never read it.
		first := 0
		if off > 0 {
			first = 1
		}
		for i := len(lines) - 1; i >= first; i-- {
			if seq, ok := lineSeq(lines[i]); ok {
				return seq, true
			}
		}
		if off == 0 {
			return 0, false
		}
	}
}

func lineSeq(line []byte) (int64, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return 0, false
	}
	var ev struct {
		Seq *int64 `json:"seq"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Seq == nil {
		return 0, false
	}
	return *ev.Seq, true
}

// EventsSince is every logged event with a seq above since, oldest first:
// the rolled log's, then the current one's. Lines that do not parse are
// skipped.
func (p *Project) EventsSince(since int64) []Event {
	cur := readEvents(p.EventsPath(), since)
	if len(cur) > 0 && cur[0].Seq == since+1 {
		return cur
	}
	return append(readEvents(p.EventsRolledPath(), since), cur...)
}

func readEvents(path string, since int64) []Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Seq <= since || ev.Event == "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// eventEnv is the notifier's environment for ev.
func eventEnv(ev Event) []string {
	exit, seq := "", ""
	if ev.Exit != nil {
		exit = strconv.Itoa(*ev.Exit)
	}
	if ev.Seq > 0 {
		seq = strconv.FormatInt(ev.Seq, 10)
	}
	return []string{
		"APE_EVENT=" + ev.Event, "APE_CHANGE=" + ev.Change, "APE_REASON=" + ev.Reason,
		"APE_MILESTONE=" + ev.Milestone, "APE_RUN=" + ev.Run, "APE_EXIT=" + exit,
		"APE_ACTION=" + ev.Action, "APE_SKILL=" + ev.Skill, "APE_SEQ=" + seq,
	}
}
