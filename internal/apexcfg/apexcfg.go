// Package apexcfg resolves a project's `_apex/config.yaml` (plus the
// gitignored `_apex/config.local.yaml` overlay) into the folder and name
// variables every APEX framework skill reads as its first act.
//
// Before this package existed, no Go file in the tree read those
// variables at all: `findADRDir()` probed a hardcoded `development/adrs`
// while a real project sets `governance_folder: development/governance`,
// so `ape adr list` reported "no ADR directory found" against 64 ADRs on
// disk. Every validator, verifier and projection in PLAN-25 resolves its
// paths through here — a checker that cannot see the corpus reports it
// clean, which is the exact failure it was built to catch.
package apexcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// On-disk layout constants. The directory name is fixed by the framework;
// `apex_folder` inside the file names the same directory and is carried
// for skills that interpolate it, not used to find the file.
const (
	DirName   = "_apex"
	BaseFile  = "config.yaml"
	LocalFile = "config.local.yaml"
)

// Extension ids the framework recognises in the `extensions` array, and
// the derived `ext_*` flags they set.
const (
	ExtADRs         = "ext-adrs"
	ExtPatterns     = "ext-patterns"
	ExtCapabilities = "ext-capabilities"
	ExtFeatures     = "ext-features"
)

// Date and timestamp layouts. The framework is explicit that both are
// system LOCAL wall-clock, never UTC unless the host itself is: skills
// write these into `updated_at` fields that must not appear to move
// backwards against a previous local-clock write.
const (
	DateLayout      = "2006-01-02"
	TimestampLayout = "20060102150405"
)

// Config is the framework's On Activation variables, in the order that
// block lists them. Field names mirror the YAML keys exactly so a reader
// can diff this against any SKILL.md.
//
// Seventeen of them are the canonical set the block has always resolved.
// The last two — model_profile and evidence_folder — are declared
// OPTIONAL variables (PLAN-64 7.1, PLAN-63 § `ape` requests item 1): each
// ships with a documented default that every framework consumer applies
// when the resolver omits it. That is why neither is defaulted here. See
// OverlayKeys.
type Config struct {
	ConfigSchemaVersion      string   `json:"config_schema_version"      yaml:"config_schema_version"`
	ProjectName              string   `json:"project_name"               yaml:"project_name"`
	Extensions               []string `json:"extensions"                 yaml:"extensions"`
	UserName                 string   `json:"user_name"                  yaml:"user_name"`
	CommunicationLanguage    string   `json:"communication_language"     yaml:"communication_language"`
	DocumentOutputLanguage   string   `json:"document_output_language"   yaml:"document_output_language"`
	UserSkillLevel           string   `json:"user_skill_level"           yaml:"user_skill_level"`
	ApexFolder               string   `json:"apex_folder"                yaml:"apex_folder"`
	OutputFolder             string   `json:"output_folder"              yaml:"output_folder"`
	DevelopmentFolder        string   `json:"development_folder"         yaml:"development_folder"`
	DocsFolder               string   `json:"docs_folder"                yaml:"docs_folder"`
	ImplementationFolder     string   `json:"implementation_folder"      yaml:"implementation_folder"`
	PlanningFolder           string   `json:"planning_folder"            yaml:"planning_folder"`
	GovernanceRepositoryPath string   `json:"governance_repository_path" yaml:"governance_repository_path"`
	GovernanceFolder         string   `json:"governance_folder"          yaml:"governance_folder"`
	GovernanceStaleness      string   `json:"governance_staleness"       yaml:"governance_staleness"`
	FunctionalityFolder      string   `json:"functionality_folder"       yaml:"functionality_folder"`

	// ModelProfile and EvidenceFolder are the framework's two declared
	// OPTIONAL variables. Both are emitted exactly as the project wrote
	// them and are EMPTY when the project never declared one — `ape` does
	// not supply a default for either.
	//
	// That is deliberate, not an omission. `model_profile` defaults to
	// `strong` and acts as a ceiling (it can force `full`, never force
	// `lean`), and `evidence_folder` resolves through a three-step
	// fallback (the key when present, else an existing `evidence/` under
	// {governance_folder}, else `evidence/`). Both defaults belong to the
	// framework, which applies them when this resolver omits the key;
	// re-deriving either here would put a second source of truth behind a
	// key whose whole purpose is that the framework resolves it. Note the
	// consequence: no Paths entry is derived for evidence_folder.
	ModelProfile   string `json:"model_profile,omitempty"   yaml:"model_profile,omitempty"`
	EvidenceFolder string `json:"evidence_folder,omitempty" yaml:"evidence_folder,omitempty"`
}

