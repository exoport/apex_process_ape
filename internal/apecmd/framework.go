package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/diegosz/apex_process_ape/internal/output"
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
  ape framework update     Refresh skills + pipelines against the framework
                           repo's current HEAD. Refuses if not yet set up
                           (run setup first).
  ape framework status     Inspect the installed framework version, with
                           optional drift report against the framework repo.

The framework repo path is resolved from --repo or $APEX_FRAMEWORK_REPO.
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
		outputFormat   string
		projectName    string
		extensionsFlag string
		noBootstrap    bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Initial install of framework skills + pipelines into the project",
		Long: `Initial install of framework-managed assets into <project>:

  - .claude/skills/apex-*  copied from <repo>/framework/_claude/skills
  - _apex/pipelines/*.yaml copied from <repo>/framework/_apex/pipelines
  - _apex/config.yaml      seeded (interactive prompt by default;
                           supply --project-name and --extensions to
                           skip the TUI; --no-bootstrap to skip seeding
                           entirely)
  - _apex/framework.yaml   metadata recording what was installed.

Refuses to run when:
  - _apex/framework.yaml already exists (pass --force to re-bootstrap;
    this resets project_name and extensions)
  - the framework repo is dirty, on a non-main branch, or its
    .claude/skills/apex-* subtree has uncommitted changes (pass
    --force to bypass)

Headless contexts: when stdout is not a TTY (or --output-format is not
human) and the project lacks _apex/config.yaml, you must supply
--project-name and --extensions, OR pass --no-bootstrap. Otherwise
'setup' refuses to seed silently.

For subsequent refreshes against a framework version bump, use
'ape framework update'.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := resolveFrameworkRepo(*repoFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			format := output.Format(outputFormat)
			bootstrap, err := pickBootstrapper(projectRoot, projectName, extensionsFlag, noBootstrap, format)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				if errors.Is(err, errBootstrapHeadlessNoArgs) {
					os.Exit(exitCodeBootstrapHeadlessNoArgs)
				}
				return err
			}
			res, err := framework.Setup(cmd.Context(), &framework.UpdateOptions{
				FrameworkRepo: repo,
				ProjectRoot:   projectRoot,
				NoFetch:       noFetch,
				Force:         force,
				ApeVersion:    Version,
				Bootstrapper:  bootstrap,
			})
			if err != nil {
				return handleSetupError(err)
			}
			return printFrameworkUpdate(&frameworkUpdateOutput{Metadata: res.Metadata, Summary: res.Summary}, format)
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip 'git fetch && merge --ff-only' on the framework repo before reading its state")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass safety checks (already installed, dirty framework, non-main branch, modified project skills)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().StringVar(&projectName, "project-name", "", "Bootstrap value for project_name (skips the TUI prompt)")
	cmd.Flags().StringVar(&extensionsFlag, "extensions", "", "Bootstrap value for extensions, comma-separated (e.g. ext-adrs,ext-features). Empty string = none.")
	cmd.Flags().BoolVar(&noBootstrap, "no-bootstrap", false, "Skip _apex/config.yaml seeding entirely")
	return cmd
}

func newFrameworkUpdateCmd(repoFlag, cwdFlag *string) *cobra.Command {
	var (
		noFetch      bool
		force        bool
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Refresh framework skills and pipelines against the framework repo",
		Long: `Refresh framework-managed assets in <project>:

  - .claude/skills/apex-*  re-copied from <repo>/framework/_claude/skills
  - _apex/pipelines/*.yaml re-copied from <repo>/framework/_apex/pipelines
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

Does NOT touch _apex/config.yaml — that's the one-time bootstrap from
'ape framework setup'. To re-bootstrap, pass --force to 'setup'.

Refuses to run when:
  - _apex/framework.yaml is absent (run 'ape framework setup' first)
  - the framework repo is dirty, on a non-main branch, or its
    .claude/skills/apex-* subtree has uncommitted changes (pass
    --force to bypass)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := resolveFrameworkRepo(*repoFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			format := output.Format(outputFormat)
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
			})
			if err != nil {
				return handleUpdateError(err)
			}
			return printFrameworkUpdate(&frameworkUpdateOutput{Metadata: res.Metadata, Summary: res.Summary}, format)
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip 'git fetch && merge --ff-only' on the framework repo before reading its state")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass safety checks (dirty framework, non-main branch, modified project skills)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func newFrameworkStatusCmd(repoFlag, cwdFlag *string) *cobra.Command {
	var (
		noFetch      bool
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect the installed framework version + drift report",
		Long: `Read <project>/_apex/framework.yaml and report what was installed.

When --repo or $APEX_FRAMEWORK_REPO is set, also reads the framework
repo's current HEAD (with a best-effort 'git fetch' unless --no-fetch
is passed) and emits drift fields comparing the installed git_hash /
version_tag against current.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectRoot, err := resolveProjectRoot(*cwdFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			repo := *repoFlag
			if repo == "" {
				repo = os.Getenv("APEX_FRAMEWORK_REPO")
			}
			res, err := framework.Status(cmd.Context(), framework.StatusOptions{
				ProjectRoot:   projectRoot,
				FrameworkRepo: repo,
				NoFetch:       noFetch,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
			}
			return printFrameworkStatus(res, output.Format(outputFormat))
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip the best-effort 'git fetch' against the framework repo")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	return cmd
}

func resolveFrameworkRepo(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv("APEX_FRAMEWORK_REPO"); env != "" {
		return env, nil
	}
	return "", errors.New("framework repo path not set: pass --repo or set APEX_FRAMEWORK_REPO")
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
	var aie *framework.AlreadyInstalledError
	if errors.As(err, &aie) {
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
		if out.Summary.ConfigSeeded {
			fmt.Printf(
				"Config:    seeded — project_name=%q extensions=[%s]\n",
				out.Metadata.Sources.Config.ProjectName,
				strings.Join(out.Metadata.Sources.Config.Extensions, ", "),
			)
		} else {
			fmt.Println("Config:    not seeded (already exists or bootstrap skipped)")
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
		fmt.Printf(
			"Installed: %s @ %s (%s) on branch %s\n",
			defaultStr(res.Installed.Framework.RepoOrigin, "(no origin)"),
			defaultStr(res.Installed.Framework.VersionTag, "(no tag)"),
			short(res.Installed.Framework.GitHash),
			res.Installed.Framework.GitBranch,
		)
		fmt.Printf("Installed by ape v%s at %s\n", res.Installed.Ape.Version, res.Installed.InstalledAt.Format("2006-01-02 15:04:05 UTC"))
		fmt.Printf("Skills:    %d  Pipelines: %d  Config seeded: %t\n",
			res.Installed.Sources.Skills.Count, res.Installed.Sources.Pipelines.Count, res.Installed.Sources.Config.Seeded)
		if res.Current != nil {
			fmt.Printf("\nFramework HEAD: %s (%s)\n", defaultStr(res.Current.VersionTag, "(no tag)"), short(res.Current.GitHash))
			if res.Drift != nil && (res.Drift.HashDrift || res.Drift.TagDrift) {
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
