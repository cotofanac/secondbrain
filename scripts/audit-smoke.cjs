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
 await page.locator('#task-editor').waitFor();
 await page.locator('#task-editor [name=repeat]').selectOption('weekly');
 await page.locator('.task-save-status').filter({hasText:/^Saved$/}).waitFor();
 await page.locator('#task-editor').getByRole('button',{name:'Done',exact:true}).click();
 await page.locator('#section-tasks .todo-repeat[aria-label="Repeats weekly"]').waitFor();
 await page.getByRole('button',{name:'Complete Water the plants',exact:true}).click();
 await page.locator('#toast').filter({hasText:/^Next one due in 7 days$/}).waitFor();
 await page.waitForFunction(()=>document.querySelectorAll('#section-tasks .todo-item:not(.done) .todo-repeat').length===1);
 assert.equal(await page.locator('#section-tasks .todo-item.done').filter({hasText:'Water the plants'}).count(),1,'completed occurrence missing');

 await page.keyboard.press('Meta+k');
 await page.locator('#search-input').fill('Water the plants');
 await page.locator('#search-results .search-result-item').first().waitFor();
 // A search result opens the task in place.
 await page.locator('#search-results .search-result-item').first().click();
 await page.locator('#task-editor').waitFor();
 await page.keyboard.press('Escape');

 // Refreshing the task list must keep the reader's place. Open projects load
 // their bodies after the list, so a plain scroll offset used to land near the
 // first open project; every path that refreshes the list is checked here.
 await page.goto(base+'/?view=todos',{waitUntil:'networkidle'});
 const ids=await page.evaluate(async()=>{
  const post=(url,body)=>fetch(url,{method:'POST',headers:{'HX-Request':'true'},body:new URLSearchParams(body)});
  for(let p=1;p<=4;p++)await post('/projects/action',{id:0,action:'create',name:'Scroll '+p});
  const html=await (await fetch('/workspace',{headers:{'HX-Request':'true'}})).text();
  const doc=new DOMParser().parseFromString(html,'text/html');
  const ids=[...doc.querySelectorAll('.project-group')].filter(p=>/Scroll \d/.test(p.querySelector('summary')?.textContent||'')).map(p=>p.id.replace('project-',''));
  for(const id of ids)for(let t=1;t<=8;t++)await post('/todos/add',{category:'todo',list:'project:'+id,text:'Scroll '+id+'-'+t});
  return ids;
 });
 assert.equal(ids.length,4,'scroll fixture projects missing');
 await page.reload({waitUntil:'networkidle'});
 for(const id of ids)await page.locator('#project-'+id).evaluate(d=>{d.open=true;});
 const settled=async()=>{
  await page.waitForFunction(()=>!document.querySelector('.project-group[open] [data-lazy-project]:not([data-loaded])')&&!document.getElementById('todo-items').style.minHeight);
  await page.waitForTimeout(300);
 };
 await settled();
 const last=page.locator('#project-'+ids[3]);
 const top=loc=>loc.evaluate(el=>Math.round(el.getBoundingClientRect().top));
 const near=(a,b,what)=>assert.ok(Math.abs(a-b)<4,what+' moved from '+b+' to '+a);
 // Adding in the last open project keeps its capture row put, focused and empty.
 const add=last.locator('.capture-form [name=text]');
 await add.scrollIntoViewIfNeeded();
 let before=await top(add);
 await add.fill('Added low down');await add.press('Enter');
 await last.getByRole('button',{name:'Added low down',exact:true}).waitFor();await settled();
 near(await top(add),before,'project capture');
 assert.equal(await add.inputValue(),'','project capture kept the added text');
 assert.equal(await add.evaluate(el=>el===document.activeElement),true,'project capture lost focus');
 assert.equal(await add.evaluate(el=>localStorage.getItem('capture-draft-'+el.form.id)),null,'added text kept as a draft');
 // Archiving a row moves the next one into its place.
 const victim=last.locator('.todo-item').nth(2);
 await victim.scrollIntoViewIfNeeded();
 const next=(await last.locator('.todo-item').nth(3).locator('.task-label').textContent()).trim();
 await victim.locator('.row-menu > summary').click();
 before=await top(victim);
 await victim.getByRole('button',{name:'Archive'}).evaluate(b=>b.click());
 await page.locator('#toast.show').waitFor();await settled();
 near(await top(last.locator('.todo-item').filter({hasText:next})),before,'row after an archived one');
 // Coming back to the app refreshes the list with nothing focused.
 await page.evaluate(()=>document.activeElement?.blur());
 const row=last.locator('.todo-item').last();
 before=await top(row);
 await page.evaluate(()=>refreshCurrentView());await page.waitForResponse(r=>r.url().endsWith('/workspace'));await settled();
 near(await top(row),before,'row after a refresh');
 // Two refreshes in a row while project bodies load slowly end in the same place.
 await page.route('**/workspace/project?*',route=>setTimeout(()=>route.continue(),400));
 await page.evaluate(()=>{refreshWorkspace();setTimeout(refreshWorkspace,150);});
 await page.waitForTimeout(200);await settled();
 near(await top(row),before,'row after overlapping slow refreshes');
 await page.unroute('**/workspace/project?*');
 // Closing the task editor after an edit refreshes the list.
 const edited=last.locator('.todo-item').nth(1);
 const title=(await edited.locator('.task-label').textContent()).trim();
 await edited.locator('.task-label').click();await page.locator('#task-editor').waitFor();
 await page.locator('#task-editor [name=text]').fill(title+' edited');
 await page.locator('.task-save-status').filter({hasText:/^Saved$/}).waitFor();
 before=await top(edited);
 await page.keyboard.press('Escape');await page.waitForResponse(r=>r.url().endsWith('/workspace'));await settled();
 near(await top(last.locator('.todo-item').filter({hasText:title+' edited'})),before,'edited row');
 // A failed add puts the text back and keeps it as a draft.
 await page.route('**/todos/add',route=>route.abort());
 await add.fill('Offline add');await add.press('Enter');
 await page.waitForFunction(id=>document.querySelector('#project-'+id+' .capture-form [name=text]').value==='Offline add',ids[3]);
 assert.equal(await last.locator('.capture-pending').count(),0,'pending row left after a failed add');
 assert.match(await add.evaluate(el=>localStorage.getItem('capture-draft-'+el.form.id)||''),/Offline add/,'failed add not kept as a draft');
 await page.unroute('**/todos/add');
 await add.fill('');
 // Adds, check-offs and archives inside a project swap only that list; its
 // header count comes with it. Adding again at once must not post natively.
 const stats=()=>page.locator('#project-'+ids[0]+' > summary .section-count').textContent();
 const first=page.locator('#project-'+ids[0]);
 const firstAdd=first.locator('.capture-form [name=text]').first();
 const total=Number((await stats()).split('/')[1]);
 let requests=0;const countRequest=r=>{if(new URL(r.url()).pathname==='/workspace/project')requests++;};
 page.on('request',countRequest);
 for(const t of ['Quick one','Quick two']){await firstAdd.fill(t);await firstAdd.press('Enter');await first.getByRole('button',{name:t,exact:true}).waitFor();}
 assert.equal(await stats(),'0/'+(total+2));
 await first.getByRole('button',{name:'Complete Quick one',exact:true}).click();
 await page.waitForFunction(([id,want])=>document.querySelector('#project-'+id+' > summary .section-count').textContent===want,[ids[0],'1/'+(total+2)]);
 const quick=first.locator('.todo-item').filter({hasText:'Quick two'});
 await quick.locator('.row-menu > summary').click();await quick.getByRole('button',{name:'Archive'}).click();
 await page.waitForFunction(([id,want])=>document.querySelector('#project-'+id+' > summary .section-count').textContent===want,[ids[0],'1/'+(total+1)]);
 page.off('request',countRequest);
 assert.equal(requests,0,'a change inside a project reloaded project bodies');
 assert.equal(new URL(page.url()).search.includes('text='),false,'a capture row posted natively');
 // Today's capture row keeps focus across an add, so the keyboard stays up.
 await page.locator('.bottom-nav-btn[data-mode="today"]').click();
 const today=page.locator('.today-capture [name=text]');
 await today.fill('Buy stamps');await today.press('Enter');
 await page.locator('#today-content .today-row-label').filter({hasText:'Buy stamps'}).waitFor();
 assert.equal(await today.inputValue(),'');
 assert.equal(await today.evaluate(el=>el===document.activeElement),true,'Today capture lost focus');
 // Today re-renders when it opens or changes; what is being typed survives.
 await today.fill('Half typed');
 await page.evaluate(()=>openToday());await page.waitForResponse(r=>r.url().endsWith('/today'));await page.waitForTimeout(100);
 assert.equal(await today.inputValue(),'Half typed','Today refresh dropped the text being typed');
 assert.equal(await today.evaluate(el=>el===document.activeElement),true,'Today refresh took focus away');
 await today.fill('');
 await browser.close();
 console.log('Audit smoke passed: reload drafts, history, repeating tasks, search, and list refreshes keeping their place.');
})().catch(error=>{console.error(error);process.exit(1)});
