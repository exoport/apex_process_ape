package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/memory"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/exoport/apex_process_ape/internal/story"
)

// The PLAN-25 D11 project-data checks. Each returns INFO outside a project
// root — absence of a project is not a failure — and each reports what it
// found rather than what it fixed.

// msgNotInAProject is the one wording for a check whose subject is a
// project and which was not run inside one. Shared so four rows cannot
// drift into four different phrasings of the same non-finding.
const msgNotInAProject = "not in a project"

// checkConfigResolved is Required: nothing else in this family can see the
// project's artifacts without it, and a malformed config.local.yaml is the
// failure most likely to send every other check looking at the wrong tree.
func checkConfigResolved(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not in a project — nothing to resolve"}
	}
	cfg, err := apexcfg.ResolveAt(env.ProjectRoot, nil)
	if err != nil {
		if errors.Is(err, apexcfg.ErrNotFound) {
			return CheckResult{
				Status:      StatusInfo,
				Message:     "no _apex/config.yaml — not an APEX project",
				Remediation: "Run `ape framework setup` to install the framework and seed the config.",
				FixCommand:  "ape framework setup",
			}
		}
		return CheckResult{
			Status:      StatusFail,
			Message:     err.Error(),
			Remediation: "Fix the YAML it names. Every skill resolves this file as its first act, so a broken override sends a whole pipeline at the wrong folders.",
			FixCommand:  "ape config resolve",
		}
	}
	overlay := "no local overlay"
	if cfg.LocalOverlayApplied {
		overlay = fmt.Sprintf("local overlay replaced %v", cfg.OverlaidKeys)
	}
	return CheckResult{
		Status:  StatusOK,
		Message: fmt.Sprintf("%s resolved; %s", cfg.ProjectName, overlay),
	}
}

// checkRegistryDrift reports the four registry checks. Not Required: drift
// is a finding for a person, and a warn keeps `ape doctor` usable on a
// project that has some.
func checkRegistryDrift(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	report, err := registry.Verify(cfg, registry.VerifyOptions{})
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d record(s) across %d famil%s, no drift",
				report.Summary.Records, report.Summary.Families-report.Summary.Skipped,
				pluralY(report.Summary.Families-report.Summary.Skipped)),
		}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("%d finding(s): %v", report.Summary.Findings, report.Summary.ByCheck),
		Remediation: "`ape registry verify --all` lists them; `ape registry sync --all` repairs the ones a tool can.",
		FixCommand:  "ape registry verify --all",
	}
}

// checkStoryFrontmatter reports the story corpus's frontmatter findings.
func checkStoryFrontmatter(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	if cfg.Paths.Implementation == "" {
		return CheckResult{Status: StatusInfo, Message: apexcfg.MsgImplementationFolderUnset}
	}
	report, err := story.VerifyCorpus(cfg)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d stor%s checked, no findings",
				report.Summary.StoriesChecked, pluralY(report.Summary.StoriesChecked)),
		}
	}
	return CheckResult{
		Status: StatusWarn,
		Message: fmt.Sprintf("%d finding(s) across %d stor%s",
			report.Summary.Findings, report.Summary.StoriesChecked,
			pluralY(report.Summary.StoriesChecked)),
		Remediation: "`ape story verify` lists them. Frontmatter is authored, so these are fixed by hand.",
		FixCommand:  "ape story verify",
	}
}

// checkSprintDivergence reports tracker-vs-disk divergence. Never more
// than a warn: which side is right is judgment, so this is information for
// a person and nothing else.
func checkSprintDivergence(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	report, err := sprint.RunCheck(cfg)
	if err != nil {
		return CheckResult{Status: StatusWarn, Message: err.Error()}
	}
	if report.OK() {
		return CheckResult{
			Status: StatusOK,
			Message: fmt.Sprintf("%d story row(s) agree with %d story file(s)",
				report.Summary.StoryRows, report.Summary.StoriesOnDisk),
		}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("%d divergence(s): %v", report.Summary.Findings, report.Summary.ByCheck),
		Remediation: "`ape sprint check` names both sides of each one. Neither is assumed correct — a person decides.",
		FixCommand:  "ape sprint check",
	}
}

