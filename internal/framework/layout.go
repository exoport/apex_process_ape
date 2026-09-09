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
	// SubtreeOutputStyles is the framework-owned table of per-skill output
	// styles: which Claude Code output style each skill's session runs
	// under. The framework measures the effect per skill and owns the
	// judgement; ape reads a name and pins it.
	//
	// Optional in the framework repo, on the same version-skew terms as
	// SubtreeTerminalContracts — a framework that predates it installs
	// nothing and every skill takes the pinned default.
	SubtreeOutputStyles = "_apex/output-styles.csv"
	// SubtreeCommitOwners is the framework-owned roster of which skills
	// commit and in what subject shape — the file `ape task`'s per-dispatch
	// commit-ownership assertion reads (PLAN-26 D2).
	//
	// It MUST be installed, and that is not obvious from ape's side: the
	// assertion reads it from the PROJECT, and an absent file means "this
	// project has not adopted the declaration", which the runner correctly
	// reports as `skipped`. So a framework that ships the roster while ape
	// declines to copy it produces a release where the assertion never
	// fires anywhere and says so in a way that reads like normal
	// operation — the deletion of 85 `## Commit Policy` sections would
	// land on a check that is permanently inert.
	//
	// Optional in the framework repo, on the same version-skew terms as
	// the three above.
	SubtreeCommitOwners = "_apex/commit-owners.csv"
	// SubtreeMigrations is the framework's per-version upgrade list, read
	// by `ape framework update`'s migration runner (PLAN-26 M14).
	//
	// Installed for the same reason the roster above is: the runner reads
	// the list from `{apex_folder}/migrations/` in the PROJECT, and an
	// absent folder is reported as "this framework ships no migration
	// list". On a framework that ships one, that report is not merely
	// unhelpful — it is false, and it is the shape the operator would act
	// on.
	//
	// Optional in the framework repo, on the same version-skew terms.
	SubtreeMigrations = "_apex/migrations"
	// SubtreeAboardRecipes is the framework's curated `ape aboard` recipe
	// library — markdown methods for one board move each, which the board
	// discovers from the project.
	//
	// This is the HIGHEST-precedence of aboard's four recipe directories
	// (`_apex/aboard/recipes` → `_aboard/recipes` → `.aboard/recipes` →
	// built into the binary), which is exactly why only curated library
	// recipes belong here. Copying a BUILT-IN into it would shadow the
	// binary's own copy and freeze it at install time, so the recipe would
	// silently stop tracking the renderer it describes — the same drift the
	// capsHash beacon exists to catch, reintroduced by hand.
	//
	// Optional in the framework repo, on the same version-skew terms as the
	// three above: a framework that ships none installs none, and every
	// built-in still reaches the project inside the binary.
	SubtreeAboardRecipes = "_apex/aboard/recipes"
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
	// ProjectOutputStyles is where the per-skill output-style table lands.
	// Absent = no skill is enrolled and every dispatch takes the pinned
	// default, which is what every dispatch did before the table existed.
	ProjectOutputStyles = "_apex/output-styles.csv"
	// ProjectCommitOwners is where the commit-ownership roster lands.
	// Absent = the project has not adopted the declaration, and every
	// dispatch's assertion is skipped with a reason.
	ProjectCommitOwners = "_apex/commit-owners.csv"
	// ProjectMigrationsDir is where the upgrade list lands. Absent = this
	// framework ships none, and the runner has nothing pending.
	ProjectMigrationsDir = "_apex/migrations"
	// ProjectAboardRecipesDir is where the framework's recipe library lands.
	// The path is aboard's to define, not ape's — the board walks up looking
	// for exactly this directory, so it is the same string on both sides and
	// must stay that way.
	ProjectAboardRecipesDir = "_apex/aboard/recipes"
	// ProjectAboardDir is the board's own directory, created by setup/update
	// so a project has a board without anyone running `ape aboard init`.
	ProjectAboardDir = ".aboard"
	// ProjectAboardGitignore keeps the board's contents out of git while
	// leaving the DIRECTORY in it.
	//
	// The alternative — one `.aboard/` line in the repo-root .gitignore — is
	// what aboard's own docs suggest, and it is not what this does. Ignoring
	// the directory at the root would ignore this file too, so the folder
	// would not exist on a fresh clone at all. Committing only the ignore
	// file means the board's home is always there, and everything the board
	// writes into it (the document, the run directory, uploads) still stays
	// out of everyone else's diffs.
	ProjectAboardGitignore = ".aboard/.gitignore"
)

// AboardGitignore is the file written into a project's board directory. It
// ignores everything beside itself.
//
// Kept byte-identical to `_apex/.gitignore` in this repo, which is the same
// pattern for the same reason: a directory that must exist in git while none
// of its contents do.
const AboardGitignore = `# Ignore everything
*

# Allow files and folders with a pattern starting with !
!.gitignore
`

// AboardInvocation is the command name the board uses in its own messages
// when ape is the host, so an error from `Init` names something the reader
// can actually type.
//
// Duplicated from apecmd's aboardArgv0 because apecmd imports this package
// and the dependency cannot run the other way. TestAboardInvocationMatchesTheMount
// in apecmd asserts the two are the same string.
const AboardInvocation = "ape aboard"

// SkillPrefix is the filename prefix that identifies framework-managed
// skills. Anything else under .claude/skills/ is left alone by
// `ape framework update`.
const SkillPrefix = "apex-"

// OrchestratorSkill is the apex-orchestrator persona skill (PLAN-47
// Workstream B). It installs via the generic apex-* skill-copy path — no
// dedicated copy logic — but `ape doctor` checks for it as part of the
// operating-rules contract, so its name is named here for that check.
const OrchestratorSkill = "apex-orchestrator"
