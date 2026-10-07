// State is limited to presentation and unsaved drafts; task truth stays on the server.
let pendingFocus = null;
let workspaceState = null;
let workspaceRestore = null;
let groupFocus = null;
let todayFocus = null;
let noteViewState = null;
let captureRequestID = 0;
let restoringHistory = false;
function readLocal(key) {
    try {
        return localStorage.getItem(key);
    } catch (_) {
        return null;
    }
}
function writeLocal(key, value) {
    try {
        localStorage.setItem(key, value);
    } catch (_) {}
}
function removeLocal(key) {
    try {
        localStorage.removeItem(key);
    } catch (_) {}
}
function sectionStorageKey(details) {
    return (
        (matchMedia('(max-width: 767px)').matches ? 'mobile-section-' : 'section-') + details.dataset.persist
    );
}
function restoreSections(root = document) {
    root.querySelectorAll('details[data-persist]').forEach(d => {
        const saved = readLocal(sectionStorageKey(d));
        if (saved !== null) d.open = saved === '1';
        else if (matchMedia('(max-width: 767px)').matches && d.classList.contains('workspace-section'))
            d.open = true;
    });
}
function loadProjectBody(details) {
    if (!details?.open) return Promise.resolve();
    const target = details.querySelector(':scope > [data-lazy-project]');
    if (!target || target.dataset.loaded) return Promise.resolve();
    if (target._loadPromise) return target._loadPromise;
    target.dataset.loading = '1';
    target.setAttribute('aria-busy', 'true');
    target._loadPromise = htmx
        .ajax('GET', '/workspace/project?id=' + target.dataset.lazyProject, {
            // htmx queues requests per source element (document.body when none
            // is given) and resolves a queued request's promise without sending
            // it, so several projects opening at once must not share a source.
            source: target,
            target: '#' + target.id,
            swap: 'innerHTML',
        })
        .then(() => {
            target.dataset.loaded = '1';
            restoreSections(target);
            restoreCaptureDrafts(target);
        })
        .catch(() => {
            delete target.dataset.loaded;
            target.textContent = 'Could not load this project. Close and reopen to retry.';
            if (taskEditor && !taskEditor.form.isConnected) closeTaskEditor({ focus: false });
        })
        .finally(() => {
            delete target.dataset.loading;
            target.removeAttribute('aria-busy');
            delete target._loadPromise;
        });
    return target._loadPromise;
}
function loadOpenProjects(root = document) {
    return Promise.all([...(root.querySelectorAll?.('.project-group[open]') || [])].map(loadProjectBody));
}
document.addEventListener(
    'toggle',
    e => {
        const d = e.target;
        if (d.matches?.('details[data-persist]')) writeLocal(sectionStorageKey(d), d.open ? '1' : '0');
        if (d.matches?.('.project-group')) loadProjectBody(d);
    },
    true,
);
function captureDraftKey(form) {
    return 'capture-draft-' + form.id;
}
function saveCaptureDraft(form) {
    const text = form.elements.text?.value || '',
        due = form.elements.due_date?.value || '';
    if (text || due) writeLocal(captureDraftKey(form), JSON.stringify({ text, due }));
    else removeLocal(captureDraftKey(form));
}
function restoreCaptureDrafts(root = document) {
    root.querySelectorAll?.('.capture-form').forEach(form => {
        let draft;
        try {
            draft = JSON.parse(readLocal(captureDraftKey(form)) || 'null');
        } catch (_) {
            draft = null;
        }
        if (!draft) return;
        if (form.elements.text && !form.elements.text.value) form.elements.text.value = draft.text || '';
        if (form.elements.due_date && !form.elements.due_date.value)
            form.elements.due_date.value = draft.due || '';
    });
    syncDateChips(root);
}
// Open projects reload their bodies after the list, so the old scroll offset
// alone lands elsewhere; remember where things sat on screen instead. The
// first that still exists afterwards wins: what was in use, its project (an
// archived row is gone), then the first row or project that was in view.
const ANCHOR_SELECTOR = '.capture-form[id], .todo-item[id], .project-group[id]';
function scrollAnchors(focused) {
    const anchors = [];
    const add = el => {
        if (el?.id && el.getClientRects().length)
            anchors.push({ id: el.id, top: el.getBoundingClientRect().top });
    };
    const inUse = focused?.closest?.('#todo-items') ? focused.closest(ANCHOR_SELECTOR) : null;
    add(inUse);
    add(inUse?.parentElement?.closest('.project-group'));
    add(
        [...document.querySelectorAll('#todo-items :is(' + ANCHOR_SELECTOR + ')')].find(
            el => el.getClientRects().length && el.getBoundingClientRect().top >= 0,
        ),
    );
    return anchors;
}
function rememberWorkspace() {
    const forms = {};
    document.querySelectorAll('#todo-items .capture-form').forEach(f => {
        forms[f.id] = Object.fromEntries(new FormData(f));
    });
    // A refresh landing while the last one still waits for project bodies
    // would measure a half-built list; keep the last one's places and focus.
    if (workspaceRestore) {
        workspaceState = { ...workspaceRestore, forms };
        return;
    }
    const el = document.activeElement;
    // The task editor keeps its own focus across swaps (see keepTaskEditor).
    const row = el?.closest('#task-editor') ? null : el?.closest('.todo-item');
    workspaceState = {
        forms,
        scroll: window.scrollY,
        height: document.getElementById('todo-items')?.offsetHeight || 0,
        anchors: scrollAnchors(el),
        focus: el?.closest('.capture-form')?.id,
        name: el?.name,
        start: el?.selectionStart,
        end: el?.selectionEnd,
        rowId: row?.dataset.taskId,
        rowControl: el?.classList.contains('check-btn')
            ? 'check'
            : el?.classList.contains('task-label')
              ? 'label'
              : '',
    };
}
function restoreWorkspace() {
    processNow(document.getElementById('todo-items'));
    restoreSections();
    applyProjectPage();
    const loading = loadOpenProjects();
    restoreCaptureDrafts();
    if (workspaceState) {
        const s = workspaceState;
        for (const [id, values] of Object.entries(s.forms)) {
            const form = document.getElementById(id);
            if (form) {
                for (const [key, value] of Object.entries(values)) {
                    const el = form.elements.namedItem(key);
                    if (el) el.value = value;
                }
                syncDateChips(form);
            }
        }
        // Rows in open projects come back only once their bodies load, so
        // focus is put back now and again after that.
        const navigation = navigationVersion;
        const refocus = () => {
            if (navigation !== navigationVersion || document.body.dataset.mode !== 'todos') return;
            if (s.focus) {
                const el = document.getElementById(s.focus)?.elements.namedItem(s.name);
                if (!el || el === document.activeElement) return;
                el.focus({ preventScroll: true });
                if (typeof s.start === 'number') el.setSelectionRange?.(s.start, s.end);
            } else if (s.rowId) {
                const row = document.getElementById('task-' + s.rowId);
                const control =
                    s.rowControl === 'check'
                        ? row?.querySelector('.check-btn')
                        : row?.querySelector('.task-label');
                if (!control || control === document.activeElement) return;
                expandAncestors(row);
                control.focus({ preventScroll: true });
            }
        };
        refocus();
        // The list may be refreshed from Today (after editing a task there);
        // its scroll offset is not the hidden list's to change.
        if (document.body.dataset.mode === 'todos') {
            // Hold the old height while project bodies load so the offset is
            // not clamped, then put the anchor back where it was on screen.
            const list = document.getElementById('todo-items');
            if (list) list.style.minHeight = s.height + 'px';
            workspaceRestore = s;
            requestAnimationFrame(() => {
                if (workspaceRestore !== s) return;
                window.scrollTo(0, s.scroll);
                // A stalled project load must not hold the list's height forever.
                Promise.race([loading, new Promise(done => setTimeout(done, 5000))]).then(() => {
                    // A newer refresh took over and will finish the job.
                    if (workspaceRestore !== s) return;
                    if (navigation !== navigationVersion || document.body.dataset.mode !== 'todos') {
                        workspaceRestore = null;
                        list?.style.removeProperty('min-height');
                        return;
                    }
                    workspaceRestore = null;
                    if (list) list.style.minHeight = '';
                    for (const a of s.anchors) {
                        const el = document.getElementById(a.id);
                        if (!el?.getClientRects().length) continue;
                        window.scrollBy(0, el.getBoundingClientRect().top - a.top);
                        break;
                    }
                    refocus();
                });
            });
        } else {
            workspaceRestore = null;
            document.getElementById('todo-items')?.style.removeProperty('min-height');
        }
        workspaceState = null;
    }
    if (pendingFocus) {
        revealTask(pendingFocus);
        pendingFocus = null;
    }
}
function refreshWorkspace() {
    return htmx.ajax('GET', '/workspace', '#todo-items');
}
function expandAncestors(el) {
    let node = el;
    while (node) {
        if (node.tagName === 'DETAILS') node.open = true;
        node = node.parentElement;
    }
}
function showWorkspaceContaining(el) {
    const project = el?.closest('.project-group');
    if (project && isDesktop()) {
        showProjectPage(Number(project.id.replace('project-', '')));
        return;
    }
    const section = el?.closest('.workspace-section');
    if (!section) return;
    const key = section.id.replace('section-', '');
    if (WORKSPACE_TITLES[key]) {
        document.body.dataset.workspaceSection = key;
        writeLocal('desktop-workspace', key);
        updateNavigation('todos');
        document.getElementById('topbar-title').textContent = WORKSPACE_TITLES[key];
        updateTopbarStat();
    }
}
function revealTask(id) {
    const row = document.getElementById('task-' + id);
    if (row) {
        showWorkspaceContaining(row);
        document.querySelectorAll('.todo-item.selected').forEach(el => el.classList.remove('selected'));
        expandAncestors(row);
        row.scrollIntoView({ block: 'nearest' });
        row.classList.add('selected');
    }
}
function setDestination(values) {
    const url = new URL(location.href);
    url.search = '';
    Object.entries(values).forEach(([k, v]) => url.searchParams.set(k, v));
    if (url.href === location.href) return;
    navigationVersion++;
    const state = { secondbrain: true, ...values };
    if (restoringHistory) history.replaceState(state, '', url);
    else history.pushState(state, '', url);
}
function closePopupMenus() {
    document.querySelectorAll('.row-menu,.app-menu').forEach(d => (d.open = false));
}

