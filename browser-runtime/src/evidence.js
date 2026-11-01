// This function is deliberately self-contained: Playwright serializes it into
// either frame.evaluate(fn, options) or locator.evaluate(fn, options). It reads
// rendered DOM structure only; it never invokes site data hooks or page state.
export function captureDOMEvidence(root, options, intrinsics) {
  const read = intrinsics ?? {
    isElement: node => node?.nodeType === 1, nodeType: node => node?.nodeType,
    ownerDocument: node => node.ownerDocument, body: doc => doc.body, url: doc => doc.URL, title: doc => doc.title,
    tag: el => el?.tagName ?? '', editable: el => el?.isContentEditable === true, parent: el => el?.parentElement ?? null,
    root: el => el.getRootNode(), host: node => node?.host ?? null, children: el => el.children ?? [], shadowChildren: node => node.children ?? [],
    shadow: el => el.shadowRoot, attr: (el, name) => el.getAttribute(name), style: el => getComputedStyle(el),
    property: (css, name) => css.getPropertyValue(name), open: el => el.open,
    walker: (doc, el) => doc.createTreeWalker(el, 4), next: walker => walker.nextNode(), range: doc => doc.createRange(),
    select: (range, node) => range.selectNodeContents(node), rects: range => range.getClientRects(),
    width: rect => rect.width, height: rect => rect.height, text: node => node.textContent,
    rowspan: el => el.rowSpan, colspan: el => el.colSpan, contains: (el, child) => el.contains(child), href: el => el.href,
  };
  const elementRoot = read.isElement(root);
  const opts = (elementRoot ? options : root ?? options) ?? {};
  const doc = elementRoot ? read.ownerDocument(root) : document;
  const scopeRoot = elementRoot ? root : read.body(doc);
  const prefix = String(opts.sourcePrefix ?? opts.observationId ?? 'dom').slice(0, 160);
  const bounded = (name, fallback, ceiling) => Number.isInteger(opts[name]) && opts[name] > 0 ? Math.min(opts[name], ceiling) : fallback;
  const limits = {
    tables: bounded('maxTables', 8, 8), rows: bounded('maxRows', 40, 40), cells: bounded('maxCells', 12, 12),
    cell_chars: bounded('maxCellChars', 300, 300), definition_groups: bounded('maxDefinitionGroups', 40, 40),
    headings: bounded('maxHeadings', 16, 16), paragraphs: bounded('maxParagraphs', 32, 32),
    paragraph_chars: bounded('maxParagraphChars', 600, 600), text_chars: bounded('maxTextChars', 8000, 8000),
    structure_chars: bounded('maxStructureChars', opts.full === true ? 28000 : 16000, 28000), entries: 600, scanned_elements: 12000,
    output_chars: Math.max(8192, bounded('maxOutputChars', opts.full === true ? 96000 : 64000, 96000)),
  };
  const output = { schema: 'dom-evidence-v1', scope: elementRoot ? 'subtree' : 'document', source_id: `${prefix}/root`,
    url: String(read.url(doc) ?? '').slice(0, 4096), title: String(read.title(doc) ?? '').slice(0, 300), text: '',
    tables: [], definitions: [], headings: [], pagination: { status: 'unknown', current: [], links: [] },
    paragraphs: [], status: 'observed', truncated: false, limits };
  if (!scopeRoot) return output;
  let remaining = limits.structure_chars, entries = 0;
  const source = path => `${prefix}/${path}`;
  const mark = holder => { output.truncated = true; output.status = 'partial'; if (holder) holder.truncated = true; };
  const tidy = value => String(value ?? '').replace(/\s+/g, ' ').trim();
  const tag = el => el ? read.tag(el).toUpperCase() : '';
  const field = el => ['INPUT', 'TEXTAREA', 'SELECT', 'OPTION'].includes(tag(el)) || el && read.editable(el);
  const parent = el => el ? read.parent(el) ?? read.host(read.root(el)) : null;
  const hidden = el => {
    for (let current = el; current; current = parent(current)) {
      if (read.nodeType(current) !== 1) continue;
      const css = read.style(current), visibility = read.property(css, 'visibility');
      if (read.attr(current, 'hidden') !== null || read.attr(current, 'aria-hidden') === 'true' ||
        read.property(css, 'display') === 'none' || visibility === 'hidden' || visibility === 'collapse' ||
        read.property(css, 'content-visibility') === 'hidden' || Number(read.property(css, 'opacity')) === 0) return true;
      const p = parent(current);
      if (tag(p) === 'DETAILS' && !read.open(p)) {
        const summary = Array.from(read.children(p)).find(child => tag(child) === 'SUMMARY');
        if (current !== summary) return true;
      }
    }
    return false;
  };
  const visibleText = (el, max, holder, excludeNestedStructures = false) => {
    if (!el || field(el) || hidden(el)) return '';
    const walker = read.walker(doc, el);
    let node, value = '', visited = 0;
    while ((node = read.next(walker))) {
      if (++visited > limits.scanned_elements) { if (holder) holder.text_truncated = true; mark(holder); break; }
      const p = read.parent(node);
      let excluded = false;
      for (let current = p; current; current = parent(current)) {
        if (field(current) || ['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'IFRAME', 'FRAME'].includes(tag(current))) { excluded = true; break; }
        if (excludeNestedStructures && current !== el && ['TABLE', 'DL'].includes(tag(current))) { excluded = true; break; }
        if (current === el) break;
      }
      if (excluded || !p || hidden(p)) continue;
      // A text node with no rendered rectangle is not a visible fact, including
      // closed details and non-rendered descendants of display:contents.
      const range = read.range(doc);
      read.select(range, node);
      if (!Array.from(read.rects(range)).some(box => read.width(box) > 0 && read.height(box) > 0)) continue;
      value += ` ${read.text(node) ?? ''}`;
      if (value.length > max * 2 + 1024) { if (holder) holder.text_truncated = true; mark(holder); break; }
    }
    value = tidy(value);
    if (value.length > max) { value = value.slice(0, max); if (holder) holder.text_truncated = true; mark(holder); }
    return value;
  };
  // Charge a complete row/group together. A remaining budget must never turn
  // "1250 g" into a seemingly exact "125" quantity.
  const commit = (texts, count, holder) => {
    const cost = texts.reduce((sum, text) => sum + text.length, 0);
    if (entries + count > limits.entries || cost > remaining) { mark(holder); return false; }
    entries += count; remaining -= cost; return true;
  };
  const elements = [], stack = [scopeRoot];
  let scanned = 0;
  while (stack.length && scanned++ < limits.scanned_elements) {
    const el = stack.pop();
    if (hidden(el) || field(el) || ['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'IFRAME', 'FRAME'].includes(tag(el))) continue;
    elements.push(el);
    const children = Array.from(read.children(el));
    for (let i = children.length - 1; i >= 0; i--) stack.push(children[i]);
    // Open shadow roots are separately traversed without reading hidden state.
    const shadow = read.shadow(el);
    if (shadow) {
      const shadowChildren = Array.from(read.shadowChildren(shadow));
      for (let i = shadowChildren.length - 1; i >= 0; i--) stack.push(shadowChildren[i]);
    }
  }
  if (stack.length) mark();
  const within = (el, container, name) => {
    for (let current = el; current; current = parent(current)) {
      if (tag(current) === name) return current === container;
      if (current === scopeRoot) break;
    }
    return false;
  };
  for (const el of elements.filter(el => tag(el) === 'TABLE')) {
    if (output.tables.length >= limits.tables) { mark(); break; }
    const index = output.tables.length, path = `table/${index}`;
    const table = { source_id: source(path), caption: '', header_rows: [], rows: [], column_mapping: 'unknown', truncated: false };
    const caption = Array.from(read.children(el)).find(child => tag(child) === 'CAPTION');
    table.caption = visibleText(caption, limits.cell_chars, table);
    if (!commit([table.caption], 1, table)) break;
    output.tables.push(table);
    const rows = elements.filter(row => tag(row) === 'TR' && within(row, el, 'TABLE'));
    let hasSpan = false, counts = [], headerCount = 0, headerPosition = -1;
    for (const [rowIndex, row] of rows.entries()) {
      if (rowIndex >= limits.rows) { mark(table); break; }
      const rowPath = `${path}/row/${rowIndex}`, observed = { source_id: source(rowPath), cells: [] };
      const cells = Array.from(read.children(row)).filter(cell => ['TH', 'TD'].includes(tag(cell)) && !hidden(cell));
      if (cells.length > limits.cells) { mark(table); continue; }
      const header = tag(parent(row)) === 'THEAD' || (cells.length > 0 && cells.every(cell => tag(cell) === 'TH'));
      for (const [cellIndex, cell] of cells.entries()) {
        const rawRowspan = read.rowspan(cell), rowspan = Number.isInteger(rawRowspan) ? rawRowspan : 1, colspan = Math.max(1, read.colspan(cell) || 1);
        hasSpan ||= rowspan !== 1 || colspan !== 1;
        const item = { source_id: source(`${rowPath}/cell/${cellIndex}`), text: '', tag: tag(cell).toLowerCase(), rowspan, colspan };
        item.text = visibleText(cell, limits.cell_chars, item, true);
        if (item.truncated) mark(table);
        observed.cells.push(item);
      }
      if (!commit(observed.cells.map(cell => cell.text), observed.cells.length + 1, table)) break;
      if (header) { table.header_rows.push(observed); headerCount++; headerPosition = rowIndex; } else table.rows.push(observed);
      counts.push(cells.length);
    }
    if (rows.length > limits.rows) mark(table);
    // Keep native positions. Complex or incomplete headers never become a
    // synthetic rectangular table, and an empty cell is never filtered away.
    if (!hasSpan && headerCount === 1 && headerPosition === 0 && counts.length && counts.every(count => count === counts[0]) && !table.truncated) table.column_mapping = 'simple';
  }
  for (const el of elements.filter(el => tag(el) === 'DL')) {
    const index = output.definitions.length, path = `definition/${index}`;
    const definition = { source_id: source(path), groups: [], truncated: false };
    if (!commit([], 1, definition)) break;
    output.definitions.push(definition);
    const fields = elements.filter(field => ['DT', 'DD'].includes(tag(field)) && within(field, el, 'DL'));
    const groups = [];
    let group = null, hadDescription = false;
    for (const item of fields) {
      const term = tag(item) === 'DT';
      if (!group || (term && hadDescription)) {
        if (groups.length >= limits.definition_groups) { mark(definition); break; }
        group = { source_id: source(`${path}/group/${groups.length}`), terms: [], descriptions: [] };
        groups.push(group); hadDescription = false;
      }
      const list = term ? group.terms : group.descriptions;
      const value = { source_id: `${group.source_id}/${term ? 'term' : 'description'}/${list.length}`, text: '' };
      value.text = visibleText(item, limits.cell_chars, value, true);
      if (value.truncated) mark(definition);
      list.push(value);
      hadDescription ||= !term;
    }
    for (const group of groups) {
      const values = [...group.terms, ...group.descriptions];
      if (!commit(values.map(value => value.text), values.length + 1, definition)) break;
      definition.groups.push(group);
    }
  }
  const headingByNode = new Map();
  for (const el of elements.filter(el => /^H[1-6]$/.test(tag(el)))) {
    if (output.headings.length >= limits.headings) { mark(); continue; }
    const heading = { source_id: source(`heading/${output.headings.length}`), level: Number(tag(el)[1]), text: '' };
    heading.text = visibleText(el, limits.cell_chars, heading);
    if (commit([heading.text], 1, heading)) { headingByNode.set(el, heading); output.headings.push(heading); }
  }
  let currentHeading = null;
  for (const el of elements) {
    if (/^H[1-6]$/.test(tag(el))) {
      currentHeading = headingByNode.get(el) ?? null;
    }
    const paragraph = tag(el) === 'P' || (tag(el) === 'SECTION' && !elements.some(child => tag(child) === 'P' && read.contains(el, child)));
    if (paragraph) {
      if (output.paragraphs.length >= limits.paragraphs) { mark(); continue; }
      // A section node precedes its child heading in traversal order. Its own
      // explicit heading must not be borrowed from the previous section.
      const sectionHeading = tag(el) === 'SECTION' ? Array.from(read.children(el)).find(child => /^H[1-6]$/.test(tag(child))) : null;
      const paragraphHeading = tag(el) === 'SECTION' ? headingByNode.get(sectionHeading) ?? null : currentHeading;
      const item = { source_id: source(`paragraph/${output.paragraphs.length}`), text: '' };
      item.text = visibleText(el, limits.paragraph_chars, item);
      if (item.text && commit([item.text, paragraphHeading?.text ?? ''], 1, item)) output.paragraphs.push({ ...item,
        ...(paragraphHeading ? { heading_source_id: paragraphHeading.source_id, heading: paragraphHeading.text } : {}) });
    }
    if (read.attr(el, 'aria-current') === 'page') {
      const item = { source_id: source(`pagination/current/${output.pagination.current.length}`), text: '', aria_current: 'page' };
      item.text = visibleText(el, limits.cell_chars, item);
      if (commit([item.text], 1, item)) output.pagination.current.push(item);
    }
    if (tag(el) === 'A' && /(?:^|\s)(?:next|prev)(?:\s|$)/.test(read.attr(el, 'rel') ?? '')) {
      const item = { source_id: source(`pagination/link/${output.pagination.links.length}`), text: '', href: String(read.href(el) ?? '').slice(0, 4096), rel: read.attr(el, 'rel') };
      item.text = visibleText(el, limits.cell_chars, item);
      if (commit([item.text, item.href, item.rel ?? ''], 1, item)) output.pagination.links.push(item);
    }
  }
  if (output.pagination.current.length) output.pagination.status = 'observed';
  output.text = visibleText(scopeRoot, limits.text_chars, output);
  // Bound metadata as well as content: hundreds of empty cells still carry
  // source paths. Remove complete structures, never split JSON or numeric text.
  const size = () => JSON.stringify(output).length;
  if (size() > limits.output_chars) {
    mark(); output.text = ''; output.text_truncated = true;
    const trim = array => { while (array.length && size() > limits.output_chars) array.pop(); };
    trim(output.paragraphs);
    trim(output.pagination.links);
    trim(output.pagination.current);
    if (!output.pagination.current.length) output.pagination.status = 'unknown';
    trim(output.headings);
    while (output.definitions.length && size() > limits.output_chars) {
      const definition = output.definitions.at(-1);
      mark(definition);
      if (definition.groups.length) definition.groups.pop(); else output.definitions.pop();
    }
    while (output.tables.length && size() > limits.output_chars) {
      const table = output.tables.at(-1);
      mark(table); table.column_mapping = 'unknown';
      if (table.rows.length) table.rows.pop();
      else if (table.header_rows.length) table.header_rows.pop();
      else output.tables.pop();
    }
  }
  return output;
}

// Install before website scripts. The capture function is supplied as a fixed
// function literal in Node-built init-script content, never parsed with page
// Function/eval. Only this DOM reader is exposed; the captured API object stays
// in the closure and cannot be replaced through observation options.
export function installEvidenceProbe({ key, capture }) {
  const invoke = Function.prototype.call.bind(Function.prototype.call);
  const method = (prototype, name) => { const fn = prototype[name]; return (receiver, ...args) => invoke(fn, receiver, ...args); };
  const getter = (prototype, name) => {
    let descriptor;
    for (let current = prototype; current && !descriptor; current = Object.getPrototypeOf(current)) descriptor = Object.getOwnPropertyDescriptor(current, name);
    const fn = descriptor?.get;
    if (!fn) throw new Error(`Native DOM getter unavailable: ${name}`);
    return receiver => invoke(fn, receiver);
  };
  const nodeType = getter(Node.prototype, 'nodeType'), parent = getter(Node.prototype, 'parentElement');
  const root = method(Node.prototype, 'getRootNode'), host = getter(ShadowRoot.prototype, 'host');
  const editable = getter(HTMLElement.prototype, 'isContentEditable');
  const intrinsics = Object.freeze({
    isElement: node => { try { return nodeType(node) === 1; } catch { return false; } }, nodeType,
    ownerDocument: getter(Node.prototype, 'ownerDocument'), body: getter(Document.prototype, 'body'),
    url: getter(Document.prototype, 'URL'), title: getter(Document.prototype, 'title'), tag: getter(Element.prototype, 'tagName'),
    editable: el => { try { return editable(el); } catch { return false; } }, parent, root,
    host: node => { try { return host(node); } catch { return null; } },
    children: getter(Element.prototype, 'children'), shadowChildren: getter(DocumentFragment.prototype, 'children'),
    shadow: getter(Element.prototype, 'shadowRoot'), attr: method(Element.prototype, 'getAttribute'),
    style: getComputedStyle.bind(globalThis), property: method(CSSStyleDeclaration.prototype, 'getPropertyValue'),
    open: getter(HTMLDetailsElement.prototype, 'open'), walker: (doc, el) => createWalker(doc, el, 4),
    next: method(TreeWalker.prototype, 'nextNode'), range: method(Document.prototype, 'createRange'),
    select: method(Range.prototype, 'selectNodeContents'), rects: method(Range.prototype, 'getClientRects'),
    width: getter(DOMRectReadOnly.prototype, 'width'), height: getter(DOMRectReadOnly.prototype, 'height'),
    text: getter(Node.prototype, 'textContent'), rowspan: getter(HTMLTableCellElement.prototype, 'rowSpan'),
    colspan: getter(HTMLTableCellElement.prototype, 'colSpan'), contains: method(Node.prototype, 'contains'), href: getter(HTMLAnchorElement.prototype, 'href'),
  });
  const createWalker = method(Document.prototype, 'createTreeWalker');
  Object.defineProperty(globalThis, key, { value: Object.freeze((root, options) => capture(root, options, intrinsics)), writable: false, configurable: false });
}
