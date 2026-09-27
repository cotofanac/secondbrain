// State is limited to presentation and unsaved drafts; task truth stays on the server.
let detailSelection = null;
let pendingFocus = null;
let workspaceState = null;
let noteViewState = null;
let archiveCategory = 'todo';
let detailReturnFocus = null;
let detailReturnMode = null;
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
    const row = el?.closest('.todo-item');
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
// Details save as you type. Leaving sends anything still waiting instead of
// blocking; the reply is ignored once another task (or none) is shown.
function canLeaveDetail() {
    const form = document.getElementById('task-detail-form');
    if (form?.dataset.dirty && form.checkValidity()) htmx.trigger(form, 'submit');
    return true;
}
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
async function openTask(id, options = {}) {
    if (!id || !canLeaveDetail()) return;
    detailReturnMode = options.returnMode || null;
    detailReturnFocus = document.querySelector('#task-' + id + ' .task-label');
    closePopupMenus();
    switchMode('todos');
    detailSelection = { kind: 'task', id };
    if (document.querySelector('#todo-items .archive-header')) {
        pendingFocus = id;
        await refreshWorkspace();
    }
    revealTask(id);
    setDestination({ view: 'todos', task: id });
    showDetail('/task/detail?id=' + id);
}
async function openProject(id) {
    if (!canLeaveDetail()) return;
    document.body.dataset.workspaceSection = 'tasks';
    writeLocal('desktop-workspace', 'tasks');
    switchMode('todos');
    if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
    detailSelection = { kind: 'project', id };
    const el = document.getElementById('project-' + id);
    if (el) {
        showWorkspaceContaining(el);
        expandAncestors(el);
        el.open = true;
    }
    setDestination({ view: 'todos', project: id });
    showDetail('/projects/detail?id=' + id);
}
function openTodayTask(id) {
    return openTask(id, { returnMode: 'today' });
}
function closeDetail() {
    if (!canLeaveDetail()) return;
    document.getElementById('detail-pane').hidden = true;
    document.querySelector('.task-layout').classList.remove('has-detail');
    detailSelection = null;
    const returnMode = detailReturnMode;
    detailReturnMode = null;
    const target = detailReturnFocus;
    detailReturnFocus = null;
    if (returnMode === 'today') {
        openToday();
        return;
    }
    setDestination({ view: 'todos' });
    target?.focus({ preventScroll: true });
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
function filterStageOptions(form) {
    if (!form?.elements.project_id) return;
    const project = form.elements.project_id.value;
    const select = form.elements.stage_id;
    for (const option of select.options) {
        option.hidden = !!option.dataset.project && option.dataset.project !== project;
        option.disabled = option.hidden;
    }
    if (select.selectedOptions[0]?.disabled) select.value = '0';
}
async function structureCreate(kind, parent) {
    const name = await showPrompt(kind === 'project' ? 'New project' : 'New stage');
    if (name) structureAction(kind, 0, 'create', { name, project_id: parent });
}
async function structureRename(kind, id, el) {
    const name = await showPrompt('Rename ' + kind, el.dataset.name);
    if (name) structureAction(kind, id, 'rename', { name });
}
async function structureAction(kind, id, action, extra = {}) {
    await htmx.ajax('POST', kind === 'project' ? '/projects/action' : '/stages/action', {
        target: '#todo-items',
        values: { id, action, ...extra }
    });
}
function openTaskArchive(category) {
    if (!canLeaveDetail()) return;
    closeDetail();
    archiveCategory = category;
    htmx.ajax('GET', '/todos/archive?category=' + category, '#todo-items');
}
function hideArchive() {
    refreshWorkspace();
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
    else if (q.get('note')) openNoteResult(Number(q.get('note')));
    else if (q.get('stage')) openStageResult(Number(q.get('stage')));
    else if (q.get('view')) switchMode(q.get('view'));
    else switchMode('today');
}
async function openStageResult(id, projectID = 0) {
    closeSearch();
    document.body.dataset.workspaceSection = 'tasks';
    writeLocal('desktop-workspace', 'tasks');
    switchMode('todos');
    if (document.querySelector('#todo-items .archive-header')) await refreshWorkspace();
    setDestination({ view: 'todos', stage: id });
    let project = projectID
        ? document.getElementById('project-' + projectID)
        : [...document.querySelectorAll('.project-group')].find(el =>
              el.dataset.stageIds?.trim().split(/\s+/).includes(String(id))
          );
    if (project) {
        project.open = true;
        await loadProjectBody(project);
    }
    const el = document.getElementById('stage-' + id);
    if (el) {
        showWorkspaceContaining(el);
        expandAncestors(el);
        el.open = true;
        el.scrollIntoView({ block: 'nearest' });
    }
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
        if (config?.path === '/task/save') {
            // Never re-render the form under the cursor: take the new revision
            // from the reply and keep whatever has been typed since.
            e.detail.shouldSwap = false;
            const form = document.getElementById('task-detail-form');
            if (e.detail.xhr.status !== 200 || !form || String(config.parameters.id) !== form.elements.id.value)
                return;
            const saved = new DOMParser()
                .parseFromString(e.detail.xhr.responseText, 'text/html')
                .querySelector('#task-detail-form [name=revision]');
            if (saved) form.elements.revision.value = saved.value;
            const current = Object.fromEntries(new FormData(form));
            const newer = Object.entries(current).some(
                ([key, value]) => key !== 'revision' && String(config.parameters[key] ?? '') !== value
            );
            if (!newer) delete form.dataset.dirty;
            form.querySelector('.detail-save-status').textContent = newer ? 'Saving…' : 'Saved';
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
    if (form?.id === 'task-detail-form') {
        const status = form.querySelector('.detail-save-status');
        if (!form.checkValidity()) {
            e.preventDefault();
            status.textContent = 'Enter a task title to save.';
            return;
        }
        status.textContent = 'Saving…';
    }
    if (form?.getAttribute('hx-post') === '/todos/toggle') {
        const row = form.closest('.todo-item');
        if (row) {
            form._toggleDelta = row.classList.contains('done') ? -1 : 1;
            form._toggleStage = row.closest('.stage-group');
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
document.body.addEventListener('htmx:afterSwap', e => {
    const id = e.detail.target.id;
    if (id === 'todo-items') {
        restoreWorkspace();
        if (detailSelection?.kind === 'task')
            document.getElementById('task-' + detailSelection.id)?.classList.add('selected');
        if (detailSelection?.kind === 'project') showDetail('/projects/detail?id=' + detailSelection.id);
    }
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
        bump(toggleForm._toggleStage, ':scope > summary .section-count', true);
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
        filterStageOptions(document.getElementById('task-detail-form'));
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
    const form = e.target.closest('#task-detail-form');
    if (form) {
        form.dataset.dirty = '1';
        form.querySelector('.detail-save-status').textContent = '';
    }
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
        if (document.body.dataset.mode === 'todos' && !document.getElementById('detail-pane').hidden)
            closeDetail();
    }
});
document.addEventListener('click', e =>
    document.querySelectorAll('.row-menu[open],.app-menu[open]').forEach(d => {
        if (!d.contains(e.target)) d.open = false;
    })
);
window.addEventListener('beforeunload', e => {
    if (
        document.getElementById('note-editor')?.dataset.dirty ||
        document.getElementById('task-detail-form')?.dataset.dirty
    ) {
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
    if (e.detail.xhr.status === 409) {
        let data;
        try {
            data = JSON.parse(e.detail.xhr.responseText);
        } catch (_) {
            return;
        }
        const form = pane.querySelector('#task-detail-form');
        if (!data.task) return;
        if (!form || form.elements.id.value !== String(data.task.id)) {
            showToast('That task changed on another device, so your last edit was not saved.');
            return;
        }
        form.dataset.dirty = '1';
        form.elements.revision.value = data.task.revision;
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
        const latest = document.createElement('p');
        latest.textContent = 'Latest task: ' + data.task.text;
        const keep = taskAction('Keep mine', () => {
            conflict.remove();
            form.requestSubmit();
        });
        const use = taskAction('Use latest', () => {
            delete form.dataset.dirty;
            showDetail('/task/detail?id=' + form.elements.id.value);
        });
        conflict.append(title, latest, keep, use);
        keep.focus();
        return;
    }
    const status = pane.querySelector('#task-detail-form .detail-save-status');
    if (status) status.textContent = 'Not saved. Check the details and try again.';
    if (!pane.querySelector('form')) {
        pane.textContent = 'This item is unavailable. It may have been archived.';
        const close = document.createElement('button');
        close.textContent = 'Close details';
        close.className = 'btn';
        close.onclick = closeDetail;
        pane.appendChild(close);
    }
});
document.body.addEventListener('htmx:sendError', e => {
    if (e.detail.elt?.id !== 'task-detail-form') return;
    const status = e.detail.elt.querySelector('.detail-save-status');
    if (status) status.textContent = 'Offline. Your changes will save when you edit again online.';
});
