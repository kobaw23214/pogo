<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { agentWorkflowCanvasSize, isAgentWorkflowWorkspace } from "@/agent/agentWorkflowWorkspace";
import { agentWorkflowRunStatusKey, useAgentWorkflowRunStatus } from "@/agent/useAgentWorkflowRunStatus";
import { useI18n } from "vue-i18n";
import { List, ArrowLeft, ArrowRight, Close, Document, Plus } from "@element-plus/icons-vue";
import { Icon } from "@iconify/vue";
import FilesPanel from "./FilesPanel.vue";
import FilePreview from "./FilePreview.vue";
import SkillDocumentPreview from "./SkillDocumentPreview.vue";
import HTMLBrowserPane from "./HTMLBrowserPane.vue";
import ShellPanel from "./ShellPanel.vue";
import ShortcutWorkspace from "./ShortcutWorkspace.vue";
import type { InformationEntry } from "@/api/information";
import SkillsPanel from "./SkillsPanel.vue";
import MemoryPanel from "./MemoryPanel.vue";
import CommandTokenIcon from "./CommandTokenIcon.vue";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import SubagentSheet from "./SubagentSheet.vue";
import SentinelSheet from "./SentinelSheet.vue";
import TaskSheet from "./TaskSheet.vue";
import AgentWorkflowSheet from "./AgentWorkflowSheet.vue";
import type { AgentWorkflowLocation } from "@/agent/agentWorkflowNavigation";
import BrowserPane from "./BrowserPane.vue";
import { resolveTab, type DockTab } from "./dockSheets";
import { browserFavicon } from "@/agent/browserFavicon";
import { fileIcon, skillsIcon, terminalIcon, workspaceIcon, sessionIcon } from "./fileIcons";
import type { PanelInfo, StarterCard } from "@/api/types";
import type { OfficeReferenceSnapshot } from "@/api/types";
import type { TimelineItem } from "@/agent/timeline";
import { officeApi, officeViewMounted, observeOfficeOpen, type OfficeSession } from "@/agent/officeTransport";
import type { OfficeRevealRequest, OfficeViewContext } from "@/agent/officeImmersion";
import { attachmentWorkspacePath, sameWorkspaceFile, workspaceFilePath } from "@/agent/officeFiles";

const { t, locale } = useI18n();

// The dock is a sheet container docked on the right at full height, the
// conversation on its left — the split workflow and shortcut sheets set.
// A slim rail is always visible and switches sheets; clicking the active icon
// collapses the dock. Files, the hub, shortcuts and background tasks stay at
// the top; the skills-and-tools catalog has its own entry at the bottom.
//
// The hub is a browser-style tab strip: the shell and every -web-panel are
// pinned tabs, and each subagent anchor opened from the conversation adds a
// closable one. A strip, not a dropdown, because the tabs are peers and the
// strip is the control that shows all of them — which one is up, which are
// still running — at a glance.
const props = defineProps<{
  active: string | null;
  /** The hub's whole strip: every open sheet, shell and browser tabs included. */
  tabs: DockTab[];
  /** Registered -web-panels — the seeds of a browser tab's start-page card
   * grid. Restored panel tabs still validate against this set. */
  panels: PanelInfo[];
  /** Which tab is showing. Null falls back to the first tab — see resolveTab. */
  activeTab: string | null;
  /** Subagent tab id → still running. Drives the pulsing dot and the sheet's
   * polling; shell and panel tabs have no liveness to show. */
  liveTabs: Record<string, boolean>;
  /** Hash route to open the active panel tab at, e.g. "#/clusters". */
  at?: string | null;
  /** Hub fills the whole post-sidebar area; only honoured while the hub is open. */
  maximized: boolean;
  /** Layout-only expansion; all existing pane instances remain in place. */
  immersive?: boolean;
  fullscreen?: boolean;
  fullscreenAvailable?: boolean;
  items: TimelineItem[];
  workspace?: string;
  sessionId: string | null;
  fileRevision?: number;
  browserBusy?: boolean;
  browserClosingTabIds?: readonly string[];
  browserRestoring?: boolean;
  browserRestoreError?: string;
  /** Studio shows the selected session library; installing Skills belongs to management. */
  studioBound?: boolean;
  toolRevision?: number;
  toolsPending?: boolean;
  agentId?: string;
  workflowSelection?: AgentWorkflowLocation & { sequence: number };
  workflowConversationOpen?: boolean;
  workflowImmersive?: boolean;
  shortcutsDisabled?: boolean;
  informationBusy?: boolean;
  /** A scheduled job is failing: the clock's rail button carries the same
   * red dot the sidebar entry used to. */
  scheduleAttention?: boolean;
}>();

const workflowRunStatus = useAgentWorkflowRunStatus(() => ({ agentId: props.agentId, revision: props.toolRevision }));
provide(agentWorkflowRunStatusKey, workflowRunStatus);
const workflowRailState = computed(() => {
  const { active, waiting, running, queued, cancelling } = workflowRunStatus.counts.value;
  const english = locale.value.startsWith("en");
  const parts: string[] = [];
  if (waiting) parts.push(english ? `${waiting} awaiting your confirmation` : `${waiting} 项等待本人确认`);
  if (running) parts.push(english ? `${running} running` : `${running} 项运行中`);
  if (queued) parts.push(english ? `${queued} queued` : `${queued} 项排队中`);
  if (cancelling) parts.push(english ? `${cancelling} cancelling` : `${cancelling} 项取消中`);
  const stale = workflowRunStatus.stale.value;
  const suffix = stale ? (english ? "Status unavailable; last known activity" : "状态暂不可用，显示上次已知进度")
    : workflowRunStatus.connection.value === "loading" ? (english ? "Checking run status" : "正在获取运行状态") : "";
  return {
    kind: stale ? "stale" : waiting ? "waiting" : running ? "running" : cancelling ? "cancelling" : queued ? "queued" : "idle",
    label: [t("agentWorkflow.title"), ...parts, suffix].filter(Boolean).join(" · "),
    badge: stale ? "?" : active > 99 ? "99+" : String(active),
    show: active > 0 || stale,
  };
});
const emit = defineEmits<{
  "update:active": [string | null];
  "update:activeTab": [string];
  closeTab: [string];
  navigateFile: [path: string];
  openSentinelFile: [file: { path: string; name: string; lineStart?: number; lineEnd?: number; sourceSessionId?: string; parentSessionId?: string }];
  "update:maximized": [boolean];
  "update:immersive": [boolean];
  fileOfficeEditing: [context: OfficeViewContext | null];
  officeOpened: [request: OfficeRevealRequest];
  fullscreen: [];
  /** A panel asked the conversation something; the text is not yet policy-checked. */
  intent: [string];
  workflowWorkspace: [boolean];
  workflowConversation: [boolean];
  "update:workflowImmersive": [boolean];
  /** A picked page element wants to join the composer as context. */
  insertShortcut: [snapshot: ShortcutSnapshot];
  insertInformation: [entry: InformationEntry];
  openStarter: [card: StarterCard];
  manageStarters: [];
  officeReference: [snapshot: OfficeReferenceSnapshot];
  pick: [pick: import("@/agent/elementPicker").ElementPick];
  /** The "+" menu's chosen sheet wants to become a tab. */
  addTab: [tab: DockTab];
  /** A pane learned its page's title; the tab renames itself. */
  renameTab: [id: string, label: string];
  /** A browser tab committed a navigation; the tab remembers where it is. */
  navigateTab: [id: string, url: string];
  browserState: [state: import("@/agent/nativeBrowser").NativeBrowserState];
  browserRecover: [];
  browserReopen: [id: string, url: string];
  openCurrentHtmlBrowser: [request: { url: string; id: string; sessionId: string; path: string }];
  claimHtmlCapability: [tabID: string, capabilityID: string];
  openHtmlBrowser: [request: { url: string; id: string; sessionId: string }];
}>();

