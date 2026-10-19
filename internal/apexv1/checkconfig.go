package apexv1

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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

// Default timeouts (H10, D58 2.4, D69). Each tier has an idle window — a
// check that makes no progress for it is killed — and a ceiling — a check
// still running at it is killed, progress or not. A check is one test
// command; the regression command is the project's whole suite, and the
// gate command a milestone's end-to-end proof, so both get longer.
const (
	DefaultCheckIdle      = 15 * time.Minute
	DefaultCheckMax       = 2 * time.Hour
	DefaultRegressionIdle = 30 * time.Minute
	DefaultRegressionMax  = 2 * time.Hour
	DefaultGateIdle       = 30 * time.Minute
	DefaultGateMax        = 4 * time.Hour
)

// Hard ceilings (D69): no config value or --timeout goes past them, so a
// typo cannot hold a check — and the worker running it — for days.
const (
	CeilingCheck      = 4 * time.Hour
	CeilingRegression = 8 * time.Hour
	CeilingGate       = 12 * time.Hour
)

// TierTimeout bounds one run of one command: Idle is the longest it may go
// without progress (new output bytes, or CPU time used by its process
// group), Max the longest it may run at all. Zero is unset: the default.
type TierTimeout struct {
	Idle time.Duration
	Max  time.Duration
	// tier names the config tier, and flag a max the --timeout flag set:
	// what a timed-out check's detail cites.
	tier string
	flag bool
}

// key is the setting a timed-out check's detail cites.
func (t TierTimeout) key(which string) string {
	if which == "max" && t.flag {
		return "--timeout"
	}
	tier := t.tier
	if tier == "" {
		tier = "check"
	}
	return "timeouts." + tier + "." + which
}

// Timeouts is timeouts: — per tier, a Go duration (`20m`, `90s`,
// `1h30m`), which is the ceiling (max), or {idle, max}:
//
//	timeouts:
//	  check: 20m                     # max 20m, idle the default (15m)
//	  regression: {idle: 10m, max: 1h}
//	  gate: {idle: 3m}               # max the default (4h)
//
// Defaults: check idle 15m / max 2h, regression 30m / 2h, gate 30m / 4h.
// Any positive duration is accepted, with no floor; an idle above its
// tier's max, or a max above ape's hard ceiling for the tier (check 4h,
// regression 8h, gate 12h), makes the config unparseable. A scalar's idle
// is the tier default clamped to that max. A --timeout flag on the
// command, when given, is the max of every tier (still capped by the
// ceiling).
type Timeouts struct {
	Check      TierTimeout
	Regression TierTimeout
	Gate       TierTimeout
}

// UnmarshalYAML parses and validates the block.
func (t *Timeouts) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "timeouts", "check", "regression", "gate"); err != nil {
		return err
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		tt, err := parseTier(k, v)
		if err != nil {
			return err
		}
		switch k {
		case "check":
			t.Check = tt
		case "regression":
			t.Regression = tt
		case "gate":
			t.Gate = tt
		}
	}
	return nil
}

// parseTier reads one tier: a scalar max, or {idle, max}.
func parseTier(tier string, n *yaml.Node) (TierTimeout, error) {
	var tt TierTimeout
	dur := func(key, v string) (time.Duration, error) {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("timeouts.%s: %q is not a positive Go duration (e.g. 20m, 90s, 1h30m)", key, v)
		}
		return d, nil
	}
	if n.Kind == yaml.ScalarNode {
		d, err := dur(tier, n.Value)
		if err != nil {
			return tt, err
		}
		tt.Max = d
	} else {
		if err := checkKeys(n, "timeouts."+tier, "idle", "max"); err != nil {
			return tt, err
		}
		var raw map[string]string
		if err := n.Decode(&raw); err != nil {
			return tt, fmt.Errorf("timeouts.%s: %w — a duration, or {idle: <duration>, max: <duration>}", tier, err)
		}
		for k, v := range raw {
			d, err := dur(tier+"."+k, v)
			if err != nil {
				return tt, err
			}
			if k == "idle" {
				tt.Idle = d
			} else {
				tt.Max = d
			}
		}
	}
	def, ceiling := tierDefaults(tier)
	if tt.Max > ceiling {
		return tt, fmt.Errorf("timeouts.%s: max %s is above ape's hard ceiling for %s checks (%s)", tier, tt.Max, tier, ceiling)
	}
	if max := orDuration(tt.Max, def.Max); tt.Idle > max {
		return tt, fmt.Errorf("timeouts.%s: idle %s is above its max %s — a check is killed at max first, so this idle never applies", tier, tt.Idle, max)
	}
	return tt, nil
}

