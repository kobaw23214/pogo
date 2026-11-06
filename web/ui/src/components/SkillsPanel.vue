<script setup lang="ts">
import { getLocale } from "@/i18n";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { ArrowLeft, ArrowDown, Close, Search, Upload, Download } from "@element-plus/icons-vue";
import { api, skillSampleURL } from "@/api/client";
import type { SkillUploadResult } from "@/api/types";
import type { AuthoredSkillSummary, SkillContent, SkillInfo, ToolCatalogEntry } from "@/api/types";
import type { BuiltinToolEntry, McpServerGroup } from "@/api/types";
import SkillDocumentViewer from "./SkillDocumentViewer.vue";
import StudioEntrySkeleton from "./studio/StudioEntrySkeleton.vue";
import ToolRow from "./tools/ToolRow.vue";
import HiText from "./tools/HiText.vue";
import { fuzzyScore } from "@/agent/fuzzy";
import { skillUploadState } from "@/agent/skillExecution";
import { builtinToolDescription, builtinSkillDescription } from "@/agent/builtinToolCopy";
import SkillExecutionNotice from "./SkillExecutionNotice.vue";
import McpMonitorPanel from "./McpMonitorPanel.vue";
import AuthoredSkillDialog from "./AuthoredSkillDialog.vue";
import { isAuthoredSkillSummary } from "@/agent/skillAuthoringReceipt";

// The skills-and-tools panel: one dock sheet, two views of one catalog.
//
// By-skill answers "what does this bundle bring" — each skill card states where
// it lives (the path is already in the model's system prompt, so showing it
// keeps the user from knowing less than the model does) and unfolds the tools
// its tools.json declares; MCP servers sit in their own section below, same
// card pattern plus a live connection state. By-tool answers the model's
// question instead: what can be called, what it takes, what it is allowed to
// do — manifest tools first, then one group per MCP server so a broken server
// is visible as a group, not as silently missing tools. Two orders of the
// same data, which is why the switch between them is a segmented control and
// not tabs — the content is the same, the lens is not.
//
// The SKILL.md viewer is read-only, inherited from the panel this one replaced:
// a skill is a file that rewrites the system prompt, and editing it through the
// browser would be a writable path to prompt injection that no one is watching.

const emit = defineEmits<{ close: [] }>();

const props = withDefaults(
  defineProps<{
    /** Scope the catalog/content fetches to one Agent session; empty means
        the workspace-wide admin catalog (the management page's own mode). */
    sessionId?: string;
    /** Agent history includes every owned conversation, not only sessionId. */
    agentId?: string;
    active?: boolean;
    toolRevision?: number;
    /** A committed Skill/MCP projection changed, independently of native tools. */
    capabilityRevision?: number;
    toolsPending?: boolean;
    /** Hide the admin install controls; inspect/search stay. */
    readOnly?: boolean;
    /** Standalone management mode: no dock close buttons, fill the page. */
    embedded?: boolean;
  }>(),
  { sessionId: "", agentId: "", active: true, toolRevision: 0, capabilityRevision: 0, toolsPending: false, readOnly: false, embedded: false },
);

const { t, te } = useI18n();

const view = ref<"skill" | "tool" | "calls">("skill");
const skills = ref<SkillInfo[] | null>(null);
const tools = ref<ToolCatalogEntry[] | null>(null);
const builtinTools = ref<BuiltinToolEntry[]>([]);
const toolCatalogError = ref("");
let toolCatalogRequest = 0;
const mcpGroups = ref<McpServerGroup[] | null>(null);
const loadError = ref("");

// One skill's tools stay unfolded while the rest collapse: the panel is narrow,
// and two unfolded bundles at once is a wall of names neither of which is being
// read. Null means "all folded". MCP server cards fold the same way.
const expanded = ref<string | null>(null);
const expandedServer = ref<string | null>(null);
const filter = ref("");

// --- the SKILL.md viewer state (ex-listing drawer) ---
const selected = ref<SkillInfo | null>(null);
const detail = ref<SkillContent | null>(null);
const detailLoading = ref(false);
const detailError = ref("");
const cache = new Map<string, SkillContent>();
let catalogGeneration = 0;
let detailGeneration = 0;
const catalogLoading = ref(false);

const authored = ref<AuthoredSkillSummary[]>([]), authoredError = ref(""), authoredLoading = ref(false);
const authoredSelected = ref<AuthoredSkillSummary | null>(null), authoredOpen = ref(false);
let authoredGeneration = 0;
const filteredAuthored = computed(() => {
  const q = filter.value.trim();
  return authored.value.filter(skill => !q || matches(q, skill.name, skill.description));
});
async function loadAuthored() {
  const request = ++authoredGeneration, aid = props.agentId;
  if (!props.active || !aid || !props.sessionId) return;
  authoredLoading.value = true;
  try {
    const result = await api.authoredSkills(aid);
    if (request !== authoredGeneration || aid !== props.agentId || !props.active) return;
    authored.value = result.skills.filter(isAuthoredSkillSummary); authoredError.value = "";
  } catch (reason) { if (request === authoredGeneration && aid === props.agentId && props.active) authoredError.value = reason instanceof Error ? reason.message : String(reason); }
  finally { if (request === authoredGeneration) authoredLoading.value = false; }
}
function openAuthored(skill: AuthoredSkillSummary) { authoredSelected.value = skill; authoredOpen.value = true; }
function authoredChanged(event: Event) {
  const aid = (event as CustomEvent<{ agentId?: string }>).detail?.agentId;
  if (aid && aid !== props.agentId || !props.active) return;
  void loadAuthored();
}
if (typeof window !== "undefined") window.addEventListener("pogo-skills-changed", authoredChanged);

const sourceLabel = (source: string) => {
  const key = `skillsPanel.sources.${source}`;
  return te(key) ? t(key) : source;
};

