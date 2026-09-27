// Run only against a disposable local database; this creates sample content.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const base = process.env.TEST_BASE_URL || 'http://127.0.0.1:18080';
// Review screenshots go to the system temp folder unless SCREENSHOT_DIR is set.
const shot = name => require('node:path').join(process.env.SCREENSHOT_DIR || require('node:os').tmpdir(), name);
(async () => {
 const browser = await chromium.launch({headless:true,executablePath:process.env.TEST_BROWSER});
 const context = await browser.newContext({viewport:{width:1280,height:900}});
 const page = await context.newPage(); const errors=[];
 async function openTaskEditor(name) {
  await page.getByRole('button',{name,exact:true}).click();
  await page.locator('#task-editor').waitFor();
 }
 const saved=()=>page.locator('.task-save-status').filter({hasText:/^Saved$/}).waitFor();
 const closeEditor=async()=>{await page.locator('#task-editor').getByRole('button',{name:'Done',exact:true}).click();await page.locator('#task-editor').waitFor({state:'detached'});};
 page.on('pageerror',e=>errors.push(e.message));
 page.on('console',m=>{if(/Content Security Policy/i.test(m.text()))errors.push(m.text());});
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
 // The capture date is one chip: picking shows the date, × clears it.
 const captureDate=page.locator('#capture-tasks .chip-date');
 await captureDate.locator('input').fill('2026-10-02');await captureDate.locator('input').dispatchEvent('change');
 assert.equal(await captureDate.locator('.chip-text').textContent(),'Oct 2');
 await captureDate.locator('.chip-clear').click();
 assert.equal(await captureDate.locator('input').inputValue(),'');assert.equal(await captureDate.locator('.chip-clear').isHidden(),true);
 // The sidebar counts open inbox tasks and lists projects, which open as pages.
 await page.waitForFunction(()=>document.getElementById('nav-count-tasks')?.textContent.trim()==='1');
 // A date typed at the end of a new task becomes its due date and leaves the title.
 await page.locator('#capture-tasks [name=text]').fill('Pay rent tomorrow');
 assert.notEqual(await page.locator('#capture-tasks .chip-text').textContent(),'Date','typed date not previewed');
 await page.locator('#capture-tasks button.add-circle').click();
 await page.locator('#section-tasks .todo-item').filter({hasText:'Pay rent'}).locator('.todo-date').waitFor();
 assert.equal(await page.getByRole('button',{name:'Pay rent',exact:true}).count(),1,'typed date left in the title');
 assert.equal(await page.locator('#capture-tasks .chip-date input').inputValue(),'','capture kept the typed date');
 await page.getByRole('button',{name:'New project',exact:true}).click();
 await page.locator('#custom-dialog-input').fill('Car');await page.locator('#custom-dialog-confirm').click();
 await page.locator('.sidebar-project').filter({hasText:'Car'}).click();
 await page.locator('[data-project-page] > summary').filter({hasText:'Car'}).waitFor();
 assert.equal(await page.locator('#capture-tasks').isVisible(),false,'inbox shown on a project page');
 assert.equal(await page.locator('.sidebar-project.active').textContent(),'Car','sidebar does not mark the open project');
 await page.getByRole('button',{name:'Heading',exact:true}).click();
 await page.locator('#custom-dialog-input').fill('Driving licence');await page.locator('#custom-dialog-confirm').click();
 await page.locator('.heading-row h3').filter({hasText:'Driving licence'}).waitFor();
 const capture=page.locator('.heading-group .capture-form');await capture.locator('[name=text]').fill('Book lessons');await capture.locator('button.add-circle').click();
 // Tapping a task opens it in place; edits save as you type.
 await openTaskEditor('Book lessons');
 const detailTitle=page.locator('#task-editor [name=text]');
 await detailTitle.fill('Book practical lessons');await saved();
 await page.locator('#task-editor [name=due_date]').fill('2026-10-01');await saved();
 assert.equal(await page.locator('#task-editor .chip.is-set').count(),1,'set date chip not marked');
 // A reply must not overwrite text typed after the request was sent, and
 // focus stays in the field being edited.
 let releaseTask;const heldTask=new Promise(resolve=>releaseTask=resolve);
 await page.route('**/task/save',async route=>{const response=await route.fetch();await heldTask;await route.fulfill({response});},{times:1});
 const taskRequest=page.waitForRequest('**/task/save');
 await detailTitle.fill('Book lessons soon');await taskRequest;
 await detailTitle.pressSequentially(' please');releaseTask();
 await saved();
 assert.equal(await detailTitle.inputValue(),'Book lessons soon please');
 assert.equal(await detailTitle.evaluate(el=>el===document.activeElement),true,'autosave moved focus');
 // A refresh while editing keeps the editor, its text and its focus.
 await detailTitle.fill('Book practical lessons');
 await page.evaluate(()=>refreshWorkspace());
 assert.equal(await detailTitle.inputValue(),'Book practical lessons');
 assert.equal(await detailTitle.evaluate(el=>el===document.activeElement),true,'refresh moved focus out of the editor');
 await saved();
 await closeEditor();
 await page.getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 // Escape and tapping elsewhere close the editor too.
 await openTaskEditor('Book practical lessons');await page.keyboard.press('Escape');await page.locator('#task-editor').waitFor({state:'detached'});
 await openTaskEditor('Book practical lessons');await page.locator('[data-project-page] > summary').click();await page.locator('#task-editor').waitFor({state:'detached'});
 // Completion and reopening preserve the task and collapsed state.
 await page.locator('#section-tasks').evaluate(el=>el.dataset.testStable='true');
 await page.getByRole('button',{name:'Complete Book practical lessons',exact:true}).click();
 assert.equal(await page.locator('#section-tasks').getAttribute('data-test-stable'),'true','task completion replaced the entire workspace section');
 const completed=page.locator('.heading-group .completed-group');if(!await completed.evaluate(el=>el.open))await completed.locator(':scope > summary').click();
 await page.getByRole('button',{name:'Reopen Book practical lessons',exact:true}).click();
 await page.getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 // Draft survives another task's mutation and a refresh.
 await capture.locator('[name=text]').fill('Keep this draft');
 await page.evaluate(()=>refreshWorkspace());
 assert.equal(await page.locator('.heading-group .capture-form [name=text]').inputValue(),'Keep this draft');
 await page.locator('.heading-group .capture-form [name=text]').fill('');
 // Deleting a heading keeps its tasks in the project.
 await page.locator('.heading-row .row-menu > summary').click();
 await page.getByRole('button',{name:'Delete heading',exact:true}).click();
 await page.locator('#custom-dialog-confirm').click();
 await page.locator('.heading-group').waitFor({state:'detached'});
 await page.locator('[data-project-page]').getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 // The project's own actions sit in its … menu; Complete waits for open tasks.
 await page.locator('[data-project-page] .project-menu > summary').click();
 assert.equal(await page.getByRole('button',{name:'Complete project',exact:true}).isDisabled(),true,'project with open tasks can be completed');
 await page.getByRole('button',{name:'Rename',exact:true}).waitFor();
 await page.screenshot({path:shot('secondbrain-desktop.png'),fullPage:true});
 await page.locator('[data-project-page] .project-menu > summary').click();
 // One archive for everything, opened from the sidebar; Back returns to the project.
 await page.locator('.bottom-nav-btn[data-workspace="archive"]').click();
 await page.locator('.archive-kinds button.active').filter({hasText:'Tasks'}).waitFor();
 await page.locator('.archive-kinds button').filter({hasText:'Projects'}).click();
 await page.locator('.archive-kinds button.active').filter({hasText:'Projects'}).waitFor();
 await page.getByRole('button',{name:'Back',exact:true}).click();
 await page.locator('[data-project-page] > summary').filter({hasText:'Car'}).waitFor();
 for (const width of [320,375,390,430,1024,1440]) {
  await page.setViewportSize({width,height:900});
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth && document.getElementById('app').getBoundingClientRect().right<=innerWidth),true,'horizontal overflow '+width);
  if(width===390)await page.screenshot({path:shot('secondbrain-iphone.png'),fullPage:true});
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
 // On phones the note title opens the note list as a sheet; the backdrop closes it.
 assert.equal(await page.locator('.note-desktop-action').first().isVisible(),false,'phone shows desktop note actions');
 await page.locator('.note-picker-btn').click();await page.locator('#note-picker-panel').waitFor();
 assert.equal(await page.locator('#note-picker-panel').evaluate(el=>{const b=el.getBoundingClientRect();return b.left>=0&&b.right<=innerWidth&&b.bottom<=innerHeight}),true,'note sheet overflows');
 await page.locator('#note-sheet-backdrop').click({position:{x:5,y:5}});await page.locator('#note-picker-panel').waitFor({state:'hidden'});
 await page.locator('.note-menu > summary').click();await page.getByRole('button',{name:'Rename',exact:true}).waitFor();await page.locator('.note-menu > summary').click();
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 await page.evaluate(()=>window.scrollTo(0,document.documentElement.scrollHeight));
 await page.locator('.bottom-nav-btn[data-mode="today"]').click();
 await page.waitForFunction(()=>scrollY===0);
 await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 const todayNav=await mobileNavGeometry();
 assert.equal(taskNav.buttons.length,3,'mobile navigation should offer Today, Tasks and Notes');
 assert.deepEqual(notesNav,taskNav,'mobile navigation moved in Notes');
 assert.deepEqual(todayNav,taskNav,'mobile navigation moved in Today');
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 await page.locator('#topbar-title').click();
 assert.equal(await page.locator('#mobile-workspace-menu').isVisible(),true,'mobile workspace sheet did not open');
 await page.locator('[data-workspace-option="groceries"]').click();
 assert.equal(await page.locator('#section-groceries').isVisible(),true,'mobile workspace selection did not change');
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="tasks"]').click();
 await page.locator('#topbar-stat').click();await page.locator('#today-content .today-heading').waitFor();
 // Today's footer says when the daily reminder goes out; there is no settings screen.
 assert.match(await page.locator('#today-content .today-reminder-line').textContent(),/Reminder at \d\d:\d\d when tasks are due/);
 await page.locator('#push-status').filter({hasText:/\S/}).waitFor();
 await page.locator('#topbar-title').click();
 assert.equal(await page.locator('#mobile-workspace-menu').getByRole('button',{name:'Log out',exact:true}).isVisible(),true,'Go-to sheet has no Log out');
 await page.keyboard.press('Escape');
 await page.locator('#topbar-title').click();await page.locator('#mobile-workspace-menu').getByRole('button',{name:'Today',exact:true}).click();
 assert.equal(await page.evaluate(()=>document.body.dataset.mode),'today','Today did not open');
 await page.locator('#today-content .today-heading').waitFor();
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="groceries"]').click();
 assert.equal(await page.locator('#section-groceries').isVisible(),true,'Today did not preserve the last task list');
 await page.locator('#topbar-title').click();await page.locator('[data-workspace-option="tasks"]').click();
 await openTaskEditor('Book practical lessons');
 assert.equal(await page.locator('#task-editor').evaluate(el=>{const b=el.getBoundingClientRect();return b.left>=0&&b.right<=innerWidth}),true,'mobile task editor overflow');
 // The list control moves a task; it changes place once the editor closes.
 const todayISO=await page.evaluate(()=>new Date().toLocaleDateString('en-CA',{timeZone:document.body.dataset.timezone}));
 await page.locator('#task-editor [name=due_date]').fill(todayISO);
 await page.locator('#task-editor [name=list]').selectOption({label:'Inbox'});await saved();
 await closeEditor();
 await page.locator('.inbox-group').getByRole('button',{name:'Book practical lessons',exact:true}).waitFor();
 // Today opens tasks in place as well.
 await page.locator('.bottom-nav-btn[data-mode="today"]').click();
 await page.locator('#today-content').getByRole('button',{name:'Book practical lessons',exact:true}).click();
 await page.locator('#today-content #task-editor').waitFor();
 await page.locator('#task-editor [name=text]').fill('Book the practical lessons');await saved();
 await closeEditor();
 await page.locator('#today-content').getByRole('button',{name:'Book the practical lessons',exact:true}).waitFor();
 await page.getByRole('button',{name:'Tasks',exact:true}).click();
 await page.locator('.inbox-group').getByRole('button',{name:'Book the practical lessons',exact:true}).waitFor();
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
 await page.evaluate(()=>openToday());await page.getByRole('heading',{name:'Today',exact:true}).waitFor();
 // Root service worker must become ready, unlike the old /static/ scope.
 const scope=await page.evaluate(async()=> (await navigator.serviceWorker.ready).scope);assert.equal(scope,base+'/');
 const manifest=await page.evaluate(()=>fetch('/static/manifest.json').then(response=>response.json()));
 assert.equal(manifest.start_url,'/?view=today','PWA does not launch into Today');
 assert.equal(manifest.launch_handler?.client_mode,'navigate-existing','desktop PWA can reopen on its stale view');
 await page.emulateMedia({colorScheme:'dark'});await page.screenshot({path:shot('secondbrain-today-dark.png'),fullPage:true});
 // The last page loaded online stays readable offline, marked read-only,
 // and logging out removes it from the device.
 await page.goto(base+'/?view=todos');await page.evaluate(()=>navigator.serviceWorker.ready);
 await page.waitForFunction(()=>caches.open('secondbrain-page').then(c=>c.match('/')).then(Boolean));
 await context.setOffline(true);await page.reload();
 assert.equal(await page.locator('body[data-offline-copy="1"]').count(),1,'offline copy not served');
 assert.equal(await page.getByRole('button',{name:'Call the driving school',exact:true}).count()>0,true,'offline copy lost tasks');
 assert.match(await page.locator('#topbar-stat').textContent(),/^Offline · /);
 await context.setOffline(false);await page.goto(base+'/');
 await page.evaluate(()=>doLogout());await page.waitForURL(url=>url.pathname==='/login');
 assert.equal(await page.evaluate(()=>caches.has('secondbrain-page')),false,'offline copy survived logout');
 assert.deepEqual(errors,[],'browser errors');
 await browser.close();console.log('UI smoke passed: Today, projects, headings, in-place task editing, completion, drafts, notes, reminder line, root worker, offline copy, six viewport widths.');
})().catch(error=>{console.error(error);process.exit(1)});
