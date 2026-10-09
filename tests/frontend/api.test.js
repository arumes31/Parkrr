'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Execute the shipped transport with isolated browser dependencies, not a copy.
const source = fs.readFileSync(path.join(__dirname, '../../web/static/js/api.js'), 'utf8');
function harness({ online = true, failure } = {}) {
    const calls = [];
    const handled = [];
    let sequence = 0;
    const response = { ok: true, status: 200 };
    const createAPI = vm.runInNewContext(source.replace('export function createAPI', 'function createAPI') + '\ncreateAPI;', {
        navigator: { onLine: online },
        fetch: async (url, options) => {
            calls.push({ url, options });
            if (failure) throw failure;
            return response;
        },
    });
    const api = createAPI({
        getCookie: name => name === 'parkrr_csrf' ? 'synthetic-csrf-token' : '',
        nextSequence: () => ++sequence,
        handle: (result, requestPath, requestSequence) => {
            handled.push({ result, requestPath, requestSequence });
            return { preview: true, persisted: false };
        },
        offlineMessage: 'Synthetischer Offline-Hinweis',
    });
    return { api, calls, handled, response };
}

test('raw JSON preview preserves duplicate keys, whitespace and Unicode without serialization', async () => {
    const { api, calls, handled, response } = harness();
    const raw = '{\r\n "year":2025, "year":2026, "source":"Prüfung €", "source":"ſource"\r\n}\n';
    const result = await api.postJSONText('/reports/tax-import/preview', raw);
    assert.deepEqual(result, { preview: true, persisted: false });
    assert.equal(calls.length, 1);
    const { url, options } = calls[0];
    assert.equal(url, '/api/reports/tax-import/preview');
    assert.equal(options.method, 'POST');
    assert.equal(options.body, raw, 'server must receive the original JSON, including duplicates');
    assert.equal(options.credentials, 'same-origin');
    assert.equal(options.headers.Accept, 'application/json');
    assert.equal(options.headers['Content-Type'], 'application/json');
    assert.equal(options.headers['X-CSRF-Token'], 'synthetic-csrf-token');
    assert.deepEqual(handled, [{ result: response, requestPath: '/reports/tax-import/preview', requestSequence: 1 }]);
});

test('raw JSON uses the existing sequencing and response handler alongside normal requests', async () => {
    const { api, calls, handled } = harness();
    await api.post('/ordinary', { value: 'unchanged' });
    await api.postJSONText('/reports/tax-import/preview', '{"version":1}');
    await api.get('/ordinary');
    assert.equal(calls[0].options.body, '{"value":"unchanged"}');
    assert.equal(calls[2].options.body, undefined);
    assert.equal(calls[2].options.headers['X-CSRF-Token'], undefined);
    assert.deepEqual(handled.map(item => item.requestSequence), [1, 2, 3]);
});

test('offline raw JSON preview fails before fetching and does not enter the response handler', async () => {
    const { api, calls, handled } = harness({ online: false });
    await assert.rejects(api.postJSONText('/reports/tax-import/preview', '{}'), /Synthetischer Offline-Hinweis/);
    assert.equal(calls.length, 0);
    assert.equal(handled.length, 0);
});

test('raw JSON preview propagates network failures for retry without a false success', async () => {
    const failure = new Error('synthetic network failure');
    const { api, calls, handled } = harness({ failure });
    await assert.rejects(api.postJSONText('/reports/tax-import/preview', '{}'), error => error === failure);
    assert.equal(calls.length, 1);
    assert.equal(handled.length, 0);
});
