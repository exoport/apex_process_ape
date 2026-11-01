package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
)

func newSupervisorCmd() *cobra.Command {
	var f coordFlags
	var id string
	var noVerify bool
	var heartbeat time.Duration
	var projects []string
	cmd := &cobra.Command{
		Use:   "supervisor [--id <id>] [--heartbeat 1m]",
		Short: "APEX v1.5 (in development): run a supervisor, which keeps each ensemble's coordinator running",
		Long: `Run a supervisor (stage 0; proposal §3.3). Two or more may run: one holds sup.leader
and serves ape.sup.ensure, ape.sup.health.report and ape.sup.status; the others wait to
take over. On ensure (` + "`ape supervisor ensure`" + `) it starts ` + "`ape coordinator --ensemble <name>`" + `
as a child process with this command's connection flags and each --project (default: the
member project it runs in), whose milestone leases the coordinator watches. A health report is verified before anything is
killed: the coordinator's lease must be stale AND its ping unanswered; otherwise the
reporter is told its own link looks unhealthy (health-unconfirmed). At most 3 starts per
ensemble in 15 minutes. --no-verify kills on any report (the drills' control only).
SIGINT/SIGTERM stops its coordinators and exits 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			nc, m, err := f.connect(ctx, "ape-supervisor")
			if err != nil {
				return err
			}
			defer nc.Close()
			if id == "" {
				id = defaultCoordID("sup")
			}
			if len(projects) == 0 && m != nil {
				projects = []string{m.Project}
			}
			start := func(ctx context.Context, ens string) (supervisor.Handle, error) {
				return f.coordinatorProcess(ctx, ens, projects)
			}
			return supervisor.Run(ctx, nc, supervisor.Config{ID: id, Start: start, Verify: !noVerify, HeartbeatEvery: heartbeat})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&id, "id", "", "this supervisor's id (default: sup-<host>-<pid>)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "kill a coordinator on any health report, unverified (a drill's control; never in use)")
	cmd.Flags().DurationVar(&heartbeat, "heartbeat", time.Minute, "how often each held conductor lease gets a heartbeat event (0: none)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "a member project whose milestone leases its coordinators watch (repeatable; default: the member project it runs in)")
	_ = cmd.Flags().MarkHidden("no-verify")
	cmd.AddCommand(newSupervisorEnsureCmd())
	return cmd
}

func newSupervisorEnsureCmd() *cobra.Command {
	var f coordFlags
	var ensemble string
	cmd := &cobra.Command{
		Use:   "ensure [--ensemble <name>]",
		Short: "Ask the leading supervisor to run the ensemble's coordinator, starting it if none answers",
		Long: `Send ape.sup.ensure: the leading supervisor pings the ensemble's coordinator and, when none
answers, starts one. Prints started or running. Exit 1 when no supervisor answered, or it
could not start one.`,
		Example: "  ape supervisor ensure\n  ape supervisor ensure --ensemble atomix-1a --nats-url nats://127.0.0.1:14222",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nc, m, err := f.connect(cmd.Context(), "ape-supervisor-ensure")
			if err != nil {
				return err
			}
			defer nc.Close()
			if ensemble == "" && m != nil {
				ensemble = m.Ensemble
			}
			if ensemble == "" {
				return usageErr(errors.New("--ensemble is required outside a member project"))
			}
			body, err := json.Marshal(supervisor.EnsureRequest{Ensemble: ensemble})
			if err != nil {
				return err
			}
			msg, err := nc.Request("ape.sup.ensure", body, 3*layout.HealthDeadline)
			if err != nil {
				return failErr(fmt.Errorf("no supervisor answered ape.sup.ensure: %w", err))
			}
			if desc := msg.Header.Get("Nats-Service-Error"); desc != "" {
				return failErr(fmt.Errorf("the supervisor could not run %s's coordinator: %s", ensemble, desc))
			}
			var rep map[string]string
			if err := json.Unmarshal(msg.Data, &rep); err != nil {
				return failErr(fmt.Errorf("the supervisor's answer is not an ensure reply: %w", err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", ensemble, rep["action"])
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ensemble, "ensemble", "", "the ensemble (default: the project's)")
	return cmd
}

// coordProcess is a coordinator child process; Stop kills it.
type coordProcess struct{ cmd *exec.Cmd }

func (p coordProcess) Stop() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
}

// coordinatorArgs is the coordinator child's command line.
func (f *coordFlags) coordinatorArgs(ens string, projects []string) []string {
	args := []string{"coordinator", "--ensemble", ens}
	for _, pr := range projects {
		args = append(args, "--project", pr)
	}
	for _, kv := range [][2]string{{"--cwd", f.cwd}, {"--nats-url", f.url}, {"--nats-creds", f.creds}} {
		if kv[1] != "" {
			args = append(args, kv[0], kv[1])
		}
	}
	return args
}

// coordinatorProcess starts `ape coordinator --ensemble ens` with the
// supervisor's own connection flags and a --project for each of projects.
func (f *coordFlags) coordinatorProcess(ctx context.Context, ens string, projects []string) (supervisor.Handle, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, self, f.coordinatorArgs(ens, projects)...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	return coordProcess{cmd: c}, nil
}
