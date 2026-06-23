package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestListCommand_PlainTableAndLimit pins that `loom ls` prints a plain table
// (no browser), newest first, and that --limit caps the rows.
func TestListCommand_PlainTableAndLimit(t *testing.T) {
	loomHomeForTest(t)
	writeRunRecord(t, "deploy", "20260101T000000Z-aaa001", "name: deploy", nil, nil)
	writeRunRecord(t, "deploy", "20260102T000000Z-aaa002", "name: deploy", nil, nil)
	writeRunRecord(t, "nightly", "20260103T000000Z-bbb003", "name: nightly", nil, nil)

	// Full list: every run's short id appears.
	out := runList(t, "runs", "ls")
	for _, want := range []string{"·aaa001", "·aaa002", "·bbb003", "WORKFLOW"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
	}

	// --limit keeps only the most-recent run (all share a start time, so the
	// highest run id wins the tie-break).
	out = runList(t, "runs", "ls", "-n", "1")
	if !strings.Contains(out, "·bbb003") {
		t.Fatalf("limited list should keep the newest run:\n%s", out)
	}
	if strings.Contains(out, "·aaa001") || strings.Contains(out, "·aaa002") {
		t.Errorf("--limit 1 should drop older runs:\n%s", out)
	}

	// Workflow filter narrows to one workflow.
	out = runList(t, "runs", "ls", "deploy")
	if strings.Contains(out, "·bbb003") {
		t.Errorf("workflow filter should exclude other workflows:\n%s", out)
	}
}

// runList executes the root command with the given args, capturing stdout.
func runList(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	return buf.String()
}
