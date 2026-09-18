// --- Mode switching (Tasks / Notes / Habits) ---
const MODE_TITLES = { todos: 'Tasks', today: 'Today', notes: 'Notes', habits: 'Habits', settings: 'Reminders & notifications' };
const WORKSPACE_TITLES = { tasks: 'Tasks', groceries: 'Groceries', shopping: 'Buys' };
let secondaryReturnDestination = { mode: 'todos', workspace: 'tasks' };
function currentWorkspaceSection() {
    return WORKSPACE_TITLES[document.body.dataset.workspaceSection] ? document.body.dataset.workspaceSection : 'tasks';
}
function updateNavigation(mode) {
    const workspace = currentWorkspaceSection();
    document.querySelectorAll('.bottom-nav-btn').forEach(button => {
        const active = mode === 'todos'
            ? button.dataset.mode === 'todos' && button.dataset.workspace === workspace
            : button.dataset.mode === mode;
        button.classList.toggle('active', active);
        if (active) button.setAttribute('aria-current', 'page');
        else button.removeAttribute('aria-current');
    });
}
function switchMode(mode) {
    if (!MODE_TITLES[mode]) mode='todos';
    const previousMode = document.body.dataset.mode || 'todos';
    if (mode === 'settings' && previousMode !== 'settings') {
        secondaryReturnDestination = { mode: previousMode, workspace: currentWorkspaceSection() };
    }
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
    title.textContent=mode === 'todos' ? WORKSPACE_TITLES[currentWorkspaceSection()] : MODE_TITLES[mode];
    title.classList.toggle('can-switch-workspace', mode === 'todos' || mode === 'today');
    title.setAttribute('aria-label', mode === 'todos' ? 'Choose task list, current list '+WORKSPACE_TITLES[currentWorkspaceSection()] : mode === 'today' ? 'Choose destination, current view Today' : MODE_TITLES[mode]);
    const back = document.getElementById('topbar-back');
    if (back) {
        const secondary = mode === 'settings';
        back.hidden = !secondary;
        back.setAttribute('aria-label', 'Back to ' + (secondaryReturnDestination.mode === 'todos' ? WORKSPACE_TITLES[secondaryReturnDestination.workspace] : MODE_TITLES[secondaryReturnDestination.mode]));
    }
    document.querySelectorAll('.app-menu').forEach(d=>d.open=false);
    document.body.dataset.mode=mode;
    if(typeof setDestination==='function')setDestination({view:mode});
    updateTopbarStat();
}

function leaveSecondaryView() {
    if (secondaryReturnDestination.mode === 'todos') {
        switchWorkspaceSection(secondaryReturnDestination.workspace, { preserveScroll: true });
        return;
    }
    switchMode(secondaryReturnDestination.mode);
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
    const today = menu.querySelector('[data-mode-option="today"]');
    const todaySelected = document.body.dataset.mode === 'today';
    today?.classList.toggle('selected', todaySelected);
    today?.setAttribute('aria-current', todaySelected ? 'page' : 'false');
    openModal(menu, todaySelected ? today : menu.querySelector('[data-workspace-option="'+current+'"]'));
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
    if (mode === 'settings') openSettings();
}
document.addEventListener('click', function(e) {
    if (e.target?.id === 'mobile-workspace-menu') closeMobileWorkspaceSwitcher();
});
document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && !document.getElementById('mobile-workspace-menu')?.hidden) closeMobileWorkspaceSwitcher();
});

function switchWorkspaceSection(section, options = {}) {
    if (!WORKSPACE_TITLES[section]) section = 'tasks';
    if (typeof canLeaveDetail === 'function' && !canLeaveDetail()) return false;
    const pane = document.getElementById('detail-pane');
    if (pane && !pane.hidden && typeof closeDetail === 'function') closeDetail();
    document.body.dataset.workspaceSection = section;
    try { localStorage.setItem('desktop-workspace', section); } catch (_) {}
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
        const archiveOpen = document.getElementById('archive-btn')?.classList.contains('active');
        if (!archiveOpen) {
            const section = document.getElementById('section-' + currentWorkspaceSection());
            const pending = section?.querySelectorAll('.todo-item:not(.done):not(.archived-item)').length || 0;
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
    if (id === 'todo-items' || id === 'habits-content') updateTopbarStat();
    if (id === 'habits-content') updateHabitsBadge();
    const path = e.detail.requestConfig?.path;
    if (path === '/todos/add' || path === '/habits/add') {
        const selector = path === '/todos/add' ? '.todo-item[data-task-id]' : '.habit-item[data-habit-id]';
        const previous = e.detail.target?._itemIDsBeforeSwap || new Set();
        e.detail.target?.querySelectorAll(selector).forEach(item => {
            const itemID = item.dataset.taskId || item.dataset.habitId;
            if (!previous.has(itemID)) item.classList.add('item-entering');
        });
    }
});

