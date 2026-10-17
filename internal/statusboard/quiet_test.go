package statusboard

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/stretchr/testify/require"
)

// TestQuiet_WhatAboardV030DoesToday measures, against the pinned aboard's
// own server running in-process, what the tab's `quiet: true` (D60, D75)
// does today:
//
//   - a refresh with no board running writes the file, `quiet` kept and no
//     `touched`: no dot;
//   - an unchanged view is not written at all: no dot;
//   - a changed view posted to the running board is stamped `touched` — the
//     dot and the banner — and the server drops `quiet` from the document
//     it persists;
//   - `quiet` draws no warning, neither from `apply` nor in the server's log.
//
// So aboard v0.3.0 tolerates `quiet` silently and honours it nowhere. When
// ape pins the aboard release that honours it, the `touched` assertion
// fails: flip it (and the dropped-key one) to pin the new behaviour.
func TestQuiet_WhatAboardV030DoesToday(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real board server")
	}
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	root := aboard.Root(proj)

	// 1. No board running: the file is written, quiet kept, no dot.
	var errOut bytes.Buffer
	require.Equal(t, ActionWrote, Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut).Action)
	tab := stateTab(t, proj)
	require.Equal(t, true, tab[keyQuiet])
	require.NotContains(t, tab, "touched")

	serverLog := &lockedWriter{}
	logger := log.New(serverLog, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- aboard.Serve(ctx, aboard.Options{Host: aboard.HostApe, Logger: logger}, aboard.ServeConfig{Root: root})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("the board did not shut down")
		}
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(root.InstanceFile(""))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the board never recorded its instance")

	// 2. The same view again, board running: nothing is written, no dot.
	require.Equal(t, ActionUnchanged, Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut).Action)
	require.NotContains(t, stateTab(t, proj), "touched")

	// 3. A changed view, posted: the server stamps touched and drops quiet.
	r := sampleReport()
	r.Archived = 98
	require.Equal(t, ActionPosted, Refresh(context.Background(), proj, r, time.Now(), &errOut).Action)
	require.Empty(t, errOut.String(), "the refresh said something on stderr")
	tab = stateTab(t, proj)
	touched, ok := tab["touched"].(map[string]any)
	require.True(t, ok, "the dot was not raised: aboard now honours quiet — pin that here")
	require.Equal(t, Actor, touched["by"])
	require.NotContains(t, tab, keyQuiet, "aboard now persists quiet — check whether it honours it, and pin that here")

	// 4. The same write through `apply` itself, so its warnings are seen.
	doc := readState(t, proj)
	tab = tabAt(t, doc, 0)
	tab[keyQuiet] = true
	r.Archived = 99
	state, err := normalized(buildState(r, time.Now()))
	require.NoError(t, err)
	tab["state"] = state
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	var applyOut, applyErr bytes.Buffer
	require.NoError(t, aboard.Apply(context.Background(), root, "", aboard.ApplyOptions{By: Actor},
		aboard.WebFS(), bytes.NewReader(body), &applyOut, &applyErr, aboard.Invocation(invocation)))
	t.Logf("apply stdout: %q stderr: %q", applyOut.String(), applyErr.String())
	t.Logf("server log:\n%s", serverLog.String())
	require.Empty(t, applyErr.String(), "apply said something about the write")
	require.NotContains(t, serverLog.String(), "quiet", "the server logged about quiet")
	require.NotContains(t, serverLog.String(), "warning", "the server logged a warning")
}

// stateTab is the first tab of the document the server persisted.
func stateTab(t *testing.T, proj string) map[string]any {
	t.Helper()
	return tabAt(t, readState(t, proj), 0)
}

// lockedWriter serialises the server's log writes against the test's read.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
