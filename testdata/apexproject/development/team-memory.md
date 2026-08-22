# Team Memory

Preamble prose that belongs to no entry.

## Conventions

### Error handling — 2026-08-01

Every HTTP handler returns an error to a single boundary.

Source: Epic 1 retrospective

### Scanner fences — 2026-08-02

Entry headings follow the framework's own template, `### {{area}} — {{YYYY-MM-DD}}`.
The block below looks like a third entry to `grep -c '^### '`, and counting it
would shift every ordinal after it — so `ape memory show 3` would hand a skill
the wrong entry with no way to notice:

```markdown
### Not an entry — 2026-08-03
```

That is the whole test.

Source: Epic 1 retrospective
