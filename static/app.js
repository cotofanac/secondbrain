// --- Mode switching (Tasks / Notes / Habits) ---
const MODE_TITLES = { todos: 'Tasks', notes: 'Notes', habits: 'Habits', settings: 'Notifications', review: 'Weekly review' };
function switchMode(mode) {
    if (!MODE_TITLES[mode]) mode='todos';
    flushPendingNoteSave();
    document.querySelectorAll('.bottom-nav-btn').forEach(b => b.classList.toggle('active',b.dataset.mode===mode));
    document.querySelectorAll('.view').forEach(v => v.classList.toggle('active',v.id===mode+'-view'));
    document.getElementById('topbar-title').textContent=MODE_TITLES[mode];
    document.querySelectorAll('.app-menu').forEach(d=>d.open=false);
    document.body.dataset.mode=mode;
    if(typeof setDestination==='function')setDestination({view:mode});
    updateTopbarStat();
}

function updateTopbarStat() {
    const stat = document.getElementById('topbar-stat');
    if (!stat) return;

    const now = new Date();
    const date = now.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric', timeZone: document.body.dataset.timezone || undefined });

    const activeMode = document.querySelector('.bottom-nav-btn.active')?.dataset.mode;
    let count = '';

    if (activeMode === 'todos') {
        const archiveOpen = document.getElementById('archive-btn')?.classList.contains('active');
        if (!archiveOpen) {
            const pending = document.querySelectorAll('#todo-items .todo-item:not(.done):not(.archived-item)').length;
            if (pending > 0) count = pending + ' left';
        }
    } else if (activeMode === 'habits') {
        // Only daily habits feed the "done/total" stat; periodic goals track
        // their own per-period progress.
        const daily = '#habits-content .habit-section[data-period="day"] .habit-item';
        const total = document.querySelectorAll(daily).length;
        const done = document.querySelectorAll(daily + '.done').length;
        if (total > 0) count = done + '/' + total;
    }

    stat.textContent = count ? date + ' · ' + count : date;
}

function updateHabitsBadge() {
    const badge = document.getElementById('habits-badge');
    if (!badge) return;
    // The nav dot reflects unfinished daily habits only, not periodic goals.
    const daily = '#habits-content .habit-section[data-period="day"] .habit-item';
    const items = document.querySelectorAll(daily);
    if (items.length === 0) { badge.hidden = true; return; }
    const undone = document.querySelectorAll(daily + ':not(.done)').length;
    if (undone > 0) {
        badge.textContent = '';
        badge.hidden = false;
    } else {
        badge.hidden = true;
    }
}

document.body.addEventListener('htmx:afterSwap', function(e) {
    const id = e.detail.target?.id;
    if (id === 'todo-items' || id === 'habits-content') updateTopbarStat();
    if (id === 'habits-content') updateHabitsBadge();
});

document.body.addEventListener('htmx:afterRequest', function(e) {
    if (!e.detail.successful) return;
    const form = e.detail.elt;
    if (!form || !form.closest('#add-form')) return;
    const textInput = form.querySelector('[name=text]');
    const dateInput = form.querySelector('[name=due_date]');
    if (textInput) { textInput.value = ''; textInput.focus(); }
    if (dateInput && dateInput.value) {
        dateInput.value = '';
        updateCalendarBtn(dateInput, document.getElementById('calendar-btn'));
    }
});

// Route hx-confirm through the app's custom dialog instead of window.confirm,
// so destructive actions (permanent delete) get a real confirmation step.
document.body.addEventListener('htmx:confirm', function(e) {
    if (!e.detail.question) return; // no hx-confirm on this element — proceed
    e.preventDefault();
    showConfirm(e.detail.question).then(function(ok) {
        if (ok) e.detail.issueRequest(true);
    });
});

// Surface otherwise-silent HTMX failures (server error or no connection).
// htmx never swaps a non-2xx response, so without this the specific reason a
// request was rejected ("Habit already exists", "Cannot delete the last note")
// would be discarded. 4xx bodies are our own http.Error text and are meant for
// the user; 5xx bodies are not, so those keep the generic message.
document.body.addEventListener('htmx:responseError', function(e) {
    const xhr = e.detail.xhr;
    // An expired session responds 401 + HX-Redirect; we're already navigating
    // to /login, so a toast would just be noise.
    if (xhr && xhr.getResponseHeader('HX-Redirect')) return;
    let msg = '';
    if (xhr && xhr.status >= 400 && xhr.status < 500) {
        msg = (xhr.responseText || '').trim();
        // Guard against an unexpectedly long or HTML body reaching the toast.
        if (msg.length > 120 || msg.indexOf('<') === 0) msg = '';
    }
    showToast(msg || 'Something went wrong — try again');
});
document.body.addEventListener('htmx:sendError', function() {
    showToast('You appear to be offline');
});

