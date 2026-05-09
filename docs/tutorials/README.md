# Tutorials

Tutorials are **lessons** — they teach a beginner how to do something by walking them through a complete, working example. A reader following a tutorial should arrive at a known good outcome without making decisions about which path to take. Tutorials are not the place to explain why something works; that's [Explanation](../explanation/). They're not lookup tables either; that's [Reference](../reference/).

Good ape tutorials hold the reader's hand from "I haven't used ape before" to "I have a working APEX project I built myself."

## Available tutorials

_(none yet)_

## Planned tutorials

- **Your first APEX pipeline.** Greenfield walk-through: install ape, scaffold a tiny project, run `ape pipeline design`, then `governance`, then `epics`. End state: a project with a real PRD, architecture, and an epic story ready to implement.
- **Lift a brownfield project.** Use `ape pipeline lift` (when available) on an existing codebase. End state: a brownfield repo with APEX-aware planning artifacts derived from its current state.

## Writing a tutorial

- One linear path. No "if you want X, do Y instead" branches.
- Concrete commands the reader copy-pastes.
- Verifiable checkpoints (`ape version` should print this; `ls development/planning/` should show these files).
- A clear end state — readers know they're done when they see X.

See the [Diátaxis tutorials guide](https://diataxis.fr/tutorials/) for the full rubric.
