package apexv1

import (
	"context"
	"strings"
)

// The assessor's triggers (v1.5 §3.2, PLAN-V15-ASSESSOR step 1): published
// on NATS only, kind project, like OQ24's events — never in events.jsonl,
// so the project's notifier and its waiters see nothing new.
const (
	EventPlanApproved   = "plan-approved"
	EventChangeArchived = "change-archived"
	EventGateRed        = "gate-red"
)

// Trigger is one of them: the milestone, and what the act was about.
type Trigger struct {
	Event     string   `json:"event"`
	Milestone string   `json:"milestone"`
	Change    string   `json:"change,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	// Gate is an approved milestone's Gate: line (plan-approved).
	Gate string `json:"gate,omitempty"`
	// Key is the act's own id within its kind — the approval's at, the
	// change's id, the gate run's sha@at — so the message id
	// <project>/<checkout>/<event>/<M>/<Key> is the same however often
	// the act's trigger is published, and a re-publish is stored once.
	Key string `json:"key"`
}

// TriggerSink, when set, publishes a trigger (internal/coord/member's
// PublishTrigger, for a project in an ensemble). Best effort: it never
// changes an outcome.
var TriggerSink func(ctx context.Context, p *Project, t Trigger)

func (p *Project) emitTrigger(ctx context.Context, t Trigger) {
	if TriggerSink != nil && t.Milestone != "" && t.Milestone != MilestoneNone {
		TriggerSink(context.WithoutCancel(ctx), p, t)
	}
}

// changedPaths are the paths a commit changed against its first parent.
func (p *Project) changedPaths(ctx context.Context, commit string) []string {
	out, err := git(ctx, p.Root, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}
