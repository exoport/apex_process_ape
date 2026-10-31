package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/conductorterm"
	"github.com/exoport/apex_process_ape/internal/conductorwatch"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessionref"
)

// conductorFlags are v1Flags plus --project, the conductor's name for
// --cwd (D661: a remote project is `ssh host 'ape conductor … --project
// /path'`), and --orders, whose standing orders.
type conductorFlags struct {
	v1Flags
	orders string
}

func (f *conductorFlags) add(cmd *cobra.Command) {
	addV1Flags(cmd, &f.v1Flags)
	cmd.Flags().StringVar(&f.cwd, "project", "", "the project's directory (the same as --cwd); on another host: ssh host 'ape conductor … --project /path'")
	cmd.Flags().StringVar(&f.orders, "orders", "", "whose standing orders (default: git config user.email)")
}

func newConductorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conductor",
		Short: "A person's delegated conductor: act under standing orders, keep its decision log",
		Long: `The delegated conductor (v1.4.2): a person's own Claude Code session that drives the
projects' orchestrators and, under the person's standing orders (` + "`ape orders --help`" + `),
performs some of the person's acts for them.

  act      perform a person act under the orders, with its reason and evidence
  note     add a hold for the person, an open question, or resolve one
  log      read the decision log (what was done, what is open)
  start    the project's orchestrator terminal (Claude Code with /apex-orchestrator)
  send     queue a message for the orchestrator (typed, marked, when it is idle)
  read     the orchestrator's replies
  status   the terminal (alive, idle, pending) and the open holds and questions
  stop     the terminal
  watch    show the terminal's screen, read-only
  watcher  follow the projects' events and wake the conductor (its tmux pane, else notify)
  ack      acknowledge a delivery the watcher typed

Every delegated act runs the notifier (event "delegated"), and is recorded with
by: "conductor under orders <person>@<commit> (<reason>)", in the act's own record and in
the person's decision log, _apex/state/conductor-<person>.md.

A remote project is driven over ssh: every verb takes --project <dir>, so
` + "`ssh host 'ape conductor act … --project /path'`" + ` runs it there (ape ≥ 1.4.2 on that host).`,
	}
	cmd.AddCommand(newConductorActCmd(), newConductorNoteCmd(), newConductorLogCmd(),
		newConductorStartCmd(), newConductorServeCmd(), newConductorSendCmd(), newConductorReadCmd(),
		newConductorStatusCmd(), newConductorStopCmd(), newConductorWatchCmd(),
		newConductorWatcherCmd(), newConductorAckCmd())
	return cmd
}

