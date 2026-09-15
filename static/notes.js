// --- Notes ---
let saveTimer = null;

function initNoteEditor() {
    const editor = document.getElementById('note-editor');
    if (!editor || editor.dataset.initialized) return;
    editor.dataset.initialized="1";
    void restoreNoteDraft(editor);

    editor.addEventListener('keydown', function(e) {
        handleNoteEditorKeydown(e, editor);
    });

    editor.addEventListener('input', function() {
        applyInlineNoteCommands(editor);
        clearTimeout(saveTimer);
        editor.dataset.dirty='1';
        clearTimeout(noteDraftTimer);
        noteDraftTimer=setTimeout(()=>{void storeNoteDraft(editor);},250);
        showNoteStatus('Unsaved…');
        saveTimer = setTimeout(() => saveCurrentNote(), 800);
    });

    // Click/tap within the checkbox marker area to toggle
    editor.addEventListener('click', function() {
        const pos = editor.selectionStart;
        const line = getCurrentLine(editor.value, pos);
        if (pos - line.start <= 6) toggleCheckboxLine(editor);
    });

    autoResize(editor);
    editor.addEventListener('input', () => autoResize(editor));
}

function toggleCheckboxLine(editor) {
    const pos = editor.selectionStart;
    const line = getCurrentLine(editor.value, pos);
    let newLineText = null;
    if (/^(\s*)- \[ \] /.test(line.text)) {
        newLineText = line.text.replace('- [ ] ', '- [x] ');
    } else if (/^(\s*)- \[x\] /i.test(line.text)) {
        newLineText = line.text.replace(/- \[x\] /i, '- [ ] ');
    }
    if (!newLineText) return;
    editor.value = editor.value.slice(0, line.start) + newLineText + editor.value.slice(line.end);
    editor.setSelectionRange(pos, pos);
    editor.dispatchEvent(new Event('input', { bubbles: true }));
}

function handleNoteEditorKeydown(e, editor) {
    const isMod = e.metaKey || e.ctrlKey;
    if (isMod && !e.shiftKey && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault();
        toggleWrappedSelection(editor, '*');
        return;
    }
    if (isMod && !e.shiftKey && (e.key === 'i' || e.key === 'I')) {
        e.preventDefault();
        toggleWrappedSelection(editor, '_');
        return;
    }
    if (isMod && e.key === 'Enter') {
        e.preventDefault();
        toggleCheckboxLine(editor);
        return;
    }

    if (e.key !== 'Enter' || e.shiftKey || e.altKey || e.ctrlKey || e.metaKey) {
        return;
    }

    const selectionStart = editor.selectionStart;
    const selectionEnd = editor.selectionEnd;
    if (selectionStart !== selectionEnd) return;

    const line = getCurrentLine(editor.value, selectionStart);
    const numbered = line.text.match(/^(\s*)(\d+)\.\s+(.*)$/);
    if (numbered) {
        e.preventDefault();
        const next = Number(numbered[2]) + 1;
        const content = numbered[3].trim();
        const insertion = content ? `\n${numbered[1]}${next}. ` : '\n';
        editor.setRangeText(insertion, selectionStart, selectionEnd, 'end');
        editor.dispatchEvent(new Event('input', { bubbles: true }));
        return;
    }

    const checkbox = line.text.match(/^(\s*)-\s\[(?: |x|X)\]\s+(.*)$/);
    if (checkbox) {
        e.preventDefault();
        const content = checkbox[2].trim();
        const insertion = content ? `\n${checkbox[1]}- [ ] ` : '\n';
        editor.setRangeText(insertion, selectionStart, selectionEnd, 'end');
        editor.dispatchEvent(new Event('input', { bubbles: true }));
    }
}

function applyInlineNoteCommands(editor) {
    const selectionStart = editor.selectionStart;
    const selectionEnd = editor.selectionEnd;
    if (selectionStart !== selectionEnd) return;

    const line = getCurrentLine(editor.value, selectionStart);
    const beforeCaret = line.text.slice(0, selectionStart - line.start);
    const indent = beforeCaret.match(/^\s*/)[0];
    const replaceFrom = line.start + indent.length;

    if (/^\s*\[(?:\s)?\]\s$/.test(beforeCaret)) {
        editor.setRangeText('- [ ] ', replaceFrom, selectionStart, 'end');
        return;
    }

    if (/^\s*\[(?:x|X)\]\s$/.test(beforeCaret)) {
        editor.setRangeText('- [x] ', replaceFrom, selectionStart, 'end');
        return;
    }

    const numbered = beforeCaret.match(/^(\s*)(\d+)\)\s$/);
    if (numbered) {
        const replacement = `${numbered[1]}${numbered[2]}. `;
        editor.setRangeText(replacement, line.start, selectionStart, 'end');
    }
}

