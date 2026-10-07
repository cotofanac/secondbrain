# Deployment, upgrades, and restoration

The current application uses schema version 9. Database migrations run
transactionally at startup. Before migrating, the app saves a standalone copy
next to the database as `secondbrain.db.v<version>-<UTC time>`. Retain the previous
image tag/digest or binary alongside this copy. `latest` alone is insufficient
for rollback. Do not remove migration markers or edit applied migrations.

## Current deployment settings

`PASSCODE` is an eight-digit secret. `PORT` defaults to 8080 and `DATA_DIR` to
`./data`. Calendar dates and reminders use `TZ` (default `Europe/Bucharest`).
`TASK_REMINDER_TIME` defaults to `09:00`; `off` disables daily reminders. Changes
to these variables apply on restart; there is no settings screen. Each device
enables push in Today. Preserve `PUSH_SUBJECT` and VAPID keys in the database.

`INACTIVITY_LOGOUT_MINUTES` defaults to 45. Set `TRUSTED_PROXY` to the proxy IPs or
CIDRs when forwarding client addresses. Do not expose the passcode in logs or
commit deployment secrets. `/health` is unauthenticated for readiness checks.

The supplied Docker container writes as UID 1000. Its data and backup bind mounts
must be writable by that user. `BACKUP_KEEP` defaults to seven daily snapshots;
zero disables them. The binary uses `BACKUP_DIR`, defaulting to `DATA_DIR/backups`.
Compose's `BACKUP_PATH` selects the host backup folder. Put an additional copy on
another disk or host if recovery must survive losing the deployment disk.

## Upgrade and rollback

1. Record the current image tag/digest and deployment variables.
2. Stop the app cleanly; the shutdown checkpoints SQLite's WAL.
3. Retain a standalone backup. With SQLite installed on the host:

   ```sh
   sqlite3 data/secondbrain.db ".backup 'data/secondbrain-before-upgrade.db'"
   sqlite3 data/secondbrain-before-upgrade.db 'PRAGMA integrity_check;'
   ```

   The integrity result should be `ok`. Use your actual data location.
4. Start the new pinned image. Check `/health` and startup logs. Verify notes,
   projects, headings, repeating tasks, and archive/undo.
5. On each installed device, test push from Today and opening its notification.

To roll back, stop the new app and move the entire current data directory aside,
including `secondbrain.db-wal` and `secondbrain.db-shm`. Create a clean data
directory containing the pre-upgrade standalone database named `secondbrain.db`,
with ownership matching the container, and start the previous pinned image.
Changes after the backup will not exist in that restored state. Preserve the
moved directory until recovery is verified; never mix old snapshots with newer
WAL sidecars.

## Restore a daily snapshot

Daily snapshots are consistent standalone SQLite files produced by `VACUUM INTO`
while the app runs. To restore one, stop the app, preserve its complete current
data directory as above, and put the selected snapshot into a clean data directory
as `secondbrain.db`. Verify integrity, restore directory ownership and deployment
variables, then start a compatible image. Startup migrates older supported
snapshots when necessary.

Snapshots contain tasks, repeat relationships, notes, projects, headings, VAPID
keys, subscriptions, delivery records, and other database state. Browser-local
unsaved drafts and deployment variables are separate; keep the deployment's
calendar and reminder configuration. The Go suite exercises restoring this
application state through the normal startup path.

## Migration history

- **9:** project revision counters and guarded, expiring undo operations.
- **8:** removes reminder/time-zone settings formerly saved by the settings screen;
  `TASK_REMINDER_TIME` and `TZ` now supply them.
- **7:** removes retired project-note links; notes remain independent.
- **6:** stages become headings. Tasks under archived stages are individually
  archived and detached before those stages are removed.
- **5:** the September 2026 rebuild normalises timestamps to UTC RFC3339, enforces
  flags and references, removes weekly reviews and task positions, and retires
  habits in favour of repeating tasks.

Databases older than version 5 are refused. Upgrade them once with the
2026-09-27 release (commit `02c622b`) before running this application.

See [ARCHITECTURE.md](ARCHITECTURE.md) for current behavior and design decisions,
and [DEVELOPMENT.md](DEVELOPMENT.md) for verification and real-device checks.
