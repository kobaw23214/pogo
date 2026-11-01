import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { SkillDocumentContent } from "@/api/types";

export type SkillDocumentLink = { kind: "document"; path: string; anchor: string } | { kind: "blocked" };

// Use package paths, never a browser URL or the server's physical skill path.
// Decode exactly once, then reject URL schemes, absolute paths and traversal
// past the package root. The endpoint independently enforces this boundary.
export function resolveSkillDocumentLink(current: string, href: string): SkillDocumentLink {
  const split = href.indexOf("#");
  let path: string, anchor: string;
  try { path = decodeURIComponent(split < 0 ? href : href.slice(0, split)); anchor = decodeURIComponent(split < 0 ? "" : href.slice(split + 1)); }
  catch { return { kind: "blocked" }; }
  if (/[\\\u0000-\u001f\u007f?]/.test(path) || path.startsWith("/") || /^[a-z][a-z\d+.-]*:/i.test(path) || /%[a-f\d]{2}/i.test(path)) return { kind: "blocked" };
  if (!path) return { kind: "document", path: current, anchor };
  const segments = current.split("/").slice(0, -1);
  for (const part of path.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") { if (!segments.length) return { kind: "blocked" }; segments.pop(); }
    else segments.push(part);
  }
  if (!segments.length) return { kind: "blocked" };
  return { kind: "document", path: segments.join("/"), anchor };
}

export function skillDocumentLanguage(doc: SkillDocumentContent | null, path: string) {
  const language = doc?.language?.toLowerCase();
  if (language) return language;
  if (doc?.media_type?.split(";")[0] === "text/markdown") return "markdown";
  const extension = path.split(".").pop()?.toLowerCase();
  return extension === "md" || extension === "markdown" ? "markdown" : extension === "json" ? "json" : "text";
}

export function useSkillDocumentNavigation(options: {
  scope: () => string;
  root: () => SkillDocumentContent;
  load: (path: string) => Promise<SkillDocumentContent>;
}) {
  const document = ref<SkillDocumentContent | null>(null);
  const location = ref({ path: "SKILL.md", anchor: "" });
  const history = ref<{ path: string; anchor: string }[]>([]);
  const loading = ref(false), error = ref(""), blocked = ref(false);
  let generation = 0;
  const rootPath = () => options.root().relative_path || "SKILL.md";
  const atRoot = computed(() => location.value.path === rootPath());
  async function load(target: { path: string; anchor: string }, force = false) {
    const current = ++generation;
    location.value = target; error.value = ""; blocked.value = false;
    if (!force && target.path === rootPath()) { loading.value = false; document.value = options.root(); return; }
    loading.value = true; document.value = null;
    try {
      const out = await options.load(target.path);
      if (current !== generation) return;
      document.value = out;
      location.value = { path: out.relative_path || target.path, anchor: target.anchor };
    } catch (err) { if (current === generation) error.value = err instanceof Error ? err.message : String(err); }
    finally { if (current === generation) loading.value = false; }
  }
  async function open(href: string) {
    const target = resolveSkillDocumentLink(location.value.path, href);
    if (target.kind === "blocked") { blocked.value = true; return; }
    blocked.value = false;
    if (target.path === location.value.path && document.value && !error.value) { location.value = { path: target.path, anchor: target.anchor }; return; }
    history.value.push({ ...location.value });
    await load({ path: target.path, anchor: target.anchor });
  }
  // The file tree already holds a package-root-relative path. Encode segments
  // before resolving so a literal # in a filename cannot turn into an anchor.
  async function openPath(path: string) {
    const target = resolveSkillDocumentLink("SKILL.md", path.split("/").map(encodeURIComponent).join("/"));
    if (target.kind === "blocked") { blocked.value = true; return; }
    if (target.path === location.value.path && document.value && !error.value) return;
    history.value.push({ ...location.value });
    await load({ path: target.path, anchor: "" });
  }
  async function back() { const previous = history.value.pop(); if (previous) await load(previous); }
  async function home() { history.value = []; await load({ path: rootPath(), anchor: "" }); }
  const retry = () => load({ ...location.value }, true);
  watch([options.scope, options.root], () => {
    generation++; history.value = []; loading.value = false; error.value = ""; blocked.value = false;
    document.value = options.root(); location.value = { path: rootPath(), anchor: "" };
  }, { immediate: true, flush: "sync" });
  onBeforeUnmount(() => { generation++; });
  return { document, location, history, loading, error, blocked, atRoot, open, openPath, back, home, retry };
}
