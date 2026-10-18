package apecmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/statusboard"
	"github.com/spf13/cobra"
)

// The aboard status tab (D60): ape keeps one tab of the project's board in
// step with `ape status`, best effort, after every v1 command that changes
// what status reports. Only where the project has a board (.aboard/); a
// project without one pays a stat and nothing else.

// boardRefreshVerbs are the v1 leaves that change what `ape status`
// reports, so the board is refreshed after each, whatever its exit (a red
// verify records attempts, a refused start may block). Paths are below the
// root: "change start", not "ape change start".
var boardRefreshVerbs = map[string]bool{
	"project init": true, "project lift": true, "project accept-config": true,
	"milestone new": true, "milestone approve": true, "milestone gate": true, "milestone close": true,
	"decision new": true, "decision accept": true,
	"next add":   true,
	"change new": true, "change approve": true, "change start": true, "change verify": true,
	"change review": true, "change accept": true, "change block": true, "change answer": true,
	"change archive": true, "change abandon": true, "change tool": true,
}

// boardSkipVerbs are the v1 leaves that do not refresh: they write nothing
// status reports (check, diff, codes, status itself — `status --board`
// refreshes on request), or they run inside a model session's every turn
// (the hooks), where the latency is paid on every tool call or stop.
// The plugins verbs change installs and settings, which status does not
// report.
var boardSkipVerbs = map[string]bool{
	"project check": true, "project codes": true, "status": true,
	"change check": true, "change diff": true,
	"hook session-start": true, "hook stop": true, "hook pre-tool": true,
	"plugins sync": true, "plugins status": true,
	// The archive layout move changes where archived changes live, not
	// what status counts or lists.
	"project archive-layout": true,
}

// boardGroups are the top-level commands whose leaves must each be in one
// of the two lists above (TestBoardRefresh_EveryV1LeafIsClassified): a new
// v1 verb has to say whether it refreshes the board.
var boardGroups = []string{"project", "status", "milestone", "change", "decision", "next", "hook", "plugins"}

// installBoardRefresh wraps every leaf in boardRefreshVerbs under cmds.
// Installed over the tree in rootSubcommands, as the unknown-verb guard is,
// so no constructor has to remember it.
func installBoardRefresh(cmds []*cobra.Command) {
	for _, c := range cmds {
		installBoardRefreshDeep(c, c.Name())
	}
}

// installBoardRefreshDeep carries the path down itself: it runs before the
// groups are added to the root, so CommandPath is not yet the full one.
func installBoardRefreshDeep(c *cobra.Command, path string) {
	for _, sub := range c.Commands() {
		installBoardRefreshDeep(sub, path+" "+sub.Name())
	}
	if c.RunE == nil || !boardRefreshVerbs[path] {
		return
	}
	run := c.RunE
	c.RunE = func(cmd *cobra.Command, args []string) error {
		err := run(cmd, args)
		// A usage error wrote nothing.
		if code, _ := ExitCode(err); err == nil || code != ExitUsage {
			refreshBoardAfter(cmd)
		}
		return err
	}
}

// boardPath is a command's path below the root, "change start", once the
// tree is assembled.
func boardPath(c *cobra.Command) string {
	_, rest, _ := strings.Cut(c.CommandPath(), " ")
	return rest
}

// refreshBoardAfter refreshes the status tab after a state-changing
// command: the project its --cwd names (else the one around the working
// directory), only when that project has .aboard/. Never fails, never
// writes stdout: a problem is one line on stderr.
func refreshBoardAfter(cmd *cobra.Command) {
	var f v1Flags
	if fl := cmd.Flags().Lookup("cwd"); fl != nil {
		f.cwd = fl.Value.String()
	}
	p, err := v1Project(&f)
	if err != nil || !statusboard.Present(p.Root) || !p.IsV1() || apexv1.IsV0Project(p.Root) {
		return
	}
	statusboard.Refresh(context.WithoutCancel(cmd.Context()), p.Root, p.Status(), time.Now(), cmd.ErrOrStderr())
}

// refreshBoardNow is `ape status --board`: the tab refreshed from r, which
// the caller has just computed.
func refreshBoardNow(ctx context.Context, p *apexv1.Project, r *apexv1.StatusReport, errOut io.Writer) statusboard.Outcome {
	if !statusboard.Present(p.Root) {
		return statusboard.Outcome{Action: statusboard.ActionNoBoard}
	}
	return statusboard.Refresh(ctx, p.Root, r, time.Now(), errOut)
}

// boardLine says what `ape status --board` did, in one line.
func boardLine(p *apexv1.Project, o statusboard.Outcome) string {
	tab := fmt.Sprintf("tab %q (key %s)", statusboard.TabName, statusboard.TabKey)
	switch o.Action {
	case statusboard.ActionPosted:
		return "Board: " + tab + " posted to the running board at " + o.Where
	case statusboard.ActionWrote:
		return "Board: " + tab + " written to " + o.Where + " (no board running; `ape aboard serve` shows it)"
	case statusboard.ActionUnchanged:
		return "Board: " + tab + " already shows this — nothing written (" + o.Where + ")"
	case statusboard.ActionNoBoard:
		return "Board: none in " + p.Root + " — no .aboard/aboard.json; `ape aboard init` creates one, and ape never does"
	}
	return "Board: refresh skipped — " + o.Detail
}
