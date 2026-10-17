package apexv1

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The _apex/config.yaml keys that tune how checks run (H10). Each block
// validates as it parses, like limits:, so a value ape cannot use makes
// the config unparseable and Open refuses it rather than guess; a mapping
// key ape does not know is refused the same way, so a typo is not a
// silently ignored setting.

// Default timeouts (H10). A check is one test command; the regression
// command is the project's whole suite, and the gate command a milestone's
// end-to-end proof, so both get longer.
const (
	DefaultRegressionTimeout = 30 * time.Minute
	DefaultGateTimeout       = 30 * time.Minute
)

// Timeouts is timeouts: — Go durations (`20m`, `90s`, `1h30m`), each
// bounding one run of one command:
//
//	timeouts:
//	  check: 20m        # a task or scenario check (default 15m)
//	  regression: 45m   # the regression command (default 30m)
//	  gate: 1h          # a milestone's Gate: command (default 30m)
//
// A --timeout flag on the command, when given, wins over all three.
type Timeouts struct {
	Check      time.Duration
	Regression time.Duration
	Gate       time.Duration
}

// UnmarshalYAML parses and validates the block.
func (t *Timeouts) UnmarshalYAML(n *yaml.Node) error {
	var raw map[string]string
	if err := checkKeys(n, "timeouts", "check", "regression", "gate"); err != nil {
		return err
	}
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("timeouts: %w", err)
	}
	for k, v := range raw {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			return fmt.Errorf("timeouts.%s: %q is not a positive Go duration (e.g. 20m, 90s, 1h30m)", k, v)
		}
		switch k {
		case "check":
			t.Check = d
		case "regression":
			t.Regression = d
		case "gate":
			t.Gate = d
		}
	}
	return nil
}

// For is the timeout a check of kind runs under: the flag's when given,
// else the configured one, else the default.
func (t Timeouts) For(kind string, flag time.Duration) time.Duration {
	if flag > 0 {
		return flag
	}
	switch kind {
	case KindRegression:
		return orDuration(t.Regression, DefaultRegressionTimeout)
	case KindGate:
		return orDuration(t.Gate, DefaultGateTimeout)
	}
	return orDuration(t.Check, DefaultCheckTimeout)
}

func orDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// RegressionConfig is regression: — the project's own test suite (backlog
// 32), as one command or in two tiers (H10):
//
//	regression: make test                                   # both tiers
//	regression: {change: make test, milestone: make test-all}
//
// The change tier runs at a change's start (its baseline), at verify and
// at a catch-up, judged against the change's baseline; the milestone tier
// (the change tier when unset) runs at the milestone gate.
type RegressionConfig struct {
	Change    string
	Milestone string
}

// UnmarshalYAML accepts the scalar form and the mapping.
func (r *RegressionConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*r = RegressionConfig{Change: strings.TrimSpace(n.Value)}
		return nil
	}
	if err := checkKeys(n, "regression", "change", "milestone"); err != nil {
		return err
	}
	var raw map[string]string
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("regression: %w", err)
	}
	*r = RegressionConfig{Change: strings.TrimSpace(raw["change"]), Milestone: strings.TrimSpace(raw["milestone"])}
	if r.Change == "" && r.Milestone != "" {
		return fmt.Errorf("regression.milestone is set without regression.change — the change tier is what a change's baseline and verify judge by")
	}
	return nil
}

// MilestoneCmd is the milestone tier: its own command, else the change
// tier's.
func (r RegressionConfig) MilestoneCmd() string {
	if r.Milestone != "" {
		return r.Milestone
	}
	return r.Change
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkKeys refuses a mapping key outside keys (and a non-mapping node).
func checkKeys(n *yaml.Node, what string, keys ...string) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: is not a mapping (%s)", what, strings.Join(keys, ", "))
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		known := false
		for _, want := range keys {
			known = known || k == want
		}
		if !known {
			return fmt.Errorf("%s.%s: not a key ape knows (%s)", what, k, strings.Join(keys, ", "))
		}
	}
	return nil
}

