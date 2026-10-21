# How to move archived changes to the current archive layout

`ape change archive` and `ape change abandon` put each finished change under
`development/archive/changes/<YYYY-MM-DD>-<id>/`, beside `development/archive/milestones/`. A
project that archived changes before that layout has them one level up, at
`development/archive/<YYYY-MM-DD>-<id>/`. `ape project archive-layout` moves them. Every ape
command reads both layouts, so nothing breaks while you wait: this is tidying, in one commit.

You usually do not run it yourself. The framework's migration `v1.0.0_seq-06` runs it during
`ape framework update` (command `ape project archive-layout`, check
`ape project archive-layout --check`). Run it by hand when you want the move on its own commit
before an update, or to see what is left.

## See what is left

```bash
ape project archive-layout --check
```

It writes nothing. Exit 0 means nothing is left in the older layout. Exit 1
(`archive.legacy-layout`) names the folders still to move.

## Move them

On the base branch, with a clean tree:

```bash
ape project archive-layout
```

It makes one commit, `apex: archive layout (seq-06)`:

- each folder moves with `git mv`, exactly as it was archived;
- in each moved `state.json`, the paths that named the old folder (`archived_to`, and an
  acceptance's evidence inside the folder) are rewritten to the new one. Nothing else in the files
  changes.

`development/archive/milestones/` stays where it is. The refs `refs/apex/archive/<id>` and
`refs/apex/abandoned/<id>`, and each `state.json`'s `tip_ref`, name commits, so they are untouched.

With nothing left to move it writes nothing and prints "nothing to move" (exit 0), so running it
twice is safe. `--json` prints `{"moved": [{"id", "from", "to"}], "rewritten": [...], "commit"}`.

Any session may run it: it is not a person act.

## When it refuses

| Code | Exit | What to do |
| --- | --- | --- |
| `branch.change-branch` | 1 | Switch to the base branch first. |
| `tree.dirty` | 1 | Commit or stash your changes first. |
| `git.not-a-repo` | 1 | Run it inside the project's repository. |
| `archive.folder-exists` | 3 | A folder's new place already exists. Nothing was written: merge the two folders by hand, commit, and run it again. |

## Related

- [The change lifecycle](../reference/change-lifecycle.md): what archive and abandon write.
- [How to refresh the framework in a project](framework-update.md): the update that runs the
  migration.
- [`ape project archive-layout --help`](../reference/cli.md#ape-project-archive-layout).
