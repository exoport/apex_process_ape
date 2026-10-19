package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/proctree"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// The loop relay (OI-11; apexv1/relay.go has the rule). Two halves:
//
//   - the `ape task <loop>` process keeps a relay record of its run
//     (loopRelay): the run, its relays so far, and its session's transcript
//     once a hook names it. When the session ends after a child launch
//     refused it, the process starts a fresh loop session with the same
//     args (dispatchLoop), its manifest recording relay_of, under the lock
//     it already holds;
//   - every child launch from the loop (gateSession, serviceGate) reads the
//     record, judges the loop's context, and on a refusal records the relay
//     it asks for (relayGate).

// relayGate is the relay's check of verb, a child launch from the loop run
// run: nil, or the refusal — apexv1.RelayCheck's session.relay or
// limits.relays — recorded on the run's relay record for the process
// running the loop to act on. No record (a loop ape did not launch, or an
// older ape's), no transcript or turn yet, or a child still running (one at
// a time refuses it anyway, and a relay must find no child running) is no
// relay. An unknown window is no relay either, said once per run on warn.
// A refusal the record cannot carry is not made: a loop told ape relays it
// must be relayed.
func relayGate(verb, projectRoot, run string, warn io.Writer) error {
	rec, err := apexv1.ReadRelayRecord(projectRoot, run)
	if err != nil || rec == nil || rec.Transcript == "" || childSlotBusy(projectRoot, run) {
		return nil //nolint:nilerr // an unreadable relay record is no relay: a child launch is never refused on it
	}
	use, ok := apexv1.TranscriptContext(rec.Transcript)
	if !ok {
		return nil
	}
	if !use.Known() {
		if rec.ContextUnknown == "" {
			rec.ContextUnknown = use.Model
			if apexv1.WriteRelayRecord(projectRoot, rec) == nil {
				fmt.Fprintf(warn, "warning: ape knows no context window for %s, so this %s session's context fill is unknown and it is not relayed (said once per run)\n",
					use.Model, apexv1.OrchestratorSkill)
			}
		}
		return nil
	}
	cfg, _ := apexv1.LoadConfig(projectRoot)
	refusal := apexv1.RelayCheck(verb, use, cfg.Limits, rec.Relays)
	if refusal == nil {
		return nil
	}
	_, exhausted := errors.AsType[*apexv1.FailError](refusal)
	rec.Requested = &apexv1.RelayRequest{
		At: time.Now().UTC(), Verb: verb, Tokens: use.Tokens, Window: use.Window, Model: use.Model,
		Threshold: cfg.Limits.ContextRelayAt(), Exhausted: exhausted,
	}
	if apexv1.WriteRelayRecord(projectRoot, rec) != nil {
		return nil //nolint:nilerr // a refusal the record cannot carry is not made (see above)
	}
	return refusal
}

// childSlotBusy reports whether run, a loop run, has a live child holding
// its one child slot (another holder than this process).
func childSlotBusy(projectRoot, run string) bool {
	cur, err := readSessionLock(filepath.Join(runlog.SessionLocksRoot(projectRoot), "child-"+safeLockName(run)+".json"))
	return err == nil && cur.Token != lockToken() && proctree.Alive(cur.PID)
}

// loopRelay is the process running the loop's side of one loop session's
// relay record. Nil-safe: a run that is not the loop's has none.
type loopRelay struct {
	root string
	// relays is how many relays the loop run has had before this session,
	// relayOf the run it relays ("" for the first session).
	relays  int
	relayOf string

	mu  sync.Mutex
	run string
}

// start writes the record when the run's id is known (OnRunDir).
func (r *loopRelay) start(run string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = run
	rec := &apexv1.RelayRecord{Run: run, RelayOf: r.relayOf, Relays: r.relays, StartedAt: time.Now().UTC()}
	if err := apexv1.WriteRelayRecord(r.root, rec); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ relay record not written (%v): this %s session is not relayed\n", err, apexv1.OrchestratorSkill)
	}
}