func newConductorActCmd() *cobra.Command {
	var f conductorFlags
	var reason, text string
	var evidence []string
	cmd := &cobra.Command{
		Use:   "act <act> <target> [<task>] --reason \"…\" [--evidence <path>]… [--text \"…\"]",
		Short: "Perform a person act under the person's standing orders",
		Long: `Perform one of the person's acts under their standing orders, checked first:
  1. --reason is given: what the act rests on, quoting the evidence (orders.no-reason, exit 2);
  2. the act is delegable at all (never: accept-config, accept-red, allow-pending-human, push,
     tag, release, orders: orders.non-delegable);
  3. the orders exist on the base branch, are accepted and not revoked (orders.missing,
     orders.unaccepted, orders.revoked);
  4. the act is named in the orders' acts: (orders.not-named), and no escalate: category
     holds for it (orders.escalate);
  5. the orders' conditions hold (orders.condition);
  6. each --evidence path exists in the project; a milestone approve names the log of the
     milestone's latest gate run when it has one (orders.evidence-missing);
  7. ape re-runs the mechanical check behind the act itself, at HEAD, whatever the conductor
     was told (D662.2): a milestone approve runs ` + "`ape milestone gate-test`" + ` on the milestone's
     gate folder (stub green, plain red), a tweak approve the change's check; not as it must be
     is orders.check-red, and the self-test's logs join the evidence.
Then the act runs as the person's own command does. --json reports {act, target, by, orders,
evidence: [{path, sha256}], checks, seq}.

The acts and their targets:
  answer <change-id> --text "<answer>"      change answer (a blocked change)
  tweak-approve <tweak-id>                  change approve (a milestone-less change)
  milestone-approve <M>                     milestone approve
  accept-task <change-id> <T<n>>            change accept (only where the project names a
                                            human-review delegate)
  decision-accept <ADR-id>                  decision accept
  question-answer <Q-id> --text "<answer>"  question answer (an orchestrator's question)
  prepare-release <version>                 record the release preparation; ape checks the
                                            CHANGELOG at HEAD names the version and hands back
                                            the tag and push commands, which stay the person's
  push-branch <branch>                      ape pushes the work branch (never main, master, the
                                            base or default branch, or a tag; no tags follow)
  open-pr <branch> --text "<title>"         gh pr create for the branch (body: --reason);
                                            without gh, refused with the push URL
Every act runs only when the orders name it under acts: (every act, D664+); before the
checks above, the orders' escalate: list (touches-gate, changes-config, tokens-over) sends
an act in one of its categories to the person (orders.escalate).

Exit codes: 0 done · 1 refused by a check or by the act itself (the code says which) · 2 usage
(no reason, an unknown act, a missing target).`,
		Example: "  ape conductor act tweak-approve fix-typo --reason \"brief asks for it; change check clean\" --evidence development/briefs/fix-typo.md\n" +
			"  ape conductor act milestone-approve M2 --reason \"gate self-test red on the 3 new scenarios, as planned\" --evidence _apex/state/logs/gate-M2.log\n" +
			"  ape conductor act answer add-export --text \"CSV, UTF-8\" --reason \"the brief names CSV\" --evidence development/briefs/export.md",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f.v1Flags, p); err != nil {
				return err
			}
			who, err := ordersPerson(cmd, p, f.orders)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			a := apexv1.DelegatedAct{Act: args[0], Person: who, Target: args[1], Text: text, Reason: reason, Evidence: evidence}
			if len(args) == 3 {
				a.Task = args[2]
			}
			switch {
			case a.Act == apexv1.OrderAcceptTask && a.Task == "":
				return v1Fail(cmd, &f.v1Flags, &apexv1.UsageError{Code: "usage.task", Msg: "accept-task needs the task: `ape conductor act accept-task <change-id> T<n> …`"}, nil)
			case (a.Act == apexv1.OrderAnswer || a.Act == apexv1.OrderQuestionAnswer) && strings.TrimSpace(text) == "":
				return v1Fail(cmd, &f.v1Flags, &apexv1.UsageError{Code: "usage.text", Msg: a.Act + " needs --text: the answer"}, nil)
			case a.Act == apexv1.OrderOpenPR && strings.TrimSpace(text) == "":
				return v1Fail(cmd, &f.v1Flags, &apexv1.UsageError{Code: "usage.text", Msg: "open-pr needs --text: the pull request's title (its body is --reason)"}, nil)
			}
			res, err := p.RunDelegated(cmd.Context(), a, cmd.ErrOrStderr())
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, res, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s: done, by %s (event seq %d)\n", res.Act, res.Target, res.By, res.Seq)
			})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&reason, "reason", "", "what the act rests on, quoting the evidence (required)")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil, "a path in the project the act rests on (repeatable)")
	cmd.Flags().StringVar(&text, "text", "", "answer's answer")
	return cmd
}

