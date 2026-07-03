#!/usr/bin/env bash
set -u -o pipefail

# Project Rust lint runner for pi and pi subagents.
# Runs the full Loom gate from AGENTS.md and prints compact sections only when
# a required tool or command fails.

start_dir="${1:-$PWD}"

if ! cd "$start_dir" 2>/dev/null; then
	echo "rust-lint: path not found: $start_dir"
	exit 1
fi

if ! cargo locate-project --message-format plain >/dev/null 2>&1; then
	echo "No Cargo project found; nothing to lint."
	exit 0
fi

metadata_json="$(cargo metadata --no-deps --format-version 1 2>/dev/null)" || {
	echo "rust-lint: failed to read cargo metadata"
	exit 1
}

workspace_root="$(python3 -c 'import json, sys; print(json.load(sys.stdin)["workspace_root"])' <<<"$metadata_json")" || {
	echo "rust-lint: failed to parse cargo metadata"
	exit 1
}

cd "$workspace_root" || exit 1

DIVIDER="════════════════════════════════════════"
findings_count=0
output=""

append_finding() {
	local label="$1"
	local tool="$2"
	local body="$3"

	findings_count=$((findings_count + 1))
	output+=$'\n'"$DIVIDER"$'\n'"SECTION: $label"$'\n'"TOOL: $tool"$'\n'"$DIVIDER"$'\n'"$body"$'\n'
}

filter_noise() {
	grep -v -E '^\s*(Compiling|Downloading|Downloaded|Finished|Building|Blocking|Updating|Locking|Packaging|Fresh|Resolving|Checking) ' |
		grep -v -E '^\s*(PASS|ok) ' |
		grep -v -E '^\s*running [0-9]+ tests?' |
		grep -v -E '^\s*test result: ok\.' ||
		true
}

has_cargo_subcommand() {
	local subcommand="$1"
	cargo "$subcommand" --version >/dev/null 2>&1
}

require_cargo_subcommand() {
	local subcommand="$1"
	local install_hint="$2"

	if ! has_cargo_subcommand "$subcommand"; then
		append_finding "Missing Tool" "cargo $subcommand" "Required cargo subcommand is not installed. Install with: $install_hint"
	fi
}

run_step() {
	local label="$1"
	local tool="$2"
	shift 2

	local step_output
	local exit_code

	local -a command=("$@")
	step_output="$("${command[@]}" 2>&1)" && exit_code=0 || exit_code=$?

	if [ "$exit_code" -ne 0 ]; then
		local filtered
		filtered="$(printf '%s\n' "$step_output" | filter_noise)"
		if [ -z "$filtered" ]; then
			filtered="$step_output"
		fi
		append_finding "$label" "$tool" "$filtered"
	fi
}

# Required external cargo subcommands for this project's gate.
require_cargo_subcommand "machete" "cargo install cargo-machete"
require_cargo_subcommand "deny" "cargo install cargo-deny --locked"

run_step "Format" "cargo fmt" cargo fmt --all --check
run_step "Clippy" "cargo clippy" cargo clippy --workspace --all-targets --all-features -- -D warnings

if has_cargo_subcommand "machete"; then
	run_step "Unused Dependencies" "cargo machete" cargo machete
fi

if has_cargo_subcommand "deny"; then
	run_step "Dependency Policy" "cargo deny" cargo deny check
fi

run_step "Tests" "cargo test" cargo test --workspace --all-features

if [ "$findings_count" -gt 0 ]; then
	echo "Lint issues found ($findings_count section(s) with findings):"
	echo "$output"
	exit 1
fi

echo "All lints clean."
