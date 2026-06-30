package main

import (
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
