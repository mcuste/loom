//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// systemdUnit is the systemd user-service template. Restart=always keeps the
// daemon alive, and WantedBy=default.target starts it at login once enabled.
const systemdUnit = `[Unit]
Description=loom workflow scheduler

[Service]
ExecStart=%s daemon
Environment=LOOM_HOME=%s
Restart=always

[Install]
WantedBy=default.target
`

// installDaemon writes a systemd user unit that supervises `loom daemon` and
// prints the commands to enable it.
func installDaemon(w io.Writer, execPath, home string) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	dir := filepath.Join(userHome, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "loom-daemon.service")
	content := fmt.Sprintf(systemdUnit, execPath, home)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err = fmt.Fprintf(w, "wrote systemd user unit %s\n\nenable it with:\n  systemctl --user daemon-reload\n  systemctl --user enable --now loom-daemon\n", path)
	return err
}
