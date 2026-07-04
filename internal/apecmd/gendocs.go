package apecmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newGenDocsCmd is a hidden repo-maintenance command that regenerates
// the combined CLI reference (docs/reference/cli.md) from the live cobra
// command tree, so the reference can't drift from the code. Wired to
// `make docs-cli`. Hidden because it is a maintainer tool, not a
// user-facing command — it stays out of `ape --help` and does not affect
// any other command's behaviour. PLAN-9 F4.
func newGenDocsCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:    "gen-docs",
		Short:  "Regenerate docs/reference/cli.md from the command tree (repo maintenance)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var buf bytes.Buffer
			writeCLIMarkdown(c.Root(), &buf)
			if out == "-" {
				_, err := os.Stdout.Write(buf.Bytes())
				return err
			}
			if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil { //nolint:gosec // generated docs are world-readable by design
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "docs/reference/cli.md", "Output file (or - for stdout)")
	return cmd
}

// writeCLIMarkdown renders a single-file Markdown reference covering the
// root command and every visible subcommand, sorted by command path.
// Hand-rolled (no cobra/doc dependency) and deterministic — no
// timestamps — so regeneration is a no-op unless a command actually
// changed. Hidden commands (mcp-bridge, notify, gen-docs) and the
// generated help/completion commands are skipped; the reference is the
// user-facing surface. PLAN-9 F4.
func writeCLIMarkdown(root *cobra.Command, w io.Writer) {
	fmt.Fprint(w, "# ape CLI reference\n\n")
	fmt.Fprint(w, "> Generated from the command tree by `make docs-cli` (which runs the hidden\n"+
		"> `ape gen-docs`). Do not edit by hand — change the command definitions in\n"+
		"> `internal/apecmd/` and regenerate. PLAN-9 F4.\n\n")

	var cmds []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		cmds = append(cmds, c)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].CommandPath() < cmds[j].CommandPath() })

	for _, c := range cmds {
		writeCommandSection(w, c)
	}
}

func writeCommandSection(w io.Writer, c *cobra.Command) {
	fmt.Fprintf(w, "## %s\n\n", c.CommandPath())
	if c.Short != "" {
		fmt.Fprintf(w, "%s\n\n", c.Short)
	}
	fmt.Fprintf(w, "```\n%s\n```\n\n", c.UseLine())
	if long := c.Long; long != "" && long != c.Short {
		fmt.Fprintf(w, "%s\n\n", long)
	}
	if subs := visibleSubcommands(c); len(subs) > 0 {
		fmt.Fprint(w, "Subcommands:\n\n")
		for _, s := range subs {
			fmt.Fprintf(w, "- `%s` — %s\n", s.Name(), s.Short)
		}
		fmt.Fprintln(w)
	}
	if c.Example != "" {
		fmt.Fprintf(w, "Examples:\n\n```\n%s\n```\n\n", c.Example)
	}
	writeFlagTable(w, "Flags", c.NonInheritedFlags())
	writeFlagTable(w, "Global flags", c.InheritedFlags())
}

func visibleSubcommands(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, s := range c.Commands() {
		if s.Hidden || s.Name() == "help" || s.Name() == "completion" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// writeFlagTable emits a Markdown table for the non-hidden flags in fs.
// No-op when fs has no visible flags.
func writeFlagTable(w io.Writer, title string, fs *pflag.FlagSet) {
	var flags []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) {
		if !f.Hidden {
			flags = append(flags, f)
		}
	})
	if len(flags) == 0 {
		return
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	fmt.Fprintf(w, "%s:\n\n", title)
	fmt.Fprint(w, "| Flag | Type | Default | Description |\n")
	fmt.Fprint(w, "| ---- | ---- | ------- | ----------- |\n")
	for _, f := range flags {
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		def := f.DefValue
		if def == "" {
			def = "—"
		}
		// Collapse newlines in usage so the table row stays on one line.
		usage := mdInline(f.Usage)
		fmt.Fprintf(w, "| `%s` | %s | `%s` | %s |\n", name, f.Value.Type(), def, usage)
	}
	fmt.Fprintln(w)
}

// mdInline flattens a multi-line flag usage string into a single
// table-cell-safe line (newlines → spaces, pipes escaped).
func mdInline(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			out = append(out, ' ')
		case '|':
			out = append(out, '\\', '|')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
