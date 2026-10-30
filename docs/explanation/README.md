# Explanation

Explanation docs answer "why" — design rationale, conceptual background, the shape of the problem ape solves. Unlike [Tutorials](../tutorials/) and [How-to guides](../how-to/), explanation is not action-oriented; unlike [Reference](../reference/), it's not exhaustive description. It's discursive. A reader of explanation is trying to deepen their understanding of the system, not get something done right now.

## Available explanation

- [apex-v1-scope.md](apex-v1-scope.md) — what APEX v1.0 promises (code reaches the base only as a reviewed squash), the shape it is built for (one operator per checkout, local trunk), the fix queue, and what only a person does. Start here.
- [person-acts.md](person-acts.md) — the acts only a person does, why, and the two ways a person does them: a plain terminal, or the typed phrase in their own Claude Code session.
- [security-model.md](security-model.md) — what ape's guards are for, where they stop (a steered model), and the known limits in 1.4.1 with what to do meanwhile: person acts and pushes through a script, the inherited environment, unscanned transcripts, expert plugins.
- [orchestrator-and-loop.md](orchestrator-and-loop.md) — `/apex-orchestrator`, the person's own session, versus `ape task apex-loop`, the unattended runner; the relay, the nesting rule and delegates as the loop meets them.
- [nested-ape-runs.md](nested-ape-runs.md) — the nesting rule: depth 0/1/2, which skills a loop may start, and how ape knows.
- [chat-task-prompt.md](chat-task-prompt.md) — the three ways ape drives a Claude session (`ape chat` / `ape task` / `ape prompt`) and how to choose between them.
- [why-pty-only.md](why-pty-only.md) — why every run drives an interactive `claude` REPL inside a PTY, and what hooks add.
- [step-completion-gates.md](step-completion-gates.md) — why a `Stop` hook alone is not proof a step finished, and what decides completion.
- [framework-updates-from-releases.md](framework-updates-from-releases.md) — why setup and update install a release tag, how `min_ape_version` is checked, and blocking migrations.
- [framework-and-governance-clones.md](framework-and-governance-clones.md) — why `framework setup/update` keep the framework and governance clones at their newest release, and where ape keeps its own clones.
- [why-setup-and-update-are-separate.md](why-setup-and-update-are-separate.md) — why installing and refreshing the framework are two commands.

## Planned explanation

- **What is ape.** What ape is, what APEX is, the relationship between them, and why a separate CLI exists when APEX skills can be invoked directly from Claude Code.
- **How ape works.** The task runner end to end: pre-flight checks, Claude CLI dispatch over a PTY, the completion gates, exit-code semantics.
- **Why Go.** Why ape ships as a single Go binary instead of a Python package or a shell script. Trade-offs of that choice.

## Writing explanation

- Take a position. Explanation reflects the project's perspective; if there are alternatives that were considered and rejected, name them and say why.
- Discuss; don't instruct. The reader is here to think alongside you, not to follow a recipe.
- Set context generously. Explanation is the right place for "before we had X, things looked like Y" or "here's why this seems weird until you know Z".
- Link to [Reference](../reference/) for facts, [How-to](../how-to/) for action.

See the [Diátaxis explanation rubric](https://diataxis.fr/explanation/).
