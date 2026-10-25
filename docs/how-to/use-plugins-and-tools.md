# How to use plugins and tool skills

An APEX v1 project can use Claude Code plugins, and can give the loop's design and review steps
extra skills to call, its tool skills. Both are declared in `_apex/config.yaml`, and ape installs,
enables and checks them. This page shows how to declare a plugin, accept the change, install it,
check it, give a step a tool skill, give the sessions an expert's rules, and pin a plugin's version.
[`_apex/config.yaml`](../reference/config-yaml.md#marketplaces-and-plugins) lists every field.

You need an initialised v1 project (`ape project init`) and `claude` on `PATH`: ape installs
plugins through the `claude plugin` CLI.

## Declare a marketplace and a plugin

A plugin comes from a marketplace. Name the marketplace under `marketplaces:` and list each plugin
under `plugins:`:

```yaml
marketplaces:
  team-tools: {source: github, repo: org/claude-plugins, ref: v2}
plugins:
  - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
  - {name: kb, marketplace: team-tools, allow: [mcp]}
```

- `source` is `github` (with `repo: <owner>/<repo>`), `git` (with `url:`) or `directory` (with
  `path:`, relative to the project root).
- A plugin is `<name>@<marketplace>` to Claude Code, and each is listed once.
- `ref` is optional on both. [Pin a plugin's version](#pin-a-plugins-version) explains it.
- `allow` lists what the plugin may carry besides skills: `hooks`, `mcp`, or both. Leave it out
  for a plugin that carries only skills.

Commit the change:

```bash
git add _apex/config.yaml
git commit -m "chore: use the team-tools plugins"
```

## Accept the change

`marketplaces`, `plugins` and `tools` are guarded blocks: they decide what runs inside a session.
A commit that changes one and is not ape's own is the finding `config.changed`, and until it is
cleared `ape change start`, `verify` and `archive` refuse with `change.provenance`:

```bash
ape project check
```

```text
  _apex/config.yaml: guarded config keys (marketplaces, plugins, tools) changed by commit 44c613a ("chore: use the team-tools plugins"), which is not ape's — … a person reverts it (git revert 44c613a), or accepts it: `ape project accept-config --by <who>`
```

Accepting is a person's act. From your terminal, on the base branch with nothing staged:

```bash
ape project accept-config --by "$(git config user.name)"
```

```text
config accepted: 7baa3ad
```

ape commits an empty `apex: config accepted by <who>`, and every guarded change before it is clean.
A later change to the blocks is judged again, so adding a plugin, an `allow:` value or a tool
later needs another acceptance. A model session is refused (`session.person-only`) unless you typed
"accept the config" in your own Claude Code session.
[What only a person does](../explanation/person-acts.md#accepting-the-config) explains why.

## Install and enable them

```bash
ape plugins sync
```

For each plugin the config lists, `ape plugins sync`:

1. adds its marketplace (`claude plugin marketplace add … --scope project`);
2. installs a missing plugin at project scope (`claude plugin install <name>@<marketplace> --scope project`),
   or, for an installed one off its `ref`, refreshes the marketplace and updates it;
3. writes `extraKnownMarketplaces` and `enabledPlugins` into `.claude/settings.json`, keeping every
   other key;
4. commits `.claude/settings.json`, and only that file, as `apex: plugins` when it changed.

It prints one `plugin` line per action, and then lists the findings `ape plugins status` reports
(`plugin.hooks-or-mcp`, `tools.*`), which do not fail it. A second run installs, writes and commits
nothing (`plugins: nothing to do`). `--json` gives the actions, the settings it changed, the commit
and the findings.

| Exit | Meaning |
| ---- | ------- |
| 0 | Synced |
| 1 | A plugin is still missing or off its ref (`plugins.missing`, with the command to run), the config is malformed (`plugins.config`), or the commit failed |
| 2 | Usage |

A directory marketplace's path is written into `settings.json` as an absolute path, so it only
works on the machine that wrote it. Removing a plugin from `plugins:` leaves its `settings.json`
entries as they were; ape does not uninstall it.

Other commands do the same sync:

- **`ape doctor --fix`** syncs the plugins and commits the settings before its checks run. It fixes
  nothing else. A failed sync is printed and the checks still run.
- **`ape project init`** installs the listed plugins, and commits `apex: plugins`.
- **`ape framework update`** installs or updates them, and its own update commit carries
  `settings.json`. A plugin it cannot put right is a warning, not a failed update.
- **`ape task`** installs a missing plugin, or updates one off its ref, before it spawns. It leaves
  `.claude/settings.json` as it was and enables the plugins for that session inline. A plugin still
  missing afterwards refuses the session with `plugins.missing` and the command to run.

## Check them

```bash
ape plugins status
```

On a project with two plugins installed it prints something like this (the versions and counts
are illustrative):

```text
plugin  nats-expert@team-tools  installed 1.4.0, enabled
        3 skill(s), 1 model-invocable
plugin  kb@team-tools  installed 0.9.2, enabled
        2 skill(s), 2 model-invocable; carries MCP servers (allowed)
tool    review/security  security-lens  ok  project <root>/.claude/skills/security-lens/SKILL.md
```

For each listed plugin it reports:

- whether it is installed at project scope, and its version against `ref` (`off ref <ref>` when
  they differ);
- whether `settings.json` enables it;
- how many skills it adds, and how many of them are model-invocable (not
  `disable-model-invocation: true`). Each model-invocable skill's description is listed in every
  session, so this is the plugin's cost per session;
- whether it carries hooks or MCP servers, and whether its `allow:` covers them.

For each `tools:` entry it reports where the skill was found (`plugin`, `project` or `user`, with
the path) and its state: `ok`, `not-installed`, `not-invocable`, or `unchecked` (a plugin skill
while `claude` is not on `PATH`, so the installs cannot be read).

`ape plugins status --json` prints:

```text
{"installs_checked", "plugins": [{"id", "ref", "installed", "version", "at_ref", "enabled",
"install_path", "skills", "model_invocable", "hooks", "mcp", "allow", "allowed"}],
"tools": [{"slot", "name", "skill", "source", "path", "state"}],
"findings": [{"file", "message", "code", "fix"}]}
```

`installs_checked` is false when `claude` is not on `PATH`. A tool row also carries the entry's
`args`, `model` and `effort` when it sets them.

The findings, each with the fix it names:

| Code | Means | Fix |
| ---- | ----- | --- |
| `plugins.config` | A `marketplaces:` or `plugins:` entry is malformed: an unknown source, a missing `repo`/`url`/`path`, a `ref` on a directory marketplace, a plugin listed twice, an `allow:` value other than `hooks` or `mcp` | Fix the config |
| `plugins.drift` | `settings.json`'s entries differ from the config, or a plugin is not installed at project scope or not at its ref | `ape plugins sync` or `ape doctor --fix` |
| `plugin.hooks-or-mcp` | An installed plugin carries hooks (`hooks/hooks.json`, or `hooks` in `.claude-plugin/plugin.json`) or MCP servers (`.mcp.json`, or `mcpServers` in `plugin.json`) its entry does not allow | A person reviews them and adds `allow: [hooks]`, `[mcp]` or `[hooks, mcp]`, or drops the plugin |
| `plugins.user-scope` | A listed plugin is also installed or enabled at user scope (`claude plugin list` scope `user`, or `enabledPlugins` in your own `settings.json`), or a `tools:` skill is a user skill. The user copy is not pinned by the project's `ref` and stands in for the project's on this machine | A person removes the user copy (`claude plugin uninstall <id> --scope user`), or moves the skill into the project's `.claude/skills/` |
| `tools.invalid`, `tools.not-installed`, `tools.not-invocable`, `tools.too-big` | A `tools:` problem | [Below](#give-a-step-a-tool-skill) |

`ape plugins status` exits 0 whatever it finds (1 when the config cannot be read). The gate is
`ape project check`, which reports the same findings and exits 1.

`ape doctor` has a row for them, `plugins.project`:

```bash
ape doctor --only plugins.project
```

It is SKIP outside a v1 project or when the config lists no plugins, and OK when every plugin is
installed at its ref and `settings.json` is in step. When `claude` is not on `PATH` it checks
`settings.json` only, and warns. Drift is a WARN with the findings and the remediation
`ape doctor --fix --cwd <project>`. The row is not required, so it fails `ape doctor` only under
`--strict`.

## Give a step a tool skill

The `tools:` block gives the design step and the review step skills to call, each with the
condition that calls for it:

```yaml
tools:
  design:
    - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
       args: "{change} --out {assets}", model: sonnet, effort: high}
  review:
    - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing"}
```

An entry is `{name, skill, when}`, with names unique across the slots (design, review and [advise](#give-the-sessions-an-experts-rules)), and three optional keys:

- `args`: the template ape fills into `ape task <skill> --args "…"`, with the placeholders `{id}`
  (the change id), `{task}` (the task), `{assets}` (the change's `development/changes/<id>/assets`)
  and `{change}` (its `change.md`), and no other. Without it, the args are `--change {id}`, plus
  `--task {task}` when the run is given `--task`;
- `model`;
- `effort`: `low`, `medium`, `high`, `xhigh` or `max`.

The skill is one of:

- `<plugin>:<skill>`, a skill of a plugin listed in `plugins:`;
- a project skill, `.claude/skills/<skill>/SKILL.md`, or a project command,
  `.claude/commands/<skill>.md`;
- a user skill or command, the same two paths under `$CLAUDE_CONFIG_DIR`, else `~/.claude`.

ape looks under the user's `.claude` first, then the project's, and in each for the skill before
the command. The first found wins, so a user skill or command wins over a project one of the same
name.

It must be installed and slash-invocable (not `user-invocable: false`), because ape types it as a
slash command. Any entry may also carry `paths:` and `touches:` ([below](#let-ape-check-when)).
Commit the block and [accept it](#accept-the-change) like any guarded change, run
`ape plugins sync` if it names a new plugin, and check it with `ape plugins status`.

### How the loop uses it

- **apex-change** reads each tool's `when:`. A change that matches a design tool gets a task naming
  it, `- T<n> <what> — tool: <name> — check: <command> | human-review`. Review tools that match go
  in the change's frontmatter as `review_tools: [<name>, …]`. `ape status --json` lists them per
  change as `tools` and `review_tools` ([status JSON](../reference/status-json.md#changes)).
- **apex-loop** runs a design tool after `ape change start` and before the change's first
  apex-apply, and each review tool once the change is verified, before its review. The review tool
  writes its notes into the change's `assets/`, and apex-review reads them.
- The loop runs a tool with `ape change tool`. ape fills the args, model and effort from the entry,
  and starts `ape task <skill>` detached:

```bash
ape change tool <id> ui-mockup --task T3 --wait 9m --json
ape change tool <id> security --json
```

A plugin tool starts the same way. `ape task` finds `<plugin>:<skill>` as `ape plugins status`
does: the plugin is listed in `plugins:`, installed, and has the skill. So a tool the status calls
`ok` also runs. The session is typed `/<plugin>:<skill>`, and the run is filed under
`tasks/<plugin>__<skill>/`, because Windows does not allow a `:` in a folder name. When the skill
cannot be found, `ape task` stops with exit 2 before it starts a session:

- the plugin is not listed, or not installed: list it in `plugins:` and run `ape plugins sync`, or
  fix the skill name;
- the plugin has no skill of that name: fix the name, or install a version of the plugin that has
  it.

The session writes what its skill writes, and its exit is the result. `--wait` (default 9m) bounds
one call: exit 75 means it is still running, and calling the same command again attaches to it.
Without an entry's `model:` or `effort:`, the session takes `models.<skill>`, then the asking
session's, then the config's `effort:`, then Claude Code's own.

| Exit | Meaning |
| ---- | ------- |
| 0 | The session ended 0 |
| 1 | `tool.unknown` (no entry has that name), `tool.args` (a filled value is not one shell-safe token), `tool.failed` (the session ended non-zero or was lost), or the change was not found |
| 2 | Usage: no such task, a template using `{task}` without `--task`, or a `--wait` outside 0–9m30s (`usage.wait`) |
| 75 | Still running: call it again |

### The allow-list

Every skill `tools:` names joins the loop's allow-list, beside `apex-plan`, `apex-change`,
`apex-apply`, `apex-review`, `apex-accept` and the `human_review:` delegate. The
loop cannot start a skill that is not on it (`session.not-allowed`), and only a valid entry adds
one, and never `apex-release`: an entry naming it is `tools.invalid` (D524). [Nesting](../explanation/orchestrator-and-loop.md#nesting-as-it-applies-to-the-loop) has the rule.

### The findings

| Code | Means | Fix |
| ---- | ----- | --- |
| `tools.invalid` | The block is malformed: an unknown slot, an entry missing `name`, `skill` or `when`, an unknown key, a skill that is not `<skill>` or `<plugin>:<skill>`, a name used twice, an unknown placeholder in `args`, an effort that is not a level. Only the valid entries are used | Fix the block |
| `tools.not-installed` | No skill of that name: the plugin is not listed in `plugins:`, not installed or has no such skill, or there is no project or user skill or command | List the plugin and run `ape plugins sync`, add the skill, or fix the name |
| `tools.not-invocable` | The skill has `user-invocable: false` | Drop the flag, or name another skill |
| `tools.unknown` | A change's `— tool: <name>` task, `review_tools:` entry or `advise:` entry names no declared tool (`ape change check`) | Re-plan the change with a declared tool |
| `tools.too-big` | An advise entry's files are over `caps.advise`, or what one session of a change reads is over `caps.advise_session` | List fewer advise entries; a person shortens the file, narrows `for:` or raises the cap |

A plugin skill is judged only when `claude` is on `PATH`.

## Give the sessions an expert's rules

An expert skill can ship a short rules file, `advise.md` say, that the sessions doing the work read
before they work. List it under `tools.advise`:

```yaml
tools:
  advise:
    - {name: nats, skill: nats-expert:nats-expert, when: "the change publishes, consumes or configures NATS",
       paths: ["internal/bus/**"], load: [advise.md], for: [change, apply, review]}
```

- `load` names files relative to the skill's folder (the folder holding its `SKILL.md`). It is
  required. For a command that folder is the whole `.claude/commands/`, shared with every other
  command, and every `.md` file there is itself a command. Ship advise files in a skill folder
  ([config reference](../reference/config-yaml.md#an-advise-entry-on-a-command)).
- `for` names the sessions that read them: `change`, `apply`, `review`. It defaults to all three.
  A delegate never reads advise.
- An advise entry starts no session and gives no verdict, so it takes no `args`, `model` or
  `effort`, and its skill need not be slash-invocable. Give the skill
  `disable-model-invocation: true` so its description is not listed in every session.

`ape plugins status` lists each advise entry's files with their size and sha256:

```text
tool    advise/nats  nats-expert:nats-expert  ok  plugin <cache>/nats-expert/0.1.0/skills/nats-expert/SKILL.md
        load advise.md  7312 bytes  sha256 9f2c41d07a1b  for change, apply, review
```

A file missing from the installed skill is `tools.not-installed`. The files of one entry together
may be at most `caps.advise` (8 KB by default), else `tools.too-big`.

### How a change uses it

1. **apex-change** reads each advise entry's `when:` and lists the ones the change matches in the
   change's frontmatter, `advise: [nats]`. `ape change check` refuses a name `tools.advise` does not
   declare (`tools.unknown`), and a session that would read more than `caps.advise_session` (16 KB
   by default; `tools.too-big`).
2. **The sessions** find the files in `ape status --json`, under `changes[].advise`: one row per
   file, `{name, skill, for, file, path, bytes, sha256}`, with `path` absolute
   ([status JSON](../reference/status-json.md#changesadvise)). A session whose role `for` names
   Reads them before it works.
3. **`ape change verify`** records each file's sha256 in `state.json`, so an expert updated in the
   middle of a change shows in the record.

ape never reads the files for meaning: it resolves, sizes and hashes them.

## Let ape check when

`when:` is prose, so whether a change matches is the planner's judgement. Add `paths:` (globs) and
`touches:` (capability slugs) to any entry, design, review or advise, and ape checks it:

```yaml
tools:
  review:
    - {name: security, skill: security-lens, when: "the change touches auth", paths: ["internal/auth/**"], touches: [auth]}
```

A change that does not list the entry gets the notice `tools.unmatched` when:

- one of its `touches:` names a slug of the entry's;
- one of its planned `paths:` meets a glob of the entry's (`ape change check`);
- a file its commits changed matches a glob of the entry's (`ape change verify`).

```text
notice: development/changes/add-login/change.md:1: tools.review entry security matches this change (internal/auth/login.go matches internal/auth/**) and the change does not list it — if its when: ("the change touches auth") applies, list it: review_tools: [security]; if not, leave it: a warning, never a refusal
```

A notice never changes the exit: a docs-only edit under `internal/auth/` may rightly not need the
tool. With `--json` the notices are under `notices`.

## Pin a plugin's version

Claude Code cannot pin a plugin version from the consumer side: a marketplace serves one version of
each plugin. Pin in two places:

```yaml
marketplaces:
  team-tools: {source: github, repo: org/claude-plugins, ref: v2}   # the catalog's branch or tag
plugins:
  - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}       # the version the install must report
```

- **The marketplace's `ref`** picks the catalog's branch or tag, for `github` and `git` sources. ape
  writes it as the source's `ref` in `settings.json`, and as `<repo>#<ref>` to `claude plugin`. A
  directory marketplace has no `ref`.
- **The plugin's `ref`** is the version its install must report: its `plugin.json` `version`, else
  the marketplace entry's, else the source commit. A leading `v` is ignored on either side, and a
  commit (7 or more hex digits) matches by prefix. An empty `ref` accepts any version.

When a plugin is off its `ref`, `ape plugins sync` (and `ape task`, `ape doctor --fix`) refreshes
the marketplace and updates it. If the marketplace does not serve that version, the plugin stays
off it: the sync exits 1 with `plugins.missing` and the command to run, and `ape task` refuses the
session. Move the marketplace's `ref` to a catalog that serves the version, or change the plugin's
`ref`. Either is a guarded change to [accept](#accept-the-change) again.

### See what is newer

`ape plugins status --outdated` lists, for each listed marketplace, the version tags of its
repository newer than its `ref`. It reads them with `git ls-remote --tags`, and fetches, installs
and writes nothing:

```yaml
marketplaces:
  experts: {source: github, repo: exoar/apish_experts_plugin, ref: v0.1.0}
```

```text
market  experts  ref v0.1.0  up to date (latest v0.1.0)
```

A newer tag shows as `newer: v0.2.0`. A tag counts when it is a version (`v1.2.0` or `1.2.0`);
pre-releases are left out. A marketplace with no `ref`, or one on a branch, gets a note naming its
latest tag instead, and a directory marketplace has none:

```text
market  experts  ref main  ref main is not a version tag; pin a tag to compare (latest v0.2.0)
market  tools  ref (no ref)  no ref: the marketplace follows its default branch; pin a tag (ref: v0.2.0) to compare
```

With `--json` they are under `outdated`: `[{marketplace, source, ref, latest, newer, note,
error}]`. Bumping `ref` is a guarded change to [accept](#accept-the-change).
