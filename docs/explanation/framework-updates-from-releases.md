# `ape framework update` installs releases

**Status: specification for ape v0.4.0.** The first cycle through the
release-candidate process (see `AGENTS.md` → "Releases"), class C. Written
before the code, so the eval can build fixtures while it is built. Where the
shipped behaviour differs from this document, the document is updated in the
same commit.

## Why

Until v0.3.x, `ape framework setup` and `update` installed whatever the
framework clone's `main` was after a fast-forward. So anything pushed to the
ship repo's `main` reached every team at their next update, tagged or not,
and "teams only see evaluated versions" held only by discipline. From v0.4.0
they install a **release**, meaning a tag, and a candidate only when you ask
for it by name.

## Choosing what to install

The framework clone (`--repo`, or `$APEX_FRAMEWORK_REPO`) keeps its meaning,
but ape uses it only as a **store of git objects**. ape never checks anything
out in it, never merges, and never looks at its working tree or branch. The
clone can be dirty, on any branch, or be the checkout you are developing in.

1. **Fetch tags:** `git fetch --tags --force origin`, unless `--no-fetch`.
   If the fetch fails (offline, or no `origin`), ape warns, names the newest
   release it already has, and continues with the tags it has.
2. **Pick a tag:**
   - **By default**, the highest tag shaped exactly `vX.Y.Z`, compared as
     semver (so `v0.10.0` beats `v0.9.0`). Candidates (`-rc.N`) and any
     other suffix are never picked.
   - **`--version vX.Y.Z`** or **`--version vX.Y.Z-rc.N`** picks exactly
     that tag, and is the only way to install a candidate. Any other shape
     is a usage error (exit 2).
3. **Export the tag's files:** git writes the tag's tree into a temporary
   directory through a throwaway index (`read-tree`, then
   `checkout-index`). `core.autocrlf=false` makes the files the release's
   own bytes, and the clone's index and working tree are left alone. That
   tree must use the RELEASED layout (`_apex/` and
   `.claude/` at its root). A build-repo tag, which nests them under
   `framework/`, is refused (exit 3, `framework_layout_invalid`), with a
   hint to point `--repo` at the ship repo.
4. **Install from the export**, exactly as before: skills, pipelines,
   tables, operating rules, migrations. Then the temporary directory is
   removed.

**`--from-worktree`** keeps the v0.3.x behaviour unchanged: install the
clone's working tree, with the `main`-only / clean / fast-forward guards
(`--force` and `--no-fetch` as before). It is for framework developers and
for building scratch installs. Its record says `source: worktree`.

`--plan` and `--dry-run` resolve the tag the same way, and read the tag's
tree rather than the clone's working tree.

**A default update never goes backwards.** When the installed release is
newer than the newest final one, it is a candidate someone installed by
name. `update` without `--version` then keeps it, prints why, commits
nothing and exits 0. `--version vX.Y.Z` goes back when you mean it, and
`status` reports the install as ahead rather than as drift.

## What is recorded

`_apex/framework.yaml` → `framework:` gains one field and changes the
meaning of two:

```yaml
framework:
  repo_origin: git@github.com:…/apex_process_framework.git
  source: tag               # NEW: tag | worktree
  version_tag: v0.27.0      # the tag installed (source: tag), or HEAD's exact tag (worktree)
  git_hash: 3c1f7602…       # the commit that tag named AT INSTALL TIME
  git_branch: ""            # empty for source: tag
```

**A moved tag is reported, never followed silently.** Whenever `update`,
`status` or `ape doctor` runs, it resolves the recorded `version_tag` in the
clone. If that tag now names a different commit than `git_hash`, it prints a
loud warning naming both commits, and `ape doctor` reports
`framework.tag_moved` as a WARN. Tags are meant never to move, so a moved
tag means either the upstream rule was broken or someone else's history is
in your clone. ape reports it and does not decide which.

`ape framework status` reports the installed release against the newest
release in the clone ("installed v0.26.2, newest v0.27.0"), instead of
comparing HEAD's tag as it did before.

## ape's own version: `min_ape_version`

The framework declares the oldest ape a release works with, as a top-level
field in its `_apex/ape-commands.yaml`:

```yaml
min_ape_version: 0.4.0
```

- **Where ape reads it:** from the TAG it is about to install
  (`git show <tag>:_apex/ape-commands.yaml`), before anything is written;
  and from the installed `_apex/ape-commands.yaml` in the new `ape doctor`
  check `framework.ape_version`.
- **An rc of exactly that version satisfies it:** `0.4.0-rc.N` satisfies
  `>= 0.4.0`; `0.4.0-rc.N` does not satisfy `>= 0.4.1`. Otherwise it is
  ordinary semver.
- **Unknown is not a pass:** a `dev` build or a Go pseudo-version
  (`0.3.2-0.20260930…`) cannot be compared. ape warns and continues. A
  missing field (every framework before v0.27.0) means there is no minimum,
  which is reported, not assumed.

