//go:build !darwin && !linux

package main

import (
	"fmt"
	"io"
	"runtime"
)

// installDaemon reports that no supervisor backend exists for the current OS.
// The daemon itself still runs in the foreground via `loom daemon`.
func installDaemon(_ io.Writer, _, _ string) error {
	return fmt.Errorf("loom daemon install is not supported on %s; run `loom daemon` under your own supervisor", runtime.GOOS)
}
