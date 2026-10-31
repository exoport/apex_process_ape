package apecmd

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
)

func newSupervisorCmd() *cobra.Command {
	var f coordFlags
	var id string
	var noVerify bool
	var heartbeat time.Duration
	cmd := &cobra.Command{
		Use:   "supervisor [--id <id>] [--heartbeat 1m]",
		Short: "APEX v1.5 (in development): run a supervisor, which keeps each ensemble's coordinator running",
		Long: `Run a supervisor (stage 0; proposal §3.3). Two or more may run: one holds sup.leader
and serves ape.sup.ensure, ape.sup.health.report and ape.sup.status; the others wait to
take over. On ensure it starts ` + "`ape coordinator --ensemble <name>`" + ` as a child process
with this command's connection flags. A health report is verified before anything is
killed: the coordinator's lease must be stale AND its ping unanswered; otherwise the
reporter is told its own link looks unhealthy (health-unconfirmed). At most 3 starts per
ensemble in 15 minutes. --no-verify kills on any report (the drills' control only).
SIGINT/SIGTERM stops its coordinators and exits 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			nc, _, err := f.connect(ctx, "ape-supervisor")
			if err != nil {
				return err
			}
			defer nc.Close()
			if id == "" {
				id = defaultCoordID("sup")
			}
			return supervisor.Run(ctx, nc, supervisor.Config{ID: id, Start: f.coordinatorProcess, Verify: !noVerify, HeartbeatEvery: heartbeat})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&id, "id", "", "this supervisor's id (default: sup-<host>-<pid>)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "kill a coordinator on any health report, unverified (a drill's control; never in use)")
	cmd.Flags().DurationVar(&heartbeat, "heartbeat", time.Minute, "how often each held conductor lease gets a heartbeat event (0: none)")
	_ = cmd.Flags().MarkHidden("no-verify")
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

// coordinatorProcess starts `ape coordinator --ensemble ens` with the
// supervisor's own connection flags.
func (f *coordFlags) coordinatorProcess(ctx context.Context, ens string) (supervisor.Handle, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"coordinator", "--ensemble", ens}
	for flag, v := range map[string]string{"--cwd": f.cwd, "--nats-url": f.url, "--nats-creds": f.creds} {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	c := exec.CommandContext(ctx, self, args...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	return coordProcess{cmd: c}, nil
}
