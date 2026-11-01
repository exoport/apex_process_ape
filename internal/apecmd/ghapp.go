package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
under its own user, holds the private keys of two GitHub Apps (apex-setup, apex-member),
and answers over NATS:

  ape.sup.ghapp.token.<user>   a member's apex-member token: one hour, narrowed to one
                               repository and a purpose (push, pr, read); never workflows
  ape.sup.ghapp.setup.<user>   an operator's templated set-up act: rulesets (R1–R4), the
                               queue workflow's PR, the secrets APE_NATS_URL and
                               APE_NATS_CREDS, a runner registration token

Every request is logged (never a token or a secret's value); secrets and runner-token also
run the operator's notify. ` + "`ape ghapp print-config`" + ` and ` + "`ape ghapp print-unit`" + ` print the files a
person installs.`,
	}
	cmd.AddCommand(newGhappServeCmd(), newGhappPrintConfigCmd(), newGhappPrintUnitCmd(), newGhappSetupCmd())
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
	var repo, natsURL, members, operators, notify string
	var setupID, memberID, setupInst, memberInst int64
	cmd := &cobra.Command{
		Use:   "print-config --repo <owner>/<name> --setup-app-id N --setup-installation N --member-app-id N --member-installation N --nats-url URL --members a,b --operators a",
		Short: "Print /etc/apex-ghapp/config.yaml, filled in",
		Long: `Print the service's config for one repository, filled in with the two Apps' ids and
their installation ids there (the App's settings page and its installation URL show them),
the NATS URL, the members who may ask for tokens and the operators who may run set-up acts
(NATS user names: the credentials' user tokens). The keys are read through systemd's
LoadCredential (` + "`ape ghapp print-unit`" + `); the act log is /var/lib/apex-ghapp/acts.jsonl.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" || natsURL == "" || setupID == 0 || memberID == 0 || setupInst == 0 || memberInst == 0 || members == "" || operators == "" {
				return usageErr(errors.New("every flag but --notify is required"))
			}
			list := func(s string) string {
				parts := strings.Split(s, ",")
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				return "[" + strings.Join(parts, ", ") + "]"
			}
			fmt.Fprintf(cmd.OutOrStdout(), `# /etc/apex-ghapp/config.yaml — written by `+"`ape ghapp print-config`"+` (PLAN-V15-GHAPP §7).
# Root-owned, mode 0644; no secrets in it (the keys come through LoadCredential).
nats:
  url: %s
  creds: /var/lib/apex-ghapp/nats.creds
api: https://api.github.com
setup:
  app_id: %d
  key: ${CREDENTIALS_DIRECTORY}/setup.pem
  installations:
    %s: %d
member:
  app_id: %d
  key: ${CREDENTIALS_DIRECTORY}/member.pem
  installations:
    %s: %d
repos:
  %s:
    members: %s
    operators: %s
log: /var/lib/apex-ghapp/acts.jsonl
notify: %q
`, natsURL, setupID, repo, setupInst, memberID, repo, memberInst, repo, list(members), list(operators), notify)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the repository, <owner>/<name>")
	f.Int64Var(&setupID, "setup-app-id", 0, "apex-setup's App id")
	f.Int64Var(&setupInst, "setup-installation", 0, "apex-setup's installation id on the repository")
	f.Int64Var(&memberID, "member-app-id", 0, "apex-member's App id")
	f.Int64Var(&memberInst, "member-installation", 0, "apex-member's installation id on the repository")
	f.StringVar(&natsURL, "nats-url", "", "the NATS URL(s), comma-separated")
	f.StringVar(&members, "members", "", "NATS users who may ask for member tokens, comma-separated")
	f.StringVar(&operators, "operators", "", "NATS users who may run set-up acts, comma-separated")
	f.StringVar(&notify, "notify", "", "a command (sh -c) run after secrets and runner-token, the act's record on stdin")
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
LoadCredential=setup.pem:/etc/apex-ghapp/keys/setup.pem
LoadCredential=member.pem:/etc/apex-ghapp/keys/member.pem
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
to it through LoadCredential from root-owned /etc/apex-ghapp/keys/{setup,member}.pem,
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

  rulesets       create or update R1–R4 (R4's required check once the queue workflow is on
                 the default branch: run it again after the workflow PR merges)
  workflow       open the PR adding .github/workflows/apex-queue.yml
  secrets        set APE_NATS_URL or APE_NATS_CREDS (--name), the value read from
                 --value-file (or - for stdin); never echoed
  runner-token   print a self-hosted runner registration token (one hour) for the person
                 who registers the runner

The order for a new repository: workflow, then (once its PR merged) rulesets, then the two
secrets; runner-token when the runner is registered. Every act is logged by the service, and
secrets and runner-token notify the operator.

Exit codes: 0 done · 1 refused or failed (the service's message) · 2 usage, or no service.`,
		Example: "  ape ghapp setup workflow\n  ape ghapp setup secrets --name APE_NATS_CREDS --value-file hot/ci-nats.creds",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			act := args[0]
			req := ghapp.Request{Act: act}
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
