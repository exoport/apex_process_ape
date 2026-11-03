package conductorwatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Gate is the conductor pane's typing rule (D664, D664+, D665+, D666+ F5),
// one for every path ape types into a person's conductor session: the
// 1.4.2 watcher's deliveries and a v1.5 `ape conductor attach`'s events
// (PaneSession). The pane must run a registered Claude Code session — the
// one pinned at the first look that found one, else the gate is paused
// until a --repin from the new session pins it — with no turn running, an
// empty input row, and a screen unchanged between two looks.
type Gate struct {
	Pane Pane
	// StateDir holds the --repin request (repin.go).
	StateDir string
	Log      func(format string, a ...any)

	lastPane string
	// session is the pinned session id; paused the other session that
	// took the pane since; why the last reason nothing could be typed.
	session, paused, why string
}

func (g *Gate) logf(format string, a ...any) {
	if g.Log != nil {
		g.Log(format, a...)
	}
}

// Session looks at whose session the pane runs. ok: the pinned one.
// pausedNow is the session that just took the pane, once per such
// session, for the caller to notify; the gate stays paused until a
// --repin from it.
func (g *Gate) Session(ctx context.Context) (ok bool, pausedNow string) {
	id, why := g.Pane.Session(ctx)
	if id != "" && g.session == "" {
		g.session = id
		g.logf("watcher: the pane runs Claude Code session %s: deliveries go to it alone\n", id)
	}
	g.checkRepin(id)
	if id != "" && id != g.session {
		g.why = "the watcher is paused: the pane runs another Claude Code session (" + id + ") than the pinned one; `ape conductor watcher --repin` from it resumes"
		g.lastPane = ""
		if g.paused != id {
			g.paused = id
			g.logf("watcher: %s\n", g.PausedReason())
			return false, id
		}
		return false, ""
	}
	if id == "" {
		if g.why != why {
			g.logf("watcher: not typing into the pane: %s — notify-only until it is the conductor's session\n", why)
		}
		g.why, g.lastPane = why, ""
		return false, ""
	}
	return true, ""
}

// PausedReason is the paused gate in one sentence, for the notice.
func (g *Gate) PausedReason() string {
	return fmt.Sprintf("the conductor's pane now runs Claude Code session %s, not %s, the pinned one: the watcher is paused and types nothing — from the new session run `ape conductor watcher --repin` (or restart the watcher)", g.paused, g.session)
}

// Ready looks at the screen (call it after Session said ok): idle, input
// row empty, and the same as at the previous look.
func (g *Gate) Ready(ctx context.Context) bool {
	pane, err := g.Pane.Capture(ctx)
	if err != nil {
		return false
	}
	steady := pane == g.lastPane
	g.lastPane = pane
	if !steady || !Idle(pane) {
		g.why = "the pane was busy, changing, or had text in its input row"
		return false
	}
	return true
}

// Typed is told the gate typed: the next look starts the steady count
// again.
func (g *Gate) Typed() { g.lastPane = "" }

// Why is the last reason nothing could be typed.
func (g *Gate) Why() string { return g.why }

// PaneSession is a conductor.Session over the gate (v1.5 stage 1a, `ape
// conductor attach`): Idle is the gate open, Type types through it and
// submits as the watcher does (Submit).
type PaneSession struct {
	Ctx  context.Context //nolint:containedctx // conductor.Session's methods take none; this is attach's own context
	Gate *Gate
	// SubmitSettle and SubmitWait are Submit's; zero: the defaults.
	SubmitSettle, SubmitWait time.Duration
}

// Idle reports the gate open now.
func (s *PaneSession) Idle() bool {
	ok, _ := s.Gate.Session(s.Ctx)
	return ok && s.Gate.Ready(s.Ctx)
}

// Type types text and presses Enter until it is submitted (v1.5 PRE-4:
// it once only typed, and a real Claude Code conductor kept the first
// delivery in its input row). Unsubmitted is an error, and nothing more
// is typed over it: the gate sees a busy row, and the event stays
// pending, to be delivered again.
func (s *PaneSession) Type(text string) error {
	defer s.Gate.Typed()
	if err := s.Gate.Pane.Type(s.Ctx, text); err != nil {
		return err
	}
	s.Gate.Typed()
	settle, wait := s.SubmitSettle, s.SubmitWait
	if settle == 0 {
		settle = DefaultSubmitSettle
	}
	if wait == 0 {
		wait = DefaultSubmitWait
	}
	if !Submit(s.Ctx, s.Gate.Pane, settle, wait) {
		s.Gate.logf("attach: a delivery was typed but not submitted after %d Enters (the input row kept it, no turn started): nothing more is typed over it\n", submitEnters)
		return errors.New("conductorwatch: the delivery was typed but not submitted")
	}
	return nil
}