// --- Task editor ---
// Tapping a task opens it in place: the title becomes editable, with its date,
// repeat and list below. Edits save as you type and never re-render the
// editor; the lists refresh once it closes, so a row never jumps away while it
// is being edited. One task is open at a time, in Tasks or in Today.
let taskEditor = null;
let taskEditorOpening = 0;
const taskBuffers = new Map();
function taskRow(id, scope) {
    return scope === 'today'
        ? document.querySelector('#today-content [data-task-id="' + id + '"]')
        : document.getElementById('task-' + id);
}
async function openTask(id, options = {}) {
    if (!id) return;
    const scope = options.returnMode === 'today' ? 'today' : 'todos';
    closePopupMenus();
    if (taskEditor?.id === id && taskEditor.scope === scope) {
        focusTaskEditor(taskEditor);
        return;
    }
    if (document.body.dataset.offlineCopy) {
        showToast('Offline. This copy is read-only.');
        return;
    }
    const opening = ++taskEditorOpening;
    closeTaskEditor({ focus: false });
    const navigation = navigationVersion;
    const buffer = taskBuffers.get(id);
    const retained = buffer && (buffer.dirty || buffer.saving || buffer.conflict) ? buffer : null;
    let html;
    try {
        const response = await fetch('/task/edit?id=' + id, {
            headers: { 'HX-Request': 'true' },
        });
        html = await response.text();
        if (response.status === 401) {
            location.href = '/login';
            return;
        }
        if (!response.ok) {
            showToast(
                response.status < 500 && html.length < 120 ? html.trim() : 'Something went wrong — try again',
            );
            return;
        }
    } catch (_) {
        showToast('You appear to be offline');
        return;
    }
    const form =
        retained?.form || new DOMParser().parseFromString(html, 'text/html').getElementById('task-editor');
    if (!form || opening !== taskEditorOpening || navigation !== navigationVersion) return;
    if (scope === 'todos') {
        if (currentWorkspaceSection() === 'archive')
            document.body.dataset.workspaceSection = readLocal('desktop-workspace') || 'tasks';
        switchMode('todos');
        const destinationVersion = navigationVersion;
        if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
        if (destinationVersion !== navigationVersion) return;
        const project = document.getElementById('project-' + form.dataset.projectId);
        if (project) {
            project.open = true;
            await loadProjectBody(project);
        }
        if (opening !== taskEditorOpening || destinationVersion !== navigationVersion) return;
        revealTask(id);
        setDestination({ view: 'todos', task: id });
    }
    const row = taskRow(id, scope);
    if (!row) {
        showToast('Task unavailable. It may have been archived.');
        return;
    }
    taskEditor = retained || {
        id,
        scope,
        form,
        savedText: form.elements.text.value,
        dirty: false,
        changed: false,
    };
    taskEditor.scope = scope;
    taskBuffers.set(id, taskEditor);
    attachTaskEditor(taskEditor, row);
    const attachedVersion = navigationVersion;
    if (!retained) await restoreTaskDraft(taskEditor);
    if (
        opening !== taskEditorOpening ||
        attachedVersion !== navigationVersion ||
        !taskEditor?.form.isConnected
    )
        return;
    if (taskEditor.conflict) showTaskConflict(taskEditor, taskEditor.conflict);
    // Only a tap animates the editor in; re-attaching it after a refresh does not.
    form.classList.add('is-opening');
    setTimeout(() => form.classList.remove('is-opening'), 260);
    focusTaskEditor(taskEditor);
}
function openTodayTask(id) {
    return openTask(id, { returnMode: 'today' });
}
function attachTaskEditor(editor, row) {
    row.classList.remove('selected');
    row.classList.add('editing');
    const label = row.querySelector('[data-edit-label]');
    if (label) label.after(editor.form);
    else row.append(editor.form);
}
function focusTaskEditor(editor) {
    const title = editor.form.elements.text;
    // Focusing on touch screens raises the keyboard over the date, repeat and
    // list controls; the title is one tap away there.
    if (matchMedia('(hover: hover) and (pointer: fine)').matches) {
        title.focus({ preventScroll: true });
        title.setSelectionRange(title.value.length, title.value.length);
    } else editor.form.focus({ preventScroll: true });
    editor.form.scrollIntoView({ block: 'nearest' });
}
function taskDraft(editor) {
    return Object.fromEntries(new FormData(editor.form));
}
async function storeTaskDraft(editor) {
    try {
        await editorDrafts.write('tasks', editor.id, taskDraft(editor));
    } catch (_) {
        showTaskStatus(editor, 'Draft kept in this tab. Browser storage is unavailable.');
    }
}
async function restoreTaskDraft(editor) {
    try {
        const draft = await editorDrafts.read('tasks', editor.id);
        if (!draft || editor.dirty) return;
        const fields = editor.form.elements;
        if (
            ['text', 'due_date', 'repeat', 'list'].every(
                key => String(draft[key] || '') === String(fields[key]?.value || ''),
            )
        ) {
            await editorDrafts.remove('tasks', editor.id);
            return;
        }
        for (const key of ['text', 'due_date', 'repeat', 'list', 'revision']) {
            if (fields[key] && draft[key] !== undefined) fields[key].value = draft[key];
        }
        editor.dirty = true;
        markSetChips(editor.form);
        showTaskStatus(editor, 'Unsaved draft restored. Edit to retry saving.');
    } catch (_) {}
}
function closeTaskEditor(options = {}) {
    const editor = taskEditor;
    if (!editor) return;
    taskEditor = null;
    const row = editor.form.closest('[data-task-id]');
    const focused = editor.form.contains(document.activeElement);
    editor.form.remove();
    row?.classList.remove('editing', 'selected');
    if (options.focus !== false && focused)
        row?.querySelector('[data-edit-label]')?.focus({ preventScroll: true });
    if (editor.scope === 'todos' && new URLSearchParams(location.search).has('task'))
        setDestination({ view: 'todos' });
    // A blank title would not save; keep the last saved one instead.
    const title = editor.form.elements.text;
    if (!title.value.trim()) title.value = editor.savedText;
    // Show the new title at once; the refresh after saving settles the rest.
    const label = row?.querySelector('.task-label');
    if (label) label.textContent = title.value.trim();
    saveTaskEditor(editor).then(() => {
        if (editor.changed) refreshTaskViews();
    });
}
function refreshTaskViews() {
    if (document.body.dataset.mode === 'today') {
        htmx.ajax('GET', '/today', '#today-content');
    }
    invalidateViews(['todos', 'today']);
    refreshStaleView();
}
function showTaskStatus(editor, message) {
    const status = editor.form.querySelector('.task-save-status');
    if (status) status.textContent = message;
}
function scheduleTaskSave(editor) {
    editor.dirty = true;
    editorDrafts.state(editor.form, 'dirty');
    void storeTaskDraft(editor);
    clearTimeout(editor.timer);
    editor.timer = setTimeout(() => saveTaskEditor(editor), 600);
}
// Saves run one at a time; a save asked for while one is running waits for
// it and then sends whatever is newest.
async function saveTaskEditor(editor, options = {}) {
    clearTimeout(editor.timer);
    editor.timer = null;
    while (editor.saving) await editor.saving;
    if (editor.conflict) return false;
    if (!editor.dirty) {
        if (!editor.form.isConnected && taskBuffers.get(editor.id) === editor) taskBuffers.delete(editor.id);
        return true;
    }
    await storeTaskDraft(editor);
    const form = editor.form;
    if (!form.elements.text.value.trim()) {
        showTaskStatus(editor, 'Enter a task title to save.');
        return false;
    }
    const body = new URLSearchParams(new FormData(form));
    editor.dirty = false;
    editorDrafts.state(form, 'saving');
    showTaskStatus(editor, 'Saving…');
    editor.saving = (async () => {
        try {
            const response = await fetch('/task/save', {
                method: 'POST',
                headers: {
                    'HX-Request': 'true',
                    'Content-Type': 'application/x-www-form-urlencoded',
                },
                body,
                keepalive: !!options.keepalive,
            });
            const text = await response.text();
            let data = null;
            try {
                data = JSON.parse(text);
            } catch (_) {}
            if (response.status === 409 && data?.task) {
                editor.dirty = true;
                editor.conflict = data.task;
                editorDrafts.state(form, 'conflicted');
                showTaskConflict(editor, data.task);
                return false;
            }
            if (!response.ok || data?.status !== 'saved') {
                editor.dirty = true;
                editorDrafts.state(form, 'failed');
                const reason =
                    response.status === 401 ? 'Session expired. Sign in again to save.' : text.trim();
                showTaskStatus(
                    editor,
                    response.status < 500 && reason.length < 120 ? reason : 'Not saved. Try again.',
                );
                if (!form.isConnected) showToast('Not saved. Your task draft is kept for next time.');
                return false;
            }
            form.elements.revision.value = data.revision;
            editor.savedText = body.get('text');
            editor.changed = true;
            if (editor.dirty) await storeTaskDraft(editor);
            else await editorDrafts.remove('tasks', editor.id);
            editorDrafts.state(form, editor.dirty ? 'dirty' : 'clean');
            reportMutationEffects(response);
            refreshSidebar();
            showTaskStatus(editor, editor.dirty ? '' : 'Saved');
            return true;
        } catch (_) {
            editor.dirty = true;
            editorDrafts.state(form, 'failed');
            showTaskStatus(editor, 'Offline. Your draft is kept. Edit to retry saving.');
            if (!form.isConnected) showToast('Offline. Your task draft is kept for next time.');
            return false;
        }
    })();
    const saved = await editor.saving;
    editor.saving = null;
    if (!editor.dirty && !editor.form.isConnected && taskBuffers.get(editor.id) === editor)
        taskBuffers.delete(editor.id);
    return saved;
}
function showTaskConflict(editor, latest) {
    const form = editor.form;
    if (!form.isConnected) {
        showToast('That task changed on another device, so your last edit was not saved.');
        return;
    }
    showTaskStatus(editor, '');
    let conflict = form.querySelector('.edit-conflict');
    if (!conflict) {
        conflict = document.createElement('section');
        conflict.className = 'edit-conflict';
        conflict.setAttribute('role', 'alert');
        form.appendChild(conflict);
    }
    conflict.replaceChildren();
    const title = document.createElement('h3');
    title.textContent = 'Changed on another device';
    const copy = document.createElement('p');
    const details = [
        latest.due_date ? relativeDate(latest.due_date) : 'No date',
        latest.repeat ? 'Repeats ' + latest.repeat : '',
    ];
    copy.textContent = 'Latest task: ' + latest.text + ' · ' + details.filter(Boolean).join(' · ');
    const keep = taskAction('Keep mine', () => {
        conflict.remove();
        editor.conflict = null;
        form.elements.revision.value = latest.revision;
        saveTaskEditor(editor);
    });
    const use = taskAction('Use latest', () => {
        conflict.remove();
        editor.conflict = null;
        void editorDrafts.remove('tasks', editor.id);
        const fields = form.elements;
        fields.revision.value = latest.revision;
        fields.text.value = latest.text;
        if (fields.due_date) fields.due_date.value = latest.due_date;
        if (fields.repeat) fields.repeat.value = latest.repeat;
        if (fields.list)
            fields.list.value = latest.heading_id
                ? 'heading:' + latest.heading_id
                : latest.project_id
                  ? 'project:' + latest.project_id
                  : '';
        editor.savedText = latest.text;
        editor.dirty = false;
        editor.changed = true;
        markSetChips(form);
    });
    conflict.append(title, copy, keep, use);
    keep.focus();
}
function markSetChips(form) {
    form.querySelectorAll('.chip:not(.chip-date)').forEach(chip => {
        const field = chip.querySelector('input,select');
        if (field?.name !== 'list') chip.classList.toggle('is-set', !!field?.value);
    });
    syncDateChips(form);
}
// Words a YYYY-MM-DD date like the list rows do (relativeDate in main.go):
// "Today", "Tomorrow", "Fri", "in 9 days", "3 days ago", "Oct 12".
// A date chip shows its date in the list rows' wording ("Tomorrow"), or "Date".
function syncDateChips(root = document) {
    root.querySelectorAll?.('.chip-date').forEach(chip => {
        const value = chip.querySelector('input').value;
        chip.classList.toggle('is-set', !!value);
        chip.querySelector('.chip-text').textContent = value ? relativeDate(value) : 'Date';
        chip.querySelector('.chip-clear').hidden = !value;
    });
}
document.addEventListener('change', e => {
    if (e.target.matches?.('.chip-date input')) syncDateChips(e.target.closest('.chip-date').parentElement);
});
document.addEventListener('click', e => {
    // × clears the date as if it had been picked away, so drafts and the
    // task editor's autosave see an ordinary input.
    const clear = e.target.closest?.('.chip-clear');
    if (clear) {
        const input = clear.closest('.chip-date').querySelector('input');
        // Clearing a typed date keeps the words in the title from now on.
        const form = clear.closest('.capture-form');
        if (form?.dataset.dateSource === 'typed') form.dataset.typedOff = '1';
        if (form) delete form.dataset.dateSource;
        input.value = '';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        syncDateChips(clear.closest('.chip-date').parentElement);
        return;
    }
    // The date field is invisible over its chip; open the picker on a click,
    // which desktop browsers otherwise only do from their own calendar icon.
    const input = e.target.closest?.('.chip-date input');
    try {
        input?.showPicker?.();
    } catch (_) {}
});
// --- Typed dates ---
// A date typed at the end of a new task ("Call the bank tomorrow", "Pay rent
// on fri", "Renew passport 30 sep") becomes its due date and leaves the title.
// The date chip previews it while typing; × or picking another date keeps the
// words as typed.
// Preview a typed date in the capture row's chip, unless a date was picked by
// hand or the typed one was cleared.
function previewTypedDate(form) {
    const due = form.querySelector('.chip-date input');
    if (!due || form.dataset.dateSource === 'picked' || form.dataset.typedOff) return;
    const typed = typedDate(form.elements.text.value);
    if (typed) {
        due.value = typed.due;
        form.dataset.dateSource = 'typed';
    } else if (form.dataset.dateSource === 'typed') {
        due.value = '';
        delete form.dataset.dateSource;
    }
    syncDateChips(form);
    saveCaptureDraft(form);
}
document.addEventListener('input', e => {
    const form = e.target.closest?.('.capture-form');
    if (!form || e.target.name !== 'text' || form.querySelector('[name=category]')?.value !== 'todo') return;
    if (!e.target.value) delete form.dataset.typedOff;
    previewTypedDate(form);
});
document.addEventListener('change', e => {
    const form = e.target.closest?.('.capture-form');
    if (form && e.target.matches('.chip-date input') && e.target.value) form.dataset.dateSource = 'picked';
});
// Send the title without its date words. Today's own capture row has no chip,
// so a typed date there simply replaces "today".
document.body.addEventListener('htmx:configRequest', e => {
    const form = e.detail.elt;
    if (!form?.classList.contains('capture-form') || e.detail.parameters.category !== 'todo') return;
    const chip = form.querySelector('.chip-date');
    if (chip && form.dataset.dateSource !== 'typed') return;
    const typed = typedDate(String(e.detail.parameters.text || ''));
    if (!typed) return;
    e.detail.parameters.text = typed.title;
    e.detail.parameters.due_date = typed.due;
});
// A swap that replaces the editor's row (a completion, a refresh) detaches
// the editor with its unsaved text; put it back into the new row.
function keepTaskEditor(e) {
    const editor = taskEditor;
    if (!editor || !e.detail.target?.contains(editor.form)) return;
    const el = document.activeElement;
    editor.focus = editor.form.contains(el)
        ? { name: el.name, start: el.selectionStart, end: el.selectionEnd }
        : null;
}
async function restoreTaskEditor() {
    const editor = taskEditor;
    if (!editor || editor.form.isConnected) return;
    const row = taskRow(editor.id, editor.scope);
    if (!row) {
        // A refresh reloads open projects' bodies after the list itself; wait
        // for them before deciding the task is gone.
        const loading = document.querySelector(
            '.project-group[open] > [data-lazy-project]:not([data-loaded])',
        );
        if (editor.scope !== 'todos' || !loading) closeTaskEditor({ focus: false });
        return;
    }
    attachTaskEditor(editor, row);
    if (
        !editor.dirty &&
        !editor.saving &&
        !editor.conflict &&
        row.dataset.revision !== editor.form.elements.revision.value
    ) {
        try {
            const response = await fetch('/task/edit?id=' + editor.id, { headers: { 'HX-Request': 'true' } });
            if (!response.ok || taskEditor !== editor || editor.dirty || editor.saving) return;
            const latest = new DOMParser()
                .parseFromString(await response.text(), 'text/html')
                .getElementById('task-editor');
            if (!latest || editor.dirty || editor.saving) return;
            for (const key of ['text', 'due_date', 'repeat', 'list', 'revision']) {
                if (editor.form.elements[key] && latest.elements[key])
                    editor.form.elements[key].value = latest.elements[key].value;
            }
            editor.savedText = editor.form.elements.text.value;
            markSetChips(editor.form);
        } catch (_) {}
    }
    const field = editor.focus?.name && editor.form.elements.namedItem(editor.focus.name);
    if (field && document.body.dataset.mode === editor.scope && field.getClientRects().length) {
        field.focus({ preventScroll: true });
        if (typeof editor.focus.start === 'number')
            field.setSelectionRange?.(editor.focus.start, editor.focus.end);
    }
    editor.focus = null;
}
document.addEventListener('input', e => {
    if (!taskEditor || !taskEditor.form.contains(e.target)) return;
    if (!taskEditor.conflict) taskEditor.form.querySelector('.edit-conflict')?.remove();
    showTaskStatus(taskEditor, '');
    markSetChips(taskEditor.form);
    scheduleTaskSave(taskEditor);
});
document.addEventListener('keydown', e => {
    if (e.key !== 'Enter' || e.isComposing || !e.target.matches?.('#task-editor [name=text]')) return;
    e.preventDefault();
    closeTaskEditor();
});
// Tapping anywhere outside the task being edited closes it.
document.addEventListener('click', e => {
    const row = taskEditor?.form.closest('[data-task-id]');
    if (row && e.target.isConnected && !row.contains(e.target)) closeTaskEditor({ focus: false });
});
function flushTaskEditor() {
    for (const editor of taskBuffers.values()) {
        if (editor.dirty) {
            void storeTaskDraft(editor);
            void saveTaskEditor(editor, { keepalive: true });
        }
    }
}
document.addEventListener('visibilitychange', () => {
    if (document.hidden) flushTaskEditor();
});
window.addEventListener('pagehide', flushTaskEditor);
// --- Project pages ---
// On desktop a project opens as its own page, reached from the sidebar. It is
// the project's section of the task workspace with everything else hidden, so
// completing, capturing and refreshing work exactly as in the full list. On
// phones projects stay inline in the one continuous Tasks page.
function applyProjectPage() {
    const id = document.body.dataset.workspaceSection === 'project' ? document.body.dataset.projectId : '';
    // An attribute, not a class: htmx resets classes when it settles a swap.
    document.querySelectorAll('.project-group').forEach(project => {
        const page = project.id === 'project-' + id;
        project.toggleAttribute('data-project-page', page);
        if (page && !project.open) project.open = true;
    });
}
function showProjectPage(id) {
    document.body.dataset.workspaceSection = 'project';
    document.body.dataset.projectId = String(id);
    applyProjectPage();
    switchMode('todos');
    const page = document.querySelector('[data-project-page]');
    if (page) loadProjectBody(page).then(updateTopbarStat);
}
async function openProject(id) {
    if (!id) return;
    if (isDesktop()) {
        const navigation = navigationVersion;
        if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
        if (navigation !== navigationVersion) return;
        showProjectPage(id);
        window.scrollTo({ top: 0, behavior: 'auto' });
        setDestination({ view: 'todos', project: id });
        return;
    }
    document.body.dataset.workspaceSection = 'tasks';
    writeLocal('desktop-workspace', 'tasks');
    switchMode('todos');
    const navigation = navigationVersion;
    if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
    if (navigation !== navigationVersion) return;
    const el = document.getElementById('project-' + id);
    if (el) {
        showWorkspaceContaining(el);
        expandAncestors(el);
        el.open = true;
        el.scrollIntoView({ block: 'start' });
    }
    setDestination({ view: 'todos', project: id });
}
// A project page's title is not a disclosure: keep it open.
document.addEventListener('click', e => {
    const summary = e.target.closest?.('[data-project-page] > summary');
    if (summary && isDesktop()) e.preventDefault();
});