async function load() {
  if (!props.active || skills.value || loadError.value || catalogLoading.value) return;
  // Agent catalogs need a real bound conversation. Never substitute the
  // workspace-wide catalog while a new Agent session is still loading.
  if (props.agentId && !props.sessionId) return;
  const generation = ++catalogGeneration;
  const toolRequest = ++toolCatalogRequest;
  catalogLoading.value = true;
  try {
    const session = props.sessionId || undefined;
    const [s, tl] = await Promise.all([api.skills(session), api.tools(session)]);
    if (generation !== catalogGeneration || !props.active) return;
    skills.value = [...s.skills];
    if (toolRequest === toolCatalogRequest) {
      tools.value = tl.tools;
      builtinTools.value = [...(tl.builtin ?? [])];
      mcpGroups.value = tl.mcp ?? [];
      toolCatalogError.value = "";
    }
    loadError.value = "";
  } catch (err) {
    if (generation === catalogGeneration) loadError.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (generation === catalogGeneration) catalogLoading.value = false;
  }
}

// Native grants do not change Skill documents. Keep their requests, selection,
// and cache alive while refreshing only the effective tool catalog.
async function refreshTools() {
  if (!props.active || props.agentId && !props.sessionId) return;
  const request = ++toolCatalogRequest, generation = catalogGeneration;
  const session = props.sessionId || undefined;
  try {
    const result = await api.tools(session);
    if (request !== toolCatalogRequest || generation !== catalogGeneration || !props.active || session !== (props.sessionId || undefined)) return;
    tools.value = result.tools;
    builtinTools.value = [...(result.builtin ?? [])];
    mcpGroups.value = result.mcp ?? [];
    toolCatalogError.value = "";
  } catch (error) {
    if (request === toolCatalogRequest && generation === catalogGeneration && props.active) toolCatalogError.value = error instanceof Error ? error.message : String(error);
  }
}

async function refreshCapabilities() {
  if (!props.active || props.agentId && !props.sessionId) return;
  const selectedName = selected.value?.name;
  catalogGeneration++;
  // A revoked document or a late read must not survive a committed projection.
  // Preserve the user's lens/filter, and reopen only a still-authorized Skill.
  back();
  cache.clear();
  catalogLoading.value = false;
  skills.value = null;
  tools.value = null;
  builtinTools.value = [];
  mcpGroups.value = null;
  loadError.value = "";
  toolCatalogError.value = "";
  const loading = load();
  // load() owns the new request generation. Compare against that generation,
  // rather than the invalidated one, before restoring the authorized detail.
  const generation = catalogGeneration;
  await loading;
  if (generation !== catalogGeneration || !props.active) return;
  const retained = (skills.value as SkillInfo[] | null)?.find(skill => skill.name === selectedName);
  if (retained) await openSkill(retained);
}

async function openSkill(skill: SkillInfo) {
  if (!props.active || (props.agentId && !props.sessionId)) return;
  const generation = ++detailGeneration;
  const catalog = catalogGeneration;
  selected.value = skill;
  detailError.value = "";
  const hit = cache.get(skill.name);
  if (hit) {
    detail.value = hit;
    detailLoading.value = false;
    return;
  }
  detailLoading.value = true;
  detail.value = null;
  try {
    const out = await api.skillContent(skill.name, props.sessionId || undefined);
    if (generation !== detailGeneration || catalog !== catalogGeneration || !props.active) return;
    cache.set(skill.name, out);
    if (selected.value?.name === skill.name) detail.value = out;
  } catch (err) {
    if (generation === detailGeneration && catalog === catalogGeneration && selected.value?.name === skill.name) {
      detailError.value = err instanceof Error ? err.message : String(err);
    }
  } finally {
    if (generation === detailGeneration && catalog === catalogGeneration && selected.value?.name === skill.name) detailLoading.value = false;
  }
}

function back() {
  detailGeneration++;
  selected.value = null;
  detail.value = null;
  detailError.value = "";
  detailLoading.value = false;
}

// The filter is shared by both views — it is the same catalog either way, and
// a person who types "yara" should not have to know which lens they are
// looking through. Matching is substring-or-fuzzy: substring so the obvious
// case always works, fuzzy so wire names survive dropped separators
// ("malsearch"). Highlighting marks only the literal run (see HiText), so a
// fuzzy-only hit shows unhighlighted — honest, not a box on the wrong chars.
function matches(q: string, ...fields: string[]): boolean {
  const needle = q.toLowerCase();
  return fields.some(
    (f) => f.toLowerCase().includes(needle) || fuzzyScore(q, f) !== null,
  );
}

const filterActive = computed(() => filter.value.trim() !== "");

const filteredBuiltinTools = computed(() => {
  const q = filter.value.trim();
  return builtinTools.value.filter(tool => !q || matches(q, tool.name, builtinToolDescription(tool.name, tool.description), tool.description, tool.skill_binding ?? ''));
});
const builtinToolGroups = computed(() => [
  { key: 'linked', title: 'studioTools.nativeLinkedTitle', tools: filteredBuiltinTools.value.filter(tool => !!tool.skill_binding) },
  { key: 'general', title: 'studioTools.nativeGeneralTitle', tools: filteredBuiltinTools.value.filter(tool => !tool.skill_binding) },
].filter(group => group.tools.length));

const filteredTools = computed(() => {
  const list = tools.value ?? [];
  const q = filter.value.trim();
  if (!q) return list;
  return list.filter((tl) =>
    matches(q, tl.name, tl.wire_name, tl.title ?? "", tl.description, tl.skill ?? ""),
  );
});

// A skill matches on its own card fields or on any tool it declares: "yara"
// finds malware-analysis through mal_yara even though the skill's own text
// never says it. A card that matched only through its tools auto-unfolds
// while the filter is on — the match would otherwise be hidden inside the
// very fold the person is trying to see past.
const toolFieldsOf = (tl: ToolCatalogEntry) => [
  tl.name,
  tl.wire_name,
  tl.title ?? "",
  tl.description,
];

