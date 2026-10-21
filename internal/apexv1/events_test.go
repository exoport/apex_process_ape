package apexv1

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// eventsOf reads every line of the project's event log, the rolled one
// first.
func eventsOf(t *testing.T, p *Project) []Event {
	t.Helper()
	return p.EventsSince(0)
}

// requireGapless fails unless evs carry seq 1..n, each once, in order.
func requireGapless(t *testing.T, evs []Event, n int) {
	t.Helper()
	require.Len(t, evs, n)
	for i, ev := range evs {
		require.EqualValues(t, i+1, ev.Seq, "seq %d is line %d", ev.Seq, i+1)
	}
}

// W3-A1: an event is a line {seq, at, event, milestone, change, run, exit,
// reason, action}, every field present, and the notifier runs after it,
// with the line's fields in its environment.
func TestEmit_LogsTheLineThenRunsTheNotifier(t *testing.T) {
	files := baseFiles()
	out := filepath.Join(t.TempDir(), "env")
	// The notifier sees the log line already written: it counts the lines.
	files["_apex/config.yaml"] = "notify: 'env | grep ^APE_ | sort > " + out + "; wc -l < _apex/state/events.jsonl >> " + out + "'\n"
	p := fixture(t, files)

	exit := 3
	ev := p.Emit(ctx(), Event{Event: EventRunEnd, Milestone: "M1", Change: "add-wave", Run: "r1", Exit: &exit, Reason: "usage_limit", Skill: "apex-apply"}, nil)
	require.EqualValues(t, 1, ev.Seq)
	require.NotEmpty(t, ev.At)

	data, err := os.ReadFile(p.EventsPath())
	require.NoError(t, err)
	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &line))
	for _, k := range []string{"seq", "at", "event", "milestone", "change", "run", "exit", "reason", "action"} {
		require.Contains(t, line, k, "every field is present")
	}
	require.Equal(t, "run-end", line["event"])
	require.EqualValues(t, 3, line["exit"])
	require.Empty(t, line["action"])

	env, err := os.ReadFile(out)
	require.NoError(t, err)
	for _, want := range []string{
		"APE_EVENT=run-end", "APE_MILESTONE=M1", "APE_CHANGE=add-wave", "APE_RUN=r1", "APE_EXIT=3",
		"APE_REASON=usage_limit", "APE_ACTION=", "APE_SKILL=apex-apply", "APE_SEQ=1",
	} {
		require.Contains(t, string(env), want+"\n")
	}
	require.True(t, strings.HasSuffix(strings.TrimSpace(string(env)), "1"), "the line was written before the notifier ran: %s", env)

	// An event with no exit logs exit null, and APE_EXIT empty.
	p.Notify(ctx(), NotifyBlock, "add-wave", "which greeting?", nil)
	evs := eventsOf(t, p)
	requireGapless(t, evs, 2)
	require.Nil(t, evs[1].Exit)
	require.Equal(t, "block", evs[1].Event)
	require.Equal(t, "which greeting?", evs[1].Reason)
	require.Contains(t, read(t, p, "_apex/state/events.jsonl"), `"exit":null`)
}

// A notifier that fails, or none, still leaves the line; a project that is
// not v1 gets no log (nor an _apex/state/ that would make it look v1).
func TestEmit_NotifierFailureKeepsTheLine(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "notify: 'exit 3'\n"
	p := fixture(t, files)
	var warn bytes.Buffer
	p.Emit(ctx(), Event{Event: EventGateGreen, Milestone: "M1"}, &warn)
	require.Contains(t, warn.String(), "notify (gate-green)")
	requireGapless(t, eventsOf(t, p), 1)

	write(t, p, "_apex/config.yaml", "project_name: x\n")
	p.Emit(ctx(), Event{Event: EventMilestoneDone, Milestone: "M1"}, &warn)
	requireGapless(t, eventsOf(t, p), 2)

	bare := &Project{Root: t.TempDir(), DevRel: DefaultDevFolder}
	bare.Emit(ctx(), Event{Event: EventStop}, &warn)
	require.NoDirExists(t, bare.StateDir())
}

