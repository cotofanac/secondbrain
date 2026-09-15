// State is limited to presentation and unsaved drafts; task truth stays on the server.
let detailSelection=null;
let pendingFocus=null;
let workspaceState=null;
let noteViewState=null;
let archiveCategory='todo';
let detailReturnFocus=null;
let captureRequestID=0;
function readLocal(key){try{return localStorage.getItem(key);}catch(_){return null;}}
function writeLocal(key,value){try{localStorage.setItem(key,value);}catch(_){}}
function restoreSections(){
 document.querySelectorAll('details[data-persist]').forEach(d=>{const saved=readLocal('section-'+d.dataset.persist);if(saved!==null)d.open=saved==='1';});
}
document.addEventListener('toggle',e=>{const d=e.target;if(d.matches?.('details[data-persist]'))writeLocal('section-'+d.dataset.persist,d.open?'1':'0');},true);
function rememberWorkspace(){
 const forms={};document.querySelectorAll('#todo-items .capture-form').forEach(f=>{forms[f.id]=Object.fromEntries(new FormData(f));});
 const el=document.activeElement;
 const row=el?.closest('.todo-item');
 workspaceState={forms,scroll:window.scrollY,focus:el?.closest('.capture-form')?.id,name:el?.name,start:el?.selectionStart,end:el?.selectionEnd,rowId:row?.dataset.taskId,rowControl:el?.classList.contains('check-btn')?'check':el?.classList.contains('task-label')?'label':''};
}
function restoreWorkspace(){
 restoreSections();
 if(workspaceState){const s=workspaceState;for(const [id,values] of Object.entries(s.forms)){const form=document.getElementById(id);if(form){for(const [key,value]of Object.entries(values)){const el=form.elements.namedItem(key);if(el)el.value=value;}}}
 const form=document.getElementById(s.focus);const el=form?.elements.namedItem(s.name);if(el){el.focus({preventScroll:true});if(typeof s.start==='number')el.setSelectionRange?.(s.start,s.end);}
 if(s.rowId){const row=document.getElementById('task-'+s.rowId);if(row)expandAncestors(row);const control=s.rowControl==='check'?row?.querySelector('.check-btn'):row?.querySelector('.task-label');control?.focus({preventScroll:true});}
 requestAnimationFrame(()=>window.scrollTo(0,s.scroll));workspaceState=null;
 }
 if(pendingFocus){revealTask(pendingFocus);pendingFocus=null;}
}
function refreshWorkspace(){return htmx.ajax('GET','/workspace','#todo-items');}
function expandAncestors(el){let node=el;while(node){if(node.tagName==='DETAILS')node.open=true;node=node.parentElement;}}
function showWorkspaceContaining(el){const section=el?.closest('.workspace-section');if(!section)return;const key=section.id.replace('section-','');if(WORKSPACE_TITLES[key]){document.body.dataset.workspaceSection=key;writeLocal('desktop-workspace',key);updateNavigation('todos');document.getElementById('topbar-title').textContent=WORKSPACE_TITLES[key];}}
function revealTask(id){const row=document.getElementById('task-'+id);if(row){showWorkspaceContaining(row);document.querySelectorAll('.todo-item.selected').forEach(el=>el.classList.remove('selected'));expandAncestors(row);row.scrollIntoView({block:'nearest'});row.classList.add('selected');}}
function setDestination(values){const url=new URL(location.href);url.search='';Object.entries(values).forEach(([k,v])=>url.searchParams.set(k,v));history.replaceState(null,'',url);}
function canLeaveDetail(){const form=document.getElementById('task-detail-form');if(form?.dataset.dirty){showToast('Save your task changes before closing these details.');return false;}return true;}
function showDetail(url){const pane=document.getElementById('detail-pane');const opening=pane.hidden;if(pane.dataset.url!==url){pane.textContent='Loading details…';pane.dataset.url=url;}pane.hidden=false;if(opening){pane.classList.remove('detail-entering');void pane.offsetWidth;pane.classList.add('detail-entering');setTimeout(()=>pane.classList.remove('detail-entering'),220);}pane.dataset.focusAfterLoad='1';document.querySelector('.task-layout').classList.add('has-detail');return htmx.ajax('GET',url,'#detail-pane').catch(()=>{});}
function discardTaskChanges(){const form=document.getElementById('task-detail-form');if(form){delete form.dataset.dirty;showDetail('/task/detail?id='+form.elements.id.value);}}
function closePopupMenus(){document.querySelectorAll('.row-menu,.app-menu,.capture-options').forEach(d=>d.open=false);}
async function openTask(id){if(!id||!canLeaveDetail())return;detailReturnFocus=document.querySelector('#task-'+id+' .task-label');closePopupMenus();switchMode('todos');detailSelection={kind:'task',id};
 if(document.querySelector('#todo-items .archive-header')){pendingFocus=id;await refreshWorkspace();}
 revealTask(id);setDestination({view:'todos',task:id});showDetail('/task/detail?id='+id);
}
async function openProject(id){if(!canLeaveDetail())return;document.body.dataset.workspaceSection='projects';writeLocal('desktop-workspace','projects');switchMode('todos');if(document.querySelector('#todo-items .archive-header'))await refreshWorkspace();detailSelection={kind:'project',id};const el=document.getElementById('project-'+id);if(el){showWorkspaceContaining(el);expandAncestors(el);el.open=true;}setDestination({view:'todos',project:id});showDetail('/projects/detail?id='+id);}
function closeDetail(){if(!canLeaveDetail())return;document.getElementById('detail-pane').hidden=true;document.querySelector('.task-layout').classList.remove('has-detail');detailSelection=null;setDestination({view:'todos'});const target=detailReturnFocus;detailReturnFocus=null;target?.focus({preventScroll:true});}

