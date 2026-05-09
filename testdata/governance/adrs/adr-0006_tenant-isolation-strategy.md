---
id: ADR-0006
title: "Tenant Isolation Strategy"
status: accepted
type: architectural
tags: [multi-tenancy, isolation, rls, postgresql, context-propagation]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# Tenant Isolation Strategy

## Summary

In the context of a multi-tenant platform with shared infrastructure, facing the critical security requirement of preventing data leakage between tenants, we decided to enforce tenant isolation at both the application layer (context propagation) and the database layer (RLS + `tenant_id` column), to achieve defense-in-depth isolation, accepting the overhead of a mandatory `tenant_id` column on every tenant-scoped table.

## Context

- The platform serves multiple independent tenants on shared PostgreSQL and NATS infrastructure.
- Data leakage between tenants is a critical security concern with regulatory implications.
- Row-level security (RLS) at the DB layer provides a safety net independent of application bugs.
- Context propagation avoids explicit `tenantID` parameters on every function signature.

## Decision

- All tenant-scoped DB tables include a `tenant_id` column with a `NOT NULL` constraint.
- PostgreSQL RLS policies enforce tenant isolation at the DB layer as a secondary safety net.
- Application layer extracts tenant ID from validated JWT claims via middleware, never from request body.
- Tenant ID is propagated via `context.Context` throughout the call chain (see PAT-MT-001).
- NATS KV bucket keys use `<tenant>.<identifier>` format for tenant-scoped entries.
- Services never accept `tenant_id` as a user-provided parameter in request bodies.

## Considered Alternatives

- **Separate DB per tenant (database-per-tenant):** Rejected — operational cost is prohibitive at scale; connection pooling and migration management become untenable.
- **Application-only enforcement (no RLS):** Rejected — single point of failure; a bug in middleware would expose all tenants' data.

## Consequences

**Positive:**

- Defense in depth: both application and DB enforce isolation independently.
- Context-based propagation avoids prop-drilling and keeps function signatures clean.
- Tenant misconfiguration at the JWT layer is caught at the DB layer.

**Negative / Tradeoffs:**

- `tenant_id` column is mandatory on every tenant-scoped table — adds schema discipline requirement.
- RLS policies add query plan complexity; must be profiled for high-volume tables.

## Compliance Notes

PV validates that all tenant-scoped tables include `tenant_id NOT NULL` and that tenant ID is never read from request body fields.
