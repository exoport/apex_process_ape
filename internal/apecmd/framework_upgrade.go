package apecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/migration"
	"github.com/exoport/apex_process_ape/internal/runlog"
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
	return loadMigrationPlanWith(ctx, projectRoot, "", runner, runChecks)
}

// loadIncomingMigrationPlan is `--plan`'s view: the list the project will
// hold AFTER `ape framework update` installs from repo, judged against the
// project as it is now. Without it `--plan` showed only what was already
// installed, so a migration arriving with the update — the one an operator
// most wants to see first — was invisible until it had run.
func loadIncomingMigrationPlan(ctx context.Context, projectRoot, repo string, runner migration.Runner,
	runChecks bool,
) (*migration.Plan, error) {
	return loadMigrationPlanWith(ctx, projectRoot, repo, runner, runChecks)
}

// loadMigrationPlanWith builds the plan over the project's installed list,
// overlaid with repo's when repo is set. The overlay is what install does:
// it copies every file from the repo over the same name and deletes
// nothing, so an incoming file replaces its installed namesake and every
// other installed file stays.
func loadMigrationPlanWith(ctx context.Context, projectRoot, repo string, runner migration.Runner,
	runChecks bool,
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
	if repo != "" {
		incoming, iErr := migration.Load(filepath.Join(repo, framework.SubtreeMigrations))
		if iErr != nil {
			return nil, iErr
		}
		entries = overlayMigrations(entries, incoming)
	}
	// An APEX v1 install never plans an entry below 1.0.0 (D162-3), whatever
	// the folder or the ledger holds. Judged by the release being installed
	// — the repo for `--plan`, else the project's installed manifest.
	if v1Install(projectRoot, repo) && entries != nil {
		entries = migration.DropBelow(entries, v1MigrationFloor)
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

// v1MigrationFloor is the lowest version an APEX v1 install plans.
const v1MigrationFloor = "1.0.0"

// v1Install reports whether the framework the plan is for is APEX v1: the
// incoming release when repo is set, else the project's installed copy.
func v1Install(projectRoot, repo string) bool {
	if repo != "" {
		return framework.IsV1Release(repo)
	}
	return framework.IsV1Release(projectRoot)
}

// blockingMigrationError is an update whose blocking derivable migration —
// the lift, above all — failed or was not attempted (D162-4). The install
// is not valid without it, so the update exits non-zero and does not
// commit. The ledger of what did succeed is written before it is returned.
type blockingMigrationError struct {
	blocked []migration.Blocked
}

func (e *blockingMigrationError) Error() string {
	b := e.blocked[0]
	msg := "blocking migration " + b.ID + " " + b.Reason
	if n := len(e.blocked) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more blocking entr%s)", n, pluralY(n))
	}
	return msg
}

// overlayMigrations is install's copy applied to the parsed lists: an
// incoming entry replaces the installed one with the same file name.
func overlayMigrations(installed, incoming []migration.Entry) []migration.Entry {
	if len(incoming) == 0 {
		return installed
	}
	replaced := make(map[string]bool, len(incoming))
	for i := range incoming {
		replaced[filepath.Base(incoming[i].Path)] = true
	}
	out := make([]migration.Entry, 0, len(installed)+len(incoming))
	for i := range installed {
		if !replaced[filepath.Base(installed[i].Path)] {
			out = append(out, installed[i])
		}
	}
	return append(out, incoming...)
}

// runUpgradeMigrations applies the derivable pending entries and records
// what succeeded.
//
// It returns no error for a migration that FAILED: the failure is
// reported, the ledger keeps the completed prefix, and the next run
// resumes after it. `ape framework update` installing the framework
// correctly and a project migration failing are different outcomes, and
// collapsing the second into a non-zero exit would make the first look
// like it had not happened. The exception is a BLOCKING derivable entry
// (D162-4): the install is not valid without it, so it is returned as a
// *blockingMigrationError, and the update neither commits nor exits 0.
func runUpgradeMigrations(ctx context.Context, w io.Writer, projectRoot string) error {
	_, err := runUpgradeMigrationsApplied(ctx, w, projectRoot)
	return err
}