// tierDefaults is a tier's defaults and its hard ceiling, by config key.
func tierDefaults(tier string) (TierTimeout, time.Duration) {
	switch tier {
	case "regression":
		return TierTimeout{Idle: DefaultRegressionIdle, Max: DefaultRegressionMax}, CeilingRegression
	case "gate":
		return TierTimeout{Idle: DefaultGateIdle, Max: DefaultGateMax}, CeilingGate
	}
	return TierTimeout{Idle: DefaultCheckIdle, Max: DefaultCheckMax}, CeilingCheck
}

// tierOf is the config tier a check of kind runs under.
func tierOf(kind string) string {
	switch kind {
	case KindRegression:
		return "regression"
	case KindGate:
		return "gate"
	}
	return "check"
}

// Limits is the idle window and ceiling a check of kind runs under: the
// configured ones, else the defaults; flag, when set, is the max (capped
// by the tier's hard ceiling). Idle never exceeds max.
func (t Timeouts) Limits(kind string, flag time.Duration) TierTimeout {
	tier := tierOf(kind)
	def, ceiling := tierDefaults(tier)
	conf := t.Check
	switch tier {
	case "regression":
		conf = t.Regression
	case "gate":
		conf = t.Gate
	}
	out := TierTimeout{Idle: orDuration(conf.Idle, def.Idle), Max: orDuration(conf.Max, def.Max), tier: tier}
	if flag > 0 {
		out.Max, out.flag = min(flag, ceiling), true
	}
	out.Idle = min(out.Idle, out.Max)
	return out
}

// For is the ceiling a check of kind runs under (Limits' max).
func (t Timeouts) For(kind string, flag time.Duration) time.Duration {
	return t.Limits(kind, flag).Max
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

// MaxRetries bounds retries:: a check that needs more is broken, not
// flaky.
const MaxRetries = 5

// Retries is retries: (H12) — how many times a red check runs again
// before it counts as red. A pass on a retry is green and recorded
// flaky: true, which `ape status` lists and review sees. Default 0.
// Vacuous and unrun results are not retried: running nothing again runs
// nothing.
type Retries int

// UnmarshalYAML validates the count.
func (r *Retries) UnmarshalYAML(n *yaml.Node) error {
	var v int
	if err := n.Decode(&v); err != nil || v < 0 || v > MaxRetries {
		return fmt.Errorf("retries: %q is not a whole number from 0 to %d", n.Value, MaxRetries)
	}
	*r = Retries(v)
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

// DocsConfig is docs: (H21, D45) — where the project keeps the documents a
// user-visible change must update, both optional, project-relative:
//
//	docs:
//	  changelog: CHANGELOG.md
//	  user_docs: docs/
//	  tested: [docs/examples/]   # docs the project executes (Q87)
//
// `ape project check` reports a configured path that does not exist
// (docs.missing), and `ape status --json` carries the block as "docs" so
// skills read it: apex-change adds an update task for a user-visible
// change, apex-review checks it. tested lists the docs the project runs
// (doc-tests, tested examples): a change touching one is never docs-only,
// so verify runs its regression command (Q87).
type DocsConfig struct {
	Changelog string   `json:"changelog,omitempty"`
	UserDocs  string   `json:"user_docs,omitempty"`
	Tested    []string `json:"tested,omitempty"`
}

// UnmarshalYAML parses and validates the block.
func (d *DocsConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "docs", "changelog", "user_docs", "tested"); err != nil {
		return err
	}
	var raw struct {
		Changelog string   `yaml:"changelog"`
		UserDocs  string   `yaml:"user_docs"`
		Tested    []string `yaml:"tested"`
	}
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("docs: %w", err)
	}
	for k, v := range map[string]string{"changelog": raw.Changelog, "user_docs": raw.UserDocs} {
		v = strings.TrimSpace(v)
		if v != "" && (filepath.IsAbs(v) || strings.HasPrefix(v, "/") || v == ".." || strings.HasPrefix(filepath.ToSlash(filepath.Clean(v)), "../")) {
			return fmt.Errorf("docs.%s: %q is not a path inside the project — write it relative to the project root", k, v)
		}
		switch k {
		case "changelog":
			d.Changelog = v
		case "user_docs":
			d.UserDocs = v
		}
	}
	// tested is validated as release.docs is: relative paths inside the
	// project, none twice; [] is none.
	seen := map[string]bool{}
	for _, t := range raw.Tested {
		c := path.Clean(strings.TrimSpace(t))
		switch {
		case strings.TrimSpace(t) == "":
			return errors.New("docs.tested: an empty path")
		case path.IsAbs(c) || filepath.IsAbs(t) || c == ".." || strings.HasPrefix(c, "../"):
			return fmt.Errorf("docs.tested: %q is not a relative path inside the project", t)
		case seen[c]:
			return fmt.Errorf("docs.tested: %q is listed twice", t)
		}
		seen[c] = true
		d.Tested = append(d.Tested, strings.TrimSpace(t))
	}
	return nil
}

