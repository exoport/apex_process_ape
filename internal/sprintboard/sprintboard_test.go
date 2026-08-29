package sprintboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/stretchr/testify/require"
)

func sampleSummary() sprint.Summary {
	return sprint.Summarize([]sprint.Row{
		{Key: "epic-1", Status: "in-progress", Kind: sprint.KindEpic, Epic: 1},
		{Key: "1-1", Status: "done", Kind: sprint.KindStory, Epic: 1},
		{Key: "1-2", Status: "in-progress", Kind: sprint.KindStory, Epic: 1},
		{Key: "1-3", Status: "blocked", Kind: sprint.KindStory, Epic: 1},
	})
}

func docWith(t *testing.T, tabs []any, nextID int) map[string]any {
	t.Helper()
	return map[string]any{
		"version": float64(1), "rev": float64(3),
		"nextId": float64(nextID), "tabs": tabs,
	}
}

// THE TEST THAT MATTERS MOST for a `ui` tab: an unknown prop draws nothing at
// all while `apply` still reports success, so a tab can be wrong AND green.
// This runs the produced document through the board's own write-time checks —
// the same ones `ape aboard apply --check` runs — and demands silence.
func TestTheTabPassesTheBoardsOwnWriteChecks(t *testing.T) {
	t.Parallel()
	doc := docWith(t, []any{}, 1)
	require.NoError(t, upsert(doc, sampleSummary(), time.Now()))
	body, err := json.Marshal(doc)
	require.NoError(t, err)

	var out, errOut bytes.Buffer
	// Check:true posts nothing and needs no server; it is purely the checks.
	err = aboard.Apply(context.Background(), aboard.Root(t.TempDir()), "",
		aboard.ApplyOptions{By: Actor, Check: true}, aboard.WebFS(),
		bytes.NewReader(body), &out, &errOut, aboard.Invocation("ape aboard"))
	require.NoError(t, err)
	require.NotContains(t, errOut.String(), "warning:",
		"the sprint tab writes state no renderer reads:\n%s", errOut.String())
}

// Found by key, never by name and never by index — the human may rename it.
func TestRefreshUpdatesTheSameTabRatherThanOpeningASecond(t *testing.T) {
	t.Parallel()
	doc := docWith(t, []any{}, 1)
	require.NoError(t, upsert(doc, sampleSummary(), time.Now()))
	require.Len(t, doc["tabs"], 1)

	// The human renames it and writes their own note.
	tab := tabAt(t, doc, 0)
	tab["name"] = "Where we are"
	tab["note"] = "mine"

	require.NoError(t, upsert(doc, sampleSummary(), time.Now()))
	require.Len(t, doc["tabs"], 1, "a second sprint tab was opened")

	after := tabAt(t, doc, 0)
	require.Equal(t, "Where we are", after["name"], "the refresher renamed the human's tab back")
	require.Equal(t, "mine", after["note"], "the refresher overwrote the human's note")
	require.Equal(t, TabKey, after["key"])
}

// This write must not be able to disturb another tab.
func TestRefreshLeavesEveryOtherTabAlone(t *testing.T) {
	t.Parallel()
	other := map[string]any{
		"id": "ab1", "key": "welcome", "name": "Welcome",
		"type": "notes", "state": map[string]any{"text": "hello"},
	}
	doc := docWith(t, []any{other}, 2)
	require.NoError(t, upsert(doc, sampleSummary(), time.Now()))

	require.Len(t, doc["tabs"], 2)
	require.Equal(t, "hello", asMap(t, tabAt(t, doc, 0)["state"])["text"])
	require.Equal(t, "ab2", tabAt(t, doc, 1)["id"], "the new tab took the board-wide counter")
	next, ok := doc["nextId"].(float64)
	require.True(t, ok, "nextId is not a number")
	require.Equal(t, 3, int(next), "nextId did not advance")
}

// The counts on the tab are the counts in the tracker. If they can disagree,
// the tab is wrong — so the numbers are asserted where they are rendered.
func TestTheRenderedCountsMatchTheTracker(t *testing.T) {
	t.Parallel()
	doc := docWith(t, []any{}, 1)
	s := sampleSummary()
	require.NoError(t, upsert(doc, s, time.Now()))
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	rendered := string(body)

	require.Contains(t, rendered, `"title":"Stories — 3"`)
	require.Contains(t, rendered, `"title":"In flight — 2"`)
	require.Contains(t, rendered, `"title":"Attention — 1"`)
	require.Contains(t, rendered, `"title":"Epics — 1"`)
}