const filteredSkills = computed(() => {
  const list = (skills.value ?? []).filter(skill => skill.source !== "authored").sort((a, b) => a.name.localeCompare(b.name, getLocale()));
  const q = filter.value.trim();
  if (!q) return list;
  return list.filter((sk) => {
    if (matches(q, sk.name, builtinSkillDescription(sk.name, sk.description, sk.source === "embedded"), sk.description, sk.path, sk.source)) return true;
    return (sk.tools ?? []).some((tl) => matches(q, ...toolFieldsOf(tl)));
  });
});

const skillMatchedViaTools = (sk: SkillInfo) => {
  const q = filter.value.trim();
  if (!q) return false;
  if (matches(q, sk.name, builtinSkillDescription(sk.name, sk.description, sk.source === "embedded"), sk.description, sk.path, sk.source)) return false;
  return (sk.tools ?? []).some((tl) => matches(q, ...toolFieldsOf(tl)));
};

// Manual expansion survives the filter; a tool-match adds to it rather than
// replacing it, so a card the person already unfolded stays unfolded.
const isExpanded = (sk: SkillInfo) =>
  expanded.value === sk.name || (filterActive.value && skillMatchedViaTools(sk));

// ── MCP servers ──────────────────────────────────────────────────────────
// The same catalog's second half: the pool's per-server projection. Status is
// a server-row concern only (icon + colour + text — never colour alone); the
// tools inherit it, which is why a down server greys its group instead of
// dropping it.
const stateLabel = (sv: McpServerGroup) => {
  const key = `skillsPanel.serverState.${sv.state}`;
  return te(key) ? t(key) : sv.state;
};

const mcpSummary = computed(() => {
  const list = mcpGroups.value ?? [];
  const online = list.filter((sv) => sv.state === "connected").length;
  return online === list.length
    ? t("skillsPanel.mcpSummary", { n: list.length })
    : t("skillsPanel.mcpSummaryPartial", { n: list.length, m: online });
});

const serverSelfMatch = (sv: McpServerGroup, q: string) =>
  matches(q, sv.name, sv.server_name ?? "");

// One filtered projection serves both views: the by-skill cards key off the
// server and show every tool inside (same as skill cards), the by-tool groups
// show only the matching rows — unless the server itself matched, where the
// whole group stays for context.
const filteredMcp = computed(() => {
  const list = mcpGroups.value ?? [];
  const q = filter.value.trim();
  if (!q) return list.map((server) => ({ server, tools: server.tools }));
  return list
    .filter((sv) => serverSelfMatch(sv, q) || sv.tools.some((tl) => matches(q, ...toolFieldsOf(tl))))
    .map((sv) => ({
      server: sv,
      tools: serverSelfMatch(sv, q) ? sv.tools : sv.tools.filter((tl) => matches(q, ...toolFieldsOf(tl))),
    }));
});

const serverMatchedViaTools = (sv: McpServerGroup) => {
  const q = filter.value.trim();
  if (!q || serverSelfMatch(sv, q)) return false;
  return sv.tools.some((tl) => matches(q, ...toolFieldsOf(tl)));
};

const isServerExpanded = (sv: McpServerGroup) =>
  expandedServer.value === sv.name || (filterActive.value && serverMatchedViaTools(sv));

const serverTagline = (sv: McpServerGroup) =>
  sv.state === "connected" && sv.server_name
    ? `${sv.server_name}@${sv.server_version || "?"}`
    : sv.last_error || "";

const toolCount = (sk: SkillInfo) => sk.tools?.length ?? 0;
const emptySkills = computed(() => skills.value !== null && skills.value.length === 0 && authored.value.length === 0 && !authoredLoading.value && !authoredError.value);

// A different Agent session is a different catalog: refetch and drop the old
// SKILL.md preview too, so the previous Agent's content can never linger.
watch(
  [() => props.sessionId, () => props.agentId, () => props.active],
  () => {
    catalogGeneration++;
    detailGeneration++;
    authoredGeneration++; authored.value = []; authoredError.value = ""; authoredLoading.value = false; authoredOpen.value = false; authoredSelected.value = null;
    catalogLoading.value = false;
    skills.value = null;
    tools.value = null;
    builtinTools.value = [];
    toolCatalogError.value = "";
    toolCatalogRequest++;
    mcpGroups.value = null;
    loadError.value = "";
    filter.value = "";
    expanded.value = null;
    expandedServer.value = null;
    selected.value = null;
    detail.value = null;
    detailError.value = "";
    detailLoading.value = false;
    cache.clear();
    void load();
    void loadAuthored();
  },
  { immediate: true, flush: "sync" },
);

// Refresh effective tools in place; keep the selected description and filter.
watch(() => props.toolRevision, () => { void refreshTools(); });
watch(() => props.capabilityRevision, () => { void refreshCapabilities(); }, { flush: "sync" });
onBeforeUnmount(() => { catalogGeneration++; detailGeneration++; authoredGeneration++; if (typeof window !== "undefined") window.removeEventListener("pogo-skills-changed", authoredChanged); });

// ── Upload ─────────────────────────────────────────────────────────────────
// The viewer stays read-only; installing a whole bundle from a zip is a
// different act with a different surface (structure validated server-side,
// the bundle lands under the user skills directory and goes live at once).

const uploadOpen = ref(false);
const uploadBusy = ref(false);
const uploadError = ref("");
const uploadResult = ref<SkillUploadResult | null>(null);
const uploadExecutionPending = computed(() => uploadResult.value !== null && skillUploadState(uploadResult.value) === "pending");
const skillFileInput = ref<HTMLInputElement | null>(null);

function reload() {
  catalogGeneration++;
  back();
  catalogLoading.value = false;
  skills.value = null;
  tools.value = null;
  builtinTools.value = [];
  toolCatalogError.value = "";
  toolCatalogRequest++;
  mcpGroups.value = null;
  loadError.value = "";
  cache.clear();
  void load();
  void loadAuthored();
}

defineExpose({ reload });

function pickSkillZip() {
  uploadError.value = "";
  skillFileInput.value?.click();
}

// One upload path for both entries: the picker's change event and the drop
// zone hand the same File to the same install flow. The transfer reports
// progress (bytes can be gigabytes now) and can be cancelled mid-flight.
const uploadPct = ref(0);
const uploadSpeed = ref("");
const uploadCancel = ref<AbortController | null>(null);

