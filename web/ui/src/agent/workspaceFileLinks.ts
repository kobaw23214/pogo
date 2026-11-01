/** These are UI navigation hints, never a substitute for server-side path checks. */
export interface WorkspaceFileContext {
  cwd: string;
  workspace?: string;
  root?: string;
}

export interface WorkspaceFileTarget {
  path: string;
  name: string;
  /** The source named only a file, so a scoped lookup may follow an exact miss. */
  lookupByName?: true;
  lineStart?: number;
  lineEnd?: number;
}

export interface WorkspaceFileMatch {
  start: number;
  end: number;
  target: WorkspaceFileTarget;
}

const extensions = new Set((
  "txt md markdown mdx mmd mermaid rst log csv tsv json jsonl ndjson yaml yml toml ini conf cfg xml html htm css scss sass less " +
  "js jsx ts tsx mjs cjs vue svelte astro py pyi ipynb go rs java kt kts scala c h cc cpp cxx hpp cs fs fsx " +
  "rb php swift m mm sh bash zsh fish ps1 bat cmd sql graphql gql proto r jl lua pl pm ex exs erl hrl clj cljs " +
  "dart tex bib typ diff patch lock env properties gradle dockerfile gitignore gitattributes editorconfig " +
  "pdf doc docx xls xlsx xlsm xlsb ppt pptx odt ods odp rtf pages numbers key " +
  "png jpg jpeg gif webp svg ico bmp tiff tif avif heic mp3 wav ogg m4a flac mp4 webm mov avi mkv " +
  "zip tar gz tgz bz2 xz 7z rar parquet arrow feather db sqlite sqlite3 wasm"
).split(" "));
const extensionless = new Set(["Dockerfile", "Makefile", "GNUmakefile", "CMakeLists.txt", "README", "LICENSE", "COPYING", "NOTICE", "Gemfile", "Rakefile", "Procfile"]);
const controls = /[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/;
const shellSyntax = /[<>|;&`$*?{}]/;
const commandStart = /^(?:(?:sudo|env|command|exec)\s+)?(?:cd|cat|ls|rm|mv|cp|mkdir|rmdir|touch|echo|printf|find|rg|grep|sed|awk|head|tail|sort|wc|chmod|chown|curl|wget|git|docker|kubectl|python\d*|node|npm|npx|pnpm|yarn|bun|deno|go|cargo|rustc|java|javac|ruby|php|bash|sh|zsh|fish|pwsh|powershell|open|xdg-open|source)\s/;

interface PreparedContext { cwd: string; prefixes: string[]; namespaces: string[] }

function normalize(path: string): string | null {
  const result: string[] = [];
  for (const part of path.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (!result.length) return null;
      result.pop();
    } else result.push(part);
  }
  return result.join("/");
}

function absoluteRoot(raw: string): string | null {
  if (!raw.startsWith("/") || raw.startsWith("//") || controls.test(raw) || raw.includes("\\") || raw.split("/").includes("..")) return null;
  const path = normalize(raw);
  return path === null ? null : `/${path}`;
}

function prepare(context: WorkspaceFileContext): PreparedContext | null {
  const cwd = absoluteRoot(context.cwd || "");
  if (!cwd) return null;
  const prefixes: string[] = [];
  const workspace = context.workspace;
  if (workspace && !workspace.startsWith("/") && !controls.test(workspace) && !workspace.includes("\\") && !workspace.includes(":") && !workspace.split("/").includes("..")) {
    const prefix = normalize(workspace);
    if (prefix) prefixes.push(prefix);
  }
  const root = context.root ? absoluteRoot(context.root) : null;
  if (root && cwd !== root && (root === "/" || cwd.startsWith(`${root}/`))) prefixes.push(cwd.slice(root === "/" ? 1 : root.length + 1));
  // A service-relative path in a different declared workspace is not a local
  // fallback. No user ID, suffix, case folding, or filesystem search is inferred.
  const unique = [...new Set(prefixes)].sort((a, b) => b.length - a.length);
  const namespaces = unique.map(prefix => prefix.slice(0, prefix.indexOf("/") + 1)).filter(Boolean);
  return { cwd, prefixes: unique, namespaces };
}

function recognizableName(path: string): boolean {
  const name = path.slice(path.lastIndexOf("/") + 1);
  if (!name || name === "." || name === "..") return false;
  if (name.startsWith(".")) return name.length > 1;
  if (extensionless.has(name)) return true;
  const dot = name.lastIndexOf(".");
  return dot > 0 && extensions.has(name.slice(dot + 1).split("#", 1)[0]!.toLowerCase());
}

function targetFor(raw: string, context: PreparedContext, mode: "literal" | "href"): WorkspaceFileTarget | null {
  if (!raw || raw !== raw.trim() || raw.startsWith("~") || controls.test(raw) || raw.includes("\\") || raw.includes("…") || raw.includes("...") || shellSyntax.test(raw) || commandStart.test(raw)) return null;
  let path = raw;
  let lineStart: number | undefined;
  let lineEnd: number | undefined;
  if (mode === "href" && /^file:/i.test(path)) {
    // Do not let URL() normalize traversal or silently accept a foreign host.
    if (!/^file:\/\/\//i.test(path)) return null;
    path = path.slice(7);
  }
  if (path.startsWith("//")) return null;

  const line = /(#L([0-9]+)(?:-L?([0-9]+))?|:([0-9]+)(?:-([0-9]+))?)$/.exec(path);
  if (line) {
    const base = path.slice(0, line.index);
    // Only a suffix after a complete filename can mean a line locator.
    let filename = base;
    if (mode === "href") {
      try { filename = decodeURIComponent(base); } catch { return null; }
    }
    if (recognizableName(filename)) {
      lineStart = Number(line[2] || line[4]);
      lineEnd = line[3] || line[5] ? Number(line[3] || line[5]) : undefined;
      if (!Number.isSafeInteger(lineStart) || lineStart < 1 || (lineEnd !== undefined && (!Number.isSafeInteger(lineEnd) || lineEnd < lineStart))) return null;
      path = base;
    }
  }
  if (mode === "href") {
    // Fragments have URL semantics only in an actual href. A literal # remains
    // part of the filename unless it was the unambiguous line suffix above.
    if (path.includes("#") || /%(?:2f|5c)/i.test(path)) return null;
    try {
      const decoded = decodeURIComponent(path);
      const rawParts = path.split("/");
      if (decoded.split("/").some((part, index) => (part === "." || part === "..") && part !== rawParts[index])) return null;
      path = decoded;
    } catch { return null; }
  }
  if (controls.test(path) || shellSyntax.test(path) || commandStart.test(path) || path.includes("\\") || path.includes(":") || path.includes("…") || path.includes("...") || path.startsWith("//") || path.startsWith("~")) return null;
  const lastSegment = path.slice(path.lastIndexOf("/") + 1);
  if (!lastSegment || lastSegment === "." || lastSegment === "..") return null;
  // Record the source's intent before normalizing an explicit ./path or
  // stripping a declared workspace prefix down to the same basename.
  const lookupByName = !path.includes("/");

  if (path.startsWith("/")) {
    if (context.cwd === "/") path = path.slice(1);
    else if (path.startsWith(`${context.cwd}/`)) path = path.slice(context.cwd.length + 1);
    else return null;
  } else {
    // ./ is explicitly local; do not reinterpret a local directory as the
    // service's workspace-relative namespace.
    const explicitlyLocal = path.startsWith("./");
    const prefix = !explicitlyLocal && context.prefixes.find(value => path.startsWith(`${value}/`));
    if (prefix) path = path.slice(prefix.length + 1);
    else if (!explicitlyLocal && context.namespaces.some(value => path.startsWith(value))) return null;
  }
  const normalized = normalize(path);
  if (!normalized || !recognizableName(normalized)) return null;
  const target: WorkspaceFileTarget = { path: normalized, name: normalized.slice(normalized.lastIndexOf("/") + 1) };
  if (lookupByName) target.lookupByName = true;
  if (lineStart !== undefined) target.lineStart = lineStart;
  if (lineEnd !== undefined) target.lineEnd = lineEnd;
  return target;
}

export function workspaceFileTarget(raw: string, context: WorkspaceFileContext, mode: "literal" | "href" = "literal"): WorkspaceFileTarget | null {
  const prepared = prepare(context);
  return prepared ? targetFor(raw, prepared, mode) : null;
}

/** Validate the already-resolved button destination without interpreting it
 * again. A literal filename may contain #L12 or a workspace-shaped directory.
 * The caller must join this cwd-relative path to the current workspace once.
 */
export function resolvedWorkspaceFileTarget(path: string, context: WorkspaceFileContext): WorkspaceFileTarget | null {
  if (!prepare(context) || !path || path !== path.trim() || path.startsWith("/") || path.startsWith("~") ||
      controls.test(path) || shellSyntax.test(path) || commandStart.test(path) || path.includes("\\") || path.includes(":") ||
      path.includes("…") || path.includes("...") || normalize(path) !== path || !recognizableName(path)) return null;
  return { path, name: path.slice(path.lastIndexOf("/") + 1) };
}

const quotes: Record<string, string> = { '"': '"', "'": "'", "“": "”", "‘": "’" };
const separator = /[\s“”‘’"'`<>，。！？；、：]/;

/** Offsets are UTF-16 offsets, matching JS slicing and markdown-it text tokens.
 * The Markdown integration should call this only on ordinary text tokens, not
 * links, code, HTML, fences, or tool output. Inline code uses target() directly.
 */
export function workspaceFileMatches(text: string, context: WorkspaceFileContext): WorkspaceFileMatch[] {
  const prepared = prepare(context);
  if (!prepared) return [];
  const matches: WorkspaceFileMatch[] = [];
  let cursor = 0;
  while (cursor < text.length) {
    if (cursor === 0 || text[cursor - 1] === "\n") {
      const newline = text.indexOf("\n", cursor);
      const lineEnd = newline < 0 ? text.length : newline;
      const line = text.slice(cursor, lineEnd).trimStart();
      if (commandStart.test(line) || line.startsWith("$ ") || /&&|\|\||\s[|<>]\s/.test(line)) {
        cursor = lineEnd + 1;
        continue;
      }
    }
    const char = text[cursor]!;
    // Also fail safely if a caller accidentally passes raw code/HTML instead
    // of a parsed text token. Each skipped span is visited only once.
    if (char === "`" || (char === "~" && text.startsWith("~~~", cursor))) {
      let end = cursor + 1;
      while (text[end] === char) end++;
      const delimiter = text.slice(cursor, end);
      const close = text.indexOf(delimiter, end);
      cursor = close < 0 ? text.length : close + delimiter.length;
      continue;
    }
    if (char === "<") {
      const close = text.indexOf(">", cursor + 1);
      cursor = close < 0 ? text.length : close + 1;
      continue;
    }
    const quote = quotes[char];
    if (quote) {
      if (char === "'" && cursor > 0 && /[\p{L}\p{N}]/u.test(text[cursor - 1]!)) { cursor++; continue; }
      const end = text.indexOf(quote, cursor + 1);
      if (end < 0) { cursor = text.length; continue; }
      const target = targetFor(text.slice(cursor + 1, end), prepared, "literal");
      if (target) matches.push({ start: cursor + 1, end, target });
      cursor = end + 1;
      continue;
    }
    if (separator.test(char)) { cursor++; continue; }
    let end = cursor + 1;
    while (end < text.length && !separator.test(text[end]!)) end++;
    let start = cursor, trimmedEnd = end;
    while (start < trimmedEnd && "([".includes(text[start]!)) start++;
    while (trimmedEnd > start && ".,!?;:]".includes(text[trimmedEnd - 1]!)) trimmedEnd--;
    // Preserve balanced parentheses in filenames, dropping a prose wrapper.
    let balance = 0;
    for (let i = start; i < trimmedEnd; i++) if (text[i] === "(") balance++; else if (text[i] === ")") balance--;
    while (balance < 0 && text[trimmedEnd - 1] === ")") { trimmedEnd--; balance++; }
    const raw = text.slice(start, trimmedEnd);
    const target = raw.includes("/") ? targetFor(raw, prepared, "literal") : null;
    if (target) matches.push({ start, end: trimmedEnd, target });
    cursor = end;
  }
  return matches;
}