// Ext carries the four booleans the framework derives from `extensions`.
// Emitted rather than left implicit because every extension-gated check
// in PLAN-25 reads them, and a silently-false flag turns a required-key
// check into a no-op.
type Ext struct {
	ADRs         bool `json:"ext_adrs"         yaml:"ext_adrs"`
	Patterns     bool `json:"ext_patterns"     yaml:"ext_patterns"`
	Capabilities bool `json:"ext_capabilities" yaml:"ext_capabilities"`
	Features     bool `json:"ext_features"     yaml:"ext_features"`
}

// Paths are the absolute locations the folder variables denote. Every
// other PLAN-25 package takes these rather than re-deriving them, so
// there is exactly one place that knows a feature record lives under
// `{functionality_folder}/features/`.
type Paths struct {
	Apex           string `json:"apex"            yaml:"apex"`
	Output         string `json:"output"          yaml:"output"`
	Development    string `json:"development"     yaml:"development"`
	Docs           string `json:"docs"            yaml:"docs"`
	Implementation string `json:"implementation"  yaml:"implementation"`
	Planning       string `json:"planning"        yaml:"planning"`
	Governance     string `json:"governance"      yaml:"governance"`
	Functionality  string `json:"functionality"   yaml:"functionality"`
	ADRs           string `json:"adrs"            yaml:"adrs"`
	Patterns       string `json:"patterns"        yaml:"patterns"`
	Features       string `json:"features"        yaml:"features"`
	Capabilities   string `json:"capabilities"    yaml:"capabilities"`
	TeamMemory     string `json:"team_memory"     yaml:"team_memory"`
	SprintStatus   string `json:"sprint_status"   yaml:"sprint_status"`
	Deferred       string `json:"deferred"        yaml:"deferred"`
	DeferredLegacy string `json:"deferred_legacy" yaml:"deferred_legacy"`
}

// Resolved is the full payload `ape config resolve` emits and every
// other command consumes.
type Resolved struct {
	Root                string `json:"root"                  yaml:"root"`
	ConfigPath          string `json:"config_path"           yaml:"config_path"`
	LocalPath           string `json:"local_path,omitempty"  yaml:"local_path,omitempty"`
	LocalOverlayApplied bool   `json:"local_overlay_applied" yaml:"local_overlay_applied"`
	// OverlaidKeys names the keys config.local.yaml actually replaced, so
	// a surprising resolution is attributable without diffing two files.
	OverlaidKeys []string `json:"overlaid_keys,omitempty" yaml:"overlaid_keys,omitempty"`
	Config       `json:",inline"                 yaml:",inline"`
	Ext          Ext    `json:"ext"                     yaml:"ext"`
	Paths        Paths  `json:"paths"                   yaml:"paths"`
	Date         string `json:"date"                    yaml:"date"`
	Timestamp    string `json:"timestamp"               yaml:"timestamp"`
}

// ErrNotFound is returned when no `_apex/config.yaml` exists in the
// start directory or any parent. Callers map it to exit 4.
var ErrNotFound = errors.New("no _apex/config.yaml found in this directory or any parent")

// MsgImplementationFolderUnset is the single wording for "the project did
// not configure implementation_folder".
//
// It reaches a user from five call sites across three packages — story
// verification, the migration status, two doctor rows and the story
// command's preflight — and they have to agree, because a person who sees
// it in one place and searches for it should find the others. Defined here
// because this package owns the variable it names.
const MsgImplementationFolderUnset = "implementation_folder is not configured"

// MalformedError reports a config file that exists but does not parse.
// This is deliberately fatal rather than a fall-back to base values: the
// resolution is the first act of every skill, so one typo'd override
// would otherwise run a whole pipeline against folders nobody chose.
type MalformedError struct {
	Path string
	Err  error
}

func (e *MalformedError) Error() string {
	return fmt.Sprintf("%s is not valid YAML: %v", e.Path, e.Err)
}

func (e *MalformedError) Unwrap() error { return e.Err }

