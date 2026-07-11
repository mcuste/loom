# Internal packages

This directory contains implementation details that support Loom's own
commands but are not part of Loom's reusable Go API. Go enforces this boundary:
packages outside the repository cannot import packages beneath `internal/`.

## `daemoninstall`

`daemoninstall` implements `loom daemon install`. It writes and optionally
enables an operating-system supervisor unit which runs `loom daemon` now and at
future logins.

```text
loom daemon install [--manual]
            |
            v
cmd/loom/daemon_cmd.go
  resolves executable path, LOOM_HOME, and stdout
            |
            v
internal/daemoninstall.Install(...)
            |
            +-- darwin: build a launchd agent specification
            +-- linux:  build a systemd user-unit specification
            +-- other:  report that installation is unsupported
            |
            v
installUnit(...)
  creates the unit directory and writes the unit file
  (including LOOM_HOME and the installing shell's PATH)
            |
            +-- --manual: print enable commands for the user
            |
            +-- default: run the supervisor enable commands
            |     macOS: launchctl load ...
            |     Linux: systemctl --user daemon-reload;
            |            systemctl --user enable --now loom-daemon
            v
supervisor starts `loom daemon` and restarts it at login/reboot
```

The package is internal because this is CLI- and platform-specific behavior,
not a stable library contract. Its unit format, filesystem locations, supported
platforms, and command behavior may change without exposing a public Go API to
downstream consumers.
