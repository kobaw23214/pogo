// Failure-only test evidence. Never serialize a Page, URL, RPC args, auth token
// or human lease. Route labels are limited to the caller's owned fixture.
const integer = value => Number.isSafeInteger(value) ? value : null;
const dimension = value => Number.isFinite(value) && value >= 0 && value <= 8192 ? value : null;
const enumValue = value => typeof value === 'string' && /^[a-zA-Z][a-zA-Z0-9_]{0,79}$/.test(value) ? value : null;
const nativeError = error => ({ name: enumValue(error?.name), operation: enumValue(error?.localOperation), code: enumValue(error?.localCode), reason: enumValue(error?.localReason) });

export function createNativeDiagnostics(host, { origin, paths = [] } = {}) {
  const started = Date.now(), events = [], pages = new Map(), allowed = new Set(paths);
  const route = value => {
    if (value === 'about:blank') return 'about:blank';
    if (value.startsWith('data:text/html')) return 'private-bootstrap';
    try { const url = new URL(value); return url.origin === origin && allowed.has(url.pathname) ? url.pathname : 'outside-owned-fixture'; }
    catch { return 'unavailable'; }
  };
  const event = (type, fields = {}) => { events.push({ at_ms: Date.now() - started, type, ...fields }); if (events.length > 100) events.shift(); };
  const watch = (page, source) => {
    if (pages.has(page)) return pages.get(page);
    const row = { label: 'page-' + (pages.size + 1), source, binding: 'not-observed', lastRecord: null }; pages.set(page, row);
    event('page', { label: row.label, source, route: route(page.url()) });
    page.on('popup', child => { const childRow = watch(child, 'opener-popup'); event('popup', { opener: row.label, child: childRow.label, route: route(child.url()) }); });
    page.on('framenavigated', frame => { if (frame === page.mainFrame()) event('document', { label: row.label, route: route(frame.url()) }); });
    page.on('close', () => event('closed', { label: row.label }));
    page.on('crash', () => event('crashed', { label: row.label }));
    observeBinding(page, row); return row;
  };
  const observeBinding = (page, row) => {
    const pending = host.pending?.get(page);
    if (!pending || row.promise === pending) return;
    row.promise = pending; row.binding = 'pending';
    pending.then(() => { row.binding = 'fulfilled'; event('binding_fulfilled', { label: row.label }); }, error => {
      row.binding = 'rejected'; row.error = nativeError(error); event('binding_rejected', { label: row.label, error: row.error });
    });
  };
  for (const page of host.context.pages()) watch(page, 'existing');
  host.context.on('page', page => watch(page, 'context-page'));
  host.context.on('close', () => event('context_closed'));
  host.browser?.on('disconnected', () => event('browser_disconnected'));
  for (const [scope, rpc] of [['host', host.rpc], ['stream', host.runtime?.rpc]]) {
    rpc?.on('rejected', value => event('rpc_rejected', { scope, operation: enumValue(value.operation), code: enumValue(value.code), reason: enumValue(value.reason) }));
    rpc?.on('failed', error => event('rpc_failed', { scope, error: nativeError(error) }));
    rpc?.on('uncertain', error => event('rpc_uncertain', { scope, error: nativeError(error) }));
  }
  return { event, snapshot() {
    for (const page of host.context.pages()) watch(page, 'snapshot');
    return { host: { failed: !!host.failed, closed: !!host.closed, rpc_closed: !!host.rpc?.closed, rpc_pending: host.rpc?.pending?.size ?? null },
      pages: [...pages].map(([page, row]) => {
        observeBinding(page, row);
        const record = host.records?.get(page);
        if (record) row.lastRecord = { tab_id: /^t[1-9]\d*$/.test(record.pageId ?? '') ? record.pageId : null,
          chrome_window: integer(record.chromeWindow), native_window: integer(record.nativeWindow), display_id: integer(record.displayId),
          removing: !!record.removing, creating: integer(record.creating), peers: record.peers?.size ?? null,
          ...(record.pairing ? { pairing: {
            requested: { width: dimension(record.pairing.requested?.width), height: dimension(record.pairing.requested?.height) },
            chrome: { width: dimension(record.pairing.chrome?.width), height: dimension(record.pairing.chrome?.height),
              window_state: ['normal', 'minimized', 'maximized', 'fullscreen'].includes(record.pairing.chrome?.windowState) ? record.pairing.chrome.windowState : null },
            native: Array.isArray(record.pairing.native) ? record.pairing.native.slice(0, 16).map(value => ({ window_id: integer(value.window_id), width: dimension(value.width), height: dimension(value.height) })) : [],
          } } : {}) };
        return { label: row.label, source: row.source, route: route(page.url()), closed: page.isClosed(), binding: row.binding, error: row.error ?? null, record: row.lastRecord };
      }), events: [...events] };
  } };
}
