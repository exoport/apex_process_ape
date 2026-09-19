package apecmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/change"
	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/stamp"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Exit codes `ape change` adds to the shared table. Command-local, as
// `ape framework`'s are, because no other command means these things;
// the reservation is recorded in exitcodes.go so nothing else takes them.
//
// The three are OUTCOMES, and a run has exactly one: the skill either
// did the work, sent it somewhere else, declined it, or stopped part way
// through. ExitRunFailed and ExitCommitContract are not outcomes — they
// say the run or the commit never got that far — and they win over all
// three.
const (
	// exitChangeEscalated: the lane refused the work as too big for it and
	// named a route. The route's commands print on stdout (B.5).
	exitChangeEscalated = 8
	// exitChangeRefused: the lane declined the request outright.
	exitChangeRefused = 9
	// exitChangeHalted: a goal stopped mid-flight. Earlier goals are
	// committed, the rest is residue, and the blocking condition prints.
	exitChangeHalted = 10
)

// The dispatch `ape change` makes. Both names are the framework's, and
// the pairing is the plan's: the persona fronts the skill, and the skill
// does the work with its hands off git.
const (
	changeSkill = "apex-maintenance"
	changeAgent = "apex-agent-coder"
	// changePromptFlag is the skill flag the request is forwarded
	// through. Not `--prompt`: the skill's own vocabulary calls this a
	// request, and the flag name is part of its interface.
	changePromptFlag = "--request"
	// changeContractFile is the name ape composes for the skill's
	// terminal contract inside the change directory.
	changeContractFile = "contract.yaml"
	// changeRequestFile is ape's own copy of the request. B.5 prints this
	// path into escalation commands, never the caller's, which may be a
	// temp file that is gone by the time anyone runs them.
	changeRequestFile = "request.txt"
)

func newChangeCmd() *cobra.Command {
	var (
		requestFile  string
		fixesFlag    string
		reviewFlag   bool
		modelFlag    string
		effortFlag   string
		dryRunFlag   bool
		contractOut  string
		outputFormat string
		quietFlag    bool
		cwdFlag      string
	)
	cmd := &cobra.Command{
		Use:   "change [request]",
		Short: "Run one maintenance change: dispatch the lane, then commit its goals",
		Long: `Carry one maintenance request through the lean lane.

ape dispatches the apex-maintenance skill with its hands off git, reads
the terminal contract the skill writes, and then composes and makes
every commit itself. The skill never runs git; ape holds the pen.

The request is text that gets TYPED INTO A REPL as keystrokes, so it
comes from a file or stdin rather than argv:

  --request-file <path>   read the request from a file
  --request-file -        read it from stdin
  <request>               positional, for a human at a shell

A newline, a control character or a trailing backslash is refused
before anything spawns: the first submits the line early, the last
never submits at all.

--fixes <deferred-record-id> names the record this change discharges. It
may be given alone, in which case there is no request and no Request:
trailer, or together with one.

Artifacts land under {output_folder}/ape/changes/<change-id>/: the
request verbatim, the skill's contract, the change record, and — where
a run left edits in the tree — the residue it could not commit.

Exit codes: 0 every goal landed · 1 the run failed, or its contract was
missing or invalid · 2 usage or preflight · 3 REPL never became ready ·
4 claude died · 5 upstream API · 6 the commit was refused (ownership,
reconciliation or validation) · 8 escalated, with the route's commands
on stdout · 9 refused by the lane · 10 halted part way, with earlier
goals committed and the rest saved as residue.`,
		Example: `  ape change "the CLI reference is out of date"
  ape change --request-file /tmp/request.txt --output-format json
  printf '%s' "$req" | ape change --request-file -
  ape change --fixes 20260919-a3f1c2 --review`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode := outputFormat == "json"
			if !jsonMode && outputFormat != "human" {
				return usageErr(fmt.Errorf("--output-format must be human or json, got %q", outputFormat))
			}
			positional := ""
			if len(args) == 1 {
				positional = args[0]
			}
			request, err := resolveChangeRequest(positional, requestFile, cmd.InOrStdin(), fixesFlag)
			if err != nil {
				return usageErr(err)
			}
			o := changeOptions{
				request:     request,
				fixes:       strings.TrimSpace(fixesFlag),
				review:      reviewFlag,
				model:       resolveModelArg(modelFlag),
				effort:      effortFlag,
				dryRun:      dryRunFlag,
				contractOut: contractOut,
				jsonMode:    jsonMode,
				// Quiet by default off a terminal: the conductor runs this
				// in a background shell and reads the envelope, and the
				// per-event stream would land in its transcript.
				quiet:   quietFlag || !term.IsTerminal(int(os.Stdout.Fd())),
				cwdFlag: cwdFlag,
			}
			return runChange(cmd.Context(), o)
		},
	}
	cmd.Flags().StringVar(&requestFile, "request-file", "", `File holding the request; "-" reads stdin`)
	cmd.Flags().StringVar(&fixesFlag, "fixes", "", "Deferred record id this change discharges (may be given alone)")
	cmd.Flags().BoolVar(&reviewFlag, "review", false, "Ask the lane to review its own change before reporting")
	cmd.Flags().StringVar(&modelFlag, "model", "", "Claude model for the dispatch")
	cmd.Flags().StringVar(&effortFlag, "effort", "", "Reasoning effort (low|medium|high|xhigh|max)")
	cmd.Flags().BoolVar(&dryRunFlag, "dry-run", false, "Print the messages ape would compose and commit nothing, leaving the tree as the run left it")
	cmd.Flags().StringVar(&contractOut, "contract-out", "",
		"Where the skill writes its terminal contract (default: contract.yaml in the change directory; must sit inside it)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress the per-event progress stream (the default when stdout is not a terminal)")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	return cmd
}

