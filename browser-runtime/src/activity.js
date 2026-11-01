// Viewer-only observations. Never move the mouse, focus an element, change a
// style, or expose values typed into the page to make an animation look busy.
const labels = {
  snapshot: 'Inspecting page', find: 'Finding target', click: 'Clicking target',
  type: 'Filling field', select: 'Selecting option', press: 'Pressing key',
  hover: 'Hovering target', scroll: 'Scrolling page', wait: 'Waiting for page',
  extract: 'Reading target', navigate: 'Opening page', screenshot: 'Capturing page',
  tabs: 'Inspecting tabs', tab: 'Changing tab', input_text: 'Entering text',
  input_scroll: 'Scrolling page', input_back: 'Going back', dialog: 'Answering dialog',
  calculate: 'Checking observed quantities',
  devtools: 'Inspecting browser internals',
  upload: 'Selecting files',
};

// Only these operations dispatch mouse input. Chromium can emit trusted moves
// during navigation, focus changes or layout; trust alone does not identify
// the actor responsible for an event.
const pointerActions = new Set(['click', 'hover']);

async function optional(promise, ms = 250) {
  let timer;
  try { return await Promise.race([promise, new Promise(resolve => { timer = setTimeout(() => resolve(null), ms); })]); }
  catch { return null; }
  finally { clearTimeout(timer); }
}

function rect(box, viewport) {
  if (!box || !['x', 'y', 'width', 'height'].every(k => Number.isFinite(box[k])) || box.width <= 0 || box.height <= 0) return null;
  const left = Math.max(0, box.x), top = Math.max(0, box.y);
  const right = Math.min(viewport.width, box.x + box.width), bottom = Math.min(viewport.height, box.y + box.height);
  return right > left && bottom > top ? { x: left, y: top, width: right - left, height: bottom - top } : null;
}

export class BrowserActivity {
  constructor(session) {
    this.session = session;
    this.seq = 0;
    this.pointerSeq = 0;
    this.records = [];
    this.target = null;
    this.pointer = null;
    this.pointerKey = '';
    this.pointerAction = null;
    this.suppressPointer = false;
    this.pendingReads = new Map();
    this.geometryFlight = null;
  }

  async readOnce(key, stamp, work, budget) {
    let entry = this.pendingReads.get(key);
    // An unresolved evaluation does not become another pending evaluation on
    // every viewer poll. A changed action may skip this decoration for now.
    if (entry && entry.stamp !== stamp) return null;
    if (!entry) {
      entry = { stamp };
      entry.promise = Promise.resolve().then(work).finally(() => {
        if (this.pendingReads.get(key) === entry) this.pendingReads.delete(key);
      });
      this.pendingReads.set(key, entry);
    }
    return optional(entry.promise, budget);
  }

  notify() {
    // Observation is optional and never joins the browser's action queue.
    for (const view of this.session.liveViews ?? []) {
      // State must precede its decoration: the client deliberately rejects a
      // marker whose action sequence differs from the observed page status.
      if (view.publishState) view.publishState(true);
      else view.publishOverlay?.();
    }
  }

  begin(action, actor) {
    const s = this.session, meta = s.pages.get(s.active);
    const now = Date.now();
    const record = { seq: ++this.seq, id: `a${this.seq}`, actor, action: action.action,
      phase: 'started', label: labels[action.action] ?? 'Browser action', tab_id: s.active ?? '',
      version: s.version(), started_at: now, updated_at: now };
    this.records.push(record);
    if (meta) meta.actionSequence = record.seq;
    if (this.records.length > 8) this.records.shift();
    this.target = action.target ? { value: action.target, tab: s.active, document: meta?.version } : null;
    const ref = s.refs.get(action.target);
    this.pointerAction = pointerActions.has(action.action) ? { record, generation: s.generation,
      tab: s.active, document: meta?.version, frame: ref?.frame ?? meta?.page.mainFrame?.() } : null;
    this.suppressPointer = false;
    if (this.pointerAction && ref && ref.frame !== meta?.page.mainFrame()) {
      // Child-frame coordinates will be mapped from their actual trusted event.
      // Never leave an earlier main-frame point visible while that is pending.
      this.pointer = null;
      this.pointerKey = '';
    }
    this.notify();
    return { record, generation: s.generation };
  }

