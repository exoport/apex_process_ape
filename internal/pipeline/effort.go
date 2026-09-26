package pipeline

import (
	"fmt"

	"github.com/exoport/apex_process_ape/internal/effort"
)

// EffectiveEffort resolves a step's explicit effort and names where it came
// from: step > stage > pipeline `effort:` > flag (the run's --effort).
// ("", "") means nothing explicit was declared, so the framework table, or
// ape's legacy default, governs; see effort.Decide.
func (s *Spec) EffectiveEffort(stageName string, stepIdx int, flag string) (level, source string, err error) {
	stage, ok := s.stageMap[stageName]
	if !ok || stage == nil {
		return "", "", fmt.Errorf("unknown stage %q", stageName)
	}
	if stepIdx < 0 || stepIdx >= len(stage.Chain) {
		return "", "", fmt.Errorf("stage %q: step index %d out of range [0,%d)", stageName, stepIdx, len(stage.Chain))
	}
	switch step := stage.Chain[stepIdx]; {
	case step.Effort != "":
		return step.Effort, effort.SourceStep, nil
	case stage.Effort != "":
		return stage.Effort, effort.SourceStage, nil
	case s.Effort != "":
		return s.Effort, effort.SourcePipeline, nil
	case flag != "":
		return flag, effort.SourceFlag, nil
	}
	return "", "", nil
}

// EffortErrors lists every `effort:` value that is not an effort level.
//
// Nothing validated these before, so a typo reached CLAUDE_CODE_EFFORT_LEVEL
// and claude decided what to make of it. They are errors rather than
// warnings, unlike an unrecognized model: a model newer than this binary
// is legitimate, but the set of effort levels is Claude Code's, and closed.
func (s *Spec) EffortErrors() []string {
	var out []string
	check := func(location, level string) {
		if err := effort.CheckLevel(level); err != nil {
			out = append(out, fmt.Sprintf("%s: effort %v", location, err))
		}
	}
	check("pipeline", s.Effort)
	for _, stage := range s.Stages() {
		check(fmt.Sprintf("stage %q", stage.Name), stage.Effort)
		for i := range stage.Chain {
			check(fmt.Sprintf("stage %q step %d (%s)", stage.Name, i, stage.Chain[i].Skill), stage.Chain[i].Effort)
		}
	}
	return out
}

// StageEffortConflict is one stage whose chain declares an effort its
// session never runs at.
type StageEffortConflict struct {
	Stage string
	// Launch is the stage's explicit launch effort — the first step's
	// cascaded `effort:`. Empty means none is declared, so the framework
	// table (or --effort, or the legacy default) governs the session.
	Launch string
	Steps  []StageModelConflictStep
}

// StageEffortConflicts is StageModelConflicts for `effort:`, and for the
// same reason: effort is fixed when the stage's claude process is
// launched, from the first step's cascade, and the chain's later steps are
// typed into that session. A later step declaring a different effort runs
// at the launch effort anyway. Reported, never fatal; the fix belongs to
// the pipeline file, by splitting the stage at its effort boundaries.
func (s *Spec) StageEffortConflicts() []StageEffortConflict {
	var out []StageEffortConflict
	for _, stage := range s.Stages() {
		if len(stage.Chain) < 2 {
			continue
		}
		launch, _, err := s.EffectiveEffort(stage.Name, 0, "")
		if err != nil {
			continue
		}
		conflict := StageEffortConflict{Stage: stage.Name, Launch: launch}
		for i := 1; i < len(stage.Chain); i++ {
			declared, _, effErr := s.EffectiveEffort(stage.Name, i, "")
			if effErr != nil || declared == "" || declared == launch {
				continue
			}
			conflict.Steps = append(conflict.Steps, StageModelConflictStep{
				Index: i, Skill: stage.Chain[i].Skill, Declared: declared,
			})
		}
		if len(conflict.Steps) > 0 {
			out = append(out, conflict)
		}
	}
	return out
}
