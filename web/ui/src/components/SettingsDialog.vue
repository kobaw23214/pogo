<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import { ArrowDown, ArrowRight, Delete, FullScreen, Plus, ScaleToOriginal, Search, Setting } from "@element-plus/icons-vue";
import VueJsonPretty from "vue-json-pretty";
import "vue-json-pretty/lib/styles.css";
import { api } from "@/api/client";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import type {
  AttachmentPolicy,
  ConfigProbeResult,
  ConfigProviderInfo,
  ModelFileEntry,
  MCPInfo,
  MCPProbeResult,
  MCPServeCatalog,
  MCPServeConfig,
  MCPServerConfig,
  MCPServerStatus,
  Prefs,
  SandboxInfo,
  SweepInfo,
  VisionProbePreview,
  VisionProbeResult,
} from "@/api/types";
import { highlightLines } from "./highlight";
import { buildCurlPreview } from "./mcpCurl";
import SettingsContainer from "./SettingsContainer.vue";
import McpExportDialog from "./McpExportDialog.vue";
import McpLogsPanel from "./McpLogsPanel.vue";
import IconLibrarySettings from "./IconLibrarySettings.vue";
import SpillBrowser from "./SpillBrowser.vue";
import ModelMetadataProbe from "./ModelMetadataProbe.vue";
const iconLibraryDirty = ref(false);
import McpMonitorPanel from "./McpMonitorPanel.vue";
import {
  navShowsSheet,
  normalizeEmbeddedSection,
  sheetForSection,
  type SettingsSection,
  type SettingsSheet,
} from "./settingsSections";
import {
  buildServeConfig,
  rowFromServeConfig,
  serveStatusParts,
  validateServeRow,
  type MCPServeRow,
} from "./mcpServe";
import {
  serveChecksFromInclude,
  serveIncludeFromChecks,
  servePickerModel,
  type ServePickerGroup,
  type ServePickerTool,
} from "./mcpServePicker";
import {
  MCP_ENV_NAME_RE,
  MCP_NAME_RE,
  MCP_TOOL_PATTERN_RE,
  parseMCPImport,
  type MCPImportEntry,
  type MCPImportResult,
} from "./mcpImport";

// The settings dialog: model endpoint configuration (providers.json) edited in
// place and applied live. Two modes over the same file —
//
//   表单: the common fields (base URL, which variable holds the key, the model
//         list, the default), plus a key-value input per provider that writes
//         the server's process memory so the change is effective immediately;
//   JSON: the raw file, parsed with the same strict decoder a restart uses.
//
// What lands on disk never contains a credential (see the server's config
// package for the line this draws); the values typed into the key inputs go
// only into the request, and from there into the environment.
//
// Each provider row also carries a curl preview: the minimal request the wire
// client would send, so a changed configuration can be checked by hand before
// it is trusted to a session.

const props = defineProps<{ modelValue: boolean; embedded?: boolean; section?: SettingsSection }>();
const emit = defineEmits<{
  "update:modelValue": [boolean];
  saved: [];
  // A cross-module link (skill monitor → MCP trace) needs the Studio sidebar
  // to switch modules: the parent answers by updating `section`.
  "navigate-section": [SettingsSection];
}>();

const { t } = useI18n();

const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit("update:modelValue", v),
});

// The dialog is a split layout, GitHub-settings shaped. Two sheets: the model
// service (providers, models and the raw file are three faces of one
// providers.json, so they live together in one scrolling sheet — the nav
// entries under that group are anchors into it, not page switches) and
// storage, which is a separate store. Anchors highlight by scroll position,
// the way a GitHub settings page's sections relate to its sidebar.
//
// Embedded (Studio module) mode reuses the same sheets one module at a time:
// the shell's sidebar owns the module level, this nav shows only the active
// module's subnavigation (see settingsSections.ts for the shared map).
const sheet = ref<SettingsSheet>("service");
const activeSection = computed<SettingsSection | null>(() =>
  normalizeEmbeddedSection(props.embedded, props.section),
);
function navShows(s: SettingsSheet): boolean {
  return navShowsSheet(s, activeSection.value);
}
const mcpMonitorTrace = ref("");
const anchors = ["providers", "models", "config"] as const;
type Anchor = (typeof anchors)[number];
const anchor = ref<Anchor>("providers");
const paneEl = ref<HTMLElement | null>(null);

function goAnchor(a: Anchor) {
  sheet.value = "service";
  // Light the clicked entry now; the smooth scroll's scrollspy events converge
  // on the same anchor at the destination.
  anchor.value = a;
  // The form views must not show a world the raw editor already changed.
  if (a !== "config" && lastEdited.value === "raw") rowsFromRaw();
  void nextTick(() => {
    paneEl.value
      ?.querySelector(`[data-anchor="${a}"]`)
      ?.scrollIntoView({ behavior: "smooth", block: "start" });
  });
}

// Scrollspy: the last section whose top passed the fold is the current one.
// Called on the pane's scroll; a switch to the storage sheet leaves the last
// anchor alone so switching back restores it.
function onPaneScroll() {
  const pane = paneEl.value;
  if (!pane || sheet.value !== "service") return;
  // Bottom pinned: the last section can be shorter than the pane and never
  // reach the fold — hitting the floor means it.
  if (pane.scrollTop + pane.clientHeight >= pane.scrollHeight - 2) {
    anchor.value = anchors[anchors.length - 1];
    return;
  }
  // Rects, not offsetTop: offsetTop is measured from the nearest positioned
  // ancestor (the el-dialog, not this pane), and that fixed offset exceeds
  // the 48px fold — the old offsetTop math could never light a section the
  // scroll landed exactly on.
  const paneTop = pane.getBoundingClientRect().top;
  let current: Anchor = anchors[0];
  for (const a of anchors) {
    const el = pane.querySelector(`[data-anchor="${a}"]`) as HTMLElement | null;
    if (el && el.getBoundingClientRect().top - paneTop <= 48) current = a;
  }
  anchor.value = current;
}

// Dirty state. The three config pages edit one file, so one flag covers them
// and the dot shows on each — honest everywhere it appears, the file really
// does have unsaved changes. Storage is a separate store with its own flag;
// both feed the close guard, the dialog-shaped equivalent of a beforeunload.
const snapshotFormJSON = ref("");
const snapshotRaw = ref("");
const snapshotDays = ref(0);

function currentConfigJSON(): string {
  try {
    return JSON.stringify({ file: buildFile(), env: buildEnv() });
  } catch {
    // An extra body still being typed does not parse. That is dirty by
    // definition — and never equal to a snapshot of a saved file, which is
    // all this string is ever compared against.
    return "\u0000unparsed";
  }
}
const configDirty = computed(
  () => currentConfigJSON() !== snapshotFormJSON.value || rawText.value !== snapshotRaw.value,
);

// Providers, models and the raw file are three views of one providers.json in
// one sheet, so their edits need a referee: whoever was edited last is what a
// save sends, and the other view says it is behind instead of pretending.
// null means clean — save then sends the form, which equals the disk anyway.
const lastEdited = ref<"form" | "raw" | null>(null);

// Programmatic rebuilds (load, save, cross-view sync) must not trip the
// edit watchers; applySync runs them with the flag up and lowers it only
// after the watchers have flushed.
let syncing = false;
async function applySync(fn: () => void) {
  syncing = true;
  fn();
  await nextTick();
  syncing = false;
}

const storageDirty = computed(
  () => !!storage.value && (storage.value.days !== snapshotDays.value || storage.value.turns !== snapshotTurns.value || storage.value.turnsLimited !== snapshotTurnsLimited.value),
);
const rawBehind = computed(() => lastEdited.value === "form");

// The raw JSON editor can take over the whole viewport via the button in its
// top-right corner. Esc leaves that fullscreen without closing the dialog —
// the keydown is stopped only while fullscreen is on, so the dialog's own
// esc-to-close keeps working during ordinary inline editing.
const rawFullscreen = ref(false);
function onRawKeydown(e: KeyboardEvent) {
  if (e.key === "Escape" && rawFullscreen.value) {
    e.stopPropagation();
    rawFullscreen.value = false;
  }
}

// The raw file has two faces: a read-only pretty tree (vue-json-pretty) for
// reading, and the textarea for editing. Editing marks the text dirty in the
// tree too — a text that no longer parses simply shows the source, which is
// where the mistake gets fixed anyway.
const rawView = ref<"pretty" | "source">("pretty");
const rawParsed = computed(() => {
  try {
    // Cast to the two shapes vue-json-pretty's data prop admits; anything else
    // JSON.parse could return is filtered by showRawPretty's object check.
    return { ok: true, data: JSON.parse(rawText.value) as Record<string, unknown> | unknown[] };
  } catch {
    return { ok: false, data: null };
  }
});
const showRawPretty = computed(
  () =>
    rawView.value === "pretty" &&
    rawParsed.value.ok &&
    typeof rawParsed.value.data === "object" &&
    rawParsed.value.data !== null,
);

// Closing with unsaved edits asks first. Both close paths route through here:
// the footer button and every dialog-native close (X, esc, overlay) via
// before-close, so no path discards silently.
async function tryClose(done: () => void) {
  // Every sheet's edits are guarded, now that all saves live in the footer:
  // closing past a dirty allowlist or sandbox choice must warn the same way
  // it always has for providers and storage.
  if (!hasUnsavedChanges()) {
    done();
    return;
  }
  try {
    await ElMessageBox.confirm(t("settingsDialog.unsavedBody"), t("settingsDialog.unsavedTitle"), {
      type: "warning",
      confirmButtonText: t("settingsDialog.discard"),
      cancelButtonText: t("settingsDialog.keepEditing"),
    });
    done();
  } catch {
    // keep editing
  }
}
const loading = ref(false);
const loadError = ref("");
// True once a load pass has put a server file on screen. Entering the module
// (or saving, which re-syncs) then refreshes in place instead of tearing the
// pane down to a lone "loading" note — that teardown was the flash users saw
// on every visit to 模型配置.
const configReady = ref(false);
const path = ref("");
const parseError = ref("");

// A provider row in the form: the file's fields plus the key value typed this
// visit ("" = leave the environment alone) and the server's current view of
// whether the key variable resolves. resolvedKey is the live value the server
// holds for key_env — the curl preview reproduces the real request, so it uses
// it. Headers are edited as ordered pairs because a Record's order is
// invisible to the user otherwise.
interface ProviderRow {
  name: string;
  baseUrl: string;
  keyEnv: string;
  baseUrlEnv: string;
  headers: { k: string; v: string }[];
  allowInsecureHttp: boolean;
  extraBody: string;
  keyValue: string;
  resolvedKey: string;
  configured: boolean;
}
interface ModelRow {
  compatibility?: ModelFileEntry["compatibility"];
  id: string;
  provider: string;
  contextWindow: number | undefined;
  maxTokens: number | undefined;
  // Tri-state like the file's field: undefined = auto (judge by the id),
  // true/false the explicit override.
  vision: boolean | undefined;
}

const rows = reactive<{ providers: ProviderRow[]; models: ModelRow[] }>({ providers: [], models: [] });
const defaultModel = ref("");
const visionModel = ref("");
const rawText = ref("");
const saving = ref(false);
const warnings = ref<string[]>([]);
watch(
  () => [rows.providers, rows.models, defaultModel.value, visionModel.value],
  () => {
    if (!syncing && lastEdited.value !== "form") lastEdited.value = "form";
  },
  { deep: true },
);
watch(rawText, () => {
  if (!syncing && lastEdited.value !== "raw") lastEdited.value = "raw";
});

// Leaving the raw view with raw edits rebuilds the form from it, so the form
// never shows a stale world. Advanced fields the form does not model
// (aliases, prices) are dropped from the rows — they live in the raw text,
// which is what a save sends while raw is the last editor. A raw text that
// does not parse leaves the rows alone; the save path will say so.
function rowsFromRaw() {
  let file: { providers?: Record<string, Record<string, unknown>>; models?: ModelFileEntry[]; default?: string; vision_model?: string };
  try {
    file = JSON.parse(rawText.value);
  } catch {
    return;
  }
  const prev = new Map(rows.providers.map((p) => [p.name, p]));
  void applySync(() => {
    rows.providers = Object.entries(file.providers ?? {}).map(([name, pf]) => {
      const old = prev.get(name);
      return {
        name,
        baseUrl: String(pf.base_url ?? ""),
        keyEnv: String(pf.key_env ?? ""),
        baseUrlEnv: pf.base_url_env ? String(pf.base_url_env) : "",
        headers: Object.entries((pf.headers as Record<string, string>) ?? {}).map(([k, v]) => ({ k, v: String(v) })),
        allowInsecureHttp: !!pf.allow_insecure_http,
        extraBody: pf.extra_body ? JSON.stringify(pf.extra_body) : "",
        keyValue: old?.keyValue ?? "",
        resolvedKey: old?.resolvedKey ?? "",
        configured: old?.configured ?? false,
      };
    });
    rows.models = (file.models ?? []).map((m) => ({
      compatibility: m.compatibility,
      id: m.id,
      provider: m.provider,
      contextWindow: m.context_window,
      maxTokens: m.max_tokens,
      vision: typeof m.vision === "boolean" ? m.vision : undefined,
    }));
    defaultModel.value = file.default ?? "";
    visionModel.value = file.vision_model ?? "";
  });
}