function toggleWrappedSelection(editor, marker) {
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    if (start === end) return;

    const selected = editor.value.slice(start, end);
    const wrapped = selected.startsWith(marker) && selected.endsWith(marker) && selected.length >= marker.length * 2;

    if (wrapped) {
        const unwrapped = selected.slice(marker.length, selected.length - marker.length);
        editor.setRangeText(unwrapped, start, end, 'select');
    } else {
        editor.setRangeText(`${marker}${selected}${marker}`, start, end, 'select');
    }

    editor.dispatchEvent(new Event('input', { bubbles: true }));
}

function getCurrentLine(text, position) {
    const start = text.lastIndexOf('\n', position - 1) + 1;
    const lineEndIndex = text.indexOf('\n', position);
    const end = lineEndIndex === -1 ? text.length : lineEndIndex;
    return {
        start,
        end,
        text: text.slice(start, end)
    };
}

function autoResize(el) {
    el.style.height = 'auto';
    el.style.height = Math.max(el.scrollHeight, window.innerHeight - 160) + 'px';
}

let noteSavePending = null;
let noteConflict = null;
let noteDraftTimer = null;
const NOTE_KEEPALIVE_LIMIT = 60000;
function noteDraftDB(){return new Promise((resolve,reject)=>{const request=indexedDB.open('secondbrain-drafts',1);request.onupgradeneeded=()=>request.result.createObjectStore('notes');request.onsuccess=()=>resolve(request.result);request.onerror=()=>reject(request.error);});}
async function noteDraftWrite(id,value){const db=await noteDraftDB();return new Promise((resolve,reject)=>{const tx=db.transaction('notes','readwrite');tx.objectStore('notes').put(value,String(id));tx.oncomplete=()=>{db.close();resolve();};tx.onerror=()=>{db.close();reject(tx.error);};});}
async function noteDraftRead(id){const db=await noteDraftDB();return new Promise((resolve,reject)=>{const tx=db.transaction('notes','readonly');const request=tx.objectStore('notes').get(String(id));request.onsuccess=()=>resolve(request.result);request.onerror=()=>reject(request.error);tx.oncomplete=()=>db.close();});}
async function noteDraftDelete(id){const db=await noteDraftDB();return new Promise((resolve,reject)=>{const tx=db.transaction('notes','readwrite');tx.objectStore('notes').delete(String(id));tx.oncomplete=()=>{db.close();resolve();};tx.onerror=()=>{db.close();reject(tx.error);};});}
function storeNoteDraft(editor) {
    return noteDraftWrite(editor.dataset.noteId,{content:editor.value,revision:Number(editor.dataset.revision),savedAt:Date.now()}).catch(()=>{});
}
async function restoreNoteDraft(editor) {
    const noteId=editor.dataset.noteId;
    const initialContent=editor.value;
    try { const draft=await noteDraftRead(noteId);if(!draft||!editor.isConnected||editor.dataset.noteId!==noteId||editor.dataset.dirty||editor.value!==initialContent)return;if(draft.content!==editor.value){editor.value=draft.content;editor.dataset.revision=String(draft.revision||editor.dataset.revision);editor.dataset.dirty='1';autoResize(editor);showNoteStatus('Unsaved draft restored.');}else await noteDraftDelete(noteId); } catch (_) {}
}
function clearNoteConflict(){document.getElementById('note-conflict')?.remove();noteConflict=null;}
function showNoteConflict(editor,latest){
 noteConflict=latest;let panel=document.getElementById('note-conflict');if(!panel){panel=document.createElement('section');panel.id='note-conflict';panel.className='edit-conflict note-conflict';panel.setAttribute('role','alertdialog');panel.setAttribute('aria-labelledby','note-conflict-title');editor.insertAdjacentElement('afterend',panel);}
 panel.replaceChildren();const heading=document.createElement('h3');heading.id='note-conflict-title';heading.textContent='Changed on another device';const copy=document.createElement('p');copy.textContent='Your draft is still in the editor. The latest saved version is below.';const latestBox=document.createElement('textarea');latestBox.readOnly=true;latestBox.value=latest.content;latestBox.setAttribute('aria-label','Latest saved note');const actions=document.createElement('div');actions.className='conflict-actions';const keep=document.createElement('button');keep.type='button';keep.className='btn btn-primary';keep.textContent='Keep mine';keep.onclick=()=>{editor.dataset.revision=String(latest.revision);clearNoteConflict();void saveCurrentNote(false,true);};const use=document.createElement('button');use.type='button';use.className='btn';use.textContent='Use latest';use.onclick=async()=>{editor.value=latest.content;editor.dataset.revision=String(latest.revision);delete editor.dataset.dirty;await noteDraftDelete(editor.dataset.noteId);clearNoteConflict();autoResize(editor);showNoteStatus('Latest version loaded');editor.focus();};actions.append(keep,use);panel.append(heading,copy,latestBox,actions);keep.focus();
}
async function saveCurrentNote(finalFlush=false, force=false) {
    clearTimeout(saveTimer); saveTimer=null;
    if(noteSavePending) {
        if(!await noteSavePending) return false;
        return saveCurrentNote(finalFlush,force);
    }
    const editor=document.getElementById('note-editor');
    if(!editor || !editor.dataset.dirty) return true;
    const content=editor.value, revision=Number(editor.dataset.revision);
    if(finalFlush) void storeNoteDraft(editor); else await storeNoteDraft(editor);
    noteSavePending=(async()=>{
        try {
            const body=new URLSearchParams({id:editor.dataset.noteId,content,revision:String(revision)});
            if(finalFlush && body.toString().length>NOTE_KEEPALIVE_LIMIT){showNoteStatus('Draft kept for next time');return false;}
            const response=await fetch('/notes/save',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},keepalive:finalFlush,body});
            if(response.redirected) throw new Error('Session expired — sign in to save your draft.');
            const data=await response.json();
            if(response.status===409&&data.note){showNoteStatus('Changed on another device');showNoteConflict(editor,data.note);return false;}
            if(!response.ok||data.status!=='saved') throw new Error('Could not save — your draft is kept here.');
            editor.dataset.revision=String(data.revision);clearNoteConflict();
            if(editor.value===content){delete editor.dataset.dirty;await noteDraftDelete(editor.dataset.noteId);showNoteStatus('Saved');}else{await storeNoteDraft(editor);}
            return true;
        } catch(error){showNoteStatus(error.message || 'Offline — your draft is kept here.');return false;}
    })();
    const ok=await noteSavePending;noteSavePending=null;return ok;
}

