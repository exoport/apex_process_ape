package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/migration"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Exit codes specific to `ape framework setup` / `update` failures so
// script callers can branch on the failure class.
const (
	exitCodeFrameworkValidation     = 3
	exitCodeProjectSkillsModified   = 4
	exitCodeBootstrapHeadlessNoArgs = 5
	exitCodeAlreadyInstalled        = 6
	exitCodeNotInstalled            = 7
)

func newFrameworkCmd() *cobra.Command {
	var (
		repoFlag string
		cwdFlag  string
	)
	cmd := &cobra.Command{
		Use:   "framework",
		Short: "Install and inspect APEX framework assets in a project",
		Long: `Manage the apex_process_framework assets installed at the project root.

  ape framework setup      One-time install: skills + pipelines + bootstrap
                           _apex/config.yaml. Refuses if already installed
                           (pass --force to re-bootstrap).
  ape framework update     Install the framework's newest release (or
                           --version) and commit it. Refuses if not yet
                           set up (run setup first).
  ape framework status     Inspect the installed framework version, with
                           optional drift report against the framework repo.

The framework repo is --repo, else $APEX_FRAMEWORK_REPO, else ape's own
clone under the user cache directory (cloned from $APEX_FRAMEWORK_URL, else
the framework's GitHub repo). Since ape v0.4.0 setup and update install a
RELEASE — the highest vX.Y.Z tag, or the one --version names (the only way
to a vX.Y.Z-rc.N candidate) — exported from that clone; since v0.7.0 they
also check that release out in the clone, and keep the project's governance
repo at its newest release the same way. --from-worktree installs the
working tree instead. See docs/explanation/framework-updates-from-releases.md
and docs/explanation/framework-and-governance-clones.md.
The project root is resolved from --cwd or the current working directory.`,
	}
	cmd.PersistentFlags().StringVar(&repoFlag, "repo", "", "Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO)")
	cmd.PersistentFlags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	cmd.AddCommand(newFrameworkSetupCmd(&repoFlag, &cwdFlag))
	cmd.AddCommand(newFrameworkUpdateCmd(&repoFlag, &cwdFlag))
	cmd.AddCommand(newFrameworkStatusCmd(&repoFlag, &cwdFlag))
	return cmd
}

// frameworkUpdateOutput is the structured payload of `ape framework update`.
type frameworkUpdateOutput struct {
	Metadata framework.Metadata      `json:"metadata" yaml:"metadata"`
	Summary  framework.UpdateSummary `json:"summary"  yaml:"summary"`
}

