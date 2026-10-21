package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// W3 Part 2: the `person` event. What waits on a person is read from the
// recorded state, not from the verb that made it: a proposed milestone is
// apex-plan's roadmap edit, which no ape verb makes. So PersonActions
// derives the set of person acts the project waits on from the data `ape
// status` reports, and EmitPersonDelta logs one `person` event for each act
// not in the set it saved last time (_apex/state/person-actions.json,
// ignored), then saves the new set. It runs after every v1 command that
// changes what status reports, and in ape's git pre-commit hook, which
// every commit runs (apex-plan's own included). An act that leaves the set
// and comes back is new again.

// PersonAction is one person act the project waits on.
type PersonAction struct {
	// Kind is the act (personact.go's Act* names); Ref its target (M<n>,
	// a change id, or "config"); Task the human-review task, for an
	// accept.
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Task string `json:"task,omitempty"`
	// Milestone and Change are what the event names.
	Milestone string `json:"milestone,omitempty"`
	Change    string `json:"change,omitempty"`
	// Action is the phrase the person types for it (PersonActs), with its
	// target filled in: "approve M3", "accept T2 of add-wave". An answer's
	// text is the person's: "answer add-wave: <answer>".
	Action string `json:"action"`
	// Reason says why the project waits on it, one line.
	Reason string `json:"reason"`
}

func (a PersonAction) key() string { return a.Kind + "\x00" + a.Ref + "\x00" + a.Task }

// PersonActions is every person act the project waits on now.
func (p *Project) PersonActions(ctx context.Context) []PersonAction {
	return p.PersonActionsOf(ctx, p.Status())
}

// PersonActionsOf is PersonActions from a status report the caller has
// already built, in a fixed order: proposed milestones, tweaks awaiting
// approval, blocked changes, human-review tasks a person accepts, the
// config.
//
// A human-review task a delegate may take now (changes[].delegable) is not
// a person's yet: `next` gives it to the delegate first (D75, D152).
func (p *Project) PersonActionsOf(ctx context.Context, r *StatusReport) []PersonAction {
	out := []PersonAction{}
	if r == nil || !r.Initialized {
		return out
	}
	for _, m := range r.Milestones {
		if m.Status == MilestoneProposed {
			out = append(out, PersonAction{
				Kind: ActMilestoneApprove, Ref: m.ID, Milestone: m.ID, Action: "approve " + m.ID,
				Reason: m.ID + " (" + m.Title + ") is proposed: nothing of it runs until a person approves its plan and its red gate",
			})
		}
	}
	milestoneOf := func(c *ChangeStatus) string {
		if c.Milestone == MilestoneNone {
			return ""
		}
		return c.Milestone
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Lane == LaneTweak && c.Approved == nil && c.Status == StatusDraft {
			out = append(out, PersonAction{
				Kind: ActTweakApprove, Ref: c.ID, Change: c.ID, Action: "approve " + c.ID,
				Reason: "tweak " + c.ID + " awaits a person's approval before it runs",
			})
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; c.Status == StatusBlocked {
			out = append(out, PersonAction{
				Kind: ActAnswer, Ref: c.ID, Milestone: milestoneOf(c), Change: c.ID, Action: "answer " + c.ID + ": <answer>",
				Reason: c.ID + " is blocked: " + c.BlockedReason,
			})
		}
	}
	for i := range r.Changes {
		c := &r.Changes[i]
		delegable := map[string]bool{}
		for _, d := range c.Delegable {
			delegable[d.Task] = true
		}
		for _, task := range c.AwaitingHuman {
			if delegable[task] {
				continue
			}
			out = append(out, PersonAction{
				Kind: ActAcceptTask, Ref: c.ID, Task: task, Milestone: milestoneOf(c), Change: c.ID, Action: "accept " + task + " of " + c.ID,
				Reason: c.ID + "'s human-review task " + task + " awaits a person's acceptance",
			})
		}
	}
	if epoch := p.v1Epoch(ctx, "HEAD"); epoch != "" {
		if fs := p.configFindings(ctx, epoch); len(fs) > 0 {
			out = append(out, PersonAction{
				Kind: ActAcceptConfig, Ref: "config", Action: "accept the config",
				Reason: "config.changed: " + fs[0].Message,
			})
		}
	}
	return out
}

// personActionsRecord is _apex/state/person-actions.json: the set the last
// delta saw.
type personActionsRecord struct {
	At      string         `json:"at"`
	Actions []PersonAction `json:"actions"`
}

// PersonActionsPath is _apex/state/person-actions.json.
func (p *Project) PersonActionsPath() string {
	return filepath.Join(p.StateDir(), "person-actions.json")
}

func (p *Project) loadPersonActions() (map[string]bool, bool) {
	data, err := os.ReadFile(p.PersonActionsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, true
	}
	var rec personActionsRecord
	if err != nil || json.Unmarshal(data, &rec) != nil {
		return map[string]bool{}, false
	}
	seen := map[string]bool{}
	for _, a := range rec.Actions {
		seen[a.key()] = true
	}
	return seen, true
}

// EmitPersonDelta logs a `person` event for every act in the project's
// person acts (from r, which the caller has built) that the saved set does
// not hold, saves the new set, then runs the notifier for each. It returns
// the events logged. warn gets the one line a failure is worth.
func (p *Project) EmitPersonDelta(ctx context.Context, r *StatusReport, warn io.Writer) []Event {
	if !p.eventsOn() {
		return nil
	}
	if warn == nil {
		warn = os.Stderr
	}
	acts := p.PersonActionsOf(ctx, r)
	var logged []Event
	err := p.withEventsLock(func() error {
		prev, _ := p.loadPersonActions()
		var evs []Event
		for _, a := range acts {
			if !prev[a.key()] {
				evs = append(evs, Event{Event: EventPerson, Milestone: a.Milestone, Change: a.Change, Action: a.Action, Reason: a.Reason})
			}
		}
		data, err := json.MarshalIndent(personActionsRecord{At: Stamp(), Actions: acts}, "", "  ")
		if err != nil {
			return err
		}
		if len(evs) > 0 {
			if logged, err = p.appendEventsLocked(evs); err != nil {
				return err
			}
		}
		return writeFileAtomic(p.PersonActionsPath(), string(data)+"\n")
	})
	if err != nil {
		_, _ = io.WriteString(warn, "events (person): "+err.Error()+" — not logged\n")
	}
	for _, ev := range logged {
		p.runNotifier(ctx, ev, warn)
	}
	return logged
}
