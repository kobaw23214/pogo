import { parseOptions, runSuite } from './runner.js';
import { TASKS } from './fixtures.js';
import { makeRedactor } from '../transport.js';

const cancellation = new AbortController();
const cancel = () => cancellation.abort(new Error('browser-use-v2 evaluation cancelled'));
process.on('SIGINT', cancel); process.on('SIGTERM', cancel);
try {
  const options = parseOptions(process.argv.slice(2));
  if (options.help) process.stdout.write(`Browser Use v2: controlled synthetic sites and controller-simulated login. Separate from historical scores.
node eval/browser-use-v2/run.js --mode validate --control reference|noop|fabricated [--seed 20261006] [--repeat 1] [--out NEW_DIR]
node eval/browser-use-v2/run.js --mode model --agent PATH --model MODEL --max-turns 50 --token-budget 60000 --timeout-ms 180000 --max-steps 80 [--providers PATH] [--out NEW_DIR]
Options: --list --tasks id,id --seed UINT32 --repeat 1..10. Model runs require explicit agent, model and all four budgets.
Provider files are passed to the adapter, never read by the runner. Each output directory must be new.
`);
  else if (options.list) process.stdout.write(JSON.stringify(TASKS, null, 2) + '\n');
  else {
    const { report, outputDir } = await runSuite(options, { abortSignal: cancellation.signal, onProgress: event => process.stdout.write(JSON.stringify(event) + '\n') });
    process.stdout.write(JSON.stringify({ type: 'run_end', output_dir: outputDir, summary: report.summary }) + '\n');
    const negative = options.mode === 'validate' && ['noop', 'fabricated'].includes(options.control);
    if (!report.summary.run_valid || report.tasks.some(row => negative ? row.status !== 'failed' : row.status !== 'passed')) process.exitCode = 1;
  }
} catch (error) { process.stderr.write(`Browser Use v2 setup failed: ${makeRedactor()(error.message)}\n`); process.exitCode = 2; }
finally { process.off('SIGINT', cancel); process.off('SIGTERM', cancel); }
