package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/store"
)

// showRecord is a two-task run (a shell task feeding an LLM task) with a
// manifest that parses against the registered claude-code runtime, so the
// dependency annotations resolve.
func showRecord() *store.RunRecord {
	return &store.RunRecord{
		RunID: "20260623T201809Z-0afad3", WorkflowID: "pipe", Status: "ok",
		Manifest: "name: pipe\nruntime: claude-code\nmodel: opus\n" +
			"tasks:\n  - id: build\n    command: make\n" +
			"  - id: summarize\n    prompt: do it\n    depends_on: [build]\n",
		Tasks: []store.TaskRecord{
			{ID: "build", Command: "make", Status: "ok", ElapsedMs: 2000, Output: "BUILD_ARTIFACT_X"},
			{ID: "summarize", Model: "opus", Status: "ok", ElapsedMs: 4000, Prompt: "do it", Output: "SUMMARY_TEXT_Y"},
		},
	}
}

// TestShowRunFull asserts the default (full) view prints the summary table,
// each task's prompt/command and output, and dependency annotations.
func TestShowRunFull(t *testing.T) {
	var buf bytes.Buffer
	if err := ShowRun(&buf, showRecord(), true); err != nil {
		t.Fatalf("ShowRun: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Tasks (2):", "needs=build", // summary table + dependency column
		"── command ──", "make", // build body
		"── prompt ──", "BUILD_ARTIFACT_X", "SUMMARY_TEXT_Y", // outputs
		"needs: build", // body dependency line
	} {
		if !strings.Contains(out, want) {
			t.Errorf("full view missing %q:\n%s", want, out)
		}
	}
}

// TestShowRunSummary asserts the summary view stops at the table: no prompts or
// outputs are printed, but dependency annotations still appear.
func TestShowRunSummary(t *testing.T) {
	var buf bytes.Buffer
	if err := ShowRun(&buf, showRecord(), false); err != nil {
		t.Fatalf("ShowRun: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Tasks (2):") || !strings.Contains(out, "needs=build") {
		t.Errorf("summary view should keep the table and deps:\n%s", out)
	}
	for _, unwanted := range []string{"SUMMARY_TEXT_Y", "BUILD_ARTIFACT_X", "── output ──"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("summary view should omit bodies, found %q:\n%s", unwanted, out)
		}
	}
}

// TestShowTaskUnknownIDLists errors with the available ids when the task is
// absent, so a typo is recoverable.
func TestShowTaskUnknownIDLists(t *testing.T) {
	var buf bytes.Buffer
	err := ShowTask(&buf, showRecord(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("want error listing ids, got %v", err)
	}
}