function showNoteStatus(msg) {
    const el = document.getElementById('note-status');
    if (el) el.textContent = msg;
}

async function saveBeforeNoteAction() {
    const saved = await saveCurrentNote();
    if (!saved || document.getElementById('note-editor')?.dataset.dirty) {
        showToast('Save or copy your current draft before changing notes.');
        return false;
    }
    return true;
}

// If the tab is being hidden or closed with an edit still in the debounce
// window, save it now (keepalive on the fetch lets it finish during unload).
function flushPendingNoteSave() {
    const editor=document.getElementById('note-editor');if(!editor?.dataset.dirty)return;
    clearTimeout(saveTimer);
    clearTimeout(noteDraftTimer);
    saveTimer = null;
    void storeNoteDraft(editor);void saveCurrentNote(true);
}
document.addEventListener('visibilitychange', function() {
    if (document.hidden) flushPendingNoteSave();
});
window.addEventListener('pagehide', flushPendingNoteSave);

function toggleNotePicker() {
    const panel = document.getElementById('note-picker-panel');
    const search = document.getElementById('note-picker-search');
    if (!panel) return;
    const opening = panel.hidden;
    panel.hidden = !opening;
    if (opening && search) {
        search.value = '';
        filterNotes('');
        search.focus();
    }
}