// Freshness has to be VISIBLE. state.heartbeat is declared by kanban and read
// by nothing in the ui renderer, so it would be stored and drawn by nothing —
// a strip that renders nothing cannot say when the view was last true.
func TestFreshnessIsRenderedNotStoredInAnInertField(t *testing.T) {
	t.Parallel()
	doc := docWith(t, []any{}, 1)
	now := time.Date(2026, 8, 29, 14, 5, 0, 0, time.UTC)
	require.NoError(t, upsert(doc, sampleSummary(), now))

	state := asMap(t, tabAt(t, doc, 0)["state"])
	require.NotContains(t, state, "heartbeat", "ui reads no heartbeat; writing one warns on every refresh")
	require.NotContains(t, state, "readOnly", "ui reads no readOnly; writing one warns on every refresh")

	body, err := json.Marshal(doc)
	require.NoError(t, err)
	require.Contains(t, string(body), "2026-08-29T14:05:00Z", "the write time is not on the tab")
}

/* ---------- the guarantee: reconcile must never notice a broken board ---------- */

// PATH 2 — nothing is listening, so the file is written. No board has ever
// been started here, and the tab still lands: `ape aboard serve` then opens on
// a populated dashboard instead of an empty one.
func TestRefreshWritesWithNoServerRunning(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, errOut.String())

	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1)
	require.Equal(t, TabKey, tabAt(t, doc, 0)["key"])
}

// The document is stamped the way the server stamps its own writes, so a
// `serve` started afterwards picks up something indistinguishable from one it
// wrote. rev in particular must advance: it is the compare-and-set token, and
// leaving it would let a browser on the same base overwrite this silently.
func TestRefreshStampsTheDocumentLikeTheServerWould(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":7,"nextId":1,"tabs":[]}`)
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), io.Discard)

	doc := readState(t, proj)
	rev, ok := doc["rev"].(float64)
	require.True(t, ok)
	require.Equal(t, 8, int(rev), "rev did not advance")
	require.Equal(t, Actor, doc["lastEditedBy"])
	version, ok := doc["version"].(float64)
	require.True(t, ok)
	require.Equal(t, 1, int(version))
	stamp, ok := doc["updatedAt"].(string)
	require.True(t, ok)
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, stamp,
		"the board's own millisecond format, not RFC3339")
}

// Most projects never run a board and must pay nothing — and must not be given
// one. Starting a board is the human's call.
func TestRefreshDoesNothingWithoutABoard(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.NoDirExists(t, filepath.Join(proj, ".aboard"))
}

// `.aboard/` without a document is a board someone has half-removed. `serve`
// tells them to run init; creating one here is not this command's call.
func TestRefreshDoesNotCreateAMissingDocument(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".aboard", "run"), 0o755))

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.NoFileExists(t, filepath.Join(proj, ".aboard", "aboard.json"))
}

// A document that will not parse is somebody else's to fix. It must not fail a
// reconcile, and it must not be destroyed on the way past.
func TestRefreshSurvivesAMalformedDocumentAndLeavesItAlone(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, "{ not json")

	var errOut bytes.Buffer
	require.NotPanics(t, func() {
		Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	})
	require.Contains(t, errOut.String(), "sprint board refresh skipped")

	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.Equal(t, "{ not json", string(body), "the unreadable document was overwritten")
}

