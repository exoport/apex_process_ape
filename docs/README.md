# ape documentation

These docs follow the [Diátaxis](https://diataxis.fr/) framework, which splits documentation into four quadrants by user need. Pick the quadrant that matches what you're trying to do:

| If you want to…                                 | Read…                       |
| ----------------------------------------------- | --------------------------- |
| **Learn** ape by following a guided walkthrough | [Tutorials](tutorials/)     |
| **Solve** a specific problem with ape           | [How-to guides](how-to/)    |
| **Look up** a flag, command, or config field    | [Reference](reference/)     |
| **Understand** why ape is the way it is         | [Explanation](explanation/) |

The four quadrants serve different needs and are written in different styles. Tutorials and how-to guides are practical (action-oriented); reference and explanation are theoretical (cognition-oriented). Tutorials and explanation are study-oriented; how-to guides and reference are work-oriented. See the [Diátaxis compass](https://diataxis.fr/compass/) if you're unsure where a given doc belongs.

## Index

### Tutorials — _learning by doing_

_(none yet — see [tutorials/README.md](tutorials/) for planned content)_

### How-to guides — _recipes for specific problems_

- [How to install ape](how-to/install.md)
- [How to update ape](how-to/update.md)
- [How to set up the APEX framework in a project (first install)](how-to/framework-setup.md)
- [How to refresh the APEX framework in a project](how-to/framework-update.md)
- [How to pass arguments to skills](how-to/pass-args-to-skills.md)
- [How to run `ape doctor` in CI](how-to/run-doctor-in-ci.md)
- [How to verify a release before tagging](how-to/pre-tag-release.md)

### Reference — _technical descriptions_

- [Pipeline spec reference](reference/pipeline-spec.md) — schema for `_apex/pipelines/*.yaml`
- [`framework.yaml` reference](reference/framework-yaml.md) — schema for `_apex/framework.yaml`
- [Pipeline TUI keybindings](reference/tui-keybindings.md) — `ape pipeline` three-panel display, modes, and key map

### Explanation — _the why and the what_

- [Why project-local pipelines](explanation/why-project-local-pipelines.md) — design rationale for moving from embedded to on-disk specs in v0.0.6
- [Why setup and update are separate](explanation/why-setup-and-update-are-separate.md) — the v0.0.7 split of `framework update` into two commands
- [Why streaming events](explanation/why-streaming-events.md) — design rationale for live event streaming in the v0.0.7 pipeline TUI

## Contributing to the docs

When adding a new doc, place it in the quadrant that matches its primary user need, not its topic. A page about ADRs could live in any of the four quadrants depending on whether it's teaching, recipe-giving, listing facts, or explaining design. If a page mixes two purposes, split it. The Diátaxis [needs](https://diataxis.fr/needs/) and [systematic approach](https://diataxis.fr/how-to-use-diataxis/) pages are useful when you're unsure.