let toastTimer;
const TOAST_MS = 3000;
// An actionable toast stays up longer: it is only useful while it is on screen.
const TOAST_ACTION_MS = 7000;

// action is optional: { label, onClick }. The content is assembled as DOM nodes
// rather than markup so a message can never be interpreted as HTML.
function showToast(msg, action) {
    const el = document.getElementById('toast');
    if (!el) return;

    el.textContent = '';
    const label = document.createElement('span');
    label.textContent = msg;
    el.appendChild(label);

    const actionable = !!(action && action.label && typeof action.onClick === 'function');
    if (actionable) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'toast-action';
        btn.textContent = action.label;
        btn.addEventListener('click', function() {
            hideToast();
            action.onClick();
        });
        el.appendChild(btn);
    }
    // The toast is pointer-transparent by default so it never blocks the list
    // underneath; it only becomes clickable when it actually offers an action.
    el.classList.toggle('has-action', actionable);

    el.hidden = false;
    // Reflow so re-triggering the animation works on a visible element.
    void el.offsetWidth;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(hideToast, actionable ? TOAST_ACTION_MS : TOAST_MS);
}

function hideToast() {
    const el = document.getElementById('toast');
    if (!el || el.hidden) return;
    clearTimeout(toastTimer);
    el.classList.remove('show');
    setTimeout(() => { el.hidden = true; }, 300);
}

// --- Undo for one-tap archiving ---
// The server answers an archive with an sbUndo trigger; the toast turns that
// into a reversal the user can take without hunting through the archive view.
document.body.addEventListener('sbUndo', function(e) {
    const d = e.detail || {};
    if (!d.id) return;
    const isHabit = d.kind === 'habit';
    showToast(isHabit ? 'Habit archived' : d.kind === 'project' ? 'Project archived' : d.kind === 'stage' ? 'Stage archived' : 'Task archived', {
        label: 'Undo',
        onClick: function() { undoArchive(d.kind, d.id); }
    });
});

// Plain confirmations (e.g. a name collision resolved by reviving an archived item).
document.body.addEventListener('sbNotice', function(e) {
    const d = e.detail || {};
    if (d.message) showToast(d.message);
});

function undoArchive(kind, id) {
    // return=list asks the restore handler for the live list rather than the
    // archive view it would normally re-render.
    if (kind === 'project' || kind === 'stage') { structureAction(kind,id,'restore'); return; }
    if (kind === 'habit') {
        htmx.ajax('POST', '/habits/restore', {
            target: '#habits-content',
            swap: 'innerHTML',
            values: { id: id, return: 'list' }
        });
    } else {
        htmx.ajax('POST', '/todos/restore', {
            target: '#todo-items',
            swap: 'innerHTML',
            values: { id: id, return: 'list' }
        });
    }
}

// /logout is POST-only, so submit a form rather than navigating (a GET).
function doLogout() {
    const f = document.createElement('form');
    f.method = 'POST';
    f.action = '/logout';
    document.body.appendChild(f);
    f.submit();
}

// --- Notes ---
let saveTimer = null;

