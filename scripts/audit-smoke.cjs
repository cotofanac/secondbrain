// Focused regression checks for audit-driven navigation, drafts, habits, and search.
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
 await page.getByRole('button',{name:'Habits',exact:true}).click();
 await page.locator('.habits-toolbar').waitFor();
 await page.goBack();
 await page.waitForFunction(()=>document.body.dataset.mode==='notes');

 await page.getByRole('button',{name:'Habits',exact:true}).click();
 await page.getByRole('button',{name:'New habit',exact:true}).click();
 await page.locator('#custom-dialog-input').fill('Audit movement');
 await page.locator('#custom-dialog-period [data-period=week]').click();
 await page.locator('#custom-dialog-target').fill('3');
 await page.locator('#custom-dialog-confirm').click();
 const habit=page.locator('.habit-item[data-habit-name="Audit movement"]');
 await habit.waitFor();
 await habit.locator('.row-menu > summary').click();
 await habit.getByRole('button',{name:'Edit',exact:true}).click();
 assert.equal(await page.locator('#custom-dialog-target').inputValue(),'3');
 await page.locator('#custom-dialog-period [data-period=month]').click();
 await page.locator('#custom-dialog-target').fill('4');
 await page.locator('#custom-dialog-confirm').click();
 await page.locator('.habit-item[data-habit-name="Audit movement"][data-habit-period="month"][data-habit-target="4"]').waitFor();

 await page.keyboard.press('Meta+k');
 await page.locator('#search-input').fill('Audit movement');
 await page.getByRole('button',{name:/Audit movement/}).waitFor();
 await browser.close();
 console.log('Audit smoke passed: reload drafts, history, habit editing, and habit search.');
})().catch(error=>{console.error(error);process.exit(1)});
