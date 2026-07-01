package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowload"
)

func newResumeCmd(env *cliEnv) *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a previous workflow run, skipping tasks that already completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doResume(cmd.OutOrStdout(), env.home, env.catalog, args[0], paramArgs)
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
func doResume(w io.Writer, home string, catalog runtime.Catalog, runID string, paramArgs []string) error {
	rec, err := store.LoadByRunID(home, runID)
	if err != nil {
		return err
	}
	// The original workflow file path is not stored in the record, so the parent
	// links with an empty selfPath: path refs resolve relative to the restored
	// cwd and the parent has no on-disk identity for cycle detection.
	return runFromRecord(w, home, catalog, "", []byte(rec.Manifest), rec, paramArgs)
}

// doRunResumeLatest is the --resume-latest entry point for `loom run`. The
// workflow body comes from the YAML on disk; the record only supplies the
// seeded outputs and the original params.
func doRunResumeLatest(w io.Writer, home, cwd string, catalog runtime.Catalog, path string, paramArgs []string) error {
	// Resolve and load before any chdir, so --resume-latest still targets the
	// file the user pointed at rather than the recorded run dir.
	wf, manifest, path, err := workflowload.Load(home, cwd, path)
	if err != nil {
		return err
	}
	recPath := store.WorkflowLatestPath(home, string(wf.ID))
	rec, err := store.Load(recPath)
	if err != nil {
		return err
	}
	return runFromRecord(w, home, catalog, path, manifest, rec, paramArgs)
}

// chdirToRecorded changes into cwd (the directory the original run was invoked
// from) when it is recorded and differs from the current dir, so a resumed
// run's shell tasks and relative paths resolve against it. A blank cwd or one
// already matching the current dir is a no-op. It returns the effective working
// directory after any chdir so callers can thread it into the new run record.
func chdirToRecorded(w io.Writer, cwd string) (string, error) {
	here, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if cwd == "" || cwd == here {
		return here, nil
	}
	if err := os.Chdir(cwd); err != nil {
		return "", fmt.Errorf("chdir to recorded run dir %s: %w", cwd, err)
	}
	if _, err := fmt.Fprintf(w, "Cwd      : %s (restored from run record)\n", cwd); err != nil {
		return "", err
	}
	return cwd, nil
}

// prepareResumedRequest rebuilds the runner request from the stored run record
// and restores the recorded working directory when runner.ResumeRequest says
// the CLI must apply that process-global side effect itself.
func prepareResumedRequest(w io.Writer, selfPath string, manifest []byte, rec *store.RunRecord) (runner.Request, error) {
	req, needsChdir, err := runner.ResumeRequest(rec, manifest, selfPath, nil)
	if err != nil {
		return runner.Request{}, err
	}

	// The CLI owns the chdir: os.Chdir is a process-global side effect that
	// must not happen inside the runner package. When the record carries a
	// working directory, change into it so shell tasks and relative paths
	// resolve against the original directory.
	if needsChdir {
		cwd, err := chdirToRecorded(w, req.Cwd)
		if err != nil {
			return runner.Request{}, err
		}
		req.Cwd = cwd
	}

	return req, nil
}

// linkResumedWorkflow re-resolves, links, and validates sub-workflow children
// from disk during resume using the same CLI resolver used by fresh runs.
func linkResumedWorkflow(home, cwd, selfPath string, wf *workflow.Workflow) error {
	return workflowload.Link(home, cwd, selfPath, wf)
}

// runFromRecord drives a resume invocation. It prepares the runner request,
// re-links any sub-workflows from disk through the CLI resolver, then hands
// off to the shared check + run pipeline. Ids present in the record but no
// longer in the current workflow are dropped from the seed by runner.Run
// (they cannot be re-gated and must not contaminate the executor's task
// count). The record's params are the lower-precedence tier under any CLI
// overrides.
func runFromRecord(w io.Writer, home string, catalog runtime.Catalog, selfPath string, manifest []byte, rec *store.RunRecord, paramArgs []string) error {
	req, err := prepareResumedRequest(w, selfPath, manifest, rec)
	if err != nil {
		return err
	}

	// Re-resolve, link, and validate sub-workflow children from disk at resume
	// time, just as a fresh run does. selfPath is the parent's on-disk path when
	// known (--resume-latest reads it from disk); for a stored-manifest resume it
	// is empty, so path refs resolve relative to the (restored) working directory
	// and registry-name refs through the cwd-based registry roots.
	if err := linkResumedWorkflow(home, req.Cwd, selfPath, req.Wf); err != nil {
		return err
	}

	// Run the shared check phase, annotating the plan with the seeded tasks,
	// then execute. The record's params are the lower-precedence tier under any
	// CLI overrides.
	seeded := runner.SeededSetFromRequest(req)
	req.Home = home
	req.Catalog = catalog
	return renderCheckRun(w, req, paramInputs{cli: paramArgs, file: rec.Params}, seeded)
}
