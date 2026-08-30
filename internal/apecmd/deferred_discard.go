package apecmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
)

// newDeferredDiscardCmd is the writer for the third status.
//
// The store has always modelled a discard completely — `StatusDiscarded`,
// `discard_reason`, `discard_evidence`, `verify` accepting either
// `resolved_by` or `discard_reason`, `--status closed` matching both. What
// it never had was a command, which made the discard the one write in the
// whole store with nothing behind it. A judgment phase reading the CLI
// concluded the status was not real, reached for `close`, and produced 20
// records claiming work was done for findings whose point was that no work
// was needed — plus 20 mutated bodies, because `close` appends a discharge
// marker.
//
// A capability with no command is a capability a caller cannot find.
func newDeferredDiscardCmd() *cobra.Command {
	var (
		cwdFlag  string
		reason   string
		evidence string
		format   string
	)
	cmd := &cobra.Command{
		Use:   "discard <id>",
		Short: "Retire a record as never-needed, moving it to closed/",
		Long: `Mark a record discarded and move it to closed/. The record is NEVER
deleted, exactly as with 'close'.

DISCARD IS NOT CLOSE. They record incompatible claims:

  close    the work was done       -> resolved_by, resolved_at
  discard  the work was not needed -> discard_reason, discard_evidence

Using 'close' for a discard asserts that work happened, and it also
APPENDS A DISCHARGE MARKER TO THE BODY — which breaks the byte-identity
the migration's whole verification design exists to protect. This command
leaves the body untouched.

Nothing here re-derives the judgment. Deciding a deferred item is moot
requires re-verifying its premises against HEAD, so --reason is required
and --evidence carries what was checked. 'ape deferred verify' flags
CANDIDATES and never discards one.

The result is visible as 'ape deferred list --status discarded', and under
'--status closed' too, since both statuses have left the working set.`,
		Args: cobra.ExactArgs(1),
		Example: "  ape deferred discard DW-20260822-a1b2c3 \\\n" +
			"    --reason \"superseded by the 91-2 rewrite\" \\\n" +
			"    --evidence \"pkg/a.go:12 no longer exists at HEAD\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return usageErr(errors.New(
					"--reason is required: a discard with no stated reason cannot be reviewed later"))
			}
			store, _, date := storeFor(cwdFlag)
			rec, err := store.Discard(args[0], reason, evidence, date)
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, rec)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "discarded %s — moved to %s\n", rec.ID, rec.Path)
			fmt.Fprintf(cmd.OutOrStdout(), "  reason: %s\n", rec.DiscardReason)
			if rec.DiscardEvidence != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  evidence: %s\n", rec.DiscardEvidence)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&reason, "reason", "", "Why the work is not needed (required)")
	cmd.Flags().StringVar(&evidence, "evidence", "", "What was checked to establish that")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

// newDeferredRecoverCmd mines the ledger's history into an existing store.
//
// This is 'migrate --recover-deleted', reachable afterwards. It has to be
// its own command because Migrate short-circuits on AlreadyDone before it
// reaches the recovery branch, so on a migrated project that flag is
// silently inert — and the documented alternative (restore the ledger,
// delete the store, re-migrate) throws away every close, discard and
// repair edit made since.
func newDeferredRecoverCmd() *cobra.Command {
	var (
		cwdFlag  string
		fromFlag string
		dryRun   bool
		format   string
	)
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Recover ledger records deleted before the migration, into closed/",
		Long: `Mine the legacy ledger's git history for records that were removed from
it, and write them to closed/ as tombstones.

The ledger's only eviction mechanism was deletion — closing a record
destroyed its own audit trail — which is the failure the record store
exists to end. This is the repair for the history that predates it.

WHY THIS IS A SEPARATE COMMAND. 'ape deferred migrate --recover-deleted'
does the same work, but only on the FIRST migration: afterwards migrate
reports 'already migrated' and returns before the recovery step, so the
flag is silently inert. The route out of that ("restore the ledger, remove
the store, re-migrate") costs every close, discard and repair edit made
since the migration. This does not.

SAFE TO RE-RUN. Recovery only ever writes to closed/, so it can never add
anything to the open working set. It de-duplicates against what is on disk
— open records AND existing tombstones — so a second run recovers nothing.

The ledger path is normally a stub by now. That is fine: git history is
read THROUGH the path, not out of the file at it, so every revision that
held the real ledger is still reachable. A repo with no history for that
path is a warning, never fatal.

Nothing is committed.`,
		Args: cobra.NoArgs,
		Example: "  ape deferred recover --dry-run\n" +
			"  ape deferred recover",
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

			// Same clean gate as migrate, and for the same reason: nothing
			// here commits, so `git status` is the only separation between
			// ape's work and the operator's.
			if !dryRun {
				if dirty := dirtyPaths(cmd.Context(), cfg.Root, []string{cfg.Paths.Deferred}); len(dirty) > 0 {
					return usageErr(fmt.Errorf(
						"the store has uncommitted changes, so `git status` could not tell ape's work from yours: %v",
						dirty))
				}
			}

			res, err := store.Recover(cmd.Context(), deferred.RecoverOptions{From: from, DryRun: dryRun})
			if err != nil {
				return err
			}
			if f := output.Format(format); f != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), f, res)
			}
			emitRecoverHuman(cmd.OutOrStdout(), res, cfg.Root)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&fromFlag, "from", "", "Legacy ledger path (default: resolved from config)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be recovered, writing nothing")
	cmd.Flags().StringVar(&format, "output-format", "human", helpFormat)
	return cmd
}

func emitRecoverHuman(w io.Writer, res *deferred.RecoverResult, root string) {
	verb := "recover:"
	if res.DryRun {
		verb = "recover (dry run):"
	}
	fmt.Fprintf(w, "%s %d record(s) from git history -> %s\n",
		verb, res.Recovered, relTo(root, res.To))
	fmt.Fprintf(w, "         de-duplicated against %d record(s) already in the store\n", res.Existing)
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	if res.Recovered == 0 {
		fmt.Fprintln(w, "\nnothing to recover — every record in the ledger's history is already in the store")
		return
	}
	for i := range res.Records {
		fmt.Fprintf(w, "  %s  %s\n", res.Records[i].ID, res.Records[i].Title)
	}
	if res.DryRun {
		fmt.Fprintln(w, "\nnothing written (--dry-run)")
		return
	}
	emitGitAddHint(w, root, res.Paths)
}
