//go:build !darwin && !linux

package daemoninstall

import (
	"fmt"
	"io"
	"runtime"
)

// Install reports that no supervisor backend exists for the current OS. The
// daemon itself still runs in the foreground via `loom daemon`.
func Install(_ io.Writer, _, _ string, _ bool) error {
	return fmt.Errorf("loom daemon install is not supported on %s; run `loom daemon` under your own supervisor", runtime.GOOS)
}
