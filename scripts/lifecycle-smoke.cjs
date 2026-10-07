// Independent recovery and request-ownership scenarios. Disposable DATA_DIR only.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.TEST_BASE_URL || 'http://127.0.0.1:18080';
const prefix = 'Recovery ' + Date.now();

(async () => {
    const browser = await chromium.launch({ headless: true, executablePath: process.env.TEST_BROWSER });
    try {
        const context = await browser.newContext({
            viewport: { width: 390, height: 844 },
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
        await page.getByRole('button', { name: 'Tasks', exact: true }).click();
        await page.locator('#capture-tasks [name=text]').fill(prefix);
        await page.locator('#capture-tasks .add-circle').click();
        await page.getByRole('button', { name: prefix, exact: true }).click();
        await page.locator('#task-editor').waitFor();
        const id = await page.locator('#task-editor [name=id]').inputValue();
        await page.route('**/task/save', route => route.fulfill({ status: 503, body: 'Unavailable' }));
        await page.locator('#task-editor [name=text]').fill(prefix + ' draft');
        await page.getByRole('button', { name: 'Today', exact: true }).click();
        await page.waitForFunction(async taskID => {
            const draft = await editorDrafts.read('tasks', taskID);
            return !!draft?.text.endsWith(' draft');
        }, id);
        await page.reload({ waitUntil: 'networkidle' });
        await page.getByRole('button', { name: 'Tasks', exact: true }).click();
        await page.getByRole('button', { name: prefix, exact: true }).click();
        await page.waitForFunction(
            title => document.querySelector('#task-editor [name=text]')?.value === title,
            prefix + ' draft',
        );
        await page.unroute('**/task/save');
        await page.locator('#task-editor').getByRole('button', { name: 'Done', exact: true }).click();
        await page.getByRole('button', { name: prefix + ' draft', exact: true }).waitFor();
        await page.waitForFunction(async taskID => !(await editorDrafts.read('tasks', taskID)), id);

        // A server edit made while the draft is offline must be chosen explicitly.
        await page.getByRole('button', { name: prefix + ' draft', exact: true }).click();
        await page.route('**/task/save', route => route.fulfill({ status: 503, body: 'Unavailable' }));
        const revision = await page.locator('#task-editor [name=revision]').inputValue();
        await page.locator('#task-editor [name=text]').fill(prefix + ' mine');
        await page.getByRole('button', { name: 'Today', exact: true }).click();
        const changed = await context.request.post(base + '/task/save', {
            form: { id, revision, text: prefix + ' latest' },
        });
        assert.equal(changed.status(), 200);
        await page.reload({ waitUntil: 'networkidle' });
        await page.unroute('**/task/save');
        await page.getByRole('button', { name: 'Tasks', exact: true }).click();
        await page.getByRole('button', { name: prefix + ' latest', exact: true }).click();
        await page.waitForFunction(
            title => document.querySelector('#task-editor [name=text]')?.value === title,
            prefix + ' mine',
        );
        await page.locator('#task-editor [name=text]').fill(prefix + ' mine again');
        await page.locator('#task-editor .edit-conflict').waitFor();
        await page.getByRole('button', { name: 'Use latest', exact: true }).click();
        assert.equal(await page.locator('#task-editor [name=text]').inputValue(), prefix + ' latest');
        await page.locator('#task-editor').getByRole('button', { name: 'Done', exact: true }).click();

        // Opening an editor cannot take navigation back after its response is delayed.
        let release;
        const held = new Promise(resolve => {
            release = resolve;
        });
        let requested;
        const started = new Promise(resolve => {
            requested = resolve;
        });
        await page.route('**/task/edit?*', async route => {
            requested();
            await held;
            await route.continue();
        });
        await page.getByRole('button', { name: prefix + ' latest', exact: true }).click();
        await started;
        await page.getByRole('button', { name: 'Notes', exact: true }).click();
        release();
        await page.waitForLoadState('networkidle');
        assert.equal(await page.evaluate(() => document.body.dataset.mode), 'notes');
        assert.equal(await page.locator('#task-editor').count(), 0);

        // Sheets own focus and background access; Escape returns to the trigger.
        await page.locator('.note-picker-btn').click();
        await page.waitForFunction(() =>
            document.getElementById('note-picker-panel').contains(document.activeElement),
        );
        await page.keyboard.press('Shift+Tab');
        assert.equal(
            await page.locator('#note-picker-panel').evaluate(el => el.contains(document.activeElement)),
            true,
        );
        assert.equal(await page.locator('#note-editor').evaluate(el => !!el.closest('[inert]')), true);
        await page.keyboard.press('Escape');
        assert.equal(
            await page.locator('.note-picker-btn').evaluate(el => el === document.activeElement),
            true,
        );
        assert.equal((await page.locator('#note-editor').getAttribute('aria-label')) !== null, true);
        assert.deepEqual(errors, []);
        console.log(
            'Lifecycle smoke passed: failed-save reload, stale-draft conflict, late response, modal focus.',
        );
    } finally {
        await browser.close();
    }
})().catch(error => {
    console.error(error);
    process.exit(1);
});
