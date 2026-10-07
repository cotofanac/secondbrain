// Durable buffers belong to entities, not the DOM nodes currently showing them.
// Transactions settle before callers acknowledge a save or discard a draft.
const editorDrafts = (() => {
    const queues = new Map();
    function open() {
        return new Promise((resolve, reject) => {
            const request = indexedDB.open('secondbrain-drafts', 2);
            request.onupgradeneeded = () => {
                for (const kind of ['notes', 'tasks']) {
                    if (!request.result.objectStoreNames.contains(kind))
                        request.result.createObjectStore(kind);
                }
            };
            request.onsuccess = () => resolve(request.result);
            request.onerror = () => reject(request.error);
        });
    }
    async function transact(kind, id, operation, value) {
        const db = await open();
        return new Promise((resolve, reject) => {
            const tx = db.transaction(kind, operation === 'get' ? 'readonly' : 'readwrite');
            const store = tx.objectStore(kind);
            const request = operation === 'put' ? store.put(value, String(id)) : store[operation](String(id));
            tx.oncomplete = () => {
                db.close();
                resolve(request.result);
            };
            tx.onabort = tx.onerror = () => {
                db.close();
                reject(tx.error);
            };
        });
    }
    function enqueue(kind, id, operation, value) {
        const key = kind + ':' + id;
        const result = (queues.get(key) || Promise.resolve())
            .catch(() => {})
            .then(() => transact(kind, id, operation, value));
        queues.set(key, result);
        result
            .finally(() => {
                if (queues.get(key) === result) queues.delete(key);
            })
            .catch(() => {});
        return result;
    }
    return {
        read: (kind, id) => enqueue(kind, id, 'get'),
        write: (kind, id, value) => enqueue(kind, id, 'put', { ...value, savedAt: Date.now() }),
        remove: (kind, id) => enqueue(kind, id, 'delete'),
        state: (element, state) => {
            element.dataset.editState = state;
        },
    };
})();
