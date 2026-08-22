# `testdata/apexproject` — a project shaped like a real one

Every package under `internal/` that reads project data builds its own fixture
with `t.TempDir()`. Those fixtures are convenient, and convenience is exactly
how two shipped commands came to be wrong on every real project: a hand-built
fixture is written by the same person who wrote the code, so it agrees with the
code's assumptions rather than with the framework's.

This tree agrees with the framework instead. It is trimmed from a real APEX
project and carries the four shapes a hand-built fixture never happens to
produce:

1. **A tracker row key is the story file's stem, not its `story_id`.**
   `development_status` holds `1-1_greet-a-name-from-the-domain`, while that
   file's frontmatter says `story_id: "1.1"`. The two are different strings by
   design — the framework writes stories to
   `{implementation_folder}/{{story_key}}.md` and keys the tracker on the same
   `story_key`. Anything that joins tracker rows to story files on `story_id`
   matches nothing and reports the whole project as divergent.

2. **The capability index carries no `file:` field.**
   `capability-index-schema.json` requires `id, slug, name, status, components,
   created_at, updated_at` and defines no `file` property at all — the record
   filename is derived from `id` + `slug`. ADRs, patterns and features all
   require `file:`. A checker that demands `file:` from every family reports a
   finding per capability, forever.

3. **The tracker carries comments, blank-line grouping and a generated header**,
   because `ape sprint reconcile` promises to change exactly the epic row and
   the body `updated_at` and to leave every other byte alone.

4. **Non-record files live beside the records** — `README.md`, a `changelog/`
   sidecar directory, a `.txt` — so "zero findings" means zero false positives
   and not merely zero records looked at.

Read-only. Tests that need to write copy it into `t.TempDir()` first.
