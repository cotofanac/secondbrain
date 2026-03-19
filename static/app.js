// --- Mode switching (Tasks / Notes) ---
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

// --- Tab switching (Groceries / To-Do) ---
function switchTab(category) {
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    document.querySelector(`[data-cat="${category}"]`).classList.add('active');

    document.getElementById('add-category').value = category;

    const dateInput = document.getElementById('add-date');
    dateInput.style.display = category === 'todo' ? 'block' : 'none';

    // Reset archive view
    document.getElementById('add-form').style.display = '';
    const archiveBtn = document.getElementById('archive-btn');
    if (archiveBtn) archiveBtn.classList.remove('active');

    htmx.ajax('GET', '/todos?category=' + category, '#todo-items');
}

// --- Scroll collapse for tabs + add form ---
(function() {
    let lastScrollY = 0;
    const threshold = 30;
    const todosView = document.getElementById('todos-view');

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

    editor.addEventListener('input', function() {
        clearTimeout(saveTimer);
        showNoteStatus('Unsaved...');
        saveTimer = setTimeout(() => saveCurrentNote(), 800);
    });

    // Auto-resize
    autoResize(editor);
    editor.addEventListener('input', () => autoResize(editor));
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

    fetch('/notes/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: 'id=' + encodeURIComponent(id) + '&content=' + encodeURIComponent(content)
    }).then(r => {
        if (r.ok) {
            showNoteStatus('Saved');
            setTimeout(() => showNoteStatus(''), 2000);
        } else {
            showNoteStatus('Error saving');
        }
    }).catch(() => showNoteStatus('Offline - not saved'));
}

function showNoteStatus(msg) {
    const el = document.getElementById('note-status');
    if (el) el.textContent = msg;
}

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
    const select = document.getElementById('note-select');
    const current = select.options[select.selectedIndex].text;
    const title = prompt('New name:', current);
    if (!title || !title.trim() || title.trim() === current) return;
    htmx.ajax('POST', '/notes/rename', {
        target: '#notes-content',
        values: { id: editor.dataset.noteId, title: title.trim() }
    });
}

// Register service worker
if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js');
}

// --- Archive ---
function showArchive() {
    const cat = document.getElementById('add-category').value;
    htmx.ajax('GET', '/todos/archive?category=' + cat, '#todo-items');
    document.getElementById('add-form').style.display = 'none';
    document.getElementById('archive-btn').classList.add('active');
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

// Init notes editor on page load
document.addEventListener('DOMContentLoaded', function() {
    initNoteEditor();
});
