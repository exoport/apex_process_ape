package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Pull requests through main's merge queue (v1.5 stage 1a, D702): in a
// project with coordination.github, a milestone's close PR and the record
// PRs go into main through its merge queue; changes and take-ins land on
// milestone/<M> directly (milestonepush.go).

// PRBranchPrefix starts the branch a change's squash is pushed to.
const PRBranchPrefix = "apex/"

// Integration codes.
const (
	// CodeIntegrationPush: a push to the coordinated repository failed.
	CodeIntegrationPush = "integration.push-failed"
	// CodeIntegrationPR: opening or enqueuing a pull request into main
	// failed.
	CodeIntegrationPR = "integration.pr-failed"
	// CodeIntegrationEjections: main's queue ejected a close PR as many
	// times as limits.attempts allows.
	CodeIntegrationEjections = "integration.ejections-exceeded"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeIntegrationPush, 1, ActionRetry, "a coordinated archive, take-in, close or record (coordination.github) could not push to the remote for a reason other than the branch having moved (that is milestone.moved): nothing landed — an archive or take-in is rolled back, the base unchanged — fix the remote or its credentials, then run the command again (v1.5 stage 1a, D702)"},
		CodeDoc{CodeIntegrationEjections, 1, ActionPerson, "a coordinated close found its close PR ejected from main's merge queue (the milestone gate red on the merge with main, or a conflict) for the limits.attempts-th time — a person looks at why the gate is red on the merge with main (v1.5 stage 1a, section 5.3)"},
		CodeDoc{CodeIntegrationPR, 1, ActionRetry, "a coordinated close or record pushed its apex/* head but could not open its pull request into main, or enqueue it in main's merge queue (GitHub's message is given; a rate limit says when it lifts) — run the command again, which reuses the open pull request"},
	)
}

// PRRef is a pull request as ape records it.
type PRRef struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	URL    string `json:"url"`
}

// PRState is a pull request's state as archive reads it.
type PRState struct {
	Merged, Closed bool
	// Queue is the merge queue entry's state, "" when not in a queue.
	Queue       string
	MergeCommit string
}

// Integrator is the coordinated repository's API, as archive uses it
// (internal/coord/github behind it).
type Integrator interface {
	OpenPR(ctx context.Context, head, base, title, body string) (PRRef, error)
	Enqueue(ctx context.Context, pr PRRef) (queue string, err error)
	PRState(ctx context.Context, number int) (PRState, error)
}

// NewIntegrator, when set, is the coordinated project's Integrator; ape
// sets it (apecmd), tests set a fake. Unset, a coordinated archive
// refuses: nothing can open its pull request.
var NewIntegrator func(p *Project) (Integrator, error)

// Integration states.
const (
	IntegrationQueued  = "queued"
	IntegrationMerged  = "merged"
	IntegrationEjected = "ejected"
)

// Integration is one change's pull request, in this checkout's own state
// (_apex/state/coord/prs.json): never in git, which the PR itself is.
type Integration struct {
	Change string `json:"change"`
	// Kind is KindClose or KindRecord.
	Kind string `json:"kind,omitempty"`
	// Head is the PR's head branch (milestone/<M>, apex/record-…); Base
	// the branch it merges into (main).
	Head string `json:"head"`
	Base string `json:"base"`
	// Commit is the squash pushed to Head; BaseSHA the base tip it sits on.
	Commit  string `json:"commit"`
	BaseSHA string `json:"base_sha"`
	PR      PRRef  `json:"pr"`
	// Attempts counts the enqueues: the first, and one per re-enqueue
	// after an ejection.
	Attempts int    `json:"attempts"`
	State    string `json:"state"`
	At       string `json:"at"`
}

func (p *Project) integrationsPath() string { return runlog.CoordPRsPath(p.Root) }

func (p *Project) loadIntegrations() (map[string]Integration, error) {
	all := map[string]Integration{}
	data, err := os.ReadFile(p.integrationsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", p.integrationsPath(), err)
	}
	return all, nil
}

// LoadIntegration is id's pull request record, nil when it has none.
func (p *Project) LoadIntegration(id string) *Integration {
	all, err := p.loadIntegrations()
	if err != nil {
		return nil
	}
	if in, ok := all[id]; ok {
		return &in
	}
	return nil
}

func (p *Project) saveIntegration(in Integration) error {
	all, err := p.loadIntegrations()
	if err != nil {
		return err
	}
	in.At = now().UTC().Format("2006-01-02T15:04:05Z")
	all[in.Change] = in
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.integrationsPath()), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(p.integrationsPath(), append(data, '\n'))
}

// integrator is the coordinated project's Integrator, or the refusal.
func (p *Project) integrator() (Integrator, error) {
	if NewIntegrator == nil {
		return nil, &FailError{Code: CodeIntegrationPR, Msg: "this project integrates through GitHub (coordination.github), and this ape has no GitHub client wired — run the archive with ape itself"}
	}
	return NewIntegrator(p)
}

// QueuePoll is how often a close waiting on main's queue reads its PR
// when no event woke it (a member's NATS events wake it sooner: CI
// publishes close-merged and ejected, OQ10).
var QueuePoll = time.Minute

// WaitQueue waits for the next look at a queued PR: a wake, or QueuePoll.
// false when ctx ended first.
func WaitQueue(ctx context.Context, wake <-chan struct{}) bool {
	t := time.NewTimer(QueuePoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
	case <-t.C:
	}
	return true
}
