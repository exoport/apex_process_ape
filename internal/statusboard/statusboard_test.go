package statusboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

func docWith(tabs []any, nextID int) map[string]any {
	return map[string]any{
		"version": float64(1), "rev": float64(3),
		"nextId": float64(nextID), "tabs": tabs,
	}
}

// THE TEST THAT MATTERS MOST for a `ui` tab: an unknown prop draws nothing
// at all while `apply` still reports success, so a tab can be wrong AND
// green. This runs the produced document through the board's own
// write-time checks — the ones `ape aboard apply --check` runs — and
// demands silence, for the empty report and the full one.
func TestTheTabPassesTheBoardsOwnWriteChecks(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]*apexv1.StatusReport{"full": sampleReport(), "empty": emptyReport()} {
		doc := docWith([]any{}, 1)
		_, err := upsert(doc, r, time.Now())
		require.NoError(t, err)
		body, err := json.Marshal(doc)
		require.NoError(t, err)

		var out, errOut bytes.Buffer
		// Check:true posts nothing and needs no server; it is purely the checks.
		err = aboard.Apply(context.Background(), aboard.Root(t.TempDir()), "",
			aboard.ApplyOptions{By: Actor, Check: true, Strict: true}, aboard.WebFS(),
			bytes.NewReader(body), &out, &errOut, aboard.Invocation(invocation))
		require.NoError(t, err, name)
		require.NotContains(t, errOut.String(), "warning:",
			"%s: the status tab writes state no renderer reads:\n%s", name, errOut.String())
	}
}

// Found by key, never by name and never by index — the human may rename it.
func TestRefreshUpdatesTheSameTabRatherThanOpeningASecond(t *testing.T) {
	t.Parallel()
	doc := docWith([]any{}, 1)
	_, err := upsert(doc, sampleReport(), time.Now())
	require.NoError(t, err)
	require.Len(t, doc["tabs"], 1)

	// The human renames it and writes their own note.
	tab := tabAt(t, doc, 0)
	tab["name"] = "Where we are"
	tab["note"] = "mine"

	r := sampleReport()
	r.Next = "something else"
	changed, err := upsert(doc, r, time.Now())
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, doc["tabs"], 1, "a second status tab was opened")

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
	doc := docWith([]any{other}, 2)
	_, err := upsert(doc, sampleReport(), time.Now())
	require.NoError(t, err)

	require.Len(t, doc["tabs"], 2)
	require.Equal(t, "hello", asMap(t, tabAt(t, doc, 0)["state"])["text"])
	require.Equal(t, "ab2", tabAt(t, doc, 1)["id"], "the new tab took the board-wide counter")
	next, ok := doc["nextId"].(float64)
	require.True(t, ok, "nextId is not a number")
	require.Equal(t, 3, int(next), "nextId did not advance")
}

// SILENT (D60): every agent write to a running board raises the tab's dot,
// so a refresh whose view did not change writes nothing — not even a new
// freshness stamp, which lives in state.data and is left out of the
// comparison.
func TestAnUnchangedViewIsNotWritten(t *testing.T) {
	t.Parallel()
	doc := docWith([]any{}, 1)
	first := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	changed, err := upsert(doc, sampleReport(), first)
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = upsert(doc, sampleReport(), first.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, changed, "a refresh that changes only the time wrote the tab")
	require.Contains(t, mustJSON(t, doc), "2026-10-17T09:00:00Z", "the stamp moved on an unchanged view")

	r := sampleReport()
	r.Changes[0].Status = apexv1.StatusVerified
	changed, err = upsert(doc, r, first.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, changed, "a changed view was not written")
	require.Contains(t, mustJSON(t, doc), "2026-10-17T11:00:00Z")
}

