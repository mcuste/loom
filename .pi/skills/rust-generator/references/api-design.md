## Closure Bounds

- **ALWAYS** use least restrictive closure bound: `Fn` → `FnMut` → `FnOnce`
- **ALWAYS** prefer `impl Fn(T) -> R` over `Box<dyn Fn(T) -> R>` unless closure must be stored
- **NEVER** `&impl Fn` — `Fn` auto-impl'd for `&F`, use `impl Fn` directly

## Slice Patterns

- **ALWAYS** prefer slice patterns (`[first, rest @ ..]`) over index access for known-length slices

## Iterator & Collection Params

- **ALWAYS** prefer `impl IntoIterator<Item = T>` over `&[T]` for params that only iterate
- **ALWAYS** prefer `impl AsRef<Path>` over `&Path` for read-only params
- **NEVER** `impl AsRef<T>`/`impl Borrow<T>` when body immediately clones to owned — take owned type or `impl Into<Owned>`

## Naming Conventions

- **ALWAYS** use `try_` prefix for fallible variants of panicking methods
- **ALWAYS** prefer `TryFrom<T>` over `fn new(T) -> Result` for fallible constructors

## Parsing

- **ALWAYS** implement `FromStr` for types parsed from human-readable text

### Private conversions: inherent method vs trait

Private `TryFrom`/`From` on parser-stage types is idiomatic when it encodes an
invariant or names a stage. The smell is the inferred `.try_into()`, not the
trait — switch that step to a named method:

```rust
ConditionCall::try_from(expression)?.try_into()  // target type is inferred
ConditionCall::parse(expression)?                // names what it builds
```

## Trait Design

- **ALWAYS** prefer associated types when determined by implementor — generic params when multiple impls per type valid
- **NEVER** implement `Deref`/`DerefMut` for field delegation — reserved for smart pointer types

## Display & Equality

- **NEVER** implement `Display` by delegating to `Debug`
- **ALWAYS** implement `PartialEq`/`Eq` manually for entity types where identity (ID) determines equality
- **ALWAYS** prefer extension traits over standalone functions for adding methods to foreign types

## Error Type Scoping

- **NEVER** shadow `std::result::Result` in modules using multiple error types