function fmtBytes(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + " GB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + " KB";
  return n + " B";
}

async function uploadSkillFile(file: File) {
  if (uploadBusy.value) return;
  if (!file.name.toLowerCase().endsWith(".zip")) {
    uploadError.value = t("skillsPanel.upload.notZip");
    return;
  }
  uploadBusy.value = true;
  uploadError.value = "";
  uploadPct.value = 0;
  uploadSpeed.value = "";
  const started = performance.now();
  const cancel = new AbortController();
  uploadCancel.value = cancel;
  try {
    uploadResult.value = await api.uploadSkill(file, {
      signal: cancel.signal,
      onProgress: (loaded, total) => {
        uploadPct.value = total > 0 ? Math.floor((loaded / total) * 100) : 0;
        const secs = (performance.now() - started) / 1000;
        if (secs > 0.5) uploadSpeed.value = fmtBytes(loaded / secs) + "/s";
      },
    });
    if (uploadExecutionPending.value) ElMessage.warning(t("skillsPanel.upload.pending", { name: uploadResult.value.name }));
    else ElMessage.success(t("skillsPanel.upload.done", { name: uploadResult.value.name }));
    reload();
    // The composer's /skill: completion list lives in AgentView; a window
    // event is the one channel that reaches it without threading emits
    // through the dock.
    window.dispatchEvent(new CustomEvent("pogo-skills-changed"));
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") {
      uploadError.value = t("skillsPanel.upload.cancelled");
    } else {
      uploadError.value = err instanceof Error ? err.message : String(err);
      ElMessage.error(t("skillsPanel.upload.failed", { msg: uploadError.value }));
    }
  } finally {
    uploadBusy.value = false;
    uploadCancel.value = null;
    uploadPct.value = 0;
    uploadSpeed.value = "";
  }
}

function cancelUpload() {
  uploadCancel.value?.abort();
}

async function onSkillZipFile(e: Event) {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = ""; // allows picking the same file again after a failure
  if (!file) return;
  await uploadSkillFile(file);
}

// The drop zone. dragenter/leave fire per crossed element, so a depth count
// keeps the highlight on while the cursor is anywhere inside the zone.
const dropActive = ref(false);
let dropDepth = 0;

function onDropEnter(e: DragEvent) {
  if (!e.dataTransfer?.types?.includes("Files")) return;
  e.preventDefault();
  dropDepth++;
  dropActive.value = true;
}

function onDropOver(e: DragEvent) {
  if (!e.dataTransfer?.types?.includes("Files")) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = "copy";
}

function onDropLeave() {
  dropDepth = Math.max(0, dropDepth - 1);
  if (!dropDepth) dropActive.value = false;
}

function onDropFile(e: DragEvent) {
  e.preventDefault();
  dropDepth = 0;
  dropActive.value = false;
  const file = e.dataTransfer?.files?.[0];
  if (file) void uploadSkillFile(file);
}

watch(uploadOpen, (open) => {
  if (!open) {
    dropDepth = 0;
    dropActive.value = false;
  }
});
</script>