const filePanel = ref<{ previewPath(): string | null; activeOfficePath(): string | null } | null>(null);
type PreviewHandle = { prepareClose(): Promise<boolean> };
const previewHandles = new Map<string, PreviewHandle>();
function previewRef(id: string, value: unknown) {
  if (value && typeof (value as PreviewHandle).prepareClose === "function") previewHandles.set(id, value as PreviewHandle);
  else previewHandles.delete(id);
}
async function requestTabClose(id: string) {
  const preview = previewHandles.get(id);
  if (preview && !await preview.prepareClose()) return;
  emit("closeTab", id);
}
const officeSessions = ref<Record<string, string>>({});
const fileOfficeSessions = ref<Record<string, string>>({});
function officeOpened(context: OfficeViewContext, tabId?: string) {
  if (context.conversationId !== props.sessionId) return;
  const sheet = tabId ? "hub" : "files";
  if (props.active !== sheet || (tabId && tabId !== (props.activeTab || props.tabs[0]?.id))) return;
  emit("officeOpened", { ...context, sheet, ...(tabId ? { tabId } : {}) });
}
function focusOffice(path: string) {
  if (sameWorkspaceFile(filePanel.value?.activeOfficePath() || undefined, path)) { emit("update:active", "files"); return; }
  const existing = props.tabs.find(tab => tab.kind === "preview" && sameWorkspaceFile(tab.path, path));
  if (existing) { emit("update:activeTab", existing.id); emit("update:active", "hub"); }
}
function openOfficeFile(rawPath: string) {
  const path = workspaceFilePath(rawPath);
  const existing = props.tabs.find(tab => tab.kind === "preview" && sameWorkspaceFile(tab.path, path));
  const id = existing?.id || `preview:${path}`;
  if (!existing) emit("addTab", { id, label: path.split("/").pop() || path, kind: "preview", path, closable: true });
  mountedSheets.value.add("hub"); mountedTabs.value.add(id);
  emit("update:activeTab", id); emit("update:active", "hub");
}
const requestedOfficeViews = new Set<string>();
const closedOfficeViews = new Set<string>();
function officeClosed(id: string) {
  closedOfficeViews.add(id);
  officeSessions.value = Object.fromEntries(Object.entries(officeSessions.value).filter(([, value]) => value !== id));
  fileOfficeSessions.value = Object.fromEntries(Object.entries(fileOfficeSessions.value).filter(([, value]) => value !== id));
}
let officeDiscoveryEpoch = 0;
let officeDiscoveryEnabled = false;
let officeDiscoveryRunning = false;
let officeDiscoveryTimer: ReturnType<typeof setInterval> | undefined;
function revealOffice(session: OfficeSession) {
  if (session.closed || closedOfficeViews.has(session.id) || session.conversationId !== props.sessionId) return;
  if (officeViewMounted(session.id)) { focusOffice(session.path); return; }
  if (session.ready || session.instanceBound) return;
  requestedOfficeViews.add(session.id);
  if (sameWorkspaceFile(filePanel.value?.previewPath() || undefined, session.path)) {
    fileOfficeSessions.value = { ...fileOfficeSessions.value, [session.path]: session.id };
    emit("update:active", "files"); return;
  }
  officeSessions.value = { ...officeSessions.value, [session.path]: session.id };
  const existing = props.tabs.find(tab => tab.kind === "preview" && sameWorkspaceFile(tab.path, session.path));
  const id = existing?.id || `preview:${session.path}`;
  if (!existing) emit("addTab", { id, label: session.title || session.path.split("/").pop() || session.path, kind: "preview", path: session.path, closable: true });
  mountedSheets.value.add("hub"); mountedTabs.value.add(id);
  emit("update:activeTab", id); emit("update:active", "hub");
}
const removeOfficeOpenObserver = observeOfficeOpen(request => {
  if (request.conversationId !== props.sessionId || closedOfficeViews.has(request.id)) return;
  const epoch = officeDiscoveryEpoch;
  void officeApi.status(request.id).then(({ session }) => {
    if (epoch !== officeDiscoveryEpoch || session.id !== request.id || session.conversationId !== request.conversationId || session.path !== request.path || session.fileType !== request.fileType) return;
    revealOffice(session);
  }).catch(() => {});
});
async function discoverOffice() {
  if (!officeDiscoveryEnabled || !props.sessionId || officeDiscoveryRunning) return;
  const epoch = officeDiscoveryEpoch, conversationId = props.sessionId;
  officeDiscoveryRunning = true;
  try {
    const result = await officeApi.list(conversationId);
    if (epoch !== officeDiscoveryEpoch || conversationId !== props.sessionId) return;
    const records = result.sessions.filter(session => !session.closed && !closedOfficeViews.has(session.id) && session.conversationId === conversationId);
    const hubMapping: Record<string, string> = {}, fileMapping: Record<string, string> = {};
    for (const session of records) {
      if (session.ready && !officeViewMounted(session.id)) continue;
      const inFiles = sameWorkspaceFile(filePanel.value?.activeOfficePath() || undefined, session.path) || (!officeViewMounted(session.id) && sameWorkspaceFile(filePanel.value?.previewPath() || undefined, session.path));
      (inFiles ? fileMapping : hubMapping)[session.path] = session.id;
    }
    officeSessions.value = hubMapping; fileOfficeSessions.value = fileMapping;
    for (const session of records) {
      if (session.ready || officeViewMounted(session.id) || requestedOfficeViews.has(session.id)) continue;
      revealOffice(session);
    }
  } catch { /* Office discovery never replaces the existing file preview on failure. */ }
  finally { officeDiscoveryRunning = false; }
}
watch(() => props.sessionId, async () => {
  const epoch = ++officeDiscoveryEpoch;
  officeDiscoveryEnabled = false; officeSessions.value = {}; fileOfficeSessions.value = {}; requestedOfficeViews.clear();
  if (!props.sessionId) return;
  try {
    const config = await officeApi.configuration();
    if (epoch !== officeDiscoveryEpoch) return;
    officeDiscoveryEnabled = config.enabled; void discoverOffice();
  } catch { /* An unconfigured backend retains its original previews. */ }
}, { immediate: true });
onMounted(() => { officeDiscoveryTimer = setInterval(() => void discoverOffice(), 1200); });
onBeforeUnmount(() => { officeDiscoveryEpoch += 1; clearInterval(officeDiscoveryTimer); removeOfficeOpenObserver(); });

