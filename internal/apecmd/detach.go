package apecmd

// `--detach` and `ape run wait|status` (ape v0.4.0).
//
// Why: Claude Code 2.1.285 stops a Bash tool's background command after at
// most 2 h (30 min by default), SIGTERM then SIGKILL ~3 s later, and it
// stops setsid'd and nohup'd descendants too. The apex-orchestrator runs
// every `ape task|change` from such a shell, so a long dispatch
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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/proctree"
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

	// The nesting rule (D62), for a session an apex-orchestrator run
	// started: the session's depth, its parent run, and the pid of the ape
	// process running the parent, which the supervisor watches (the child
	// dies with it). Locks are the session lock files the run holds under
	// this handle's id; the supervisor releases them when the run ends.
	Depth     int      `json:"depth,omitempty"`
	ParentRun string   `json:"parent_run,omitempty"`
	ParentPID int      `json:"parent_pid,omitempty"`
	Locks     []string `json:"locks,omitempty"`
	// RunDir / RunID are the run's own record, written by the run once it
	// has one (an `ape task` run's manifest directory).
	RunDir string `json:"run_dir,omitempty"`
	RunID  string `json:"run_id,omitempty"`
	// StopReason is why the supervisor stopped the run, when it did:
	// "parent_gone".
	StopReason string `json:"stop_reason,omitempty"`
}

// stopParentGone is the handle's stop_reason, and the run manifest's
// termination kind, when the supervisor stopped a session because the
// orchestrator run that started it ended (D62).
const stopParentGone = "parent_gone"

// parentPollInterval is how often the supervisor checks that the run's
// parent is alive (RC3-PLAN Q4); a test seam.
var parentPollInterval = 2 * time.Second

// parentGoneGrace is how long a run the supervisor asked to stop, because
// its parent is gone, has to end before it is killed.
var parentGoneGrace = 60 * time.Second

// detachMeta is what a --detach start knows before it launches: the run's
// id (chosen first, so a lock can name it before the run exists) and its
// place in the nesting rule.
type detachMeta struct {
	id     string
	launch *sessionLaunch
}

func newDetachedID() string {
	return time.Now().UTC().Format("20060102-150405") + "-" + randomHex(3)
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

// updateHandle applies change to the handle as it is on disk now, and writes
// it back.
//
// Two processes write a handle: the --detach process records the
// supervisor's pid, and the supervisor records the child's pid and the exit.
// Each must change only its own fields on a fresh read. Writing back a copy
// read earlier undoes the other's write: the supervisor reads the handle as
// it starts, which can be before --detach has recorded its pid, and then
// saved that copy back with the child's pid, erasing supervisor_pid. With
// no supervisor_pid, `ape run stop` and the lost-supervisor check (exit 76)
// cannot find the process. CI caught it under load.
func updateHandle(path string, change func(*detachedHandle)) error {
	h, err := readHandle(path)
	if err != nil {
		return err
	}
	change(h)
	return writeHandle(path, h)
}

// recordSupervisorPID is the --detach process's one write after the start:
// the supervisor's pid, unless the supervisor has already recorded it.
func recordSupervisorPID(path string, pid int) error {
	return updateHandle(path, func(cur *detachedHandle) {
		if cur.SupervisorPID == 0 {
			cur.SupervisorPID = pid
		}
	})
}

// recordChildPID is the supervisor's write once the child has started. It
// records its own pid too, so supervisor_pid is set whichever process wrote
// last.
func recordChildPID(path string, supervisorPID, childPID int) error {
	return updateHandle(path, func(cur *detachedHandle) {
		cur.SupervisorPID, cur.ChildPID = supervisorPID, childPID
	})
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
		"Start the run in the background, detached from this shell, print its id (with --output-format json: {id, wait, log, stdout}), and return at once; `ape run wait <id> --wait 9m` waits for it")
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
//
// launch is the session the run will start (the nesting rule, D62): its
// locks are taken here, under the run's id, before the run exists, so a
// second start is refused at once rather than after a supervisor came up;
// the run takes them over, and the supervisor releases them. jsonOut prints
// {id, wait, log, stdout} instead of the four lines.
func startDetached(cmd *cobra.Command, projectRoot string, launch *sessionLaunch, jsonOut bool) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return usageErr(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return usageErr(err)
	}
	meta := detachMeta{id: newDetachedID(), launch: launch}
	if launch != nil {
		if err := launch.acquire(root, meta.id); err != nil {
			return sessionRefusal(err)
		}
	}
	h, err := launchDetached(root, wd, withoutDetach(os.Args[1:]), nil, meta)
	if err != nil {
		launch.release()
		return usageErr(err)
	}
	out := cmd.OutOrStdout()
	wait := fmt.Sprintf("ape run wait %s --cwd %s", h.ID, root)
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]string{"id": h.ID, "wait": wait, "log": h.Log, "stdout": h.Stdout})
	}
	fmt.Fprintf(out, "detached: %s\n", h.ID)
	fmt.Fprintf(out, "wait:     %s\n", wait)
	fmt.Fprintf(out, "log:      %s\n", h.Log)
	fmt.Fprintf(out, "stdout:   %s\n", h.Stdout)
	return nil
}