<template>
  <section class="skills-sheet" :class="{ embedded }">
    <AuthoredSkillDialog v-if="authoredOpen && authoredSelected" v-model="authoredOpen" :agent-id="agentId" :skill="authoredSelected" @saved="loadAuthored" />
    <!-- The viewer replaces the whole panel body: a SKILL.md is long, and a
         sliver of list above it would only truncate the one thing being read. -->
    <template v-if="selected">
      <header class="head">
        <button class="ghost" :title="t('skillsPanel.back')" @click="back">
          <el-icon><ArrowLeft /></el-icon>
        </button>
        <span class="head-name">{{ selected.name }}</span>
        <button v-if="!embedded" class="ghost" :title="t('common.close')" @click="emit('close')">
          <el-icon><Close /></el-icon>
        </button>
      </header>
      <div class="body">
        <div v-if="detailLoading" role="status" :aria-label="t('skillsPanel.loading')" aria-busy="true"><StudioEntrySkeleton area="document" /></div>
        <div v-else-if="detailError" class="note bad">{{ t("skillsPanel.loadFailed") }}{{ detailError }} <button class="ghost" type="button" @click="openSkill(selected)">{{ t("common.retry") }}</button></div>
        <SkillDocumentViewer v-else-if="detail" :key="`${sessionId}:${selected.name}`" :scope-key="`${sessionId}:${selected.name}`" :root-document="detail" :load-document="path => api.skillContent(selected!.name, sessionId || undefined, path)" />
      </div>
    </template>

    <template v-else>
      <header class="head">
        <!-- Segmented, FilesPanel's tab shape: same content, two lenses. -->
        <div class="tabs" role="tablist" :aria-label="t('skillsPanel.views')">
          <button class="tab" role="tab" :aria-selected="view === 'skill'" :class="{ on: view === 'skill' }" data-testid="skills-introduction-tab" @click="view = 'skill'">
            {{ t("skillsPanel.viewBySkill") }}
          </button>
          <button class="tab" role="tab" :aria-selected="view === 'tool'" :class="{ on: view === 'tool' }" data-testid="tools-introduction-tab" @click="view = 'tool'">
            {{ t("skillsPanel.viewByTool") }}
          </button>
          <button v-if="agentId" class="tab" role="tab" :aria-selected="view === 'calls'" :class="{ on: view === 'calls' }" data-testid="skills-mcp-history-tab" @click="view = 'calls'">
            {{ t("skillsPanel.callHistory") }}
          </button>
        </div>
        <!-- In the manage page the upload is the section's one affirmative
             action, so it wears the Studio's solid ink — a ghost icon next to
             the tabs is where it used to hide. -->
        <button
          v-if="!readOnly"
          :class="embedded ? 'upload-labeled' : 'ghost'"
          :title="t('skillsPanel.upload.button')"
          data-testid="skill-upload-open"
          @click="uploadOpen = true"
        >
          <el-icon><Upload /></el-icon>
          <span v-if="embedded">{{ t('skillsPanel.upload.manageButton') }}</span>
        </button>
        <button v-if="!embedded" class="ghost" :title="t('common.close')" @click="emit('close')">
          <el-icon><Close /></el-icon>
        </button>
      </header>

      <div v-if="view === 'calls'" class="body calls-body">
        <McpMonitorPanel :active="active" scope="agent" :agent-id="agentId" compact />
      </div>
      <div v-else class="body">
        <p v-if="toolsPending" class="tool-policy-pending" role="status" data-testid="tools-catalog-pending">{{ t('studioTools.pending') }}</p>
        <div v-if="agentId && !sessionId" class="note">{{ t("skillsPanel.sessionRequired") }}</div>
        <div v-else-if="loadError" class="note bad">{{ t("skillsPanel.loadFailed") }}{{ loadError }} <el-button size="small" text @click="reload">{{ t("mcpMonitor.retry") }}</el-button></div>
        <div v-else-if="skills === null" role="status" :aria-label="t('skillsPanel.loading')" aria-busy="true"><StudioEntrySkeleton area="sessions" /></div>
        <div v-else-if="emptySkills && !filteredMcp.length && (view !== 'tool' || !builtinTools.length && !tools?.length)" class="note">
          {{ view === "tool"
            ? t(sessionId ? "skillsPanel.toolsEmptySession" : "skillsPanel.toolsEmpty")
            : t(sessionId ? "skillsPanel.selectionEmpty" : embedded ? "skillsPanel.libraryEmpty" : "skillsPanel.empty") }}
        </div>

        <template v-else>
          <!-- One filter for both lenses: the catalog is the same data, and
               "yara" should find the tool whichever view is showing. Hits are
               highlighted in place (HiText) in both views. -->
          <div class="filter-row">
            <el-icon class="filter-icon"><Search /></el-icon>
            <input
              v-model="filter"
              class="filter"
              type="text"
              :placeholder="t('skillsPanel.filterPlaceholder')"
            />
          </div>

          <!-- By skill: where it lives, what it declares. -->
          <template v-if="view === 'skill'">
            <section v-if="agentId" class="authored-skills" data-testid="authored-skills-list">
              <div class="section-head"><span>{{ t('skillAuthoring.library') }}</span><span class="section-note">{{ t('skillAuthoring.libraryHint') }}</span></div>
              <div v-if="authoredError" class="note bad" role="alert">{{ t('skillAuthoring.libraryFailed') }} <el-button text size="small" @click="loadAuthored">{{ t('skillAuthoring.retry') }}</el-button></div>
              <div v-else-if="authoredLoading && !authored.length" role="status" :aria-label="t('skillAuthoring.loading')" aria-busy="true"><StudioEntrySkeleton area="sessions" /></div>
              <p v-else-if="!authored.length" class="note authored-empty">{{ t('skillAuthoring.empty') }}</p>
              <button v-for="item in filteredAuthored" :key="item.id" type="button" class="authored-skill-card" data-testid="authored-skill-entry" @click="openAuthored(item)"><span><strong><HiText :text="item.name" :q="filter" /></strong><span class="tag">v{{ item.version }}</span><span v-if="!item.enabled" class="tag">{{ t('skillAuthoring.draft') }}</span></span><p><HiText :text="item.description" :q="filter" /></p></button>
            </section>
            <div v-if="filterActive && !authoredLoading && !filteredSkills.length && !filteredAuthored.length" class="note">{{ t("skillsPanel.noSkillMatch") }}</div>
            <div v-for="sk in filteredSkills" :key="sk.name" class="skill-card">
              <div class="skill-head">
                <button class="skill-name" :title="t('skillsPanel.openSkill')" @click="openSkill(sk)">
                  <HiText :text="sk.name" :q="filter" />
                  <span v-if="sk.manual_only" class="tag">{{ t("skillsPanel.manualOnly") }}</span>
                </button>
                <!-- Unfolding is a second control next to the name, not the name's
                     own click: the name opens SKILL.md, the chevron unfolds the
                     tools — two destinations, two affordances. While the filter
                     is on, a card that matched through its tools unfolds itself:
                     the match is inside the fold. -->
                <button
                  v-if="toolCount(sk)"
                  class="fold"
                  :class="{ open: isExpanded(sk) }"
                  :title="t('skillsPanel.toolCount', { n: toolCount(sk) })"
                  @click="expanded = expanded === sk.name ? null : sk.name"
                >
                  {{ t("skillsPanel.toolCount", { n: toolCount(sk) }) }}
                  <el-icon class="caret"><ArrowDown /></el-icon>
                </button>
              </div>
              <div class="skill-desc"><HiText :text="builtinSkillDescription(sk.name, sk.description, sk.source === 'embedded')" :q="filter" /></div>
              <div class="skill-source" :title="sk.path">
                <HiText :text="sourceLabel(sk.source) + ' · ' + sk.path" :q="filter" />
              </div>
              <div v-if="isExpanded(sk)" class="tool-list">
                <ToolRow v-for="tl in sk.tools" :key="tl.wire_name" :tool="tl" :q="filter" />
              </div>
            </div>

            <!-- MCP servers wear the same card pattern, plus the one thing a
                 skill never has: a live connection state. -->
            <template v-if="filteredMcp.length">
              <div class="section-head">
                <span>{{ t("skillsPanel.mcpSection") }}</span>
                <span class="section-note">{{ mcpSummary }}</span>
              </div>
              <div v-for="sv in filteredMcp" :key="sv.server.name" class="skill-card mcp-card" :class="{ offline: sv.server.state !== 'connected' }">
                <div class="skill-head">
                  <span class="server-name"><HiText :text="sv.server.name" :q="filter" /></span>
                  <span class="tag state-tag" :class="sv.server.state"><i class="s-dot"></i>{{ stateLabel(sv.server) }}</span>
                  <button
                    v-if="sv.server.tools.length"
                    class="fold"
                    :class="{ open: isServerExpanded(sv.server) }"
                    @click="expandedServer = expandedServer === sv.server.name ? null : sv.server.name"
                  >
                    {{ t("skillsPanel.toolCount", { n: sv.server.tools.length }) }}
                    <el-icon class="caret"><ArrowDown /></el-icon>
                  </button>
                </div>
                <div v-if="serverTagline(sv.server)" class="skill-desc">{{ serverTagline(sv.server) }}</div>
                <div class="skill-source">
                  MCP<template v-if="sv.server.catalog_stale && sv.server.tools.length"> · {{ t("skillsPanel.staleCatalog") }}</template>
                </div>
                <div v-if="isServerExpanded(sv.server)" class="tool-list">
                  <ToolRow v-for="tl in sv.server.tools" :key="tl.wire_name" :tool="tl" :q="filter" />
                </div>
              </div>
            </template>
          </template>

          <!-- By tool: the model's view — callable, what it takes, what it may
               do. Manifest tools flat, then one group per MCP server: a broken
               server shows up as a labelled group, not as missing rows. -->
          <template v-else>
            <p v-if="toolCatalogError" class="note bad" role="status" data-testid="native-tools-error">{{ t('studioTools.catalogFailed') }} <button type="button" class="ghost" @click="refreshTools">{{ t('studioTools.retry') }}</button></p>
            <div v-if="!filteredBuiltinTools.length && !filteredTools.length && !filteredMcp.length" class="note">
              {{ filterActive ? t("skillsPanel.noToolMatch") : t(sessionId ? "skillsPanel.toolsEmptySession" : "skillsPanel.toolsEmpty") }}
            </div>

            <section v-for="group in builtinToolGroups" :key="group.key" class="native-tools" data-testid="native-tools-list" :data-native-group="group.key">
              <div class="section-head"><span>{{ t(group.title) }}</span><span class="section-note">{{ t(toolCatalogError ? 'studioTools.lastConfirmedHint' : sessionId ? 'studioTools.effectiveHint' : 'studioTools.catalogHint') }}</span></div>
              <div v-for="tool in group.tools" :key="tool.name" class="native-tool-row" data-testid="native-tool-row" :data-tool-name="tool.name">
                <div><strong><HiText :text="tool.name" :q="filter" /></strong><span v-if="tool.skill_binding" class="tag">{{ t('studioTools.linkedSkill', { skill: tool.skill_binding }) }}</span><span v-if="tool.protected" class="tag">{{ t('studioTools.protected') }}</span><span v-if="tool.read_only" class="tag">{{ t('studioTools.readOnly') }}</span></div>
                <p><HiText :text="builtinToolDescription(tool.name, tool.description)" :q="filter" /></p>
              </div>
            </section>

            <template v-if="filteredTools.length">
              <div v-if="filteredMcp.length || filteredBuiltinTools.length" class="section-head">
                <span>{{ t("skillsPanel.skillToolsSection") }}</span>
              </div>
              <ToolRow v-for="tl in filteredTools" :key="tl.wire_name" :tool="tl" :q="filter" show-skill />
            </template>

            <div v-for="grp in filteredMcp" :key="grp.server.name" class="mcp-group" :class="{ offline: grp.server.state !== 'connected' }">
              <div class="mcp-group-head">
                <i class="s-dot" :class="grp.server.state"></i>
                <span class="gname"><HiText :text="grp.server.name" :q="filter" /></span>
                <span class="gstate">{{ stateLabel(grp.server) }}</span>
                <span class="gcount">{{ t("skillsPanel.toolCount", { n: grp.server.tools.length }) }}</span>
              </div>
              <div v-if="grp.server.state !== 'connected' && grp.server.last_error" class="mcp-error">{{ grp.server.last_error }}</div>
              <div v-else-if="grp.server.catalog_stale && grp.server.tools.length" class="mcp-stale">{{ t("skillsPanel.staleCatalog") }}</div>
              <ToolRow v-for="tl in grp.tools" :key="tl.wire_name" :tool="tl" :q="filter" />
              <div v-if="!grp.tools.length" class="note">
                {{ grp.server.state === "connected" ? t("skillsPanel.serverNoTools") : t("skillsPanel.pendingCatalog") }}
              </div>
            </div>
          </template>
        </template>
      </div>
    </template>

    <!-- The structure doc doubles as the format contract: what the server's
         validator accepts is what this list says, because both come from the
         same loader. The sample zip is generated server-side for the same
         reason. -->
    <el-dialog
      v-if="!readOnly"
      v-model="uploadOpen"
      :title="t('skillsPanel.upload.title')"
      width="560px"
      append-to-body
      class="skill-upload-dialog"
    >
      <div class="su-body">
        <div class="su-structure">
          <p class="su-structure-title">{{ t("skillsPanel.upload.structureTitle") }}</p>
          <pre class="su-tree">{{ t("skillsPanel.upload.structureTree") }}</pre>
          <ul class="su-notes">
            <li>{{ t("skillsPanel.upload.noteRoot") }}</li>
            <li>{{ t("skillsPanel.upload.noteFrontmatter") }}</li>
            <li>{{ t("skillsPanel.upload.noteFiles") }}</li>
            <li>{{ t(embedded ? "skillsPanel.upload.noteManage" : "skillsPanel.upload.noteReplace") }}</li>
          </ul>
        </div>

        <!-- The drop zone is the dialog's front door: drag a bundle onto it,
             or click it to open the same picker the button below uses. -->
        <div
          class="su-drop"
          :class="{ active: dropActive, busy: uploadBusy }"
          data-testid="skill-upload-drop"
          role="button"
          tabindex="0"
          :aria-label="t('skillsPanel.upload.dropMain')"
          @dragenter="onDropEnter"
          @dragover="onDropOver"
          @dragleave="onDropLeave"
          @drop="onDropFile"
          @click="pickSkillZip"
          @keydown.enter.prevent="pickSkillZip"
        >
          <el-icon class="su-drop-ico" aria-hidden="true"><Upload /></el-icon>
          <p class="su-drop-main">{{ t("skillsPanel.upload.dropMain") }}</p>
          <p class="su-drop-sub">{{ t("skillsPanel.upload.dropSub") }}</p>
        </div>

        <div class="su-actions">
          <a class="su-sample" :href="skillSampleURL()" download>
            <el-icon><Download /></el-icon>
            {{ t("skillsPanel.upload.sample") }}
          </a>
          <input
            ref="skillFileInput"
            class="su-file"
            type="file"
            accept=".zip"
            data-testid="skill-upload-file"
            @change="onSkillZipFile"
          />
          <el-button type="primary" :loading="uploadBusy" data-testid="skill-upload-pick" @click="pickSkillZip">
            {{ t("skillsPanel.upload.pick") }}
          </el-button>
        </div>

        <!-- The transfer itself: percent and speed for the long haul, one
             cancel that aborts the XHR and frees both sides. -->
        <div v-if="uploadBusy" class="su-progress" data-testid="skill-upload-progress">
          <div class="su-progress-bar" :style="{ width: uploadPct + '%' }" />
          <span class="su-progress-text">{{ uploadPct }}%<template v-if="uploadSpeed"> · {{ uploadSpeed }}</template></span>
          <el-button size="small" data-testid="skill-upload-cancel" @click="cancelUpload">
            {{ t("skillsPanel.upload.cancelUpload") }}
          </el-button>
        </div>

        <div v-if="uploadError" class="su-error">{{ t("skillsPanel.upload.failed", { msg: uploadError }) }}</div>
        <div v-else-if="uploadResult" class="su-done" :class="{ pending: uploadExecutionPending }">
          {{ t(uploadExecutionPending ? "skillsPanel.upload.pending" : "skillsPanel.upload.installed", { name: uploadResult.name }) }}
          <p v-if="uploadExecutionPending">{{ t("skillsPanel.upload.draftHint") }}</p>
          <span v-if="uploadResult.path" class="su-path">{{ uploadResult.path }}</span>
          <SkillExecutionNotice :execution="uploadResult.execution" :issues="uploadResult.issues" />
          <ul v-if="uploadResult.warnings?.length" class="su-warnings">
            <li v-for="(wn, i) in uploadResult.warnings" :key="i">{{ wn }}</li>
          </ul>
        </div>
      </div>
    </el-dialog>
  </section>