const failedFavicons = ref(new Map<string, string>());
const tabFavicons = computed(() => new Map(props.tabs.flatMap(tab => {
  const src = tab.kind === "browser" ? browserFavicon(tab.favicon) : undefined;
  return src && failedFavicons.value.get(tab.id) !== src ? [[tab.id, src] as const] : [];
})));
function faviconFailed(tab: DockTab) {
  const src = browserFavicon(tab.favicon);
  if (src) failedFavicons.value.set(tab.id, src);
}
watch(() => props.tabs, tabs => {
  const ids = new Set(tabs.map(tab => tab.id));
  for (const id of failedFavicons.value.keys()) if (!ids.has(id)) failedFavicons.value.delete(id);
});

const open = computed(() => props.active !== null);
const hubOpen = computed(() => props.active === "hub");
const workflowWorkspace = ref(false);
const canvasWorkspace = computed(() => isAgentWorkflowWorkspace(props.active, workflowWorkspace.value, props.agentId, !!props.immersive, !!props.workflowImmersive));
function setWorkflowImmersive(requested: boolean) {
  const active = isAgentWorkflowWorkspace(props.active, workflowWorkspace.value, props.agentId, !!props.immersive, requested);
  if (active !== !!props.workflowImmersive) emit("update:workflowImmersive", active);
}
watch(() => props.agentId, () => { workflowWorkspace.value = false; setWorkflowImmersive(false); });
watch(() => props.sessionId, () => { setWorkflowImmersive(false); });
watch(() => props.active, active => { if (active !== "workflows") setWorkflowImmersive(false); });
watch(() => props.immersive, active => { if (active) setWorkflowImmersive(false); });
watch(workflowWorkspace, selected => { if (!selected) setWorkflowImmersive(false); });
watch(canvasWorkspace, active => {
  emit("workflowWorkspace", active);
  if (!active) emit("workflowConversation", false);
}, { immediate: true });

function toggle(id: string) {
  emit("update:active", props.active === id ? null : id);
}

// Generic app-window icon: the hub's rail icon while collapsed, and every
// panel tab's icon.
const panelIcon = {
  body:
    '<rect x="3" y="4" width="18" height="16" rx="2" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<path d="M3 9h18" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<circle cx="6.5" cy="6.5" r="0.5" fill="currentColor"/>' +
    '<circle cx="9.5" cy="6.5" r="0.5" fill="currentColor"/>',
  width: 24,
  height: 24,
};

// A delegated run's tab icon: a small agent glyph, so the strip says what the
// tab is before anyone reads its label.
const agentIcon = {
  body:
    '<circle cx="12" cy="6.5" r="3" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<path d="M5 20c0-3.9 3.1-7 7-7s7 3.1 7 7" fill="none" stroke="currentColor" stroke-width="2"/>',
  width: 24,
  height: 24,
};

const tab = computed(() => resolveTab(props.tabs, props.activeTab));
const canImmerse = computed(() => tab.value && ["browser", "panel", "preview", "skill", "html"].includes(tab.value.kind));
// A preview tab wears the file's own type icon — the strip says what the tab
// is before anyone reads its label, same contract as the shell's terminal and
// the browser's globe.
const tabIcon = (tb: DockTab) =>
  tb.kind === "sentinel" || tb.kind === "task" ? sessionIcon : tb.kind === "shell"
    ? terminalIcon
    : tb.kind === "browser" || tb.kind === "html"
      ? globeIcon
      : tb.kind === "panel"
        ? panelIcon
        : tb.kind === "preview" || tb.kind === "skill"
          ? fileIcon(tb.label)
          : agentIcon;

// The globe is the free-navigation sheet's icon: unlike a panel it belongs to
// no application, it goes wherever the address bar is pointed.
const globeIcon = {
  body:
    '<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" fill="none" stroke="currentColor" stroke-width="2"/>',
  width: 24,
  height: 24,
};

// The add menu offers two kinds of sheet: the browser and the shell. Panels
// are no longer sheets of their own — a browser sheet starts on the new-tab
// page, whose card grid carries the panel links (seeded from the registry and
// curated by hand), so the intranet apps live one card-click away instead of
// one menu-click away and the strip stays two concepts wide.
function addBrowser() {
  if (props.browserBusy) return;
  emit("addTab", { id: "", label: t("agentView.dock.browser"), kind: "browser", closable: true });
}
function addShell() {
  emit("addTab", { id: "shell", label: t("agentView.dock.shell"), kind: "shell", closable: true });
}

// Collapsed, the rail shows the generic hub icon rather than the active tab's:
// an icon that changes while the dock is shut would read as a button that does
// different things on different days. Open, it mirrors the tab, which is what
// gives the single button back some at-a-glance identity.
const hubIcon = computed(() => (hubOpen.value && tab.value ? tabIcon(tab.value) : panelIcon));
const hubTitle = computed(() =>
  tab.value ? `${t("agentView.dock.hub")} · ${tab.value.label}` : t("agentView.dock.hub"),
);

