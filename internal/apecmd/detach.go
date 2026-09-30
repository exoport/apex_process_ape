package apecmd

// `--detach` and `ape run wait|status` (ape v0.4.0).
//
// Why: Claude Code 2.1.285 stops a Bash tool's background command after at
// most 2 h (30 min by default), SIGTERM then SIGKILL ~3 s later, and it
// stops setsid'd and nohup'd descendants too. The apex-orchestrator runs
// every `ape pipeline|task|change` from such a shell, so a long dispatch
// was killed mid-run. The framework measured that a process ORPHANED at
// launch survives, and worked around it in shell. This gives it a
// supported form: `--detach` starts a supervisor in its own session and
// returns at once, so the supervisor is orphaned; `ape run wait <id>`
// waits for it, re-armable under the tool limit.
//
// The supervisor, not the command, records the exit: several commands end
// through os.Exit, which no in-process hook could observe.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/spf13/cobra"
)

const (
	// exitCodeStillRunning: `ape run wait --timeout` expired with the run
	// still going. Wait again. Distinct from every code a run itself ends
	// with, so a caller's loop can tell "not yet" from any outcome.
	exitCodeStillRunning = 75

	// exitCodeRunLost: the supervisor is gone without recording an exit
	// (killed, or the machine restarted). Its own code, so a caller never
	// has to tell it from a run that really exited 1.
	exitCodeRunLost = 76

	// envDetachedID marks the process a supervisor started, for the log
	// and for anything that wants to know it runs detached.
	envDetachedID = "APE_DETACHED_ID"
)

// detachedHandle is the supervisor's record of one detached run, at
// {output_folder}/ape/detached/<id>.json.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type detachedHandle struct {
	ID            string     `json:"id"`
	Argv          []string   `json:"argv"`
	Dir           string     `json:"dir"`
	Log           string     `json:"log"`    // the run's stderr
	Stdout        string     `json:"stdout"` // the run's stdout, alone: a --output-format json envelope, clean
	SupervisorPID int        `json:"supervisor_pid"`
	ChildPID      int        `json:"child_pid,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	Error         string     `json:"error,omitempty"`
}

func handlePath(projectRoot, id string) string {
	return filepath.Join(runlog.DetachedRoot(projectRoot), id+".json")
}

func readHandle(path string) (*detachedHandle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var h detachedHandle
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &h, nil
}

func writeHandle(path string, h *detachedHandle) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'))
}

func addDetachFlag(cmd *cobra.Command, detach *bool) {
	cmd.Flags().BoolVar(detach, "detach", false,
		"Start the run in the background, detached from this shell, print its id, and return at once; `ape run wait <id>` waits for it")
}

// withoutDetach is this process's arguments minus --detach: what the
// supervisor runs.
func withoutDetach(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--detach" || strings.HasPrefix(a, "--detach=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// startDetached launches the supervisor for this command and returns. The
// supervisor is started in its own session and this process exits right
// after, so the supervisor is an orphan: nothing that stops this shell's
// descendants reaches it.
func startDetached(cmd *cobra.Command, projectRoot string) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return usageErr(err)
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + randomHex(3)
	dir := runlog.DetachedRoot(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return usageErr(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return usageErr(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return usageErr(err)
	}
	h := &detachedHandle{
		ID:        id,
		Argv:      withoutDetach(os.Args[1:]),
		Dir:       wd,
		Log:       filepath.Join(dir, id+".log"),
		Stdout:    filepath.Join(dir, id+".out"),
		StartedAt: time.Now().UTC(),
	}
	path := handlePath(root, id)
	if err := writeHandle(path, h); err != nil {
		return usageErr(err)
	}
	sup := exec.Command(exe, "run", "supervise", "--handle", path) //nolint:noctx // this binary; the supervisor must outlive this process's context
	sup.Dir = wd
	sup.SysProcAttr = detachedProcAttr()
	if err := sup.Start(); err != nil {
		return usageErr(fmt.Errorf("start the supervisor: %w", err))
	}
	h.SupervisorPID = sup.Process.Pid
	if err := writeHandle(path, h); err != nil {
		return usageErr(err)
	}
	_ = sup.Process.Release()

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "detached: %s\n", id)
	fmt.Fprintf(out, "wait:     ape run wait %s --cwd %s\n", id, root)
	fmt.Fprintf(out, "log:      %s\n", h.Log)
	fmt.Fprintf(out, "stdout:   %s\n", h.Stdout)
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Wait for, or inspect, a run started with --detach",
		Long: `A run started with --detach (ape pipeline|task|change --detach) returns an id at