// Freshness has to be VISIBLE, and in a node the ui renderer reads: a
// `heartbeat` or `readOnly` would be stored and drawn by nothing.
func TestFreshnessIsRenderedNotStoredInAnInertField(t *testing.T) {
	t.Parallel()
	doc := docWith([]any{}, 1)
	now := time.Date(2026, 8, 29, 14, 5, 0, 0, time.UTC)
	_, err := upsert(doc, sampleReport(), now)
	require.NoError(t, err)

	state := asMap(t, tabAt(t, doc, 0)["state"])
	require.NotContains(t, state, "heartbeat")
	require.NotContains(t, state, "readOnly")
	require.Equal(t, "2026-08-29T14:05:00Z", asMap(t, state["data"])[dataRefreshed])
	require.Contains(t, mustJSON(t, state["root"]), `{"bind":"refreshed"}`, "nothing on the tab shows the stamp")
}

/* ---------- the guarantee: a command must never notice a broken board ---------- */

// PATH 2 — nothing is listening, so the file is written.
func TestRefreshWritesWithNoServerRunning(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)

	var errOut bytes.Buffer
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, ActionWrote, out.Action)
	require.Equal(t, filepath.Join(proj, ".aboard", "aboard.json"), out.Where)

	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1)
	require.Equal(t, TabKey, tabAt(t, doc, 0)["key"])
	require.NotContains(t, tabAt(t, doc, 0), "touched", "a file write raised the dot")

	// The second refresh with the same view writes nothing.
	again := Refresh(context.Background(), proj, sampleReport(), time.Now().Add(time.Minute), &errOut)
	require.Equal(t, ActionUnchanged, again.Action)
	rev, ok := readState(t, proj)["rev"].(float64)
	require.True(t, ok)
	require.Equal(t, 2, int(rev), "an unchanged view still advanced rev")
}

// The document is stamped the way the server stamps its own writes. rev
// in particular must advance: it is the compare-and-set token.
func TestRefreshStampsTheDocumentLikeTheServerWould(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":7,"nextId":1,"tabs":[]}`)
	Refresh(context.Background(), proj, sampleReport(), time.Now(), io.Discard)

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

// Most projects never run a board and must pay nothing — and must not be
// given one. Starting a board is the human's call.
func TestRefreshDoesNothingWithoutABoard(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	require.False(t, Present(proj))
	var errOut bytes.Buffer
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, ActionNoBoard, out.Action)
	require.NoDirExists(t, filepath.Join(proj, ".aboard"))
}

// `.aboard/` without a document is a board someone has half-removed.
// Creating one here is not this command's call.
func TestRefreshDoesNotCreateAMissingDocument(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".aboard", "run"), 0o755))
	require.True(t, Present(proj))

	var errOut bytes.Buffer
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, ActionNoBoard, out.Action)
	require.NoFileExists(t, filepath.Join(proj, ".aboard", "aboard.json"))
}

// A document that will not parse is somebody else's to fix. It must not
// fail the command, and it must not be destroyed on the way past.
func TestRefreshSurvivesAMalformedDocumentAndLeavesItAlone(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, "{ not json")

	var errOut bytes.Buffer
	var out Outcome
	require.NotPanics(t, func() {
		out = Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	})
	require.Contains(t, errOut.String(), "status board refresh skipped")
	require.Equal(t, ActionSkipped, out.Action)
	require.NotEmpty(t, out.Detail)

	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.Equal(t, "{ not json", string(body), "the unreadable document was overwritten")
}

