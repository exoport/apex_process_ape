---
id: PAT-0005
pattern: tenant-context-propagation
status: draft
tags: [multi-tenancy, context, go, middleware, isolation]
version: v1
source_codebase: exoport/apex_process_ape
applicability: every Go service that handles tenant-scoped requests in a multi-tenant system
changed_at: 2026-03-19
---

# tenant-context-propagation

## Overview

Tenant ID is extracted once at the request boundary (HTTP middleware or NATS consumer wrapper) from validated JWT claims, then stored in the `context.Context` using an unexported key type. All downstream service and repository methods retrieve the tenant ID from context via `TenantFromContext` or `RequireTenant`. Tenant ID is never passed as an explicit function parameter.

This pattern is idiomatic Go for cross-cutting context values. It keeps function signatures clean, prevents accidental parameter shadowing, and ensures the tenant ID is always extracted from a trusted source (validated JWT), never from user-controlled input.

## When to Use

Apply this pattern when:

- A service handles requests on behalf of multiple independent tenants.
- The tenant ID must be available throughout the call chain without being an explicit parameter on every function.
- Tenant ID extraction happens at a single authoritative boundary (JWT validation middleware).

Do not apply this pattern for:

- Single-tenant services where tenant context is irrelevant.
- Services that receive tenant ID via a trusted internal protocol field (not JWT) — adapt the extraction point accordingly.

## Reference Codebase

### Context key and accessor functions (`tenant/context.go`)

```go
package tenant

type contextKey struct{}

// WithTenant stores the tenant ID in the context.
func WithTenant(ctx context.Context, tenantID string) context.Context {
    return context.WithValue(ctx, contextKey{}, tenantID)
}

// TenantFromContext retrieves the tenant ID from context.
// Returns ("", false) if the tenant ID is absent.
func TenantFromContext(ctx context.Context) (string, bool) {
    v, ok := ctx.Value(contextKey{}).(string)
    return v, ok && v != ""
}

// RequireTenant retrieves the tenant ID from context.
// Returns an error if the tenant ID is absent or empty.
func RequireTenant(ctx context.Context) (string, error) {
    id, ok := TenantFromContext(ctx)
    if !ok {
        return "", errors.New("tenant ID missing from context")
    }
    return id, nil
}
```

### HTTP middleware extraction

```go
func TenantMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        claims, ok := jwtClaimsFromContext(r.Context())
        if !ok {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        tenantID := claims.TenantID
        if tenantID == "" {
            http.Error(w, "missing tenant claim", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r.WithContext(tenant.WithTenant(r.Context(), tenantID)))
    })
}
```

### NATS consumer wrapper extraction

```go
func withTenantFromSubject(subject string, handler func(ctx context.Context) error) func(*nats.Msg) error {
    return func(msg *nats.Msg) error {
        parts := strings.SplitN(subject, ".", 5)
        if len(parts) < 2 {
            return fmt.Errorf("cannot extract tenant from subject: %s", subject)
        }
        tenantID := parts[0] // tenant slug is the first segment for tenant-scoped subjects
        ctx := tenant.WithTenant(context.Background(), tenantID)
        return handler(ctx)
    }
}
```

### Service method consuming tenant ID

```go
func (s *service) GetResource(ctx context.Context, resourceID uuid.UUID) (*Resource, error) {
    tenantID, err := tenant.RequireTenant(ctx)
    if err != nil {
        return nil, err
    }
    return s.repo.FindByTenantAndID(ctx, tenantID, resourceID)
}
```

## Implementation Guide

### Step 1: Define the context key as an unexported type

Always use a private struct type as the context key — never a string or int. This prevents key collisions with other packages.

### Step 2: Provide `WithTenant`, `TenantFromContext`, and `RequireTenant`

Put these three functions in a shared `tenant` package or within the service package if not shared. Never duplicate the extraction logic.

### Step 3: Extract in middleware, not in handlers

HTTP middleware and NATS consumer wrappers are the correct extraction points. Handlers and services never read JWT claims directly.

### Step 4: Call `RequireTenant` at the top of service methods that need tenant scope

Fail fast at the service boundary if the tenant ID is missing. This makes missing-tenant bugs obvious during development.

## Compliance Checklist

- [ ] Context key is an unexported struct type (not a string, int, or exported type)
- [ ] `WithTenant`, `TenantFromContext`, and `RequireTenant` are defined in a shared location
- [ ] Tenant ID is extracted from JWT claims in middleware, never from request body or query parameters
- [ ] Service methods that require tenant scope call `RequireTenant` at the top of the method
- [ ] Tenant ID is never passed as an explicit function parameter alongside context
- [ ] NATS consumer wrappers call `WithTenant` before invoking handlers

## Anti-Patterns

### Passing tenant ID as an explicit parameter

**What it looks like:** `func (s *service) GetResource(ctx context.Context, tenantID string, resourceID uuid.UUID) (*Resource, error)`

**Why it is wrong:** Explicit tenant ID parameters must be threaded through every call site. A single missed parameter leaves the tenant unset, and there is no compiler enforcement that the same tenant ID flows through the entire chain.

**Correct approach:** Store tenant ID in context with `WithTenant` and retrieve it with `RequireTenant`. The context is already threaded through every call.

### Extracting tenant ID from the request body

**What it looks like:** `tenantID := req.TenantID` where `req` is a user-controlled JSON body.

**Why it is wrong:** User-controlled tenant IDs allow tenant impersonation. A malicious client could claim any tenant ID.

**Correct approach:** Extract tenant ID exclusively from validated JWT claims in the authentication middleware.
