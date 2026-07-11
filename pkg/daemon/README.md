# Daemon

`daemon` runs the loop that turns persisted schedule records into
workflow runs. It scans enabled records, decides which are due, applies the
overlap policy, and turns each accepted occurrence into a run through
`launcher.RunLauncher`.

```text
loom daemon
    |
    v
daemon.Daemon.Run
    |
    v
Daemon.scan: enabled schedule records <---------------------+
    |                                                        |
    v                                                        |
schedule.Record.Due(now, firstScan)                          |
    |                                                        |
    +-- not due --------------------> persist NextRunAt -------+
    |                                                        |
    +-- missed one-off, no catch-up -> remove record --------+
    |                                                        |
    `-- due                                                  |
          |                                                  |
          v                                                  |
      Daemon.startScheduledRun                               |
      apply overlap policy                                   |
          |                                                  |
          +-- skip + active  -> consume this cron tick -------+
          |                                                  |
          +-- queue + active -> retry on a later scan --------+
          |                                                  |
          `-- allow or idle                                  |
                  |                                          |
                  v                                          |
          mark schedule in flight                            |
          advance cron or remove one-off                     |
                  |                                          |
                  v                                          |
          Daemon.launchScheduledRun                          |
                  |                                          |
                  v                                          |
          launcher.RunLauncher.Launch                             |
                  |                                          |
                  v                                          |
          launch result                                      |
                  |                                          |
                  v                                          |
          Daemon.complete                                    |
          - clear in-flight state                            |
          - persist LastRunAt and LastRunID for cron           |
                  |                                          |
                  v                                          |
wait for the next scheduled run, schedule change, completion, |
or cancellation                                              |
    |                                                        |
    +--------------------------------------------------------+
```

## Why this package exists

`pkg/schedule` owns durable records and the pure `Record.Due` timing decision.
The daemon owns the long-running coordination around that decision: scan
cadence, catch-up behavior, overlap handling, in-flight state, and completion
updates.

An accepted due occurrence becomes an opaque workflow run request. The daemon
passes it with schedule provenance to `launcher.RunLauncher`; loading workflow
YAML, validating parameters, selecting runtimes, and running tasks stay outside
this package. The daemon watches `$LOOM_HOME/schedules` and rescans as soon as
a schedule record changes. A 10-minute reconciliation timer catches filesystem
events that may have been missed.
