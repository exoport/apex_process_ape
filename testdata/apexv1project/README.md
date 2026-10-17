# `testdata/apexv1project` — a minimal APEX v1 project

The project ape's live gates RUN against (`make check-hooks`,
`make check-task-subagents`): the tree `ape project init` writes, plus a filled
`development/project.md` and one proposed milestone, so `ape project check` is
green on it (`TestFixture_ApexV1ProjectIsGreen` keeps it that way).

Made by running `ape project init` in an empty repository, filling
`project.md`, and `ape milestone new "Walking skeleton" --gate true`; the files
git tracked afterwards are what is committed here, with one exception:

- **No `CLAUDE.md`.** `ape project init` writes one (`@AGENTS.md`), but this
  repository must not hold a CLAUDE.md anywhere (see the repo's AGENTS.md).
  The seeders write it into their copy, then commit the copy as
  `apex: project init`, which is also the v1 epoch provenance is judged from.

The v0 fixture beside it, `testdata/apexproject`, stays: it is the v0 source
`ape project lift`'s tests convert, and the v0 doctor checks' contract fixture.

Read-only. A test that needs to write copies it into `t.TempDir()` first.
