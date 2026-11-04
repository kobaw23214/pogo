#!/usr/bin/env node
// Local macOS development packaging using Electron's documented Resources/app
// layout. This produces an ad-hoc signed app, not a notarized distribution.
// https://www.electronjs.org/docs/latest/tutorial/application-distribution
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createRequire } from 'node:module';
import { createHash, randomUUID } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const run = promisify(execFile);
const desktop = path.dirname(fileURLToPath(import.meta.url));
const pogo = path.dirname(desktop);
const repo = path.dirname(pogo);
const runtime = path.join(pogo, 'browser-runtime');
const args = process.argv.slice(2);
if (args.some(arg => arg !== '--dry-run')) throw new Error('Usage: node pogo/desktop/package-app.mjs [--dry-run]');
const dryRun = args.includes('--dry-run');
if (process.platform !== 'darwin' || process.arch !== 'arm64') throw new Error('This local package target requires macOS arm64');
const artifacts = path.join(repo, '.localagent/checks/desktop-integration-20261008/artifacts');
const target = path.join(artifacts, 'Pogo Desktop.app');
const electronRoot = path.join(desktop, 'node_modules/electron');
const electronApp = path.join(electronRoot, 'dist/Electron.app');
const electronVersion = JSON.parse(await fs.readFile(path.join(electronRoot, 'package.json'), 'utf8')).version;
const metadata = JSON.parse(await fs.readFile(path.join(desktop, 'package.json'), 'utf8'));
const architecture = (await run('/usr/bin/lipo', ['-archs', path.join(electronApp, 'Contents/MacOS/Electron')])).stdout.trim();
if (architecture !== 'arm64') throw new Error('Installed Electron binary is not the expected arm64 build');

const excludedDirectories = new Set(['node_modules', '.local-browsers', '.cache', '.git', 'test', 'tests', '__tests__', 'fixtures', 'test-results', 'playwright-report', 'coverage']);
const forbiddenFiles = /^(?:\.env(?:\..*)?|\.npmrc|\.DS_Store|desktop\.json|users\.json|auth\.json|credentials(?:\..*)?|id_rsa|id_ed25519)$/i;
async function collect(root, relative = '') {
  const files = [];
  for (const entry of await fs.readdir(path.join(root, relative), { withFileTypes: true })) {
    if (forbiddenFiles.test(entry.name)) continue;
    const child = path.join(relative, entry.name);
    if (entry.isSymbolicLink()) throw new Error(`Unexpected payload symlink: ${child}`);
    if (entry.isDirectory()) { if (!excludedDirectories.has(entry.name)) files.push(...await collect(root, child)); }
    else if (entry.isFile()) files.push(child);
  }
  return files.sort();
}
const payload = [];
for (const entry of await fs.readdir(desktop, { withFileTypes: true })) {
  if (entry.isFile() && /\.(?:cjs|mjs)$/.test(entry.name) && entry.name !== 'package-app.mjs') payload.push({ source: path.join(desktop, entry.name), relative: `desktop/${entry.name}` });
}
if (!payload.some(file => file.relative === 'desktop/main.cjs')) throw new Error('Desktop entry point is missing');
for (const file of await collect(path.join(runtime, 'src'))) payload.push({ source: path.join(runtime, 'src', file), relative: `browser-runtime/src/${file}` });
payload.push({ source: path.join(runtime, 'package.json'), relative: 'browser-runtime/package.json' });

// Copy the installed production dependency closure, never all node_modules.
const dependencies = new Map();
async function dependency(name, from) {
  if (!/^(?:@[a-z0-9_.-]+\/)?[a-z0-9_.-]+$/i.test(name)) throw new Error('Invalid dependency name');
  const require = createRequire(path.join(from, 'package.json'));
  const packageFile = require.resolve(`${name}/package.json`);
  const root = path.dirname(packageFile);
  const pkg = JSON.parse(await fs.readFile(packageFile, 'utf8'));
  const previous = dependencies.get(name);
  if (previous) { if (previous.version !== pkg.version) throw new Error(`Cannot flatten conflicting dependency versions: ${name}`); return; }
  dependencies.set(name, { name, version: pkg.version });
  for (const file of await collect(root)) payload.push({ source: path.join(root, file), relative: `browser-runtime/node_modules/${name}/${file}` });
  for (const child of Object.keys(pkg.dependencies ?? {})) await dependency(child, root);
}
await dependency('playwright', runtime);
for (const file of payload) {
  if (forbiddenFiles.test(path.basename(file.relative))) throw new Error('Forbidden configuration file in payload');
  file.sha256 = createHash('sha256').update(await fs.readFile(file.source)).digest('hex');
}
const summary = {
  app: target, electronVersion, architecture, appVersion: metadata.version,
  payloadFiles: payload.length, dependencies: [...dependencies.values()],
  privateConfiguration: '~/Library/Application Support/Pogo Desktop/desktop.json',
  configurationEmbedded: false, tokensEmbedded: false,
  excluded: ['user configuration and credentials', 'browser profiles and caches', 'repository tests and lab fixtures', 'Go binary/workspace', 'Electron installer dependencies'],
  signing: 'local ad-hoc', notarized: false,
};
if (dryRun) { console.log(JSON.stringify({ ...summary, dryRun: true }, null, 2)); process.exit(0); }

