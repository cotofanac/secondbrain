// --- Declarative handlers ---
// Markup names a function with data-call (and optionally data-args, a JSON
// array, and data-event, default "click") instead of inline on* attributes,
// so the CSP can refuse inline script. "$el", "$value" and "$form" in
// data-args stand for the element, its value and its form. Only functions
// listed here can be called.
const CALLABLE = new Set([
    'chooseMobileUtility', 'chooseMobileWorkspace', 'closeNotePicker', 'closeSearch', 'createNote',
    'archiveNote', 'closeTaskEditor', 'deleteHeading', 'disablePush', 'enablePush', 'filterNotes', 'hideArchive',
    'doLogout', 'openArchive', 'openMobileWorkspaceSwitcher',
    'openNoteResult', 'openProject', 'openProjectResult', 'openSearch',
    'openTask', 'openToday', 'openTodayTask', 'openTodoResult', 'renameNote',
    'selectNote', 'showNotesArchive', 'structureAction', 'structureCreate', 'structureRename',
    'switchMode', 'switchWorkspaceSection', 'testPush', 'toggleNotePicker'
]);
function dispatchCall(e) {
    const el = e.target.closest?.('[data-call]');
    if (!el || (el.dataset.event || 'click') !== e.type) return;
    const fn = CALLABLE.has(el.dataset.call) ? window[el.dataset.call] : null;
    if (typeof fn !== 'function') return;
    let args;
    try { args = JSON.parse(el.dataset.args || '[]'); } catch (_) { return; }
    fn(...args.map(a => a === '$el' ? el : a === '$value' ? el.value : a === '$form' ? el.form : a));
}
['click', 'change', 'input'].forEach(type => document.addEventListener(type, dispatchCall));

// --- Mode switching (Today / Tasks / Notes) ---
const MODE_TITLES = { todos: 'Tasks', today: 'Today', notes: 'Notes' };
const WORKSPACE_TITLES = { tasks: 'Tasks', groceries: 'Groceries', shopping: 'Buys', project: 'Project', archive: 'Archive' };
// The sidebar, project pages and counts beside each list appear from this width.
const isDesktop = () => matchMedia('(min-width: 1024px)').matches;
function currentProjectName() {
    const id = document.body.dataset.projectId;
    return document.querySelector('#project-' + id + ' > summary .project-name')?.textContent || 'Project';
}
function workspaceTitle(section = currentWorkspaceSection()) {
    return section === 'project' ? currentProjectName() : WORKSPACE_TITLES[section];
}
function currentWorkspaceSection() {
    return WORKSPACE_TITLES[document.body.dataset.workspaceSection] ? document.body.dataset.workspaceSection : 'tasks';
}
function updateNavigation(mode) {
    const workspace = currentWorkspaceSection();
    const mark = (button, active) => {
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'page');
        else button.removeAttribute('aria-current');
    };
    document.querySelectorAll('.bottom-nav-btn').forEach(button => mark(button, mode === 'todos'
        ? button.dataset.mode === 'todos' && button.dataset.workspace === workspace
        : button.dataset.mode === mode));
    document.querySelectorAll('.sidebar-project').forEach(button => mark(button, mode === 'todos' && workspace === 'project'
        && button.dataset.projectId === document.body.dataset.projectId));
}
function switchMode(mode) {
    if (!MODE_TITLES[mode]) mode='todos';
    const previousMode = document.body.dataset.mode || 'todos';
    flushPendingNoteSave();
    updateNavigation(mode);
    // Each mode owns an independent page-length layout. Keeping the previous
    // mode's scroll offset while swapping a long view for a short one makes
    // iOS Safari clamp the document as soon as the long view is hidden; fixed
    // bottom navigation can then be left in the old composited position. The
    // reset must happen before changing which view participates in layout.
    if (mode !== previousMode) window.scrollTo(0, 0);
    document.querySelectorAll('.view').forEach(v => v.classList.toggle('active',v.id===mode+'-view'));
    // Safari may defer scroll clamping until its next layout pass. Correct it
    // once more there so the visual viewport and fixed chrome cannot diverge.
    if (mode !== previousMode) requestAnimationFrame(() => window.scrollTo(0, 0));
    const activeView = document.getElementById(mode + '-view');
    if (mode !== previousMode && activeView) {
        activeView.classList.remove('view-entering');
        void activeView.offsetWidth;
        activeView.classList.add('view-entering');
    }
    const title = document.getElementById('topbar-title');
    title.textContent=mode === 'todos' ? workspaceTitle() : MODE_TITLES[mode];
    title.classList.toggle('can-switch-workspace', mode === 'todos' || mode === 'today');
    title.setAttribute('aria-label', mode === 'todos' ? 'Choose task list, current list '+workspaceTitle() : mode === 'today' ? 'Choose destination, current view Today' : MODE_TITLES[mode]);
    document.querySelectorAll('.app-menu').forEach(d=>d.open=false);
    document.body.dataset.mode=mode;
    if(typeof setDestination==='function')setDestination({view:mode});
    updateTopbarStat();
}

