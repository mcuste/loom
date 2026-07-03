## Newtypes

- **ALWAYS** parse external input into validated newtypes at system boundaries if value crosses >1 function boundary
- **NEVER** force newtypes in generic pipelines — trait bounds are the abstraction
- **NEVER** newtype if value crosses only one function boundary; exception: safety-critical argument transposition
- **ALWAYS** newtype public API parameters even if used once internally

## Enum State Machines vs Typestate

- **ALWAYS** prefer enum state machines by default
- **ALWAYS** prefer typestate for safety-critical protocols where wrong transition is a vulnerability
- **NEVER** use typestate for >7 states or heterogeneous storage needs
- **ALWAYS** consider typestate for builders with required fields

## Compile-Time Computation

- **ALWAYS** prefer `const fn` + `const` over `lazy_static!`/`OnceLock` for compile-time computable values
- **NEVER** shoehorn runtime values (env vars, config) into `const` — use `OnceLock`
- **NEVER** fight `const fn` limitations — use `OnceLock` when unsupported ops needed

## Representation & Layout

- **ALWAYS** use const generics for fixed dimensions known at compile time
- **NEVER** use const generics for runtime-sized or heterogeneous-collection scenarios

## Dispatch

- **ALWAYS** prefer `dyn Trait` at 5+ cascading bounds, for heterogeneous collections, or to reduce monomorphization bloat in large binaries
- **ALWAYS** prefer `-> impl Iterator<Item = T>` over `-> Vec<T>` when caller only iterates
- **ALWAYS** return `Vec<T>` when callers need `.len()` before iterating or iterator would borrow local state

## Lifetimes

- **ALWAYS** use `for<'a>` HRTB when closure must accept borrows of any lifetime
- **ALWAYS** prefer concrete lifetime over HRTB for single known lifetime
- **ALWAYS** prefer returning owned data at public API boundaries when lifetime would propagate into caller structs

## Architecture Selection

- **NEVER** apply hexagonal for CRUD under ~5k lines — unless team consistency requires it
- **NEVER** adopt CQRS/event sourcing as default — only for audit trails, temporal queries, separate read/write scaling
- **ALWAYS** use hexagonal when 3+ infrastructure deps need independent testing

## Hexagonal

- **ALWAYS** use generics for ports — switch to `dyn Trait` when generic params reach 4-5
- **NEVER** define port traits in infrastructure — ports belong in domain
- **ALWAYS** co-locate port trait with domain types it operates on; exception: cross-cutting ports in `domain::ports`

## DI

- **ALWAYS** prefer call-site injection over stored deps; store in `self` only for per-instance state or 5+ call level threading

## Workspace

- **ALWAYS** use flat `crates/` layout for 10k-1M line workspaces
- **NEVER** create crate under ~500 lines with one dependent — use `pub(crate)` module; exception: `#[no_std]` shared with embedded
- **NEVER** extract module into own crate unless it has own error types + public API, is shared across binaries, or measurably hurts compile times
- **ALWAYS** use `[workspace.dependencies]` for shared dependency versions
- **ALWAYS** use `[workspace.lints]` for shared lint configuration
- **ALWAYS** watch for dependency hub crates bottlenecking incremental compilation

## Functional Core, Imperative Shell

- **ALWAYS** extract decision logic out of async fns into pure `fn(data) -> enum/tuple` — keeps I/O and decisions separate
- **ALWAYS** extract pure fn before reaching for trait-based mocks — traits belong in I/O shell integration tests, not branch-logic tests
- **NEVER** apply FCIS to pure I/O pipelines (no branching) — no decisions means nothing to extract

## Layering

- **ALWAYS** keep domain layer free of runtime I/O — derive macros acceptable
- **NEVER** introduce full four-layer structure for projects under ~5k lines

## Project Structure Details

- **ALWAYS** order `Cargo.toml`: `[package]` → `[features]` → `[dependencies]` → `[dev-dependencies]` → `[build-dependencies]` → `[lints]`, alpha-sorted within each
- **ALWAYS** place integration test helpers in `tests/common/mod.rs`
- **ALWAYS** keep `pub mod prelude` minimal (core types, traits, `Result` alias)
- **ALWAYS** place multiple binaries in `src/bin/`; shared logic in `lib.rs`
