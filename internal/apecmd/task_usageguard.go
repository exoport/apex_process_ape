package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/usage"
)

// usageGuard is W4-A2: before an APEX v1 `ape task` session starts — the
// loop's children, and the sessions ape change starts, included: they are
// all `ape task` — ape reads the plan's usage (internal/usage) against
// limits.usage and launches, holds the launch on the 5-hour window until
// it resets, or stops on the 7-day window for a person (exit 14,
// termination usage_guard). It runs once the run has its manifest and
// before claude is spawned (pipeline.RunOptions.BeforeSpawn), so a hold is
// a live run ape status shows as held, and a stop is a run with a record.
type usageGuard struct {
	cfg     usage.GuardConfig
	skill   string
	args    string
	usageOK bool

	// decision is the last decision, and heldFrom/heldUntil the hold
	// before it, for the manifest (record).
	decision  *usage.Decision
	heldFrom  time.Time
	heldUntil time.Time
}

// newUsageGuard is the guard of one launch of skill in p.
func newUsageGuard(p *apexv1.Project, skill, args string, usageOK bool) *usageGuard {
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil {
		// Open has already refused an unparseable config; the defaults.
		return &usageGuard{cfg: usage.DefaultGuard(), skill: skill, args: args, usageOK: usageOK}
	}
	return &usageGuard{cfg: cfg.Limits.UsageGuard(), skill: skill, args: args, usageOK: usageOK}
}

// Test seams: the hold's sleep, and how often a held launch says so.
var (
	usageSleep = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	usageHoldLineEvery = 10 * time.Minute
)

// share is the launch's estimated use of each window.
func (g *usageGuard) share() usage.Share {
	if g.cfg.Share != nil {
		return usage.Share{FiveHour: *g.cfg.Share, SevenDay: *g.cfg.Share, Source: "config"}
	}
	return usage.AutoShare(g.skill)
}

// beforeSpawn decides, holding as long as the decision is a hold, and
// returns the stop as a *pipeline.LimitStopError (termination
// usage_guard). held is told when a hold starts (until) and ends (nil).
func (g *usageGuard) beforeSpawn(ctx context.Context, held func(*time.Time), w io.Writer) error {
	if g == nil {
		return nil
	}
	for {
		d := usage.Decide(g.cfg, usage.Load(), g.share(), usageNow(), g.usageOK)
		g.decision = &d
		switch d.Outcome {
		case usage.Stop:
			// Said once, as the run's error.
			return g.stopError(d)
		case usage.Hold:
			if g.heldFrom.IsZero() {
				g.heldFrom = usageNow()
			}
			g.heldUntil = d.HeldUntil
			until := d.HeldUntil
			held(&until)
			err := g.hold(ctx, d, w)
			held(nil)
			if err != nil {
				return fmt.Errorf("usage guard: held launch: %w", err)
			}
		case usage.Unknown, usage.UsageOK:
			fmt.Fprintf(w, "⚠ %s — launching\n", d.Why)
			return nil
		default:
			return nil
		}
	}
}

// hold waits until d.HeldUntil, saying so now and every
// usageHoldLineEvery.
func (g *usageGuard) hold(ctx context.Context, d usage.Decision, w io.Writer) error {
	for {
		left := d.HeldUntil.Sub(usageNow())
		if left <= 0 {
			return nil
		}
		fmt.Fprintf(w, "⏸ usage guard: %s — holding this %s launch until %s (%s left; the window's reset + 1 min); ape status shows it held\n",
			d.Why, g.skill, d.HeldUntil.Local().Format(time.RFC3339), left.Round(time.Minute))
		if err := usageSleep(ctx, min(left, usageHoldLineEvery)); err != nil {
			return err
		}
	}
}

// overrideCommand is the command a person runs to launch anyway.
func (g *usageGuard) overrideCommand() string {
	cmd := "ape task " + shellQuote(g.skill)
	if a := strings.TrimSpace(g.args); a != "" {
		cmd += " --args " + shellQuote(a)
	}
	return cmd + " --usage-ok"
}

