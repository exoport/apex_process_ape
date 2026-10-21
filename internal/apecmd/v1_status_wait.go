package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// W3-A3: `ape status --wait <d> --until <conditions> [--since <seq>]`.
// One call waits at most --wait (default 9m, at most 9m30s: one call stays
// under Claude Code's 10-minute Bash cap), so a caller re-arms it on 75 with
// the seq it printed. It reads only ape's records — the event log, the run
// handles and locks, the session registry — and never matches processes.

const (
	// statusWaitPoll is how often a wait reads the event log and, for
	// stopped, the runs; statusWaitLevelEvery how often it judges every
	// level again with no event to cue it (a state changed by hand logs
	// none). Test seams.
	defaultStatusWaitPoll  = time.Second
	defaultStatusWaitLevel = 15 * time.Second
)

var (
	statusWaitPoll       = defaultStatusWaitPoll
	statusWaitLevelEvery = defaultStatusWaitLevel
)

// statusWaitHelp is the paragraph status's help gets for the wait.
const statusWaitHelp = `
Waiting (W3): --until <conditions> waits, at most --wait (default 9m, at most 9m30s;
usage.wait), until one of the comma-separated conditions holds:

  person   the project waits on a person act: a proposed milestone ("approve M2"), a tweak
           awaiting approval ("approve <id>"), a blocked change ("answer <id>: <answer>"),
           a human-review task no delegate may take now ("accept T2 of <id>"), an
           unaccepted config change ("accept the config")
  stopped  no run of the project is live: no detached run going (a run whose supervisor
           is gone counts as stopped, and is logged run-lost), no long-run lock held, no
           session ape launched running here
  done     every milestone of the roadmap is done (a roadmap with none is not)
  blocked  a change is blocked
  event    any event is logged after --since

Level first: when a condition already holds, the call returns at once. Then the events: an
event after --since that makes a condition hold returns it — person on a person or block
event, blocked on block, event on any; stopped and done on run-end, run-lost, stop and
milestone-done, once their level holds — so nothing logged between two calls is missed.
Without --since, only events after the call started count. ape status (with or without
--until), ape run wait and ape run status log run-lost for a detached run whose supervisor
is gone without recording an exit, once per run. The log is
_apex/state/events.jsonl, one line per event: {seq, at, event, milestone, change, run, exit,
reason, action} (skill too, for an ape task run), seq strictly increasing; it rolls at 10 MB
into events.1.jsonl (one kept), seq carrying on. The events: person (action is the phrase
the person types), gate-green, milestone-done, run-end (exit is the run's exit code),
run-lost, stop and block. _apex/config.yaml's notify: runs after each line is written, with
APE_EVENT, APE_SEQ, APE_MILESTONE, APE_CHANGE, APE_RUN, APE_EXIT, APE_SKILL, APE_REASON and
APE_ACTION in its environment.

Every exit prints "seq: <n>" last (JSON "seq", and wait.last_seq): the --since to pass next.
It is the returned event's seq when an event made the condition hold — the events after it
are the next call's — else the newest seq logged. Exit 0: a condition holds; the status is
printed with "Waited: <condition> …" and the event. Exit 75: --wait ran out; run the same
command again with --since <n>. --json prints the status object with "seq" and "wait":
{until, since (null without --since), condition (null on 75), how ("level", "edge" or
"timeout"), event (the line, or null), last_seq, person_actions [{kind, ref, task,
milestone, change, action, reason}], running (the live runs' ids), waited_secs}. From
another machine, run ape by its absolute path:
  ssh host 'cd /path/to/project && /abs/path/to/ape status --wait 9m --until person,stopped --since 41 --json'`

// statusWaitReport is the wait object of `ape status --until … --json`.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract
type statusWaitReport struct {
	Until         []string              `json:"until"`
	Since         *int64                `json:"since"`
	Condition     *string               `json:"condition"`
	How           string                `json:"how"`
	Event         *apexv1.Event         `json:"event"`
	LastSeq       int64                 `json:"last_seq"`
	PersonActions []apexv1.PersonAction `json:"person_actions"`
	Running       []string              `json:"running"`
	WaitedSecs    float64               `json:"waited_secs"`
}

// statusWithWait is `ape status --until … --json`.
type statusWithWait struct {
	*apexv1.StatusReport
	Seq  int64            `json:"seq"`
	Wait statusWaitReport `json:"wait"`
}

// The ways a wait ends (wait.how).
const (
	waitLevel   = "level"
	waitEdge    = "edge"
	waitTimeout = "timeout"
)

// statusWaitOptions are a wait's flags, validated.
type statusWaitOptions struct {
	until []string
	since *int64
	wait  time.Duration
	brief bool
}

