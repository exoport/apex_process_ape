package apecmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/service"
	"github.com/nats-io/nats.go/micro"
	"github.com/spf13/cobra"
)

// The `ape service` side of the nesting rule (D83; service/depth.go has the
// daemon's half). The client stamps every job with where it runs
// (callerStamp, from the same record `ape task` reads), and the daemon
// checks a depth-1 caller with serviceGate — gateSession's rule over the
// stamp instead of this process's own session — before it spawns the job.

// callerStamp is this process's place in the nesting rule, for a job it
// submits about projectRoot.
func callerStamp(projectRoot string) *service.CallerStamp {
	at := currentSession(projectRoot)
	c := &service.CallerStamp{Depth: at.Depth, PID: os.Getpid()}
	if at.Depth > 0 {
		c.RunID = at.Run
		c.ParentRun = os.Getenv(repl.EnvParentRun)
	}
	return c
}

// serviceGate is the daemon's check of a depth-1 caller's job (the daemon
// refuses every other non-zero depth itself): the caller's run must be a
// running apex-orchestrator run in the job's project — its manifest, ape's
// own record — and the job a task of a skill on that project's allow-list,
// with the orchestrator's one child slot free. The same functions decide it
// as for a direct `ape task` (apexv1.SessionStart, the session locks). The
// job's ape then runs under the caller's session marker, so it applies the
// rule again itself as that orchestrator's child: it takes the child lock,
// records the orchestrator as its parent run (the parent lists it among its
// children) and is stopped when the orchestrator's ape process is gone.
func serviceGate(kind service.Kind, req service.RunRequest) ([]string, error) {
	c, root := req.Caller, req.ProjectRoot
	verb := "ape service " + string(kind) + ".run"
	at := apexv1.SessionAt{Depth: 1, Run: c.RunID, Kind: "task", Skill: runningTaskSkill(root, c.RunID)}
	if !at.Orchestrator() {
		return nil, &apexv1.UsageError{Code: apexv1.CodeSessionDepth, Action: apexv1.ActionStop, Msg: fmt.Sprintf(
			"%s refused: the caller runs inside an ape session (depth 1, run %s) that is not a running apex-orchestrator run in %s,"+
				" and only an orchestrator starts model sessions from inside ape — run it from a terminal, or let the orchestrator start it", verb, c.RunID, root)}
	}
	skill := ""
	if kind == service.KindTask {
		skill = req.Skill
	}
	if _, err := apexv1.SessionStart(at, verb, skill, childAllowList(root)); err != nil {
		return nil, err
	}
	// The one child at a time, looked at now so a busy slot is refused
	// before the job is accepted; the job's ape takes the lock itself.
	probe := &sessionLaunch{depth: 2, parentRun: c.RunID, skill: skill}
	if err := probe.acquire(root, "svc-probe-"+randomHex(4)); err != nil {
		return nil, err
	}
	probe.release()
	return []string{
		repl.EnvApeSession + "=task/" + c.RunID, repl.EnvDepth + "=1",
		repl.EnvParentRun + "=", repl.EnvParentPID + "=", envDetachedID + "=",
	}, nil
}

// submitKinds are the job kinds `ape service submit` sends (pipeline.run is
// retired).
var submitKinds = []service.Kind{service.KindTask, service.KindPrompt, service.KindScript}

