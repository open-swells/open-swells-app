const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const template = fs.readFileSync(path.join(__dirname, '../templates/components/forecastclock.html'), 'utf8');
const script = template.match(/<script>([\s\S]*?)<\/script>/)[1].replace('{{.}}', '"America/Los_Angeles"');
const window = {};
vm.runInNewContext(script, { window, Intl, Date, document: { addEventListener() {} } });
const clock = window.createForecastClock;

test('forecast date and hour follow the beach across the date line', () => {
    const instant = new Date('2026-07-20T03:30:00Z');
    assert.equal(clock('America/Los_Angeles').dateKey(instant), '2026-07-19');
    assert.equal(clock('America/Los_Angeles').hour(instant), 20.5);
    assert.equal(clock('Pacific/Auckland').dateKey(instant), '2026-07-20');
    assert.equal(clock('Pacific/Auckland').hour(instant), 15.5);
});

test('day boundaries account for daylight saving changes', () => {
    const la = clock('America/Los_Angeles');
    for (const [key, start, end, hours] of [
        ['2026-03-08', '2026-03-08T08:00:00.000Z', '2026-03-09T07:00:00.000Z', 23],
        ['2026-11-01', '2026-11-01T07:00:00.000Z', '2026-11-02T08:00:00.000Z', 25],
    ]) {
        const bounds = la.dayBounds(key);
        assert.equal(bounds.start.toISOString(), start);
        assert.equal(bounds.end.toISOString(), end);
        assert.equal((bounds.end - bounds.start) / 3600000, hours);
        assert.equal(la.hour(la.atHour(key, 23)), 23);
    }
});

test('header shows ahead, behind, equal and fractional differences', () => {
    const previous = process.env.TZ;
    process.env.TZ = 'America/Los_Angeles';
    try {
        const now = new Date('2026-07-20T12:00:00Z');
        assert.match(clock('America/New_York').note(now), /3h ahead of you$/);
        assert.match(clock('Pacific/Honolulu').note(now), /3h behind you$/);
        assert.match(clock('America/Los_Angeles').note(now), /same time as you$/);
        assert.match(clock('Asia/Kolkata').note(now), /12h 30m ahead of you$/);
        assert.match(clock('America/New_York').note(new Date('2026-12-20T12:00:00Z')), /3h ahead of you$/);
    } finally {
        if (previous === undefined) delete process.env.TZ;
        else process.env.TZ = previous;
    }
});

test('midnight gaps keep day labels and neighboring day boundaries consistent', () => {
    for (const [zone, key, previous, expected] of [
        ['America/Santiago', '2026-09-06', '2026-09-05', '2026-09-06T04:00:00.000Z'],
        ['America/Havana', '2026-03-08', '2026-03-07', '2026-03-08T05:00:00.000Z'],
    ]) {
        const local = clock(zone);
        const bounds = local.dayBounds(key);
        assert.equal(bounds.start.toISOString(), expected);
        assert.equal(local.dateKey(bounds.start), key);
        assert.equal(local.hour(bounds.start), 1);
        assert.equal(local.dayBounds(previous).end.toISOString(), expected);
        assert.equal((bounds.end - bounds.start) / 3600000, 23);
        assert.equal((local.dayBounds(previous).end - local.dayBounds(previous).start) / 3600000, 24);
    }
});

test('southern hemisphere and fractional-hour DST day boundaries', () => {
    for (const [zone, key, hours, start] of [
        ['Australia/Adelaide', '2026-04-05', 25, '2026-04-04T13:30:00.000Z'],
        ['Australia/Adelaide', '2026-10-04', 23, '2026-10-03T14:30:00.000Z'],
        ['Pacific/Chatham', '2026-04-05', 25, '2026-04-04T10:15:00.000Z'],
        ['Australia/Lord_Howe', '2026-04-05', 24.5, '2026-04-04T13:00:00.000Z'],
    ]) {
        const local = clock(zone);
        const bounds = local.dayBounds(key);
        assert.equal(bounds.start.toISOString(), start);
        assert.equal(local.dateKey(bounds.start), key);
        assert.equal(local.hour(bounds.start), 0);
        assert.equal((bounds.end - bounds.start) / 3600000, hours);
    }
});