// ── Keep-alive ─────────────────────────────────────────────────────────────
// Every sheet and hub tab, once shown, stays mounted: switching only hides it
// (v-show), it never destroys it (v-if). An iframe torn out of the DOM reloads
// on the way back — the PDF back on page 1, the browser tab back on its start
// page, the shell reconnecting — so the pane's whole live state is the thing
// being preserved here. Mounting stays lazy: a tab nobody opened costs nothing.
const everOpened = ref(false);
const mountedSheets = ref(new Set<string>());
watch(
  open,
  (v) => {
    if (v) everOpened.value = true;
  },
  { immediate: true },
);
watch(
  () => props.active,
  (v) => {
    if (v) mountedSheets.value.add(v);
  },
  { immediate: true },
);
// The hub's per-tab instances, by tab id. A tab mounts on its first activation;
// closing it is the one real destroy — the instance would have nowhere to show.
const mountedTabs = ref(new Set<string>());
watch(
  tab,
  (t) => {
    if (t) mountedTabs.value.add(t.id);
  },
  { immediate: true },
);
watch(
  () => props.tabs,
  (ts) => {
    const live = new Set(ts.map((t) => t.id));
    for (const id of [...mountedTabs.value]) {
      if (!live.has(id)) mountedTabs.value.delete(id);
    }
  },
);



// Maximize is an explicit, temporary override of the 0.5 conversation floor
// below — a force graph or a wide table is worth the whole width for a moment.
// It is scoped to the hub because that is where the sheets that need it live,
// and it is not persisted: a maximized dock on next boot would hide the
// conversation with nothing on screen to explain why.
const maximized = computed(() => canvasWorkspace.value || props.maximized && hubOpen.value);

// The dock's width is a fraction of the space left after the sidebar, not a
// fixed pixel count: collapsing the sidebar or resizing the window must keep
// the split (default is an even 1/2). Drags update the fraction; the pixel
// width is derived from the measured parent.
const RATIO_KEY = "pogo:files-ratio";
// The 0.5 cap is a hard lock, not a default: the conversation never shrinks
// below half of the post-sidebar space, no matter where the drag ends.
const ratio = ref(Math.min(0.5, Math.max(0.15, Number(localStorage.getItem(RATIO_KEY)) || 0.5)));
watch(ratio, (r) => localStorage.setItem(RATIO_KEY, String(r)));
localStorage.removeItem("pogo:files-width");
// Retired with the bottom stack: the dock is always the right-side split now.
localStorage.removeItem("pogo:files-height");

const width = ref(0);
const availW = ref(0);
const availH = ref(0);
const panelEl = ref<HTMLElement | null>(null);
let panelRO: ResizeObserver | undefined;

// measure caches the space the dock may take: everything right of the
// sidebar. Maximize reads it from a computed, so it has to live in refs
// rather than be measured on demand.
function measure() {
  const parent = panelEl.value?.parentElement;
  if (!parent) return;
  const sidebar = parent.querySelector(".sidebar");
  availW.value = parent.clientWidth - (sidebar?.clientWidth ?? 0);
  availH.value = parent.clientHeight;
  applyRatio();
}

// applyRatio derives the pixel width from the fraction. It runs on every
// relevant resize: the observer covers window resizes and the sidebar's own
// collapse, the watchers cover immersion flips and drag updates.
function applyRatio() {
  if (availW.value <= 0) return;
  width.value = Math.round(Math.min(availW.value * 0.5, Math.max(240, ratio.value * availW.value)));
}
watch([ratio, () => props.immersive], measure, { flush: "post" });

onMounted(() => {
  const parent = panelEl.value?.parentElement;
  if (parent) {
    panelRO = new ResizeObserver(measure);
    panelRO.observe(parent);
    const sidebar = parent.querySelector(".sidebar");
    if (sidebar) panelRO.observe(sidebar);
  }
  measure();
});
onBeforeUnmount(() => {
  panelRO?.disconnect();
});

// Paging the tab strip: the strip scrolls sideways on overflow, but a hidden
// scrollbar makes that undiscoverable — Chrome-style arrows appear at the
// strip's edges only while there is something to scroll to. The mutation
// observer catches tab additions and label changes; the resize observer
// catches the strip itself shrinking.
const stripEl = ref<HTMLElement | null>(null);
const stripOverflow = ref(false);
const stripCanLeft = ref(false);
const stripCanRight = ref(false);
let stripRO: ResizeObserver | undefined;
let stripMO: MutationObserver | undefined;

function measureStrip() {
  const el = stripEl.value;
  if (!el) {
    stripOverflow.value = false;
    stripCanLeft.value = stripCanRight.value = false;
    return;
  }
  stripOverflow.value = el.scrollWidth > el.clientWidth + 1;
  stripCanLeft.value = el.scrollLeft > 1;
  stripCanRight.value = el.scrollLeft + el.clientWidth < el.scrollWidth - 1;
}

function pageStrip(direction: 1 | -1) {
  const el = stripEl.value;
  if (el) el.scrollBy({ left: direction * el.clientWidth * 0.85, behavior: "smooth" });
}

onMounted(() => {
  if (stripEl.value) {
    stripRO = new ResizeObserver(measureStrip);
    stripRO.observe(stripEl.value);
    stripMO = new MutationObserver(measureStrip);
    stripMO.observe(stripEl.value, { childList: true, subtree: true, characterData: true });
  }
  measureStrip();
});
onBeforeUnmount(() => {
  stripRO?.disconnect();
  stripMO?.disconnect();
});

// The rail stays at the edge even when no sheet is open: it is the only way
// to open one. So the aside's size is sheet size + rail, or rail alone.
const RAIL = 40;
const panelStyle = computed(() => {
  if (props.immersive) return { width: "100%", height: "100%" };
  if (canvasWorkspace.value) return agentWorkflowCanvasSize(availW.value, availH.value);
  if (!open.value) return { width: `${RAIL}px` };
  if (maximized.value) return { width: `${Math.max(240, availW.value)}px` };
  return { width: `${width.value}px` };
});

const dragging = ref(false);

// Dragging the docked left edge resizes (col-resize). The transition is
// suspended for the drag or it lags behind.
function startDrag(e: MouseEvent) {
  if (canvasWorkspace.value) return;
  e.preventDefault();
  const startPos = e.clientX;
  const startSize = width.value;
  const avail = availW.value;
  dragging.value = true;
  const move = (ev: MouseEvent) => {
    if (canvasWorkspace.value) return;
    // The drag sets pixels live; the fraction follows so the next sidebar
    // collapse or window resize rebuilds the same split.
    width.value = Math.round(Math.min(avail * 0.5, Math.max(240, startSize + (startPos - ev.clientX))));
    if (avail > 0) ratio.value = width.value / avail;
  };
  const up = () => {
    dragging.value = false;
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
  };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
}
</script>

