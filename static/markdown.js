// --- Markdown for notes ---
// A small renderer that builds DOM nodes (never HTML strings). It covers what
// notes use: headings, paragraphs (a newline is a line break), lists with task
// boxes, quotes, fenced code, tables, rules, **bold**, *italic*, ~~struck~~,
// `code`, links and bare URLs. Links are limited to http(s) and mailto.
// Blocks and list items carry data-line, the source line they start on, so a
// tap can put the caret there and a task box can flip its own line.
// Every scan is bounded or remembers failed searches, so a 1 MB note stays linear.

const MD_FENCE = /^ {0,3}(`{3,}|~{3,})/;
const MD_HEADING = /^ {0,3}(#{1,6})(?:[ \t]+|$)/;
const MD_RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const MD_QUOTE = /^ {0,3}> ?/;
const MD_ITEM = /^([ \t]*)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/;
const MD_TASK = /^\[([ xX])\](?:[ \t]+(.*))?$/;
// Checked with blanks removed, which keeps the match linear.
const MD_TABLE_RULE = /^\|?:?-+:?(?:\|:?-+:?)*\|?$/;
const MD_SAFE_URL = /^(?:https?:\/\/|mailto:)/i;
const MD_WORD = /[\p{L}\p{N}]/u;
const MD_PUNCT = /[!-/:-@[-`{-~]/;
const MD_SCAN_LIMIT = 2000;

function renderMarkdown(source) {
    const lines = source.split('\n').map((text, n) => ({ text: text.replace(/\r$/, ''), n }));
    const fragment = document.createDocumentFragment();
    mdBlocks(lines, fragment);
    return fragment;
}

function mdEl(tag, line, className) {
    const el = document.createElement(tag);
    if (line !== undefined) el.dataset.line = String(line);
    if (className) el.className = className;
    return el;
}

function mdIndent(text) {
    let width = 0;
    for (const c of text) {
        if (c === ' ') width++;
        else if (c === '\t') width += 4;
        else break;
    }
    return width;
}

function mdTableStart(lines, i) {
    return (
        lines[i].text.includes('|') &&
        i + 1 < lines.length &&
        MD_TABLE_RULE.test(lines[i + 1].text.replace(/[ \t]/g, ''))
    );
}

// Whether a line opens a block of its own (and so ends a paragraph).
function mdOpensBlock(lines, i) {
    const text = lines[i].text;
    return (
        MD_FENCE.test(text) ||
        MD_HEADING.test(text) ||
        MD_RULE.test(text) ||
        MD_QUOTE.test(text) ||
        mdItemInterrupts(text) ||
        mdTableStart(lines, i)
    );
}

// Only a bullet or a list starting at 1, with text, interrupts a paragraph,
// so a line like "2026. was a good year" stays part of it.
function mdItemInterrupts(text) {
    const item = text.match(MD_ITEM);
    return !!item && !!item[3] && (!/\d/.test(item[2]) || parseInt(item[2], 10) === 1);
}

function mdBlocks(lines, parent) {
    let i = 0;
    while (i < lines.length) {
        const { text, n } = lines[i];
        if (!text.trim()) {
            i++;
            continue;
        }
        const fence = text.match(MD_FENCE);
        if (fence) {
            i = mdCode(lines, i, fence[1], parent);
            continue;
        }
        const heading = text.match(MD_HEADING);
        if (heading) {
            // The note's title is the page heading, so # starts at h2.
            const el = mdEl('h' + Math.min(heading[1].length + 1, 6), n, 'md-heading');
            mdInline(text.slice(heading[0].length).replace(/[ \t]+#+[ \t]*$/, '').trim(), el);
            parent.append(el);
            i++;
            continue;
        }
        if (MD_RULE.test(text)) {
            parent.append(mdEl('hr', n));
            i++;
            continue;
        }
        if (MD_QUOTE.test(text)) {
            const quoted = [];
            while (i < lines.length && MD_QUOTE.test(lines[i].text)) {
                quoted.push({ text: lines[i].text.replace(MD_QUOTE, ''), n: lines[i].n });
                i++;
            }
            const el = mdEl('blockquote', n);
            mdBlocks(quoted, el);
            parent.append(el);
            continue;
        }
        if (MD_ITEM.test(text)) {
            i = mdList(lines, i, parent);
            continue;
        }
        if (mdTableStart(lines, i)) {
            i = mdTable(lines, i, parent);
            continue;
        }
        const el = mdEl('p', n);
        let first = true;
        while (i < lines.length && lines[i].text.trim() && (first || !mdOpensBlock(lines, i))) {
            if (!first) el.append(document.createElement('br'));
            mdInline(lines[i].text.trim(), el);
            first = false;
            i++;
        }
        parent.append(el);
    }
}

function mdCode(lines, i, marker, parent) {
    const pre = mdEl('pre', lines[i].n);
    const code = document.createElement('code');
    const body = [];
    i++;
    while (i < lines.length) {
        const close = lines[i].text.match(MD_FENCE);
        if (close && close[1][0] === marker[0] && close[1].length >= marker.length && !lines[i].text.slice(close[0].length).trim()) {
            i++;
            break;
        }
        body.push(lines[i].text);
        i++;
    }
    code.textContent = body.join('\n');
    pre.append(code);
    parent.append(pre);
    return i;
}

function mdList(lines, i, parent) {
    // Collect the items first: a line that is not an item continues the one
    // above it, and a blank line ends the list unless an item or an indented
    // line follows.
    const items = [];
    while (i < lines.length) {
        const { text, n } = lines[i];
        const item = text.match(MD_ITEM);
        if (item && !MD_RULE.test(text)) {
            items.push({
                indent: mdIndent(item[1]),
                ordered: /\d/.test(item[2]),
                number: parseInt(item[2], 10),
                lines: [item[3] || ''],
                n,
            });
            i++;
            continue;
        }
        if (!text.trim()) {
            let next = i + 1;
            while (next < lines.length && !lines[next].text.trim()) next++;
            if (next < lines.length && (MD_ITEM.test(lines[next].text) || mdIndent(lines[next].text) >= 2)) {
                i = next;
                continue;
            }
            break;
        }
        if (mdOpensBlock(lines, i) && mdIndent(text) < 2) break;
        items[items.length - 1].lines.push(text.trim());
        i++;
    }

    // Then nest them by indent.
    const stack = [];
    for (const item of items) {
        while (stack.length && stack[stack.length - 1].indent > item.indent) stack.pop();
        let top = stack[stack.length - 1];
        if (!top || top.indent < item.indent || top.ordered !== item.ordered) {
            const list = mdEl(item.ordered ? 'ol' : 'ul', item.n);
            if (item.ordered && item.number !== 1) list.start = item.number;
            if (top && top.indent < item.indent && top.list.lastElementChild) {
                top.list.lastElementChild.append(list);
            } else if (top) {
                top.list.after(list);
                stack.pop();
            } else {
                parent.append(list);
            }
            top = { indent: item.indent, ordered: item.ordered, list };
            stack.push(top);
        }
        top.list.append(mdItem(item));
    }
    return i;
}

function mdItem(item) {
    const li = mdEl('li', item.n);
    const body = document.createElement('span');
    const task = item.lines[0].match(MD_TASK);
    let first = item.lines[0];
    if (task) {
        li.className = 'md-task';
        const box = document.createElement('input');
        box.type = 'checkbox';
        box.checked = task[1] !== ' ';
        box.dataset.line = String(item.n);
        body.id = 'md-task-' + item.n;
        box.setAttribute('aria-labelledby', body.id);
        if (box.checked) li.classList.add('done');
        li.append(box);
        first = task[2] || '';
    }
    item.lines.forEach((text, index) => {
        if (index) body.append(document.createElement('br'));
        mdInline(index ? text : first, body);
    });
    li.append(body);
    return li;
}

function mdSplitRow(text) {
    let row = text.trim();
    if (row.startsWith('|')) row = row.slice(1);
    if (row.endsWith('|') && !row.endsWith('\\|')) row = row.slice(0, -1);
    const cells = [];
    let cell = '';
    for (let i = 0; i < row.length; i++) {
        if (row[i] === '\\' && row[i + 1] === '|') {
            cell += '|';
            i++;
        } else if (row[i] === '|') {
            cells.push(cell.trim());
            cell = '';
        } else cell += row[i];
    }
    cells.push(cell.trim());
    return cells;
}

function mdTable(lines, i, parent) {
    const wrap = mdEl('div', lines[i].n, 'md-table-wrap');
    const table = document.createElement('table');
    const align = mdSplitRow(lines[i + 1].text).map(rule =>
        rule.endsWith(':') ? (rule.startsWith(':') ? 'center' : 'right') : '',
    );
    const addRow = (section, text, tag) => {
        const tr = document.createElement('tr');
        mdSplitRow(text).forEach((value, index) => {
            const cell = document.createElement(tag);
            if (align[index]) cell.style.textAlign = align[index];
            mdInline(value, cell);
            tr.append(cell);
        });
        section.append(tr);
    };
    const head = document.createElement('thead');
    addRow(head, lines[i].text, 'th');
    const body = document.createElement('tbody');
    i += 2;
    while (i < lines.length && lines[i].text.trim() && lines[i].text.includes('|')) {
        addRow(body, lines[i].text, 'td');
        i++;
    }
    table.append(head, body);
    wrap.append(table);
    parent.append(wrap);
    return i;
}

// The index of ch in text at or after from, looking at most MD_SCAN_LIMIT
// characters. found remembers the last hit for each character; callers ask
// with a growing from, so a line full of < or [ is scanned once.
function mdFind(text, ch, from, found) {
    let at = found.get(ch);
    if (at === undefined || (at !== -1 && at < from)) {
        at = text.indexOf(ch, from);
        found.set(ch, at);
    }
    return at !== -1 && at - from < MD_SCAN_LIMIT ? at : -1;
}

function mdLink(href, parent) {
    const a = document.createElement('a');
    a.href = href;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    parent.append(a);
    return a;
}

// Where an emphasis run that opens at i ends, or -1. Searches that found
// nothing are remembered in dead: a later opener cannot find more.
function mdCloser(text, i, marker, dead) {
    if (dead.has(marker)) return -1;
    const single = marker.length === 1;
    let j = text.indexOf(marker, i + marker.length);
    while (j !== -1) {
        const before = text[j - 1];
        const after = text[j + marker.length];
        const valid =
            j > i + marker.length &&
            !/\s/.test(before) &&
            !(single && (before === marker || after === marker)) &&
            !(marker[0] === '_' && after && MD_WORD.test(after));
        if (valid) return j;
        j = text.indexOf(marker, j + 1);
    }
    dead.add(marker);
    return -1;
}

function mdInline(text, parent, inLink = false) {
    const dead = new Set();
    const found = new Map();
    let plain = '';
    const flush = () => {
        if (plain) parent.append(plain);
        plain = '';
    };
    let i = 0;
    while (i < text.length) {
        const c = text[i];
        if (c === '\\' && MD_PUNCT.test(text[i + 1] || '')) {
            plain += text[i + 1];
            i += 2;
            continue;
        }
        if (c === '`') {
            let run = 1;
            while (text[i + run] === '`') run++;
            const fence = '`'.repeat(run);
            const close = dead.has(fence) ? -1 : text.indexOf(fence, i + run);
            if (close === -1) {
                dead.add(fence);
                plain += fence;
                i += run;
                continue;
            }
            flush();
            const code = document.createElement('code');
            code.textContent = text.slice(i + run, close);
            parent.append(code);
            i = close + run;
            continue;
        }
        if (!inLink && c === '<') {
            const close = mdFind(text, '>', i + 1, found);
            const url = close === -1 ? '' : text.slice(i + 1, close);
            if (MD_SAFE_URL.test(url) && !/\s/.test(url)) {
                flush();
                mdLink(url, parent).textContent = url;
                i = close + 1;
                continue;
            }
        }
        const image = c === '!' && text[i + 1] === '[';
        if (!inLink && (c === '[' || image)) {
            const start = i + (image ? 2 : 1);
            const labelEnd = mdFind(text, ']', start, found);
            const urlEnd = labelEnd !== -1 && text[labelEnd + 1] === '(' ? mdFind(text, ')', labelEnd + 2, found) : -1;
            if (urlEnd !== -1) {
                const url = text.slice(labelEnd + 2, urlEnd).trim().split(/\s/)[0].replace(/^<|>$/g, '');
                if (MD_SAFE_URL.test(url)) {
                    flush();
                    const label = text.slice(start, labelEnd);
                    // Images from other sites are blocked by the page's CSP; link to them.
                    if (image) mdLink(url, parent).textContent = label || url;
                    else mdInline(label || url, mdLink(url, parent), true);
                    i = urlEnd + 1;
                    continue;
                }
            }
        }
        if (!inLink && c === 'h' && (i === 0 || /[\s(]/.test(text[i - 1])) && /^https?:\/\//.test(text.slice(i, i + 8))) {
            let end = i;
            while (end < text.length && !/[\s<]/.test(text[end])) end++;
            let url = text.slice(i, end);
            while (/[.,;:!?'"*_]$/.test(url) || (url.endsWith(')') && !url.includes('('))) url = url.slice(0, -1);
            flush();
            mdLink(url, parent).textContent = url;
            i += url.length;
            continue;
        }
        const pair = text.slice(i, i + 2);
        const marker = pair === '**' || pair === '__' || pair === '~~' ? pair : c === '*' || c === '_' ? c : '';
        if (marker) {
            const next = text[i + marker.length];
            const opens =
                next &&
                !/\s/.test(next) &&
                !(marker[0] === '_' && i > 0 && MD_WORD.test(text[i - 1]));
            const end = opens ? mdCloser(text, i, marker, dead) : -1;
            if (end !== -1) {
                flush();
                const el = document.createElement(marker === '~~' ? 'del' : marker.length === 2 ? 'strong' : 'em');
                mdInline(text.slice(i + marker.length, end), el, inLink);
                parent.append(el);
                i = end + marker.length;
                continue;
            }
            plain += marker;
            i += marker.length;
            continue;
        }
        plain += c;
        i++;
    }
    flush();
}
