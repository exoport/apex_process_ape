package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/exoport/apex_process_ape/internal/framework"
)

// PreflightErrorKind distinguishes which kind of pre-run check produced
// the *PreflightError. The error message format adapts so users see
// "required file missing" for file checks and "skill missing" for skill
// resolution failures.
type PreflightErrorKind int

const (
	// PreflightKindFiles — Preflight failed because one or more
	// `requires.files` paths were absent.
	PreflightKindFiles PreflightErrorKind = iota
	// PreflightKindSkills — PreflightSkills failed because one or more
	// referenced skills (or agents) had no SKILL.md in the project's or
	// the user's .claude/skills/ tree.
	PreflightKindSkills
)

// PreflightError reports a missing prerequisite for a pipeline run.
// The error names the missing entries so the user can take corrective
// action (typically: run an upstream pipeline first, install the
// framework, or correct a typo'd skill name in the pipeline YAML).
type PreflightError struct {
	Pipeline string
	Missing  []string
	Kind     PreflightErrorKind
}

func (e *PreflightError) Error() string {
	label := "required file"
	if e.Kind == PreflightKindSkills {
		label = "skill"
	}
	if len(e.Missing) == 1 {
		return fmt.Sprintf("pipeline %q: %s missing: %s", e.Pipeline, label, e.Missing[0])
	}
	return fmt.Sprintf("pipeline %q: %d %ss missing:\n  - %s",
		e.Pipeline, len(e.Missing), label, strings.Join(e.Missing, "\n  - "))
}

// Preflight verifies the pipeline's prerequisites against the working
// directory. Returns nil if all required files are present, otherwise
// a *PreflightError listing every missing path at once.
//
// A `requires.conditional-files` entry is checked only when ext enables
// at least one of its extensions; a nil ext, or --ignore-extensions,
// checks them all.
func Preflight(spec *Spec, projectRoot string, ext *Extensions) error {
	paths := slices.Clone(spec.Requires.Files)
	for _, cf := range spec.Requires.ConditionalFiles {
		if ext.anyEnabled(cf.ExtensionsAny) {
			paths = append(paths, cf.Path)
		}
	}
	var missing []string
	for _, rel := range paths {
		p := filepath.Join(projectRoot, rel)
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, rel)
				continue
			}
			return fmt.Errorf("preflight: stat %s: %w", rel, err)
		}
	}
	if len(missing) > 0 {
		return &PreflightError{Pipeline: spec.Name, Missing: missing, Kind: PreflightKindFiles}
	}
	return nil
}

// PluginSkillResolver resolves a plugin skill, <plugin>:<skill>, for
// PreflightSkills: nil when Claude Code would load it, else an error that
// says what is missing and how to fix it.
type PluginSkillResolver func(name string) error

// PreflightSkills verifies that every skill (and agent) referenced by
// the pipeline's stage chains resolves to a SKILL.md on disk. The
// resolver mirrors claude's lookup order: project-scoped
// `<projectRoot>/.claude/skills/<name>/SKILL.md` first, then
// user-scoped `~/.claude/skills/<name>/SKILL.md`.
//
// This catches typos and missing framework installs before claude is
// spawned. Without it, an unresolved skill name reaches claude as the
// prompt body, claude prints a polite "skill does not exist" message,
// and exits 0 — which the runner records as "completed" because the
// subprocess succeeded. See the sandbox `sketch` regression for the
// canonical failure mode this guards against.
//
// A plugin skill, <plugin>:<skill>, has no SKILL.md under either tree:
// Claude Code loads it from the plugin's install. plugins resolves such a
// name when the caller has one (ape task in an APEX v1 project); a name
// without a ':', or any name when plugins is nil, resolves as above.
func PreflightSkills(spec *Spec, projectRoot string, plugins PluginSkillResolver) error {
	projDir := framework.ProjectSkillsPath(projectRoot)
	seen := make(map[string]struct{})
	var missing []string

	check := func(name, where string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		if plugins != nil && strings.Contains(name, ":") {
			if err := plugins(name); err != nil {
				missing = append(missing, fmt.Sprintf("%q (%s): %s", name, where, err))
			}
			return
		}
		if _, _, found := framework.ResolveSkill(name, projectRoot); found {
			return
		}
		missing = append(missing, fmt.Sprintf(
			"%q (%s): SKILL.md not found in %s or ~/.claude/skills/",
			name, where, projDir,
		))
	}

	for si := range spec.Stages() {
		stage := &spec.Stages()[si]
		for i := range stage.Chain {
			step := &stage.Chain[i] // pointer: avoid copying the 128-byte Step per iteration
			where := fmt.Sprintf("stage %s, step %d", stage.Name, i+1)
			check(step.Skill, where)
			check(step.Agent, where)
		}
	}

	if len(missing) > 0 {
		return &PreflightError{Pipeline: spec.Name, Missing: missing, Kind: PreflightKindSkills}
	}
	return nil
}
