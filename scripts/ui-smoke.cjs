// Run only against a disposable local database; this creates sample content.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.TEST_BASE_URL || 'http://127.0.0.1:18080';
(async () => {
 const browser = await chromium.launch({headless:true,executablePath:process.env.TEST_BROWSER});
 const context = await browser.newContext({viewport:{width:1280,height:900}});
 const page = await context.newPage(); const errors=[];
 async function openTaskDetails(name) {
  const title=page.getByRole('button',{name,exact:true});
  const row=title.locator('..');
  await row.locator('.row-menu > summary').click();
  await row.getByRole('button',{name:'Details',exact:true}).click();
  await page.locator('#task-detail-form').waitFor();
 }
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base);
 await page.locator('[name=passcode]').fill(process.env.TEST_PASSCODE || '12345678');
 await page.waitForURL(url=>url.pathname==='/',{waitUntil:'networkidle'});
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'today','Today is not the default view');
 await page.locator('.bottom-nav-btn[data-workspace="tasks"]').click();
 assert.equal(await page.locator('#section-tasks').isVisible(),true,'selected desktop task list hidden');
 assert.equal(await page.locator('#capture-tasks [name=text]').isVisible(),true,'desktop task capture hidden');
 await page.locator('#capture-tasks [name=text]').fill('Call the driving school');
 await page.locator('#capture-tasks button[type=submit], #capture-tasks button.add-circle').click();
 await page.getByRole('button',{name:'Call the driving school',exact:true}).waitFor();
 assert.equal(await page.locator('#capture-tasks [name=text]').inputValue(),'');
 await page.locator('#projects-heading').waitFor();
 await page.getByRole('button',{name:'New project',exact:true}).click();
 await page.locator('#custom-dialog-input').fill('Car');await page.locator('#custom-dialog-confirm').click();
 await page.locator('.project-group > summary').filter({hasText:'Car'}).click();
 await page.getByRole('button',{name:'Stage',exact:true}).click();
 await page.locator('#custom-dialog-input').fill('Driving licence');await page.locator('#custom-dialog-confirm').click();
 await page.locator('.stage-group > summary').filter({hasText:'Driving licence'}).click();
 const capture=page.locator('.stage-group .capture-form');await capture.locator('[name=text]').fill('Book lessons');await capture.locator('button.add-circle').click();
 // The primary title interaction is direct editing on pointer and keyboard.
 await page.getByRole('button',{name:'Book lessons',exact:true}).click();
 const inlineTitle=page.locator('.task-title-editor input');await inlineTitle.fill('Book practical lessons');await inlineTitle.press('Enter');
 await page.getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 await page.getByRole('button',{name:'Book practical lessons',exact:true}).click();await page.locator('.task-title-editor input').press('Escape');
 await openTaskDetails('Book practical lessons');
 await page.locator('#task-detail-form [name=due_date]').fill('2026-10-01');await page.locator('#task-detail-form button.btn-primary').click();
 // A response must not overwrite text entered after the request was sent.
 let releaseTask;const heldTask=new Promise(resolve=>releaseTask=resolve);
 await page.route('**/task/save',async route=>{const response=await route.fetch();await heldTask;await route.fulfill({response});});
 const taskRequest=page.waitForRequest('**/task/save');
 await page.locator('#task-detail-form button.btn-primary').click();await taskRequest;
 await page.locator('#task-detail-form [name=text]').fill('Draft typed during save');releaseTask();
 await page.locator('.detail-save-status').filter({hasText:'newer edits'}).waitFor();
 assert.equal(await page.locator('#task-detail-form [name=text]').inputValue(),'Draft typed during save');
 await page.unroute('**/task/save');
 await page.getByRole('button',{name:'Discard',exact:true}).click();
 await page.waitForFunction(()=>document.querySelector('#task-detail-form [name=text]').value==='Book practical lessons');
 await page.getByRole('button',{name:'Close details',exact:true}).click();
 // Completion and reopening preserve the task and collapsed state.
 await page.locator('#section-tasks').evaluate(el=>el.dataset.testStable='true');
 await page.getByRole('button',{name:'Complete Book practical lessons',exact:true}).click();
 assert.equal(await page.locator('#section-tasks').getAttribute('data-test-stable'),'true','task completion replaced the entire workspace section');
 const completed=page.locator('.stage-group .completed-group');if(!await completed.evaluate(el=>el.open))await completed.locator(':scope > summary').click();
 await page.getByRole('button',{name:'Reopen Book practical lessons',exact:true}).click();
 await page.getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 // Draft survives another task's mutation and a refresh.
 await capture.locator('[name=text]').fill('Keep this draft');
 await page.evaluate(()=>refreshWorkspace());
 assert.equal(await page.locator('.stage-group .capture-form [name=text]').inputValue(),'Keep this draft');
 await page.locator('.stage-group .capture-form [name=text]').fill('');
 await page.getByRole('button',{name:'Project details',exact:true}).click();
 await page.getByRole('button',{name:'Link note',exact:true}).click();
 await page.getByRole('button',{name:'Quick Notes',exact:true}).waitFor();
 await page.screenshot({path:'/private/tmp/secondbrain-desktop.png',fullPage:true});
 await page.getByRole('button',{name:'Close details',exact:true}).click();
 for (const width of [320,375,390,430,1024,1440]) {
  await page.setViewportSize({width,height:900});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth && document.getElementById('app').getBoundingClientRect().right<=innerWidth),true,'horizontal overflow '+width);
  if(width===390)await page.screenshot({path:'/private/tmp/secondbrain-iphone.png',fullPage:true});
 }
 await page.setViewportSize({width:390,height:844});
 const mobileNavGeometry=async()=>page.evaluate(()=>{
  const nav=document.getElementById('bottom-nav').getBoundingClientRect();
  const buttons=[...document.querySelectorAll('.bottom-nav > .bottom-nav-btn:not(.workspace-nav-btn)')];
  return {nav:{top:nav.top,bottom:nav.bottom,height:nav.height},buttons:buttons.map(button=>{const box=button.getBoundingClientRect();return {left:box.left,top:box.top,width:box.width,height:box.height};})};
 });
 const taskNav=await mobileNavGeometry();
 assert.equal(taskNav.nav.bottom,844,'mobile navigation is not pinned to the viewport bottom');
 assert.equal(taskNav.nav.height,52,'mobile navigation has an unstable content height');
 assert.equal(taskNav.buttons.every(button=>button.top>=taskNav.nav.top&&button.height===51&&button.top+button.height<=taskNav.nav.bottom),true,'mobile navigation buttons escape the compact tab row');
 await page.getByRole('button',{name:'Notes',exact:true}).click();const notesNav=await mobileNavGeometry();
 await page.getByRole('button',{name:'Habits',exact:true}).click();const habitsNav=await mobileNavGeometry();
 assert.deepEqual(notesNav,taskNav,'mobile navigation moved in Notes');
 assert.deepEqual(habitsNav,taskNav,'mobile navigation moved in Habits');
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 await page.locator('#topbar-title').click();
 assert.equal(await page.locator('#mobile-workspace-menu').isVisible(),true,'mobile workspace sheet did not open');
 await page.locator('[data-workspace-option="groceries"]').click();
 assert.equal(await page.locator('#section-groceries').isVisible(),true,'mobile workspace selection did not change');
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="tasks"]').click();
 await page.locator('#topbar-stat').click();await page.locator('#today-content .today-heading').waitFor();
 await page.getByRole('button',{name:'Reminders & notifications',exact:true}).click();
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'settings','notifications did not open');
 assert.equal(await page.locator('#topbar-back').isVisible(),true,'notifications has no mobile return control');
 await page.locator('#topbar-back').click();
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'today','notifications back did not return to Today');
 await page.locator('#topbar-title').click();await page.getByRole('button',{name:'Today',exact:true}).click();
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'today','Today did not open');
 await page.locator('#today-content .today-heading').waitFor();
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="groceries"]').click();
 assert.equal(await page.locator('#section-groceries').isVisible(),true,'Today did not preserve the last task list');
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="tasks"]').click();
 await openTaskDetails('Book practical lessons');
 assert.equal(await page.locator('#detail-pane').evaluate(el=>{const b=el.getBoundingClientRect();return b.left>=0&&b.right<=innerWidth&&b.top>=0&&b.bottom<=innerHeight}),true,'mobile details overflow');
 assert.equal(await page.locator('#detail-pane').evaluate(el=>el.getBoundingClientRect().height < innerHeight*.75),true,'mobile task details should be a compact sheet');
 await page.getByRole('button',{name:'Close details',exact:true}).click();
 await page.evaluate(()=>document.documentElement.style.fontSize='24px');
 assert.equal(await page.evaluate(()=>document.getElementById('app').getBoundingClientRect().right<=innerWidth),true,'large-text overflow');
 await page.evaluate(()=>document.documentElement.style.fontSize='');
 await page.setViewportSize({width:1280,height:900});
 await page.getByRole('button',{name:'Notes',exact:true}).click();
 assert.equal(await page.locator('#note-picker-panel').isVisible(),true,'Mac note list missing');
 await page.locator('#note-editor').fill('A note that should survive refresh.');
 await page.waitForFunction(()=>!document.getElementById('note-editor').dataset.dirty);
 await page.evaluate(()=>refreshCurrentView());
 await page.waitForTimeout(200);
 assert.equal(await page.locator('#note-editor').inputValue(),'A note that should survive refresh.');
 await page.locator('#note-editor').focus();await page.locator('#note-editor').evaluate(el=>el.setSelectionRange(3,9));
 const noteRefresh=page.waitForResponse(response=>response.url().includes('/notes?id='));await page.evaluate(()=>refreshCurrentView());await noteRefresh;
 await page.waitForFunction(()=>document.getElementById('note-editor').selectionStart===3&&document.getElementById('note-editor').selectionEnd===9);
 await page.keyboard.press('Meta+k');assert.equal(await page.locator('#search-input').evaluate(el=>el===document.activeElement),true);await page.keyboard.press('Escape');
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'notes');
 // Delay the fetch response in-page: service-worker requests do not always
 // produce Playwright page request events.
 await page.evaluate(()=>{
  window.smokeOriginalFetch=window.fetch;let count=0;
  window.fetch=async(...args)=>{const response=await window.smokeOriginalFetch(...args);if(args[0]==='/notes/save'&&++count===1)await new Promise(resolve=>window.smokeReleaseNote=resolve);return response;};
 });
 await page.locator('#note-editor').fill('First note revision');await page.evaluate(()=>{void saveCurrentNote();});
 await page.waitForFunction(()=>typeof window.smokeReleaseNote==='function');
 await page.locator('#note-editor').fill('Newer note revision');await page.evaluate(()=>window.smokeReleaseNote());
 await page.waitForFunction(()=>!document.getElementById('note-editor').dataset.dirty);
 assert.equal(await page.locator('#note-editor').inputValue(),'Newer note revision');
 await page.evaluate(()=>{window.fetch=window.smokeOriginalFetch;delete window.smokeOriginalFetch;delete window.smokeReleaseNote;});
 await page.evaluate(()=>openSettings());
 await page.locator('#schedule-form').waitFor();
 await page.locator('[name=task_enabled]').check();await page.getByRole('button',{name:'Save reminders',exact:true}).click();
 await page.waitForTimeout(200);assert.equal(await page.locator('[name=task_enabled]').isChecked(),true);
 await page.evaluate(()=>openToday());await page.getByRole('heading',{name:'Today'}).waitFor();
 // Root service worker must become ready, unlike the old /static/ scope.
 const scope=await page.evaluate(async()=> (await navigator.serviceWorker.ready).scope);assert.equal(scope,base+'/');
 await page.emulateMedia({colorScheme:'dark'});await page.screenshot({path:'/private/tmp/secondbrain-today-dark.png',fullPage:true});
 assert.deepEqual(errors,[],'browser errors');
 await browser.close();console.log('UI smoke passed: Today, projects, stages, task editing, completion, drafts, notes, notifications, root worker, six viewport widths.');
})().catch(error=>{console.error(error);process.exit(1)});
