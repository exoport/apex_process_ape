# Why project-local pipelines

ape v0.0.5 and earlier shipped its three canonical pipeline specs (`design`, `governance`, `epics`) compiled into the binary via `//go:embed`. v0.0.6 removes the embed and reads pipeline YAMLs from `<project>/_apex/pipelines/` at runtime. This page explains why.

## What the embedded model gave us

A self-contained binary that "just worked" with no external dependencies. Install ape, run `ape pipeline design`, and the spec was already there. For a v0 trying to prove out a workflow, that was the right shape — there was nothing to configure and no install step to get wrong.

## Where it broke

### Real projects can't customize the chain

The three embedded pipelines are an **opinion** about how to drive APEX skills. Different teams have different opinions: some want a wireframes pre-step before architecture, some run their own bespoke `apex-*` skill that the canonical pipelines don't know about, some want to run the chain with different model selections per stage. With pipelines compiled into ape, the only way to customize was to fork ape and rebuild — a non-starter.

### The framework owns the skill catalog; ape owned the chain

The skills the pipelines invoke (`apex-create-prd`, `apex-shard-doc`, etc.) live in `apex_process_framework`. The pipelines that orchestrate them lived in `apex_process_ape`. When the framework added a new skill or renamed an existing one, the pipelines couldn't follow until a new ape release shipped — and the pipelines and the skill set were drifting at different rates.

The natural home for the chain is alongside the catalog. Both live in the framework now. When the framework adds a skill, it can also publish a pipeline that uses it, in the same release.

### Skill installation had no first-class path

Even before this change, projects needed `<project>/.claude/skills/apex-*/` populated for ape to actually run anything. Installation options were: symlink the framework's skills directory, manually copy them, or rely on `apex_process_framework_eval`'s harness to inject at runtime. None of these is the right answer for a real project.

`ape framework update` solves both problems — pipeline distribution **and** skill installation — under one command. It also writes `_apex/framework.yaml`, which closes a metadata gap: now anyone reading the project tree can tell which framework version produced the installed assets.

## Why not other approaches

### User-global pipelines at `~/.apex/pipelines/`

Considered and deferred. Project-local already covers the team-customization use case (the file is in version control, all collaborators see the same chain). User-global would let one developer customize across all their projects, but at the cost of: (a) divergence between team members' local runs, and (b) no way to audit which chain was actually used for a given project. Project-local makes the chain a property of the project, not the operator. We can add a user-global tier later if there's clear demand.

### A backwards-compatibility shim that loads embedded pipelines as a fallback

Considered and rejected. The shim would be invisible — a developer running `ape pipeline design` on a fresh-cloned project that hadn't run `ape framework update` would get the embedded pipelines silently and not know there was a missing step. Failing loudly with `pipeline "design" not found at _apex/pipelines/design.yaml — run "ape framework update"` makes the install step discoverable.

### A scaffolding command (`ape pipeline new`) that creates customized pipelines from scratch

Out of scope for v0.0.6. Once `_apex/pipelines/` is on disk, customization is `cp _apex/pipelines/design.yaml _apex/pipelines/my-flow.yaml && $EDITOR _apex/pipelines/my-flow.yaml`. A scaffolding helper might land later if the boilerplate becomes painful.

## What this means for a project

After `ape framework update`, your project has:

- A copy of the canonical pipelines in `_apex/pipelines/` (committed to your repo).
- A copy of all `apex-*` skills in `.claude/skills/` (also committed).
- A `_apex/framework.yaml` recording exactly which framework SHA those copies came from (committed).

Because the pipelines are files in your repo, you can:

- Add new ones (`_apex/pipelines/my-flow.yaml`) without forking ape.
- Audit changes via git diff on the framework version bump.
- Run `ape framework status` to see whether your installed framework version has fallen behind the framework HEAD.

The cost: there's an extra install step beyond `apt install ape` (or its equivalent). That cost is paid once per project and once per framework version bump, and the alternative — embedded specs — is what we just spent a release migrating away from.

## Trade-offs we accepted

- **Schema lock-in.** Once projects are checking pipeline YAMLs into git, the schema becomes external API. Adding a field is fine; renaming or removing one needs a deprecation cycle. The shape is small enough that this is a manageable cost.
- **More moving parts at install time.** v0.0.5 install was "download binary". v0.0.6 install is "download binary, then run `ape framework update`". We document this prominently in [How to install ape](../how-to/install.md#next-steps).
- **Coupled releases.** A framework rev that ships a new pipeline shape (e.g., new step field) requires a compatible ape rev. We handle this by versioning both repos and recording the pinned versions in `framework.yaml`.

## Related

- [How to install the framework](../how-to/framework-update.md) — the practical recipe.
- [Pipeline spec reference](../reference/pipeline-spec.md) — the YAML schema.
- [`framework.yaml` reference](../reference/framework-yaml.md) — the metadata file.
