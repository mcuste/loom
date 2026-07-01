package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

func newResumeCmd(env *cliEnv) *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a previous workflow run, skipping tasks that already completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doResume(cmd.OutOrStdout(), env.home, args[0], paramArgs)
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
func doResume(w io.Writer, home, runID string, paramArgs []string) error {
	rec, err := store.LoadByRunID(home, runID)
	if err != nil {
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
func doRunResumeLatest(w io.Writer, home, path string, paramArgs []string) error {
	path, err := resolveWorkflowRef(home, path)
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
	recPath := store.WorkflowLatestPath(home, string(wf.ID))
	rec, err := store.Load(recPath)
	if err != nil {
		return err
	}
	return runFromRecord(w, home, path, manifest, rec, paramArgs)
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

// runFromRecord drives a resume invocation. It delegates domain assembly
// (manifest parse, seed plan, cwd) to runner.ResumeRequest, then handles
// the CLI-specific steps: chdir into the recorded working directory,
// workflow linking via the CLI resolver, and rendering + execution.
// Ids present in the record but no longer in the current workflow are
// dropped from the seed by runner.Run (they cannot be re-gated and must
// not contaminate the executor's task count). The record's params are the
// lower-precedence tier under any CLI overrides.
func runFromRecord(w io.Writer, home, selfPath string, manifest []byte, rec *store.RunRecord, paramArgs []string) error {
	req, needsChdir, err := runner.ResumeRequest(rec, manifest, selfPath, nil)
	if err != nil {
		return err
	}

	// The CLI owns the chdir: os.Chdir is a process-global side effect that
	// must not happen inside the runner package. When the record carries a
	// working directory, change into it so shell tasks and relative paths
	// resolve against the original directory.
	if needsChdir {
		cwd, err := chdirToRecorded(w, req.Cwd)
		if err != nil {
			return err
		}
		req.Cwd = cwd
	}

	// Re-resolve, link, and validate sub-workflow children from disk at resume
	// time, just as a fresh run does. selfPath is the parent's on-disk path when
	// known (--resume-latest reads it from disk); for a stored-manifest resume it
	// is empty, so path refs resolve relative to the (restored) working directory
	// and registry-name refs through the cwd-based registry roots.
	if err := workflow.Link(req.Wf, selfPath, func(ref, parentDir string) (string, error) {
		return resolveSubWorkflowRef(home, ref, parentDir)
	}); err != nil {
		return err
	}

	// Run the shared check phase, annotating the plan with the seeded tasks,
	// then execute. The record's params are the lower-precedence tier under any
	// CLI overrides.
	seeded := runner.SeededSetFromRequest(req)
	req.Home = home
	return renderCheckRun(w, req, paramInputs{cli: paramArgs, file: rec.Params}, seeded)
}
