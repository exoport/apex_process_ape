package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
)

// W3 Part 2, the run events (apexv1/events.go has the log):
//
//   - run-end, for every `ape task` run in an APEX v1 project, attached or
//     detached. An attached run logs it itself, after its manifest's end;
//     a detached one's supervisor logs it when it records the exit, as
//     several commands end through os.Exit, which nothing in-process
//     observes — so the run a supervisor started (APE_DETACHED_ID) does
//     not log it too.
//   - run-lost, once per detached run, by the first ape command that finds
//     its supervisor gone without recording an exit (ape run wait|status,
//     ape status --wait): the one that creates <id>.lost beside the
//     handle, exclusively, logs it.

// emitTaskRunEnd logs run-end for an attached `ape task` run that ended
// with exit (the manifest's run id; the termination kind as the reason).
func emitTaskRunEnd(ctx context.Context, projectRoot, skill, args, runID string, exit int, runErr error) {
	if os.Getenv(envDetachedID) != "" {
		return // the supervisor logs it, with the code the process exits with
	}
	p, ok := v1ProjectAt(projectRoot)
	if !ok {
		return
	}
	ctx = context.WithoutCancel(ctx)
	milestone, change := p.EventScope(ctx, args)
	p.Emit(ctx, apexv1.Event{
		Event: apexv1.EventRunEnd, Milestone: milestone, Change: change, Run: runID, Exit: &exit,
		Reason: pipeline.TerminationKind(runErr), Skill: skill,
	}, os.Stderr)
}

// handleRoot is the project a detached run's handle belongs to: the one
// --detach recorded, else the one around the directory it ran in.
func handleRoot(h *detachedHandle) string {
	if h.Root != "" {
		return h.Root
	}
	return apexv1.FindRoot(h.Dir)
}

// handleTask is the skill and --args of a detached `ape task` run, from
// its argv; "" for any other command.
func handleTask(argv []string) (skill, args string) {
	if len(argv) < 2 || argv[0] != "task" {
		return "", ""
	}
	skill = argv[1]
	for i := 2; i < len(argv); i++ {
		if a := argv[i]; a == "--args" && i+1 < len(argv) {
			args = argv[i+1]
		} else if v, ok := strings.CutPrefix(a, "--args="); ok {
			args = v
		}
	}
	return skill, args
}

// handleEvent is ev filled in from h: the scope its --args name, its run
// and skill. The run is the run's own id (its manifest's run_id, as an
// attached run's events and ape status name it), else — a run that never
// wrote one — the handle's id.
func handleEvent(ctx context.Context, p *apexv1.Project, h *detachedHandle, ev apexv1.Event) apexv1.Event {
	skill, args := handleTask(h.Argv)
	ev.Milestone, ev.Change = p.EventScope(ctx, args)
	ev.Run, ev.Skill = h.ID, skill
	if cur, err := readHandle(handlePath(p.Root, h.ID)); err == nil && cur.RunID != "" {
		ev.Run = cur.RunID
	} else if h.RunID != "" {
		ev.Run = h.RunID
	}
	return ev
}

// emitDetachedRunEnd is the supervisor's run-end, once it has recorded
// the exit: the stop reason it recorded, else the run's termination kind.
func emitDetachedRunEnd(ctx context.Context, h *detachedHandle, code int) {
	p, ok := v1ProjectAt(handleRoot(h))
	if !ok {
		return
	}
	// The run has ended: its event outlives a cancelled supervisor.
	ctx = context.WithoutCancel(ctx)
	reason := h.StopReason
	if reason == "" {
		if cur, err := readHandle(handlePath(p.Root, h.ID)); err == nil && cur.RunDir != "" {
			if m, err := pipeline.LoadManifest(cur.RunDir); err == nil && m.Termination != nil {
				reason = m.Termination.Kind
			}
		}
	}
	p.Emit(ctx, handleEvent(ctx, p, h, apexv1.Event{Event: apexv1.EventRunEnd, Exit: &code, Reason: reason}), os.Stderr)
}

// lostMarkerPath marks a detached run whose loss is logged.
func lostMarkerPath(projectRoot, id string) string {
	return strings.TrimSuffix(handlePath(projectRoot, id), ".json") + ".lost"
}

// noteRunLost logs run-lost for h, whose supervisor is gone without
// recording an exit — once: the caller that creates the marker logs it.
func noteRunLost(ctx context.Context, projectRoot string, h *detachedHandle) {
	p, ok := v1ProjectAt(projectRoot)
	if !ok {
		return
	}
	f, err := os.OpenFile(lostMarkerPath(projectRoot, h.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return // logged already (os.ErrExist), or nowhere to mark it: never twice
	}
	_ = f.Close()
	ctx = context.WithoutCancel(ctx)
	p.Emit(ctx, handleEvent(ctx, p, h, apexv1.Event{
		Event: apexv1.EventRunLost, Reason: "its supervisor (pid " + strconv.Itoa(h.SupervisorPID) + ") is gone without recording an exit",
	}), os.Stderr)
}

// detachedRuns are the project's detached runs still going: a handle with
// no exit whose supervisor is ours (or not yet recorded). A handle found
// lost is logged (noteRunLost) and does not count.
func detachedRuns(ctx context.Context, projectRoot string) []string {
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(handlePath(projectRoot, "x")), "*.json"))
	var live []string
	for _, path := range paths {
		h, err := readHandle(path)
		if err != nil || h.ExitCode != nil {
			continue
		}
		if h.SupervisorPID != 0 && !supervisorIsOurs(ctx, h) {
			// Re-read once: the exit may have been recorded between the two.
			if h2, err := readHandle(path); err == nil && h2.ExitCode != nil {
				continue
			}
			noteRunLost(ctx, projectRoot, h)
			continue
		}
		live = append(live, h.ID)
	}
	return live
}
