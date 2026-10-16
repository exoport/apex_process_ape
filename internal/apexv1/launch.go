package apexv1

// Where an ape-launched session's model and effort came from, as recorded
// in its run manifest (launch:, and review_launch: on a review session).
const (
	LaunchFromFlag    = "flag"           // --model / --effort
	LaunchFromConfig  = "config"         // _apex/config.yaml models.<skill>
	LaunchFromParent  = "parent-session" // the ape session that started this one
	LaunchFromDefault = "default"        // nothing decided: claude's own default
	// LaunchFromConfigDefault is _apex/config.yaml's top-level effort:.
	LaunchFromConfigDefault = "config-default"
)

// Launch is the model and effort an ape-launched session of a v1 project
// starts with, and what decided each. An empty Model or Effort passes
// nothing to claude.
type Launch struct {
	Model        string `json:"model,omitempty"`
	ModelSource  string `json:"model_source"`
	Effort       string `json:"effort,omitempty"`
	EffortSource string `json:"effort_source"`
}

// DecideLaunch picks a v1 session's model and effort (backlog 29, D22).
//
// The model: the flag, else models.<skill> (skill), else the model of the
// ape session that started this one (parentModel, APE_SESSION_MODEL),
// else claude's default.
//
// The effort: the flag, else models.<skill>'s effort, else the parent
// session's effort, else the top-level config effort: (topEffort), else
// none — claude's own default; there is no hidden ape default.
//
// D22's exception, kept: when models.<skill> names a model without an
// effort (and no --model overrides it), the parent's effort is skipped.
// That effort was chosen for the parent's model, and the config just chose
// a different one; the project's own default (topEffort) still applies.
func DecideLaunch(flagModel, flagEffort string, skill SkillModel, topEffort, parentModel, parentEffort string) Launch {
	var l Launch
	switch {
	case flagModel != "":
		l.Model, l.ModelSource = flagModel, LaunchFromFlag
	case skill.Model != "":
		l.Model, l.ModelSource = skill.Model, LaunchFromConfig
	case parentModel != "":
		l.Model, l.ModelSource = parentModel, LaunchFromParent
	default:
		l.ModelSource = LaunchFromDefault
	}
	parentApplies := l.ModelSource != LaunchFromConfig
	switch {
	case flagEffort != "":
		l.Effort, l.EffortSource = flagEffort, LaunchFromFlag
	case skill.Effort != "":
		l.Effort, l.EffortSource = skill.Effort, LaunchFromConfig
	case parentApplies && parentEffort != "":
		l.Effort, l.EffortSource = parentEffort, LaunchFromParent
	case topEffort != "":
		l.Effort, l.EffortSource = topEffort, LaunchFromConfigDefault
	default:
		l.EffortSource = LaunchFromDefault
	}
	return l
}