// A document with no rev has no compare-and-set token to advance.
func TestRefreshRefusesADocumentWithNoRev(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"nextId":1,"tabs":[]}`)

	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Contains(t, errOut.String(), "no rev")
	require.Empty(t, readState(t, proj)["tabs"], "a refused write still changed the document")
}

// A nil report is a caller's bug; it stays inside the refresh.
func TestRefreshSurvivesAPanic(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	var errOut bytes.Buffer
	var out Outcome
	require.NotPanics(t, func() {
		out = Refresh(context.Background(), proj, nil, time.Now(), &errOut)
	})
	require.Contains(t, errOut.String(), "panicked, ignored")
	require.Equal(t, ActionSkipped, out.Action)
}

// An unwritable board directory is a permissions problem, not a command
// failure. It reports and moves on.
func TestRefreshSurvivesAnUnwritableBoard(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a directory's mode does not gate file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test relies on")
	}
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	dir := filepath.Join(proj, ".aboard")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var errOut bytes.Buffer
	require.NotPanics(t, func() {
		Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	})
	require.Contains(t, errOut.String(), "status board refresh skipped")
}

// The write is atomic, and leaves no temporary file beside it.
func TestRefreshLeavesAParseableDocument(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	for i := range 5 {
		r := sampleReport()
		r.Archived = i // a different view each time, so each one writes
		Refresh(context.Background(), proj, r, time.Now(), io.Discard)
	}
	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1, "five refreshes opened more than one tab")
	rev, ok := doc["rev"].(float64)
	require.True(t, ok)
	require.Equal(t, 6, int(rev))

	entries, err := os.ReadDir(filepath.Join(proj, ".aboard"))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".aboard-", "a temp file survived the write")
	}
}

// Refresh writes nowhere but errOut: anything on stdout would land in
// output skills parse as JSON.
func TestRefreshWritesOnlyToTheWriterItIsGiven(t *testing.T) {
	proj := boardOnDisk(t, "{ not json")
	stdout := captureStdout(t)
	var errOut bytes.Buffer
	Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, stdout(), "the refresh wrote to stdout")
	require.NotEmpty(t, errOut.String())
}

// PATH 1 — a board IS listening, so the document is POSTed and the file is
// left for the server to write.
func TestRefreshPostsToARunningBoard(t *testing.T) {
	t.Parallel()
	posted := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			posted <- body
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
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String())
	require.Equal(t, ActionPosted, out.Action)
	require.Equal(t, srv.URL, out.Where)
	require.Len(t, posted, 1, "a running board was posted to %d times, want 1", len(posted))
	require.Contains(t, string(<-posted), TabKey, "the posted document has no status tab")

	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.JSONEq(t, document, string(body), "the file was written behind a live server")
}

// A server that is THERE and refuses is not a reason to write the file: a
// 409 is another writer's work.
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
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.NotEmpty(t, errOut.String(), "a refusal should be visible on stderr")
	require.Equal(t, ActionSkipped, out.Action)

	body, err := os.ReadFile(filepath.Join(proj, ".aboard", "aboard.json"))
	require.NoError(t, err)
	require.JSONEq(t, document, string(body), "the refresh wrote around a 409")
}

// A CRASHED board leaves its instance record behind, so the record is not
// evidence that anything is listening. Nothing answers: the file write
// takes over.
func TestRefreshFallsBackWhenTheRecordedBoardIsDead(t *testing.T) {
	t.Parallel()
	proj := boardOnDisk(t, `{"version":1,"rev":1,"nextId":1,"tabs":[]}`)
	// Port 1: privileged, so nothing here can bind it and the dial is
	// refused every time (a bound-then-freed port races a busy machine).
	recordInstance(t, proj, "http://127.0.0.1:1")

	var errOut bytes.Buffer
	out := Refresh(context.Background(), proj, sampleReport(), time.Now(), &errOut)
	require.Empty(t, errOut.String(), "a dead board should fall back quietly")
	require.Equal(t, ActionWrote, out.Action)

	doc := readState(t, proj)
	require.Len(t, doc["tabs"], 1, "the fallback did not write the tab")
	require.Equal(t, Actor, doc["lastEditedBy"])
}

/* ---------- helpers ---------- */

// recordInstance points the project's instance record at url, which is
// where aboard.Apply posts.
func recordInstance(t *testing.T, proj, url string) {
	t.Helper()
	rec, err := json.Marshal(map[string]any{
		"app": "ape-aboard", "project": proj, "url": url, "pid": 4242,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(proj, ".aboard", "run", "instance.json"), rec, 0o644,
	))
}

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

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// boardOnDisk writes a board document and nothing else — no instance
// record, no server.
func boardOnDisk(t *testing.T, document string) string {
	t.Helper()
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".aboard", "run"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(proj, ".aboard", "aboard.json"), []byte(document), 0o644,
	))
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

// captureStdout redirects os.Stdout and returns a reader for what was
// written. Process-global, so its callers do not run in parallel.
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
