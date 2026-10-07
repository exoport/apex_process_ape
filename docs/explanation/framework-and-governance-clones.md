# The framework and governance clones are caches

**Status: specification for ape v0.7.0**, class C (it changes
`ape framework setup/update`, `ape config resolve` and `_apex/framework.yaml`).
Written before the code. Where the shipped behaviour differs from this
document, the document is updated in the same commit. It extends
[`ape framework update` installs releases](framework-updates-from-releases.md),
which still describes how a release is chosen and installed.

## Why

Since v0.4.0 ape installs a framework *release*, exported from a tag, and
leaves the clone's checkout alone. Two things were still missing:

- **The clone's working tree is what people read.** A person reading the
  framework's docs, or anything opening files in the clone, saw whatever
  branch happened to be checked out, not the release their projects run.
- **The governance repo was not managed at all.** `apex-adr-reconciliation`
  and `apex-pattern-reconciliation` read
  `{governance_repository_path}/governance/` straight from that working
  tree, every time they run. Nothing kept it at a release, and a machine
  with no human to set the path could not run them.

So both clones are now treated as caches of their newest release: fetched,
and checked out at the newest final `vX.Y.Z` tag. And when no one has said
where a clone lives, ape keeps its own under the user cache directory.

## Where each clone lives

ape resolves each clone in this order. The first that is set wins.

| | framework | governance |
| --- | --- | --- |
| 1 | `--repo` | `governance_repository_path` in `_apex/config.local.yaml`, else `_apex/config.yaml` |
| 2 | `$APEX_FRAMEWORK_REPO` | `$APEX_GOVERNANCE_REPO` (new) |
| 3 | the ape cache (below) | the ape cache (below) |

Rows 1 and 2 are **the user's** clone. Row 3 is **ape's** clone. Who owns a
clone decides what ape may do to it (see "Moving to the tag").

### The ape cache

```text
<user cache dir>/ape/framework/<host>/<path>
<user cache dir>/ape/governance/<host>/<path>
```

`<user cache dir>` is Go's `os.UserCacheDir()`: `$XDG_CACHE_HOME` or
`~/.cache` on Linux, `~/Library/Caches` on macOS, `%LocalAppData%` on Windows.
`<host>/<path>` comes from the clone URL, so
`https://github.com/exoar/apex_process_governance.git` lives at
`~/.cache/ape/governance/github.com/exoar/apex_process_governance`.

Keyed by URL on purpose. A governance URL belongs to a project, and skills
read the governance clone live: two projects on one machine with different
governance repos would otherwise take turns overwriting one directory, and
each would read the other's canon.

### The clone URL

The cache is cloned the first time it is needed, by `setup` or `update`.
Nothing else clones it: `status`, `doctor` and `config resolve` report a
cache that does not exist yet and never create one.

- **Framework:** `$APEX_FRAMEWORK_URL`, else the built-in
  `https://github.com/exoar/apex_process_framework.git`.
- **Governance:** `governance_repository_url` in `_apex/config.yaml` (new,
  optional; a project-wide value, so it is committed, while the local path
  stays per-machine). No URL and no path means the project has no governance
  repo, and nothing is done, exactly as before.

Both repositories are private. ape runs plain `git`, so cloning uses
whatever credentials the machine has for that URL: a credential helper or
token for https, a key for ssh. A failed clone names the URL and says to
check access.

## Moving to the tag

`ape framework setup` and `update` sync each clone in two halves.

**First, before any check that could refuse the install**, the framework
release is resolved:

1. **Clone** ape's own copy when it does not exist yet.
2. **Fetch:** `git fetch --tags --force origin` under the clone's lock (an
   exclusive lock on `<git dir>/ape-sync.lock`, so two projects updating at
   once cannot interleave), unless `--no-fetch`. A failed fetch warns and
   continues with the tags the clone has. `--no-fetch` applies to both
   clones.
3. **Pick the tag:** the highest final `vX.Y.Z`, never a candidate.
   `--version` picks one exactly, as before.

**Then, after every check** (the project tree, the release's layout,
`min_ape_version`, the kept-candidate rule) **and before the install
writes**, the governance clone syncs and the framework checkout moves. A
refused install moves neither clone. An update that keeps a newer candidate
installs nothing and moves nothing, so the clone and the install never tell
two stories.

What a move does depends on who owns the clone.

**ape's own clone** is detached at the release with
`git checkout --force --detach`, then `git clean -ffdx`. Nobody edits it.

**A clone of yours only ever moves forward**, because it may be where
someone commits. The framework's ship repo holds promotion commits past its
last tag, and its release process needs it "clean, on main". A developer's
build repo holds work past an rc.

| your clone | what happens |
| --- | --- |
| already at the release | nothing, branch and edits kept |
| behind it, on a branch | `git merge --ff-only` to the release: still on its branch |
| behind it, detached | detached at the release |
| behind it, with local changes | refused (exit 3, `framework_clone_dirty` / `governance_clone_dirty`); `--force-clone` overwrites them (and `git clean -fd`) |
| ahead of it, or diverged | left where it is: `not moved: main is 2 commit(s) past v0.28.2` |

