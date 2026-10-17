// Permission is requested synchronously from the user's tap, after preparation.
let pushRegistration = null,
    pushKey = null,
    pushSubscription = null,
    pushRegistered = false,
    preparingPush = null,
    pushNeedsRenewal = false;
function pushSupported() {
    return (
        isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
    );
}
function urlBase64ToUint8Array(value) {
    const raw = atob(
        (value + '='.repeat((4 - (value.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/'),
    );
    return Uint8Array.from(raw, c => c.charCodeAt(0));
}
async function isBraveBrowser() {
    try {
        return !!navigator.brave && (await navigator.brave.isBrave());
    } catch (_) {
        return false;
    }
}
async function pushSetupError(error) {
    const message = error?.message || 'Could not turn on reminders. Try again.';
    if ((await isBraveBrowser()) && /registration failed|push service error/i.test(message)) {
        return 'Brave’s push service is off. Open brave://settings/privacy, enable “Use Google services for push messaging,” relaunch Brave, then try again.';
    }
    return message;
}
async function pushAPI(path, body) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 20000);
    try {
        const response = await fetch(path, {
            method: body ? 'POST' : 'GET',
            headers: body ? { 'Content-Type': 'application/json' } : {},
            body: body ? JSON.stringify(body) : undefined,
            signal: controller.signal,
        });
        if (response.status === 401 || response.redirected)
            throw new Error('Session expired. Sign in again, then turn on reminders.');
        const data = await response.json();
        if (!response.ok) {
            const error = new Error(data.error || 'Could not update notifications.');
            error.renew = data.renew === true;
            throw error;
        }
        return data;
    } finally {
        clearTimeout(timeout);
    }
}
// Today shows one line about reminders, only while this device is not getting
// them; it is re-rendered often, so renderPush puts the last state back after each swap.
let pushMessage = '';
function renderPush() {
    displayPush(pushMessage);
}
function displayPush(message) {
    pushMessage = message;
    const line = document.getElementById('push-line');
    if (line) line.hidden = !message;
    const status = document.getElementById('push-status');
    if (status) status.textContent = message;
    // Turn on only shows when it can work: supported, ready and not blocked.
    const enable = document.getElementById('push-enable');
    if (enable) {
        enable.disabled = !pushKey;
        enable.hidden =
            pushRegistered ||
            !pushRegistration ||
            (typeof Notification !== 'undefined' && Notification.permission === 'denied');
    }
}
function waitForWorker(reg) {
    return new Promise((resolve, reject) => {
        if (reg.active) {
            resolve(reg);
            return;
        }
        const timer = setTimeout(
            () => reject(new Error('App setup timed out. Close and reopen the app, then try again.')),
            15000,
        );
        const check = () => {
            if (reg.active) {
                clearTimeout(timer);
                resolve(reg);
            }
        };
        reg.addEventListener('updatefound', () => reg.installing?.addEventListener('statechange', check));
        reg.installing?.addEventListener('statechange', check);
        reg.waiting?.addEventListener('statechange', check);
    });
}
async function preparePush() {
    if (preparingPush) return preparingPush;
    if (!pushSupported()) {
        displayPush(
            !isSecureContext
                ? 'Reminders need an HTTPS connection.'
                : 'Open the installed Home Screen app to turn on reminders.',
        );
        return;
    }
    preparingPush = (async () => {
        try {
            displayPush('');
            const registered = await navigator.serviceWorker
                .register('/static/sw.js', { scope: '/', updateViaCache: 'none' })
                .catch(() => {
                    throw new Error('Could not set up reminders here. Reload to try again.');
                });
            pushRegistration = await waitForWorker(registered);
            pushKey = (await pushAPI('/push/public-key')).key;
            pushSubscription = await pushRegistration.pushManager.getSubscription();
            pushRegistered = false;
            if (Notification.permission === 'denied') {
                displayPush('Reminders are blocked. Allow SecondBrain in your device’s notification settings.');
                return;
            }
            if (pushSubscription) {
                const currentKey = pushSubscription.options.applicationServerKey;
                const expected = urlBase64ToUint8Array(pushKey);
                if (
                    currentKey &&
                    (new Uint8Array(currentKey).length !== expected.length ||
                        new Uint8Array(currentKey).some((v, i) => v !== expected[i]))
                ) {
                    pushNeedsRenewal = true;
                    displayPush('Reminders need turning on again on this device.');
                    return;
                }
                // Upsert preserves per-device delivery history; repairs a missing server row.
                await pushAPI('/push/subscribe', pushSubscription.toJSON());
                const status = await pushAPI('/push/status', {
                    endpoint: pushSubscription.endpoint,
                });
                pushRegistered = status.registered;
                displayPush(pushRegistered ? '' : 'Reminders need turning on again on this device.');
                if (pushRegistered) await retireOldWorker();
            } else
                displayPush('Reminders are off on this device.');
        } catch (error) {
            if (error.renew) pushNeedsRenewal = true;
            displayPush(
                error.name === 'AbortError' ? 'The server did not respond. Try again.' : error.message,
            );
        } finally {
            preparingPush = null;
        }
    })();
    return preparingPush;
}
async function retireOldWorker() {
    // Do not lose a working old subscription until the root subscription is saved.
    for (const reg of await navigator.serviceWorker.getRegistrations()) {
        if (reg.scope === new URL('/static/', location.origin).href) {
            const old = await reg.pushManager.getSubscription();
            if (old) {
                await pushAPI('/push/unsubscribe', { endpoint: old.endpoint });
                await old.unsubscribe();
            }
            await reg.unregister();
        }
    }
}
function enablePush() {
    if (!pushRegistration || !pushKey) {
        preparePush();
        return;
    }
    // No await before this call: Safari requires direct user activation.
    const permission =
        Notification.permission === 'granted' ? Promise.resolve('granted') : Notification.requestPermission();
    const button = document.getElementById('push-enable');
    if (button) button.disabled = true;
    permission
        .then(async granted => {
            if (granted !== 'granted') {
                displayPush('Reminders were not allowed. Allow SecondBrain in notification settings.');
                return;
            }
            try {
                if (pushNeedsRenewal && pushSubscription) {
                    await pushAPI('/push/unsubscribe', {
                        endpoint: pushSubscription.endpoint,
                    });
                    await pushSubscription.unsubscribe();
                    pushSubscription = null;
                    pushNeedsRenewal = false;
                }
                pushSubscription = await pushRegistration.pushManager.subscribe({
                    userVisibleOnly: true,
                    applicationServerKey: urlBase64ToUint8Array(pushKey),
                });
                await pushAPI('/push/subscribe', pushSubscription.toJSON());
                pushRegistered = true;
                displayPush('');
                if (typeof showToast === 'function') showToast('Reminders are on for this device');
                await retireOldWorker();
            } catch (error) {
                pushRegistered = false;
                displayPush(await pushSetupError(error));
            }
        })
        .catch(error => displayPush(error.message));
}
// Register even on browsers without PushManager so asset updates still work.
document.addEventListener('DOMContentLoaded', () => {
    if (pushSupported()) preparePush();
    else if (isSecureContext && 'serviceWorker' in navigator)
        navigator.serviceWorker
            .register('/static/sw.js', { scope: '/', updateViaCache: 'none' })
            .catch(() => {});
});