once and continues in the background, orphaned from the shell that started
it, so a tool's time limit on that shell does not reach it.

  ape run wait <id>     wait for it; exit with the run's own exit code
                        (75: --timeout expired, wait again; 76: its
                        supervisor is gone without recording an exit)
  ape run status <id>   show its handle: command, pids, stdout and log
                        files, and the exit code once it has ended
  ape run stop <id>     stop it as Ctrl-C would, and wait for its exit
                        to be recorded`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newRunWaitCmd(), newRunStatusCmd(), newRunStopCmd(), newRunSuperviseCmd())
	return cmd
}

func newRunWaitCmd() *cobra.Command {
	var (
		cwd     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <id>",
		Short: "Wait for a detached run and exit with its exit code",
		Long: `Wait for a run started with --detach, print where its log is and how it
ended, and exit with the run's own exit code.

--timeout bounds the wait. When it expires with the run still going, the
command exits 75 and the run is untouched: wait again. That is how a caller
under a tool time limit waits for a run longer than the limit — each wait
stays inside it.

A run whose supervisor is gone without recording an exit (killed, or the
machine rebooted) is reported as such, exit 76, rather than waited on
forever — its own code, so it is never mistaken for a run that exited 1.

The run's stdout is in its own file (the handle's "stdout"), apart from
stderr (its "log"), so a --output-format json envelope reads clean.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwd)
			if err != nil {
				return usageErr(err)
			}
			return waitDetached(cmd.Context(), cmd, handlePath(root, args[0]), timeout)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Stop waiting after this long (e.g. 25m) and exit 75 if the run is still going; 0 waits until it ends")
	return cmd
}

