package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"github.com/spf13/cobra"
)

// `ape eval conduct` hosts the orchestrator persona for the framework's
// eval, and only for it.
//
// v0.2.0 refuses `ape prompt --agent apex-orchestrator` (ape never runs
// inside a session ape started), which would end the eval's persona stages:
// the conductor dispatches `ape change`, and a halt stage asserts what it
// says when the scope is ambiguous. This is the one sanctioned exception,
// and it is shaped to stay one:
//
//   - a separate hidden verb rather than a flag on a command operators run
//     daily, refused unless APE_EVAL_HOST=1;
//   - the conductor's session carries NO APE_SESSION marker — the inner
//     `ape change` / `ape task` it dispatches marks its own session, and the
//     orchestrator HALTs on activation if it sees one, so a marker here
//     would make the halt stage pass for the wrong reason;
//   - it refuses to start inside an ape session, so it can never nest.
//
// Everything else is `ape prompt`'s path: PTY, trust dialog, env scrub,
// PATH pin, the per-model effort table, the bridge and the run record.
// What differs is the completion rule (sessiondriver.SetConductMode): the
// conductor backgrounds its dispatch and ends its turn to await it, so a
// Stop reporting background work is a yield, and the run ends at the
// first Stop reporting none.

// conductHost is the record's `host:` for a conductor session.
const conductHost = "eval-conduct"

// envEvalHost gates the verb.
const envEvalHost = "APE_EVAL_HOST"

// Command-local exit codes (see exitcodes.go: codes above 6 are local,
// and 8..10 are reserved for `ape change`). 0..5 keep their shared meaning.
const (
	exitConductIdleTimeout = 20
	exitConductMaxDuration = 21
	exitConductNotEvalHost = 22
	exitConductNested      = 23
)

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "eval",
		Short:  "Eval-only hosts (not for operators)",
		Hidden: true,
	}
	cmd.AddCommand(newEvalConductCmd())
	return cmd
}

func newEvalConductCmd() *cobra.Command {
	var (
		requestFile     string
		modelFlag       string
		cwdFlag         string
		idleTimeoutFlag time.Duration
		maxDurationFlag time.Duration
		outputFormat    string
		quietFlag       bool
	)
	cmd := &cobra.Command{
		Use:   "conduct",
		Short: "Host the orchestrator persona for the framework eval",
		Long: `Host the APEX orchestrator for the framework eval, and nothing else.

Types /apex-orchestrator --autonomous -- <request> into a PTY-hosted claude
session, as an operator would in a plain Claude Code session. The request
comes from --request-file (a path, or - for stdin).

The conductor's session carries NO APE_SESSION marker, so the ape change or
ape task it dispatches runs, and marks its own session. It is the only
session ape spawns without one.

Completion: a Stop hook with no background task outstanding. A Stop while
the conductor waits on a background task (its own ape change, say) is a
yield: the run keeps waiting, the idle timer is suspended, and Claude Code
resumes the conductor when the task ends. --max-duration is a hard ceiling
from the start; --idle-timeout applies only while nothing runs in the
background. A correct HALT is a completed run.

The run record lands in {output_folder}/ape/prompts/<run-id>/: manifest.json
(host: eval-conduct; the conductor's own session id, transcript, cost,
tokens and turns; its model and effort_source; start, end and status),
prompt.yaml with the same record, and hook-events.jsonl. The inner runs
keep their own manifests. --output-format json prints ape prompt's summary.

Refused unless APE_EVAL_HOST=1, and refused inside an ape session.

Exit codes:
  0   completed (a Stop with nothing outstanding, a HALT included)
  1   the session failed
  2   usage: bad flags, no project config, unreadable request
  3   the claude REPL never became ready
  4   claude exited before completing
  5   the session's turn failed against the API
  20  idle timeout (nothing running in the background)
  21  max-duration ceiling
  22  refused: APE_EVAL_HOST is not 1
  23  refused: already inside an ape session (APE_SESSION is set)`,
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv(envEvalHost) != "1" {
				return usageErrExit(exitConductNotEvalHost, fmt.Errorf(
					"ape eval conduct is an eval-only host and is refused unless %s=1", envEvalHost))
			}
			if owner := os.Getenv(repl.EnvApeSession); owner != "" {
				return usageErrExit(exitConductNested, fmt.Errorf(
					"ape eval conduct refused: already inside the ape session %s (%s is set); "+
						"the host must never nest", owner, repl.EnvApeSession))
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman && format != output.FormatJSON && format != output.FormatYAML {
				return usageErr(fmt.Errorf("--output-format must be human, json, or yaml, got %q", outputFormat))
			}
			request, err := readConductRequest(requestFile, cmd.InOrStdin())
			if err != nil {
				return usageErr(err)
			}
			projectRoot, err := conductProjectRoot(cwdFlag)
			if err != nil {
				return usageErr(err)
			}
			return runPrompt(cmd.Context(), promptOptions{
				text:        request,
				agent:       orchestratorAgent,
				model:       modelFlag,
				idleTimeout: idleTimeoutFlag,
				maxDuration: maxDurationFlag,
				projectRoot: projectRoot,
				quiet:       quietFlag,
				format:      format,
				conduct:     true,
			})
		},
	}
	cmd.Flags().StringVar(&requestFile, "request-file", "", "The request, from a file or - for stdin (required)")
	cmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage("Claude model for the conductor."))
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().DurationVar(&idleTimeoutFlag, "idle-timeout", 0,
		"Idle backstop, applied only while nothing runs in the background (default 60m)")
	cmd.Flags().DurationVar(&maxDurationFlag, "max-duration", sessiondriver.DefaultMaxDuration,
		"Hard wall-clock ceiling from the start, never reset (0 disables)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress the progress stream on stderr")
	return cmd
}

// readConductRequest reads --request-file: a path, or - for stdin.
func readConductRequest(path string, stdin io.Reader) (string, error) {
	if path == "" {
		return "", errors.New("--request-file is required (a path, or - for stdin)")
	}
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read --request-file: %w", err)
	}
	// One line: the request is typed into the REPL, where a newline would
	// submit it early.
	request := strings.Join(strings.Fields(string(data)), " ")
	if request == "" {
		return "", errors.New("--request-file is empty")
	}
	return request, nil
}

func conductProjectRoot(cwd string) (string, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		cwd = wd
	}
	if _, err := os.Stat(filepath.Join(cwd, "_apex", "config.yaml")); err != nil {
		return "", fmt.Errorf("ape eval conduct requires a project root with _apex/config.yaml; not found in %s", cwd)
	}
	return cwd, nil
}

// conductExitCode gives the two backstops the conductor's own codes; every
// other outcome keeps `ape prompt`'s.
func conductExitCode(status string, code int) int {
	switch status {
	case promptStatusIdleTimeout:
		return exitConductIdleTimeout
	case promptStatusMaxDuration:
		return exitConductMaxDuration
	}
	return code
}
