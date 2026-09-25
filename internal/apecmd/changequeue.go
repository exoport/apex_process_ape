package apecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/change"
	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// The queue and the drain: a request written down now, and every
// written-down request run later.
//
// `--queue` exists because a maintenance request usually arrives while
// someone is in the middle of something else, and the lane's preflight
// refuses a dirty tree. Writing it into the deferred store keeps the
// operator's words without starting anything.
//
// The record is an ordinary `[Defer]` record whose BODY'S FIRST LINE is
// the request, verbatim. Not a dedicated field: an older `ape deferred
// close` would drop a field it does not know, and bodies survive every
// version. Every later marker — a discharge line, the drain's own mark —
// is appended AFTER that first line, and every reader takes the first
// line only.
//
// `--drain` then runs each open maintenance record as its own change,
// and its stopping rule is the ordinary way it ends rather than the rare
// one: any halt, refusal or dead dispatch leaves residue, every later
// run would refuse at preflight, so the drain stops at the first run
// that leaves the tree dirty and names what it did not reach.

// The queued record's provenance. Both are the vocabulary the framework
// reads, not ape's invention.
const (
	queueOwner   = "maintenance"
	queueTrigger = "operator request"
)

// runQueue writes the request down and commits it.
func runQueue(ctx context.Context, o changeOptions) error {
	if o.request == "" {
		return usageErr(errors.New("--queue needs a request to write down: pass one, or " +
			"--request-file, or --request-file -"))
	}
	cfg := tryResolveProjectConfig(o.cwdFlag)
	if cfg == nil {
		return usageErr(errors.New("no _apex/config.yaml in this directory or any parent"))
	}
	store := deferred.New(cfg.Paths.Deferred)

	// An id already in the store, or already discharged by a commit, is
	// the same request twice. Ids derive from the date and the content,
	// so the same words on the same day collide by design — which is the
	// check, not a problem with it.
	id := deferred.NewID(cfg.Date, o.request, o.request+"\n")
	if _, err := store.Find(id); err == nil {
		return usageErr(fmt.Errorf("this request is already queued as %s", id))
	}
	fixed, err := fixesTrailerIDs(ctx, cfg.Root)
	if err == nil && fixed[id] {
		return usageErr(fmt.Errorf("a commit already carries `Fixes: %s`, so this request has "+
			"been done", id))
	}

	res, err := store.IngestStructured([]deferred.Structured{{
		Title:   o.request,
		Owner:   queueOwner,
		Trigger: queueTrigger,
		Body:    o.request + "\n",
	}}, deferred.IngestOptions{Skill: changeSkill, Date: cfg.Date})
	cfg.StampUsed()
	if err != nil {
		return failErr(fmt.Errorf("write the queued record: %w", err))
	}
	rec := res.Records[0]
	rel, relErr := filepath.Rel(cfg.Root, rec.Path)
	if relErr != nil {
		return failErr(fmt.Errorf("resolve %s: %w", rec.Path, relErr))
	}

	// ape commits its own write, so the next preflight sees a clean tree.
	commit, err := change.CommitFiles(ctx, cfg.Root, runlog.ChangesRoot(cfg.Root), "queue",
		fmt.Sprintf("chore(deferred): queue %s\n\nGenerator: ape change --queue\n", rec.ID),
		[]string{filepath.ToSlash(rel)})
	if err != nil {
		return failErr(fmt.Errorf("commit the queued record: %w", err))
	}
	fmt.Printf("queued %s — %s\n", rec.ID, commit.SHA)
	return nil
}

// runDrain runs every open maintenance record as its own change.
func runDrain(ctx context.Context, o changeOptions) error {
	cfg := tryResolveProjectConfig(o.cwdFlag)
	if cfg == nil {
		return usageErr(errors.New("no _apex/config.yaml in this directory or any parent"))
	}
	store := deferred.New(cfg.Paths.Deferred)
	pending, err := drainable(ctx, cfg, store)
	if err != nil {
		return usageErr(err)
	}
	if len(pending) == 0 {
		fmt.Println("nothing queued for the maintenance lane")
		return nil
	}

	for i := range pending {
		rec := &pending[i]
		text, lineErr := validateTypedLine([]byte(firstLine(rec.Body)), "the record's first line")
		if lineErr != nil {
			// The record cannot be dispatched as a request. Reported and
			// skipped rather than stopping the drain: it blocks nothing,
			// and the rest of the queue is still runnable.
			fmt.Fprintf(os.Stderr, "⚠ skipping %s: %s\n", rec.ID, lineErr)
			continue
		}
		each := o
		each.drain = false
		each.request = text
		each.fixes = rec.ID

		run, runErr := changeOnce(ctx, each)
		if run == nil {
			return runErr
		}
		// The mark goes on BEFORE the drain stops, so a second drain does
		// not re-run a record this one already carried as far as it goes.
		if mark := drainMark(run.env); mark != "" {
			if err := markDrained(ctx, cfg, store, *rec, mark); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ %s could not be marked: %s\n", rec.ID, err)
			}
		}
		if len(run.env.Residue) > 0 {
			// The ordinary way a drain ends. Every later run would refuse
			// at preflight over this tree, so stopping here and naming
			// what was not reached beats failing the rest one by one.
			fmt.Fprintf(os.Stderr,
				"drain stopped at %s: it left %d %s in the tree, saved under %s\n",
				rec.ID, len(run.env.Residue), plural(len(run.env.Residue), "path", "paths"),
				run.env.ChangeDir)
			if rest := pending[i+1:]; len(rest) > 0 {
				fmt.Fprintf(os.Stderr, "  not reached: %s\n", strings.Join(recordIDs(rest), ", "))
			}
			return runErr
		}
		if runErr != nil && !isMarkedOutcome(run.env.Outcome) {
			// A failure that left a clean tree — a dead dispatch, an
			// unreadable contract — is still a failure to report.
			return runErr
		}
	}
	return nil
}