// Find walks up from start looking for `<dir>/_apex/config.yaml` and
// returns the project root that holds it. The walk stops at the
// filesystem root; a nil error guarantees the config file exists.
func Find(start string) (root string, err error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", start, err)
	}
	for dir := abs; ; {
		if _, statErr := os.Stat(filepath.Join(dir, DirName, BaseFile)); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// filepath.Dir is its own fixed point at the volume root, on
			// every platform — this is the loop's only exit.
			return "", ErrNotFound
		}
		dir = parent
	}
}

// Clock supplies the wall-clock reading for date/timestamp. Tests inject
// a fixed one; production passes nil for time.Now.
type Clock func() time.Time

// Resolve finds the project root at or above start, reads the base
// config, applies the local overlay key-wise, and derives the flags and
// paths. now may be nil.
func Resolve(start string, now Clock) (*Resolved, error) {
	root, err := Find(start)
	if err != nil {
		return nil, err
	}
	return ResolveAt(root, now)
}

// ResolveAt is Resolve with the project root already known — no walk.
func ResolveAt(root string, now Clock) (*Resolved, error) {
	if now == nil {
		now = time.Now
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", root, err)
	}
	basePath := filepath.Join(abs, DirName, BaseFile)
	cfg, err := readBase(basePath)
	if err != nil {
		return nil, err
	}
	res := &Resolved{
		Root:       abs,
		ConfigPath: basePath,
		Config:     *cfg,
	}
	localPath := filepath.Join(abs, DirName, LocalFile)
	overlaid, applied, err := applyLocal(&res.Config, localPath)
	if err != nil {
		return nil, err
	}
	if applied {
		res.LocalPath = localPath
		res.LocalOverlayApplied = true
		res.OverlaidKeys = overlaid
	}
	res.Ext = deriveExt(res.Extensions)
	res.Paths = derivePaths(abs, &res.Config)
	stamp := now()
	res.Date = stamp.Format(DateLayout)
	res.Timestamp = stamp.Format(TimestampLayout)
	return res, nil
}

func readBase(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, &MalformedError{Path: path, Err: err}
	}
	return &cfg, nil
}

// applyLocal overlays the present keys of config.local.yaml onto cfg.
// An absent file is a normal outcome (applied=false, nil error); a
// present-but-malformed file is a MalformedError.
//
// The overlay is key-wise and explicit rather than reflective: a key
// missing from the local file must leave the base value alone, which a
// decode-into-the-same-struct would not do (it would zero every absent
// field, silently blanking `governance_repository_path` and friends).
func applyLocal(cfg *Config, path string) (overlaid []string, applied bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	raw := map[string]yaml.Node{}
	if unmarshalErr := yaml.Unmarshal(data, &raw); unmarshalErr != nil {
		return nil, false, &MalformedError{Path: path, Err: unmarshalErr}
	}
	for _, key := range OverlayKeys() {
		node, ok := raw[key]
		if !ok {
			continue
		}
		if decodeErr := decodeInto(cfg, key, node); decodeErr != nil {
			return nil, false, &MalformedError{
				Path: path,
				Err:  fmt.Errorf("key %q: %w", key, decodeErr),
			}
		}
		overlaid = append(overlaid, key)
	}
	return overlaid, true, nil
}

// OverlayKeys is the ordered list of keys config.local.yaml may replace:
// the seventeen canonical variables the framework's On Activation block
// resolves, plus its two declared optional ones.
//
// The loop in applyLocal looks up ONLY these keys and silently skips
// anything else, so a key absent from this list cannot be overridden in
// config.local.yaml and cannot be emitted by `ape config resolve` — which
// is why adding a framework variable means adding it here, not only to
// Config. (The `unknown overlay key` error decodeInto returns is
// unreachable from this path by construction; it guards a direct call.)
func OverlayKeys() []string {
	return []string{
		"config_schema_version",
		"project_name",
		"extensions",
		"user_name",
		"communication_language",
		"document_output_language",
		"user_skill_level",
		"apex_folder",
		"output_folder",
		"development_folder",
		"docs_folder",
		"implementation_folder",
		"planning_folder",
		"governance_repository_path",
		"governance_folder",
		"governance_staleness",
		"functionality_folder",
		"model_profile",
		"evidence_folder",
	}
}

