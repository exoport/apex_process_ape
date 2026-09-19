package apecmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/change"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/sprint"
)

// The escalation print: exit 8, the route, and the exact commands for
// it — from the framework's own table, never from ape and never from
// the skill's contract.
//
// The story key is the reason this file does more than substitute
// strings. `rung 2` names a story, that name lands in a printed shell
// command, and it arrives in a string a model wrote. A contract naming
// `12-3; rm -rf ~` must not produce a command carrying it. So the key is
// RESOLVED — matched against the tracker's own rows and the story files
// on disk, and the string that gets printed is the one ape read from
// them, not the one the contract offered.

// routesFor builds the printable block for a contract's route.
func (r *changeRun) routesFor(contract *change.Contract) change.Printed {
	table := change.LoadRouteTable(filepath.Join(r.cfg.Root, framework.ProjectChangeRoutes))
	route := strings.TrimSpace(contract.Route)

	fill := change.RouteFill{
		ChangeID: r.id,
		RecordID: r.opts.fixes,
	}
	if path := r.requestFilePath(); path != "" {
		fill.RequestFile = change.ShellQuote(path)
	}
	if candidate := change.StoryKeyFrom(route); candidate != "" {
		fill.StoryKey = resolveStoryKey(r.cfg, candidate)
	}
	return table.Lookup(route, fill)
}

// requestFilePath is ape's own copy of the request, absolute.
//
// Absolute and ape's own, never the caller's: a conductor's request file
// may be a temp file that is gone by the time anyone runs the printed
// command, and the printed command may be run from anywhere.
//
// Empty when this run has no request — a `--fixes`-only run whose
// record's first line could not stand in for one. The route then prints
// its name and a note rather than a command with an empty path in it.
func (r *changeRun) requestFilePath() string {
	path := filepath.Join(r.dir, changeRequestFile)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// resolveStoryKey matches a contract's story key against the project's
// own records, and returns the string READ FROM THEM.
//
// Three ways to resolve, in order of how much they assume:
//
//   - an exact tracker row;
//   - an exact story file on disk;
//   - a bare key (`12-3`) that prefixes exactly ONE row. One is a
//     resolution; two is an ambiguity, and ape prints no command rather
//     than picking.
//
// Nothing resolves to the candidate itself. That is the whole point: the
// returned key comes from the project's data, so a contract string can
// name a story but can never BE one.
func resolveStoryKey(cfg *apexcfg.Resolved, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	tracker, err := sprint.Load(cfg.Paths.SprintStatus)
	if err == nil && !tracker.Missing {
		for _, row := range tracker.Rows {
			if row.Key == candidate {
				return row.Key
			}
		}
	}
	if key := storyKeyOnDisk(cfg.Paths.Implementation, candidate); key != "" {
		return key
	}
	if err == nil && !tracker.Missing && sprint.IsBareStoryRowKey(candidate) {
		var matched []string
		for _, row := range tracker.Rows {
			if strings.HasPrefix(row.Key, candidate+"_") {
				matched = append(matched, row.Key)
			}
		}
		if len(matched) == 1 {
			return matched[0]
		}
	}
	return ""
}

// storyKeyOnDisk looks for a story file whose name is the key.
func storyKeyOnDisk(implDir, candidate string) string {
	if implDir == "" {
		return ""
	}
	found := ""
	_ = filepath.WalkDir(implDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil //nolint:nilerr // an unreadable subtree is not a resolution failure
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		if sprint.StoryKeyFromPath(path) == candidate {
			found = sprint.StoryKeyFromPath(path)
		}
		return nil
	})
	return found
}

// printRoute writes the escalation block: what the route is, the
// commands in order, and the notes that belong to them.
//
// ape sequences none of these. The block is addressed to whoever ran
// the verb, and it says so by printing rather than doing.
func printRoute(w io.Writer, p change.Printed) {
	if p.Route == "" {
		fmt.Fprintln(w, "escalated without a route")
		return
	}
	fmt.Fprintf(w, "escalated → %s\n", p.Route)
	if p.Summary != "" {
		fmt.Fprintf(w, "  %s\n", p.Summary)
	}
	if len(p.Commands) > 0 {
		fmt.Fprintln(w, "\n  next, in order — ape runs none of these:")
		for _, c := range p.Commands {
			fmt.Fprintf(w, "    %s\n", c)
		}
	}
	if p.Note != "" {
		fmt.Fprintf(w, "\n  no commands: %s\n", p.Note)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(w, "  · %s\n", strings.TrimSpace(n))
	}
}
