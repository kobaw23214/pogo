import { parseOptions, runSuite } from './runner.js';
import { TASKS } from './fixtures/index.js';
import { makeRedactor } from '../transport.js';

const cancellation = new AbortController();
const cancel = () => cancellation.abort(new Error('general web evaluation cancelled'));
process.on('SIGINT', cancel); process.on('SIGTERM', cancel);
try {
  const options = parseOptions(process.argv.slice(2));
  if (options.help) process.stdout.write('General Web v1: controlled synthetic sites; URL + business goal; natural answers. Historical browser/auth scores unchanged.\nnode eval/general-web/run.js --mode validate [--control reference|noop] [--seed 20261003] [--repeat 1] [--out NEW_DIR]\nnode eval/general-web/run.js --mode model --agent PATH --providers PATH --model MODEL [--seed 20261003] [--repeat 1] [--out NEW_DIR]\nOptions: --list --tasks id,id --max-turns 40 --token-budget 60000 --timeout-ms 180000 --max-steps 50. Each repeat uses seed+repeat; serial isolated contexts. No provider files are read by the runner.\n');
  else if (options.list) process.stdout.write(JSON.stringify(TASKS, null, 2) + '\n');
  else {
    const { report, outputDir } = await runSuite(options, { abortSignal: cancellation.signal, onProgress: event => process.stdout.write(JSON.stringify(event) + '\n') });
    process.stdout.write(JSON.stringify({ type: 'run_end', output_dir: outputDir, summary: report.summary }) + '\n');
    const negative = options.mode === 'validate' && options.control === 'noop';
    if (report.cancelled || report.infrastructure_error || report.tasks.some(row => negative ? row.status !== 'failed' : row.status !== 'passed')) process.exitCode = 1;
  }
} catch (error) { process.stderr.write(`General Web evaluation setup failed: ${makeRedactor()(error.message)}\n`); process.exitCode = 2; }
finally { process.off('SIGINT', cancel); process.off('SIGTERM', cancel); }
