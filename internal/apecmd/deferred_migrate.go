package apecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// repairSkill is the framework skill `ape deferred repair` dispatches.
// The prompt lives there — versioned and reviewable — rather than as a Go
// string literal, which is what keeps ape free of an HTTP client, API
// credentials and a model constant.
const repairSkill = "apex-defer-repair"

// repairModel is the tier the judgment phase runs on. Passed through the
// existing PTY task runner's --model, not an API call.
const repairModel = "opus"

func newDeferredMigrateCmd() *cobra.Command {
	var (
		cwdFlag        string
		fromFlag       string
		recoverDeleted bool
		dryRun         bool
		format         string
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Convert the legacy deferred-work.md into record files",
		Long: `Convert a single-file deferred-work.md into one file per record.

Four properties, all asserted rather than assumed:

  VERIFIED BEFORE WRITE   N records parsed must equal N files written and
                          every body must survive byte-for-byte, or NOTHING
                          is written. A lossy conversion that passed
                          silently is the one failure here that git cannot
                          undo.
  IDEMPOTENT              detected from disk state — does the store hold
                          records, is the legacy file already a stub. No
                          version marker is stored, so nothing can drift.
  NEVER DELETES THE SOURCE  the legacy file becomes a short signpost; its
                          content stays in git.
  NO COMMIT               the files land in the working tree. You commit
                          them, as one commit or two, however you like.

A record whose tail does not match the expected shape keeps its full text
as the body and takes its title from the first line — 26 of 109 records in
the reference ledger are free-form, so that path always runs. Nothing is
dropped and nothing is guessed at; 'ape deferred verify' flags them, and
'ape deferred repair' completes or retires them.

--recover-deleted mines the ledger's git history for records removed from
it and writes them straight to closed/. The ledger's own preamble
documents 'git log -p' as the recovery route; this automates exactly that.
Failure to read history is a warning, never fatal.`,
		Args: cobra.NoArgs,
		Example: "  ape deferred migrate --dry-run\n" +
			"  ape deferred migrate --recover-deleted",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			store := deferred.New(cfg.Paths.Deferred)
			from := fromFlag
			if from == "" {
				from = cfg.Paths.DeferredLegacy
			}
			if from == "" {
				return usageErr(errors.New("no legacy ledger resolved; pass --from"))
			}
			if _, err := os.Stat(from); err != nil {
				return usageErr(fmt.Errorf("%s does not exist", from))
			}

			// The clean gate is scoped to the paths this migration touches,
			// not the whole tree. With no commit to isolate ape's work,
			// `git status` is the only separation between it and the
			// operator's WIP — so those paths have to start clean, while
			// unrelated WIP elsewhere is nobody's business.
			if !dryRun {
				if dirty := dirtyPaths(cmd.Context(), cfg.Root, migrationPaths(cfg.Paths.Deferred, from)); len(dirty) > 0 {
					return usageErr(fmt.Errorf(
						"migration paths have uncommitted changes, so `git status` could not tell ape's work from yours: %s",
						strings.Join(dirty, ", ")))
				}
			}

			res, err := store.Migrate(cmd.Context(), deferred.MigrateOptions{
				From:           from,
				RecoverDeleted: recoverDeleted,
				DryRun:         dryRun,
			})
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			emitMigrateHuman(cmd.OutOrStdout(), res, cfg.Root)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&fromFlag, "from", "", "Legacy ledger path (default: resolved from config)")
	cmd.Flags().BoolVar(&recoverDeleted, "recover-deleted", false,
		"Also recover records removed from the ledger, from git history, into closed/")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Parse and verify, writing nothing")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

// migrationPaths is the write set: the store and the legacy ledger. This
// is deliberately disjoint from what `ape framework update` installs
// (.claude/skills, _apex/*, CLAUDE.md), which is what makes the two
// order-independent.
func migrationPaths(store, legacy string) []string {
	return []string{store, legacy}
}

func emitMigrateHuman(w io.Writer, res *deferred.MigrateResult, root string) {
	if res.AlreadyDone {
		fmt.Fprintln(w, "already migrated — the store holds records and the legacy file is a stub")
		return
	}
	verb := "migration:"
	if res.DryRun {
		verb = "migration (dry run):"
	}
	fmt.Fprintf(w, "%s %d record(s) -> %s\n", verb, res.RecordsIn, relTo(root, res.To))
	if res.Recovered > 0 {
		fmt.Fprintf(w, "           %d recovered from git history -> %s\n",
			res.Recovered, relTo(root, filepath.Join(res.To, deferred.ClosedDirName)))
	}
	if res.FreeForm > 0 {
		fmt.Fprintf(w, "           %d free-form record(s) kept verbatim, flagged for `ape deferred verify`\n",
			res.FreeForm)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	if res.DryRun {
		fmt.Fprintln(w, "\nverify: parsed and round-tripped cleanly. Nothing written (--dry-run).")
		return
	}
	fmt.Fprintf(w, "  verify:  %d in -> %d out, bodies byte-identical ... OK\n",
		res.RecordsIn, res.RecordsOut)
	if res.StubWritten {
		fmt.Fprintf(w, "           %s -> stub\n", relTo(root, res.From))
	}
	emitGitAddHint(w, root, res.Paths)
}

// emitGitAddHint prints what a commit message would have carried. This is
// a requirement, not a nicety: nothing here commits, so this output is the
// only record of what changed and where.
func emitGitAddHint(w io.Writer, root string, paths []string) {
	if len(paths) == 0 {
		return
	}
	seen := map[string]bool{}
	var roots []string
	for _, p := range paths {
		// Collapse per-record files to their directory: a `git add` line
		// with 227 paths in it is not usable.
		dir := relTo(root, filepath.Dir(p))
		if !seen[dir] {
			seen[dir] = true
			roots = append(roots, dir)
		}
	}
	fmt.Fprintln(w, "\nnothing committed. review and commit when you are happy:")
	fmt.Fprintf(w, "  git add %s\n", strings.Join(roots, " "))
}

func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func newDeferredRepairCmd() *cobra.Command {
	var (
		cwdFlag string
		dryRun  bool
		force   bool
		format  string
	)
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Complete or retire free-form records (judgment, on opus)",
		Long: `Dispatch the free-form records to a framework skill for completion.

The deterministic migration cannot finish a record that has no fields to
read. Deciding whether a messy note is real work, what it points at, and
whether it is already dead is JUDGMENT — so an LLM does it, in its own
phase, never inside the verified migration. That separation is what keeps
the migration assertable and revertible: a model's output can never
invalidate a byte-identity check.

Mechanism: this spawns ` + repairSkill + ` on ` + repairModel + ` through
the same PTY task runner ` + "`ape task`" + ` uses. The prompt lives in the
framework as a versioned, reviewable skill rather than a Go string
literal, so ape gains no HTTP client, no credentials and no model
constant.

Two guards this command adds:

  It REFUSES without a TTY unless --force. It spends real money, and it
  should not do that from a script that did not ask — the same refusal
  'ape framework setup' already makes rather than seeding silently.

  The on-disk record count MUST NOT FALL. "discard never deletes" is an
  absolute rule that otherwise lives only in a prompt, and a prompt is not
  an enforcement mechanism. If records vanish, this says so and names
  them, so you can restore those paths before committing anything.

Nothing is committed.`,
		Args:    cobra.NoArgs,
		Example: "  ape deferred repair --dry-run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			store := deferred.New(cfg.Paths.Deferred)
			before, err := store.Load(deferred.LoadOptions{IncludeClosed: true})
			if err != nil {
				return err
			}
			freeForm := 0
			for i := range before.Records {
				if before.Records[i].FreeForm && before.Records[i].IsOpen() {
					freeForm++
				}
			}

			plan := repairPlan{
				Skill:    repairSkill,
				Model:    repairModel,
				Records:  len(before.Records),
				FreeForm: freeForm,
				Store:    cfg.Paths.Deferred,
			}
			if f := output.Format(format); f != output.FormatHuman {
				if err := output.Print(cmd.OutOrStdout(), f, plan); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(),
					"repair: %d free-form record(s) of %d -> %s on %s\n",
					plan.FreeForm, plan.Records, plan.Skill, plan.Model)
			}
			if freeForm == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to repair")
				return nil
			}
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "--dry-run: no session spawned")
				return nil
			}
			if !force && !isInteractiveStdout() {
				return usageErr(errors.New(
					"refusing to spawn a paid " + repairModel +
						" session without a TTY — pass --force if that is what you meant"))
			}

			opts := repairTaskOptions(cfg.Root, plan)
			if err := runTask(cmd.Context(), opts); err != nil {
				return err
			}
			return assertNoRecordsLost(cmd.OutOrStdout(), store, before)
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan without spawning a session")
	cmd.Flags().BoolVar(&force, "force", false, "Spawn the session even without a TTY")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

