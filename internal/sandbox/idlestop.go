package sandbox

import (
	"fmt"
	"strings"
	"time"
)

// The per-workspace idle-stop setting (PLAN-24 D7).
//
// It is one string that travels from the project's committed descriptor to the
// node's reaper, because the two things a project wants to say about reaping —
// "leave this one alone" and "this one can go sooner/later than the node's
// default" — are the same field with different values. A separate boolean plus a
// duration would make "off with a threshold" expressible and meaningless.
//
// The setting is a REQUEST like everything else in the descriptor. The node's
// reaper decides whether it runs at all; this only narrows or disables what it
// would otherwise do to this workspace.

// IdleStopOff is the descriptor value that exempts a workspace from the reaper.
const IdleStopOff = "off"

// DefaultIdleStop is the node-wide threshold when neither the workspace nor the
// operator says otherwise: two hours without the guest doing any work.
//
// Long enough that a lunch break, a long test run whose output nobody is
// watching, or a debugging session spent reading does not lose a workspace;
// short enough that a machine left running overnight gives its memory back. The
// action is `stop`, which costs ~30s to undo, so the penalty for being wrong in
// either direction is small — which is what allows a default at all.
const DefaultIdleStop = 2 * time.Hour

// ParseIdleStop interprets a descriptor's idle_stop value.
//
// Returns (0, false, nil) for an empty value — "no opinion, use the node's
// default". Returns (0, true, nil) for "off" — an explicit exemption. Otherwise
// a Go duration.
//
// A zero or negative duration is an ERROR rather than a synonym for off:
// `idle_stop: 0s` most plausibly means someone expected "never", and reading it
// as "stop immediately" would reap every workspace on the node the moment it
// went quiet.
func ParseIdleStop(value string) (after time.Duration, disabled bool, err error) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "":
		return 0, false, nil
	case IdleStopOff, "false", "never":
		return 0, true, nil
	}
	d, perr := time.ParseDuration(v)
	if perr != nil {
		return 0, false, fmt.Errorf("idle_stop %q is not a duration or %q (e.g. 30m, 4h)", value, IdleStopOff)
	}
	if d <= 0 {
		return 0, false, fmt.Errorf("idle_stop %q must be positive — use %q to disable idle-stop for this workspace",
			value, IdleStopOff)
	}
	return d, false, nil
}

// ResolveIdleStop combines a workspace's setting with the node's default and
// reports the threshold to apply, or ok=false when this workspace is exempt.
//
// An unparseable per-workspace value falls back to the node default rather than
// disabling the reaper: the value was already validated where it was written, so
// reaching here with a bad one means something else is wrong, and the failure
// mode of "quietly never reaps" is the one nobody notices.
func ResolveIdleStop(workspaceValue string, nodeDefault time.Duration) (after time.Duration, ok bool) {
	if nodeDefault <= 0 {
		return 0, false // the node's reaper is off entirely
	}
	d, disabled, err := ParseIdleStop(workspaceValue)
	switch {
	case disabled:
		return 0, false
	case err != nil || d <= 0:
		return nodeDefault, true
	default:
		return d, true
	}
}