// launchDetached starts a supervisor running this binary with argv, in
// dir, orphaned from the caller, and returns its handle. env, when non-nil,
// is the supervisor's (and so the run's) whole environment. meta.id, when
// set, is the run's id; meta.launch its place in the nesting rule, whose
// locks the supervisor is then recorded as holding.
func launchDetached(root, dir string, argv, env []string, meta detachMeta) (*detachedHandle, error) {
	id := meta.id
	if id == "" {
		id = newDetachedID()
	}
	hdir := runlog.DetachedRoot(root)
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	h := &detachedHandle{
		ID:        id,
		Argv:      argv,
		Dir:       dir,
		Log:       filepath.Join(hdir, id+".log"),
		Stdout:    filepath.Join(hdir, id+".out"),
		StartedAt: time.Now().UTC(),
	}
	if l := meta.launch; l != nil {
		h.Depth, h.ParentRun, h.ParentPID, h.Locks = l.depth, l.parentRun, l.parentPID, l.locks
	}
	path := handlePath(root, id)
	if err := writeHandle(path, h); err != nil {
		return nil, err
	}
	sup := exec.Command(exe, "run", "supervise", "--handle", path) //nolint:noctx // this binary; the supervisor must outlive this process's context
	sup.Dir = dir
	sup.Env = env
	sup.SysProcAttr = detachedProcAttr()
	if err := sup.Start(); err != nil {
		return nil, fmt.Errorf("start the supervisor: %w", err)
	}
	h.SupervisorPID = sup.Process.Pid
	if err := recordSupervisorPID(path, h.SupervisorPID); err != nil {
		return nil, err
	}
	// The locks were taken by this process, which exits now; the
	// supervisor lives as long as the run.
	retargetSessionLocks(h.Locks, id, h.SupervisorPID)
	_ = sup.Process.Release()
	return h, nil
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
		Long: `A run started with --detach (ape task|change --detach) returns an id at
once and continues in the background, orphaned from the shell that started
it, so a tool's time limit on that shell does not reach it.

  ape run wait <id>     wait for it; exit with the run's own exit code
                        (75: --wait/--timeout expired, wait again; 76:
                        its supervisor is gone without recording an exit)
  ape run status <id>   show its handle: command, pids, stdout and log
                        files, and the exit code once it has ended
  ape run stop <id>     stop it (gracefully on Linux/macOS, hard on
                        Windows) and wait for its exit to be recorded`,
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
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "wait <id>",
		Short: "Wait for a detached run and exit with its exit code",
		Long: `Wait for a run started with --detach, print where its log is and how it
ended, and exit with the run's own exit code.

--wait (or its older name --timeout) bounds the wait. When it expires with
the run still going, the command exits 75 and the run is untouched: wait
again. That is how a caller under a tool time limit waits for a run longer
than the limit — each wait stays inside it. A loop run dispatches a
skill as ` + "`ape task <skill> --args <id> --detach`" + ` and then
` + "`ape run wait <id> --wait 9m`" + ` until it stops exiting 75 (D61).

Exit 76 means the run's supervisor is gone without recording an exit
(killed, or the machine restarted), so ape cannot report how the run ended
— it does NOT mean the run did nothing: the ape commands its session ran may
have committed state, so read ` + "`ape status`" + ` before starting it again. A
code of its own, so it is never mistaken for a run that exited 1.

The run's stdout is in its own file (the handle's "stdout"), apart from
stderr (its "log"), so a --output-format json envelope reads clean.

--json prints one object instead of the lines: id, state (running, ended or
lost), exit_code (null until it ends), log, stdout, run_id and run_dir (the
run's own record, once it has one), depth and parent_run (the nesting rule:
2 and the loop's run for a session a loop run started),
stop_reason ("parent_gone" when ape stopped it because that loop run
ended), and result: the session's last assistant message, read from its
transcript, at most 2048 bytes (result_truncated: true when cut). result is
absent when there is no transcript to read. The exit codes are the same.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwd)
			if err != nil {
				return usageErr(err)
			}
			return waitDetached(cmd.Context(), cmd, handlePath(root, args[0]), timeout, jsonOut)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().DurationVar(&timeout, "wait", 0, "Stop waiting after this long (e.g. 9m) and exit 75 if the run is still going; 0 waits until it ends")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Alias of --wait")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the run as one JSON object (id, state, exit_code, log, stdout, depth, parent_run, result, …)")
	return cmd
}

func waitDetached(ctx context.Context, cmd *cobra.Command, path string, timeout time.Duration, jsonOut bool) error {
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
			if jsonOut {
				_ = printDetachedReport(out, h, runEnded)
			} else {
				fmt.Fprintf(out, "run %s ended with exit %d (%s)\n", h.ID, *h.ExitCode, strings.Join(h.Argv, " "))
				fmt.Fprintf(out, "log: %s\n", h.Log)
				fmt.Fprintf(out, "stdout: %s\n", h.Stdout)
			}
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
			lost := fmt.Errorf(
				"run %s: its supervisor (pid %d) is gone without recording an exit — it was killed, or the machine restarted; see %s",
				h.ID, h.SupervisorPID, h.Log)
			if jsonOut {
				_ = printDetachedReport(out, h, runLost)
				fmt.Fprintf(os.Stderr, "Error: %s\n", lost)
				return reportedErr(exitCodeRunLost, lost)
			}
			return usageErrExit(exitCodeRunLost, lost)
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if jsonOut {
				_ = printDetachedReport(out, h, runRunning)
			} else {
				fmt.Fprintf(out, "run %s is still running; wait again\n", h.ID)
			}
			return reportedErr(exitCodeStillRunning, errors.New("still running"))
		}
		select {
		case <-ctx.Done():
			return reportedErr(exitCodeStillRunning, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// A detached run's state in `ape run wait|status --json`.
const (
	runRunning = "running"
	runEnded   = "ended"
	runLost    = "lost"
)

// resultCap bounds the result a --json report carries: the session's last
// message is for the caller to read, not a transcript.
const resultCap = 2048

// detachedReport is `ape run wait|status --json`'s object.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type detachedReport struct {
	ID              string   `json:"id"`
	State           string   `json:"state"`
	ExitCode        *int     `json:"exit_code"`
	Argv            []string `json:"argv"`
	Log             string   `json:"log"`
	Stdout          string   `json:"stdout"`
	RunID           string   `json:"run_id,omitempty"`
	RunDir          string   `json:"run_dir,omitempty"`
	Depth           int      `json:"depth"`
	ParentRun       string   `json:"parent_run"`
	StopReason      string   `json:"stop_reason,omitempty"`
	Result          *string  `json:"result,omitempty"`
	ResultTruncated bool     `json:"result_truncated,omitempty"`
}

func newDetachedReport(h *detachedHandle, state string) detachedReport {
	r := detachedReport{
		ID: h.ID, State: state, ExitCode: h.ExitCode, Argv: h.Argv, Log: h.Log, Stdout: h.Stdout,
		RunID: h.RunID, RunDir: h.RunDir, Depth: h.Depth, ParentRun: h.ParentRun, StopReason: h.StopReason,
	}
	if text, ok := sessionResult(h.RunDir); ok {
		text, r.ResultTruncated = capUTF8(text, resultCap)
		r.Result = &text
	}
	return r
}

func printDetachedReport(w io.Writer, h *detachedHandle, state string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(newDetachedReport(h, state))
}

// sessionResult is the last assistant message of a run's main session,
// read from the transcript ape snapshotted into its run directory. ok is
// false when there is none to read (no run directory yet, no transcript —
// a stand-in claude writes none).
func sessionResult(runDir string) (string, bool) {
	if runDir == "" {
		return "", false
	}
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		return "", false
	}
	for i := range slices.Backward(m.Stages) {
		for j := range slices.Backward(m.Stages[i].Steps) {
			for _, sess := range m.Stages[i].Steps[j].Sessions {
				if sess.ParentSessionID != "" || sess.SessionID == "" {
					continue
				}
				if text, ok := cost.LastAssistantText(filepath.Join(runDir, "transcripts", sess.SessionID+".jsonl")); ok {
					return text, true
				}
			}
		}
	}
	return "", false
}

// runMainSessions is the session ids of a run's main sessions (not its
// sub-agents'), from its manifest; nil without one.
func runMainSessions(runDir string) []string {
	if runDir == "" {
		return nil
	}
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		return nil
	}
	var out []string
	for i := range m.Stages {
		for j := range m.Stages[i].Steps {
			for _, sess := range m.Stages[i].Steps[j].Sessions {
				if sess.ParentSessionID == "" && sess.SessionID != "" {
					out = append(out, sess.SessionID)
				}
			}
		}
	}
	return out
}

// capUTF8 cuts s to at most n bytes, at a rune boundary.
func capUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func newRunStatusCmd() *cobra.Command {
	var (
		cwd     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "status <id>",
		Short: "Show a detached run's handle",
		Long: `Show a detached run's handle, with its state: running, "ended, exit N", or
"gone without recording an exit".

--json prints the object ` + "`ape run wait --json`" + ` does instead — id, state
(running, ended or lost), exit_code, log, stdout, run_id, run_dir, depth,
parent_run, stop_reason, and result (the session's last assistant message,
at most 2048 bytes; absent without a transcript) — and exits 0.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveProjectRoot(cwd)
			if err != nil {
				return usageErr(err)
			}
			h, err := readHandle(handlePath(root, args[0]))
			if err != nil {
				return usageErrExit(ExitUsage, err)
			}
			if jsonOut {
				state := runRunning
				switch {
				case h.ExitCode != nil:
					state = runEnded
				case h.SupervisorPID != 0 && !supervisorIsOurs(cmd.Context(), h):
					state = runLost
				}
				return printDetachedReport(cmd.OutOrStdout(), h, state)
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the run as `ape run wait --json` does (id, state, exit_code, …, result)")
	return cmd
}

func newRunStopCmd() *cobra.Command {
	var (
		cwd     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "stop <id>",
		Short: "Stop a detached run and wait for its exit to be recorded",
		Long: `Stop a run started with --detach, then wait (up to --timeout) for its exit to
be recorded and report it. The supervisor ends itself once the run it
supervises ends: it records the exit, then exits with that code.

On Linux and macOS the stop is graceful: the supervisor forwards SIGTERM, and
the run shuts down as on Ctrl-C — ape change saves its residue, and the
commands the skill started are stopped. On Windows it is a HARD stop: there
is no SIGTERM to forward, so the run's own process is terminated; no residue
is saved and the skill's commands are not stopped.

Only this run's own process is ever signalled. A pid is reused once its
process is gone, so ape first checks the process is still this run's (Unix:
its "ape run supervise --handle …/<id>.json" command line; Windows: an ape
process created after the handle). Anything else is refused, exit 76, and
nothing is signalled.

Exits 0 once the run has ended, whatever its own exit code (ape run wait
reports that); 75 if it has not ended within --timeout; 76 if its supervisor
is gone or the pid is no longer the run's; 0 at once if it had already ended.`,
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
//
// A session an apex-orchestrator run started dies with it (D62, RC3-PLAN
// Q4): the supervisor checks the parent's ape process every
// parentPollInterval, and when it is gone stops the run the way `ape run
// stop` does, records stop_reason parent_gone on the handle and, once the
// run has ended, termination kind parent_gone in its manifest. Pdeathsig
// cannot do this: --detach reparents the run on purpose.
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
	// The session locks the run holds under this handle's id are released
	// before its exit is recorded, so a caller that saw the exit may start
	// the next session at once.
	releaseLocks := func() {
		for _, lock := range h.Locks {
			releaseSessionLock(lock, h.ID)
		}
	}
	if err := child.Start(); err != nil {
		releaseLocks()
		return recordExit(path, h, ExitRunFailed, err.Error())
	}
	h.ChildPID = child.Process.Pid
	_ = recordChildPID(path, os.Getpid(), h.ChildPID)

	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var parentGone <-chan time.Time
	if h.ParentPID > 0 {
		tick := time.NewTicker(parentPollInterval)
		defer tick.Stop()
		parentGone = tick.C
	}
	var waitErr error
wait:
	for {
		select {
		case waitErr = <-done:
			break wait
		case <-ctx.Done():
			forwardTerm(child.Process)
			waitErr = <-done
			break wait
		case <-parentGone:
			if proctree.Alive(h.ParentPID) {
				continue
			}
			h.StopReason = stopParentGone
			_ = updateHandle(path, func(cur *detachedHandle) { cur.StopReason = stopParentGone })
			forwardTerm(child.Process)
			select {
			case waitErr = <-done:
			case <-time.After(parentGoneGrace):
				_ = child.Process.Kill()
				waitErr = <-done
			}
			break wait
		}
	}
	releaseLocks()
	if h.StopReason == stopParentGone {
		markParentGone(path, h.ParentRun, h.ParentPID)
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

// markParentGone records termination parent_gone in the stopped run's
// manifest, whatever its own shutdown recorded there (cancelled, on a
// forwarded SIGTERM; nothing, on Windows, where the stop is a kill).
func markParentGone(path, parentRun string, parentPID int) {
	h, err := readHandle(path)
	if err != nil || h.RunDir == "" {
		return
	}
	patchManifestAt(h.RunDir, func(m *pipeline.Manifest) {
		if m.Status == pipeline.StatusRunning || m.Status == "" {
			m.Status = pipeline.StatusCancelled
		}
		m.Termination = &pipeline.TerminationRecord{Kind: pipeline.TerminationParentGone, Message: fmt.Sprintf(
			"the loop run %s that started this session ended (its ape process, pid %d, is gone), and ape stopped the session", parentRun, parentPID)}
	})
}

// noteDetachedRun records, on the handle of the detached run this process
// is (APE_DETACHED_ID), the run's own record: what `ape run wait --json`
// reads the session's result from, and the supervisor marks parent_gone in.
func noteDetachedRun(projectRoot, runDir string) {
	id := os.Getenv(envDetachedID)
	if id == "" || runDir == "" {
		return
	}
	_ = updateHandle(handlePath(projectRoot, id), func(cur *detachedHandle) {
		if cur.ID == id {
			cur.RunDir, cur.RunID = runDir, runIDFromDir(runDir)
		}
	})
}

// recordExit records how the run ended. Only the supervisor calls it, so the
// handle's supervisor_pid is its own.
func recordExit(path string, h *detachedHandle, code int, msg string) error {
	now := time.Now().UTC()
	h.EndedAt, h.ExitCode, h.Error = &now, &code, msg
	if err := updateHandle(path, func(cur *detachedHandle) {
		cur.SupervisorPID = os.Getpid()
		if h.ChildPID != 0 {
			cur.ChildPID = h.ChildPID
		}
		cur.EndedAt, cur.ExitCode, cur.Error = h.EndedAt, h.ExitCode, h.Error
	}); err != nil {
		return usageErr(err)
	}
	if code != 0 {
		return reportedErr(code, errors.New(msg))
	}
	return nil
}
