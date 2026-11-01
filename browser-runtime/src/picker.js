import { randomBytes } from 'node:crypto';

// Installed before site scripts. Internal node identities live in a WeakMap;
// picking never adds attributes/styles or dispatches page input events.
function installProbe({ key }) {
  const invoke = Function.prototype.call.bind(Function.prototype.call);
  const method = (proto, name) => { const fn = proto[name]; return (receiver, ...args) => invoke(fn, receiver, ...args); };
  const getter = (proto, name) => { const fn = Object.getOwnPropertyDescriptor(proto, name).get; return receiver => invoke(fn, receiver); };
  const getAttribute = method(Element.prototype, 'getAttribute');
  const getBox = method(Element.prototype, 'getBoundingClientRect');
  const clientLeft = getter(Element.prototype, 'clientLeft'), clientTop = getter(Element.prototype, 'clientTop');
  const clientWidth = getter(Element.prototype, 'clientWidth'), clientHeight = getter(Element.prototype, 'clientHeight');
  const getRoot = method(Node.prototype, 'getRootNode');
  const getAnimations = method(Element.prototype, 'getAnimations');
  const parseCSSNumber = Number.parseFloat;
  const pageAnimations = Document.prototype.getAnimations.bind(document);
  const keyframes = method(KeyframeEffect.prototype, 'getKeyframes');
  const animationState = getter(Animation.prototype, 'playState');
  const elementFromPoint = method(Document.prototype, 'elementFromPoint');
  const shadowElementFromPoint = method(ShadowRoot.prototype, 'elementFromPoint');
  const byId = Document.prototype.getElementById.bind(document);
  const createRange = Document.prototype.createRange.bind(document);
  const rangeSelect = method(Range.prototype, 'selectNodeContents');
  const rangeBoxes = method(Range.prototype, 'getClientRects');
  const computedStyle = getComputedStyle.bind(globalThis);
  const styleProperty = method(CSSStyleDeclaration.prototype, 'getPropertyValue');
  const parentElement = getter(Node.prototype, 'parentElement');
  const childNodes = getter(Node.prototype, 'childNodes');
  const textContent = getter(Node.prototype, 'textContent');
  const children = getter(Element.prototype, 'children');
  const shadowRoot = getter(Element.prototype, 'shadowRoot');
  const rootHost = getter(ShadowRoot.prototype, 'host');
  const tagOf = getter(Element.prototype, 'tagName');
  const editable = getter(HTMLElement.prototype, 'isContentEditable');
  const detailsOpen = getter(HTMLDetailsElement.prototype, 'open');
  const labelsGetters = new Map([
    ['INPUT', getter(HTMLInputElement.prototype, 'labels')], ['TEXTAREA', getter(HTMLTextAreaElement.prototype, 'labels')],
    ['SELECT', getter(HTMLSelectElement.prototype, 'labels')], ['BUTTON', getter(HTMLButtonElement.prototype, 'labels')],
  ]);
  const styleOf = el => {
    const css = computedStyle(el);
    const out = {};
    for (const name of ['display', 'visibility', 'opacity', 'content-visibility', 'clip-path', 'clip', 'overflow-x', 'overflow-y', 'transform', 'perspective', 'translate', 'rotate', 'scale', 'zoom', 'padding-left', 'padding-top', 'padding-right', 'padding-bottom']) out[name] = styleProperty(css, name);
    return out;
  };
  const identities = new WeakMap();
  // Every document (including same-origin frames and later navigations) gets
  // its own namespace. A local node sequence alone can alias another frame's
  // first node when reconciling an Overlay selection with a coordinate hit.
  const documentPrefix = Array.from(crypto.getRandomValues(new Uint8Array(12)), byte => byte.toString(16).padStart(2, '0')).join('');
  let next = 0;
  const identityOf = el => {
    if (!identities.has(el)) identities.set(el, `${documentPrefix}:${++next}`);
    return identities.get(el);
  };
  const attr = getAttribute;
  const parent = el => { const direct = parentElement(el); if (direct) return direct; const root = getRoot(el); return root instanceof ShadowRoot ? rootHost(root) : null; };
  const boxOf = getBox;
  const tidy = (value, limit) => String(value ?? '').replace(/\s+/g, ' ').trim().slice(0, limit);
  const closedDetails = node => {
    for (let current = node; current; current = parent(current)) {
      const container = parent(current);
      if (container && tagOf(container) === 'DETAILS' && !detailsOpen(container)) {
        const summary = Array.from(children(container)).find(child => tagOf(child) === 'SUMMARY');
        if (current !== summary) return true;
      }
    }
    return false;
  };
  const hidden = el => {
    if (closedDetails(el)) return true;
    for (let node = el; node; node = parent(node)) {
      const style = styleOf(node);
      if (attr(node, 'hidden') !== null || attr(node, 'aria-hidden') === 'true' || style.display === 'none' || style.visibility !== 'visible' || style['content-visibility'] === 'hidden' || Number(style.opacity) === 0) return true;
    }
    return false;
  };
  const field = el => ['INPUT', 'TEXTAREA', 'SELECT', 'OPTION'].includes(tagOf(el)) || (el instanceof HTMLElement && editable(el));
  const visibleText = (el, limit) => {
    if (field(el) || hidden(el)) return '';
    let text = '', count = 0;
    const visit = node => {
      if (++count > 1200 || text.length >= limit) return;
      if (node.nodeType === Node.TEXT_NODE) {
        // Text in closed details is present in the DOM but not rendered. Range
        // rects also preserve visible descendants of display:contents parents.
        if (closedDetails(node)) return;
        const range = createRange();
        rangeSelect(range, node);
        let left = 0, top = 0, right = innerWidth, bottom = innerHeight;
        for (let ancestor = parentElement(node); ancestor; ancestor = parent(ancestor)) {
          const style = styleOf(ancestor), box = boxOf(ancestor);
          if (style['clip-path'] !== 'none' || style.clip !== 'auto') return;
          if (['hidden', 'clip', 'scroll', 'auto'].includes(style['overflow-x'])) { left = Math.max(left, box.left); right = Math.min(right, box.right); }
          if (['hidden', 'clip', 'scroll', 'auto'].includes(style['overflow-y'])) { top = Math.max(top, box.top); bottom = Math.min(bottom, box.bottom); }
        }
        const boxes = Array.from(rangeBoxes(range)).filter(r => r.width > 0 && r.height > 0);
        if (boxes.length && boxes.every(r => r.left >= left - .5 && r.right <= right + .5 && r.top >= top - .5 && r.bottom <= bottom + .5)) text += ` ${textContent(node) ?? ''}`;
        return;
      }
      if (node.nodeType !== Node.ELEMENT_NODE || ['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'IFRAME', 'FRAME'].includes(tagOf(node)) || field(node) || hidden(node)) return;
      for (const child of childNodes(node)) {
        if (count >= 1200 || text.length >= limit) break;
        visit(child);
      }
    };
    visit(el);
    return tidy(text, limit);
  };
  const inspect = el => {
    const box = boxOf(el);
    if (hidden(el) || box.width <= 0 || box.height <= 0) return null;
    // A moving sibling can uncover a stationary target without a DOM write.
    // Reject running layout/transform animations for picking, not for frames.
    let transformed = false, animated = pageAnimations().some(animation => animationState(animation) === 'running' && animation.effect instanceof KeyframeEffect &&
      keyframes(animation.effect).some(frame => Object.keys(frame).some(key => /^(transform|translate|rotate|scale|offsetDistance|left|right|top|bottom|width|height|margin.*|padding.*|display|visibility|pointerEvents)$/.test(key))));
    let padding;
    for (let node = el; node; node = parent(node)) {
      const style = styleOf(node);
      if (node === el) padding = ['left', 'top', 'right', 'bottom'].map(side => parseCSSNumber(style[`padding-${side}`]));
      transformed ||= style.transform !== 'none' || style.perspective !== 'none' ||
        !['none', ''].includes(style.translate) || !['none', ''].includes(style.rotate) || !['none', ''].includes(style.scale) ||
        (style.zoom !== 'normal' && style.zoom !== '' && Number(style.zoom) !== 1);
      animated ||= getAnimations(node).some(animation => animationState(animation) === 'running');
    }
    const tag = tagOf(el).toLowerCase();
    const frame = ['iframe', 'frame'].includes(tag), validPadding = padding.every(value => Number.isFinite(value) && value >= 0);
    if (frame && !validPadding) transformed = true; // An uncertain content origin stays on the frame container.
    const [left, top, right, bottom] = frame && validPadding ? padding : [0, 0, 0, 0];
    return { tag, animated, transformed, left: box.left + clientLeft(el) + left, top: box.top + clientTop(el) + top,
      width: Math.max(0, clientWidth(el) - left - right), height: Math.max(0, clientHeight(el) - top - bottom) };
  };
  const roleOf = el => {
    const tag = tagOf(el).toLowerCase();
    const inputType = attr(el, 'type');
    const inputRole = ['checkbox', 'radio'].includes(inputType) ? inputType : ['button', 'submit', 'reset'].includes(inputType) ? 'button' : 'textbox';
    return tidy(attr(el, 'role') || ({ a: 'link', button: 'button', input: inputRole, textarea: 'textbox', select: 'combobox', img: 'img' }[tag] || tag), 80);
  };
  const geometry = el => { const box = boxOf(el); return { x: box.left, y: box.top, width: box.width, height: box.height }; };
  const summarize = el => {
    const tag = tagOf(el).toLowerCase(), role = roleOf(el);
    const labelled = (attr(el, 'aria-labelledby') || '').split(/\s+/).slice(0, 8).map(id => {
      const label = byId(id);
      return label ? visibleText(label, 300) : '';
    }).join(' ');
    const label = Array.from(labelsGetters.get(tagOf(el))?.(el) ?? []).slice(0, 8).map(label => visibleText(label, 300)).join(' ');
    const text = visibleText(el, 800);
    const name = tidy(attr(el, 'aria-label') || labelled || label || attr(el, 'alt') || attr(el, 'title') || attr(el, 'placeholder') || text, 300);
    return { identity: identityOf(el), tag: tag.slice(0, 32), role, name, text };
  };
  const hit = (x, y) => {
    let el = elementFromPoint(document, x, y);
    for (let depth = 0; el && shadowRoot(el) && depth < 12; depth++) {
      const deeper = shadowElementFromPoint(shadowRoot(el), x, y);
      if (!deeper || deeper === el) break;
      el = deeper;
    }
    return el && !['HTML', 'BODY'].includes(tagOf(el)) ? el : null;
  };
  const preview = (x, y, revisionKey) => {
    const el = hit(x, y);
    if (!el) return null;
    // A root introduced through another realm/native parser may not have
    // reached the incremental observer. Preview cannot certify that coverage.
    for (let node = el; node; node = parent(node)) {
      const root = getRoot(node);
      if (root instanceof ShadowRoot && !globalThis[revisionKey]?.observes?.(root)) return { uncovered: true };
    }
    const facts = inspect(el);
    if (!facts) return null;
    const box = boxOf(el);
    // No accessible-name, labels, text traversal, value read, or pN allocation.
    return { identity: identityOf(el), tag: facts.tag.slice(0, 32), role: roleOf(el),
      rect: { x: box.left, y: box.top, width: box.width, height: box.height }, facts };
  };
  Object.defineProperty(globalThis, key, { value: Object.freeze({ hit, inspect, summarize, identityOf, preview, geometry }), writable: false, configurable: false });
}

function clipped(box, viewport) {
  if (!box) return null;
  const x = Math.max(0, box.x), y = Math.max(0, box.y);
  const right = Math.min(viewport.width, box.x + box.width), bottom = Math.min(viewport.height, box.y + box.height);
  return right > x && bottom > y ? { x, y, width: right - x, height: bottom - y } : null;
}

export class BrowserPicker {
  constructor(session, HTTPError) {
    this.session = session;
    this.HTTPError = HTTPError;
    this.key = `__pogoPick_${randomBytes(12).toString('hex')}`;
    this.frames = new WeakMap();
    this.nextRef = 0;
    this.pending = new Map();
    this.previews = new Map();
  }

  async init() { await this.session.context.addInitScript(installProbe, { key: this.key }); }

  ready(body, generation, meta) {
    const s = this.session;
    s.assertLive();
    if (s.phase !== 'ready' || s.controlFor?.(meta).phase === 'takeover' || s.operationTouches?.(meta) || s.liveActions?.has(meta) || s.contextBlocked?.() || (meta.id === s.active && (s.rawAction || s.inOperation)) || s.dialog?.page === meta.page || s.devtools?.isPaused(meta) || s.devtools?.isBlocked(meta) ||
      s.pages.get(meta.id) !== meta || meta.page.isClosed() || body.tab_id !== meta.id || body.context_ref !== undefined && body.context_ref !== s.contextRef ||
      s.generation !== generation || s.version(meta) !== body.version) {
      throw new this.HTTPError(409, 'page changed or is busy; refresh the frame before selecting an element');
    }
  }

  async pick(body, { expectedIdentity } = {}) {
    // Only the trusted desktop host supplies this second argument after
    // resolving the actual Overlay backend node. HTTP JSON is never options.
    if (expectedIdentity !== undefined && (typeof expectedIdentity !== 'string' || !/^[a-f0-9]{24}:[1-9]\d{0,15}$/.test(expectedIdentity))) {
      throw new this.HTTPError(409, 'selected node identity is unavailable; select the element again');
    }
    return this.observe(body, false, async (meta, check) => {
      if (body.preview_token !== undefined) {
        if (typeof body.preview_token !== 'string' || !/^pv_[a-f0-9]{48}$/.test(body.preview_token)) throw new this.HTTPError(400, 'preview_token must be an issued preview capability');
        const token = this.previews.get(body.preview_token);
        this.previews.delete(body.preview_token);
        if (!token || token.expires <= Date.now() || token.meta !== meta || token.context !== this.session.contextRef || token.generation !== this.session.generation || token.version !== body.version || token.x !== body.x || token.y !== body.y || token.controlEpoch !== (this.session.controlFor?.(meta).epoch ?? 0) || expectedIdentity !== undefined && expectedIdentity !== token.identity) throw new this.HTTPError(409, 'element preview expired or changed; preview the current position again');
        expectedIdentity = token.identity;
      }
      return this.read(body, check, meta, expectedIdentity);
    });
  }

  async preview(body) {
    if (!body || typeof body.context_ref !== 'string' || Object.keys(body).some(key => !['x', 'y', 'tab_id', 'context_ref', 'version'].includes(key))) throw new this.HTTPError(400, 'preview requires a bound context, tab and displayed frame version');
    return this.observe(body, true, (meta, check) => this.readPreview(body, check, meta));
  }

  async observe(body, preview, read) {
    const s = this.session;
    if (!body || !Number.isFinite(body.x) || !Number.isFinite(body.y) || body.x < 0 || body.y < 0 ||
      typeof body.version !== 'string' || !body.version || body.version.length > 256 || typeof body.tab_id !== 'string' || !/^t[1-9]\d{0,9}$/.test(body.tab_id)) {
      throw new this.HTTPError(400, 'pick requires viewport coordinates, tab_id and a displayed frame version');
    }
    const meta = body.context_ref === undefined ? s.page() : s.selectedPage({ tab_id: body.tab_id, context_ref: body.context_ref });
    const viewport = s.viewport(meta);
    if (body.x >= viewport.width || body.y >= viewport.height) throw new this.HTTPError(400, 'pick coordinates must be inside the selected page viewport');
    if (body.context_ref === undefined && meta.id !== body.tab_id) throw new this.HTTPError(409, 'non-active tab picking requires context_ref');
    if (this.pending.has(meta)) throw new this.HTTPError(409, 'another element observation is still pending on this tab', 'PICK_OBSERVATION_PENDING');
    const generation = s.generation;
    const actionSequence = meta.actionSequence ?? 0;
    this.ready(body, generation, meta);
    s.touch();
    const budget = preview ? 750 : 1500, deadline = Date.now() + budget;
    const check = () => {
      if (Date.now() > deadline) throw new this.HTTPError(504, 'element observation timed out', 'PICK_OBSERVATION_PENDING');
      this.ready(body, generation, meta);
      if ((meta.actionSequence ?? 0) !== actionSequence) throw new this.HTTPError(409, 'browser action started during observation; refresh the frame');
    };
    const pending = Promise.resolve().then(() => { check(); return read(meta, check); }).finally(() => { if (this.pending.get(meta) === pending) this.pending.delete(meta); });
    this.pending.set(meta, pending);
    let timer;
    try {
      return await Promise.race([pending, new Promise((_, reject) => { timer = setTimeout(() => reject(new this.HTTPError(504, 'element observation timed out', 'PICK_OBSERVATION_PENDING')), budget); })]);
    } finally { clearTimeout(timer); }
  }

  async hit(frame, x, y, depth, check, offset = { x: 0, y: 0 }, bounds = { x: 0, y: 0, ...this.session.viewport() }) {
    check();
    const handle = await frame.evaluateHandle(({ key, x, y }) => globalThis[key]?.hit(x, y) ?? null, { key: this.key, x, y });
    const el = handle.asElement();
    if (!el) { await handle.dispose(); return null; }
    let retained = false;
    try {
      check();
      const facts = await el.evaluate((el, key) => globalThis[key]?.inspect(el) ?? null, this.key);
      check();
      if (!facts) return null;
      if (facts.animated) throw new this.HTTPError(409, 'selected element is animating; wait for it to settle and refresh the frame');
      if (['iframe', 'frame'].includes(facts.tag)) {
        const cx = x - facts.left, cy = y - facts.top;
        if (!facts.transformed && depth < 8 && cx >= 0 && cy >= 0 && cx < facts.width && cy < facts.height) {
          const child = await el.contentFrame();
          if (child) {
            const nextOffset = { x: offset.x + facts.left, y: offset.y + facts.top };
            const right = Math.min(bounds.x + bounds.width, nextOffset.x + facts.width), bottom = Math.min(bounds.y + bounds.height, nextOffset.y + facts.height);
            const clip = { x: Math.max(bounds.x, nextOffset.x), y: Math.max(bounds.y, nextOffset.y) };
            clip.width = Math.max(0, right - clip.x); clip.height = Math.max(0, bottom - clip.y);
            const found = clip.width && clip.height ? await this.hit(child, cx, cy, depth + 1, check, nextOffset, clip) : null;
            if (found) return found;
          }
        }
        retained = true;
        return { el, frame, offset, bounds, fallback: 'frame' };
      }
      retained = true;
      return { el, frame, offset, bounds };
    } finally { if (!retained) await handle.dispose(); }
  }

  async previewHit(frame, x, y, depth, check, offset, bounds) {
    check();
    const s = this.session;
    const value = await frame.evaluate(({ key, revisionKey, x, y }) => globalThis[key]?.preview(x, y, revisionKey) ?? null,
      { key: this.key, revisionKey: s.revisionKey, x, y });
    check();
    if (!value) return null;
    if (value.uncovered) throw new this.HTTPError(409, 'page observation coverage is incomplete; refresh the frame before selecting');
    if (value.facts.animated) throw new this.HTTPError(409, 'selected element is animating; wait for it to settle and refresh the frame');
    const box = { x: offset.x + value.rect.x, y: offset.y + value.rect.y, width: value.rect.width, height: value.rect.height };
    const intersect = (a, b) => {
      const x = Math.max(a.x, b.x), y = Math.max(a.y, b.y), right = Math.min(a.x + a.width, b.x + b.width), bottom = Math.min(a.y + a.height, b.y + b.height);
      return right > x && bottom > y ? { x, y, width: right - x, height: bottom - y } : null;
    };
    const rect = intersect(box, bounds);
    if (!rect) return null;
    if (['iframe', 'frame'].includes(value.tag)) {
      const facts = value.facts, cx = x - facts.left, cy = y - facts.top;
      if (!facts.transformed && depth < 8 && cx >= 0 && cy >= 0 && cx < facts.width && cy < facts.height) {
        // Only iframe descent needs a handle. Reconcile the exact native node
        // before obtaining its child frame; ordinary hover is one evaluation.
        const handle = await frame.evaluateHandle(({ key, x, y, identity }) => {
          const probe = globalThis[key], el = probe?.hit(x, y);
          return el && probe.identityOf(el) === identity ? el : null;
        }, { key: this.key, x, y, identity: value.identity });
        try {
          check();
          const el = handle.asElement();
          if (!el) throw new this.HTTPError(409, 'frame element changed during preview; refresh the frame');
          const child = await el.contentFrame();
          check();
          if (child) {
            const nextOffset = { x: offset.x + facts.left, y: offset.y + facts.top };
            const clip = intersect(bounds, { ...nextOffset, width: facts.width, height: facts.height });
            if (clip) {
              const found = await this.previewHit(child, cx, cy, depth + 1, check, nextOffset, clip);
              if (found) return found;
            }
          }
        } finally { await handle.dispose(); }
      }
      return { identity: value.identity, element: { tag: value.tag, role: value.role, rect, fallback: 'frame' } };
    }
    return { identity: value.identity, element: { tag: value.tag, role: value.role, rect } };
  }

  async readPreview(body, check, meta) {
    const s = this.session;
    await s.refreshRevision(s.observation(), meta, { fast: true, unbounded: true });
    check();
    if (meta.revisionCovered === false) throw new this.HTTPError(409, 'page observation coverage is incomplete; this page cannot be previewed reliably');
    const viewport = s.viewport(meta);
    const found = await this.previewHit(meta.page.mainFrame(), body.x, body.y, 0, check, { x: 0, y: 0 }, { x: 0, y: 0, ...viewport });
    await s.refreshRevision(s.observation(), meta, { fast: true, unbounded: true });
    check();
    const response = { context_ref: s.contextRef, version: body.version, tab_id: body.tab_id, viewport, element: found?.element ?? null };
    if (!found) return response;
    const now = Date.now();
    for (const [token, record] of this.previews) if (record.expires <= now || s.pages.get(record.meta.id) !== record.meta) this.previews.delete(token);
    const own = [...this.previews].filter(([, record]) => record.meta === meta);
    while (own.length >= 32) this.previews.delete(own.shift()[0]);
    while (this.previews.size >= 512) this.previews.delete(this.previews.keys().next().value);
    const token = `pv_${randomBytes(24).toString('hex')}`;
    this.previews.set(token, { meta, identity: found.identity, context: s.contextRef, generation: s.generation,
      controlEpoch: s.controlFor?.(meta).epoch ?? 0, version: body.version, x: body.x, y: body.y, expires: now + 3000 });
    return { ...response, preview_token: token, expires_in_ms: 3000 };
  }

  async read(body, check, meta, expectedIdentity) {
    const s = this.session;
    await s.refreshRevision(s.observation(), meta, { unbounded: true });
    check();
    if (meta.revisionCovered === false) throw new this.HTTPError(409, 'page observation coverage is incomplete; this page cannot be picked reliably');
    const found = await this.hit(meta.page.mainFrame(), body.x, body.y, 0, check, { x: 0, y: 0 }, { x: 0, y: 0, ...s.viewport(meta) });
    if (!found && expectedIdentity !== undefined) throw new this.HTTPError(409, 'selected node is no longer at this position; select the element again');
    let element = null;
    try {
      if (found) {
        const observation = await found.el.evaluate((el, key) => {
          const probe = globalThis[key]; return probe ? { summary: probe.summarize(el), rect: probe.geometry(el) } : null;
        }, this.key);
        const summary = observation?.summary;
        if (expectedIdentity !== undefined && summary?.identity !== expectedIdentity) throw new this.HTTPError(409, 'selected node no longer matches this position; select the element again');
        const box = observation?.rect;
        const local = box && clipped({ x: box.x + found.offset.x - found.bounds.x, y: box.y + found.offset.y - found.bounds.y, width: box.width, height: box.height }, found.bounds);
        const rect = local && { ...local, x: local.x + found.bounds.x, y: local.y + found.bounds.y };
        check();
        if (summary && rect && body.x >= rect.x - 0.01 && body.y >= rect.y - 0.01 && body.x <= rect.x + rect.width + 0.01 && body.y <= rect.y + rect.height + 0.01) {
          let entry = this.frames.get(found.frame);
          const stamp = `${meta.id}:${meta.version}`;
          if (!entry || entry.stamp !== stamp) { entry = { stamp, refs: new Map() }; this.frames.set(found.frame, entry); }
          let ref = entry.refs.get(summary.identity);
          if (!ref) {
            if (entry.refs.size >= 2048) throw new this.HTTPError(409, 'element observation limit reached for this document');
            ref = `p${++this.nextRef}`;
            entry.refs.set(summary.identity, ref);
          }
          const { identity, ...facts } = summary;
          element = { ref, ...facts, rect, ...(found.frame !== meta.page.mainFrame() ? { frame_url: found.frame.url().slice(0, 4096) } : {}), ...(found.fallback ? { fallback: found.fallback } : {}) };
        } else if (summary && rect || expectedIdentity !== undefined) throw new this.HTTPError(409, 'element moved during observation; refresh the frame');
      }
      await s.refreshRevision(s.observation(), meta, { unbounded: true });
      check();
      return { context_ref: s.contextRef, version: body.version, tab_id: body.tab_id, url: meta.page.url().slice(0, 4096), title: meta.title.slice(0, 300), viewport: s.viewport(meta), element };
    } finally { if (found) await found.el.dispose(); }
  }
}
