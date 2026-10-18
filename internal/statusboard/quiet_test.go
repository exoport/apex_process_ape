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

// TestQuiet_AboardV040HonoursIt pins, against the pinned aboard v0.4.0's
// own server running in-process, what the tab's `quiet: true` (D60, D126)
// does:
//
//   - a refresh with no board running writes the file, quiet set and no
//     `touched`: no dot;
//   - an unchanged view is not written at all: no dot;
//   - a changed view posted to the running board is taken, and the server
//     keeps `quiet` and stamps no `touched`: no dot, banner or count;
//   - once a person turns quiet off, ape does not turn it back on: its next
//     changed view raises the dot (QUIET: ape sets quiet once);
//   - neither `apply` nor the server's log warns about any of it.
func TestQuiet_AboardV040HonoursIt(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real board server")
	}
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	root := aboard.Root(proj)

	// 1. No board running: the file is written, quiet set, no dot.
	var errOut bytes.Buffer
	require.Equal(t, ActionWrote, Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut).Action)
	tab := stateTab(t, proj)
	require.Equal(t, true, tab[keyQuiet])
	require.NotContains(t, tab, "touched")
	require.True(t, quietSet(tab["state"]), "the tab records that ape set quiet")

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

	// 3. A changed view, posted: taken, quiet kept, no dot.
	r := sampleReport()
	r.Archived = 98
	require.Equal(t, ActionPosted, Refresh(context.Background(), proj, r, time.Now(), &errOut).Action)
	require.Empty(t, errOut.String(), "the refresh said something on stderr")
	tab = stateTab(t, proj)
	require.Equal(t, true, tab[keyQuiet], "the server dropped quiet")
	require.NotContains(t, tab, "touched", "a changed view on a quiet tab raised the dot")

	// 4. Quiet turned off (through apply, as the tab menu would): ape's next
	// changed view does not turn it back on, so the dot comes back.
	doc := readState(t, proj)
	delete(tabAt(t, doc, 0), keyQuiet)
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	var applyOut, applyErr bytes.Buffer
	require.NoError(t, aboard.Apply(context.Background(), root, "", aboard.ApplyOptions{By: "smoke-person"},
		aboard.WebFS(), bytes.NewReader(body), &applyOut, &applyErr, aboard.Invocation(invocation)))
	require.Empty(t, applyErr.String(), "apply said something about the write")
	require.NotContains(t, stateTab(t, proj), keyQuiet)
	r.Archived = 99
	require.Equal(t, ActionPosted, Refresh(context.Background(), proj, r, time.Now(), &errOut).Action)
	tab = stateTab(t, proj)
	require.NotContains(t, tab, keyQuiet, "ape turned quiet back on over the person's choice")
	touched, ok := tab["touched"].(map[string]any)
	require.True(t, ok, "a changed view on a loud tab raised no dot")
	require.Equal(t, Actor, touched["by"])

	t.Logf("server log:\n%s", serverLog.String())
	require.NotContains(t, serverLog.String(), "warning", "the server logged a warning")
}

// An older ape's tab (no quietSet in its state: an aboard v0.3.0 server
// dropped quiet) is set quiet on its first changed write, once.
func TestUpsert_SetsQuietOnceOnAnOlderTab(t *testing.T) {
	old := sampleReport()
	old.Archived = 1
	oldState, err := normalized(buildState(old, time.Now()))
	require.NoError(t, err)
	data, ok := oldState["data"].(map[string]any)
	require.True(t, ok, "the state has no data map")
	delete(data, dataQuietSet)
	doc := map[string]any{"tabs": []any{map[string]any{"id": "t1", "key": TabKey, "name": TabName, "type": "ui", "state": oldState}}}

	changed, err := upsert(doc, sampleReport(), time.Now())
	require.NoError(t, err)
	require.True(t, changed)
	tab := tabAt(t, doc, 0)
	require.Equal(t, true, tab[keyQuiet], "the older tab was not set quiet")
	require.True(t, quietSet(tab["state"]))

	// Turned off since: a later changed write leaves it off.
	delete(tab, keyQuiet)
	next := sampleReport()
	next.Archived = 7
	changed, err = upsert(doc, next, time.Now())
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, tabAt(t, doc, 0), keyQuiet)
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
