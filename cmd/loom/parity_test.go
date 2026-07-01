package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// parityBar is the 40-rune rule the summary block draws above and below the
// totals, built with strings.Repeat so the count is pinned rather than relying
// on a literal a reviewer cannot eyeball.
var parityBar = strings.Repeat("─", 40)

// TestDoCheckParity is the end-to-end byte-for-byte check that the rendering
// seam left `loom check` output unchanged. doCheck's stdout is fully
// deterministic (no run-file path, cwd, or timings), so the whole block is
// compared against the pre-seam golden text.
func TestDoCheckParity(t *testing.T) {
	home := loomHomeForTest(t)
	path := writeWorkflow(t, "name: demo\ntasks:\n  - id: a\n    command: echo hi\n")

	var buf bytes.Buffer
	if err := doCheck(&buf, home, path, nil); err != nil {
		t.Fatalf("doCheck: %v\noutput:\n%s", err, buf.String())
	}

	want := "Workflow : demo\n" +
		"Runtime  : -\n" +
		"Model    : -\n" +
		"Effort   : -\n" +
		"\n" +
		"Execution order (1 task):\n" +
		"   1. a  kind=shell  cmd=\"echo hi\"  deps=none\n"
	if got := buf.String(); got != want {
		t.Errorf("doCheck output mismatch:\n got=%q\nwant=%q", got, want)
	}
}

// TestDoRunParity is the end-to-end parity check for `loom run`. The run-file
// path, cwd, and per-task elapsed time are non-deterministic, so they are
// normalized to placeholders before comparing the remaining bytes against the
// pre-seam golden text. Everything else (plan, progress framing, summary block)
// must match exactly.
func TestDoRunParity(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	path := writeWorkflow(t, "name: demo\ntasks:\n  - id: a\n    command: echo hi\n")

	var buf bytes.Buffer
	if err := doRun(&buf, home, path, nil); err != nil {
		t.Fatalf("doRun: %v\noutput:\n%s", err, buf.String())
	}

	got := buf.String()
	got = regexp.MustCompile(`(?m)^Run file : .*$`).ReplaceAllString(got, "Run file : <path>")
	got = regexp.MustCompile(`(?m)^Cwd      : .*$`).ReplaceAllString(got, "Cwd      : <cwd>")
	got = regexp.MustCompile(`done [^ ]+  exit`).ReplaceAllString(got, "done <d>  exit")

	want := "Workflow : demo\n" +
		"Runtime  : -\n" +
		"Model    : -\n" +
		"Effort   : -\n" +
		"\n" +
		"Execution order (1 task):\n" +
		"   1. a  kind=shell  cmd=\"echo hi\"  deps=none\n" +
		"\n" +
		"Run file : <path>\n" +
		"Cwd      : <cwd>\n" +
		"\n" +
		"[1/1] a (shell)\n" +
		"  a done <d>  exit=0\n" +
		"\n" +
		parityBar + "\n" +
		"  total tokens : 0 in / 0 out / 0 cache-read\n" +
		"  total cost   : $0.000000\n" +
		parityBar + "\n" +
		"✓ workflow \"demo\" complete\n"
	if got != want {
		t.Errorf("doRun output mismatch:\n got=%q\nwant=%q", got, want)
	}
}
