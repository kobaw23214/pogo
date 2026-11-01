// Stages a pinned SDK extension offline. Never writes the input tree/container.
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
if (args.length !== 4 || args[0] !== '--input' || args[2] !== '--output') {
  throw new Error('Usage: node install-extension.mjs --input <original-sdkjs-directory> --output <empty-staging-directory>');
}
const input = path.resolve(args[1]), output = path.resolve(args[3]);
if (input === output || output.startsWith(`${input}${path.sep}`)) throw new Error('The output must be a separate staging tree, never the input SDK directory.');
const manifest = JSON.parse(await fs.readFile(path.join(root, 'engine-manifest.json'), 'utf8'));
const extension = await fs.readFile(path.join(root, 'engine-extension.js'));
const sha256 = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');
const prepared = [];
for (const asset of manifest.assets) {
  const relative = asset.path.replace(/^\/sdkjs\//, '');
  const original = await fs.readFile(path.join(input, relative));
  if (sha256(original) !== asset.sha256) throw new Error(`Unapproved SDK asset: ${relative}; staging was not started.`);
  const bytes = asset.appendExtension ? Buffer.concat([original, Buffer.from('\n;\n'), extension]) : original;
  prepared.push({ relative, bytes, originalSha256: asset.sha256, servedSha256: sha256(bytes) });
}
try { await fs.access(output); throw new Error('The staging output already exists; choose a new empty directory.'); }
catch (error) { if (error.code !== 'ENOENT') throw error; }
await fs.mkdir(output, { recursive: true });
for (const asset of prepared) {
  const target = path.join(output, asset.relative);
  await fs.mkdir(path.dirname(target), { recursive: true }); await fs.writeFile(target, asset.bytes, { flag: 'wx' });
}
await fs.writeFile(path.join(output, 'pogo-extension-manifest.json'), JSON.stringify({
  adapterId: manifest.adapterId, adapterVersion: manifest.adapterVersion, engineVersion: manifest.engineVersion,
  upstreamImageDigest: manifest.upstreamImageDigest, extensionSha256: sha256(extension),
  assets: prepared.map(({ bytes, ...asset }) => asset)
}, null, 2), { flag: 'wx' });
console.log(JSON.stringify({ staged: true, output, assets: prepared.length, extensionSha256: sha256(extension), inputUnchanged: true }));
