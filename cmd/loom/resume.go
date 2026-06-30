package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

func newResumeCmd() *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a previous workflow run, skipping tasks that already completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doResume(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	return cmd
}

// doResume loads the named run record and re-runs the workflow with `ok`
// tasks seeded with their stored output. The literal id "latest" follows the
// most-recently-updated $LOOM_HOME/runs/<wf>/latest.json symlink. When the
// record carries the directory the original run was invoked from, this chdirs
// into it before re-running so the resumed run's shell tasks and relative paths
// resolve against the original dir rather than the resume's launch dir.
func doResume(w io.Writer, runID string, paramArgs []string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	rec, err := loadRunRecord(home, runID)
	if err != nil {
		return err
	}
	if err := chdirToRecorded(w, rec.Cwd); err != nil {
		return err
	}
	// The original workflow file path is not stored in the record, so the parent
	// links with an empty selfPath: path refs resolve relative to the restored
	// cwd and the parent has no on-disk identity for cycle detection.
	return runFromRecord(w, home, "", []byte(rec.Manifest), rec, paramArgs)
}

// doRunResumeLatest is the --resume-latest entry point for `loom run`. The
// workflow body comes from the YAML on disk; the record only supplies the
// seeded outputs and the original params.
func doRunResumeLatest(w io.Writer, path string, paramArgs []string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	path, err = resolveWorkflowRef(path)
	if err != nil {
		return err
	}
	// The YAML path arg is relative to the CURRENT cwd, so read and parse the
	// manifest BEFORE any chdir; otherwise a relative path would resolve against
	// the recorded dir and miss the file the user pointed at. ReadAndParse inlines
	// `prompt_file:` refs relative to the workflow's directory (still the current
	// cwd here) so the resumed run stores and replays the same self-contained
	// manifest as a fresh run.
	wf, manifest, err := workflow.ReadAndParse(path)
	if err != nil {
		return err
	}
	recPath := filepath.Join(home, "runs", string(wf.ID), "latest.json")
	rec, err := store.Load(recPath)
	if err != nil {
		return err
	}
	if err := chdirToRecorded(w, rec.Cwd); err != nil {
		return err
	}
	return runFromRecord(w, home, path, manifest, rec, paramArgs)
}

// chdirToRecorded changes into cwd (the directory the original run was invoked
// from) when it is recorded and differs from the current dir, so a resumed
// run's shell tasks and relative paths resolve against it. A blank cwd or one
// already matching the current dir is a no-op.
func chdirToRecorded(w io.Writer, cwd string) error {
	if cwd == "" {
		return nil
	}
	here, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if cwd == here {
		return nil
	}
	if err := os.Chdir(cwd); err != nil {
		return fmt.Errorf("chdir to recorded run dir %s: %w", cwd, err)
	}
	if _, err := fmt.Fprintf(w, "Cwd      : %s (restored from run record)\n", cwd); err != nil {
		return err
	}
	return nil
}

// runFromRecord drives a resume invocation: parse the manifest, merge the
// record's params (as the lower-precedence tier) with any CLI overrides,
// build the seed from ok tasks, then dispatch a fresh run that bypasses
// seeded tasks entirely. Ids present in the record but no longer in the
// current workflow are dropped from the seed (they cannot be re-gated and
// must not contaminate the executor's task count). For each surviving
// seeded task, the new run record gets a synthetic ok entry written through
// the store hooks before the executor starts, so a subsequent resume of
// THIS run finds them already-completed instead of re-dispatching.
func runFromRecord(w io.Writer, home, selfPath string, manifest []byte, rec *store.RunRecord, paramArgs []string) error {
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return err
	}
	// Re-resolve, link, and validate sub-workflow children from disk at resume
	// time, just as a fresh run does. selfPath is the parent's on-disk path when
	// known (--resume-latest reads it from disk); for a stored-manifest resume it
	// is empty, so path refs resolve relative to the (restored) working directory
	// and registry-name refs through the cwd-based registry roots.
	if err := linkAndValidate(wf, selfPath); err != nil {
		return err
	}

	// Seed every ok task from the record unfiltered; resolveSeed is the single
	// authority that drops ids no longer present in the current workflow (the
	// executor ignores Seed keys with no matching task).
	plan := seedPlanFromRecord(rec)

	// Run the shared check phase, annotating the plan with the seeded tasks,
	// then execute. The record's params are the lower-precedence tier under any
	// CLI overrides.
	seeded := resolveSeed(wf, plan).set
	return renderCheckRun(w, runRequest{wf: wf, manifest: manifest, home: home, plan: plan}, paramInputs{cli: paramArgs, file: rec.Params}, seeded)
}
