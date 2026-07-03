## Domain: CLI

- **ALWAYS** use clap derive over builder; `argh`/`lexopt` for size-constrained
- **ALWAYS** normalize config sources into single `Ctx` struct; skip for ≤3-flag single-command CLIs
- **ALWAYS** put each subcommand in own module with `run(args, ctx) -> Result<()>`
- **ALWAYS** write diagnostics/progress to stderr, data to stdout