"Behind" means HEAD is an ancestor of the release, so `--version` never
moves a clone of yours backwards either. `--force-clone` is its own flag:
`--force` keeps its old meaning (the worktree guards, modified project
skills) and never touches a clone.

The framework is then installed from the tag's export exactly as before.
The checkout is for whoever reads the clone. The install never reads it.

### Read-only clones are pinned

A clone ape cannot write to is **pinned**. That is the sandbox case: `aped`
mounts each clone read-only, and its origin is a host path the guest cannot
reach. ape detects it (a write into the git dir fails) and:

- does not fetch, lock or move it;
- installs the framework release **checked out there** (HEAD's exact tag),
  not the newest tag the clone happens to hold, so a workspace pinned to
  `v0.27.3` installs `v0.27.3`. `--version` still picks a tag explicitly. A
  pinned clone whose HEAD has no release tag is refused with a hint to pass
  `--version` or `--from-worktree`;
- reads governance as it is.

### What does not sync

- `--from-worktree` installs the clone's working tree, so it must not move
  it. Its main-only / clean / fast-forward guards are unchanged.
- `--plan` and `--dry-run` write nothing, the clones included.
- `setup` syncs governance only when the project already has an
  `_apex/config.yaml`. A brand-new project's config is seeded by setup
  itself; the next `update` syncs it.

### Governance in the sequence

Governance syncs just before the framework checkout moves, so a refused
governance clone stops the run before anything is written. A governance
clone that cannot be cloned or fetched is a warning, not a failure: the
framework update does not depend on it, and the reconciliation skills fail
fast with their own message when the canon is missing.

**A configured path that is a directory but not a git clone** (the eval
injects a `git archive` export of a governance tag) is read as it is: no
clone, no fetch, no `governance:` record, no warning, exit 0, and one line,
`governance repo: <path> (not a git clone: read as-is)`. ape never clones
`governance_repository_url` while a path is configured.

## How skills find the governance clone

They ask ape. `ape config resolve` emits the **effective**
`governance_repository_path`: the configured value, else
`$APEX_GOVERNANCE_REPO`, else ape's cache clone when it exists. A new field
says which:

```yaml
governance_repository_path: /home/u/.cache/ape/governance/github.com/exoar/apex_process_governance
governance_repository_source: cache   # config | env | cache | cache_missing | "" (none)
governance_repository_url: https://github.com/exoar/apex_process_governance.git
```

`cache_missing` means the URL is set but ape's clone does not exist yet; the
path is then empty, and the fix is `ape framework update`.

The reconciliation skills read `_apex/config.yaml` directly today, so they
do not see the env or cache fallback until the framework moves them to
`ape config resolve`. That is a framework change, released after this ape,
with its ape floor raised.

## What is recorded

`_apex/framework.yaml` gains a `governance:` block, present when a
governance clone was synced:

```yaml
framework:
  repo_origin: https://github.com/exoar/apex_process_framework.git
  source: tag
  version_tag: v0.28.2
  git_hash: 3c1f7602…
  git_branch: ""
governance:
  repo_origin: https://github.com/exoar/apex_process_governance.git
  version_tag: v0.1.2
  git_hash: 9a0e…
```

It records which canon this project's update synced. It is not a pin:
skills read the clone, which moves when any project on the machine updates.

The install commit carries it too, as `Governance-Version:` and
`Governance-Commit:` trailers after the framework's. An update where only
the governance clone moved (same framework release, no migration) is titled
for what it did, `chore(framework): sync governance v0.1.3`, rather than as a
framework update that would read as a no-op.

An update that keeps a newer installed candidate moves neither clone and
says so: `framework repo: <path> (not moved: installed rc kept)`.

## Reporting where things are

`ape framework status` prints each clone's path, where it came from
(`flag`, `env`, `config`, `cache`) and where it stands against its newest
release ("main, 2 commit(s) past v0.28.2"), so a person knows where to read
the docs and why a clone was not moved. `ape doctor`'s framework-repo row resolves the same way, cache
included, and never clones.

## The sandbox

`aped` serves governance the same way it serves the framework:

- `ape sandbox governance materialize <ref>` clones a ref into
  `$APE_GOVERNANCE_ROOT/<ref>` (default `/srv/apex-governance`), and
  `ape sandbox governance ls` lists them;
- `aped front --governance-root/--governance-ref`, and
  `ape sandbox up --governance-ref`;
- mounted **read-only** at `/opt/apex-governance`, with
  `APEX_GOVERNANCE_REPO` set to it in the workspace (the image sets
  `APEX_FRAMEWORK_REPO` itself; nothing sets a governance variable, so aped
  does, and only when it mounts one). Policy refuses a writable mount there.

Inside the workspace both clones are pinned (above).

## Exit codes

Unchanged, plus: a clone of yours that is behind the release and has local
changes exits **3** (the framework source) without `--force-clone`,
whichever clone it is.

## A release is installable once its tag is pushed

With ape's own clones, a project sees a framework or governance release
only once its tag is pushed: ape clones from the remote. A tag that exists
only in a local ship repo is installed with `--repo <that repo>` for that
one command.
