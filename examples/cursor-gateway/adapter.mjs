import { createRequire } from 'node:module';
import { createHash, timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { protectTransport } from './transport-policy.mjs';

const require = createRequire(import.meta.url);
const { CursorAuthPlugin } = await import(pathToFileURL(process.env.CURSOR_PLUGIN_PATH || '/lab/vendor/cursor/index.mjs'));
const { createGateway } = require(process.env.BKAI_GATEWAY_PATH || '/lab/vendor/bkai/cursor-gateway.js');
const plugin = await CursorAuthPlugin();
const auth = { type: 'api', key: process.env.CURSOR_API_KEY };
const gatewayKey = process.env.BKAI_GATEWAY_KEY;
if (!auth.key || !gatewayKey) throw new Error('CURSOR_API_KEY and BKAI_GATEWAY_KEY are required');
let cachedUsage, usagePending;
async function queryUsage() {
  if (cachedUsage && Date.now() - Date.parse(cachedUsage.observed_at) < 60_000) return cachedUsage;
  if (!usagePending) usagePending = (async () => {
    let timeout;
    try {
      const data = await Promise.race([
        plugin.auth.usage(async () => auth, { models: { 'grok-4.7@500k': {} } }),
        new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('usage_timeout')), 20_000); }),
      ]);
      return cachedUsage = {
        object: 'cursor_usage', observed_at: new Date().toISOString(), source: 'cursor-plugin-0.2.11',
        status: data.error ? 'unavailable' : data.windows?.length ? 'available' : 'unknown',
        routing_policy: 'observational_only; windows are not interchangeable or a USD balance',
        windows: data.error ? [] : (data.windows || []).map(w => ({
          name: w.name, used_percent: Number.isFinite(w.used) ? w.used : null,
          resets_at: w.resetsAt, display: w.display, informational_only: w.aside === true,
        })),
      };
    } catch {
      return cachedUsage = { object: 'cursor_usage', observed_at: new Date().toISOString(), status: 'unavailable', windows: [] };
    } finally { clearTimeout(timeout); }
  })().finally(() => { usagePending = undefined; });
  return usagePending;
}
if (process.argv.includes('--usage')) {
  const snapshot = await queryUsage();
  console.log(JSON.stringify(snapshot, null, 2));
  process.exit(snapshot.status === 'available' ? 0 : 1);
}
const policy = protectTransport(await plugin.auth.loader(async () => auth));
const gateway = createGateway({
  transport: policy.transport,
  listModels: () => plugin.provider.models({ models: {} }, { auth }),
  gatewayKey, maxConcurrency: 2, timeoutMs: 95_000,
});
const originalHandler = gateway.server.listeners('request')[0];
gateway.server.removeAllListeners('request');
const digest = value => createHash('sha256').update(value).digest();
gateway.server.on('request', async (req, res) => {
  const path = req.url?.split('?')[0];
  if (!['/account/usage', '/account/status'].includes(path)) return originalHandler(req, res);
  const send = (status, payload) => {
    res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' });
    res.end(JSON.stringify(payload));
  };
  if (!timingSafeEqual(digest(req.headers.authorization || ''), digest('Bearer ' + gatewayKey))) {
    return send(401, { error: { code: 'invalid_api_key', message: 'Invalid gateway API key.' } });
  }
  if (req.method !== 'GET') return send(405, { error: { code: 'method_not_allowed' } });
  const account = { id: process.env.ACCOUNT_ID || 'cursor-account-1', ...policy.snapshot() };
  return send(200, path === '/account/status' ? { account } : { ...await queryUsage(), account });
});
await new Promise(resolve => gateway.server.listen(8787, '0.0.0.0', resolve));
console.log('Cursor account adapter ready; concurrency=2; first event=60s; total=90s; cooldown=30s');
for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, async () => {
  await gateway.close(); process.exit(0);
});
