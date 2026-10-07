# Audit implementation and verification — 6 October 2026

The application retains its Go/SQLite/HTMX architecture and current appearance.
The implementation covers the application audit and its follow-up recommendations:

| Recommendation | Implementation | Verification |
| --- | --- | --- |
| Recoverable editors | Shared ordered IndexedDB storage and explicit edit states; entity-owned task buffers; retained revisions and conflict choices | Failed-save navigation/reload and stale-draft browser scenarios |
| Scoped reads | Active workspace summaries, one-group reads, count-only sidebar, paged archive; request cancellation and bounded reads | Handler tests, cancelled/error reads, measurements below |
| Browser ownership | Navigation generations, per-target read ownership, view invalidation, common mutation effects, request-specific scroll restoration | Delayed responses, history, overlapping project reads, focus and scroll smokes |
| Domain contracts | Transactional task commands, desired completion state, consistent revision updates and error handling | Concurrent duplicate completion, repeat and revision tests |
| Shared undo | Server-issued expiring capabilities, guarded inverses, atomic heading recovery, batch skips for newer changes | Single-item conflict, batch preservation, duplicate undo, heading restoration tests |
| Truthful types | Separate project summaries/progress from loaded project bodies and direct task groups | Group, summary and project-body consistency tests |
| Recovery UX | Archive search and older/newer pages; shared modal focus/inertness/return; named note editor; one-tap note archive with undo | Archive page/search tests and modal/browser checks |
| Design implementation | Authoritative light/dark tokens, obsolete declarations removed, formatted authored assets | 42 settled light/dark comparisons against the previous stylesheet |
| Calendar specification | Shared Go/JavaScript fixtures for wording, typed dates, zones, DST, and month boundaries | Go calendar tests and `calendar-check.cjs` |
| Restore verification | Complete snapshot opened through startup, preserving notes, projects, headings, repeats and push state; deployment calendar/reminder configuration | `TestSnapshotRestoresApplicationStateAtStartup` |
| Repeatable review | Seeded settled visual exports, separate interaction checks, CI artifacts, current architecture/development/upgrade documentation | Local suites below; CI workflow updated |

## Read measurements

Warm medians on the same local audit harness, with 200 active tasks and 20,000
unarchived tasks belonging to an archived project:

| Operation | Before | After |
| --- | ---: | ---: |
| Workspace read | 46.89 ms | 0.16 ms |
| Sidebar response | 54.52 ms | 0.51 ms |
| Inbox-group response | 46.61 ms | 0.47 ms |
| Sidebar allocation | about 16.3 MiB | about 42 KiB |

With 20,000 archived completions in an active project, the workspace read changed
from 5.34 to 2.24 ms and the sidebar response from 8.21 to 2.23 ms. Progress still
counts those completions. These are synthetic local stress cases, with a warm-up
and 30 iterations per operation; they exclude networking and production host
differences. Today retains its existing query behavior and offline rendering.

## Completed checks

- `go test ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`, and `git diff --check`.
- Notification client and shared calendar checks.
- UI, audit, and independent lifecycle browser suites.
- Today, Tasks, and Notes in light/dark at 320/375/390/768/1023/1024/1280px.
  All 42 comparisons passed with identical dimensions and at most two colour
  levels per channel for glyph rasterisation differences.
- Authored asset formatting checked with the repository's Prettier configuration.

All databases and browser fixtures were disposable. No production data was read
or modified, and no commit, push, publication, or deployment was performed.
CI execution itself and installed iPhone/Mac PWA push, keyboards, and safe areas
still require verification in their actual environments.

Schema version 9 is a material deployment change. Follow [UPGRADE.md](UPGRADE.md)
for migration, snapshot restoration, and rollback. See [ARCHITECTURE.md](ARCHITECTURE.md)
for the resulting contracts and [DEVELOPMENT.md](DEVELOPMENT.md) for reproducible checks.
