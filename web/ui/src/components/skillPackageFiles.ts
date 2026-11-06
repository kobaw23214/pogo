import { getLocale } from "@/i18n";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { SkillPackageEntry, SkillPackageFiles } from "@/api/types";

export interface SkillFileTreeRow {
  key: string;
  depth: number;
  directory: string;
  entry?: SkillPackageEntry;
  state?: "loading" | "error" | "truncated" | "empty";
  message?: string;
}

export function skillFileBytes(size: number) {
  return size < 1024 ? `${size} B` : size < 1048576 ? `${(size / 1024).toFixed(1)} KiB` : `${(size / 1048576).toFixed(1)} MiB`;
}

export function useSkillPackageFiles(options: { scope: () => string; load: (path: string) => Promise<SkillPackageFiles> }) {
  const directories = ref<Record<string, SkillPackageFiles>>({});
  const expanded = ref(new Set<string>(["", "references"]));
  const loading = ref(new Set<string>()), errors = ref<Record<string, string>>({});
  let generation = 0;
  const inFlight = new Map<string, Promise<SkillPackageFiles | undefined>>();
  const index = computed(() => new Map(Object.values(directories.value).flatMap(dir => dir.entries).map(entry => [entry.path, entry])));
  const rows = computed<SkillFileTreeRow[]>(() => {
    const out: SkillFileTreeRow[] = [];
    function walk(path: string, depth: number) {
      if (loading.value.has(path) && !directories.value[path]) out.push({ key: `${path}:loading`, depth, directory: path, state: "loading" });
      if (errors.value[path]) out.push({ key: `${path}:error`, depth, directory: path, state: "error", message: errors.value[path] });
      const dir = directories.value[path]; if (!dir) return;
      const sorted = [...dir.entries].sort((a, b) => Number(b.kind === "directory") - Number(a.kind === "directory") || Number(b.path === "SKILL.md") - Number(a.path === "SKILL.md") || a.name.localeCompare(b.name, getLocale()));
      for (const entry of sorted) {
        out.push({ key: entry.path, entry, depth, directory: path });
        if (entry.kind === "directory" && entry.expandable !== false && expanded.value.has(entry.path)) walk(entry.path, depth + 1);
      }
      if (!dir.entries.length) out.push({ key: `${path}:empty`, depth, directory: path, state: "empty" });
      if (dir.truncated) out.push({ key: `${path}:truncated`, depth, directory: path, state: "truncated" });
    }
    walk("", 0); return out;
  });
  async function ensure(path: string, force = false): Promise<SkillPackageFiles | undefined> {
    if (!force && directories.value[path]) return directories.value[path];
    if (inFlight.has(path)) return inFlight.get(path);
    const request = generation;
    loading.value = new Set(loading.value).add(path); delete errors.value[path];
    const task = Promise.resolve().then(async () => {
      try {
        const out = await options.load(path);
        if (request !== generation) return;
        directories.value = { ...directories.value, [path]: out };
        // Only the small references root is opened eagerly. Other directories,
        // including large runtime assets, stay untouched until expanded.
        if (path === "" && out.entries.some(entry => entry.path === "references" && entry.kind === "directory" && entry.expandable !== false)) void ensure("references");
        return out;
      } catch (err) { if (request === generation) errors.value = { ...errors.value, [path]: err instanceof Error ? err.message : String(err) }; }
      finally {
        if (request === generation) { const next = new Set(loading.value); next.delete(path); loading.value = next; inFlight.delete(path); }
      }
    });
    inFlight.set(path, task); return task;
  }
  async function toggle(entry: SkillPackageEntry) {
    if (entry.kind !== "directory" || entry.expandable === false) return;
    const next = new Set(expanded.value);
    if (next.has(entry.path)) next.delete(entry.path); else next.add(entry.path);
    expanded.value = next;
    if (next.has(entry.path)) await ensure(entry.path);
  }
  async function reveal(path: string) {
    const request = generation;
    await ensure(""); if (request !== generation) return;
    const segments = path.split("/").slice(0, -1);
    let parent = "";
    for (const segment of segments) {
      parent = parent ? `${parent}/${segment}` : segment;
      const entry = index.value.get(parent);
      if (!entry || entry.kind !== "directory" || entry.expandable === false) break;
      expanded.value = new Set(expanded.value).add(parent);
      await ensure(parent); if (request !== generation) return;
    }
    return index.value.get(path);
  }
  watch(options.scope, () => {
    generation++; directories.value = {}; expanded.value = new Set(["", "references"]); loading.value = new Set(); errors.value = {}; inFlight.clear(); void ensure("");
  }, { immediate: true, flush: "sync" });
  onBeforeUnmount(() => { generation++; inFlight.clear(); });
  return { directories, expanded, loading, errors, index, rows, ensure, toggle, reveal };
}
