# How to keep cost pricing current

`ape` turns the token counts in a Claude Code transcript into dollars using a
price table compiled into the binary. Anthropic publishes no price API, so
that table is hand-curated — and Claude Code releases on a schedule `ape` does
not control. A model id can therefore change under an installed `ape` at any
time.

When it does, the failure is quiet: token counts stay perfectly correct and
the dollars go to zero. This page covers how `ape` makes that visible, and
what to do when it happens.

## The short version

```bash
ape costs coverage          # does my price table cover what Claude Code is emitting?
ape costs reprice           # what would correcting it change? (dry run)
ape costs reprice --write   # apply, then:
ape costs roll              # refresh the rollup cache
```

## How a gap surfaces

You do not have to go looking. A model with no exact rate is reported at three
points, from soonest to broadest:

| When | Where |
|---|---|
| The step that used it, as it finishes | `⚠ telemetry:` on stderr, and `telemetry_note` on that step in `manifest.yaml` |
| Any time you read costs | A warning under `ape costs`, `ape costs run`, `ape costs prompt` |
| On demand | `ape doctor` → the `cost.price_table_coverage` check |

Each says which model, how many turns, and that the total is a lower bound.

`ape doctor` reads a **1-hour cache** for this check, because a full sweep reads
every transcript in the window (hundreds of megabytes on an active machine). A
cached verdict always says so — `[cached 12m ago; ape costs coverage re-checks]`
— and the cache is keyed on the price table's own stamp, so upgrading `ape`
invalidates it immediately rather than waiting out the hour. Anything that
*gates* (`ape costs coverage`, `--strict`, `make check-prices`) always sweeps
fresh. `APE_COVERAGE_CACHE_TTL=0` disables it.

## The three pricing states

`ape costs coverage` classifies every model it finds in your local
transcripts:

- **`exact`** — a rate for this exact model id, from the built-in table, a
  dated promotional window, or your own `~/.ape/prices.yaml` override. This is
  the only state that is fully trustworthy.
- **`family`** — no exact row, so the model was priced from its family tier
  (any `claude-opus-*` at the Opus rate, and so on). The number is the right
  order of magnitude and is always labelled an estimate. It exists so a model
  released after your `ape` build does not report as free.
- **`unpriced`** — nothing matched. Those turns contribute `$0.00`, which is
  not the same as being free.

## Closing a gap

`ape costs coverage` prints the YAML rows to add, pre-filled with its own
estimate:

```
to close the gap, add the exact rate(s) to internal/cost/prices.yaml:
  claude-opus-6:
    base_input: 5.00
    output: 25.00
```

**Confirm every rate against
<https://platform.claude.com/docs/en/about-claude/pricing> before using it** —
the printed values are a guess from the family tier, not a published rate.

You then have two routes:

**Locally, no rebuild** — put the confirmed rows in a file and persist them.
Overrides win over the built-in table for every later `ape` invocation:

```yaml
# newprices.yaml
prices:
  claude-opus-6:
    base_input: 5.00
    output: 25.00
```

```bash
ape costs update --from newprices.yaml
```

An override may carry `effective_from: 2026-09-01` to apply only to turns at
or after a date — use it when a rate changes rather than when a model appears.

Overrides cover **rates only**. Which generation a bare `sonnet` resolves to
lives in the `aliases:` block, which `--from` does not read — correcting that
needs a new `ape` build.

A row with a zero or negative rate is **rejected**, not applied — on write by
`ape costs update`, and on read, where the row is dropped and the built-in rate
used instead. A misspelled key (`base_imput:`) unmarshals to zero, and because
an override outranks the built-in table, honouring it would price that model at
$0. `ape costs coverage` and `ape doctor` list any row that was ignored.

**For everyone** — add the same rows to `internal/cost/prices.yaml` and cut a
release. `make check-prices` gates this: it fails the local CI run while any
model your Claude Code is emitting lacks an exact rate.

## Repairing runs recorded during the gap

