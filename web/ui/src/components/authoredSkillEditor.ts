import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { AuthoredSkillVersion, SkillDocumentContent, SkillDocumentUpdate, SkillPackageFiles } from "@/api/types";

/** One private Skill viewer. Scope and version changes invalidate late replies. */
export function useAuthoredSkillEditor(options: {
  scope: () => string;
  open: () => boolean;
  content: (path?: string, revision?: string) => Promise<SkillDocumentContent>;
  versions: () => Promise<{ versions: AuthoredSkillVersion[]; current_revision: string }>;
  files: (path?: string, revision?: string) => Promise<SkillPackageFiles>;
  save: (input: SkillDocumentUpdate) => Promise<SkillDocumentContent>;
  confirmLeave: () => Promise<boolean>;
  saved: (document: SkillDocumentContent) => void;
}) {
  const rootDocument = ref<SkillDocumentContent | null>(null), versions = ref<AuthoredSkillVersion[]>([]);
  const selectedRevision = ref(""), currentRevision = ref(""), loading = ref(false), error = ref("");
  let generation = 0;
  const historical = computed(() => !!selectedRevision.value && selectedRevision.value !== currentRevision.value);
  const newerVersion = computed(() => !selectedRevision.value && !!rootDocument.value?.revision && !!currentRevision.value && rootDocument.value.revision !== currentRevision.value);
  const scopeKey = computed(() => `${options.scope()}:${selectedRevision.value || "latest"}`);
  const valid = (request: number, scope: string) => generation === request && options.open() && options.scope() === scope;
  async function load() {
    if (!options.open() || !options.scope()) return;
    const request = ++generation, scope = options.scope(), rev = selectedRevision.value;
    loading.value = true; error.value = ""; rootDocument.value = null;
    try {
      const [document, listing] = await Promise.all([options.content(undefined, rev || undefined), options.versions()]);
      if (!valid(request, scope)) return;
      currentRevision.value = listing.current_revision; versions.value = listing.versions;
      rootDocument.value = { ...document, ...(rev && rev !== listing.current_revision ? { editable: false } : {}) };
    } catch (reason) { if (valid(request, scope)) error.value = reason instanceof Error ? reason.message : String(reason); }
    finally { if (valid(request, scope)) loading.value = false; }
  }
  async function selectRevision(revision: string) {
    const request = generation, scope = options.scope();
    if (selectedRevision.value === revision || !(await options.confirmLeave()) || !valid(request, scope)) return;
    selectedRevision.value = revision;
    await load();
  }
  async function loadDocument(path: string) {
    const scope = options.scope(), request = generation;
    // Reference files come from the same immutable bundle as the visible root.
    // Reloading the root explicitly asks for the latest revision instead.
    const rev = path === "SKILL.md" ? selectedRevision.value || undefined : rootDocument.value?.revision;
    const document = await options.content(path, rev);
    if (!valid(request, scope)) throw new Error("Skill scope changed");
    return historical.value ? { ...document, editable: false } : document;
  }
  async function loadFiles(path: string) {
    const request = generation, scope = options.scope();
    const listing = await options.files(path, rootDocument.value?.revision || selectedRevision.value || undefined);
    if (!valid(request, scope)) throw new Error("Skill scope changed");
    return listing;
  }
  async function saveDocument(input: SkillDocumentUpdate) {
    if (historical.value) throw new Error("Historical Skill versions are read-only");
    const request = generation, scope = options.scope();
    const document = await options.save(input);
    if (!valid(request, scope)) throw new Error("Skill scope changed");
    // Let the document editor consume the acknowledged revision before its
    // saved event replaces the root; never reset the writer while it is busy.
    return document;
  }
  function documentSaved(document: SkillDocumentContent) {
    rootDocument.value = document; currentRevision.value = document.revision || currentRevision.value;
    const request = generation, scope = options.scope();
    void options.versions().then(listing => {
      if (valid(request, scope)) { versions.value = listing.versions; currentRevision.value = listing.current_revision; }
    }).catch(() => { /* The acknowledged saved document remains usable. */ });
    options.saved(document);
  }
  function documentReloaded(document: SkillDocumentContent) {
    rootDocument.value = document;
    if (!selectedRevision.value) {
      currentRevision.value = document.revision || currentRevision.value;
      const request = generation, scope = options.scope();
      void options.versions().then(listing => {
        if (valid(request, scope)) { versions.value = listing.versions; currentRevision.value = listing.current_revision; }
      }).catch(() => {});
    }
  }
  watch([options.scope, options.open], () => {
    generation++; loading.value = false; error.value = ""; rootDocument.value = null; versions.value = [];
    selectedRevision.value = ""; currentRevision.value = "";
    void load();
  }, { immediate: true, flush: "sync" });
  onBeforeUnmount(() => { generation++; });
  return { rootDocument, versions, selectedRevision, currentRevision, loading, error, historical, newerVersion, scopeKey, load, selectRevision, loadDocument, loadFiles, saveDocument, documentSaved, documentReloaded };
}
