# SecondBrain

Minimalist self-hosted PWA for tasks, notes, and habits. Built with Go, HTMX, and SQLite — single binary, no JS frameworks, no external dependencies at runtime.

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

> The passcode must be exactly 8 digits (e.g. `12345678`). Choose your own — it is the only credential protecting the app.

## Features

- **Groceries & Buys**: Reusable checklists — re-adding an item unchecks it instead of duplicating
- **Tasks**: To-do list with optional due dates and overdue highlighting
- **Notes**: Multiple notes, auto-save, last-edited timestamp
- **Habits**: Daily tracking with streak counter
- **PWA**: Install on mobile or desktop, works from home screen
- **Secure**: 8-digit passcode, HttpOnly cookies, constant-time comparison, session persistence
- **Minimal**: Single Go binary, SQLite, HTMX — no JS frameworks
- **Collapsible UI**: Tabs and add form hide on scroll for focus

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PASSCODE` | *(required)* | 8-digit numeric passcode |
| `PORT` | `8080` | Server port |
| `DATA_DIR` | `./data` | SQLite database location |
| `INACTIVITY_LOGOUT_MINUTES` | `45` | Auto-logout timeout after inactivity |

## Development

```bash
export PASSCODE=12345678
go run cmd/fetch_htmx.go   # download HTMX locally (once)
go run .
```

## Data & Backup

All data lives in a single SQLite file at `DATA_DIR/secondbrain.db`. To back up or migrate, copy that file — no export or transformation needed.
