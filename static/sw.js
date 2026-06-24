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
