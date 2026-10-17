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
		Short: "Accept the committed _apex/config.yaml's limits, checks, retries and requires",
		Long: `Four blocks of _apex/config.yaml loosen what ape enforces: limits (attempts, re-plans,
consecutive blocks), checks (vacuous output), requires (unrun checks) and retries. Since the
project's v1 epoch, a commit that changes one of them and is not ape's own ("apex: …") is
the finding config.changed (action person; D50), and change start, verify and archive
refuse (change.provenance) — so a session cannot loosen its own limits with an Edit.

A person who made the change, or approves it, accepts it: this command commits an empty
"apex: config accepted by <who>" on the base branch, and every guarded change before it
is clean. A later change is judged again. A git revert of the change also clears it.

Only a person accepts (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE set,
which includes a ` + "`!`" + ` line inside Claude Code — with exit 2, code session.person-only.
Refused on a change branch, and with staged changes in the index.

Exit codes: 0 accepted (prints the commit) · 1 HEAD on a change branch, staged changes, or
git failed · 2 usage, or a model session.`,
		Example: "  ape project accept-config --by diego",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			sha, err := p.AcceptConfig(cmd.Context(), by)
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
