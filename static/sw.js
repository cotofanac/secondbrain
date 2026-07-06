// __ASSET_VERSION__ is replaced at request time by the Go server (see the
// /static/sw.js handler) so a deploy changes the cache name and SW bytes.
const CACHE_NAME = 'secondbrain-__ASSET_VERSION__';
const PRECACHE = [
    '/static/style.css?v=__ASSET_VERSION__',
    '/static/app.js?v=__ASSET_VERSION__',
    '/static/htmx.min.js?v=__ASSET_VERSION__',
    '/static/manifest.json'
];

self.addEventListener('install', event => {
    event.waitUntil(
        caches.open(CACHE_NAME).then(cache => cache.addAll(PRECACHE))
    );
    self.skipWaiting();
});

self.addEventListener('activate', event => {
    event.waitUntil(
        caches.keys().then(keys =>
            Promise.all(keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k)))
        )
    );
    self.clients.claim();
});

self.addEventListener('fetch', event => {
    const url = new URL(event.request.url);

    if (url.pathname.startsWith('/static/')) {
        event.respondWith(
            fetch(event.request)
                .then(response => {
                    const clone = response.clone();
                    caches.open(CACHE_NAME).then(cache => cache.put(event.request, clone));
                    return response;
                })
                .catch(() => caches.match(event.request))
        );
    }
});

// --- Push notifications ---
// The server sends a JSON payload ({title, body, tag, url}); show it as a
// notification even when the app is closed. `tag` collapses repeat reminders
// of the same kind so they don't stack.
self.addEventListener('push', event => {
    let data = {};
    try {
        data = event.data ? event.data.json() : {};
    } catch (e) {
        data = { title: 'SecondBrain', body: event.data ? event.data.text() : '' };
    }
    const title = data.title || 'SecondBrain';
    event.waitUntil(
        self.registration.showNotification(title, {
            body: data.body || '',
            icon: '/static/icon-192.svg',
            badge: '/static/icon-192.svg',
            tag: data.tag || 'secondbrain',
            data: { url: data.url || '/' }
        })
    );
});

// Focus an already-open window if there is one, otherwise open the app.
self.addEventListener('notificationclick', event => {
    event.notification.close();
    const target = (event.notification.data && event.notification.data.url) || '/';
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then(clientList => {
            for (const client of clientList) {
                if ('focus' in client) {
                    client.navigate(target);
                    return client.focus();
                }
            }
            if (clients.openWindow) return clients.openWindow(target);
        })
    );
});
