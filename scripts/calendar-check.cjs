const assert = require('node:assert/strict');
const fixtures = require('../testdata/calendar.json');
const calendar = require('../static/calendar.js');
for (const item of fixtures.relative) assert.equal(calendar.relativeDate(item.date, item.today), item.label);
for (const item of fixtures.typed) {
    const parsed = calendar.typedDate(item.text, item.today);
    assert.equal(parsed?.due || '', item.date, item.text);
    if (parsed) assert.equal(parsed.title, item.title);
}
for (const item of fixtures.zones) assert.equal(calendar.calendarISO(new Date(item.instant), item.zone), item.date);
for (const date of fixtures.invalid) assert.equal(calendar.validCalendarDate(date), false, date);
console.log('Shared calendar fixtures passed.');
