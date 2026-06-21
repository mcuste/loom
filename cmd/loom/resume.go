package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
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
	path, err := findRunRecord(home, runID)
	if err != nil {
		return err
	}
	rec, err := store.Load(path)
	if err != nil {
		return err
	}
	if err := chdirToRecorded(w, rec.Cwd); err != nil {
		return err
	}
	return runFromRecord(w, home, []byte(rec.Manifest), rec, paramArgs)
}

// doRunResumeLatest is the --resume-latest entry point for `loom run`. The
// workflow body comes from the YAML on disk; the record only supplies the
// seeded outputs and the original params.
func doRunResumeLatest(w io.Writer, path string, paramArgs []string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	// The YAML path arg is relative to the CURRENT cwd, so read and parse the
	// manifest BEFORE any chdir; otherwise a relative path would resolve against
	// the recorded dir and miss the file the user pointed at.
	manifest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	wf, err := workflow.Parse(manifest)
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
	return runFromRecord(w, home, manifest, rec, paramArgs)
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
func runFromRecord(w io.Writer, home string, manifest []byte, rec *store.RunRecord, paramArgs []string) (err error) {
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return err
	}

	inWorkflow := make(map[workflow.TaskID]bool, len(wf.Tasks))
	for i := range wf.Tasks {
		inWorkflow[wf.Tasks[i].ID] = true
	}

	plan := seedPlan{seed: make(map[workflow.TaskID]string, len(rec.Tasks))}
	for _, t := range rec.Tasks {
		if t.Status != store.StatusOK {
			continue
		}
		tid := workflow.TaskID(t.ID)
		if !inWorkflow[tid] {
			continue
		}
		plan.seed[tid] = t.Output
		plan.entries = append(plan.entries, seedEntry{
			id:      tid,
			prompt:  t.Prompt,
			command: t.Command,
			output:  t.Output,
		})
	}

	// Run the shared check phase, annotating the plan with the seeded tasks,
	// then execute. The record's params are the lower-precedence tier under any
	// CLI overrides.
	seeded, _ := resolveSeed(wf, plan)
	// One renderer drives the resume's check phase and the run that follows, so a
	// stateful renderer keeps a unified display across both. Its teardown error
	// surfaces unless a prior error already won.
	r := tui.New(w)
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	resolved, _, err := check(r, wf, paramArgs, rec.Params, false, seeded)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	return runWorkflow(r, w, home, manifest, wf, resolved, plan)
}

// findRunRecord resolves a user-supplied run id to a path under
// $LOOM_HOME/runs. The literal "latest" follows the most-recently-updated
// latest.json symlink across all workflows; any other id is matched verbatim
// against <home>/runs/<wf>/<runID>.json across every workflow directory. The
// runID must be a single path component (no separators) so an attacker cannot
// point the loader at an arbitrary file via `..` traversal.
func findRunRecord(home, runID string) (string, error) {
	root := filepath.Join(home, "runs")
	if runID == "latest" {
		return findLatestRecord(root)
	}
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), runID+".json")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("run id %q: not found under %s", runID, root)
}

// validateRunID rejects ids that contain a path separator; without this,
// filepath.Join silently cleans `../../foo` to a path outside the runs root and
// Load reads an arbitrary file off disk. The `..` traversal vectors (`../`,
// `..\`) all carry a separator, so the separator check covers them; a bare `..`
// substring (e.g. `a..b`) is a legitimate id and must not be rejected.
func validateRunID(runID string) error {
	if runID == "" {
		return errors.New("run id: empty")
	}
	if strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("run id %q: must be a single path component", runID)
	}
	return nil
}

// findLatestRecord picks the most-recently-modified <home>/runs/*/latest.json
// link, so `loom resume latest` resolves to the user's most recent run even
// when several workflows share the home directory.
func findLatestRecord(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	var (
		best     string
		bestTime time.Time
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		link := filepath.Join(root, e.Name(), "latest.json")
		info, err := os.Stat(link)
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			best = link
		}
	}
	if best == "" {
		return "", fmt.Errorf("no latest run found under %s", root)
	}
	return best, nil
}
