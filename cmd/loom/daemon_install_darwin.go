//go:build darwin

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// launchdPlist is the launchd agent template. RunAtLoad starts the daemon on
// login and KeepAlive restarts it if it exits, so the schedule loop survives
// logout/reboot without the user re-running `loom daemon`.
const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>ai.loom.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>LOOM_HOME</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

// installDaemon writes a launchd agent that supervises `loom daemon`. Unless
// manual is set it also loads the agent so the daemon starts immediately;
// otherwise it prints the command to load it.
func installDaemon(w io.Writer, execPath, home string, manual bool) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	dir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	logPath := filepath.Join(home, "schedules", "daemon.log")
	path := filepath.Join(dir, "ai.loom.daemon.plist")
	content := fmt.Sprintf(launchdPlist, execPath, home, logPath, logPath)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if manual {
		_, err = fmt.Fprintf(w, "wrote launchd agent %s\n\nenable it with:\n  launchctl load %s\n", path, path)
		return err
	}
	if _, err := fmt.Fprintf(w, "wrote launchd agent %s\n", path); err != nil {
		return err
	}
	cmd := exec.Command("launchctl", "load", path)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl load %s: %w (re-run with --manual to load it yourself)", path, err)
	}
	_, err = fmt.Fprintf(w, "loaded launchd agent; the daemon is now running and will start at login\n")
	return err
}