async function load() {
  loading.value = true;
  loadError.value = "";
  warnings.value = [];
  try {
    const cfg = await api.getConfig();
    path.value = cfg.path;
    parseError.value = cfg.parse_error ?? "";
    rawText.value = cfg.raw;
    const info = new Map(cfg.providers.map((p: ConfigProviderInfo) => [p.name, p]));
    rows.providers = Object.entries(cfg.file?.providers ?? {})
      .map(([name, pf]) => ({
        name,
        baseUrl: pf.base_url,
        keyEnv: pf.key_env,
        baseUrlEnv: pf.base_url_env ?? "",
        headers: Object.entries(pf.headers ?? {}).map(([k, v]) => ({ k, v })),
        allowInsecureHttp: pf.allow_insecure_http ?? false,
        extraBody: pf.extra_body ? JSON.stringify(pf.extra_body) : "",
        keyValue: "",
        resolvedKey: info.get(name)?.key_value ?? "",
        configured: info.get(name)?.configured ?? false,
      }))
      .sort((a, b) => a.name.localeCompare(b.name));
    rows.models = (cfg.file?.models ?? []).map((m) => ({
      compatibility: m.compatibility,
      id: m.id,
      provider: m.provider,
      contextWindow: m.context_window,
      maxTokens: m.max_tokens,
      vision: typeof m.vision === "boolean" ? m.vision : undefined,
    }));
    await applySync(() => {
      defaultModel.value = cfg.file?.default ?? cfg.default ?? "";
      visionModel.value = cfg.file?.vision_model ?? "";
      lastEdited.value = null;
    });
    snapshotFormJSON.value = currentConfigJSON();
    snapshotRaw.value = cfg.raw;
    configReady.value = true;
    // A file the strict decoder cannot read still has a raw form to fix.
    if (parseError.value) goAnchor("config");
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}

// ── 账户 / Account ───────────────────────────────────────────────────────────
// The pane is mode-aware: with no users.json server-side there is no password
// to change, and the pane says so instead of showing a form that can only 404.
const authMode = ref<"token" | "password">("token");
// The account whose password the inline form is editing (null = form closed).
// Changing an admin-row account asks for that account's current credential
// (old password or the deployment token); ordinary rows need nothing else —
// the viewer is already the operator.
const pwTarget = ref<{ username: string; admin: boolean } | null>(null);
const pwdCurrent = ref("");
const pwdNext = ref("");
const pwdConfirm = ref("");
const changingPw = ref(false);

function beginPwChange(a: { username: string; admin?: boolean }) {
  pwTarget.value = { username: a.username, admin: !!a.admin };
  pwdCurrent.value = "";
  pwdNext.value = "";
  pwdConfirm.value = "";
}

function cancelPwChange() {
  pwTarget.value = null;
  pwdCurrent.value = pwdNext.value = pwdConfirm.value = "";
}
// The viewer's operator bit and, when they hold it, the accounts table:
// names, roles, token presence — the values themselves stay server-side.
const meAdmin = ref(false);
const accounts = ref<{ username: string; created?: string; admin?: boolean; has_token: boolean }[]>([]);
const accountsError = ref("");

function loadAuthMode(includeAccounts = true) {
  api.authStatus().then(
    (st) => {
      // Delegated embeds do not mount these server-wide credential settings.
      if (st.mode === "token" || st.mode === "password") authMode.value = st.mode;
      meAdmin.value = st.admin;
      if (st.admin && includeAccounts) void loadAccounts();
    },
    () => (authMode.value = "token"),
  );
}

async function loadAccounts() {
  accountsError.value = "";
  try {
    accounts.value = (await api.accounts()).accounts;
  } catch (err) {
    accountsError.value = err instanceof Error ? err.message : String(err);
  }
}

async function changePw() {
  if (!pwTarget.value) return;
  if (pwTarget.value.admin && !pwdCurrent.value) {
    ElMessage.warning(t("settingsDialog.pwdNeedCurrent"));
    return;
  }
  if (pwdNext.value !== pwdConfirm.value) {
    ElMessage.warning(t("settingsDialog.pwdMismatch"));
    return;
  }
  if ([...pwdNext.value].length < 8) {
    ElMessage.warning(t("settingsDialog.pwdTooShort"));
    return;
  }
  changingPw.value = true;
  try {
    await api.changePassword({
      username: pwTarget.value.username,
      current: pwdCurrent.value,
      next: pwdNext.value,
    });
    ElMessage.success(t("settingsDialog.pwdChanged"));
    cancelPwChange();
  } catch (err) {
    const status = (err as { status?: number }).status;
    if (status === 401) ElMessage.error(t("settingsDialog.pwdWrongCurrent"));
    else if (status === 403) ElMessage.error(t("settingsDialog.pwdNoGrant"));
    else if (status === 429) ElMessage.error(t("settingsDialog.pwdLocked"));
    else ElMessage.error(err instanceof Error ? err.message : String(err));
  } finally {
    changingPw.value = false;
  }
}

// The account row's delete action: confirm, then the admin-only endpoint
// removes the users.json row. Admin rows never offer the button, and the
// server refuses them again — the check here is UX, not the boundary.
const deletingAccount = ref("");

async function removeAccount(a: { username: string; admin?: boolean }) {
  if (a.admin || deletingAccount.value) return;
  try {
    await ElMessageBox.confirm(t("settingsDialog.acctDeleteConfirm", { name: a.username }), t("common.confirm"), {
      type: "warning",
      confirmButtonText: t("common.delete"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  deletingAccount.value = a.username;
  try {
    await api.deleteAccount(a.username);
    if (pwTarget.value?.username === a.username) cancelPwChange();
    ElMessage.success(t("settingsDialog.acctDeleted"));
    await loadAccounts();
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : String(err));
  } finally {
    deletingAccount.value = "";
  }
}

// ── 沙箱 / Sandbox ───────────────────────────────────────────────────────────
// The runtime switch for OS containment, delivered the way the operator asked
// for it: visible and changeable from the dialog. It is a deliberate escape
// hatch, and the section is built to keep the choice an informed one — the
// current state is shown next to it, and choosing a mode that means
// unconfined execution shows the cost in red before it is saved.

const sandbox = ref<SandboxInfo | null>(null);
const sandboxError = ref("");
const sandboxSaving = ref(false);
const sandboxChoice = ref<"" | "enforce" | "degrade" | "off">("");

async function loadSandbox() {
  sandboxError.value = "";
  try {
    const info = await api.getSandbox();
    sandbox.value = info;
    sandboxChoice.value = info.override;
  } catch (err) {
    sandbox.value = null;
    sandboxError.value = err instanceof Error ? err.message : String(err);
  }
}

async function saveSandbox() {
  sandboxSaving.value = true;
  sandboxError.value = "";
  try {
    const info = await api.saveSandbox(sandboxChoice.value);
    sandbox.value = info;
    sandboxChoice.value = info.override;
    ElMessage.success(t("settingsDialog.sandboxSaved"));
  } catch (err) {
    sandboxError.value = err instanceof Error ? err.message : String(err);
  } finally {
    sandboxSaving.value = false;
  }
}

// What the current choice would mean once saved: "follow env" resolves
// against the deployment's baseline. Unconfined execution is the outcome the
// warning exists for — off always, degrade only when the sandbox is actually
// unavailable here.
const sandboxChoiceUnconfined = computed(() => {
  const s = sandbox.value;
  if (!s) return false;
  const mode = sandboxChoice.value === "" ? s.env_mode : sandboxChoice.value;
  if (mode === "off") return true;
  if (mode === "degrade" && !!s.unavailable) return true;
  return false;
});

// The sandbox's dirty bit: the picker says something the saved override does
// not. "" means "follow env", which is the absence of an override.
const sandboxDirty = computed(
  () => !!sandbox.value && sandboxChoice.value !== (sandbox.value.override ?? ""),
);

const sandboxStatusText = computed(() => {
  const s = sandbox.value;
  if (!s) return "";
  if (s.active) {
    return t("settingsDialog.sandboxStatusOn", {
      effective: s.effective,
      proc: s.proc ? t("settingsDialog.sandboxProcPrivate") : t("settingsDialog.sandboxProcShared"),
      net: s.net ? t("settingsDialog.sandboxNetAllowed") : t("settingsDialog.sandboxNetDenied"),
    });
  }
  if (s.unavailable) {
    const key = s.effective === "enforce" ? "settingsDialog.sandboxStatusRefused" : "settingsDialog.sandboxStatusDegraded";
    return t(key, { why: s.unavailable });
  }
  return t("settingsDialog.sandboxStatusOff");
});

// ── 附件 / Attachments ───────────────────────────────────────────────────────
// The upload allowlist, as group toggles over the one list the server
// enforces. The composer's door check and the picker's accept attribute both
// re-fetch what this saves, so a change here is a change everywhere without
// a reload.
const attachPolicy = ref<AttachmentPolicy | null>(null);
const attachError = ref("");
const attachSaving = ref(false);
// A local copy while editing; saved as a whole, like every other section.
const attachPick = ref(new Set<string>());

// The two heavy groups start collapsed: text holds 60+ extensions, and the
// binary sample group is a sandbox-facing set most deployments never touch.
// A non-empty query overrides collapse — every group with a match opens.
const attachQuery = ref("");
const attachCollapsed = ref(new Set<string>(["text", "binary"]));

// The sample/archive group is risky anywhere but a sandbox, so it gets the
// warning treatment: a hint line, and danger-colored chips when picked.
const DANGER_GROUPS = new Set(["binary"]);

function attachNorm(s: string): string {
  return s.trim().toLowerCase().replace(/^\.+/, "");
}

// Groups visible under the current query, with `shown` narrowed to matching
// extensions. Counts and the tri-state still read the full group, so a
// filter never looks like it dropped a selection.
const attachFiltered = computed(() => {
  const p = attachPolicy.value;
  if (!p) return [];
  const q = attachNorm(attachQuery.value);
  return p.groups
    .map((g) => ({ ...g, shown: q ? g.exts.filter((e) => e.slice(1).includes(q)) : g.exts }))
    .filter((g) => g.shown.length > 0);
});

function attachGroupCollapsed(id: string): boolean {
  if (attachNorm(attachQuery.value)) return false;
  return attachCollapsed.value.has(id);
}

function toggleAttachCollapsed(id: string) {
  const next = new Set(attachCollapsed.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  attachCollapsed.value = next;
}

// The cap slider snaps to these stops — a raw 10–1024 drag is too fine at
// the low end (where every MB matters to a document) and too twitchy at the
// top. These are the sizes a deployment actually picks between.
const CAP_STOPS = [10, 16, 25, 50, 100, 200, 512, 1024];
const capIdx = ref(0);

function capLabel(mb: number): string {
  return mb >= 1024 ? "1 GB" : `${mb} MB`;
}

// snapCapIdx maps an effective cap onto the nearest stop, so a server-side
// clamp (a hand-edited prefs outlier lands on 10 or 1024) still has a home.
function snapCapIdx(mb: number): number {
  let best = 0;
  for (let i = 1; i < CAP_STOPS.length; i++) {
    if (Math.abs(CAP_STOPS[i] - mb) < Math.abs(CAP_STOPS[best] - mb)) best = i;
  }
  return best;
}

async function loadAttach() {
  attachError.value = "";
  try {
    const p = await api.getAttachmentPolicy();
    attachPolicy.value = p;
    attachPick.value = new Set(p.allowed_exts);
    capIdx.value = snapCapIdx(p.max_mb);
  } catch (err) {
    attachPolicy.value = null;
    attachError.value = err instanceof Error ? err.message : String(err);
  }
}

function groupOn(id: string): boolean {
  const g = attachPolicy.value?.groups.find((x) => x.id === id);
  return !!g && g.exts.every((e) => attachPick.value.has(e));
}

function groupPart(id: string): boolean {
  const g = attachPolicy.value?.groups.find((x) => x.id === id);
  if (!g) return false;
  const n = g.exts.filter((e) => attachPick.value.has(e)).length;
  return n > 0 && n < g.exts.length;
}

function groupCount(id: string): number {
  const g = attachPolicy.value?.groups.find((x) => x.id === id);
  return g ? g.exts.filter((e) => attachPick.value.has(e)).length : 0;
}

function toggleExt(ext: string, on: boolean) {
  const next = new Set(attachPick.value);
  if (on) next.add(ext);
  else next.delete(ext);
  attachPick.value = next;
}

function toggleGroup(id: string, on: boolean) {
  const g = attachPolicy.value?.groups.find((x) => x.id === id);
  if (!g) return;
  const next = new Set(attachPick.value);
  for (const e of g.exts) {
    if (on) next.add(e);
    else next.delete(e);
  }
  attachPick.value = next;
}

const attachDirty = computed(() => {
  const p = attachPolicy.value;
  if (!p) return false;
  if (CAP_STOPS[capIdx.value] !== p.max_mb) return true;
  const saved = new Set(p.allowed_exts);
  if (saved.size !== attachPick.value.size) return true;
  for (const e of attachPick.value) if (!saved.has(e)) return true;
  return false;
});

async function saveAttach() {
  attachSaving.value = true;
  attachError.value = "";
  try {
    const p = await api.saveAttachmentPolicy([...attachPick.value], CAP_STOPS[capIdx.value]);
    attachPolicy.value = p;
    attachPick.value = new Set(p.allowed_exts);
    capIdx.value = snapCapIdx(p.max_mb);
    ElMessage.success(t("settingsDialog.attachSaved"));
  } catch (err) {
    attachError.value = err instanceof Error ? err.message : String(err);
  } finally {
    attachSaving.value = false;
  }
}

// ── MCP 服务 / MCP servers ───────────────────────────────────────────────────
// The mcp.json editor: the external MCP servers this agent connects to. Like
// storage and sandbox it is its own store with its own save; unlike them the
// list's quick edits (the enabled switch, delete) save immediately — only the
// add/edit form goes through the footer's save button. A file that does not
// parse still renders (with an empty config and the error on top), the same
// stance prefs takes: a broken file is when the editor is needed most.

// One row of the form: the file's fields, with the editing-friendly shapes —
// env/headers as ordered pairs (a Record's order is invisible to the user),
// args one per line, the tool filters comma-separated. kind is form-only: it
// picks which half of the fields a save sends, derived from command/url on
// load.
interface MCPRow {
  name: string;
  kind: "stdio" | "http";
  enabled: boolean;
  command: string;
  argsText: string;
  env: { k: string; v: string }[];
  cwd: string;
  url: string;
  transport: "streamable" | "sse";
  headers: { k: string; v: string }[];
  // The bearer auth's two mutually exclusive shapes: bearerMode picks which
  // field a save sends (the other is dropped, so the pair can never ship
  // together and earn the server's 400). Form-only, like kind.
  bearerMode: "env" | "direct";
  bearerTokenEnv: string;
  bearerToken: string;
  startupTimeoutMs: number | undefined;
  toolTimeoutMs: number | undefined;
  includeText: string;
  excludeText: string;
  trust: boolean;
  required: boolean;
}

// The name/endpoint/filter rules live in mcpImport.ts (shared with the JSON
// importer, so form and importer cannot drift apart) and are imported above.

const mcpPath = ref("");
const mcpLoadError = ref("");
const mcpError = ref("");
const mcpSaving = ref(false);
const mcpServers = ref<MCPRow[]>([]);
const mcpStatusList = ref<MCPServerStatus[]>([]);
// The saved servers map exactly as GET /api/mcp returned it: the export
// dialog's source of truth, so an unsaved edit form can never leak into an
// exported file.
const mcpSavedServers = ref<Record<string, MCPServerConfig>>({});

// The add/edit form's draft, null when the sheet shows the list. The name is
// the servers-map key, so it is editable only while adding (mcpEditingNew) —
// renaming is delete-plus-add.
const mcpEditing = ref<MCPRow | null>(null);
const mcpEditingNew = ref(false);
const mcpDraftSnapshot = ref("");
const mcpProbing = ref(false);
const mcpProbe = ref<MCPProbeResult | null>(null);
const mcpProbeEl = ref<HTMLElement | null>(null);

const mcpDirty = computed(() => !!mcpEditing.value && JSON.stringify(mcpEditing.value) !== mcpDraftSnapshot.value);

const mcpStatusMap = computed(() => new Map(mcpStatusList.value.map((s) => [s.name, s])));

function statusFor(name: string): MCPServerStatus | undefined {
  return mcpStatusMap.value.get(name);
}

function rowFromMCPConfig(name: string, sc: MCPServerConfig): MCPRow {
  return {
    name,
    kind: sc.url ? "http" : "stdio",
    enabled: sc.enabled ?? true,
    command: sc.command ?? "",
    argsText: (sc.args ?? []).join("\n"),
    env: Object.entries(sc.env ?? {}).map(([k, v]) => ({ k, v })),
    cwd: sc.cwd ?? "",
    url: sc.url ?? "",
    transport: sc.transport === "sse" ? "sse" : "streamable",
    headers: Object.entries(sc.headers ?? {}).map(([k, v]) => ({ k, v })),
    bearerMode: sc.bearer_token ? "direct" : "env",
    bearerTokenEnv: sc.bearer_token_env ?? "",
    bearerToken: sc.bearer_token ?? "",
    startupTimeoutMs: sc.startup_timeout_ms || undefined,
    toolTimeoutMs: sc.tool_timeout_ms || undefined,
    includeText: (sc.include_tools ?? []).join(", "),
    excludeText: (sc.exclude_tools ?? []).join(", "),
    trust: !!sc.trust,
    required: !!sc.required,
  };
}

// buildMCPServer projects a row back onto the file's shape: only set fields,
// and only the half of the form the kind switch governs — the strict decoder
// rejects the wrong half's fields on the other kind.
function buildMCPServer(r: MCPRow): MCPServerConfig {
  const sc: MCPServerConfig = {};
  if (!r.enabled) sc.enabled = false;
  if (r.kind === "stdio") {
    sc.command = r.command.trim();
    const args = r.argsText
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    if (args.length) sc.args = args;
    const env: Record<string, string> = {};
    for (const e of r.env) {
      if (e.k.trim()) env[e.k.trim()] = e.v; // value kept verbatim, spaces included
    }
    if (Object.keys(env).length) sc.env = env;
    if (r.cwd.trim()) sc.cwd = r.cwd.trim();
  } else {
    sc.url = r.url.trim();
    if (r.transport === "sse") sc.transport = "sse"; // absent means streamable
    const headers: Record<string, string> = {};
    for (const h of r.headers) {
      if (h.k.trim()) headers[h.k.trim()] = h.v;
    }
    if (Object.keys(headers).length) sc.headers = headers;
    // The bearer form the mode switch selected; the other's field is never
    // sent (the two are mutually exclusive server-side).
    if (r.bearerMode === "direct") {
      const tok = r.bearerToken.trim();
      if (tok) sc.bearer_token = tok;
    } else if (r.bearerTokenEnv.trim()) {
      sc.bearer_token_env = r.bearerTokenEnv.trim();
    }
  }
  if (r.startupTimeoutMs) sc.startup_timeout_ms = r.startupTimeoutMs;
  if (r.toolTimeoutMs) sc.tool_timeout_ms = r.toolTimeoutMs;
  const include = r.includeText.split(",").map((s) => s.trim()).filter(Boolean);
  const exclude = r.excludeText.split(",").map((s) => s.trim()).filter(Boolean);
  if (include.length) sc.include_tools = include;
  if (exclude.length) sc.exclude_tools = exclude;
  if (r.trust) sc.trust = true;
  if (r.required) sc.required = true;
  return sc;
}

// The connection half of the checks, shared by save and probe — a probe
// rehearses an entry that may not have a name yet, so the name rule is the
// save's alone.
function validateMCPConn(r: MCPRow): string {
  if (r.kind === "stdio") {
    if (!r.command.trim()) return t("settingsDialog.mcpNoCommand");
  } else {
    const u = r.url.trim();
    if (!u) return t("settingsDialog.mcpNoUrl");
    if (!/^https?:\/\//.test(u)) return t("settingsDialog.mcpBadUrl");
    if (r.bearerMode === "direct") {
      // A literal token: never a $-reference (that is the variable form's
      // job), never blank — the server 400s both, so say so inline.
      const tok = r.bearerToken;
      if (tok && (tok.startsWith("$") || !tok.trim())) return t("settingsDialog.mcpBadBearerToken");
    } else if (r.bearerTokenEnv.trim() && !MCP_ENV_NAME_RE.test(r.bearerTokenEnv.trim())) {
      return t("settingsDialog.mcpBadBearerEnv");
    }
  }
  for (const [field, text] of [
    ["include_tools", r.includeText],
    ["exclude_tools", r.excludeText],
  ] as const) {
    for (const p of text.split(",").map((s) => s.trim()).filter(Boolean)) {
      if (!MCP_TOOL_PATTERN_RE.test(p)) return t("settingsDialog.mcpBadToolFilter", { field, pattern: p });
    }
  }
  return "";
}

function applyMCPView(v: MCPInfo) {
  mcpPath.value = v.path;
  mcpLoadError.value = v.load_error ?? "";
  mcpStatusList.value = v.status ?? [];
  mcpSrvCatalog.value = v.serve_catalog ?? null;
  mcpSavedServers.value = v.config?.servers ?? {};
  mcpServers.value = Object.entries(v.config?.servers ?? {})
    .map(([name, sc]) => rowFromMCPConfig(name, sc))
    .sort((a, b) => a.name.localeCompare(b.name));
  // The serve section resets only when the saved value actually moved —
  // otherwise a client-side quick save (a card's switch) would clobber the
  // operator's half-edited serve edits with the section's own echo.
  const savedJSON = JSON.stringify(v.config?.server ?? {});
  if (savedJSON !== mcpSrvSavedJSON.value) {
    mcpSrvSavedJSON.value = savedJSON;
    const row = rowFromServeConfig(v.config?.server);
    // The allowlist's live value rides server_state (it is hot-applied); the
    // file section is the fallback for backends that do not report it.
    const include = v.server_state?.include_tools ?? v.config?.server?.include_tools;
    row.includeText = (include ?? []).join(", ");
    mcpSrv.value = row;
    mcpSrvChecks.value = serveChecksFromInclude(include ?? [], v.serve_catalog);
    mcpSrvRowSnapshot.value = JSON.stringify(row);
  }
  mcpSrvState.value = v.server_state ?? null;
}

// ── 本机作为 MCP 服务端 / This machine as an MCP server ──────────────────────
// mcp.json's second section: how this agent serves *others* over /mcp and
// /sse. One form with its own save (no immediate PUTs here, unlike the card
// switches); every save anywhere on the sheet is wholesale over both
// sections, so the client saves re-send the serve section's last *saved*
// shape (mcpSrvSavedJSON), never the half-edited form.

const mcpSrv = ref<MCPServeRow>(rowFromServeConfig(undefined));
const mcpSrvSavedJSON = ref("");
const mcpSrvRowSnapshot = ref("");
const mcpSrvState = ref<MCPInfo["server_state"] | null>(null);
const mcpSrvAdvOpen = ref(false);

// The grouped check tree over GET /api/mcp's serve_catalog (null on older
// backends or a failed load — the comma box stays as the fallback then).
// includeText remains the save's source of truth: every toggle rewrites it
// through serveIncludeFromChecks, so validation and the save path are
// exactly the text box's.
const mcpSrvCatalog = ref<MCPServeCatalog | null>(null);
const mcpSrvChecks = ref<Set<string>>(new Set());

const mcpSrvPickSects = computed(() => {
  const groups = servePickerModel(mcpSrvCatalog.value);
  const byKind = (k: ServePickerGroup["kind"]) => groups.filter((g) => g.kind === k);
  const sects: { id: string; title: string; empty: string; groups: ServePickerGroup[] }[] = [];
  const builtin = byKind("builtin");
  if (builtin.length) sects.push({ id: "builtin", title: t("settingsDialog.mcpSrvPickBuiltin"), empty: "", groups: builtin });
  sects.push({ id: "skill", title: t("settingsDialog.mcpSrvPickSkills"), empty: t("settingsDialog.mcpSrvPickNoSkills"), groups: byKind("skill") });
  sects.push({ id: "forward", title: t("settingsDialog.mcpSrvPickForward"), empty: t("settingsDialog.mcpSrvPickNoServers"), groups: byKind("forward") });
  // The runtime section renders only when the catalog carries the field at
  // all — older backends without it get exactly the old three sections.
  if (mcpSrvCatalog.value?.runtime) {
    sects.push({ id: "runtime", title: t("settingsDialog.mcpSrvPickRuntime"), empty: t("settingsDialog.mcpSrvPickNoRuntime"), groups: byKind("runtime") });
  }
  return sects;
});

// A forward group whose server is not connected greys out (the tools are
// stale at best, and the pool will re-list them on reconnect); a runtime
// package greys out unless it is available (revoked / needs_configuration …).
function serveGroupDown(g: ServePickerGroup): boolean {
  if (g.kind === "forward") return g.serverState !== "connected";
  if (g.kind === "runtime") return g.serverState !== "available";
  return false;
}

function serveGroupStateText(g: ServePickerGroup): string {
  if (g.serverState === "connecting") return t("settingsDialog.mcpStateConnecting");
  return t("settingsDialog.mcpStateDisconnected");
}

function serveGroupDownHint(g: ServePickerGroup): string {
  // Runtime states are stable slugs (revoked, needs_configuration …) and
  // read fine raw in both locales.
  if (g.kind === "runtime") return t("settingsDialog.mcpSrvPickRuntimeDown", { state: g.serverState });
  return t("settingsDialog.mcpSrvPickServerDown", { state: serveGroupStateText(g) });
}

function serveGroupOn(g: ServePickerGroup): boolean {
  return g.tools.length > 0 && g.tools.every((x) => mcpSrvChecks.value.has(x.name));
}

function serveGroupPart(g: ServePickerGroup): boolean {
  const n = serveGroupCount(g);
  return n > 0 && n < g.tools.length;
}

function serveGroupCount(g: ServePickerGroup): number {
  return g.tools.filter((x) => mcpSrvChecks.value.has(x.name)).length;
}

function serveGroupLabel(g: ServePickerGroup): string {
  if (g.kind === "forward") return g.serverName ?? g.id;
  if (g.kind === "runtime") return g.version ? `${g.packageName}@${g.version}` : (g.packageName ?? g.id);
  return g.wildcard ?? g.tools.map((x) => x.name).join(", ");
}

function serveToolSub(tool: ServePickerTool): string {
  return tool.description || (tool.title && tool.title !== tool.name ? tool.name : "");
}

function applyServeChecks(next: Set<string>) {
  mcpSrvChecks.value = next;
  mcpSrv.value.includeText = serveIncludeFromChecks(next, mcpSrvCatalog.value).join(", ");
}

function toggleServeTool(name: string, on: boolean) {
  const next = new Set(mcpSrvChecks.value);
  if (on) next.add(name);
  else next.delete(name);
  applyServeChecks(next);
}

function toggleServeGroup(g: ServePickerGroup, on: boolean) {
  const next = new Set(mcpSrvChecks.value);
  for (const x of g.tools) {
    if (on) next.add(x.name);
    else next.delete(x.name);
  }
  applyServeChecks(next);
}

const mcpSrvDirty = computed(() => JSON.stringify(mcpSrv.value) !== mcpSrvRowSnapshot.value);
const mcpSrvParts = computed(() => serveStatusParts(mcpSrvState.value));
// An env-sourced effective config means the POGO_MCP* variables have taken
// over: editing the file changes nothing, so the form says so and disables.
const mcpSrvEnvTaken = computed(() => mcpSrvParts.value.source === "env");

const mcpSrvDotClass = computed(() => {
  const p = mcpSrvParts.value;
  if (p.restart) return "wait";
  return p.on ? "on" : "";
});

const mcpSrvStatusText = computed(() => {
  const p = mcpSrvParts.value;
  if (!p.on) return t("settingsDialog.mcpSrvStatusOff");
  const token = p.tokenRequired ? t("settingsDialog.mcpSrvStatusToken") : t("settingsDialog.mcpSrvStatusOpen");
  return t("settingsDialog.mcpSrvStatusOn", { token, policy: p.policy });
});

// The serve section's save: validate, then PUT both sections — the client
// map from the current rows, the serve section from the form.
async function saveMCPServe() {
  const bad = validateServeRow(mcpSrv.value);
  if (bad) {
    const key =
      bad.code === "token"
        ? "settingsDialog.mcpBadBearerToken"
        : bad.code === "tokenEnv"
          ? "settingsDialog.mcpSrvErrTokenEnv"
          : "settingsDialog.mcpSrvErrIncludeTools";
    ElMessage.error(t(key, bad.param ? { pattern: bad.param } : {}));
    return;
  }
  const servers: Record<string, MCPServerConfig> = {};
  for (const r of mcpServers.value) servers[r.name] = buildMCPServer(r);
  await putMCPConfig({ servers, server: buildServeConfig(mcpSrv.value) }, t("settingsDialog.mcpSrvSaved"));
}

async function loadMCP() {
  mcpError.value = "";
  try {
    applyMCPView(await api.getMCP());
  } catch (err) {
    // A failed refresh keeps the cards already on screen — clearing them was
    // a destructive flash of the same family the models module had. The
    // first load starts empty anyway, so nothing is lost by keeping stale.
    mcpError.value = err instanceof Error ? err.message : String(err);
  }
}

// Connects are asynchronous — the pool reconciles in the background, so right
// after a save the lights read "connecting" at best. Two cheap status reads
// catch the first round's outcome.
function scheduleMCPStatusPoll() {
  for (const ms of [1500, 4000]) {
    window.setTimeout(() => {
      api
        .mcpStatus()
        .then((r) => (mcpStatusList.value = r.status ?? []))
        .catch(() => {});
    }, ms);
  }
}

// putMCP is every save's shared tail: the whole servers map goes up, the
// answer (file + live status) comes back into the rows, and the status poll
// picks up the connections the reconcile just started.
async function putMCP(rows: MCPRow[], okMsg: string): Promise<boolean> {
  const servers: Record<string, MCPServerConfig> = {};
  for (const r of rows) {
    const name = r.name.trim();
    if (!name) continue;
    servers[name] = buildMCPServer(r);
  }
  return putMCPServers(servers, okMsg);
}

// putMCPConfig is every save's shared tail: the PUT is wholesale over both
// mcp.json sections, the answer (file + live status) comes back into the
// rows, and the status poll picks up the connections the reconcile just
// started.
async function putMCPConfig(body: { servers: Record<string, MCPServerConfig>; server: MCPServeConfig }, okMsg: string): Promise<boolean> {
  mcpSaving.value = true;
  mcpError.value = "";
  try {
    applyMCPView(await api.saveMCP(body));
    if (okMsg) ElMessage.success(okMsg);
    scheduleMCPStatusPoll();
    return true;
  } catch (err) {
    mcpError.value = err instanceof Error ? err.message : String(err);
    ElMessage.error(t("settingsDialog.saveFailed", { msg: mcpError.value }));
    return false;
  } finally {
    mcpSaving.value = false;
  }
}

// putMCPServers is the client side's save: the new servers map, with the
// serve section re-sent at its last *saved* shape — wholesale means every
// save names both sections, and the half-edited serve form is not this
// save's business.
async function putMCPServers(servers: Record<string, MCPServerConfig>, okMsg: string): Promise<boolean> {
  const server = (mcpSrvSavedJSON.value ? JSON.parse(mcpSrvSavedJSON.value) : {}) as MCPServeConfig;
  return putMCPConfig({ servers, server }, okMsg);
}

function blankMCPRow(): MCPRow {
  return {
    name: "", kind: "stdio", enabled: true, command: "", argsText: "", env: [], cwd: "",
    url: "", transport: "streamable", headers: [], bearerMode: "env", bearerTokenEnv: "", bearerToken: "",
    startupTimeoutMs: undefined, toolTimeoutMs: undefined, includeText: "", excludeText: "",
    trust: false, required: false,
  };
}

// The form edits a deep copy: nothing in the list moves until the save lands.
function openMCPEdit(row: MCPRow, isNew: boolean) {
  mcpEditing.value = JSON.parse(JSON.stringify(row)) as MCPRow;
  mcpEditingNew.value = isNew;
  mcpDraftSnapshot.value = JSON.stringify(mcpEditing.value);
  mcpProbe.value = null;
  mcpError.value = "";
}

async function cancelMCPEdit() {
  if (mcpDirty.value) {
    try {
      await ElMessageBox.confirm(t("settingsDialog.mcpDiscardDraft"), t("settingsDialog.unsavedTitle"), {
        type: "warning",
        confirmButtonText: t("settingsDialog.discard"),
        cancelButtonText: t("settingsDialog.keepEditing"),
      });
    } catch {
      return; // keep editing
    }
  }
  mcpEditing.value = null;
  mcpProbe.value = null;
}

// The footer save, reachable only while the form is open: validate, apply the
// draft onto the list, PUT the whole map.
async function saveMCP() {
  const draft = mcpEditing.value;
  if (!draft) return;
  const name = draft.name.trim();
  if (!MCP_NAME_RE.test(name)) {
    ElMessage.error(t("settingsDialog.mcpBadName"));
    return;
  }
  if (mcpEditingNew.value && mcpServers.value.some((r) => r.name === name)) {
    ElMessage.error(t("settingsDialog.mcpNameTaken", { name }));
    return;
  }
  const bad = validateMCPConn(draft);
  if (bad) {
    ElMessage.error(bad);
    return;
  }
  draft.name = name;
  const rows = mcpEditingNew.value
    ? [...mcpServers.value, draft]
    : mcpServers.value.map((r) => (r.name === name ? draft : r));
  if (await putMCP(rows, t("settingsDialog.mcpSaved"))) {
    mcpEditing.value = null;
    mcpProbe.value = null;
  }
}

async function toggleMCPEnabled(row: MCPRow) {
  if (mcpSaving.value) return;
  const prev = row.enabled;
  row.enabled = !prev;
  if (!(await putMCP(mcpServers.value, t("settingsDialog.mcpSaved")))) {
    row.enabled = prev; // the save failed; put the switch back
  }
}

async function removeMCPServer(row: MCPRow) {
  try {
    await ElMessageBox.confirm(t("settingsDialog.mcpDeleteConfirm", { name: row.name }), t("common.confirm"), {
      type: "warning",
      confirmButtonText: t("common.delete"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  await putMCP(mcpServers.value.filter((r) => r.name !== row.name), t("settingsDialog.mcpSaved"));
}

// The equivalent-curl board: the probe's own dance as runnable commands,
// recomputed from the live draft so unsaved edits show. The templates are
// pure functions in mcpCurl.ts.
const mcpCurlText = computed(() => (mcpEditing.value ? buildCurlPreview(buildMCPServer(mcpEditing.value)) : ""));

async function copyMCPCurl() {
  try {
    await navigator.clipboard.writeText(mcpCurlText.value);
    ElMessage.success(t("settingsDialog.mcpCurlCopied"));
  } catch {
    ElMessage.error(t("settingsDialog.copyFailed"));
  }
}

// Test connection: probe with the form's current values, saved or not. The
// answer lists the server-reported tools; a failure is the reason, not an
// HTTP error.
async function probeMCP() {
  const draft = mcpEditing.value;
  if (!draft || mcpProbing.value) return;
  const bad = validateMCPConn(draft);
  if (bad) {
    ElMessage.error(bad);
    return;
  }
  mcpProbing.value = true;
  mcpProbe.value = null;
  try {
    mcpProbe.value = await api.probeMCP(buildMCPServer(draft));
  } catch (err) {
    mcpProbe.value = { ok: false, error: err instanceof Error ? err.message : String(err) };
  } finally {
    mcpProbing.value = false;
  }
  // The result block lands at the foot of a long form; nothing would scroll
  // to it on its own, so an outcome below the fold reads as "the button did
  // nothing". Bring it into view once it has rendered.
  await nextTick();
  mcpProbeEl.value?.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

function mcpSummary(r: MCPRow): string {
  if (r.kind === "stdio") {
    const args = r.argsText.split("\n").map((s) => s.trim()).filter(Boolean).join(" ");
    return `stdio: ${r.command}${args ? " " + args : ""}`;
  }
  return `${r.url.startsWith("https:") ? "https" : "http"}: ${r.url}`;
}

// The status light: red wins (a last_error means the latest attempt failed),
// then the pool's state; a disabled server keeps no connection, so no status
// entry exists for it — gray with a tooltip that says why.
function mcpDotClass(r: MCPRow): string {
  if (!r.enabled) return "";
  const st = statusFor(r.name);
  if (st?.last_error) return "bad";
  if (st?.state === "connected") return "on";
  if (st?.state === "connecting") return "wait";
  return "";
}

function mcpDotTip(r: MCPRow): string {
  if (!r.enabled) return t("settingsDialog.mcpStateDisabled");
  const st = statusFor(r.name);
  if (st?.last_error) return st.last_error;
  if (st?.state === "connected") return t("settingsDialog.mcpStateConnected");
  if (st?.state === "connecting") return t("settingsDialog.mcpStateConnecting");
  return t("settingsDialog.mcpStateDisconnected");
}

// The one-line version beside the light: a connected card reports its tool
// count, a failed one the error itself (CSS-clipped, the tooltip keeps the
// full text via mcpDotTip).
function mcpStatusText(r: MCPRow): string {
  if (!r.enabled) return t("settingsDialog.mcpStateDisabled");
  const st = statusFor(r.name);
  if (st?.last_error) return st.last_error;
  if (st?.state === "connected") return t("settingsDialog.mcpStateConnectedTools", { n: st.tools });
  if (st?.state === "connecting") return t("settingsDialog.mcpStateConnecting");
  return t("settingsDialog.mcpStateDisconnected");
}

// ── JSON 导入 / 示例模板 ─────────────────────────────────────────────────────
// The import dialog: paste or pick a JSON file (pogo's "servers" root or the
// ecosystem's "mcpServers"), parse it into a preview of per-entry verdicts,
// then merge the valid ones into the servers map with one PUT. The mapping
// and the rules live in mcpImport.ts as pure functions; here is only the
// dialog's state machine.

const mcpImportVisible = ref(false);
const mcpImportText = ref("");
// null until the parse button runs; typing afterwards invalidates the
// preview (@input on the textarea), so the confirm never acts on stale text.
const mcpImportParsed = ref<MCPImportResult | null>(null);
const mcpImportFileInput = ref<HTMLInputElement | null>(null);

const mcpImportableCount = computed(() => {
  const p = mcpImportParsed.value;
  return p && p.ok ? p.entries.filter((e) => e.config).length : 0;
});

const mcpNameSet = computed(() => new Set(mcpServers.value.map((r) => r.name)));

// The ignored-field footnote, one "name: field, field" segment per entry.
const mcpImportIgnoredText = computed(() => {
  const p = mcpImportParsed.value;
  if (!p || !p.ok) return "";
  return p.entries
    .filter((e) => e.ignored.length)
    .map((e) => `${e.name}: ${e.ignored.join(", ")}`)
    .join("; ");
});

function openMCPImport() {
  mcpImportText.value = "";
  mcpImportParsed.value = null;
  mcpImportVisible.value = true;
}

function runMCPImportParse() {
  mcpImportParsed.value = parseMCPImport(mcpImportText.value);
}

function pickMCPImportFile() {
  mcpImportFileInput.value?.click();
}

async function onMCPImportFile(e: Event) {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = ""; // allows picking the same file again after a failure
  if (!file) return;
  mcpImportText.value = await file.text().catch(() => "");
  // A file was chosen deliberately — parse it at once (the @input reset does
  // not fire for programmatic fills, so the preview survives).
  runMCPImportParse();
}

// The preview's one-line summary of a valid entry, same grammar as the cards.
function mcpImportSummary(sc: MCPServerConfig): string {
  if (sc.url) return `${sc.url.startsWith("https:") ? "https" : "http"}: ${sc.url}`;
  const args = (sc.args ?? []).join(" ");
  return `stdio: ${sc.command}${args ? " " + args : ""}`;
}

function mcpImportErrText(e: MCPImportEntry): string {
  if (!e.error) return "";
  return t(`settingsDialog.mcpImportErr_${e.error.code}`, e.error.params ?? {});
}

// Confirm merges every valid entry over the current map — same name means
// overwrite, which the preview has already announced.
async function confirmMCPImport() {
  const p = mcpImportParsed.value;
  if (!p || !p.ok) return;
  const valid = p.entries.filter((e) => e.config);
  if (!valid.length) return;
  const merged: Record<string, MCPServerConfig> = {};
  for (const r of mcpServers.value) merged[r.name] = buildMCPServer(r);
  for (const e of valid) merged[e.name] = e.config!;
  if (await putMCPServers(merged, t("settingsDialog.mcpImported", { n: valid.length }))) {
    mcpImportVisible.value = false;
  }
}

// The example template, on the ecosystem's mcpServers root so it doubles as
// documentation for the import door: one http entry against a local
// pogo/malagent server, one stdio entry parked disabled. Standard JSON — no
// comments; the explicit timeout values carry the defaults' documentation.
function downloadMCPSample() {
  const sample = {
    mcpServers: {
      "malagent-local": {
        url: "http://127.0.0.1:7778/mcp",
        bearer_token_env: "MALAGENT_MCP_TOKEN",
        include_tools: ["mal.*", "agent_*"],
        trust: false,
        startup_timeout_ms: 10000,
        tool_timeout_ms: 120000,
      },
      "filesystem-parked": {
        enabled: false,
        command: "npx",
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/workspace"],
        env: { API_KEY: "$MY_SERVICE_API_KEY" },
      },
    },
  };
  const blob = new Blob([JSON.stringify(sample, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "mcp-servers-example.json";
  a.click();
  URL.revokeObjectURL(a.href);
}

// ── MCP 导出 / MCP export ───────────────────────────────────────────────────
// Both entries open the same dialog (McpExportDialog.vue) in two modes: the
// client sheet exports selected saved servers, the serving sheet exports one
// connection entry for calling this machine. The mapping rules live in
// mcpExport.ts; here is only which mode is open and what data feeds it.
const mcpExportVisible = ref(false);
const mcpExportMode = ref<"client" | "serve">("client");

const mcpExportList = computed(() => Object.entries(mcpSavedServers.value).map(([name, config]) => ({ name, config })));

// The saved token_env name seeds the serve export's variable-name field; the
// token value itself is never read here.
const mcpSrvSavedTokenEnv = computed(() => {
  if (!mcpSrvSavedJSON.value) return "";
  try {
    return (JSON.parse(mcpSrvSavedJSON.value) as MCPServeConfig).token_env ?? "";
  } catch {
    return "";
  }
});

function openMCPExport(mode: "client" | "serve") {
  mcpExportMode.value = mode;
  mcpExportVisible.value = true;
}

// ── 存储 / Storage ───────────────────────────────────────────────────────────
// The storage section (spill retention, current usage, prune-now) is its own
// world: separate API from the providers editor above, separate save button,
// and a load failure here must never block editing providers.json — so it
// keeps its own state and degrades to an inline note instead of loadError.

const storage = ref<{
  days: number;
  files: number;
  bytes: number;
  dir: string;
  lastSweep: SweepInfo | null;
  turns: number;
  turnsFlag: number;
  turnsEffective: number;
  turnsLimited: boolean;
} | null>(null);
// The full prefs object as loaded, so the storage sheet's wholesale
// PUT /api/prefs carries the fields other sheets own instead of zeroing them.
const prefsLoaded = ref<Prefs | null>(null);
const snapshotTurns = ref(0);
const snapshotTurnsLimited = ref(false);
const turnsEffectiveText = computed(() => storage.value?.turnsLimited ? t("settingsDialog.turnsEffective", { n: storage.value.turnsEffective }) : t("settingsDialog.turnsUnlimited"));
const storageError = ref("");
const storageSaving = ref(false);
const pruning = ref(false);
const spillBrowserVisible = ref(false);
// The memory-distillation toggle saves itself on flip — a switch is its own
// commit button, unlike the numeric fields above.
const memoryAuto = ref(false);
const memoryAutoSaving = ref(false);

async function saveMemoryAuto(on: boolean) {
  memoryAutoSaving.value = true;
  try {
    await api.setMemoryAuto(on);
    if (prefsLoaded.value) prefsLoaded.value.memory_auto = on;
    ElMessage.success(t("settingsDialog.memorySaved"));
  } catch (err) {
    memoryAuto.value = !on; // the server kept the old value; the switch must say so
    ElMessage.error(t("settingsDialog.saveFailed", { msg: err instanceof Error ? err.message : String(err) }));
  } finally {
    memoryAutoSaving.value = false;
  }
}

async function loadStorage() {
  storageError.value = "";
  try {
    const info = await api.getPrefs();
    prefsLoaded.value = info.prefs;
    memoryAuto.value = !!info.prefs.memory_auto;
    const turnOverride = info.turns?.max_turns ?? 0;
    storage.value = {
      days: info.prefs.spill_retention_days,
      files: info.spill.files,
      bytes: info.spill.bytes,
      dir: info.spill.dir,
      lastSweep: info.last_sweep ?? null,
      turns: turnOverride > 0 ? info.turns?.effective ?? turnOverride : 300,
      turnsFlag: info.turns?.flag ?? 300,
      turnsEffective: info.turns?.effective ?? 0,
      turnsLimited: turnOverride > 0,
    };
    snapshotDays.value = storage.value.days;
    snapshotTurns.value = storage.value.turns;
    snapshotTurnsLimited.value = storage.value.turnsLimited;
    // A prefs file the server cannot read pauses the automatic sweep; the
    // section still renders (with the defaults) so the file can be fixed by
    // saving from here.
    if (info.load_error) storageError.value = info.load_error;
  } catch (err) {
    storage.value = null;
    storageError.value = err instanceof Error ? err.message : String(err);
  }
}

function formatMB(bytes: number): string {
  return (bytes / 1048576).toFixed(1) + " MB";
}

const lastSweepText = computed(() => {
  const sw = storage.value?.lastSweep;
  if (!sw) return t("settingsDialog.storageNoSweep");
  return t("settingsDialog.storageLastSweep", {
    time: new Date(sw.time).toLocaleString(),
    n: sw.removed,
    size: formatMB(sw.bytes),
  });
});

async function saveStorage() {
  if (!storage.value) return;
  const days = Math.trunc(storage.value.days);
  if (Number.isNaN(days) || days < 0 || days > 3650) {
    ElMessage.error(t("settingsDialog.storageBadDays"));
    return;
  }
  const turns = storage.value.turnsLimited ? storage.value.turns : 0;
  if (storage.value.turnsLimited && (!Number.isInteger(turns) || turns < 1 || turns > 1000)) {
    ElMessage.error(t("settingsDialog.turnsBad"));
    return;
  }
  storageSaving.value = true;
  try {
    // The wholesale prefs save carries the loaded object plus this sheet's
    // days edit, so fields other sheets own survive it; the turn cap then
    // goes through its dedicated endpoint, which persists it server-side and
    // pushes it into already-open conversations.
    const info = await api.savePrefs({ ...(prefsLoaded.value ?? {}), spill_retention_days: days });
    storage.value.days = info.prefs.spill_retention_days;
    snapshotDays.value = storage.value.days;
    prefsLoaded.value = info.prefs;
    if (turns !== snapshotTurns.value || storage.value.turnsLimited !== snapshotTurnsLimited.value) {
      const tv = await api.setMaxTurns(turns);
      storage.value.turnsFlag = tv.flag;
      storage.value.turnsEffective = tv.effective;
      prefsLoaded.value.max_turns = tv.max_turns;
    }
    snapshotTurns.value = storage.value.turns;
    snapshotTurnsLimited.value = storage.value.turnsLimited;
    storageError.value = info.load_error ?? "";
    ElMessage.success(t("settingsDialog.storageSaved"));
  } catch (err) {
    ElMessage.error(t("settingsDialog.saveFailed", { msg: err instanceof Error ? err.message : String(err) }));
    const fresh = await api.getPrefs().catch(() => null);
    if (fresh) {
      prefsLoaded.value = fresh.prefs;
      if (storage.value) {
        const override = fresh.turns?.max_turns ?? 0;
        storage.value.turns = override > 0 ? fresh.turns?.effective ?? override : 300;
        storage.value.turnsFlag = fresh.turns?.flag ?? storage.value.turnsFlag;
        storage.value.turnsEffective = fresh.turns?.effective ?? storage.value.turnsEffective;
        storage.value.turnsLimited = override > 0;
        snapshotTurns.value = storage.value.turns;
        snapshotTurnsLimited.value = storage.value.turnsLimited;
      }
    }
  } finally {
    storageSaving.value = false;
  }
}

async function pruneSpillNow() {
  pruning.value = true;
  try {
    const res = await api.pruneSpill();
    ElMessage.success(t("settingsDialog.pruneDone", { n: res.removed, size: formatMB(res.bytes) }));
    await loadStorage(); // refresh usage + last-sweep after the pass
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : String(err));
  } finally {
    pruning.value = false;
  }
}

const providerNames = computed(() => rows.providers.map((p) => p.name));
const modelIds = computed(() => rows.models.map((m) => m.id));

function addProvider() {
  rows.providers.push({
    name: "", baseUrl: "", keyEnv: "", baseUrlEnv: "",
    headers: [], allowInsecureHttp: false, extraBody: "",
    keyValue: "", resolvedKey: "", configured: false,
  });
}

function addModel() {
  const provider = providerNames.value[0] ?? "";
  rows.models.push({ id: "", provider, contextWindow: undefined, maxTokens: undefined, vision: undefined });
}

function buildFile() {
  const providers: Record<string, {
    base_url: string; key_env: string; base_url_env?: string;
    headers?: Record<string, string>; extra_body?: Record<string, unknown>;
    allow_insecure_http?: boolean;
  }> = {};
  for (const p of rows.providers) {
    const name = p.name.trim();
    if (!name) continue; // a nameless row is a row still being typed
    const entry: {
      base_url: string; key_env: string; base_url_env?: string;
      headers?: Record<string, string>; extra_body?: Record<string, unknown>;
      allow_insecure_http?: boolean;
    } = {
      base_url: p.baseUrl.trim(),
      key_env: p.keyEnv.trim(),
    };
    if (p.baseUrlEnv.trim()) entry.base_url_env = p.baseUrlEnv.trim();
    const headers: Record<string, string> = {};
    for (const h of p.headers) {
      if (h.k.trim()) headers[h.k.trim()] = h.v; // value kept verbatim, spaces included
    }
    if (Object.keys(headers).length) entry.headers = headers;
    if (p.extraBody.trim()) {
      // Unparsable here only if save()'s check was bypassed; leave the entry
      // unchanged rather than inventing a value, so the server refuses it.
      const parsed: unknown = JSON.parse(p.extraBody);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        entry.extra_body = parsed as Record<string, unknown>;
      }
    }
    if (p.allowInsecureHttp) entry.allow_insecure_http = true;
    providers[name] = entry;
  }
  return {
    ...(defaultModel.value ? { default: defaultModel.value } : {}),
    ...(visionModel.value ? { vision_model: visionModel.value } : {}),
    providers,
    models: rows.models
      .filter((m) => m.id.trim() && m.provider)
      .map((m) => ({
        ...(m.compatibility ? { compatibility: m.compatibility } : {}),
        id: m.id.trim(),
        provider: m.provider,
        ...(m.contextWindow ? { context_window: m.contextWindow } : {}),
        ...(m.maxTokens ? { max_tokens: m.maxTokens } : {}),
        ...(typeof m.vision === "boolean" ? { vision: m.vision } : {}),
      })),
  };
}

// The key values are collected separately from the file: they become env
// entries in the request, not fields in providers.json.
function buildEnv(): Record<string, string> {
  const env: Record<string, string> = {};
  for (const p of rows.providers) {
    const key = p.keyEnv.trim();
    if (key && p.keyValue.trim()) env[key] = p.keyValue.trim();
  }
  return env;
}

// extra_body is free-form JSON in a textarea, so it is checked before the save
// leaves the browser: a JSON object, not an array or a bare scalar, and nothing
// the strict server decoder would have to re-reject after a round trip.
function validateExtraBody(): string {
  for (const p of rows.providers) {
    const text = p.extraBody.trim();
    if (!text) continue;
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      return t("settingsDialog.extraBodyBadJson", { name: p.name || "?" });
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return t("settingsDialog.extraBodyNotObject", { name: p.name || "?" });
    }
  }
  return "";
}

async function save() {
  saving.value = true;
  warnings.value = [];
  try {
    let res;
    // The last-edited view is what the user means; the other may be behind
    // and is labelled as such rather than silently overridden.
    if (lastEdited.value === "raw") {
      res = await api.saveConfig({ raw: rawText.value });
    } else {
      const bad = validateExtraBody();
      if (bad) {
        ElMessage.error(bad);
        return;
      }
      res = await api.saveConfig({ file: buildFile(), env: buildEnv() });
    }
    warnings.value = res.warnings ?? [];
    if (warnings.value.length) {
      ElMessage.warning(t("settingsDialog.savedWithWarnings", { n: warnings.value.length }));
    } else {
      ElMessage.success(t("settingsDialog.saved"));
    }
    emit("saved");
    // GitHub's settings rule: saving keeps you on the page. A reload (not a
    // close) re-syncs what the server normalized — configured flags, the
    // trimmed file — and refreshes the snapshots, so the dots go out.
    await load();
  } catch (err) {
    ElMessage.error(t("settingsDialog.saveFailed", { msg: err instanceof Error ? err.message : String(err) }));
  } finally {
    saving.value = false;
  }
}

function downloadSample() {
  // The bailian gateway is the shape worth handing a newcomer: an intranet
  // endpoint with a vendor header, beside three ordinary https providers, so
  // both grammars are on the page. Mirrors 补充资料/百炼专属版-providers示例.json.
  const sample = {
    default: "qwen-max",
    providers: {
      bailian: {
        base_url: "http://10.0.0.1:8080/xlm-gateway-xxxx/sfm-api-gateway/gateway/compatible-mode/v1",
        key_env: "BAILIAN_API_KEY",
        headers: { "X-DashScope-SSE": "enable" },
        extra_body: { parameters: {} },
      },
      zhipu: { base_url: "https://open.bigmodel.cn/api/coding/paas/v4", key_env: "ZHIPU_API_KEY" },
    },
    models: [
      { id: "qwen-max", provider: "bailian", aliases: ["qwen"], context_window: 32768, max_tokens: 8192 },
      { id: "glm-5.3", provider: "zhipu", aliases: ["glm"], context_window: 1048576, max_tokens: 16384 },
    ],
  };
  const blob = new Blob([JSON.stringify(sample, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "providers.sample.json";
  a.click();
  URL.revokeObjectURL(a.href);
}

// ── curl 预览 ────────────────────────────────────────────────────────────────

const curlVisible = ref(false);
const curlRow = ref<ProviderRow | null>(null);
const curlMetadata = ref(false);
const metadataProbe = ref<InstanceType<typeof ModelMetadataProbe> | null>(null);
// Non-streaming by default: a single JSON answer is easier to eyeball. The
// toggle reproduces the agent's real streaming shape (curl -N, the Accept
// header, stream_options) for checking a gateway's SSE path.
const curlStream = ref(false);
// The vision variant of the same preview/send: checked, the body becomes the
// probe's first image question and the send button runs the three-question
// probe. Mutually exclusive with streaming — the grader reads one JSON body.
const curlVision = ref(false);

// The probe's own material (asks + inline image data URLs), fetched once per
// dialog open so the preview shows bytes the server will actually send.
const visionPreview = ref<VisionProbePreview["questions"]>([]);

// Checking one mode unchecks the other and drops both results: whatever is on
// screen must belong to the mode the preview is showing.
watch(curlStream, (v) => {
  if (v && curlVision.value) curlVision.value = false;
  resetProbe();
});
watch(curlVision, (v) => {
  if (v && curlStream.value) curlStream.value = false;
  resetProbe();
});

// Mirrors the server's conventional fallback: a provider with key_env left
// empty resolves <NAME>_API_KEY — the naming deploy.env already follows.
function conventionalKeyEnv(name: string): string {
  const n = name.trim().toUpperCase().replace(/[^A-Z0-9_]/g, "_");
  return n ? n + "_API_KEY" : "";
}

// Shell quoting. dq escapes what a double-quoted argument must not pass
// through ($, ", \); sq wraps single quotes, the only safe home for JSON.
function dq(s: string): string {
  return '"' + s.replace(/([$"\\])/g, "\\$1") + '"';
}
function sq(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}

// The model the probe and the preview agree on: the provider's default when it
// has one, else its first listed model.
function modelFor(p: ProviderRow): string {
  const mine = rows.models.filter((m) => m.provider === p.name);
  return (
    (defaultModel.value && mine.some((m) => m.id === defaultModel.value) ? defaultModel.value : "") ||
    mine[0]?.id ||
    "MODEL_ID"
  );
}

// The curl a person can run against one provider: same URL join, same header
// order (custom headers after the standard pair, so they win collisions), same
// extra_body-wins merge as the wire request — carried on a minimal body,
// because the point is to read the gateway's answer, not to replay an agent
// turn. Reads the live form state, so unsaved edits show, and recomputes when
// the streaming toggle flips.
function buildCurl(p: ProviderRow): string {
  const base = p.baseUrl.trim().replace(/\/+$/, "");
  const model = modelFor(p);
  // The real request's key: typed this visit wins (fresher than the loaded
  // snapshot), then the live value the server holds — a placeholder only when
  // neither exists, which is a configuration with no key anywhere.
  const key = p.keyValue.trim() || p.resolvedKey;
  const envName = p.keyEnv.trim() || conventionalKeyEnv(p.name) || "API_KEY";
  const auth = key ? dq(`Authorization: Bearer ${key}`) : `"Authorization: Bearer $${envName}"`;
  const parts = [
    `curl ${curlStream.value ? "-N " : ""}-X POST ${dq(base + "/chat/completions")}`,
    ...(curlStream.value ? [`-H ${dq("Accept: text/event-stream")}`] : []),
    `-H ${auth}`,
    `-H ${dq("Content-Type: application/json")}`,
  ];
  for (const h of p.headers) {
    if (h.k.trim()) parts.push(`-H ${dq(`${h.k.trim()}: ${h.v}`)}`);
  }
  // The vision mode previews the probe's first question verbatim — ask, the
  // inline base64 image, no stream — the exact body the server sends. It is
  // the longest thing in the dialog by far (the image is real bytes); the
  // pre block wraps and scrolls, and honesty beats brevity here.
  const body: Record<string, unknown> = curlVision.value
    ? {
        model,
        messages: [
          {
            role: "user",
            content: [
              { type: "text", text: visionPreview.value[0]?.ask ?? "（探测题加载中）" },
              { type: "image_url", image_url: { url: visionPreview.value[0]?.image_url ?? "" } },
            ],
          },
        ],
        stream: false,
      }
    : {
        model,
        messages: [{ role: "user", content: "ping" }],
        stream: curlStream.value,
      };
  if (curlStream.value) body.stream_options = { include_usage: true };
  if (p.extraBody.trim()) {
    try {
      const parsed: unknown = JSON.parse(p.extraBody);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        Object.assign(body, parsed); // custom keys win, same rule as the server merge
      }
    } catch {
      // save()'s check reports this; the preview just omits the broken value
    }
  }
  parts.push(`-d ${sq(JSON.stringify(body))}`);
  return parts.join(" \\\n  ");
}

const curlName = computed(() => curlRow.value?.name || "?");
const curlText = computed(() => (curlRow.value ? buildCurl(curlRow.value) : ""));
const curlTokens = computed(() => highlightLines(curlText.value, "shell"));

// ── Sending it for real ──────────────────────────────────────────────────────
// The probe goes through the server, not the browser: gateways live on
// networks only the server reaches, and custom headers would drown in CORS
// preflights anyway. The response block shows what came back.

const probing = ref(false);
const probe = ref<ConfigProbeResult | null>(null);

function resetProbe() {
  probe.value = null;
  visionRes.value = null;
}

async function sendProbe() {
  if (curlMetadata.value) { await metadataProbe.value?.send(); return; }
  const p = curlRow.value;
  if (!p || probing.value) return;
  // One send button, two modes: the vision checkbox decides which probe the
  // press runs; everything else about the send (key, headers, extra body) is
  // shared.
  if (curlVision.value) return sendVisionProbe();
  probing.value = true;
  probe.value = null;
  try {
    let extra: Record<string, unknown> = {};
    if (p.extraBody.trim()) {
      try {
        const parsed: unknown = JSON.parse(p.extraBody);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          extra = parsed as Record<string, unknown>;
        }
      } catch {
        // already warned when the dialog opened; the request just omits it
      }
    }
    const headers: Record<string, string> = {};
    for (const h of p.headers) {
      if (h.k.trim()) headers[h.k.trim()] = h.v;
    }
    probe.value = await api.probeConfig({
      name: p.name,
      base_url: p.baseUrl.trim(),
      key_env: p.keyEnv.trim(),
      key: p.keyValue.trim(), // typed this visit; the server falls back to key_env
      model: modelFor(p),
      headers,
      extra_body: extra,
      stream: curlStream.value,
    });
  } catch (err) {
    probe.value = { ok: false, error: err instanceof Error ? err.message : String(err) };
  } finally {
    probing.value = false;
  }
}

// A JSON answer is pretty-printed for reading; SSE and anything else stay raw.
const prettyProbeBody = computed(() => {
  const raw = probe.value?.body ?? "";
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
});

// ── The vision probe ─────────────────────────────────────────────────────────
// The connectivity probe above says the gateway answers; this one says
// whether the model reads pixels — three multiple-choice questions about
// embedded test images (colour / digit / count), sent through the same
// server-side path with the same key resolution, graded on return. The
// verdict is what the model's "vision" switch in the editor wants to be.

const visionProbing = ref(false);
const visionRes = ref<VisionProbeResult | null>(null);

async function sendVisionProbe() {
  const p = curlRow.value;
  if (!p || visionProbing.value) return;
  visionProbing.value = true;
  visionRes.value = null;
  try {
    let extra: Record<string, unknown> = {};
    if (p.extraBody.trim()) {
      try {
        const parsed: unknown = JSON.parse(p.extraBody);
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          extra = parsed as Record<string, unknown>;
        }
      } catch {
        // save()'s check reports this; the request just omits it
      }
    }
    const headers: Record<string, string> = {};
    for (const h of p.headers) {
      if (h.k.trim()) headers[h.k.trim()] = h.v;
    }
    visionRes.value = await api.visionProbe({
      name: p.name,
      base_url: p.baseUrl.trim(),
      key_env: p.keyEnv.trim(),
      key: p.keyValue.trim(),
      model: modelFor(p),
      headers,
      extra_body: extra,
    });
  } catch (err) {
    visionRes.value = {
      ok: false,
      questions: [],
      error: err instanceof Error ? err.message : String(err),
    };
  } finally {
    visionProbing.value = false;
  }
}

// The ask's first line is the question proper; the options follow on their own lines.
function askLabel(q: VisionProbeResult["questions"][number]): string {
  return q.ask.split("\n")[0];
}

function openCurl(p: ProviderRow) {
  if (p.extraBody.trim()) {
    try {
      const parsed: unknown = JSON.parse(p.extraBody);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("not an object");
    } catch {
      ElMessage.warning(t("settingsDialog.extraBodyBadJson", { name: p.name || "?" }));
    }
  }
  curlRow.value = p;
  curlMetadata.value = false;
  resetProbe();
  curlVisible.value = true;
  // The vision preview's material: one small fetch per open, best-effort —
  // a failure leaves the placeholder text, and the probe itself still works.
  api
    .visionProbeQuestions()
    .then((r) => (visionPreview.value = r.questions))
    .catch(() => {});
}

async function copyCurl() {
  if (curlMetadata.value) { await metadataProbe.value?.copy(); return; }
  try {
    await navigator.clipboard.writeText(curlText.value);
    ElMessage.success(t("common.copied"));
  } catch {
    ElMessage.error(t("settingsDialog.copyFailed"));
  }
}

// ── Import a providers.json from disk into the JSON editor ──────────────────

const jsonFileInput = ref<HTMLInputElement | null>(null);

function pickImport() {
  jsonFileInput.value?.click();
}

async function onImportFile(e: Event) {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = ""; // allows picking the same file again after a failure
  if (!file) return;
  const text = await file.text().catch(() => "");
  try {
    // Parsed here only to reject a non-JSON file before it overwrites the
    // editor; the strict rules (unknown fields, dangling references) stay the
    // server's, applied on save — the same ones a restart would apply.
    JSON.parse(text);
  } catch {
    ElMessage.error(t("settingsDialog.importBadFile"));
    return;
  }
  rawText.value = text;
  ElMessage.success(t("settingsDialog.imported"));
}

// ── 打开与模块路由 / Open & module routing ────────────────────────────────────
function hasUnsavedChanges(): boolean {
  const models = snapshotFormJSON.value !== "" && configDirty.value;
  const general = storageDirty.value || attachDirty.value || sandboxDirty.value || iconLibraryDirty.value;
  const mcp = mcpDirty.value || (mcpSrvRowSnapshot.value !== "" && mcpSrvDirty.value);
  if (activeSection.value === "models") return models;
  if (activeSection.value === "general") return general;
  if (activeSection.value === "mcp") return mcp;
  if (activeSection.value === "account") return false;
  return models || general || mcp;
}

async function confirmLeave(): Promise<boolean> {
  if (!visible.value) return true;
  let allowed = false;
  await tryClose(() => { allowed = true; });
  return allowed;
}

function preventDiscard(event: BeforeUnloadEvent) {
  if (props.embedded && visible.value && hasUnsavedChanges()) {
    event.preventDefault();
    event.returnValue = "";
  }
}

let unregisterNavigation: (() => void) | undefined;
onMounted(() => {
  if (props.embedded) {
    unregisterNavigation = registerStudioNavigationGuard(confirmLeave);
    window.addEventListener("beforeunload", preventDiscard);
    // The Skill monitor now lives on its own management page. Its "view this
    // trace in the MCP monitor" link parks the trace id here and navigates to
    // the MCP module; consume it once so the correlation survives the move.
    try {
      const trace = sessionStorage.getItem("pogo:skill-mcp-trace");
      if (trace) {
        mcpMonitorTrace.value = trace;
        sheet.value = "mcpMonitor";
        sessionStorage.removeItem("pogo:skill-mcp-trace");
      }
    } catch {
      // private mode etc. — arrive without the correlation, that is all.
    }
  }
});
onBeforeUnmount(() => {
  unregisterNavigation?.();
  window.removeEventListener("beforeunload", preventDiscard);
});

// The dialog loads every store on open, as it always has. A Studio embed is
// mounted already open (modelValue never moves false → true), so the load
// pass also runs immediately — and loads only the active module's stores:
// account and general never fetch unrelated privileged config to decide what
// to show; the per-sheet loaders and the auth-mode call are the same ones.
function loadSection(s: SettingsSection) {
  if (s === "models") void load();
  else if (s === "mcp") void loadMCP();
  else if (s === "account") loadAuthMode();
  else {
    loadAuthMode(false);
    void loadStorage();
    void loadSandbox();
    void loadAttach();
  }
}

// Lives at the end of setup on purpose: immediate: true runs this during
// setup, and the loaders below touch refs declared throughout the file.
watch(
  visible,
  (v) => {
    if (!v) {
      rawFullscreen.value = false;
      spillBrowserVisible.value = false;
      return;
    }
    if (activeSection.value) {
      sheet.value = sheetForSection(sheet.value, activeSection.value);
      loadSection(activeSection.value);
      return;
    }
    sheet.value = "service";
    anchor.value = "providers";
    void load();
    void loadStorage();
    void loadSandbox();
    void loadAttach();
    void loadMCP();
    void loadAuthMode();
  },
  { immediate: true },
);

// Studio's sidebar switches modules by changing `section`. The sheet follows
// unless it already belongs to the new module — the trace handoff from the
// relocated Skill monitor sets mcpMonitor before the section arrives, so the
// aligned section must keep that target rather than resetting to the first.
watch(activeSection, (s, prev) => {
  if (!s || !visible.value) return;
  sheet.value = sheetForSection(sheet.value, s);
  if (s !== prev) loadSection(s);
});
</script>

<template>
  <SettingsContainer
    v-model="visible"
    :embedded="embedded"
    :footer-visible="!embedded || ['service', 'storage', 'sandbox', 'attach'].includes(sheet) || (sheet === 'mcp' && mcpEditing !== null)"
    :title="t('settingsDialog.title')"
    :before-close="tryClose"
  >
    <!-- The blank loading/error states are for a pane with nothing on it yet.
         Once a file is on screen, re-loads (module re-entry, the post-save
         re-sync) refresh in place — swapping the whole pane for one note is
         the flash this gate used to cause. -->
    <div v-if="(!embedded || activeSection === 'models') && !configReady && loading" class="note">{{ t("common.loading") }}</div>
    <div v-else-if="(!embedded || activeSection === 'models') && !configReady && loadError" class="note bad">{{ t("settingsDialog.loadFailed") }}{{ loadError }}</div>
    <template v-else>
      <!-- A re-load failed with the previous file still showing: say so above
           the pane instead of replacing it. -->
      <div v-if="(!embedded || activeSection === 'models') && loadError" class="note bad">{{ t("settingsDialog.loadFailed") }}{{ loadError }}</div>
      <!-- GitHub-settings shaped: the nav is an index of pages grouped by
           concern; the pane is one page at a time with its own header. -->
      <div class="settings-split">
        <nav v-if="activeSection !== 'account'" class="settings-nav" aria-label="设置子模块">
          <!-- The service group's entries are anchors into its one sheet; the
               storage entry is a sheet of its own. Visually one list. -->
          <!-- Embedded in Studio, the shell's sidebar owns the module level,
               so each group below shows only while its module is active. -->
          <template v-if="navShows('service')">
            <div class="nav-group">{{ t("settingsDialog.navModelService") }}</div>
            <button
              class="nav-item"
              :class="{ on: sheet === 'service' && anchor === 'providers' }"
              :aria-current="sheet === 'service' && anchor === 'providers'"
              @click="goAnchor('providers')"
            >
              <span>{{ t("settingsDialog.providers") }}</span>
              <span v-if="configDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'service' && anchor === 'models' }"
              :aria-current="sheet === 'service' && anchor === 'models'"
              @click="goAnchor('models')"
            >
              <span>{{ t("settingsDialog.modelSelection") }}</span>
              <span v-if="configDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'service' && anchor === 'config' }"
              :aria-current="sheet === 'service' && anchor === 'config'"
              @click="goAnchor('config')"
            >
              <span>{{ t("settingsDialog.configFile") }}</span>
              <span v-if="configDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
          </template>
          <template v-if="navShows('storage')">
            <div class="nav-group">{{ t("settingsDialog.navWorkspace") }}</div>
            <button
              class="nav-item"
              :class="{ on: sheet === 'storage' }"
              :aria-current="sheet === 'storage'"
              @click="sheet = 'storage'"
            >
              <span>{{ t("settingsDialog.storage") }}</span>
              <span v-if="storageDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'sandbox' }"
              :aria-current="sheet === 'sandbox'"
              @click="sheet = 'sandbox'"
            >
              <span>{{ t("settingsDialog.sandbox") }}</span>
              <span v-if="sandboxDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'attach' }"
              :aria-current="sheet === 'attach'"
              @click="sheet = 'attach'"
            >
              <span>{{ t("settingsDialog.attach") }}</span>
              <span v-if="attachDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button class="nav-item" :class="{ on: sheet === 'icons' }" :aria-current="sheet === 'icons'" @click="sheet = 'icons'">
              <span>{{ t('iconLibrary.title') }}</span><span v-if="iconLibraryDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
          </template>
          <template v-if="navShows('account')">
            <div class="nav-group">{{ t("settingsDialog.navAccount") }}</div>
            <button
              class="nav-item"
              :class="{ on: sheet === 'account' }"
              :aria-current="sheet === 'account'"
              @click="sheet = 'account'"
            >
              <span>{{ t("settingsDialog.accountSecurity") }}</span>
            </button>
          </template>
          <!-- mcp.json's two sections are two pages: connecting out (the card
               wall and its edit form) and serving others. -->
          <template v-if="navShows('mcp')">
            <div class="nav-group">{{ t("settingsDialog.navMCP") }}</div>
            <button
              class="nav-item"
              :class="{ on: sheet === 'mcp' }"
              :aria-current="sheet === 'mcp'"
              @click="sheet = 'mcp'"
            >
              <span>{{ t("settingsDialog.mcpNavClient") }}</span>
              <span v-if="mcpDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'mcpServe' }"
              :aria-current="sheet === 'mcpServe'"
              @click="sheet = 'mcpServe'"
            >
              <span>{{ t("settingsDialog.mcpNavServe") }}</span>
              <span v-if="mcpSrvDirty" class="dirty-dot" :title="t('settingsDialog.unsavedDot')" />
            </button>
            <button
              class="nav-item"
              :class="{ on: sheet === 'mcpMonitor' }"
              :aria-current="sheet === 'mcpMonitor'"
              @click="mcpMonitorTrace = ''; sheet = 'mcpMonitor'"
            >
              <span>{{ t("mcpMonitor.title") }}</span>
            </button>
          </template>
          <!-- Embedded mode pins this sheet's actions to the tab row's right
               end, so they stay put while the pane scrolls; the fullscreen
               dialog keeps them in the section head below. -->
          <div v-if="embedded && sheet === 'mcpServe'" class="nav-actions">
            <button class="mini" @click="openMCPExport('serve')">{{ t("settingsDialog.mcpExportServe") }}</button>
            <button class="mini" :disabled="mcpSaving || mcpSrvEnvTaken || !mcpSrvDirty" @click="saveMCPServe">
              {{ t("common.save") }}
            </button>
          </div>
        </nav>

        <div ref="paneEl" class="settings-pane form-body" @scroll.passive="onPaneScroll">
          <!-- ── 模型服务:接口 / 模型 / 配置文件,一份 providers.json 的三个视图 ── -->
          <template v-if="sheet === 'service'">
            <section data-anchor="providers" class="pane-page">
              <header class="pane-head">
                <h3>{{ t("settingsDialog.providers") }}</h3>
                <p>{{ t("settingsDialog.descProviders") }}</p>
              </header>
              <div class="sect">
                <!-- No inner title — the page header already names the sheet;
                     the row keeps only the add button, pinned right. -->
                <div class="sect-head end">
                  <button class="mini" @click="addProvider">
                    <el-icon><Plus /></el-icon>{{ t("settingsDialog.addProvider") }}
                  </button>
                </div>
                <div v-for="(p, i) in rows.providers" :key="i" class="prow">
                  <div class="prow-line">
                    <label class="fld grow">
                      <span>{{ t("settingsDialog.providerName") }}</span>
                      <input v-model="p.name" type="text" placeholder="zhipu" />
                    </label>
                    <label class="fld grow">
                      <span>{{ t("settingsDialog.baseUrl") }}</span>
                      <input v-model="p.baseUrl" type="text" placeholder="https://api.example.com/v1" />
                    </label>
                    <button class="mini" :title="t('settingsDialog.curlBtn')" @click="openCurl(p)">
                      {{ t("settingsDialog.curlBtn") }}
                    </button>
                    <button class="icon-btn del" :title="t('common.delete')" @click="rows.providers.splice(i, 1)">
                      <el-icon><Delete /></el-icon>
                    </button>
                  </div>
                  <div class="prow-line">
                    <label class="fld grow">
                      <span>{{ t("settingsDialog.keyEnv") }}</span>
                      <input v-model="p.keyEnv" type="text" placeholder="MY_PROVIDER_API_KEY" />
                    </label>
                    <label class="fld grow">
                      <span>{{ t("settingsDialog.keyValue") }}</span>
                      <input
                        v-model="p.keyValue"
                        type="password"
                        autocomplete="new-password"
                        :placeholder="p.configured ? t('settingsDialog.keySet') : t('settingsDialog.keyUnset')"
                      />
                    </label>
                  </div>
                  <!-- Vendor switches: extra headers (DashScope's SSE flag being the
                       case at hand) and the plain-http opt-in for intranet gateways
                       that are neither https nor a private address. -->
                  <div class="prow-line adv">
                    <div class="hdrs grow">
                      <span class="lbl">{{ t("settingsDialog.headers") }}</span>
                      <div v-for="(h, j) in p.headers" :key="j" class="hdr-row">
                        <input v-model="h.k" type="text" placeholder="X-DashScope-SSE" spellcheck="false" />
                        <input v-model="h.v" type="text" placeholder="enable" spellcheck="false" />
                        <button class="icon-btn del" :title="t('common.delete')" @click="p.headers.splice(j, 1)">
                          <el-icon><Delete /></el-icon>
                        </button>
                      </div>
                      <button class="mini" @click="p.headers.push({ k: '', v: '' })">
                        <el-icon><Plus /></el-icon>{{ t("settingsDialog.addHeader") }}
                      </button>
                    </div>
                    <label class="fld chk">
                      <input v-model="p.allowInsecureHttp" type="checkbox" />
                      <span>{{ t("settingsDialog.allowInsecureHttp") }}</span>
                    </label>
                  </div>
                  <div class="prow-line adv">
                    <label class="fld grow">
                      <span>{{ t("settingsDialog.extraBody") }}</span>
                      <textarea
                        v-model="p.extraBody"
                        class="extra-body"
                        rows="2"
                        spellcheck="false"
                        placeholder='{"parameters":{}}'
                      />
                    </label>
                  </div>
                </div>
                <p class="hint">{{ t("settingsDialog.keyHint") }}</p>
                <p class="hint">{{ t("settingsDialog.headerHint") }}</p>
              </div>
            </section>

            <section data-anchor="models" class="pane-page">
              <header class="pane-head">
                <h3>{{ t("settingsDialog.modelSelection") }}</h3>
                <p>{{ t("settingsDialog.descModels") }}</p>
              </header>
              <!-- One big frame holding two stacked sub-frames: the default and
                   vision picks above, the model catalog below. -->
              <div class="sect nest">
                <div class="sect">
                  <label class="fld">
                    <span>{{ t("settingsDialog.defaultModel") }}</span>
                    <select v-model="defaultModel">
                      <option value="">{{ t("settingsDialog.defaultFirst") }}</option>
                      <option v-for="id in modelIds" :key="id" :value="id">{{ id }}</option>
                    </select>
                  </label>
                  <label class="fld">
                    <span>{{ t("settingsDialog.visionModel") }}</span>
                    <select v-model="visionModel">
                      <option value="">{{ t("settingsDialog.visionNone") }}</option>
                      <option v-for="id in modelIds" :key="id" :value="id">{{ id }}</option>
                    </select>
                  </label>
                  <p class="hint">{{ t("settingsDialog.visionHint") }}</p>
                </div>
                <div class="sect">
                  <div class="sect-head">
                    <span>{{ t("settingsDialog.models") }}</span>
                    <button class="mini" @click="addModel">
                      <el-icon><Plus /></el-icon>{{ t("settingsDialog.addModel") }}
                    </button>
                  </div>
                  <div v-for="(m, i) in rows.models" :key="i" class="prow">
                    <div class="prow-line">
                      <label class="fld grow">
                        <span>ID</span>
                        <input v-model="m.id" type="text" placeholder="glm-5.3" />
                      </label>
                      <label class="fld">
                        <span>{{ t("settingsDialog.modelProvider") }}</span>
                        <select v-model="m.provider">
                          <option v-for="n in providerNames" :key="n" :value="n">{{ n }}</option>
                        </select>
                      </label>
                      <label class="fld num">
                        <span>{{ t("settingsDialog.contextWindow") }}</span>
                        <input v-model.number="m.contextWindow" type="number" min="0" placeholder="200000" />
                      </label>
                      <label class="fld num">
                        <span>{{ t("settingsDialog.maxTokens") }}</span>
                        <input v-model.number="m.maxTokens" type="number" min="0" placeholder="8192" />
                      </label>
                      <!-- Tri-state 视觉: auto follows the id's naming, the
                           explicit choices override it — one small select, no
                           checkbox ambiguity between unset and false. -->
                      <label class="fld">
                        <span>{{ t("settingsDialog.modelVision") }}</span>
                        <select :value="m.vision === undefined ? '' : m.vision ? '1' : '0'" @change="m.vision = ($event.target as HTMLSelectElement).value === '' ? undefined : ($event.target as HTMLSelectElement).value === '1'">
                          <option value="">{{ t("settingsDialog.visionAuto") }}</option>
                          <option value="1">{{ t("settingsDialog.visionYes") }}</option>
                          <option value="0">{{ t("settingsDialog.visionNo") }}</option>
                        </select>
                      </label>
                      <button class="icon-btn del" :title="t('common.delete')" @click="rows.models.splice(i, 1)">
                        <el-icon><Delete /></el-icon>
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </section>

            <section data-anchor="config" class="pane-page">
              <header class="pane-head">
                <h3>{{ t("settingsDialog.configFile") }}</h3>
                <p>{{ t("settingsDialog.descConfig") }}</p>
              </header>
              <div class="path-row" :title="path">
                <el-icon><Setting /></el-icon>
                <span class="path">{{ path }}</span>
              </div>
              <div v-if="parseError" class="note bad">{{ t("settingsDialog.parseError") }}</div>
              <!-- The raw view shows the file on disk; form edits are not in it
                   until saved, and it says so rather than pretending. -->
              <div v-if="rawBehind" class="note">{{ t("settingsDialog.rawBehindNote") }}</div>
              <!-- Toolbar above the editor: import/sample on the left. The
                   view toggle and the fullscreen button are the box's own
                   tools row, floating to its top-right corner in fullscreen. -->
              <div class="hint-row">
                <div class="hint-actions">
                  <button class="mini" @click="pickImport">{{ t("settingsDialog.importFile") }}</button>
                  <button class="mini" @click="downloadSample">{{ t("settingsDialog.sampleFile") }}</button>
                </div>
              </div>
              <!-- The raw file's two faces share one box: the pretty tree for
                   reading, the textarea for editing — both bind the same
                   rawText, so dirty tracking and the save path are untouched. -->
              <div class="raw-wrap" :class="{ full: rawFullscreen }" tabindex="-1" @keydown="onRawKeydown">
                <div class="raw-tools">
                  <div class="raw-toggle">
                    <button class="mode" :class="{ on: rawView === 'pretty' }" @click="rawView = 'pretty'">
                      {{ t("settingsDialog.viewPretty") }}
                    </button>
                    <button class="mode" :class="{ on: rawView === 'source' }" @click="rawView = 'source'">
                      {{ t("settingsDialog.viewSource") }}
                    </button>
                  </div>
                  <button
                    class="raw-full-btn"
                    :title="rawFullscreen ? t('settingsDialog.exitFullscreen') : t('settingsDialog.fullscreen')"
                    :aria-label="rawFullscreen ? t('settingsDialog.exitFullscreen') : t('settingsDialog.fullscreen')"
                    @click="rawFullscreen = !rawFullscreen"
                  >
                    <el-icon><ScaleToOriginal v-if="rawFullscreen" /><FullScreen v-else /></el-icon>
                  </button>
                </div>
                <vue-json-pretty
                  v-if="showRawPretty"
                  class="raw-pretty"
                  :data="rawParsed.data"
                  :show-length="true"
                  :show-icon="true"
                />
                <textarea v-else v-model="rawText" class="raw-editor" spellcheck="false" />
              </div>
              <input
                ref="jsonFileInput"
                type="file"
                accept="application/json,.json"
                style="display: none"
                @change="onImportFile"
              />
            </section>
          </template>

          <!-- ── 存储 ── -->
          <section v-else-if="sheet === 'storage'" class="pane-page">
            <header class="pane-head">
              <h3>{{ t("settingsDialog.storage") }}</h3>
              <p>{{ t("settingsDialog.descStorage") }}</p>
            </header>
            <div v-if="storage" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.spill") }}</span>
                <div class="spill-actions">
                  <button v-if="meAdmin" class="mini" data-testid="spill-browse-open" @click="spillBrowserVisible = true">
                    {{ t("spillBrowser.browse") }}
                  </button>
                  <button class="mini" :disabled="pruning" @click="pruneSpillNow">
                    {{ t("settingsDialog.pruneNow") }}
                  </button>
                </div>
              </div>
              <p class="hint spill-root"><span>{{ t("spillBrowser.rootLabel") }}</span><code>{{ storage.dir }}</code></p>
              <div class="prow-line">
                <label class="fld num">
                  <span>{{ t("settingsDialog.storageRetention") }}</span>
                  <input v-model.number="storage.days" type="number" min="0" max="3650" step="1" />
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.storageRetentionHint") }}</p>
              <p class="hint" :title="storage.dir">
                {{ t("settingsDialog.storageUsage", { files: storage.files, size: formatMB(storage.bytes) }) }}
              </p>
              <p class="hint">{{ lastSweepText }}</p>
              <p v-if="storageError" class="note bad">{{ t("settingsDialog.storageLoadFailed") }}{{ storageError }}</p>
            </div>
            <div v-if="storage" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.turnsTitle") }}</span>
                <span class="hint">{{ turnsEffectiveText }}</span>
              </div>
              <div class="prow-line">
                <label class="fld num">
                  <span>{{ t("settingsDialog.turnsField") }}</span>
                  <el-switch v-model="storage.turnsLimited" :active-text="t('settingsDialog.turnsLimitEnabled')" />
                  <input v-if="storage.turnsLimited" v-model.number="storage.turns" type="number" min="1" max="1000" step="1" />
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.turnsHint", { flag: storage.turnsFlag }) }}</p>
            </div>
            <div v-if="storage" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.memoryTitle") }}</span>
                <el-switch
                  :model-value="memoryAuto"
                  :disabled="memoryAutoSaving"
                  data-testid="memory-auto"
                  @change="saveMemoryAuto"
                />
              </div>
              <p class="hint">{{ t("settingsDialog.memoryAutoHint") }}</p>
            </div>
            <div v-else-if="storageError" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.storage") }}</span>
              </div>
              <p class="note bad">{{ t("settingsDialog.storageLoadFailed") }}{{ storageError }}</p>
            </div>
          </section>

          <!-- ── 账户：修改登录密码（模式感知：token 部署显示说明而非表单） ── -->
          <section v-else-if="sheet === 'account'" class="pane-page">
            <header class="pane-head">
              <h3>{{ t("settingsDialog.accountSecurity") }}</h3>
              <p>{{ t("settingsDialog.descAccount") }}</p>
            </header>
            <div v-if="meAdmin" class="sect">
              <div class="sect-title-row">
                <h4>{{ t("settingsDialog.accountsTitle") }}</h4>
                <button class="mini" @click="loadAccounts">{{ t("settingsDialog.accountsRefresh") }}</button>
              </div>
              <p v-if="accountsError" class="hint rejected">{{ accountsError }}</p>
              <table v-else class="acct-table">
                <thead>
                  <tr>
                    <th>{{ t("settingsDialog.acctUser") }}</th>
                    <th>{{ t("settingsDialog.acctRole") }}</th>
                    <th>{{ t("settingsDialog.acctToken") }}</th>
                    <th>{{ t("settingsDialog.acctCreated") }}</th>
                    <th>{{ t("settingsDialog.acctActions") }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="a in accounts" :key="a.username">
                    <td>{{ a.username }}</td>
                    <td>{{ a.admin ? t("settingsDialog.roleAdmin") : t("settingsDialog.roleUser") }}</td>
                    <td>{{ a.has_token ? t("settingsDialog.tokenYes") : t("settingsDialog.tokenNo") }}</td>
                    <td>{{ a.created || "—" }}</td>
                    <td>
                      <button class="mini" @click="beginPwChange(a)">{{ t("settingsDialog.pwdChangeBtn") }}</button>
                      <button
                        v-if="!a.admin"
                        class="mini danger"
                        :disabled="deletingAccount === a.username"
                        @click="removeAccount(a)"
                      >
                        {{ deletingAccount === a.username ? t("settingsDialog.acctDeleting") : t("common.delete") }}
                      </button>
                    </td>
                  </tr>
                  <tr v-if="accounts.length === 0">
                    <td colspan="5" class="acct-empty">{{ t("settingsDialog.accountsEmpty") }}</td>
                  </tr>
                </tbody>
              </table>
              <p class="hint">{{ t("settingsDialog.accountsHint") }}</p>

              <!-- 行内改密表单：点哪行的按钮，改哪个账号。 -->
              <div v-if="pwTarget" class="pw-box">
                <div class="sect-title-row">
                  <h4>{{ t("settingsDialog.pwdChangeFor", { name: pwTarget.username }) }}</h4>
                </div>
                <div v-if="pwTarget.admin" class="prow-line">
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.pwdCurrent") }}</span>
                    <input
                      v-model="pwdCurrent"
                      type="password"
                      autocomplete="current-password"
                      :placeholder="t('settingsDialog.pwdCurrentHint')"
                    />
                  </label>
                </div>
                <div class="prow-line">
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.pwdNew") }}</span>
                    <input
                      v-model="pwdNext"
                      type="password"
                      autocomplete="new-password"
                      :placeholder="t('settingsDialog.pwdNewHint')"
                    />
                  </label>
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.pwdConfirm") }}</span>
                    <input
                      v-model="pwdConfirm"
                      type="password"
                      autocomplete="new-password"
                      @keyup.enter="changePw"
                    />
                  </label>
                </div>
                <div class="hint-row">
                  <div class="hint-actions">
                    <button
                      class="mini"
                      :disabled="changingPw || !pwdNext || !pwdConfirm || pwdNext !== pwdConfirm"
                      @click="changePw"
                    >
                      {{ changingPw ? t("settingsDialog.pwdChanging") : t("settingsDialog.pwdChangeBtn") }}
                    </button>
                    <button class="mini" :disabled="changingPw" @click="cancelPwChange">
                      {{ t("settingsDialog.pwdCancel") }}
                    </button>
                  </div>
                </div>
                <p class="hint">{{ pwTarget.admin ? t("settingsDialog.pwdAdminHint") : t("settingsDialog.pwdHint") }}</p>
              </div>
            </div>
            <div v-else class="sect">
              <p class="hint">{{ t("settingsDialog.pwdModeOff") }}</p>
            </div>
          </section>

          <!-- ── 沙箱：OS 级约束的运行期开关 ── -->
          <section v-else-if="sheet === 'sandbox'" class="pane-page">
            <header class="pane-head">
              <h3>{{ t("settingsDialog.sandbox") }}</h3>
              <p>{{ t("settingsDialog.descSandbox") }}</p>
            </header>
            <div v-if="sandbox" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.sandboxMode") }}</span>
              </div>
              <div class="prow-line">
                <label class="fld">
                  <span>{{ t("settingsDialog.sandboxModeLabel") }}</span>
                  <select v-model="sandboxChoice">
                    <option value="">{{ t("settingsDialog.sandboxFollowEnv", { mode: sandbox.env_mode }) }}</option>
                    <option value="enforce">{{ t("settingsDialog.sandboxEnforce") }}</option>
                    <option value="degrade">{{ t("settingsDialog.sandboxDegrade") }}</option>
                    <option value="off">{{ t("settingsDialog.sandboxOff") }}</option>
                  </select>
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.sandboxModesHint") }}</p>
              <p class="hint">{{ sandboxStatusText }}</p>
              <!-- The escape hatch is deliberate, so the cost of using it is
                   stated before it is saved, not discovered after. -->
              <p v-if="sandboxChoiceUnconfined" class="note bad">{{ t("settingsDialog.sandboxWarnUnconfined") }}</p>
              <p v-if="sandboxError" class="note bad">{{ sandboxError }}</p>
            </div>
            <div v-else-if="sandboxError" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.sandbox") }}</span>
              </div>
              <p class="note bad">{{ sandboxError }}</p>
            </div>
          </section>

          <!-- ── 附件:上传白名单的组开关,前后端共用一份列表 ── -->
          <section v-else-if="sheet === 'attach'" class="pane-page">
            <header class="pane-head">
              <h3>{{ t("settingsDialog.attach") }}</h3>
              <p>{{ t("settingsDialog.descAttach") }}</p>
            </header>
            <div v-if="attachPolicy" class="sect">
              <div class="sect-head">
                <span>{{ t("settingsDialog.attachGroups") }}</span>
                <el-input
                  v-model="attachQuery"
                  class="attach-search"
                  size="small"
                  clearable
                  :placeholder="t('settingsDialog.attachSearch')"
                  :prefix-icon="Search"
                />
              </div>
              <!-- Each category is a collapsible card: a tri-state header
                   checkbox over the chips beneath it plus a count badge.
                   Chips replace flat checkboxes — at 60+ options a filled
                   pill scans far better than a wall of ticked boxes. -->
              <div
                v-for="g in attachFiltered"
                :key="g.id"
                class="attach-cat"
                :class="{ danger: DANGER_GROUPS.has(g.id) }"
              >
                <div class="attach-cat-head">
                  <button class="attach-cat-fold" type="button" @click="toggleAttachCollapsed(g.id)">
                    <el-icon :size="12">
                      <ArrowDown v-if="!attachGroupCollapsed(g.id)" /><ArrowRight v-else />
                    </el-icon>
                  </button>
                  <el-checkbox
                    :model-value="groupOn(g.id)"
                    :indeterminate="groupPart(g.id)"
                    @update:model-value="(v: boolean) => toggleGroup(g.id, v)"
                  >
                    {{ t(`settingsDialog.attachGroup_${g.id}`) }}
                  </el-checkbox>
                  <span class="attach-cat-count">{{ groupCount(g.id) }}/{{ g.exts.length }}</span>
                </div>
                <p
                  v-if="DANGER_GROUPS.has(g.id) && !attachGroupCollapsed(g.id)"
                  class="attach-danger-hint"
                  :class="{ hot: groupCount(g.id) > 0 }"
                >
                  {{ t("settingsDialog.attachDangerHint") }}
                </p>
                <div v-show="!attachGroupCollapsed(g.id)" class="attach-chip-grid">
                  <button
                    v-for="e in g.shown"
                    :key="e"
                    type="button"
                    class="attach-chip"
                    :class="{ on: attachPick.has(e) }"
                    @click="toggleExt(e, !attachPick.has(e))"
                  >
                    {{ e.slice(1) }}
                  </button>
                </div>
              </div>
              <p v-if="!attachFiltered.length" class="hint">{{ t("settingsDialog.attachNoMatch") }}</p>
              <div class="sect-head cap-head">
                <span>{{ t("settingsDialog.attachCap") }}</span>
                <span class="attach-cap-value">{{ capLabel(CAP_STOPS[capIdx]) }}</span>
              </div>
              <!-- Index-driven slider: the thumb snaps to the stops above,
                   which is the damping — no 993 MB caps nobody means. -->
              <el-slider
                v-model="capIdx"
                :min="0"
                :max="CAP_STOPS.length - 1"
                :step="1"
                show-stops
                :format-tooltip="(i: number) => capLabel(CAP_STOPS[i])"
              />
              <div class="attach-cap-scale">
                <span>10 MB</span><span>1 GB</span>
              </div>
              <p class="hint">{{ t("settingsDialog.attachHint") }}</p>
              <p v-if="attachError" class="note bad">{{ attachError }}</p>
              <!-- Sticky summary: what the policy currently says and the save
                   that commits it, on screen while the group list scrolls. -->
              <div class="attach-summary">
                <span>{{
                  t("settingsDialog.attachSummary", { n: attachPick.size, cap: capLabel(CAP_STOPS[capIdx]) })
                }}</span>
                <button class="mini" :disabled="attachSaving || !attachDirty" @click="saveAttach">
                  {{ t("settingsDialog.attachSave") }}
                </button>
              </div>
            </div>
            <div v-else-if="attachError" class="sect">
              <p class="note bad">{{ attachError }}</p>
            </div>
          </section>

          <!-- ── MCP 接入（客户端）:servers 节的卡片墙 + 编辑表单 ── -->
          <section v-else-if="sheet === 'mcp'" class="pane-page">
            <header class="pane-head">
              <div class="head-row">
                <h3>{{ t("settingsDialog.mcpClient") }}</h3>
              </div>
              <p>{{ t("settingsDialog.descMcpClient") }}</p>
            </header>
            <div class="path-row" :title="mcpPath">
              <el-icon><Setting /></el-icon>
              <span class="path">{{ mcpPath }}</span>
            </div>
            <div v-if="mcpLoadError" class="note bad">{{ t("settingsDialog.mcpLoadError") }}{{ mcpLoadError }}</div>

            <!-- The add/edit form: a sub-view over the same sheet, with the
                 test-connection button probing the form's current values. -->
            <div v-if="mcpEditing" class="sect">
              <div class="sect-head">
                <span>{{ mcpEditingNew ? t("settingsDialog.mcpAddTitle") : t("settingsDialog.mcpEditTitle", { name: mcpEditing.name }) }}</span>
              </div>
              <div class="prow-line">
                <label class="fld grow">
                  <span>{{ t("settingsDialog.mcpName") }}</span>
                  <input v-model="mcpEditing.name" type="text" :disabled="!mcpEditingNew" placeholder="my-server" spellcheck="false" />
                </label>
                <label class="fld">
                  <span>{{ t("settingsDialog.mcpType") }}</span>
                  <select v-model="mcpEditing.kind">
                    <option value="stdio">{{ t("settingsDialog.mcpKindStdio") }}</option>
                    <option value="http">{{ t("settingsDialog.mcpKindHttp") }}</option>
                  </select>
                </label>
              </div>

              <template v-if="mcpEditing.kind === 'stdio'">
                <div class="prow-line">
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.mcpCommand") }}</span>
                    <input v-model="mcpEditing.command" type="text" placeholder="npx" spellcheck="false" />
                  </label>
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.mcpCwd") }}</span>
                    <input v-model="mcpEditing.cwd" type="text" spellcheck="false" />
                  </label>
                </div>
                <div class="prow-line">
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.mcpArgs") }}</span>
                    <textarea
                      v-model="mcpEditing.argsText"
                      class="extra-body"
                      rows="2"
                      spellcheck="false"
                      :placeholder="'-y\n@modelcontextprotocol/server-filesystem'"
                    />
                  </label>
                </div>
                <p class="hint">{{ t("settingsDialog.mcpArgsHint") }}</p>
                <div class="prow-line adv">
                  <div class="hdrs grow">
                    <span class="lbl">{{ t("settingsDialog.mcpEnv") }}</span>
                    <div v-for="(e, j) in mcpEditing.env" :key="j" class="hdr-row">
                      <input v-model="e.k" type="text" placeholder="API_KEY" spellcheck="false" />
                      <input v-model="e.v" type="text" :placeholder="t('settingsDialog.mcpEnvValHint')" spellcheck="false" />
                      <button class="icon-btn del" :title="t('common.delete')" @click="mcpEditing.env.splice(j, 1)">
                        <el-icon><Delete /></el-icon>
                      </button>
                    </div>
                    <button class="mini" @click="mcpEditing.env.push({ k: '', v: '' })">
                      <el-icon><Plus /></el-icon>{{ t("settingsDialog.mcpAddEnv") }}
                    </button>
                  </div>
                </div>
              </template>

              <template v-else>
                <div class="prow-line">
                  <label class="fld grow">
                    <span>{{ t("settingsDialog.mcpUrl") }}</span>
                    <input v-model="mcpEditing.url" type="text" placeholder="https://example.com/mcp" spellcheck="false" />
                  </label>
                  <label class="fld">
                    <span>{{ t("settingsDialog.mcpTransport") }}</span>
                    <select v-model="mcpEditing.transport">
                      <option value="streamable">{{ t("settingsDialog.mcpTransportStreamable") }}</option>
                      <option value="sse">{{ t("settingsDialog.mcpTransportSse") }}</option>
                    </select>
                  </label>
                </div>
                <div class="prow-line">
                  <div class="fld grow">
                    <span>{{ t("settingsDialog.mcpBearerEnv") }}</span>
                    <el-radio-group v-model="mcpEditing.bearerMode" size="small">
                      <el-radio-button value="env">{{ t("settingsDialog.mcpBearerModeEnv") }}</el-radio-button>
                      <el-radio-button value="direct">{{ t("settingsDialog.mcpBearerModeDirect") }}</el-radio-button>
                    </el-radio-group>
                  </div>
                </div>
                <div class="prow-line">
                  <label v-if="mcpEditing.bearerMode === 'env'" class="fld grow">
                    <input
                      v-model="mcpEditing.bearerTokenEnv"
                      type="text"
                      :placeholder="t('settingsDialog.mcpBearerPlaceholder')"
                      spellcheck="false"
                    />
                  </label>
                  <label v-else class="fld grow">
                    <input
                      v-model="mcpEditing.bearerToken"
                      type="password"
                      autocomplete="off"
                      :placeholder="t('settingsDialog.mcpBearerTokenPlaceholder')"
                      spellcheck="false"
                    />
                  </label>
                </div>
                <div class="prow-line adv">
                  <div class="hdrs grow">
                    <span class="lbl">{{ t("settingsDialog.mcpHeaders") }}</span>
                    <div v-for="(h, j) in mcpEditing.headers" :key="j" class="hdr-row">
                      <input v-model="h.k" type="text" placeholder="X-Custom-Header" spellcheck="false" />
                      <input v-model="h.v" type="text" :placeholder="t('settingsDialog.mcpEnvValHint')" spellcheck="false" />
                      <button class="icon-btn del" :title="t('common.delete')" @click="mcpEditing.headers.splice(j, 1)">
                        <el-icon><Delete /></el-icon>
                      </button>
                    </div>
                    <button class="mini" @click="mcpEditing.headers.push({ k: '', v: '' })">
                      <el-icon><Plus /></el-icon>{{ t("settingsDialog.mcpAddHeader") }}
                    </button>
                  </div>
                </div>
              </template>

              <div class="prow-line">
                <label class="fld chk" :title="t('settingsDialog.mcpEnabledHint')">
                  <input v-model="mcpEditing.enabled" type="checkbox" />
                  <span>{{ t("settingsDialog.mcpEnabled") }}</span>
                </label>
                <label class="fld chk" :title="t('settingsDialog.mcpTrustHint')">
                  <input v-model="mcpEditing.trust" type="checkbox" />
                  <span>{{ t("settingsDialog.mcpTrust") }}</span>
                </label>
                <label class="fld chk" :title="t('settingsDialog.mcpRequiredHint')">
                  <input v-model="mcpEditing.required" type="checkbox" />
                  <span>{{ t("settingsDialog.mcpRequired") }}</span>
                </label>
              </div>
              <!-- These flags never leave the box: they shape this end's
                   connection and approval only, so say so right under them. -->
              <p class="note">{{ t("settingsDialog.mcpFlagsNote") }}</p>
              <!-- The trust grant is stated before it is saved, not discovered
                   after — same treatment as the sandbox's escape hatch. -->
              <p v-if="mcpEditing.trust" class="note bad">{{ t("settingsDialog.mcpTrustWarn") }}</p>

              <div class="prow-line">
                <label class="fld num">
                  <span>{{ t("settingsDialog.mcpStartupTimeout") }}</span>
                  <input v-model.number="mcpEditing.startupTimeoutMs" type="number" min="0" placeholder="10000" />
                </label>
                <label class="fld num">
                  <span>{{ t("settingsDialog.mcpToolTimeout") }}</span>
                  <input v-model.number="mcpEditing.toolTimeoutMs" type="number" min="0" placeholder="120000" />
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.mcpTimeoutHint") }}</p>

              <div class="prow-line">
                <label class="fld grow">
                  <span>{{ t("settingsDialog.mcpInclude") }}</span>
                  <input v-model="mcpEditing.includeText" type="text" placeholder="tool_a, prefix.*" spellcheck="false" />
                </label>
              </div>
              <div class="prow-line">
                <label class="fld grow">
                  <span>{{ t("settingsDialog.mcpExclude") }}</span>
                  <input v-model="mcpEditing.excludeText" type="text" spellcheck="false" />
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.mcpToolFilterHint") }}</p>

              <!-- The equivalent-curl board: what the test button does, as
                   commands a person can run — recomputed from the live form. -->
              <div class="mcp-curl">
                <div class="mcp-curl-head">
                  <span>{{ t("settingsDialog.mcpCurlTitle") }}</span>
                  <button class="mini" @click="copyMCPCurl">{{ t("settingsDialog.mcpCurlCopy") }}</button>
                </div>
                <!-- The mask note sits between the head and the code: literal
                     tokens render as *** on the board; the probe sends the
                     real one. -->
                <p v-if="mcpEditing.bearerMode === 'direct' && mcpEditing.bearerToken" class="mcp-curl-note">
                  {{ t("settingsDialog.mcpCurlMaskedNote") }}
                </p>
                <pre class="mcp-curl-pre">{{ mcpCurlText }}</pre>
              </div>

              <div class="hint-row">
                <p class="hint">{{ t("settingsDialog.mcpProbeHint") }}</p>
                <div class="hint-actions">
                  <button class="mini" :disabled="mcpProbing" @click="probeMCP">
                    {{ mcpProbing ? t("common.loading") : t("settingsDialog.mcpTest") }}
                  </button>
                </div>
              </div>
              <div v-if="mcpProbe" ref="mcpProbeEl" class="mcp-probe" :class="{ bad: !mcpProbe.ok }">
                <template v-if="mcpProbe.ok">
                  <p class="mcp-probe-head">
                    {{ t("settingsDialog.mcpProbeOk", { name: mcpProbe.server_name || "?", version: mcpProbe.server_version || "", protocol: mcpProbe.protocol_version || "?" }) }}
                  </p>
                  <p class="mcp-probe-tools">
                    {{ t("settingsDialog.mcpProbeTools", { n: mcpProbe.tools?.length ?? 0 }) }}{{ (mcpProbe.tools ?? []).join(", ") }}
                  </p>
                  <!-- Reported is not registered: the pool's wrapper validation
                       drops invalid declarations with only a WARN log, so the
                       probe names them here instead of letting the card count
                       disagree with this list later. -->
                  <details v-if="mcpProbe.unregistrable?.length" class="mcp-probe-warn">
                    <summary>{{ t("settingsDialog.mcpProbeUnregistrable", { n: mcpProbe.unregistrable.length }) }}</summary>
                    <p v-for="u in mcpProbe.unregistrable" :key="u.name" class="mcp-probe-issue">{{ u.name }}: {{ u.reason }}</p>
                  </details>
                </template>
                <pre v-else class="mcp-probe-err">{{ t("settingsDialog.mcpProbeFailed") }}: {{ mcpProbe.error }}</pre>
              </div>
              <p v-if="mcpError" class="note bad">{{ mcpError }}</p>
            </div>

            <!-- The card wall: one card per server, Chatbox-style — the
                 switch up top decides whether the server's tools are injected
                 at all (saves immediately), the body opens the edit form, and
                 a dashed add-card closes the grid. The toolbar above it holds
                 the JSON door: import and the example template. -->
            <div v-else class="sect">
              <div class="hint-row">
                <p class="hint">{{ mcpServers.length || mcpError ? "" : t("settingsDialog.mcpEmpty") }}</p>
                <div class="hint-actions">
                  <button class="mini" @click="openMCPImport">{{ t("settingsDialog.mcpImport") }}</button>
                  <button class="mini" @click="downloadMCPSample">{{ t("settingsDialog.mcpSample") }}</button>
                  <button class="mini" :disabled="!mcpExportList.length" @click="openMCPExport('client')">
                    {{ t("settingsDialog.mcpExport") }}
                  </button>
                </div>
              </div>
              <div class="mcp-grid">
                <div
                  v-for="row in mcpServers"
                  :key="row.name"
                  class="mcp-card"
                  :class="{ off: !row.enabled }"
                  role="button"
                  tabindex="0"
                  :title="t('settingsDialog.mcpEditTitle', { name: row.name })"
                  @click="openMCPEdit(row, false)"
                  @keydown.enter="openMCPEdit(row, false)"
                >
                  <div class="mcp-card-head">
                    <span class="mcp-name">{{ row.name }}</span>
                    <!-- The switch and the trash are the card's actions, not
                         the body's: clicks there must not open the form. -->
                    <el-switch
                      :model-value="row.enabled"
                      size="small"
                      :title="t('settingsDialog.mcpSwitchHint')"
                      :disabled="mcpSaving"
                      @click.stop
                      @change="toggleMCPEnabled(row)"
                    />
                  </div>
                  <div class="mcp-card-status">
                    <span class="mcp-dot" :class="mcpDotClass(row)" />
                    <span class="mcp-status-text" :title="mcpDotTip(row)">{{ mcpStatusText(row) }}</span>
                  </div>
                  <div class="mcp-card-foot">
                    <span class="mcp-summary" :title="mcpSummary(row)">{{ mcpSummary(row) }}</span>
                    <span v-if="row.trust" class="mcp-badge">{{ t("settingsDialog.mcpTrusted") }}</span>
                    <button class="icon-btn del" :title="t('common.delete')" @click.stop="removeMCPServer(row)">
                      <el-icon><Delete /></el-icon>
                    </button>
                  </div>
                </div>
                <button class="mcp-add" @click="openMCPEdit(blankMCPRow(), true)">
                  <el-icon><Plus /></el-icon>
                  <span>{{ t("settingsDialog.mcpAdd") }}</span>
                </button>
              </div>
              <p v-if="mcpError" class="note bad">{{ mcpError }}</p>
            </div>
          </section>

          <!-- ── MCP 建立（服务端）:mcp.json 的 server 节,本机开放给别人调用 ── -->
          <section v-else-if="sheet === 'mcpServe'" class="pane-page">
            <header class="pane-head">
              <h3>{{ t("settingsDialog.mcpSrv") }}</h3>
              <p>{{ t("settingsDialog.descMcpSrv") }}</p>
            </header>
            <div class="path-row" :title="mcpPath">
              <el-icon><Setting /></el-icon>
              <span class="path">{{ mcpPath }}</span>
            </div>
            <div v-if="mcpLoadError" class="note bad">{{ t("settingsDialog.mcpLoadError") }}{{ mcpLoadError }}</div>

            <div class="sect">
              <!-- Tools-only head — the page header already names the sheet.
                   Embedded mode moves these two into the tab row's right end
                   (see .nav-actions), so they are only rendered here for the
                   fullscreen dialog. -->
              <div v-if="!embedded" class="sect-head end">
                <button class="mini" @click="openMCPExport('serve')">
                  {{ t("settingsDialog.mcpExportServe") }}
                </button>
                <button class="mini" :disabled="mcpSaving || mcpSrvEnvTaken || !mcpSrvDirty" @click="saveMCPServe">
                  {{ t("common.save") }}
                </button>
              </div>
              <div class="mcp-srv-status">
                <span class="mcp-dot" :class="mcpSrvDotClass" />
                <span class="mcp-srv-status-text">{{ mcpSrvStatusText }}</span>
                <span v-if="mcpSrvParts.endpoints.length" class="mcp-srv-eps">{{ mcpSrvParts.endpoints.join("  ") }}</span>
              </div>
              <p v-if="mcpSrvParts.restart" class="note bad">{{ t("settingsDialog.mcpSrvRestart") }}</p>
              <!-- Env takeover is stated before any field: editing the file
                   changes nothing while POGO_MCP* holds the config. -->
              <p v-if="mcpSrvEnvTaken" class="note bad">{{ t("settingsDialog.mcpSrvEnvTaken") }}</p>

              <div class="prow-line">
                <div class="fld">
                  <span>{{ t("settingsDialog.mcpEnabled") }}</span>
                  <el-switch v-model="mcpSrv.enabled" size="small" :disabled="mcpSrvEnvTaken" />
                </div>
              </div>
              <div class="prow-line">
                <div class="fld grow">
                  <span>{{ t("settingsDialog.mcpSrvToken") }}</span>
                  <el-radio-group v-model="mcpSrv.tokenMode" size="small" :disabled="mcpSrvEnvTaken">
                    <el-radio-button value="off">{{ t("settingsDialog.mcpSrvTokenOff") }}</el-radio-button>
                    <el-radio-button value="env">{{ t("settingsDialog.mcpBearerModeEnv") }}</el-radio-button>
                    <el-radio-button value="direct">{{ t("settingsDialog.mcpBearerModeDirect") }}</el-radio-button>
                  </el-radio-group>
                </div>
              </div>
              <div v-if="mcpSrv.tokenMode === 'env'" class="prow-line">
                <label class="fld grow">
                  <input
                    v-model="mcpSrv.tokenEnv"
                    type="text"
                    :disabled="mcpSrvEnvTaken"
                    :placeholder="t('settingsDialog.mcpBearerPlaceholder')"
                    spellcheck="false"
                  />
                </label>
              </div>
              <div v-else-if="mcpSrv.tokenMode === 'direct'" class="prow-line">
                <label class="fld grow">
                  <input
                    v-model="mcpSrv.token"
                    type="password"
                    autocomplete="off"
                    :disabled="mcpSrvEnvTaken"
                    :placeholder="t('settingsDialog.mcpBearerTokenPlaceholder')"
                    spellcheck="false"
                  />
                </label>
              </div>

              <div class="prow-line">
                <label class="fld">
                  <span>{{ t("settingsDialog.mcpSrvPolicy") }}</span>
                  <select v-model="mcpSrv.policy" :disabled="mcpSrvEnvTaken">
                    <option value="">{{ t("settingsDialog.mcpSrvPolicyStandard") }}</option>
                    <option value="strict">{{ t("settingsDialog.mcpSrvPolicyStrict") }}</option>
                    <option value="auto">{{ t("settingsDialog.mcpSrvPolicyAuto") }}</option>
                  </select>
                </label>
              </div>
              <p class="hint">{{ t("settingsDialog.mcpSrvPolicyHint") }}</p>

              <div class="prow-line">
                <div class="fld grow">
                  <span>{{ mcpSrvCatalog ? t("settingsDialog.mcpSrvIncludePick") : t("settingsDialog.mcpSrvInclude") }}</span>
                  <input
                    v-if="!mcpSrvCatalog"
                    v-model="mcpSrv.includeText"
                    type="text"
                    :disabled="mcpSrvEnvTaken"
                    placeholder="mal.*, agent_task_status"
                    spellcheck="false"
                  />
                </div>
              </div>

              <!-- The grouped check tree over the backend's serve_catalog:
                   builtin / skill families / one group per server / one group
                   per runtime skill package, each with a tri-state head
                   checkbox. -->
              <div v-if="mcpSrvCatalog" class="mcp-pick">
                <div v-for="sect in mcpSrvPickSects" :key="sect.id" class="mcp-pick-sect">
                  <div class="mcp-pick-sect-title">{{ sect.title }}</div>
                  <p v-if="!sect.groups.length" class="hint">{{ sect.empty }}</p>
                  <div v-for="g in sect.groups" :key="g.id" class="mcp-pick-group" :class="{ down: serveGroupDown(g) }">
                    <div class="mcp-pick-group-head">
                      <el-checkbox
                        :model-value="serveGroupOn(g)"
                        :indeterminate="serveGroupPart(g)"
                        :disabled="mcpSrvEnvTaken || serveGroupDown(g) || !g.tools.length"
                        @update:model-value="(v: boolean) => toggleServeGroup(g, v)"
                      >
                        <span class="mcp-pick-group-name">{{ serveGroupLabel(g) }}</span>
                      </el-checkbox>
                      <span
                        v-if="g.kind === 'runtime' && g.serverState"
                        class="mcp-pick-state"
                        :class="{ ok: g.serverState === 'available' }"
                      >{{ g.serverState }}</span>
                      <span class="mcp-pick-count">{{ serveGroupCount(g) }}/{{ g.tools.length }}</span>
                    </div>
                    <p v-if="serveGroupDown(g)" class="hint">
                      {{ serveGroupDownHint(g) }}
                    </p>
                    <div class="mcp-pick-tools">
                      <div v-for="tool in g.tools" :key="tool.name" class="mcp-pick-tool">
                        <el-checkbox
                          :model-value="mcpSrvChecks.has(tool.name)"
                          :disabled="mcpSrvEnvTaken || serveGroupDown(g)"
                          @update:model-value="(v: boolean) => toggleServeTool(tool.name, v)"
                        >
                          <span class="mcp-pick-tool-name">{{ tool.title || tool.name }}</span>
                        </el-checkbox>
                        <template v-if="g.kind === 'runtime'">
                          <span v-if="tool.contract" class="mcp-pick-contract" :title="tool.contract">{{ tool.contract }}</span>
                          <span
                            v-if="tool.readOnly"
                            class="mcp-pick-badge ok"
                            :title="t('settingsDialog.mcpSrvPickRuntimeReadOnlyHint')"
                          >{{ t("settingsDialog.mcpSrvPickRuntimeReadOnly") }}</span>
                          <span
                            v-else
                            class="mcp-pick-write"
                            :title="t('settingsDialog.mcpSrvPickWriteHint')"
                          >{{ t("settingsDialog.mcpSrvPickWrite") }}</span>
                          <span
                            v-if="tool.fileInputs"
                            class="mcp-pick-badge"
                            :title="t('settingsDialog.mcpSrvPickRuntimeUploadHint')"
                          >{{ t("settingsDialog.mcpSrvPickRuntimeUpload") }}</span>
                          <span
                            v-if="tool.writesArtifacts"
                            class="mcp-pick-badge"
                            :title="t('settingsDialog.mcpSrvPickRuntimeArtifactHint')"
                          >{{ t("settingsDialog.mcpSrvPickRuntimeArtifact") }}</span>
                        </template>
                        <span
                          v-if="g.kind === 'forward' && !tool.readOnly"
                          class="mcp-pick-write"
                          :title="t('settingsDialog.mcpSrvPickWriteHint')"
                        >{{ t("settingsDialog.mcpSrvPickWrite") }}</span>
                        <span v-if="serveToolSub(tool)" class="mcp-pick-tool-sub" :title="serveToolSub(tool)">
                          {{ serveToolSub(tool) }}
                        </span>
                      </div>
                    </div>
                  </div>
                </div>
                <!-- The live preview of what the checks serialize to — the
                     '_' sentinel included, so it never surprises in the
                     saved file. -->
                <p class="mcp-pick-preview" :title="mcpSrv.includeText">
                  <span class="mcp-pick-preview-label">include_tools</span>{{ mcpSrv.includeText }}
                </p>
                <p v-if="mcpSrv.includeText.trim() === '_'" class="hint">{{ t("settingsDialog.mcpSrvPickNoneHint") }}</p>
              </div>
              <p class="hint">{{ mcpSrvCatalog ? t("settingsDialog.mcpSrvPickHint") : t("settingsDialog.mcpSrvIncludeHint") }}</p>

              <button class="mcp-srv-adv" type="button" @click="mcpSrvAdvOpen = !mcpSrvAdvOpen">
                <el-icon :size="12"><ArrowDown v-if="mcpSrvAdvOpen" /><ArrowRight v-else /></el-icon>
                <span>{{ t("settingsDialog.mcpSrvAdvanced") }}</span>
              </button>
              <template v-if="mcpSrvAdvOpen">
                <div class="prow-line">
                  <label class="fld num">
                    <span>{{ t("settingsDialog.mcpSrvMaxConcurrent") }}</span>
                    <input v-model.number="mcpSrv.maxConcurrent" type="number" min="0" placeholder="8" :disabled="mcpSrvEnvTaken" />
                  </label>
                  <label class="fld num">
                    <span>{{ t("settingsDialog.mcpSrvSubmitRpm") }}</span>
                    <input v-model.number="mcpSrv.submitRpm" type="number" min="0" placeholder="60" :disabled="mcpSrvEnvTaken" />
                  </label>
                  <label class="fld num">
                    <span>{{ t("settingsDialog.mcpSrvMaxWaits") }}</span>
                    <input v-model.number="mcpSrv.maxWaits" type="number" min="0" placeholder="32" :disabled="mcpSrvEnvTaken" />
                  </label>
                </div>
                <p class="hint">{{ t("settingsDialog.mcpSrvAdvancedHint") }}</p>
              </template>
            </div>

            <!-- The call log lives with the server it observes: every request
                 that reached /mcp or /sse, classified, with diagnoses. The
                 panel polls only while this sheet is showing. -->
            <div class="sect">
              <div class="sect-head">
                <h4>{{ t("mcpLogs.title") }}</h4>
              </div>
              <p class="hint">{{ t("mcpLogs.subtitle") }}</p>
              <McpLogsPanel :active="sheet === 'mcpServe' && visible" />
            </div>
          </section>

          <section v-else-if="sheet === 'mcpMonitor'" class="pane-page monitor-page">
            <header class="pane-head"><h3>{{ t("mcpMonitor.title") }}</h3></header>
            <McpMonitorPanel :active="visible && sheet === 'mcpMonitor'" :initial-trace-id="mcpMonitorTrace" />
          </section>

          <IconLibrarySettings v-show="sheet === 'icons'" :active="visible && sheet === 'icons'" @dirty="iconLibraryDirty = $event" />

          <ul v-if="warnings.length" class="warnings">
            <li v-for="(wn, i) in warnings" :key="i">{{ wn }}</li>
          </ul>
        </div>
      </div>
    </template>

    <template #footer>
      <el-button v-if="!embedded" @click="tryClose(() => (visible = false))">{{ t("common.close") }}</el-button>
      <!-- Every writable sheet saves from the same place: right of Close in
           the footer, one shape of button, instead of a save pinned to each
           section's header. The button reflects its sheet's own state —
           loading, and disabled when there is nothing loaded (or, for the
           allowlist, nothing changed) to save. -->
      <el-button
        v-if="sheet === 'service'"
        type="primary"
        :loading="saving"
        :disabled="loading || !!loadError"
        @click="save"
      >
        {{ t("common.save") }}
      </el-button>
      <el-button
        v-else-if="sheet === 'storage'"
        type="primary"
        :loading="storageSaving"
        :disabled="!storage"
        @click="saveStorage"
      >
        {{ t("common.save") }}
      </el-button>
      <el-button
        v-else-if="sheet === 'sandbox'"
        type="primary"
        :loading="sandboxSaving"
        :disabled="!sandbox"
        @click="saveSandbox"
      >
        {{ t("common.save") }}
      </el-button>
      <el-button
        v-else-if="sheet === 'attach'"
        type="primary"
        :loading="attachSaving"
        :disabled="!attachPolicy || !attachDirty"
        @click="saveAttach"
      >
        {{ t("common.save") }}
      </el-button>
      <!-- The MCP list's quick edits save themselves; the footer buttons are
           only for the open add/edit form — the way back to the list sits in
           the same row as the save, one button shape, to its left. -->
      <template v-else-if="sheet === 'mcp' && mcpEditing">
        <el-button @click="cancelMCPEdit">{{ t("settingsDialog.mcpBack") }}</el-button>
        <el-button type="primary" :loading="mcpSaving" @click="saveMCP">
          {{ t("common.save") }}
        </el-button>
      </template>
    </template>
  </SettingsContainer>

  <SpillBrowser v-if="meAdmin" v-model="spillBrowserVisible" :root="storage?.dir ?? ''" />

  <!-- The curl preview, one level above the editor so both modes can reach it
       through the same openCurl(). Sibling root, not nested: the main dialog's
       footer should not be its anchor point. -->
  <el-dialog
    v-model="curlVisible"
    :title="t('settingsDialog.curlTitle', { name: curlName })"
    width="860px"
    append-to-body
    class="settings-curl-dialog"
  >
    <div class="curl-toggles">
      <label v-if="!curlMetadata" class="stream-toggle">
        <input v-model="curlStream" type="checkbox" />
        <span>{{ t("settingsDialog.curlStream") }}</span>
      </label>
      <label v-if="!curlMetadata" class="stream-toggle">
        <input v-model="curlVision" type="checkbox" />
        <span>{{ t("settingsDialog.curlVision") }}</span>
      </label>
      <button type="button" class="curl-metadata-toggle" :class="{ active: curlMetadata }" :disabled="probing || visionProbing || metadataProbe?.busy" :aria-pressed="curlMetadata" data-testid="curl-metadata-toggle" @click="curlMetadata = !curlMetadata">{{ t(curlMetadata ? 'modelMetadata.back' : 'modelMetadata.open') }}</button>
    </div>
    <ModelMetadataProbe v-if="curlVisible && curlMetadata && curlRow" ref="metadataProbe" :provider="curlRow" :model="modelFor(curlRow)" />
    <template v-if="!curlMetadata">
    <pre class="curl-pre"><code><span v-for="(line, i) in curlTokens" :key="i" class="row"><span class="txt"><span
      v-for="(tok, j) in line" :key="j" :class="tok.kind">{{ tok.text }}</span></span></span></code></pre>
    <p class="hint">{{ curlVision ? t("settingsDialog.curlVisionHint") : t("settingsDialog.curlHint") }}</p>

    <div v-if="probe" class="probe-res" :class="{ bad: !probe.ok }">
      <div class="probe-head">
        <template v-if="probe.ok">
          <span class="probe-status">{{ probe.status_text || probe.status }}</span>
          <span v-if="probe.elapsed_ms !== undefined" class="probe-meta">{{ probe.elapsed_ms }}ms</span>
          <span v-if="probe.content_type" class="probe-meta">{{ probe.content_type }}</span>
          <span v-if="probe.truncated" class="probe-meta">{{ t("settingsDialog.probeTruncated") }}</span>
        </template>
        <template v-else>
          <span class="probe-status">{{ t("settingsDialog.probeFailed") }}</span>
          <span v-if="probe.elapsed_ms !== undefined" class="probe-meta">{{ probe.elapsed_ms }}ms</span>
        </template>
      </div>
      <pre v-if="prettyProbeBody" class="probe-body">{{ prettyProbeBody }}</pre>
      <!-- Transport errors carry the full URL and must wrap in their own block;
           the one-line flex head clipped them to a sliver. -->
      <pre v-else-if="!probe.ok && probe.error" class="probe-error">{{ probe.error }}</pre>
    </div>

    <!-- The vision verdict: one row per question, the model's reply beside the
         right answer, so a wrong mark can be eyeballed before it flips the
         model's vision switch. -->
    <div v-if="visionRes" class="vision-res" :class="{ yes: visionRes.ok && visionRes.verdict === 'yes', no: visionRes.ok && visionRes.verdict === 'no' }">
      <div class="probe-head">
        <template v-if="visionRes.ok">
          <span class="probe-status">
            {{ visionRes.verdict === "yes" ? t("settingsDialog.visionVerdictYes") : t("settingsDialog.visionVerdictNo") }}
          </span>
          <span v-if="visionRes.rejected" class="probe-meta">{{ t("settingsDialog.visionRejected") }}</span>
          <span v-if="visionRes.elapsed_ms !== undefined" class="probe-meta">{{ visionRes.elapsed_ms }}ms</span>
        </template>
        <template v-else>
          <span class="probe-status">{{ t("settingsDialog.visionInconclusive") }}</span>
        </template>
      </div>
      <div v-for="(q, i) in visionRes.questions" :key="i" class="vision-q" :class="{ pass: q.pass }">
        <span class="vision-mark">{{ q.pass ? "✓" : "✗" }}</span>
        <span class="vision-ask">{{ askLabel(q) }}</span>
        <span class="vision-ans">{{ t("settingsDialog.visionReply", { reply: q.reply || "—" }) }}</span>
        <span class="vision-key">{{ t("settingsDialog.visionAnswer", { answer: q.answer }) }}</span>
      </div>
      <pre v-if="visionRes.note" class="probe-error">{{ visionRes.note }}</pre>
      <pre v-else-if="!visionRes.ok && visionRes.error" class="probe-error">{{ visionRes.error }}</pre>
      <p class="hint">{{ t("settingsDialog.visionProbeHint") }}</p>
    </div>
    </template>

    <template #footer>
      <el-button @click="curlVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button :disabled="curlMetadata && !metadataProbe?.canSend" @click="copyCurl">{{ t("common.copy") }}</el-button>
      <el-button type="primary" :loading="curlMetadata ? metadataProbe?.busy : probing || visionProbing" :disabled="curlMetadata && !metadataProbe?.canSend" @click="sendProbe">
        {{ t(curlMetadata ? "modelMetadata.send" : "settingsDialog.sendProbe") }}
      </el-button>
    </template>
  </el-dialog>

  <!-- The MCP import dialog: paste or pick a JSON file, parse into per-entry
       verdicts, confirm merges the valid ones. Sibling root like the curl
       preview, for the same teleport reason. -->
  <el-dialog
    v-model="mcpImportVisible"
    :title="t('settingsDialog.mcpImportTitle')"
    width="640px"
    append-to-body
    class="settings-mcp-import-dialog"
  >
    <p class="hint">{{ t("settingsDialog.mcpImportHint") }}</p>
    <p class="hint">
      {{ t("settingsDialog.mcpImportNoClue")
      }}<button class="mcp-link" @click="downloadMCPSample">{{ t("settingsDialog.mcpSample") }}</button>
    </p>
    <textarea
      v-model="mcpImportText"
      class="mcp-import-editor"
      spellcheck="false"
      :placeholder="'{\n  &quot;mcpServers&quot;: {\n    &quot;my-server&quot;: { &quot;command&quot;: &quot;…&quot; }\n  }\n}'"
      @input="mcpImportParsed = null"
    />
    <div class="hint-row">
      <p class="hint" />
      <div class="hint-actions">
        <button class="mini" @click="pickMCPImportFile">{{ t("settingsDialog.mcpImportPick") }}</button>
        <button class="mini" :disabled="!mcpImportText.trim()" @click="runMCPImportParse">
          {{ t("settingsDialog.mcpImportParse") }}
        </button>
      </div>
    </div>
    <input
      ref="mcpImportFileInput"
      type="file"
      accept="application/json,.json"
      style="display: none"
      @change="onMCPImportFile"
    />

    <div v-if="mcpImportParsed" class="mcp-import-res">
      <p v-if="!mcpImportParsed.ok && mcpImportParsed.reason === 'syntax'" class="note bad">
        {{ t("settingsDialog.mcpImportSyntaxErr", { msg: mcpImportParsed.detail ?? "" }) }}
      </p>
      <p v-else-if="!mcpImportParsed.ok" class="note bad">{{ t("settingsDialog.mcpImportNoServers") }}</p>
      <template v-else>
        <div v-for="(e, i) in mcpImportParsed.entries" :key="i" class="mcp-import-row" :class="{ bad: !!e.error }">
          <span class="mcp-import-mark">{{ e.error ? "✗" : "✓" }}</span>
          <span class="mcp-import-name" :title="e.name">{{ e.name }}</span>
          <template v-if="e.config">
            <span class="mcp-import-sum" :title="mcpImportSummary(e.config)">{{ mcpImportSummary(e.config) }}</span>
            <span v-if="mcpNameSet.has(e.name)" class="mcp-import-over">{{ t("settingsDialog.mcpImportOverwrite") }}</span>
          </template>
          <span v-else class="mcp-import-err">{{ mcpImportErrText(e) }}</span>
        </div>
        <p v-if="!mcpImportParsed.entries.length" class="hint">{{ t("settingsDialog.mcpImportNoServers") }}</p>
        <p v-if="mcpImportIgnoredText" class="hint">
          {{ t("settingsDialog.mcpImportIgnored", { fields: mcpImportIgnoredText }) }}
        </p>
      </template>
    </div>

    <template #footer>
      <el-button @click="mcpImportVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button
        type="primary"
        :loading="mcpSaving"
        :disabled="!mcpImportableCount"
        @click="confirmMCPImport"
      >
        {{ t("settingsDialog.mcpImportConfirm", { n: mcpImportableCount }) }}
      </el-button>
    </template>
  </el-dialog>

  <!-- The MCP export dialog: one component behind both entries (client sheet
       and serving sheet); fed only with the saved config/server_state. -->
  <McpExportDialog
    v-model="mcpExportVisible"
    :mode="mcpExportMode"
    :servers="mcpExportList"
    :serve-state="mcpSrvState"
    :serve-token-env="mcpSrvSavedTokenEnv"
  />
</template>

<style lang="scss">
/* Unscoped: el-dialog teleports to body when append-to-body is set, so scoped
   rules would never reach it. Names stay prefixed to avoid collisions. */
.settings-dialog {
  /* A fullscreen dialog is a page: the body must stretch so the footer sits
     on the floor instead of floating after whatever the current page
     rendered. The flex lives on .settings-dialog itself because that IS the
     .el-dialog element — el-dialog spreads its attrs, class included, onto
     the dialog box, so a ".settings-dialog .el-dialog" descendant rule
     matches nothing. Default el-dialog is a block, which is what left the
     gap. */
  display: flex;
  flex-direction: column;

  .el-dialog__body {
    flex: 1;
    min-height: 0;
    overflow: hidden; /* the pane scrolls, not the body */
  }

  .attach-cat {
    padding: 6px 0 10px;
    border-bottom: 1px dashed var(--el-border-color-lighter);

    &:last-of-type {
      border-bottom: none;
    }
  }

  .attach-cat-head {
    display: flex;
    align-items: center;
    gap: 4px;

    .el-checkbox {
      flex: 1;
    }
  }

  .attach-cat-fold {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 20px;
    padding: 0;
    border: none;
    border-radius: 4px;
    background: transparent;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
      background: var(--el-fill-color-light);
    }
  }

  .attach-cat-count {
    font-family: var(--pg-mono, monospace);
    font-size: 11px;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color-light);
    border-radius: 999px;
    padding: 1px 8px;
  }

  .attach-danger-hint {
    margin: 2px 0 2px 24px;
    font-size: 12px;
    color: var(--el-text-color-secondary);

    /* .hot: at least one sample/archive type is on — the hint stops being
       informational and starts being a warning. */
    &.hot {
      color: var(--el-color-danger);
    }
  }

  .attach-chip-grid {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 6px 0 2px 24px;
  }

  .attach-chip {
    font-family: var(--pg-mono, monospace);
    font-size: 12px;
    line-height: 1;
    padding: 5px 10px;
    border-radius: 999px;
    border: 1px solid var(--el-border-color);
    background: transparent;
    color: var(--el-text-color-secondary);
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      border-color 0.12s ease,
      color 0.12s ease;

    &:hover {
      border-color: var(--el-color-primary);
      color: var(--el-color-primary);
    }

    &.on {
      background: var(--el-color-primary);
      border-color: var(--el-color-primary);
      color: var(--el-color-white);
    }

    /* Picked sample/archive chips read as armed, not merely selected. */
    .attach-cat.danger &.on {
      background: var(--el-color-danger);
      border-color: var(--el-color-danger);
    }

    .attach-cat.danger &:hover {
      border-color: var(--el-color-danger);
      color: var(--el-color-danger);
    }

    .attach-cat.danger &.on:hover {
      color: var(--el-color-white);
    }
  }

  .attach-search {
    width: 200px;
  }

  /* Sticky so "what will be saved" and the save button stay on screen while
     the group list scrolls; negative side margins stretch the background
     over .sect's padding. */
  .attach-summary {
    position: sticky;
    bottom: 0;
    z-index: 1;
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 2px -10px 0;
    padding: 8px 10px 0;
    border-top: 1px solid var(--el-border-color-lighter);
    background: var(--el-bg-color);
    font-size: 12.5px;
    color: var(--el-text-color-secondary);
  }

  .cap-head {
    margin-top: 14px;

    .attach-cap-value {
      font-family: var(--pg-mono, monospace);
      font-size: 13px;
      color: var(--el-color-primary);
    }
  }

  .attach-cap-scale {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    margin-top: -6px;
  }

  .path-row {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
    font-size: 12px;
    color: var(--el-text-color-secondary);

    .path {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      direction: rtl;
      text-align: left;
      font-family: var(--pg-mono, monospace);
    }
  }

  .note {
    font-size: 12px;
    color: var(--el-text-color-secondary);

    &.bad {
      color: var(--el-color-danger);
    }
  }

  /* The split: GitHub-settings shape — a grouped index on the left, one page
     at a time on the right. The nav is an index, not a tree: every entry is a
     peer, groups are labels rather than collapsibles, because nothing here is
     deep enough to bury. */
  .settings-split {
    display: flex;
    gap: 16px;
    min-height: 0;
    height: 100%;
  }

  .settings-nav {
    flex: 0 0 200px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-y: auto;
    padding-right: 4px;

    .nav-group {
      padding: 10px 8px 4px;
      font-size: 11px;
      font-weight: 600;
      color: var(--el-text-color-secondary);
      text-transform: uppercase;
      letter-spacing: 0.04em;

      &:first-child {
        padding-top: 2px;
      }
    }

    .nav-item {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 6px;
      /* Indented under the group label: the label is level one, these are its
         children, and the indent is what says so. */
      margin-left: 12px;
      border: 0;
      background: transparent;
      padding: 6px 8px;
      border-radius: 7px;
      font: inherit;
      font-size: 13px;
      text-align: left;
      color: var(--el-text-color-regular);
      cursor: pointer;
      transition: background var(--pg-transition);

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

    .dirty-dot {
      flex: 0 0 auto;
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background: var(--el-color-danger);
    }
  }

  /* The pane takes what the dialog's body leaves; it is the old .form-body
     (scroll + column gap) grown to fill the split. */
  .settings-pane {
    flex: 1;
    min-width: 0;
    max-height: none;
    height: 100%;
  }

  .pane-head {
    margin-bottom: 2px;

    /* Title and a page-level action (e.g. the edit form's back-to-list)
       share one line; the description stays full-width beneath them. */
    .head-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;

      h3 {
        margin: 0;
      }
    }

    h3 {
      margin: 0;
      font-size: 15px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    p {
      margin: 2px 0 8px;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  .monitor-page {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-height: 0;
    height: 100%;
    flex: 1;
    > :last-child { flex: 1; min-height: 0; height: auto; }
  }

  /* Narrow screens: the nav becomes a horizontally scrolling strip of chips
     above the pane — the same index, laid out for a thumb. */
  @media (max-width: 720px) {
    .settings-split {
      flex-direction: column;
      gap: 8px;
    }

    .settings-nav {
      flex: 0 0 auto;
      flex-direction: row;
      align-items: center;
      gap: 4px;
      overflow-x: auto;
      overflow-y: hidden;
      padding: 0 0 4px;

      .nav-group {
        padding: 0 6px 0 8px;
        white-space: nowrap;
      }

      .nav-item {
        flex: 0 0 auto;
        white-space: nowrap;
      }
    }

    .settings-pane {
      height: auto;
      max-height: 62vh;
    }
  }

  .form-body {
    // No max-height here: the split pane's own height: 100% governs (narrow
    // screens: the 62vh media query above). A vh cap at this spot wins on
    // source order over .settings-pane's max-height: none and would pin the
    // pane short of the footer on tall windows.
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sect {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  /* Stacked section cards on one page (e.g. the storage sheet's spill and
     turn-limit cards) need air between them; nested stacks already get their
     gap from the parent .sect's flex gap. */
  .pane-page > .sect + .sect {
    margin-top: 10px;
  }

  .sect-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  /* A tools-only head row (no title) pins its buttons to the right. */
  .spill-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .spill-root {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
  }
  .spill-root code {
    color: var(--el-text-color-regular);
    overflow-wrap: anywhere;
    user-select: text;
  }

  .sect-head.end {
    justify-content: flex-end;
  }

  .prow {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px;
    border: 1px solid var(--el-border-color-extra-light);
    border-radius: 7px;
    background: var(--el-fill-color-blank);
  }

  .prow-line {
    display: flex;
    align-items: flex-end;
    gap: 8px;
  }

  .fld {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;

    &.grow {
      flex: 1 1 0;
    }

    &.num {
      width: 110px;
    }

    span {
      font-size: 11px;
      color: var(--el-text-color-secondary);
    }

    input,
    select {
      border: 1px solid var(--el-border-color-lighter);
      border-radius: 6px;
      background: var(--el-fill-color-blank);
      color: var(--el-text-color-primary);
      font: inherit;
      font-size: 12px;
      padding: 4px 8px;
      outline: none;
      min-width: 0;

      &:focus {
        border-color: var(--el-color-primary);
      }
    }
  }

  .icon-btn {
    border: 0;
    background: transparent;
    padding: 5px;
    border-radius: 6px;
    color: var(--el-text-color-secondary);
    cursor: pointer;
    flex: 0 0 auto;

    &:hover {
      background: var(--el-fill-color-light);
      color: var(--el-color-danger);
    }
  }

  .mini {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: 1px dashed var(--el-border-color);
    background: transparent;
    padding: 3px 10px;
    border-radius: 6px;
    font: inherit;
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
      border-color: var(--el-color-primary);
    }

    /* Filled variant: a solid primary-blue button for the one action that
       must not read as decoration (back out of the MCP edit form). */
    &.solid {
      border: 1px solid var(--el-color-primary);
      background: var(--el-color-primary);
      color: #fff;

      &:hover {
        color: #fff;
        opacity: 0.88;
      }
    }

    /* Danger variant: the destructive row action (account delete). Red
       outline, not a solid blob — a quiet color cue next to its sibling. */
    &.danger {
      border-style: solid;
      color: var(--el-color-danger);

      &:hover {
        border-color: var(--el-color-danger);
        background: var(--el-color-danger-light-9);
      }

      &:disabled {
        opacity: 0.55;
        cursor: wait;
      }
    }
  }

  .hint {
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    margin: 0;
    line-height: 1.5;
  }

  .hint-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;

    .hint {
      flex: 1 1 0;
    }
  }

  .hint-actions {
    display: flex;
    gap: 6px;
    flex: 0 0 auto;
  }

  /* The vendor-switch line: quieter than the fields above it, because most
     providers have nothing here and the row should read as optional. */
  .prow-line.adv {
    padding-top: 2px;
  }

  .extra-body {
    width: 100%;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 6px;
    background: var(--el-fill-color-blank);
    color: var(--el-text-color-primary);
    font: inherit;
    font-size: 12px;
    font-family: var(--pg-mono, monospace);
    padding: 4px 8px;
    outline: none;
    resize: vertical;
    min-height: 0;

    &:focus {
      border-color: var(--el-color-primary);
    }
  }

  .hdrs {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;

    .lbl {
      font-size: 11px;
      color: var(--el-text-color-secondary);
    }

    .hdr-row {
      display: flex;
      align-items: center;
      gap: 6px;

      input {
        flex: 1 1 0;
        min-width: 0;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;
        background: var(--el-fill-color-blank);
        color: var(--el-text-color-primary);
        font: inherit;
        font-size: 12px;
        padding: 4px 8px;
        outline: none;

        &:focus {
          border-color: var(--el-color-primary);
        }

        &:first-child {
          flex: 0 1 220px;
        }
      }
    }
  }

  .fld.chk {
    flex-direction: row;
    align-items: center;
    gap: 6px;
    align-self: flex-end;
    padding-bottom: 4px;
    white-space: nowrap;
    cursor: pointer;

    input {
      margin: 0;
    }

    span {
      font-size: 11.5px;
    }
  }

  /* The MCP server wall, Chatbox-style: one card per server plus a dashed
     add-card, on a grid that reflows with the pane's width. The card body is
     the edit button (whole-card click), so the switch and the trash stop
     their own clicks from reaching it. */
  .mcp-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 10px;
  }

  .mcp-card {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 9px 10px;
    border: 1px solid var(--el-border-color-extra-light);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    cursor: pointer;
    transition: border-color var(--pg-transition);

    &:hover {
      border-color: var(--el-color-primary);
    }

    &:focus-visible {
      outline: none;
      border-color: var(--el-color-primary);
    }

    /* A switched-off card reads as parked, not broken — dimmed, but its
       status line still says 已停用 rather than pretending a state. */
    &.off {
      opacity: 0.62;
    }
  }

  .mcp-card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .mcp-card-status {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .mcp-status-text {
    flex: 1 1 0;
    min-width: 0;
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mcp-card-foot {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;

    .icon-btn {
      padding: 3px;
    }
  }

  /* The add card: same frame, dashed and hollow — an invitation, not a
     server. Height follows the grid's stretch, so it matches its neighbours. */
  .mcp-add {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    min-height: 92px;
    border: 1px dashed var(--el-border-color);
    border-radius: 8px;
    background: transparent;
    font: inherit;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    .el-icon {
      font-size: 16px;
    }

    &:hover {
      color: var(--el-color-primary);
      border-color: var(--el-color-primary);
    }
  }

  .mcp-dot {
    flex: 0 0 auto;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--el-text-color-disabled);

    &.on {
      background: var(--el-color-success);
    }

    &.wait {
      background: var(--el-color-warning);
    }

    &.bad {
      background: var(--el-color-danger);
    }
  }

  .mcp-name {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* The serve section's status line: the same light grammar as the cards
     (green serving, yellow restart-pending, gray off), the summary text, and
     the endpoint paths in mono. */
  .mcp-srv-status {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    font-size: 12px;
    color: var(--el-text-color-regular);

    .mcp-srv-status-text {
      flex: 1 1 0;
      min-width: 0;
    }

    .mcp-srv-eps {
      flex: 0 0 auto;
      font-family: var(--pg-mono, monospace);
      font-size: 11px;
      color: var(--el-text-color-secondary);
      background: var(--el-fill-color-light);
      border-radius: 999px;
      padding: 1px 8px;
    }
  }

  /* The advanced fold: a quiet text-button with the attach sheet's caret
     grammar. */
  .mcp-srv-adv {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    align-self: flex-start;
    border: 0;
    background: transparent;
    padding: 2px 0;
    font: inherit;
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
    }
  }

  /* The serve allowlist's grouped check tree: quiet section captions over
     tri-state group heads (the attach sheet's count-badge grammar), tool
     rows with a dim description, and the live include_tools preview in mono
     underneath. */
  .mcp-pick {
    display: flex;
    flex-direction: column;
    gap: 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    padding: 8px 10px;
  }

  .mcp-pick-sect {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .mcp-pick-sect-title {
    font-size: 11px;
    font-weight: 600;
    color: var(--el-text-color-secondary);
  }

  .mcp-pick-group {
    display: flex;
    flex-direction: column;
    gap: 2px;

    &.down {
      opacity: 0.55;
    }
  }

  .mcp-pick-group-head {
    display: flex;
    align-items: center;
    gap: 6px;

    .el-checkbox {
      flex: 1 1 0;
      min-width: 0;
      height: auto;
    }
  }

  .mcp-pick-group-name {
    font-family: var(--pg-mono, monospace);
    font-size: 11.5px;
  }

  .mcp-pick-count {
    flex: 0 0 auto;
    font-family: var(--pg-mono, monospace);
    font-size: 11px;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color-light);
    border-radius: 999px;
    padding: 1px 8px;
  }

  .mcp-pick-tools {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding-left: 22px;
  }

  .mcp-pick-tool {
    display: flex;
    align-items: baseline;
    gap: 6px;
    min-width: 0;

    .el-checkbox {
      flex: 0 0 auto;
      height: auto;
      margin-right: 0;
    }
  }

  .mcp-pick-tool-name {
    font-size: 12px;
    color: var(--el-text-color-primary);
  }

  .mcp-pick-write {
    flex: 0 0 auto;
    font-size: 10px;
    line-height: 1.4;
    color: var(--el-color-warning);
    border: 1px solid var(--el-color-warning-light-5);
    border-radius: 4px;
    padding: 0 4px;
  }

  /* Runtime package rows: a neutral metadata badge (file/artifact hints), an
     ok variant for read-only, the group's state chip, and the contract id in
     mono — all in the write badge's grammar. */
  .mcp-pick-badge {
    flex: 0 0 auto;
    font-size: 10px;
    line-height: 1.4;
    color: var(--el-text-color-secondary);
    border: 1px solid var(--el-border-color);
    border-radius: 4px;
    padding: 0 4px;

    &.ok {
      color: var(--el-color-success);
      border-color: var(--el-color-success-light-5);
    }
  }

  .mcp-pick-state {
    flex: 0 0 auto;
    font-family: var(--pg-mono, monospace);
    font-size: 10px;
    line-height: 1.4;
    color: var(--el-color-warning);
    border: 1px solid var(--el-color-warning-light-5);
    border-radius: 4px;
    padding: 0 4px;

    &.ok {
      color: var(--el-color-success);
      border-color: var(--el-color-success-light-5);
    }
  }

  .mcp-pick-contract {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--pg-mono, monospace);
    font-size: 10.5px;
    color: var(--el-text-color-secondary);
  }

  .mcp-pick-tool-sub {
    flex: 1 1 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .mcp-pick-preview {
    display: flex;
    align-items: baseline;
    gap: 6px;
    margin: 0;
    font-family: var(--pg-mono, monospace);
    font-size: 11px;
    color: var(--el-text-color-regular);
    word-break: break-all;

    .mcp-pick-preview-label {
      flex: 0 0 auto;
      color: var(--el-text-color-secondary);
      background: var(--el-fill-color-light);
      border-radius: 4px;
      padding: 1px 6px;
    }
  }

  /* The equivalent-curl board: a quiet pre block with its own head row, like
     the probe verdict below it. Wraps like the curl preview's block does. */
  .mcp-curl {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    overflow: hidden;

    .mcp-curl-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
      padding: 5px 10px;
      background: var(--el-fill-color-light);
      font-size: 11.5px;
      color: var(--el-text-color-secondary);

      .mini {
        border-style: solid;
      }
    }

    .mcp-curl-pre {
      margin: 0;
      padding: 10px;
      max-height: 28vh;
      overflow: auto;
      font-family: var(--pg-mono, monospace);
      font-size: 11.5px;
      line-height: 1.6;
      white-space: pre-wrap;
      word-break: break-all;
      color: var(--el-text-color-primary);
      background: var(--el-fill-color-blank);
    }

    .mcp-curl-note {
      margin: 0;
      padding: 5px 10px;
      font-size: 11px;
      line-height: 1.5;
      color: var(--el-color-warning);
      background: var(--el-color-warning-light-9, #fdf6ec);
    }
  }

  /* The trust badge names the file field verbatim and wears danger's outline:
     a trusted server's tools skip the approval gate, so the mark should read
     as armed, not informational. */
  .mcp-badge {
    flex: 0 0 auto;
    font-family: var(--pg-mono, monospace);
    font-size: 10.5px;
    line-height: 1.4;
    color: var(--el-color-danger);
    border: 1px solid var(--el-color-danger-light-5, var(--el-color-danger));
    border-radius: 999px;
    padding: 0 7px;
  }

  .mcp-summary {
    flex: 1 1 0;
    min-width: 0;
    font-family: var(--pg-mono, monospace);
    font-size: 11px;
    color: var(--el-text-color-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* The probe verdict under the form: a quiet frame like the curl dialog's
     probe-res, success in green, the failure's reason in red and wrapping. */
  .mcp-probe {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    padding: 8px 10px;
    font-size: 12px;
    /* Keeps scrollIntoView (probeMCP) from parking the card flush against
       the pane's bottom edge, where the footer makes it read as clipped. */
    scroll-margin-bottom: 12px;

    &.bad {
      border-color: var(--el-color-danger-light-5, var(--el-color-danger));
    }

    .mcp-probe-head {
      margin: 0;
      font-weight: 600;
      color: var(--el-color-success);
    }

    .mcp-probe-tools {
      margin: 4px 0 0;
      font-family: var(--pg-mono, monospace);
      font-size: 11.5px;
      color: var(--el-text-color-regular);
      word-break: break-all;
    }

    .mcp-probe-err {
      margin: 0;
      font-family: var(--pg-mono, monospace);
      font-size: 12px;
      white-space: pre-wrap;
      word-break: break-all;
      color: var(--el-color-danger);
    }

    .mcp-probe-warn {
      margin: 6px 0 0;
      font-size: 11.5px;
      color: var(--el-color-warning);

      summary {
        cursor: pointer;
        font-weight: 600;
      }

      .mcp-probe-issue {
        margin: 4px 0 0;
        font-family: var(--pg-mono, monospace);
        font-size: 11px;
        color: var(--el-text-color-regular);
        white-space: pre-wrap;
        word-break: break-all;
      }
    }
  }

  .raw-editor {
    width: 100%;
    min-height: 320px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    color: var(--el-text-color-primary);
    font-family: var(--pg-mono, monospace);
    font-size: 12px;
    line-height: 1.55;
    padding: 10px;
    outline: none;
    resize: vertical;
    box-sizing: border-box;

    &:focus {
      border-color: var(--el-color-primary);
    }
  }

  /* The wrapper holds the editor plus its tools row. .full lifts the box out
     of the pane to cover the viewport (fixed escapes the dialog body's
     overflow: hidden; no transformed ancestor once the dialog's open
     transition has finished), and the tools row floats to the corner. */
  .raw-wrap {
    position: relative;
    display: flex;
    flex-direction: column;

    &:focus {
      outline: none;
    }

    &.full {
      position: fixed;
      inset: 0;
      z-index: 5;
      padding: 12px;
      box-sizing: border-box;
      background: var(--el-bg-color);

      .raw-editor,
      .raw-pretty {
        flex: 1;
        min-height: 0;
        resize: none;
      }

      .raw-tools {
        position: absolute;
        top: 20px;
        right: 28px;
        margin-bottom: 0;
      }
    }
  }

  .raw-tools {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 6px;
    margin-bottom: 6px;
    /* Above the tree/textarea when floating in fullscreen: they follow the
       tools in DOM order and would otherwise paint over them (a hovered first
       row's background swallowed the toggle). z-index only bites once the
       tools are positioned, which the .full rule above does. */
    z-index: 2;
  }

  .raw-toggle {
    display: flex;
    gap: 2px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 5px;
    background: var(--el-fill-color);
    padding: 1px;

    .mode {
      border: 0;
      background: transparent;
      padding: 2px 10px;
      border-radius: 4px;
      font-size: 11px;
      color: var(--el-text-color-secondary);
      cursor: pointer;

      &.on {
        background: var(--el-bg-color);
        color: var(--el-text-color-primary);
      }
    }
  }

  .raw-full-btn {
    display: inline-flex;
    align-items: center;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 4px;
    padding: 4px;
    background: var(--el-fill-color);
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      background: var(--el-fill-color-dark);
      color: var(--el-text-color-primary);
    }
  }

  /* The pretty tree, re-tokened onto the skin: the library's defaults are
     light-only, the syntax colours here are the same --pg-syn-* ramp the code
     viewer uses. */
  .raw-pretty {
    width: 100%;
    min-height: 320px;
    overflow: auto;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    padding: 10px;
    box-sizing: border-box;
    font-family: var(--pg-mono, monospace);
    font-size: 12px;
    line-height: 1.55;

    &.vjs-tree {
      background: transparent;
      color: var(--el-text-color-primary);
    }

    /* Unscoped block (the dialog teleports to body), so plain selectors — no
       :deep() wrappers, which only scoped styles rewrite. */
    .vjs-tree-node:hover,
    .vjs-tree-node.is-highlight {
      background-color: var(--el-fill-color-light);
    }

    .vjs-key {
      color: var(--pg-syn-keyword);
    }

    .vjs-value-string {
      color: var(--pg-syn-string);
    }

    .vjs-value-number {
      color: var(--pg-syn-number);
    }

    .vjs-value-boolean,
    .vjs-value-null,
    .vjs-value-undefined {
      color: var(--pg-syn-type);
    }

    .vjs-colon,
    .vjs-tree-brackets,
    .vjs-carets,
    .vjs-node-index {
      color: var(--el-text-color-secondary);
    }

    .vjs-comment {
      color: var(--pg-syn-comment);
    }
  }

  .warnings {
    margin: 8px 0 0;
    padding: 8px 10px 8px 26px;
    border-radius: 7px;
    background: var(--el-color-warning-light-9, #fdf6ec);
    font-size: 11.5px;
    color: var(--el-text-color-regular);
    max-height: 120px;
    overflow-y: auto;
  }
}

/* The curl preview dialog: same teleport-to-body caveat as its parent, so it
   gets its own prefix and unscoped home too. */
.settings-curl-dialog {
  .curl-toggles {
    display: flex;
    align-items: center;
    gap: 16px;
    margin-bottom: 8px;
    flex-wrap: wrap;
  }

  .curl-metadata-toggle {
    margin-left: auto;
    border: 1px solid var(--el-border-color);
    border-radius: 7px;
    padding: 6px 10px;
    background: var(--el-bg-color);
    color: var(--el-text-color-regular);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    transition: color .15s, border-color .15s, background .15s;
    &:hover:not(:disabled), &.active { color: var(--el-color-primary); border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
    &:disabled { opacity: .45; cursor: default; }
  }

  .stream-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    input {
      margin: 0;
    }
  }

  .curl-pre {
    margin: 0;
    padding: 10px;
    border-radius: 7px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
    font-family: var(--pg-mono, monospace);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 40vh;
    overflow-y: auto;

    // Token colours from the same skin vars CodeBlock uses — the preview is a
    // code block in everything but name. Classes are the tokenizer's kinds.
    .row {
      display: flex;
    }

    .txt {
      white-space: pre-wrap;
      word-break: break-all;
      min-width: 0;
    }

    .comment {
      color: var(--pg-syn-comment);
      font-style: italic;
    }

    .string {
      color: var(--pg-syn-string);
    }

    .number {
      color: var(--pg-syn-number);
    }

    .keyword {
      color: var(--pg-syn-keyword);
    }

    .type {
      color: var(--pg-syn-type);
    }

    .func {
      color: var(--pg-syn-func);
    }
  }

  .probe-res {
    margin-top: 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    overflow: hidden;

    &.bad {
      border-color: var(--el-color-danger-light-5, var(--el-color-danger));

      .probe-head {
        color: var(--el-color-danger);
      }
    }

    .probe-head {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 6px 10px;
      background: var(--el-fill-color-light);
      font-size: 12px;
      color: var(--el-text-color-primary);

      .probe-meta {
        color: var(--el-text-color-secondary);
      }
    }

    .probe-body {
      margin: 0;
      padding: 10px;
      font-family: var(--pg-mono, monospace);
      font-size: 12px;
      line-height: 1.55;
      white-space: pre-wrap;
      word-break: break-all;
      max-height: 30vh;
      overflow-y: auto;
      color: var(--el-text-color-primary);
    }

    .probe-error {
      margin: 0;
      padding: 10px;
      font-family: var(--pg-mono, monospace);
      font-size: 12px;
      line-height: 1.55;
      white-space: pre-wrap;
      word-break: break-all;
      max-height: 30vh;
      overflow-y: auto;
      color: var(--el-color-danger);
    }
  }

  /* The vision verdict block shares the probe's frame; the verdict colouring
     lands on the head and the marks, never on the question text itself. */
  .vision-res {
    margin-top: 10px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    overflow: hidden;

    &.yes .probe-head {
      color: var(--el-color-success);
    }

    &.no .probe-head {
      color: var(--el-color-danger);
    }

    .vision-q {
      display: flex;
      align-items: baseline;
      gap: 8px;
      padding: 5px 10px;
      font-size: 12px;
      border-top: 1px solid var(--el-border-color-extra-light);

      .vision-mark {
        flex: none;
        font-weight: 600;
        color: var(--el-color-danger);
      }

      &.pass .vision-mark {
        color: var(--el-color-success);
      }

      .vision-ask {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        color: var(--el-text-color-regular);
      }

      .vision-ans {
        flex: none;
        max-width: 40%;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        color: var(--el-text-color-secondary);
      }

      .vision-key {
        flex: none;
        color: var(--el-text-color-secondary);
      }
    }
  }

  .hint {
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    margin: 8px 0 0;
    line-height: 1.5;
  }
}

/* The MCP import dialog: same teleport-to-body caveat as the curl preview, so
   its own prefix and unscoped home. */
.settings-mcp-import-dialog {
  .hint {
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    margin: 0 0 6px;
    line-height: 1.5;
  }

  /* An inline text link inside a hint line — quieter than a .mini button for
     the "no idea? get the example" nudge. */
  .mcp-link {
    border: 0;
    background: transparent;
    padding: 0;
    font: inherit;
    color: var(--el-color-primary);
    cursor: pointer;
    text-decoration: underline;
    text-underline-offset: 2px;
  }

  .mcp-import-editor {
    width: 100%;
    min-height: 140px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    color: var(--el-text-color-primary);
    font-family: var(--pg-mono, monospace);
    font-size: 12px;
    line-height: 1.55;
    padding: 10px;
    outline: none;
    resize: vertical;
    box-sizing: border-box;

    &:focus {
      border-color: var(--el-color-primary);
    }
  }

  .hint-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: 6px 0;

    .hint {
      flex: 1 1 0;
      margin: 0;
    }

    .hint-actions {
      display: flex;
      gap: 6px;
      flex: 0 0 auto;
    }

    .mini {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      border: 1px dashed var(--el-border-color);
      background: transparent;
      padding: 3px 10px;
      border-radius: 6px;
      font: inherit;
      font-size: 11.5px;
      color: var(--el-text-color-secondary);
      cursor: pointer;

      &:hover:not(:disabled) {
        color: var(--el-color-primary);
        border-color: var(--el-color-primary);
      }

      &:disabled {
        opacity: 0.5;
        cursor: default;
      }
    }
  }

  /* The parse preview: one row per entry — verdict mark, name, then the
     summary or the reason. Bad rows turn the mark and the reason red; the
     entry stays readable either way. */
  .mcp-import-res {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 7px;
    padding: 6px 10px;
    max-height: 40vh;
    overflow-y: auto;

    .note.bad {
      margin: 0;
      font-size: 12px;
      color: var(--el-color-danger);
      word-break: break-all;
    }
  }

  .mcp-import-row {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 3px 0;
    font-size: 12px;

    .mcp-import-mark {
      flex: none;
      font-weight: 600;
      color: var(--el-color-success);
    }

    &.bad .mcp-import-mark {
      color: var(--el-color-danger);
    }

    .mcp-import-name {
      flex: none;
      max-width: 30%;
      font-weight: 600;
      color: var(--el-text-color-primary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .mcp-import-sum {
      flex: 1 1 0;
      min-width: 0;
      font-family: var(--pg-mono, monospace);
      font-size: 11px;
      color: var(--el-text-color-secondary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .mcp-import-over {
      flex: none;
      font-size: 11px;
      color: var(--el-color-warning);
      border: 1px solid var(--el-color-warning-light-5, var(--el-color-warning));
      border-radius: 999px;
      padding: 0 7px;
    }

    .mcp-import-err {
      flex: 1 1 0;
      min-width: 0;
      font-size: 11.5px;
      color: var(--el-color-danger);
      word-break: break-all;
    }
  }

  .mcp-import-res .hint {
    margin: 4px 0 2px;
  }
}

/* The accounts table: monospaced names, quiet borders — a roster, not a grid
   of editable fields (management is editing users.json, by design). */
.sect-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;

  h4 {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }
}

.acct-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;

  th,
  td {
    padding: 7px 10px;
    border-bottom: 1px solid var(--el-border-color-lighter);
    text-align: left;
    color: var(--el-text-color-regular);
  }

  th {
    color: var(--el-text-color-secondary);
    font-weight: 500;
  }

  td:first-child {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  /* The actions cell holds change-password and delete side by side. */
  td:last-child .mini + .mini {
    margin-left: 6px;
  }

  .acct-empty {
    text-align: center;
    color: var(--el-text-color-placeholder);
  }
}

.hint.rejected {
  color: var(--el-color-danger);
}

/* The per-account change form: a quiet inset so it reads as attached to the
   row that opened it, not a second section. */
.pw-box {
  margin-top: 14px;
  padding: 14px 16px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-fill-color-lighter);
}
</style>
