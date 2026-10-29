const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const zlib = require('node:zlib');

const template = fs.readFileSync(path.join(__dirname, '../templates/components/forecastbundle.html'), 'utf8');
const script = template.match(/<script>([\s\S]*?)<\/script>/)[1];
const window = {};
vm.runInNewContext(script, {
    window, fetch, Blob, Response, DecompressionStream, TextDecoder, DataView, Uint8Array, Int16Array, Map, Promise, Math, JSON, Error,
});

const NODATA = -32768;
// Values built inside the vm context come from its own Array/Object.
const plain = value => JSON.parse(JSON.stringify(value));

// Builds a bundle the way pipeline/bundle.py writes one.
// Sets a gzip member's FNAME, like pipeline/bundle.py name_chunk.
function nameChunk(gz, tag) {
    const flagged = Buffer.from(gz.subarray(0, 10));
    flagged[3] |= 0x08;
    return Buffer.concat([flagged, Buffer.from(tag + '\0', 'ascii'), gz.subarray(10)]);
}

function bundleBytes({ kind = 'grid', hours, vars, lat, lon, chunk, padHeader = 0, run = '' }) {
    const chunks = [];
    for (const hour of hours) {
        for (const v of vars) chunks.push(nameChunk(zlib.gzipSync(chunk(hour, v)), `${run}/${hour}/${v.name}`));
    }
    const header = { format: 'openswells-bundle', version: 1, kind, hours, vars, chunks: chunks.map(c => c.length) };
    if (run) header.run = run;
    if (kind === 'grid') Object.assign(header, { lat, lon, nodata: NODATA });
    if (padHeader) header.pad = 'x'.repeat(padHeader);
    const json = Buffer.from(JSON.stringify(header));
    const prefix = Buffer.alloc(8);
    prefix.write('OSB1');
    prefix.writeUInt32LE(json.length, 4);
    return Buffer.concat([prefix, json, ...chunks]);
}

function int16(values) {
    const buf = Buffer.alloc(values.length * 2);
    values.forEach((v, i) => buf.writeInt16LE(v, i * 2));
    return buf;
}

// A fetch serving one file; honorRange false mimics a server ignoring Range.
function fakeFetch(bytes, { honorRange = true } = {}) {
    const calls = [];
    const impl = async (url, init = {}) => {
        const range = init.headers && init.headers.Range;
        calls.push(range);
        if (url !== '/static/x.bin') return new Response('missing', { status: 404 });
        if (!honorRange || !range) return new Response(bytes, { status: 200 });
        const [, start, end] = range.match(/bytes=(\d+)-(\d+)/).map(Number);
        return new Response(bytes.subarray(start, Math.min(end + 1, bytes.length)), { status: 206 });
    };
    return { impl, calls };
}

const lat = [10, 5];
const lon = [350, 355, 0];
const vars = [{ name: 'h1', scale: 0.01 }, { name: 'p1', scale: 0.1 }, { name: 'd1', scale: 1 }];
const grids = {
    // hour -> var -> 2x3 raw lattice; NODATA is land
    0: { h1: [57, NODATA, 123, 7, 8, NODATA], p1: [120, NODATA, 99, 1, 2, NODATA], d1: [270, NODATA, 0, 1, 2, NODATA] },
    3: { h1: [1, 2, 3, 4, 5, 6], p1: [10, 20, 30, 40, 50, 60], d1: [1, 2, 3, 4, 5, 6] },
};
const gridFile = bundleBytes({ hours: [0, 3], vars, lat, lon, chunk: (hour, v) => int16(grids[hour][v.name]) });

for (const honorRange of [true, false]) {
    test(`grid values, features and nearest cell (server ${honorRange ? 'honors' : 'ignores'} Range)`, async () => {
        const { impl, calls } = fakeFetch(gridFile, { honorRange });
        const bundles = window.createForecastBundles(impl);
        const bundle = await bundles.open('/static/x.bin');
        assert.deepEqual(plain(bundle.header.hours), [0, 3]);

        const grid = await bundles.grid(bundle, 0, ['h1', 'p1', 'd1']);
        assert.equal(grid.value('h1', 0), 0.57); // not 0.5700000000000001
        assert.equal(grid.value('p1', 0), 12);
        assert.equal(grid.value('h1', 1), null);

        const fc = bundles.gridFeatures(grid, { h1: 'h', p1: 'p', d1: 'd' });
        assert.equal(fc.features.length, 4);
        assert.deepEqual(plain(fc.features[0].geometry.coordinates), [350, 10]);
        assert.deepEqual(plain(fc.features[1].properties), { h: 1.23, p: 9.9, d: 0 });
        assert.deepEqual(plain(fc.features[3].geometry.coordinates), [355, 5]);

        // -10 lng is the 350 column across the wrap; land cells are skipped.
        const hasH = c => grid.value('h1', c) !== null;
        assert.equal(bundles.nearestCell(grid, hasH, { lng: -10, lat: 9 }), 0);
        // The land cell under the cursor is skipped; of the two equally near
        // wet cells the first in row-major order wins.
        assert.equal(bundles.nearestCell(grid, hasH, { lng: -5, lat: 10 }), -1);
        assert.equal(bundles.nearestCell(grid, hasH, { lng: -5, lat: 10 }, 30), 0);
        assert.equal(bundles.nearestCell(grid, hasH, { lng: 120, lat: 9 }), -1);

        assert.equal(await bundles.grid(bundle, 6, ['h1']), null); // hour not in the run
        const later = await bundles.grid(bundle, 3, ['p1']);
        assert.equal(later.value('p1', 5), 6);
        if (honorRange) {
            // One request for the header, then one span per hour fetched.
            assert.equal(calls.length, 3);
            assert.match(calls[1], /^bytes=\d+-\d+$/);
        }
    });
}

test('a header beyond the first probe is fetched in a second request', async () => {
    const file = bundleBytes({ hours: [0], vars: [{ name: 'h1', scale: 0.01 }], lat: [1], lon: [2], chunk: () => int16([42]), padHeader: 40000 });
    const { impl, calls } = fakeFetch(file);
    const bundles = window.createForecastBundles(impl);
    const bundle = await bundles.open('/static/x.bin');
    assert.equal(calls.length, 2);
    const grid = await bundles.grid(bundle, 0, ['h1']);
    assert.equal(grid.value('h1', 0), 0.42);
});

test('documents decode per hour, and failed opens are retried', async () => {
    const docs = { 0: { type: 'FeatureCollection', features: [{ n: 0 }] }, 3: { type: 'FeatureCollection', features: [] } };
    const file = bundleBytes({ kind: 'geojson', hours: [0, 3], vars: [{ name: 'geojson' }], chunk: hour => Buffer.from(JSON.stringify(docs[hour])) });
    const bundles = window.createForecastBundles(fakeFetch(file).impl);
    const bundle = await bundles.open('/static/x.bin');
    assert.deepEqual(plain(await bundles.documentAt(bundle, 0)), docs[0]);
    assert.deepEqual(plain(await bundles.documentAt(bundle, 3)), docs[3]);
    // A failed header read rejects and isn't kept: the next open retries.
    await assert.rejects(bundles.open('/static/other.bin'), /404/);
    let calls = 0;
    const flaky = window.createForecastBundles(async (url, init) => (calls++ ? fakeFetch(file).impl(url, init) : new Response('', { status: 503 })));
    await assert.rejects(flaky.open('/static/x.bin'), /503/);
    assert.deepEqual(plain(await flaky.documentAt(await flaky.open('/static/x.bin'), 0)), docs[0]);
});