function openMobileWorkspaceSwitcher() {
    if (document.body.dataset.mode !== 'todos' && document.body.dataset.mode !== 'today') return;
    const menu = document.getElementById('mobile-workspace-menu');
    if (!menu) return;
    const current = currentWorkspaceSection();
    menu.querySelectorAll('[data-workspace-option]').forEach(button => {
        const selected = document.body.dataset.mode === 'todos' && button.dataset.workspaceOption === current;
        button.classList.toggle('selected', selected);
        button.setAttribute('aria-current', selected ? 'page' : 'false');
    });
    const mode = document.body.dataset.mode;
    menu.querySelectorAll('[data-mode-option]').forEach(button => {
        const option = button.dataset.modeOption;
        const selected = option === 'archive' ? mode === 'todos' && current === 'archive' : mode === option;
        button.classList.toggle('selected', selected);
        button.setAttribute('aria-current', selected ? 'page' : 'false');
    });
    // The sheet repeats the counts shown beside each list in the sidebar.
    menu.querySelectorAll('[data-count-from]').forEach(count => {
        const source = document.getElementById(count.dataset.countFrom);
        count.textContent = (source?.querySelector('.nav-total') || source)?.textContent.trim() || '';
        count.classList.toggle('has-overdue', !!source?.classList.contains('has-overdue'));
    });
    openModal(menu, menu.querySelector('.selected') || menu.querySelector('button'));
}
function closeMobileWorkspaceSwitcher() {
    closeModal(document.getElementById('mobile-workspace-menu'));
}
function chooseMobileWorkspace(section) {
    closeMobileWorkspaceSwitcher();
    switchWorkspaceSection(section);
}
function chooseMobileUtility(mode) {
    closeMobileWorkspaceSwitcher();
    if (mode === 'today') openToday();
    if (mode === 'archive') openArchive('todo');
}
document.addEventListener('click', function(e) {
    if (e.target?.id === 'mobile-workspace-menu') closeMobileWorkspaceSwitcher();
});
document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && !document.getElementById('mobile-workspace-menu')?.hidden) closeMobileWorkspaceSwitcher();
});

function switchWorkspaceSection(section, options = {}) {
    if (!['tasks', 'groceries', 'shopping'].includes(section)) section = 'tasks';
    document.body.dataset.workspaceSection = section;
    try { localStorage.setItem('desktop-workspace', section); } catch (_) {}
    if (document.querySelector('#todo-items .archive-header')) refreshWorkspace();
    if (typeof applyProjectPage === 'function') applyProjectPage();
    const target = document.getElementById('section-' + section);
    if (target) target.open = true;
    switchMode('todos');
    if (target) {
        target.open = true;
        target.classList.remove('workspace-entering');
        void target.offsetWidth;
        target.classList.add('workspace-entering');
        setTimeout(() => target.classList.remove('workspace-entering'), 220);
    }
    if (!options.preserveScroll) {
        if (matchMedia('(max-width: 767px)').matches && target) {
            const behavior = matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth';
            requestAnimationFrame(() => target.scrollIntoView({ block: 'start', behavior }));
        } else window.scrollTo({ top: 0, behavior: 'auto' });
    }
    return true;
}