func newConductorNoteCmd() *cobra.Command {
	var f conductorFlags
	var hold, question string
	var resolves int
	cmd := &cobra.Command{
		Use:   "note --hold \"…\" | --question \"…\" | --resolved <n> \"…\"",
		Short: "Add a hold for the person or an open question to the decision log, or resolve one",
		Long: `Add to the person's decision log (_apex/state/conductor-<person>.md, written only by ape):
  --hold "…"          something waiting for the person
  --question "…"      a question open with the person or an orchestrator
  --resolved <n> "…"  close hold or question <n>, saying how
A conductor that resumes reads what is open with ` + "`ape conductor log --open`" + `.

Exit codes: 0 noted (prints the entry's number) · 2 usage (no text, or <n> is not open).`,
		Example: "  ape conductor note --hold \"M3 waits for the pricing call\"\n  ape conductor note --resolved 4 \"EUR, per Dana\"",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			who, err := ordersPerson(cmd, p, f.orders)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			kind, txt := "", ""
			set := 0
			if hold != "" {
				kind, txt = apexv1.DecisionHold, hold
				set++
			}
			if question != "" {
				kind, txt = apexv1.DecisionQuestion, question
				set++
			}
			if cmd.Flags().Changed("resolved") {
				kind, txt = apexv1.DecisionResolved, strings.Join(args, " ")
				set++
			}
			if set != 1 {
				return v1Fail(cmd, &f.v1Flags, &apexv1.UsageError{Code: "usage.kind", Msg: "give exactly one of --hold, --question, --resolved <n> \"…\""}, nil)
			}
			e, err := p.NoteDecision(who, kind, txt, resolves)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, e, func(w io.Writer) {
				fmt.Fprintf(w, "[%d] %s noted\n", e.N, e.Kind)
			})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&hold, "hold", "", "a hold for the person")
	cmd.Flags().StringVar(&question, "question", "", "an open question")
	cmd.Flags().IntVar(&resolves, "resolved", 0, "the hold or question this resolves (its number); the text is the argument")
	return cmd
}

func newConductorLogCmd() *cobra.Command {
	var f conductorFlags
	var open bool
	cmd := &cobra.Command{
		Use:   "log [--open]",
		Short: "Read the person's decision log: the acts done under the orders, the holds and questions",
		Long: `Print the person's decision log, newest first: every delegated act with its reason, orders
and evidence, every hold and question, and their resolutions. --open prints only the holds
and questions not resolved yet, what a resumed conductor reads first. --json prints
{person, path, entries} (with --open, entries are the open ones).

Exit codes: 0 printed (an absent log is empty) · 2 usage.`,
		Example: "  ape conductor log --open\n  ape conductor log --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			who, err := ordersPerson(cmd, p, f.orders)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			l, err := p.ReadDecisionLog(who)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			if open {
				l.Entries = l.Open()
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, l, func(w io.Writer) {
				if len(l.Entries) == 0 {
					fmt.Fprintln(w, "nothing")
					return
				}
				for _, e := range slices.Backward(l.Entries) {
					fmt.Fprintf(w, "[%d] %s %-8s %s", e.N, e.At, e.Kind, e.Text)
					if e.Reason != "" {
						fmt.Fprintf(w, " — %s", e.Reason)
					}
					fmt.Fprintln(w)
				}
			})
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&open, "open", false, "only the holds and questions not resolved")
	return cmd
}

// conductorTermName is the orchestrator terminal's session name.
func conductorTermName(p *apexv1.Project) string {
	return "ape-orchestrator-" + apexv1.PersonSlug(filepath.Base(p.Root))
}