Correcting the table only fixes runs from that point on. Past runs keep the
cost that was computed when they ran — but not permanently, because every run
artefact stores its full per-model token breakdown alongside the dollars.

```bash
ape costs reprice           # preview: per-artefact old → new
ape costs reprice --write   # rewrite cost_usd in place
ape costs roll              # rebuild the rollup cache from the corrected files
```

Repricing recomputes from stored tokens using the current table, so the result
is exact rather than an approximation. Only `cost_usd` scalars are rewritten;
comments, key order, and every other field survive. A model that is *still*
unpriced is left alone and named in the report — repricing cannot invent a
rate it does not have.

## Model names you can write

Anywhere `ape` takes a model — `--model` on `ape task` / `ape prompt` /
`ape chat`, or `model:` at the step, stage, or pipeline level of a spec — these
all work:

| You write | Resolves to |
|---|---|
| `sonnet`, `Sonnet`, `claude-sonnet` | `claude-sonnet-5` — the family's current generation |
| `opus`, `Opus`, `claude-opus` | `claude-opus-5` |
| `sonnet-5`, `claude-sonnet-5` | `claude-sonnet-5` — as written |
| `claude-sonnet-4.6`, `claude_sonnet_4_6` | `claude-sonnet-4-6` |
| `opus[1m]`, `Opus[1m]` | `claude-opus-5[1m]` — the suffix rides along |

Write the bare family when you want "whatever the current one is"; write an
explicit generation to pin it. Either way `ape` substitutes a concrete id
before spawning, so the model a run used is recorded exactly and the manifest's
per-model attribution matches the transcript's.

Which generation a bare family maps to lives in the `aliases:` block of
`internal/cost/prices.yaml`.

A name `ape` does not recognize is passed to Claude Code unchanged with a
warning — a model newer than your `ape` must not be blocked by `ape`'s table,
so Claude Code gives the final verdict.

That warning covers specs too, not just flags. `ape pipeline` and the apescript
pipeline runner report any unrecognized `model:` before anything spawns, naming
the exact location (`stage "review" step 2 (apex-review-code)`), and `ape doctor`
validates every installed spec without running one. A typo like
`claude-sonet-5` used to stay hidden until claude rejected it mid-run.

### Alias drift

Because a bare family word now *selects* a model rather than deferring to
Claude Code, a stale `aliases:` entry picks the **wrong model**, not just the
wrong price. `ape costs coverage` therefore checks it: if a **newer**
generation of a family is in use locally than the alias names, it reports

```
⚠ family alias drift
    opus     → claude-opus-5    superseded by: claude-opus-6
```

This fails `--strict`, so `make check-prices` and the release gate catch it,
and `ape doctor` warns.

The condition is deliberately *"a strictly newer generation is in use"*, not
*"the alias target is missing"*. Absence proves nothing: if you pin
`claude-opus-4-8` in every spec, `claude-opus-5` never appears, and flagging
that would fail your release gate over a deliberate choice. Ordering comes from
parsing the numeric segments of `claude-<family>-<major>[-<minor>]`, so
`claude-opus-5` correctly outranks `claude-opus-4-8` — the pair a string sort
gets backwards. An id that does not parse that way (a legacy
`claude-3-5-sonnet`, a future scheme) yields no comparison and therefore no
claim; silence on something we cannot order is the correct answer.

## Why this can't be solved at release time alone

`make check-prices` runs during the release flow and verifies the table
against the Claude Code installed on the release machine. That is worth doing,
but it can only prove the table was current *at that moment* — a Claude Code
release the next day invalidates it, and no gate in `ape`'s release can
prevent that.

That is why the durable protections run continuously instead: the per-step
note, the warning on every cost report, the `ape doctor` check, the family
tier that keeps a new model in the right order of magnitude, and `ape costs
reprice` to repair the record afterwards.

## See also

- [How to read run artefacts](run-artefacts.md) — where `cost_usd`,
  `model_usage`, and `telemetry_note` live
- [Pipeline run manifest reference](../reference/pipeline-run-manifest.md)
- [CLI reference](../reference/cli.md) — full flags for `ape costs`
