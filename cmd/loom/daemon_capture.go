package main

import (
	"path/filepath"

	"github.com/mcuste/loom/pkg/tui"
)

// captureRenderer wraps a renderer to record the run-file path the pipeline
// reports in its header, so the daemon can recover the run id without the
// pipeline returning it.
type captureRenderer struct {
	tui.Renderer
	runFile string
}

func (c *captureRenderer) Header(meta tui.RunMeta) error {
	c.runFile = meta.RunFile
	return c.Renderer.Header(meta)
}

// runIDFromPath extracts the run id from a run-record path (its basename minus
// the .json extension). Returns "" for an empty path.
func runIDFromPath(p string) string {
	if p == "" {
		return ""
	}
	base := filepath.Base(p)
	return base[:len(base)-len(filepath.Ext(base))]
}
