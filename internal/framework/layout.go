// Package framework implements the project-side install machinery for
// apex_process_framework: copying skills + pipelines from a checked-out
// framework repo into a project root, seeding _apex/config.yaml on
// first run, and writing _apex/framework.yaml metadata.
package framework

// Subtree paths inside a checked-out apex_process_framework repo.
// These target the *released* framework layout, where .claude/ and
// _apex/ sit at the repo root (identical to the shape a project
// consumes). The earlier build layout nested these under a framework/
// subfolder (framework/_claude, framework/_apex); that layout is no
// longer supported. Hard-coded for now; if the layout changes again
// we'll lift these to a manifest the framework itself ships.
const (
	SubtreeSkills             = ".claude/skills"
	SubtreePipelines          = "_apex/pipelines"
	SubtreeConfig             = "_apex/config.yaml"
	SubtreeConfigLocalExample = "_apex/config.local.example.yaml"
	// SubtreeOperatingRules is the always-on APEX operating-rules
	// fragment (PLAN-47 Workstream C). Optional in the framework repo:
	// versions that predate it are handled by version-skew suppression
	// (setup/update skip fragment + CLAUDE.md management rather than
	// failing), so it is deliberately NOT part of validateFrameworkLayout.
	SubtreeOperatingRules = "_apex/apex-operating-rules.md"
)

// Project-side paths, relative to the project root the user is
// installing into.
const (
	ProjectSkillsDir          = ".claude/skills"
	ProjectPipelinesDir       = "_apex/pipelines"
	ProjectConfig             = "_apex/config.yaml"
	ProjectConfigLocalExample = "_apex/config.local.example.yaml"
	ProjectMetadata           = "_apex/framework.yaml"
	// ProjectOperatingRules is where the operating-rules fragment lands
	// in the project (checked into the project's git). ProjectClaudeMd is
	// the repo-root file carrying the managed @import of it.
	ProjectOperatingRules = "_apex/apex-operating-rules.md"
	ProjectClaudeMd       = "CLAUDE.md"
)

// SkillPrefix is the filename prefix that identifies framework-managed
// skills. Anything else under .claude/skills/ is left alone by
// `ape framework update`.
const SkillPrefix = "apex-"

// OrchestratorSkill is the apex-orchestrator persona skill (PLAN-47
// Workstream B). It installs via the generic apex-* skill-copy path — no
// dedicated copy logic — but `ape doctor` checks for it as part of the
// operating-rules contract, so its name is named here for that check.
const OrchestratorSkill = "apex-orchestrator"