// repairPlan is what the judgment phase is about to do, emitted before it
// runs so an operator can see the cost before paying it.
type repairPlan struct {
	Skill    string `json:"skill"     yaml:"skill"`
	Model    string `json:"model"     yaml:"model"`
	Records  int    `json:"records"   yaml:"records"`
	FreeForm int    `json:"free_form" yaml:"free_form"`
	Store    string `json:"store"     yaml:"store"`
}

// repairTaskOptions builds the invocation. Split out so a test can assert
// the dispatch shape without spawning claude — the way `task` and
// `pipeline` invocation shapes are already tested here.
func repairTaskOptions(projectRoot string, plan repairPlan) taskOptions {
	return taskOptions{
		skill:       plan.Skill,
		model:       resolveModelArg(plan.Model),
		args:        "--autonomous",
		projectRoot: projectRoot,
		// The judgment phase must not commit: the operator groups the
		// migration and the repair into however many commits they want.
		skillNoCommit: true,
	}
}

// assertNoRecordsLost is the ape-side post-condition on the prompt's
// "discard never deletes" rule.
func assertNoRecordsLost(w io.Writer, store *deferred.Store, before *deferred.LoadResult) error {
	after, err := store.Load(deferred.LoadOptions{IncludeClosed: true})
	if err != nil {
		return err
	}
	if len(after.Records) >= len(before.Records) {
		fmt.Fprintf(w, "post-check: %d record(s) before, %d after — none lost\n",
			len(before.Records), len(after.Records))
		emitGitAddHint(w, filepath.Dir(store.Dir), []string{store.Dir})
		return nil
	}
	present := map[string]bool{}
	for i := range after.Records {
		present[after.Records[i].ID] = true
	}
	var missing []string
	for i := range before.Records {
		if !present[before.Records[i].ID] {
			missing = append(missing, before.Records[i].ID)
		}
	}
	return fmt.Errorf(
		"the repair pass LOST %d record(s), which it is never allowed to do — restore them before committing: %s",
		len(missing), strings.Join(missing, ", "))
}

// isInteractiveStdout reports whether stdout is a terminal, mirroring the
// gate `pickBootstrapper` uses to refuse seeding a config silently.
func isInteractiveStdout() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// dirtyPaths returns the given paths that have uncommitted changes,
// relative to root. A non-repo or an unavailable git yields no dirty
// paths: there is nothing to protect and nothing to compare against.
func dirtyPaths(ctx context.Context, root string, paths []string) []string {
	args := []string{"status", "--porcelain", "--"}
	for _, p := range paths {
		if p != "" {
			args = append(args, p)
		}
	}
	if len(args) == 3 {
		return nil
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil
	}
	var dirty []string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		if len(line) > 3 {
			dirty = append(dirty, strings.TrimSpace(line[3:]))
		}
	}
	return dirty
}
