package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// newGhappCmd is `ape ghapp` (v1.5, PLAN-V15-GHAPP): the GitHub App
// service that holds the App keys on the coordination host.
func newGhappCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ghapp",
		Short: "The GitHub App service: holds the App keys, issues narrowed tokens, runs templated set-up acts",
		Long: `The key-holding service of a project integrating through GitHub (APEX v1.5,
PLAN-V15-GHAPP). It runs on the coordination host as the systemd unit apex-ghapp.service
under its own user, holds the private keys of two GitHub Apps (apex-warden, apex-pusher),
and answers over NATS:

  ape.sup.ghapp.pusher.<user>   a member's apex-pusher token: one hour, narrowed to one
                               repository and a purpose (push, pr, read); never workflows
  ape.sup.ghapp.warden.<user>   an operator's templated set-up act: rulesets (R1–R4), the
                               queue workflow's PR, the secrets APE_NATS_URL and
                               APE_NATS_CREDS, a runner registration token

Every request is logged (never a token or a secret's value); secrets and runner-token also
run the operator's notify. ` + "`ape ghapp print-config`" + ` and ` + "`ape ghapp print-unit`" + ` print the files a
person installs.`,
	}
	cmd.AddCommand(newGhappServeCmd(), newGhappPrintConfigCmd(), newGhappPrintUnitCmd(), newGhappSetupCmd(), newGhappLandCmd(), newGhappFetchCmd(), newGhappProbeCmd())
	return cmd
}