func newServiceSubmitCmd() *cobra.Command {
	var (
		nameFlag      string
		serviceRoot   string
		natsURLFlag   string
		natsCredsFlag string
		timeoutFlag   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "submit <task|prompt|script> <request-json>",
		Short: "Submit a job to an ape service daemon, stamped with the caller's session depth",
		Long: `Send one task.run, prompt.run or script.run request to an ` + "`ape service`" + `
daemon and print its reply (the accepted job's id) on stdout.

The request is the JSON body the endpoint takes (see ape service --help and
docs/reference/events.md); project_root defaults to the current directory.
ape adds the caller stamp every *.run request needs (D83):

  "caller": {"depth": <n>, "run_id": "<run>", "parent_run": "<run>", "pid": <pid>}

computed as ` + "`ape task`" + ` computes its own nesting depth — the session marker
(APE_SESSION, APE_DEPTH) and the process ancestry — so a session cannot
widen it by scrubbing its environment. Any caller in the given body is
replaced. The daemon refuses a request without a stamp, and one from depth
2, with session.depth (action stop). From depth 1 it accepts only an
apex-orchestrator run's job, a task of a skill on that project's allow-list,
one child at a time (session.not-allowed, session.child-running), and runs
it as that orchestrator's child (depth 2, parent_run, listed in its
children, stopped with it). From depth 0 it runs the job as before.

The stamp is the client's word: a raw NATS publish can claim depth 0. The
NATS credentials and subject permissions remain the boundary.

The subject is ape.svc.<name>.<slug>.<kind>.run, the slug derived from the
daemon's primary project: --service-root, by default the request's
project_root.

Exit codes: 0 accepted (the reply on stdout) · 1 no daemon answered, or NATS
failed · 2 usage, a malformed request, or a refusal (session.depth,
session.not-allowed, VALIDATION, PROJECT_NOT_ALLOWED) · 3 busy
(session.child-running, session.orchestrator-running, BUSY_EXCLUSIVE,
BUSY_KEY): retry later. A refusal prints the daemon's message with its code.`,
		Example: `  ape service submit task '{"skill":"apex-plan","args":"M2"}'
  ape service submit task '{"project_root":"/abs/repo","skill":"apex-orchestrator","args":"M1"}' --name ci`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := service.Kind(args[0])
			if !slices.Contains(submitKinds, kind) {
				return usageErr(fmt.Errorf("unknown job kind %q — task, prompt or script", args[0]))
			}
			var rr service.RunRequest
			dec := json.NewDecoder(strings.NewReader(args[1]))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&rr); err != nil {
				return usageErr(fmt.Errorf("the request is not a %s.run body: %w", kind, err))
			}
			if rr.ProjectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return usageErr(fmt.Errorf("cannot determine working directory: %w", err))
				}
				rr.ProjectRoot = wd
			}
			if abs, err := filepath.Abs(rr.ProjectRoot); err == nil {
				rr.ProjectRoot = abs
			}
			rr.V = service.WireVersion
			rr.Caller = callerStamp(rr.ProjectRoot)
			if serviceRoot == "" {
				serviceRoot = rr.ProjectRoot
			}
			subject := service.RunSubject(nameFlag, serviceRoot, kind)
			data, err := json.Marshal(rr)
			if err != nil {
				return failErr(err)
			}
			ncfg := natsconn.Resolve(natsURLFlag, natsCredsFlag)
			if !ncfg.Enabled() {
				return usageErr(service.ErrNoURL)
			}
			conn, err := natsconn.Connect(cmd.Context(), ncfg, "ape-service-submit/"+Version)
			if err != nil {
				return failErr(err)
			}
			defer conn.Close()
			msg, err := conn.Request(subject, data, timeoutFlag)
			if err != nil {
				return failErr(fmt.Errorf("no ape service answered on %s: %w", subject, err))
			}
			if code := msg.Header.Get(micro.ErrorCodeHeader); code != "" {
				return usageErrExit(submitExitCode(code), fmt.Errorf("%s (%s)", msg.Header.Get(micro.ErrorHeader), code))
			}
			out := cmd.OutOrStdout()
			_, _ = out.Write(bytes.TrimRight(msg.Data, "\n"))
			_, _ = fmt.Fprintln(out)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&nameFlag, "name", "ape", "The daemon's --name.")
	f.StringVar(&serviceRoot, "service-root", "", "The daemon's primary project root, for the subject's slug (default: the request's project_root).")
	f.StringVar(&natsURLFlag, "nats-url", "", "NATS server URL (env APE_NATS_URL). Required.")
	f.StringVar(&natsCredsFlag, "nats-creds", "", "NATS .creds file (env APE_NATS_CREDS).")
	f.DurationVar(&timeoutFlag, "timeout", 10*time.Second, "How long to wait for the daemon's reply.")
	return cmd
}

// submitExitCode maps a daemon refusal's code onto submit's exit codes: 3
// for a busy slot or lock (retry), 2 for everything else it refuses.
func submitExitCode(code string) int {
	switch code {
	case apexv1.CodeChildRunning, apexv1.CodeOrchestratorRunning, service.CodeBusyExclusive, service.CodeBusyKey:
		return exitV1Conflict
	}
	return ExitUsage
}

// serviceGate is the daemon's SessionGate.
var _ service.SessionGate = serviceGate