document.body.addEventListener('htmx:beforeSwap', function(e) {
    const path = e.detail.requestConfig?.path;
    if (path !== '/todos/add' && path !== '/habits/add') return;
    const selector = path === '/todos/add' ? '.todo-item[data-task-id]' : '.habit-item[data-habit-id]';
    e.detail.target._itemIDsBeforeSwap = new Set([...e.detail.target.querySelectorAll(selector)].map(item => item.dataset.taskId || item.dataset.habitId));
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
    if (xhr?.status === 409 && e.detail.target?.id === 'detail-pane') return;
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
function showHabitDialog(initial = {}) {
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

        labelEl.textContent = initial.id ? 'Edit habit' : 'New habit';
        input.value = initial.name || '';
        targetInput.value = initial.target || '1';
        selectPeriod(initial.period || 'day');
        extra.hidden = false;
        openModal(dialog, input);
        setTimeout(() => input.select(), 0);

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
            closeModal(dialog);
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

async function editHabit(button) {
    const item = button.closest('.habit-item');
    if (!item) return;
    const goal = await showHabitDialog({ id: item.dataset.habitId, name: item.dataset.habitName, period: item.dataset.habitPeriod, target: item.dataset.habitTarget });
    if (!goal) return;
    htmx.ajax('POST', '/habits/update', { target: '#habits-content', values: { id: item.dataset.habitId, ...goal } });
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
    if(mode==='today') { preserveScroll(); htmx.ajax('GET','/today','#today-content'); }
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

// --- Inline habit editing ---
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

    let done = false, saving = false;
    async function save() {
        if (done || saving) return;
        const newName = input.value.trim();
        if (!newName || newName === originalName) { done = true; input.replaceWith(spanEl); spanEl.focus(); return; }
        saving = true;
        input.disabled = true;
        try {
            await htmx.ajax('POST', '/habits/rename', { target: '#habits-content', swap: 'innerHTML', values: { id, name: newName } });
            done = true;
        } catch (_) {
            saving = false;
            input.disabled = false;
            input.setAttribute('aria-invalid', 'true');
            input.focus();
            showToast('Could not rename habit. Your edit is still here.');
        }
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

// Pointer users can edit directly; long press remains as a forgiving mobile
// gesture for people already accustomed to it.
const LONG_PRESS_MS = 500;
const LONG_PRESS_SLOP_PX = 10;

function editableLabel(target) {
    if (!target || typeof target.closest !== 'function') return null;
    const label = target.closest('.habit-name');
    if (!label) return null;
    const row = label.closest('.habit-item');
    if (!row || row.classList.contains('archived-item')) return null;
    return label;
}

function beginInlineEdit(label) {
    return startHabitRename(label);
}

document.addEventListener('dblclick', function(e) {
    const label = editableLabel(e.target);
    if (label) beginInlineEdit(label);
});
document.addEventListener('click', function(e) {
    const label = e.target.closest?.('[data-action="rename-habit"]');
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

document.addEventListener('keydown', function(e) {
    if (e.key !== 'Escape') return;
    closeSearch();
    const pickerPanel = document.getElementById('note-picker-panel');
    if (pickerPanel) pickerPanel.hidden = true;
});


document.addEventListener('DOMContentLoaded', function() {
    let savedWorkspace = 'tasks';
    try { savedWorkspace = localStorage.getItem('desktop-workspace') || 'tasks'; } catch (_) {}
    document.body.dataset.workspaceSection = WORKSPACE_TITLES[savedWorkspace] ? savedWorkspace : 'tasks';
    initNoteEditor();
    initCalendarBtn();
    setupInactivityLogout();
    updateTopbarStat();
    updateHabitsBadge();
    setupMobileKeyboard();

});
