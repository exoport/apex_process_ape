// Package sprintboard projects a sprint Summary onto the project's board as a
// single tab, and does so strictly best-effort.
//
// WHY THIS IS A SEPARATE PACKAGE FROM internal/sprint: the derivation is
// arithmetic over the tracker and belongs to anything that wants sprint state;
// writing it to a board is one consumer of that. Keeping them apart means
// `sprint` never grows a dependency on aboard, and a second consumer — a TUI,
// a status line — reuses the numbers without reusing the board.
//
// WHY THE CALLER IS THE COMMAND AND NOT sprint.Reconcile: the guarantee below
// is about a process's exit code and stdout, and those belong to the command
// layer. Reconcile is a library other code calls; giving it a side effect on a
// network service would make every caller inherit one.
package sprintboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/sprint"
)

// Document-level keys the board maintains. Named here rather than inlined
// because a typo in one of them writes a field the board ignores and leaves
// the real one stale — the silent half of the same failure `ui` has.
const (
	keyRev          = "rev"
	keyVersion      = "version"
	keyUpdatedAt    = "updatedAt"
	keyLastEditedBy = "lastEditedBy"
	// schemaVersion is the document version this writes. It matches
	// aboard.SchemaVersion; a mismatch makes the board warn about a document
	// from a future it does not know.
	schemaVersion = 1
)

const (
	// TabKey identifies the tab across refreshes. BY KEY, never by name — the
	// human may rename the tab — and never by index.
	TabKey = "apex-sprint"
	// TabName is what the tab is called when this creates it. Renaming it is
	// the human's to do and survives every refresh.
	TabName = "Sprint"
	// Actor is who the write is attributed to. Not "claude", and not "human":
	// the board reserves "human" for a person acting in the browser, and it
	// carries powers an agent must not have.
	Actor = "ape-sprint"
	// postTimeout bounds the attempt to reach a running board. Short, because
	// reconcile runs at every boundary that moves a story; a board that is
	// there but wedged must not become the slow step.
	postTimeout = 2 * time.Second
	// invocation is the command name the board names in its own messages when
	// ape is the host. Same string as framework.AboardInvocation and apecmd's
	// aboardArgv0; not imported from either, because framework imports this
	// package's sibling and apecmd imports both.
	invocation = "ape aboard"
)

// Refresh writes the sprint summary to the project's board, if there is one.
//
// IT NEVER RETURNS AN ERROR, AND THAT IS THE POINT. `ape sprint reconcile`
// sits on mutation paths inside apex-review-story, apex-code-review and
// apex-epic-batch-review, where the framework's operating rules read a
// non-zero exit as a CONTENT VERDICT: it converts a defer into a patch, raises
// unfixed_patches and demotes the story. A board that is down, slow or
// mid-restart must not be able to reach that outcome — the failure would show
// up as wrong review verdicts on projects that happen to run a board, and not
// on projects that do not, which is close to undebuggable.
//
// Anything it has to say goes to errOut, never stdout: skills parse stdout
// with --output-format json.
func Refresh(ctx context.Context, projectRoot string, s sprint.Summary, now time.Time, errOut io.Writer) {
	// A panic here would take down a command whose exit code is a verdict, so
	// even a bug in this file or below it stays inside it. aboard has shipped
	// a panic on a malformed file before (stampedHash, v0.1.1), so this is a
	// demonstrated failure mode rather than a hypothetical one.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(errOut, "sprint board refresh panicked, ignored: %v\n", r)
		}
	}()
	if err := refresh(ctx, projectRoot, s, now); err != nil {
		fmt.Fprintf(errOut, "sprint board refresh skipped: %v\n", err)
	}
}

// refresh is the fallible half, so the guarantee above is one deferred recover
// and one error check rather than a rule every branch has to remember.
//
// TWO WRITE PATHS, and which one runs is decided by whether a board answers:
//
//  1. A board is listening → POST it. This is the only way a browser already
//     showing the board learns of the change: the server emits its SSE frames
//     from its own write path and never watches the file, so a page would
//     otherwise sit stale until somebody reloaded it. It also gets the write a
//     real compare-and-set, so a concurrent change from the browser is refused
//     rather than clobbered.
//  2. Nothing is listening → write the file. Most projects are here most of
//     the time, and it means the tab is current BEFORE anyone starts a board:
//     `ape aboard serve` opens on a populated dashboard rather than an empty
//     one that fills in at the next story boundary.
//
// The fallback is entered ONLY when nothing is listening. A server that is
// there and refuses — a 409, a timeout, an HTTP error — stops this instead,
// because writing the file behind a live server is the one move here that can
// destroy another writer's work, and a 409 IS another writer's work.
//
// The file path writes through a temp file and a rename, so a concurrent
// reader sees the whole old document or the whole new one, never a torn read.
func refresh(ctx context.Context, projectRoot string, s sprint.Summary, now time.Time) error {
	root := aboard.Root(projectRoot)

	// The one guard that always applies, and it is a stat: a project with no
	// board is not given one. Starting a board is the human's call, and so is
	// initialising it.
	state := root.StateFile("")
	if _, err := os.Stat(state); err != nil {
		return nil //nolint:nilerr // no board here; not a failure, and not ours to create
	}

	doc, err := readDoc(state)
	if err != nil {
		return err
	}
	if err := upsert(doc, s, now); err != nil {
		return err
	}

	// Try the SERVER first, but only where one has ever run — the instance
	// record is a stat, and a project that never started a board must not pay
	// for a dial it cannot use.
	if _, err := os.Stat(root.InstanceFile("")); err == nil {
		postErr := post(ctx, root, doc)
		switch {
		case postErr == nil:
			return nil
		case !boardNotAnswering(postErr):
			// The server is THERE and refused, timed out, or lost a
			// compare-and-set. Writing the file behind its back now is the one
			// move that can destroy somebody else's change — a 409 IS another
			// writer's work — so this stops instead.
			return postErr
		}
		// Nothing listening. Fall through and write the file.
	}

	// Stamped the way the server stamps its own writes, so the document a
	// later `serve` picks up is indistinguishable from one it wrote itself.
	// `rev` in particular has to advance: it is the compare-and-set token, and
	// leaving it where it was would let a browser holding the same base
	// overwrite this write without ever being told.
	//
	// Only on this path: on the server path the server does its own stamping,
	// and the `rev` it was handed is the base it compares against.
	if err := stampWrite(doc, now); err != nil {
		return err
	}

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal board document: %w", err)
	}
	return writeFileAtomic(state, append(body, '\n'))
}