<template>
  <aside
    ref="panelEl"
    class="dock"
    :class="{ open, dragging, maximized, immersive, 'canvas-workspace': canvasWorkspace }"
    :style="panelStyle"
  >
    <div v-if="open && !maximized && !immersive && !canvasWorkspace" class="grip" @mousedown="startDrag" />
    <div v-if="everOpened" v-show="open" class="dock-inner">
      <div class="slot">
        <!-- Sheets switch by hiding, never by destroying — see keep-alive above. -->
        <FilesPanel
          v-if="mountedSheets.has('files')"
          v-show="active === 'files'"
          :items="items"
          :workspace="workspace"
          ref="filePanel"
          :session-id="sessionId"
          :revision="fileRevision"
          :office-sessions="fileOfficeSessions"
          :immersive="immersive"
          @open-office="openOfficeFile"
          @navigate-file="emit('navigateFile', $event)"
          @open-browser="emit('openHtmlBrowser', $event)"
          @open-current-browser="emit('openCurrentHtmlBrowser', $event)"
          @focus-office="focusOffice"
          @office-closed="officeClosed"
          @office-editing="emit('fileOfficeEditing', $event)"
          @office-opened="officeOpened($event)"
          @office-reference="emit('officeReference', $event)"
          @exit-immersive="emit('update:immersive', false)"
          @close="emit('update:active', null)"
        />
        <ShortcutWorkspace
          v-if="mountedSheets.has('shortcuts')"
          v-show="active === 'shortcuts'"
          :agent-id="agentId"
          :session-id="sessionId"
          :disabled="shortcutsDisabled"
          :information-busy="informationBusy"
          :active="active === 'shortcuts'"
          @insert="emit('insertShortcut', $event)"
          @insert-information="emit('insertInformation', $event)"
          @open-starter="emit('openStarter', $event)"
          @manage-starters="emit('manageStarters')"
          @close="emit('update:active', null)"
        />
        <section
          v-if="mountedSheets.has('tasks')"
          v-show="active === 'tasks'"
          class="task-workspace"
          data-testid="task-center-workspace"
        >
          <slot name="tasks" />
        </section>
        <AgentWorkflowSheet
          v-if="mountedSheets.has('workflows') && agentId"
          :key="agentId"
          v-show="active === 'workflows'"
          :agent-id="agentId"
          :session-id="sessionId"
          :visible="active === 'workflows'"
          :selection="workflowSelection"
          :tool-revision="toolRevision"
          :conversation-open="workflowConversationOpen"
          :immersive="canvasWorkspace"
          @workspace="workflowWorkspace = $event"
          @immersive="setWorkflowImmersive"
          @conversation="emit('workflowConversation', $event)"
          @intent="emit('intent', $event)"
          @close="emit('update:active', null)"
        />
        <MemoryPanel
          v-if="mountedSheets.has('memory') && sessionId"
          :key="sessionId"
          v-show="active === 'memory'"
          :active="active === 'memory'"
          :session-id="sessionId"
          @close="emit('update:active', null)"
        />
        <SkillsPanel
          v-if="mountedSheets.has('skills') && (!studioBound || sessionId)"
          :key="studioBound ? `${agentId || ''}:${sessionId || ''}` : 'workspace-skills'"
          v-show="active === 'skills'"
          :agent-id="agentId"
          :active="active === 'skills'"
          :session-id="studioBound ? sessionId || undefined : undefined"
          :read-only="studioBound"
          :tool-revision="toolRevision"
          :tools-pending="toolsPending"
          @close="emit('update:active', null)"
        />
        <!-- The hub: one tab strip for every occupant, so switching tabs does
             not move the layout and close controls around. -->
        <section v-if="mountedSheets.has('hub')" v-show="hubOpen" class="hub-sheet">
          <header class="sheet-head">
            <button v-if="stripOverflow" class="ghost head-btn strip-page" :disabled="!stripCanLeft" :title="t('agentView.dock.scrollTabsLeft')" :aria-label="t('agentView.dock.scrollTabsLeft')" @click="pageStrip(-1)">
              <el-icon><ArrowLeft /></el-icon>
            </button>
            <div ref="stripEl" class="tabstrip" role="tablist" @scroll.passive="measureStrip">
              <button
                v-for="tb in tabs"
                :key="tb.id"
                class="tab"
                :class="{ on: tb.id === tab?.id }"
                role="tab"
                :aria-selected="tb.id === tab?.id"
                :data-sheet-id="tb.id"
                :data-browser-tab-id="tb.browserBinding?.tab_id"
                :data-browser-context-ref="tb.browserBinding?.context_ref"
                :title="tb.label"
                @click="emit('update:activeTab', tb.id)"
                @click.middle.prevent="tb.closable && requestTabClose(tb.id)"
              >
                <span
                  v-if="tb.kind === 'subagent' && liveTabs[tb.id]"
                  class="tablive"
                  aria-hidden="true"
                />
                <img v-else-if="tabFavicons.get(tb.id)" class="tab-favicon" :src="tabFavicons.get(tb.id)" alt="" aria-hidden="true" @error="faviconFailed(tb)" />
                <Icon v-else :icon="tabIcon(tb)" width="13" />
                <span class="tab-name">{{ tb.label }}</span>
                <!-- stop: the × closes the tab, it must not also activate it. -->
                <span
                  v-if="tb.closable"
                  class="x"
                  role="button"
                  :aria-label="t('common.close')"
                  :title="t('common.close')"
                  @click.stop="requestTabClose(tb.id)"
                >
                  <el-icon><Close /></el-icon>
                </span>
              </button>
              <!-- The add button sits right after the last tab — Chrome's
                   placement. A menu, because the hub's occupants are of
                   different kinds and a bare "+" could only guess. -->
              <el-dropdown trigger="click" placement="bottom-start" @command="(cmd: string) => (cmd === 'shell' ? addShell() : addBrowser())">
                <button class="tab add" :title="t('agentView.dock.addSheet')" :aria-label="t('agentView.dock.addSheet')">
                  <el-icon><Plus /></el-icon>
                </button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="browser:@new" :disabled="browserBusy">
                      <Icon :icon="globeIcon" width="13" />
                      {{ t("agentView.dock.browser") }}
                    </el-dropdown-item>
                    <el-dropdown-item command="shell">
                      <Icon :icon="terminalIcon" width="13" />
                      {{ t("agentView.dock.shell") }}
                    </el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
            <button v-if="stripOverflow" class="ghost head-btn strip-page" :disabled="!stripCanRight" :title="t('agentView.dock.scrollTabsRight')" :aria-label="t('agentView.dock.scrollTabsRight')" @click="pageStrip(1)">
              <el-icon><ArrowRight /></el-icon>
            </button>
            <button v-if="canImmerse" class="ghost head-btn immersion-toggle" :class="{ on: immersive }" :title="t(immersive ? 'immersiveBrowser.exitTip' : 'immersiveBrowser.enter')" :aria-label="t(immersive ? 'immersiveBrowser.exit' : 'immersiveBrowser.enter')" :aria-pressed="Boolean(immersive)" @click="emit('update:immersive', !immersive)">
              <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><rect x="1.5" y="2.5" width="13" height="11" rx="2" stroke="currentColor"/><rect x="8" y="7" width="5" height="5" rx="1" fill="currentColor" opacity=".8"/></svg>
              <span>{{ t(immersive ? 'immersiveBrowser.exit' : 'immersiveBrowser.enter') }}</span>
            </button>
            <button v-if="immersive && fullscreenAvailable" class="ghost head-btn" :title="t(fullscreen ? 'immersiveBrowser.leaveFullscreen' : 'immersiveBrowser.fullscreen')" :aria-label="t(fullscreen ? 'immersiveBrowser.leaveFullscreen' : 'immersiveBrowser.fullscreen')" :aria-pressed="Boolean(fullscreen)" @click="emit('fullscreen')">
              <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M6 2H2v4m8-4h4v4M2 10v4h4m8-4v4h-4" stroke="currentColor" stroke-width="1.4"/></svg>
            </button>
            <button
              v-if="!immersive"
              class="ghost head-btn"
              :title="maximized ? t('agentView.dock.restore') : t('agentView.dock.maximize')"
              @click="emit('update:maximized', !maximized)"
            >
              <svg v-if="!maximized" width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
                <path d="M6 2.5H2.5V6M9 12.5H12.5V9" stroke="currentColor" stroke-linecap="round" />
                <path d="M8.5 6.5L12.5 2.5M6.5 8.5L2.5 12.5" stroke="currentColor" stroke-linecap="round" />
              </svg>
              <svg v-else width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
                <path d="M9.5 6V2.5M9.5 6H6M5.5 9V12.5M5.5 9H9" stroke="currentColor" stroke-linecap="round" />
                <path d="M13 2.5L9.5 6M2 12.5L5.5 9" stroke="currentColor" stroke-linecap="round" />
              </svg>
            </button>
            <button v-if="!immersive" class="ghost head-btn" :title="t('common.close')" @click="emit('update:active', null)">
              <el-icon><Close /></el-icon>
            </button>
          </header>
          <!-- An empty workspace is a place to add from, not a shut dock: the
               watermark says what lives here, and the tiles are the add menu
               made visible — the same actions, one click shorter. -->
          <div v-if="!tab" class="hub-empty">
            <p class="hub-empty-title">{{ t("agentView.dock.emptyHub") }}</p>
            <div class="hub-empty-tiles">
              <button class="tile" @click="addBrowser">
                <Icon :icon="globeIcon" width="20" />
                <span>{{ t("agentView.dock.browser") }}</span>
              </button>
              <button class="tile" @click="addShell">
                <Icon :icon="terminalIcon" width="20" />
                <span>{{ t("agentView.dock.shell") }}</span>
              </button>
            </div>
          </div>
          <!-- Every opened tab keeps a live body — switching hides, it never
               destroys (see keep-alive above). A tab's instance is keyed by
               the v-for, so each keeps its own history the way browser tabs
               do. -->
          <template v-for="tb in tabs" :key="tb.id">
            <div v-if="mountedTabs.has(tb.id)" v-show="tab?.id === tb.id" class="hub-pane">
              <ShellPanel
                v-if="tb.kind === 'shell'"
                :header="false"
                :session-id="sessionId"
                :workspace="workspace"
                @close="emit('update:active', null)"
              />
              <SubagentSheet
                v-else-if="tb.kind === 'subagent' && tb.childSession"
                :child-session="tb.childSession"
                :parent-session="tb.parentSession"
                :agent-id="tb.agentId"
                :live="!!liveTabs[tb.id]"
              />
              <SentinelSheet
                v-else-if="tb.kind === 'sentinel' && tb.sentinelId"
                :sentinel-id="tb.sentinelId"
                :parent-session="tb.parentSession"
                :agent-id="tb.agentId"
                :visible="hubOpen && tab?.id === tb.id"
                @open-file="emit('openSentinelFile', $event)"
              />
              <TaskSheet
                v-else-if="tb.kind === 'task' && tb.taskId && tb.taskSource === 'schedule' && tb.taskSourceId && tb.agentId && tb.agentId === agentId"
                :task-id="tb.taskId"
                :task-source="tb.taskSource"
                :task-source-id="tb.taskSourceId"
                :agent-id="tb.agentId"
                :task-run-session="tb.taskRunSession"
                :label="tb.label"
                :visible="hubOpen && tab?.id === tb.id"
              />
              <!-- A browser tab is the same pane pointed anywhere — and nowhere,
                   until a destination is chosen: with no url it opens on the
                   new-tab page, whose cards are the way in to the panels. Its
                   label follows the page it is on, like every browser tab. -->
              <BrowserPane
                v-else-if="tb.kind === 'browser'"
                :initial-url="tb.browserDraft || tb.url"
                :binding="tb.browserBinding"
                :closing="browserClosingTabIds?.includes(tb.id) === true"
                :unavailable="tb.browserUnavailable"
                :restoring="browserRestoring"
                :restore-error="browserRestoreError"
                :title="tb.label"
                :session-id="sessionId"
                :active="hubOpen && tab?.id === tb.id"
                @pick="(p) => emit('pick', p)"
                @title="(v) => emit('renameTab', tb.id, v)"
                @navigate="(u) => emit('navigateTab', tb.id, u)"
                @state="emit('browserState', $event)"
                @recover="emit('browserRecover')"
                @reopen="(url) => emit('browserReopen', tb.id, url)"
                @open-file="openOfficeFile(attachmentWorkspacePath(workspace, $event))"
              />
              <!-- A preview tab is the browser-area reading of an opened file: the
                   same viewer the files panel uses, as a tab — the way a browser
                   opens a PDF or an image as a tab rather than a lightbox. Back
                   closes the tab; there is no tree to return to in the hub. -->
              <HTMLBrowserPane
                v-else-if="tb.kind === 'html' && tb.path && tb.parentSession"
                :path="tb.path"
                :session-id="tb.parentSession"
                :initial-url="tb.url"
                :initial-id="tb.htmlPreviewId"
                :active="hubOpen && tab?.id === tb.id"
                @claim-capability="emit('claimHtmlCapability', tb.id, $event)"
              />
              <SkillDocumentPreview
                v-else-if="tb.kind === 'skill' && tb.skill && tb.path && tb.parentSession"
                :skill="tb.skill"
                :session-id="tb.parentSession"
                :path="tb.path"
              />
              <FilePreview
                v-else-if="tb.kind === 'preview' && tb.path"
                :path="tb.path"
                :ref="(value) => previewRef(tb.id, value)"
                :session-id="sessionId"
                :revision="fileRevision"
                :office-session-id="officeSessions[tb.path]"
                :immersive="immersive"
                :citation="tb.citation"
                :line-start="tb.lineStart"
                :line-end="tb.lineEnd"
                :reveal-version="tb.revealVersion"
                :back-close="true"
                @back="requestTabClose(tb.id)"
                @open-browser="emit('openHtmlBrowser', $event)"
                @open-current-browser="emit('openCurrentHtmlBrowser', $event)"
                @focus-office="focusOffice"
                @open-copy="openOfficeFile"
                @office-closed="officeClosed"
                @office-opened="officeOpened($event, tb.id)"
                @office-reference="emit('officeReference', $event)"
              />
            </div>
          </template>
        </section>
      </div>
    </div>
    <nav v-show="!immersive" class="rail">
      <button
        class="rail-btn"
        :class="{ on: active === 'files' }"
        :title="t('agentView.topbar.files')"
        @click="toggle('files')"
      >
        <Icon :icon="workspaceIcon" width="18" />
      </button>
      <button class="rail-btn" :class="{ on: hubOpen }" :title="hubTitle" @click="toggle('hub')">
        <Icon :icon="hubIcon" width="18" />
      </button>
      <button
        class="rail-btn"
        :class="{ on: active === 'shortcuts' }"
        :title="t('shortcuts.title')"
        @click="toggle('shortcuts')"
      >
        <Icon :icon="skillsIcon" width="18" />
      </button>
      <button
        v-if="agentId"
        class="rail-btn clock"
        :class="{ on: active === 'tasks' }"
        :title="t('taskCenter.title')"
        :aria-expanded="active === 'tasks'"
        data-testid="schedule-open"
        @click="toggle('tasks')"
      >
        <el-icon :size="18"><List /></el-icon>
        <span v-if="scheduleAttention" class="sched-dot" />
      </button>
      <button
        v-if="agentId"
        class="rail-btn workflow-entry"
        :class="[{ on: active === 'workflows' }, `workflow-entry--${workflowRailState.kind}`]"
        :title="workflowRailState.label"
        :aria-label="workflowRailState.label"
        :aria-expanded="active === 'workflows'"
        data-testid="agent-workflows-open"
        @click="toggle('workflows')"
      >
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true"><rect x="3" y="3" width="7" height="6" rx="1.5" stroke="currentColor" stroke-width="1.7"/><rect x="14" y="15" width="7" height="6" rx="1.5" stroke="currentColor" stroke-width="1.7"/><path d="M6.5 9v8a1 1 0 0 0 1 1H14m-2-3 3 3-3 3" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>
        <span v-if="workflowRailState.show" class="workflow-rail-count" aria-hidden="true" data-testid="workflow-rail-run-status">{{ workflowRailState.badge }}</span>
        <span class="workflow-rail-announcement" role="status" aria-live="polite" aria-atomic="true">{{ workflowRailState.label }}</span>
      </button>
      <button
        class="rail-btn"
        :class="{ on: active === 'memory' }"
        :title="sessionId ? t('memoryPanel.title') : t('memoryPanel.awaitSession')"
        :aria-label="t('memoryPanel.title')"
        :aria-expanded="active === 'memory'"
        :disabled="!sessionId"
        data-testid="memory-open"
        @click="toggle('memory')"
      >
        <el-icon><Document /></el-icon>
      </button>
      <button
        class="rail-btn skills-entry"
        :class="{ on: active === 'skills' }"
        :title="studioBound && !sessionId ? t('agentView.dock.skillsAwaitSession') : t('agentView.dock.skills')"
        :aria-label="t('agentView.dock.skills')"
        :aria-expanded="active === 'skills'"
        :disabled="studioBound && !sessionId"
        data-testid="skills-open"
        @click="toggle('skills')"
      >
        <CommandTokenIcon kind="skill" />
      </button>
    </nav>
  </aside>
