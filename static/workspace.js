// State is limited to presentation and unsaved drafts; task truth stays on the server.
let detailSelection=null;
let pendingFocus=null;
let workspaceState=null;
let noteViewState=null;
let archiveCategory='todo';
function readLocal(key){try{return localStorage.getItem(key);}catch(_){return null;}}
function writeLocal(key,value){try{localStorage.setItem(key,value);}catch(_){}}
function restoreSections(){
 document.querySelectorAll('details[data-persist]').forEach(d=>{const saved=readLocal('section-'+d.dataset.persist);if(saved!==null)d.open=saved==='1';});
}
document.addEventListener('toggle',e=>{const d=e.target;if(d.matches?.('details[data-persist]'))writeLocal('section-'+d.dataset.persist,d.open?'1':'0');},true);
function rememberWorkspace(){
 const forms={};document.querySelectorAll('#todo-items .capture-form').forEach(f=>{forms[f.id]=Object.fromEntries(new FormData(f));});
 const el=document.activeElement;
 workspaceState={forms,scroll:window.scrollY,focus:el?.closest('.capture-form')?.id,name:el?.name,start:el?.selectionStart,end:el?.selectionEnd};
}
function restoreWorkspace(){
 restoreSections();
 if(workspaceState){const s=workspaceState;for(const [id,values] of Object.entries(s.forms)){const form=document.getElementById(id);if(form){for(const [key,value]of Object.entries(values)){const el=form.elements.namedItem(key);if(el)el.value=value;}}}
 const form=document.getElementById(s.focus);const el=form?.elements.namedItem(s.name);if(el){el.focus({preventScroll:true});if(typeof s.start==='number')el.setSelectionRange?.(s.start,s.end);}
 requestAnimationFrame(()=>window.scrollTo(0,s.scroll));workspaceState=null;
 }
 if(pendingFocus){revealTask(pendingFocus);pendingFocus=null;}
}
function refreshWorkspace(){return htmx.ajax('GET','/workspace','#todo-items');}
function expandAncestors(el){let node=el;while(node){if(node.tagName==='DETAILS')node.open=true;node=node.parentElement;}}
function revealTask(id){const row=document.getElementById('task-'+id);if(row){document.querySelectorAll('.todo-item.selected').forEach(el=>el.classList.remove('selected'));expandAncestors(row);row.scrollIntoView({block:'nearest'});row.classList.add('selected');}}
function setDestination(values){const url=new URL(location.href);url.search='';Object.entries(values).forEach(([k,v])=>url.searchParams.set(k,v));history.replaceState(null,'',url);}
function canLeaveDetail(){const form=document.getElementById('task-detail-form');if(form?.dataset.dirty){showToast('Save your task changes before closing these details.');return false;}return true;}
function showDetail(url){const pane=document.getElementById('detail-pane');if(pane.dataset.url!==url){pane.textContent='Loading details…';pane.dataset.url=url;}pane.hidden=false;document.querySelector('.task-layout').classList.add('has-detail');return htmx.ajax('GET',url,'#detail-pane').catch(()=>{});}
function discardTaskChanges(){const form=document.getElementById('task-detail-form');if(form){delete form.dataset.dirty;showDetail('/task/detail?id='+form.elements.id.value);}}
async function openTask(id){if(!id||!canLeaveDetail())return;switchMode('todos');detailSelection={kind:'task',id};
 if(document.querySelector('#todo-items .archive-header')){pendingFocus=id;await refreshWorkspace();}
 revealTask(id);setDestination({view:'todos',task:id});showDetail('/task/detail?id='+id);
}
async function openProject(id){if(!canLeaveDetail())return;switchMode('todos');if(document.querySelector('#todo-items .archive-header'))await refreshWorkspace();detailSelection={kind:'project',id};const el=document.getElementById('project-'+id);if(el){expandAncestors(el);el.open=true;}setDestination({view:'todos',project:id});showDetail('/projects/detail?id='+id);}
function closeDetail(){if(!canLeaveDetail())return;document.getElementById('detail-pane').hidden=true;document.querySelector('.task-layout').classList.remove('has-detail');detailSelection=null;setDestination({view:'todos'});}
function filterStageOptions(form){if(!form?.elements.project_id)return;const project=form.elements.project_id.value;const select=form.elements.stage_id;for(const option of select.options){option.hidden=!!option.dataset.project&&option.dataset.project!==project;option.disabled=option.hidden;}if(select.selectedOptions[0]?.disabled)select.value='0';}
async function structureCreate(kind,parent){const name=await showPrompt(kind==='project'?'New project':'New stage');if(name)structureAction(kind,0,'create',{name,project_id:parent});}
async function structureRename(kind,id,el){const name=await showPrompt('Rename '+kind,el.dataset.name);if(name)structureAction(kind,id,'rename',{name});}
async function structureAction(kind,id,action,extra={}){await htmx.ajax('POST',kind==='project'?'/projects/action':'/stages/action',{target:'#todo-items',values:{id,action,...extra}});}
function openTaskArchive(category){if(!canLeaveDetail())return;closeDetail();archiveCategory=category;htmx.ajax('GET','/todos/archive?category='+category,'#todo-items');}
function hideArchive(){refreshWorkspace();}
function openSettings(){switchMode('settings');setDestination({view:'settings'});htmx.ajax('GET','/settings','#settings-content');}
function openReview(id){switchMode('review');setDestination(id?{view:'review',review:id}:{view:'review'});htmx.ajax('GET','/review'+(id?'?id='+id:''),'#review-content');}
function routeLocation(){const q=new URLSearchParams(location.search);if(q.get('task'))openTask(Number(q.get('task')));else if(q.get('project'))openProject(Number(q.get('project')));else if(q.get('view')==='review')openReview(q.get('review'));else if(q.get('view')==='settings')openSettings();else if(q.get('note'))openNoteResult(Number(q.get('note')));else if(q.get('stage'))openStageResult(Number(q.get('stage')));else if(q.get('view'))switchMode(q.get('view'));}
async function openStageResult(id){closeSearch();switchMode('todos');if(document.querySelector('#todo-items .archive-header'))await refreshWorkspace();setDestination({view:'todos',stage:id});const el=document.getElementById('stage-'+id);if(el){expandAncestors(el);el.open=true;el.scrollIntoView({block:'nearest'});}}
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
document.body.addEventListener('htmx:afterSwap',e=>{
 const id=e.detail.target.id;
 if(id==='todo-items'){restoreWorkspace();if(detailSelection?.kind==='task')document.getElementById('task-'+detailSelection.id)?.classList.add('selected');if(detailSelection?.kind==='project')showDetail('/projects/detail?id='+detailSelection.id);}
 if(id==='detail-pane'){filterStageOptions(document.getElementById('task-detail-form'));}
 if(id==='notes-content'){initNoteEditor();const editor=document.getElementById('note-editor');if(editor&&document.body.dataset.mode==='notes')setDestination({view:'notes',note:editor.dataset.noteId});if(editor&&noteViewState?.id===editor.dataset.noteId){editor.setSelectionRange(noteViewState.start,noteViewState.end);editor.scrollTop=noteViewState.scroll;if(noteViewState.focused)editor.focus({preventScroll:true});}noteViewState=null;}
 if(id==='settings-content'){document.body.dataset.timezone=document.querySelector('#schedule-form [name=timezone]')?.value || document.body.dataset.timezone;updateTopbarStat();preparePush();}
});
document.body.addEventListener('sbWorkspaceChanged',e=>{refreshWorkspace();if(e.detail.message)showToast(e.detail.message);});
document.addEventListener('input',e=>{const form=e.target.closest('#task-detail-form');if(form)form.dataset.dirty='1';});
document.addEventListener('keydown',e=>{
 if(e.metaKey&&e.key.toLowerCase()==='k'){e.preventDefault();openSearch();}
 if(e.metaKey&&e.key==='Enter'){const form=document.activeElement.closest('form');if(form){e.preventDefault();form.requestSubmit();}}
 if(e.key==='Escape'){closeSearch();document.querySelectorAll('.row-menu,.app-menu,.capture-options').forEach(d=>d.open=false);if(document.body.dataset.mode==='todos'&&!document.getElementById('detail-pane').hidden)closeDetail();}
});
document.addEventListener('click',e=>document.querySelectorAll('.row-menu[open],.app-menu[open]').forEach(d=>{if(!d.contains(e.target))d.open=false;}));
window.addEventListener('beforeunload',e=>{if(document.getElementById('note-editor')?.dataset.dirty||document.getElementById('task-detail-form')?.dataset.dirty){e.preventDefault();e.returnValue='';}});
document.addEventListener('DOMContentLoaded',()=>{restoreSections();routeLocation();});

document.body.addEventListener('htmx:responseError',e=>{if(e.detail.target?.id==='detail-pane'&&!document.getElementById('detail-pane').querySelector('form')){const pane=document.getElementById('detail-pane');pane.textContent='This item is unavailable. It may have been archived.';const close=document.createElement('button');close.textContent='Close details';close.className='btn';close.onclick=closeDetail;pane.appendChild(close);}});
