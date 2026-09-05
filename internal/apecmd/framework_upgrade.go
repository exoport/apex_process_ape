package apecmd

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/migration"
	"github.com/exoport/apex_process_ape/internal/stamp"
)

// The framework's per-version upgrade list, `_apex/migrations/*.md`, run
// by `ape framework update` alongside the project-data migrations in
// framework_migrate.go. The two are separate on purpose: those convert
// ape's own stores between ape versions and are detected from disk state,
// while these are authored by the framework, ordered by its own semantics,
// and recorded in a ledger.

// loadMigrationPlan builds the plan for a project. Every failure short of
// an unreadable folder is carried in the plan itself, because a runner
// that stopped on one bad entry could not report the others.
func loadMigrationPlan(ctx context.Context, projectRoot string, runner migration.Runner, runChecks bool,
) (*migration.Plan, error) {
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		// No resolvable config means no `{apex_folder}` to look in. Not a
		// failure of the update: the install already succeeded.
		return &migration.Plan{}, nil //nolint:nilerr // an unresolvable config is "no list", not an error
	}
	dir := migration.Dir(cfg.Paths.Apex)
	entries, err := migration.Load(dir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		// An absent folder is a normal state on a framework that ships no
		// list, and is reported as such rather than as an empty plan that
		// looks like "everything is applied".
		return &migration.Plan{Dir: dir}, nil
	}

	var ledger []migration.Applied
	if meta, mErr := framework.ReadMetadata(projectRoot); mErr == nil {
		for _, a := range meta.Migrations {
			ledger = append(ledger, migration.Applied{ID: a.ID, Version: a.Version, AppliedAt: a.AppliedAt})
		}
	}
	return migration.BuildPlan(ctx, dir, projectRoot, entries, ledger, runner, runChecks), nil
}

// runUpgradeMigrations applies the derivable pending entries and records
// what succeeded.
//
// It returns no error for a migration that FAILED: the failure is
// reported, the ledger keeps the completed prefix, and the next run
// resumes after it. `ape framework update` installing the framework
// correctly and a project migration failing are different outcomes, and
// collapsing the second into a non-zero exit would make the first look
// like it had not happened.
func runUpgradeMigrations(ctx context.Context, w io.Writer, projectRoot string) error {
	plan, err := loadMigrationPlan(ctx, projectRoot, migration.ShellRunner{}, true)
	if err != nil {
		return err
	}
	if !plan.Present {
		return nil
	}
	if len(plan.Rows) == 0 {
		fmt.Fprintln(w, "migrations: none declared")
		return nil
	}
	emitMigrationFindings(w, plan)

	counts := plan.Counts()
	if counts.Runnable == 0 && counts.JudgedToRun == 0 {
		fmt.Fprintf(w, "migrations: nothing to run (%s)\n", migrationCountLine(counts))
		return nil
	}

	res := migration.Apply(ctx, w, projectRoot, plan, migration.ShellRunner{}, stamp.New(projectRoot, nil).Issue)

	rows := make([]framework.AppliedMigration, 0, len(res.Applied))
	for _, a := range res.Applied {
		rows = append(rows, framework.AppliedMigration{ID: a.ID, Version: a.Version, AppliedAt: a.AppliedAt})
	}
	if err := framework.AppendMigrations(projectRoot, rows); err != nil {
		// The migrations RAN. Failing to record them is serious — the next
		// run would re-run them — so it is reported loudly and returned,
		// unlike a migration's own failure.
		return fmt.Errorf("recording applied migrations in framework.yaml: %w", err)
	}

	emitApplyResult(w, res)
	return nil
}

func emitApplyResult(w io.Writer, res *migration.ApplyResult) {
	for _, o := range res.Outcomes {
		switch {
		case o.Skipped != "":
			fmt.Fprintf(w, "migration %s: %s\n", o.ID, o.Skipped)
		case o.Error != "":
			fmt.Fprintf(w, "migration %s: FAILED (%s)\n", o.ID, o.Error)
		default:
			fmt.Fprintf(w, "migration %s: applied at %s\n", o.ID, o.AppliedAt)
		}
	}
	for _, j := range res.Judged {
		fmt.Fprintf(w, "migration %s: %s\n", j.ID, j.Skipped)
	}
	if res.Failed != "" {
		fmt.Fprintf(w,
			"migrations: stopped at %s — later entries may depend on it, so none after it was attempted.\n"+
				"            Fix it and re-run `ape framework update`; the ledger already records what succeeded.\n",
			res.Failed)
	}
}