function taskJSON(response){return response.json().catch(()=>({error:'Could not save task'}));}
function taskAction(label,onClick){const button=document.createElement('button');button.type='button';button.className='task-edit-action';button.textContent=label;button.addEventListener('mousedown',e=>e.preventDefault());button.addEventListener('click',onClick);return button;}
function beginTaskTitleEdit(button){
 const row=button.closest('.todo-item');if(!row||row.querySelector('.task-title-editor'))return;
 const original=button.textContent.trim();const editor=document.createElement('span');editor.className='task-title-editor';
 const input=document.createElement('input');input.type='text';input.maxLength=500;input.value=original;input.setAttribute('aria-label','Task title');
 const status=document.createElement('span');status.className='task-edit-message';status.setAttribute('role','status');status.setAttribute('aria-live','polite');
 const actions=document.createElement('span');actions.className='task-edit-actions';editor.append(input,status,actions);button.replaceWith(editor);input.focus();input.select();
 let saving=false,finished=false,latest=null;
 const cancel=()=>{if(finished)return;finished=true;const restored=document.createElement('button');restored.type='button';restored.className='task-label';restored.dataset.action='edit-task-title';restored.textContent=original;editor.replaceWith(restored);restored.focus({preventScroll:true});};
 const useLatest=()=>{if(!latest)return;finished=true;row.dataset.revision=String(latest.revision);const restored=document.createElement('button');restored.type='button';restored.className='task-label';restored.dataset.action='edit-task-title';restored.textContent=latest.text;editor.replaceWith(restored);restored.focus({preventScroll:true});};
 async function save(force=false){
  if(finished||saving)return;const text=input.value.trim();if(!text){status.textContent='Enter a task title';input.focus();return;}if(text===original&&!force){cancel();return;}
  saving=true;status.textContent='Saving…';actions.replaceChildren();input.disabled=true;
  try{const response=await fetch('/todos/edit',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded','Accept':'application/json'},body:new URLSearchParams({id:row.dataset.taskId,text,revision:force?latest.revision:row.dataset.revision})});const data=await taskJSON(response);
   if(response.status===409){latest=data.task;saving=false;input.disabled=false;status.textContent='Changed elsewhere. Latest: '+latest.text;actions.append(taskAction('Keep mine',()=>save(true)),taskAction('Use latest',useLatest));actions.querySelector('button')?.focus();return;}
   if(!response.ok)throw new Error(data.error||'Could not save task');
   finished=true;row.dataset.revision=String(data.revision);const saved=document.createElement('button');saved.type='button';saved.className='task-label';saved.dataset.action='edit-task-title';saved.textContent=data.text;editor.replaceWith(saved);saved.focus({preventScroll:true});const live=row.querySelector('.task-edit-status');if(live){live.textContent='Saved';setTimeout(()=>{if(live.textContent==='Saved')live.textContent='';},1200);}
  }catch(error){saving=false;input.disabled=false;status.textContent=error.message||'Could not save task';actions.append(taskAction('Retry',()=>save(false)),taskAction('Cancel',cancel));input.focus();}
 }
 input.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();save();}else if(e.key==='Escape'){e.preventDefault();cancel();}});
 input.addEventListener('blur',()=>setTimeout(()=>{if(!finished&&!saving&&!editor.contains(document.activeElement)&&!actions.childElementCount)save();},0));
}
document.addEventListener('click',e=>{const button=e.target.closest?.('[data-action="edit-task-title"]');if(button)beginTaskTitleEdit(button);});
function filterStageOptions(form){if(!form?.elements.project_id)return;const project=form.elements.project_id.value;const select=form.elements.stage_id;for(const option of select.options){option.hidden=!!option.dataset.project&&option.dataset.project!==project;option.disabled=option.hidden;}if(select.selectedOptions[0]?.disabled)select.value='0';}
async function structureCreate(kind,parent){const name=await showPrompt(kind==='project'?'New project':'New stage');if(name)structureAction(kind,0,'create',{name,project_id:parent});}
async function structureRename(kind,id,el){const name=await showPrompt('Rename '+kind,el.dataset.name);if(name)structureAction(kind,id,'rename',{name});}
async function structureAction(kind,id,action,extra={}){await htmx.ajax('POST',kind==='project'?'/projects/action':'/stages/action',{target:'#todo-items',values:{id,action,...extra}});}
function openTaskArchive(category){if(!canLeaveDetail())return;closeDetail();archiveCategory=category;htmx.ajax('GET','/todos/archive?category='+category,'#todo-items');}
function hideArchive(){refreshWorkspace();}
function openSettings(){switchMode('settings');setDestination({view:'settings'});htmx.ajax('GET','/settings','#settings-content');}
function openToday(){switchMode('today');setDestination({view:'today'});htmx.ajax('GET','/today','#today-content');}
function openReview(id){switchMode('today');setDestination(id?{view:'review',review:id}:{view:'today'});htmx.ajax('GET','/review'+(id?'?id='+id:''),'#today-content');}
function routeLocation(){const q=new URLSearchParams(location.search);if(q.get('task'))openTask(Number(q.get('task')));else if(q.get('project'))openProject(Number(q.get('project')));else if(q.get('view')==='review')openReview(q.get('review'));else if(q.get('view')==='today')openToday();else if(q.get('view')==='settings')openSettings();else if(q.get('note'))openNoteResult(Number(q.get('note')));else if(q.get('stage'))openStageResult(Number(q.get('stage')));else if(q.get('view'))switchMode(q.get('view'));}
async function openStageResult(id){closeSearch();document.body.dataset.workspaceSection='projects';writeLocal('desktop-workspace','projects');switchMode('todos');if(document.querySelector('#todo-items .archive-header'))await refreshWorkspace();setDestination({view:'todos',stage:id});const el=document.getElementById('stage-'+id);if(el){showWorkspaceContaining(el);expandAncestors(el);el.open=true;el.scrollIntoView({block:'nearest'});}}
document.body.addEventListener('htmx:beforeSwap',e=>{
 if(e.detail.target.id==='detail-pane') {
  const config=e.detail.requestConfig;
  const pane=document.getElementById('detail-pane');
  if(config?.verb==='get' && pane.dataset.url && e.detail.xhr.responseURL!==new URL(pane.dataset.url,location.origin).href){e.detail.shouldSwap=false;return;}
  if(config?.path==='/task/save') {
   const form=document.getElementById('task-detail-form');
   if(!form || String(config.parameters.id)!==form.elements.id.value){e.detail.shouldSwap=false;return;}
   const current=Object.fromEntries(new FormData(form));
   if(Object.entries(current).some(([key,value])=>String(config.parameters[key] ?? '')!==value)){
    e.detail.shouldSwap=false;
    form.dataset.dirty='1';
    form.querySelector('.detail-save-status').textContent='Previous changes saved. Your newer edits are still here.';
   }
  }
 }
 if(e.detail.target.id==='todo-items') {rememberWorkspace();const form=e.detail.requestConfig?.elt;if(e.detail.xhr.status<300 && form?.classList.contains('capture-form')){const values=workspaceState.forms[form.id];if(values&&values.text===e.detail.requestConfig.parameters.text){values.text='';values.due_date='';}}}
 // A background response must never overwrite a note being edited now.
 if(e.detail.target.id==='notes-content'){
  const editor=document.getElementById('note-editor');
  if(editor?.dataset.dirty){e.detail.shouldSwap=false;return;}
  if(editor)noteViewState={id:editor.dataset.noteId,start:editor.selectionStart,end:editor.selectionEnd,scroll:editor.scrollTop,focused:document.activeElement===editor};
 }
});
document.body.addEventListener('htmx:beforeRequest',e=>{
 const form=e.detail.elt;
 if(form?.getAttribute('hx-post')==='/todos/toggle'){
  const row=form.closest('.todo-item');
  if(row&&!row.classList.contains('done'))row.classList.add('is-completing');
 }
 if(!form?.classList.contains('capture-form'))return;
 const input=form.elements.text;
 const text=input?.value.trim();
 if(!text)return;
 const pending=document.createElement('div');
 pending.className='todo-item capture-pending';
 pending.dataset.captureRequest=String(++captureRequestID);
 const marker=document.createElement('span');marker.className='pending-marker';marker.setAttribute('aria-hidden','true');
 const label=document.createElement('span');label.className='task-label';label.textContent=text;
 pending.append(marker,label);
 form.insertAdjacentElement('afterend',pending);
 form.closest('.task-group')?.querySelector('.quiet-empty')?.setAttribute('hidden','');
 form.dataset.pendingRow=pending.dataset.captureRequest;
 input.value='';
 input.focus({preventScroll:true});
});
function restoreFailedCapture(e){
 const form=e.detail.elt;
 if(!form?.classList.contains('capture-form'))return;
 const pending=document.querySelector('[data-capture-request="'+form.dataset.pendingRow+'"]');
 if(pending&&!form.elements.text.value)form.elements.text.value=pending.querySelector('.task-label')?.textContent||'';
 pending?.remove();
 form.closest('.task-group')?.querySelector('.quiet-empty')?.removeAttribute('hidden');
}
document.body.addEventListener('htmx:responseError',restoreFailedCapture);
document.body.addEventListener('htmx:sendError',restoreFailedCapture);
function restoreFailedCompletion(e){e.detail.elt?.closest('.todo-item')?.classList.remove('is-completing');}
document.body.addEventListener('htmx:responseError',restoreFailedCompletion);
document.body.addEventListener('htmx:sendError',restoreFailedCompletion);
document.addEventListener('keydown',e=>{
 const input=e.target.closest?.('.capture-form input[name="text"]');
 if(!input||e.key!=='Enter'||e.isComposing)return;
 e.preventDefault();
 input.form.requestSubmit();
});
document.body.addEventListener('htmx:afterSwap',e=>{
 const id=e.detail.target.id;
 if(id==='todo-items'){restoreWorkspace();if(detailSelection?.kind==='task')document.getElementById('task-'+detailSelection.id)?.classList.add('selected');if(detailSelection?.kind==='project')showDetail('/projects/detail?id='+detailSelection.id);}
 if(id==='detail-pane'){filterStageOptions(document.getElementById('task-detail-form'));const pane=document.getElementById('detail-pane');if(pane.dataset.focusAfterLoad){delete pane.dataset.focusAfterLoad;pane.focus({preventScroll:true});}}
 if(id==='notes-content'){initNoteEditor();const editor=document.getElementById('note-editor');if(editor&&document.body.dataset.mode==='notes')setDestination({view:'notes',note:editor.dataset.noteId});if(editor&&noteViewState?.id===editor.dataset.noteId){editor.setSelectionRange(noteViewState.start,noteViewState.end);editor.scrollTop=noteViewState.scroll;if(noteViewState.focused)editor.focus({preventScroll:true});}noteViewState=null;}
 if(id==='settings-content'){document.body.dataset.timezone=document.querySelector('#schedule-form [name=timezone]')?.value || document.body.dataset.timezone;updateTopbarStat();preparePush();}
});
document.body.addEventListener('sbWorkspaceChanged',e=>{refreshWorkspace();if(e.detail.message)showToast(e.detail.message);});
document.addEventListener('input',e=>{const form=e.target.closest('#task-detail-form');if(form)form.dataset.dirty='1';});
document.addEventListener('keydown',e=>{
 if(e.metaKey&&e.key.toLowerCase()==='k'){e.preventDefault();openSearch();}
 if(e.metaKey&&e.key==='Enter'){const form=document.activeElement.closest('form');if(form){e.preventDefault();form.requestSubmit();}}
 if(e.key==='Escape'){closeSearch();closePopupMenus();if(document.body.dataset.mode==='todos'&&!document.getElementById('detail-pane').hidden)closeDetail();}
});
document.addEventListener('click',e=>document.querySelectorAll('.row-menu[open],.app-menu[open]').forEach(d=>{if(!d.contains(e.target))d.open=false;}));
window.addEventListener('beforeunload',e=>{if(document.getElementById('note-editor')?.dataset.dirty||document.getElementById('task-detail-form')?.dataset.dirty){e.preventDefault();e.returnValue='';}});
document.addEventListener('DOMContentLoaded',()=>{restoreSections();routeLocation();});

