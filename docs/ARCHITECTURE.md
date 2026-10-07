# Application contracts

Second Brain is a single-user, self-hosted PWA. One Go process owns one SQLite
file. HTML templates and authored assets are embedded in the binary. HTMX swaps
server-rendered fragments; native JavaScript owns editing and presentation state.
There is no JavaScript build step, ORM, or separate API application.

## Product decisions

Capture needs only text. Dates and organisation are optional. Overdue work stays
visible at the head of Today and its count; amber means attention, and red means
permanent deletion. Future work folds away. Repeating tasks replace habits;
there are no streaks, weekly reviews, or settings screen. Deployment variables
supply the calendar zone and reminder time. Each device enables push in Today.

Below 1024px, Tasks is a continuous list with a tab bar and destination sheet.
Below 768px, controls and spacing become more compact. At 1024px the tab bar
becomes a sidebar and projects open as individual pages. Notes use a modal picker
below 1024px and a persistent list on desktop. All sheets and dialogs share focus
trapping, background inertness, accessible names, Escape, and return focus.

## Read models

`ProjectSummary` contains identity, lifecycle flags, and `ProjectProgress`.
`Project` adds its loaded direct `TaskGroup` and headings. A task group's rows
never masquerade as whole-project totals. Progress includes active rows and
archived completions, including headings; archived unfinished rows do not count.

`loadWorkspaceContext` loads inbox/list rows and active project summaries.
Project bodies load separately. `loadTaskGroup` reads one group and its summary;
`loadSidebar` reads counts and summaries. The archive searches retained data and
returns 200 rows plus a lookahead, with deterministic ordering and paging.
Scoped HTTP reads inherit request cancellation and a five-second read budget.
Search failures are errors, rather than successful empty results.

The initial page still embeds Today, lists, and the selected note. This supports
the last-page offline copy. Lazy project bodies are absent from that copy.
Keep the single SQLite connection: transaction boundaries protect invariants,
whereas the connection limit alone cannot protect a read followed by a write.

## Mutation rules

A task or note revision describes its stored state, including completion,
archive/restore, text, dates, repeat rule, and membership. A write changes the
revision. Editor saves compare revisions atomically; conflicts return HTTP 409
and the latest active entity. Project revisions protect undo of its lifecycle.

Task completion sends a desired `done` state and the displayed revision.
Repeating the same command has no second effect or second repeat occurrence.
The legacy toggle route also accepts requests without a desired state; new UI
commands always supply it. Completing/reopening a series and creating/retracting
its next occurrence happen in the same transaction. A touched next occurrence
is retained when its predecessor is reopened.

`task_commands.go` owns completion and archival; `undo.go` owns guarded inverse
operations. Other small operations remain direct SQL in their handlers. Extract
business operations when they protect a real invariant, rather than creating an
interface for every table. `writeDomainError` distinguishes missing, validation,
conflict, and operational errors; operational details belong in server logs.

Archive is soft; permanent deletion accepts only archived rows. Shared undo
capabilities are random server-side tokens, issued in the original transaction,
retained for 24 hours, and pruned hourly. Inverses compare resulting revisions.
Single-item and structural undo refuse newer changes; batch undo skips changed
items. Applying an already-used token has no second effect. The browser never
supplies SQL or trusted inverse field values. Completion is reversed by reopening;
ordinary edits can be changed again in their editor.

## Browser ownership and recovery

`app.js` owns navigation generations, per-target request ownership, dirty view
versions, and modality. `workspace.js` owns capture drafts, task buffers, and
workspace restoration. `notes.js` owns note editing. `calendar.js` owns browser
calendar rules. `drafts.js` supplies ordered IndexedDB storage and the common
clean/dirty/saving/failed/conflicted editor states.

Editor buffers belong to entity IDs. Closing a row detaches its form but retains
unsaved state. Both editors write a durable draft before saving. A successful
acknowledgement removes only the acknowledged draft; newer input stays dirty.
Reopening or reloading restores a draft with its original revision, so a newer
server version still requires an explicit conflict choice. Notes gate switching
between notes; task navigation can proceed immediately. Drafts do not expire
silently. Browser storage clearing removes them; if storage is unavailable,
the task buffer remains in its tab and reports that limitation.

HTMX and fetch mutations share `X-SB-Effects`. An inactive view is marked dirty,
then refreshed when shown. A group response covers its own change without
replacing the entire workspace. GET responses are owned by their targets;
obsolete reads cannot replace newer destinations. Focus and scroll restoration
belong to a request and navigation generation. Late responses cannot take focus
or navigation back from the user's current view.

## Calendar, assets, and deployment

Calendar dates are `YYYY-MM-DD` in the deployment's `TZ`; timestamps are UTC
RFC3339 via `dbTime`/`sqlNow`. Elapsed hours never define calendar days. Go and
browser rules share `testdata/calendar.json`, including DST and month boundaries.

All dynamic HTML uses `html/template`; user text in JavaScript uses DOM text
properties. CSP permits local external scripts only. New callable actions use
`data-call` and the whitelist. New code assets must enter both asset hashing and
service-worker precaching. Rebuild the binary after any embedded asset edit.

Schema version 9 adds project revisions and undo capabilities. New databases and
transactional migrations have equivalent schemas. Never edit applied migrations.
Daily `VACUUM INTO` snapshots retain all persistent application state; deployment
variables must be retained separately. See [UPGRADE.md](UPGRADE.md) for restoration
and [DEVELOPMENT.md](DEVELOPMENT.md) for verification.
