// Focused regression checks for audit-driven navigation, drafts, repeating tasks, and search.
// Run only against a disposable local database.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.TEST_BASE_URL || 'http://127.0.0.1:18080';

(async () => {
 const browser = await chromium.launch({headless:true,executablePath:process.env.TEST_BROWSER});
 const page = await browser.newPage({viewport:{width:390,height:844}});
 await page.goto(base);
 if (await page.locator('[name=passcode]').isVisible()) {
  await page.locator('[name=passcode]').fill(process.env.TEST_PASSCODE || '12345678');
  await page.waitForURL(url=>url.pathname==='/',{waitUntil:'networkidle'});
 }
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 const capture=page.locator('#capture-tasks [name=text]');
 await capture.fill('Reload-safe draft');
 await page.reload({waitUntil:'networkidle'});
 assert.equal(await page.locator('#capture-tasks [name=text]').inputValue(),'Reload-safe draft');
 await page.locator('#capture-tasks [name=text]').fill('');

 await page.getByRole('button',{name:'Notes',exact:true}).click();
 await page.locator('#note-editor').waitFor();
 await page.locator('.bottom-nav-btn[data-mode="today"]').click();
 await page.locator('#today-content .today-heading').waitFor();
 await page.goBack();
 await page.waitForFunction(()=>document.body.dataset.mode==='notes');
 // Old links to the retired Habits view land on Today.
 await page.goto(base+'/?view=habits',{waitUntil:'networkidle'});
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'today');

 // A repeating task comes back with its next date when checked off.
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 await page.locator('#capture-tasks [name=text]').fill('Water the plants');
 await page.locator('#capture-tasks button.add-circle').click();
 await page.getByRole('button',{name:'Water the plants',exact:true}).click();
 await page.locator('#task-detail-form').waitFor();
 await page.waitForFunction(()=>document.getElementById('task-detail-form')?.['htmx-internal-data']?.listenerInfos);
 await page.locator('#task-detail-form [name=repeat]').selectOption('weekly');
 await page.locator('.detail-save-status').filter({hasText:/^Saved$/}).waitFor();
 await page.getByRole('button',{name:'Close details',exact:true}).click();
 await page.locator('#section-tasks .todo-repeat[aria-label="Repeats weekly"]').waitFor();
 await page.getByRole('button',{name:'Complete Water the plants',exact:true}).click();
 await page.locator('#toast').filter({hasText:/^Next one due [A-Z][a-z]{2} [0-9]+$/}).waitFor();
 await page.waitForFunction(()=>document.querySelectorAll('#section-tasks .todo-item:not(.done) .todo-repeat').length===1);
 assert.equal(await page.locator('#section-tasks .todo-item.done').filter({hasText:'Water the plants'}).count(),1,'completed occurrence missing');

 await page.keyboard.press('Meta+k');
 await page.locator('#search-input').fill('Water the plants');
 await page.locator('#search-results .search-result-item').first().waitFor();
 await browser.close();
 console.log('Audit smoke passed: reload drafts, history, repeating tasks, and search.');
})().catch(error=>{console.error(error);process.exit(1)});