</template>

<style scoped lang="scss">
.dock {
  position: relative;
  flex: 0 0 auto;
  /* Pinned to the shell's right edge: when maximize hides .main (display:none)
     the row suddenly has free space, and flex's left packing would drop the
     dock next to the sidebar — the width transition then grew it rightward
     back to the edge, the "expands right" glitch. The auto margin eats the
     free space instead, so the right edge stays put and the sheet sweeps
     left. In steady state there is no free space (.main is flex:1). */
  margin-left: auto;
  overflow: hidden;
  transition: width 0.18s ease;
  display: flex;

  &.dragging {
    transition: none;
  }

  &.open {
    /* A definite divider, because this is the one edge in the layout with content
       of a different kind on both sides. It used to be pure black — the
       light-grey line read as no boundary at all on two adjacent white panels —
       and with the warm palette a dark warm line does the same job without
       drawing a hard rule across the window. */
    border-left: 1px solid var(--el-border-color-darker);
  }
}

.grip {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 5px;
  cursor: col-resize;
  z-index: 2;

  &:hover {
    background: var(--el-fill-color);
  }
}

.task-workspace {
  display: flex;
  flex-direction: column;
  container: task-center / inline-size;
  overflow: hidden;
}

.dock-inner {
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.slot {
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  display: flex;
  overflow: hidden;

  > * {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
  }
}

// The sheet rail: always visible, this is how sheets are opened at all.
.rail {
  flex: 0 0 44px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 3px;
  padding: 8px 0;
  border-left: 1px solid var(--el-border-color-extra-light);
  background: var(--el-bg-color-page);
}

.rail-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 0;
  border-radius: 9px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition:
    background var(--pg-transition),
    color var(--pg-transition);

  &.skills-entry {
    margin-top: auto;
    flex-shrink: 0;
  }

  &:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }

  /* The open sheet's button reads as a lit tab: a white tile lifted off the
     rail's paper, matching how the active session is drawn in the sidebar. Its
     predecessor was a darker grey fill, which looked pressed rather than
     selected. */
  &.on {
    background: var(--el-bg-color);
    color: var(--el-color-primary-dark-2);
    box-shadow:
      var(--el-box-shadow-lighter),
      inset 0 0 0 1px var(--el-border-color-lighter);
  }

  &.clock {
    position: relative;
  }
}