// OptionalKeys are the OverlayKeys a config template need not declare.
//
// The framework's canonical seventeen are mandatory: every one of them
// names a folder or a name a skill reads, and a template missing one
// resolves it as empty, which sends a writer to the wrong directory. The
// framework's *declared optional* variables are different by design —
// each ships with a documented default its consumers apply when the
// resolver omits the key — so a template that never mentions one is
// correct, not drifted.
//
// The contract test reads this: a template key `ape` does not resolve is
// still a failure in both directions, but an optional key absent from a
// template is not.
func OptionalKeys() []string {
	return []string{"model_profile", "evidence_folder"}
}

// IsOptionalKey reports whether key is one of OptionalKeys.
func IsOptionalKey(key string) bool {
	return slices.Contains(OptionalKeys(), key)
}

// decodeInto writes one overlay key into cfg. The switch is exhaustive
// over OverlayKeys by construction — the test asserts every key is
// handled, so a new variable cannot be added to one list and forgotten
// in the other.
func decodeInto(cfg *Config, key string, node yaml.Node) error {
	targets := map[string]any{
		"config_schema_version":      &cfg.ConfigSchemaVersion,
		"project_name":               &cfg.ProjectName,
		"extensions":                 &cfg.Extensions,
		"user_name":                  &cfg.UserName,
		"communication_language":     &cfg.CommunicationLanguage,
		"document_output_language":   &cfg.DocumentOutputLanguage,
		"user_skill_level":           &cfg.UserSkillLevel,
		"apex_folder":                &cfg.ApexFolder,
		"output_folder":              &cfg.OutputFolder,
		"development_folder":         &cfg.DevelopmentFolder,
		"docs_folder":                &cfg.DocsFolder,
		"implementation_folder":      &cfg.ImplementationFolder,
		"planning_folder":            &cfg.PlanningFolder,
		"governance_repository_path": &cfg.GovernanceRepositoryPath,
		"governance_folder":          &cfg.GovernanceFolder,
		"governance_staleness":       &cfg.GovernanceStaleness,
		"functionality_folder":       &cfg.FunctionalityFolder,
		"model_profile":              &cfg.ModelProfile,
		"evidence_folder":            &cfg.EvidenceFolder,
	}
	target, ok := targets[key]
	if !ok {
		return fmt.Errorf("unknown overlay key %q", key)
	}
	return node.Decode(target)
}

func deriveExt(extensions []string) Ext {
	var e Ext
	for _, x := range extensions {
		switch x {
		case ExtADRs:
			e.ADRs = true
		case ExtPatterns:
			e.Patterns = true
		case ExtCapabilities:
			e.Capabilities = true
		case ExtFeatures:
			e.Features = true
		}
	}
	return e
}

// join resolves a configured folder value against the project root. An
// empty value yields an empty result rather than the root itself, so a
// caller can tell "not configured" from "configured as the root".
func join(root, rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

func derivePaths(root string, cfg *Config) Paths {
	p := Paths{
		Apex:           join(root, cfg.ApexFolder),
		Output:         join(root, cfg.OutputFolder),
		Development:    join(root, cfg.DevelopmentFolder),
		Docs:           join(root, cfg.DocsFolder),
		Implementation: join(root, cfg.ImplementationFolder),
		Planning:       join(root, cfg.PlanningFolder),
		Governance:     join(root, cfg.GovernanceFolder),
		Functionality:  join(root, cfg.FunctionalityFolder),
	}
	if p.Apex == "" {
		p.Apex = filepath.Join(root, DirName)
	}
	if p.Governance != "" {
		p.ADRs = filepath.Join(p.Governance, "adrs")
		p.Patterns = filepath.Join(p.Governance, "patterns")
	}
	if p.Functionality != "" {
		p.Features = filepath.Join(p.Functionality, "features")
		p.Capabilities = filepath.Join(p.Functionality, "capabilities")
	}
	if p.Development != "" {
		p.TeamMemory = filepath.Join(p.Development, "team-memory.md")
		// The deferred store sits under development_folder, NOT under
		// implementation_folder: ten skills glob
		// `{implementation_folder}/**/*.md` across 17 sites, and 227
		// record files under that folder would feed every one of them.
		p.Deferred = filepath.Join(p.Development, "deferred")
	}
	if p.Implementation != "" {
		p.SprintStatus = filepath.Join(p.Implementation, "sprint-status.yaml")
		p.DeferredLegacy = filepath.Join(p.Implementation, "deferred-work.md")
	}
	return p
}
