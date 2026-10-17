#!/usr/bin/env node
// Import local Markdown files as notes into a running Secondbrain.
//
//   SB_URL=https://your-host PASSCODE=12345678 node scripts/push-note.cjs notes/*.md
//
// Uses POST /notes/import, the same endpoint as the app's "Import Markdown…"
// link and drag-and-drop. Each file becomes a note named after the file; a
// name already in use (active or archived) is skipped, never overwritten, so
// re-running is safe. Any client can do the same after logging in:
//
//   curl -b session=… -F file=@note.md https://your-host/notes/import
//
// The reply is JSON: {"imported":[{file,title,id}], "skipped":[{file,title,reason}]}.
// No dependencies; Node 18+.
'use strict';

const fs = require('fs');
const path = require('path');

const base = (process.env.SB_URL || '').replace(/\/+$/, '');
const passcode = process.env.PASSCODE || '';
const files = process.argv.slice(2);
// Requests are capped at 2 MB; send files in batches comfortably under it.
const BATCH_BYTES = 1_500_000;

if (!base || !passcode || files.length === 0) {
    console.error('usage: SB_URL=https://host PASSCODE=xxxxxxxx node scripts/push-note.cjs file.md [...]');
    process.exit(2);
}

async function login() {
    const res = await fetch(base + '/login', {
        method: 'POST',
        body: new URLSearchParams({ passcode }),
        redirect: 'manual',
    });
    const cookies = res.headers.getSetCookie ? res.headers.getSetCookie() : [res.headers.get('set-cookie') || ''];
    const session = cookies.map(c => c.split(';')[0]).find(c => c.startsWith('session=') && c.length > 'session='.length);
    if (!session) {
        throw new Error(`login failed (HTTP ${res.status}); check SB_URL and PASSCODE`);
    }
    return session;
}

async function post(session, route, body) {
    return fetch(base + route, {
        method: 'POST',
        headers: { Cookie: session },
        body,
    });
}

function batches(list) {
    const out = [];
    let current = [];
    let size = 0;
    for (const file of list) {
        const bytes = fs.statSync(file).size;
        if (current.length && size + bytes > BATCH_BYTES) {
            out.push(current);
            current = [];
            size = 0;
        }
        current.push(file);
        size += bytes;
    }
    if (current.length) out.push(current);
    return out;
}

async function importBatch(session, batch) {
    const body = new FormData();
    for (const file of batch) {
        body.append('file', new Blob([fs.readFileSync(file)], { type: 'text/markdown' }), path.basename(file));
    }
    const res = await post(session, '/notes/import', body);
    const text = await res.text();
    let result;
    try {
        result = JSON.parse(text);
    } catch (_) {
        throw new Error(text.trim() || `import failed (HTTP ${res.status})`);
    }
    if (!res.ok) throw new Error(result.error || `import failed (HTTP ${res.status})`);
    return result;
}

(async () => {
    const session = await login();
    let failed = 0;
    for (const batch of batches(files)) {
        try {
            const { imported, skipped } = await importBatch(session, batch);
            for (const note of imported) console.log(`ok    ${note.file} -> "${note.title}"`);
            for (const note of skipped) console.log(`skip  ${note.file}: ${note.reason}`);
        } catch (err) {
            failed += batch.length;
            for (const file of batch) console.error(`fail  ${file}: ${err.message}`);
        }
    }
    await post(session, '/logout', new URLSearchParams()).catch(() => {});
    process.exit(failed ? 1 : 0);
})().catch(err => {
    console.error(err.message);
    process.exit(1);
});
