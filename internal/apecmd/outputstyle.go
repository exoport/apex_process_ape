package apecmd

import (
	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/spf13/cobra"
)

// helpOutputStyle is the one wording for the flag, shared by every
// command that spawns a Claude Code session.
const helpOutputStyle = "Output style pinned on the spawned session " +
	"(default \"" + config.DefaultOutputStyle + "\"). " +
	"Pass \"" + config.InheritOutputStyle + "\" to keep whatever style the machine is configured with."

// addOutputStyleFlag registers --output-style on a spawn command.
//
// Every path that launches claude pins the style, because a skill run is
// machine-consumed output rather than a conversation: an output style
// claims precedence over other formatting guidance, and the framework's
// fenced return contracts, guided menus and completion summaries are
// exactly what that precedence would rewrite. The flag exists for the
// operator who wants a style deliberately — see internal/bridge/config
// for why the key has to be written explicitly to have any effect.
func addOutputStyleFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "output-style", "", helpOutputStyle)
}
