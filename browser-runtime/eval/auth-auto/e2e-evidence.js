import { matchesLoginScenario } from './tasks.js';

// The mock UI expires credentials, then confirms expiry with its own GET.
// Both operations must finish before the model phase's baseline is captured.
export function expiryFixtureComplete(before, after) {
  const fields = ['labExpires', 'unauthorizedRequests'];
  if (fields.some(key => !Number.isInteger(before?.counters?.[key]) || !Number.isInteger(after?.counters?.[key]))) return false;
  if (before.counters.labExpires !== 0) throw new Error('Expiry fixture must start from an unexpired test run');
  const expectedUnauthorized = before.counters.unauthorizedRequests + 1;
  if (after.counters.labExpires > 1 || after.counters.unauthorizedRequests > expectedUnauthorized) throw new Error('Expiry fixture produced unexpected extra expiry or protected requests');
  return after.counters.labExpires === 1 && after.counters.unauthorizedRequests === expectedUnauthorized && after.activeSessions === 0 && after.activeTokens === 0;
}

// Full-Pogo SSE tools use call_id rather than the recording proxy's pairs.
export function browserCalls(events) {
  const pending = new Map(), calls = [];
  for (const event of events) {
    if (event.name !== 'browser' || !event.call_id) continue;
    if (event.type === 'tool_start') {
      try { pending.set(event.call_id, typeof event.args === 'string' ? JSON.parse(event.args) : event.args); } catch { /* malformed request is not evidence */ }
    } else if (event.type === 'tool_end' && pending.has(event.call_id)) {
      const args = pending.get(event.call_id); pending.delete(event.call_id);
      calls.push({ args, ok: !event.is_error && event.details?.ok === true, text: event.text ?? '', details: event.details });
    }
  }
  return calls;
}
export function jsonPayload(text, field) {
  for (const line of String(text).split('\n')) {
    try { const value = JSON.parse(line); if (value && typeof value === 'object' && Object.hasOwn(value, field)) return value; } catch { /* runtime/page headers are not JSON */ }
  }
  return null;
}
export function assertLoginReadEvidence({ calls, before, after, path, orderID, amount, scenario }) {
  const delta = key => (after?.counters?.[key] ?? 0) - (before?.counters?.[key] ?? 0);
  const reads = calls.filter(call => call.args?.action === 'api_read');
  const payloads = reads.filter(call => call.ok && call.args.path === path).map(call => jsonPayload(call.text, 'status'));
  const failures = [];
  if (!calls.some(call => call.ok && call.args?.action === 'auth_status' && jsonPayload(call.text, 'api_access')?.api_access.mode === 'automatic')) failures.push('automatic_status_not_observed');
  if (!reads.length || reads.some(call => !call.ok || call.args.path !== path) || !payloads.some(payload => payload?.status === 200 && payload.data?.total === 1 && Array.isArray(payload.data.orders) && payload.data.orders.some(row => row.id === orderID && row.amount === amount))) failures.push('successful_api_payload_missing');
  if (delta('loginSubmissions') !== 1 || delta('successfulLogins') !== 1 || delta('verificationSubmissions') !== 0 || delta('orderReads') < payloads.length) failures.push('model_login_or_read_not_verified');
  if (!matchesLoginScenario(after, scenario)) failures.push('login_scenario_or_credential_type_mismatch');
  if (['tokenCreates', 'tokenRevokes', 'logoutSubmissions', 'labExpires', 'labRevokes'].some(key => delta(key) !== 0)) failures.push('unexpected_account_mutation');
  if (calls.some(call => call.args?.credential_ref !== undefined)) failures.push('legacy_credential_ref_used');
  if (calls.some(call => {
    if (call.args?.action !== 'navigate') return false;
    try { return new URL(call.args.url).pathname.startsWith('/api/lab/'); } catch { return false; }
  })) failures.push('evaluation_internals_accessed');
  return { passed: failures.length === 0, failures, login_submissions: delta('loginSubmissions'), successful_logins: delta('successfulLogins'), successful_api_reads: payloads.filter(payload => payload?.status === 200).length, server_order_reads: delta('orderReads') };
}