// statusWaitOpts validates the wait flags; nil when the call does not wait.
func statusWaitOpts(cmd *cobra.Command, until string, since int64, wait time.Duration, brief, board bool) (*statusWaitOptions, error) {
	fl := cmd.Flags()
	if !fl.Changed("until") {
		if fl.Changed("wait") || fl.Changed("since") {
			return nil, &apexv1.UsageError{Code: "usage.until", Msg: "--wait and --since are the wait's: give --until <conditions> (one or more of " +
				strings.Join(apexv1.UntilConditions, ", ") + ")"}
		}
		return nil, nil //nolint:nilnil // not a wait
	}
	conds, err := apexv1.ParseUntil(until)
	if err != nil {
		return nil, &apexv1.UsageError{Code: "usage.until", Msg: err.Error()}
	}
	if board {
		return nil, &apexv1.UsageError{Code: "usage.until", Msg: "--board refreshes the tab now and does not wait: run it without --until"}
	}
	if err := validateWait(wait); err != nil {
		return nil, err
	}
	o := &statusWaitOptions{until: conds, wait: wait, brief: brief}
	if fl.Changed("since") {
		if since < 0 {
			return nil, &apexv1.UsageError{Code: "usage.until", Msg: fmt.Sprintf("--since %d: a seq is 0 or more (the seq a wait printed)", since)}
		}
		o.since = &since
	}
	return o, nil
}

// waitLevelOf reads the state a level is judged by. running is read only
// when stopped is waited for: it is the one part that costs a look at
// every run.
func waitLevelOf(ctx context.Context, p *apexv1.Project, withRuns bool) apexv1.WaitLevel {
	r := p.Status()
	l := apexv1.WaitLevel{Report: r, Persons: p.PersonActionsOf(ctx, r), Running: []string{}}
	if withRuns {
		l.Running = liveRuns(ctx, p, r)
	}
	return l
}

// liveRuns are the project's runs that are live now, by id: detached runs
// going (a lost one is logged and left out), long runs holding their lock,
// and sessions ape launched that run in the project.
func liveRuns(ctx context.Context, p *apexv1.Project, r *apexv1.StatusReport) []string {
	live := detachedRuns(ctx, p.Root)
	if live == nil {
		live = []string{}
	}
	if apexv1.AnyRunLive(p.Root) {
		live = append(live, "long-run lock")
	}
	sessions := p.LiveSessions()
	if r != nil {
		sessions = r.Sessions
	}
	for _, s := range sessions {
		live = append(live, s.RunID)
	}
	return live
}

// firstHolding is the first of conds that holds on l (event never does).
func firstHolding(conds []string, l apexv1.WaitLevel) string {
	for _, c := range conds {
		if l.Holds(c) {
			return c
		}
	}
	return ""
}

// meets is the first of conds ev decides on its own, or that it cues and
// whose level then holds (judged by level, called at most once).
func meets(conds []string, ev apexv1.Event, level func() apexv1.WaitLevel) string {
	var l *apexv1.WaitLevel
	for _, c := range conds {
		if !apexv1.EventMeets(c, ev) {
			continue
		}
		if !apexv1.EventNeedsLevel(c) {
			return c
		}
		if l == nil {
			cur := level()
			l = &cur
		}
		if l.Holds(c) {
			return c
		}
	}
	return ""
}

// newestMeeting is the newest of evs that bears on cond, nil when none.
func newestMeeting(cond string, evs []apexv1.Event) *apexv1.Event {
	for i := len(evs) - 1; i >= 0; i-- {
		if apexv1.EventMeets(cond, evs[i]) {
			ev := evs[i]
			return &ev
		}
	}
	return nil
}

