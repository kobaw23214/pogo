import fs from 'node:fs/promises';
const manifest = JSON.parse(await fs.readFile(new URL('./engine-manifest.json', import.meta.url), 'utf8'));
const identity = manifest.vendorVersion.match(/^(\d+\.\d+\.\d+)-([a-f0-9]{32})$/);
if (!identity || identity[1] !== manifest.engineVersion.split(' ')[0]) throw new Error('Pinned engine vendor identity is invalid.');
console.log(identity[2]);