</template>

<style scoped lang="scss">
.skills-sheet {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--el-bg-color);
}
.body.calls-body { overflow: hidden; padding: 10px; }
.tabs { overflow-x: auto; scrollbar-width: thin; }
.tab { white-space: nowrap; }

/* Standalone management mode: the page canvas sits behind the cards, the
   dock's close chrome is gone, and the body gets room to breathe. */
.skills-sheet.embedded {
  background: var(--el-bg-color-page);
}

.skills-sheet.embedded .body {
  padding: 14px 20px 24px;
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  flex: 0 0 auto;
  min-width: 0;
}

.head-name {
  flex: 1 1 0;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tabs {
  display: flex;
  gap: 2px;
  flex: 1 1 0;
  min-width: 0;
}

.upload-labeled {
  display: inline-flex;
  width: auto;
  min-width: max-content;
  align-items: center;
  gap: 6px;
  padding: 7px 13px;
  border: 1px solid var(--pg-solid);
  border-radius: 8px;
  background: var(--pg-solid);
  color: var(--pg-on-solid);
  font: inherit;
  font-size: 12.5px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: var(--el-box-shadow-lighter);
  transition: background var(--pg-transition);

  &:hover {
    background: var(--pg-solid-hover);
    border-color: var(--pg-solid-hover);
  }

  .el-icon {
    font-size: 13.5px;
  }
}

.tab {
  border: 0;
  background: transparent;
  padding: 5px 10px;
  border-radius: 7px;
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition:
    background var(--pg-transition),
    color var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }

  &.on {
    background: var(--el-fill-color-dark);
    color: var(--el-text-color-primary);
    font-weight: 600;
  }
}

