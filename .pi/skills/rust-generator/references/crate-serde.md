# Crate: serde

- **ALWAYS** use `#[serde(deny_unknown_fields)]` on config and request input types
- **ALWAYS** use `#[serde(transparent)]` on single-field newtypes
- **ALWAYS** choose enum representation explicitly: `#[serde(tag = "type")]` for most APIs, `#[serde(tag = "type", content = "data")]` for heterogeneous content
- **ALWAYS** use `#[serde(rename_all = "camelCase")]` or appropriate convention on public API types
- **ALWAYS** test serde roundtrips for types with custom serde attributes
- **NEVER** use `#[serde(untagged)]` unless no alternative exists
- **ALWAYS** prefer field-level `#[serde(default = "fn")]` over struct-level `#[serde(default)]` when only some fields have defaults
- **ALWAYS** use `#[serde(skip_serializing_if = "Option::is_none")]` on `Option` fields in API response types
- **ALWAYS** prefer `#[serde(try_from = "String")]` over custom `Deserialize` impl for validated newtypes
- **NEVER** combine `#[serde(flatten)]` with `#[serde(deny_unknown_fields)]`
- **ALWAYS** separate wire/serialization types (DTOs) from domain types when serde conflicts with domain invariants