function filterNotes(query) {
    const q = query.toLowerCase().trim();
    document.querySelectorAll('.note-picker-item').forEach(item => {
        item.hidden = q !== '' && !item.dataset.title.toLowerCase().includes(q);
    });
}

async function selectNote(id) {
    if(!await saveBeforeNoteAction()) return;
    const panel=document.getElementById('note-picker-panel');if(panel)panel.hidden=true;
    htmx.ajax('GET','/notes?id='+id,'#notes-content');
}

document.addEventListener('click', function(e) {
    const picker = document.getElementById('note-picker');
    if (picker && !picker.contains(e.target)) {
        const panel = document.getElementById('note-picker-panel');
        if (panel) panel.hidden = true;
    }
});


function loadNote(id) {
    htmx.ajax('GET', '/notes?id=' + id, '#notes-content');
}

function showPrompt(label, defaultValue = '') {
    return new Promise(resolve => {
        const dialog = document.getElementById('custom-dialog');
        const labelEl = document.getElementById('custom-dialog-label');
        const input = document.getElementById('custom-dialog-input');
        const confirmBtn = document.getElementById('custom-dialog-confirm');
        const cancelBtn = document.getElementById('custom-dialog-cancel');

        labelEl.textContent = label;
        input.value = defaultValue;
        openModal(dialog, input);
        setTimeout(() => input.select(), 0);

        function submit() {
            const val = input.value.trim();
            cleanup();
            resolve(val || null);
        }
        function dismiss() { cleanup(); resolve(null); }
        function cleanup() {
            closeModal(dialog);
            confirmBtn.removeEventListener('click', submit);
            cancelBtn.removeEventListener('click', dismiss);
            input.removeEventListener('keydown', onKey);
            dialog.removeEventListener('click', onBackdrop);
        }
        function onKey(e) {
            if (e.key === 'Enter') { e.preventDefault(); submit(); }
            if (e.key === 'Escape') dismiss();
        }
        function onBackdrop(e) { if (e.target === dialog) dismiss(); }

        confirmBtn.addEventListener('click', submit);
        cancelBtn.addEventListener('click', dismiss);
        input.addEventListener('keydown', onKey);
        dialog.addEventListener('click', onBackdrop);
    });
}

async function createNote() {
    const title = await showPrompt('Note name');
    if (!title) return;
    if (!await saveBeforeNoteAction()) return;
    htmx.ajax('POST', '/notes/create', {
        target: '#notes-content',
        values: { title }
    });
}

function showConfirm(message) {
    return new Promise(resolve => {
        const dialog = document.getElementById('custom-dialog');
        const labelEl = document.getElementById('custom-dialog-label');
        const input = document.getElementById('custom-dialog-input');
        const confirmBtn = document.getElementById('custom-dialog-confirm');
        const cancelBtn = document.getElementById('custom-dialog-cancel');

        labelEl.textContent = message;
        input.hidden = true;
        openModal(dialog, confirmBtn);

        function finish(result) {
            closeModal(dialog);
            input.hidden = false;
            confirmBtn.removeEventListener('click', onConfirm);
            cancelBtn.removeEventListener('click', onCancel);
            document.removeEventListener('keydown', onKey);
            dialog.removeEventListener('click', onBackdrop);
            resolve(result);
        }
        function onConfirm() { finish(true); }
        function onCancel() { finish(false); }
        function onKey(e) {
            if (e.key === 'Enter') { e.preventDefault(); finish(true); }
            if (e.key === 'Escape') finish(false);
        }
        function onBackdrop(e) { if (e.target === dialog) finish(false); }

        confirmBtn.addEventListener('click', onConfirm);
        cancelBtn.addEventListener('click', onCancel);
        document.addEventListener('keydown', onKey);
        dialog.addEventListener('click', onBackdrop);
    });
}

async function deleteNote() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;
    const ok = await showConfirm('Archive this note?');
    if (!ok) return;
    if (!await saveBeforeNoteAction()) return;
    htmx.ajax('POST', '/notes/delete', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId }
    });
}

async function renameNote() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;
    const label = document.querySelector('.note-picker-label');
    const current = label ? label.textContent.trim() : '';
    const title = await showPrompt('Rename note', current);
    if (!title || title === current) return;
    if (!await saveBeforeNoteAction()) return;
    htmx.ajax('POST', '/notes/rename', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId, title }
    });
}
