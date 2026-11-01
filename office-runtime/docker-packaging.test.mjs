// Read-only packaging tests. Compose sees synthetic config and no daemon.
// Optional live proxy tests read only the existing isolated DocServer assets.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { createEngineProxy } from './engine-proxy.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const manifest = JSON.parse(await fs.readFile(path.join(root, 'engine-manifest.json'), 'utf8'));
const hash = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');

test('Docker image pins the same engine identity and copies only an explicit Node runtime', async () => {
  const dockerfile = await fs.readFile(path.join(root, 'Dockerfile'), 'utf8');
  assert.ok(dockerfile.includes('ARG OFFICE_ENGINE_IMAGE=' + manifest.upstreamImageDigest));
  assert.match(dockerfile, /FROM \$\{OFFICE_ENGINE_IMAGE\}/);
  assert.match(dockerfile, /ARG OFFICE_ENGINE_MANIFEST=engine-manifest\.json/);
  assert.match(dockerfile, /COPY \$\{OFFICE_ENGINE_MANIFEST\} \.\/engine-manifest\.json/);
  assert.match(dockerfile, /verify-platform\.py/);
  assert.match(dockerfile, /ARG NODE_RUNTIME_IMAGE=node@sha256:[a-f0-9]{64}/);
  assert.match(dockerfile, /FROM \$\{NODE_RUNTIME_IMAGE\} AS node-runtime/);
  assert.match(dockerfile, /COPY --from=node-runtime \/usr\/local\/bin\/node/);
  assert.match(dockerfile, /EXPOSE 8080/);
  assert.doesNotMatch(dockerfile, /EXPOSE 80(?:\s|$)/);
});

test('Office platform locks reject architecture and engine-manifest substitutions', async () => {
  const manifests = {
    arm64: manifest,
    amd64: JSON.parse(await fs.readFile(path.join(root, 'engine-manifest.amd64.json'), 'utf8')),
  };
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'pogo-office-platform-test-'));
  try {
    for (const [arch, lock] of Object.entries(manifests)) {
      assert.equal(lock.platform, 'linux/' + arch);
      const filename = path.join(temp, arch + '.json');
      await fs.writeFile(filename, JSON.stringify(lock));
      const invoke = (platform, image, runtimeArch) => spawnSync('python3', [path.join(root, 'verify-platform.py'),
        '--manifest', filename, '--platform', platform, '--upstream-image', image,
        '--runtime-architecture', runtimeArch], { encoding: 'utf8' });
      assert.equal(invoke(lock.platform, lock.upstreamImageDigest, arch).status, 0);
      const other = arch === 'arm64' ? 'amd64' : 'arm64';
      assert.notEqual(invoke('linux/' + other, lock.upstreamImageDigest, other).status, 0);
      assert.notEqual(invoke(lock.platform, manifests[other].upstreamImageDigest, arch).status, 0);
      assert.notEqual(invoke(lock.platform, lock.upstreamImageDigest, other).status, 0);
    }
    assert.notEqual(manifests.amd64.upstreamImageDigest, manifests.arm64.upstreamImageDigest);
    assert.equal(manifests.amd64.engineVersion, manifests.arm64.engineVersion);
    assert.deepEqual(manifests.amd64.assets.map((entry) => entry.path), manifests.arm64.assets.map((entry) => entry.path));
    assert.deepEqual(manifests.amd64.loaderAssets.map((entry) => entry.path), manifests.arm64.loaderAssets.map((entry) => entry.path));
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});

test('existing CLI cannot bind a LAN listener or proxy arbitrary upstreams', async () => {
  for (const options of [{ listenHost: '0.0.0.0' }, { listenHost: '192.0.2.1', containerListen: true }, { upstream: 'http://office:80' }, { upstream: 'https://127.0.0.1:80' }]) {
    await assert.rejects(createEngineProxy(options));
  }
});

test('Docker supplies the fixed cache tag to the official API template generation', async () => {
  const result = spawnSync(process.execPath, [path.join(root, 'docker-engine-identity.mjs')], { encoding: 'utf8' });
  assert.equal(result.status, 0);
  assert.equal(result.stdout.trim(), manifest.vendorVersion.slice(manifest.vendorVersion.indexOf('-') + 1));
  assert.match(await fs.readFile(path.join(root, 'docker-entrypoint.sh'), 'utf8'), /export HASH="\$cache_tag"/);
});

test('JWT entry rejects missing and unsafe input without leaking its contents', async () => {
  const temp = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'pogo-office-secret-test-')));
  try {
    for (const [index, value] of ['', 'too-short', "synthetic-invalid-'secret".repeat(3), 'a'.repeat(32) + '\nb'.repeat(2), 'a'.repeat(8193)].entries()) {
      const filename = path.join(temp, String(index));
      await fs.writeFile(filename, value, { mode: 0o600 });
      const result = spawnSync('bash', [path.join(root, 'docker-entrypoint.sh')], { env: { PATH: process.env.PATH, POGO_OFFICE_JWT_SECRET_FILE: filename }, encoding: 'utf8' });
      assert.equal(result.status, 1);
      assert.equal(result.stdout, '');
      assert.match(result.stderr, /JWT secret must contain/);
      if (value.length > 8) assert.ok(!result.stderr.includes(value));
    }
    const result = spawnSync('bash', [path.join(root, 'docker-entrypoint.sh')], { env: { PATH: process.env.PATH, POGO_OFFICE_JWT_SECRET_FILE: path.join(temp, 'missing') }, encoding: 'utf8' });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /secret file is unavailable/);
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});

