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
// own server running in-process, what D60's "silent" asks for and what the
// board does with a tab-level `quiet: true` (RC3-PLAN Q1):
//
//   - an agent write that changes the status tab stamps `touched` — the dot
//     and the banner — whatever the tab says;
//   - `quiet` draws no warning, neither from `apply` nor in the server's
//     log, and the server drops it from the document it persists.
//
// So `quiet` is tolerated but honoured by nothing, and quietTab stays off
// until the aboard release that honours it is pinned. When that bump lands
// this test fails on the `touched` assertion: flip it, and set quietTab.
func TestQuiet_WhatAboardV030DoesToday(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real board server")
	}
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	root := aboard.Root(proj)
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

	// 1. The refresh ape makes today: the tab is created, and dotted.
	var errOut bytes.Buffer
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, ActionPosted, out.Action)
	tab := stateTab(t, proj)
	require.NotNil(t, tab["touched"], "aboard v0.3.0 raised no dot for an agent's new tab")

	// 2. A tab-level quiet:true on a changing write, through `apply` itself
	// so its warnings are seen: still dotted, no warning, key dropped.
	doc := readState(t, proj)
	tab = tabAt(t, doc, 0)
	tab[keyQuiet] = true
	delete(tab, "touched")
	r := sampleReport()
	r.Archived = 99
	state, err := normalized(buildState(r, time.Now()))
	require.NoError(t, err)
	tab["state"] = state
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	var applyOut, applyErr bytes.Buffer
	require.NoError(t, aboard.Apply(context.Background(), root, "", aboard.ApplyOptions{By: Actor},
		aboard.WebFS(), bytes.NewReader(body), &applyOut, &applyErr, aboard.Invocation(invocation)))
	require.NotContains(t, applyErr.String(), "warning", "apply warned about quiet")
	tab = stateTab(t, proj)
	require.NotNil(t, tab["touched"], "the dot was not raised: aboard now honours quiet — set quietTab")
	require.NotContains(t, tab, keyQuiet, "aboard now persists quiet — check whether it honours it, and set quietTab")
	t.Logf("server log:\n%s", serverLog.String())
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