document.body.addEventListener('htmx:responseError',e=>{if(e.detail.target?.id!=='detail-pane')return;const pane=document.getElementById('detail-pane');if(e.detail.xhr.status===409){let data;try{data=JSON.parse(e.detail.xhr.responseText);}catch(_){return;}const form=pane.querySelector('#task-detail-form');if(!form||!data.task)return;form.dataset.dirty='1';form.elements.revision.value=data.task.revision;let conflict=form.querySelector('.edit-conflict');if(!conflict){conflict=document.createElement('section');conflict.className='edit-conflict';conflict.setAttribute('role','alert');form.appendChild(conflict);}conflict.replaceChildren();const title=document.createElement('h3');title.textContent='Changed on another device';const latest=document.createElement('p');latest.textContent='Latest task: '+data.task.text;const keep=taskAction('Keep mine',()=>{conflict.remove();form.requestSubmit();});const use=taskAction('Use latest',()=>{delete form.dataset.dirty;showDetail('/task/detail?id='+form.elements.id.value);});conflict.append(title,latest,keep,use);keep.focus();return;}if(!pane.querySelector('form')){pane.textContent='This item is unavailable. It may have been archived.';const close=document.createElement('button');close.textContent='Close details';close.className='btn';close.onclick=closeDetail;pane.appendChild(close);}});
