# SecondBrain

**A quiet place for your tasks, notes, and habits.** Self-hosted, single binary, no clutter.

SecondBrain is a personal organizer built on a simple premise: the tool should disappear and leave you with your thoughts. No accounts, no sync services, no telemetry, no JavaScript framework churn — just a Go binary, a SQLite file, and a fast, installable web app you own end to end.

```
Go + HTMX + SQLite  →  one binary  →  your server  →  your data
```

## Philosophy

- **One binary, one file.** The whole app compiles to a single executable; all your data is one SQLite file you can copy, back up, or move at will.
- **No framework tax.** The UI is server-rendered HTML with [HTMX](https://htmx.org) for interactivity. There is no build step, no bundler, no client-side state to debug.
- **Calm by default.** Chrome gets out of the way — tabs and inputs collapse as you scroll, and there's exactly one credential between you and your data.
- **Yours, privately.** Nothing leaves your server. No third parties, no analytics, no outbound calls at runtime.

## Quick Start

```bash
# 1. Create a .env file with your chosen passcode
cat > .env <<EOF
PASSCODE=your8digits
PORT=8080
EOF

# 2. Pull and run (image hosted on Docker Hub)
docker compose up -d

# 3. Open http://localhost:8080
```

> The passcode is exactly 8 digits (e.g. `12345678`) and is the only credential protecting the app. Choose your own, and put SecondBrain behind HTTPS if you expose it beyond your network.

## Features

**Lists that fit how you actually shop and plan**
- **Groceries & Buys** — reusable checklists. Re-adding an item un-checks the existing one instead of piling up duplicates, so recurring staples stay a single row.
- **Tasks** — a focused to-do list with optional due dates, overdue highlighting, and gentle "archives soon" hints. Tasks older than a week tidy themselves away automatically, so the list never becomes a graveyard.

**Notes that stay out of your way**
- Multiple notes with a quick-filter picker and a last-edited timestamp.
- **Auto-save** as you type, with optimistic concurrency — edit the same note on two devices and SecondBrain warns you instead of silently clobbering a version.
- Lightweight inline formatting: numbered lists, `*bold*`, `_italic_`, and `[] ` checkboxes.

**Habits worth keeping**
- One-tap daily tracking with a running **streak counter**.

**Find anything, fast**
- Global search across notes and tasks — pull down from the top or press `/`.

**Built like an app**
- **Installable PWA** — add it to your phone or desktop and launch it from the home screen. A service worker keeps assets fresh across deploys, with cache-busting handled for you.
- **Archive, restore, and permanent-delete** for tasks, notes, and habits — nothing is lost by accident.

## Security

A single passcode, taken seriously:

- **8-digit passcode** compared in constant time, with a lockout after repeated failures.
- **Hardened sessions** — `HttpOnly`, `Secure` (over HTTPS), `SameSite=Strict` cookies; configurable auto-logout on inactivity; clean server-side session expiry.
- **Sensible headers** — Content-Security-Policy, `X-Frame-Options`, `X-Content-Type-Options`, HSTS over HTTPS, and `no-store` on your private pages.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PASSCODE` | *(required)* | 8-digit numeric passcode |
| `PORT` | `8080` | Server port |
| `DATA_DIR` | `./data` | SQLite database location |
| `INACTIVITY_LOGOUT_MINUTES` | `45` | Auto-logout timeout after inactivity |

## Architecture

SecondBrain is deliberately small enough to read in one sitting.

- **Backend** — a single Go program. HTML templates and static assets are embedded into the binary, so deployment is "copy the file and run it."
- **Frontend** — server-rendered templates swapped in place by HTMX. No SPA, no client framework, no build pipeline.
- **Storage** — SQLite in WAL mode behind a single connection, with a graceful shutdown that checkpoints cleanly on `SIGTERM`.
- **Runtime dependencies** — none beyond the binary and its data directory.

## Development

```bash
export PASSCODE=12345678
go run .
```

HTMX is vendored at `static/htmx.min.js` and embedded into the binary, so there's no fetch step and the build needs no network access. To bump the pinned version, run `go run cmd/fetch_htmx.go` and commit the updated file.

## Data & Backup

Everything lives in one SQLite file at `DATA_DIR/secondbrain.db`. To back up or migrate, copy that file — no export, no transformation, no lock-in.
