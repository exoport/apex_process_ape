# Reference

Reference docs are **information** — exhaustive, accurate, neutral. They describe what exists; they don't teach (that's [Tutorials](../tutorials/)) and they don't recommend (that's [How-to guides](../how-to/) and [Explanation](../explanation/)). A reader consults reference when they need a specific fact.

For ape, reference is the surface area: every command, every flag, every config field, every exit code.

## Available reference

_(none yet)_

## Planned reference

- **Command reference.** Every `ape` subcommand, every flag, every argument. Auto-generatable from cobra (`cobra-cli docs`) — likely the right path once the command surface stabilizes.
- **Pipeline catalog.** Every built-in pipeline (`design`, `governance`, `epics`, …) with its stages, the skills each stage runs, and the pre-flight files each pipeline expects.
- **Configuration reference.** The `_apex/config.yaml` schema: every field, type, default, and where it's consumed.
- **Trait reference.** Available APEX traits, their conflicts, and the artifacts they produce.
- **Exit codes.** What each non-zero exit means and which command emits it.

## Writing reference

- Match the structure of the thing you're documenting (commands → command-shaped pages, config → field-shaped pages).
- Be exhaustive within the topic — don't leave out edge cases.
- Be neutral. No recommendations, no opinions, no "you might want X". Save those for [Explanation](../explanation/) or [How-to guides](../how-to/).
- Cross-reference: reference pages should link to the related how-to and explanation pages, not duplicate them.

See the [Diátaxis reference rubric](https://diataxis.fr/reference/).