// runStatusWait is the wait: level first, then the events after --since,
// then the log polled until --wait runs out (exit 75).
func runStatusWait(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, o *statusWaitOptions) error {
	ctx := cmd.Context()
	start := time.Now()
	deadline := start.Add(o.wait)
	withRuns := false
	for _, c := range o.until {
		withRuns = withRuns || c == apexv1.UntilStopped
	}
	cursor := p.LastEventSeq()
	if o.since != nil {
		cursor = *o.since
		// A log that was removed starts again at 1: its events are all new.
		if cursor > p.LastEventSeq() {
			cursor = 0
		}
	}
	since := cursor
	// A level is judged after the seq it is handed on with is read: an
	// event logged meanwhile is the next call's.
	levelSeq := p.LastEventSeq()
	level := waitLevelOf(ctx, p, withRuns)
	finish := func(cond, how string, ev *apexv1.Event, last int64) error {
		return printStatusWait(cmd, f, p, o, level, cond, how, ev, last, time.Since(start))
	}

	// Level first.
	if cond := firstHolding(o.until, level); cond != "" {
		return finish(cond, waitLevel, newestMeeting(cond, p.EventsSince(since)), max(levelSeq, cursor))
	}
	// The events since --since: what happened between two calls.
	lastLevel := time.Now()
	judge := func() apexv1.WaitLevel {
		levelSeq = p.LastEventSeq()
		level, lastLevel = waitLevelOf(ctx, p, withRuns), time.Now()
		return level
	}
	scan := func(evs []apexv1.Event) (bool, error) {
		for i := range evs {
			ev := evs[i]
			cursor = ev.Seq
			if cond := meets(o.until, ev, judge); cond != "" {
				if cond != apexv1.UntilEvent && !apexv1.EventNeedsLevel(cond) {
					judge() // the status printed is now's
				}
				return true, finish(cond, waitEdge, &ev, ev.Seq)
			}
		}
		return false, nil
	}
	// readNew scans what was logged since the last scan.
	readNew := func() (bool, error) {
		last := p.LastEventSeq()
		if last == cursor {
			return false, nil
		}
		if last < cursor {
			cursor = 0 // the log was removed and started again
		}
		return scan(p.EventsSince(cursor))
	}
	if done, err := scan(p.EventsSince(cursor)); done {
		return err
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return finish("", waitTimeout, nil, cursor)
		case <-time.After(min(statusWaitPoll, time.Until(deadline))):
		}
		if done, err := readNew(); done {
			return err
		}
		// stopped is also read from the runs themselves: a supervisor
		// killed logs nothing until someone looks (detachedRuns does).
		recheck := time.Since(lastLevel) >= statusWaitLevelEvery
		if withRuns && !recheck {
			if len(liveRuns(ctx, p, nil)) == 0 {
				recheck = true
			}
		}
		if recheck {
			judge()
			if cond := firstHolding(o.until, level); cond != "" {
				return finish(cond, waitLevel, newestMeeting(cond, p.EventsSince(since)), max(levelSeq, cursor))
			}
		}
	}
	// Out of time: one last read, so the seq handed on is one every event
	// up to which was read.
	if done, err := readNew(); done {
		return err
	}
	judge()
	return finish("", waitTimeout, nil, cursor)
}

// printStatusWait prints how the wait ended — the status, the condition
// and its event, and the seq to pass next — and returns 0, or 75 when no
// condition held.
func printStatusWait(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, o *statusWaitOptions, l apexv1.WaitLevel,
	cond, how string, ev *apexv1.Event, last int64, waited time.Duration,
) error {
	rep := statusWaitReport{
		Until: o.until, Since: o.since, How: how, Event: ev, LastSeq: last,
		PersonActions: l.Persons, Running: l.Running, WaitedSecs: waited.Round(time.Millisecond).Seconds(),
	}
	if cond != "" {
		rep.Condition = &cond
	}
	r := l.Report
	if r == nil {
		r = p.Status()
	}
	out := cmd.OutOrStdout()
	err := v1Emit(out, f, statusWithWait{StatusReport: r, Seq: last, Wait: rep}, func(w io.Writer) {
		if o.brief {
			fmt.Fprint(w, r.Brief())
		} else {
			fmt.Fprint(w, statusLong(r))
		}
		fmt.Fprintln(w, waitLine(o, rep, waited))
		fmt.Fprintf(w, "seq: %d\n", last)
	})
	if err != nil {
		return err
	}
	if cond == "" {
		return reportedErr(exitCodeStillRunning, errors.New("no condition held within --wait"))
	}
	return nil
}

// waitLine says, in one line, how the wait ended.
func waitLine(o *statusWaitOptions, rep statusWaitReport, waited time.Duration) string {
	if rep.Condition == nil {
		return fmt.Sprintf("\nWaited %s: none of %s held — run the same command again with --since %d",
			waited.Round(time.Second), strings.Join(o.until, ","), rep.LastSeq)
	}
	line := "\nWaited: " + *rep.Condition + " holds (" + rep.How + ")"
	if ev := rep.Event; ev != nil {
		line += fmt.Sprintf(" — event %d %s", ev.Seq, ev.Event)
		for _, part := range []string{ev.Milestone, ev.Change, ev.Run, ev.Action, ev.Reason} {
			if part != "" {
				line += " · " + part
			}
		}
	}
	if *rep.Condition == apexv1.UntilPerson {
		for _, a := range rep.PersonActions {
			line += "\n  " + a.Action + " — " + a.Reason
		}
	}
	return line
}
