// Package statusboard projects `ape status` onto the project's board as a
// single tab, and does so strictly best-effort (D60).
//
// Ported from v0's internal/sprintboard (deleted at 5a5252a): the write
// path — a running board first, else the document on disk, stamped the way
// the server stamps its own writes — is that package's, unchanged in
// substance. The view is new: the v1 records `ape status --json` reports.
//
// WHY A SEPARATE PACKAGE FROM internal/apexv1: the status report is the
// records' arithmetic and belongs to anything that wants it; the board is
// one consumer. apexv1 never grows a dependency on aboard.
//
// WHY THE CALLER IS THE COMMAND: the guarantee below is about a process's
// exit code and stdout, and those belong to the command layer.
//
// THE BOARD IS NOT A RECORD. The tab is a view of what `ape status` derives
// from the records in git; when the two disagree, the tab is wrong, and
// nothing reads it back.
package statusboard

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
	"reflect"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// Document-level keys the board maintains. Named here rather than inlined
// because a typo in one of them writes a field the board ignores and leaves
// the real one stale.
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
	TabKey = "ape-status"
	// TabName is what the tab is called when this creates it. Renaming it is
	// the human's to do and survives every refresh.
	TabName = "APEX status"
	// Actor is who the write is attributed to. Not "claude", and not "human":
	// the board reserves "human" for a person acting in the browser, and it
	// carries powers an agent must not have.
	Actor = "ape-status"
	// postTimeout bounds the attempt to reach a running board. Short, because
	// the refresh runs after every state-changing v1 command; a board that is
	// there but wedged must not become the slow step.
	postTimeout = 2 * time.Second
	// invocation is the command name the board names in its own messages when
	// ape is the host. Same string as apecmd's aboardArgv0; not imported,
	// because apecmd imports this package.
	invocation = "ape aboard"
)

// QUIET (D60 "the status tab updates silently"; RC3-PLAN Q1, D75).
//
// The tab always carries a tab-level `quiet: true`. The aboard change that
// honours it — an agent write to a quiet tab is journaled but never stamps
// `touched`, the unread dot on the tab and the banner inside it — is on its
// own branch; ape bumps github.com/exoport/aboard once it is released.
//
// What the pinned aboard v0.3.0 does with it, measured against its own
// server in TestQuiet_WhatAboardV030DoesToday: nothing, silently. `quiet`
// is not a field of its tab struct, so the server drops it from the
// document it persists, warns about it nowhere (not in `apply`'s output,
// not in its log), and stamps `touched` on every tab an agent write
// changes (pkg/aboard/tabs.go reconcileDoc, guarantee 2) — so until that
// release the tab still raises the dot. A write to the FILE keeps the key.
//
// What keeps the dot down already: a refresh whose view is unchanged
// writes nothing at all — a missing `quiet` is never a reason to write,
// since v0.3.0's server drops it every time — and a write to the file (no
// board running) never passes through the server's reconcile, so it raises
// no dot either. Only a changed view posted to a running board does.
const keyQuiet = "quiet"

// What Refresh did, for `ape status --board` to say. The automatic refresh
// after a command says nothing unless it failed.
const (
	// ActionPosted: a running board took the write.
	ActionPosted = "posted"
	// ActionWrote: no board answered; the document on disk was rewritten.
	ActionWrote = "wrote"
	// ActionUnchanged: the tab already shows this view; nothing was written.
	ActionUnchanged = "unchanged"
	// ActionNoBoard: the project has no board document; nothing was created.
	ActionNoBoard = "no-board"
	// ActionSkipped: the refresh failed; Detail says why. Never an error.
	ActionSkipped = "skipped"
)

