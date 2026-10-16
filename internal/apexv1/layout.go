// Package apexv1 is the deterministic half of the APEX v1 way of working:
// the project layout under development/, the few shapes ape validates, the
// change lifecycle recorded in state.json, the delta merge into specs, and
// the hook verdicts. Skills supply judgment; everything here must be right
// every time, so it is code.
//
// The contract this implements is the v1 trial brief (r1, 2026-10-15). Only
// ape writes state.json, development/.apex/ and the specs/ tree; models
// write change.md, design.md, project.md and the roadmap's prose.
package apexv1

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

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
}

// SkillModel is one models.<skill> entry.
type SkillModel struct {
	Model  string `yaml:"model"`
	Effort string `yaml:"effort"`
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
// _apex/config.yaml or a v1 development folder (development/.apex or
// development/project.md), else the git top level the walk passes, else
// dir itself. A bare development/ folder is not a marker: plenty of repos
// have one for other reasons (ape's own does).
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	gitTop := ""
	for cur := abs; ; {
		dev := filepath.Join(cur, DefaultDevFolder)
		if fileExists(ConfigPath(cur)) || dirExists(filepath.Join(dev, ".apex")) || fileExists(filepath.Join(dev, "project.md")) {
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

func (p *Project) ArchiveDir() string {
	return filepath.Join(p.ChangesDir(), "archive")
}

func (p *Project) DecisionsDir() string {
	return filepath.Join(p.Dev(), "decisions")
}

func (p *Project) ApexDir() string {
	return filepath.Join(p.Dev(), ".apex")
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
	return filepath.Join(p.ApexDir(), "milestones.json")
}

func (p *Project) ProjectJSON() string {
	return filepath.Join(p.ApexDir(), "project.json")
}

func (p *Project) LogDir(id string) string {
	return filepath.Join(p.ApexDir(), "logs", id)
}

func (p *Project) StopGateDir() string {
	return filepath.Join(p.ApexDir(), "stop-gate")
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

// IsV1 reports whether the project has a v1 development folder: one
// `ape project init` made (.apex/) or one with a project.md.
func (p *Project) IsV1() bool {
	return dirExists(p.ApexDir()) || fileExists(p.ProjectMD())
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