// stopMessage says the stop: the reading, the share, the reset and the
// override.
func (g *usageGuard) stopMessage(d usage.Decision) string {
	msg := "usage guard: " + d.Why + " (share " + d.Share.Source + ")"
	if !d.ResetsAt.IsZero() {
		msg += "; it resets " + d.ResetsAt.Local().Format(time.RFC3339)
	}
	if r := d.Reading; r != nil {
		msg += "; reading " + r.Short() + ", " + usage.Ago(r.Age(usageNow()))
	}
	return msg + " — ape did not start the " + g.skill + " session, a person's call: wait for the reset, or launch it anyway with `" +
		g.overrideCommand() + "` (a person only), or change limits.usage (guarded: ape project accept-config)"
}

func (g *usageGuard) stopError(d usage.Decision) *pipeline.LimitStopError {
	e := &pipeline.LimitStopError{Kind: pipeline.TerminationUsageGuard, Message: g.stopMessage(d), ResetsAt: d.ResetsAt}
	if !d.ResetsAt.IsZero() {
		e.Resets = d.ResetsAt.Local().Format(time.RFC3339)
	}
	return e
}

// record is the decision in the manifest's shape; nil before any.
func (g *usageGuard) record() *pipeline.UsageGuardRecord {
	if g == nil || g.decision == nil {
		return nil
	}
	d := g.decision
	rec := &pipeline.UsageGuardRecord{
		Decision: string(d.Outcome), Reading: manifestReading(d.Reading),
		ShareFiveHour: d.Share.FiveHour, ShareSevenDay: d.Share.SevenDay, ShareSource: d.Share.Source,
		UsageOK: d.Outcome == usage.UsageOK,
	}
	if d.Outcome == usage.Unknown || d.Outcome == usage.UsageOK {
		rec.Note = d.Why
	}
	if !g.heldFrom.IsZero() {
		until := g.heldUntil
		rec.HeldUntil = &until
		rec.HeldSeconds = usageNow().Sub(g.heldFrom).Seconds()
	}
	if d.Outcome == usage.Off {
		rec.ShareFiveHour, rec.ShareSevenDay, rec.ShareSource = 0, 0, ""
	}
	return rec
}

// recordUsageGuard writes the guard's decision into the run's manifest.
func recordUsageGuard(runDir string, g *usageGuard) {
	rec := g.record()
	if rec == nil || runDir == "" {
		return
	}
	patchManifestAt(runDir, func(m *pipeline.Manifest) {
		if m.Usage == nil {
			m.Usage = &pipeline.UsageRecord{}
		}
		m.Usage.Guard = rec
	})
}

// emitUsageGuardStop logs the stop's events (W3): stop, reason
// usage-guard, and person, the override as its action.
func emitUsageGuardStop(ctx context.Context, p *apexv1.Project, g *usageGuard, runID string, lse *pipeline.LimitStopError) {
	ctx = context.WithoutCancel(ctx)
	milestone, change := p.EventScope(ctx, g.args)
	p.Emit(ctx, apexv1.Event{Event: apexv1.EventStop, Milestone: milestone, Change: change, Run: runID, Reason: usageGuardReason, Skill: g.skill}, os.Stderr)
	p.Emit(ctx, apexv1.Event{Event: apexv1.EventPerson, Milestone: milestone, Change: change, Run: runID, Reason: lse.Message, Action: g.overrideCommand(), Skill: g.skill}, os.Stderr)
}

// usageGuardReason is a usage-guard stop's APE_REASON.
const usageGuardReason = "usage-guard"

// isUsageGuardStop reports whether a run ended on the usage guard.
func isUsageGuardStop(runErr error) bool {
	lse, ok := errors.AsType[*pipeline.LimitStopError](runErr)
	return ok && lse.Kind == pipeline.TerminationUsageGuard
}