func newConductorStartCmd() *cobra.Command {
	var f conductorFlags
	var model, idleAlert string
	cmd := &cobra.Command{
		Use:   "start [--project <dir>] [--model <m>] [--idle-alert 30m]",
		Short: "Start the project's orchestrator terminal: Claude Code running /apex-orchestrator, owned by ape",
		Long: `Start the project's orchestrator terminal in the background: a supervisor process (it
outlives this command and the shell that ran it) owns a terminal running Claude Code in the
project, as a person would start it (bypass permissions on, no ape session marker, so the
orchestrator starts its loops as from a person's session), and types /apex-orchestrator once
it is ready. Then it types the conductor's messages (` + "`ape conductor send`" + `), each marked
and on one line, only while the orchestrator is idle.

It logs the terminal's liveness as project events, which ` + "`ape status --wait --until conductor`" + `
wakes on:
  orchestrator-dead   the terminal's session ended
  orchestrator-idle   idle past --idle-alert (default: conductor.idle_alert in
                      _apex/config.yaml, else 30m) with a conductor message pending
One terminal per project: a second start while one runs is refused (exit 1).
On another host: ssh host 'ape conductor start --project /path'.

Exit codes: 0 started (prints the supervisor's pid) · 1 one already runs, or it did not start ·
2 usage.`,
		Example: "  ape conductor start\n  ssh build-host 'ape conductor start --project /srv/projects/notes'",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			if err := v1RefuseV0(cmd, &f.v1Flags, p); err != nil {
				return err
			}
			if st, err := conductorterm.LoadState(p.Root); err == nil && st.Alive && pidAlive(st.PID) {
				return v1Fail(cmd, &f.v1Flags, &apexv1.FailError{Code: apexv1.CodeConductorRunning, Msg: fmt.Sprintf("the orchestrator terminal already runs here (supervisor pid %d): `ape conductor stop` first", st.PID)}, nil)
			}
			alert, err := conductorIdleAlert(p, idleAlert)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			args := []string{"conductor", "serve", "--project", p.Root, "--idle-alert", alert.String()}
			if model != "" {
				args = append(args, "--model", model)
			}
			if err := os.MkdirAll(runlog.ConductorDir(p.Root), 0o755); err != nil {
				return err
			}
			logf, err := os.OpenFile(conductorterm.LogPath(p.Root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer logf.Close()
			c := exec.Command(self, args...) //nolint:noctx // ape starting its own supervisor, detached: it outlives this command
			c.Dir = p.Root
			c.Stdout, c.Stderr = logf, logf
			c.SysProcAttr = detachedProcAttr()
			if err := c.Start(); err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			pid := c.Process.Pid
			_ = c.Process.Release()
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, map[string]any{"ok": true, "pid": pid, "log": p.Rel(conductorterm.LogPath(p.Root))}, func(w io.Writer) {
				fmt.Fprintf(w, "orchestrator terminal starting (supervisor pid %d; log %s)\n", pid, p.Rel(conductorterm.LogPath(p.Root)))
			})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&model, "model", "", "the orchestrator's model (default: Claude Code's)")
	cmd.Flags().StringVar(&idleAlert, "idle-alert", "", "how long idle with a message pending before orchestrator-idle (default conductor.idle_alert, else 30m)")
	return cmd
}

// conductorIdleAlert is --idle-alert, else conductor.idle_alert, else
// the default.
func conductorIdleAlert(p *apexv1.Project, flag string) (time.Duration, error) {
	v := flag
	if v == "" {
		if cfg, err := apexv1.LoadConfig(p.Root); err == nil {
			v = cfg.Conductor.IdleAlert
		}
	}
	if v == "" {
		return conductorterm.DefaultIdleAlert, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, &apexv1.UsageError{Code: "usage.idle-alert", Msg: fmt.Sprintf("idle alert %q is not a positive duration (e.g. 30m)", v)}
	}
	return d, nil
}

func newConductorServeCmd() *cobra.Command {
	var f conductorFlags
	var model, idleAlert string
	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Run the orchestrator terminal's supervisor in the foreground (what start runs)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			alert, err := conductorIdleAlert(p, idleAlert)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			sid := uuid.NewString()
			argv := []string{"claude", "--dangerously-skip-permissions", "--session-id", sid}
			if model != "" {
				argv = append(argv, "--model", model)
			}
			nonce := uuid.NewString()[:8]
			return conductorterm.Serve(ctx, conductorterm.Config{
				Root: p.Root, Name: conductorTermName(p), Nonce: nonce, First: "/apex-orchestrator", IdleAlert: alert,
				Driver: &conductorterm.ReplDriver{Name: conductorTermName(p), Dir: p.Root, Argv: argv},
				Ready:  repl.PaneIdle,
				Transcript: func(time.Time) string {
					return sessionref.FindTranscript(sid)
				},
				Emit: func(event, reason string) {
					p.Emit(context.WithoutCancel(ctx), apexv1.Event{Event: event, Reason: reason}, cmd.ErrOrStderr())
				},
			})
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&model, "model", "", "the orchestrator's model")
	cmd.Flags().StringVar(&idleAlert, "idle-alert", "", "idle alert")
	return cmd
}

