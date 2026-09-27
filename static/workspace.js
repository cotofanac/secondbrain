// State is limited to presentation and unsaved drafts; task truth stays on the server.
let detailSelection = null;
let pendingFocus = null;
let workspaceState = null;
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
            swap: 'innerHTML'
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
    root.querySelectorAll?.('.project-group[open]').forEach(loadProjectBody);
}
document.addEventListener(
    'toggle',
    e => {
        const d = e.target;
        if (d.matches?.('details[data-persist]')) writeLocal(sectionStorageKey(d), d.open ? '1' : '0');
        if (d.matches?.('.project-group')) loadProjectBody(d);
    },
    true
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
}
function rememberWorkspace() {
    const forms = {};
    document.querySelectorAll('#todo-items .capture-form').forEach(f => {
        forms[f.id] = Object.fromEntries(new FormData(f));
    });
    const el = document.activeElement;
    // The task editor keeps its own focus across swaps (see keepTaskEditor).
    const row = el?.closest('#task-editor') ? null : el?.closest('.todo-item');
    workspaceState = {
        forms,
        scroll: window.scrollY,
        focus: el?.closest('.capture-form')?.id,
        name: el?.name,
        start: el?.selectionStart,
        end: el?.selectionEnd,
        rowId: row?.dataset.taskId,
        rowControl: el?.classList.contains('check-btn')
            ? 'check'
            : el?.classList.contains('task-label')
              ? 'label'
              : ''
    };
}
function restoreWorkspace() {
    restoreSections();
    applyProjectPage();
    loadOpenProjects();
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
            }
        }
        const form = document.getElementById(s.focus);
        const el = form?.elements.namedItem(s.name);
        if (el) {
            el.focus({ preventScroll: true });
            if (typeof s.start === 'number') el.setSelectionRange?.(s.start, s.end);
        }
        if (s.rowId) {
            const row = document.getElementById('task-' + s.rowId);
            if (row) expandAncestors(row);
            const control =
                s.rowControl === 'check'
                    ? row?.querySelector('.check-btn')
                    : row?.querySelector('.task-label');
            control?.focus({ preventScroll: true });
        }
        requestAnimationFrame(() => window.scrollTo(0, s.scroll));
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
    const state = { secondbrain: true, ...values };
    if (restoringHistory) history.replaceState(state, '', url);
    else history.pushState(state, '', url);
}
// The detail pane now only shows project details, which save on each action.
function showDetail(url) {
    const pane = document.getElementById('detail-pane');
    const opening = pane.hidden;
    if (pane.dataset.url !== url) {
        pane.textContent = 'Loading details…';
        pane.dataset.url = url;
    }
    pane.hidden = false;
    if (opening) {
        pane.classList.remove('detail-entering');
        void pane.offsetWidth;
        pane.classList.add('detail-entering');
        setTimeout(() => pane.classList.remove('detail-entering'), 220);
    }
    pane.dataset.focusAfterLoad = '1';
    document.querySelector('.task-layout').classList.add('has-detail');
    return htmx.ajax('GET', url, '#detail-pane').catch(() => {});
}
function closePopupMenus() {
    document.querySelectorAll('.row-menu,.app-menu,.capture-options').forEach(d => (d.open = false));
}

