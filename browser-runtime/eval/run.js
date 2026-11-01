import { parseOptions, runSuite } from './runner.js';
import { TASKS } from './fixtures/index.js';
import { makeRedactor } from './transport.js';

const cancellation = new AbortController();
const cancel = () => cancellation.abort(new Error('evaluation cancelled'));
process.on('SIGINT', cancel);
process.on('SIGTERM', cancel);

try {
  const options = parseOptions(process.argv.slice(2));
  if (options.help) {
    process.stdout.write(`Browser task evaluation (isolated local fixture sites)\n\n` +
      `node eval/run.js --mode validate [--seed 20261001] [--repeat 1] [--out DIR]\n` +
      `node eval/run.js --mode model --agent /absolute/path/to/browser-eval-agent --model MODEL [--providers FILE]\n\n` +
      `Options: --list, --tasks id,id, --concurrency 1..4, --max-turns 40,\n` +
      `--token-budget 100000, --timeout-ms 180000, --total-tokens N, --total-time-ms N,\n` +
      `The --total-* limits stop admission of new tasks; in-flight tasks retain their individual limits.\n` +
      `--no-frames. Validation-only: --control noop (negative judge control).\n` +
      `Model credentials stay in the provider's configured environment variable.\n` +
      `Validation uses reference scripts; its pass rate is NOT model task success.\n`);
  } else if (options.list) {
    process.stdout.write(JSON.stringify(TASKS, null, 2) + '\n');
  } else {
    const { report, outputDir } = await runSuite(options, { abortSignal: cancellation.signal, onProgress: event => process.stdout.write(JSON.stringify(event) + '\n') });
    process.stdout.write(JSON.stringify({ type: 'run_end', mode: report.mode, output_dir: outputDir, summary: report.summary }) + '\n');
    const expectedNegative = options.mode === 'fixture-validation' && options.control === 'noop';
    if (report.cancelled || report.infrastructure_error || report.tasks.some(row => expectedNegative ? row.status !== 'failed' : row.status !== 'passed')) process.exitCode = 1;
  }
} catch (error) {
  process.stderr.write(`Evaluation setup failed: ${makeRedactor()(error.message)}\n`);
  process.exitCode = 2;
} finally {
  process.off('SIGINT', cancel);
  process.off('SIGTERM', cancel);
}
