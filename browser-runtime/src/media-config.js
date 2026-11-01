// Media tuning is independent of the sole Web-port relay transport. Auto keeps
// W2's interactive quality/latency preset, historically named "lan"; that label
// does not infer the viewer's network from a Docker or private IP address.
// Explicit "wan" remains available for deployments that validate that preset.
function mediaNetwork(setting) {
  if (!['auto', 'lan', 'wan'].includes(setting)) throw new Error('POGO_BROWSER_WEBRTC_NETWORK must be auto, lan or wan');
  return setting === 'auto' ? 'lan' : setting;
}

// Zero playout delay (viewers render each frame when decoded instead of
// holding it for a jitter-buffer target) is a separate switch. 'auto' follows
// the network profile: zero on lan, the receiver default on wan.
function mediaPlayoutDelay(setting, network) {
  if (!['auto', 'zero', 'default'].includes(setting)) throw new Error('POGO_BROWSER_WEBRTC_PLAYOUT_DELAY must be auto, zero or default');
  if (setting !== 'auto') return setting;
  return network === 'lan' ? 'zero' : 'default';
}

// Deployment ceiling for each Sheet's native Wayland output density.
// Viewers request a density within this ceiling; CSS geometry stays unchanged.
export const MEDIA_DEVICE_SCALE_FACTORS = Object.freeze([1, 1.25, 1.5, 2]);
export const MEDIA_DEFAULT_DEVICE_SCALE_FACTOR = 2;
function deviceScaleFactor(value = '') {
  if (value === '') return MEDIA_DEFAULT_DEVICE_SCALE_FACTOR;
  if (!['1', '1.25', '1.5', '2'].includes(value)) throw new Error('POGO_BROWSER_DEVICE_SCALE_FACTOR must be 1, 1.25, 1.5 or 2');
  return Number(value);
}

// Deployment-owned configuration only. Neither a webpage nor a viewer can
// choose a capture target, TURN service, or advertised media endpoint.
export function mediaConfiguration(environment = process.env) {
  const relaySetting = environment.POGO_BROWSER_WEBRTC_RELAY_ONLY ?? '';
  if (!['', '1', 'true'].includes(relaySetting)) throw new Error('the browser worker only supports Web-port relay media');
  // A Web-port deployment owns the client TURN grant in the Pogo service.
  // Old .browser.env advertisements/ICE credentials must not escape through
  // worker offers, even when an upgrade retains that configuration file.
  const advertisedIP = '';
  const udpPortRange = environment.POGO_BROWSER_WEBRTC_UDP_PORT_RANGE || '19780-19811';
  const ports = /^(\d+)-(\d+)$/.exec(udpPortRange);
  if (!ports || Number(ports[1]) < 1024 || Number(ports[2]) > 65535 || Number(ports[2]) < Number(ports[1]) || Number(ports[2]) - Number(ports[1]) > 1023) throw new Error('POGO_BROWSER_WEBRTC_UDP_PORT_RANGE must be a bounded range within 1024–65535');
  const iceServers = [];
  const network = mediaNetwork(environment.POGO_BROWSER_WEBRTC_NETWORK || 'auto');
  const playoutDelay = mediaPlayoutDelay(environment.POGO_BROWSER_WEBRTC_PLAYOUT_DELAY || 'auto', network);
  return { advertisedIP, udpPortRange, iceServers, network, playoutDelay, deviceScaleFactor: deviceScaleFactor(environment.POGO_BROWSER_DEVICE_SCALE_FACTOR), relayOnly: true };
}
