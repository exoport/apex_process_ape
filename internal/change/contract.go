package change

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// The terminal contract: what the skill writes down about what it did,
// and the only thing ape composes commits from.
//
// It is a VERSIONED INTERFACE the framework owns (apex-maintenance's
// SKILL.md holds the template). A newer framework may add optional
// fields; it may not change what these mean. `make check-framework`
// parses the installed skill's own template through this parser, so a
// framework edit that breaks it fails there rather than silently here,
// after an hour of model time.
//
// Nothing in this file trusts the contract as a description of the
// repository — the reconciliation does that, against git. What it does
// check is that the contract is coherent WITH ITSELF: counts that match
// the goals they count, and an ordering that a halt could actually have
// produced. An incoherent contract is exit 1 with nothing committed,
// because the alternative is composing commits from a document that is
// already known to be wrong about its own contents.

// Outcome values for maintenance_status.
const (
	StatusLanded    = "landed"
	StatusRefused   = "refused"
	StatusEscalated = "escalated"
	StatusHalted    = "halted"
)

// Per-goal status values.
const (
	GoalLanded     = "landed"
	GoalHalted     = "halted"
	GoalNotStarted = "not-started"
)

// Contract is the block apex-maintenance writes to --contract-out.
//
//nolint:tagliatelle // the framework owns these key names; ape reads what it writes
type Contract struct {
	ContractVersion string `yaml:"contract_version"`
	Status          string `yaml:"maintenance_status"`
	GoalsTotal      int    `yaml:"goals_total"`
	GoalsLanded     int    `yaml:"goals_landed"`
	// Route is where escalated work goes; "none" on every other outcome.
	Route string `yaml:"route"`
	// BlockingCondition is why a halted run stopped; "none" otherwise.
	BlockingCondition string `yaml:"blocking_condition"`
	Goals             []Goal `yaml:"goals"`
}

// Goal is one unit of work the skill split the request into, and one
// commit ape composes.
//
//nolint:tagliatelle // the framework owns these key names
type Goal struct {
	Goal   string   `yaml:"goal"`
	Status string   `yaml:"status"`
	Paths  []string `yaml:"paths"`
	// Subject is the commit's subject line, as the skill wrote it.
	Subject string `yaml:"subject"`
	Gates   string `yaml:"gates"`
	// Evidence is the folder the goal's gate output went to, or "none".
	Evidence string `yaml:"evidence"`
	// Triage is the governance-clearance note inside Evidence, or "none".
	Triage           string  `yaml:"triage"`
	Governance       string  `yaml:"governance"`
	Deferred         []Defer `yaml:"deferred"`
	FindingsPatched  int     `yaml:"findings_patched"`
	FindingsDeferred int     `yaml:"findings_deferred"`
}

// Defer is one review finding the run did not fix, carried as structured
// fields so no model-written string has to survive a regex to become
// project data.
type Defer struct {
	Title   string   `yaml:"title"`
	Anchors []string `yaml:"anchors"`
	Owner   string   `yaml:"owner"`
	Trigger string   `yaml:"trigger"`
	// Body may span lines — it is the finding, not a commit field.
	Body string `yaml:"body"`
}

// ErrNoContract reports a contract file that is not there at all. Kept
// distinct because it is the ordinary shape of a dispatch that died
// before it finished, and the message for that is not "this YAML is
// wrong".
var ErrNoContract = errors.New("the skill wrote no contract")

// ReadContract loads and checks the contract at path.
func ReadContract(path string) (*Contract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoContract, path)
		}
		return nil, fmt.Errorf("read the contract: %w", err)
	}
	return ParseContract(data)
}

// ParseContract reads the block, tolerating a ```yaml fence around it.
//
// The skill is told to write plain YAML with no fence. It is a model
// writing a file, and a fenced block is the one deviation that is
// unambiguous to undo — the alternative is failing a run whose work is
// done and whose contract is perfectly readable, over three backticks.
func ParseContract(data []byte) (*Contract, error) {
	var c Contract
	if err := yaml.Unmarshal(stripFence(data), &c); err != nil {
		return nil, fmt.Errorf("the contract is not valid YAML: %w", err)
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

// stripFence removes a single leading ```… / trailing ``` pair.
func stripFence(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if !bytes.HasPrefix(trimmed, []byte("```")) {
		return data
	}
	if i := bytes.IndexByte(trimmed, '\n'); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if j := bytes.LastIndex(trimmed, []byte("```")); j >= 0 {
		trimmed = trimmed[:j]
	}
	return trimmed
}

// check is the contract's coherence with itself.
func (c *Contract) check() error {
	switch c.Status {
	case StatusLanded, StatusRefused, StatusEscalated, StatusHalted:
	case "":
		return errors.New("the contract has no maintenance_status")
	default:
		return fmt.Errorf("maintenance_status is %q, which is not one of landed, refused, "+
			"escalated or halted", c.Status)
	}

	halted := false
	landed := 0
	for i := range c.Goals {
		g := &c.Goals[i]
		switch g.Status {
		case GoalLanded:
			// A landed goal AFTER a halted one cannot have happened: the
			// skill stops at the first halt, and every later goal's gate
			// would have run over the halted goal's partial edits. Reading
			// this as "commit both" would commit work no gate covered.
			if halted {
				return fmt.Errorf("goal %d landed after an earlier goal halted, which the skill "+
					"does not do: every goal after a halt is not-started", i+1)
			}
			landed++
		case GoalHalted:
			halted = true
		case GoalNotStarted:
		case "":
			return fmt.Errorf("goal %d has no status", i+1)
		default:
			return fmt.Errorf("goal %d has status %q, which is not one of landed, halted or "+
				"not-started", i+1, g.Status)
		}
		if g.FindingsDeferred != len(g.Deferred) {
			return fmt.Errorf("goal %d says findings_deferred: %d and carries %d deferred %s: "+
				"ape writes those records, so the count and the list have to be the same thing",
				i+1, g.FindingsDeferred, len(g.Deferred), plural(len(g.Deferred), "finding", "findings"))
		}
	}

	if c.GoalsTotal != len(c.Goals) {
		return fmt.Errorf("goals_total is %d and the contract lists %d %s",
			c.GoalsTotal, len(c.Goals), plural(len(c.Goals), "goal", "goals"))
	}
	if c.GoalsLanded != landed {
		return fmt.Errorf("goals_landed is %d and %d of the listed goals landed",
			c.GoalsLanded, landed)
	}
	if c.Status == StatusLanded && landed != len(c.Goals) {
		return fmt.Errorf("maintenance_status is landed but %d of %d goals did not land",
			len(c.Goals)-landed, len(c.Goals))
	}
	return nil
}

// LandedGoals returns the goals to compose commits for, in order.
func (c *Contract) LandedGoals() []Goal {
	out := make([]Goal, 0, len(c.Goals))
	for i := range c.Goals {
		if c.Goals[i].Status == GoalLanded {
			out = append(out, c.Goals[i])
		}
	}
	return out
}

// None reports the framework's spelling of an absent optional value. The
// contract writes the word rather than leaving the key out, so "none"
// and "" mean the same thing here and both have to be read that way.
func None(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || strings.EqualFold(t, "none")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