// checkMemorySize is Required, and that is the whole point.
//
// runDoctor silently downgrades a non-required FAIL to WARN, so a
// non-required check could never surface the hard ceiling — the state where
// team-memory.md has passed the Read cap and become unreadable by its own
// writer. `ape memory check` deliberately exits 0 in that state (a failing
// exit there would abort the retrospective at exactly the moment
// compaction is due); this is where the breach becomes non-ignorable
// instead.
func checkMemorySize(_ context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	if cfg.Paths.TeamMemory == "" {
		return CheckResult{Status: StatusInfo, Message: "development_folder is not configured"}
	}
	c := memory.CheckSize(cfg.Paths.TeamMemory, 0, 0)
	switch c.State {
	case memory.StateAbsent:
		return CheckResult{Status: StatusOK, Message: "no team-memory.md yet"}
	case memory.StateOK:
		return CheckResult{
			Status:  StatusOK,
			Message: fmt.Sprintf("%s of %s", humanBytes(c.Bytes), humanBytes(c.SoftBudget)),
		}
	case memory.StateOverSoft:
		return CheckResult{
			Status:      StatusWarn,
			Message:     fmt.Sprintf("%s over the %s soft budget — compaction is due", humanBytes(c.Bytes), humanBytes(c.SoftBudget)),
			Remediation: "The next retrospective should fold older entries. Nothing is broken yet.",
			FixCommand:  "ape memory check",
		}
	case memory.StateOverHard:
		return CheckResult{
			Status: StatusFail,
			Message: fmt.Sprintf("%s is past the %s hard ceiling — the file is unreadable by its own writer",
				humanBytes(c.Bytes), humanBytes(c.HardCeiling)),
			Remediation: "Compact it now, and treat the miss as a bug report: the soft gate should have caught this several retrospectives ago.",
			FixCommand:  "ape memory check",
		}
	default:
		return CheckResult{Status: StatusInfo, Message: string(c.State)}
	}
}

// checkMigrationPending makes --no-migrate leave a visible state rather
// than a silent one.
func checkMigrationPending(_ context.Context, env doctorEnv) CheckResult {
	if !isProjectRoot(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: msgNotInAProject}
	}
	pending := pendingMigrations(env.ProjectRoot)
	if len(pending) == 0 {
		return CheckResult{Status: StatusInfo, Message: "no project config — nothing to migrate"}
	}
	var names []string
	for _, st := range pending {
		if st.Pending {
			names = append(names, st.Name)
		}
	}
	if len(names) == 0 {
		return CheckResult{Status: StatusOK, Message: "no project-data migration pending"}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     fmt.Sprintf("pending: %v", names),
		Remediation: "`ape framework update` runs them, or `ape deferred migrate --dry-run` to look first. Nothing is committed either way.",
		FixCommand:  "ape deferred migrate --dry-run",
	}
}

