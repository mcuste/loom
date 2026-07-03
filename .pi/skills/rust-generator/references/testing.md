# Crates: Testing Tools

## Test Strategy

- **ALWAYS** use TDD for domain logic, algorithms, libraries, public APIs, bug fixes, complex state
- **NEVER** use TDD for UI-heavy code or legacy code without tests (use characterization tests)
- **NEVER** test trivial getters/simple mappings
- **ALWAYS** refactor high-complexity many-collaborator code before testing
- **ALWAYS** unit test high-complexity few-collaborator code heavily
- **ALWAYS** integration test low-complexity many-collaborator code

## Integration Tests

- **ALWAYS** expose through `lib.rs` any logic for integration testing
- **ALWAYS** mock only unmanaged deps (SMTP, message bus); use real managed deps (app-owned DB)
- **ALWAYS** mock at outermost edge; mock only types you own
- **ALWAYS** prefer one happy path + edge cases unit tests can't reach
- **ALWAYS** run integration tests sequentially
- **ALWAYS** place API contract tests in `tests/`

## External Dependency Testing Tiers

Test at the cheapest tier that covers the risk. Escalate only when a lower tier can't exercise the behavior under test.

- **ALWAYS** pick the lowest-cost tier that covers the risk — never default to a heavier tier
- **ALWAYS** use injected async closures or pure functions as the default (tier 1) — no Docker, no network, instant feedback
- **ALWAYS** use `wiremock` (tier 2) when testing real HTTP client behavior (headers, retries, error codes) — still in-process, no Docker
- **ALWAYS** use `testcontainers` with service emulators (tier 3) when protocol fidelity matters (gRPC, auth chains, transaction semantics) — requires Docker in CI
- **ALWAYS** gate real-service tests with `#[ignore]` (tier 4) when no emulator exists — run in dedicated CI job with credentials
- **NEVER** jump to tier 3/4 when tier 1/2 suffices — Docker overhead is not free

## Niche Testing Rules

- **ALWAYS** add `#[track_caller]` on test helper functions containing assertions
- **ALWAYS** use `#[should_panic(expected = "substring")]` over bare `#[should_panic]`
- **ALWAYS** consider Sans-IO for protocol impls; **NEVER** for simple request/response

## Functional Core, Imperative Shell

- **ALWAYS** extract decision logic from async fns into pure `fn(data) -> data + side-effect instructions`
- **ALWAYS** keep I/O shell to: read → call pure fn → write — no branching beyond dispatching on pure fn's return
- **ALWAYS** use decision enum (`UseCache`, `TryRefresh`, `FullFlow`) when pure fn's output picks which I/O path runs next
- **ALWAYS** test pure decision fns with plain sync unit tests — no traits, mocks, or async runtime
- **ALWAYS** test I/O shell via integration tests (`#[ignore]`) — unit tests OK only if shell has non-trivial orchestration
- **NEVER** apply FCIS to pure I/O pipelines (get→pass→return, no branching) — integration-test directly
- **NEVER** model I/O-dependent recovery/fallback as second decision enum — indirection costs more than it saves; integration-test instead
- **ALWAYS** clean up at test start, not teardown
- **NEVER** test repositories directly — only within integration tests
- **ALWAYS** prefer `assert!(matches!(value, Pattern))` or `assert_matches!` over manual `match` + `panic!`
- **ALWAYS** use `tempfile::TempDir`/`NamedTempFile` over hardcoded paths in tests
- **ALWAYS** await properly in async tests
- **NEVER** sleep in tests — use deterministic sync

## Parameterized & Property Testing

- **ALWAYS** use `rstest` for parameterized tests and fixtures
- **ALWAYS** use `proptest` for invariants over arbitrary input

## Snapshot Testing

- **ALWAYS** use `insta`/`expect-test` for snapshot testing complex output

## Mocking

- **ALWAYS** use `mockall` when verifying specific call args and fake is impractical
- **ALWAYS** use `wiremock` for HTTP mocking

## CLI & Fuzz Testing

- **ALWAYS** use `assert_cmd` + `predicates` for CLI e2e testing
- **ALWAYS** use `cargo-fuzz` for security-sensitive parsers
