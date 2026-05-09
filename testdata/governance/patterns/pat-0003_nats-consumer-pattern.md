---
id: PAT-0003
pattern: nats-consumer-pattern
status: draft
tags: [nats, jetstream, consumer, ack, durable, messaging]
version: v1
source_codebase: diegosz/apex_process_ape
applicability: every Go service that consumes NATS JetStream messages with at-least-once delivery guarantees
changed_at: 2026-03-19
---

# nats-consumer-pattern

## Overview

NATS JetStream consumers use a durable, push-based model with explicit manual ACK and structured error handling. Every consumer declares itself durable so it survives service restarts without message loss. Handlers return typed errors; the consumer wrapper decides whether to ACK, NAK, or Term the message based on the error type.

This pattern ensures at-least-once delivery without silent message loss. Durable names tie the consumer to a specific service role; manual ACK prevents premature acknowledgement before processing completes.

## When to Use

Apply this pattern when:

- A service subscribes to a JetStream stream and must not lose messages across restarts.
- Message processing can fail transiently (DB unavailable, downstream service timeout) and must be retried.
- Permanent failures (malformed payload, business logic rejection) must be dead-lettered rather than retried indefinitely.

Do not apply this pattern for:

- Core NATS (non-JetStream) pub/sub where no persistence or delivery guarantee is needed.
- Request/reply patterns — use `nc.Request` directly.

## Reference Codebase

### Consumer struct

```go
type Consumer struct {
    js     nats.JetStreamContext
    log    *zerolog.Logger
    sub    *nats.Subscription
}

func NewConsumer(js nats.JetStreamContext, opts ...Option) *Consumer {
    nop := zerolog.Nop()
    c := &Consumer{js: js, log: &nop}
    for _, o := range opts {
        o(c)
    }
    return c
}
```

### Subscribe with explicit ACK

```go
func (c *Consumer) Subscribe(subject, durable string, handler func(msg *nats.Msg) error) error {
    sub, err := c.js.Subscribe(subject, func(msg *nats.Msg) {
        if err := handler(msg); err != nil {
            c.log.Error().Err(err).Str("subject", msg.Subject).Msg("message processing failed")
            _ = msg.Nak()
            return
        }
        _ = msg.Ack()
    },
        nats.Durable(durable),
        nats.ManualAck(),
        nats.AckExplicit(),
    )
    if err != nil {
        return fmt.Errorf("subscribe %s: %w", subject, err)
    }
    c.sub = sub
    return nil
}
```

### Graceful drain

```go
func (c *Consumer) Close() error {
    if c.sub != nil {
        return c.sub.Drain()
    }
    return nil
}
```

## Implementation Guide

### Step 1: Declare a durable consumer name

The durable name must be unique per consumer role per stream. Convention: `<service>-<subject-slug>` (e.g., `svcoutbox-records-log`).

### Step 2: Use `ManualAck` + `AckExplicit`

Never use auto-ack. The handler must explicitly call `msg.Ack()` on success and `msg.Nak()` or `msg.Term()` on failure.

### Step 3: Set `MaxDeliver` on the stream

Configure the stream's `MaxDeliver` to bound retry loops. A value of 5–10 is typical for transient retries before dead-lettering.

### Step 4: Drain on shutdown

Call `sub.Drain()` (not `Unsubscribe`) during graceful shutdown. Drain waits for in-flight messages to be ACKed before closing the subscription.

## Compliance Checklist

- [ ] Consumer is declared durable with `nats.Durable(name)` using the `<service>-<subject-slug>` convention
- [ ] `nats.ManualAck()` and `nats.AckExplicit()` are set on all subscriptions
- [ ] `msg.Ack()` is called only after successful handler completion
- [ ] `msg.Nak()` is called on transient errors
- [ ] `msg.Term()` is called on permanent errors (malformed payload, non-retriable business errors)
- [ ] Processing errors are logged before NAK/Term — never silently dropped
- [ ] `sub.Drain()` is called during graceful shutdown, not `Unsubscribe()`
- [ ] Stream `MaxDeliver` is set to prevent infinite retry loops

## Anti-Patterns

### Auto-ACK without manual handling

**What it looks like:** Using `nats.Subscribe` without `ManualAck`, allowing NATS to auto-ACK on delivery.

**Why it is wrong:** The message is ACKed before the handler runs. If the handler fails, the message is lost permanently.

**Correct approach:** Always use `ManualAck()` + `AckExplicit()`. Call `msg.Ack()` only after successful handler completion.

### Undeclared durable consumers

**What it looks like:** Subscribing with `nats.Subscribe(subject, handler)` without `nats.Durable(...)`.

**Why it is wrong:** Ephemeral consumers are removed when the subscription is closed. Service restarts lose all pending messages.

**Correct approach:** Always declare a durable name that uniquely identifies the consumer role.
