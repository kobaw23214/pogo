import test from 'node:test';
import assert from 'node:assert/strict';
import { runAdapter, readUsage, failureCategory } from '../adapter.js';
import { makeRedactor } from '../transport.js';

const environment = Object.fromEntries(['PATH', 'HOME', 'TMPDIR', 'SYSTEMROOT'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
const run = (source, options = {}) => runAdapter({ executable: process.execPath, args: ['--input-type=module', '-e', source], input: { prompt: 'Test input 中文' }, environment,
  timeoutMs: 3000, redact: makeRedactor({}), ...options });

test('JSONL accepts fragmented UTF-8, multiple events and a final line without newline', async () => {
  const events = [];
  const result = await run(`
    let input='';for await(const part of process.stdin)input+=part;
    const request=JSON.parse(input);
    const events=[{type:'tool_start',name:'browser'}, {type:'tool_end',ok:true}, {type:'message',text:'中文🙂',usage:{input:7,output:3}}, {type:'end',answer:request.prompt,usage:{input:7,output:3},usage_measured:true}];
    const bytes=Buffer.from(events.map(e=>JSON.stringify(e)).join('\\n'));
    for(let i=0;i<bytes.length;i++){process.stdout.write(bytes.subarray(i,i+1));if(bytes[i]>=128)await new Promise(r=>setTimeout(r,1))}
  `, { onEvent: event => events.push(event) });
  assert.equal(result.exitCode, 0);
  assert.equal(result.error, null);
  assert.equal(result.timedOut, false);
  assert.equal(result.end.answer, 'Test input 中文');
  assert.deepEqual(events.map(e => e.type), ['tool_start', 'tool_end', 'message', 'end']);
  assert.equal(events[2].text, '中文🙂');
  assert.equal(result.usage.input, 7, 'final aggregate must not double count message usage');
  assert.equal(result.usage.output, 3);
  assert.equal(result.usage_partial, false);
});

for (const [name, output, expected] of [
  ['malformed JSON', 'not-json\n', /invalid JSONL/],
  ['unsupported event', '{"type":"unknown"}\n', /unknown event/],
  ['event after final result', '{"type":"end","answer":"done"}\n{"type":"message","text":"late"}\n', /after its final result/],
]) test(`protocol rejects ${name}`, async () => {
  const result = await run(`process.stdout.write(${JSON.stringify(output)})`);
  assert.match(result.error, expected);
});

test('missing executable is an explicit infrastructure error', async () => {
  const result = await runAdapter({ executable: '/definitely-not-a-browser-evaluation-adapter', input: {}, environment, timeoutMs: 1000, redact: value => value });
  assert.equal(result.exitCode, null);
  assert.match(result.error, /could not be started/);
  assert.equal(result.usage, null);
});

test('unreported usage remains unknown and partial message usage is retained', async () => {
  const unknown = await run(`console.log(JSON.stringify({type:'end',answer:'No usage provided',usage_measured:false}))`);
  assert.equal(unknown.usage, null);
  assert.equal(unknown.usage_partial, true);
  const partial = await run(`console.log(JSON.stringify({type:'message',usage:{input:9,output:2}}));console.log(JSON.stringify({type:'end',usage_measured:false,answer:'partial'}))`);
  assert.equal(partial.usage.input, 9);
  assert.equal(partial.usage.output, 2);
  assert.equal(partial.usage_partial, true);
  assert.equal(readUsage(null), null);
  assert.equal(readUsage({ input: -1, output: Infinity }), null);
  assert.equal(readUsage({ input: '100', output: null }), null);
});

test('missing usage fields stay unknown instead of becoming measured zeros', () => {
  const usage = readUsage({ input: 9, output: 2 });
  assert.equal(usage.input, 9);
  assert.equal(usage.output, 2);
  assert.equal(usage.cache_read, null);
  assert.equal(usage.reasoning, null);
});

test('an explicitly measured zero total stays zero while default zero usage remains unknown', async () => {
  const result = await run(`console.log(JSON.stringify({type:'end',usage:{input:0,output:0},usage_measured:true}))`);
  assert.equal(result.usage.input, 0);
  assert.equal(result.usage.output, 0);
  assert.equal(result.usage.cache_read, null);
  assert.equal(readUsage({ input: 0, output: 0 }), null);
});

test('partial usage aggregation does not invent missing fields or claim aborted totals are complete', async () => {
  const result = await run(`
    console.log(JSON.stringify({type:'message',usage:{input:9,output:2,cache_read:3}}));
    console.log(JSON.stringify({type:'message',usage:{input:5,output:1}}));
    console.log(JSON.stringify({type:'end',answer:'aborted',end_reason:'api_error',usage_measured:false}));
  `);
  assert.equal(result.usage.input, 14);
  assert.equal(result.usage.output, 3);
  assert.equal(result.usage.cache_read, null);
  assert.equal(result.usage.reasoning, null);
  assert.equal(result.usage_partial, true);
});

test('adapter diagnostic redacts inherited dummy environment secrets', async () => {
  const secret = 'test-only-adapter-key-628504';
  const env = { ...environment, TEST_API_KEY: secret };
  const result = await run(`process.stderr.write('failure '+process.env.TEST_API_KEY+' Bearer local-test-token');console.log(JSON.stringify({type:'end',answer:'done'}))`, { environment: env, redact: makeRedactor(env) });
  assert.doesNotMatch(result.diagnostic, /test-only-adapter-key-628504|local-test-token/);
  assert.match(result.diagnostic, /\[REDACTED\]/);
});

test('timeout terminates a child which ignores SIGTERM', { timeout: 8000 }, async () => {
  let pid;
  const result = await run(`process.on('SIGTERM',()=>{});console.log(JSON.stringify({type:'message',pid:process.pid}));setInterval(()=>{},1000)`, { timeoutMs: 200, onEvent: event => { pid = event.pid; } });
  assert.equal(result.timedOut, true);
  assert.ok(pid);
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
});

test('timeout reaps an owned descendant even when its parent exits on SIGTERM', { timeout: 8000, skip: process.platform === 'win32' }, async () => {
  let childPID, parentPID;
  try {
    const result = await run(`
      import {spawn} from 'node:child_process';
      const child=spawn(process.execPath,['-e',"process.on('SIGTERM',()=>{});process.send('ready');setInterval(()=>{},1000)"],{stdio:['ignore','ignore','ignore','ipc']});
      child.on('message',()=>{console.log(JSON.stringify({type:'message',child_pid:child.pid,parent_pid:process.pid}));child.disconnect()});
      setInterval(()=>{},1000);
    `, { timeoutMs: 500, onEvent: event => { childPID = event.child_pid; parentPID = event.parent_pid; } });
    assert.equal(result.timedOut, true);
    assert.ok(childPID);
    await new Promise(resolve => setTimeout(resolve, 1700));
    assert.throws(() => process.kill(childPID, 0), { code: 'ESRCH' });
  } finally {
    // The regression is safe even against an unfixed adapter: only the PID and
    // process group created by this test are cleaned up here.
    if (parentPID) { try { process.kill(-parentPID, 'SIGKILL'); } catch {} }
    if (childPID) { try { process.kill(childPID, 'SIGKILL'); } catch {} }
  }
});

test('abort signal terminates an active adapter and keeps observed usage partial', { timeout: 6000 }, async () => {
  const controller = new AbortController();
  let pid;
  const result = await run(`
    process.on('SIGTERM',()=>{});
    console.log(JSON.stringify({type:'message',pid:process.pid,usage:{input:11,output:3}}));
    setInterval(()=>{},1000);
  `, { timeoutMs: 20_000, abortSignal: controller.signal, onEvent: event => { pid = event.pid; controller.abort(new Error('test cancellation')); } });
  assert.ok(pid);
  assert.equal(result.aborted, true);
  assert.equal(result.timedOut, false, 'cancellation is not a task timeout');
  assert.equal(result.usage_partial, true);
  assert.equal(result.usage.input, 11);
  assert.equal(result.usage.output, 3);
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
});

test('an already aborted signal does not let an adapter continue running', { timeout: 5000 }, async () => {
  const controller = new AbortController();
  controller.abort(new Error('already stopped'));
  const result = await run('setInterval(()=>{},1000)', { timeoutMs: 20_000, abortSignal: controller.signal });
  assert.equal(result.aborted, true);
  assert.equal(result.timedOut, false);
  assert.equal(result.end, null);
});

test('failure classification keeps duplicate submissions and budget outcomes explicit', () => {
  assert.equal(failureCategory({ status: 'passed', duplicate_submissions: 1 }), 'duplicate_submission');
  assert.equal(failureCategory({ status: 'timeout' }), 'timeout');
  assert.equal(failureCategory({ status: 'budget_exceeded' }), 'budget_or_schedule');
  assert.equal(failureCategory({ status: 'failed', failures: ['stale ref e2'] }), 'stale_observation');
  assert.equal(failureCategory({ status: 'error', reason: 'dialog requires a human' }), 'human_needed');
});
