// Package apexv1 is the deterministic half of the APEX v1 way of working:
// the project layout under development/, the few shapes ape validates, the
// change lifecycle recorded in state.json, the delta merge into specs, and
// the hook verdicts. Skills supply judgment; everything here must be right
// every time, so it is code.
//
// The contract this implements is the v1 trial brief (r1, 2026-10-15). Only
// ape writes state.json, _apex/state/ and the specs/ tree; models write
// change.md, design.md, project.md and the roadmap's prose.
package apexv1

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"gopkg.in/yaml.v3"
)

// Size caps from the brief. Bytes, not runes: a cap is about what a model
// has to read, and bytes are what a context window pays for.
const (
	ProjectMaxLines = 150
	ProjectMaxBytes = 8 * 1024
	SpecMaxBytes    = 20 * 1024
	ChangeMaxBytes  = 8 * 1024
	DesignMaxBytes  = 8 * 1024
	AgentsMaxLines  = 120
)

// DefaultDevFolder is the development folder when _apex/config.yaml does
// not name one.
const DefaultDevFolder = "development"

// Project is a resolved v1 project: its root and its development folder.
type Project struct {
	Root string
	// DevRel is the development folder relative to Root, slash-separated.
	DevRel string
}

// Config is the minimal _apex/config.yaml v1 reads.
type Config struct {
	ProjectName       string `yaml:"project_name"`
	DevelopmentFolder string `yaml:"development_folder"`
	// Models pins a skill's model (and optionally effort), keyed by skill:
	//
	//	models:
	//	  apex-review: sonnet                          # model only
	//	  apex-review: {model: sonnet, effort: high}   # model and effort
	Models map[string]SkillModel `yaml:"models"`
	// Regression is the project's own test suite, a shell command run
	// from the project root (backlog 32): at a change's start (its
	// baseline), at verify and at the milestone gate, failing only when
	// newly red. One command, or two tiers (H10, RegressionConfig).
	Regression RegressionConfig `yaml:"regression"`
	// Effort is the project's default effort for an ape-launched session
	// that no flag, models.<skill> entry or parent session decides
	// (DecideLaunch). Empty: Claude Code's own setting.
	Effort string `yaml:"effort"`
	// Limits are the loop limits ape enforces (backlog 30): the
	// milestone budget (off unless enforce) and the attempts cap per
	// change start.
	Limits Limits `yaml:"limits"`
	// Marketplaces and Plugins are the Claude Code plugins the project
	// uses; ape keeps them installed at project scope (plugins.go).
	Marketplaces map[string]Marketplace `yaml:"marketplaces"`
	Plugins      []PluginRef            `yaml:"plugins"`
	// Timeouts bound one run of a check, the regression command and a
	// gate command (H10); a --timeout flag wins.
	Timeouts Timeouts `yaml:"timeouts"`
	// Retries is how many times a red check is run again (H12, default
	// 0); a pass on a retry is green and recorded flaky.
	Retries Retries `yaml:"retries"`
	// Checks and Requires keep a check from passing by running nothing
	// (H11): output that says it ran nothing, and a requirement the
	// machine lacks.
	Checks   ChecksConfig `yaml:"checks"`
	Requires Requires     `yaml:"requires"`
}

// SkillModel is one models.<skill> entry.
type SkillModel struct {
	Model  string `json:"model,omitempty"  yaml:"model"`
	Effort string `json:"effort,omitempty" yaml:"effort"`
}

// UnmarshalYAML accepts the scalar form (a model name) and the mapping.
func (m *SkillModel) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		m.Model = strings.TrimSpace(n.Value)
		return nil
	}
	type plain SkillModel
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*m = SkillModel{Model: strings.TrimSpace(p.Model), Effort: strings.TrimSpace(p.Effort)}
	return nil
}

// ConfigPath is the project's _apex/config.yaml.
func ConfigPath(root string) string { return filepath.Join(root, "_apex", "config.yaml") }