function updateTopbarStat() {
    const stat = document.getElementById('topbar-stat');
    if (!stat) return;

    const now = new Date();
    const date = now.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric', timeZone: document.body.dataset.timezone || undefined });

    const activeMode = document.body.dataset.mode || 'todos';
    let count = '';

    if (activeMode === 'todos') {
        const workspace = currentWorkspaceSection();
        const section = workspace === 'project' ? document.querySelector('[data-project-page]') : document.getElementById('section-' + workspace);
        const pending = section?.querySelectorAll('.todo-item:not(.done):not(.archived-item)').length || 0;
        if (pending > 0) count = pending + ' left';
    }

    const text = count ? date + ' · ' + count : date;
    stat.textContent = document.body.dataset.offlineCopy ? 'Offline · ' + text : text;
}

function setupMobileKeyboard() {
    const viewport = window.visualViewport;
    if (!viewport) return;
    let baseline = Math.max(viewport.height, document.documentElement.clientHeight);
    const update = function() {
        if (!document.activeElement?.matches('input:not([type=checkbox]):not([type=radio]), textarea')) baseline = Math.max(baseline, viewport.height);
        const editingText = document.activeElement?.matches('input:not([type=checkbox]):not([type=radio]), textarea');
        document.body.classList.toggle('keyboard-open', !!editingText && baseline - viewport.height > 120);
    };
    viewport.addEventListener('resize', update);
    viewport.addEventListener('scroll', update);
    document.addEventListener('focusin', update);
    document.addEventListener('focusout', () => setTimeout(update, 0));
}