function taskAction(label, onClick) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'task-edit-action';
    button.textContent = label;
    button.addEventListener('mousedown', e => e.preventDefault());
    button.addEventListener('click', onClick);
    return button;
}
async function structureCreate(kind, parent) {
    const name = await showPrompt(kind === 'project' ? 'New project' : 'New heading');
    if (name) structureAction(kind, 0, 'create', { name, project_id: parent });
}
async function structureRename(kind, id, el) {
    closePopupMenus();
    const name = await showPrompt('Rename ' + kind, el.dataset.name);
    if (name) structureAction(kind, id, 'rename', { name });
}
// Archiving a heading puts it away with its tasks, for a finished section.
async function archiveHeading(id, el) {
    closePopupMenus();
    if (
        await showConfirm(
            'Archive the heading “' +
                el.dataset.name +
                '” and its tasks? You can restore the tasks from the Archive.',
        )
    )
        structureAction('heading', id, 'archive');
}
async function deleteHeading(id, el) {
    closePopupMenus();
    if (await showConfirm('Delete the heading “' + el.dataset.name + '”? Its tasks stay in the project.'))
        structureAction('heading', id, 'delete');
}
async function structureAction(kind, id, action, extra = {}) {
    closePopupMenus();
    await htmx.ajax('POST', kind === 'project' ? '/projects/action' : '/headings/action', {
        target: '#todo-items',
        values: { id, action, ...extra },
    });
}
// Finishing a project is a big moment: its ring closes into a tick, the project
// lifts away, and only then is it sent to the archive.
async function completeProject(id) {
    closePopupMenus();
    const project = document.getElementById('project-' + id);
    const onPage = project?.hasAttribute('data-project-page');
    try {
        if (project && !matchMedia('(prefers-reduced-motion: reduce)').matches) {
            // The ring is the moment; on a phone it may have scrolled under the top bar.
            project.querySelector('summary')?.scrollIntoView({ block: 'nearest' });
            project.classList.add('is-finishing');
            await new Promise(done => setTimeout(done, 760));
        }
        await structureAction('project', id, 'complete');
    } finally {
        // A refused completion leaves the project where it was.
        project?.classList.remove('is-finishing');
    }
    if (
        onPage &&
        document.body.dataset.mode === 'todos' &&
        currentWorkspaceSection() === 'project' &&
        document.body.dataset.projectId === String(id) &&
        !document.getElementById('project-' + id)
    )
        switchWorkspaceSection('tasks');
}
// --- Archive ---
// One view for everything archived, shown in the task area. Back returns to
// wherever it was opened from.
let archiveReturn = null;
function openArchive(kind = 'todo') {
    const mode = document.body.dataset.mode || 'todos';
    if (currentWorkspaceSection() !== 'archive' || mode !== 'todos')
        archiveReturn = {
            mode,
            workspace: currentWorkspaceSection(),
            project: document.body.dataset.projectId,
        };
    document.body.dataset.workspaceSection = 'archive';
    switchMode('todos');
    setDestination({ view: 'archive', kind });
    htmx.ajax('GET', '/archive?kind=' + encodeURIComponent(kind), '#todo-items');
}
function hideArchive() {
    const navigation = navigationVersion;
    const back = archiveReturn || { mode: 'todos', workspace: 'tasks' };
    archiveReturn = null;
    refreshWorkspace().then(() => {
        if (navigation !== navigationVersion) return;
        if (back.mode !== 'todos') {
            document.body.dataset.workspaceSection = readLocal('desktop-workspace') || 'tasks';
            if (back.mode === 'today') openToday();
            else switchMode(back.mode);
        } else if (back.workspace === 'project' && back.project) showProjectPage(Number(back.project));
        else
            switchWorkspaceSection(back.workspace === 'archive' ? 'tasks' : back.workspace, {
                preserveScroll: true,
            });
    });
}
function openToday() {
    switchMode('today');
    setDestination({ view: 'today' });
    htmx.ajax('GET', '/today', '#today-content');
}
function routeLocation() {
    const q = new URLSearchParams(location.search);
    if (q.get('task')) openTask(Number(q.get('task')));
    else if (q.get('project')) openProject(Number(q.get('project')));
    // Habits, weekly reviews and the settings screen were retired; keep old links working.
    else if (['today', 'habits', 'review', 'settings'].includes(q.get('view'))) openToday();
    else if (q.get('view') === 'archive') openArchive(q.get('kind') || 'todo');
    else if (q.get('note')) openNoteResult(Number(q.get('note')));
    else if (q.get('view')) switchMode(q.get('view'));
    else switchMode('today');
}
document.body.addEventListener('htmx:beforeSwap', e => {
    if (e.detail.target.id === 'todo-items') rememberWorkspace();
    if (e.detail.target.id === 'today-content') {
        const el = document.activeElement;
        todayFocus = el?.closest('#capture-today')
            ? { start: el.selectionStart, end: el.selectionEnd }
            : null;
    }
    // A list replaced after a change inside it takes its capture row with it;
    // remember whether that row was being typed in.
    if (e.detail.target.classList?.contains('task-group')) {
        const el = document.activeElement;
        const form = e.detail.target.contains(el) ? el.closest('.capture-form') : null;
        groupFocus = form
            ? {
                  form: form.id,
                  name: el.name,
                  start: el.selectionStart,
                  end: el.selectionEnd,
              }
            : null;
    }
    // A background response must never overwrite a note being edited now.
    if (e.detail.target.id === 'notes-content') {
        const editor = document.getElementById('note-editor');
        if (editor?.dataset.dirty) {
            e.detail.shouldSwap = false;
            return;
        }
        if (editor)
            noteViewState = {
                id: editor.dataset.noteId,
                start: editor.selectionStart,
                end: editor.selectionEnd,
                scroll: editor.scrollTop,
                focused: document.activeElement === editor,
            };
    }
});
document.body.addEventListener('htmx:beforeRequest', e => {
    const form = e.detail.elt;
    if (form?.getAttribute('hx-post') === '/todos/toggle') {
        const row = form.closest('.todo-item, .today-row');
        if (row && !row.classList.contains('done')) row.classList.add('is-completing');
    }
    if (!form?.classList.contains('capture-form')) return;
    const input = form.elements.text;
    // The title as sent, without any typed date.
    const text = String(e.detail.requestConfig?.parameters?.text ?? input?.value ?? '').trim();
    if (!text) return;
    const pending = document.createElement('div');
    pending.className = 'todo-item capture-pending';
    pending.dataset.captureRequest = String(++captureRequestID);
    const marker = document.createElement('span');
    marker.className = 'pending-marker';
    marker.setAttribute('aria-hidden', 'true');
    const label = document.createElement('span');
    label.className = 'task-label';
    label.textContent = text;
    pending.append(marker, label);
    form.insertAdjacentElement('afterend', pending);
    form.closest('.task-group')?.querySelector('.quiet-empty')?.setAttribute('hidden', '');
    form.dataset.pendingRow = pending.dataset.captureRequest;
    input.value = '';
    // The next task starts without a date; a failed add gets this one back.
    delete form.dataset.dateSource;
    delete form.dataset.typedOff;
    const due = form.querySelector('.chip-date input');
    if (due?.value) {
        pending.dataset.due = due.value;
        due.value = '';
        syncDateChips(form);
    }
    // Drop the draft now: the swap detaches this form, so its own afterRequest
    // never reaches body, and the new list would restore the text.
    saveCaptureDraft(form);
    input.focus({ preventScroll: true });
});
function restoreFailedCapture(e) {
    const form = e.detail.elt;
    if (!form?.classList.contains('capture-form')) return;
    const pending = document.querySelector('[data-capture-request="' + form.dataset.pendingRow + '"]');
    if (pending && !form.elements.text.value)
        form.elements.text.value = pending.querySelector('.task-label')?.textContent || '';
    const due = form.querySelector('.chip-date input');
    if (pending?.dataset.due && due && !due.value) {
        due.value = pending.dataset.due;
        form.dataset.dateSource = 'picked';
        syncDateChips(form);
    }
    pending?.remove();
    form.closest('.task-group')?.querySelector('.quiet-empty')?.removeAttribute('hidden');
    saveCaptureDraft(form);
}
document.body.addEventListener('htmx:responseError', restoreFailedCapture);
document.body.addEventListener('htmx:sendError', restoreFailedCapture);
function restoreFailedCompletion(e) {
    e.detail.elt?.closest('.todo-item, .today-row')?.classList.remove('is-completing');
}
document.body.addEventListener('htmx:responseError', restoreFailedCompletion);
document.body.addEventListener('htmx:sendError', restoreFailedCompletion);
document.addEventListener('keydown', e => {
    const input = e.target.closest?.('.capture-form input[name="text"]');
    if (!input || e.key !== 'Enter' || e.isComposing) return;
    e.preventDefault();
    input.form.requestSubmit();
});
document.body.addEventListener('htmx:beforeSwap', keepTaskEditor);
// htmx wires up swapped-in forms when it settles, a moment after the swap. A
// capture row that is focused at once could be submitted in between (a quick
// second Enter, or taps queued on a busy phone) and would then post natively,
// navigating away; wire it up now.
function processNow(root) {
    if (root?.isConnected) htmx.process(root);
}
// A list re-rendered after an add, check-off or archive inside it. Its header
// count arrives out-of-band with it.
function restoreTaskGroup(group) {
    processNow(group);
    restoreSections(group);
    restoreCaptureDrafts(group);
    const focus = groupFocus;
    groupFocus = null;
    const el = focus && document.getElementById(focus.form)?.elements.namedItem(focus.name);
    if (el && group.contains(el) && document.body.dataset.mode === 'todos' && el.getClientRects().length) {
        el.focus({ preventScroll: true });
        if (typeof focus.start === 'number') el.setSelectionRange?.(focus.start, focus.end);
    }
    updateTopbarStat();
}
document.body.addEventListener('htmx:afterSwap', e => {
    const id = e.detail.target.id;
    if (id === 'todo-items') restoreWorkspace();
    if (id === 'today-content') restoreSections(e.detail.target);
    // After the list has reopened its sections and started loading projects.
    restoreTaskEditor();
    // An outerHTML swap fires on the new list; detail.target is the old one.
    if (e.target.classList?.contains('task-group')) restoreTaskGroup(e.target);
    if (e.detail.target.matches?.('[data-lazy-project]')) {
        processNow(e.detail.target);
        e.detail.target.dataset.loaded = '1';
        restoreSections(e.detail.target);
        restoreCaptureDrafts(e.detail.target);
    }
    if (id === 'notes-content') {
        initNoteEditor();
        const editor = document.getElementById('note-editor');
        // Opening another note fades it in; reloading the same note does not.
        if (editor && noteViewState && noteViewState.id !== editor.dataset.noteId) {
            const view = e.detail.target;
            view.classList.remove('note-entering');
            void view.offsetWidth;
            view.classList.add('note-entering');
            setTimeout(() => view.classList.remove('note-entering'), 220);
        }
        if (editor && document.body.dataset.mode === 'notes')
            setDestination({ view: 'notes', note: editor.dataset.noteId });
        if (editor && noteViewState?.id === editor.dataset.noteId) {
            editor.setSelectionRange(noteViewState.start, noteViewState.end);
            editor.scrollTop = noteViewState.scroll;
            if (noteViewState.focused && document.body.dataset.mode === 'notes')
                editor.focus({ preventScroll: true });
        }
        noteViewState = null;
    }
    // The notifications line lives in the Today footer.
    if (id === 'today-content') renderPush();
    // Today's capture row is re-rendered by an add; keep typing in the new one,
    // as the Tasks capture rows do, so the phone keyboard stays up.
    // Today is re-rendered whenever it opens or changes, capture row included:
    // keep what was being typed there (it is saved as a draft on input), and
    // keep typing after an add so the phone keyboard stays up.
    if (id === 'today-content') {
        const form = document.getElementById('capture-today');
        processNow(form);
        if (form) restoreCaptureDrafts(form.parentElement);
        const added = e.detail.requestConfig?.elt?.id === 'capture-today';
        if (form && document.body.dataset.mode === 'today' && (added || todayFocus)) {
            form.elements.text.focus({ preventScroll: true });
            if (typeof todayFocus?.start === 'number')
                form.elements.text.setSelectionRange(todayFocus.start, todayFocus.end);
        }
        todayFocus = null;
    }
});
document.body.addEventListener('sbWorkspaceChanged', e => {
    invalidateViews(['todos', 'today', 'sidebar']);
    if (e.detail.message) showToast(e.detail.message);
});
document.addEventListener('input', e => {
    const capture = e.target.closest('.capture-form');
    if (capture) {
        saveCaptureDraft(capture);
        syncDateChips(capture);
    }
});
document.addEventListener('keydown', e => {
    if (e.metaKey && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        openSearch();
    }
    if (e.metaKey && e.key === 'Enter') {
        const form = document.activeElement.closest('form');
        if (form) {
            e.preventDefault();
            form.requestSubmit();
        }
    }
    if (e.key === 'Escape') {
        closeSearch();
        closePopupMenus();
        if (taskEditor) closeTaskEditor();
    }
});
document.addEventListener('click', e =>
    document.querySelectorAll('.row-menu[open],.app-menu[open]').forEach(d => {
        if (!d.contains(e.target)) d.open = false;
    }),
);
window.addEventListener('beforeunload', e => {
    if (document.getElementById('note-editor')?.dataset.dirty || taskEditor?.dirty) {
        e.preventDefault();
        e.returnValue = '';
    }
});
window.addEventListener('popstate', () => {
    restoringHistory = true;
    routeLocation();
    queueMicrotask(() => {
        restoringHistory = false;
    });
});
document.addEventListener('DOMContentLoaded', () => {
    if (!history.state) history.replaceState({ secondbrain: true }, '', location.href);
    restoreSections();
    loadOpenProjects();
    restoreCaptureDrafts();
    restoringHistory = true;
    routeLocation();
    restoringHistory = false;
});
