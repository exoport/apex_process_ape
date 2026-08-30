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
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// repairSkill is the framework skill `ape deferred repair` dispatches.
// The prompt lives there — versioned and reviewable — rather than as a Go
// string literal, which is what keeps ape free of an HTTP client, API
// credentials and a model constant.
//
// repairSkillLegacy is what the framework called it before it was renamed
// to match the `ape deferred` noun. Both are accepted, and that is not
// politeness — it is what stops this from being a lockstep release. A hard
// switch would mean a new ape against an older framework finds nothing, and
// an older ape against the renamed framework finds nothing, so the two
// repos would have to ship in the same hour. Resolving against what is
// actually installed removes the constraint instead of documenting it.
const (
	repairSkill       = "apex-deferred-repair"
	repairSkillLegacy = "apex-defer-repair"
)

// resolveRepairSkill picks whichever repair skill this project has, current
// name first.
//
// A real dispatch is already safe without this: runTask builds a single-step
// spec and pipeline.Run calls PreflightSkills, so an unresolvable skill exits
// 2 with a clear message and never reaches claude. What this adds is
// three things that check cannot give:
//
//   - It chooses BETWEEN the two names, which is the whole point of
//     tolerating the rename.
//   - It fires under --dry-run, where the runner never runs at all.
//     Otherwise a dry run prints a plan naming a skill that does not exist
//     and reports "no session spawned", which reads as fine, and the real
//     run fails later. A dry run that cannot tell you the dispatch would
//     fail is not doing its job.
//   - It fires before the TTY refusal, so an operator learns the skill is
//     missing instead of being told to pass --force first and finding out
//     after that.
func resolveRepairSkill(projectRoot string) (name string, found bool) {
	for _, candidate := range []string{repairSkill, repairSkillLegacy} {
		if _, _, ok := framework.ResolveSkill(candidate, projectRoot); ok {
			return candidate, true
		}
	}
	return repairSkill, false
}

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

  VERIFIED BEFORE WRITE   every significant line of the ledger must come
                          back out in something this writes — a record
                          body, a record's source_heading, or the preamble
                          file — N records parsed must equal N files
                          written, and every body must survive a round
                          trip, or NOTHING is written. The first of those
                          three is checked against the LEDGER, not just
                          against the parser's own output: a conversion
                          that silently lost text while reporting success
                          is the one failure here that git cannot undo.
                          Line endings are the single normalisation — a
                          CRLF ledger yields LF bodies.
  IDEMPOTENT              detected from disk state — does the store hold
                          records, is the legacy file already a stub. No
                          version marker is stored, so nothing can drift.
  NEVER DELETES THE SOURCE  the legacy file becomes a short signpost; its
                          content stays in git, and its preamble is kept
                          verbatim beside the records.
  NO COMMIT               the files land in the working tree. You commit
                          them, as one commit or two, however you like.

Each record carries the ledger heading it sat under verbatim, in
source_heading, alongside the source/source_story/created extracted from
it. A heading holds more than three fields can take, and that remainder is
often attribution.

A record whose tail does not match the expected shape keeps its full text
as the body and takes its title from the first line, with NO fields parsed
from it — 'free_form: true' means nothing in it was interpreted. Any real
ledger has records that land there. What fraction is not quoted here on
purpose: it is a property of your corpus, and a number measured against
someone else's would only invite you to trust it. 'ape deferred verify'
flags them and 'ape deferred repair' completes or retires them.

The migration MOVES cited text out of the folder it was living in. A
repo-wide gate scoped to that folder — anchor counts, citation ratchets —
will drop the moment this lands, without anything regressing. The
completion output says how much moved so that drop is explainable.

Re-migrating after a parser fix: record ids are derived from the ledger,
so a corrected parse yields different ids and the two sets cannot be
merged. Restore the ledger from git and delete the store directory; this
refuses to run into a store that still holds records rather than
interleaving two migrations on disk.

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
						strings.Join(dirty, ", "),
					))
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
		fmt.Fprintf(w, "  to re-migrate after a parser fix: restore %s from git and remove %s\n",
			relTo(root, res.From), relTo(root, res.To))
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
	if res.Closed > 0 {
		fmt.Fprintf(w, "           %d arrived already resolved -> %s\n",
			res.Closed, relTo(root, filepath.Join(res.To, deferred.ClosedDirName)))
	}
	if res.FreeForm > 0 {
		fmt.Fprintf(w, "           %d free-form record(s) kept verbatim, flagged for `ape deferred verify`\n",
			res.FreeForm)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	if res.DryRun {
		fmt.Fprintln(w, "\nverify: every ledger line accounted for, records round-trip. Nothing written (--dry-run).")
		emitMigrateRelocationNote(w, res, root)
		return
	}
	fmt.Fprintf(w, "  verify:  %d in -> %d out, every ledger line accounted for ... OK\n",
		res.RecordsIn, res.RecordsOut)
	if res.PreambleWritten {
		fmt.Fprintf(w, "           ledger preamble kept at %s\n",
			relTo(root, filepath.Join(res.To, deferred.PreambleFileName)))
	}
	if res.StubWritten {
		fmt.Fprintf(w, "           %s -> stub\n", relTo(root, res.From))
	}
	emitMigrateRelocationNote(w, res, root)
	emitGitAddHint(w, root, res.Paths)
}

// emitMigrateRelocationNote says that cited text has changed folders.
//
// Not a nicety and not a warning: a project whose gates count anchors or
// citations under one folder watches that count fall the moment this runs,
// with nothing actually regressed. The first field migration moved 456 KB
// and took a ratchet from 1039 to 995, which read as a regression and cost
// a revert to work out. Saying it here is cheaper than explaining it after.
func emitMigrateRelocationNote(w io.Writer, res *deferred.MigrateResult, root string) {
	if res.Bytes == 0 {
		return
	}
	fmt.Fprintf(w,
		"\nnote: %d bytes of cited text moved out of %s into %s.\n"+
			"      A gate scoped to the old folder will count fewer anchors now. That is\n"+
			"      the move, not a regression — re-baseline it rather than chasing it.\n",
		res.Bytes, relTo(root, filepath.Dir(res.From)), relTo(root, res.To))
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
		// listing every record file in it is not usable.
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
constant. ` + repairSkillLegacy + ` is accepted as the pre-rename name, so
an older framework install still works.

Three guards this command adds:

  It REFUSES when neither skill is installed, --dry-run included. A real
  dispatch is already caught by the runner's skill preflight, but a dry
  run never reaches the runner — so without this it would print a plan
  naming a skill that does not exist, say "no session spawned", and read
  as fine.

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

			skill, skillFound := resolveRepairSkill(cfg.Root)
			plan := repairPlan{
				Skill:    skill,
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
			// Checked before --dry-run returns: finding out whether this
			// would work is most of what a dry run is for.
			if !skillFound {
				return fmt.Errorf(
					"no repair skill installed: neither %s nor %s resolves under %s or ~/.claude/skills/ — "+
						"run `ape framework update` to install it",
					repairSkill, repairSkillLegacy, framework.ProjectSkillsDir,
				)
			}
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "--dry-run: no session spawned")
				return nil
			}
			if !force && !isInteractiveStdout() {
				return usageErr(errors.New(
					"refusing to spawn a paid " + repairModel +
						" session without a TTY — pass --force if that is what you meant",
				))
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
		len(missing), strings.Join(missing, ", "),
	)
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