func newGhappServeCmd() *cobra.Command {
	var config string
	cmd := &cobra.Command{
		Use:   "serve --config <path>",
		Short: "Run the service (the systemd unit runs this)",
		Long: `Run the GitHub App service with its config: load both keys, connect to NATS with the
service's own credential, and answer token and set-up requests until stopped.

Exit codes: 0 stopped · 1 the config, a key or NATS failed · 2 usage.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := ghapp.LoadConfig(config)
			if err != nil {
				return failErr(err)
			}
			s, err := ghapp.NewService(cfg)
			if err != nil {
				return failErr(err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			nc, err := natsconn.Connect(ctx, natsconn.Config{URL: cfg.NATS.URL, CredsFile: cfg.NATS.Creds}, "ape-ghapp")
			if err != nil {
				return failErr(err)
			}
			defer nc.Close()
			fmt.Fprintf(cmd.ErrOrStderr(), "ape ghapp: serving %d repository(ies) on %s and %s\n", len(cfg.Repos), ghapp.TokenSubject, ghapp.SetupSubject)
			return s.Serve(ctx, nc)
		},
	}
	cmd.Flags().StringVar(&config, "config", "/etc/apex-ghapp/config.yaml", "the service's config")
	return cmd
}

func newGhappPrintConfigCmd() *cobra.Command {
	var repo, natsURL, members, operators, notify, runnerLabel, watchers string
	var wardenID, pusherID, wardenInst, pusherInst int64
	cmd := &cobra.Command{
		Use:   "print-config --repo <owner>/<name> --warden-app-id N --warden-installation N --pusher-app-id N --pusher-installation N --nats-url URL --members a,b --operators a",
		Short: "Print /etc/apex-ghapp/config.yaml, filled in",
		Long: `Print the service's config for one repository, filled in with the two Apps' ids and
their installation ids there (the App's settings page and its installation URL show them),
the NATS URL, the members who may ask for tokens and the operators who may run set-up acts
(NATS user names: the credentials' user tokens), and for queue/runner the repository's own
runner label, which the workflow set-up act writes into every job (D871), and for queue/watch the
NATS users of ` + "`ape ci watch`" + ` (--watchers), who may ask for the read-only watch token and nothing else. The keys are read through systemd's
LoadCredential (` + "`ape ghapp print-unit`" + `); the act log is /var/lib/apex-ghapp/acts.jsonl, and each act is
also one JSON line on stderr (the unit's journal), after "ape ghapp: act", never a token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" || natsURL == "" || wardenID == 0 || pusherID == 0 || wardenInst == 0 || pusherInst == 0 || members == "" || operators == "" {
				return usageErr(errors.New("every flag but --notify and --runner-label is required"))
			}
			labelLine := ""
			if runnerLabel != "" {
				if err := apexv1.CheckRunnerLabel(runnerLabel); err != nil {
					return usageErr(err)
				}
				labelLine = "\n    runner_label: " + runnerLabel
			}
			list := func(s string) string {
				parts := strings.Split(s, ",")
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				return "[" + strings.Join(parts, ", ") + "]"
			}
			if watchers != "" {
				labelLine += "\n    watchers: " + list(watchers)
			}
			fmt.Fprintf(cmd.OutOrStdout(), `# /etc/apex-ghapp/config.yaml — written by `+"`ape ghapp print-config`"+` (PLAN-V15-GHAPP §7).
# Root-owned, mode 0644; no secrets in it (the keys come through LoadCredential).
nats:
  url: %s
  creds: /var/lib/apex-ghapp/nats.creds
api: https://api.github.com
warden:
  app_id: %d
  key: ${CREDENTIALS_DIRECTORY}/warden.pem
  installations:
    %s: %d
pusher:
  app_id: %d
  key: ${CREDENTIALS_DIRECTORY}/pusher.pem
  installations:
    %s: %d
repos:
  %s:
    members: %s
    operators: %s%s
log: /var/lib/apex-ghapp/acts.jsonl
notify: %q
`, natsURL, wardenID, repo, wardenInst, pusherID, repo, pusherInst, repo, list(members), list(operators), labelLine, notify)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the repository, <owner>/<name>")
	f.Int64Var(&wardenID, "warden-app-id", 0, "apex-warden's App id")
	f.Int64Var(&wardenInst, "warden-installation", 0, "apex-warden's installation id on the repository")
	f.Int64Var(&pusherID, "pusher-app-id", 0, "apex-pusher's App id")
	f.Int64Var(&pusherInst, "pusher-installation", 0, "apex-pusher's installation id on the repository")
	f.StringVar(&natsURL, "nats-url", "", "the NATS URL(s), comma-separated")
	f.StringVar(&members, "members", "", "NATS users who may ask for member tokens, comma-separated")
	f.StringVar(&operators, "operators", "", "NATS users who may run set-up acts, comma-separated")
	f.StringVar(&notify, "notify", "", "a command (sh -c) run after secrets and runner-token, the act's record on stdin")
	f.StringVar(&watchers, "watchers", "", "queue/watch: the NATS users of `ape ci watch`, comma-separated (the watch token only)")
	f.StringVar(&runnerLabel, "runner-label", "", "queue/runner: the repository's own self-hosted runner label, for the workflow act (never self-hosted or a default label)")
	return cmd
}

// ghappUnit is apex-ghapp.service (PLAN-V15-GHAPP §3, research finding 7).
const ghappUnit = `# /etc/systemd/system/apex-ghapp.service — written by ` + "`ape ghapp print-unit`" + ` (PLAN-V15-GHAPP §3, §7).
[Unit]
Description=APEX GitHub App service (holds the App keys; NATS request-reply)
After=network-online.target
Wants=network-online.target

[Service]
User=apex-ghapp
Group=apex-ghapp
ExecStart=%s ghapp serve --config /etc/apex-ghapp/config.yaml
Restart=on-failure
RestartSec=5s
# The keys, read by systemd from root-owned files and handed to the
# service only (research finding 7): never in the service's home.
LoadCredential=warden.pem:/etc/apex-ghapp/keys/warden.pem
LoadCredential=pusher.pem:/etc/apex-ghapp/keys/pusher.pem
StateDirectory=apex-ghapp
StateDirectoryMode=0700
UMask=0077
LimitCORE=0
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectProc=invisible
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
SystemCallArchitectures=native
ReadWritePaths=/var/lib/apex-ghapp

[Install]
WantedBy=multi-user.target
`

func newGhappPrintUnitCmd() *cobra.Command {
	var bin string
	cmd := &cobra.Command{
		Use:   "print-unit [--ape /usr/local/bin/ape]",
		Short: "Print the hardened systemd unit apex-ghapp.service",
		Long: `Print apex-ghapp.service: the service under its own user apex-ghapp, the two keys handed
to it through LoadCredential from root-owned /etc/apex-ghapp/keys/{warden,pusher}.pem,
its state in /var/lib/apex-ghapp (0700), no core dumps, umask 0077, and the systemd
sandboxing of PLAN-V15-GHAPP §3. Per-host egress filtering (api.github.com and NATS only)
is not expressible in a unit and is stated in the plan's threat model.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), ghappUnit, bin)
			return nil
		},
	}
	cmd.Flags().StringVar(&bin, "ape", "/usr/local/bin/ape", "the ape binary the unit runs")
	return cmd
}

func newGhappSetupCmd() *cobra.Command {
	var f coordFlags
	var name, valueFile string
	cmd := &cobra.Command{
		Use:   "setup <rulesets|workflow|secrets|runner-token> [--name APE_NATS_URL|APE_NATS_CREDS --value-file <path>|-]",
		Short: "Run one templated set-up act on the coordinated repository (the operator's)",
		Long: `Ask the GitHub App service to run one set-up act on this project's coordinated repository
(coordination.github.repo), as this checkout's NATS user — the service runs it only for an
operator of that repository (PLAN-V15-GHAPP §4):

  rulesets       create or update R1–R4. In this release R4 carries no required check:
                 queue/poll and queue/watch run nothing on GitHub (D878, D884)
  workflow       queue/runner's: open the PR adding .github/workflows/apex-queue.yml
  secrets        queue/runner's: set APE_NATS_URL or APE_NATS_CREDS (--name), the value
                 read from --value-file (or - for stdin); never echoed
  runner-token   queue/runner's: print a self-hosted runner registration token (one hour)
                 for the person who registers the runner

rulesets is the only act this release's modes use: a new repository needs it once. The
three queue/runner acts are refused (ci.runner-not-in-release), since this release does not
ship queue/runner (D884) — the secrets would only put a NATS credential into GitHub. Every
act is logged by the service, refusals too.

Exit codes: 0 done · 1 refused or failed (the service's message) · 2 usage, or no service.`,
		Example: "  ape ghapp setup rulesets",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act := args[0]
			req := ghapp.Request{Act: act}
			// D884 (RQR-4): queue/runner's acts are refused here, before a
			// secret's file is read or NATS is reached.
			if act == "workflow" || act == "secrets" || act == "runner-token" {
				if err := apexv1.RunnerModeAvailable(); err != nil {
					return failErr(err)
				}
			}
			switch act {
			case "rulesets", "workflow", "runner-token":
			case "secrets":
				if !ghapp.SecretNames[name] || valueFile == "" {
					return usageErr(errors.New("secrets needs --name APE_NATS_URL|APE_NATS_CREDS and --value-file <path>|-"))
				}
				var data []byte
				var err error
				if valueFile == "-" {
					data, err = io.ReadAll(cmd.InOrStdin())
				} else {
					data, err = os.ReadFile(valueFile)
				}
				if err != nil {
					return usageErr(err)
				}
				req.Name, req.Value = name, strings.TrimRight(string(data), "\n")+"\n"
				if name == "APE_NATS_URL" {
					req.Value = strings.TrimSpace(string(data))
				}
			default:
				return usageErr(fmt.Errorf("act %q: rulesets, workflow, secrets or runner-token", act))
			}
			p, err := f.project()
			if err != nil {
				return usageErr(err)
			}
			m, err := member.Load(p)
			if err != nil || m.GitHubRepo == "" {
				return usageErr(errors.New("this project names no coordinated repository (coordination.github.repo)"))
			}
			req.Repo = m.GitHubRepo
			id, err := natsconn.DecodeIdentity(m.Creds)
			if err != nil {
				return failErr(err)
			}
			nc, _, err := f.connect(cmd.Context(), "ape-ghapp-setup")
			if err != nil {
				return err
			}
			defer nc.Close()
			rep, err := ghapp.Ask(cmd.Context(), nc, ghapp.SetupSubject, id.SubjectToken, req)
			if err != nil {
				return failErr(err)
			}
			for _, line := range rep.Result {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			if act == "runner-token" {
				fmt.Fprintf(cmd.OutOrStdout(), "runner registration token (expires %s): %s\n", rep.ExpiresAt.Format(time.RFC3339), rep.Token)
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&name, "name", "", "the secret's name (secrets): APE_NATS_URL or APE_NATS_CREDS")
	cmd.Flags().StringVar(&valueFile, "value-file", "", "the secret's value, from a file (or - for stdin)")
	return cmd
}

func newGhappLandCmd() *cobra.Command {
	var f v1Flags
	var title string
	var wait time.Duration
	var dequeue, state bool
	cmd := &cobra.Command{
		Use:   "land <branch> [--title <t>] [--wait 30m]",
		Short: "Land a local branch on main through main's merge queue (push, PR, enqueue, wait)",
		Long: `Land a local branch on main through main's merge queue, for changes outside a milestone a
person decides to land — a framework update, the coordination config (v1.5 stage 1a, D706):
push the branch through the coordinated repository's pusher path (the App service's token
when coordination.github.app is set, else git's own credentials), open its pull request
into main (or reuse the open one; the title is --title, else the branch tip's subject),
enqueue it, and wait for merged, ejected or --wait. Never milestone/<M> (ape's) or main.

Exit codes: 0 merged · 1 refused, failed, ejected or closed · 2 usage · 75 still queued when
--wait ran out.`,
		Example: "  ape ghapp land apex/framework-v15-1a --wait 30m",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			branch := args[0]
			if state {
				line, err := member.PRStatus(ctx, p, branch, "main")
				if err != nil {
					return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: err.Error()}, nil)
				}
				fmt.Fprintln(cmd.OutOrStdout(), line)
				return nil
			}
			if dequeue {
				u, err := member.Dequeue(ctx, p, branch, "main")
				if err != nil {
					return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: "dequeuing " + branch + "'s PR failed: " + err.Error()}, nil)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s taken out of main's merge queue (still open)\n", u)
				return nil
			}
			sha, err := gitOut(ctx, p.Root, "rev-parse", "--verify", "refs/heads/"+branch)
			if err != nil {
				return usageErr(fmt.Errorf("no local branch %s", branch))
			}
			sha = strings.TrimSpace(sha)
			if title == "" {
				subject, _ := gitOut(ctx, p.Root, "log", "-1", "--format=%s", sha)
				title = strings.TrimSpace(subject)
			}
			if err := p.PushHead(ctx, sha, branch); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			api, err := p.Integrator(ctx)
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			pr, err := api.OpenPR(ctx, branch, "main", title, "Landed through main's merge queue by `ape ghapp land` (APEX v1.5).")
			if err != nil {
				return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: "opening the PR failed: " + err.Error()}, nil)
			}
			if _, err := api.Enqueue(ctx, pr); err != nil {
				return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: pr.URL + " opened, but enqueuing it in main's merge queue failed: " + err.Error()}, nil)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pushed %.7s to %s; %s enqueued in main's merge queue\n", sha, branch, pr.URL)
			deadline := time.Now().Add(wait)
			for {
				st, err := api.PRState(ctx, pr.Number)
				switch {
				case err != nil:
					return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: "reading " + pr.URL + ": " + err.Error()}, nil)
				case st.Merged:
					fmt.Fprintf(cmd.OutOrStdout(), "merged into main as %.7s\n", st.MergeCommit)
					return nil
				case st.Queue == "" || st.Closed:
					return v1Fail(cmd, &f, &apexv1.FailError{Code: apexv1.CodeIntegrationPR, Msg: pr.URL + " left main's queue without merging (ejected or closed): read its checks on GitHub"}, nil)
				}
				if !time.Now().Before(deadline) || !apexv1.WaitQueue(ctx, nil) {
					fmt.Fprintf(cmd.OutOrStdout(), "still queued (%s)\n", st.Queue)
					return usageErrExit(exitCodeStillRunning, fmt.Errorf("%s is still in main's queue", pr.URL))
				}
			}
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&title, "title", "", "the PR's title (default: the branch tip's subject)")
	cmd.Flags().DurationVar(&wait, "wait", 30*time.Minute, "how long to wait for the queue")
	cmd.Flags().BoolVar(&state, "state", false, "print the branch's newest PR into main and its state instead (read-only)")
	cmd.Flags().BoolVar(&dequeue, "dequeue", false, "take the branch's open PR out of main's merge queue instead (a hold; the PR stays open)")
	return cmd
}

func newGhappFetchCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "fetch [<branch>...]",
		Short: "Fetch branches of the coordinated repository through the App's read token, fast-forwarding them here",
		Long: `Fetch branches (default main) of the coordinated repository into refs/remotes/<remote>/…
— through the App service's read token when coordination.github.app is set, so no
person's ssh key or agent is needed — and fast-forward each local branch that is only
behind (checked out with a clean tree, or not checked out). A branch with commits the
remote lacks is left as it is.

Exit codes: 0 fetched · 1 the fetch or a fast-forward failed · 2 usage.`,
		Example: "  ape ghapp fetch main",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				args = []string{"main"}
			}
			lines, err := p.FetchAndSync(cmd.Context(), args...)
			for _, l := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), l)
			}
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return nil
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func newGhappProbeCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:    "probe <milestone/M>",
		Short:  "Research's mirror checks of R1–R4 (D706 C.5): rules for a branch and for main, the App's push and force push",
		Hidden: true,
		Long: `Run research's mirror checks against the coordinated repository: (1) the rules GitHub
applies to <branch> by name (it need not exist); (2) the App's push creating <branch> at
main's tip (R1's bypass: accepted); (3) the App's force push of a commit that does not
descend from it (R2, no bypass: refused); (4) the rules on main (the merge queue, method
MERGE). The pushed branch stays: R3 refuses its deletion, a person prunes it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			ctx, w, branch := cmd.Context(), cmd.OutOrStdout(), args[0]
			show := func(b string) {
				rules, err := member.RulesFor(ctx, p, b)
				if err != nil {
					fmt.Fprintf(w, "rules for %s: %v\n", b, err)
					return
				}
				fmt.Fprintf(w, "rules for %s: %s\n", b, strings.Join(rules, ", "))
			}
			show(branch)
			url, env, err := member.GitVia(ctx, p, "push")
			if err != nil {
				return failErr(fmt.Errorf("no App push path: %w", err))
			}
			if url == "" {
				return failErr(errors.New("no App push path: coordination.github.app is off"))
			}
			if _, err := p.FetchAndSync(ctx, "main"); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			mainTip, _ := gitOut(ctx, p.Root, "rev-parse", "refs/remotes/origin/main")
			mainTip = strings.TrimSpace(mainTip)
			push := func(what string, args ...string) {
				c := exec.CommandContext(ctx, "git", //nolint:gosec // fixed git verb; refspecs are ours
					append([]string{"-C", p.Root, "push", "--porcelain", "--no-follow-tags"}, args...)...)
				c.Env = append(os.Environ(), append(env, "GIT_TERMINAL_PROMPT=0")...)
				out, err := c.CombinedOutput()
				verdict := "accepted"
				if err != nil {
					verdict = "refused"
				}
				fmt.Fprintf(w, "%s: %s — %s\n", what, verdict, strings.TrimSpace(probeLastLine(string(out))))
			}
			push("the App's push creating "+branch+" at main "+mainTip[:min(7, len(mainTip))], url, mainTip+":refs/heads/"+branch)
			orphan, err := gitOut(ctx, p.Root, "commit-tree", "-m", "probe: a commit not descending from "+branch, mainTip+"^{tree}")
			if err == nil {
				push("the App's force push of a non-descendant to "+branch, "--force", url, strings.TrimSpace(orphan)+":refs/heads/"+branch)
			}
			show("main")
			return nil
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

func probeLastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
