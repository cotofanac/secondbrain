// Calendar dates are plain ISO dates; elapsed milliseconds never define a day.
function calendarISO(instant, zone) {
    return instant.toLocaleDateString('en-CA', { timeZone: zone });
}
function validCalendarDate(value) {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
    const date = new Date(value + 'T00:00:00Z');
    return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
}
function relativeDate(value, today = todayISO()) {
    const [y, m, d] = value.split('-').map(Number);
    const [ty, tm, td] = today.split('-').map(Number);
    const days = Math.round((Date.UTC(y, m - 1, d) - Date.UTC(ty, tm - 1, td)) / 86400000);
    const date = new Date(Date.UTC(y, m - 1, d));
    const format = options => date.toLocaleDateString('en-US', { timeZone: 'UTC', ...options });
    if (days === 0) return 'Today';
    if (days === 1) return 'Tomorrow';
    if (days === -1) return 'Yesterday';
    if (days >= 2 && days <= 6) return format({ weekday: 'short' });
    if (days >= 7 && days <= 13) return `in ${days} days`;
    if (days <= -2 && days >= -13) return `${-days} days ago`;
    if (y === ty) return format({ month: 'short', day: 'numeric' });
    return format({ month: 'short', day: 'numeric', year: 'numeric' });
}
const WEEKDAY_NAMES = ['sunday', 'monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday'];
const MONTH_NAMES = [
    'january',
    'february',
    'march',
    'april',
    'may',
    'june',
    'july',
    'august',
    'september',
    'october',
    'november',
    'december',
];
const NUMBER_WORDS = {
    a: 1,
    an: 1,
    one: 1,
    two: 2,
    three: 3,
    four: 4,
    five: 5,
    six: 6,
};
function todayISO() {
    return calendarISO(new Date(), document.body.dataset.timezone || undefined);
}
function addDaysISO(iso, n) {
    const d = new Date(iso + 'T00:00:00Z');
    d.setUTCDate(d.getUTCDate() + n);
    return d.toISOString().slice(0, 10);
}
// A name matches when it is at least three letters of the full word ("fri", "sept").
function matchName(word, names) {
    return word.length >= 3 ? names.findIndex(name => name.startsWith(word)) : -1;
}
function parseDatePhrase(phrase, today) {
    const dow = new Date(today + 'T00:00:00Z').getUTCDay();
    if (phrase === 'today') return today;
    if (['tomorrow', 'tmr', 'tmrw'].includes(phrase)) return addDaysISO(today, 1);
    if (phrase === 'next week') return addDaysISO(today, (8 - dow) % 7 || 7);
    let m = phrase.match(/^in (\d{1,3}|a|an|one|two|three|four|five|six) (day|days|week|weeks)$/);
    if (m) {
        const n = NUMBER_WORDS[m[1]] || Number(m[1]);
        return addDaysISO(today, m[2].startsWith('week') ? n * 7 : n);
    }
    // A weekday means its next one, never today: "fri" on a Friday is a week out.
    const weekday = matchName(phrase.replace(/^next /, ''), WEEKDAY_NAMES);
    if (weekday >= 0) return addDaysISO(today, (weekday - dow + 7) % 7 || 7);
    m = phrase.match(/^(\d{1,2}) ([a-z]+)$/) || phrase.match(/^([a-z]+) (\d{1,2})$/);
    if (m) {
        const [day, month] = /^\d/.test(m[1]) ? [Number(m[1]), m[2]] : [Number(m[2]), m[1]];
        const index = matchName(month, MONTH_NAMES);
        if (index < 0) return null;
        let year = Number(today.slice(0, 4));
        const date = y => new Date(Date.UTC(y, index, day)).toISOString().slice(0, 10);
        if (new Date(Date.UTC(year, index, day)).getUTCDate() !== day) return null;
        if (date(year) < today) year++;
        return validCalendarDate(date(year)) && new Date(date(year) + 'T00:00:00Z').getUTCDate() === day
            ? date(year)
            : null;
    }
    if (validCalendarDate(phrase)) return phrase;
    return null;
}
// typedDate returns { title, due } for a title ending in a date, else null.
function typedDate(text, today = todayISO()) {
    const words = text.trim().split(/\s+/);
    for (let n = Math.min(3, words.length - 1); n >= 1; n--) {
        const due = parseDatePhrase(words.slice(-n).join(' ').toLowerCase(), today);
        if (!due) continue;
        let rest = words.slice(0, -n);
        if (rest.length > 1 && ['on', 'by', 'due'].includes(rest[rest.length - 1].toLowerCase()))
            rest = rest.slice(0, -1);
        return { title: rest.join(' '), due };
    }
    return null;
}

if (typeof module !== 'undefined')
    module.exports = {
        relativeDate,
        typedDate,
        parseDatePhrase,
        calendarISO,
        validCalendarDate,
    };
