package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diegosz/apex_process_ape/internal/framework"
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
// a *PreflightError listing the missing paths.
func Preflight(spec *Spec, projectRoot string) error {
	var missing []string
	for _, rel := range spec.Requires.Files {
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
func PreflightSkills(spec *Spec, projectRoot string) error {
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
		if _, _, found := framework.ResolveSkill(name, projectRoot); found {
			return
		}
		missing = append(missing, fmt.Sprintf(
			"%q (%s): SKILL.md not found in %s or ~/.claude/skills/",
			name, where, projDir,
		))
	}

	for _, stage := range spec.Stages() {
		for i, step := range stage.Chain {
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