// changeOptions is one resolved `ape change` invocation.
type changeOptions struct {
	request     string
	fixes       string
	review      bool
	model       string
	effort      string
	dryRun      bool
	contractOut string
	jsonMode    bool
	quiet       bool
	cwdFlag     string
}

// resolveChangeRequest applies B.1's grammar: a request, --fixes, or
// both. The request comes from exactly one source.
func resolveChangeRequest(positional, requestFile string, stdin io.Reader, fixes string) (string, error) {
	hasPositional := strings.TrimSpace(positional) != ""
	hasFile := requestFile != ""

	switch {
	case hasPositional && hasFile:
		return "", errors.New("a request and --request-file are mutually exclusive: pass the text once")
	case !hasPositional && !hasFile:
		if strings.TrimSpace(fixes) == "" {
			return "", errors.New("nothing to do: pass a request, --request-file, or --fixes <deferred-record-id>")
		}
		// --fixes alone. The record is the request; no Request: trailer.
		return "", nil
	case hasPositional:
		return validateTypedLine([]byte(positional))
	default:
		data, err := readTextInput(requestFile, stdin)
		if err != nil {
			return "", err
		}
		return validateTypedLine(data)
	}
}

// computeChangeID is YYYYMMDD-HHMMSS-<7-char hash>, the shape ape's run
// ids already use. The hash mixes the nanosecond clock, the request and
// the project so two changes in the same second cannot collide — the
// monotonic stamp is second-granularity, and a collision here would be
// two runs writing into one directory.
func computeChangeID(at time.Time, request, projectRoot string) string {
	at = at.UTC()
	seed := strconv.FormatInt(at.UnixNano(), 10) + "|" + request + "|" + projectRoot
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s-%s", at.Format("20060102-150405"), hex.EncodeToString(sum[:4])[:7])
}

// resolveContractOut places the skill's contract file.
//
// The default is inside the change directory, and an override must stay
// there. Anywhere else is refused, because {output_folder}/ape is the
// one subtree preflight has established is git-ignored: a contract
// written outside it is an untracked file in the working tree that no
// goal claims, and the reconciliation would then be right to refuse the
// whole run over ape's own artifact.
func resolveContractOut(changeDir, override string) (string, error) {
	if override == "" {
		return filepath.Join(changeDir, changeContractFile), nil
	}
	abs, err := filepath.Abs(override)
	if err != nil {
		return "", fmt.Errorf("resolve --contract-out: %w", err)
	}
	rel, err := filepath.Rel(changeDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("--contract-out must sit inside the change directory (%s): "+
			"a contract written elsewhere is an untracked file no goal claims, and the "+
			"reconciliation would refuse the run over ape's own artifact", changeDir)
	}
	return abs, nil
}

// resolveFixesRecord checks --fixes against the store before anything
// spawns. An id ape cannot resolve is an id the skill cannot read, and a
// closed one names work that is already discharged.
func resolveFixesRecord(cfg *apexcfg.Resolved, id string) (deferred.Record, error) {
	if id == "" {
		return deferred.Record{}, nil
	}
	rec, err := deferred.New(cfg.Paths.Deferred).Find(id)
	if err != nil {
		if errors.Is(err, deferred.ErrNotFound) {
			return deferred.Record{}, fmt.Errorf("--fixes %s: no such deferred record", id)
		}
		return deferred.Record{}, fmt.Errorf("--fixes %s: %w", id, err)
	}
	if !rec.IsOpen() {
		return deferred.Record{}, fmt.Errorf("--fixes %s: record is %s, so there is nothing left to discharge",
			id, rec.Status)
	}
	return rec, nil
}

