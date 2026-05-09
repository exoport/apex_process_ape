# Explanation

Explanation docs answer "why" — design rationale, conceptual background, the shape of the problem ape solves. Unlike [Tutorials](../tutorials/) and [How-to guides](../how-to/), explanation is not action-oriented; unlike [Reference](../reference/), it's not exhaustive description. It's discursive. A reader of explanation is trying to deepen their understanding of the system, not get something done right now.

## Available explanation

_(none yet)_

## Planned explanation

- **What is ape.** What ape is, what APEX is, the relationship between them, and why a separate CLI exists when APEX skills can be invoked directly from Claude Code.
- **How ape works.** The pipeline runner architecture: pre-flight checks, stage chains, Claude CLI dispatch, the Bubble Tea TUI, exit-code semantics.
- **Why Go.** Why ape ships as a single Go binary instead of a Python package or a shell script. Trade-offs of that choice.
- **Pipeline design philosophy.** Why pipelines are the unit of work, not skills. Why pre-flight checks are declarative rather than discovered. Why ape doesn't auto-commit between stages.

## Writing explanation

- Take a position. Explanation reflects the project's perspective; if there are alternatives that were considered and rejected, name them and say why.
- Discuss; don't instruct. The reader is here to think alongside you, not to follow a recipe.
- Set context generously. Explanation is the right place for "before we had X, things looked like Y" or "here's why this seems weird until you know Z".
- Link to [Reference](../reference/) for facts, [How-to](../how-to/) for action.

See the [Diátaxis explanation rubric](https://diataxis.fr/explanation/).