// DefaultVacuousOutput are test runners' own words for "ran nothing" on a
// zero exit (H11) — each checked against the tool that prints it:
//
//	no tests to run     go test, when -run matches no test of a package:
//	                    `ok  pkg 0.01s [no tests to run]` (and, with -v,
//	                    `testing: warning: no tests to run`), exit 0
//	running 0 tests     cargo test (Rust libtest), when a filter matches
//	                    nothing, per test binary; exit 0
//	Ran 0 tests         Python unittest before 3.12 ("OK", exit 0; 3.12
//	                    exits 5 itself)
//	0 passing           mocha, when --grep or the spec glob matches nothing;
//	                    exit 0
//	collected 0 items   pytest — which exits 5 for it by itself, so this
//	                    catches only a wrapper that turned 5 into 0
//
// Not "no test files": `go test ./...` prints `? pkg [no test files]` for
// every package without tests beside the packages that ran, under a first
// word ("?") no running package's line shares, so it would make an
// ordinary whole-module run vacuous.
var DefaultVacuousOutput = []string{"no tests to run", "running 0 tests", "Ran 0 tests", "0 passing", "collected 0 items"}

// ChecksConfig is checks: (H11).
//
//	checks:
//	  vacuous_output: ["no tests to run", "0 examples"]   # replaces the default list
//	  vacuous_output: []                                   # turns the rule off
//
// Absent, the rule uses DefaultVacuousOutput.
type ChecksConfig struct {
	VacuousOutput []string
	// vacuousSet: vacuous_output: was given (an empty list turns the rule
	// off), as opposed to absent (the default list).
	vacuousSet bool
}

// UnmarshalYAML parses and validates the block.
func (c *ChecksConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "checks", "vacuous_output"); err != nil {
		return err
	}
	var raw map[string][]string
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("checks.vacuous_output: %w — a list of phrases", err)
	}
	phrases, ok := raw["vacuous_output"]
	if !ok {
		return nil
	}
	c.vacuousSet = true
	c.VacuousOutput = []string{}
	for _, ph := range phrases {
		if strings.TrimSpace(ph) == "" {
			return fmt.Errorf("checks.vacuous_output: an empty phrase would match every output")
		}
		c.VacuousOutput = append(c.VacuousOutput, ph)
	}
	return nil
}

// Vacuous is the phrase list in force.
func (c ChecksConfig) Vacuous() []string {
	if c.vacuousSet {
		return c.VacuousOutput
	}
	return DefaultVacuousOutput
}

// Requirement kinds of requires: (H11).
const (
	RequireEnv = "env"
	RequireCmd = "cmd"
)

// Requires is requires: — what a check needs from the machine it runs on
// (H11), name → env|cmd:
//
//	requires:
//	  RADIX_DSN: env    # a check that mentions $RADIX_DSN needs it set
//	  docker: cmd       # a check that runs docker needs it on PATH
//
// A check whose requirement is missing is not run: it records unrun,
// which counts as red and is never green.
type Requires map[string]string

// UnmarshalYAML parses and validates the block.
func (r *Requires) UnmarshalYAML(n *yaml.Node) error {
	var raw map[string]string
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("requires: %w — a mapping NAME: env|cmd", err)
	}
	out := Requires{}
	for name, kind := range raw {
		kind = strings.TrimSpace(kind)
		switch {
		case kind != RequireEnv && kind != RequireCmd:
			return fmt.Errorf("requires.%s: %q is not env or cmd", name, kind)
		case kind == RequireEnv && !envNameRe.MatchString(name):
			return fmt.Errorf("requires.%s: not an environment variable name", name)
		case kind == RequireCmd && (name == "" || strings.ContainsAny(name, "/ \t")):
			return fmt.Errorf("requires.%s: a command is named by its program name, without a path", name)
		}
		out[name] = kind
	}
	*r = out
	return nil
}
