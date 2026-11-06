import { getLocale } from "@/i18n";
// A folder upload reaches the message as one file entry per member, each
// carrying its workspace-relative path ("uploads/<root>/<sub>/file"). The
// bubble's flat chip row throws that structure away; this rebuilds the tree
// the paths describe so the folder renders as a folder — collapsible, with a
// descendant count — while a message of loose files keeps the flat chips.

export interface AttachFile {
  /** Workspace-relative path, slash-separated ("uploads/root/a.pdf"). */
  path: string;
  /** Display name as sent. */
  name: string;
}

export interface AttachNode {
  /** Segment label shown in the tree. */
  name: string;
  /** Workspace-relative path of this node (directories included). */
  path: string;
  dir: boolean;
  /** Directories first, then files, each group alphabetical. */
  children: AttachNode[];
  /** Descendant file count; 0 on files. */
  count: number;
  /** Leaf payload, files only. */
  file?: AttachFile;
}

export interface AttachTree {
  roots: AttachNode[];
  /** True when any directory node exists — the bubble switches to the tree. */
  hasDir: boolean;
}

interface Raw {
  name: string;
  path: string;
  dir: boolean;
  kids: Map<string, Raw>;
  file?: AttachFile;
}

const byName = (a: Raw, b: Raw) => a.name.localeCompare(b.name, getLocale(), { numeric: true });

function toNode(raw: Raw): AttachNode {
  const kids = [...raw.kids.values()];
  const ordered = [...kids.filter((k) => k.dir).sort(byName), ...kids.filter((k) => !k.dir).sort(byName)];
  const children = ordered.map(toNode);
  return {
    name: raw.name,
    path: raw.path,
    dir: raw.dir,
    children,
    count: raw.dir ? children.reduce((n, c) => n + (c.dir ? c.count : 1), 0) : 0,
    file: raw.file,
  };
}

export function buildAttachTree(files: AttachFile[]): AttachTree {
  const uniq = new Map<string, AttachFile>();
  for (const f of files) if (!uniq.has(f.path)) uniq.set(f.path, f);
  const list = [...uniq.values()];
  if (!list.length) return { roots: [], hasDir: false };

  // Hide the first segment every path shares — it is the workspace's uploads/
  // inbox, not part of what the user picked. Any path too short to share it
  // keeps the segment (and nothing is hidden).
  const segs = list.map((f) => f.path.split("/"));
  const head = segs[0][0];
  const drop = segs.every((s) => s.length > 1 && s[0] === head) ? 1 : 0;

  const root: Raw = { name: "", path: "", dir: true, kids: new Map() };
  list.forEach((f, i) => {
    const parts = segs[i].slice(drop);
    let node = root;
    let acc = segs[i].slice(0, drop).join("/");
    for (let d = 0; d < parts.length - 1; d++) {
      acc = acc ? `${acc}/${parts[d]}` : parts[d];
      let next = node.kids.get(parts[d]);
      if (!next) {
        next = { name: parts[d], path: acc, dir: true, kids: new Map() };
        node.kids.set(parts[d], next);
      }
      node = next;
    }
    // Leaf keys carry the full path so a file can never collide with (or
    // merge into) a directory key.
    node.kids.set(`${f.path}`, {
      name: parts[parts.length - 1] || f.name,
      path: f.path,
      dir: false,
      kids: new Map(),
      file: f,
    });
  });

  const roots = [...root.kids.values()].map(toNode);
  // toNode sorts a node's own children but the root call sorted nothing, so
  // order the top level the same way: directories first, then files.
  roots.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name, getLocale(), { numeric: true }) : a.dir ? -1 : 1));
  return { roots, hasDir: roots.some((r) => r.dir) };
}

// The timeline hands out stable files[] references, and AgentView re-renders
// on every stream tick — memoize so a 200-file folder rebuilds once, not per
// render.
const cache = new WeakMap<AttachFile[], AttachTree>();

export function attachTree(files: AttachFile[]): AttachTree {
  let t = cache.get(files);
  if (!t) {
    t = buildAttachTree(files);
    cache.set(files, t);
  }
  return t;
}
