package runner

import (
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// ResumeRequest assembles a runner.Request from a prior run record so the
// CLI or daemon can resume it. manifest is the workflow body to execute:
// either the stored manifest embedded in rec (for `loom resume`) or fresh
// bytes read from disk (for `loom run --resume-latest`). selfPath is the
// workflow's on-disk file path when known; it is empty for stored-manifest
// resumes where path refs resolve relative to the restored working directory.
// cliParams are the already-parsed CLI overrides (map from param name to
// value); rec.Params supplies the lower-precedence tier so the original
// run's params are reused without the caller having to re-supply them.
//
// It returns the assembled request with Wf, Manifest, Plan, and Cwd
// (from rec.Cwd) populated, the seeded task set for the plan renderer, and
// any parse error.
//
// The caller is responsible for:
//   - Calling workflow.Link on req.Wf (linking needs the CLI resolver).
//   - Setting req.Home.
//   - Calling os.Chdir(req.Cwd) when needsChdir is true and printing the
//     "Cwd restored" message.
func ResumeRequest(rec *store.RunRecord, manifest []byte, selfPath string, cliParams map[string]string) (req Request, needsChdir bool, err error) {
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return Request{}, false, err
	}

	// Seed every ok task from the record unfiltered; resolveSeed (called inside
	// Run) is the single authority that drops ids no longer present in the
	// current workflow, so no pre-filtering is needed here.
	plan := SeedPlanFromRecord(rec)

	req = Request{
		Wf:       wf,
		Manifest: manifest,
		Plan:     plan,
		Cwd:      rec.Cwd,
	}
	// needsChdir is true when the record carried a working directory, signalling
	// that the caller must os.Chdir there before executing so shell tasks and
	// relative paths resolve against the original directory.
	needsChdir = rec.Cwd != ""
	return req, needsChdir, nil
}

// SeededSetFromRequest returns the set of task IDs that Run will skip when
// executing req, so CLI callers can annotate the plan display before calling
// renderCheckRun. It is a convenience wrapper around SeededSet for the resume
// path where the Request has already been assembled.
func SeededSetFromRequest(req Request) map[workflow.TaskID]bool {
	return SeededSet(req.Wf, req.Plan)
}
