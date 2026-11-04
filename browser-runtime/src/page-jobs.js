// A collection is one queued browser transaction with a small internal work
// pool. It never selects a global active tab or replays a navigation/input.
export const MAX_PAGE_JOBS = 8;
export const MAX_PAGE_CONCURRENCY = 3;

const tabID = /^t[1-9]\d{0,9}$/;
const contextID = /^ctx_[a-f0-9]{24}$/;
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const integer = (value, minimum, maximum = Number.MAX_SAFE_INTEGER) => Number.isSafeInteger(value) && value >= minimum && value <= maximum;
const elapsed = started => Math.round(Math.max(0, performance.now() - started) * 100) / 100;
const failure = (message, code, status = 409) => Object.assign(new Error(message), { status, code });
const messageOf = error => String(error?.message ?? error ?? 'Browser page operation failed').slice(0, 2000);

function absoluteURL(value) {
  if (typeof value !== 'string' || !value.trim() || value.length > 4096 || /[\u0000-\u001f\u007f]/.test(value)) {
    throw failure('open_pages requires nonempty absolute HTTP(S) URLs without control characters', 'PAGE_JOBS_INVALID', 400);
  }
  let url;
  try { url = new URL(value.trim()); }
  catch { throw failure('open_pages requires valid absolute HTTP(S) URLs', 'PAGE_JOBS_INVALID', 400); }
  if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || url.hostname.includes(',') || url.username || url.password) {
    throw failure('open_pages URLs must use HTTP(S), a valid host, and no embedded credentials', 'PAGE_JOBS_INVALID', 400);
  }
  return url.href;
}

/** Validate the whole collection before creating a tab or reading any page.
 * Resource identity/ownership is checked separately for each page, so one
 * closed or human-owned tab does not erase other pages' legitimate results. */
export function validatePageJobsAction(action) {
  if (!record(action) || !['open_pages', 'observe_pages'].includes(action.action)) {
    throw failure('unsupported browser page collection', 'PAGE_JOBS_INVALID', 400);
  }
  const opening = action.action === 'open_pages';
  const allowed = new Set(['action', 'op_id', 'concurrency', 'seconds', opening ? 'urls' : 'pages', ...(!opening ? ['full'] : [])]);
  if (Object.keys(action).some(key => !allowed.has(key))) throw failure('unsupported browser page collection argument', 'PAGE_JOBS_INVALID', 400);
  if (action.concurrency !== undefined && !integer(action.concurrency, 1, MAX_PAGE_CONCURRENCY)) {
    throw failure(`page collection concurrency must be 1–${MAX_PAGE_CONCURRENCY}`, 'PAGE_JOBS_INVALID', 400);
  }
  if (action.seconds !== undefined && (typeof action.seconds !== 'number' || !Number.isFinite(action.seconds) || action.seconds < 0 || action.seconds > 60)) {
    throw failure('page collection seconds must be 0–60; 0 uses the configured default', 'PAGE_JOBS_INVALID', 400);
  }
  if (action.full !== undefined && typeof action.full !== 'boolean') throw failure('observe_pages full must be boolean', 'PAGE_JOBS_INVALID', 400);
  const entries = opening ? action.urls : action.pages;
  if (!Array.isArray(entries) || entries.length < 1 || entries.length > MAX_PAGE_JOBS) {
    throw failure(`page collection requires 1–${MAX_PAGE_JOBS} ${opening ? 'URLs' : 'page bindings'}`, 'PAGE_JOBS_INVALID', 400);
  }
  const seen = new Set();
  for (const entry of entries) {
    let key;
    if (opening) key = absoluteURL(entry);
    else {
      if (!record(entry) || Object.keys(entry).some(field => !['tab_id', 'context_ref', 'control_epoch'].includes(field)) ||
          !tabID.test(entry.tab_id ?? '') || !contextID.test(entry.context_ref ?? '') || !integer(entry.control_epoch, 0)) {
        throw failure('observe_pages requires tab_id/context_ref/control_epoch from each selected tabs row', 'PAGE_JOBS_INVALID', 400);
      }
      key = `${entry.context_ref}:${entry.tab_id}`;
    }
    if (seen.has(key)) throw failure('page collection entries must be unique', 'PAGE_JOBS_INVALID', 400);
    seen.add(key);
  }
}

function pageMetadata(session, meta, controlEpoch) {
  return { tab_id: meta.id, context_ref: session.contextRef, control_epoch: controlEpoch,
    generation: session.generation, document_version: meta.version, version: session.version(meta),
    url: meta.page.url(), title: meta.title ?? '' };
}

function pendingError(error) {
  return ['PAGE_JOB_DEADLINE', 'PAGE_JOB_CHANGED', 'PAGE_JOB_DIALOG', 'PAGE_JOB_DEBUGGER', 'BROWSER_OBSERVATION_CHANGED'].includes(error?.code) ||
    error?.name === 'TimeoutError' || /page observation (?:timed out|is still pending|was superseded)|page changed during|Execution context was destroyed|ERR_ABORTED|navigation was interrupted/i.test(messageOf(error));
}

