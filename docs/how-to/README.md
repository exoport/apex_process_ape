# How-to guides

How-to guides are **recipes** — they answer "how do I X?" for a reader who already knows the basics. Each guide solves a specific problem in a specific context. Unlike [Tutorials](../tutorials/), how-to guides assume competence and skip the hand-holding; unlike [Reference](../reference/), they're goal-oriented rather than exhaustive.

## Available guides

- [How to install ape](install.md)
- [How to update ape](update.md)
- [How to verify a release artifact](verify.md)
- [How to pass arguments to skills](pass-args-to-skills.md)
- [How to run `ape doctor` in CI](run-doctor-in-ci.md)
- [How to verify a release before tagging](pre-tag-release.md)

## Planned guides

- **How to run ape in CI.** Non-interactive (`--no-tui`), exit code semantics, environment requirements (`ANTHROPIC_API_KEY`, etc.).
- **How to manage ADRs.** `ape adr list` / `validate` / `new` workflow; per-project ADR conventions.
- **How to manage governance patterns.** `ape pattern list` and the pattern catalog.
- **How to inspect APEX traits.** `ape trait list` / `show` / `validate` / `conflicts`.
- **How to bootstrap governance from traits.** `ape bootstrap` for new projects.

## Writing a how-to guide

- Start with the problem (the goal in the reader's words), not the solution.
- Each guide focuses on one outcome. Don't bundle unrelated tasks.
- Show only the path that works for the stated problem. If a problem branches into materially different cases, write separate guides.
- Don't teach concepts in how-to guides — link to [Explanation](../explanation/) instead.

See the [Diátaxis how-to guide rubric](https://diataxis.fr/how-to-guides/).
