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
	// SubtreeTerminalContracts is the framework-owned table of per-skill
	// terminal contracts (PLAN-25 Gate C): which skills end a run with a
	// machine-readable return block, and the pattern that recognises it.
	// Optional in the framework repo, on the same version-skew terms as
	// SubtreeOperatingRules — a framework that predates it installs
	// nothing and ape simply runs no contract check.
	SubtreeTerminalContracts = "_apex/terminal-contracts.csv"
	// SubtreeApeCommands is the framework-owned manifest of the ape command
	// surface an installed framework requires. From v0.11.0 the skills shell
	// out to ape subcommands with no fallback branch, so a binary missing one
	// makes a skill fail deep inside a stage rather than degrade.
	//
	// Installed here so the contract lives in the project rather than only in
	// the framework build repo. ape does not read it yet — the check that
	// diffs `required_commands` against this binary's own command tree is
	// separate work. Copying it first is deliberate: it means the framework
	// can ship the manifest and have it reach projects on their next update,
	// without waiting for the ape release that consumes it.
	//
	// Optional in the framework repo, on the same version-skew terms as
	// SubtreeOperatingRules and SubtreeTerminalContracts.
	SubtreeApeCommands = "_apex/ape-commands.yaml"
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
	// ProjectTerminalContracts is where the per-skill terminal-contract
	// table lands in the project. Absent = no contract check runs.
	ProjectTerminalContracts = "_apex/terminal-contracts.csv"
	// ProjectApeCommands is where the required-command-surface manifest
	// lands in the project. Absent = the framework predates the contract.
	ProjectApeCommands = "_apex/ape-commands.yaml"
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