func newFrameworkSetupCmd(repoFlag, cwdFlag *string) *cobra.Command {
	var (
		noFetch        bool
		force          bool
		forceClone     bool
		outputFormat   string
		projectName    string
		extensionsFlag string
		noBootstrap    bool
		rf             releaseFlags
	)
	cmd := &cobra.Command{
		Use: "setup",
		// Takes none: without this, a stray argument is silently ignored
		// and the command answers 0 to an invocation nobody meant.
		Args:  cobra.NoArgs,
		Short: "Initial install of a framework release into the project, committed",
		Long: `Initial install of framework-managed assets into <project>, from the
framework's newest release tag (--version picks one; --from-worktree
installs the repo's working tree instead):

  - .claude/skills/apex-*  copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml copied from <repo>/_apex/pipelines
  - _apex/config.yaml      seeded (interactive prompt by default;
                           supply --project-name and --extensions to
                           skip the TUI; --no-bootstrap to skip seeding
                           entirely)
  - _apex/framework.yaml   metadata recording what was installed.

The install is committed as 'chore(framework): install APEX framework
vX.Y.Z' with Framework-Version / Framework-Commit / Generator trailers.
--no-commit leaves it in the working tree instead.

Refuses to run when:
  - _apex/framework.yaml already exists (pass --force to re-bootstrap;
    this resets project_name and extensions)
  - committing, and the project is not a git repo, is on a detached HEAD,
    or has modified or staged tracked files (exit 4)
  - this ape is older than the release's min_ape_version (exit 11; on a
    terminal it offers to update ape first and re-runs itself)
  - --from-worktree, and the framework repo is dirty, on a non-main
    branch, or its .claude/skills/apex-* subtree has uncommitted changes
    (pass --force to bypass)
  - your framework or governance clone is behind the release and has
    local changes (exit 3; pass --force-clone to overwrite them). A clone
    of yours only ever moves forward: one whose branch is ahead of the
    release, or diverged from it, is left where it is and reported. ape's
    own clones under the user cache are always overwritten

Headless contexts: when stdout is not a TTY (or --output-format is not
human) and the project lacks _apex/config.yaml, you must supply
--project-name and --extensions, OR pass --no-bootstrap. Otherwise
'setup' refuses to seed silently.

For subsequent refreshes against a framework version bump, use
'ape framework update'.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clone, err := resolveFrameworkClone(*repoFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			repo := clone.Path
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			format := output.Format(outputFormat)
			sel, err := rf.selector(noFetch)
			if err != nil {
				return err
			}
			var untracked map[string]bool
			if !rf.noCommit {
				if untracked, err = commitPrecheck(cmd.Context(), projectRoot); err != nil {
					return err
				}
			}
			fwTarget, sel, err := prepareFramework(cmd.Context(), clone, sel)
			if err != nil {
				return handleSetupError(err)
			}
			sel, done, err := prepareInstall(cmd.Context(), repo, projectRoot, sel, !rf.noCommit, rf.version == "")
			if err != nil {
				return handleSetupError(err)
			}
			defer done()
			bootstrap, err := pickBootstrapper(projectRoot, projectName, extensionsFlag, noBootstrap, format)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				if errors.Is(err, errBootstrapHeadlessNoArgs) {
					os.Exit(exitCodeBootstrapHeadlessNoArgs)
				}
				return err
			}
			// The clones move last, after every check that could refuse the
			// install. Governance syncs only when the project already has a
			// config to name it; a new project's is seeded by this setup.
			gov, err := syncClones(cmd.Context(), cloneNoteWriter(cmd, format), clone, fwTarget, projectRoot, noFetch, forceClone)
			if err != nil {
				return err
			}
			res, err := framework.Setup(cmd.Context(), &framework.UpdateOptions{
				FrameworkRepo: repo,
				ProjectRoot:   projectRoot,
				NoFetch:       noFetch,
				Force:         force,
				ApeVersion:    Version,
				Bootstrapper:  bootstrap,
				Release:       sel,
				Governance:    gov,
			})
			if err != nil {
				return handleSetupError(err)
			}
			warnOperatingRulesSkew(res.Summary)
			if err := printFrameworkUpdate(&frameworkUpdateOutput{Metadata: res.Metadata, Summary: res.Summary}, format); err != nil {
				return err
			}
			if rf.noCommit {
				return nil
			}
			return commitInstall(cmd.Context(), cmd.OutOrStdout(), projectRoot, untracked,
				"chore(framework): install APEX framework "+installLabel(res.Metadata), res.Metadata, nil)
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip fetching the framework and governance repos; use the releases they already hold")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass safety checks (already installed, dirty framework, non-main branch, modified project skills)")
	cmd.Flags().BoolVar(&forceClone, "force-clone", false, forceCloneHelp)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().StringVar(&projectName, "project-name", "", "Bootstrap value for project_name (skips the TUI prompt)")
	cmd.Flags().StringVar(&extensionsFlag, "extensions", "", "Bootstrap value for extensions, comma-separated (e.g. ext-adrs,ext-features). Empty string = none.")
	cmd.Flags().BoolVar(&noBootstrap, "no-bootstrap", false, "Skip _apex/config.yaml seeding entirely")
	addReleaseFlags(cmd, &rf)
	return cmd
}

func newFrameworkUpdateCmd(repoFlag, cwdFlag *string) *cobra.Command {
	var (
		noFetch      bool
		force        bool
		forceClone   bool
		outputFormat string
		dryRun       bool
		noMigrate    bool
		repair       bool
		plan         bool
		noCheck      bool
		rf           releaseFlags
	)
	cmd := &cobra.Command{
		Use: "update",
		// Takes none: without this, a stray argument is silently ignored
		// and the command answers 0 to an invocation nobody meant.
		Args:  cobra.NoArgs,
		Short: "Install the framework's newest release, run pending migrations, and commit the result",
		Long: `Install the framework's newest release tag (or the one --version names;
--from-worktree installs the repo's working tree instead) into <project>:

  - .claude/skills/apex-*  re-copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml re-copied from <repo>/_apex/pipelines
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

Then any pending PROJECT-DATA migration (PLAN-25 D10), and then the
framework's own per-version UPGRADE list from _apex/migrations/*.md.
Migrations run here rather than as a separate command a skill has to
police, so no skill ever meets an un-migrated project and no skill needs a
migration failure path. This is the right transaction boundary: explicitly
invoked, at the moment framework expectations change, outside the build
loop.

The upgrade list is ordered semantically — semver on 'version', integer on
'seq', honouring 'after:' — and never by filename, under which v0.9.0
sorts after v0.10.0. Applied ids are recorded in _apex/framework.yaml as
an ordered list, which is what makes a second run a no-op and a failed run
resumable. Only 'kind: derivable' entries are executed; 'kind: judged' is
listed with the skill to dispatch and is NEVER run, under any flag. An
entry's 'check:' reports and never gates: one that cannot run leaves the
entry unapplied-and-unverifiable, which is reported and blocks nothing.

The install and the migrations it ran are committed together as
'chore(framework): update APEX framework to vX.Y.Z', with Framework-Version,
Framework-Commit, Framework-Migrations and Generator trailers; an update
that changed nothing commits nothing. When this ape is older than the
release's min_ape_version, a terminal is offered an ape update first (bingo
when the project pins ape, else 'ape update'), committed as
'chore(ape): update ape to vA.B.C' when bingo changed, and the command
re-runs on the new ape; without a terminal it exits 11.

Until ape v0.4.0 this command committed nothing. --no-commit keeps that:
the whole result sits in the working tree for one 'git diff'. --repair's
output is never committed.

Does NOT touch _apex/config.yaml — that's the one-time bootstrap from
'ape framework setup'. To re-bootstrap, pass --force to 'setup'.

  --plan        print the upgrade-migration plan and do NOTHING ELSE — no
                install, no fetch, no migration. The plan is the list the
                project will hold AFTER this update: the installed entries
                overlaid by the repo's, with a SOURCE column marking the
                ones the update brings, so a migration can be read before
                it runs. The repo is read as it stands, unfetched. It
                distinguishes pending / applied / half-applied /
                cannot-tell rather than collapsing them, because a runner
                that reads cannot-tell as pending re-applies things
  --dry-run     show the framework drift AND the pending migrations,
                writing nothing
  --no-migrate  install framework files only; migrations stay pending, and
                'ape doctor' reports them so the state is visible
  --repair      also run the judgment phase over free-form deferred
                records. OFF by default: it spawns a paid opus session, and
                a file-copying verb should not start doing that silently.

Refuses to run when:
  - _apex/framework.yaml is absent (run 'ape framework setup' first)
  - committing, and the project is not a git repo, is on a detached HEAD,
    or has modified or staged tracked files (exit 4)
  - this ape is older than the release's min_ape_version (exit 11)
  - --from-worktree, and the framework repo is dirty, on a non-main
    branch, or its .claude/skills/apex-* subtree has uncommitted changes
    (pass --force to bypass)
  - your framework or governance clone is behind the release and has
    local changes (exit 3; pass --force-clone to overwrite them). A clone
    of yours only ever moves forward: one whose branch is ahead of the
    release, or diverged from it, is left where it is and reported. ape's
    own clones under the user cache are always overwritten

Exit codes: 2 usage; 3 the framework source (no release, missing tag,
build layout); 4 the project tree; 7 not installed; 11 ape below
min_ape_version; 12 a commit failed (the changes are left staged).

A migration is skipped (never forced) when ITS OWN paths have uncommitted
changes. The gate is path-scoped rather than whole-tree: those paths are
disjoint from what the install writes, so the two are order-independent,
and unrelated work-in-progress elsewhere does not block anything.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clone, err := resolveFrameworkClone(*repoFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			repo := clone.Path
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			sel, err := rf.selector(noFetch)
			if err != nil {
				return err
			}
			// --plan runs before the repo resolution's consequences and
			// before any install: it must be readable against a project in
			// any state, including one whose framework repo has moved on.
			if plan || dryRun {
				// Both write nothing, the clones included: ape's own is
				// not cloned for them.
				if err := requireCloned(clone); err != nil {
					return usageErrExit(exitCodeFrameworkValidation, err)
				}
			}
			if plan {
				return runFrameworkUpdatePlan(cmd, repo, projectRoot, sel, noCheck)
			}
			if dryRun {
				return emitFrameworkDryRun(cmd.Context(), cmd.OutOrStdout(), repo, projectRoot, rf.fromWorktree, noFetch)
			}
			format := output.Format(outputFormat)
			var untracked map[string]bool
			if !rf.noCommit {
				if untracked, err = commitPrecheck(cmd.Context(), projectRoot); err != nil {
					return err
				}
			}
			if meta, mErr := framework.ReadMetadata(projectRoot); mErr == nil {
				if moved, now := framework.TagMoved(cmd.Context(), repo, meta.Framework); moved {
					fmt.Fprintf(os.Stderr, "WARNING: %s\n", framework.TagMovedMessage(meta.Framework, now))
				}
			}
			fwTarget, sel, err := prepareFramework(cmd.Context(), clone, sel)
			if err != nil {
				return handleUpdateError(err)
			}
			sel, done, err := prepareInstall(cmd.Context(), repo, projectRoot, sel, !rf.noCommit, rf.version == "")
			if errors.Is(err, errKeepInstalled) {
				return nil
			}
			if err != nil {
				return handleUpdateError(err)
			}
			defer done()
			// The clones move last, after every check that could refuse
			// the install; an update that keeps an installed rc returned
			// above, moving neither.
			gov, err := syncClones(cmd.Context(), cloneNoteWriter(cmd, format), clone, fwTarget, projectRoot, noFetch, forceClone)
			if err != nil {
				return err
			}
			res, err := framework.Update(cmd.Context(), &framework.UpdateOptions{
				FrameworkRepo: repo,
				ProjectRoot:   projectRoot,
				NoFetch:       noFetch,
				Force:         force,
				ApeVersion:    Version,
				// Bootstrapper is intentionally nil — Update does not
				// seed config.yaml. installCore skips bootstrapConfig
				// when doBootstrap=false.
				Bootstrapper: framework.NoopBootstrapper{},
				NoMigrate:    noMigrate,
				Release:      sel,
				Governance:   gov,
			})
			if err != nil {
				return handleUpdateError(err)
			}
			warnOperatingRulesSkew(res.Summary)
			if err := printFrameworkUpdate(
				&frameworkUpdateOutput{Metadata: res.Metadata, Summary: res.Summary}, format,
			); err != nil {
				return err
			}
			if err := migrateAndCommitUpdate(cmd, projectRoot, res.Metadata, untracked, noMigrate, !rf.noCommit); err != nil {
				return err
			}
			if repair {
				return runFrameworkRepair(cmd, projectRoot)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip fetching the framework and governance repos; use the releases they already hold")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass safety checks (dirty framework, non-main branch, modified project skills)")
	cmd.Flags().BoolVar(&forceClone, "force-clone", false, forceCloneHelp)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the framework diff and pending migrations, writing nothing")
	cmd.Flags().BoolVar(&noMigrate, "no-migrate", false, "Install framework files only; leave migrations pending")
	cmd.Flags().BoolVar(&plan, "plan", false,
		"Print the upgrade-migration plan, incoming entries included, and do nothing else")
	cmd.Flags().BoolVar(&noCheck, "no-check", false,
		"With --plan: do not run any migration's check: command; every row falls back to the ledger alone")
	cmd.Flags().BoolVar(&repair, "repair", false, "Also run the opus judgment phase over free-form deferred records (spends money)")
	addReleaseFlags(cmd, &rf)
	return cmd
}

// migrateAndCommitUpdate is the half of `update` after the install: the
// project-data and upgrade migrations (unless --no-migrate), then the one
// commit recording the install and the migrations it ran.
func migrateAndCommitUpdate(cmd *cobra.Command, projectRoot string, installed framework.Metadata,
	untracked map[string]bool, noMigrate, commit bool,
) error {
	var applied []string
	if noMigrate {
		fmt.Fprintln(cmd.OutOrStdout(),
			"migration: skipped (--no-migrate) — `ape doctor` will report it as pending")
	} else {
		if err := runProjectMigrations(cmd.Context(), cmd.OutOrStdout(), projectRoot); err != nil {
			return err
		}
		var err error
		if applied, err = runUpgradeMigrationsApplied(cmd.Context(), cmd.OutOrStdout(), projectRoot); err != nil {
			return err
		}
	}
	if !commit {
		return nil
	}
	// Re-read: the migration ledger was appended after Update returned.
	meta := installed
	if m, err := framework.ReadMetadata(projectRoot); err == nil {
		meta = *m
	}
	return commitInstall(cmd.Context(), cmd.OutOrStdout(), projectRoot, untracked,
		"chore(framework): update APEX framework to "+installLabel(meta), meta, applied)
}

// runFrameworkUpdatePlan is `update --plan`: the upgrade-migration plan the
// project will hold after this update, and nothing else.
func runFrameworkUpdatePlan(cmd *cobra.Command, repo, projectRoot string, sel *framework.ReleaseSelector, noCheck bool) error {
	src, _, cleanup, err := readableSource(cmd.Context(), repo, sel)
	if err != nil {
		return handleUpdateError(err)
	}
	defer cleanup()
	// The same self-shadow the run uses, so `--plan` and the run
	// cannot be answered by two different `ape` binaries.
	runner, cleanupRunner, notice := migration.NewShellRunner()
	defer cleanupRunner()
	if notice != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "migrations: %s\n", notice)
	}
	p, err := loadIncomingMigrationPlan(cmd.Context(), projectRoot, src, runner, !noCheck)
	if err != nil {
		return err
	}
	emitMigrationPlan(cmd.OutOrStdout(), p, repo)
	return nil
}

