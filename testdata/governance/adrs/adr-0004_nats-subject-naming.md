---
id: ADR-0004
title: "NATS Subject Naming Conventions"
status: accepted
type: architectural
tags: [nats, messaging, subject-naming, zones]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# NATS Subject Naming Conventions

## Summary

In the context of a multi-service system using NATS as the messaging backbone, facing the challenge of subject name collisions, poor discoverability, and difficult per-zone access control, we decided to adopt a structured `<zone>.<domain>.<version>.<entity>.<action>` naming convention, to achieve a predictable and enforceable subject hierarchy, accepting that all subject changes require a version bump.

## Context

- NATS subjects form the API surface of the messaging layer.
- Without a naming convention, subjects proliferate in incompatible ways across services.
- Zone-based NATS permissions (crux, edge, oper) require predictable prefixes.
- Breaking changes to subject schemas affect all subscribers simultaneously.

## Decision

Subject naming follows the pattern:

```
<zone>.<domain>.<version>.<entity>.<action>
```

Examples:

- `crux.resource.v1.created`
- `edge.access.v1.granted`
- `oper.tenant.v1.provisioned`

Zones:

- `crux` — core business domain services
- `edge` — access point and device layer
- `oper` — operator and admin actions

Rules:

- All subjects are lowercase with dot separators.
- No wildcards in producer subjects.
- Version is a `v{N}` integer (e.g., `v1`, `v2`).
- Breaking payload changes require a version bump in the subject path.

## Considered Alternatives

- **Free-form names:** Rejected — leads to collisions and undiscoverable subjects.
- **HTTP-style paths with slashes:** Rejected — NATS subjects use dots; slashes are not idiomatic.

## Consequences

**Positive:**

- Predictable, discoverable subject hierarchy.
- Zone-based NATS permissions map cleanly to subject prefixes.
- Breaking changes are signalled by the version segment.

**Negative / Tradeoffs:**

- All subject changes require a coordinated version bump.
- Five-segment names can be verbose for simple events.

## Compliance Notes

PV validates that all NATS publish calls and consumer subscriptions use subjects matching the `<zone>.<domain>.<version>.<entity>.<action>` pattern.
