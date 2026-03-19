# SecondBrain

Minimalist self-hosted to-do list + notes PWA. Built with Go, HTMX, and SQLite.

## Quick Start

```bash
# 1. Create .env with your 8-digit passcode
echo "PASSCODE=12345678" > .env

# 2. Build and run
docker compose up -d

# 3. Open http://localhost:8080
```

## Features

- **Tasks**: Groceries + To-Do tabs with due dates
- **Notes**: Multiple files, auto-save
- **PWA**: Install on mobile, works from home screen
- **Secure**: 8-digit passcode, HttpOnly cookies, constant-time comparison
- **Minimal**: Single Go binary, SQLite, HTMX — no JS frameworks
- **Collapsible UI**: Tabs hide on scroll for focus

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PASSCODE` | *(required)* | 8-digit numeric passcode |
| `PORT` | `8080` | Server port |
| `DATA_DIR` | `./data` | SQLite database location |

## Development

```bash
export PASSCODE=12345678
go run .
```

## Data

SQLite database stored in `DATA_DIR`. Back up the `secondbrain.db` file.
