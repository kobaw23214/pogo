// Read-only facts from a root-target Overlay selection. The caller binds send
// to that target and rejects untransformed child-target coordinates. These are
// DOM geometry and accessibility labels, not a DOM text dump or a claim that
// every label is visibly rendered. No page code, form value or HTML is read.
const flights = new WeakMap();
const quotas = new WeakMap();
const failure = (message, status = 409) => Object.assign(new Error(message), { status });
const tidy = (value, limit) => typeof value === 'string' ? Array.from(value.replace(/\s+/g, ' ').trim()).slice(0, limit).join('') : '';

function boxRect(model, viewport) {
  const quad = model?.border;
  if (!Array.isArray(quad) || quad.length !== 8 || !quad.every(Number.isFinite)) throw failure('Selected node geometry is unavailable; select it again');
  const xs = [quad[0], quad[2], quad[4], quad[6]], ys = [quad[1], quad[3], quad[5], quad[7]];
  const x = Math.max(0, Math.min(...xs)), y = Math.max(0, Math.min(...ys));
  const right = Math.min(viewport.width, Math.max(...xs)), bottom = Math.min(viewport.height, Math.max(...ys));
  if (right <= x || bottom <= y) throw failure('Selected node is outside the current viewport');
  return { quad, rect: { x, y, width: right - x, height: bottom - y } };
}

function facts(node, tree, backendNodeId) {
  if (node?.backendNodeId !== backendNodeId || node.nodeType !== 1 || typeof node.localName !== 'string' || !node.localName) {
    throw failure('Selected node is no longer an available element');
  }
  const ax = tree?.nodes?.find(value => value.backendDOMNodeId === backendNodeId);
  if (!ax || ax.ignored) throw failure('Selected node has no available accessibility facts');
  const tag = tidy(node.localName.toLowerCase(), 32), role = tidy(ax.role?.value, 80) || tag;
  const editable = ['input', 'textarea', 'select', 'option'].includes(tag) ||
    ['textbox', 'combobox', 'searchbox'].includes(role) ||
    ax.properties?.some(property => property.name === 'editable' && ['plaintext', 'richtext', true].includes(property.value?.value));
  const name = tidy(ax.name?.value, 300);
  // In particular, never inspect node.attributes/nodeValue or ax.value. An AX
  // response can contain those fields even when they were not requested.
  const text = editable ? '' : tidy([name, tidy(ax.description?.value, 800)].filter(Boolean).join(' '), 800);
  return { tag, role, name, text };
}

export async function inspectNativeNode({ send, backendNodeId, runtime, meta, snapshotStamp, checkCurrent }) {
  if (typeof send !== 'function' || typeof checkCurrent !== 'function' || !Number.isSafeInteger(backendNodeId) || backendNodeId < 1 || !runtime?.picker || !meta?.page) {
    throw failure('Native inspection requires a current owned node selection');
  }
  if (flights.has(meta)) throw failure('A native node observation is still pending; try again after it completes');
  const generation = runtime.generation, documentVersion = meta.version, contextRef = runtime.contextRef;
  const control = runtime.control, controlEpoch = runtime.controlEpoch, lease = runtime.lease;
  const viewport = runtime.viewport(meta), version = runtime.version(meta);
  const deadline = Date.now() + 1500;
  let live = true;
  const check = () => {
    if (!live || Date.now() >= deadline) throw failure('Native node observation timed out; select the element again', 504);
    runtime.assertLive();
    if (runtime.contextRef !== contextRef || runtime.generation !== generation || runtime.control !== control || runtime.controlEpoch !== controlEpoch || runtime.lease !== lease ||
      runtime.phase !== 'ready' || runtime.inOperation || runtime.rawAction || runtime.pages.get(meta.id) !== meta || meta.page.isClosed() || meta.version !== documentVersion || runtime.version(meta) !== version ||
      runtime.dialog?.page === meta.page || runtime.devtools?.isPaused(meta) || runtime.devtools?.isBlocked(meta)) throw failure('Page or browser control changed during native inspection');
    if (checkCurrent(snapshotStamp) === false) throw failure('Native node selection is no longer current');
  };
  const command = async (method, params) => {
    check();
    let result;
    try { result = await send(method, params); }
    catch { throw failure('Selected node is no longer available; select it again'); }
    check();
    return result;
  };
  const sample = async () => {
    const { node } = await command('DOM.describeNode', { backendNodeId, depth: 0, pierce: false });
    const { model } = await command('DOM.getBoxModel', { backendNodeId });
    const tree = await command('Accessibility.getPartialAXTree', { backendNodeId, fetchRelatives: false });
    return { ...facts(node, tree, backendNodeId), ...boxRect(model, viewport) };
  };
  const raw = (async () => {
    // Re-read the same native node. A detached node, changed accessible label
    // or moving geometry must not be published as a stable selection.
    const before = await sample(), after = await sample();
    check();
    if (JSON.stringify(before) !== JSON.stringify(after)) throw failure('Selected node changed during observation; select it again');
    let quota = quotas.get(meta);
    if (!quota || quota.generation !== generation || quota.documentVersion !== documentVersion) quota = { generation, documentVersion, count: 0 };
    if (quota.count >= 2048 || !Number.isSafeInteger(runtime.picker.nextRef) || runtime.picker.nextRef >= Number.MAX_SAFE_INTEGER) throw failure('Native observation limit reached for this document');
    quota.count++; quotas.set(meta, quota);
    const { quad, rect, ...observed } = after;
    return { context_ref: contextRef, version, tab_id: meta.id, url: tidy(meta.page.url(), 4096), title: tidy(meta.title, 300), viewport,
      observation_scope: 'native-dom-accessibility', element: { ref: `p${++runtime.picker.nextRef}`, ...observed, rect } };
  })();
  flights.set(meta, raw);
  raw.finally(() => { if (flights.get(meta) === raw) flights.delete(meta); }).catch(() => {});
  let timer;
  try { return await Promise.race([raw, new Promise((_, reject) => { timer = setTimeout(() => reject(failure('Native node observation timed out; select the element again', 504)), 1500); })]); }
  finally { live = false; clearTimeout(timer); }
}
