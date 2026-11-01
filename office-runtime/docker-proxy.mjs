// Container-only entry point. The lab CLI retains its loopback-only listener.
import fs from 'node:fs/promises';
import { createEngineProxy, officeGatewayPrefix, officeStartupTimeoutMs, officeEngineHealth } from './engine-proxy.mjs';

// The same dedicated secret signs Office documents and authenticates only the
// gateway metadata through a separate HMAC domain. It is never put in a URL,
// returned to the browser, copied to forwarded headers or printed in logs.
let gatewaySecret;
try {
  gatewaySecret = (await fs.readFile(process.env.POGO_OFFICE_JWT_SECRET_FILE || '/run/secrets/pogo-office-jwt', 'utf8')).trim();
} catch { throw new Error('POGO_OFFICE_GATEWAY_SECRET_UNAVAILABLE: Office gateway secret file is unavailable; check private runtime preparation for the ds account.'); }
if (Buffer.byteLength(gatewaySecret) < 32 || Buffer.byteLength(gatewaySecret) > 8192 || /[\r\n]/.test(gatewaySecret)) throw new Error('POGO_OFFICE_GATEWAY_SECRET_INVALID: Office gateway secret is invalid.');

const upstream = 'http://127.0.0.1:80';
// This is one startup stage, not an individual cheap health sample. Slow cold
// DocServer boot gets the same minimum five-minute budget as document opening.
const deadline = Date.now() + officeStartupTimeoutMs;
let engineReady = false;
while (Date.now() < deadline) {
  let health;
  try {
    health = await officeEngineHealth(upstream);
  } catch { /* DocServer starts its supervised components independently. */ }
  if (health?.ready) { engineReady = true; break; }
  if (health?.status >= 400 && health.status < 500) throw new Error('Pinned DocServer rejected its internal health request.');
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
if (!engineReady) throw new Error('Pinned DocServer did not become ready; Office adapter was not exposed.');

// Initialization verifies all nine pinned SDK/loader assets before listen().
const proxy = await createEngineProxy({ upstream, port: 8080, listenHost: '0.0.0.0', containerListen: true, healthEndpoint: true,
  gatewayPrefix: officeGatewayPrefix, gatewaySecret });
console.log(JSON.stringify({ ready: true, adapter: proxy.state.adapter, extensionSha256: proxy.state.extensionSha256, runtimeVersion: proxy.state.runtimeVersion }));
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => { void proxy.close().then(() => process.exit(0)); });
