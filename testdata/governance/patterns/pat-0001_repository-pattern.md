---
id: PAT-0001
pattern: repository-pattern
status: draft
tags: [go, repository, interface, dependency-injection, testing]
version: v1
source_codebase: diegosz/apex_process_ape
applicability: every Go service package that performs data access against a persistent store
changed_at: 2026-03-19
---

# repository-pattern

## Overview

Every Go service that needs data access defines a `Repository` interface in the service package. The interface declares only the operations the service needs. A concrete implementation backed by `sqlc`-generated queries is wired at construction time via the service constructor. Test doubles implement the same interface without touching a live database.

This pattern decouples business logic from persistence technology. Services depend on the `Repository` interface, not on any concrete DB type, which enables unit testing without infrastructure and makes the storage layer swappable.

## When to Use

Apply the repository pattern when:

- A service package reads or writes persistent data.
- You need to unit-test business logic without a running database.
- The persistence implementation may change (e.g., switching from PostgreSQL to a different store for a subset of queries).

Do not apply this pattern for:

- Read-models or projections where direct query wrappers suffice and no business logic is involved.
- Simple CRUD proxies with no domain logic — a thin handler calling a query directly is acceptable.

## Reference Codebase

The pattern consists of three components in every service package:

### 1. The `Repository` interface

```go
// Repository defines the data access operations for the service.
type Repository interface {
    FindByID(ctx context.Context, id uuid.UUID) (*Entity, error)
    Save(ctx context.Context, e *Entity) error
    Delete(ctx context.Context, id uuid.UUID) error
}
```

### 2. The concrete `postgresRepository`

```go
type postgresRepository struct {
    q *sordb.Queries
}

// NewPostgresRepository creates a Repository backed by sqlc-generated queries.
func NewPostgresRepository(q *sordb.Queries) Repository {
    return &postgresRepository{q: q}
}

func (r *postgresRepository) FindByID(ctx context.Context, id uuid.UUID) (*Entity, error) {
    row, err := r.q.GetEntityByID(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("get entity by id: %w", err)
    }
    return toEntity(row), nil
}
```

### 3. Wiring in `cmd/<service>/main.go`

```go
queries := sordb.New(pool)
repo    := svcfoo.NewPostgresRepository(queries)
svc, _  := svcfoo.New(repo)
```

## Implementation Guide

### Step 1: Define the interface in the service package

Place the `Repository` interface at the top of `repository.go` in the service package. Include only the operations the service actually calls — avoid over-specifying the interface.

### Step 2: Implement the concrete type

Create `repository_postgres.go` with the `postgresRepository` struct. The constructor returns the `Repository` interface, not the concrete type.

### Step 3: Wire in `main.go`

Instantiate the concrete repository in `cmd/<service>/main.go` and inject it into the service constructor. Never instantiate repositories inside service methods.

### Step 4: Create a stub for tests

For unit tests, implement the interface with an in-memory stub:

```go
type stubRepository struct {
    entities map[uuid.UUID]*Entity
}

func (r *stubRepository) FindByID(_ context.Context, id uuid.UUID) (*Entity, error) {
    e, ok := r.entities[id]
    if !ok {
        return nil, ErrNotFound
    }
    return e, nil
}
```

## Compliance Checklist

- [ ] `Repository` interface is defined in the service package (`repository.go`)
- [ ] Interface declares only the operations the service needs — no over-specification
- [ ] Concrete implementation is unexported (`postgresRepository`) and returns `Repository` from constructor
- [ ] Constructor name is `NewPostgresRepository` and returns the `Repository` interface
- [ ] All concrete methods wrap errors with `fmt.Errorf("context: %w", err)`
- [ ] `Repository` is injected via the service `New` constructor, never instantiated inside service methods
- [ ] Unit tests inject a stub that implements `Repository` without a live DB

## Anti-Patterns

### Returning the concrete type from the constructor

**What it looks like:** `func NewPostgresRepository(q *sordb.Queries) *postgresRepository`

**Why it is wrong:** Callers depend on the concrete type, defeating the purpose of the interface. Test doubles cannot be swapped in.

**Correct approach:** Always return the `Repository` interface: `func NewPostgresRepository(q *sordb.Queries) Repository`

### Fat interfaces with unused methods

**What it looks like:** A `Repository` interface with 15 methods, but the service only calls 3 of them.

**Why it is wrong:** Over-specification forces test stubs to implement methods they will never be called with, adding maintenance burden.

**Correct approach:** Define the interface with only the methods the service actually calls. Use `sqlc` query functions directly for read-models that don't require business logic isolation.