let modalReturnFocus = null;
function openModal(container, initialFocus) {
    if (!container) return;
    modalReturnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    document.getElementById('app')?.setAttribute('inert', '');
    document.getElementById('bottom-nav')?.setAttribute('inert', '');
    container.hidden = false;
    container.setAttribute('aria-hidden', 'false');
    setTimeout(() => initialFocus?.focus(), 0);
}
function closeModal(container) {
    if (!container) return;
    container.hidden = true;
    container.setAttribute('aria-hidden', 'true');
    document.getElementById('app')?.removeAttribute('inert');
    document.getElementById('bottom-nav')?.removeAttribute('inert');
    const target = modalReturnFocus;
    modalReturnFocus = null;
    if (target?.isConnected) target.focus({ preventScroll: true });
}
document.addEventListener('keydown', function(e) {
    if (e.key !== 'Tab') return;
    const modal = document.querySelector('.custom-dialog:not([hidden]), .inactivity-warning:not([hidden]), .mobile-workspace-menu:not([hidden])');
    if (!modal) return;
    const focusable = [...modal.querySelectorAll('button:not([disabled]):not([hidden]), input:not([disabled]):not([hidden]), select:not([disabled]):not([hidden]), textarea:not([disabled]):not([hidden]), [tabindex]:not([tabindex="-1"])')].filter(el => el.getClientRects().length);
    if (!focusable.length) { e.preventDefault(); return; }
    const first = focusable[0], last = focusable[focusable.length - 1];
    if (!modal.contains(document.activeElement)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
    else if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
});

document.body.addEventListener('htmx:afterSwap', function(e) {
    const id = e.detail.target?.id;
    if (id === 'todo-items') updateTopbarStat();
    if (e.detail.requestConfig?.path === '/todos/add') {
        const previous = e.detail.target?._itemIDsBeforeSwap || new Set();
        e.detail.target?.querySelectorAll('.todo-item[data-task-id]').forEach(item => {
            if (!previous.has(item.dataset.taskId)) item.classList.add('item-entering');
        });
    }
});

document.body.addEventListener('htmx:beforeSwap', function(e) {
    if (e.detail.requestConfig?.path !== '/todos/add') return;
    e.detail.target._itemIDsBeforeSwap = new Set([...e.detail.target.querySelectorAll('.todo-item[data-task-id]')].map(item => item.dataset.taskId));
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
// request was rejected ("Cannot delete the last note")
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
    showToast(d.kind === 'project' ? 'Project archived' : 'Task archived', {
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
    if (kind === 'project') { structureAction(kind,id,'restore'); return; }
    htmx.ajax('POST', '/todos/restore', {
        target: '#todo-items',
        swap: 'innerHTML',
        values: { id: id, return: 'list' }
    });
}

// /logout is POST-only, so submit a form rather than navigating (a GET).
function doLogout() {
    const f = document.createElement('form');
    f.method = 'POST';
    f.action = '/logout';
    document.body.appendChild(f);
    f.submit();
}

// --- Archive ---
async function showNotesArchive() {
    closeNoteMenu();
    closeNotePicker();
    if (!await saveBeforeNoteAction()) return;
    openArchive('notes');
}
// Restoring a note from the archive puts it back in the note list.
document.body.addEventListener('sbNotesChanged', function() {
    htmx.ajax('GET', '/notes', { target: '#notes-content', source: document.getElementById('notes-content') });
});

// --- Sidebar counts and projects ---
// Any change to tasks, lists or projects can move a count, so the server
// re-renders them after each one (out-of-band, into their places in the nav).
let sidebarTimer = null;
function refreshSidebar() {
    clearTimeout(sidebarTimer);
    sidebarTimer = setTimeout(() => {
        const nav = document.getElementById('bottom-nav');
        if (!nav || document.body.dataset.offlineCopy) return;
        htmx.ajax('GET', '/sidebar', { source: nav, target: nav, swap: 'none' })
            .then(() => updateNavigation(document.body.dataset.mode || 'todos'));
    }, 150);
}
document.body.addEventListener('htmx:afterRequest', function(e) {
    const path = e.detail.requestConfig?.path || '';
    if (!e.detail.successful || e.detail.requestConfig?.verb === 'get') return;
    if (/^\/(todos|projects|headings|today)\//.test(path)) refreshSidebar();
});

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
    if(mode==='today') { preserveScroll(); htmx.ajax('GET','/today','#today-content'); }
    refreshSidebar(); // counts move with the date too
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
    openModal(warning, document.getElementById('inactivity-stay-btn'));
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

    closeModal(warning);

    if (inactivityCountdownTimer) {
        clearInterval(inactivityCountdownTimer);
        inactivityCountdownTimer = null;
    }
}

// --- Global search ---
function openSearch() {
    const overlay = document.getElementById('search-overlay');
    if (overlay) { overlay.inert=false;overlay.setAttribute('aria-hidden','false');overlay.classList.add('visible'); }
    const input = document.getElementById('search-input');
    if (input) { input.value = ''; input.focus(); }
    const results = document.getElementById('search-results');
    if (results) results.innerHTML = '';
}

function closeSearch() {
    const overlay = document.getElementById('search-overlay');
    if (overlay) { overlay.classList.remove('visible');overlay.inert=true;overlay.setAttribute('aria-hidden','true'); }
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
function openProjectResult(id) { closeSearch(); openProject(id); }

document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    closeSearch();
    closeNotePicker();
});


document.addEventListener('DOMContentLoaded', function() {
    let savedWorkspace = 'tasks';
    try { savedWorkspace = localStorage.getItem('desktop-workspace') || 'tasks'; } catch (_) {}
    document.body.dataset.workspaceSection = ['tasks', 'groceries', 'shopping'].includes(savedWorkspace) ? savedWorkspace : 'tasks';
    initNoteEditor();
    if (document.body.dataset.offlineCopy) {
        // Served by the service worker from the last page loaded online.
        showToast('Offline. Showing the last copy saved on this device; changes will not save.');
        window.addEventListener('online', () => showToast('Back online', { label: 'Reload', onClick: () => location.reload() }), { once: true });
    }
    setupInactivityLogout();
    updateTopbarStat();
    setupMobileKeyboard();

});
