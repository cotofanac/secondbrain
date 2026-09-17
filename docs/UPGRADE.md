# Workspace update: database and rollout notes

## Database changes

**This update changes the SQLite schema on first startup. Back up the database before deploying it.** Migration 1 runs in a transaction, records its version, and is safe to run again. It adds:

- `projects`, `stages`, and `project_notes` tables.
- Optional `todos.project_id` and `todos.stage_id` references.
- `todos.completed_at` and `todos.archive_after` timestamps.
- `weekly_reviews` for immutable generated summaries.
- `push_deliveries` for per-device results, retry counts, and expiration.
- `schema_migrations` and indexes for membership, due dates, completion, and pending delivery.

Migration 2 adds `revision INTEGER NOT NULL DEFAULT 1` to `todos` and `notes`. These counters provide atomic conflict detection for direct task-title edits, task details, and note saves; `updated_at` remains display metadata rather than a concurrency token. Migration 2 is also transactional and versioned.

There are no dropped tables or columns, and existing task/note/habit rows are preserved. Existing tasks become standalone. Previously archived tasks remain archived. Existing completed tasks receive a seven-day cleanup window starting at migration; their unknown completion dates remain NULL, so the review does not claim they were completed this week.

The cleanup behavior changes: **unfinished tasks no longer automatically archive**. Completed ordinary tasks archive seven days after completion. Restoring a completed task restarts its cleanup window without changing its completion history. Groceries and Buys remain reusable lists.

## Compatibility and rollback

The old binary is not a safe rollback target for a database used by this update. Although the schema additions are compatible with its explicit-column SQL, its old cleanup job can archive unfinished project tasks, and its subscription replacement behavior can erase delivery tracking.

For rollback, stop the new app, keep a copy of the new database, and restore the pre-upgrade database together with the previous pinned image/binary. Changes made after the backup will not exist in the restored database. Do not delete migration markers or manually drop the added columns.

The push test API now requires a JSON body identifying the current device's endpoint. It no longer broadcasts a test to all devices. Push APIs return JSON errors, including HTTP 401 on expired sessions.

## Backup and deployment

1. Record the current image tag/digest or retain the old binary. Avoid relying on `latest` for rollback.
2. Stop SecondBrain cleanly (`docker compose stop secondbrain` for the supplied deployment). Shutdown checkpoints SQLite's WAL.
3. Back up the database before starting the new image. For the supplied `./data` bind mount, with `sqlite3` installed on the host:

   ```sh
   sqlite3 data/secondbrain.db ".backup 'data/secondbrain-before-workspace.db'"
   sqlite3 data/secondbrain-before-workspace.db 'PRAGMA integrity_check;'
   ```

   The integrity result should be `ok`. Store an additional copy outside the deployment directory. For a custom data location, substitute its path.

4. Start the new image. Check startup logs for successful database initialization and absence of migration/scheduler errors.
5. Check existing notes, habits, and lists; create a project and task; then confirm archive/restore.
6. Open Notifications on each installed device and run the notification test. Keep the backup until both devices have been verified.

No live database, deployment, image publication, or repository push was performed as part of local implementation validation.

## Schedule and PWA changes

On first startup, existing `TASK_REMINDER_TIME` and `HABIT_REMINDER_TIME` values seed the shared reminder schedule. After initialization, the saved schedule under Today → Reminders & notifications is authoritative; changing these environment variables does not overwrite saved preferences. Existing daily sent markers are retained to avoid duplicate reminders on migration day.

The shared calendar time zone defaults to Europe/Bucharest and is editable under Today → Reminders & notifications. It determines due-date comparisons, habit days, and daily reminder times. Scheduled weekly reviews are retired; existing saved reviews remain readable from their old links and their database rows are preserved.

Each device opts into delivery separately. Subscription registration is confirmed by the server. Revoked subscriptions are removed and remembered by an endpoint hash in settings, so resuming the app cannot silently re-register a revoked endpoint. Failed transient deliveries retry at most twice, after one and then five minutes; successful devices are not retried. Queued daily delivery attempts expire at the next local midnight. Legacy queued weekly deliveries are skipped. Server acceptance cannot establish that an Apple device actually displayed a banner.

The service worker retains `/static/sw.js` but gains `/` scope through `Service-Worker-Allowed: /`. The old `/static/` registration is removed only after a working root subscription has been saved. PNG icons and a stable manifest identity are included. A device may need to be enabled again after updating. VAPID keys remain in the existing database and must be retained across deploys.

If a push service rejects credentials, inspect the result in Notifications and configure `PUSH_SUBJECT` with a real `mailto:` contact or HTTPS contact URL. The server must have working public DNS and outbound HTTPS access to the push endpoint.

Brave requires **Use Google services for push messaging** under `brave://settings/privacy`. If it is disabled, Brave grants notification permission and then rejects `PushManager.subscribe()` with “Registration failed - push service error” before contacting SecondBrain. The app detects this case and shows the required setting. Relaunch Brave after changing it. Safari is the recommended Mac verification browser for this Apple-only deployment.

## Validation record

Local verification passed with `go test -race ./...`, the notification client checks, and the full browser workflow. Automated checks use disposable temporary databases, never `./data/secondbrain.db`:

- Go tests: legacy migration and idempotence, Today grouping and suggestions, revision conflicts, large-note saves, membership validation, reassignment, completion/archival, shared note links, ordering, template rendering/search, weekly-review retirement, per-device retry limits, push test targeting, and expired-session errors.
- `node scripts/push-smoke.cjs`: direct user activation, denied permission, rejected subscription storage, test status, and expired-subscription renewal.
- `scripts/ui-smoke.cjs`: real Chromium interactions for Today, tasks/projects/stages, completion, due-date editing, capture/notes preservation including delayed responses and cursor selection, mobile detail panels and navigation geometry, larger text, keyboard search, reminders, root worker readiness, and 320/375/390/430/1024/1440px layouts. Playwright is a development-only tool; it is not added to the application dependencies. See the script for optional `PLAYWRIGHT_MODULE`, `TEST_BROWSER`, `TEST_BASE_URL`, and `TEST_PASSCODE` environment variables. Run it only against a fresh disposable database.

Still required on real devices before rollout is considered verified:

- Installed iPhone PWA: initial permission tap, blocked-permission guidance, test delivery with the app closed, notification opening, Home Screen icon, software keyboard, safe areas, and larger system text.
- Installed Mac PWA: permission/test delivery with the app closed, Focus/notification settings, keyboard navigation, and existing service-worker/subscription upgrade.
- Open a task or reminder notification after session expiry and verify login returns to its destination.

These Apple checks cannot be replaced by Chromium emulation or mocked push responses. Reference: [Apple Web Push requirements](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/).
