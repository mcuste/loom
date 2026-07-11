# Schedule

`schedule` owns the durable definition and timing rules for workflow schedules.
It stores cron and one-off records, computes scheduled times, and reconciles
inline workflow `schedule:` blocks. It does not run workflows or own the daemon
loop.

```text
loom schedule cron/at/sync
          |
          v
      pkg/schedule
      - validate triggers
      - construct records
      - compute NextRunAt
      - persist records atomically
          |
          v
$LOOM_HOME/schedules/<schedule-id>.json
          |
          v
      pkg/daemon
      - find due records
      - skip times missed during downtime
      - apply overlap policy
      - launch runs through launcher.RunLauncher
      - persist LastRunAt and LastRunID
```

## Schedule and trigger model

`Schedule` is the persisted domain entity. It identifies the workflow, carries
the parameters for each run, and records its timing and run state. A `Trigger` has
exactly one timing form:

- `Cron` is a recurring gronx expression evaluated in `TZ`, or local time when
  `TZ` is empty.
- `At` is a one-off UTC instant.

`NextRunAt` is the next scheduled time. `LastRunAt` is the scheduled time of
the most recent run, and `LastRunID` links that occurrence to the run store.
Scheduled timestamps use the `next_run_at` and `last_run_at` JSON keys.

## Timing and policies

`Schedule.NextRunAfter` computes the next scheduled time without side effects.
`Schedule.Due` decides whether a schedule should run at a given instant, whether a
one-off record should be removed, and which next scheduled time to persist.
The daemon owns all resulting I/O and launch coordination.

`OverlapPolicy` controls what happens when a run becomes due while the previous
run is still active:

- `OverlapSkip` consumes the due occurrence without starting another run.
- `OverlapQueue` holds the occurrence until the active run completes.
- `OverlapAllow` starts the new run concurrently.

On the first daemon scan after downtime, elapsed cron times are skipped and the
record advances to its next scheduled time. An elapsed one-off is removed. Loom
does not start delayed runs for scheduled times missed while the daemon was
stopped.

## Persistence and inline schedules

`Add`, `Update`, `Get`, `List`, and `Remove` are the storage API. Writes use a
temporary file followed by rename so a schedule record is never partially
written. `SchedulesDir` is the authority for the on-disk location.

`SyncInline` maps a workflow's inline `schedule:` block to the stable
`<workflow-id>_inline` record. Re-syncing preserves `CreatedAt`, `Enabled`,
`LastRunAt`, and `LastRunID` while recomputing `NextRunAt`. Removing the inline
block removes its synced record.

Scheduled-run logs live below `$LOOM_HOME/schedules/logs/<schedule-id>/`, but
`pkg/launcher` creates and writes those files. The schedule package owns only
schedule records.

## File layout

The package is intentionally consolidated:

- `schedule.go` contains the model, timing, persistence, and sync behavior.
- `schedule_test.go` contains the corresponding public behavior tests.
