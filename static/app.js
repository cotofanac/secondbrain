// --- Mode switching (Tasks / Notes / Habits) ---
const MODE_TITLES = { todos: 'Tasks', notes: 'Notes', habits: 'Habits' };

const TAB_CYCLE = ['groceries', 'shopping', 'todo'];

function switchMode(mode) {
    const currentMode = document.querySelector('.bottom-nav-btn.active')?.dataset.mode;

    if (mode === 'todos' && currentMode === 'todos') {
        const activeCat = document.querySelector('#tab-bar .tab.active')?.dataset.cat || 'groceries';
        const idx = TAB_CYCLE.indexOf(activeCat);
        const nextCat = TAB_CYCLE[(idx + 1) % TAB_CYCLE.length];
        switchTab(nextCat, document.querySelector(`[data-cat="${nextCat}"]`));
        return;
    }

    document.querySelectorAll('.bottom-nav-btn').forEach(b => b.classList.remove('active'));
    document.querySelector(`.bottom-nav-btn[data-mode="${mode}"]`).classList.add('active');

    document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
    document.getElementById(mode + '-view').classList.add('active');

    const titleEl = document.getElementById('topbar-title');
    if (titleEl) titleEl.textContent = MODE_TITLES[mode] || mode;

    if (mode === 'notes') {
        const editor = document.getElementById('note-editor');
        if (editor) editor.focus();
    }

    updateTopbarStat();
}

function updateTopbarStat() {
    const stat = document.getElementById('topbar-stat');
    if (!stat) return;

    const now = new Date();
    const date = now.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' });

    const activeMode = document.querySelector('.bottom-nav-btn.active')?.dataset.mode;
    let count = '';

    if (activeMode === 'todos') {
        const archiveOpen = document.getElementById('archive-btn')?.classList.contains('active');
        if (!archiveOpen) {
            const pending = document.querySelectorAll('#todo-items .todo-item:not(.done):not(.archived-item)').length;
            if (pending > 0) count = pending + ' left';
        }
    } else if (activeMode === 'habits') {
        const total = document.querySelectorAll('#habits-content .habit-item').length;
        const done = document.querySelectorAll('#habits-content .habit-item.done').length;
        if (total > 0) count = done + '/' + total;
    }

    stat.textContent = count ? date + ' · ' + count : date;
}

function updateHabitsBadge() {
    const badge = document.getElementById('habits-badge');
    if (!badge) return;
    const items = document.querySelectorAll('#habits-content .habit-item');
    if (items.length === 0) { badge.hidden = true; return; }
    const undone = document.querySelectorAll('#habits-content .habit-item:not(.done)').length;
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

// Surface otherwise-silent HTMX failures (server error or no connection).
document.body.addEventListener('htmx:responseError', function() {
    showToast('Something went wrong — try again');
});
document.body.addEventListener('htmx:sendError', function() {
    showToast('You appear to be offline');
});

let toastTimer;
function showToast(msg) {
    const el = document.getElementById('toast');
    if (!el) return;
    el.textContent = msg;
    el.hidden = false;
    // Reflow so re-triggering the animation works on a visible element.
    void el.offsetWidth;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => {
        el.classList.remove('show');
        setTimeout(() => { el.hidden = true; }, 300);
    }, 3000);
}

// /logout is POST-only, so submit a form rather than navigating (a GET).
function doLogout() {
    const f = document.createElement('form');
    f.method = 'POST';
    f.action = '/logout';
    document.body.appendChild(f);
    f.submit();
}

// --- Tab switching (Shopping List / To-Do / Groceries Checklist) ---
function switchTab(category, el) {
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    const target = el || document.querySelector(`[data-cat="${category}"]`);
    if (target) target.classList.add('active');

    document.getElementById('add-category').value = category;

    const dateInput = document.getElementById('add-date');
    const calendarBtn = document.getElementById('calendar-btn');
    const calendarWrap = document.getElementById('calendar-wrap');
    const isTodo = category === 'todo';
    if (calendarWrap) calendarWrap.style.display = isTodo ? 'inline-flex' : 'none';
    if (!isTodo && dateInput) {
        dateInput.value = '';
        updateCalendarBtn(dateInput, calendarBtn);
    }

    // Reset archive view
    document.getElementById('add-form').style.display = '';
    const archiveBtn = document.getElementById('archive-btn');
    if (archiveBtn) archiveBtn.classList.remove('active');

    const addInput = document.querySelector('.add-input');
    if (addInput) {
        if (category === 'groceries') addInput.setAttribute('list', 'grocery-suggestions');
        else if (category === 'shopping') addInput.setAttribute('list', 'shopping-suggestions');
        else addInput.removeAttribute('list');
    }

    const clearBtn = document.getElementById('clear-checked-btn');
    if (clearBtn) {
        clearBtn.style.visibility = category !== 'todo' ? '' : 'hidden';
        clearBtn.style.pointerEvents = category !== 'todo' ? '' : 'none';
    }

    htmx.ajax('GET', '/todos?category=' + category, '#todo-items');
}