// changeStart resolves the project, runs the preflight, and lays down
// the change directory. Everything here happens before a claude process
// exists, so every failure is exit 2.
func changeStart(ctx context.Context, o changeOptions) (*changeRun, error) {
	cfg := tryResolveProjectConfig(o.cwdFlag)
	if cfg == nil {
		return nil, usageErr(errors.New("no _apex/config.yaml in this directory or any parent: " +
			"`ape change` runs from a project root"))
	}
	if err := changePreflight(ctx, cfg); err != nil {
		return nil, err
	}
	fixesRec, err := resolveFixesRecord(cfg, o.fixes)
	if err != nil {
		return nil, usageErr(err)
	}

	at := stamp.New(cfg.Root, nil).Now()
	id := computeChangeID(at, o.request, cfg.Root)
	dir := runlog.ChangeDir(cfg.Root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, usageErr(fmt.Errorf("create change directory: %w", err))
	}
	contractPath, err := resolveContractOut(dir, o.contractOut)
	if err != nil {
		return nil, usageErr(err)
	}
	// A leftover from a killed run would otherwise be read as this run's
	// contract. Removed rather than refused: the path is ape's own
	// composition, so there is nothing here a caller could have meant to
	// keep.
	if err := os.Remove(contractPath); err != nil && !os.IsNotExist(err) {
		return nil, usageErr(fmt.Errorf("clear the contract path: %w", err))
	}
	if o.request != "" {
		if err := os.WriteFile(filepath.Join(dir, changeRequestFile), []byte(o.request), 0o600); err != nil {
			return nil, usageErr(fmt.Errorf("write the request: %w", err))
		}
	}
	return &changeRun{
		cfg:          cfg,
		id:           id,
		dir:          dir,
		contractPath: contractPath,
		startedAt:    at,
		fixesRecord:  fixesRec,
	}, nil
}

// changeRun is one change's resolved context: where it writes, what it
// dispatches, and what it has learned so far.
type changeRun struct {
	cfg          *apexcfg.Resolved
	id           string
	dir          string
	contractPath string
	startedAt    time.Time
	fixesRecord  deferred.Record
}

// skillArgs assembles the flags that follow the skill name on the
// invocation line. Whitespace-separated, as the task runner splits them,
// which is why the contract path is project-relative and ape-composed
// rather than an arbitrary caller path.
func (r *changeRun) skillArgs(o changeOptions) string {
	rel, err := filepath.Rel(r.cfg.Root, r.contractPath)
	if err != nil {
		rel = r.contractPath
	}
	args := []string{"--contract-out", filepath.ToSlash(rel)}
	if o.review {
		args = append(args, "--review")
	}
	if o.fixes != "" {
		args = append(args, "--fixes", o.fixes)
	}
	return strings.Join(args, " ")
}

// taskOptions builds the dispatch. The skill is told not to commit and
// is handed a contract path; everything else about the invocation is the
// ordinary task runner's.
func (r *changeRun) taskOptions(o changeOptions) taskOptions {
	return taskOptions{
		skill:          changeSkill,
		agent:          changeAgent,
		model:          o.model,
		effort:         o.effort,
		args:           r.skillArgs(o),
		prompt:         o.request,
		promptFlagName: changePromptFlag,
		// The skill layer's own flag. ape holds the pen for this
		// dispatch, and the non-committer assertion is what checks that
		// the skill kept its hands off git.
		skillNoCommit: true,
		projectRoot:   r.cfg.Root,
		quiet:         o.quiet,
		jsonMode:      o.jsonMode,
	}
}

// composedCommit is one commit ape made, in the order it made them.
type composedCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	// Goal is the 1-based goal this commit belongs to, or 0 for a commit
	// that is not a goal's own (the evidence and deferred commits).
	Goal int `json:"goal"`
}