// A document with no rev has no compare-and-set token to advance. Refused
// rather than guessed at, and the file is left as it was.
func TestRefreshRefusesADocumentWithNoRev(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"nextId":1,"tabs":[]}`)

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Contains(t, errOut.String(), "no rev")
	require.Empty(t, readState(t, proj)["tabs"], "a refused write still changed the document")
}

// An unwritable board directory is a permissions problem, not a reconcile
// failure. It reports and moves on.
func TestRefreshSurvivesAnUnwritableBoard(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test relies on")
	}
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	dir := filepath.Join(proj, ".aboard")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var errOut bytes.Buffer
	require.NotPanics(t, func() {
		Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	})
	require.Contains(t, errOut.String(), "sprint board refresh skipped")
}

// The write is atomic: a reader sees the whole old document or the whole new
// one. Asserted by reading it back as JSON, which a torn write would fail.
func TestRefreshLeavesAParseableDocument(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	for range 5 {
		Refresh(context.Background(), proj, sampleSummary(), time.Now(), io.Discard)
	}
	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1, "five refreshes opened more than one tab")

	// And no temporary files were left beside it.
	entries, err := os.ReadDir(filepath.Join(proj, ".aboard"))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".aboard-", "a temp file survived the write")
	}
}

// Refresh's whole contract is that it writes nowhere but errOut. Anything it
// printed to stdout would land in output skills parse as JSON.
func TestRefreshWritesOnlyToTheWriterItIsGiven(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, "{ not json")
	stdout := captureStdout(t)
	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, stdout(), "the refresh wrote to stdout")
}

// PATH 1 — a board IS listening, so the document is POSTed and the file is
// left for the server to write. That is the only way a browser already showing
// the board learns of the change: the server emits SSE from its own write path
// and never watches the file.
func TestRefreshPostsToARunningBoard(t *testing.T) {
	t.Parallel()
	var posted atomic.Int32
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			got, _ = io.ReadAll(r.Body)
			posted.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"rev":2,"updatedAt":"2026-08-29T00:00:00.000Z"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	const document = `{"version":1,"rev":1,"nextId":1,"tabs":[]}`
	proj := boardOnDisk(t, document)
	recordInstance(t, proj, srv.URL)

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, int32(1), posted.Load(), "a running board was not posted to")
	require.Contains(t, string(got), TabKey, "the posted document has no sprint tab")

	// The file is the SERVER's to write. Going behind it would bypass the
	// compare-and-set that posting exists to get.
	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.JSONEq(t, document, string(body), "the file was written behind a live server")
}

// A server that is THERE and refuses is not a reason to write the file: a 409
// is another writer's work, and going around it is the one move here that can
// destroy it. It reports and stops.
func TestRefreshDoesNotWriteBehindAServerThatRefuses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"stale base"}`))
	}))
	t.Cleanup(srv.Close)

	const document = `{"version":1,"rev":1,"nextId":1,"tabs":[]}`
	proj := boardOnDisk(t, document)
	recordInstance(t, proj, srv.URL)

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.NotEmpty(t, errOut.String(), "a refusal should be visible on stderr")

	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.JSONEq(t, document, string(body), "reconcile wrote around a 409")
}

// A CRASHED board leaves its instance record behind — aboard removes it only
// on a graceful shutdown — so the record is not evidence that anything is
// listening. Nothing answers, so the file write takes over.
func TestRefreshFallsBackWhenTheRecordedBoardIsDead(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, ln.Close()) // free it: nothing answers here now

	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	recordInstance(t, proj, "http://127.0.0.1:"+strconv.Itoa(addr.Port))

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleSummary(), time.Now(), &errOut)
	require.Empty(t, errOut.String(), "a dead board should fall back quietly")

	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1, "the fallback did not write the tab")
	require.Equal(t, Actor, doc["lastEditedBy"])
}

/* ---------- helpers ---------- */

// recordInstance points the project's instance record at url, which is where
// aboard.Apply posts.
func recordInstance(t *testing.T, proj, url string) {
	t.Helper()
	rec, err := json.Marshal(map[string]any{
		"app": "ape-aboard", "project": proj, "url": url, "pid": 4242,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(proj, ".aboard", "run", "instance.json"), rec, 0o644))
}

// asMap and tabAt assert their way into the document, so a shape change fails
// the test with a message rather than a panic.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected an object, got %T", v)
	return m
}

func tabAt(t *testing.T, doc map[string]any, i int) map[string]any {
	t.Helper()
	tabs, ok := doc["tabs"].([]any)
	require.True(t, ok, "tabs is not an array")
	require.Greater(t, len(tabs), i)
	return asMap(t, tabs[i])
}

// boardOnDisk writes a board document and nothing else — no instance record,
// no server. That is the state this feature is now designed for.
func boardOnDisk(t *testing.T, document string) string {
	t.Helper()
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".aboard", "run"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(proj, ".aboard", "aboard.json"), []byte(document), 0o644))
	return proj
}

func readState(t *testing.T, proj string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	return doc
}

// captureStdout redirects os.Stdout and returns a reader for what was written.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	prev := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	return func() string {
		os.Stdout = prev
		_ = w.Close()
		out := <-done
		_ = r.Close()
		return out
	}
}