// emitMigrationPlan is `--plan`: it prints and does nothing.
func emitMigrationPlan(w io.Writer, plan *migration.Plan) {
	if !plan.Present {
		fmt.Fprintln(w, "migrations: no _apex/migrations/ folder — this framework ships no migration list")
		return
	}
	if len(plan.Rows) == 0 {
		fmt.Fprintf(w, "migrations: none declared in %s\n", plan.Dir)
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATE\tCHECK\tBLOCKING\tACTION")
	for i := range plan.Rows {
		r := &plan.Rows[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
			r.Label(), migrationKind(r), r.State, r.Check, r.Blocking, migrationAction(r))
	}
	if err := tw.Flush(); err != nil {
		return
	}
	fmt.Fprintf(w, "\n%s\n", migrationCountLine(plan.Counts()))
	emitMigrationFindings(w, plan)
}

func emitMigrationFindings(w io.Writer, plan *migration.Plan) {
	if len(plan.Cycle) > 0 {
		fmt.Fprintf(w,
			"\nmigrations: after: CYCLE among %v — the order is undefined, so none of them runs.\n"+
				"            An invented order is how a migration runs before what it depends on.\n",
			plan.Cycle)
	}
	for i := range plan.Rows {
		r := &plan.Rows[i]
		for _, f := range r.Findings {
			fmt.Fprintf(w, "  %s: %s\n", r.Label(), f)
		}
		if r.State == migration.StateHalfApplied {
			fmt.Fprintf(w,
				"  %s: HALF-APPLIED — the ledger records it at %s and its check says the "+
					"post-condition does not hold.\n", r.Label(), r.AppliedAt)
		}
		if r.State == migration.StateCannotTell {
			fmt.Fprintf(w,
				"  %s: CANNOT TELL — its check could not run (%s), so it is unapplied and "+
					"unverifiable. Not run, and blocking nothing.\n", r.Label(), r.CheckDetail)
		}
	}
}

func migrationKind(r *migration.Row) string {
	if r.Kind == "" {
		return "(unset→judged)"
	}
	return r.Kind
}

// migrationAction says what the runner would do, in the runner's own
// terms, so `--plan` and the run cannot read differently.
func migrationAction(r *migration.Row) string {
	switch {
	case r.State == migration.StateSuperseded:
		return "superseded by " + r.SupersededBy
	case r.State == migration.StateApplied, r.State == migration.StateHalfApplied:
		return "nothing"
	case r.State == migration.StateCannotTell:
		return "nothing — unverifiable"
	case r.Runnable:
		return "run: " + r.Command
	case r.IsJudged() && r.Skill != "":
		return "dispatch " + r.Skill
	case r.IsJudged():
		return "judged, and names no skill"
	default:
		return "nothing — " + firstFinding(r)
	}
}

func firstFinding(r *migration.Row) string {
	if len(r.Findings) > 0 {
		return r.Findings[0]
	}
	return "not runnable"
}

// checkUpgradeMigrations is the `migrations.pending` doctor row: pending
// and half-applied entries from the framework's upgrade list.
//
// NOT Required, deliberately. A pending migration is work a project owes,
// not a broken project — and `ape doctor` reds sit on the orchestrator's
// never-worked-around list, so a red here would either stop a build loop
// over a migration the operator has not got to yet, or teach operators
// that doctor reds are sometimes ignorable. The one it reports loudest is
// half-applied, which IS a defect, and it still warns rather than fails
// because the remedy is a dispatch and not a repair of ape's own state.
//
// The checks are NOT run here. They shell out, doctor rows are expected
// to be cheap and to run unattended in `--strict` CI, and a check that
// cannot run would turn every doctor invocation into a CANNOT-TELL
// report. The ledger alone answers "what does this project still owe",
// which is the question this row asks.
func checkUpgradeMigrations(ctx context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: msgNotInAProject}
	}
	plan, err := loadMigrationPlan(ctx, env.ProjectRoot, migration.NoCheckRunner(), false)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if !plan.Present {
		return CheckResult{Status: StatusInfo, Message: "this framework ships no migration list"}
	}
	if len(plan.Rows) == 0 {
		return CheckResult{Status: StatusOK, Message: "no migrations declared"}
	}

	c := plan.Counts()
	if len(plan.Cycle) > 0 {
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("after: cycle among %v — the order is undefined, so none of them runs", plan.Cycle),
			Remediation: "The cycle is in the framework's own migration files; report it upstream.",
			FixCommand:  "ape framework update --plan",
		}
	}
	if c.Pending == 0 && c.HalfApplied == 0 {
		return CheckResult{
			Status:  StatusOK,
			Message: fmt.Sprintf("%d applied, nothing pending", c.Applied),
		}
	}
	msg := fmt.Sprintf("%d pending (%d ape can run, %d need a skill dispatch)",
		c.Pending, c.Runnable, c.JudgedToRun)
	if c.BlockingOpen > 0 {
		msg += fmt.Sprintf(", %d marked blocking", c.BlockingOpen)
	}
	if c.HalfApplied > 0 {
		// Only reachable when a check ran, which this row does not do — it
		// is carried so a caller that built the plan WITH checks and passed
		// it here still reports the state rather than hiding it.
		msg += fmt.Sprintf(", %d HALF-APPLIED", c.HalfApplied)
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     msg,
		Remediation: "`ape framework update` runs the derivable ones and lists the judged ones with the skill to dispatch.",
		FixCommand:  "ape framework update --plan",
	}
}

func migrationCountLine(c migration.Counts) string {
	return fmt.Sprintf(
		"%d pending (%d runnable now, %d judged), %d applied, %d half-applied, %d cannot-tell, %d superseded",
		c.Pending, c.Runnable, c.JudgedToRun, c.Applied, c.HalfApplied, c.CannotTell, c.Superseded)
}