function initNoteEditor() {
    const editor = document.getElementById('note-editor');
    if (!editor || editor.dataset.initialized) return;
    editor.dataset.initialized="1";
    restoreNoteDraft(editor);

    editor.addEventListener('keydown', function(e) {
        handleNoteEditorKeydown(e, editor);
    });

    editor.addEventListener('input', function() {
        applyInlineNoteCommands(editor);
        clearTimeout(saveTimer);
        editor.dataset.dirty='1';
        storeNoteDraft(editor);
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
function storeNoteDraft(editor) {
    try { sessionStorage.setItem('note-draft-'+editor.dataset.noteId,JSON.stringify({content:editor.value,updatedAt:editor.dataset.updatedAt})); } catch (_) {}
}
function restoreNoteDraft(editor) {
    try { const raw=sessionStorage.getItem('note-draft-'+editor.dataset.noteId); if(raw){const draft=JSON.parse(raw);if(draft.content!==editor.value){editor.value=draft.content;editor.dataset.updatedAt=draft.updatedAt;editor.dataset.dirty='1';showNoteStatus('Unsaved draft restored. Edit to retry saving.');}else sessionStorage.removeItem('note-draft-'+editor.dataset.noteId);} } catch (_) {}
}
async function saveCurrentNote() {
    clearTimeout(saveTimer); saveTimer=null;
    if(noteSavePending) {
        if(!await noteSavePending) return false;
        return saveCurrentNote();
    }
    const editor=document.getElementById('note-editor');
    if(!editor || !editor.dataset.dirty) return true;
    const content=editor.value, updatedAt=editor.dataset.updatedAt || '';
    storeNoteDraft(editor);
    noteSavePending=(async()=>{
        try {
            const response=await fetch('/notes/save',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},keepalive:true,body:new URLSearchParams({id:editor.dataset.noteId,content,updated_at:updatedAt})});
            if(response.redirected) throw new Error('Session expired — sign in to save your draft.');
            const data=await response.json();
            if(data.status==='conflict'){showNoteStatus('Edited on another device. Your draft is kept here; copy it before reloading the latest version.');return false;}
            if(!response.ok||data.status!=='saved') throw new Error('Could not save — your draft is kept here.');
            editor.dataset.updatedAt=data.updated_at || updatedAt;
            if(editor.value===content){delete editor.dataset.dirty;try{sessionStorage.removeItem('note-draft-'+editor.dataset.noteId);}catch(_){}showNoteStatus('Saved');}else{storeNoteDraft(editor);}
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
    if (!saveTimer) return;
    clearTimeout(saveTimer);
    saveTimer = null;
    saveCurrentNote();
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
        dialog.hidden = false;
        setTimeout(() => { input.focus(); input.select(); }, 50);

        function submit() {
            const val = input.value.trim();
            cleanup();
            resolve(val || null);
        }
        function dismiss() { cleanup(); resolve(null); }
        function cleanup() {
            dialog.hidden = true;
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
        dialog.hidden = false;
        setTimeout(() => confirmBtn.focus(), 50);

        function finish(result) {
            dialog.hidden = true;
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

// --- Calendar button for todo due date ---
const CALENDAR_ICON = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="18" rx="2" ry="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/></svg>`;

function openDatePicker(dateInput) {
    if (!dateInput) return;
    if (typeof dateInput.showPicker === 'function') {
        try {
            dateInput.showPicker();
            return;
        } catch (e) { /* fall through to focus-based fallback */ }
    }
    dateInput.focus();
    dateInput.click();
}

function updateCalendarBtn(dateInput, calendarBtn) {
    if (!calendarBtn) return;
    const val = dateInput ? dateInput.value : '';
    if (val) {
        const d = new Date(val + 'T12:00:00');
        const label = d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
        calendarBtn.innerHTML = `<span class="calendar-btn-label">${label}</span>`;
        calendarBtn.classList.add('has-date');
        calendarBtn.title = 'Clear due date';
        calendarBtn.onclick = function() {
            if (dateInput) dateInput.value = '';
            updateCalendarBtn(dateInput, calendarBtn);
        };
    } else {
        calendarBtn.innerHTML = CALENDAR_ICON;
        calendarBtn.classList.remove('has-date');
        calendarBtn.title = 'Due date';
        calendarBtn.onclick = function() {
            openDatePicker(dateInput);
        };
    }
}

function initCalendarBtn() {
    const dateInput = document.getElementById('add-date');
    const calendarBtn = document.getElementById('calendar-btn');
    if (!dateInput || !calendarBtn) return;

    updateCalendarBtn(dateInput, calendarBtn);

    dateInput.addEventListener('change', function() {
        updateCalendarBtn(dateInput, calendarBtn);
    });

    const addRow = document.querySelector('.add-row');
    if (addRow) {
        addRow.addEventListener('htmx:afterRequest', function() {
            setTimeout(function() { updateCalendarBtn(dateInput, calendarBtn); }, 0);
        });
    }
}

// --- Archive ---
function clearChecked() {
    const category = document.getElementById('add-category').value;
    htmx.ajax('POST', '/todos/clear-checked', {
        target: '#todo-items',
        swap: 'innerHTML',
        values: { category }
    });
}

function showArchive() {
    hideToast(); // an undo would render the live list back over the archive
    const cat = document.getElementById('add-category').value;
    htmx.ajax('GET', '/todos/archive?category=' + cat, '#todo-items');
    document.getElementById('add-form').style.display = 'none';
    document.getElementById('archive-btn').classList.add('active');
    const clearBtn = document.getElementById('clear-checked-btn');
    if (clearBtn) {
        clearBtn.style.visibility = 'hidden';
        clearBtn.style.pointerEvents = 'none';
    }
}

function hideArchive() {
    const cat = document.getElementById('add-category').value;
    htmx.ajax('GET', '/todos?category=' + cat, '#todo-items');
    document.getElementById('add-form').style.display = '';
    document.getElementById('archive-btn').classList.remove('active');
}

async function showNotesArchive() {
    if (!await saveBeforeNoteAction()) return;
    htmx.ajax('GET', '/notes/archive', '#notes-content');
}

function hideNotesArchive() {
    htmx.ajax('GET', '/notes', '#notes-content');
}

// Habit creation dialog: name plus an optional period + target for goals.
// Reuses the shared #custom-dialog sheet, unhiding the extra fields and
// restoring their hidden state on cleanup so plain showPrompt() stays pristine.
function showHabitDialog() {
    return new Promise(resolve => {
        const dialog = document.getElementById('custom-dialog');
        const labelEl = document.getElementById('custom-dialog-label');
        const input = document.getElementById('custom-dialog-input');
        const extra = document.getElementById('custom-dialog-extra');
        const periodGroup = document.getElementById('custom-dialog-period');
        const chips = periodGroup.querySelectorAll('.chip');
        const targetInput = document.getElementById('custom-dialog-target');
        const confirmBtn = document.getElementById('custom-dialog-confirm');
        const cancelBtn = document.getElementById('custom-dialog-cancel');

        function selectPeriod(p) {
            chips.forEach(c => c.classList.toggle('active', c.dataset.period === p));
            targetInput.hidden = p === 'day'; // target only applies to periodic goals
        }
        function currentPeriod() {
            const active = periodGroup.querySelector('.chip.active');
            return active ? active.dataset.period : 'day';
        }

        labelEl.textContent = 'New habit';
        input.value = '';
        targetInput.value = '1';
        selectPeriod('day');
        extra.hidden = false;
        dialog.hidden = false;
        setTimeout(() => { input.focus(); input.select(); }, 50);

        function onChipClick(e) {
            const chip = e.target.closest('.chip');
            if (chip) selectPeriod(chip.dataset.period);
        }
        function submit() {
            const name = input.value.trim();
            if (!name) { cleanup(); resolve(null); return; }
            const period = currentPeriod();
            let target = 1;
            if (period !== 'day') {
                target = parseInt(targetInput.value, 10);
                if (!Number.isFinite(target) || target < 1) target = 1;
            }
            cleanup();
            resolve({ name, period, target: String(target) });
        }
        function dismiss() { cleanup(); resolve(null); }
        function cleanup() {
            dialog.hidden = true;
            extra.hidden = true;
            selectPeriod('day');
            confirmBtn.removeEventListener('click', submit);
            cancelBtn.removeEventListener('click', dismiss);
            input.removeEventListener('keydown', onKey);
            periodGroup.removeEventListener('click', onChipClick);
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
        periodGroup.addEventListener('click', onChipClick);
        dialog.addEventListener('click', onBackdrop);
    });
}

async function addHabit() {
    const goal = await showHabitDialog();
    if (!goal) return;
    htmx.ajax('POST', '/habits/add', {
        target: '#habits-content',
        values: goal
    });
}

function showHabitsArchive() {
    hideToast(); // see showArchive
    htmx.ajax('GET', '/habits/archive', '#habits-content');
}

function hideHabitsArchive() {
    htmx.ajax('GET', '/habits', '#habits-content');
}

// --- Inactivity auto-logout ---
const INACTIVITY_WARNING_MS = 60 * 1000;
let inactivityWarnTimer = null;
let inactivityLogoutTimer = null;
let inactivityCountdownTimer = null;
let inactivityLastActivityTs = 0;

function setupInactivityLogout() {
    const body = document.body;
    if (!body) return;

    const timeoutSeconds = Number(body.dataset.inactivityTimeoutSeconds || 0);
    if (!Number.isFinite(timeoutSeconds) || timeoutSeconds <= 0) return;

    const timeoutMs = timeoutSeconds * 1000;
    bindInactivityActions(timeoutMs);
    startInactivityTimers(timeoutMs);
}

function bindInactivityActions(timeoutMs) {
    const stayBtn = document.getElementById('inactivity-stay-btn');
    const logoutBtn = document.getElementById('inactivity-logout-btn');
    const warning = document.getElementById('inactivity-warning');

    if (stayBtn) {
        stayBtn.addEventListener('click', function() {
            hideInactivityWarning();
            startInactivityTimers(timeoutMs);
        });
    }

    if (logoutBtn) {
        logoutBtn.addEventListener('click', function() {
            doLogout();
        });
    }

    if (warning) {
        warning.addEventListener('click', function(e) {
            if (e.target === warning) {
                hideInactivityWarning();
                startInactivityTimers(timeoutMs);
            }
        });
    }

    const activityEvents = ['pointerdown', 'touchstart', 'keydown', 'scroll'];
    activityEvents.forEach(function(eventName) {
        window.addEventListener(eventName, function() {
            const now = Date.now();
            if (now - inactivityLastActivityTs < 750) return;
            inactivityLastActivityTs = now;
            hideInactivityWarning();
            startInactivityTimers(timeoutMs);
        }, { passive: true });
    });

    document.addEventListener('visibilitychange', function() {
        if (!document.hidden) {
            hideInactivityWarning();
            startInactivityTimers(timeoutMs);
            refreshCurrentView();
        }
    });

    // Re-fetch current view data when restored from bfcache
    window.addEventListener('pageshow', function(e) {
        if (e.persisted) refreshCurrentView();
    });

    document.body.addEventListener('htmx:afterRequest', function() {
        hideInactivityWarning();
        startInactivityTimers(timeoutMs);
    });
}

function refreshCurrentView() {
    const mode=document.body.dataset.mode || 'todos';
    if(mode==='todos' && !document.querySelector('#todo-items .archive-header')) refreshWorkspace();
    if(mode==='notes') {
        const editor=document.getElementById('note-editor');
        if(editor && !editor.dataset.dirty && !noteSavePending) { preserveScroll(); loadNote(editor.dataset.noteId); }
    }
    if(mode==='habits') { preserveScroll(); htmx.ajax('GET','/habits','#habits-content'); }
    if(typeof preparePush==='function') preparePush();
}

// Restore the window scroll position after the next htmx swap settles. Swapping
// a list's innerHTML clamps the page back to the top, so refreshing the current
// view (e.g. when the tab is re-focused) would otherwise lose the user's place.
function preserveScroll() {
    const scrollY = window.scrollY;
    if (scrollY === 0) return;
    document.body.addEventListener('htmx:afterSettle', function restore() {
        window.scrollTo(0, scrollY);
    }, { once: true });
}

function startInactivityTimers(timeoutMs) {
    clearInactivityTimers();

    let warningLeadMs = Math.min(INACTIVITY_WARNING_MS, timeoutMs);
    if (timeoutMs <= INACTIVITY_WARNING_MS) {
        // For short sessions, show the warning in the second half, not immediately.
        warningLeadMs = Math.max(Math.floor(timeoutMs / 2), 5 * 1000);
        warningLeadMs = Math.min(warningLeadMs, Math.max(timeoutMs - 1000, 1000));
    }
    const warningDelayMs = Math.max(timeoutMs - warningLeadMs, 0);

    inactivityWarnTimer = setTimeout(function() {
        showInactivityWarning(Math.ceil(warningLeadMs / 1000));
    }, warningDelayMs);

    inactivityLogoutTimer = setTimeout(function() {
        doLogout();
    }, timeoutMs);
}

function clearInactivityTimers() {
    if (inactivityWarnTimer) clearTimeout(inactivityWarnTimer);
    if (inactivityLogoutTimer) clearTimeout(inactivityLogoutTimer);
    if (inactivityCountdownTimer) clearInterval(inactivityCountdownTimer);
    inactivityWarnTimer = null;
    inactivityLogoutTimer = null;
    inactivityCountdownTimer = null;
}

function showInactivityWarning(seconds) {
    const warning = document.getElementById('inactivity-warning');
    const countdown = document.getElementById('inactivity-countdown');
    if (!warning || !countdown) return;

    let remaining = seconds;
    warning.hidden = false;
    warning.setAttribute('aria-hidden', 'false');
    countdown.textContent = String(remaining);

    inactivityCountdownTimer = setInterval(function() {
        remaining -= 1;
        if (remaining <= 0) {
            clearInterval(inactivityCountdownTimer);
            inactivityCountdownTimer = null;
            countdown.textContent = '0';
            return;
        }
        countdown.textContent = String(remaining);
    }, 1000);
}

function hideInactivityWarning() {
    const warning = document.getElementById('inactivity-warning');
    if (!warning || warning.hidden) return;

    warning.hidden = true;
    warning.setAttribute('aria-hidden', 'true');

    if (inactivityCountdownTimer) {
        clearInterval(inactivityCountdownTimer);
        inactivityCountdownTimer = null;
    }
}

// --- Inline todo editing ---
// Returns the edit input so a touch caller can re-focus it from inside a real
// user gesture (see the long-press handler).
function startTodoEdit(spanEl) {
    const originalText = spanEl.textContent.trim();
    const item = spanEl.closest('.todo-item');
    if (!item) return null;
    const id = item.querySelector('input[name="id"]').value;

    const input = document.createElement('input');
    input.type = 'text';
    input.value = originalText;
    input.className = 'todo-edit-input';
    spanEl.replaceWith(input);
    input.focus();
    input.select();

    let done = false;
    function save() {
        if (done) return;
        done = true;
        const newText = input.value.trim();
        if (!newText || newText === originalText) { input.replaceWith(spanEl); return; }
        htmx.ajax('POST', '/todos/edit', {
            target: '#todo-items',
            swap: 'innerHTML',
            values: { id, text: newText }
        });
    }
    function cancel() {
        if (done) return;
        done = true;
        input.replaceWith(spanEl);
    }
    input.addEventListener('keydown', e => {
        if (e.key === 'Enter') { e.preventDefault(); save(); }
        if (e.key === 'Escape') cancel();
    });
    input.addEventListener('blur', save);
    return input;
}

function startHabitRename(spanEl) {
    const originalName = spanEl.textContent.trim();
    const item = spanEl.closest('.habit-item');
    if (!item) return null;
    const id = item.querySelector('input[name="id"]').value;

    const input = document.createElement('input');
    input.type = 'text';
    input.value = originalName;
    input.className = 'todo-edit-input';
    spanEl.replaceWith(input);
    input.focus();
    input.select();

    let done = false;
    function save() {
        if (done) return;
        done = true;
        const newName = input.value.trim();
        if (!newName || newName === originalName) { input.replaceWith(spanEl); return; }
        htmx.ajax('POST', '/habits/rename', {
            target: '#habits-content',
            swap: 'innerHTML',
            values: { id, name: newName }
        });
    }
    function cancel() {
        if (done) return;
        done = true;
        input.replaceWith(spanEl);
    }
    input.addEventListener('keydown', e => {
        if (e.key === 'Enter') { e.preventDefault(); save(); }
        if (e.key === 'Escape') cancel();
    });
    input.addEventListener('blur', save);
    return input;
}

// Inline edit is reachable two ways: double-click with a mouse, press-and-hold
// on touch. iOS has no usable dblclick — double-tap is the zoom gesture — so
// without the long press this feature was desktop-only on a phone-first app.
const LONG_PRESS_MS = 500;
const LONG_PRESS_SLOP_PX = 10;

// Resolves an event target to the editable label of a live (non-archived) row.
// Archived rows render the same .todo-text but have no edit endpoint: posting
// to /todos/edit for one 404s, since that query requires archived = 0.
function editableLabel(target) {
    if (!target || typeof target.closest !== 'function') return null;
    const label = target.closest('.todo-text, .habit-name');
    if (!label) return null;
    const row = label.closest('.todo-item, .habit-item');
    if (!row || row.classList.contains('archived-item')) return null;
    return label;
}

function beginInlineEdit(label) {
    return label.classList.contains('todo-text')
        ? startTodoEdit(label)
        : startHabitRename(label);
}

document.addEventListener('dblclick', function(e) {
    const label = editableLabel(e.target);
    if (label) beginInlineEdit(label);
});

(function() {
    let timer = null;
    let startX = 0, startY = 0;
    let pendingInput = null;
    let justFired = false;

    function cancelPress() {
        if (timer) { clearTimeout(timer); timer = null; }
    }

    document.addEventListener('pointerdown', function(e) {
        // Mouse keeps dblclick; a 500ms hold with a mouse is not an edit intent.
        if (e.pointerType === 'mouse' || !e.isPrimary) return;
        // Reset before the label check: if a previous long press never produced
        // the click we expected to swallow, a stale flag here would eat the
        // user's next unrelated tap.
        cancelPress();
        justFired = false;
        pendingInput = null;
        const label = editableLabel(e.target);
        if (!label) return;
        startX = e.clientX;
        startY = e.clientY;
        timer = setTimeout(function() {
            timer = null;
            justFired = true;
            // Swap now so the hold gives immediate visual feedback. focus() from
            // a timer doesn't open the iOS keyboard (not a user gesture), so the
            // input is re-focused on pointerup below, which is one.
            pendingInput = beginInlineEdit(label);
        }, LONG_PRESS_MS);
    }, { passive: true });

    document.addEventListener('pointermove', function(e) {
        if (!timer) return;
        if (Math.abs(e.clientX - startX) > LONG_PRESS_SLOP_PX ||
            Math.abs(e.clientY - startY) > LONG_PRESS_SLOP_PX) cancelPress();
    }, { passive: true });

    document.addEventListener('pointerup', function() {
        cancelPress();
        if (pendingInput) {
            pendingInput.focus();
            pendingInput.select();
            pendingInput = null;
        }
    }, { passive: true });

    document.addEventListener('pointercancel', function() {
        cancelPress();
        pendingInput = null;
    }, { passive: true });

    window.addEventListener('scroll', cancelPress, { passive: true });

    // Swallow the click synthesized at the end of a long press so it can't also
    // trigger whatever now sits under the finger.
    document.addEventListener('click', function(e) {
        if (!justFired) return;
        justFired = false;
        e.preventDefault();
        e.stopPropagation();
    }, true);

    // Suppress the iOS callout / context menu on labels we handle ourselves.
    document.addEventListener('contextmenu', function(e) {
        if (editableLabel(e.target)) e.preventDefault();
    });
})();

// --- Global search ---
function openSearch() {
    const overlay = document.getElementById('search-overlay');
    if (overlay) overlay.classList.add('visible');
    const input = document.getElementById('search-input');
    if (input) { input.value = ''; input.focus(); }
    const results = document.getElementById('search-results');
    if (results) results.innerHTML = '';
}

function closeSearch() {
    const overlay = document.getElementById('search-overlay');
    if (overlay) overlay.classList.remove('visible');
    const input = document.getElementById('search-input');
    if (input) input.value = '';
    const results = document.getElementById('search-results');
    if (results) results.innerHTML = '';
}

// "/" key on desktop
document.addEventListener('keydown', function(e) {
    const tag = document.activeElement.tagName;
    if (e.key === '/' && tag !== 'INPUT' && tag !== 'TEXTAREA') {
        e.preventDefault();
        openSearch();
    }
});

// Pull-down-from-top on mobile — only when touch STARTED at the very top
(function() {
    let touchStartY = 0;
    let triggered = false;
    let startedAtTop = false;
    document.addEventListener('touchstart', function(e) {
        touchStartY = e.touches[0].clientY;
        triggered = false;
        startedAtTop = window.scrollY === 0;
    }, { passive: true });
    document.addEventListener('touchmove', function(e) {
        if (triggered || !startedAtTop) return;
        if (e.touches[0].clientY - touchStartY > 60) {
            triggered = true;
            openSearch();
        }
    }, { passive: true });
})();

function openNoteResult(id) {
    closeSearch();
    switchMode('notes');
    selectNote(id);
}

function openTodoResult(category,id) { closeSearch(); switchMode('todos'); openTask(id); }

document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    closeSearch();
    const pickerPanel = document.getElementById('note-picker-panel');
    if (pickerPanel) pickerPanel.hidden = true;
});


document.addEventListener('DOMContentLoaded', function() {
    initNoteEditor();
    initCalendarBtn();
    setupInactivityLogout();
    updateTopbarStat();
    updateHabitsBadge();

});
