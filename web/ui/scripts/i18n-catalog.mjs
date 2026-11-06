// Export reviewed source messages for offline translation tooling. No network.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import ts from 'typescript';
const root = path.resolve(import.meta.dirname, '../src/i18n/messages');
export function flatten(value, prefix = '', result = {}) {
  for (const [key, text] of Object.entries(value ?? {})) {
    if (typeof text === 'string') result[prefix + key] = text;
    else if (text && typeof text === 'object') flatten(text, prefix + key + '.', result);
  }
  return result;
}
export function sourceCatalog() {
  const result = {};
  for (const filename of fs.readdirSync(root).filter(name => name.endsWith('.ts')).sort()) {
    const source = fs.readFileSync(path.join(root, filename), 'utf8');
    const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText;
    const box = { exports: {} };
    vm.runInNewContext(code, box, { filename });
    const mod = box.exports.default;
    if (!mod?.namespace || result[mod.namespace]) throw new Error(`Invalid or duplicate message namespace: ${filename}`);
    result[mod.namespace] = { file: filename, zh: flatten(mod.messages['zh-CN']), en: flatten(mod.messages.en) };
  }
  return result;
}
if (process.argv[1] === import.meta.filename) process.stdout.write(JSON.stringify(sourceCatalog(), null, 2) + '\n');
