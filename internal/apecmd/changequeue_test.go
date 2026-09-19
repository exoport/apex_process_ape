package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/stretchr/testify/require"
)

// --queue writes the operator's words down and commits the record
// itself, so the next preflight still sees a clean tree.
func TestRunQueue_WritesTheRequestAndCommitsIt(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	o := changeOptions{request: "the CLI reference is out of date", cwdFlag: root, queue: true}

	require.NoError(t, runQueue(context.Background(), o))

	cfg := resolveProjectConfig(root)
	res, err := deferred.New(cfg.Paths.Deferred).Load(deferred.LoadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)

	rec := res.Records[0]
	require.Equal(t, "the CLI reference is out of date", firstLine(rec.Body),
		"the body's FIRST LINE is the request, verbatim — a field an older ape's close would drop")
	require.Equal(t, queueOwner, rec.Owner)
	require.Equal(t, queueTrigger, rec.Trigger)

	subjects := changeLogSubjects(t, root)
	require.Equal(t, "chore(deferred): queue "+rec.ID, subjects[0])
	require.Contains(t, commitBodyOf(t, root, "HEAD"), "Generator: ape change --queue")

	left, err := exec.CommandContext(context.Background(), "git", "-C", root,
		"status", "--porcelain").Output()
	require.NoError(t, err)
	require.Empty(t, string(left), "the tree is clean for the next preflight")
}

// Ids derive from the date and the content, so the same words twice in
// one day collide — which is the duplicate check, not a problem with it.
func TestRunQueue_RefusesARequestAlreadyQueued(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	o := changeOptions{request: "the same request", cwdFlag: root, queue: true}

	require.NoError(t, runQueue(context.Background(), o))
	err := runQueue(context.Background(), o)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.ErrorContains(t, err, "already queued")
}

// A commit carrying `Fixes: <id>` says the work is done, so the request
// is not queued again.
func TestRunQueue_RefusesARequestACommitAlreadyDischarged(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	cfg := resolveProjectConfig(root)
	id := deferred.NewID(cfg.Date, "done already", "done already\n")

	require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("x\n"), 0o644))
	gitCommitAllWithBody(t, root, "fix(x): the work\n\nFixes: "+id+"\n")

	err := runQueue(context.Background(), changeOptions{
		request: "done already", cwdFlag: root, queue: true,
	})
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.ErrorContains(t, err, "has been done")
}

// The queue the drain will run: open lane records, minus the ones a
// commit already discharged. `Refs:` is NOT a discharge — it is what a
// partial run writes, and that is the one case where re-running is
// exactly right.
func TestDrainable_SkipsFixedButNotRefsOrOtherOwners(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	cfg := resolveProjectConfig(root)
	store := deferred.New(cfg.Paths.Deferred)

	res, err := store.IngestStructured([]deferred.Structured{
		{Title: "first", Owner: queueOwner, Trigger: queueTrigger, Body: "first\n"},
		{Title: "second", Owner: queueOwner, Trigger: queueTrigger, Body: "second\n"},
		{Title: "third", Owner: queueOwner, Trigger: queueTrigger, Body: "third\n"},
		{Title: "not the lane's", Owner: "platform", Trigger: "x", Body: "elsewhere\n"},
	}, deferred.IngestOptions{Date: cfg.Date})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("x\n"), 0o644))
	gitCommitAllWithBody(t, root,
		"fix(x): done\n\nFixes: "+res.Records[0].ID+"\nRefs: "+res.Records[1].ID+"\n")

	pending, err := drainable(context.Background(), cfg, store)
	require.NoError(t, err)
	// Order is the store's, not the ingest's, so this is a set.
	require.ElementsMatch(t, []string{res.Records[1].ID, res.Records[2].ID}, recordIDs(pending),
		"Fixes: discharges; Refs: does not; another owner's record is not the lane's")
}