// modelsFindings reports a models: entry that can do nothing (Q45): a key
// that names no skill ape could launch with it — neither one of the
// defaults init writes (modelDefaultSkills), a project or user skill, nor
// a skill tools: or human_review names, whose sessions read models.<skill>
// too — and an entry field other than model and effort. models: is read
// leniently, so each is otherwise a silently ignored setting: the
// delegate probe's `models: {effort: high}` pinned a skill named "effort".
func (p *Project) modelsFindings() []Finding {
	data, err := os.ReadFile(ConfigPath(p.Root))
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	models := topLevelModels(&doc)
	if models == nil || models.Kind != yaml.MappingNode {
		return nil
	}
	known := map[string]bool{}
	for _, s := range modelDefaultSkills() {
		known[s] = true
	}
	if cfg, err := LoadConfig(p.Root); err == nil {
		for _, slot := range ToolSlots {
			for _, t := range cfg.Tools.Slot(slot) {
				known[t.Skill] = true
			}
		}
		if cfg.HumanReview != nil {
			known[cfg.HumanReview.Skill()] = true
		}
	}
	var out []Finding
	for i := 0; i+1 < len(models.Content); i += 2 {
		k, v := models.Content[i], models.Content[i+1]
		skill := k.Value
		switch _, _, installed := p.localSkill(skill); {
		case skill == "model" || skill == "effort":
			fix := "a skill's " + skill + " goes inside its entry: models.<skill>: {model: sonnet, effort: high}"
			if skill == "effort" {
				fix += "; the default effort of every session is the top-level effort:"
			}
			out = append(out, Finding{File: configYAML, Line: k.Line, Message: "models." + skill + " is not a skill — " + fix})
		case skill == OperatorSkill:
			// Its own notice (models.operator-row, Q96): read as
			// apex-loop's before seq-07, a leftover after.
		case !known[skill] && !installed:
			out = append(out, Finding{File: configYAML, Line: k.Line, Message: "models." + skill + " names no skill installed in the project (.claude/skills/" + skill +
				"/SKILL.md) nor one of ape's defaults (" + strings.Join(modelDefaultSkills(), ", ") + "), so it pins nothing — fix the skill name, or drop the entry"})
		}
		if v.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(v.Content); j += 2 {
			if f := v.Content[j]; f.Value != "model" && f.Value != "effort" {
				out = append(out, Finding{File: configYAML, Line: f.Line, Message: "models." + skill + "." + f.Value +
					" is not a field of a models entry, so it does nothing — an entry is a model name or {model, effort}: fix or drop " + f.Value})
			}
		}
	}
	return out
}
