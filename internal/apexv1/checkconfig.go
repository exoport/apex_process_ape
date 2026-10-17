package apexv1

import (
	"fmt"
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
