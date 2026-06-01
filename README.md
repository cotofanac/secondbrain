# SecondBrain

Minimalist self-hosted PWA for tasks, notes, and habits. Built with Go, HTMX, and SQLite.

## Quick Start

```bash
# 1. Create .env with your 8-digit passcode
echo "PASSCODE=12345678" > .env

# 2. Pull and run (image hosted on Docker Hub)
docker compose up -d

# 3. Open http://localhost:8080
```

## Features

- **Groceries & Buys**: Reusable checklists — re-adding an item unchecks it instead of duplicating
- **Tasks**: To-do list with optional due dates and overdue highlighting
- **Notes**: Multiple notes, auto-save, last-edited timestamp
- **Habits**: Daily tracking with streak counter
- **PWA**: Install on mobile or desktop, works from home screen
- **Secure**: 8-digit passcode, HttpOnly cookies, constant-time comparison
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
go run .
```

## Data

SQLite database stored in `DATA_DIR`. Back up the `secondbrain.db` file.
