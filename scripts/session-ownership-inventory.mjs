// Read-only legacy inventory. It emits no conversation bodies and never assigns
// ownership. An operator must supply a verified mapping before migration.
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, lstatSync } from 'node:fs';
import { resolve, join, basename } from 'node:path';

const source = process.argv[2];
if (!source) { console.error('Usage: node scripts/session-ownership-inventory.mjs <legacy-session-directory>'); process.exit(2); }
const root = resolve(source);
const entries = [];
for (const name of readdirSync(root).filter(name => name.endsWith('.jsonl')).sort()) {
  const path = join(root, name);
  if (!lstatSync(path).isFile()) continue;
  const bytes = readFileSync(path);
  let model = '', malformedRecords = 0;
  for (const line of bytes.toString('utf8').split(/\r?\n/)) {
    if (!line.trim()) continue;
    try { const r = JSON.parse(line); if (r.type === 'meta' && typeof r.meta?.model === 'string') model = r.meta.model; }
    catch { malformedRecords++; }
  }
  entries.push({ session_id: basename(name, '.jsonl'), bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex'), model, malformed_records: malformedRecords, owner_mapping: null });
}
console.log(JSON.stringify({ format: 'pogo-ownership-inventory-v1', source_dir: root, mode: 'read-only', entries }, null, 2));
