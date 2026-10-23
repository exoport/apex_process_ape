package proctree

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A session's leftovers (O-1): the processes a model session started and
// left running when it ended. KillTree stops claude and its descendants by
// parent pid, but a server the Bash tool backgrounded (`cmd &`, nohup,
// setsid) is re-parented to init — or the nearest subreaper — the moment
// its shell exits, so it is no descendant of claude by the time the
// session is torn down, and it survived every run. Each such process still
// carries the session's marker, APE_SESSION=<kind>/<run-id>, in the
// environment it was started with: that is how SessionLeftovers finds it.
//
// What is never a leftover:
//
//   - this process and its ancestors;
//   - a zombie: it has exited, there is nothing to stop;
//   - ape's own detached processes — the long verbs' `ape __run` workers,
//     which outlive the call that started them by design, and the
//     `ape run supervise` supervisor of a detached run — and every process
//     in their sessions, or below them;
//   - a process whose environment cannot be read (another user's).
//
// A process that scrubbed its environment (`env -i`, a container) carries
// no marker and is not found.

// Leftover is one process a session left running, and what was done to it.
type Leftover struct {
	PID     int    `json:"pid"     yaml:"pid"`
	Command string `json:"command" yaml:"command"`
	// Started is when the process started (UTC).
	Started time.Time `json:"started,omitzero" yaml:"started,omitempty"`
	// Action is LeftoverKilled, LeftoverKept or LeftoverKillFailed.
	Action string `json:"action" yaml:"action"`
}

// The actions a Leftover records.
const (
	LeftoverKilled     = "killed"
	LeftoverKept       = "kept"
	LeftoverKillFailed = "kill-failed"
)

// The --leftovers modes: kill stops each leftover's tree, report records
// it and leaves it running.
const (
	LeftoversKill   = "kill"
	LeftoversReport = "report"
)

// LeftoversGrace is how long a leftover has between SIGTERM and SIGKILL.
const LeftoversGrace = 2 * time.Second

// ParseLeftoversMode validates a --leftovers value; empty is kill.
func ParseLeftoversMode(s string) (string, error) {
	switch s {
	case "", LeftoversKill:
		return LeftoversKill, nil
	case LeftoversReport:
		return LeftoversReport, nil
	}
	return "", fmt.Errorf("--leftovers must be %s or %s, got %q", LeftoversKill, LeftoversReport, s)
}

// SessionLeftovers finds the processes still running with marker — an
// exact "KEY=VALUE" entry of their environment, the session's
// APE_SESSION — and, unless mode is report, stops each one's tree (SIGTERM,
// grace, SIGKILL). Only the topmost of a chain of leftovers is listed: the
// rest go with its tree. Nil when there are none, and always nil where the
// scan is not supported (anything but Linux).
//
// Call it after the session's claude has been stopped: whatever still
// carries the marker then is a leftover.
func SessionLeftovers(ctx context.Context, marker, mode string, grace time.Duration) []Leftover {
	if marker == "" || !strings.Contains(marker, "=") {
		return nil
	}
	found := findLeftovers(marker)
	if len(found) == 0 {
		return nil
	}
	out := make([]Leftover, 0, len(found))
	for _, p := range found {
		l := Leftover{PID: p.pid, Command: p.command(), Started: p.started(), Action: LeftoverKept}
		if mode != LeftoversReport {
			l.Action = stopLeftover(ctx, p, grace)
		}
		out = append(out, l)
	}
	return out
}

// LeftoversLine is the one stderr line a run's leftovers are worth.
func LeftoversLine(list []Leftover) string {
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, l := range list {
		parts = append(parts, fmt.Sprintf("pid %d %s (%s)", l.PID, l.Action, l.Command))
	}
	noun := "processes"
	if len(list) == 1 {
		noun = "process"
	}
	return fmt.Sprintf("⚠ the session left %d %s running after it ended: %s — recorded under session_leftovers in the run record",
		len(list), noun, strings.Join(parts, "; "))
}

// procInfo is what the scan reads of one process.
type procInfo struct {
	pid, ppid, pgid, sid int
	state                byte
	// start is the process's start time in clock ticks since boot: with
	// the pid, it names the process, so a reused pid is not mistaken for it.
	start uint64
	argv  []string
	// boot is the system's boot time, to turn start into a time.
	boot time.Time
}

const maxCommandLen = 200

func (p procInfo) command() string {
	c := strings.Join(p.argv, " ")
	if r := []rune(c); len(r) > maxCommandLen {
		c = string(r[:maxCommandLen]) + "…"
	}
	if c == "" {
		c = "pid " + strconv.Itoa(p.pid)
	}
	return c
}

func (p procInfo) started() time.Time {
	if p.boot.IsZero() {
		return time.Time{}
	}
	return p.boot.Add(time.Duration(p.start) * clockTick).UTC().Truncate(time.Second)
}

// clockTick is USER_HZ, the unit /proc/<pid>/stat counts start time in:
// 100 on every Linux ape runs on.
const clockTick = 10 * time.Millisecond

// isApeDetached reports whether argv is one of ape's own detached
// processes: a long verb's worker (`ape __run …`, and the `ape __run
// --spawn` that starts it) or a detached run's supervisor (`ape run
// supervise …`). Matched on the arguments, not the binary's name: the
// binary may be called anything, and is whatever ape the session ran.
func isApeDetached(argv []string) bool {
	if len(argv) >= 2 && argv[1] == "__run" {
		return true
	}
	return len(argv) >= 3 && argv[1] == "run" && argv[2] == "supervise"
}

// selectLeftovers picks, from the marked processes, the leftovers to
// report: not self, not an ancestor of self, not a zombie, not one of ape's
// detached processes, nor in one's session or process group, nor below any
// process that is — and, of those left, only the ones no other leftover is
// above. lookup reads any process (an ancestor, a session leader), false
// when it is gone.
func selectLeftovers(marked []procInfo, self int, selfAncestors []int, lookup func(pid int) (procInfo, bool)) []procInfo {
	skip := map[int]bool{self: true}
	for _, a := range selfAncestors {
		skip[a] = true
	}
	parent := func(pid int) (int, bool) {
		q, ok := lookup(pid)
		return q.ppid, ok
	}
	isDetached := func(pid int) bool {
		if pid <= 1 {
			return false
		}
		q, ok := lookup(pid)
		return ok && isApeDetached(q.argv)
	}
	// ape's: a detached process, or in a detached process's session or group.
	apes := func(pid int) bool {
		q, ok := lookup(pid)
		return ok && (isApeDetached(q.argv) || isDetached(q.sid) || isDetached(q.pgid))
	}
	var keep []procInfo
	for _, p := range marked {
		if skip[p.pid] || p.state == 'Z' || p.state == 'X' ||
			isApeDetached(p.argv) || isDetached(p.sid) || isDetached(p.pgid) ||
			slices.ContainsFunc(Ancestors(p.pid, parent), apes) {
			continue
		}
		keep = append(keep, p)
	}
	kept := map[int]bool{}
	for _, p := range keep {
		kept[p.pid] = true
	}
	var roots []procInfo
	for _, p := range keep {
		if !slices.ContainsFunc(Ancestors(p.pid, parent), func(a int) bool { return kept[a] }) {
			roots = append(roots, p)
		}
	}
	return roots
}