.sched-dot {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--el-color-danger);
  pointer-events: none;
}

.workflow-entry {
  position: relative;
  overflow: visible;
  --workflow-status-color: var(--el-color-info);
  &.workflow-entry--running { --workflow-status-color: var(--el-color-primary); color: var(--workflow-status-color); }
  &.workflow-entry--waiting { --workflow-status-color: var(--el-color-warning-dark-2); color: var(--workflow-status-color); }
  &.workflow-entry--queued { --workflow-status-color: var(--el-color-primary-light-3); }
  &.workflow-entry--stale { --workflow-status-color: var(--el-text-color-secondary); }
  &.workflow-entry--running::after {
    content: ""; position: absolute; inset: 2px; border: 1px solid var(--workflow-status-color); border-radius: 9px;
    pointer-events: none; animation: workflow-rail-breathe 2s ease-in-out infinite;
  }
}
.workflow-rail-count {
  position: absolute; top: -2px; right: -3px; min-width: 14px; height: 14px; box-sizing: border-box;
  display: inline-flex; align-items: center; justify-content: center; padding: 0 3px;
  border: 2px solid var(--el-bg-color-page); border-radius: 8px; background: var(--workflow-status-color);
  color: #fff; font-size: 9px; line-height: 1; font-weight: 700; font-variant-numeric: tabular-nums;
  pointer-events: none; z-index: 1;
}
.workflow-entry--stale .workflow-rail-count { background: var(--el-bg-color); color: var(--el-text-color-secondary); box-shadow: inset 0 0 0 1px var(--el-border-color); }
.workflow-rail-announcement { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@keyframes workflow-rail-breathe { 0%, 100% { opacity: .22; } 50% { opacity: .85; } }
@media (prefers-reduced-motion: reduce) { .workflow-entry.workflow-entry--running::after { animation: none; opacity: .5; } }

.hub-sheet {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;

  /* The tab body fills what the strip leaves. */
  > :not(.sheet-head) {
    flex: 1 1 0;
    min-height: 0;
  }
}

/* A kept-alive tab body: the flex sizing comes from the .hub-sheet child rule
   above; the pane passes the height through to whichever tenant it wraps. */
.hub-pane {
  display: flex;
  flex-direction: column;
  min-width: 0;

  > * {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
  }
}

/* The empty workspace: a watermark plus the add menu as tiles. Centreed and
   quiet — it is a doorstep, not a dashboard. */
.hub-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 18px;
  padding: 24px;
}

