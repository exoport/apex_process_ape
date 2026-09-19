package change

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// What the verb PRINTS when the lane escalates, and why it prints
// rather than runs.
//
// Every escalated route is conducted by whoever ran the verb. The
// measured reason is in the framework's own table: the lean-story chain
// costs the same four dispatches rung 3 already gives, it cannot name
// its epic from a raw request, and it skips the injection passes dev
// presupposes. So ape sequences none of it.
//
// The vocabulary is the FRAMEWORK'S. The table lives in
// `_apex/change-routes.yaml`, which is why a route change is not an ape
// release, and why the commands cannot come from the skill's own
// contract: those would be model-written shell that the next model runs
// with nothing guarding its flags.
//
// Nothing here can fail a run. An absent file, an unreadable one, an
// unknown route, an unfillable placeholder — every one of them degrades
// the same way, to the route's name and no commands, and the run still
// exits with the outcome the contract named.

// Placeholders the table may use. A closed set: anything else makes the
// table invalid, because a placeholder ape does not understand would be
// printed literally into a command someone then runs.
const (
	phRequestFile = "{request_file}"
	phStoryKey    = "{story_key}"
	phChangeID    = "{change_id}"
	phRecordID    = "{record_id}"
)

// placeholderRe finds every `{…}` in a command line. `<…>` markers such
// as `--epic <N>` are deliberately NOT placeholders: they are literal,
// and they are left for the conductor to fill.
var placeholderRe = regexp.MustCompile(`\{[^}]*\}`)

// RouteTable is the framework's escalation table.
type RouteTable struct {
	Routes map[string]Route `yaml:"routes"`
}

// Route is one escalation target.
type Route struct {
	Summary  string   `yaml:"summary"`
	Commands []string `yaml:"commands"`
	Notes    []string `yaml:"notes"`
}

// RouteFill is what the placeholders resolve to for one run.
type RouteFill struct {
	// RequestFile is ape's own copy of the request, absolute and
	// shell-quoted. Never the caller's path: a conductor's file can be
	// gone by the time anyone runs the printed line.
	RequestFile string
	// StoryKey is resolved against the tracker, never pasted from the
	// request — it lands in a shell command.
	StoryKey string
	ChangeID string
	RecordID string
}

// Printed is what the verb writes on stdout, and what the envelope
// carries as next_commands.
type Printed struct {
	Route    string
	Summary  string
	Commands []string
	Notes    []string
	// Note explains an empty command list: the table was missing, the
	// route unknown, or a placeholder unfillable. Printing a command
	// with an empty file path would be worse than printing none.
	Note string
}

// LoadRouteTable reads the table, or reports that there is none.
//
// An unreadable or unparseable file is NOT an error to the caller: it
// returns an empty table, and every lookup then degrades to the route's
// name. A run that did its work must not fail over a reference table.
func LoadRouteTable(path string) *RouteTable {
	data, err := os.ReadFile(path)
	if err != nil {
		return &RouteTable{}
	}
	var t RouteTable
	if err := yaml.Unmarshal(data, &t); err != nil {
		return &RouteTable{}
	}
	return &t
}

// Lookup renders one route's commands with the fill applied.
func (t *RouteTable) Lookup(name string, fill RouteFill) Printed {
	out := Printed{Route: strings.TrimSpace(name)}
	if out.Route == "" {
		out.Note = "the contract named no route"
		return out
	}
	route, ok := t.Routes[routeKey(out.Route)]
	if !ok {
		out.Note = "no entry for this route in _apex/change-routes.yaml — " +
			"either the framework predates it or the route is one ape's table does not carry"
		return out
	}
	out.Summary = strings.TrimSpace(route.Summary)
	out.Notes = route.Notes

	for _, cmd := range route.Commands {
		filled, err := fillCommand(cmd, fill)
		if err != nil {
			// One unfillable command invalidates the whole list. Printing
			// the rest would give a conductor a chain with a hole in it,
			// which is worse than printing none and saying why.
			return Printed{
				Route:   out.Route,
				Summary: out.Summary,
				Notes:   route.Notes,
				Note:    err.Error(),
			}
		}
		out.Commands = append(out.Commands, filled)
	}
	return out
}

// routeKey maps a contract's route string onto a table key.
//
// The contract writes `rung 2 <story-key>` for the amendment route, so
// the key is the first token pair with a hyphen: `rung-2`. Everything
// else is already the key.
func routeKey(route string) string {
	fields := strings.Fields(route)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "rung") {
		return "rung-" + fields[1]
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return route
}

// StoryKeyFrom reads the story key out of a `rung 2 <key>` route string.
func StoryKeyFrom(route string) string {
	fields := strings.Fields(route)
	if len(fields) >= 3 && strings.EqualFold(fields[0], "rung") && fields[1] == "2" {
		return fields[2]
	}
	return ""
}

// fillCommand substitutes the closed placeholder set.
func fillCommand(cmd string, fill RouteFill) (string, error) {
	var bad error
	out := placeholderRe.ReplaceAllStringFunc(cmd, func(ph string) string {
		switch ph {
		case phRequestFile:
			if fill.RequestFile == "" {
				bad = fmt.Errorf("this route's commands carry the request in a file, and this run "+
					"has no request to put in one (%s)", ph)
				return ph
			}
			return fill.RequestFile
		case phStoryKey:
			if fill.StoryKey == "" {
				bad = fmt.Errorf("this route's commands name a story, and the key could not be "+
					"resolved against the tracker (%s). It is never pasted from the contract: it "+
					"lands in a shell command", ph)
				return ph
			}
			return fill.StoryKey
		case phChangeID:
			return fill.ChangeID
		case phRecordID:
			return fill.RecordID
		default:
			bad = fmt.Errorf("the route table uses %s, which is not one of the placeholders ape "+
				"fills — the table is invalid until the framework fixes it", ph)
			return ph
		}
	})
	if bad != nil {
		return "", bad
	}
	return out, nil
}

// ShellQuote wraps a path for the single-quoted shell context the
// printed commands are read in.
//
// The printed line is meant to be pasted into a shell. A path with a
// space, a quote or a `$` in it would otherwise split into two
// arguments or expand — and this path comes from a project's own
// output_folder, which ape does not get to choose.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
