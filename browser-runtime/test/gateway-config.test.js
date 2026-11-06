import test from 'node:test';
import assert from 'node:assert/strict';
import { gatewayChromeArguments } from '../src/gateway-config.js';
import { WaylandRuntime, WaylandSessionHost } from '../src/wayland.js';

const origin = 'http://172.20.10.5:19770';
const settings = { POGO_BROWSER_GATEWAY_ORIGIN: origin, POGO_BROWSER_GATEWAY_CONNECT_HOST: 'host.docker.internal' };
const proxyKeys = ['HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy', 'HtTp_PrOxY', 'All_Proxy'];
const routeKeys = ['POGO_BROWSER_GATEWAY_ORIGIN', 'POGO_BROWSER_GATEWAY_CONNECT_HOST', ...proxyKeys];
function withEnvironment(t, values) {
  const saved = Object.fromEntries(routeKeys.map(key => [key, process.env[key]]));
  for (const key of routeKeys) delete process.env[key];
  Object.assign(process.env, values);
  t.after(() => {
    for (const key of routeKeys) {
      if (saved[key] === undefined) delete process.env[key];
      else process.env[key] = saved[key];
    }
  });
}

test('gateway routing defaults to no flags and does not change existing proxy configuration', () => {
  for (const env of [{}, { POGO_BROWSER_GATEWAY_ORIGIN: '', POGO_BROWSER_GATEWAY_CONNECT_HOST: '' }, { HTTP_PROXY: 'http://corporate.invalid:8080' }]) {
    assert.deepEqual(gatewayChromeArguments(env), []);
  }
  assert.equal(Object.isFrozen(gatewayChromeArguments({})), true);
});

test('only canonical LAN HTTP authorities generate one exact host and port rule', () => {
  for (const ip of ['10.0.0.1', '172.16.0.1', '172.20.10.5', '172.31.255.254', '192.168.1.20']) {
    const args = gatewayChromeArguments({ ...settings, POGO_BROWSER_GATEWAY_ORIGIN: `http://${ip}:19770` });
    assert.deepEqual(args, [`--host-resolver-rules=MAP ${ip}:19770 host.docker.internal:19770`]);
    assert.equal(Object.isFrozen(args), true);
  }
  assert.deepEqual(gatewayChromeArguments({ ...settings, HTTP_PROXY: '', NO_PROXY: '172.20.10.5' }), gatewayChromeArguments(settings));
});

test('gateway route rejects ambiguous origins and arbitrary Chromium rule injection', () => {
  for (const value of [
    'https://172.20.10.5:19770', 'HTTP://172.20.10.5:19770', 'http://172.20.10.5', 'http://172.20.10.5:80',
    origin + '/', origin + '/_pogo/web/token/', origin + '/a/..', origin + '?', origin + '?secret=1', origin + '#',
    'http://user:password@172.20.10.5:19770', 'http://localhost:19770', 'http://pogo.example:19770', 'http://[fd00::1]:19770',
    'http://127.0.0.1:19770', 'http://169.254.1.2:19770', 'http://172.15.1.2:19770', 'http://172.32.1.2:19770',
    'http://8.8.8.8:19770', 'http://0.0.0.0:19770', 'http://224.0.0.1:19770', 'http://192.168.1.256:19770',
    'http://172.020.10.5:19770', 'http://0xac140a05:19770', 'http://2886994437:19770', 'http://172.20.2565:19770',
    'http://%31%37%32.20.10.5:19770', 'http://172.20.10.5.:19770', 'http://*:19770', origin + ', MAP * attack.invalid',
    ' ' + origin, origin + '\n', origin + '\t', origin + ' --no-sandbox', origin + String.fromCharCode(0), null, 19770, {},
  ]) {
    assert.throws(() => gatewayChromeArguments({ ...settings, POGO_BROWSER_GATEWAY_ORIGIN: value }), /POGO_BROWSER_GATEWAY_/, String(value));
  }
  for (const value of ['', 'HOST.DOCKER.INTERNAL', 'host.docker.internal:19770', 'http://host.docker.internal', '127.0.0.1', 'web-runner', '*.internal', 'host.docker.internal, MAP * attack.invalid', 'host.docker.internal\n', null, {}]) {
    assert.throws(() => gatewayChromeArguments({ ...settings, POGO_BROWSER_GATEWAY_CONNECT_HOST: value }), /POGO_BROWSER_GATEWAY_/, String(value));
  }
  assert.throws(() => gatewayChromeArguments({ POGO_BROWSER_GATEWAY_ORIGIN: origin }), /set together/);
  assert.throws(() => gatewayChromeArguments({ POGO_BROWSER_GATEWAY_CONNECT_HOST: 'host.docker.internal' }), /set together/);
});

test('proxy combinations fail closed without global direct or broad bypass flags', () => {
  for (const key of proxyKeys) {
    const secret = 'http://synthetic-user:synthetic-password@corporate.invalid:8080';
    assert.throws(() => gatewayChromeArguments({ ...settings, [key]: secret, NO_PROXY: '172.20.10.5:19770' }), error => {
      assert.match(error.message, /unsupported/);
      assert.equal(error.message.includes('synthetic-password'), false);
      return true;
    }, key);
  }
});

test('native session Chrome arguments carry only the validated administrator route', t => {
  withEnvironment(t, settings);
  const runtime = new WaylandRuntime({});
  const host = new WaylandSessionHost(runtime, 'gateway-configuration');
  const flags = host.chromeArgs.filter(arg => /resolver|proxy/i.test(arg));
  assert.deepEqual(flags, [`--host-resolver-rules=MAP 172.20.10.5:19770 host.docker.internal:19770`]);
  assert.equal(host.chromeArgs.includes('--site-per-process'), true);
  assert.equal(host.chromeArgs.includes('--no-sandbox'), false);
  // Read once from the administrator environment, not from a session URL or
  // a later caller changing environment values behind the allocated runtime.
  process.env.POGO_BROWSER_GATEWAY_ORIGIN = 'http://192.168.1.20:19770';
  assert.deepEqual(new WaylandSessionHost(runtime, 'second-window').chromeArgs.filter(arg => arg.startsWith('--host-resolver-rules=')), flags);
});

test('native runtime rejects invalid gateway settings before allocating processes', t => {
  withEnvironment(t, { ...settings, POGO_BROWSER_GATEWAY_CONNECT_HOST: 'web-runner' });
  assert.throws(() => new WaylandRuntime({}), /must be host.docker.internal/);
});

test('ordinary native sessions have no gateway flag when the opt-in is absent', t => {
  withEnvironment(t, {});
  assert.equal(new WaylandSessionHost(new WaylandRuntime({}), 'ordinary-window').chromeArgs.some(arg => arg.startsWith('--host-resolver-rules=')), false);
});