// Outcome is what one refresh did.
type Outcome struct {
	Action string `json:"action"`
	// Where is the board's URL (posted) or the document's path (wrote,
	// unchanged).
	Where  string `json:"where,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Present reports whether the project has a board directory: the one stat
// a command pays before deciding to refresh at all (D60: never runs when
// .aboard/ is absent).
func Present(projectRoot string) bool {
	info, err := os.Stat(aboard.Root(projectRoot).Dir())
	return err == nil && info.IsDir()
}

// Refresh writes the status view to the project's board, if there is one.
//
// IT NEVER RETURNS AN ERROR, AND THAT IS THE POINT. It runs after commands
// whose exit codes the framework's skills branch on (the diagnostic
// actions), so a board that is down, slow or mid-restart must not be able to
// change one — the failure would show up as wrong verdicts on projects that
// happen to run a board, and not on projects that do not.
//
// Anything it has to say goes to errOut, never stdout: skills parse stdout
// with --json.
func Refresh(ctx context.Context, projectRoot string, r *apexv1.StatusReport, now time.Time, errOut io.Writer) (out Outcome) {
	// A panic here would take down a command whose exit code is a verdict, so
	// even a bug in this file or below it stays inside it. aboard has shipped
	// a panic on a malformed file before (stampedHash, v0.1.1).
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(errOut, "status board refresh panicked, ignored: %v\n", rec)
			out = Outcome{Action: ActionSkipped, Detail: fmt.Sprint(rec)}
		}
	}()
	out, err := refresh(ctx, projectRoot, r, now)
	if err != nil {
		fmt.Fprintf(errOut, "status board refresh skipped: %v\n", err)
		return Outcome{Action: ActionSkipped, Detail: err.Error()}
	}
	return out
}

// refresh is the fallible half, so the guarantee above is one deferred
// recover and one error check rather than a rule every branch has to
// remember.
//
// TWO WRITE PATHS, and which one runs is decided by whether a board answers:
//
//  1. A board is listening → POST it. This is the only way a browser already
//     showing the board learns of the change: the server emits its SSE
//     frames from its own write path. It also gets the write a real
//     compare-and-set, so a concurrent change from the browser is refused
//     rather than clobbered.
//  2. Nothing is listening → write the file, so the tab is current before
//     anyone starts a board.
//
// The fallback is entered ONLY when nothing is listening. A server that is
// there and refuses — a 409, a timeout, an HTTP error — stops this instead,
// because writing the file behind a live server is the one move here that
// can destroy another writer's work.
func refresh(ctx context.Context, projectRoot string, r *apexv1.StatusReport, now time.Time) (Outcome, error) {
	root := aboard.Root(projectRoot)

	// A project with no board is not given one. Starting a board is the
	// human's call, and so is initialising it.
	state := root.StateFile("")
	if _, err := os.Stat(state); err != nil {
		return Outcome{Action: ActionNoBoard}, nil //nolint:nilerr // no board here; not a failure, and not ours to create
	}

	doc, err := readDoc(state)
	if err != nil {
		return Outcome{}, err
	}
	changed, err := upsert(doc, r, now)
	if err != nil {
		return Outcome{}, err
	}
	if !changed {
		// Every write an agent makes to a running board raises the tab's dot
		// (QUIET above), so the one write that is certain not to is the one
		// that is not made.
		return Outcome{Action: ActionUnchanged, Where: state}, nil
	}

	// Try the SERVER first, but only where one has ever run — the instance
	// record is a stat, and a project that never started a board must not
	// pay for a dial it cannot use.
	if _, err := os.Stat(root.InstanceFile("")); err == nil {
		postErr := post(ctx, root, doc)
		switch {
		case postErr == nil:
			return Outcome{Action: ActionPosted, Where: instanceURL(root)}, nil
		case !boardNotAnswering(postErr):
			// The server is THERE and refused, timed out, or lost a
			// compare-and-set. Writing the file behind its back now is the
			// one move that can destroy somebody else's change.
			return Outcome{}, postErr
		}
		// Nothing listening. Fall through and write the file.
	}

	// Stamped the way the server stamps its own writes, so the document a
	// later `serve` picks up is indistinguishable from one it wrote itself.
	// `rev` in particular has to advance: it is the compare-and-set token.
	// Only on this path: on the server path the server does its own
	// stamping, and the `rev` it was handed is the base it compares against.
	if err := stampWrite(doc, now); err != nil {
		return Outcome{}, err
	}

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Outcome{}, fmt.Errorf("marshal board document: %w", err)
	}
	if err := writeFileAtomic(state, append(body, '\n')); err != nil {
		return Outcome{}, err
	}
	return Outcome{Action: ActionWrote, Where: state}, nil
}

// post hands the document to a running board. Bounded by a short timeout,
// because the refresh runs after every state-changing command.
func post(ctx context.Context, root aboard.Root, doc map[string]any) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal board document: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()
	return aboard.Apply(ctx, root, "", aboard.ApplyOptions{
		By:    Actor,
		Label: "ape refreshed the APEX status view",
	}, aboard.WebFS(), bytes.NewReader(body), io.Discard, io.Discard,
		aboard.Invocation(invocation))
}

// instanceURL is where the running board said it listens, for the message
// `ape status --board` prints; the record's path when it cannot be read.
func instanceURL(root aboard.Root) string {
	path := root.InstanceFile("")
	data, err := os.ReadFile(path)
	if err != nil {
		return path
	}
	var rec struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.URL == "" {
		return path
	}
	return rec.URL
}

// boardNotAnswering reports that nothing is listening at the recorded
// address — a crashed or SIGKILLed server, since aboard removes its instance
// record only on a graceful shutdown. Matched on a failure to DIAL rather
// than on a platform errno: ECONNREFUSED is spelled differently on Windows.
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
// renames over the target, so a concurrent reader sees either the whole old
// document or the whole new one, never a torn read.
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
			return nil, nil //nolint:nilnil // handled by upsert's nil check
		}
		return nil, fmt.Errorf("read board document: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse board document: %w", err)
	}
	return doc, nil
}

// upsert replaces the status tab's state in place, or appends the tab when
// it is not there; changed is false when the tab already shows this view.
// Everything else in the document is carried through untouched — this write
// must not be able to disturb another tab.
func upsert(doc map[string]any, r *apexv1.StatusReport, now time.Time) (changed bool, err error) {
	if doc == nil {
		return false, errors.New("no board document")
	}
	tabs, ok := doc["tabs"].([]any)
	if !ok {
		return false, errors.New("board document has no tabs array")
	}
	state, err := normalized(buildState(r, now))
	if err != nil {
		return false, err
	}

	for _, raw := range tabs {
		tab, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		if key, _ := tab["key"].(string); key == TabKey {
			// The view is compared WITHOUT its freshness stamp, which lives
			// in state.data alone: a refresh that would change nothing but
			// the time writes nothing, so it raises no dot.
			if tab["type"] == "ui" && sameView(tab["state"], state) {
				return false, nil
			}
			// Name and note are NOT rewritten: the human may have renamed
			// the tab, and a refresher that undoes that on every call is a
			// bug they cannot fix.
			tab["type"] = "ui"
			tab["state"] = state
			tab[keyQuiet] = true
			doc["tabs"] = tabs
			return true, nil
		}
	}

	id, err := allocID(doc)
	if err != nil {
		return false, err
	}
	tab := map[string]any{
		"id":   id,
		"key":  TabKey,
		"name": TabName,
		"type": "ui",
		"note": "APEX status, derived from the records in git by `ape status`, refreshed by ape " +
			"after every state-changing v1 command. A view, not a record: if this and " +
			"`ape status` disagree, this is wrong. Edits here are overwritten on the next refresh.",
		"state": state,
		// QUIET above: honoured from the aboard release after v0.3.0.
		keyQuiet: true,
	}
	doc["tabs"] = append(tabs, tab)
	return true, nil
}

// sameView compares two tab states on their component tree, ignoring
// state.data (where the freshness stamp is bound from).
func sameView(cur any, next map[string]any) bool {
	m, ok := cur.(map[string]any)
	if !ok {
		return false
	}
	return reflect.DeepEqual(m["root"], next["root"])
}

// normalized round-trips the state through JSON, so it compares equal to
// the same state read back from the document (numbers as float64, slices
// as []any).
func normalized(state map[string]any) (map[string]any, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal status view: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("re-read status view: %w", err)
	}
	return out, nil
}

// allocID takes the board-wide counter. Never "highest in this container +
// 1": a reused id silently re-points an instruction at a different object.
func allocID(doc map[string]any) (string, error) {
	n, ok := doc["nextId"].(float64)
	if !ok {
		return "", errors.New("board document has no nextId")
	}
	doc["nextId"] = n + 1
	return fmt.Sprintf("ab%d", int(n)), nil
}