// runFrameworkRepair runs the judgment phase after a migration, reusing
// the same command so its TTY refusal and record-count post-condition
// apply here too.
func runFrameworkRepair(cmd *cobra.Command, projectRoot string) error {
	repairCmd := newDeferredRepairCmd()
	repairCmd.SetOut(cmd.OutOrStdout())
	repairCmd.SetErr(cmd.ErrOrStderr())
	repairCmd.SetArgs([]string{"--cwd", projectRoot})
	return repairCmd.Execute()
}

func newFrameworkStatusCmd(repoFlag, cwdFlag *string) *cobra.Command {
	var (
		noFetch      bool
		outputFormat string
		fromWorktree bool
	)
	cmd := &cobra.Command{
		Use: "status",
		// Takes none: without this, a stray argument is silently ignored
		// and the command answers 0 to an invocation nobody meant.
		Args:  cobra.NoArgs,
		Short: "Inspect the installed framework version + drift report",
		Long: `Read <project>/_apex/framework.yaml and report what was installed.

When there is a framework clone — --repo, $APEX_FRAMEWORK_REPO, or ape's
own once cloned — also compares the install with what that repo offers now (after a best-effort tag fetch, unless
--no-fetch): the NEWEST RELEASE tag, which is what 'update' would install,
and whether the installed tag still names the installed commit (a moved
tag is reported, never followed). --from-worktree compares against the
repo's working-tree HEAD instead, as before ape v0.4.0.

Then it says where the framework and governance clones are, and where
each path came from (flag, env, config, or ape's cache), so you know where
to read their docs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			// The drift report needs a clone: ape's own counts once it
			// exists, and status never clones it.
			repo, _ := resolveFrameworkRepo(*repoFlag)
			res, err := framework.Status(cmd.Context(), framework.StatusOptions{
				ProjectRoot:   projectRoot,
				FrameworkRepo: repo,
				NoFetch:       noFetch,
				FromWorktree:  fromWorktree,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			if res.Drift != nil {
				for _, n := range res.Drift.Notes {
					if strings.HasPrefix(n, "framework tag moved:") {
						fmt.Fprintf(os.Stderr, "WARNING: %s\n", n)
					}
				}
			}
			format := output.Format(outputFormat)
			if err := printFrameworkStatus(res, format); err != nil {
				return err
			}
			if format == output.FormatHuman {
				printCloneLocations(cmd.OutOrStdout(), *repoFlag, projectRoot)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip the best-effort 'git fetch' against the framework repo")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolVar(&fromWorktree, "from-worktree", false, "Compare against the repo's working-tree HEAD instead of its newest release")
	return cmd
}

// warnOperatingRulesSkew emits a stderr note (in every output format) when
// the framework repo predates the operating-rules fragment, so the skip is
// visible to scripts that only read stdout for the JSON/YAML payload.
func warnOperatingRulesSkew(s framework.UpdateSummary) {
	if s.OperatingRulesSkipped {
		fmt.Fprintf(os.Stderr,
			"⚠ operating-rules: framework repo predates %s — skipped the fragment + %s managed block. "+
				"Upgrade the framework to a version that ships it to enable the always-on APEX rules.\n",
			framework.SubtreeOperatingRules, framework.ProjectClaudeMd)
	}
}

func resolveProjectRoot(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory: %w", err)
	}
	return wd, nil
}

// errBootstrapHeadlessNoArgs is returned when the project lacks
// _apex/config.yaml AND we're in a non-interactive context AND no
// bootstrap-related flags were provided. Headless callers MUST be
// explicit; we refuse to seed silently.
var errBootstrapHeadlessNoArgs = errors.New(
	"config bootstrap required but no TTY and no flags supplied — pass --project-name + --extensions, or --no-bootstrap, or re-run interactively",
)

func pickBootstrapper(projectRoot, projectName, extensionsFlag string, noBootstrap bool, format output.Format) (framework.Bootstrapper, error) {
	if noBootstrap {
		return framework.NoopBootstrapper{}, nil
	}
	configPresent := false
	if _, err := os.Stat(projectRoot + "/" + framework.ProjectConfig); err == nil {
		configPresent = true
	}
	// If config already exists, the install flow won't call
	// Bootstrap(); any bootstrapper works. Return Noop for safety.
	if configPresent {
		return framework.NoopBootstrapper{}, nil
	}
	// Flags supplied — short-circuit the TUI.
	if projectName != "" || extensionsFlag != "" {
		exts, err := parseExtensions(extensionsFlag)
		if err != nil {
			return nil, err
		}
		name := projectName
		if name == "" {
			name = framework.DefaultProjectName(projectRoot)
		}
		return framework.StaticBootstrapper{Values: framework.BootstrapValues{ProjectName: name, Extensions: exts}}, nil
	}
	// No flags. Need a TTY + human output to run the TUI.
	if !term.IsTerminal(int(os.Stdout.Fd())) || format != output.FormatHuman {
		return nil, errBootstrapHeadlessNoArgs
	}
	return framework.TUIBootstrapper{}, nil
}

func parseExtensions(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !framework.IsKnownExtension(r) {
			return nil, fmt.Errorf("unknown extension %q (known: %s)", r, strings.Join(framework.ExtensionIDs(), ", "))
		}
		out = append(out, r)
	}
	return out, nil
}

// handleSetupError maps Setup failures to structured stderr output +
// process exit codes. Mirrors handleUpdateError but also recognizes
// AlreadyInstalledError.
func handleSetupError(err error) error {
	if aie, ok := errors.AsType[*framework.AlreadyInstalledError](err); ok {
		fmt.Fprintf(os.Stderr, "Error: %s\n", aie.Error())
		os.Exit(exitCodeAlreadyInstalled)
	}
	return handleUpdateError(err)
}

// handleUpdateError maps Update failures to structured stderr output +
// process exit codes. Used by both setup and update via
// handleSetupError; the NotInstalledError case is update-specific but
// safe to surface from either entry point.
func handleUpdateError(err error) error {
	// Already carries its exit code, and its message when reported.
	if _, ok := errors.AsType[*exitError](err); ok {
		return err
	}
	var fve *framework.ValidationError
	var pse *framework.ProjectSkillsModifiedError
	var nie *framework.NotInstalledError
	switch {
	case errors.As(err, &nie):
		fmt.Fprintf(os.Stderr, "Error: %s\n", nie.Error())
		os.Exit(exitCodeNotInstalled)
	case errors.As(err, &fve):
		fmt.Fprintf(os.Stderr, "Error: %s\n", fve.Detail)
		os.Exit(exitCodeFrameworkValidation)
	case errors.As(err, &pse):
		fmt.Fprintln(os.Stderr, "Error: uncommitted changes under .claude/skills/apex-*:")
		for _, p := range pse.Paths {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		fmt.Fprintln(os.Stderr, "Pass --force to override.")
		os.Exit(exitCodeProjectSkillsModified)
	default:
		fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
	}
	return err
}

func printFrameworkUpdate(out *frameworkUpdateOutput, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, out)
	default:
		fmt.Printf(
			"Framework: %s @ %s (%s)\n",
			defaultStr(out.Metadata.Framework.RepoOrigin, "(no origin)"),
			defaultStr(out.Metadata.Framework.VersionTag, "(no tag)"),
			short(out.Metadata.Framework.GitHash),
		)
		fmt.Printf("Skills:    %d installed (%d removed)\n", out.Summary.SkillsInstalled, out.Summary.SkillsRemoved)
		fmt.Printf("Pipelines: %d installed\n", out.Summary.PipelinesInstalled)
		// Reported only when the framework carried a library. Zero is the
		// ordinary case for a framework that ships none, and every built-in
		// recipe reaches the project inside the binary regardless — so a
		// "0 installed" line would suggest a shortfall that does not exist.
		if n := out.Summary.AboardRecipesInstalled; n > 0 {
			fmt.Printf("Recipes:   %d installed into %s (`ape aboard recipes list`)\n",
				n, framework.ProjectAboardRecipesDir)
		}
		// Reported only when something happened, or when the board is not
		// this project's to make. A run that found everything already in
		// place says nothing, like every other line here.
		switch {
		case out.Summary.AboardParentRoot != "":
			fmt.Printf("Board:     not created — %s already has one, and a board here would be invisible from it\n",
				out.Summary.AboardParentRoot)
		case out.Summary.AboardCreated:
			fmt.Printf("Board:     %s/ created (`ape aboard serve`)\n", framework.ProjectAboardDir)
		case out.Summary.AboardGitignoreSeeded:
			fmt.Printf("Board:     %s added — the folder is committed, its contents are not\n",
				framework.ProjectAboardGitignore)
		}
		if out.Summary.ConfigSeeded {
			fmt.Printf(
				"Config:    seeded — project_name=%q extensions=[%s]\n",
				out.Metadata.Sources.Config.ProjectName,
				strings.Join(out.Metadata.Sources.Config.Extensions, ", "),
			)
		} else {
			fmt.Println("Config:    not seeded (already exists or bootstrap skipped)")
		}
		switch {
		case out.Summary.OperatingRulesInstalled:
			state := "already current"
			switch {
			case out.Summary.ClaudeMdCreated:
				state = "created"
			case out.Summary.ManagedBlockUpdated:
				state = "updated"
			}
			fmt.Printf("Op-rules:  fragment installed; %s managed block %s\n", framework.ProjectClaudeMd, state)
		case out.Summary.OperatingRulesSkipped:
			fmt.Println("Op-rules:  skipped (framework predates the operating-rules fragment)")
		}
		// Reported in BOTH directions, unlike the lines around it. An
		// absent roster is not a quiet default: it makes every dispatch's
		// commit-ownership assertion skip, and a release whose headline
		// check never fires should say so on the run that would have
		// installed it.
		if out.Summary.CommitOwnersInstalled {
			fmt.Printf("Commits:   %s installed — per-dispatch commit ownership is asserted\n",
				framework.ProjectCommitOwners)
		} else {
			fmt.Printf("Commits:   no %s in the framework — every dispatch's commit-ownership assertion will SKIP\n",
				framework.SubtreeCommitOwners)
		}
		// Both directions, for the same reason as the roster above: a
		// missing table is not a quiet default. The framework declares
		// which skills run under which output style, and if the file
		// never arrived every one of them runs the pinned default while
		// the framework's own declaration looks correct on its side.
		if out.Summary.OutputStylesInstalled {
			fmt.Printf("Styles:    %s installed — per-skill output styles apply to `ape task`\n",
				framework.ProjectOutputStyles)
		} else {
			fmt.Printf("Styles:    no %s in the framework — every skill runs the %s output style\n",
				framework.SubtreeOutputStyles, config.DefaultOutputStyle)
		}
		if out.Summary.EffortDefaultsInstalled {
			fmt.Printf("Effort:    %s installed — per-model effort defaults (`ape config effort`)\n",
				framework.ProjectEffortDefaults)
		} else {
			fmt.Printf("Effort:    no %s in the framework — every spawn keeps the legacy xhigh default\n",
				framework.SubtreeEffortDefaults)
		}
		if n := out.Summary.MigrationsInstalled; n > 0 {
			fmt.Printf("Migrations: %d upgrade entr%s installed into %s/\n",
				n, pluralY(n), framework.ProjectMigrationsDir)
		}
		if out.Summary.GitignoreLockAdded {
			// Reported only when it wrote. ape edited a file the operator
			// owns, so it says so; on every subsequent run the entry is
			// already there and a line about it would be noise.
			fmt.Printf("Ignore:    %s += %s (lock sidecar `ape sprint reconcile` leaves behind)\n",
				framework.ProjectGitignore, framework.GitignoreLockPattern)
		}
		// Reported only when it acted. ape moved a user's own run history,
		// so it says exactly how much and where to; on every later run there
		// is nothing left to move and a line about it would be noise.
		if n := out.Summary.RunsRelocated; n > 0 {
			fmt.Printf("Runs:      relocated %d run(s) into %s/ (was _output/pipelines, _output/tasks)\n",
				n, out.Summary.RunsRoot)
		}
		if c := out.Summary.RunsRelocationConflicts; len(c) > 0 {
			// Never folded into the count above: these did NOT move, and a
			// summary that implied they had would be the worst outcome here.
			fmt.Printf("Runs:      %d run(s) left in place — a run of the same id already exists at the new path:\n", len(c))
			for _, r := range c {
				fmt.Printf("             %s\n", r)
			}
			fmt.Println("           Nothing was overwritten. Compare the two and remove whichever is stale.")
		}
		fmt.Printf("Metadata:  %s\n", framework.ProjectMetadata)
		return nil
	}
}

func printFrameworkStatus(res *framework.StatusResult, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, res)
	default:
		branch := ""
		if res.Installed.Framework.GitBranch != "" {
			branch = " on branch " + res.Installed.Framework.GitBranch
		}
		fmt.Printf(
			"Installed: %s @ %s (%s) from %s%s\n",
			defaultStr(res.Installed.Framework.RepoOrigin, "(no origin)"),
			defaultStr(res.Installed.Framework.VersionTag, "(no tag)"),
			short(res.Installed.Framework.GitHash),
			defaultStr(res.Installed.Framework.Source, framework.SourceWorktree),
			branch,
		)
		fmt.Printf("Installed by ape v%s at %s\n", res.Installed.Ape.Version, res.Installed.InstalledAt.Format("2006-01-02 15:04:05 UTC"))
		fmt.Printf("Skills:    %d  Pipelines: %d  Config seeded: %t\n",
			res.Installed.Sources.Skills.Count, res.Installed.Sources.Pipelines.Count, res.Installed.Sources.Config.Seeded)
		if res.Current != nil {
			label := "Framework HEAD"
			if res.Current.Source == framework.SourceTag {
				label = "Newest release"
			}
			fmt.Printf("\n%s: %s (%s)\n", label, defaultStr(res.Current.VersionTag, "(no tag)"), short(res.Current.GitHash))
			if res.Drift != nil && len(res.Drift.Notes) > 0 {
				fmt.Println("Drift:")
				for _, n := range res.Drift.Notes {
					fmt.Println("  - " + n)
				}
			} else {
				fmt.Println("Drift: in sync")
			}
		}
		return nil
	}
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func short(sha string) string {
	const w = 7
	if len(sha) <= w {
		return sha
	}
	return sha[:w]
}
