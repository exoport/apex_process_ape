---
id: ADR-0005
title: "NATS KV Bucket Design"
status: accepted
type: architectural
tags: [nats, kv, jetstream, ephemeral-state, cas]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# NATS KV Bucket Design

## Summary

In the context of services using NATS JetStream Key-Value for ephemeral state, facing the challenge of bucket proliferation and inconsistent key schemas, we decided to adopt one bucket per logical domain entity with a `<tenant>.<identifier>` key format and CAS for atomic state transitions, to achieve clear ownership, TTL semantics, and race-free updates, accepting that CAS requires retry logic on revision conflicts.

## Context

- NATS JetStream KV provides durable, replicated, TTL-aware storage suitable for ephemeral domain state.
- Without consistent bucket design, key collisions across domains and unpredictable eviction behaviour emerge.
- CAS (Compare-And-Swap) is the only safe primitive for atomic state transitions in a distributed KV store.

## Decision

- One bucket per logical domain entity (e.g., `grants`, `soul-bonds`, `soul-bans`).
- Key format: `<tenant>.<identifier>` for tenant-scoped buckets.
- TTL set at the bucket level via `MaxAge` in `nats.KeyValueConfig`; per-key TTL is not used.
- CAS operations (`Update` with revision) are used for all state transitions that must be atomic.
- Bucket names are lowercase, hyphenated.
- Soft deletion via CAS status update (not `Delete`/`Purge`) for auditable state transitions.

## Considered Alternatives

- **Single bucket for all domain state:** Rejected — mixing TTLs and ownership across domains creates fragile eviction behaviour and unclear access control boundaries.
- **Per-key TTL:** Rejected — not uniformly supported across NATS server versions; bucket-level TTL is simpler and predictable.

## Consequences

**Positive:**

- Clear ownership and TTL semantics per domain.
- Atomic state transitions prevent race conditions.
- Consistent key format is indexable and predictable across services.

**Negative / Tradeoffs:**

- CAS requires retry logic on revision conflicts.
- Bucket-level TTL applies uniformly — per-entry TTL overrides require workarounds.

## Compliance Notes

PV validates that KV bucket names are lowercase-hyphenated, key format follows `<tenant>.<identifier>` for tenant-scoped buckets, and state transitions use `Update` with explicit revision.
