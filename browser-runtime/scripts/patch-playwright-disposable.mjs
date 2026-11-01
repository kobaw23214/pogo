import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Playwright 1.63 can finish addInitScript/exposeBinding after its Page or
// BrowserContext dispatcher was disposed. Do not emit a child __create__ for
// that missing parent: the normal request dispatcher must reject the operation.
// Keep this pinned, reviewable patch at the producer, not a global exception
// handler which would conceal unrelated failures on the shared connection.
export const playwrightVersion = '1.63.0';
export const originalBundleSHA256 = '549070af3acabb3efcc4f55bfe6210f9f7c2fcf633cf7eaa59bfe60719969171';
const before = `    DisposableDispatcher = class extends Dispatcher {
      constructor(scope, disposable) {
        super(scope, disposable, "Disposable", {});`;
const after = `    DisposableDispatcher = class extends Dispatcher {
      constructor(scope, disposable) {
        // Pogo: reject late initialization through the original request channel.
        if ((scope._type === "Page" || scope._type === "BrowserContext") && scope._disposed)
          throw new TargetClosedError(scope._object.closeReason());
        super(scope, disposable, "Disposable", {});`;

export const sha256 = value => createHash('sha256').update(value).digest('hex');

export function patchedBundle(source, version) {
  if (version !== playwrightVersion) throw new Error('PLAYWRIGHT_DISPOSABLE_PATCH_VERSION_MISMATCH');
  const occurrences = source.split(after).length - 1;
  if (occurrences === 1 && sha256(source.replace(after, before)) === originalBundleSHA256) return source;
  if (sha256(source) !== originalBundleSHA256 || source.split(before).length !== 2) {
    throw new Error('PLAYWRIGHT_DISPOSABLE_PATCH_SOURCE_MISMATCH');
  }
  return source.replace(before, after);
}

export async function patchPlaywright(packageRoot) {
  const metadata = JSON.parse(await readFile(join(packageRoot, 'package.json'), 'utf8'));
  const path = join(packageRoot, 'lib/coreBundle.js');
  const source = await readFile(path, 'utf8');
  const patched = patchedBundle(source, metadata.version);
  if (patched !== source) await writeFile(path, patched);
  return { version: metadata.version, changed: patched !== source, sha256: sha256(patched) };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const require = createRequire(import.meta.url);
  const result = await patchPlaywright(dirname(require.resolve('playwright-core/package.json')));
  console.log(JSON.stringify({ patch: 'disposed-parent-disposable', ...result }));
}
