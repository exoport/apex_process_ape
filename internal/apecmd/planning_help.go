package apecmd

import (
	"os"
	"regexp"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// planningDiagramRaw is the ASCII swimlanes view of the greenfield
// planning pipeline, with no color escapes. Rows are roughly
// topological depth; columns are agent personas. Cross-column
// dependencies are notated textually (`◉ FOO←BAR,BAZ`) rather than
// drawn — diagonal box-drawing arrows don't survive at 80-col widths
// and would make the diagram fragile against future skill additions.
//
// Source of truth for the dependency edges: docs/explanation/pipelines/
// planning-pipeline.md in apex_process_docs. Keep both in sync when
// the planning skill set changes.
const planningDiagramRaw = `Planning Pipeline                  (arrows: ← reads from)

 analyst    pm           ux           arch      modeler         sm        dev
 ────────  ───────────  ───────────  ────────  ──────────────  ────────  ────────
 ◉ PB
            ◉ CP←PB
                         ◉ CU←CP     ◉ CA←CP   ◉ ES←CP
                         ◉ CW←CU                ◉ DM←ES
                         ◉ CM←CW                ◉ DA←CA,DM,ES
            ◉ IR←CA,DA
            ◉ CE←IR
                                                                ◉ SC←CE
                         ◉ SU←CW,SC             ◉ FM←SC,DM,ES
                                                ◉ SE←FM
                                                ◉ SM←SE
                                                                          ◉ SD←SU,SM

 Lanes      analyst · pm · ux · arch · modeler · sm · dev
 Skill IDs  PB product-brief  CP create-prd  CU create-ux  CA create-arch
            ES event-storming  DM data-map  CW wireframes  CM mockups
            DA data-arch  IR readiness  CE epics  SC story-batch
            SU screen-inject  FM flow-inject  SE event-inject  SM data-inject
            SD story-batch-dev
`

// ANSI color escapes for the colorized variant. Bright magenta /
// bright green map to the 16-color palette every modern terminal
// supports; no 256-color or truecolor escapes so the output remains
// legible on minimal terminals.
const (
	ansiReset   = "\x1b[0m"
	ansiMagenta = "\x1b[95m" // skill IDs (bright magenta)
	ansiGreen   = "\x1b[92m" // agent personas (bright green)
)

// Magenta applies only to skill IDs that name the *action* — the node
// itself, on the left of `←`. Right-of-arrow IDs are parent references
// and stay uncolored so the reader can tell the action and its parents
// apart at a glance.
//
// Two contexts where an ID names an action:
//  1. In the body, right after the `◉ ` node glyph.
//  2. In the legend, where each ID is followed by a space and the
//     skill's lowercase descriptor ("PB product-brief").
//
// Parent IDs in `←CA,DM,ES`-style suffixes deliberately don't match
// either regex.
var (
	planningActionInBodyRe   = regexp.MustCompile(`◉ ([A-Z]{2})`)
	planningActionInLegendRe = regexp.MustCompile(`([A-Z]{2}) ([a-z])`)
	planningAgentRe          = regexp.MustCompile(`\b(?:analyst|pm|ux|arch|modeler|sm|dev)\b`)
)

// renderPlanningDiagram returns the swimlanes view, colorized when
// `colorize` is true. Color application is purely string-substitution
// on the raw template; ANSI escapes have zero visual width so column
// alignment is preserved.
func renderPlanningDiagram(colorize bool) string {
	if !colorize {
		return planningDiagramRaw
	}
	out := planningActionInBodyRe.ReplaceAllString(planningDiagramRaw, "◉ "+ansiMagenta+"$1"+ansiReset)
	out = planningActionInLegendRe.ReplaceAllString(out, ansiMagenta+"$1"+ansiReset+" $2")
	out = planningAgentRe.ReplaceAllString(out, ansiGreen+"$0"+ansiReset)
	return out
}

// shouldColorize reports whether the diagram should be rendered with
// ANSI escapes. Honors the NO_COLOR convention (https://no-color.org)
// and falls back to plain text when stdout is not a terminal (pipes,
// redirects, CI logs).
func shouldColorize() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// newPlanningCmd registers the `ape planning` command. Both
// `ape planning` and `ape help planning` print the swimlanes view —
// the latter via a custom HelpFunc so cobra's default help template
// (Usage / Flags / Global Flags) is bypassed in favor of the diagram.
func newPlanningCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "planning",
		Short: "Show the planning pipeline diagram",
		Long: "Print an ASCII swimlanes view of the greenfield planning pipeline.\n" +
			"Lanes are agent personas; rows are topological depth; `←` lists each\n" +
			"skill's upstream dependencies. Source of truth for edges: the\n" +
			"apex_process_docs planning-pipeline explanation.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Print(renderPlanningDiagram(shouldColorize()))
			return nil
		},
	}
	cmd.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		cmd.Print(renderPlanningDiagram(shouldColorize()))
	})
	return cmd
}
