---
id: PAT-0004
pattern: nats-error-handling
status: draft
tags: [nats, error-handling, transient, permanent, dead-letter, term]
version: v1
source_codebase: exoport/apex_process_ape
applicability: every NATS JetStream message handler that must distinguish retriable from non-retriable errors
changed_at: 2026-03-19
---

# nats-error-handling

## Overview

NATS JetStream message handlers classify errors into two categories: **transient** (retriable) and **permanent** (non-retriable). Transient errors trigger `NakWithDelay` for backoff-based retry. Permanent errors trigger `Term` to move the message to the dead-letter stream without further retry. Malformed payloads are always permanent.

This pattern prevents infinite retry loops for non-recoverable errors while ensuring transient failures (DB unavailable, downstream timeout) are retried with appropriate backoff.

## When to Use

Apply this pattern to every JetStream message handler. The classification applies regardless of the consumer pattern (push or pull).

Do not apply this pattern for:

- Core NATS pub/sub without JetStream — no ACK/NAK semantics.
- Request/reply handlers — return errors directly to the requester.

## Reference Codebase

### Transient error type

```go
// TransientError signals a retriable failure with an optional retry delay.
type TransientError struct {
    Cause      error
    RetryAfter time.Duration
}

func (e *TransientError) Error() string {
    return fmt.Sprintf("transient: %v", e.Cause)
}

func (e *TransientError) Unwrap() error { return e.Cause }
```

### Handler with error classification

```go
func handleMessage(log *zerolog.Logger) func(msg *nats.Msg) {
    return func(msg *nats.Msg) {
        var payload MyPayload
        if err := json.Unmarshal(msg.Data, &payload); err != nil {
            // Permanent: malformed message, no point retrying
            log.Error().Err(err).Msg("malformed payload — terming message")
            _ = msg.Term()
            return
        }

        if err := processPayload(payload); err != nil {
            var transient *TransientError
            if errors.As(err, &transient) {
                delay := transient.RetryAfter
                if delay <= 0 {
                    delay = 5 * time.Second
                }
                log.Warn().Err(err).Dur("retry_after", delay).Msg("transient error — naking with delay")
                _ = msg.NakWithDelay(delay)
                return
            }
            // Permanent business error
            log.Error().Err(err).Msg("permanent error — terming message")
            _ = msg.Term()
            return
        }

        _ = msg.Ack()
    }
}
```

### Wrapping transient errors at the service layer

```go
func (s *service) processAction(ctx context.Context, payload MyPayload) error {
    if err := s.repo.Save(ctx, toEntity(payload)); err != nil {
        if isDBUnavailable(err) {
            return &TransientError{Cause: err, RetryAfter: 10 * time.Second}
        }
        return fmt.Errorf("save entity: %w", err)
    }
    return nil
}
```

## Implementation Guide

### Step 1: Define `TransientError` in the service package

Place `TransientError` in a shared `errors.go` file. It must implement `error` and `Unwrap`.

### Step 2: Classify errors at the service layer

The service method wraps transient conditions (DB connection errors, timeouts) in `TransientError`. Business rule violations are returned as plain errors.

### Step 3: Inspect error type in the NATS handler

Use `errors.As(err, &transient)` to check for `TransientError`. Fall through to `Term` for all non-transient errors.

### Step 4: Always log before ACK/NAK/Term

Log the error with sufficient context (subject, payload ID if available) before calling any ACK method. Never swallow errors silently.

## Compliance Checklist

- [ ] `TransientError` type is defined with `Cause error` and `RetryAfter time.Duration` fields
- [ ] `TransientError` implements `Unwrap() error`
- [ ] Malformed payloads (unmarshal failures) always call `msg.Term()` — never NAK
- [ ] Transient errors call `msg.NakWithDelay(retryAfter)` with a non-zero delay
- [ ] Permanent business errors call `msg.Term()`
- [ ] All errors are logged with subject and error details before ACK/NAK/Term
- [ ] Service methods wrap retriable conditions in `TransientError`, not raw errors
- [ ] Handler uses `errors.As` (not type assertion) to check for `TransientError`

## Anti-Patterns

### NAKing malformed payloads

**What it looks like:** An unmarshal error triggers `msg.Nak()`, causing the same malformed message to be redelivered indefinitely until `MaxDeliver` is reached.

**Why it is wrong:** A malformed payload will never unmarshal successfully. Retrying it wastes resources and fills logs with identical errors.

**Correct approach:** Call `msg.Term()` for any payload that cannot be parsed. Optionally publish the raw bytes to a dead-letter subject for inspection.

### Swallowing errors without logging

**What it looks like:** `_ = msg.Nak()` called without any log statement after a failed handler.

**Why it is wrong:** Silent failures are undetectable in production. Operators cannot distinguish a retrying consumer from a stuck one.

**Correct approach:** Always log the error (at `Warn` for transient, `Error` for permanent) before calling the ACK method.
