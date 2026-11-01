export const EVIDENCE_CONTRACT = 'native-dom-evidence-calculate-v1';

export function quantities(reply) {
  const evidence = reply?.evidence;
  if (evidence?.schema !== 'dom-evidence-v1' || !['observed', 'partial'].includes(evidence.status) || !Array.isArray(evidence.frames)) throw new Error('interface_pending: native DOM Evidence frames missing');
  return evidence.frames.flatMap(frame => (frame.quantities ?? []).map(quantity => ({ ...quantity, provenance: frame.provenance, frame_truncated: frame.truncated || frame.quantities_truncated })));
}
export function calculation(reply) {
  const evidence = reply?.evidence;
  if (!evidence || !['calculated', 'unsupported', 'error'].includes(evidence.status) || !evidence.operation && evidence.status !== 'error') throw new Error('interface_pending: native calculate Evidence missing');
  return evidence;
}
export function actionsFrom(events = []) {
  const actions = []; let pending;
  for (const event of events) {
    if (event.type === 'browser_start') { if (pending) return []; pending = event.args; }
    if (event.type !== 'browser_end' || !pending) continue;
    const args = pending; pending = null; const result = event.result ?? {};
    if (args.action === 'batch') for (const [i, step] of (result.steps ?? []).entries()) {
      const child = args.actions?.[i]; if (child?.action === step.action) actions.push({ ...child, ok: step.ok === true, result: i === result.steps.length - 1 ? { ...step, ...result, steps: undefined } : step });
    }
    else actions.push({ ...args, ok: event.ok === true, result });
  }
  return actions;
}

/** This is a tool protocol diagnostic, never a natural-answer or model business score. */
export function assessProtocol(actions, control, readEvidence) {
  const seen = new Map(), calculations = [], failures = [];
  for (const action of actions) {
    if (action.ok && action.result?.evidence?.schema === 'dom-evidence-v1') for (const q of quantities(action.result)) seen.set(q.quantity_ref, q);
    if (action.action !== 'calculate') continue;
    const c = calculation(action.result); calculations.push(c);
    if (c.status === 'error') { failures.push('calculation_error'); continue; }
    if (!Array.isArray(c.sources) || c.sources.some(s => !seen.has(s.quantity_ref) || s.raw !== seen.get(s.quantity_ref).raw || JSON.stringify(s.provenance) !== JSON.stringify(seen.get(s.quantity_ref).provenance))) failures.push('calculation_sources_not_observed');
    if (c.scope !== 'as_observed' || c.current_state_verified !== false || c.verifies_entire_answer !== false) failures.push('calculation_scope_missing');
  }
  if (!readEvidence.verified) failures.push('required_browser_reads_missing');
  for (const operation of control.operations) {
    const matching = calculations.filter(c => c.status === 'calculated' && c.operation === operation && c.result?.unit === control.unit);
    if (!matching.length) { failures.push(`required_${operation}_missing`); continue; }
    const supported = matching.some(c => {
      const raws = c.sources.map(s => s.raw);
      if (operation === 'sort') return JSON.stringify([...raws].sort()) === JSON.stringify([...control.raws].sort()) && new Set(c.sources.map(s => s.provenance?.observation_id)).size === 1 && c.result.ascending_groups?.[0]?.value === control.minimum && c.result.ascending_groups[0].quantity_refs.length === control.tie_count;
      if (raws.length !== 2 || !raws.every(r => control.raws.includes(r))) return false;
      const reversed = raws[0] !== control.raws[0];
      if (operation === 'difference') return c.result.order === 'first_minus_second' && c.result.value === `${reversed ? '-' : ''}${control.absolute_difference}`;
      return c.result.relation === (reversed ? 'less' : 'greater');
    });
    if (!supported) failures.push(`required_${operation}_evidence_mismatch`);
  }
  return { interface_verified: failures.length === 0, failures: [...new Set(failures)], calculations,
    observed_quantity_count: seen.size, browser_reads: readEvidence, answer_review: 'pending_review', verifies_entire_answer: false };
}

export async function reference(task, control, execute) {
  const observed = [];
  if (task.id === 'paged-tied-minimum') { observed.push(await execute({ action: 'snapshot' })); observed.push(await execute({ action: 'click', target: '#continue' })); }
  else for (const page of control.pages) observed.push(await execute({ action: page === '/' ? 'snapshot' : 'navigate', ...(page === '/' ? {} : { url: new URL(page.slice(1), task.startURL).href }) }));
  const qs = task.id === 'paged-tied-minimum' ? quantities(observed.at(-1)) : observed.flatMap(quantities), picked = [];
  for (const raw of control.raws) { const q = qs.find(q => q.supported && q.raw === raw && !picked.includes(q.quantity_ref)); if (!q) throw new Error(`observed quantity not available: ${raw}`); picked.push(q.quantity_ref); }
  for (const operation of control.operations) await execute({ action: 'calculate', operation, quantity_refs: picked });
  if (control.incompatible_raw) { const year = qs.find(q => q.raw === control.incompatible_raw); if (!year) throw new Error('year source missing'); const c = calculation(await execute({ action: 'calculate', operation: 'compare', quantity_refs: [year.quantity_ref, picked[0]] })); if (c.status !== 'unsupported' || c.reason !== 'incompatible_units') throw new Error('calendar conversion must remain unsupported'); }
  return `${control.answer}\n来源：${task.startURL}${task.id === 'cross-detail-mass' ? '\n' + control.pages.map(p => new URL(p.slice(1), task.startURL).href).join('\n') : ''}`;
}