.hub-empty-title {
  margin: 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.hub-empty-tiles {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 10px;
}

.tile {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  width: 96px;
  padding: 16px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition:
    border-color var(--pg-transition),
    color var(--pg-transition),
    box-shadow var(--pg-transition);

  &:hover {
    border-color: var(--el-color-primary);
    color: var(--el-color-primary);
    box-shadow: var(--el-box-shadow-light);
  }

  span {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

/* The add button: tab-shaped so the strip reads as one continuous row of
   peers — Chrome puts its "+" in the strip, not in the toolbar. */
.tab.add {
  color: var(--el-text-color-secondary);

  &:hover {
    color: var(--el-color-primary);
  }
}

.sheet-head {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 4px 0 0;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  background: var(--el-bg-color-page);
  flex: 0 0 auto;
}

/* The strip: tabs butt against each other like a browser's, scroll sideways
   when they overflow rather than wrapping into a second row nobody reads. */
.tabstrip {
  flex: 1 1 0;
  min-width: 0;
  display: flex;
  align-items: stretch;
  overflow-x: auto;
  scrollbar-width: none;

  &::-webkit-scrollbar {
    display: none;
  }
}

/* Strip paging arrows: rendered only while the strip overflows, pinned outside
   it so paging never moves the buttons themselves. */
.strip-page {
  flex: 0 0 auto;

  &:disabled {
    opacity: 0.35;
    cursor: default;
  }
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 180px;
  min-width: 0;
  border: 0;
  border-right: 1px solid var(--el-border-color-extra-light);
  background: transparent;
  padding: 7px 8px;
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  white-space: nowrap;
  transition:
    background var(--pg-transition),
    color var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
  }

  /* The active tab is the body: same paper, and the strip's bottom line breaks
     under it — the oldest tab cue there is. */
  &.on {
    background: var(--el-bg-color);
    color: var(--el-text-color-primary);
    box-shadow: 0 1px 0 0 var(--el-bg-color);
    font-weight: 600;
  }

  .tab-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
}

.tab-favicon {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
  object-fit: contain;
}

/* The × appears on hover and on the active tab: a permanent one on every tab
   is visual noise, and a hidden one on the tab you are reading is a hunt. */
.x {
  display: inline-flex;
  align-items: center;
  border-radius: 4px;
  padding: 1px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  opacity: 0;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.tab:hover .x,
.tab.on .x {
  opacity: 1;
}

/* A delegated run still in flight: the same pulsing dot the subagent card's
   own header uses, so the strip and the card agree on what "running" is. */
.tablive {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--el-color-primary);
  flex: 0 0 auto;
  animation: tabpulse 1.2s ease-in-out infinite;
}

@keyframes tabpulse {
  50% {
    opacity: 0.4;
  }
}

@media (prefers-reduced-motion: reduce) {
  .tablive {
    animation: none;
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

.head-btn svg {
  display: block;
}

.head-btn.immersion-toggle {
  width: auto;
  gap: 6px;
  padding: 0 9px;
  white-space: nowrap;
  font-size: 11px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 7px;
  margin: 0 4px;

  &.on { color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
}
.dock.immersive {
  position: absolute;
  inset: 0;
  margin: 0;
  border: 0;
  transition: none;
  .sheet-head { min-height: 44px; padding-right: 12px; }
}
.dock.canvas-workspace {
  min-height: 0;
  border: 0;
  transition: none;
}
@media (max-width: 720px) {
  /* A split pane has no useful reading width on a phone. Keep the same
     close control and rail while showing the selected sheet over the chat. */
  .dock.open:not(.immersive) {
    position: fixed;
    inset: 0 0 0 auto;
    z-index: 75;
    width: 100vw !important;
    height: 100dvh !important;
    margin: 0;
    background: var(--el-bg-color);
  }
  .head-btn.immersion-toggle { padding: 0 6px; font-size: 10px; }
  .dock.immersive .sheet-head { padding-right: 4px; }
}
</style>