  async finish(handle, ok, pending = false) {
    const { record, generation } = handle;
    if (generation !== this.session.generation) {
      record.phase = 'failed';
      record.label = 'Action interrupted';
    } else if (pending) {
      record.phase = 'started';
      record.label = 'Waiting for dialog';
    } else record.phase = ok ? 'succeeded' : 'failed';
    record.updated_at = Date.now();
    record.version = this.session.version();
    if (!pending && generation === this.session.generation) await optional(this.capturePointer(record), 150);
    this.notify();
  }

  fence() {
    for (const record of this.records) if (record.phase === 'started') {
      record.phase = 'failed'; record.label = 'Action interrupted'; record.updated_at = Date.now();
    }
    this.target = null;
    this.pointer = null;
    this.pointerKey = '';
    this.pointerAction = null;
    this.suppressPointer = true;
    this.notify();
  }

  state() {
    const recent_actions = this.records.map(record => ({ ...record }));
    return { last_action: recent_actions.at(-1) ?? null, recent_actions };
  }

  coordinateClick(x, y, generation) {
    const s = this.session, last = this.records.at(-1);
    if (s.generation !== generation || last?.action !== 'click' || !this.pointerMatches(last, s.pages.get(s.active))) return;
    this.pointer = { tab: s.active, document: s.pages.get(s.active)?.version, value: {
      x, y, seq: ++this.pointerSeq, action_seq: last.seq, kind: 'click', actor: last.actor,
    } };
    this.suppressPointer = false;
    this.notify();
  }

  async mainFramePoint(frame, observed, meta, stamp) {
    if (frame === meta.page.mainFrame()) return observed;
    if (!frame || frame.isDetached?.()) return null;
    // boundingBox is in the main viewport. Reject rotated/skewed/perspective
    // iframe trees rather than guessing from the bounding rectangle's centre.
    const mapping = await this.readOnce('pointer-frame', stamp, async () => {
      let source = frame, first = null;
      while (source && source !== meta.page.mainFrame()) {
        const element = await source.frameElement();
        try {
          const metrics = await element.evaluate(el => {
            for (let node = el; node; node = node.parentElement) {
              const style = getComputedStyle(node);
              if (style.perspective !== 'none' || style.rotate !== 'none' ||
                style.scale !== 'none' && style.scale.split(/\s+/).some(value => Number(value) <= 0)) return null;
              const transform = style.transform;
              if (transform !== 'none') {
                const matrix = new DOMMatrixReadOnly(transform);
                if (!matrix.is2D || matrix.b || matrix.c || matrix.a <= 0 || matrix.d <= 0) return null;
              }
            }
            return { width: el.offsetWidth, height: el.offsetHeight, left: el.clientLeft, top: el.clientTop };
          });
          if (!metrics?.width || !metrics?.height) return null;
          if (!first) {
            const box = await element.boundingBox();
            if (!box) return null;
            first = { box, metrics };
          }
        } finally { await element.dispose(); }
        source = source.parentFrame();
      }
      return first;
    }, 120);
    if (!mapping) return null;
    const { box, metrics } = mapping;
    return { ...observed, x: box.x + (metrics.left + observed.x) * box.width / metrics.width,
      y: box.y + (metrics.top + observed.y) * box.height / metrics.height };
  }

  pointerMatches(last, meta) {
    const source = this.pointerAction;
    return Boolean(source && meta && source.record === last && source.generation === this.session.generation &&
      source.tab === meta.id && source.document === meta.version);
  }

  async capturePointer(last = this.records.at(-1)) {
    const s = this.session, meta = s.pages.get(s.active);
    if (!meta || meta.page.isClosed() || typeof meta.page.mainFrame !== 'function' || !last || this.suppressPointer || !this.pointerMatches(last, meta)) return;
    const generation = s.generation, documentVersion = meta.version;
    const frame = this.pointerAction.frame ?? meta.page.mainFrame();
    const stamp = `${generation}:${meta.id}:${documentVersion}:${last.seq}`;
    const local = await this.readOnce('pointer', stamp, () => frame.evaluate(key => globalThis[key]?.() ?? null, s.pointerKey), 120);
    const observed = local ? await this.mainFramePoint(frame, local, meta, stamp) : null;
    if (s.generation !== generation || s.active !== meta.id || meta.version !== documentVersion || last !== this.records.at(-1) || this.suppressPointer || !this.pointerMatches(last, meta)) return;
    const viewport = s.viewport(meta);
    if (observed && Number.isFinite(observed.x) && Number.isFinite(observed.y) && observed.x >= 0 && observed.x < viewport.width && observed.y >= 0 && observed.y < viewport.height) {
      const key = `${meta.id}:${meta.version}:${last.seq}:${observed.seq}`;
      if (key !== this.pointerKey && !this.suppressPointer) {
        const duringAction = observed.at >= last.started_at && (last.phase === 'started' || observed.at <= last.updated_at);
        const expectedKind = observed.kind === 'move' || observed.kind === 'click' && last.action === 'click';
        if (duringAction && expectedKind) {
          this.pointer = { tab: meta.id, document: meta.version, value: {
            x: observed.x, y: observed.y, seq: ++this.pointerSeq, action_seq: last.seq,
            kind: observed.kind === 'click' ? 'click' : 'move', actor: last.actor,
          } };
        }
        this.pointerKey = key;
      }
    }
  }

