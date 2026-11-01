import { spawn } from 'node:child_process';

function numeric(value) { return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null; }
export function readUsage(value, explicitlyMeasured = false) {
  if (!value || typeof value !== 'object') return null;
  const usage = Object.fromEntries(['input', 'output', 'cache_read', 'reasoning'].map(key => [key, numeric(value[key])]));
  return (usage.input ?? 0) + (usage.output ?? 0) > 0 || explicitlyMeasured && usage.input !== null && usage.output !== null ? usage : null;
}

/** An adapter is a child process, not a shell expression. A separate process
 * group lets the watchdog reap the entire owned adapter without touching Pogo. */
export async function runAdapter({ executable, args = [], input, environment = process.env, timeoutMs, redact, onEvent = () => {}, abortSignal }) {
  return new Promise(resolve => {
    const child = spawn(executable, args, { env: environment, stdio: ['pipe', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    let buffer = '', bytes = 0, stderr = '', end = null, timedOut = false, aborted = false, problem = null, finished = false, killTimer;
    let usage = null;
    const signal = name => {
      if (!child.pid) return;
      try { if (process.platform === 'win32') child.kill(name); else process.kill(-child.pid, name); } catch { /* already reaped */ }
    };
    const stop = () => {
      signal('SIGTERM');
      killTimer ??= setTimeout(() => signal('SIGKILL'), 1500);
    };
    const timer = setTimeout(() => { timedOut = true; stop(); }, timeoutMs);
    const onAbort = () => { aborted = true; stop(); };
    abortSignal?.addEventListener('abort', onAbort, { once: true });
    const finish = code => {
      if (finished) return;
      finished = true;
      // The leader can exit while a child in its owned process group ignores
      // SIGTERM and has already closed stdout. Reap that group before clearing
      // the escalation timer; 'close' alone does not confirm group cleanup.
      if (killTimer) signal('SIGKILL');
      clearTimeout(timer); clearTimeout(killTimer);
      abortSignal?.removeEventListener('abort', onAbort);
      resolve({ end, exitCode: code, timedOut, aborted, error: problem, diagnostic: redact(stderr).slice(-4000),
        usage: end?.usage_measured !== false && readUsage(end?.usage, end?.usage_measured === true) || usage,
        usage_partial: !end || end.usage_measured === false || timedOut || aborted || code !== 0 || !!problem || !!end.error || /time_budget|abort|cancel|api_error|model_error/.test(end.end_reason ?? '') });
    };
    child.once('error', () => { problem = 'model adapter could not be started'; finish(null); });
    const accept = line => {
      if (!line.trim() || problem) return;
      let event;
      try { event = JSON.parse(line); } catch { problem = 'model adapter emitted invalid JSONL'; stop(); return; }
      if (!event || !['tool_start', 'tool_end', 'message', 'end'].includes(event.type)) { problem = 'model adapter emitted an unknown event'; stop(); return; }
      if (end) { problem = 'model adapter emitted events after its final result'; stop(); return; }
      if (event.type === 'end') end = event;
      if (event.type === 'message') {
        const part = readUsage(event.usage);
        if (part) {
          if (!usage) usage = { ...part };
          else for (const key of Object.keys(usage)) usage[key] = usage[key] === null || part[key] === null ? null : usage[key] + part[key];
        }
      }
      onEvent(event);
    };
    child.stdout.on('data', chunk => {
      bytes += Buffer.byteLength(chunk);
      if (bytes > 16 * 1024 * 1024) { problem = 'model adapter exceeded its output limit'; stop(); return; }
      buffer += chunk.toString();
      let newline;
      while ((newline = buffer.indexOf('\n')) !== -1) { const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1); accept(line); }
    });
    child.stderr.on('data', chunk => { stderr = (stderr + chunk.toString()).slice(-8000); });
    child.stdin.on('error', () => {});
    child.stdin.end(JSON.stringify(input) + '\n');
    child.once('close', code => { if (buffer.trim()) accept(buffer); finish(code); });
    if (abortSignal?.aborted) onAbort();
  });
}

export function failureCategory({ status, reason = '', failures = [], duplicate_submissions = 0 }) {
  if (duplicate_submissions > 0) return 'duplicate_submission';
  if (status === 'passed') return null;
  if (status === 'timeout') return 'timeout';
  if (status === 'budget_exceeded' || status === 'skipped') return 'budget_or_schedule';
  const last = `${reason} ${failures.at(-1) ?? ''}`.toLowerCase();
  if (/dialog|human|intervention/.test(last)) return 'human_needed';
  if (/stale|page changed/.test(last)) return 'stale_observation';
  if (/locator|ref not found|matching elements/.test(last)) return 'target_not_found';
  return status === 'error' ? 'execution_or_model_error' : 'wrong_result';
}
