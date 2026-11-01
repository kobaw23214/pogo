import { createGeneralWebFixtures } from './fixtures/index.js';

// Human preview only. All controller/oracle methods remain Node functions;
// this process exposes only the fixtures' normal business HTTP pages.
const args = process.argv.slice(2);
const seed = args.length === 0 ? 20261003 : args.length === 2 && args[0] === '--seed' ? Number(args[1]) : NaN;
if (!Number.isInteger(seed) || seed < 0 || seed > 0xffffffff) {
  process.stderr.write('Usage: node eval/general-web/preview.js [--seed UINT32]\n');
  process.exitCode = 2;
} else {
  const fixtures = await createGeneralWebFixtures({ seed, host: '127.0.0.1', ports: [5191, 5192, 5193] });
  process.stdout.write(JSON.stringify({ type: 'general_web_preview', seed, scope: 'controlled_synthetic_sites',
    tasks: fixtures.tasks.map(({ id, title, startURL, goal }) => ({ id, title, url: startURL, goal })) }, null, 2) + '\n');
  let closing = false;
  const close = async () => {
    if (closing) return;
    closing = true;
    await fixtures.close();
  };
  process.once('SIGINT', close);
  process.once('SIGTERM', close);
}
