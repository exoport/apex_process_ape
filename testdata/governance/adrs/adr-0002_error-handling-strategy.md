---
id: ADR-0002
title: "Error Handling Strategy"
status: accepted
type: architectural
tags: [go, error-handling, wrapping, propagation]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# Error Handling Strategy

## Summary

In the context of Go backend services with multiple layers (handler → service → repository), facing the challenge of inconsistent error wrapping and loss of error context across package boundaries, we decided to adopt explicit wrapping via `fmt.Errorf("context: %w", err)` and typed sentinel errors, to achieve full error chain traceability and consistent HTTP error mapping, accepting the verbosity cost of wrapping at every boundary.

## Context

- Go's error model is explicit: every function that can fail returns an error.
- Without consistent wrapping, errors lose context as they propagate up the stack.
- Mixed patterns (logging + returning, swallowing, raw returns) produce unpredictable behaviour.
- HTTP handlers need a reliable way to map domain errors to status codes.

## Decision

- Always wrap errors at package boundaries with `fmt.Errorf("context: %w", err)`.
- Define sentinel errors as `var Err<Type> = errors.New(...)` at the package level.
- Define structured error types implementing `error` with `errors.As` support for typed inspection.
- Never log **and** return the same error — choose one; logging is the handler boundary's responsibility.
- HTTP handlers map errors to status codes via a central error mapper that uses `errors.Is` / `errors.As`.

## Considered Alternatives

- **Third-party error libraries (pkg/errors, go-multierror):** Rejected — standard library `errors` package is sufficient since Go 1.13; additional dependencies increase maintenance surface.
- **Panic/recover for unexpected errors:** Rejected — panics cross goroutine boundaries unpredictably; explicit returns are idiomatic.

## Consequences

**Positive:**

- Full error chain preserved and inspectable via `errors.Is` / `errors.As`.
- Consistent HTTP error responses from the central mapper.
- Logging is centralised at the handler boundary, avoiding duplicate log entries.

**Negative / Tradeoffs:**

- Wrapping at every boundary is verbose.
- Incorrect wrapping (missing `%w`) silently breaks `errors.Is` chains.

## Compliance Notes

PV validates that `fmt.Errorf("...: %w", err)` is used at package boundaries and that sentinel errors follow the `Err<Type>` naming convention.
