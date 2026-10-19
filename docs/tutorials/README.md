# Tutorials

Tutorials are **lessons** — they teach a beginner how to do something by walking them through a complete, working example. A reader following a tutorial should arrive at a known good outcome without making decisions about which path to take. Tutorials are not the place to explain why something works; that's [Explanation](../explanation/). They're not lookup tables either; that's [Reference](../reference/).

Good ape tutorials hold the reader's hand from "I haven't used ape before" to "I have a working APEX project I built myself."

## Available tutorials

- [Your first APEX v1 project](first-v1-project.md) — doctor → `framework setup` → `project init` → project.md → a milestone a person approves → a green `project check` and `doctor --strict`

## Planned tutorials

- **Lift a v0 project.** `ape framework update` to v1 on an APEX v0 project, which runs `ape project lift` (the branch needs an upstream, and the `apex-v0-final` tag pushed to it). End state: the v1 layout in one commit, with the disposition list a person reviews.

## Writing a tutorial

- One linear path. No "if you want X, do Y instead" branches.
- Concrete commands the reader copy-pastes.
- Verifiable checkpoints (`ape version` should print this; `ape project check` should be green).
- A clear end state — readers know they're done when they see X.

See the [Diátaxis tutorials guide](https://diataxis.fr/tutorials/) for the full rubric.