await fs.mkdir(artifacts, { recursive: true });
const staging = path.join(artifacts, `.package-staging-${randomUUID()}`);
const stagedApp = path.join(staging, 'Pogo Desktop.app');
await fs.mkdir(staging);
let published = false;
async function setPlist(plist, key, value) {
  try { await run('/usr/libexec/PlistBuddy', ['-c', `Set :${key} ${value}`, plist]); }
  catch { await run('/usr/libexec/PlistBuddy', ['-c', `Add :${key} string ${value}`, plist]); }
}
async function signingItems(root) {
  const binaries = [], bundles = [];
  async function visit(directory) {
    for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
      const file = path.join(directory, entry.name);
      if (entry.isSymbolicLink()) continue;
      if (entry.isDirectory()) {
        await visit(file);
        if (/\.(?:app|framework|xpc)$/.test(entry.name)) bundles.push(file);
      } else if (entry.isFile()) {
        const handle = await fs.open(file, 'r');
        try {
          const header = Buffer.alloc(4), { bytesRead } = await handle.read(header, 0, 4, 0);
          if (bytesRead === 4 && [0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca].includes(header.readUInt32BE())) binaries.push(file);
        } finally { await handle.close(); }
      }
    }
  }
  await visit(root);
  return [...binaries, ...bundles, root];
}
try {
  // ditto preserves Electron's framework symlinks and executable permissions.
  await run('/usr/bin/ditto', [electronApp, stagedApp]);
  const resources = path.join(stagedApp, 'Contents/Resources');
  await fs.rm(path.join(resources, 'default_app.asar'), { force: true });
  const appRoot = path.join(resources, 'app');
  await fs.mkdir(appRoot, { recursive: true });
  await fs.writeFile(path.join(appRoot, 'package.json'), JSON.stringify({ name: 'pogo-desktop', productName: 'Pogo Desktop', version: metadata.version, private: true, main: 'desktop/main.cjs' }, null, 2) + '\n');
  for (const file of payload) {
    const destination = path.join(appRoot, file.relative);
    await fs.mkdir(path.dirname(destination), { recursive: true });
    await fs.copyFile(file.source, destination);
    const copied = createHash('sha256').update(await fs.readFile(destination)).digest('hex');
    if (copied !== file.sha256) throw new Error(`Source changed during packaging: ${file.relative}; retry when source is frozen`);
  }
  const legal = path.join(resources, 'licenses');
  await fs.mkdir(legal, { recursive: true });
  for (const file of ['LICENSE', 'LICENSES.chromium.html']) await fs.copyFile(path.join(electronRoot, 'dist', file), path.join(legal, file));
  const plist = path.join(stagedApp, 'Contents/Info.plist');
  await setPlist(plist, 'CFBundleName', 'Pogo Desktop');
  await setPlist(plist, 'CFBundleDisplayName', 'Pogo Desktop');
  await setPlist(plist, 'CFBundleIdentifier', 'dev.pogo.desktop');
  await setPlist(plist, 'CFBundleShortVersionString', metadata.version);
  await setPlist(plist, 'CFBundleVersion', metadata.version);
  // npm's Electron archive may contain linker signatures without resource
  // seals. Sign the staging copy inside out, preserving each executable's
  // existing entitlements, then seal frameworks/helpers and the outer app.
  // This local ad-hoc seal does not confer Developer ID trust.
  const signed = await signingItems(stagedApp);
  for (const item of signed) await run('/usr/bin/codesign', ['--force', '--sign', '-', '--timestamp=none', '--preserve-metadata=entitlements,flags', item]);
  await run('/usr/bin/codesign', ['--verify', '--deep', '--strict', stagedApp]);

  // Import using the packaged Electron/Node and packaged dependency paths.
  // RUN_AS_NODE avoids launching a GUI, reading private config or binding ports.
  const env = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'LANG', 'LC_ALL'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
  env.ELECTRON_RUN_AS_NODE = '1';
  const serverURL = pathToFileURL(path.join(appRoot, 'browser-runtime/src/server.js')).href;
  const hostURL = pathToFileURL(path.join(appRoot, 'desktop/browser-host.mjs')).href;
  const inspectorURL = pathToFileURL(path.join(appRoot, 'desktop/native-inspector.mjs')).href;
  const probe = `Promise.all([import(${JSON.stringify(serverURL)}),import(${JSON.stringify(hostURL)}),import(${JSON.stringify(inspectorURL)})]).then(([worker,host,inspector])=>{if(typeof worker.createWorker!=='function'||typeof host.DesktopBrowserHost!=='function'||typeof inspector.inspectNativeNode!=='function')throw new Error('Missing packaged exports');process.stdout.write('PACKAGED_IMPORTS_OK\\n');}).catch(error=>{process.stderr.write(String(error));process.exitCode=1;});`;
  const imported = await run(path.join(stagedApp, 'Contents/MacOS/Electron'), ['-e', probe], { env, timeout: 30000 });
  if (!imported.stdout.includes('PACKAGED_IMPORTS_OK')) throw new Error('Packaged import check did not confirm success');
  for (const file of payload) {
    const current = createHash('sha256').update(await fs.readFile(file.source)).digest('hex');
    if (current !== file.sha256) throw new Error(`Source changed before publication: ${file.relative}; package was not published`);
  }
  const exists = async file => { try { await fs.lstat(file); return true; } catch (error) { if (error.code === 'ENOENT') return false; throw error; } };
  const previous = `Pogo Desktop.previous-${new Date().toISOString().replaceAll(':', '-')}`;
  const backup = await exists(target) ? path.join(artifacts, `${previous}.app`) : undefined;
  const manifestPath = path.join(artifacts, 'Pogo Desktop.package-manifest.json');
  const manifestBackup = await exists(manifestPath) ? path.join(artifacts, `${previous}.package-manifest.json`) : undefined;
  const manifest = { ...summary, createdAt: new Date().toISOString(), signatureVerified: true, signedComponents: signed.length, signingOrder: 'native binaries then nested bundles then outer app', packagedImportsPassed: true, GUIStarted: false, ...(backup ? { previousApp: backup } : {}), files: payload.map(({ relative, sha256 }) => ({ path: relative, sha256 })) };
  const stagedManifest = path.join(staging, 'package-manifest.json');
  await fs.writeFile(stagedManifest, JSON.stringify(manifest, null, 2) + '\n', { flag: 'wx' });
  let movedApp = false, movedManifest = false, installedApp = false, installedManifest = false;
  try {
    if (backup) { await fs.rename(target, backup); movedApp = true; }
    if (manifestBackup) { await fs.rename(manifestPath, manifestBackup); movedManifest = true; }
    await fs.rename(stagedApp, target); installedApp = true;
    await fs.rename(stagedManifest, manifestPath); installedManifest = true;
    published = true;
  } catch (error) {
    const failures = [];
    const restore = async operation => { try { await operation(); } catch (failure) { failures.push(failure); } };
    if (installedManifest) await restore(() => fs.rm(manifestPath));
    if (installedApp) await restore(() => fs.rm(target, { recursive: true }));
    if (movedManifest) await restore(() => fs.rename(manifestBackup, manifestPath));
    if (movedApp) await restore(() => fs.rename(backup, target));
    if (failures.length) throw new AggregateError([error, ...failures], 'Package publication and restoration failed; preserve timestamped backups for recovery');
    throw error;
  }
  console.log(JSON.stringify({ ...summary, signatureVerified: true, packagedImportsPassed: true, GUIStarted: false, manifest: manifestPath }, null, 2));
} finally {
  await fs.rm(staging, { recursive: true, force: true });
  if (!published) console.error('No new app was published. Inspect the error and any timestamped backups before retrying. Private configuration was not accessed.');
}
