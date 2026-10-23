package proctree

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The selection rules, on a made-up process table: what a session's
// teardown may stop, and what it must leave alone.
func TestSelectLeftovers_Rules(t *testing.T) {
	table := map[int]procInfo{
		// ape task (self, 100) under a shell (90).
		90:  {pid: 90, ppid: 1, sid: 90, argv: []string{"zsh"}},
		100: {pid: 100, ppid: 90, sid: 90, argv: []string{"ape", "task", "apex-plan"}},
		// A re-parented server, and a worker it started: one leftover.
		200: {pid: 200, ppid: 1, sid: 200, argv: []string{"taskforge", "serve"}},
		201: {pid: 201, ppid: 200, sid: 200, argv: []string{"taskforge", "worker"}},
		// A long verb's worker and what it runs: never.
		300: {pid: 300, ppid: 1, sid: 300, pgid: 300, argv: []string{"/usr/bin/ape", "__run", "r1", "milestone", "gate", "M1"}},
		301: {pid: 301, ppid: 1, sid: 300, pgid: 300, argv: []string{"sh", "-c", "./gate.sh"}},
		302: {pid: 302, ppid: 301, sid: 302, pgid: 302, argv: []string{"setsid", "db"}},
		// A detached run's supervisor and its session: never.
		400: {pid: 400, ppid: 1, sid: 400, argv: []string{"ape", "run", "supervise", "--handle", "x/detached/d1.json"}},
		401: {pid: 401, ppid: 400, sid: 400, argv: []string{"ape", "task", "apex-review"}},
		// A zombie: nothing to stop.
		500: {pid: 500, ppid: 1, sid: 500, state: 'Z', argv: []string{"sleep"}},
	}
	lookup := func(pid int) (procInfo, bool) { p, ok := table[pid]; return p, ok }
	pids := []int{90, 100, 200, 201, 300, 301, 302, 400, 401, 500}
	marked := make([]procInfo, 0, len(pids))
	for _, pid := range pids {
		marked = append(marked, table[pid])
	}
	got := selectLeftovers(marked, 100, Ancestors(100, func(pid int) (int, bool) { p, ok := table[pid]; return p.ppid, ok }), lookup)
	roots := make([]int, 0, len(got))
	for _, p := range got {
		roots = append(roots, p.pid)
	}
	require.Equal(t, []int{200}, roots, "only the server: its worker goes with its tree")
}

func TestIsApeDetached(t *testing.T) {
	require.True(t, isApeDetached([]string{"/x/ape", "__run", "--spawn", "--log", "l", "r1"}))
	require.True(t, isApeDetached([]string{"ape-dev", "run", "supervise", "--handle", "h"}))
	require.False(t, isApeDetached([]string{"ape", "run", "wait"}))
	require.False(t, isApeDetached([]string{"ape", "task", "__run"}))
	require.False(t, isApeDetached(nil))
}

func TestParseLeftoversMode(t *testing.T) {
	for in, want := range map[string]string{"": LeftoversKill, "kill": LeftoversKill, "report": LeftoversReport} {
		got, err := ParseLeftoversMode(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := ParseLeftoversMode("keep")
	require.ErrorContains(t, err, "kill or report")
}

func TestLeftoversLine(t *testing.T) {
	require.Empty(t, LeftoversLine(nil))
	line := LeftoversLine([]Leftover{{PID: 7, Command: "sleep 600", Action: LeftoverKilled}})
	require.Contains(t, line, "1 process running")
	require.Contains(t, line, "pid 7 killed (sleep 600)")
	require.Equal(t, 1, strings.Count(line, "\n")+1, "one line")
}

func TestSessionLeftovers_NoMarkerFindsNothing(t *testing.T) {
	require.Nil(t, SessionLeftovers(t.Context(), "", LeftoversKill, 0))
	require.Nil(t, SessionLeftovers(t.Context(), "NOEQUALS", LeftoversKill, 0))
}
