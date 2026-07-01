//go:build darwin

package daemoninstall

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// launchdPlistName is the on-disk filename of the launchd agent; the enable
// step loads it by absolute path while installUnit writes it by name, so both
// must reference the same literal.
const launchdPlistName = "ai.loom.daemon.plist"

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
    <key>PATH</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

// Install writes a launchd agent that supervises `loom daemon`. Unless manual
// is set it also loads the agent so the daemon starts immediately; otherwise
// it prints the command to load it. It builds the launchd unitSpec and defers
// the shared write/enable flow to installUnit.
func Install(w io.Writer, execPath, home string, manual bool) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	dir := filepath.Join(userHome, "Library", "LaunchAgents")
	path := filepath.Join(dir, launchdPlistName) // launchd loads by absolute path
	logPath := filepath.Join(home, "schedules", "daemon.log")
	spec := unitSpec{
		dir:         dir,
		filename:    launchdPlistName,
		content:     fmt.Sprintf(launchdPlist, execPath, home, daemonPATH(), logPath, logPath),
		enableSteps: [][]string{{"launchctl", "load", path}},
		noun:        "launchd agent",
		successMsg:  "loaded launchd agent; the daemon is now running and will start at login\n",
	}
	return installUnit(w, spec, manual, execRunner)
}
