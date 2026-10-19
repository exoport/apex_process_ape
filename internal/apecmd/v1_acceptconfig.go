package apecmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newProjectAcceptConfigCmd() *cobra.Command {
	var f v1Flags
	var by string
	cmd := &cobra.Command{
		Use:   "accept-config --by <who>",
		Short: "Accept the committed _apex/config.yaml's guarded blocks (limits, checks, caps, …)",
		Long: `Fifteen blocks of _apex/config.yaml can loosen what ape enforces, and are guarded:
  limits        attempts, re-plans, consecutive blocks, the budget
  checks        vacuous output
  retries       red checks run again
  requires      unrun checks
  timeouts      how long a check, the regression command or a gate may run
  caps          the artefact size caps (D69; see ape project check --help)
  regression    the project's own check command (unset: the net is off)
  commit        the archive's squash (hooks: skip skips the project's hooks)
  evidence      archive|off (off keeps no evidence)
  marketplaces, plugins, tools   what runs inside a session (hooks, MCP, skills)
  human_review  who accepts a human-review task in a person's place (D75; docs: D152)
  release       how apex-release proposes the next version, and what it brings up to date (D120)
  secrets       the fingerprints the commit secret scan lets through (D155; commit.secret):
                an entry counts only once a person accepts it (R23)
The guard compares whole blocks, either way: raising a cap or a timeout loosens as surely
as lowering one. Since the project's v1 epoch (the newest "apex: project init" or "apex:
lift" commit; a block committed before it, or in it, is the baseline), a commit that
changes one of them and is not ape's own ("apex: …") is the finding config.changed (action
person; D50), and change start, verify and archive refuse (change.provenance) — so a
session cannot loosen its own limits with an Edit. Unguarded: project_name,
development_folder, models, effort, docs, notify.

A person who made the change, or approves it, accepts it: this command commits an empty
"apex: config accepted by <who>" on the base branch (its body lists the guarded blocks),
and every guarded change before it is clean. A later change is judged again. A git revert of the change also clears it.

Only a person accepts (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE set,
which includes a ` + "`!`" + ` line inside Claude Code — with exit 2, code session.person-only,
unless, in the person's own Claude Code session, the person typed "accept the config" in
this turn (E2: ` + "`ape hook pre-tool --help`" + `; --by is then git config user.name, recorded with
the prompt; another --by is session.by-mismatch, exit 1).
Refused on a change branch, and with staged changes in the index.

Exit codes: 0 accepted (prints the commit) · 1 HEAD on a change branch, staged changes, or
git failed · 2 usage, or a model session.`,
		Example: "  ape project accept-config --by diego",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if err := v1PersonAct(cmd, p, personCallOf(cmd, args)); err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			sha, err := p.AcceptConfig(cmd.Context(), personBy(cmd.Context(), by))
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"ok": true, "commit": sha}, func(w io.Writer) {
				fmt.Fprintf(w, "config accepted: %.7s\n", sha)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().StringVar(&by, "by", "", "who accepts (required)")
	return cmd
}