  async geometry() {
    const s = this.session, meta = s.pages.get(s.active);
    if (!meta || meta.page.isClosed()) return {};
    const generation = s.generation, documentVersion = meta.version, sequence = this.seq;
    const current = () => generation === s.generation && s.active === meta.id && meta.version === documentVersion && sequence === this.seq;
    const deadline = Date.now() + 550;
    const stamp = `${generation}:${meta.id}:${documentVersion}:${sequence}`;
    const fields = {};
    await this.capturePointer();
    if (!current()) return {};
    if (!this.suppressPointer && this.pointer?.tab === meta.id && this.pointer.document === meta.version) fields.pointer = { ...this.pointer.value };
    if (this.target?.tab === meta.id && this.target.document === meta.version) {
      const target = this.target.value;
      const box = await this.readOnce('target', stamp, async () => (await s.locator(target)).boundingBox({ timeout: 150 }), 160);
      if (!current()) return {};
      const clipped = rect(box, s.viewport(meta));
      if (clipped) fields.target = { rect: clipped };
    }
    // Query only geometry and editability. In particular, never read value,
    // textContent, accessible name, or a password field's contents.
    for (const frame of meta.page.frames().slice(0, 32)) {
      const remaining = deadline - Date.now();
      if (remaining <= 0 || !current()) break;
      const focused = await this.readOnce(frame, stamp, async () => {
        const handle = await frame.evaluateHandle(() => {
          if (!document.hasFocus()) return null;
          let element = document.activeElement;
          while (element?.shadowRoot?.activeElement) element = element.shadowRoot.activeElement;
          if (!element || ['BODY', 'HTML', 'IFRAME', 'FRAME'].includes(element.tagName)) return null;
          return element;
        });
        try {
          const element = handle.asElement();
          if (!element) return null;
          const [box, editable] = await Promise.all([element.boundingBox(), element.evaluate(el => el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))]);
          const clipped = rect(box, s.viewport(meta));
          return clipped ? { rect: clipped, editable } : null;
        } finally { await handle.dispose(); }
      }, Math.min(180, remaining));
      if (!current()) return {};
      if (focused) { fields.focus = focused; break; }
    }
    return fields;
  }

  async overlay(version) {
    const s = this.session;
    const stamp = `${s.generation}:${s.active}:${s.pages.get(s.active)?.version}:${this.seq}`;
    if (!this.geometryFlight) {
      const flight = { stamp };
      flight.promise = this.geometry().finally(() => { if (this.geometryFlight === flight) this.geometryFlight = null; });
      this.geometryFlight = flight;
    }
    const fields = this.geometryFlight.stamp === stamp ? await optional(this.geometryFlight.promise, 600) ?? {} : {};
    return { version, tab_id: s.active ?? '', viewport: s.viewport(),
      sampled_at: Date.now(), last_action: this.state().last_action ?? undefined, ...fields };
  }

  cachedOverlay(version) {
    const s = this.session, meta = s.pages.get(s.active), last = this.records.at(-1);
    const pointer = this.pointer?.tab === meta?.id && this.pointer.document === meta?.version &&
      this.pointer.value.action_seq === last?.seq && this.pointer.value.actor === last?.actor && !this.suppressPointer ? { ...this.pointer.value } : undefined;
    return { version, tab_id: s.active ?? '', viewport: s.viewport(), sampled_at: Date.now(),
      ...(last ? { last_action: { ...last } } : {}), ...(pointer ? { pointer } : {}) };
  }
}