export async function runPageJobs(session, args, epoch, { timing, mode = 'agent', parentObservation } = {}) {
  validatePageJobsAction(args);
  if (mode !== 'agent') throw failure('browser page collections require Agent control', 'PAGE_JOBS_INVALID', 400);
  const operation = session.currentOperation;
  if (!operation || operation.mode !== 'agent') throw failure('browser page collection requires its queued operation', 'PAGE_JOBS_UNBOUND');
  const opening = args.action === 'open_pages';
  const entries = opening ? args.urls : args.pages;
  const concurrency = Math.min(args.concurrency ?? MAX_PAGE_CONCURRENCY, entries.length);
  const budget = Math.min(args.seconds ? args.seconds * 1000 : 60_000, session.actionTimeout ?? 60_000, 60_000);
  const startedAt = performance.now(), deadline = Date.now() + budget;
  const rows = entries.map((entry, index) => ({ index, ...(opening ? { requested_url: entry.trim() } : {
    tab_id: entry.tab_id, context_ref: entry.context_ref, control_epoch: entry.control_epoch,
  }), ok: false, status: 'not_started' }));
  let next = 0;
  let stoppedBy;
  // Native page adoption has its own serial setup. Serialize creation here too
  // so each capacity check observes earlier new tabs; their navigations and
  // subsequent independent observations still overlap within the work pool.
  let creations = Promise.resolve();

  const checkGroup = (allowExpired = false) => {
    session.assertLive();
    if (epoch !== session.generation || session.phase !== 'ready' || session.currentOperation !== operation ||
        parentObservation && (!parentObservation.live || parentObservation.epoch !== epoch)) {
      throw failure('page collection was fenced by cancellation or browser replacement', 'PAGE_JOB_FENCED');
    }
    if (!allowExpired && Date.now() >= deadline) throw failure('page collection deadline reached; unfinished work was not retried', 'PAGE_JOB_DEADLINE');
  };
  const checkPage = (meta, controlEpoch, allowExpired = false) => {
    checkGroup(allowExpired);
    if (!meta || session.pages.get(meta.id) !== meta || meta.page.isClosed()) throw failure('selected browser page closed during this collection', 'BROWSER_TAB_CLOSED', 410);
    session.guard(epoch, mode, undefined, meta, controlEpoch);
    if (session.dialog?.page === meta.page) throw failure('this page is waiting for a human dialog decision', 'PAGE_JOB_DIALOG');
    if (session.devtools.isPaused(meta) || session.devtools.isBlocked(meta)) throw failure('this page is paused or awaiting a debug outcome', 'PAGE_JOB_DEBUGGER');
  };
  const registerPage = (meta, controlEpoch) => {
    checkPage(meta, controlEpoch);
    return session.beginPageJob(meta);
  };

  const createPage = () => {
    const creating = creations.then(async () => {
      checkGroup();
      if (session.maxPages > 0 && session.pages.size >= session.maxPages) throw failure('browser tab capacity reached', 'PAGE_JOB_CAPACITY', 429);
      // Do not race this factory against a timer and release its raw-action
      // barrier while an unknown creation may still finish. The host factory
      // and the enclosing operation enforce cancellation/transport deadlines.
      const page = await session.createPage();
      const meta = [...session.pages.values()].find(value => value.page === page);
      if (!meta) throw failure('new browser page could not be bound to this session', 'PAGE_JOB_UNBOUND');
      return meta;
    });
    creations = creating.catch(() => {});
    return creating;
  };

  async function runOne(index) {
    const row = rows[index], input = entries[index];
    const rowTiming = { total_ms: 0, queue_ms: elapsed(startedAt), navigation_ms: 0, snapshot_ms: 0 };
    let meta, controlEpoch, observation, finishPageJob;
    let navigationDispatched = false, started = false;
    try {
      checkGroup();
      started = true;
      if (opening) {
        meta = await createPage();
        controlEpoch = session.controlFor(meta).epoch;
        // Preserve the concrete newly-created tab even if its navigation is
        // subsequently refused, cancelled or interrupted. Never use active as
        // a fallback, and never close a tab a human may now have taken over.
        Object.assign(row, pageMetadata(session, meta, controlEpoch));
        finishPageJob = registerPage(meta, controlEpoch);
        await meta.devtoolsReady;
        checkPage(meta, controlEpoch);
        const remaining = Math.max(1, deadline - Date.now());
        const navStarted = performance.now();
        try {
          navigationDispatched = true;
          await meta.page.goto(absoluteURL(input), { waitUntil: 'domcontentloaded', timeout: remaining });
        } finally {
          rowTiming.navigation_ms = elapsed(navStarted);
          if (timing) timing.navigation_ms += rowTiming.navigation_ms;
        }
        checkPage(meta, controlEpoch);
      } else {
        meta = session.selectedPage({ tab_id: input.tab_id, context_ref: input.context_ref });
        controlEpoch = input.control_epoch;
        finishPageJob = registerPage(meta, controlEpoch);
      }
      const queuedAt = performance.now();
      const captured = await session.enqueuePageRead(meta, async () => {
        rowTiming.queue_ms += elapsed(queuedAt);
        checkPage(meta, controlEpoch);
        const version = meta.version, identity = session.frameIdentity(meta);
        const readDeadline = Math.min(deadline, Date.now() + Math.min(session.actionTimeout ?? 4000, 4000));
        observation = session.observation(epoch, readDeadline, { meta, mode, controlEpoch });
        if (parentObservation) observation.parent = parentObservation;
        observation.targetMeta = meta;
        observation.timing = timing;
        const readStarted = performance.now();
        let value;
        try { value = await session.snapshot({ full: args.full === true }, observation); }
        finally {
          rowTiming.snapshot_ms = elapsed(readStarted);
          observation.live = false;
        }
        checkPage(meta, controlEpoch);
        if (meta.version !== version || session.frameIdentity(meta) !== identity) {
          throw failure('page document changed during its collection read; take a fresh bound observation', 'PAGE_JOB_CHANGED');
        }
        return { value, metadata: pageMetadata(session, meta, controlEpoch) };
      });
      const value = captured.value;
      Object.assign(row, captured.metadata);
      if (!value?.ok || value.observation_pending) {
        row.status = value?.observation_pending ? 'pending' : 'failed';
        row.observation_pending = value?.observation_pending === true;
        row.error = typeof value?.error === 'string' ? value.error.slice(0, 2000) : 'The page could not be observed coherently; no input was repeated';
      } else {
        row.ok = true; row.status = 'observed'; row.observed_at = new Date().toISOString();
        if (typeof value.text === 'string') row.text = value.text;
        if (value.evidence !== undefined) row.evidence = value.evidence;
        if (typeof value.url === 'string') row.url = value.url;
        if (typeof value.title === 'string') row.title = value.title;
      }
    } catch (error) {
      row.ok = false; row.status = !started ? 'not_started' : pendingError(error) ? 'pending' : 'failed';
      if (row.status === 'pending') row.observation_pending = true;
      row.error = messageOf(error);
      if (opening && meta && !navigationDispatched) row.error += ' The new tab was retained; its requested URL was not navigated. Inspect this tab rather than reopening the whole collection.';
      // A failed ownership/document check cannot publish DOM returned under a
      // different authority, even if the renderer completed a late read.
      delete row.text; delete row.evidence; delete row.observed_at;
      delete row.version; delete row.document_version; delete row.url; delete row.title;
    } finally {
      if (observation) observation.live = false;
      finishPageJob?.();
      rowTiming.total_ms = elapsed(startedAt);
      row.timing = rowTiming;
    }
  }

  async function worker() {
    for (;;) {
      try { checkGroup(); }
      catch (error) { stoppedBy ??= error; return; }
      if (next >= rows.length) return;
      const index = next++;
      await runOne(index);
    }
  }
  await Promise.all(Array.from({ length: concurrency }, worker));
  for (const row of rows) if (row.status === 'not_started') {
    row.error = stoppedBy ? `Not started: ${messageOf(stoppedBy)}`
      : Date.now() >= deadline ? 'Not started: the collection deadline was reached.'
      : 'Not started: the browser operation was cancelled or its authority changed.';
  }
  const coverage = { requested: rows.length, observed: 0, failed: 0, pending: 0, skipped: 0 };
  for (const row of rows) {
    if (row.status === 'observed') coverage.observed++;
    else if (row.status === 'failed') coverage.failed++;
    else if (row.status === 'pending') coverage.pending++;
    else coverage.skipped++;
  }
  const data = { kind: 'browser-pages-v1', operation: args.action, pages: rows, coverage, concurrency };
  if (opening && !session.dialog) {
    for (const row of rows) {
      if (!row.ok) continue;
      const meta = session.pages.get(row.tab_id);
      try {
        // This is metadata for already-observed pixels, not another page
        // read. A different page using up the collection budget must not hide
        // a retained successful page; cancellation/ownership still fence it.
        checkPage(meta, row.control_epoch, true);
        if (meta.version !== row.document_version || session.version(meta) !== row.version) continue;
        data.focus = { tab_id: row.tab_id, context_ref: row.context_ref, version: row.version,
          generation: row.generation, control_epoch: row.control_epoch };
        break;
      } catch { /* An already-observed page can change before focus selection. */ }
    }
  }
  // Success means the collection produced at least one observation, never
  // that it covered all requested pages. The complete coverage and per-page
  // states always remain available, including failed and pending bindings.
  const ok = coverage.observed > 0;
  return { ok, ...(ok ? {} : { error: `Page collection incomplete: ${coverage.observed}/${coverage.requested} observed; ${coverage.failed} failed, ${coverage.pending} pending, ${coverage.skipped} not started. Keep successful pages and inspect unfinished bindings; do not replay the whole collection.` }), data };
}