test('Office Compose requires dedicated secrets and keeps the adapter on its private network', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'pogo-office-compose-test-'));
  try {
    await fs.writeFile(path.join(temp, 'base.yml'), 'services:\n  pogo:\n    image: pogo:synthetic-test\n');
    await fs.copyFile(path.join(root, '../../w_deploy/compose.office.yml'), path.join(temp, 'office.yml'));
    await fs.writeFile(path.join(temp, 'empty.env'), '');
    const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('POGO_') && !key.startsWith('DOCKER_') && !key.startsWith('COMPOSE_')));
    env.DOCKER_HOST = 'unix:///nonexistent-pogo-office-test.sock';
    const args = ['compose', '--project-name', 'pogo-office-config-test', '--project-directory', temp, '--env-file', path.join(temp, 'empty.env'), '-f', path.join(temp, 'base.yml'), '-f', path.join(temp, 'office.yml'), 'config', '--format', 'json'];
    const missing = spawnSync('docker', args, { env, encoding: 'utf8', timeout: 30000 });
    assert.notEqual(missing.status, 0);
    const result = spawnSync('docker', args, { env: { ...env, POGO_OFFICE_JWT_SECRET_HOST_FILE: '/synthetic/office-jwt', POGO_OFFICE_IMAGE: 'sha256:' + 'a'.repeat(64) }, encoding: 'utf8', timeout: 30000 });
    assert.equal(result.status, 0, 'Synthetic Compose parsing failed');
    const config = JSON.parse(result.stdout);
    const app = config.services.pogo, office = config.services['pogo-office'];
    assert.equal(app.environment.POGO_OFFICE_PUBLIC_URL, 'http://localhost:19770');
    assert.equal(app.environment.POGO_OFFICE_BROWSER_URL, undefined);
    assert.equal(app.environment.POGO_OFFICE_SERVICE_URL, 'http://pogo-office:8080');
    assert.equal(app.environment.POGO_OFFICE_FETCH_URL, 'http://pogo:7777');
    assert.equal(office.platform, 'linux/arm64');
    assert.equal(office.environment.ALLOW_META_IP_ADDRESS, 'false');
    assert.equal(office.environment.JWT_SECRET, undefined);
    assert.equal(office.ports, undefined);
    assert.deepEqual(office.expose, ['8080']);
    for (const service of [app, office]) {
      const secret = service.volumes.find((volume) => volume.target === '/run/secrets/pogo-office-jwt');
      assert.equal(secret.type, 'bind');
      assert.equal(secret.read_only, true);
      assert.equal(secret.bind.create_host_path, false);
    }
    assert.equal(office.volumes.filter((volume) => volume.type === 'volume').length, 4);
    assert.equal(app.depends_on?.['pogo-office'], undefined);
    assert.deepEqual(office.tmpfs, ['/run/pogo-office-private:rw,nosuid,nodev,noexec,size=64k,mode=0710']);
    const amd64 = spawnSync('docker', args, { env: { ...env, POGO_PLATFORM: 'linux/amd64',
      POGO_OFFICE_JWT_SECRET_HOST_FILE: '/synthetic/office-jwt', POGO_OFFICE_IMAGE: 'sha256:' + 'b'.repeat(64) },
      encoding: 'utf8', timeout: 30000 });
    assert.equal(amd64.status, 0, 'AMD64 Office Compose parsing failed');
    const amdOffice = JSON.parse(amd64.stdout).services['pogo-office'];
    assert.equal(amdOffice.platform, 'linux/amd64');
    assert.equal(amdOffice.ports, undefined);
    assert.deepEqual(amdOffice.volumes, office.volumes);
    assert.deepEqual(amdOffice.tmpfs, office.tmpfs);
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});

test('pinned resources, adapter health, runtime version path and hash refusal', { skip: !process.env.OFFICE_PROXY_TEST_UPSTREAM }, async () => {
  const upstream = process.env.OFFICE_PROXY_TEST_UPSTREAM;
  const proxy = await createEngineProxy({ upstream, port: 0, healthEndpoint: true });
  try {
    assert.equal(Object.keys(proxy.state.assets).length, 9);
    const result = await fetch(proxy.state.url + '/pogo-office/healthz');
    const health = await result.json();
    assert.equal(result.status, 200);
    assert.equal(health.ready, true);
    assert.equal(health.verifiedAssets, 9);
    const loader = await (await fetch(proxy.state.url + '/web-apps/apps/api/documents/api.js')).text();
    assert.ok(loader.includes(proxy.state.runtimeVersion));
    for (const entry of manifest.assets) {
      const bytes = Buffer.from(await (await fetch(proxy.state.url + '/' + proxy.state.runtimeVersion + entry.path)).arrayBuffer());
      assert.equal(hash(bytes), proxy.state.assets[entry.path].servedSha256);
    }
    const unknown = await fetch(proxy.state.url + '/' + manifest.vendorVersion + '-pogo-' + '0'.repeat(64) + '/healthcheck');
    assert.equal(unknown.status, 400);
  } finally { await proxy.close(); }
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (input, options) => {
    const response = await originalFetch(input, options);
    if (String(input).endsWith(manifest.loaderAssets.at(-1).path)) {
      const bytes = Buffer.from(await response.arrayBuffer());
      bytes[0] ^= 1;
      return new Response(bytes, { status: response.status });
    }
    return response;
  };
  try { await assert.rejects(createEngineProxy({ upstream, port: 0 }), /Loader hash mismatch/); }
  finally { globalThis.fetch = originalFetch; }
});