// runUpgradeMigrationsApplied is runUpgradeMigrations, also returning the
// ids it applied — what the install commit's Framework-Migrations trailer
// names.
func runUpgradeMigrationsApplied(ctx context.Context, w io.Writer, projectRoot string) ([]string, error) {
	runner, cleanup, notice := migration.NewShellRunner()
	defer cleanup()
	if notice != "" {
		fmt.Fprintf(w, "migrations: %s\n", notice)
	}
	plan, err := loadMigrationPlan(ctx, projectRoot, runner, true)
	if err != nil {
		return nil, err
	}
	if !plan.Present {
		return nil, nil
	}
	if len(plan.Rows) == 0 {
		fmt.Fprintln(w, "migrations: none declared")
		return nil, nil
	}
	emitMigrationFindings(w, plan)

	counts := plan.Counts()
	if counts.Runnable == 0 && counts.JudgedToRun == 0 {
		fmt.Fprintf(w, "migrations: nothing to run (%s)\n", migrationCountLine(counts))
		if blocked := migration.BlockingUnapplied(plan, nil); len(blocked) > 0 {
			return nil, &blockingMigrationError{blocked: blocked}
		}
		reportStandardOptOut(w, projectRoot)
		return nil, nil
	}

	// A command's own stderr reaches the person: a migration that refuses
	// says why there, and its checks stay quiet.
	cmdRunner := runner
	cmdRunner.Stderr = os.Stderr
	res := migration.Apply(ctx, w, projectRoot, plan, cmdRunner, ledgerStamp(projectRoot))

	rows := make([]framework.AppliedMigration, 0, len(res.Applied))
	ids := make([]string, 0, len(res.Applied))
	for _, a := range res.Applied {
		rows = append(rows, framework.AppliedMigration{ID: a.ID, Version: a.Version, AppliedAt: a.AppliedAt})
		ids = append(ids, a.ID)
	}
	if err := framework.AppendMigrations(projectRoot, rows); err != nil {
		// The migrations RAN. Failing to record them is serious — the next
		// run would re-run them — so it is reported loudly and returned,
		// unlike a migration's own failure.
		return ids, fmt.Errorf("recording applied migrations in framework.yaml: %w", err)
	}

	emitApplyResult(w, res)
	if blocked := migration.BlockingUnapplied(plan, res); len(blocked) > 0 {
		return ids, &blockingMigrationError{blocked: blocked}
	}
	reportStandardOptOut(w, projectRoot)
	return ids, nil
}

// reportStandardOptOut says, on an APEX v1 project that opted out of the
// standard ADR-0001, that the update left it alone (D206, R7b-2): the
// standard's entry is judged applied by its check, which says nothing.
func reportStandardOptOut(w io.Writer, projectRoot string) {
	p, err := apexv1.Open(projectRoot)
	if err != nil || !p.IsV1() {
		return
	}
	if line := p.StandardOptOut(); line != "" {
		fmt.Fprintln(w, line)
	}
}

