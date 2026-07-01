//go:build linux

package daemoninstall

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
Environment=PATH=%s
Restart=always

[Install]
WantedBy=default.target
`

// Install writes a systemd user unit that supervises `loom daemon`. Unless
// manual is set it also reloads systemd and enables the unit so the daemon
// starts immediately; otherwise it prints the commands to enable it. It builds
// the systemd unitSpec and defers the shared write/enable flow to installUnit.
func Install(w io.Writer, execPath, home string, manual bool) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	spec := unitSpec{
		dir:      filepath.Join(userHome, ".config", "systemd", "user"),
		filename: "loom-daemon.service",
		content:  fmt.Sprintf(systemdUnit, execPath, home, daemonPATH()),
		enableSteps: [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", "loom-daemon"},
		},
		noun:       "systemd user unit",
		successMsg: "enabled systemd unit; the daemon is now running and will start at login\n",
	}
	return installUnit(w, spec, manual, execRunner)
}