// LoadConfig reads _apex/config.yaml; an absent file is the zero Config.
func LoadConfig(root string) (Config, error) {
	var c Config
	data, err := os.ReadFile(ConfigPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Open resolves the project at root. The development folder comes from
// _apex/config.yaml's development_folder, else DefaultDevFolder. An
// unparseable config is an error: guessing the folder would let every
// command act on the wrong tree.
func Open(root string) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(abs)
	if err != nil {
		return nil, err
	}
	dev := strings.Trim(filepath.ToSlash(cfg.DevelopmentFolder), "/")
	if dev == "" {
		dev = DefaultDevFolder
	}
	return &Project{Root: abs, DevRel: dev}, nil
}

// FindRoot walks up from dir to the nearest directory holding
// _apex/config.yaml, _apex/state/ or a v1 development folder
// (development/project.md, or an older ape's development/.apex), else the
// git top level the walk passes, else dir itself. A bare development/ folder is not a marker: plenty of repos
// have one for other reasons (ape's own does).
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	gitTop := ""
	for cur := abs; ; {
		dev := filepath.Join(cur, DefaultDevFolder)
		if fileExists(ConfigPath(cur)) || dirExists(runlog.V1StateDir(cur)) || dirExists(filepath.Join(dev, ".apex")) || fileExists(filepath.Join(dev, "project.md")) {
			return cur
		}
		if gitTop == "" && fileOrDirExists(filepath.Join(cur, ".git")) {
			gitTop = cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	if gitTop != "" {
		return gitTop
	}
	return abs
}

// Dev is the absolute development folder.
func (p *Project) Dev() string { return filepath.Join(p.Root, filepath.FromSlash(p.DevRel)) }

// Rel returns path relative to the project root, slash-separated.
func (p *Project) Rel(path string) string {
	if r, err := filepath.Rel(p.Root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(path)
}

func (p *Project) ProjectMD() string {
	return filepath.Join(p.Dev(), "project.md")
}

func (p *Project) RoadmapMD() string {
	return filepath.Join(p.Dev(), "roadmap.md")
}

func (p *Project) SpecsDir() string {
	return filepath.Join(p.Dev(), "specs")
}

func (p *Project) ChangesDir() string {
	return filepath.Join(p.Dev(), "changes")
}

// ArchiveDir is where archived and abandoned changes live, beside
// changes/ rather than inside it (backlog 24): changes/ holds only the
// changes in flight.
func (p *Project) ArchiveDir() string {
	return filepath.Join(p.Dev(), "archive")
}

// OldArchiveDir is changes/archive/, where an older ape archived changes.
// Only two readers: `project check`, which reports it, and `change new`'s
// id-uniqueness scan, so an id archived there is never reused.
func (p *Project) OldArchiveDir() string {
	return filepath.Join(p.ChangesDir(), "archive")
}

func (p *Project) DecisionsDir() string {
	return filepath.Join(p.Dev(), "decisions")
}

// BriefsDir is development/briefs/: every apex-plan run's input, verbatim,
// as a dated file that is never edited (D39).
func (p *Project) BriefsDir() string {
	return filepath.Join(p.Dev(), "briefs")
}

// StateDir is _apex/state/, ape's own records (backlog 26–28):
// milestones.json and decisions.json are tracked; logs/, runs/ and hooks/
// are ignored by its whitelist .gitignore. The path is runlog's, the one
// place it is decided.
func (p *Project) StateDir() string {
	return runlog.V1StateDir(p.Root)
}

// StateRel is StateDir relative to the project root, slash-separated.
func (p *Project) StateRel() string { return p.Rel(p.StateDir()) }

// OldStateDir is {development_folder}/.apex/, where an older ape kept its
// state. Read only to recognise the project (IsV1, FindRoot) and by
// `project check`, which reports it.
func (p *Project) OldStateDir() string {
	return filepath.Join(p.Dev(), ".apex")
}

// ensureState creates _apex/state/ and its whitelist .gitignore before ape
// writes an ignored record there, so the record cannot dirty the tree.
func (p *Project) ensureState() { runlog.EnsureV1State(p.Root) }

// StateGitignore is the path of _apex/state/.gitignore.
func (p *Project) StateGitignore() string {
	return filepath.Join(p.StateDir(), ".gitignore")
}

func (p *Project) ChangeDir(id string) string {
	return filepath.Join(p.ChangesDir(), id)
}

func (p *Project) ChangeMD(id string) string {
	return filepath.Join(p.ChangeDir(id), "change.md")
}

func (p *Project) DesignMD(id string) string {
	return filepath.Join(p.ChangeDir(id), "design.md")
}

func (p *Project) StateFile(id string) string {
	return filepath.Join(p.ChangeDir(id), "state.json")
}

func (p *Project) MilestonesJSON() string {
	return filepath.Join(p.StateDir(), "milestones.json")
}

// ProjectJSON is init's record (initialized_at, brownfield, base_sha):
// ignored, as nothing reads it back.
func (p *Project) ProjectJSON() string {
	return filepath.Join(p.StateDir(), "project.json")
}

// LogsDir holds the check output of every run of checks.
func (p *Project) LogsDir() string {
	return filepath.Join(p.StateDir(), "logs")
}

func (p *Project) LogDir(id string) string {
	return filepath.Join(p.LogsDir(), id)
}

// HooksDir holds the Stop/SubagentStop hook's per-session verdict records
// and logs.
func (p *Project) HooksDir() string {
	return filepath.Join(p.StateDir(), "hooks")
}

func (p *Project) AgentsMD() string {
	return filepath.Join(p.Root, "AGENTS.md")
}

func (p *Project) ClaudeMD() string {
	return filepath.Join(p.Root, "CLAUDE.md")
}

func (p *Project) SettingsJSON() string {
	return filepath.Join(p.Root, ".claude", "settings.json")
}

// IsV1 reports whether the project is an APEX v1 one: `ape project init`
// made it (_apex/state/), its development folder has a project.md, or an
// older ape made it (development/.apex/, which `project check` reports).
func (p *Project) IsV1() bool {
	return dirExists(p.StateDir()) || dirExists(p.OldStateDir()) || fileExists(p.ProjectMD())
}

func (p *Project) devPrefix() string {
	return p.DevRel + "/"
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func fileOrDirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func (p *Project) SpecFile(capability string) string {
	return filepath.Join(p.SpecsDir(), capability, "spec.md")
}