// A record the drain carried as far as it goes — escalated, refused, or
// halted part way — is marked so the next drain does not retry it. The
// mark is a plain line, never a bullet: the store's closure recognisers
// read bullets, and one shaped like that would read as the record
// closing itself.
func TestMarkDrained_AppendsAPlainLineAndCommitsIt(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	cfg := resolveProjectConfig(root)
	store := deferred.New(cfg.Paths.Deferred)

	res, err := store.IngestStructured([]deferred.Structured{
		{Title: "escalate me", Owner: queueOwner, Trigger: queueTrigger, Body: "escalate me\n"},
	}, deferred.IngestOptions{Date: cfg.Date})
	require.NoError(t, err)
	rec := res.Records[0]
	gitCommitAll(t, root, "the record")

	require.NoError(t, markDrained(context.Background(), cfg, store, rec, "escalated lean-story"))

	reread, err := store.Find(rec.ID)
	require.NoError(t, err)
	require.Equal(t, "escalate me", firstLine(reread.Body), "the request is still the first line")
	require.Contains(t, reread.Body, drainedMarker+cfg.Date+" escalated lean-story")
	require.NotContains(t, reread.Body, "- "+drainedMarker, "a bullet would read as a closure")
	require.True(t, reread.IsOpen(), "a mark is not a discharge")
	require.True(t, drainedAlready(reread.Body))

	require.Equal(t, "chore(deferred): mark "+rec.ID, changeLogSubjects(t, root)[0])
	require.Contains(t, commitBodyOf(t, root, "HEAD"), "Generator: ape change --drain")
}

// A landed run needs no mark: its own commit carries Fixes:, and the
// release record closes the record from there.
func TestDrainMark_OnlyForOutcomesThatWereActedOnWithoutDischarging(t *testing.T) {
	require.Empty(t, drainMark(changeEnvelope{Outcome: "landed"}))
	require.Equal(t, "escalated lean-story",
		drainMark(changeEnvelope{Outcome: "escalated", Route: "lean-story"}))
	require.Equal(t, "escalated", drainMark(changeEnvelope{Outcome: "escalated"}))
	require.Equal(t, "refused", drainMark(changeEnvelope{Outcome: "refused"}))
	require.Equal(t, "halted finding too large to patch",
		drainMark(changeEnvelope{Outcome: "halted", BlockingCondition: "finding too large to patch"}))
	require.Empty(t, drainMark(changeEnvelope{}), "no contract, no mark")
}

func TestRunDrain_NothingQueuedIsNotAFailure(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	require.NoError(t, runDrain(context.Background(), changeOptions{cwdFlag: root, drain: true}))
}

// A record whose first line cannot be typed into the REPL is skipped
// with a reason rather than stopping the drain: it blocks nothing, and
// the rest of the queue is still runnable.
func TestRunDrain_SkipsARecordItCannotDispatch(t *testing.T) {
	root := changeProject(t, "_output/ape/")
	cfg := resolveProjectConfig(root)
	store := deferred.New(cfg.Paths.Deferred)

	_, err := store.IngestStructured([]deferred.Structured{
		{Title: "bad", Owner: queueOwner, Trigger: queueTrigger, Body: "ends in a backslash \\\n"},
	}, deferred.IngestOptions{Date: cfg.Date})
	require.NoError(t, err)
	gitCommitAll(t, root, "the record")

	// No dispatch happens, so this returns without spawning anything.
	require.NoError(t, runDrain(context.Background(), changeOptions{cwdFlag: root, drain: true}))
	require.Equal(t, []string{"the record", "base"}, changeLogSubjects(t, root))
}

func commitBodyOf(t *testing.T, root, rev string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "log", "-1", "--format=%B", rev)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out)
}

func gitCommitAllWithBody(t *testing.T, root, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-F", "-", "--cleanup=verbatim"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		if args[0] == "commit" {
			cmd.Stdin = strings.NewReader(message)
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}