.body {
  flex: 1 1 0;
  min-height: 0;
  overflow-y: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.note {
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}

.skill-card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-bg-color);
}

.skill-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.skill-name {
  flex: 1 1 0;
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 0;
  background: transparent;
  padding: 0;
  font: inherit;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-color-primary-dark-2);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;

  &:hover {
    text-decoration: underline;
  }
}

.tag {
  flex: 0 0 auto;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--el-fill-color-dark);
  color: var(--el-text-color-secondary);
  font-size: 10.5px;
  font-weight: 400;
}

.fold {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  border: 0;
  background: transparent;
  padding: 2px 6px;
  border-radius: 6px;
  font: inherit;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }

  .caret {
    font-size: 11px;
    transition: transform 0.15s ease;
  }

  &.open .caret {
    transform: rotate(180deg);
  }
}

.skill-desc {
  font-size: 12px;
  color: var(--el-text-color-regular);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.skill-source {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tool-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 4px;
}

/* ── MCP sections ─────────────────────────────────────────────────────── */

/* Slim group label between the two provenances; the note keeps quiet when
   everything is online (a state that needs no action takes no attention). */
.section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin: 10px 2px -2px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  color: var(--el-text-color-secondary);
}

.section-note {
  font-weight: 400;
  letter-spacing: 0;
}

.mcp-card.offline {
  opacity: 0.72;
}

.server-name {
  flex: 1 1 0;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Status is icon + colour + text, never colour alone. */
.state-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;

  .s-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: currentColor;
  }

  &.connected {
    background: var(--el-color-success-light-9);
    color: var(--el-color-success);
  }

  &.connecting {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning);

    .s-dot {
      animation: mcp-pulse 1.6s ease-in-out infinite;
    }
  }

  &.disconnected {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }
}

@keyframes mcp-pulse {
  50% {
    opacity: 0.3;
  }
}

.mcp-group {
  display: flex;
  flex-direction: column;
  gap: 6px;

  &.offline .tool-card {
    opacity: 0.72;
  }
}

