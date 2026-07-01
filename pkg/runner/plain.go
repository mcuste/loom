package runner

import (
	"context"
	"io"

	"github.com/mcuste/loom/pkg/tui"
)

// RunPlain creates a tui.Renderer backed by w, runs the workflow described by
// req, closes the renderer, and returns the run ID and any error. It is a
// convenience wrapper for callers (such as the scheduler daemon) that do not
// need to own the renderer directly.
func RunPlain(ctx context.Context, w io.Writer, req Request) (string, error) {
	r := tui.New(w)
	defer func() { _ = r.Close() }()
	return Run(ctx, r, w, req)
}