// changeEnvelope is `--output-format json`. snake_case is the wire
// contract, as it is for `ape task`.
//
//nolint:tagliatelle // envelope mirrors the manifest's snake_case contract
type changeEnvelope struct {
	ChangeID string `json:"change_id"`
	// Outcome is the contract's maintenance_status, and is EMPTY when the
	// run produced no contract — a dispatch that died, or one whose
	// contract would not parse. The key is always present so a consumer
	// can read it without guarding; the exit code and error say what
	// happened when it is empty.
	Outcome  string           `json:"outcome"`
	ExitCode int              `json:"exit_code"`
	Commits  []composedCommit `json:"commits"`
	// Route is where escalated work goes; NextCommands is what to run,
	// from the framework's own table.
	Route             string   `json:"route,omitempty"`
	NextCommands      []string `json:"next_commands,omitempty"`
	BlockingCondition string   `json:"blocking_condition,omitempty"`
	// Residue is every path left in the working tree, saved under the
	// change directory. Empty when the tree is clean.
	Residue []string `json:"residue"`
	// The two paths a caller would otherwise have to construct, both
	// project-relative.
	ChangeDir    string  `json:"change_dir"`
	ManifestPath string  `json:"manifest_path,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	Error        *string `json:"error,omitempty"`
}

func runChange(ctx context.Context, o changeOptions) error {
	r, err := changeStart(ctx, o)
	if err != nil {
		return err
	}

	res, derr := dispatchTask(ctx, r.taskOptions(o))
	if derr != nil {
		return usageErr(derr)
	}

	env := changeEnvelope{
		ChangeID:     r.id,
		ChangeDir:    relTo(r.cfg.Root, r.dir),
		ManifestPath: res.Envelope.ManifestPath,
		CostUSD:      res.Envelope.CostUSD,
		Commits:      []composedCommit{},
		Residue:      []string{},
	}
	// What the dispatch left behind, read from git rather than from the
	// contract. Everything downstream reconciles against this.
	changed, changedErr := change.Changed(ctx, r.cfg.Root)
	if changedErr != nil {
		return reportChange(o, env, failErr(changedErr))
	}
	env.Residue = changed

	// The dispatch's own verdict comes first: a run that never became
	// ready, died, or moved HEAD when it was told not to is reported as
	// that, not as a contract that happens to be missing.
	if res.Envelope.ExitCode != ExitOK {
		env.ExitCode = res.Envelope.ExitCode
		if res.RunErr != nil {
			msg := res.RunErr.Error()
			env.Error = &msg
		}
		for _, v := range res.Contract.Violations {
			fmt.Fprintf(os.Stderr, "Error: %s: %s\n", v.Check, v.Message)
		}
		return reportChange(o, env, reportedErr(env.ExitCode, errors.New("the dispatch failed")))
	}

	contract, contractErr := change.ReadContract(r.contractPath)
	if contractErr != nil {
		// Exit 1, and nothing committed. ape never infers `landed` from a
		// tree it cannot account for: the edits stay where they are and
		// the paths are reported, which is what the operator needs to
		// decide what happened.
		env.ExitCode = ExitRunFailed
		msg := contractErr.Error()
		env.Error = &msg
		return reportChange(o, env, reportedErr(ExitRunFailed, contractErr))
	}

	env.Outcome = contract.Status
	if !change.None(contract.Route) {
		env.Route = strings.TrimSpace(contract.Route)
	}
	if !change.None(contract.BlockingCondition) {
		env.BlockingCondition = strings.TrimSpace(contract.BlockingCondition)
	}
	env.ExitCode = changeExitCode(contract.Status)
	if env.ExitCode == ExitOK {
		return reportChange(o, env, nil)
	}
	return reportChange(o, env, reportedErr(env.ExitCode, errors.New(contract.Status)))
}

// changeExitCode maps the contract's outcome onto the process status.
// The outcomes are mutually exclusive: one run, one outcome.
func changeExitCode(status string) int {
	switch status {
	case change.StatusLanded:
		return ExitOK
	case change.StatusEscalated:
		return exitChangeEscalated
	case change.StatusRefused:
		return exitChangeRefused
	case change.StatusHalted:
		return exitChangeHalted
	default:
		// check() has already refused an unknown status, so this is
		// unreachable through ReadContract. Reported as a failed run
		// rather than as a successful one if it is ever reached.
		return ExitRunFailed
	}
}

// reportChange writes the envelope or the human summary, then returns
// the error that carries the exit code.
func reportChange(o changeOptions, env changeEnvelope, exitErr error) error {
	if o.jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(env); err != nil {
			return err
		}
		return exitErr
	}
	printChangeSummary(os.Stdout, env)
	return exitErr
}

// printChangeSummary is the human report: what ape committed, what it
// could not, and where the rest of it is.
func printChangeSummary(w io.Writer, env changeEnvelope) {
	outcome := env.Outcome
	if outcome == "" {
		outcome = "no contract"
	}
	fmt.Fprintf(w, "change %s — %s\n", env.ChangeID, outcome)
	for _, c := range env.Commits {
		fmt.Fprintf(w, "  %s  %s\n", c.SHA, c.Subject)
	}
	if env.Route != "" {
		fmt.Fprintf(w, "  route: %s\n", env.Route)
	}
	if env.BlockingCondition != "" {
		fmt.Fprintf(w, "  blocked: %s\n", env.BlockingCondition)
	}
	if len(env.Residue) > 0 {
		fmt.Fprintf(w, "  %d %s left in the tree, saved under %s:\n",
			len(env.Residue), plural(len(env.Residue), "path", "paths"), env.ChangeDir)
		for _, p := range env.Residue {
			fmt.Fprintf(w, "    %s\n", p)
		}
	}
	if env.Error != nil {
		fmt.Fprintf(w, "  error: %s\n", *env.Error)
	}
}

// plural picks the noun form. Local to the command's own output.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