// ledgerStamp issues the ledger's applied_at stamps. In an APEX v1
// project the monotonic floor lives under _apex/state/, which `ape
// framework update` never writes (backlog 26): the stamps are read
// against the floor there, and not recorded in it.
func ledgerStamp(projectRoot string) func() string {
	iss := stamp.New(projectRoot, nil)
	if !runlog.IsV1Project(projectRoot) {
		return iss.Issue
	}
	return func() string { return iss.Peek().Format(apexcfg.TimestampLayout) }
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

// emitMigrationPlan is `--plan`: it prints and does nothing. repo, when
// set, is where incoming entries were read from, and adds a SOURCE column
// saying which rows the update brings.
func emitMigrationPlan(w io.Writer, plan *migration.Plan, repo string) {
	if !plan.Present {
		fmt.Fprintln(w, "migrations: no _apex/migrations/ folder — this framework ships no migration list")
		return
	}
	if len(plan.Rows) == 0 {
		fmt.Fprintf(w, "migrations: none declared in %s\n", plan.Dir)
		return
	}

	if repo != "" {
		fmt.Fprintf(w, "migrations: the list this project will hold after `ape framework update` from %s\n"+
			"            (read as the repo stands now; the update itself fetches first unless --no-fetch)\n\n", repo)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "ID\tKIND\tSTATE\tCHECK\tBLOCKING\tACTION"
	if repo != "" {
		header = "ID\tSOURCE\tKIND\tSTATE\tCHECK\tBLOCKING\tACTION"
	}
	fmt.Fprintln(tw, header)
	for i := range plan.Rows {
		r := &plan.Rows[i]
		source := ""
		if repo != "" {
			source = migrationSource(r, repo, plan.Dir) + "\t"
		}
		fmt.Fprintf(tw, "%s\t%s%s\t%s\t%s\t%t\t%s\n",
			r.Label(), source, migrationKind(r), r.State, r.Check, r.Blocking, migrationAction(r))
	}
	if err := tw.Flush(); err != nil {
		return
	}
	fmt.Fprintf(w, "\n%s\n", migrationCountLine(plan.Counts()))
	emitMigrationFindings(w, plan)
}

// migrationSource says where a --plan row comes from: already installed,
// or brought by the update — as a new file or over an installed one.
func migrationSource(r *migration.Row, repo, installedDir string) string {
	rel, err := filepath.Rel(repo, r.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "installed"
	}
	if _, statErr := os.Stat(filepath.Join(installedDir, filepath.Base(r.Path))); statErr == nil {
		return "incoming (replaces installed)"
	}
	return "incoming (new)"
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
			// Not final (E-R7-1): an update asks again once an earlier entry
			// in its run has applied, on the layout that entry left.
			blocks := "blocking nothing"
			if r.Blocking {
				blocks = "it is blocking"
			}
			fmt.Fprintf(w,
				"  %s: CANNOT TELL — its check could not run (%s), so it is unapplied and "+
					"unverifiable. Not run as the project stands, and %s; `ape framework update` "+
					"asks the check again once an earlier entry in its run applies.\n",
				r.Label(), r.CheckDetail, blocks)
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
// The checks ARE run, through the same self-pinned shell runner as
// `ape framework update` and its `--plan`. The row once read the ledger
// alone, and on a project that already had an entry's shape without a
// ledger row — upgraded by hand, or born after the change — it reported
// "pending" forever: the update judged the entry applied by its check,
// ran nothing, and (rightly) recorded nothing, so the remedy the row
// named could never clear it. A row and its remedy must judge by one
// rule. A check that cannot run is reported as such, never as pending.
func checkUpgradeMigrations(ctx context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: msgNotInAProject}
	}
	runner, cleanup, _ := migration.NewShellRunner()
	defer cleanup()
	plan, err := loadMigrationPlan(ctx, env.ProjectRoot, runner, true)
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
	applied := fmt.Sprintf("%d applied", c.Applied)
	if byCheck := appliedByCheck(plan); byCheck > 0 {
		applied += fmt.Sprintf(" (%d by check, not in the ledger)", byCheck)
	}
	if c.Pending == 0 && c.HalfApplied == 0 && c.CannotTell == 0 {
		return CheckResult{
			Status:  StatusOK,
			Message: applied + ", nothing pending",
		}
	}
	var parts []string
	if c.Pending > 0 {
		p := fmt.Sprintf("%d pending (%d ape can run, %d need a skill dispatch)",
			c.Pending, c.Runnable, c.JudgedToRun)
		if c.BlockingOpen > 0 {
			p += fmt.Sprintf(", %d marked blocking", c.BlockingOpen)
		}
		parts = append(parts, p)
	}
	if c.HalfApplied > 0 {
		parts = append(parts, fmt.Sprintf("%d HALF-APPLIED", c.HalfApplied))
	}
	if c.CannotTell > 0 {
		parts = append(parts, fmt.Sprintf("%d cannot tell (check could not run)", c.CannotTell))
	}
	msg := strings.Join(parts, ", ")
	return CheckResult{
		Status:      StatusWarn,
		Message:     msg,
		Remediation: "`ape framework update` runs the derivable ones and lists the judged ones with the skill to dispatch.",
		FixCommand:  "ape framework update --plan",
	}
}

// appliedByCheck counts the rows applied on their check's word alone.
func appliedByCheck(plan *migration.Plan) int {
	n := 0
	for i := range plan.Rows {
		if plan.Rows[i].State == migration.StateApplied && plan.Rows[i].Source == migration.SourceCheck {
			n++
		}
	}
	return n
}

func migrationCountLine(c migration.Counts) string {
	return fmt.Sprintf(
		"%d pending (%d runnable now, %d judged), %d applied, %d half-applied, %d cannot-tell, %d superseded",
		c.Pending, c.Runnable, c.JudgedToRun, c.Applied, c.HalfApplied, c.CannotTell, c.Superseded)
}