// transcript records the session's transcript, which a hook named
// (interactiveCore.onTranscript). Read-modify-write: a child launch's
// fields survive it.
func (r *loopRelay) transcript(path string) {
	if r == nil || path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := apexv1.ReadRelayRecord(r.root, r.run)
	if err != nil || rec == nil || rec.Transcript == path {
		return
	}
	rec.Transcript = path
	_ = apexv1.WriteRelayRecord(r.root, rec)
}

// finish reads the record as the session left it and removes it; nil when
// there was none.
func (r *loopRelay) finish() *apexv1.RelayRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _ := apexv1.ReadRelayRecord(r.root, r.run)
	apexv1.RemoveRelayRecord(r.root, r.run)
	return rec
}

func (r *loopRelay) relayOfOrEmpty() string {
	if r == nil {
		return ""
	}
	return r.relayOf
}

func (r *loopRelay) count() int {
	if r == nil {
		return 0
	}
	return r.relays
}

// dispatchLoop runs the loop's `ape task` with its relays: one session,
// and while a session ends on a child launch's session.relay refusal, a
// fresh one with the same args. It returns the last session's run, its
// envelope naming relays and relay_of; a session that ended on
// limits.relays stops the run (exit 1, termination relays, a person's). The
// sessions share o.launch: one lock for the whole loop run.
func dispatchLoop(ctx context.Context, o taskOptions, cfg runConfig) (taskRun, error) {
	relays, relayOf := 0, ""
	for {
		c := cfg
		c.relay = &loopRelay{root: o.projectRoot, relays: relays, relayOf: relayOf}
		run, err := dispatchTaskOnce(ctx, o, c)
		rec := c.relay.finish()
		if err != nil {
			return run, err
		}
		run.Envelope.Relays, run.Envelope.RelayOf = relays, relayOf
		if rec != nil && rec.ContextUnknown != "" {
			fmt.Fprintf(os.Stderr, "⚠ relay: ape knows no context window for %s, so run %s's context fill was unknown and it was not relayed\n", rec.ContextUnknown, run.RunID)
		}
		if run.RunErr != nil || rec == nil || rec.Requested == nil || run.RunID == "" {
			return run, nil //nolint:nilerr // run.RunErr is the run's own outcome, carried in run; no relay follows it
		}
		req := rec.Requested
		if req.Exhausted {
			return relaysStopped(o, run, req, relays), nil
		}
		cfgNow, _ := apexv1.LoadConfig(o.projectRoot)
		use := req.Use()
		relays++
		fmt.Fprintf(os.Stderr, "↻ relay %d of %d: the %s session of run %s ended with its context at %d %% (%d of %d tokens) ≥ limits.context_relay %d %% — starting a fresh session with the same args\n",
			relays, cfgNow.Limits.RelaysCap(), o.skill, run.RunID, apexv1.Percent(use.Fill()), use.Tokens, use.Window, apexv1.Percent(req.Threshold))
		relayOf = run.RunID
	}
}

// relaysStopped ends the loop run whose session a limits.relays refusal
// ended: the error, with its resume line, and termination relays in the
// session's manifest.
func relaysStopped(o taskOptions, run taskRun, req *apexv1.RelayRequest, relays int) taskRun {
	cfg, _ := apexv1.LoadConfig(o.projectRoot)
	stop := apexv1.RelaysExhausted(req, relays, cfg.Limits.RelaysCap())
	patchManifestAt(run.RunDir, func(m *pipeline.Manifest) {
		m.Termination = &pipeline.TerminationRecord{Kind: pipeline.TerminationRelays, Message: stop.Error()}
	})
	run.RunErr = &resumeHintError{err: stop, line: pipeline.ResumeLine(pipeline.TerminationRelays, strings.TrimSpace(o.args), orchestratorResumeCommand(o.args))}
	msg := run.RunErr.Error()
	run.Envelope.Error, run.Envelope.Success, run.Envelope.ExitCode = &msg, false, ExitRunFailed
	return run
}