// drainable is the queue: open records owned by the lane, minus the ones
// a commit already says are done.
//
// The `Fixes:` trailer is the only discharge that counts. `Refs:` is
// what a PARTIAL run writes, and reading it as a discharge would drop a
// record whose work is half done — the one case where re-running is
// exactly what should happen.
func drainable(ctx context.Context, cfg *apexcfg.Resolved, store *deferred.Store) ([]deferred.Record, error) {
	res, err := store.Load(deferred.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("read the deferred store: %w", err)
	}
	fixed, err := fixesTrailerIDs(ctx, cfg.Root)
	if err != nil {
		return nil, err
	}
	var out []deferred.Record
	selected := deferred.Select(res.Records, deferred.Filter{Owner: queueOwner})
	for i := range selected {
		if fixed[selected[i].ID] || drainedAlready(selected[i].Body) {
			continue
		}
		out = append(out, selected[i])
	}
	return out, nil
}

// fixesTrailerIDs reads every id a commit claims to have discharged.
func fixesTrailerIDs(ctx context.Context, root string) (map[string]bool, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "log", "--format=%(trailers:key=Fixes,valueonly)")
	cmd.Dir = root
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			// A repository with no commits yet. Nothing is discharged,
			// which is the honest answer rather than a failure.
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read Fixes: trailers: %w", err)
	}
	ids := map[string]bool{}
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

// drainMark is the line a record gets when the drain carried it as far
// as it goes without discharging it.
//
// A landed run needs none: its commit carries `Fixes:`, and the release
// record closes the record from there. Everything else — escalated,
// refused, or halted after a partial landing — has been ACTED on and
// must not be retried by the next drain.
func drainMark(env changeEnvelope) string {
	switch env.Outcome {
	case change.StatusEscalated:
		if env.Route != "" {
			return "escalated " + env.Route
		}
		return "escalated"
	case change.StatusRefused:
		return "refused"
	case change.StatusHalted:
		if env.BlockingCondition != "" {
			return "halted " + env.BlockingCondition
		}
		return "halted"
	default:
		return ""
	}
}

// drainedMarker is the line's prefix. A plain line, never a bullet: the
// store's closure recognisers read bullets, and a marker shaped like one
// would be read as the record closing itself.
const drainedMarker = "drained: "

func drainedAlready(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), drainedMarker) {
			return true
		}
	}
	return false
}

// markDrained appends the line and commits the record.
func markDrained(
	ctx context.Context, cfg *apexcfg.Resolved, store *deferred.Store,
	rec deferred.Record, mark string,
) error {
	body := rec.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	rec.Body = body + drainedMarker + cfg.Date + " " + mark + "\n"
	cfg.StampUsed()
	path, err := store.Write(rec)
	if err != nil {
		return fmt.Errorf("write the drain mark: %w", err)
	}
	rel, err := filepath.Rel(cfg.Root, path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	_, err = change.CommitFiles(ctx, cfg.Root, runlog.ChangesRoot(cfg.Root), "mark",
		fmt.Sprintf("chore(deferred): mark %s\n\nGenerator: ape change --drain\n", rec.ID),
		[]string{filepath.ToSlash(rel)})
	if err != nil && !change.IsNothingStaged(err) {
		return fmt.Errorf("commit the drain mark: %w", err)
	}
	return nil
}

// isMarkedOutcome reports the outcomes a drain treats as handled: they
// were acted on and recorded, so the drain moves to the next record
// rather than reporting a failure.
func isMarkedOutcome(outcome string) bool {
	switch outcome {
	case change.StatusEscalated, change.StatusRefused:
		return true
	default:
		return false
	}
}

func recordIDs(records []deferred.Record) []string {
	out := make([]string, 0, len(records))
	for i := range records {
		out = append(out, records[i].ID)
	}
	return out
}
