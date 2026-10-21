# How to record a decision

A decision that binds later work (a language, a dependency and its version, a storage engine, a
convention every change follows) is an architecture decision record (ADR) in
`development/decisions/`. This page shows how to propose one, accept it, and replace it, and what
`ape decision standard` does with ADR-0001. The last section covers `ape next`, the list of things
noticed but out of scope. The full rules are in [cli.md](../reference/cli.md#ape-decision).

## Propose a decision

```bash
ape decision new use-sqlite
```

```text
ADR-0002  development/decisions/0002-use-sqlite.md
```

ape takes the next free number, writes the skeleton and commits it as `apex: ADR-0002 proposed`:

```markdown
# ADR-0002: Use sqlite
Status: proposed
Date: 2026-10-21

## Context
<!-- the forces at play -->

## Decision
<!-- what we decided -->

## Consequences
<!-- what becomes easier or harder -->

Enforced by: review
```

`--title "<title>"` sets the title; without it, the title comes from the slug. Replace the three
placeholders and set `Enforced by:` to the command, hook or review that holds the project to it, then
commit. That commit is yours.

ADRs are numbered `ADR-NNNN` and the files `NNNN-<slug>.md`. ADR-0001 is APEX's standard ADR
(below), so a project's own decisions start at 0002.

Point at a decision from a change's frontmatter (`decisions: [ADR-0002]`) or a requirement
(`Decisions: ADR-0002`). `ape change check` refuses a change whose `decisions:` names an ADR that
does not exist.

`ape decision new` runs on any branch. An ADR written on a change's branch reaches the base branch
with the change's squash commit at archive, and can be accepted only once it is there.

## Accept it

Accepting is a person's act, and it fixes the text. An ADR that still holds one of the skeleton's
placeholder comments records no decision yet, and accept refuses it (`decision.placeholder`), so
fill the sections in and commit first. Then:

```bash
ape decision accept ADR-0002 --by "$(git config user.name)"
```

The `Status:` line becomes `accepted`, and `_apex/state/decisions.json` records who accepted it,
when, and the SHA-256 of its text. Both are committed as `apex: ADR-0002 accepted`, on the base
branch: on a change branch, accept is refused with `branch.change-branch`.

In a model session (`APE_SESSION` or `CLAUDECODE` set, a `!` line included), accept is refused
with exit 2, `session.person-only`. Run it from a terminal outside Claude Code, or type
`accept ADR-0002` in your own Claude Code session so the model runs it that turn; `--by` is then
your `git config user.name`. See [What only a person does](../explanation/person-acts.md).

From then on the text is fixed. `ape project check` fails when anything but the `Status:` line of
an accepted ADR changes (`decision.accepted-edited`). An ADR whose `Status:` says accepted but has
no record (accepted by hand) is `decision.accepted-unrecorded`: `ape decision accept` records it
as it is, unless its text changed since the commit that first accepted it.
An ADR accepted while it still held the skeleton's placeholders (before ape refused that) is the
`ape project check` notice `decision.placeholder-accepted`: its text is fixed, so write the decision
as a new ADR that supersedes it.

## Replace it

A different decision is a new ADR that supersedes the old one:

```bash
ape decision new use-postgres --supersedes ADR-0002 --title "Store tenants in Postgres"
```

The new ADR carries `Supersedes: ADR-0002`, and ADR-0002's status becomes
`superseded by ADR-0003`, in the commit `apex: ADR-0003 proposed, supersedes ADR-0002`. The new
one is proposed until a person accepts it. An ADR that is missing or already superseded cannot be
superseded (exit 1).

## ADR-0001, the standard

Every APEX project's ADR-0001 is the same record, "Record architecture decisions": the decision
file, its sections, the proposed-to-accepted lifecycle, and change only by superseding. It is
framework-owned. Its text is the installed payload's `_apex/standards/adr-0001.md`, else ape's own
copy. `ape project init` and the lift write it, and `decisions.json` records it as accepted by
`APEX standard (<the ape command>)`, with the version and digest of the text.

`ape decision standard` keeps it there. The framework's migration `v1.0.0_seq-08` runs it, as does
the migration of any later release that changes the standard. You can run it yourself:

```bash
ape decision standard --check   # exit 0 when nothing is due, 1 when a write is
ape decision standard
```

What it does, in order:

| Action | When |
| --- | --- |
| `differs` | ADR-0001 is the project's own decision, or there are two ADR-0001 files. Nothing is done: a person decides. |
| `opted-out` | An accepted ADR supersedes ADR-0001. Nothing is done. |
| `created` | There is no ADR-0001. The standard is written. |
| `kept` | ADR-0001 is the installed standard already. |
| `replaced` | ADR-0001 is framework-owned but an older text: it is replaced by the standard. |

A write is its own commit, `apex: ADR-0001 standard <framework version>`. The command refuses on
a change branch and while a file it writes has uncommitted changes. Any session may run it: it
applies the framework's record and decides nothing.

`ape project check` shows the state as a notice: `decision.standard-stale` (a write is due),
`decision.standard-superseded`, or `decision.standard-differs`. A hand edit of ADR-0001 is
refused like any accepted ADR's. A scenario whose check names something the standard removed can
never pass again, and gets the notice `spec.check-names-removed`: a change retires it with a
REMOVED delta.

### Opting out

To keep your own decision format instead, supersede ADR-0001 with an accepted ADR:

```bash
ape decision new our-adr-format --supersedes ADR-0001
# fill it in and commit, then, as a person:
ape decision accept ADR-0003 --by "$(git config user.name)"
ape decision standard
```

`ape decision standard` then reports:

```text
ADR-0001: development/decisions/0001-record-architecture-decisions.md left alone: the project opted out of the standard (ADR-0003 accepted, superseding it)
```

Framework updates then leave ADR-0001 alone. A proposed supersede does not opt out. To opt back
in, record a new ADR that supersedes yours.

## Note what is out of scope: `ape next`

Next holds what was noticed during the work but is out of scope now: one dated line per finding,
in `development/next.md`. apex-plan triages it at the next delivery (into a milestone, kept, or
deleted with the reason in its commit). Next is not a milestone.

```bash
ape next add "the CSV export ignores --tz" --from add-tenant-export
```

```text
development/next.md: - 2026-10-21 the CSV export ignores --tz (from add-tenant-export) (committed on main)
```

The line is `- YYYY-MM-DD <text> (from <change-id>)`, without `(from …)` when `--from` is not
given. It is committed alone on the base branch as `apex: next — <text>`, from the base branch or
from a change's branch without a checkout. The change's branch and working tree do not move, and
a base moved only by such a record does not make the change catch up. apex-apply and apex-change
record what they notice here instead of building it. `ape status` lists the items under
`roadmap_next`.

The roadmap keeps one line where Next used to be, `Next — not milestones: [next.md](next.md)`. A
project made before `next.md` existed keeps Next in `development/roadmap.md` until it is moved;
until then `ape next add` keeps writing there, and `ape project check` shows the notice
`roadmap.next-inline`. The framework's migration `v1.0.0_seq-09` moves it, and so can you:

```bash
ape next move --check   # exit 0 when Next is in next.md, 1 when a move is due
ape next move
```

The move appends the items to `next.md` byte for byte and in order, each once, replaces the
roadmap's Next with the pointer line, and commits `apex: Next moved to development/next.md (seq-09)`.
Nothing else in the roadmap changes.
