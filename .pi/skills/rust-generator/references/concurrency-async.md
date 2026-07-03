## Core Concurrency & Async Patterns

Rules for async Rust projects using tokio or similar runtimes.

### Interior Mutability

- **ALWAYS** escalate: `Cell` → `RefCell` → `AtomicXxx` → `Mutex` → `RwLock` (prefer `Mutex` unless profiling shows read contention)
- **NEVER** return `MutexGuard` from functions unless deliberate projected-view API
- **ALWAYS** keep lock scopes minimal — clone out, drop guard, operate; exception: expensive clones
- **ALWAYS** lock multiple mutexes in consistent documented order
- **NEVER** hold lock while sending on channel

### Async Mutex

- **ALWAYS** default to `std::sync::Mutex` in async code — `tokio::sync::Mutex` only when held across `.await`

### Async State

- **ALWAYS** design shared async state as `Clone + Send + Sync + 'static`

### select! vs spawn

- **ALWAYS** use `select!` when one completing should cancel others
- **ALWAYS** use `tokio::spawn` for independent long-lived work
- **NEVER** spawn CPU-bound work on tokio runtime — use `spawn_blocking`

### Channels

- **ALWAYS** use `oneshot` for single request/response
- **ALWAYS** use `watch` for latest-value-only shared state
- **ALWAYS** prefer `tokio::sync::Notify` over channel-based signaling for wake-without-data
- **ALWAYS** prefer `CancellationToken` over `broadcast` for shutdown
- **NEVER** use unbounded channels without explicit justification
- **ALWAYS** prefer sending owned data through channels over `Arc`-wrapping when sender doesn't need data after send

### Tracing Integration

- **ALWAYS** instrument spawned futures with `.instrument(span)`
