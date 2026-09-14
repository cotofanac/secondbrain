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

// Habit rename is reachable two ways: double-click with a mouse, press-and-hold
// on touch. Task titles use the direct single-tap editor in workspace.js.
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
    initNoteEditor();
    initCalendarBtn();
    setupInactivityLogout();
    updateTopbarStat();
    updateHabitsBadge();
    setupMobileKeyboard();

});
