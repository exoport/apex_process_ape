package repl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scanCoupling backs the live gate that asks whether Claude Code still reads
// EnvDisableBGShellReap where it registers its memory-pressure handler. A
// scanner that missed a pair, or invented one, would turn that gate into
// either a false alarm on every release or a green light over a coupling that
// has moved — so the chunking is tested rather than trusted, with a chunk
// small enough to land boundaries inside the matches.
func TestScanCoupling(t *testing.T) {
	t.Parallel()
	const (
		anchor = "ANCHOR"
		needle = "NEEDLE"
	)
	pad := func(n int) string { return strings.Repeat("x", n) }

	for _, tc := range []struct {
		name          string
		body          string
		window, chunk int
		anchors, hits int
		together      bool
	}{
		{
			name: "adjacent", body: pad(50) + anchor + pad(10) + needle + pad(50),
			window: 100, chunk: 64, anchors: 1, hits: 1, together: true,
		},
		{
			name: "needle before anchor still counts", body: needle + pad(20) + anchor,
			window: 100, chunk: 64, anchors: 1, hits: 1, together: true,
		},
		{
			name: "too far apart", body: anchor + pad(500) + needle,
			window: 100, chunk: 64, anchors: 1, hits: 1, together: false,
		},
		{
			name: "no needle", body: pad(200) + anchor + pad(200),
			window: 100, chunk: 64, anchors: 1, hits: 0, together: false,
		},
		{
			name: "no anchor", body: pad(200) + needle + pad(200),
			window: 100, chunk: 64, anchors: 0, hits: 1, together: false,
		},
		{
			// The pair sits astride a chunk boundary, and the needle itself is
			// split across one: both are what the overlap exists for.
			name: "pair straddles a chunk boundary", body: pad(60) + anchor + pad(4) + needle + pad(60),
			window: 100, chunk: 64, anchors: 1, hits: 1, together: true,
		},
		{
			name: "counted once despite the overlap rescan", body: pad(62) + needle + pad(2) + needle + pad(62),
			window: 10, chunk: 64, anchors: 0, hits: 2, together: false,
		},
		{
			name: "far apart across many chunks", body: anchor + pad(5000) + needle,
			window: 64, chunk: 64, anchors: 1, hits: 1, together: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			anchors, hits, together, err := scanCoupling(
				strings.NewReader(tc.body), []byte(anchor), []byte(needle), tc.window, tc.chunk)
			require.NoError(t, err)
			require.Equal(t, tc.anchors, anchors, "anchor count")
			require.Equal(t, tc.hits, hits, "needle count")
			require.Equal(t, tc.together, together, "within %d bytes", tc.window)
		})
	}
}

// Every chunk size must give the same verdict: the boundary is an artifact of
// reading, never of the file.
func TestScanCoupling_ChunkSizeDoesNotChangeTheVerdict(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("y", 300) + "ANCHOR" + strings.Repeat("z", 40) + "NEEDLE" + strings.Repeat("y", 300)
	for chunk := 1; chunk <= 128; chunk++ {
		anchors, hits, together, err := scanCoupling(
			strings.NewReader(body), []byte("ANCHOR"), []byte("NEEDLE"), 64, chunk)
		require.NoError(t, err)
		require.Equal(t, 1, anchors, "chunk %d", chunk)
		require.Equal(t, 1, hits, "chunk %d", chunk)
		require.True(t, together, "chunk %d", chunk)
	}
}

func TestScanCoupling_EmptyPatternIsAnError(t *testing.T) {
	t.Parallel()
	anchors, hits, together, err := scanCoupling(bytes.NewReader([]byte("body")), nil, []byte("x"), 8, 8)
	require.Error(t, err)
	require.Zero(t, anchors)
	require.Zero(t, hits)
	require.False(t, together)
}

// The live gate's verdict, without a claude: each failure class has its own
// remedy, so each must be told apart rather than collapsed into "not found".
func TestReapSwitchProblem(t *testing.T) {
	t.Parallel()
	require.Empty(t, reapSwitchProblem(3, 3, true), "the coupling is intact")
	require.Contains(t, reapSwitchProblem(3, 0, false), "variable is gone")
	require.Contains(t, reapSwitchProblem(0, 3, false), "handler is gone")
	require.Contains(t, reapSwitchProblem(3, 3, false), "no longer near each other")
}