.mcp-group-head {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 6px 2px 0;
  font-size: 12px;
  min-width: 0;

  .s-dot {
    flex: 0 0 auto;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--el-color-success);

    &.connecting {
      background: var(--el-color-warning);
      animation: mcp-pulse 1.6s ease-in-out infinite;
    }

    &.disconnected {
      background: var(--el-color-danger);
    }
  }

  .gname {
    font-weight: 600;
    color: var(--el-text-color-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .gstate {
    color: var(--el-text-color-secondary);
  }

  .gcount {
    margin-left: auto;
    flex: 0 0 auto;
    font-size: 11px;
    color: var(--el-text-color-placeholder);
  }
}

.mcp-group.offline .mcp-group-head .gname {
  color: var(--el-text-color-secondary);
}

.mcp-error,
.mcp-stale {
  margin: 0 2px;
  font-size: 11px;
  line-height: 1.6;
  word-break: break-all;
}

.mcp-error {
  color: var(--el-color-danger);
}

.mcp-stale {
  color: var(--el-text-color-secondary);
}

.filter-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 7px;
  background: var(--el-fill-color-blank);
}

.filter-icon {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.filter {
  flex: 1 1 0;
  min-width: 0;
  border: 0;
  background: transparent;
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-primary);
  outline: none;

  &::placeholder {
    color: var(--el-text-color-placeholder);
  }
}

.ghost {
  display: inline-flex;
  border: 0;
  background: transparent;
  padding: 5px;
  border-radius: 7px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition:
    background var(--pg-transition),
    color var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }
}
</style>

<style lang="scss">
/* Unscoped: the dialog teleports to body (append-to-body), so scoped rules
   cannot reach it. Prefixed classes keep the blast radius stated. */
.skill-upload-dialog {
  .su-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
    font-size: 12.5px;
  }

  .su-structure-title {
    margin: 0 0 6px;
    font-weight: 600;
    font-size: 12.5px;
    color: var(--el-text-color-primary);
  }

  .su-tree {
    margin: 0 0 8px;
    padding: 10px 12px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-fill-color-light);
    font-family: var(--pg-mono, monospace);
    font-size: 11.5px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
    overflow-x: auto;
  }

  .su-notes {
    margin: 0;
    padding-left: 18px;
    color: var(--el-text-color-secondary);
    font-size: 11.5px;
    line-height: 1.7;
  }

  .su-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .su-drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 22px 16px;
    border: 1.5px dashed var(--el-border-color);
    border-radius: 10px;
    background: var(--el-fill-color-lighter);
    cursor: pointer;
    text-align: center;
    transition:
      border-color var(--pg-transition),
      background var(--pg-transition),
      box-shadow var(--pg-transition);
  }

  .su-drop:hover,
  .su-drop.active {
    border-color: var(--el-color-primary);
    background: var(--pg-accent-wash);
  }

  .su-drop.active {
    box-shadow: var(--pg-ring);
  }

  .su-drop.busy {
    cursor: wait;
    opacity: 0.7;
  }

  .su-drop-ico {
    font-size: 26px;
    color: var(--el-text-color-secondary);
    margin-bottom: 4px;
  }

  .su-drop:hover .su-drop-ico,
  .su-drop.active .su-drop-ico {
    color: var(--el-color-primary);
  }

  .su-drop-main {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .su-drop-sub {
    margin: 0;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .su-progress {
    display: flex;
    align-items: center;
    gap: 10px;
    position: relative;
    height: 28px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    padding-right: 6px;
  }

  .su-progress-bar {
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    border-radius: 999px;
    background: var(--pg-accent-wash);
    box-shadow: inset 0 0 0 1px var(--pg-accent-line);
    transition: width 0.2s ease;
    min-width: 0;
  }

  .su-progress-text {
    position: relative;
    flex: 1;
    text-align: center;
    font-size: 12px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);
    pointer-events: none;
  }

  .su-sample {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--el-color-primary);
    text-decoration: none;

    &:hover {
      text-decoration: underline;
    }
  }

  .su-file {
    display: none;
  }

  .su-error {
    color: var(--el-color-danger);
    font-size: 12px;
    word-break: break-all;
  }

  .su-done {
    color: var(--el-text-color-regular);
    font-size: 12px;

    &.pending {
      color: var(--el-color-warning);
    }

    .su-path {
      display: block;
      margin-top: 2px;
      color: var(--el-text-color-secondary);
      font-family: var(--pg-mono, monospace);
      font-size: 11px;
      word-break: break-all;
    }
  }

  .su-warnings {
    margin: 6px 0 0;
    padding-left: 16px;
    color: var(--el-text-color-secondary);
    font-size: 11px;
  }
}
</style>

<style scoped>
.tool-policy-pending{margin:0 0 14px;padding:9px 11px;border-radius:8px;background:var(--el-color-primary-light-9);color:var(--el-color-primary);font-size:12px;line-height:1.6}.native-tools{margin-bottom:20px}.native-tool-row{padding:11px 0;border-bottom:1px solid var(--el-border-color-lighter)}.native-tool-row>div{display:flex;align-items:center;flex-wrap:wrap;gap:7px}.native-tool-row strong{font-size:12px;font-weight:550}.native-tool-row p{margin:5px 0 0;color:var(--el-text-color-secondary);font-size:12px;line-height:1.65;overflow-wrap:anywhere}
</style>

<style scoped>
.authored-skills{margin-bottom:20px}.authored-skill-card{display:block;width:100%;text-align:left;padding:13px 15px;border:1px solid var(--el-border-color-lighter);border-radius:10px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;cursor:pointer;margin-bottom:8px}.authored-skill-card:hover{border-color:var(--el-color-primary);background:var(--el-fill-color-light)}.authored-skill-card:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.authored-skill-card>span{display:flex;gap:8px;align-items:center;flex-wrap:wrap}.authored-skill-card strong{font-size:14px;overflow-wrap:anywhere}.authored-skill-card p{font-size:12px;line-height:1.7;color:var(--el-text-color-secondary);margin:6px 0 0;overflow-wrap:anywhere}.authored-empty{font-size:12px;line-height:1.8;padding:10px 0!important}
</style>