func newConductorSendCmd() *cobra.Command {
	var f conductorFlags
	cmd := &cobra.Command{
		Use:   "send \"<message>\"",
		Short: "Queue a message for the project's orchestrator; ape types it, marked, once it is idle",
		Long: `Queue one message for the orchestrator terminal (` + "`ape conductor start`" + `). The supervisor
types it when the orchestrator is idle, on one line, marked as a conductor message
(<ape-delivery seq=… nonce=…> … </ape-delivery>): ape's hooks record it as no person's words,
so it never grants a person act, and apex-orchestrator reads it as its person's delegate.
At most 600 characters; for more, write a file in the project and name its path.

Exit codes: 0 queued (prints its seq) · 1 no terminal runs here · 2 usage (empty, too long).`,
		Example: "  ape conductor send \"M2's plan is approved: start it\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			st, err := conductorterm.LoadState(p.Root)
			if err != nil || !st.Alive {
				return v1Fail(cmd, &f.v1Flags, &apexv1.FailError{Code: apexv1.CodeConductorNotRunning, Msg: "no orchestrator terminal runs here: `ape conductor start` first"}, nil)
			}
			m, err := conductorterm.Send(p.Root, args[0], time.Now())
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, &apexv1.UsageError{Code: "usage.message", Msg: err.Error()}, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, m, func(w io.Writer) {
				fmt.Fprintf(w, "queued %d\n", m.Seq)
			})
		},
	}
	f.add(cmd)
	return cmd
}

func newConductorReadCmd() *cobra.Command {
	var f conductorFlags
	var since int
	cmd := &cobra.Command{
		Use:   "read [--since <n>]",
		Short: "Print the orchestrator's messages after the n-th, from its transcript",
		Long: `Print the orchestrator's assistant messages after the n-th (default 0: all), numbered from 1,
from its session transcript. Pass the last n you read as --since next time.
--json prints [{n, at, text}].

Exit codes: 0 printed · 1 no terminal ran here.`,
		Example: "  ape conductor read --since 12 --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			rs, err := conductorterm.Read(p.Root, since)
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, &apexv1.FailError{Code: apexv1.CodeConductorNotRunning, Msg: err.Error()}, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, rs, func(w io.Writer) {
				for _, r := range rs {
					fmt.Fprintf(w, "[%d] %s\n%s\n\n", r.N, r.At, r.Text)
				}
			})
		},
	}
	f.add(cmd)
	cmd.Flags().IntVar(&since, "since", 0, "print the messages after this one")
	return cmd
}

func newConductorStatusCmd() *cobra.Command {
	var f conductorFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "The orchestrator terminal (alive, idle, pending messages) and the decision log's open holds and questions",
		Long: `Print the project's orchestrator terminal — alive, idle and since when, the messages pending
(queued, or typed with no reply since), the newest reply — and the person's decision log's
open holds and questions (` + "`ape conductor log --open`" + `). --json prints {terminal, open}.

Exit codes: 0 printed (no terminal is "terminal": null) · 2 usage.`,
		Example: "  ape conductor status --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			st, err := conductorterm.LoadState(p.Root)
			if err != nil && !errors.Is(err, conductorterm.ErrNoTerminal) {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			if st != nil && st.Alive && !pidAlive(st.PID) {
				st.Alive, st.Error = false, "the supervisor is gone without recording an end"
			}
			open := []apexv1.DecisionEntry{}
			if who, err := ordersPerson(cmd, p, f.orders); err == nil {
				if l, err := p.ReadDecisionLog(who); err == nil {
					open = l.Open()
				}
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, map[string]any{"terminal": st, "open": open}, func(w io.Writer) {
				if st == nil {
					fmt.Fprintln(w, "no orchestrator terminal (ape conductor start)")
				} else {
					fmt.Fprintf(w, "terminal %s: alive %v, idle %v (since %s), pending %d, last reply %s\n", st.Name, st.Alive, st.Idle, st.IdleSince, len(st.Pending), st.LastReplyAt)
				}
				for _, e := range open {
					fmt.Fprintf(w, "open [%d] %s: %s\n", e.N, e.Kind, e.Text)
				}
			})
		},
	}
	f.add(cmd)
	return cmd
}

