// Small, dependency-injected HTTP client. Application-specific session handling
// remains in app.js; this module owns only request construction and transport.
export function createAPI({ getCookie, handle, nextSequence, offlineMessage }) {
    const api = {
        async request(method, path, body, headers = {}, rawJSON = false) {
            if (method !== 'GET' && typeof navigator !== 'undefined' && navigator.onLine === false) {
                throw new Error(offlineMessage);
            }
            const options = {
                method,
                headers: { Accept: 'application/json', ...headers },
                credentials: 'same-origin',
            };
            if (body !== undefined) {
                options.headers['Content-Type'] = 'application/json';
                options.body = rawJSON ? body : JSON.stringify(body);
            }
            if (method !== 'GET') options.headers['X-CSRF-Token'] = getCookie('parkrr_csrf');
            const sequence = nextSequence();
            return handle(await fetch('/api' + path, options), path, sequence);
        },
        async upload(path, formData, method = 'POST') {
            const sequence = nextSequence();
            const response = await fetch('/api' + path, {
                method,
                body: formData,
                credentials: 'same-origin',
                headers: { 'X-CSRF-Token': getCookie('parkrr_csrf') },
            });
            return handle(response, path, sequence);
        },
    };
    api.get = (path) => api.request('GET', path);
    api.post = (path, body, headers) => api.request('POST', path, body, headers);
    // Keep imported JSON intact so server validation can detect duplicate keys.
    api.postJSONText = (path, text) => api.request('POST', path, text, {}, true);
    api.put = (path, body) => api.request('PUT', path, body);
    api.del = (path) => api.request('DELETE', path);
    return api;
}
