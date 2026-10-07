# Development and verification

Use Go from `go.mod`, with CGO enabled for SQLite. No npm packages are required
by the running application. Rebuild after editing templates or static assets.

Never run a smoke test against the personal database. Start a disposable server:

```sh
scratch=$(mktemp -d)
go build -o "$scratch/secondbrain" .
PASSCODE=12345678 PORT=18080 DATA_DIR="$scratch/data" "$scratch/secondbrain"
```

In another terminal, run the checks:

```sh
go test ./...
go vet ./...
gofmt -l .
node scripts/push-smoke.cjs
node scripts/calendar-check.cjs
```

Run `go test -race ./...` before release. Tests use temporary databases and include
concurrent completion, guarded undo, migrations, repeating tasks, archive pages,
calendar contracts, and restoring a complete snapshot through startup.

## Browser checks

Install Playwright outside the repository and provide its module path:

```sh
npm install --prefix /some/scratch/pw playwright@1.63.0
/some/scratch/pw/node_modules/.bin/playwright install --only-shell chromium
export PLAYWRIGHT_MODULE=/some/scratch/pw/node_modules/playwright
export TEST_BASE_URL=http://127.0.0.1:18080
export TEST_PASSCODE=12345678
node scripts/ui-smoke.cjs
node scripts/audit-smoke.cjs
node scripts/lifecycle-smoke.cjs
```

`TEST_BROWSER` optionally selects a local Chromium executable. The UI smoke
expects a fresh database at its start. It also checks mutations and animation
behavior. The independent lifecycle smoke checks failed-save navigation/reload,
stale draft conflicts, late responses, and sheet focus. CSP console violations
and browser exceptions fail these checks.

## Settled visual checks

Start another server with a fresh disposable `DATA_DIR`, then run:

```sh
SCREENSHOT_DIR=/some/scratch/screenshots node scripts/visual-check.cjs
```

The script seeds a representative project, heading, long text, overdue/due/future
and undated tasks, grocery items, and a note. It exports settled full-page PNGs
for Today, Tasks, and Notes, light/dark, at 320/375/390/768/1023/1024/1280px.
It normalises incidental clock labels, waits for fonts and requests, disables
motion, checks document overflow, and writes a manifest with the browser version.
CI publishes these files for review. Animations are exercised separately by the
interaction smokes. Visual exports require a fresh database; the script refuses
an existing task fixture.

For a CSS-only consolidation, retain the previous stylesheet outside the repo,
then supply `COMPARE_CSS=/some/scratch/previous.css`. The script compares each
settled image with the same fixture using the previous stylesheet. It permits
at most two colour levels per channel for browser glyph rasterisation, and
requires identical image dimensions.
Keep browser version and platform constant when comparing screenshots.

Author JavaScript and CSS using `.prettierrc.json`. Install Prettier outside the
repo when needed; do not format vendored `static/htmx.min.js`. The app gains no
npm runtime or build dependency.

## Hardware checks

Chromium cannot validate installed iPhone/Mac PWA push delivery, software
keyboards, safe areas, Focus settings, or notification banners. Check them on
real devices before release, including notification links after session expiry.