func newConductorStopCmd() *cobra.Command {
	var f conductorFlags
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the project's orchestrator terminal",
		Long: `Stop the orchestrator terminal: its supervisor ends the Claude Code session and records the
end. Messages still queued stay queued for the next start.

Exit codes: 0 stopped, or none ran · 1 the supervisor could not be signalled.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			st, err := conductorterm.LoadState(p.Root)
			if err != nil || !st.Alive || !pidAlive(st.PID) {
				return v1Emit(cmd.OutOrStdout(), &f.v1Flags, map[string]any{"ok": true, "stopped": false}, func(w io.Writer) {
					fmt.Fprintln(w, "no orchestrator terminal runs here")
				})
			}
			proc, err := os.FindProcess(st.PID)
			if err == nil {
				err = proc.Signal(os.Interrupt)
			}
			if err != nil {
				return v1Fail(cmd, &f.v1Flags, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f.v1Flags, map[string]any{"ok": true, "stopped": true, "pid": st.PID}, func(w io.Writer) {
				fmt.Fprintf(w, "stopping the orchestrator terminal (supervisor pid %d)\n", st.PID)
			})
		},
	}
	f.add(cmd)
	return cmd
}

func newConductorWatchCmd() *cobra.Command {
	var f conductorFlags
	var follow bool
	cmd := &cobra.Command{
		Use:   "watch [--follow]",
		Short: "Show the orchestrator terminal's screen, read-only",
		Long: `Print the orchestrator terminal's last screen, as the supervisor captured it. Read-only: nothing
typed here reaches the orchestrator (the conductor sends through ` + "`ape conductor send`" + `).
--follow reprints it every 2 seconds until interrupted.

Exit codes: 0 printed · 1 no terminal ran here.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f.v1Flags)
			if err != nil {
				return err
			}
			for {
				pane, err := os.ReadFile(conductorterm.PanePath(p.Root))
				if err != nil {
					return v1Fail(cmd, &f.v1Flags, &apexv1.FailError{Code: apexv1.CodeConductorNotRunning, Msg: "no orchestrator terminal ran here: `ape conductor start`"}, nil)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\x1b[2J\x1b[H%s\n", pane)
				if !follow {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&follow, "follow", false, "reprint every 2 seconds")
	return cmd
}

func newConductorWatcherCmd() *cobra.Command {
	var projects []string
	var pane, ackAlert string
	var poll time.Duration
	cmd := &cobra.Command{
		Use:   "watcher --project <dir>… [--pane <tmux pane>] [--ack-alert 15m]",
		Short: "Follow the projects' events and wake the conductor: typed into its tmux pane, else notify-only",
		Long: `Run the conductor's watcher until interrupted (another terminal, or in the background). It
follows each --project's events — block, person (a question included), question,
orchestrator-dead, orchestrator-idle, run-lost, delegated, orders — and turns each into a
delivery, id <project>.<seq>.

When your conductor (your own Claude Code session) runs in a tmux pane — --pane <target>, or
the one tmux pane running claude when there is exactly one — the watcher types each delivery
into it, marked (<ape-delivery …>, never your words: a prompt carrying one grants no person
act), only when all of these hold:
  - the pane's foreground process is claude and a Claude Code session registered on this
    machine (<config>/sessions/<pid>.json) — a stale or wrong pane, a shell, gets nothing;
  - no turn is running ("esc to interrupt" absent) and the input row is empty — never after
    your half-typed line;
  - the screen did not change between two looks (one --poll apart).
The conductor acknowledges each: ` + "`ape conductor ack <id>`" + `. A delivery not acknowledged within
--ack-alert (default conductor.ack_alert of the first project, else 15m) logs
conductor-stalled in its project, once, so that project's notify: command reaches you.

Without a pane the watcher only records: each of those events already runs the project's
notify: command (notify-only). State and acknowledgements are kept in
$XDG_STATE_HOME/ape/conductor (else ~/.local/state/ape/conductor).

Exit codes: 0 stopped (Ctrl-C) · 2 usage.`,
		Example: "  ape conductor watcher --project ~/src/notes --project ~/src/billing --pane %3",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(projects) == 0 {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projects = []string{wd}
			}
			roots := make([]string, 0, len(projects))
			for _, d := range projects {
				abs, err := filepath.Abs(d)
				if err != nil {
					return err
				}
				roots = append(roots, apexv1.FindRoot(abs))
			}
			alert, err := conductorAckAlert(roots[0], ackAlert)
			if err != nil {
				return usageErr(err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if pane == "" {
				pane = conductorwatch.DiscoverPane(ctx)
			}
			var tp conductorwatch.Pane
			if pane != "" {
				tp = &conductorwatch.Tmux{Target: pane}
				fmt.Fprintf(cmd.ErrOrStderr(), "watcher: delivering into tmux pane %s\n", pane)
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "watcher: no conductor pane (--pane, or exactly one tmux pane running claude): notify-only")
			}
			w := conductorwatch.New(conductorwatch.Config{
				Projects: roots, StateDir: conductorwatch.StateDir(), Pane: tp, Nonce: uuid.NewString()[:8],
				AckAlert: alert, Poll: poll,
				Log: func(format string, a ...any) { fmt.Fprintf(cmd.ErrOrStderr(), format, a...) },
			})
			return w.Run(ctx)
		},
	}
	cmd.Flags().StringArrayVar(&projects, "project", nil, "a project to follow (repeatable; default the current directory's)")
	cmd.Flags().StringVar(&pane, "pane", "", "the conductor's tmux pane (-t target); default the one pane running claude")
	cmd.Flags().StringVar(&ackAlert, "ack-alert", "", "how long a delivery may go unacknowledged before conductor-stalled (default conductor.ack_alert, else 15m)")
	cmd.Flags().DurationVar(&poll, "poll", 2*time.Second, "how often it looks")
	return cmd
}

// conductorAckAlert is --ack-alert, else conductor.ack_alert, else 15m.
func conductorAckAlert(root, flag string) (time.Duration, error) {
	v := flag
	if v == "" {
		if cfg, err := apexv1.LoadConfig(root); err == nil {
			v = cfg.Conductor.AckAlert
		}
	}
	if v == "" {
		return conductorwatch.DefaultAckAlert, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("ack alert %q is not a positive duration (e.g. 15m)", v)
	}
	return d, nil
}

func newConductorAckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ack <id>",
		Short: "Acknowledge a delivery the watcher typed (its id, <project>.<seq>)",
		Long: `Acknowledge a delivery the conductor's watcher typed into its pane: the conductor has read
the event (it reads ` + "`ape status`" + ` for it; the delivery is never the person's words). A
delivery not acknowledged within the ack alert notifies the person.

Exit codes: 0 acknowledged · 2 usage.`,
		Example: "  ape conductor ack notes.42",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := conductorwatch.Ack(conductorwatch.StateDir(), args[0], time.Now()); err != nil {
				return usageErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "acknowledged %s\n", args[0])
			return nil
		},
	}
	return cmd
}
