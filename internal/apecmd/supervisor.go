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
	var projects, assessFrom []string
	var assess bool
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
				return f.coordinatorProcess(ctx, ens, projects, assessArgs(assess, assessFrom)...)
			}
			logf := func(format string, args ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
			}
			return supervisor.Run(ctx, nc, supervisor.Config{ID: id, Start: start, Verify: !noVerify, HeartbeatEvery: heartbeat, Logf: logf})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&id, "id", "", "this supervisor's id (default: sup-<host>-<pid>)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "kill a coordinator on any health report, unverified (a drill's control; never in use)")
	cmd.Flags().DurationVar(&heartbeat, "heartbeat", time.Minute, "how often each held conductor lease gets a heartbeat event (0: none)")
	cmd.Flags().StringSliceVar(&projects, "project", nil, "a member project whose milestone leases its coordinators watch (repeatable; default: the member project it runs in)")
	cmd.Flags().BoolVar(&assess, "assess", false, "start each coordinator with --assess (the assessor, v1.5 §3.2)")
	cmd.Flags().StringArrayVar(&assessFrom, "assess-checkout", nil, "passed to each coordinator: <project>=<path>, the checkout the assessor reads a member from (repeatable)")
	_ = cmd.Flags().MarkHidden("no-verify")
	cmd.AddCommand(newSupervisorEnsureCmd(), newSupervisorStatusCmd())
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

// Stop kills the coordinator: one judged dead is replaced at once.
func (p coordProcess) Stop() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
}

// coordTermGrace is how long a coordinator asked to stop has to release
// its lease and exit before it is killed. A var so tests can wait less.
var coordTermGrace = 5 * time.Second

// Terminate asks the coordinator to stop (SIGTERM; on Windows a kill),
// so it releases coord.<ens> on its clean exit, and kills it after
// coordTermGrace (D736): a successor supervisor's coordinator then claims
// the lease at once instead of one TTL later.
func (p coordProcess) Terminate() {
	if p.cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	forwardTerm(p.cmd.Process)
	select {
	case <-done:
	case <-time.After(coordTermGrace):
		_ = p.cmd.Process.Kill()
		<-done
	}
}

// assessArgs are the coordinator child's assessor flags.
func assessArgs(assess bool, from []string) []string {
	if !assess {
		return nil
	}
	out := []string{"--assess"}
	for _, a := range from {
		out = append(out, "--assess-checkout", a)
	}
	return out
}

// coordinatorArgs is the coordinator child's command line.
func (f *coordFlags) coordinatorArgs(ens string, projects []string, extra ...string) []string {
	args := []string{"coordinator", "--ensemble", ens}
	for _, pr := range projects {
		args = append(args, "--project", pr)
	}
	args = append(args, extra...)
	for _, kv := range [][2]string{{"--cwd", f.cwd}, {"--nats-url", f.url}, {"--nats-creds", f.creds}} {
		if kv[1] != "" {
			args = append(args, kv[0], kv[1])
		}
	}
	return args
}

// coordinatorProcess starts `ape coordinator --ensemble ens` with the
// supervisor's own connection flags and a --project for each of projects.
func (f *coordFlags) coordinatorProcess(ctx context.Context, ens string, projects []string, extra ...string) (supervisor.Handle, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, self, f.coordinatorArgs(ens, projects, extra...)...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Start(); err != nil {
		return nil, err
	}
	return coordProcess{cmd: c}, nil
}

func newSupervisorStatusCmd() *cobra.Command {
	var f coordFlags
	var ensemble string
	cmd := &cobra.Command{
		Use:   "status [--ensemble <name>]",
		Short: "Which supervisor leads, and whether the ensemble's coordinator answers (read-only: starts nothing)",
		Long: `Ask ape.sup.status which supervisor leads, and ape.coord.<ensemble>.status whether the
ensemble's coordinator answers, each within the health deadline. Unlike ensure, it never starts a
coordinator: a drill waits on it to see the supervisor bring one back by itself. Exit 0 when a
supervisor leads and the coordinator answers, 1 otherwise.`,
		Example: "  ape supervisor status\n  ape supervisor status --ensemble atomix-1a --nats-url nats://127.0.0.1:14222",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nc, m, err := f.connect(cmd.Context(), "ape-supervisor-status")
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
			w := cmd.OutOrStdout()
			ok := true
			var sup map[string]string
			if msg, err := nc.Request("ape.sup.status", nil, layout.HealthDeadline); err == nil && json.Unmarshal(msg.Data, &sup) == nil && sup["id"] != "" {
				fmt.Fprintf(w, "supervisor: %s leads\n", sup["id"])
			} else {
				ok = false
				fmt.Fprintln(w, "supervisor: none leads (no answer on ape.sup.status)")
			}
			var st struct {
				ID string `json:"id"`
			}
			if msg, err := nc.Request("ape.coord."+ensemble+".status", nil, layout.HealthDeadline); err == nil && json.Unmarshal(msg.Data, &st) == nil && st.ID != "" {
				fmt.Fprintf(w, "%s: coordinator %s answers\n", ensemble, st.ID)
			} else {
				ok = false
				fmt.Fprintf(w, "%s: no coordinator answers\n", ensemble)
			}
			if !ok {
				return failErr(errors.New("the supervisor or the coordinator did not answer"))
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ensemble, "ensemble", "", "the ensemble (default: the project's)")
	return cmd
}
