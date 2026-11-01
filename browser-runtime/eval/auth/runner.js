import path from 'node:path';
import { makeRedactor } from '../transport.js';
import { retiredAuthProfile } from '../retired-profile.js';
import { TASKS } from './tasks.js';

function integer(value, min, max, name) {
  const number = Number(value); if (!Number.isInteger(number) || number < min || number > max) throw new Error(`${name} must be ${min}..${max}`); return number;
}
export function parseOptions(argv) {
  const options = { mode: 'validate', control: 'reference', repeat: 1, model: null, agent: null, providers: null, outputDir: null, tasks: null, maxTurns: 25, tokenBudget: 60000, timeoutMs: 180000, maxSteps: 40 };
  for (let index = 0; index < argv.length; index++) {
    const name = argv[index];
    if (name === '--help' || name === '--list') { options[name.slice(2)] = true; continue; }
    const value = argv[++index]; if (!value || value.startsWith('--')) throw new Error(`${name} requires a value`);
    if (['--mode', '--control', '--model'].includes(name)) options[name.slice(2)] = value;
    else if (['--agent', '--providers', '--out'].includes(name)) options[name === '--out' ? 'outputDir' : name.slice(2)] = path.resolve(value);
    else if (name === '--tasks') options.tasks = value.split(',').filter(Boolean);
    else if (name === '--repeat') options.repeat = integer(value, 1, 10, name);
    else if (name === '--max-turns') options.maxTurns = integer(value, 1, 100, name);
    else if (name === '--token-budget') options.tokenBudget = integer(value, 1, 200000, name);
    else if (name === '--timeout-ms') options.timeoutMs = integer(value, 1000, 600000, name);
    else if (name === '--max-steps') options.maxSteps = integer(value, 1, 100, name);
    else throw new Error(`unknown option ${name}`);
  }
  if (!['validate', 'model'].includes(options.mode) || !['reference', 'noop'].includes(options.control)) throw new Error('mode must be validate|model; control must be reference|noop');
  if (options.mode === 'model' && options.control !== 'reference') throw new Error('noop is only a validation control');
  if (!options.help && !options.list && options.mode === 'model' && (!options.model || !options.agent)) throw new Error('model mode requires explicit --agent and --model');
  if (options.tasks && (!options.tasks.length || new Set(options.tasks).size !== options.tasks.length || options.tasks.some(id => !TASKS.some(task => task.id === id)))) throw new Error('unknown, empty or duplicate task selection');
  return options;
}

export function authRedactor(environment, token) {
  const base = makeRedactor(environment, [token]);
  return value => base(value).replace(/\bmock_(?:session|access|pat)_[a-f0-9]{48}\b/g, '[REDACTED_MOCK_CREDENTIAL]');
}

// Model output and website JSON can put credentials in object keys too. The
// shared legacy sanitizer intentionally remains untouched by this new suite.
export function safeAuthValue(value, redact) {
  if (Array.isArray(value)) return value.slice(0, 1000).map(item => safeAuthValue(item, redact));
  if (value && typeof value === 'object') {
    const result = Object.create(null);
    for (const [key, item] of Object.entries(value)) {
      if (/^(?:authorization|api_?key|lease_token|worker_token|data_url|dataurl|headers)$/i.test(key)) continue;
      if (key === 'screenshot' && item && typeof item === 'object') {
        const { data, ...metadata } = item;
        result[key] = safeAuthValue(metadata, redact);
      } else result[redact(key).slice(0, 2000)] = safeAuthValue(item, redact);
    }
    return result;
  }
  return typeof value === 'string' ? redact(value).slice(0, 64000) : value;
}

// Retained as a refusal boundary for callers that imported the old runner.
// Parsing, redaction, tasks and reports above remain available for offline use.
export async function runSuite() {
  throw retiredAuthProfile('auth');
}
