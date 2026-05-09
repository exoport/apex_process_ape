---
id: ADR-0003
title: "Dependency Injection Pattern"
status: accepted
type: architectural
tags: [go, dependency-injection, functional-options, constructor, testing]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# Dependency Injection Pattern

## Summary

In the context of Go backend services with multiple dependencies (DB, NATS, config, logger), facing the challenge of ad-hoc constructor patterns that cause inconsistency and impede testing, we decided to use constructor-based dependency injection with functional options, to achieve clean separation between wiring and logic with easy test double injection, accepting a small upfront ceremony cost for each new service.

## Context

- Services have mandatory dependencies (repository, logger) and optional ones (config overrides, test doubles).
- Without a consistent constructor pattern, each service invents its own wiring convention.
- Test doubles (stub repositories, mock publishers) need to be injectable without changing the production code path.
- Functional options are idiomatic Go for optional constructor parameters.

## Decision

Use constructor-based dependency injection with functional options:

```go
type Option func(*service)

func WithLogger(l *zerolog.Logger) Option {
    return func(s *service) {
        if l != nil {
            s.logger = l
        }
    }
}

func New(repo Repository, opts ...Option) (*service, error) {
    nop := zerolog.Nop()
    s := &service{repo: repo, logger: &nop}
    for _, o := range opts {
        o(s)
    }
    return s, nil
}
```

Wire all dependencies in `cmd/<service>/main.go`. Never instantiate services inside other services.

## Considered Alternatives

- **Config struct constructor:** `New(cfg Config)` with a flat config struct. Rejected — optional fields require pointer fields or sentinel values; test doubles require constructing a full config.
- **Global/package-level variables:** Rejected — creates hidden coupling and makes parallel tests impossible.

## Consequences

**Positive:**

- Clean separation between construction and business logic.
- Easy to provide test doubles via `With*` options.
- Optional dependencies have sensible defaults (nop logger, nil-safe guards).

**Negative / Tradeoffs:**

- `With*` functions add boilerplate per service.
- The unexported `service` struct requires a corresponding exported interface.

## Compliance Notes

PV validates that services define `type Option func(*service)` in `options.go` and apply options after initialising defaults in `New`.
