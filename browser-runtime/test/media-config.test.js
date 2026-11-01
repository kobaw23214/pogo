import test from 'node:test';
import assert from 'node:assert/strict';
import { MEDIA_DEFAULT_DEVICE_SCALE_FACTOR, MEDIA_DEVICE_SCALE_FACTORS, mediaConfiguration } from '../src/media-config.js';

const defaults = { advertisedIP: '', udpPortRange: '19780-19811', iceServers: [], network: 'lan', playoutDelay: 'zero', deviceScaleFactor: 2, relayOnly: true };

test('the default worker has exactly one internal media path with no external address or ICE credential', () => {
  assert.deepEqual(mediaConfiguration({}), defaults);
  for (const value of ['', '1', 'true']) assert.deepEqual(mediaConfiguration({ POGO_BROWSER_WEBRTC_RELAY_ONLY: value }), defaults);
  for (const value of ['0', 'false', 'on', 'off', '2', ' 1', 'TRUE']) assert.throws(() => mediaConfiguration({ POGO_BROWSER_WEBRTC_RELAY_ONLY: value }), /only supports Web-port relay media/);
});

test('retained legacy address and public ICE settings cannot restore direct media or disclose old secrets', () => {
  const oldICE = JSON.stringify([{ urls: ['stun:old.example:3478', 'turns:old.example:5349?transport=tcp'], username: 'old-user', credential: 'synthetic-old-secret' }]);
  for (const address of ['127.0.0.1', '192.168.50.12', '203.0.113.10', '2001:db8::10', 'outdated.invalid', 'https://old.example']) {
    assert.deepEqual(mediaConfiguration({ POGO_BROWSER_WEBRTC_ADVERTISE_IP: address, POGO_BROWSER_WEBRTC_ICE_SERVERS: oldICE }), defaults);
  }
  for (const oldICE of ['invalid outdated JSON', '{}', 'null', '[{"urls":"https://old.example"}]']) {
    assert.deepEqual(mediaConfiguration({ POGO_BROWSER_WEBRTC_ICE_SERVERS: oldICE }), defaults);
  }
  assert.equal(JSON.stringify(mediaConfiguration({ POGO_BROWSER_WEBRTC_ICE_SERVERS: oldICE })).includes('synthetic-old-secret'), false);
});

test('the internal UDP range remains bounded and independently configurable', () => {
  assert.deepEqual(mediaConfiguration({ POGO_BROWSER_WEBRTC_UDP_PORT_RANGE: '19820-19839' }), { ...defaults, udpPortRange: '19820-19839' });
  for (const range of ['1-65535', '19820', '19839-19820', '19780-65536', '19780-22000', '19780--19811', '19780-19811\n']) {
    assert.throws(() => mediaConfiguration({ POGO_BROWSER_WEBRTC_UDP_PORT_RANGE: range }), /bounded range/);
  }
});

test('auto retains the W2 interactive preset independently of the relay path or advertised address', () => {
  for (const network of ['', 'auto', 'lan']) assert.equal(mediaConfiguration({ POGO_BROWSER_WEBRTC_NETWORK: network }).network, 'lan');
  assert.equal(mediaConfiguration({ POGO_BROWSER_WEBRTC_NETWORK: 'wan' }).network, 'wan', 'explicit WAN tuning remains an opt-in');
  for (const address of ['127.0.0.1', '192.168.50.12', '203.0.113.10', '2001:db8::10']) {
    assert.equal(mediaConfiguration({ POGO_BROWSER_WEBRTC_ADVERTISE_IP: address }).network, 'lan', 'retired advertisements cannot select a media preset');
  }
  for (const value of ['LAN', 'on', '1', 'true', 'lan ', 'local']) assert.throws(() => mediaConfiguration({ POGO_BROWSER_WEBRTC_NETWORK: value }), /auto, lan or wan/);
});

test('playout delay remains an independent validated setting with the same explicit override semantics', () => {
  const resolved = environment => { const { network, playoutDelay } = mediaConfiguration(environment); return { network, playoutDelay }; };
  for (const value of ['', 'auto']) {
    assert.deepEqual(resolved({ POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: value }), { network: 'lan', playoutDelay: 'zero' });
    assert.deepEqual(resolved({ POGO_BROWSER_WEBRTC_NETWORK: 'wan', POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: value }), { network: 'wan', playoutDelay: 'default' });
  }
  assert.deepEqual(resolved({ POGO_BROWSER_WEBRTC_NETWORK: 'wan', POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: 'zero' }), { network: 'wan', playoutDelay: 'zero' });
  assert.deepEqual(resolved({ POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: 'default' }), { network: 'lan', playoutDelay: 'default' });
  assert.deepEqual(resolved({ POGO_BROWSER_WEBRTC_NETWORK: 'lan', POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: 'default' }), { network: 'lan', playoutDelay: 'default' });
  for (const value of ['ZERO', 'Default', 'AUTO', '0', 'off', 'on', 'none', 'true', 'lan', 'zero ', ' auto']) {
    assert.throws(() => mediaConfiguration({ POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: value }), /POGO_BROWSER_WEBRTC_PLAYOUT_DELAY must be auto, zero or default/);
  }
});

test('the W2 output scale ceiling and native pixel densities are unchanged by transport consolidation', () => {
  const dsf = value => mediaConfiguration(value === undefined ? {} : { POGO_BROWSER_DEVICE_SCALE_FACTOR: value }).deviceScaleFactor;
  assert.equal(MEDIA_DEFAULT_DEVICE_SCALE_FACTOR, 2);
  assert.equal(dsf(), 2); assert.equal(dsf(''), 2);
  for (const value of ['1', '1.25', '1.5', '2']) assert.equal(dsf(value), Number(value));
  assert.deepEqual(MEDIA_DEVICE_SCALE_FACTORS, [1, 1.25, 1.5, 2]);
  for (const value of ['0', '0.5', '3', '2.0', '1.50', ' 1', '1 ', 'auto', 'high', '-1', 'NaN', '1e0']) {
    assert.throws(() => dsf(value), /must be 1, 1\.25, 1\.5 or 2/);
  }
});

test('explicit quality and latency settings survive an automatic upgrade of old browser configuration', () => {
  assert.deepEqual(mediaConfiguration({
    POGO_BROWSER_WEBRTC_ADVERTISE_IP: '203.0.113.10',
    POGO_BROWSER_WEBRTC_ICE_SERVERS: 'outdated invalid JSON',
    POGO_BROWSER_WEBRTC_NETWORK: 'lan',
    POGO_BROWSER_WEBRTC_PLAYOUT_DELAY: 'zero',
    POGO_BROWSER_DEVICE_SCALE_FACTOR: '1.5',
    POGO_BROWSER_WEBRTC_UDP_PORT_RANGE: '21000-21015',
  }), { ...defaults, network: 'lan', playoutDelay: 'zero', deviceScaleFactor: 1.5, udpPortRange: '21000-21015' });
});
