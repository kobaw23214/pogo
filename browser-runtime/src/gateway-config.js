import { isIP } from 'node:net';

const proxyKeys = new Set(['http_proxy', 'https_proxy', 'all_proxy']);

// Administrator-only transport routing. Never accept raw Chromium rules or
// browser session/action input: the public origin remains the page's identity.
export function gatewayChromeArguments(environment = process.env) {
  const origin = environment.POGO_BROWSER_GATEWAY_ORIGIN ?? '';
  const connectHost = environment.POGO_BROWSER_GATEWAY_CONNECT_HOST ?? '';
  if (origin === '' && connectHost === '') return Object.freeze([]);
  if (typeof origin !== 'string' || typeof connectHost !== 'string' || !origin || !connectHost) {
    throw new Error('POGO_BROWSER_GATEWAY_ORIGIN and POGO_BROWSER_GATEWAY_CONNECT_HOST must be set together');
  }
  // Validate the original text before any URL parser could normalize unusual
  // IP forms, dot paths, credentials, whitespace or encoded delimiters.
  const match = /^http:\/\/((?:[0-9]{1,3}\.){3}[0-9]{1,3}):19770$/.exec(origin);
  const host = match?.[0] === origin ? match[1] : undefined;
  const bytes = host?.split('.').map(Number);
  if (!host || isIP(host) !== 4 || !(bytes[0] === 10 || bytes[0] === 172 && bytes[1] >= 16 && bytes[1] <= 31 || bytes[0] === 192 && bytes[1] === 168)) {
    throw new Error('POGO_BROWSER_GATEWAY_ORIGIN must be a canonical RFC1918 IPv4 HTTP origin on port 19770, without a path');
  }
  if (connectHost !== 'host.docker.internal') {
    throw new Error('POGO_BROWSER_GATEWAY_CONNECT_HOST must be host.docker.internal');
  }
  // Local resolver rules do not apply when a forward proxy resolves the target.
  // Refuse that combination rather than changing unrelated egress or silently
  // bypassing the administrator's existing proxy policy.
  if (Object.entries(environment).some(([key, value]) => proxyKeys.has(key.toLowerCase()) && value !== undefined && value !== '')) {
    throw new Error('gateway routing with HTTP_PROXY, HTTPS_PROXY or ALL_PROXY is unsupported; retain the proxy policy or disable gateway routing');
  }
  return Object.freeze([`--host-resolver-rules=MAP ${host}:19770 host.docker.internal:19770`]);
}
