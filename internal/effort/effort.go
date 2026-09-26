// Package effort resolves the reasoning effort ape gives a spawned claude
// session: an explicit override when one is declared, and otherwise the
// framework's per-model table, `_apex/effort-defaults.yaml`.
//
// # Why a table, and why it rides in --settings
//
// One claude process runs more than one model: an Opus session at medium
// spawns Sonnet sub-agents that the framework wants at high. The only
// effort knob ape used to turn, CLAUDE_CODE_EFFORT_LEVEL, is process-wide
// and outranks every per-model setting, so it cannot express that. Claude
// Code's settings can: `modelSettings.<model>.effortLevel` is looked up
// per request by the model making it, and a top-level `effortLevel` covers
// every model without a row. ape writes both into the `--settings` JSON it
// already builds (flagSettings, which outranks user, project and local
// settings; only policy settings outrank it).
//
// Measured on claude 2.1.283 with real sessions, reading the per-line
// `effort` every transcript records for the main session and each
// sub-agent: an Opus parent at low spawned a Sonnet sub-agent that ran at
// high, in one process; with no --model the default model took its
// family's row; the top-level value applied to a model with no row and
// beat the user's own per-model row. The probe had to use values the
// machine's user settings could not produce — a naive probe passed with no
// ape settings at all, because those settings already said the same thing.
//
// # Keys
//
// A family word (`opus`) is accepted as a key but matches only the model
// it currently aliases — `--model claude-opus-5` did NOT take the `opus`
// row. So for each family ape writes the word (it follows the alias as new
// models ship) and every id of that family in ape's price table (older and
// pinned ids). An id newer than this binary that is not the alias target
// gets the fallback.
//
// # Precedence
//
//	step > stage > pipeline `effort:` > --effort   → CLAUDE_CODE_EFFORT_LEVEL (process-wide, explicit)
//	defaults[family(model)] > fallback              → --settings (per model)
//
// With no table file ape keeps its legacy behaviour, LegacyDefault
// process-wide: a new ape on an install that predates the table must
// behave exactly as the old ape did.
package effort

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/cost"
	"gopkg.in/yaml.v3"
)

// FileName is the table's path relative to the apex folder.
const FileName = "effort-defaults.yaml"

// LegacyDefault is the process-wide effort ape applied before the table
// existed, and still applies when a project has none.
const LegacyDefault = "xhigh"

// Levels are the efforts an explicit override may name. `max` goes through
// CLAUDE_CODE_EFFORT_LEVEL, which accepts it.
var Levels = []string{"low", "medium", "high", "xhigh", "max"}

// TableLevels are the efforts the table may name: Claude Code's
// `modelSettings.<model>.effortLevel` enum, which has no `max`.
var TableLevels = []string{"low", "medium", "high", "xhigh"}

// Defaults is the parsed `_apex/effort-defaults.yaml`.
type Defaults struct {
	Version int `json:"version" yaml:"version"`
	// Defaults maps a model family word to its effort.
	Defaults map[string]string `json:"defaults" yaml:"defaults"`
	// Fallback applies to a model that is unknown or whose family is not
	// listed.
	Fallback string `json:"fallback" yaml:"fallback"`
}

// Path returns the table's location for an apex folder.
func Path(apexDir string) string { return filepath.Join(apexDir, FileName) }

var familyWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Load reads and validates the table. A missing file is (nil, nil) — the
// legacy default applies — and an unreadable or invalid one is an error:
// a table ape cannot trust must not be silently replaced by a different
// effort than the framework declared.
func Load(apexDir string) (*Defaults, error) {
	if apexDir == "" {
		return nil, nil //nolint:nilnil // no apex folder: no table, the documented legacy case
	}
	path := Path(apexDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil //nolint:nilnil // an absent table is the documented legacy case, not an error
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var d Defaults
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
}

// Validate checks the version, every level and every family word.
func (d *Defaults) Validate() error {
	if d.Version != 1 {
		return fmt.Errorf("version %d is not supported (this ape reads version 1)", d.Version)
	}
	if err := CheckTableLevel(d.Fallback); err != nil {
		return fmt.Errorf("fallback: %w", err)
	}
	for family, level := range d.Defaults {
		if !familyWord.MatchString(family) {
			return fmt.Errorf("defaults: %q is not a model family word (e.g. opus, sonnet, haiku)", family)
		}
		if err := CheckTableLevel(level); err != nil {
			return fmt.Errorf("defaults.%s: %w", family, err)
		}
	}
	return nil
}

// CheckLevel validates an explicit override (a spec's `effort:` or
// --effort). Empty is "not set" and valid.
func CheckLevel(level string) error {
	if level == "" || slices.Contains(Levels, level) {
		return nil
	}
	return fmt.Errorf("%q is not an effort level (one of %s)", level, strings.Join(Levels, ", "))
}

// CheckTableLevel validates a level the table carries. Required, and never
// `max`, which Claude Code's per-model setting cannot hold.
func CheckTableLevel(level string) error {
	switch {
	case level == "":
		return errors.New("is empty (one of " + strings.Join(TableLevels, ", ") + ")")
	case level == "max":
		return errors.New(`"max" cannot be a per-model default: Claude Code's modelSettings.<model>.effortLevel ` +
			"accepts low, medium, high and xhigh only. An explicit step/stage/pipeline effort or --effort can still say max")
	case !slices.Contains(TableLevels, level):
		return fmt.Errorf("%q is not an effort level (one of %s)", level, strings.Join(TableLevels, ", "))
	}
	return nil
}

// For returns the table's effort for a model, and whether it came from the
// model's family row rather than the fallback. An empty model — no --model,
// so claude's own default — cannot be attributed by ape; claude applies the
// table to whatever it picks, so this reports the fallback with ok=false.
func (d *Defaults) For(model string) (level string, fromFamily bool) {
	canonical, _ := cost.CanonicalModelArg(model)
	if family := cost.ModelFamily(canonical); canonical != "" && family != "" {
		if l, ok := d.Defaults[family]; ok {
			return l, true
		}
	}
	return d.Fallback, false
}

// SettingsKeys returns, per family, the modelSettings keys ape writes: the
// family word and every id of that family ape's price table knows.
func (d *Defaults) SettingsKeys() map[string][]string {
	byFamily := map[string][]string{}
	for family := range d.Defaults {
		byFamily[family] = []string{family}
	}
	for _, id := range cost.KnownModels() {
		family := cost.ModelFamily(id)
		if _, listed := d.Defaults[family]; listed {
			byFamily[family] = append(byFamily[family], id)
		}
	}
	for family := range byFamily {
		sort.Strings(byFamily[family][1:])
	}
	return byFamily
}

// Settings returns the --settings fragment carrying the table: the
// top-level `effortLevel` (the fallback) and `modelSettings`.
func (d *Defaults) Settings() map[string]any {
	models := map[string]any{}
	for family, keys := range d.SettingsKeys() {
		for _, key := range keys {
			models[key] = map[string]any{"effortLevel": d.Defaults[family]}
		}
	}
	return map[string]any{"effortLevel": d.Fallback, "modelSettings": models}
}

// Sources say where a spawn's effort came from, for the run record.
const (
	SourceStep     = "step"
	SourceStage    = "stage"
	SourcePipeline = "pipeline"
	SourceFlag     = "flag"
	// SourceTable is the framework table: per model, so sub-agents on
	// another family get their own row.
	SourceTable = "table"
	// SourceLegacy is LegacyDefault, process-wide, for a project with no
	// table.
	SourceLegacy = "legacy-default"
)

// Plan is what one spawn gets. snake_case: it lands in run records.
//
//nolint:tagliatelle // snake_case is the run-record contract
type Plan struct {
	// Env is the CLAUDE_CODE_EFFORT_LEVEL value, "" when the table governs.
	// Process-wide: it outranks the table for the session and every
	// sub-agent, which is exactly an explicit override's meaning.
	Env string `json:"env,omitempty" yaml:"env,omitempty"`
	// Table is written into --settings when non-nil. It stays present
	// under an explicit override (Env outranks it), so a stage override
	// never has to rebuild the run's settings.
	Table *Defaults `json:"-" yaml:"-"`
	// Source is one of the Source* constants.
	Source string `json:"source" yaml:"source"`
	// Resolved is the effort the launch model runs at. Under the table
	// with no --model it is the fallback's value: ape cannot know at
	// launch which model claude defaults to, so FromFamily is false. A
	// record written after the session ran goes through Observed instead.
	Resolved string `json:"resolved" yaml:"resolved"`
	// FromFamily is true when Resolved came from the model's family row.
	FromFamily bool `json:"from_family,omitempty" yaml:"from_family,omitempty"`
}

// Decide resolves a spawn's effort. explicit is the first non-empty of
// step, stage, pipeline and --effort, and source names which one; table is
// the loaded file (nil when absent); model is the launch model, "" for
// claude's default.
func Decide(explicit, source string, table *Defaults, model string) Plan {
	switch {
	case explicit != "":
		return Plan{Env: explicit, Table: table, Source: source, Resolved: explicit}
	case table != nil:
		level, fromFamily := table.For(model)
		return Plan{Table: table, Source: SourceTable, Resolved: level, FromFamily: fromFamily}
	default:
		return Plan{Env: LegacyDefault, Source: SourceLegacy, Resolved: LegacyDefault}
	}
}

// Unattributed reports whether Resolved is only the table's fallback
// standing in for a model ape did not pick: the table governs and there
// was no --model. Claude applies the row of whatever model it defaults to,
// so a record written before the session ran must not claim Resolved.
func (p Plan) Unattributed(launchModel string) bool {
	return p.Source == SourceTable && p.Table != nil && launchModel == ""
}

// Observed re-resolves an unattributed plan against the model the session's
// own telemetry says it ran on, so a record written after the session says
// what ran rather than the fallback. Any other plan, or an empty ran, is
// returned unchanged.
func (p Plan) Observed(launchModel, ran string) Plan {
	if p.Unattributed(launchModel) && ran != "" {
		p.Resolved, p.FromFamily = p.Table.For(ran)
	}
	return p
}

// EnvEntries returns the environment entry the plan needs, if any.
func (p Plan) EnvEntries() []string {
	if p.Env == "" {
		return nil
	}
	return []string{"CLAUDE_CODE_EFFORT_LEVEL=" + p.Env}
}
