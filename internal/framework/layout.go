// Package framework implements the project-side install machinery for
// apex_process_framework: copying skills + pipelines from a checked-out
// framework repo into a project root, seeding _apex/config.yaml on
// first run, and writing _apex/framework.yaml metadata.
package framework

// Subtree paths inside a checked-out apex_process_framework repo.
// Hard-coded for now; if framework layout ever changes we'll lift these
// to a manifest the framework itself ships.
const (
	SubtreeSkills             = "framework/_claude/skills"
	SubtreePipelines          = "framework/_apex/pipelines"
	SubtreeConfig             = "framework/_apex/config.yaml"
	SubtreeConfigLocalExample = "framework/_apex/config.local.example.yaml"
)

// Project-side paths, relative to the project root the user is
// installing into.
const (
	ProjectSkillsDir          = ".claude/skills"
	ProjectPipelinesDir       = "_apex/pipelines"
	ProjectConfig             = "_apex/config.yaml"
	ProjectConfigLocalExample = "_apex/config.local.example.yaml"
	ProjectMetadata           = "_apex/framework.yaml"
)

// SkillPrefix is the filename prefix that identifies framework-managed
// skills. Anything else under .claude/skills/ is left alone by
// `ape framework update`.
const SkillPrefix = "apex-"
