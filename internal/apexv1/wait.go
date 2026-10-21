package apexv1

import (
	"fmt"
	"slices"
	"strings"
)

// W3-A3: what `ape status --wait --until <conditions>` waits for. Each
// condition holds on the recorded state (its level), and some events make
// one hold (its edge): the waiter checks the level first, then reads the
// event log for an event after --since.
//
//	person   a person act is awaited (PersonActions)  · edge: person, block
//	stopped  no run of the project is live            · edge: run-end, run-lost, stop, then the level
//	done     every roadmap milestone is done          · edge: milestone-done, then the level
//	blocked  a change is blocked                      · edge: block
//	event    an event after --since                   · edge: any event
const (
	UntilPerson  = "person"
	UntilStopped = "stopped"
	UntilDone    = "done"
	UntilBlocked = "blocked"
	UntilEvent   = "event"
)

// UntilConditions are the conditions, in the order help lists them.
var UntilConditions = []string{UntilPerson, UntilStopped, UntilDone, UntilBlocked, UntilEvent}

// ParseUntil reads --until's comma-separated conditions (any of them is
// enough), each once, in the order given.
func ParseUntil(s string) ([]string, error) {
	var out []string
	for c := range strings.SplitSeq(s, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !slices.Contains(UntilConditions, c) {
			return nil, fmt.Errorf("--until %q: %q is not a condition — one or more of %s, comma-separated", s, c, strings.Join(UntilConditions, ", "))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--until names no condition — one or more of %s, comma-separated", strings.Join(UntilConditions, ", "))
	}
	return out, nil
}

// WaitLevel is the state a wait judges a level by: the status report, the
// person acts it waits on, and the live runs (the caller's: detached runs,
// long-run locks, ape-launched sessions).
type WaitLevel struct {
	Report  *StatusReport
	Persons []PersonAction
	Running []string
}

// Holds reports whether cond holds on the level. event has no level: it
// holds only on an event (EventMeets).
func (l WaitLevel) Holds(cond string) bool {
	switch cond {
	case UntilPerson:
		return len(l.Persons) > 0
	case UntilStopped:
		return len(l.Running) == 0
	case UntilDone:
		return l.Report != nil && AllMilestonesDone(l.Report)
	case UntilBlocked:
		if l.Report == nil {
			return false
		}
		for i := range l.Report.Changes {
			if l.Report.Changes[i].Status == StatusBlocked {
				return true
			}
		}
	}
	return false
}

// AllMilestonesDone reports a roadmap with at least one milestone, every
// one of them done. A roadmap with none is not done: nothing is planned.
func AllMilestonesDone(r *StatusReport) bool {
	if len(r.Milestones) == 0 {
		return false
	}
	for _, m := range r.Milestones {
		if m.Status != MilestoneDone {
			return false
		}
	}
	return true
}

// EventMeets reports whether ev bears on cond: for person, blocked and
// event the event alone makes it hold (it happened after --since); for
// stopped and done it is the cue to judge the level again (EventNeedsLevel).
func EventMeets(cond string, ev Event) bool {
	switch cond {
	case UntilPerson:
		return ev.Event == EventPerson || ev.Event == EventBlock
	case UntilStopped:
		return ev.Event == EventRunEnd || ev.Event == EventRunLost || ev.Event == EventStop
	case UntilDone:
		return ev.Event == EventMilestoneDone
	case UntilBlocked:
		return ev.Event == EventBlock
	case UntilEvent:
		return true
	}
	return false
}

// EventNeedsLevel reports a condition an event cues but does not decide:
// a run ending is not every run ended, and a milestone done not the last.
func EventNeedsLevel(cond string) bool { return cond == UntilStopped || cond == UntilDone }
