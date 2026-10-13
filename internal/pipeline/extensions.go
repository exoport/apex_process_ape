package pipeline

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
)

// SkipReasonExtension is the manifest's skip_reason for a stage or step
// whose `requires-extensions:` gate the project's config does not meet.
// A stable string: the eval reads it.
const SkipReasonExtension = "extension"

// Skip says why a stage or step did not run.
type Skip struct {
	Reason string
	// Missing lists the required extension ids the project has not
	// enabled, in the order the spec declares them.
	Missing []string
}

// Extensions is the extension set a run is gated on, resolved once at
// run start from the project's effective config (config.yaml plus the
// config.local.yaml overlay).
type Extensions struct {
	// Enabled is the effective `extensions` list, as configured.
	Enabled []string
	// Ignored is --ignore-extensions: every gate is treated as met, as
	// though ape had none.
	Ignored bool
}

// NewExtensions builds a set from an enabled list.
func NewExtensions(enabled []string, ignored bool) *Extensions {
	return &Extensions{Enabled: slices.Clone(enabled), Ignored: ignored}
}

// missing returns the ids in required that are not enabled. Nil when the
// gate is met, when there is no gate, or when gates are ignored.
func (e *Extensions) missing(required []string) []string {
	if e == nil || e.Ignored {
		return nil
	}
	var out []string
	for _, id := range required {
		if !slices.Contains(e.Enabled, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// anyEnabled reports whether at least one of ids is enabled. Ignored
// counts as enabled, so --ignore-extensions checks everything.
func (e *Extensions) anyEnabled(ids []string) bool {
	if e == nil || e.Ignored {
		return true
	}
	for _, id := range ids {
		if slices.Contains(e.Enabled, id) {
			return true
		}
	}
	return false
}

// ResolveExtensions reads the project's effective `extensions` list.
//
// The error is returned to the caller, which decides how much it matters:
// a spec with no gates runs fine without a config, while a gated spec
// cannot be planned without one. Treating an unreadable config as "no
// extensions" would skip every gated stage of a run that asked for them.
func ResolveExtensions(projectRoot string, ignored bool) (*Extensions, error) {
	res, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		return nil, err
	}
	return NewExtensions(res.Extensions, ignored), nil
}

// HasExtensionGates reports whether the spec declares any stage or step
// `requires-extensions:` gate or any `requires.conditional-files` entry.
func (s *Spec) HasExtensionGates() bool {
	if len(s.Requires.ConditionalFiles) > 0 {
		return true
	}
	for si := range s.Stages() {
		stage := &s.Stages()[si]
		if len(stage.RequiresExtensions) > 0 {
			return true
		}
		for i := range stage.Chain {
			if len(stage.Chain[i].RequiresExtensions) > 0 {
				return true
			}
		}
	}
	return false
}

// extensionsForRun is the run's extension set: the caller's, or
// ExtensionsFor's.
func extensionsForRun(spec *Spec, opts RunOptions) (*Extensions, error) {
	if opts.Extensions != nil {
		return opts.Extensions, nil
	}
	return ExtensionsFor(spec, opts.ProjectRoot, opts.IgnoreExtensions)
}

// ExtensionsFor resolves the extension set a run of spec is gated on, or
// explains why it cannot. A nil set with a nil error means the spec has
// no gates and the config could not be read: the run proceeds ungated
// and records nothing, as every ape before extension gates did.
func ExtensionsFor(spec *Spec, projectRoot string, ignored bool) (*Extensions, error) {
	ext, err := ResolveExtensions(projectRoot, ignored)
	switch {
	case err == nil:
		return ext, nil
	case ignored:
		return NewExtensions(nil, true), nil
	case !spec.HasExtensionGates():
		return nil, nil //nolint:nilnil // no gates, nothing to resolve: documented above
	default:
		return nil, ExtensionsUnresolvedError(spec.Name, err)
	}
}

// ExtensionsUnresolvedError is the refusal for a gated spec whose project
// config cannot be read.
func ExtensionsUnresolvedError(pipelineName string, err error) error {
	hint := ""
	if errors.Is(err, apexcfg.ErrNotFound) {
		hint = " (no _apex/config.yaml)"
	}
	return fmt.Errorf("pipeline %q gates stages on extensions, but the project's extensions could not be resolved%s: %w — "+
		"fix the config, or pass --ignore-extensions to run every stage", pipelineName, hint, err)
}

// StageSkips is the skip plan for one stage.
type StageSkips struct {
	// Stage is non-nil when the whole stage is skipped: its own gate is
	// unmet, or every step in its chain is skipped.
	Stage *Skip
	// Steps holds one entry per chain step; nil means the step runs.
	// When Stage is set every entry is set.
	Steps []*Skip
}

// firstRun returns the index of the first step that runs, -1 when none.
func (p StageSkips) firstRun() int {
	for i, s := range p.Steps {
		if s == nil {
			return i
		}
	}
	return -1
}

// lastRun returns the index of the last step that runs, -1 when none.
func (p StageSkips) lastRun() int {
	for i := range slices.Backward(p.Steps) {
		if p.Steps[i] == nil {
			return i
		}
	}
	return -1
}

// PlanSkips computes which stages and steps a run against ext skips.
// The plan is fixed before the first stage starts: config is read once,
// so a skill that edits `extensions` mid-run does not change it.
func (s *Spec) PlanSkips(ext *Extensions) map[string]StageSkips {
	out := make(map[string]StageSkips, len(s.stages))
	for si := range s.Stages() {
		stage := &s.Stages()[si]
		plan := StageSkips{Steps: make([]*Skip, len(stage.Chain))}
		if missing := ext.missing(stage.RequiresExtensions); len(missing) > 0 {
			skip := &Skip{Reason: SkipReasonExtension, Missing: missing}
			plan.Stage = skip
			for i := range plan.Steps {
				plan.Steps[i] = skip
			}
			out[stage.Name] = plan
			continue
		}
		var union []string
		for i := range stage.Chain {
			if missing := ext.missing(stage.Chain[i].RequiresExtensions); len(missing) > 0 {
				plan.Steps[i] = &Skip{Reason: SkipReasonExtension, Missing: missing}
				for _, id := range missing {
					if !slices.Contains(union, id) {
						union = append(union, id)
					}
				}
			}
		}
		if len(stage.Chain) > 0 && plan.firstRun() < 0 {
			plan.Stage = &Skip{Reason: SkipReasonExtension, Missing: union}
		}
		out[stage.Name] = plan
	}
	return out
}

// ResolveFromStage maps `--from <stage>` onto the stage a run actually
// starts at. When the named stage is skipped, the run starts at the next
// stage that is not; started is empty when every stage from there on is
// skipped. skipped reports whether the named stage itself was.
func (s *Spec) ResolveFromStage(from string, plan map[string]StageSkips) (started string, skipped bool) {
	seen := false
	for si := range s.Stages() {
		stage := &s.Stages()[si]
		if stage.Name == from {
			seen = true
			skipped = plan[stage.Name].Stage != nil
		}
		if seen && plan[stage.Name].Stage == nil {
			return stage.Name, skipped
		}
	}
	return "", skipped
}

// validateExtensionIDs rejects an id that is not one of the framework's
// four. A typo here would otherwise skip a stage on every project, in
// silence, which is the most expensive way for a gate to be wrong.
func (s *Spec) validateExtensionIDs() error {
	var bad []string
	check := func(where string, ids []string, required bool) {
		if required && len(ids) == 0 {
			bad = append(bad, where+": extensions-any is required")
		}
		for _, id := range ids {
			if !apexcfg.IsKnownExtension(id) {
				bad = append(bad, fmt.Sprintf("%s: unknown extension %q", where, id))
			}
		}
	}
	for i, cf := range s.Requires.ConditionalFiles {
		where := fmt.Sprintf("requires.conditional-files[%d]", i)
		if cf.Path == "" {
			bad = append(bad, where+": path is required")
		}
		check(where, cf.ExtensionsAny, true)
	}
	for si := range s.Stages() {
		stage := &s.Stages()[si]
		check(fmt.Sprintf("stage %q requires-extensions", stage.Name), stage.RequiresExtensions, false)
		for i := range stage.Chain {
			check(fmt.Sprintf("stage %q step %d (%s) requires-extensions", stage.Name, i, stage.Chain[i].Skill),
				stage.Chain[i].RequiresExtensions, false)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("invalid extension gate(s) — known ids are %s:\n  - %s",
		strings.Join(apexcfg.KnownExtensions(), ", "), strings.Join(bad, "\n  - "))
}
