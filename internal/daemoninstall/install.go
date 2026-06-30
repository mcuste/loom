// Package daemoninstall writes and enables a platform supervisor unit
// (launchd on macOS, systemd on Linux) that keeps `loom daemon` running
// across logout and reboot. The exported entry point is [Install]; each
// platform provides its own implementation via build tags.
package daemoninstall

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// commandRunner runs one enable step (its argv) wiring the command's stdout
// and stderr to w. It is the seam that lets installUnit be tested without
// spawning launchctl/systemctl: production passes execRunner, tests pass a
// fake that records the steps. The platform Install functions supply it.
type commandRunner func(w io.Writer, args []string) error

// execRunner is the production commandRunner: it runs args as a real process
// with its output streamed to w.
func execRunner(w io.Writer, args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// unitSpec describes one platform's supervisor unit: the directory and
// filename the unit is written to, its already-rendered content, the commands
// that enable it, the noun used in the human-facing messages ("launchd agent",
// "systemd user unit"), and the success line printed once the unit is enabled.
// It is the only thing that differs between the launchd and systemd install
// flows.
type unitSpec struct {
	dir         string
	filename    string
	content     string
	enableSteps [][]string
	noun        string
	successMsg  string
}

// installUnit writes spec.content to spec.dir/spec.filename (0644, creating
// the directory) and then either enables the unit or prints how to. With
// manual set it writes the unit and prints the enable steps for the user to
// run; otherwise it runs each enable step via run and prints spec.successMsg.
// It is the shared body behind every platform's Install: the launchd and
// systemd flows are the same control flow over a different unitSpec.
func installUnit(w io.Writer, spec unitSpec, manual bool, run commandRunner) error {
	if err := os.MkdirAll(spec.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", spec.dir, err)
	}
	path := filepath.Join(spec.dir, spec.filename)
	if err := os.WriteFile(path, []byte(spec.content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if manual {
		var b strings.Builder
		fmt.Fprintf(&b, "wrote %s %s\n\nenable it with:\n", spec.noun, path)
		for _, args := range spec.enableSteps {
			fmt.Fprintf(&b, "  %s\n", strings.Join(args, " "))
		}
		_, err := io.WriteString(w, b.String())
		return err
	}
	if _, err := fmt.Fprintf(w, "wrote %s %s\n", spec.noun, path); err != nil {
		return err
	}
	for _, args := range spec.enableSteps {
		if err := run(w, args); err != nil {
			return fmt.Errorf("%s: %w (re-run with --manual to enable it yourself)", strings.Join(args, " "), err)
		}
	}
	_, err := io.WriteString(w, spec.successMsg)
	return err
}
