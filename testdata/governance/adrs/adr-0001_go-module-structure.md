---
id: ADR-0001
title: "Go Module Structure"
status: accepted
type: architectural
tags: [go, module, layout, structure]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# Go Module Structure

## Summary

In the context of Go backend services, facing the challenge of inconsistent project layouts across services and developers, we decided to adopt a standard module layout convention, to achieve predictable navigation and consistent `internal/` enforcement, accepting that a fixed layout constrains flexibility in edge cases.

## Context

- Go projects have no enforced directory convention beyond `internal/` semantics.
- Without a standard layout, each service adopts ad-hoc structures that differ from one another.
- Onboarding friction increases when contributors must discover each service's conventions independently.
- `cmd/<service>/` and `internal/<pkg>/` are the dominant community convention for production Go services.

## Decision

All Go services follow this standard layout:

- `cmd/<service>/` — entry point only; wires dependencies and starts the process
- `internal/<pkg>/` — private application packages not importable by external modules
- `internal/apecmd/` — cobra command tree (for CLI tools only)
- No `pkg/` directory; all shared code lives in dedicated modules with explicit import paths
- Module path format: `github.com/<org>/<repo>`

## Considered Alternatives

- **Flat structure:** All packages at the root. Rejected — no `internal/` boundary, pollutes the import graph.
- **`pkg/` directory:** Community convention from Kubernetes era. Rejected — redundant with module boundaries; adds no value in single-repo services.

## Consequences

**Positive:**

- Consistent layout across all services reduces cognitive load.
- `internal/` enforces package privacy at the Go compiler level.
- New contributors can navigate any service immediately.

**Negative / Tradeoffs:**

- Fixed layout constrains experimental or non-standard service shapes.
- `cmd/<service>/` adds one level of indirection for simple single-binary projects.

## Compliance Notes

All new services must follow this layout. PV with this ADR in scope validates layout compliance.
