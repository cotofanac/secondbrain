// Waits up to 30 seconds for each server's /health to answer (CI starts several).
const urls = process.argv.slice(2);
(async () => {
    for (const url of urls) {
        for (let i = 0; ; i++) {
            try {
                if ((await fetch(url + '/health')).ok) break;
            } catch (_) {}
            if (i >= 30) throw new Error(url + ' did not become healthy');
            await new Promise(r => setTimeout(r, 1000));
        }
    }
})().catch(e => {
    console.error(e.message);
    process.exit(1);
});
