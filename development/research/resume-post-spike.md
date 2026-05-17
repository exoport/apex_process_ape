# Continuation Prompt — post UI spike, pre-PLAN-5

Use this prompt to resume work in a fresh session. The UI-stack selection spike
is **complete** and the design doc is updated. Two pieces of work remain before
PLAN-5 drafting begins:

1. Commit the spike + doc updates in a sensible structure (working tree is dirty).
2. Draft `plan-5_ape-chat-and-pipeline-web.md` per
   [`resume-mcp-bridge-plan.md`](resume-mcp-bridge-plan.md) — which is now
   unblocked because the frontend stack is locked.

This repo is `/home/diegos/_dev/github/diegosz/apex_process_ape`.

---

## What just happened

A 2–3 hour spike built the same pipeline-progress screen in three Go programs
(vanilla / htmx / hybrid), measured them along LOC / build steps / diff-per-change
/ gzipped bundle / ergonomics, and **redirected the design doc's expected
GOAT-hybrid pick to htmx-only**. Verdict:
[`ui-spike.md`](ui-spike.md). Spike code: [`../spikes/ui/`](../spikes/ui/).

Headline metrics that drove the call:

| | vanilla | **htmx** | hybrid |
|---|---|---|---|
| gzipped bundle | 2.5 KB | 19.5 KB | 37 KB |
| build steps | `go build` | `go build` | `templ generate → tailwindcss → go build` |
| diff/change handwritten | +8/−1 | **+5/−0** | +18/−0 + ~100 LOC regen |
| committed generated | 0 | 0 | 645 LOC `_templ.go` + 10.6 KB CSS |

Followups already folded in (do not redo):

- `claude-mcp-bridge.md` §6 fully rewritten — htmx-only + a new "future route"
  subsection covering Alpine-when-needed and the islands-architecture pattern
  for client-heavy features (collaborative editing, complex DnD, code editors).
- `resume-mcp-bridge-plan.md` decisions table + Web UI scope updated; the
  "spike first" caveat is removed; context references swapped GOAT/Templ links
  for spike + variant-htmx references.

---

## Current git state (as of session end)

```
 M Makefile                                              # top-level `make spike` target
 M development/research/claude-mcp-bridge.md             # §6 rewritten (htmx + islands)
?? development/research/resume-mcp-bridge-plan.md        # decisions table + scope
?? development/research/resume-ui-spike.md               # the original prompt — preserve as-is
?? development/research/ui-spike.md                      # verdict doc (this is the spike output)
?? development/research/resume-post-spike.md             # this file
?? development/research/claude-channel-bridge.md         # unrelated, pre-existing untracked
?? development/spikes/                                   # entire spike tree (see below)
```

Verify with `git status` before staging. Anything in this list that has changed
since this prompt was written should be reconciled by reading the files, not by
trusting this snapshot.

Inside `development/spikes/ui/`:

```
.gitignore             # ignores .tools/ and bin/
Makefile               # tools / generate / build / spike / run-all
README.md              # rubric + run instructions
go.mod, go.sum         # standalone module: apex_process_ape/spikes/ui (templ dep only)
shared/pipeline.go     # deterministic 30 s mock generator — the contract
variant-vanilla/       # main.go + assets/{index.html, app.js, styles.css}
variant-htmx/          # main.go + templates/*.tmpl + assets/{styles.css, vendor/*.js}
variant-hybrid/        # main.go + components/{*.templ, *_templ.go} + styles/input.css
                       # + assets/{styles.css, vendor/*.js}
.tools/                # gitignored — tailwindcss standalone CLI
bin/                   # gitignored — built binaries
```

Sanity-rebuild before committing: `cd development/spikes/ui && make build` should
finish clean and produce three binaries.

---

## Step 1 — commit the work

**Don't squash this into one commit.** Two readers benefit from separation:
someone reviewing PLAN-5 wants the design-doc change isolated, and someone
re-running the spike wants the spike code alone.

Suggested commit chain (run them in order, each with the trailer policy from
project memory — **no `Co-Authored-By: Claude` trailer**):

1. `spike(ui): pipeline-progress screen in 3 frontend stacks`
   - `development/spikes/ui/` (entire tree)
   - `Makefile` (top-level `make spike` target)
   - Body: one paragraph naming the three variants, the contract (deterministic
     30 s mock pipeline), and that the rubric measurement lives in
     `development/spikes/ui/README.md`.

2. `research(ui-spike): lock htmx-only, redirect from expected hybrid`
   - `development/research/ui-spike.md`
   - `development/research/claude-mcp-bridge.md`
   - Body: one paragraph saying the spike redirected the design doc's expected
     GOAT-hybrid to htmx-only on bundle-weight + build-step + diff-per-change
     evidence; verdict doc has the numbers.

3. `research(plan-5): unblock plan from spike result`
   - `development/research/resume-mcp-bridge-plan.md` (decisions table + Web UI
     scope reflect the locked stack)
   - `development/research/resume-post-spike.md` (this file — or omit if you'd
     rather treat resume docs as session-local and gitignore them)
   - Body: one line — spike output folded into PLAN-5 prereqs.

Decide before committing whether `development/research/resume-*.md` files
belong in git or are session-local. `resume-mcp-bridge-poc.md` and
`resume-ui-spike.md` are already untracked-but-present in the working tree;
follow whichever convention the project has been observing for those.

`claude-channel-bridge.md` is unrelated to this work — leave it as untracked.

---

## Step 2 — draft PLAN-5

After the commits land, the next task is drafting
`development/planning/plan-5_ape-chat-and-pipeline-web.md`. The full briefing
lives in [`resume-mcp-bridge-plan.md`](resume-mcp-bridge-plan.md) — read it
first; it covers scope, format, style notes, and the explicit decisions that
must not be re-litigated.

Two reminders for the Web UI section specifically (these are the things the
spike changed in the plan's premises):

- The frontend stack is **HTMX 2.x + stdlib `html/template` + handwritten CSS**.
  No Templ, no Tailwind, no Alpine. Vendor HTMX core + SSE extension under
  `internal/web/assets/vendor/`. End-user install is `go install` only.
- The SSE wire schema is spike-validated: events `pipeline-init`,
  `stage-start`, `stage-update`, `stage-end`, `hook`, `reply`. Reuse the schema
  verbatim and lock it in the plan; the working reference is
  `development/spikes/ui/variant-htmx/`.

The islands-pattern subsection in `claude-mcp-bridge.md` §6 is informational
for PLAN-5 — no island has a concrete candidate today, and the plan should not
design one. Mention it only if a reader asks why PLAN-5 doesn't address rich
client-side state.

---

## Context references

| Path                                                                                          | What                                                       |
| --------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `development/research/ui-spike.md`                                                            | Spike verdict (read first)                                 |
| `development/spikes/ui/README.md`                                                             | Rubric + run instructions                                  |
| `development/spikes/ui/variant-htmx/`                                                         | Reference implementation of the locked stack               |
| `development/research/claude-mcp-bridge.md` §6                                                | Rewritten frontend section + islands future route          |
| `development/research/resume-mcp-bridge-plan.md`                                              | Full PLAN-5 drafting briefing — the next major task        |
| Project memory: `feedback_no_claude_attribution.md`                                           | No `Co-Authored-By: Claude` trailer on commits             |
