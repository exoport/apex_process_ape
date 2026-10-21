package apexv1

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// H20 (D47 Q7): an optional notifier. _apex/config.yaml's `notify:` is a
// shell command run on every event ape logs (W3, events.go): an ape task
// run stopping on a limit or ending, a change becoming blocked, the project
// starting to wait on a person act, a gate turning green, a milestone
// closed, a detached run lost — so an operator away from `ape status`
// hears of it. It runs after the event's line is in _apex/state/
// events.jsonl. Best effort by design: it runs under a short timeout, its
// output is dropped, and its failure is one line on stderr, never a
// changed outcome.

// The notifier's first events (APE_EVENT), H20's.
const (
	NotifyStop  = "stop"
	NotifyBlock = "block"
)

// NotifyTimeout bounds one notifier run.
var NotifyTimeout = 10 * time.Second

// Notify logs event and runs the project's notifier for it (Emit), with
// the change and the reason only. warn gets the one line a failure is
// worth (nil: stderr).
func (p *Project) Notify(ctx context.Context, event, change, reason string, warn io.Writer) {
	p.Emit(ctx, Event{Event: event, Change: change, Reason: reason}, warn)
}

// runNotifier runs the project's notifier for ev, if it has one, with
// ev's fields in its environment (eventEnv).
func (p *Project) runNotifier(ctx context.Context, ev Event, warn io.Writer) {
	cfg, err := LoadConfig(p.Root)
	if err != nil || strings.TrimSpace(cfg.Notify) == "" {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), NotifyTimeout)
	defer cancel()
	c := exec.CommandContext(nctx, "sh", "-c", cfg.Notify) //nolint:gosec // the notifier is a shell command the project's _apex/config.yaml names, by design
	c.Dir = p.Root
	c.Env = append(os.Environ(), eventEnv(ev)...)
	// A notifier that leaves a child holding the pipes must not hold ape.
	c.WaitDelay = time.Second
	if err := c.Run(); err != nil {
		fmt.Fprintf(warn, "notify (%s): %v — ignored\n", ev.Event, err)
	}
}

// ChangeStatus is a change's status and its blocked reason, read where
// commands read it (its branch when HEAD is elsewhere); "" when the change
// does not load.
func (p *Project) ChangeStatus(ctx context.Context, id string) (status, reason string) {
	ci, err := p.requireChange(ctx, id)
	if err != nil || ci.State == nil {
		return "", ""
	}
	if ci.State.BlockedReason != nil {
		reason = *ci.State.BlockedReason
	}
	return ci.State.Status, reason
}

// ChangeIDIn returns the first word of args when it names one of the
// project's changes: the change an `ape task` dispatch works on.
func (p *Project) ChangeIDIn(ctx context.Context, args string) string {
	fields := strings.Fields(args)
	if len(fields) == 0 || !ChangeIDRe.MatchString(fields[0]) {
		return ""
	}
	if status, _ := p.ChangeStatus(ctx, fields[0]); status == "" {
		return ""
	}
	return fields[0]
}
