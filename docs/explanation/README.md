# Explanation

Explanation docs answer "why" — design rationale, conceptual background, the shape of the problem ape solves. Unlike [Tutorials](../tutorials/) and [How-to guides](../how-to/), explanation is not action-oriented; unlike [Reference](../reference/), it's not exhaustive description. It's discursive. A reader of explanation is trying to deepen their understanding of the system, not get something done right now.

## Available explanation

- [apex-v1-scope.md](apex-v1-scope.md) — what APEX v1.0 promises (code reaches the base only as a reviewed squash), the shape it is built for (one operator per checkout, local trunk), the fix queue, and what only a person does.
- [why-pty-only.md](why-pty-only.md) — why every run drives an interactive `claude` REPL inside a PTY, and why the programmatic `claude -p` axis was removed in v0.0.36.
- [chat-task-prompt.md](chat-task-prompt.md) — the three ways ape drives a Claude session (`ape chat` / `ape task` / `ape prompt`) and how to choose between them.
- [exec-modes.md](exec-modes.md) — the per-stage interactive runtime: process-spawn cost, context sharing, the step contract, and what hooks add.
- [step-completion-gates.md](step-completion-gates.md) — why a `Stop` hook alone is not proof a step finished, and the four gates that decide completion since v0.0.52.
- [why-setup-and-update-are-separate.md](why-setup-and-update-are-separate.md) — the v0.0.7 split of `framework update` into two commands.
- [framework-and-governance-clones.md](framework-and-governance-clones.md) — why `framework setup/update` keep the framework and governance clones at their newest release (v0.7.0), and where ape keeps its own clones.

## Planned explanation

- **What is ape.** What ape is, what APEX is, the relationship between them, and why a separate CLI exists when APEX skills can be invoked directly from Claude Code.
- **How ape works.** The task runner: pre-flight checks, Claude CLI dispatch over a PTY, the completion gates, exit-code semantics.
- **Why Go.** Why ape ships as a single Go binary instead of a Python package or a shell script. Trade-offs of that choice.
- **The unit of change.** Why APEX v1 replaced v0's pipelines and story ceremony with changes ape validates and records.

## Writing explanation

- Take a position. Explanation reflects the project's perspective; if there are alternatives that were considered and rejected, name them and say why.
- Discuss; don't instruct. The reader is here to think alongside you, not to follow a recipe.
- Set context generously. Explanation is the right place for "before we had X, things looked like Y" or "here's why this seems weird until you know Z".
- Link to [Reference](../reference/) for facts, [How-to](../how-to/) for action.

See the [Diátaxis explanation rubric](https://diataxis.fr/explanation/).
