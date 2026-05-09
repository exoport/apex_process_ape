---
id: ADR-0007
title: "PostgreSQL Migration Strategy"
status: accepted
type: architectural
tags: [postgresql, migrations, sqlc, nayux, schema-evolution]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# PostgreSQL Migration Strategy

## Summary

In the context of services with an evolving PostgreSQL schema, facing the requirement for controlled, reversible, and dual-database-compatible schema changes, we decided to adopt numbered SQL migration files following the nayux_framework convention with `sqlc`-generated Go code, to achieve type-safe DB access and reproducible schema state, accepting the upfront discipline of writing reversible migrations.

## Context

- Database schema evolution must be reproducible across all environments (dev, staging, production).
- Ad-hoc schema changes cause drift and deployment risk.
- The system must remain compatible with both PostgreSQL and YugabyteDB (YSQL).
- Manual SQL query writing is error-prone; type-safe generated code eliminates a class of runtime errors.

## Decision

- All schema changes are expressed as numbered SQL migration files: `<sequence>_<description>.sql`.
- Migrations include both `-- +migrate Up` and `-- +migrate Down` sections.
- PSQL-specific syntax uses `-- psql` comments; YSQL alternatives use `-- ysql` for dual-database compatibility.
- Triggers use `SECURITY DEFINER` functions in a dedicated schema.
- `sqlc` generates type-safe Go code from annotated SQL queries; generated files are never hand-edited.
- Migrations are applied at service startup via the embedded migrator.
- Migration sequences use zero-padded 6-digit integers (e.g., `000001`, `001010`).

## Considered Alternatives

- **ORM-based migrations (GORM, ent):** Rejected — ORMs abstract away SQL in ways that impede fine-grained control over index strategies and dialect-specific features.
- **Manual schema management:** Rejected — no reproducibility, no rollback, no audit trail.

## Consequences

**Positive:**

- Fully reversible migrations (with documented limitations for destructive operations).
- Dual-database compatibility via dialect comment annotations.
- Type-safe DB access eliminates SQL injection at compile time.
- `make generate` is the single command to regenerate all DB code.

**Negative / Tradeoffs:**

- Reversibility is theoretical for destructive operations (DROP TABLE, column removal).
- `sqlc` code generation must be re-run after every SQL change; stale generated code causes compile errors.

## Compliance Notes

PV validates that all migration files follow the `<sequence>_<description>.sql` naming convention and include both Up and Down sections. `make generate` must pass without errors after any schema change.