// checkSprintLockIgnored reports a `sprint-status.yaml.lock` that git is
// not ignoring.
//
// `ape sprint reconcile` takes an advisory lock on that sidecar and does
// NOT unlink it — releasing a lock and deleting the file are different
// acts, and deleting one another process may be waiting on is how the
// mutual exclusion is lost. So the file stays, and the framework invokes
// reconcile at six boundaries, so it appears on essentially every project
// that runs a batch.
//
// It is a runtime artifact and belongs in nobody's history. Left
// unignored it sits in `git status` waiting to be swept up by a
// `git add -A` — which is exactly how it reached a commit in ape's own
// repository while this check was being written. `reconcile-epic-status.py`
// leaves the same file, so this is not a regression ape introduced; it is a
// hygiene problem neither side had noticed and that only a tool looking for
// it will surface.
//
// Never more than a WARN, and never a write: `.gitignore` is the operator's
// file, and a tool that edits it uninvited is worse than one that points.
func checkSprintLockIgnored(ctx context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	tracker := cfg.Paths.SprintStatus
	if tracker == "" {
		return CheckResult{Status: StatusInfo, Message: apexcfg.MsgImplementationFolderUnset}
	}
	lock := sprint.LockPath(tracker)
	rel := relTo(cfg.Root, lock)

	if _, err := os.Stat(tracker); err != nil {
		// No tracker means reconcile has nothing to lock. Warning here would
		// be a finding about a file that cannot yet exist.
		return CheckResult{Status: StatusOK, Message: "no sprint-status.yaml yet — nothing to lock"}
	}

	switch gitIgnores(ctx, cfg.Root, lock) {
	case ignoreYes:
		return CheckResult{Status: StatusOK, Message: rel + " is ignored"}
	case ignoreNotARepo:
		// Nothing to ignore into, and nothing that can accidentally commit it.
		return CheckResult{Status: StatusInfo, Message: "not a git repository"}
	}

	// Worse if it is already tracked: the artifact is in history, so
	// ignoring it now changes nothing until it is also removed from the index.
	if gitTracked(ctx, cfg.Root, lock) {
		return CheckResult{
			Status:  StatusWarn,
			Message: rel + " is COMMITTED — a lock sidecar is in the project's history",
			Remediation: "Add `" + rel + "` to .gitignore and untrack it. It is a runtime artifact of " +
				"`ape sprint reconcile`, which never unlinks it, so it will keep reappearing.",
			FixCommand: "git rm --cached " + rel,
		}
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: rel + " is not ignored by git",
		Remediation: "`ape sprint reconcile` leaves this advisory-lock sidecar behind on every run and " +
			"never unlinks it, so an untracked file sits beside the tracker waiting for a `git add -A`. " +
			"Add `" + rel + "` (or `*" + sprint.LockSuffix + "`) to .gitignore.",
		FixCommand: "echo '" + rel + "' >> .gitignore",
	}
}

// checkOutputApeIgnored reports whether git is ignoring the resolved
// `{output_folder}/ape/` — the one subtree ape writes into a project.
//
// Everything ape produces during a run lands there: manifests, runlogs,
// transcript links, cost rollups. It is regenerated on every run and
// belongs in nobody's history. Left unignored, the first commit-emitting
// step's `git add -A` sweeps it into the operator's commit, and from then
// on every run rewrites tracked files — which is also what makes the NEXT
// `ape pipeline` fail its pre-flight dirty-tree gate, since the artifacts
// of the last run are sitting uncommitted in the tree.
//
// The framework already requires this and states it plainly:
// apex-orchestrator's preflight lists `output_folder` as one that "must be
// gitignored", and apex-epic-retrospective depends on the fact when it
// tells a batch to write its corpus artifact somewhere ELSE precisely
// because `{output_folder}/` is not tracked. What the framework does not
// do is install the rule — its own `_output/.gitignore` catchall is
// repo-local housekeeping and is not part of the payload
// `ape framework setup` copies into a project. So the requirement exists,
// nothing enforces it, and a project can be years into violating it
// without a single line of output saying so. That gap is what this row
// closes.
//
// REPORTING ONLY, and never a write — the same standing as
// checkSprintLockIgnored below, for the reason stated there: `.gitignore`
// is the operator's file, and a tool that edits it uninvited is worse than
// one that points. An earlier draft of this had `ape framework update`
// append the line. It was dropped, and two things killed it. An ignore
// line does not untrack anything, so on the projects most in need of the
// fix it would have written a line and changed nothing while reading as
// success. And `.gitignore` is captured into the eval's overlays, so a
// write would have propagated a line into 20+ committed fixtures that the
// eval's own `_output`-stripper does not match.
//
// Deliberately narrower than the framework's rule: this asks only about
// ape's subtree, not the whole output folder, because `{output_folder}/`
// holds the framework's handoffs, briefs and verify reports and whether
// THOSE are tracked is the framework's call to make, not ape's.
func checkOutputApeIgnored(ctx context.Context, env doctorEnv) CheckResult {
	cfg, res := projectDataConfig(env)
	if res != nil {
		return *res
	}
	apeRoot := runlog.ApeRoot(cfg.Root)
	rel, relErr := filepath.Rel(cfg.Root, apeRoot)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// output_folder points outside the repository. Nothing here can be
		// covered by this project's .gitignore, and there is no finding.
		return CheckResult{
			Status:  StatusInfo,
			Message: "output_folder resolves outside the project — nothing for .gitignore to cover",
		}
	}
	rel = filepath.ToSlash(rel)

	switch gitIgnores(ctx, cfg.Root, apeRoot) {
	case ignoreYes:
		return CheckResult{Status: StatusOK, Message: rel + " is ignored"}
	case ignoreNotARepo:
		return CheckResult{Status: StatusInfo, Message: "not a git repository"}
	}

	// Worse if it is already tracked. Adding the ignore line now would
	// change nothing — git keeps honouring the index for paths it already
	// follows — so the remediation has to say `git rm --cached` out loud or
	// it is advice that cannot work.
	if gitTracked(ctx, cfg.Root, apeRoot) {
		return CheckResult{
			Status:  StatusWarn,
			Message: rel + " is COMMITTED — ape's run artifacts are in the project's history",
			Remediation: "Every run rewrites these, so each one dirties the tree and the next " +
				"`ape pipeline` fails its dirty-tree pre-flight. Ignoring the path is not enough on " +
				"its own: git keeps tracking what is already in the index, so untrack it too.",
			FixCommand: "git rm -r --cached " + rel + " && echo '" + rel + "/' >> .gitignore",
		}
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: rel + " is not ignored by git",
		Remediation: "ape writes every manifest, runlog and transcript link under this path and " +
			"rewrites them on each run. Untracked, they sit in `git status` waiting for a " +
			"`git add -A` — including ape's own commit-emitting steps. The framework already " +
			"requires the output folder to be gitignored; this is the part of it ape can see.",
		FixCommand: "echo '" + rel + "/' >> .gitignore",
	}
}

