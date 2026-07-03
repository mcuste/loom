## Advanced Concurrency Patterns

Rules for complex concurrent systems: task management, cancellation, actors, concurrency limiting.

### Cancellation Safety

- **NEVER** place non-cancel-safe futures in `select!` arms — wrap in `tokio::spawn`
- **ALWAYS** document cancellation safety of public async functions used in `select!`

### Structured Concurrency

- **ALWAYS** prefer `JoinSet` over `FuturesUnordered` for spawned tasks
- **ALWAYS** prefer `FuturesUnordered` for local concurrency without spawning
- **NEVER** drop `JoinHandle` expecting cancellation — call `.abort()`

### Concurrency Limiting

- **ALWAYS** prefer `tokio::sync::Semaphore` over manual `AtomicUsize` for concurrency limiting
- **ALWAYS** prefer `Semaphore::acquire_owned` when permit must outlive semaphore borrow

### Pinning

- **ALWAYS** use `biased;` in `tokio::select!` when one branch must be checked first
- **ALWAYS** prefer `std::pin::pin!()` over `Box::pin()` when future consumed in same scope

### Actor Pattern

- **ALWAYS** spawn actor task inside handle constructor
- **ALWAYS** use bounded channels for actor mailboxes
- **NEVER** create bounded channel cycles between actors
- **ALWAYS** rely on sender-drop for shutdown; explicit shutdown message before dropping for graceful drain