### When ape is below the release's minimum

| context | behaviour |
| --- | --- |
| stdin and stderr are a terminal | Asks `ape 0.3.1 is older than v0.27.0 needs (0.4.0). Update ape first? [Y/n]`. Yes: update, then continue (below). No: exit 11, nothing written. |
| not a terminal (CI, scripts, a skill) | Never asks, never updates. Prints the exact command to run and exits 11, nothing written. |

When ape meets the minimum and the update check has cached a newer final
ape, one line says so. It never prompts and never blocks: a courtesy must
not make an install depend on GitHub being reachable.

### How ape updates itself here

- **The project pins ape with bingo** (`.bingo/ape.mod` exists in the
  project root): `bingo get github.com/exoport/apex_process_ape/cmd/ape@vA.B.C`,
  where `vA.B.C` is the newest FINAL ape release. The new binary is
  `$(go env GOBIN, else GOPATH/bin)/ape-vA.B.C`. If `bingo` is not on
  PATH, ape exits 11 with instructions. It does not install bingo.
- **Otherwise:** the same update `ape update` performs, meaning the newest
  final release, verified with cosign, replacing this binary in place.
- **Then ape re-runs itself:** the NEW binary is started with the same
  arguments. The install logic belongs to ape, and a framework release that
  needs a newer ape usually needs its install logic too. The re-run carries
  `APE_FRAMEWORK_UPDATE_REEXEC=1`, and a re-run that is still below the
  minimum exits 11 instead of updating again.

Trust differs between the two routes, and the docs say so: `ape update`
installs the cosign-verified release artifact, while bingo compiles the
tagged source through the Go module proxy (checksummed by `sum.golang.org`,
but not the signed binary). A bingo-built ape reports its version correctly,
but its `git commit` as `unknown`.

## Commits

**This reverses a documented v0.3.x rule.** `framework update` used to
commit nothing, "a property worth more than the convenience". From v0.4.0 it
commits by default, because an install is now a release with an identity
worth recording, and the commits are that record. `--no-commit` keeps the
old behaviour exactly.

**Before anything is written**, in commit mode:

- The project must be a git repository on a branch (not detached HEAD).
- No tracked file may be modified or staged, anywhere in the tree.
  Untracked files are fine. A violation exits 4 and lists the paths (v0.3.x
  checked only `.claude/skills/apex-*`, and `--no-commit` still does only
  that).

**Commit 1**, only when ape was updated through bingo, touching only
`.bingo/`:

```
chore(ape): update ape to v0.4.0

Generator: ape framework update
```

**Commit 2**, the install plus the derivable migrations that ran:

```
chore(framework): update APEX framework to v0.27.0

Framework-Version: v0.27.0
Framework-Commit: 3c1f7602e4…(full sha)
Framework-Migrations: v0.27.0_seq-01, v0.27.0_seq-02
Generator: ape framework update
```

- `ape framework setup` writes `chore(framework): install APEX framework v0.27.0`
  with the same trailers.
- `Framework-Migrations:` is present only when a migration was applied. A
  judged migration never runs, so it never appears.
- `--repair` runs after commit 2, and what it changes is left uncommitted.
- If the install changed nothing (already at that release, files
  identical), there is no commit, and the output says so.
- Commits use your git identity and run your hooks, never `--no-verify`. If
  a hook fails, everything stays staged, ape names the commit it could not
  make, and exits 12.

The framework's release record skips a commit only when it carries
`Generator: ape framework update` AND every path it changes is under
`.bingo/`.

**A partial result is kept on purpose.** If ape was updated (commit 1) and
the framework install then fails, the ape update stays committed. It is
valid by itself, since a newer ape that meets the release's minimum works
with the installed framework. ape reports what was and was not done, and
exits with the install's own code.

## Exit codes

| code | meaning |
| --- | --- |
| 0 | installed, or already at that release |
| 2 | usage: a bad `--version` shape, or conflicting flags (`--version` with `--from-worktree`) |
| 3 | the framework source: no release tag found, the `--version` tag does not exist, the layout is not the released one, or a `--from-worktree` guard failed |
| 4 | the project tree: modified tracked files in commit mode, or modified apex skills with `--no-commit` |
| 5, 6, 7 | unchanged: headless setup without arguments, already installed, not installed |
| 11 | **new:** ape is below the release's `min_ape_version`, and was not (or could not be) updated |
| 12 | **new:** the install is complete but a commit could not be made (a hook failed); the changes are staged |

8–10 remain reserved for `ape change`.

## `ape doctor`

- `framework.ape_version`: installed `min_ape_version` against this ape.
  FAIL below it, WARN when unknown (a dev build or pseudo-version), INFO
  when the framework declares none, OK otherwise.
- `framework.tag_moved`: WARN when the installed tag now names a different
  commit.