// ignoreState is the three-way answer gitIgnores can give: git's own
// yes/no, plus "the question does not apply".
type ignoreState int

const (
	ignoreNo ignoreState = iota
	ignoreYes
	ignoreNotARepo
)

// gitIgnores asks GIT whether a path is ignored, rather than reading
// .gitignore and matching patterns by hand.
//
// The hand-rolled version is wrong in ways nobody notices until it matters:
// the answer can come from a nested .gitignore, from .git/info/exclude, from
// core.excludesFile, or from a negation later in the file. `git check-ignore`
// is the only implementation that agrees with what git will actually do,
// which is the thing being predicted.
func gitIgnores(ctx context.Context, root, path string) ignoreState {
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-q", "--", path)
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return ignoreYes
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// 1 is git's "not ignored". Anything else — 128 for "not a
		// repository", or git missing entirely — is not an answer, and
		// reporting "not ignored" for it would be a finding about the
		// environment dressed up as one about the project.
		return ignoreNo
	}
	return ignoreNotARepo
}

// gitTracked reports whether git has the path in its index.
func gitTracked(ctx context.Context, root, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", path)
	cmd.Dir = root
	return cmd.Run() == nil
}

// projectDataConfig resolves the config for a check, or returns the
// CheckResult to report instead. Outside a project, or with no config, the
// answer is INFO — absence of evidence is not a finding.
func projectDataConfig(env doctorEnv) (*apexcfg.Resolved, *CheckResult) {
	if !isProjectRoot(env.ProjectRoot) {
		return nil, &CheckResult{Status: StatusInfo, Message: msgNotInAProject}
	}
	cfg, err := apexcfg.ResolveAt(env.ProjectRoot, nil)
	if err != nil {
		// config.resolved owns reporting the real state; duplicating its
		// verdict on five more rows would just be noise.
		return nil, &CheckResult{Status: StatusInfo, Message: "config not resolvable — see config.resolved"}
	}
	return cfg, nil
}
