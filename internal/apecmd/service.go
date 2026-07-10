package apecmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/service"
	"github.com/spf13/cobra"
)

// `ape service` uses the shared exit-code table in exitcodes.go with a
// daemon reading:
//
//	0  clean shutdown — drained after SIGINT/SIGTERM
//	1  runtime failure — NATS connect or micro-service registration failed
//	2  usage/config error — bad --name, missing/invalid service.yaml, or no
//	   NATS URL configured (detected before the service registers)
//
// All diagnostics (startup banner, drain progress, NATS warnings) go to
// stderr; stdout is left clean.

func newServiceCmd() *cobra.Command {
	var (
		nameFlag         string
		configFlag       string
		cwdFlag          string
		natsURLFlag      string
		natsCredsFlag    string
		eventsPrefixFlag string
		drainTimeoutFlag time.Duration
	)
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply",
		Long: `Turn this machine into a remotely drivable ape worker (PLAN-14). The
daemon registers a NATS micro service on

  ape.svc.<name>.<project-slug>.<endpoint>

and accepts JSON request/reply jobs: pipeline.run and task.run dispatch an
ape child process (headless, PTY-only); job.status / job.list / job.stop
manage them; status / health report the daemon. NATS-micro $SRV.PING /
$SRV.INFO / $SRV.STATS discovery is available for free. command.run and
script.run are registered but rejected (VALIDATION) until their runners
ship.

Admission is keyed exclusivity, exclusive by default: a job holds its
exclusivity_key (default "") exclusively unless nonexclusive:true. Conflicts
are rejected immediately (BUSY_EXCLUSIVE / BUSY_KEY) — never queued. Requests
naming a project_root outside the allowlist are rejected (PROJECT_NOT_ALLOWED).

The daemon serves the project plus its declared component repositories, read
from _apex/service.yaml (or ~/.ape/service.yaml, or --config):

  project_root: /abs/path/main-project
  allow:
    - /abs/path/main-project
    - /abs/path/component-repo

SECURITY: anyone who can publish on the service subjects can run pipelines on
this machine. Scope the NATS credential's publish/subscribe permissions to
ape.svc.<name>.<project-slug>.> on the server — that is the real trust
boundary (see docs/how-to/run-ape-as-a-service.md).

Shutdown is graceful: SIGINT/SIGTERM stops accepting new jobs and waits for
in-flight children (indefinitely by default; bound it with --drain-timeout).
A second signal terminates them immediately.

Exit codes: 0 clean shutdown · 1 connect/registration failure · 2 usage or
config error (bad --name, missing/invalid service.yaml, no NATS URL).`,
		Example: `  ape service --nats-url nats://127.0.0.1:4222 --nats-creds ./ape.creds
  ape service --name ci --drain-timeout 5m
  # discovery + a task submission from another host:
  nats req '$SRV.PING.ape' ''
  nats req ape.svc.ape.myproject.task.run '{"project_root":"/abs/path/myproject","skill":"apex-shard-doc"}'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %s\n", err)
					os.Exit(ExitUsage)
				}
				projectRoot = wd
			}
			err := service.Run(cmd.Context(), service.Options{
				Name:         nameFlag,
				ConfigPath:   configFlag,
				ProjectRoot:  projectRoot,
				NatsURL:      natsURLFlag,
				NatsCreds:    natsCredsFlag,
				EventsPrefix: eventsPrefixFlag,
				DrainTimeout: drainTimeoutFlag,
				ApeVersion:   Version,
				Stderr:       os.Stderr,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err)
				os.Exit(serviceExitCode(err))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&nameFlag, "name", "ape", "Service name — the <name> subject segment and $SRV discovery name (run several daemons on one cluster with distinct names).")
	f.StringVar(&configFlag, "config", "", "Path to service.yaml (default: <cwd>/_apex/service.yaml, then ~/.ape/service.yaml).")
	f.StringVar(&cwdFlag, "cwd", "", "Project root for config resolution (default: current working dir).")
	f.StringVar(&natsURLFlag, "nats-url", "", "NATS server URL (env APE_NATS_URL). Required.")
	f.StringVar(&natsCredsFlag, "nats-creds", "", "NATS .creds file; its user identity is the <user> token on job lifecycle events (env APE_NATS_CREDS).")
	f.StringVar(&eventsPrefixFlag, "events-subject-prefix", "ape.evt", "Subject root for daemon job lifecycle events.")
	f.DurationVar(&drainTimeoutFlag, "drain-timeout", 0, "On shutdown, wait this long for in-flight jobs before terminating them (0 = wait indefinitely; a second signal forces).")
	return cmd
}

// serviceExitCode maps a service.Run error onto the exit-code table: a
// config/usage error → 2, a clean shutdown → 0, anything else → 1.
func serviceExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, service.ErrNoURL), errors.Is(err, service.ErrConfig):
		return ExitUsage
	default:
		return ExitRunFailed
	}
}