// seq is strictly increasing under concurrent writers: goroutines, each
// with its own Project (its own open of the lock file), and a second
// process doing the same.
func TestEmit_SeqUnderConcurrentWriters(t *testing.T) {
	if os.Getenv("APE_EVENTS_TEST_EMITTER") == "1" {
		root := os.Getenv("APE_EVENTS_TEST_ROOT")
		n, _ := strconv.Atoi(os.Getenv("APE_EVENTS_TEST_N"))
		for i := range n {
			(&Project{Root: root, DevRel: DefaultDevFolder}).Emit(ctx(), Event{Event: EventRunEnd, Run: "proc-" + strconv.Itoa(i)}, nil)
		}
		return
	}
	p := fixture(t, baseFiles())
	const perWriter, writers = 40, 4
	child := exec.Command(os.Args[0], "-test.run=^TestEmit_SeqUnderConcurrentWriters$") //nolint:gosec,noctx // this test binary
	child.Env = append(os.Environ(), "APE_EVENTS_TEST_EMITTER=1", "APE_EVENTS_TEST_ROOT="+p.Root, "APE_EVENTS_TEST_N="+strconv.Itoa(perWriter))
	var childOut bytes.Buffer
	child.Stdout, child.Stderr = &childOut, &childOut
	require.NoError(t, child.Start())
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			q := &Project{Root: p.Root, DevRel: p.DevRel}
			for i := range perWriter {
				q.Emit(ctx(), Event{Event: EventRunEnd, Run: strconv.Itoa(w) + "-" + strconv.Itoa(i)}, nil)
			}
		})
	}
	wg.Wait()
	require.NoError(t, child.Wait(), childOut.String())

	evs := eventsOf(t, p)
	requireGapless(t, evs, perWriter*(writers+1))
	runs := map[string]bool{}
	for _, ev := range evs {
		require.False(t, runs[ev.Run], "run %s logged twice", ev.Run)
		runs[ev.Run] = true
	}
	require.EqualValues(t, perWriter*(writers+1), p.LastEventSeq())
}

// At EventsRollBytes the log becomes events.1.jsonl (one kept) and seq
// carries on; EventsSince reads across the roll with no gap.
func TestEmit_RollKeepsSeq(t *testing.T) {
	p := fixture(t, baseFiles())
	old := EventsRollBytes
	EventsRollBytes = 400
	t.Cleanup(func() { EventsRollBytes = old })

	for range 30 {
		p.Emit(ctx(), Event{Event: EventPerson, Action: "approve M2", Reason: strings.Repeat("x", 40)}, nil)
	}
	require.FileExists(t, p.EventsRolledPath())
	st, err := os.Stat(p.EventsPath())
	require.NoError(t, err)
	require.Less(t, st.Size(), EventsRollBytes+200, "the current log was rolled")
	matches, _ := filepath.Glob(filepath.Join(p.StateDir(), "events*.jsonl"))
	require.Len(t, matches, 2, "one rolled log kept")

	require.EqualValues(t, 30, p.LastEventSeq())
	since := p.EventsSince(0)
	require.NotEmpty(t, since)
	require.EqualValues(t, 30, since[len(since)-1].Seq)
	for i := 1; i < len(since); i++ {
		require.Equal(t, since[i-1].Seq+1, since[i].Seq, "no gap across the roll")
	}
	got := p.EventsSince(27)
	require.Len(t, got, 3)
	require.EqualValues(t, 28, got[0].Seq)

	// Just after a roll the current log is gone: seq continues from the
	// rolled one.
	require.NoError(t, os.Rename(p.EventsPath(), p.EventsRolledPath()))
	require.EqualValues(t, 30, p.LastEventSeq())
	ev := p.Emit(ctx(), Event{Event: EventStop}, nil)
	require.EqualValues(t, 31, ev.Seq)
}

// A line a crash cut short is skipped, and the next one starts on a line
// of its own.
func TestEmit_TornLineIsSkipped(t *testing.T) {
	p := fixture(t, baseFiles())
	p.Emit(ctx(), Event{Event: EventStop}, nil)
	f, err := os.OpenFile(p.EventsPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"seq":2,"at":"2026`)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.EqualValues(t, 1, p.LastEventSeq())
	ev := p.Emit(ctx(), Event{Event: EventBlock}, nil)
	require.EqualValues(t, 2, ev.Seq)
	requireGapless(t, eventsOf(t, p), 2)
}
