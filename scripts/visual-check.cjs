// Seed representative content on a fresh disposable server. Capture settled
// layouts independently of the mutation/animation smoke checks.
const playwright = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const output = process.env.SCREENSHOT_DIR || path.join(require('node:os').tmpdir(), 'secondbrain-visuals');
const base = process.env.TEST_BASE_URL || 'http://127.0.0.1:18080';
const widths = [320, 375, 390, 768, 1023, 1024, 1280];

async function comparePixels(page, current, previous) {
    if (current.equals(previous)) return;
    const result = await page.evaluate(
        async sources => {
            const pixels = [];
            const sizes = [];
            for (const source of sources) {
                const image = new Image();
                image.src = source;
                await image.decode();
                const canvas = document.createElement('canvas');
                canvas.width = image.width;
                canvas.height = image.height;
                const context = canvas.getContext('2d');
                context.drawImage(image, 0, 0);
                pixels.push(context.getImageData(0, 0, image.width, image.height).data);
                sizes.push([image.width, image.height]);
            }
            if (sizes[0][0] !== sizes[1][0] || sizes[0][1] !== sizes[1][1]) return { sameSize: false };
            let delta = 0;
            for (let i = 0; i < pixels[0].length; i++)
                delta = Math.max(delta, Math.abs(pixels[0][i] - pixels[1][i]));
            return { sameSize: true, delta };
        },
        [current, previous].map(buffer => 'data:image/png;base64,' + buffer.toString('base64')),
    );
    assert.equal(result.sameSize, true, 'Screenshot dimensions changed');
    assert.ok(
        result.delta <= 2,
        'Pixel colour difference ' + result.delta + ' exceeds the rasterisation tolerance',
    );
}

