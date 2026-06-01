// --- Mode switching (Tasks / Notes / Habits) ---
function switchMode(mode) {
    document.querySelectorAll('.mode-btn').forEach(b => b.classList.remove('active'));
    document.querySelector(`[data-mode="${mode}"]`).classList.add('active');

    document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
    document.getElementById(mode + '-view').classList.add('active');

    if (mode === 'notes') {
        const editor = document.getElementById('note-editor');
        if (editor) editor.focus();
    }
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

    autoResize(editor);
    editor.addEventListener('input', () => autoResize(editor));
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

function createNote() {
    const title = prompt('Note name:');
    if (!title || !title.trim()) return;
    htmx.ajax('POST', '/notes/create', {
        target: '#notes-content',
        values: { title: title.trim() }
    });
}

function deleteNote() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;
    htmx.ajax('POST', '/notes/delete', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId }
    });
}

function renameNote() {
    const editor = document.getElementById('note-editor');
    if (!editor) return;
    const label = document.querySelector('.note-picker-label');
    const current = label ? label.textContent.trim() : '';
    const title = prompt('New name:', current);
    if (!title || !title.trim() || title.trim() === current) return;
    htmx.ajax('POST', '/notes/rename', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId, title: title.trim() }
    });
}

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js?v=20260528b');
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

function addHabit() {
    const name = prompt('Habit name:');
    if (!name || !name.trim()) return;
    htmx.ajax('POST', '/habits/add', {
        target: '#habits-content',
        values: { name: name.trim() }
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
            window.location.href = '/logout';
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
        }
    });

    document.body.addEventListener('htmx:afterRequest', function() {
        hideInactivityWarning();
        startInactivityTimers(timeoutMs);
    });
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
        window.location.href = '/logout';
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

// Pull-down-from-top on mobile
(function() {
    let touchStartY = 0;
    let triggered = false;
    document.addEventListener('touchstart', function(e) {
        touchStartY = e.touches[0].clientY;
        triggered = false;
    }, { passive: true });
    document.addEventListener('touchmove', function(e) {
        if (triggered || window.scrollY > 0) return;
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
});