// post hands the document to a running board, which is the only way an open
// browser learns of it: the server emits its SSE frames from its own write
// path and never watches the file, so a page already showing the board would
// otherwise sit stale until somebody reloaded it.
//
// Bounded by a short timeout because `sprint reconcile` runs at every boundary
// that moves a story and must not become the slow step.
func post(ctx context.Context, root aboard.Root, doc map[string]any) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal board document: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()
	return aboard.Apply(ctx, root, "", aboard.ApplyOptions{
		By:    Actor,
		Label: "sprint reconcile refreshed the tracker projection",
	}, aboard.WebFS(), bytes.NewReader(body), io.Discard, io.Discard,
		aboard.Invocation(invocation))
}

// boardNotAnswering reports that nothing is listening at the recorded address
// — a crashed or SIGKILLed server, since aboard removes its instance record
// only on a graceful shutdown. That is the ordinary "no board is running"
// state, and the file write handles it.
//
// Matched on a failure to DIAL rather than on a platform errno: ECONNREFUSED
// is spelled differently on Windows, and any dial failure means the same thing
// here. Every other error means a server IS there, which is exactly when
// writing the file behind it would be wrong.
func boardNotAnswering(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// stampWrite applies the document-level fields the board maintains on every
// write: the schema version, the next revision, the time, and who wrote it.
func stampWrite(doc map[string]any, now time.Time) error {
	rev, ok := doc[keyRev].(float64)
	if !ok {
		return errors.New("board document has no rev")
	}
	doc[keyRev] = int(rev) + 1
	doc[keyVersion] = schemaVersion
	// The board's own format, to the millisecond — not RFC3339, which would
	// read as a different writer's document.
	doc[keyUpdatedAt] = now.UTC().Format("2006-01-02T15:04:05.000Z")
	doc[keyLastEditedBy] = Actor
	return nil
}

// writeFileAtomic writes through a temporary file in the same directory and
// renames over the target, so a concurrent reader — the server, the browser's
// next fetch, another reconcile — sees either the whole old document or the
// whole new one, never a torn read.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".aboard-*.json")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename has succeeded
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func readDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A run directory without a document: `serve` says to run init,
			// and creating a board is not this command's call.
			return nil, nil //nolint:nilnil // handled by the caller's nil check
		}
		return nil, fmt.Errorf("read board document: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse board document: %w", err)
	}
	return doc, nil
}

// upsert replaces the sprint tab's state in place, or appends the tab when it
// is not there. Everything else in the document is carried through untouched —
// this write must not be able to disturb another tab.
func upsert(doc map[string]any, s sprint.Summary, now time.Time) error {
	if doc == nil {
		return errors.New("no board document")
	}
	tabs, ok := doc["tabs"].([]any)
	if !ok {
		return errors.New("board document has no tabs array")
	}
	state := buildState(s, now)

	for _, raw := range tabs {
		tab, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		if key, _ := tab["key"].(string); key == TabKey {
			// Name and note are NOT rewritten: the human may have renamed the
			// tab, and a refresher that undoes that on every call is a bug
			// they cannot fix.
			tab["type"] = "ui"
			tab["state"] = state
			doc["tabs"] = tabs
			return nil
		}
	}

	id, err := allocID(doc)
	if err != nil {
		return err
	}
	doc["tabs"] = append(tabs, map[string]any{
		"id":   id,
		"key":  TabKey,
		"name": TabName,
		"type": "ui",
		"note": "Sprint state, derived from sprint-status.yaml by `ape sprint reconcile`. " +
			"A view, not a record: if this and the tracker disagree, this is wrong. " +
			"Edits here are overwritten on the next refresh.",
		"state": state,
	})
	return nil
}

// allocID takes the board-wide counter. Never "highest in this container + 1":
// a reused id silently re-points an instruction at a different object, which
// is why the counter is board-wide and the server refuses to let it regress.
func allocID(doc map[string]any) (string, error) {
	n, ok := doc["nextId"].(float64)
	if !ok {
		return "", errors.New("board document has no nextId")
	}
	doc["nextId"] = n + 1
	return fmt.Sprintf("ab%d", int(n)), nil
}