func waitDetached(ctx context.Context, cmd *cobra.Command, path string, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	out := cmd.OutOrStdout()
	for {
		h, err := readHandle(path)
		if err != nil {
			return usageErrExit(ExitUsage, fmt.Errorf("no detached run at %s: %w", path, err))
		}
		if h.ExitCode != nil {
			fmt.Fprintf(out, "run %s ended with exit %d (%s)\n", h.ID, *h.ExitCode, strings.Join(h.Argv, " "))
			fmt.Fprintf(out, "log: %s\n", h.Log)
			fmt.Fprintf(out, "stdout: %s\n", h.Stdout)
			if *h.ExitCode == 0 {
				return nil
			}
			return reportedErr(*h.ExitCode, fmt.Errorf("the detached run exited %d", *h.ExitCode))
		}
		if h.SupervisorPID != 0 && !supervisorIsOurs(ctx, h) {
			// Re-read once: the supervisor may have recorded the exit
			// between the two reads and then ended.
			if h2, err := readHandle(path); err == nil && h2.ExitCode != nil {
				continue
			}
			return usageErrExit(exitCodeRunLost, fmt.Errorf(
				"run %s: its supervisor (pid %d) is gone without recording an exit — it was killed, or the machine restarted; see %s",
				h.ID, h.SupervisorPID, h.Log))
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			fmt.Fprintf(out, "run %s is still running; wait again\n", h.ID)
			return reportedErr(exitCodeStillRunning, errors.New("still running"))
		}
		select {
		case <-ctx.Done():
			return reportedErr(exitCodeStillRunning, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func newRunStatusCmd() *cobra.Command {
	var cwd string
	cmd := &cobra.Command{
		Use:   "status <id>",
		Short: "Show a detached run's handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwd)
			if err != nil {
				return usageErr(err)
			}
			h, err := readHandle(handlePath(root, args[0]))
			if err != nil {
				return usageErrExit(ExitUsage, err)
			}
			state := "running"
			switch {
			case h.ExitCode != nil:
				state = fmt.Sprintf("ended, exit %d", *h.ExitCode)
			case h.SupervisorPID != 0 && !supervisorIsOurs(cmd.Context(), h):
				state = "gone without recording an exit"
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				State string `json:"state"`
				*detachedHandle
			}{state, h})
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project root directory (default: current working dir)")
	return cmd
}

func newRunStopCmd() *cobra.Command {
	var (
		cwd     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "stop <id>",
		Short: "Stop a detached run as Ctrl-C would, and wait for its exit to be recorded",
		Long: `Stop a run started with --detach. The supervisor passes the stop on to the
run, which shuts down the way it does on Ctrl-C: ape change saves its
residue, and the commands the skill started are stopped. Then this waits
(up to --timeout) for the exit to be recorded and reports it.

Exits 0 once the run has ended, whatever its own exit code (ape run wait
reports that); 75 if it has not ended within --timeout; 0 at once if it had
already ended.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwd)
			if err != nil {
				return usageErr(err)
			}
			path := handlePath(root, args[0])
			h, err := readHandle(path)
			if err != nil {
				return usageErrExit(ExitUsage, fmt.Errorf("no detached run at %s: %w", path, err))
			}
			out := cmd.OutOrStdout()
			if h.ExitCode != nil {
				fmt.Fprintf(out, "run %s had already ended with exit %d\n", h.ID, *h.ExitCode)
				return nil
			}
			// Identity, not just liveness: a pid is reused once its process is
			// gone, and stop must never signal a process that is not this run's.
			if err := stopDetached(cmd.Context(), h); err != nil {
				return usageErrExit(exitCodeRunLost, err)
			}
			deadline := time.Now().Add(timeout)
			for time.Now().Before(deadline) {
				if h2, err := readHandle(path); err == nil && h2.ExitCode != nil {
					fmt.Fprintf(out, "run %s stopped; it ended with exit %d\n", h2.ID, *h2.ExitCode)
					return nil
				}
				time.Sleep(200 * time.Millisecond)
			}
			fmt.Fprintf(out, "run %s was asked to stop and has not ended yet; ape run wait %s\n", h.ID, h.ID)
			return reportedErr(exitCodeStillRunning, errors.New("still running"))
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "How long to wait for the run to end after asking it to stop")
	return cmd
}

// newRunSuperviseCmd is the supervisor a --detach run starts. Hidden: it
// is ape's own plumbing, not a command anyone types.
func newRunSuperviseCmd() *cobra.Command {
	var handle string
	cmd := &cobra.Command{
		Use:    "supervise",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return supervise(cmd.Context(), handle)
		},
	}
	cmd.Flags().StringVar(&handle, "handle", "", "the detached run's handle file")
	return cmd
}

// supervise runs the handle's command to its end and records how it ended.
// A signal to the supervisor is passed on to the command, whose own
// shutdown then decides the exit code that gets recorded.
func supervise(ctx context.Context, path string) error {
	h, err := readHandle(path)
	if err != nil {
		return usageErr(err)
	}
	logf, err := os.OpenFile(h.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return usageErr(err)
	}
	defer func() { _ = logf.Close() }()
	stdout := logf
	if h.Stdout != "" {
		outf, err := os.OpenFile(h.Stdout, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return usageErr(err)
		}
		defer func() { _ = outf.Close() }()
		stdout = outf
	}
	exe, err := os.Executable()
	if err != nil {
		return usageErr(err)
	}
	child := exec.Command(exe, h.Argv...) //nolint:noctx // this binary, with the arguments --detach recorded; a signal is forwarded below, not a context kill
	child.Dir = h.Dir
	child.Stdout, child.Stderr = stdout, logf
	child.Env = append(os.Environ(), envDetachedID+"="+h.ID)
	if err := child.Start(); err != nil {
		return recordExit(path, h, ExitRunFailed, err.Error())
	}
	h.ChildPID = child.Process.Pid
	_ = writeHandle(path, h)

	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		forwardTerm(child.Process)
		waitErr = <-done
	}
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](waitErr); ok {
		code = ee.ExitCode()
		if code < 0 {
			code = ExitRunFailed // killed by a signal: no status of its own
		}
	} else if waitErr != nil {
		code = ExitRunFailed
	}
	msg := ""
	if waitErr != nil {
		msg = waitErr.Error()
	}
	return recordExit(path, h, code, msg)
}

func recordExit(path string, h *detachedHandle, code int, msg string) error {
	now := time.Now().UTC()
	h.EndedAt, h.ExitCode, h.Error = &now, &code, msg
	if err := writeHandle(path, h); err != nil {
		return usageErr(err)
	}
	if code != 0 {
		return reportedErr(code, errors.New(msg))
	}
	return nil
}