// --- Task editor ---
// Tapping a task opens it in place: the title becomes editable, with its date,
// repeat and list below. Edits save as you type and never re-render the
// editor; the lists refresh once it closes, so a row never jumps away while it
// is being edited. One task is open at a time, in Tasks or in Today.
let taskEditor = null;
let taskEditorOpening = 0;
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
    let html;
    try {
        const response = await fetch('/task/edit?id=' + id, { headers: { 'HX-Request': 'true' } });
        html = await response.text();
        if (response.status === 401) {
            location.href = '/login';
            return;
        }
        if (!response.ok) {
            showToast(response.status < 500 && html.length < 120 ? html.trim() : 'Something went wrong — try again');
            return;
        }
    } catch (_) {
        showToast('You appear to be offline');
        return;
    }
    const form = new DOMParser().parseFromString(html, 'text/html').getElementById('task-editor');
    if (!form || opening !== taskEditorOpening) return;
    if (scope === 'todos') {
        if (currentWorkspaceSection() === 'archive')
            document.body.dataset.workspaceSection = readLocal('desktop-workspace') || 'tasks';
        switchMode('todos');
        if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
        const project = document.getElementById('project-' + form.dataset.projectId);
        if (project) {
            project.open = true;
            await loadProjectBody(project);
        }
        if (opening !== taskEditorOpening) return;
        revealTask(id);
        setDestination({ view: 'todos', task: id });
    }
    const row = taskRow(id, scope);
    if (!row) {
        showToast('Task unavailable. It may have been archived.');
        return;
    }
    taskEditor = { id, scope, form, savedText: form.elements.text.value, dirty: false, changed: false };
    attachTaskEditor(taskEditor, row);
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
function closeTaskEditor(options = {}) {
    const editor = taskEditor;
    if (!editor) return;
    taskEditor = null;
    const row = editor.form.closest('[data-task-id]');
    const focused = editor.form.contains(document.activeElement);
    editor.form.remove();
    row?.classList.remove('editing', 'selected');
    if (options.focus !== false && focused) row?.querySelector('[data-edit-label]')?.focus({ preventScroll: true });
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
        preserveScroll();
        htmx.ajax('GET', '/today', '#today-content');
    }
    refreshWorkspace();
}
function showTaskStatus(editor, message) {
    const status = editor.form.querySelector('.task-save-status');
    if (status) status.textContent = message;
}
function scheduleTaskSave(editor) {
    editor.dirty = true;
    clearTimeout(editor.timer);
    editor.timer = setTimeout(() => saveTaskEditor(editor), 600);
}
// Saves run one at a time; a save asked for while one is running waits for
// it and then sends whatever is newest.
async function saveTaskEditor(editor, options = {}) {
    clearTimeout(editor.timer);
    editor.timer = null;
    while (editor.saving) await editor.saving;
    if (!editor.dirty) return true;
    const form = editor.form;
    if (!form.elements.text.value.trim()) {
        showTaskStatus(editor, 'Enter a task title to save.');
        return false;
    }
    const body = new URLSearchParams(new FormData(form));
    editor.dirty = false;
    showTaskStatus(editor, 'Saving…');
    editor.saving = (async () => {
        try {
            const response = await fetch('/task/save', {
                method: 'POST',
                headers: { 'HX-Request': 'true', 'Content-Type': 'application/x-www-form-urlencoded' },
                body,
                keepalive: !!options.keepalive
            });
            const text = await response.text();
            let data = null;
            try {
                data = JSON.parse(text);
            } catch (_) {}
            if (response.status === 409 && data?.task) {
                editor.dirty = true;
                showTaskConflict(editor, data.task);
                return false;
            }
            if (!response.ok || data?.status !== 'saved') {
                editor.dirty = true;
                const reason = response.status === 401 ? 'Session expired. Sign in again to save.' : text.trim();
                showTaskStatus(editor, response.status < 500 && reason.length < 120 ? reason : 'Not saved. Try again.');
                if (!form.isConnected) showToast('That task edit was not saved.');
                return false;
            }
            form.elements.revision.value = data.revision;
            editor.savedText = body.get('text');
            editor.changed = true;
            refreshSidebar();
            showTaskStatus(editor, editor.dirty ? '' : 'Saved');
            return true;
        } catch (_) {
            editor.dirty = true;
            showTaskStatus(editor, 'Offline. Your changes will save when you edit again online.');
            if (!form.isConnected) showToast('Offline. That task edit was not saved.');
            return false;
        }
    })();
    const saved = await editor.saving;
    editor.saving = null;
    return saved;
}
function showTaskConflict(editor, latest) {
    const form = editor.form;
    if (!form.isConnected) {
        showToast('That task changed on another device, so your last edit was not saved.');
        return;
    }
    showTaskStatus(editor, '');
    form.elements.revision.value = latest.revision;
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
    copy.textContent = 'Latest task: ' + latest.text;
    const keep = taskAction('Keep mine', () => {
        conflict.remove();
        saveTaskEditor(editor);
    });
    const use = taskAction('Use latest', () => {
        conflict.remove();
        const fields = form.elements;
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
    form.querySelectorAll('.chip').forEach(chip => {
        const field = chip.querySelector('input,select');
        if (field?.name !== 'list') chip.classList.toggle('is-set', !!field?.value);
    });
    const due = form.elements.due_date;
    const text = form.querySelector('.chip-date .chip-text');
    if (due && text) {
        // Same wording as the list rows ("Sep 30").
        const [y, m, d] = due.value.split('-').map(Number);
        text.textContent = due.value
            ? new Date(y, m - 1, d).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
            : 'Date';
    }
}
// The date field is invisible over its chip; open the picker on a click,
// which desktop browsers otherwise only do from their own calendar icon.
document.addEventListener('click', e => {
    const input = e.target.closest?.('.chip-date input');
    try {
        input?.showPicker?.();
    } catch (_) {}
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
function restoreTaskEditor() {
    const editor = taskEditor;
    if (!editor || editor.form.isConnected) return;
    const row = taskRow(editor.id, editor.scope);
    if (!row) {
        // A refresh reloads open projects' bodies after the list itself; wait
        // for them before deciding the task is gone.
        const loading = document.querySelector('.project-group[open] > [data-lazy-project]:not([data-loaded])');
        if (editor.scope !== 'todos' || !loading) closeTaskEditor({ focus: false });
        return;
    }
    attachTaskEditor(editor, row);
    const field = editor.focus?.name && editor.form.elements.namedItem(editor.focus.name);
    if (field) {
        field.focus({ preventScroll: true });
        if (typeof editor.focus.start === 'number') field.setSelectionRange?.(editor.focus.start, editor.focus.end);
    }
    editor.focus = null;
}
document.addEventListener('input', e => {
    if (!taskEditor || !taskEditor.form.contains(e.target)) return;
    taskEditor.form.querySelector('.edit-conflict')?.remove();
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
    if (taskEditor?.dirty) saveTaskEditor(taskEditor, { keepalive: true });
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
    const pane = document.getElementById('detail-pane');
    if (isDesktop()) {
        if (!pane.hidden) closeDetail();
        if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
        showProjectPage(id);
        window.scrollTo({ top: 0, behavior: 'auto' });
        setDestination({ view: 'todos', project: id });
        return;
    }
    document.body.dataset.workspaceSection = 'tasks';
    writeLocal('desktop-workspace', 'tasks');
    switchMode('todos');
    if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
    const el = document.getElementById('project-' + id);
    if (el) {
        showWorkspaceContaining(el);
        expandAncestors(el);
        el.open = true;
    }
    setDestination({ view: 'todos', project: id });
    openProjectDetails(id);
}
function openProjectDetails(id) {
    detailSelection = { kind: 'project', id };
    showDetail('/projects/detail?id=' + id);
}
// A project page's title is not a disclosure: keep it open.
document.addEventListener('click', e => {
    const summary = e.target.closest?.('[data-project-page] > summary');
    if (summary && isDesktop()) e.preventDefault();
});
function closeDetail() {
    document.getElementById('detail-pane').hidden = true;
    document.querySelector('.task-layout').classList.remove('has-detail');
    detailSelection = null;
    setDestination({ view: 'todos' });
}

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
    const name = await showPrompt('Rename ' + kind, el.dataset.name);
    if (name) structureAction(kind, id, 'rename', { name });
}
async function deleteHeading(id, el) {
    closePopupMenus();
    if (await showConfirm('Delete the heading “' + el.dataset.name + '”? Its tasks stay in the project.'))
        structureAction('heading', id, 'delete');
}
async function structureAction(kind, id, action, extra = {}) {
    await htmx.ajax('POST', kind === 'project' ? '/projects/action' : '/headings/action', {
        target: '#todo-items',
        values: { id, action, ...extra }
    });
}
// --- Archive ---
// One view for everything archived, shown in the task area. Back returns to
// wherever it was opened from.
let archiveReturn = null;
function openArchive(kind = 'todo') {
    if (!document.getElementById('detail-pane').hidden) closeDetail();
    const mode = document.body.dataset.mode || 'todos';
    if (currentWorkspaceSection() !== 'archive' || mode !== 'todos')
        archiveReturn = { mode, workspace: currentWorkspaceSection(), project: document.body.dataset.projectId };
    document.body.dataset.workspaceSection = 'archive';
    switchMode('todos');
    setDestination({ view: 'archive', kind });
    htmx.ajax('GET', '/archive?kind=' + encodeURIComponent(kind), '#todo-items');
}
function hideArchive() {
    const back = archiveReturn || { mode: 'todos', workspace: 'tasks' };
    archiveReturn = null;
    refreshWorkspace().then(() => {
        if (back.mode !== 'todos') {
            document.body.dataset.workspaceSection = readLocal('desktop-workspace') || 'tasks';
            if (back.mode === 'today') openToday();
            else if (back.mode === 'settings') openSettings();
            else switchMode(back.mode);
        } else if (back.workspace === 'project' && back.project) showProjectPage(Number(back.project));
        else switchWorkspaceSection(back.workspace === 'archive' ? 'tasks' : back.workspace, { preserveScroll: true });
    });
}
function openSettings() {
    switchMode('settings');
    setDestination({ view: 'settings' });
    htmx.ajax('GET', '/settings', '#settings-content');
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
    // Habits and weekly reviews were retired; keep old links working.
    else if (['today', 'habits', 'review'].includes(q.get('view'))) openToday();
    else if (q.get('view') === 'settings') openSettings();
    else if (q.get('view') === 'archive') openArchive(q.get('kind') || 'todo');
    else if (q.get('note')) openNoteResult(Number(q.get('note')));
    else if (q.get('view')) switchMode(q.get('view'));
    else switchMode('today');
}
document.body.addEventListener('htmx:beforeSwap', e => {
    if (e.detail.target.id === 'detail-pane') {
        const config = e.detail.requestConfig;
        const pane = document.getElementById('detail-pane');
        if (
            config?.verb === 'get' &&
            pane.dataset.url &&
            e.detail.xhr.responseURL !== new URL(pane.dataset.url, location.origin).href
        ) {
            e.detail.shouldSwap = false;
            return;
        }
    }
    if (e.detail.target.id === 'todo-items') {
        rememberWorkspace();
        const form = e.detail.requestConfig?.elt;
        if (e.detail.xhr.status < 300 && form?.classList.contains('capture-form')) {
            const values = workspaceState.forms[form.id];
            if (values && values.text === e.detail.requestConfig.parameters.text) {
                values.text = '';
                values.due_date = '';
            }
        }
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
                focused: document.activeElement === editor
            };
    }
});
document.body.addEventListener('htmx:beforeRequest', e => {
    const form = e.detail.elt;
    if (form?.getAttribute('hx-post') === '/todos/toggle') {
        const row = form.closest('.todo-item');
        if (row) {
            form._toggleDelta = row.classList.contains('done') ? -1 : 1;
            form._toggleProject = row.closest('.project-group');
            form._toggleSection = row.closest('.workspace-section');
            if (form._toggleDelta > 0) row.classList.add('is-completing');
        }
    }
    if (!form?.classList.contains('capture-form')) return;
    const input = form.elements.text;
    const text = input?.value.trim();
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
    input.focus({ preventScroll: true });
});
function restoreFailedCapture(e) {
    const form = e.detail.elt;
    if (!form?.classList.contains('capture-form')) return;
    const pending = document.querySelector('[data-capture-request="' + form.dataset.pendingRow + '"]');
    if (pending && !form.elements.text.value)
        form.elements.text.value = pending.querySelector('.task-label')?.textContent || '';
    pending?.remove();
    form.closest('.task-group')?.querySelector('.quiet-empty')?.removeAttribute('hidden');
    saveCaptureDraft(form);
}
document.body.addEventListener('htmx:responseError', restoreFailedCapture);
document.body.addEventListener('htmx:sendError', restoreFailedCapture);
function restoreFailedCompletion(e) {
    e.detail.elt?.closest('.todo-item')?.classList.remove('is-completing');
}
document.body.addEventListener('htmx:responseError', restoreFailedCompletion);
document.body.addEventListener('htmx:sendError', restoreFailedCompletion);
document.body.addEventListener('htmx:afterRequest', e => {
    const form = e.detail.elt;
    if (form?.classList.contains('capture-form') && e.detail.successful) removeLocal(captureDraftKey(form));
});
document.addEventListener('keydown', e => {
    const input = e.target.closest?.('.capture-form input[name="text"]');
    if (!input || e.key !== 'Enter' || e.isComposing) return;
    e.preventDefault();
    input.form.requestSubmit();
});
document.body.addEventListener('htmx:beforeSwap', keepTaskEditor);
document.body.addEventListener('htmx:afterSwap', e => {
    const id = e.detail.target.id;
    if (id === 'todo-items') {
        restoreWorkspace();
        if (detailSelection?.kind === 'project') showDetail('/projects/detail?id=' + detailSelection.id);
    }
    // After the list has reopened its sections and started loading projects.
    restoreTaskEditor();
    const toggleForm = e.detail.requestConfig?.elt;
    if (toggleForm?.getAttribute('hx-post') === '/todos/toggle') {
        const bump = (container, selector, ratio = false) => {
            const el = container?.querySelector(selector);
            if (!el) return;
            if (ratio) {
                const parts = el.textContent.split('/').map(Number);
                if (parts.length === 2 && !parts.some(Number.isNaN)) {
                    parts[0] += toggleForm._toggleDelta;
                    el.textContent = parts.join('/');
                    container.querySelector('.project-progress')?.style.setProperty('--progress', parts[0]);
                }
            } else {
                const value = Number(el.textContent);
                if (!Number.isNaN(value)) el.textContent = String(value - toggleForm._toggleDelta);
            }
        };
        bump(toggleForm._toggleProject, ':scope > summary .section-count', true);
        if (toggleForm._toggleSection?.id === 'section-tasks' && !toggleForm._toggleProject)
            bump(toggleForm._toggleSection, '.inbox-group > .workspace-subheading .section-count');
        else if (!toggleForm._toggleProject)
            bump(toggleForm._toggleSection, ':scope > summary .section-count');
        restoreSections();
        restoreCaptureDrafts(e.detail.target);
    }
    if (e.detail.target.matches?.('[data-lazy-project]')) {
        e.detail.target.dataset.loaded = '1';
        restoreSections(e.detail.target);
        restoreCaptureDrafts(e.detail.target);
    }
    if (id === 'detail-pane') {
        const pane = document.getElementById('detail-pane');
        if (pane.dataset.focusAfterLoad) {
            delete pane.dataset.focusAfterLoad;
            pane.focus({ preventScroll: true });
        }
    }
    if (id === 'notes-content') {
        initNoteEditor();
        const editor = document.getElementById('note-editor');
        if (editor && document.body.dataset.mode === 'notes')
            setDestination({ view: 'notes', note: editor.dataset.noteId });
        if (editor && noteViewState?.id === editor.dataset.noteId) {
            editor.setSelectionRange(noteViewState.start, noteViewState.end);
            editor.scrollTop = noteViewState.scroll;
            if (noteViewState.focused) editor.focus({ preventScroll: true });
        }
        noteViewState = null;
    }
    if (id === 'settings-content') {
        document.body.dataset.timezone =
            document.querySelector('#schedule-form [name=timezone]')?.value || document.body.dataset.timezone;
        updateTopbarStat();
        preparePush();
    }
});
document.body.addEventListener('sbWorkspaceChanged', e => {
    refreshWorkspace();
    if (e.detail.message) showToast(e.detail.message);
});
document.addEventListener('input', e => {
    const capture = e.target.closest('.capture-form');
    if (capture) saveCaptureDraft(capture);
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
        else if (document.body.dataset.mode === 'todos' && !document.getElementById('detail-pane').hidden)
            closeDetail();
    }
});
document.addEventListener('click', e =>
    document.querySelectorAll('.row-menu[open],.app-menu[open]').forEach(d => {
        if (!d.contains(e.target)) d.open = false;
    })
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

document.body.addEventListener('htmx:responseError', e => {
    if (e.detail.target?.id !== 'detail-pane') return;
    const pane = document.getElementById('detail-pane');
    pane.textContent = 'This item is unavailable. It may have been archived.';
    const close = document.createElement('button');
    close.textContent = 'Close details';
    close.className = 'btn';
    close.onclick = closeDetail;
    pane.appendChild(close);
});