// --- Scroll collapse for tabs + add form ---
(function() {
    let lastScrollY = 0;
    const threshold = 30;
    window.addEventListener('scroll', function() {
        const sy = window.scrollY;
        const tabBar = document.getElementById('tab-bar');
        const addForm = document.getElementById('add-form');

        if (!tabBar || !addForm) return;

        if (sy > threshold && sy > lastScrollY) {
            tabBar.classList.add('collapsed');
            addForm.classList.add('collapsed');
        } else if (sy < lastScrollY - 5 || sy <= threshold) {
            tabBar.classList.remove('collapsed');
            addForm.classList.remove('collapsed');
        }
        lastScrollY = sy;
    }, { passive: true });
})();

// --- Notes ---
let saveTimer = null;

function initNoteEditor() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;

    editor.addEventListener('keydown', function(e) {
        handleNoteEditorKeydown(e, editor);
    });

    editor.addEventListener('input', function() {
        applyInlineNoteCommands(editor);
        clearTimeout(saveTimer);
        showNoteStatus('Unsaved...');
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

function saveCurrentNote() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;

    const id = editor.dataset.noteId;
    const content = editor.value;
    const updatedAt = editor.dataset.updatedAt || '';

    fetch('/notes/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: 'id=' + encodeURIComponent(id)
            + '&content=' + encodeURIComponent(content)
            + '&updated_at=' + encodeURIComponent(updatedAt)
    }).then(r => r.json()).then(data => {
        if (data.status === 'saved') {
            editor.dataset.updatedAt = data.updated_at || updatedAt;
            showNoteStatus('Saved');
            setTimeout(() => showNoteStatus(''), 2000);
        } else if (data.status === 'conflict') {
            showNoteStatus('Conflict — edited on another device. Reload to see latest.');
        } else {
            showNoteStatus('Error saving');
        }
    }).catch(() => showNoteStatus('Offline — not saved'));
}

function showNoteStatus(msg) {
    const el = document.getElementById('note-status');
    if (el) el.textContent = msg;
}

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

function selectNote(id) {
    const panel = document.getElementById('note-picker-panel');
    if (panel) panel.hidden = true;
    htmx.ajax('GET', '/notes?id=' + id, '#notes-content');
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
    htmx.ajax('POST', '/notes/rename', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId, title }
    });
}

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js', { updateViaCache: 'none' });
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

function showNotesArchive() {
    htmx.ajax('GET', '/notes/archive', '#notes-content');
}

function hideNotesArchive() {
    htmx.ajax('GET', '/notes', '#notes-content');
}

async function addHabit() {
    const name = await showPrompt('Habit name');
    if (!name) return;
    htmx.ajax('POST', '/habits/add', {
        target: '#habits-content',
        values: { name }
    });
}

function showHabitsArchive() {
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
    const activeMode = document.querySelector('.bottom-nav-btn.active')?.dataset.mode || 'todos';
    if (activeMode === 'todos') {
        const activeCat = document.querySelector('#tab-bar .tab.active')?.dataset.cat || 'groceries';
        switchTab(activeCat, document.querySelector(`[data-cat="${activeCat}"]`));
    } else if (activeMode === 'notes') {
        const noteId = document.getElementById('note-editor')?.dataset.noteId;
        if (noteId) loadNote(noteId);
    } else if (activeMode === 'habits') {
        htmx.ajax('GET', '/habits', '#habits-content');
    }
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
function startTodoEdit(spanEl) {
    const originalText = spanEl.textContent.trim();
    const item = spanEl.closest('.todo-item');
    if (!item) return;
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
}

function startHabitRename(spanEl) {
    const originalName = spanEl.textContent.trim();
    const item = spanEl.closest('.habit-item');
    if (!item) return;
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
}

document.addEventListener('dblclick', function(e) {
    const todoSpan = e.target.closest('.todo-text');
    if (todoSpan) { startTodoEdit(todoSpan); return; }
    const habitSpan = e.target.closest('.habit-name');
    if (habitSpan) startHabitRename(habitSpan);
});

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
    htmx.ajax('GET', '/notes?id=' + id, '#notes-content');
}

function openTodoResult(category) {
    closeSearch();
    switchMode('todos');
    switchTab(category, document.querySelector('[data-cat="' + category + '"]'));
}

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
