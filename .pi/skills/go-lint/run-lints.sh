#!/usr/bin/env bash
set -u -o pipefail

# Project Go lint runner for pi and pi subagents.
# Runs the full Loom gate from AGENTS.md and prints compact sections only when
# a required tool or command fails.

start_path="${1:-$PWD}"

if [ -f "$start_path" ]; then
	start_dir="$(dirname "$start_path")"
else
	start_dir="$start_path"
fi

if ! cd "$start_dir" 2>/dev/null; then
	echo "go-lint: path not found: $start_path"
	exit 1
fi

if ! command -v go >/dev/null 2>&1; then
	echo "go-lint: go is not installed or not on PATH"
	exit 1
fi

if ! command -v make >/dev/null 2>&1; then
	echo "go-lint: make is not installed or not on PATH"
	exit 1
fi

gomod="$(go env GOMOD 2>/dev/null || true)"
if [ -z "$gomod" ] || [ "$gomod" = "/dev/null" ]; then
	echo "No Go module found; nothing to lint."
	exit 0
fi

module_root="$(dirname "$gomod")"
cd "$module_root" || exit 1

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
	grep -v -E '^\s*(ok|PASS)\b' |
		grep -v -E '^\s*\?\s+' |
		grep -v -E '^\s*go: (downloading|found) ' |
		grep -v -E '^\s*=== RUN\s+' |
		grep -v -E '^\s*--- PASS:' ||
		true
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

if [ ! -f Makefile ]; then
	append_finding "Missing Makefile" "make lint-test" "No Makefile found at Go module root: $module_root"
elif ! awk -F: '/^[A-Za-z0-9_.-]+:/ { if ($1 == "lint-test") found = 1 } END { exit(found ? 0 : 1) }' Makefile; then
	append_finding "Missing Make Target" "make lint-test" "Required Makefile target 'lint-test' was not found at Go module root: $module_root"
else
	run_step "Lint and Tests" "make lint-test" make lint-test
fi

if [ "$findings_count" -gt 0 ]; then
	echo "Lint issues found ($findings_count section(s) with findings):"
	echo "$output"
	exit 1
fi

echo "All lints clean."
