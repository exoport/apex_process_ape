package apecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// ANSI colour escapes used by the doctor renderer. Kept narrow on
// purpose — only 16-colour codes so the output is legible on any
// terminal that survives the NO_COLOR / non-TTY gate in
// shouldColorizeWriter.
const (
	doctorAnsiYellow = "\x1b[33m"
	doctorAnsiRed    = "\x1b[31m"
	doctorAnsiDim    = "\x1b[2m"
	doctorAnsiBold   = "\x1b[1m"
)

// statusGlyph returns the emoji + ANSI colour to prefix a row with
// for a given status. Glyphs match the rest of the CLI's status
// vocabulary (✅ ⚠️ ❌ ⏭️ ℹ️) so screenshots stay consistent.
func statusGlyph(s DoctorStatus) (emoji, color string) {
	switch s {
	case StatusOK:
		return "✅", ansiGreen
	case StatusWarn:
		return "⚠️ ", doctorAnsiYellow
	case StatusFail:
		return "❌", doctorAnsiRed
	case StatusSkip:
		return "⏭️ ", doctorAnsiDim
	case StatusInfo:
		return "ℹ️ ", ansiBlue
	default:
		return "•", ""
	}
}

// shouldColorizeWriter reports whether ANSI colour escapes are safe to
// emit. Honours NO_COLOR and falls back to plain text whenever the
// writer is not an *os.File pointing at a terminal — covers pipes,
// redirects, CI logs, and the bytes.Buffer instances tests inject.
func shouldColorizeWriter(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// DoctorStatus is the per-check verdict in the doctor report.
type DoctorStatus string

const (
	StatusOK   DoctorStatus = "ok"
	StatusWarn DoctorStatus = "warn"
	StatusFail DoctorStatus = "fail"
	StatusSkip DoctorStatus = "skip"
	StatusInfo DoctorStatus = "info"
)

// CheckResult is the structured outcome of a single doctor probe.
type CheckResult struct {
	Name        string       `json:"name"                   yaml:"name"`
	Status      DoctorStatus `json:"status"                 yaml:"status"`
	Message     string       `json:"message"                yaml:"message"`
	Remediation string       `json:"remediation,omitempty"  yaml:"remediation,omitempty"`
	FixCommand  string       `json:"fix_command,omitempty"  yaml:"fix_command,omitempty"`
	DurationMs  int64        `json:"duration_ms"            yaml:"duration_ms"`
}

// DoctorReport is the top-level structured output of `ape doctor`.
type DoctorReport struct {
	Checks  []CheckResult `json:"checks"  yaml:"checks"`
	Summary DoctorSummary `json:"summary" yaml:"summary"`
}

// DoctorSummary aggregates per-status counts for quick CI consumption.
type DoctorSummary struct {
	OK   int `json:"ok"   yaml:"ok"`
	Warn int `json:"warn" yaml:"warn"`
	Fail int `json:"fail" yaml:"fail"`
	Skip int `json:"skip" yaml:"skip"`
	Info int `json:"info" yaml:"info"`
}

// doctorEnv carries the resolved environment a check sees. Tests inject
// it directly so probes don't have to touch the real filesystem.
type doctorEnv struct {
	ProjectRoot string
	Home        string
	OS          string
	Arch        string
	OSRelease   map[string]string
}

// doctorCheck pairs a probe function with its name and severity.
type doctorCheck struct {
	Name     string
	Required bool
	Run      func(ctx context.Context, env doctorEnv) CheckResult
}

// allChecks is the registry consumed by the cobra command. Tests can
// reference individual probes directly without going through this list.
var allChecks = []doctorCheck{
	{Name: "claude.binary", Required: true, Run: checkClaudeBinary},
	{Name: "git.binary", Required: true, Run: checkGitBinary},
	{Name: "node.binary", Run: checkNodeBinary},
	{Name: "npx.binary", Run: checkNpxBinary},
	{Name: "playwright.host_supported", Run: checkPlaywrightHostSupported},
	{Name: "playwright.cache", Run: checkPlaywrightCache},
	{Name: "framework.metadata", Run: checkFrameworkMetadata},
	{Name: "skills.project", Run: checkSkillsProject},
	{Name: "skills.user", Run: checkSkillsUser},
	{Name: "pipelines.project", Run: checkPipelinesProject},
	{Name: "permissions.home_claude", Run: checkPermissionsHomeClaude},
	{Name: "ape.update_available", Run: checkApeUpdateAvailable},
}

func newDoctorCmd() *cobra.Command {
	var (
		outputFormat string
		strict       bool
		skipCSV      string
		cwdFlag      string
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the local environment for prerequisites",
		Long: `Probe the local environment for prerequisites and report a per-check
verdict.

Doctor runs a fixed set of checks against the host (claude / git /
node / npx binaries, Playwright host compatibility, ~/.claude
writability) and the project at --cwd (framework metadata, installed
skills + pipelines). Project-scoped checks degrade to INFO when run
outside a project root.

Exit codes:
  0  every required check passed (warnings allowed unless --strict)
  1  at least one required check failed (or any warning under --strict)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := resolveDoctorEnv(cwdFlag)
			if err != nil {
				return err
			}
			skip := parseSkipList(skipCSV)
			report := runDoctor(cmd.Context(), allChecks, env, skip)
			format := output.Format(outputFormat)
			if err := emitDoctorReport(cmd.OutOrStdout(), report, format); err != nil {
				return err
			}
			if doctorShouldFail(report, strict) {
				os.Exit(1) //nolint:gocritic // cobra-managed exit code; the report has already flushed.
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolVar(&strict, "strict", false, "Treat WARN-level findings as failures (exit 1)")
	cmd.Flags().StringVar(&skipCSV, "skip", "", "Comma-separated list of check names to skip (e.g. node.binary,npx.binary)")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root to probe (default: current working directory)")
	return cmd
}

// resolveDoctorEnv assembles the doctorEnv passed to each check. The
// project root defaults to the working directory; pass --cwd to override.
func resolveDoctorEnv(cwdFlag string) (doctorEnv, error) {
	root := cwdFlag
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return doctorEnv{}, fmt.Errorf("resolve cwd: %w", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return doctorEnv{}, fmt.Errorf("abs(%s): %w", root, err)
	}
	home, _ := os.UserHomeDir() // best-effort; some checks tolerate empty
	return doctorEnv{
		ProjectRoot: abs,
		Home:        home,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		OSRelease:   readOSRelease(),
	}, nil
}

func parseSkipList(csv string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

// runDoctor executes each check (or marks it skipped) and aggregates
// the report. Exposed for tests; the cobra command goes through the
// same path.
func runDoctor(ctx context.Context, checks []doctorCheck, env doctorEnv, skip map[string]struct{}) DoctorReport {
	if ctx == nil {
		ctx = context.Background()
	}
	report := DoctorReport{Checks: make([]CheckResult, 0, len(checks))}
	for _, c := range checks {
		if _, skipped := skip[c.Name]; skipped {
			report.Checks = append(report.Checks, CheckResult{
				Name:    c.Name,
				Status:  StatusSkip,
				Message: "skipped by --skip flag",
			})
			continue
		}
		start := time.Now()
		res := c.Run(ctx, env)
		res.Name = c.Name
		res.DurationMs = time.Since(start).Milliseconds()
		// Non-required checks downgrade FAIL to WARN — only required
		// checks can hard-fail the run.
		if !c.Required && res.Status == StatusFail {
			res.Status = StatusWarn
		}
		report.Checks = append(report.Checks, res)
	}
	report.Summary = tallyReport(report.Checks)
	return report
}

func tallyReport(checks []CheckResult) DoctorSummary {
	var s DoctorSummary
	for _, c := range checks {
		switch c.Status {
		case StatusOK:
			s.OK++
		case StatusWarn:
			s.Warn++
		case StatusFail:
			s.Fail++
		case StatusSkip:
			s.Skip++
		case StatusInfo:
			s.Info++
		}
	}
	return s
}

func doctorShouldFail(r DoctorReport, strict bool) bool {
	if r.Summary.Fail > 0 {
		return true
	}
	if strict && r.Summary.Warn > 0 {
		return true
	}
	return false
}

func emitDoctorReport(w io.Writer, r DoctorReport, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case output.FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer enc.Close()
		return enc.Encode(r)
	default:
		if shouldColorizeWriter(w) {
			return emitDoctorHumanColor(w, r)
		}
		return emitDoctorHuman(w, r)
	}
}

// emitDoctorHuman writes the aligned table form. Tabwriter handles
// the column alignment so the status label and check name line up
// regardless of message length. This is the plain (non-colour) path
// used when stdout is not a terminal — CI logs, pipes, redirects.
func emitDoctorHuman(w io.Writer, r DoctorReport) error {
	fmt.Fprintln(w, "ape doctor — environment health")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCHECK\tDETAIL")
	for _, c := range r.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", strings.ToUpper(string(c.Status)), c.Name, c.Message)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	writeRemediationsAndSummary(w, r, false)
	return nil
}

// emitDoctorHumanColor mirrors emitDoctorHuman but emits an emoji
// prefix + ANSI-coloured STATUS label per row. Avoids tabwriter
// because ANSI escapes and emoji glyphs confuse its width accounting;
// instead we pre-compute the longest check name and pad manually.
func emitDoctorHumanColor(w io.Writer, r DoctorReport) error {
	fmt.Fprintf(w, "%sape doctor%s — environment health\n\n", doctorAnsiBold, ansiReset)

	maxCheck := len("CHECK")
	for _, c := range r.Checks {
		if len(c.Name) > maxCheck {
			maxCheck = len(c.Name)
		}
	}

	// Header row. Two leading spaces account for the emoji+space prefix
	// rows carry below it, so the CHECK header lines up with the check
	// names.
	fmt.Fprintf(w, "   %s%-6s %-*s %s%s\n",
		doctorAnsiDim, "STATUS", maxCheck, "CHECK", "DETAIL", ansiReset)

	for _, c := range r.Checks {
		emoji, color := statusGlyph(c.Status)
		label := strings.ToUpper(string(c.Status))
		// Pad STATUS label to 4 chars (OK/WARN/FAIL/SKIP/INFO max). ANSI
		// escapes have zero visual width, so the padding inside the
		// colour wrap still produces aligned columns.
		fmt.Fprintf(w, "%s %s%-4s%s  %-*s  %s\n",
			emoji, color, label, ansiReset, maxCheck, c.Name, c.Message)
	}

	writeRemediationsAndSummary(w, r, true)
	return nil
}

// writeRemediationsAndSummary prints the remediation block (if any
// rows carry one) and the per-status footer. Colour is applied only
// when colorize is true.
func writeRemediationsAndSummary(w io.Writer, r DoctorReport, colorize bool) {
	first := true
	for _, c := range r.Checks {
		if c.Remediation == "" && c.FixCommand == "" {
			continue
		}
		if first {
			fmt.Fprintln(w)
			if colorize {
				fmt.Fprintf(w, "%sRemediations:%s\n", doctorAnsiBold, ansiReset)
			} else {
				fmt.Fprintln(w, "Remediations:")
			}
			first = false
		}
		emoji, color := statusGlyph(c.Status)
		if colorize {
			fmt.Fprintf(w, "  %s %s%s%s\n", emoji, color, c.Name, ansiReset)
		} else {
			fmt.Fprintf(w, "  %s\n", c.Name)
		}
		if c.Remediation != "" {
			fmt.Fprintf(w, "    %s\n", c.Remediation)
		}
		if c.FixCommand != "" {
			if colorize {
				fmt.Fprintf(w, "    %s$%s %s\n", doctorAnsiDim, ansiReset, c.FixCommand)
			} else {
				fmt.Fprintf(w, "    $ %s\n", c.FixCommand)
			}
		}
	}

	fmt.Fprintln(w)
	if colorize {
		fmt.Fprintf(w,
			"%s%d ok%s · %s%d warn%s · %s%d info%s · %s%d fail%s · %s%d skip%s\n",
			ansiGreen, r.Summary.OK, ansiReset,
			doctorAnsiYellow, r.Summary.Warn, ansiReset,
			ansiBlue, r.Summary.Info, ansiReset,
			doctorAnsiRed, r.Summary.Fail, ansiReset,
			doctorAnsiDim, r.Summary.Skip, ansiReset,
		)
	} else {
		fmt.Fprintf(w, "%d ok · %d warn · %d info · %d fail · %d skip\n",
			r.Summary.OK, r.Summary.Warn, r.Summary.Info, r.Summary.Fail, r.Summary.Skip)
	}
}

// readOSRelease parses /etc/os-release into a map. Returns an empty
// map on non-Linux hosts or when the file can't be read; checks that
// need this data should tolerate emptiness rather than fail.
func readOSRelease() map[string]string {
	out := make(map[string]string)
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

// checkNames is exposed for documentation / tab completion; callers
// outside the package can introspect which checks exist without
// running the command.
func checkNames() []string {
	names := make([]string, 0, len(allChecks))
	for _, c := range allChecks {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}
