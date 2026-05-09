---
id: PAT-0002
pattern: service-layer
status: draft
tags: [go, service, business-logic, handler, separation-of-concerns]
version: v1
source_codebase: diegosz/apex_process_ape
applicability: every Go service package that implements business logic beyond simple CRUD
changed_at: 2026-03-19
---

# service-layer

## Overview

Business logic is encapsulated in a service layer that sits between HTTP/NATS handlers and repositories. Services coordinate multiple repositories, publish domain events, and enforce business rules. Handlers are thin adapters that translate protocol concerns (HTTP request parsing, NATS message decoding) into service method calls.

This pattern keeps business rules out of handlers and repositories. Handlers become replaceable transport adapters; repositories remain focused on persistence. The service is the stable unit that changes only when business logic changes.

## When to Use

Apply the service layer pattern when:

- A handler needs to perform more than one repository operation.
- Business rules (validation, state machine transitions, event publishing) exist beyond simple data mapping.
- Multiple handlers (HTTP and NATS) need to invoke the same business logic.

Do not apply this pattern for:

- Trivial CRUD endpoints where a handler calling a repository directly is sufficient and no business rules exist.
- Read-only projections that map DB rows to API responses without transformation.

## Reference Codebase

### Service struct and constructor

```go
type service struct {
    repo   Repository
    events EventPublisher
    log    *zerolog.Logger
    wg     *sync.WaitGroup
}

func New(repo Repository, events EventPublisher, opts ...Option) (*service, error) {
    nop := zerolog.Nop()
    s := &service{
        repo:   repo,
        events: events,
        log:    &nop,
        wg:     &sync.WaitGroup{},
    }
    for _, o := range opts {
        o(s)
    }
    return s, nil
}
```

### Business method pattern

```go
func (s *service) ProcessAction(ctx context.Context, req Request) (*Response, error) {
    entity, err := s.repo.FindByID(ctx, req.ID)
    if err != nil {
        return nil, fmt.Errorf("find entity: %w", err)
    }
    // business logic
    entity.Apply(req)
    if err := s.repo.Save(ctx, entity); err != nil {
        return nil, fmt.Errorf("save entity: %w", err)
    }
    s.events.Publish(ctx, NewEntityUpdatedEvent(entity))
    return toResponse(entity), nil
}
```

### Handler as thin adapter

```go
func (h *handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
    var req UpdateRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }
    resp, err := h.svc.ProcessAction(r.Context(), toServiceRequest(req))
    if err != nil {
        h.writeError(w, err)
        return
    }
    h.writeJSON(w, http.StatusOK, resp)
}
```

## Implementation Guide

### Step 1: Define the `Service` interface

Expose an interface that handlers depend on:

```go
type Service interface {
    ProcessAction(ctx context.Context, req Request) (*Response, error)
}
```

### Step 2: Implement business methods on the unexported `service` struct

Each method: validates input, calls repository, applies business rules, publishes events, returns response. Never return raw DB types from service methods.

### Step 3: Keep handlers thin

Handlers translate between HTTP/NATS and service types. No business logic in handlers. No repository calls from handlers.

### Step 4: Wire in `main.go`

```go
repo := svcfoo.NewPostgresRepository(queries)
pub  := events.NewNATSPublisher(nc)
svc  := svcfoo.New(repo, pub, svcfoo.WithLogger(&log))
```

## Compliance Checklist

- [ ] Business logic lives in service methods, not in handlers or repositories
- [ ] Service struct is unexported (`service`); public interface (`Service`) is exported
- [ ] Constructor applies functional options after initialising defaults
- [ ] Service methods never return raw DB types — map to domain types before returning
- [ ] Handlers contain no business logic — only protocol translation and error mapping
- [ ] Repository is never called directly from handlers
- [ ] Events are published from service methods, not handlers

## Anti-Patterns

### Business logic in handlers

**What it looks like:** An HTTP handler that validates domain rules, calls multiple repository methods, and publishes events.

**Why it is wrong:** Business logic tied to a transport protocol cannot be reused by NATS consumers or background jobs. Testing requires spinning up an HTTP server.

**Correct approach:** Move all business logic into a service method. The handler calls the service method and maps the result to an HTTP response.

### Service methods returning DB types

**What it looks like:** `func (s *service) GetEntity(ctx context.Context, id uuid.UUID) (*sordb.Entity, error)`

**Why it is wrong:** Callers depend on the DB schema shape. Any schema change breaks all callers of the service method.

**Correct approach:** Map DB types to domain types in repository methods. Service methods return domain types only.