(async () => {
    fs.mkdirSync(output, { recursive: true });
    const browserType = process.env.TEST_BROWSER_ENGINE || 'chromium';
    const browser = await playwright[browserType].launch({ headless: true, executablePath: process.env.TEST_BROWSER });
    try {
        const context = await browser.newContext({
            viewport: { width: 1280, height: 900 },
            reducedMotion: 'reduce',
            serviceWorkers: 'block',
        });
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        page.on('console', message => {
            if (/Content Security Policy/i.test(message.text())) errors.push(message.text());
        });
        await page.goto(base);
        await page.locator('[name=passcode]').fill(process.env.TEST_PASSCODE || '12345678');
        await page.waitForURL(url => url.pathname === '/', { waitUntil: 'networkidle' });
        assert.equal(
            await page.locator('#todo-items [data-task-id]').count(),
            0,
            'Visual fixtures require a fresh disposable DATA_DIR',
        );
        async function post(url, fields) {
            const response = await context.request.post(base + url, { form: fields });
            assert.equal(response.ok(), true, url + ': ' + (await response.text()));
            return response.text();
        }
        const today = await page.evaluate(() => todayISO());
        const shifted = days =>
            new Date(Date.parse(today + 'T00:00:00Z') + days * 86400000).toISOString().slice(0, 10);
        await post('/projects/action', { action: 'create', name: 'Home, one step at a time' });
        await post('/headings/action', { action: 'create', project_id: '1', name: 'Kitchen' });
        for (const [text, due, list] of [
            ['Call the driving school', shifted(-3), ''],
            ['Book a quiet afternoon for paperwork', shifted(-1), ''],
            ['Take the parcel to the post office', today, ''],
            [
                'Read the document with a very long title that should wrap naturally without pushing the date or controls off the screen',
                today,
                '',
            ],
            ['Get tiles for the kitchen', shifted(1), 'heading:1'],
            ['Measure the shelves', shifted(4), 'project:1'],
            ['Choose one small next step', '', 'project:1'],
            ['An idea captured before deciding where it belongs', '', ''],
        ])
            await post('/todos/add', { category: 'todo', text, due_date: due, list });
        for (const text of ['Apples', 'Coffee', 'Oat milk'])
            await post('/todos/add', { category: 'groceries', text });
        await post('/todos/add', { category: 'shopping', text: 'A reading lamp' });
        await post('/notes/create', { title: 'Ideas for a quiet weekend' });
        await post('/notes/save', {
            id: '2',
            revision: '1',
            content:
                'A little room to think.\n\n1. Take a walk\n2. Read a chapter\n\n[] Call a friend\n\nLong paragraphs stay readable on a phone and have a comfortable measure on a larger screen.',
        });
        await page.goto(base, { waitUntil: 'networkidle' });
        const manifest = {
            engine: browserType,
            browser: browser.version(),
            widths,
            modes: ['today', 'todos', 'notes'],
            themes: ['light', 'dark'],
            files: [],
        };
        let baselineCSS;
        if (process.env.COMPARE_CSS) baselineCSS = fs.readFileSync(process.env.COMPARE_CSS, 'utf8');
        for (const theme of manifest.themes) {
            await page.emulateMedia({ colorScheme: theme });
            for (const width of widths) {
                await page.setViewportSize({ width, height: 900 });
                for (const mode of manifest.modes) {
                    await page.evaluate(view => switchMode(view), mode);
                    if (mode === 'todos') {
                        await page.evaluate(async () => {
                            document.body.dataset.workspaceSection = 'tasks';
                            document.querySelectorAll('.project-group').forEach(project => {
                                project.open = true;
                            });
                            await loadOpenProjects();
                            applyProjectPage();
                        });
                    }
                    await page.waitForLoadState('networkidle');
                    await page.evaluate(async () => {
                        await document.fonts.ready;
                        document.getElementById('toast').hidden = true;
                        document
                            .querySelectorAll('.view-entering,.note-entering,.is-opening')
                            .forEach(el =>
                                el.classList.remove('view-entering', 'note-entering', 'is-opening'),
                            );
                        document.querySelectorAll('.note-picker-time,.note-updated').forEach(el => {
                            el.textContent = '10:00 AM';
                        });
                        document.getElementById('topbar-stat').textContent = 'Tue, Oct 6';
                        document.querySelector('.today-heading p').textContent = 'Tuesday, October 6';
                        window.scrollTo(0, 0);
                    });
                    assert.equal(
                        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.body.scrollWidth <= innerWidth),
                        true,
                        `${mode} ${theme} ${width} overflow`,
                    );
                    if (mode === 'todos' && width <= 390) {
                        // WebKit's native date and list controls used to expand
                        // the page when the editor opened, despite fitting boxes.
                        await page.getByRole('button', { name: 'Get tiles for the kitchen', exact: true }).click();
                        await page.locator('#task-editor').waitFor();
                        await page.waitForLoadState('networkidle');
                        const editorLayout = await page.evaluate(() => ({
                            page: document.documentElement.scrollWidth,
                            body: document.body.scrollWidth,
                            viewport: innerWidth,
                        }));
                        assert.equal(
                            editorLayout.page <= editorLayout.viewport && editorLayout.body <= editorLayout.viewport,
                            true,
                            `${theme} ${width} task editor overflow: ${JSON.stringify(editorLayout)}`,
                        );
                        await page.keyboard.press('Escape');
                        await page.locator('#task-editor').waitFor({ state: 'detached' });
                        await page.waitForLoadState('networkidle');
                        await page.evaluate(() => {
                            document.activeElement?.blur();
                            window.scrollTo(0, 0);
                        });
                    }
                    const name = `${mode}-${theme}-${width}.png`;
                    const current = await page.screenshot({
                        path: path.join(output, name),
                        fullPage: true,
                        animations: 'disabled',
                    });
                    manifest.files.push(name);
                    if (baselineCSS) {
                        const link = page.locator('link[rel=stylesheet]');
                        const original = await link.getAttribute('href');
                        await page.route('**/style.css?comparison*', route =>
                            route.fulfill({ contentType: 'text/css', body: baselineCSS }),
                        );
                        await link.evaluate(el => {
                            el.href = '/static/style.css?comparison=before';
                        });
                        await page.waitForLoadState('networkidle');
                        const before = await page.screenshot({ fullPage: true, animations: 'disabled' });
                        if (!current.equals(before))
                            fs.writeFileSync(path.join(output, 'before-' + name), before);
                        try {
                            await comparePixels(page, current, before);
                        } catch (error) {
                            throw new Error(`${mode} ${theme} ${width}: ${error.message}`);
                        }
                        await link.evaluate((el, href) => {
                            el.href = href;
                        }, original);
                        await page.waitForLoadState('networkidle');
                        await page.unroute('**/style.css?comparison*');
                    }
                }
            }
        }
        fs.writeFileSync(path.join(output, 'manifest.json'), JSON.stringify(manifest, null, 2));
        assert.deepEqual(errors, []);
        console.log(`Visual checks passed: ${manifest.files.length} settled layouts in ${output}`);
    } finally {
        await browser.close();
    }
})().catch(error => {
    console.error(error);
    process.exit(1);
});
