import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { originalBundleSHA256, patchedBundle, sha256 } from '../scripts/patch-playwright-disposable.mjs';

const require = createRequire(import.meta.url);
const source = await readFile(join(dirname(require.resolve('playwright-core/package.json')), 'lib/coreBundle.js'), 'utf8');

test('pinned producer patch is exact, idempotent, and rejects version or source drift', () => {
  const patched = patchedBundle(source, '1.63.0');
  assert.match(patched, /scope\._type === "Page" \|\| scope\._type === "BrowserContext"/);
  assert.match(patched, /throw new TargetClosedError\(scope\._object\.closeReason\(\)\);\n        super\(scope, disposable, "Disposable"/);
  assert.equal(patchedBundle(patched, '1.63.0'), patched);
  assert.throws(() => patchedBundle(source, '1.64.0'), /VERSION_MISMATCH/);
  assert.throws(() => patchedBundle(source + '\n', '1.63.0'), /SOURCE_MISMATCH/);
  assert.throws(() => patchedBundle(patched.replace('throw new TargetClosedError(scope._object.closeReason());', 'return;'), '1.63.0'), /SOURCE_MISMATCH/);
  assert.equal(sha256(source) === originalBundleSHA256 || source === patched, true);
});

test('Docker applies the checked-in patch after installing pinned dependencies', async () => {
  const dockerfile = await readFile(new URL('../Dockerfile', import.meta.url), 'utf8');
  assert.match(dockerfile, /COPY scripts\/patch-playwright-disposable\.mjs/);
  assert.match(dockerfile, /npm ci[^\n]*\\\n\s*&& node scripts\/patch-playwright-disposable\.mjs/);
});
