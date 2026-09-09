package apecmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/outputstyles"
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

// outputStyleFlagSet reports whether the operator actually typed
// --output-style.
//
// The empty string is NOT a usable sentinel for "unset" once a project
// can declare styles of its own: `--output-style Default` and no flag at
// all both leave the variable empty after resolveOutputStyle folds them
// together, and those two have to rank differently against a project's
// declaration. Cobra's Changed is the only thing that distinguishes an
// explicit choice from a default — without it a framework file would
// silently outrank the operator, which inverts the precedence.
func outputStyleFlagSet(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("output-style")
	return f != nil && f.Changed
}

// warnUnknownOutputStyleFlag echoes a typed --output-style value that
// names no built-in.
//
// The third of the three surfaces a style can be declared on, and the
// only one with a human watching at the moment it is used — the other
// two are checked-in files that `ape doctor` reports on. ape folds a
// built-in's case before writing the key, so a casing slip is no longer
// a failure; what remains is a name that resolves to nothing, and Claude
// Code ignores those in silence. ape cannot tell a typo from a custom
// style the machine has installed, so this states what will happen
// rather than convicting the value.
func warnUnknownOutputStyleFlag(cmd *cobra.Command, style string) {
	if !outputStyleFlagSet(cmd) {
		return
	}
	if note := unknownOutputStyleNote(style); note != "" {
		fmt.Fprintln(os.Stderr, "⚠ "+note)
	}
}

// unknownOutputStyleNote returns what to say about a style name, or ""
// when there is nothing to say. Split out from the printing so the
// wording is testable without capturing stderr.
func unknownOutputStyleNote(style string) string {
	trimmed := strings.TrimSpace(style)
	if trimmed == "" || strings.EqualFold(trimmed, config.InheritOutputStyle) {
		return ""
	}
	if _, builtin := config.BuiltinOutputStyleSpelling(trimmed); builtin {
		return ""
	}
	return fmt.Sprintf(
		"--output-style %q names no built-in style (%s). It is passed through unchanged: it applies only if "+
			"this machine has a custom style by that name, and otherwise Claude Code ignores it silently and the "+
			"session runs %s.",
		style, strings.Join(config.BuiltinOutputStyles(), ", "), config.DefaultOutputStyle)
}

// resolveSkillOutputStyle picks the style for a single-skill dispatch
// (`ape task`) under the documented precedence:
//
//	--output-style flag  >  _apex/output-styles.csv  >  DefaultOutputStyle
//
// A missing table, an unenrolled skill, and an unreadable table all
// resolve to the pinned default, which is what every dispatch did before
// the table existed. warn receives anything the operator should see: a
// table that could not be read at all, and rows the loader distrusted.
//
// An unreadable table is reported and stepped over rather than failing
// the dispatch. The table expresses a preference about how a session
// narrates itself, not a contract about what it must produce — unlike
// commit-owners, where refusing to run is the honest response to a file
// that cannot be parsed, because there the file IS the assertion.
func resolveSkillOutputStyle(projectRoot, skill, flagStyle string, flagSet bool, warn func(string)) string {
	if flagSet {
		return flagStyle
	}
	tbl, err := outputstyles.Load(projectRoot)
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("output styles: %v — every skill takes %s", err, config.DefaultOutputStyle))
		}
		return flagStyle
	}
	if warn != nil {
		for _, w := range tbl.Warnings {
			warn("output styles: " + w)
		}
	}
	if style, ok := tbl.Style(skill); ok {
		return style
	}
	return flagStyle
}
