package repl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionOfMarker(t *testing.T) {
	for marker, want := range map[string][2]string{
		"task/20261023-101010-abc1234":                                  {"task", "20261023-101010-abc1234"},
		"task/20261023-101010-abc1234/worker/20261023-101011-42-a1b2c3": {"task", "20261023-101010-abc1234"},
		"prompt/p1/worker/w1":                                           {"prompt", "p1"},
		"chat/20261023T101010Z":                                         {"chat", "20261023T101010Z"},
		"":                                                              {"", ""},
		"task":                                                          {"task", ""},
	} {
		kind, run := SessionOfMarker(marker)
		require.Equal(t, want, [2]string{kind, run}, marker)
	}
}

// D447: a worker's marker names the session and the worker, is never the
// session's exact value, and a worker's worker keeps the session part.
func TestWorkerMarker(t *testing.T) {
	require.Equal(t, "task/r1/worker/w1", WorkerMarker("task/r1", "w1"))
	require.Equal(t, "task/r1/worker/w2", WorkerMarker("task/r1/worker/w1", "w2"))
	kind, run := SessionOfMarker(WorkerMarker("task/r1", "w1"))
	require.Equal(t, "task", kind)
	require.Equal(t, "r1", run)
}

func TestWorkerEnv(t *testing.T) {
	require.Equal(t,
		[]string{"A=1", "APE_SESSION=task/r1/worker/w1", "APE_DEPTH=1", "APE_SESSION_ID=x"},
		WorkerEnv([]string{"A=1", "APE_SESSION=task/r1", "APE_DEPTH=1", "APE_SESSION_ID=x"}, "w1"))
	// Outside every session a worker stays outside one: an empty or absent
	// marker is never invented.
	require.Equal(t, []string{"APE_SESSION=", "B=2"}, WorkerEnv([]string{"APE_SESSION=", "B=2"}, "w1"))
	require.Equal(t, []string{"B=2"}, WorkerEnv([]string{"B=2"}, "w1"))
}
