package apecmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/repl"
)

// v1TaskLaunch is an `ape task` session's launch in an APEX v1 project
// (backlog 29): the decision and the record its manifest gets.
type v1TaskLaunch struct {
	launch apexv1.Launch
	parent string
}

// v1ProjectAt returns the v1 project rooted at root, if it is one.
func v1ProjectAt(root string) (*apexv1.Project, bool) {
	p, err := apexv1.Open(root)
	if err != nil || !p.IsV1() {
		return nil, false
	}
	return p, true
}

// decideV1TaskLaunch resolves a v1 session's model and effort: --model /
// --effort, then _apex/config.yaml models.<skill>, then the session that
// started this one (APE_SESSION_MODEL / APE_SESSION_EFFORT), then the
// config's top-level effort:, then nothing (apexv1.DecideLaunch).
//
// A review session started by `ape change review --run` arrives with the
// decision already made: its flags carry the values and APE_REVIEW_LAUNCH
// the sources, so launch and review_launch record the same thing.
func decideV1TaskLaunch(p *apexv1.Project, skill, flagModel, flagEffort string) (v1TaskLaunch, error) {
	cfg, err := apexv1.LoadConfig(p.Root)
	if err != nil {
		return v1TaskLaunch{}, fmt.Errorf("%s: %w", apexv1.ConfigPath(p.Root), err)
	}
	entry := cfg.Models[skill]
	if skill == apexv1.OrchestratorSkill {
		// Q96: before seq-07, models.apex-orchestrator is the loop's.
		var legacy bool
		if entry, legacy = apexv1.LoopModel(cfg.Models); legacy {
			fmt.Fprintln(os.Stderr, apexv1.LoopModelWarning)
		}
	}
	if entry.Model != "" {
		entry.Model = resolveModelArg(entry.Model)
	}
	if err := effort.CheckLevel(entry.Effort); err != nil {
		return v1TaskLaunch{}, fmt.Errorf("%s models.%s.effort: %w", apexv1.ConfigPath(p.Root), skill, err)
	}
	if err := effort.CheckLevel(cfg.Effort); err != nil {
		return v1TaskLaunch{}, fmt.Errorf("%s effort: %w", apexv1.ConfigPath(p.Root), err)
	}
	if err := effort.CheckLevel(flagEffort); err != nil {
		return v1TaskLaunch{}, fmt.Errorf("--effort %w", err)
	}
	parentEffort := os.Getenv(repl.EnvSessionEffort)
	if effort.CheckLevel(parentEffort) != nil {
		parentEffort = ""
	}
	out := v1TaskLaunch{launch: apexv1.DecideLaunch(flagModel, flagEffort, entry, cfg.Effort,
		os.Getenv(repl.EnvSessionModel), parentEffort)}
	if review, ok := reviewLaunchFromEnv(skill); ok {
		// The flags carry this decision's values; it adds the sources.
		out.launch, out.parent = review, os.Getenv(envReviewParent)
		if flagModel != "" {
			out.launch.Model = flagModel
		}
	}
	return out, nil
}

// reviewLaunchFromEnv is the decision `ape change review --run` passed its
// review session in APE_REVIEW_LAUNCH.
func reviewLaunchFromEnv(skill string) (apexv1.Launch, bool) {
	raw := os.Getenv(envReviewLaunch)
	if raw == "" || skill != reviewSkill {
		return apexv1.Launch{}, false
	}
	var l apexv1.Launch
	if json.Unmarshal([]byte(raw), &l) != nil {
		return apexv1.Launch{}, false
	}
	return l, true
}

func (l v1TaskLaunch) record() *pipeline.LaunchRecord {
	return &pipeline.LaunchRecord{
		Model: l.launch.Model, ModelSource: l.launch.ModelSource,
		Effort: l.launch.Effort, EffortSource: l.launch.EffortSource,
		Parent: l.parent,
	}
}

// recordLaunch writes a session's launch decision into its run manifest:
// launch: for a v1 session (v1 nil otherwise), and review_launch: for a
// review session started by `ape change review --run`. Best-effort, like
// recordCommitContract.
func recordLaunch(projectRoot, skill, manifestDir string, v1 *v1TaskLaunch) {
	var launch, review *pipeline.LaunchRecord
	if v1 != nil {
		launch = v1.record()
	}
	if l, ok := reviewLaunchFromEnv(skill); ok && launch != nil {
		same := *launch
		review = &same
	} else if ok {
		review = &pipeline.LaunchRecord{
			Model: l.Model, ModelSource: l.ModelSource, Effort: l.Effort, EffortSource: l.EffortSource,
			Parent: os.Getenv(envReviewParent),
		}
	}
	if launch == nil && review == nil {
		return
	}
	patchLatestManifest(projectRoot, skill, manifestDir, func(m *pipeline.Manifest) {
		if launch != nil {
			m.Launch = launch
		}
		if review != nil {
			m.ReviewLaunch = review
		}
	})
}

// v1InlineHooks are a v1 project's own hooks as ape task injects them,
// and the environment entry that tells the settings.json copies to stand
// down (apexv1.EnvInlineHooks).
func v1InlineHooks() (hooks []config.ExtraHook, env []string) {
	specs := apexv1.InlineHooks()
	hooks = make([]config.ExtraHook, 0, len(specs))
	for _, h := range specs {
		hooks = append(hooks, config.ExtraHook{Event: h.Event, Matcher: h.Matcher, Command: h.Command, Timeout: h.Timeout})
	}
	return hooks, []string{apexv1.EnvInlineHooks + "=inline"}
}
