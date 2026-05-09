---
id: ADR-0008
title: "Flutter State Management"
status: accepted
type: technology
tags: [flutter, riverpod, state-management, pwa]
version: v1
date: 2026-03-19
changed_at: 2026-03-19
---

# Flutter State Management

## Summary

In the context of Flutter PWA applications requiring predictable and testable state management, facing the challenge of multiple competing patterns with different trade-offs, we decided to adopt Riverpod as the sole state management solution, to achieve a compile-time safe dependency graph and easy provider-level testing, accepting a steeper initial learning curve compared to simpler alternatives.

## Context

- Flutter has a mature ecosystem of state management solutions: Provider, Riverpod, BLoC, GetX, MobX.
- Multiple patterns used in the same codebase increase maintenance burden and onboarding friction.
- PWA applications have asynchronous data loading patterns that benefit from `AsyncNotifier` semantics.
- Testability at the provider level (without running the full widget tree) is a core requirement.

## Decision

Use **Riverpod** for all state management:

- All state is expressed as `Provider`, `StateNotifierProvider`, or `AsyncNotifierProvider`.
- UI widgets extend `ConsumerWidget` or `ConsumerStatefulWidget`.
- Side effects (API calls, navigation) are encapsulated in `AsyncNotifier` subclasses.
- No global mutable state outside Riverpod providers.
- `ProviderContainer` is used in tests to isolate and override providers.

## Considered Alternatives

- **Provider (flutter_provider):** Simpler but lacks Riverpod's compile-time safety and `ref.watch` invalidation model.
- **BLoC:** Strong separation of concerns but higher boilerplate for simple state; stream-based model adds complexity.
- **GetX:** Rejected — global reactive state violates encapsulation; testing requires global state setup.

## Consequences

**Positive:**

- Compile-time safe dependency graph; missing providers are caught at build time.
- Easy to test in isolation with `ProviderContainer` and provider overrides.
- Hot reload preserves provider state, improving developer experience.
- `AsyncNotifierProvider` handles loading/error/data states idiomatically.

**Negative / Tradeoffs:**

- Steeper initial learning curve than Provider.
- Riverpod code generation (`riverpod_generator`) adds a build step.

## Compliance Notes

PV validates that no global mutable state exists outside Riverpod providers and that all UI widgets that consume state extend `ConsumerWidget` or `ConsumerStatefulWidget`.
