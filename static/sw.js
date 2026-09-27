// __ASSET_VERSION__ is replaced at request time by the Go server (see the
// /static/sw.js handler) so a deploy changes the cache name and SW bytes.
const CACHE_NAME = 'secondbrain-__ASSET_VERSION__';
const PRECACHE = [
    '/static/style.css?v=__ASSET_VERSION__',
    '/static/app.js?v=__ASSET_VERSION__',
    '/static/notes.js?v=__ASSET_VERSION__',
    '/static/htmx.min.js?v=__ASSET_VERSION__',
    '/static/workspace.js?v=__ASSET_VERSION__',
    '/static/push.js?v=__ASSET_VERSION__',
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
            Promise.all(keys.filter(k => k.startsWith('secondbrain-') && k !== CACHE_NAME && k !== PAGE_CACHE).map(k => caches.delete(k)))
        )
    );
    self.clients.claim();
});

// The last page loaded online is kept on this device so the lists can still be
// read without a connection (a shop with no signal). It is dropped whenever the
// login page loads or a logout is sent (even one that fails offline), which
// covers logout, inactivity and expired sessions.
const PAGE_CACHE = 'secondbrain-page';

function offlineCopy(cached) {
    // Mark the copy so the page can say it is read-only.
    return cached.text().then(html => new Response(
        html.replace('<body ', '<body data-offline-copy="1" '),
        { headers: cached.headers }
    ));
}

self.addEventListener('fetch', event => {
    const url = new URL(event.request.url);

    if (event.request.mode === 'navigate' && url.origin === self.location.origin) {
        if (url.pathname === '/login' || url.pathname === '/logout') {
            event.waitUntil(caches.delete(PAGE_CACHE));
            return;
        }
        if (url.pathname !== '/') return;
        event.respondWith(
            fetch(event.request)
                .then(response => {
                    if (response.ok && response.type === 'basic') {
                        const copy = response.clone();
                        event.waitUntil(caches.open(PAGE_CACHE).then(cache => cache.put('/', copy)));
                    } else if (response.type === 'opaqueredirect') {
                        // Signed out: the app page redirects to /login.
                        event.waitUntil(caches.delete(PAGE_CACHE));
                    }
                    return response;
                })
                .catch(() => caches.open(PAGE_CACHE)
                    .then(cache => cache.match('/'))
                    .then(cached => cached ? offlineCopy(cached) : Response.error()))
        );
        return;
    }

    if (url.pathname.startsWith('/static/')) {
        event.respondWith(
            fetch(event.request)
                .then(response => {
                    if(response.ok){const clone=response.clone();event.waitUntil(caches.open(CACHE_NAME).then(cache=>cache.put(event.request,clone)));}
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
            icon: '/static/icon-192.png',
            badge: '/static/icon-192.png',
            tag: data.tag || 'secondbrain',
            data: { url: data.url || '/' }
        })
    );
});

// Focus an already-open window if there is one, otherwise open the app.
self.addEventListener('notificationclick', event => {
    event.notification.close();
    let target=new URL('/',self.location.origin);
    try{const requested=new URL(event.notification.data?.url || '/',self.location.origin);if(requested.origin===self.location.origin&&requested.pathname==='/')target=requested;}catch(_){}
    event.waitUntil((async()=>{
        const windows=await clients.matchAll({type:'window',includeUncontrolled:true});
        for(const client of windows){if(new URL(client.url).origin===self.location.origin&&'focus' in client){await client.navigate(target.href);return client.focus();}}
        return clients.openWindow(target.href);
    })());
});
