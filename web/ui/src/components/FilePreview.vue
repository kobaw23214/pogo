<script setup lang="ts">
import { knownHostReason } from "@/agent/hostStatusCopy";
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, Check, Close, CopyDocument, Download, EditPen, Monitor } from "@element-plus/icons-vue";
import CodeBlock from "./CodeBlock.vue";
import MarkdownView from "./MarkdownView.vue";
import MermaidDiagram from "./MermaidDiagram.vue";
import { api, authHeaders, fileDownloadURL, fileRawURL } from "@/api/client";
import type { FileContent, FileRender, WorkspaceHTMLPreview } from "@/api/types";
import type { OfficeReferenceSnapshot } from "@/api/types";
import { fileLineSelection, preservePreviewTrailingLine } from "./filePreviewLines";
import { workspaceFileCardKind } from "@/agent/markdownFileLinks";
import { officeApi, officePathMounted } from "@/agent/officeTransport";
import type { OfficeOpen, OfficeSession } from "@/agent/officeTransport";
import OfficeAccessPanel from "./OfficeAccessPanel.vue";
import type { OfficeViewContext } from "@/agent/officeImmersion";
import { isEditableOfficeFile, reserveOfficeFile, workspaceFilePath } from "@/agent/officeFiles";
import { retryOfficeStartup } from "@/agent/officeStartup";
import { observeWorkspaceFileMutation, registerWorkspaceMutationGuard, workspaceMutationPending, workspacePathContains } from "./workspaceFileActions";
const OfficeEditor = defineAsyncComponent(() => import("./OfficeEditor.vue"));

// The spreadsheet viewer: @vue-office/excel renders a real grid — sheet bar,
// cell selection, frozen headers — for legacy read-only spreadsheet formats. It
// rides an async chunk: exceljs is among the heaviest dependencies in the app,
// and only a spreadsheet preview click should ever pay for it.
const VueOfficeExcel = defineAsyncComponent(async () => {
  await import("@vue-office/excel/lib/index.css");
  const mod = await import("@vue-office/excel");
  return mod.default ?? mod;
});

// Whole-panel file viewer: breadcrumb back to the tree, then the content in
// the best shape the file allows — markdown rendered, images inline, code
// highlighted, PDFs in the browser's own viewer, docx/xlsx in the shared
// ONLYOFFICE editor. Other legacy formats retain their existing preview.
//
// The same viewer is the dock's preview tab (backClose): an attachment click
// opens it as a browser-area tab, where "back" means closing the tab.
const props = defineProps<{
  path: string;
  backClose?: boolean;
  citation?: boolean;
  lineStart?: number;
  lineEnd?: number;
  revealVersion?: number;
  revision?: number;
  sessionId?: string | null;
  officeSessionId?: string;
  immersive?: boolean;
  officeMode?: "view" | "edit";
}>();
const emit = defineEmits<{ back: []; focusOffice: [path: string]; openCopy: [path: string]; openCurrentBrowser: [request: { url: string; id: string; sessionId: string; path: string }]; openBrowser: [request: { url: string; id: string; sessionId: string }]; officeClosed: [id: string]; officeEditing: [context: OfficeViewContext | null]; officeOpened: [context: OfficeViewContext]; officeReference: [snapshot: OfficeReferenceSnapshot] }>();
const { t, d } = useI18n();

const content = ref<FileContent | null>(null);
const failed = ref("");
const mdPreview = ref(true);
const copied = ref(false);
const body = ref<HTMLElement | null>(null);
const htmlError = ref("");
const mediaError = ref("");
const mediaLoading = ref(true);
const mediaDuration = ref(0);
const mediaNonce = ref(0);
const pdfLoading = ref(true);
const pdfError = ref("");
const pdfNonce = ref(0);
const htmlCurrentBrowserLoading = ref(false);
const htmlBrowserLoading = ref(false);
let htmlCurrentBrowserVersion = 0, htmlBrowserVersion = 0;
const hasLineTarget = computed(() => props.lineStart !== undefined || props.lineEnd !== undefined);
const lineSelection = computed(() => content.value ? fileLineSelection(content.value, props.lineStart, props.lineEnd) : { status: "none" } as const);
const selectedRange = computed(() => lineSelection.value.status === "available" ? lineSelection.value : null);
const officeEditing = ref(false);
const officeEnabled = ref(false);
const officeChecking = ref(false);
const officeCheckElapsed = ref(0);
const officeCheckAttempt = ref(1);
let officeCheckController: AbortController | null = null;
let officeCheckTimer: ReturnType<typeof setInterval> | undefined;
const officeBinding = ref<{ path: string; conversationId: string; id?: string; mtime?: number; size?: number; mode: "view" | "edit" } | null>(null);
const officeEditor = ref<{ prepareClose(): Promise<boolean>; refreshStatus(): Promise<void> } | null>(null);
const officeModeChanging = ref(false);
let officeModeChange: Promise<void> | null = null;
const officeNotice = ref("");
const officeVersionObserved = ref(false);
const closedOfficeSessions = new Set<string>();
const MAX_SHEET_PREVIEW_BYTES = 20 * 1024 * 1024;
let releaseOfficeFile: (() => void) | null = null;
let sheetController: AbortController | null = null;
const officeDuplicate = ref(false);
const readOnlyOffice = ref(false);
const officeClaim = ref<{ claim: OfficeOpen; instanceId: string } | null>(null);
const displayPath = computed(() => officeBinding.value?.path || props.path);
// Opening an editor is a one-time layout intent. Save receipts, polling and
// later tab selections must never undo the user's choice to leave immersion.
watch(officeEditing, active => {
  const binding = officeBinding.value;
  const context = active && binding ? { path: binding.path, conversationId: binding.conversationId } : null;
  emit("officeEditing", context);
  if (context) emit("officeOpened", context);
}, { flush: "sync" });
onBeforeUnmount(() => { ++loadVersion; sheetController?.abort(); clearHTMLRequests(); cancelOfficeCheck(); releaseOfficeFile?.(); if (officeEditing.value) emit("officeEditing", null); });

// A Markdown heading's rendered row is not its source line. Every citation
// navigation starts in source mode, even when the same tab was already open.
watch([() => props.path, () => props.citation, () => props.lineStart, () => props.lineEnd, () => props.revealVersion], () => {
  if (props.citation || hasLineTarget.value) mdPreview.value = false;
}, { immediate: true });

// The server-side markdown conversion for docx, fetched only when the content
// read says the file is one.
const rendered = ref<FileRender | null>(null);
const renderError = ref("");

// The xlsx bytes the spreadsheet component renders. They come from the
// download-flavoured raw endpoint — Content-Disposition means nothing to a
// fetch, and that flavour is the one allowed to carry non-image bytes.
const sheetBytes = ref<ArrayBuffer | null>(null);

// In-panel editing. Two guards: only files read in full (a truncated preview
// saved back would silently amputate the tail), and an mtime check so an
// outside change is never overwritten blind — the server 409s, and the user
// picks override or discard.
const editing = ref(false);
const pathInvalidated = ref(false);
const stopMutationGuard = registerWorkspaceMutationGuard(path => !workspacePathContains(path,props.path) || (!editing.value && !saving.value && !officeEditing.value));
const stopMutationObserver = observeWorkspaceFileMutation(change => {
  if (!workspacePathContains(change.path,props.path)) return;
  pathInvalidated.value = true;
  if (editing.value || officeEditing.value) { saveError.value = t("filePreview.pathChanged"); return; }
  emit("back");
});
onBeforeUnmount(() => { stopMutationGuard(); stopMutationObserver(); });
const draft = ref("");
const saving = ref(false);
const savedOk = ref(false);
const conflict = ref(false);
const saveError = ref("");
let loadVersion = 0;
let refreshAfterEdit = false;

const ext = computed(() => {
  const base = props.path.split("/").pop() ?? "";
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(i + 1).toLowerCase() : "";
});

const isMd = computed(() => ext.value === "md" || ext.value === "markdown");
const isMermaid = computed(() => ext.value === "mmd" || ext.value === "mermaid");
const isHTML = computed(() => ext.value === "html" || ext.value === "htm");
const hasPreview = computed(() => isMd.value || isMermaid.value);

function revokeHTMLPreview(value: WorkspaceHTMLPreview, owner: string) {
  // The capability route is independent of the host token. Revocation still
  // goes through the authorized host endpoint and never visits its HTML URL.
  return api.revokeHTMLPreview(owner, value.id).catch(() => {});
}
function clearHTMLRequests() {
  htmlCurrentBrowserVersion++; htmlCurrentBrowserLoading.value = false;
  htmlBrowserVersion++; htmlBrowserLoading.value = false;
  htmlError.value = "";
}
async function issueHTMLBrowser(kind: "current" | "native") {
  const owner = props.sessionId, path = props.path;
  const loading = kind === "current" ? htmlCurrentBrowserLoading : htmlBrowserLoading;
  if (loading.value || !isHTML.value || !content.value || content.value.binary || !owner || editing.value || pathInvalidated.value) return;
  const version = kind === "current" ? ++htmlCurrentBrowserVersion : ++htmlBrowserVersion;
  const current = () => version === (kind === "current" ? htmlCurrentBrowserVersion : htmlBrowserVersion);
  loading.value = true; htmlError.value = "";
  try {
    // Every newly opened dock Page owns its own capability. The host takes
    // over its lifecycle after emission; this source tab retains no live URL.
    // A late receipt is revoked rather than opening a Page for another file.
    const value = await api.createHTMLPreview(owner, path);
    if (!current() || path !== props.path || owner !== props.sessionId || editing.value) {
      void revokeHTMLPreview(value, owner);
      return;
    }
    if (kind === "current") emit("openCurrentBrowser", { url: value.url, id: value.id, sessionId: owner, path });
    else emit("openBrowser", { url: value.browser_url, id: value.id, sessionId: owner });
  } catch (error) {
    if (current()) htmlError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (current()) loading.value = false;
  }
}
function openCurrentHTMLBrowser() { return issueHTMLBrowser("current"); }
function openHTMLBrowser() { return issueHTMLBrowser("native"); }

// Extension classes the browser cannot render on its own: the PDF gets the
// browser's viewer through the sandboxed raw endpoint; xlsx/xlsm get the
// spreadsheet component for legacy formats; old binary .doc/.xls go to the
// server's render endpoint. docx/xlsx take the direct Office route first.
const isPdf = computed(() => ext.value === "pdf");
const isAudio = computed(() => ["mp3", "wav", "ogg", "m4a", "flac", "aac", "opus"].includes(ext.value));
const isVideo = computed(() => ["mp4", "webm", "mov", "mkv", "avi", "m4v"].includes(ext.value));
const isSheet = computed(() => ext.value === "xlsx" || ext.value === "xlsm");
const isOffice = computed(() => ["docx", "doc", "xls"].includes(ext.value));
const supportsOfficeEditing = computed(() => isEditableOfficeFile(props.path));
const officeCheckLabel = computed(() => t(officeCheckElapsed.value >= 15 ? "officeEditor.startup.slow" : "officeEditor.startup.progress", { stage: t("officeEditor.startup.phases.checking"), elapsed: officeCheckElapsed.value }));

function cancelOfficeCheck() {
  officeCheckController?.abort(); officeCheckController = null;
  clearInterval(officeCheckTimer); officeCheckTimer = undefined;
  officeChecking.value = false;
}

function startOffice(mode: "view" | "edit" = props.officeMode ?? "view") {
  if (!officeEnabled.value || !supportsOfficeEditing.value || !props.sessionId || officeEditing.value) return;
  const path = workspaceFilePath(props.path);
  const reserved = officePathMounted(path, props.sessionId) ? null : reserveOfficeFile(path, props.sessionId);
  if (!reserved) {
    officeDuplicate.value = true; failed.value = t("officeEditor.duplicate"); emit("focusOffice", props.path); return;
  }
  releaseOfficeFile = reserved;
  ++loadVersion;
  const id = props.officeSessionId && !closedOfficeSessions.has(props.officeSessionId) ? props.officeSessionId : undefined;
  officeBinding.value = { path, conversationId: props.sessionId, id, mtime: content.value?.mtime_ms, size: content.value?.size, mode };
  readOnlyOffice.value = false; officeEditing.value = true; officeNotice.value = "";
}
async function viewSavedOffice() {
  // Used only when no editor was claimed. Never tear down an existing dirty
  // canvas to obtain a preview or remove another window's editing ownership.
  if (officeClaim.value) return;
  officeEditing.value = false; officeBinding.value = null;
  releaseOfficeFile?.(); releaseOfficeFile = null;
  readOnlyOffice.value = true; failed.value = "";
  await load();
}
function enableOfficeEdit(): Promise<void> {
  if (officeModeChange) return officeModeChange;
  const binding = officeBinding.value, currentEditor = officeEditor.value;
  if (!binding || binding.mode === "edit" || !currentEditor) return Promise.resolve();
  const version = loadVersion, requestedPath = props.path, owner = props.sessionId;
  officeModeChanging.value = true;
  const work = (async () => {
    // Changing the keyed mode unmounts the old canvas. Retire its exact clean
    // session first, then request a fresh editor: reusing binding.id races the
    // old component's detach beacon and yields SESSION_CLOSED on /config.
    if (!await currentEditor.prepareClose()) return;
    if (version !== loadVersion || officeBinding.value !== binding || props.path !== requestedPath || props.sessionId !== owner || !officeEditing.value) return;
    if (binding.id) officeClosedPrevious(binding.id);
    const supplied = officeClaim.value;
    officeClaim.value = supplied && supplied.claim.session.id !== binding.id ? supplied : null;
    officeBinding.value = { ...binding, id: officeClaim.value?.claim.session.id, mode: "edit" };
  })().catch(error => { if (version === loadVersion && officeBinding.value === binding) failed.value = error instanceof Error ? error.message : String(error); })
    .finally(() => { if (officeModeChange === work) { officeModeChange = null; officeModeChanging.value = false; } });
  officeModeChange = work;
  return work;
}
function editSavedOffice() {
  officeClaim.value = null;
  if (officeBinding.value) { enableOfficeEdit(); return; }
  startOffice("edit");
}
function installOfficeClaim(value: { claim: OfficeOpen; instanceId: string }) {
  officeClaim.value = value;
  if (officeBinding.value) { enableOfficeEdit(); return; }
  startOffice("edit");
  if (!officeEditing.value) { officeClaim.value = null; void officeApi.release(value.claim, value.instanceId).catch(() => {}); }
}
async function endOffice() {
  markOfficeClosed();
  officeEditing.value = false; officeBinding.value = null; officeNotice.value = "";
  releaseOfficeFile?.(); releaseOfficeFile = null;
  emit("back");
}
async function prepareClose(): Promise<boolean> {
  if (!officeEditing.value) return true;
  if (!officeEditor.value || !await officeEditor.value.prepareClose()) return false;
  markOfficeClosed();
  officeEditing.value = false; officeBinding.value = null;
  releaseOfficeFile?.(); releaseOfficeFile = null;
  return true;
}
function markOfficeClosed() {
  const id = officeBinding.value?.id;
  if (id) officeClosedPrevious(id);
}
function officeClosedPrevious(id: string) { closedOfficeSessions.add(id); emit("officeClosed", id); }
function officeStatus(value: OfficeSession) {
  const binding = officeBinding.value;
  // OfficeEditor has verified requestedPath and the account-scoped fileRef.
  // Keep the server's canonical path for metadata and future reopening.
  if (binding && binding.conversationId === value.conversationId && value.fileRef) { binding.path = value.path; binding.id = value.id; }
  officeVersionObserved.value = typeof value.canvasVersion === "string" && typeof value.diskVersion === "string";
  if (officeVersionObserved.value) officeNotice.value = "";
}
function officeOpenFailed(message: string) {
  // This event is emitted only after the unused server claim was released,
  // or before any claim existed. A live/dirty/unknown editor stays mounted.
  officeEditing.value = false; officeBinding.value = null;
  releaseOfficeFile?.(); releaseOfficeFile = null;
  failed.value = message; officeDuplicate.value = false;
}
function officeFocusExisting(path: string) {
  officeEditing.value = false; officeBinding.value = null;
  releaseOfficeFile?.(); releaseOfficeFile = null;
  emit("focusOffice", path); emit("back");
}
async function showPreview() { await back(); }
async function back() { if (await prepareClose()) emit("back"); }
async function refreshPreview(): Promise<boolean> {
  if (!officeEditing.value) return true;
  officeNotice.value = t("officeEditor.refreshKept");
  const binding = officeBinding.value;
  try {
    await officeEditor.value?.refreshStatus();
    if (!binding || binding !== officeBinding.value) return false;
    // The host now checks the actual file version and OfficeEditor owns the
    // conflict/reload UI. Do not replace it with a second mtime-only verdict.
    if (officeVersionObserved.value) { officeNotice.value = ""; return false; }
    const latest = await api.fileContent(binding.path);
    if (binding !== officeBinding.value) return false;
    if ((binding.mtime !== undefined && latest.mtime_ms !== binding.mtime) || (binding.size !== undefined && latest.size !== binding.size)) officeNotice.value = t("officeEditor.externalChange");
  } catch (error) { officeNotice.value = error instanceof Error ? error.message : String(error); }
  return false;
}
async function officeSaved() {
  const binding = officeBinding.value;
  if (!binding) return;
  try {
    const latest = await api.fileContent(binding.path);
    if (binding !== officeBinding.value) return;
    binding.mtime = latest.mtime_ms; binding.size = latest.size;
    officeNotice.value = "";
  } catch { /* A preview metadata failure does not claim or undo the saved Office revision. */ }
}
defineExpose({ prepareClose, refreshPreview, activeOfficePath: () => officeEditing.value ? officeBinding.value?.path || null : null });

const kind = computed(() => {
  const c = content.value;
  if (!c) return "loading";
  if (!c.binary) return "text";
  if (c.mime?.startsWith("image/")) return "image";
  if (isPdf.value) return "pdf";
  if (isAudio.value) return "audio";
  if (isVideo.value) return "video";
  if (isSheet.value) return "sheet";
  if (isOffice.value) return "office";
  return "binary";
});

const baseName = computed(() => props.path.split("/").pop() ?? props.path);
const fileTypeLabel = computed(() => {
  const kind = workspaceFileCardKind(props.path);
  return kind ? t(`workspaceFiles.cardKinds.${kind}`) : "";
});
const mediaDurationLabel = computed(() => {
  const total = Math.max(0, Math.floor(mediaDuration.value));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor(total % 3600 / 60);
  const seconds = String(total % 60).padStart(2, "0");
  return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
});
function mediaMetadata(event: Event) {
  const media = event.target as HTMLMediaElement;
  mediaLoading.value = false;
  mediaDuration.value = Number.isFinite(media.duration) && media.duration > 0 ? media.duration : 0;
}
function mediaFailed() {
  mediaLoading.value = false;
  mediaError.value = t("filePreview.mediaFailed");
}
function retryMedia() {
  mediaError.value = "";
  mediaLoading.value = true;
  mediaDuration.value = 0;
  mediaNonce.value++;
}
function pdfLoaded() {
  pdfLoading.value = false;
  pdfError.value = "";
}
function pdfFailed() {
  pdfLoading.value = false;
  pdfError.value = t("filePreview.pdfFailed");
}
function retryPDF() {
  pdfLoading.value = true;
  pdfError.value = "";
  pdfNonce.value++;
}

// Scroll only the preview pane; scrollIntoView would also move the surrounding
// conversation. The reveal version handles a second click on the same range.
watch([selectedRange, mdPreview, editing, () => props.revealVersion], async () => {
  await nextTick();
  const range = selectedRange.value;
  // Rendered Markdown can contain code blocks with their own local row numbers.
  // Only source mode's full-file rows are legitimate citation targets.
  if (!range || editing.value || (hasPreview.value && mdPreview.value)) return;
  const pane = body.value;
  const line = pane?.querySelector<HTMLElement>(`[data-source-line="${range.lineStart}"]`);
  if (pane && line) pane.scrollTop += line.getBoundingClientRect().top - pane.getBoundingClientRect().top - 40;
}, { flush: "post" });

async function load(keepHTMLSource?: unknown) {
  // Read-only availability checks must never tear down an existing canvas.
  if (officeEditing.value) { officeNotice.value = t("officeEditor.refreshKept"); return; }
  const version = ++loadVersion;
  clearHTMLRequests();
  cancelOfficeCheck();
  sheetController?.abort();
  sheetController = null;
  const path = props.path;
  const conversationId = props.sessionId;
  const current = () => version === loadVersion && path === props.path && conversationId === props.sessionId;
  refreshAfterEdit = false;
  // Keep the current HTML source visible while a completed run's updated
  // contents are fetched. Rendered Pages belong to the explicit browser tabs.
  if (keepHTMLSource !== true || !isHTML.value) content.value = null;
  failed.value = "";
  mediaError.value = "";
  mediaLoading.value = true;
  mediaDuration.value = 0;
  pdfLoading.value = true;
  pdfError.value = "";
  rendered.value = null;
  renderError.value = "";
  sheetBytes.value = null;
  officeDuplicate.value = false;
  if (supportsOfficeEditing.value && !readOnlyOffice.value) {
    officeEnabled.value = false;
    if (!conversationId) { failed.value = t("officeEditor.noSession"); return; }
    const controller = new AbortController(), startedAt = Date.now();
    officeCheckController = controller; officeChecking.value = true; officeCheckElapsed.value = 0; officeCheckAttempt.value = 1;
    officeCheckTimer = setInterval(() => { if (officeCheckController === controller) officeCheckElapsed.value = Math.max(0, Math.floor((Date.now() - startedAt) / 1000)); }, 1000);
    try {
      const config = await retryOfficeStartup("checking", signal => officeApi.configuration(signal), { signal: controller.signal, onAttempt: value => { if (officeCheckController === controller) officeCheckAttempt.value = value.attempt; } });
      if (!current()) return;
      // A restarting/slow upstream is transient. The scoped open request owns
      // its five-minute readiness wait; a short health check must not preempt it.
      officeEnabled.value = config.enabled && config.state !== "config_error" && config.state !== "disabled";
      if (!officeEnabled.value) {
        failed.value = knownHostReason(config.reason) || t(config.configured || config.enabled ? "officeEditor.unavailable" : "officeEditor.notConfigured");
        return;
      }
      startOffice();
    } catch (error) {
      if (current() && !controller.signal.aborted) failed.value = (error as { code?: string }).code === "OFFICE_STARTUP_TIMEOUT" ? t("officeEditor.startup.timeout", { stage: t("officeEditor.startup.phases.checking") }) : t("officeEditor.startup.failed", { stage: t("officeEditor.startup.phases.checking"), message: error instanceof Error ? error.message : String(error) });
    } finally {
      if (officeCheckController === controller) { clearInterval(officeCheckTimer); officeCheckTimer = undefined; officeCheckController = null; officeChecking.value = false; }
    }
    return;
  }
  try {
    const result = await api.fileContent(path);
    if (!current()) return;
    content.value = result;
  } catch (e) {
    if (current()) failed.value = (e as { status?: number } | null)?.status === 404
      ? t("workspaceFiles.notFound")
      : e instanceof Error ? e.message : String(e);
    return;
  }
  if (kind.value === "sheet") await loadSheet(path, current);
  else if (kind.value === "office") await loadRender(path, current);
}

// The spreadsheet component takes the file's bytes; the fetch is ours so the
// loading and failure states are ours too.
async function loadSheet(path: string, current: () => boolean) {
  const size = content.value?.size;
  if (size !== undefined && size > MAX_SHEET_PREVIEW_BYTES) {
    renderError.value = t("filePreview.tooLarge");
    return;
  }
  const controller = new AbortController();
  sheetController = controller;
  try {
    const res = await fetch(fileDownloadURL(path), { headers: authHeaders(), signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const bytes = await res.arrayBuffer();
    if (current()) sheetBytes.value = bytes;
  } catch (e) {
    if (current()) renderError.value = e instanceof Error ? e.message : String(e);
  } finally {
    if (sheetController === controller) sheetController = null;
  }
}

async function loadRender(path: string, current: () => boolean) {
  try {
    const result = await api.fileRender(path);
    if (current()) rendered.value = result;
  } catch (e) {
    if (current()) renderError.value = e instanceof Error ? e.message : String(e);
  }
}

// The component's own failure (a workbook it cannot parse) lands in the same
// error slot as the fetch's, with the download button one row up as the way
// out.
function sheetFailed(info: unknown) {
  const detail = info instanceof Event ? "" : info instanceof Error ? info.message : String(info ?? "");
  renderError.value = detail ? `${t("filePreview.renderFailed")}${detail}` : t("filePreview.renderFailed");
}
// Each citation click reads the current file again, even in a kept-alive tab.
// Defer same-file refreshes while a draft is open; finishing or discarding the
// edit performs the pending read without replacing unsaved text.
watch([() => props.path, () => props.sessionId, () => props.citation ? props.revealVersion : undefined, () => props.revision], ([path, conversationId], previous) => {
  if (path !== previous?.[0] || conversationId !== previous?.[1]) pathInvalidated.value = false;
  if (path !== previous?.[0] || conversationId !== previous?.[1]) { readOnlyOffice.value = false; officeClaim.value = null; }
  if (editing.value && path === previous?.[0]) {
    refreshAfterEdit = true;
    return;
  }
  if (officeEditing.value) { officeNotice.value = t("officeEditor.refreshKept"); return; }
  void load(isHTML.value && path === previous?.[0] && conversationId === previous?.[1]);
}, { immediate: true });
watch(editing, (active) => {
  if (!active && refreshAfterEdit) void load();
});

function fmtSize(n?: number): string {
  if (n === undefined) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function fmtTime(ms?: number): string {
  if (!ms) return "";
  return d(new Date(ms), "short");
}

async function copyPath() {
  try {
    await navigator.clipboard.writeText(displayPath.value);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; failing quietly is better than an alert.
  }
}

const canEdit = computed(() => kind.value === "text" && content.value && !content.value.truncated && !pathInvalidated.value && !workspaceMutationPending(props.path));

function startEdit() {
  if (!canEdit.value) return;
  draft.value = content.value?.text ?? "";
  conflict.value = false;
  saveError.value = "";
  editing.value = true;
}

async function save(force = false) {
  if (pathInvalidated.value || workspaceMutationPending(props.path)) { saveError.value = t("filePreview.pathChanged"); return; }
  saving.value = true;
  saveError.value = "";
  try {
    const res = await api.saveFile(props.path, draft.value, content.value?.mtime_ms ?? 0, force);
    clearHTMLRequests();
    content.value = { ...content.value!, text: draft.value, size: res.size, mtime_ms: res.mtime_ms };
    editing.value = false;
    conflict.value = false;
    savedOk.value = true;
    window.setTimeout(() => (savedOk.value = false), 1500);
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (status === 409) conflict.value = true;
    else saveError.value = e instanceof Error ? e.message : String(e);
  } finally {
    saving.value = false;
  }
}

// discard reloads from disk: the user picked the file's version over theirs.
async function discard() {
  editing.value = false;
  conflict.value = false;
  await load();
}
</script>

<template>
  <div class="preview">
    <div v-if="!officeEditing" class="bar">
      <button v-if="!backClose" class="ghost back" @click="back">
        <el-icon><ArrowLeft /></el-icon> {{ t("filePreview.back") }}
      </button>
      <button v-else class="ghost" :title="t('common.close')" :aria-label="t('common.close')" @click="back">
        <el-icon><Close /></el-icon>
      </button>
      <span class="path" :title="displayPath">{{ displayPath }}</span>
      <span v-if="fileTypeLabel" class="file-kind">{{ fileTypeLabel }}</span>
      <span v-if="savedOk" class="saved">{{ t("filePreview.saved") }}</span>
      <button v-if="canEdit && !editing && !officeEditing" class="ghost" :title="t('filePreview.edit')" @click="startEdit">
        <el-icon><EditPen /></el-icon>
      </button>
      <button class="ghost" :title="t('filePreview.copyPath')" @click="copyPath">
        <el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>
      </button>
      <!-- The original bytes, for every file: text previews are served decoded
           and possibly truncated, so the download link is the only way to get
           the file itself; the attachment disposition is what makes serving
           any binary safe. Rightmost in the bar. -->
      <a
        v-if="(content || supportsOfficeEditing) && !officeEditing"
        class="ghost"
        :href="fileDownloadURL(path)"
        :download="baseName"
        :title="t('filePreview.download')"
      >
        <el-icon><Download /></el-icon>
      </a>
    </div>

    <p v-if="officeEditing && officeNotice" class="note office-notice" role="status">{{ officeNotice }}</p>
    <OfficeEditor v-if="officeEditing && officeBinding" :key="`${officeBinding.path}:${officeBinding.mode}`" ref="officeEditor" :path="officeBinding.path" :conversation-id="officeBinding.conversationId" :current-conversation-id="sessionId || undefined" :office-session-id="officeBinding.id" :initial-open="officeClaim?.claim" :initial-instance-id="officeClaim?.instanceId" :immersive="immersive" :view-only="officeBinding.mode === 'view'" @preview="showPreview" @closed="endOffice" @closed-previous="officeClosedPrevious" @saved="officeSaved" @status="officeStatus" @open-failed="officeOpenFailed" @view-saved="viewSavedOffice" @focus-existing="officeFocusExisting" @reference="emit('officeReference', $event)">
      <template #document-details>
        <code class="office-detail-path">{{ displayPath }}</code>
        <div class="office-detail-actions">
          <button v-if="officeBinding.mode === 'view'" type="button" class="ghost" :disabled="officeModeChanging || !officeEditor" :aria-busy="officeModeChanging" @click="enableOfficeEdit"><el-icon><EditPen /></el-icon>{{ t('officeEditor.edit') }}</button>
          <button type="button" class="ghost" :title="t('filePreview.copyPath')" @click="copyPath"><el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>{{ t('filePreview.copyPath') }}</button>
          <button type="button" class="ghost" :title="backClose ? t('common.close') : t('filePreview.back')" @click="back"><el-icon><Close v-if="backClose" /><ArrowLeft v-else /></el-icon>{{ backClose ? t('common.close') : t('filePreview.back') }}</button>
        </div>
      </template>
    </OfficeEditor>

    <OfficeAccessPanel v-if="readOnlyOffice && !officeEditing && sessionId" :path="path" :conversation-id="sessionId" @edit="editSavedOffice" @claimed="installOfficeClaim" @refresh="load" @open-copy="emit('openCopy', $event)" />

    <div v-else-if="failed" class="note bad" role="alert">
      {{ failed }}
      <button v-if="supportsOfficeEditing" type="button" class="ghost" @click="officeDuplicate ? emit('focusOffice', path) : load()">{{ t(officeDuplicate ? 'officeEditor.openExisting' : 'officeEditor.retry') }}</button>
    </div>
    <div v-else-if="officeChecking" class="note office-checking" role="status" aria-live="polite"><span>{{ officeCheckLabel }}</span><small v-if="officeCheckAttempt > 1">{{ t('officeEditor.startup.retrying', { stage: t('officeEditor.startup.phases.checking'), attempt: officeCheckAttempt, max: 3 }) }}</small></div>
    <div v-else-if="!content" class="note">{{ t("common.loading") }}</div>

    <!-- The user's own draft. Saving checks the file's mtime against the one
         this preview read; a 409 offers override or discard, never a blind
         overwrite. -->
    <template v-if="editing && !officeEditing">
      <div v-if="conflict" class="conflict">
        {{ t("filePreview.conflict") }}
        <button class="link danger" :disabled="saving" @click="save(true)">{{ t("filePreview.overwrite") }}</button>
        <button class="link" @click="discard">{{ t("filePreview.discard") }}</button>
      </div>
      <div v-if="saveError" class="note bad">{{ saveError }}</div>
      <textarea v-model="draft" class="editor" spellcheck="false" />
      <div class="editbar">
        <button class="act primary" :disabled="saving" @click="save(false)">
          {{ saving ? t("filePreview.saving") : t("common.save") }}
        </button>
        <button class="act" @click="discard">{{ t("common.cancel") }}</button>
      </div>
    </template>

    <template v-else-if="content && !officeEditing">
      <div class="meta">
        <span>{{ fmtSize(content.size) }}</span>
        <span v-if="content.mtime_ms">{{ fmtTime(content.mtime_ms) }}</span>
        <span v-if="content.truncated" class="warn">
          {{ content.truncated_by === "lines" ? t("filePreview.truncatedLines") : t("filePreview.truncatedSize") }}
        </span>
        <span v-if="rendered?.truncated" class="warn">{{ t("filePreview.renderTruncated") }}</span>
        <span class="spacer" />
        <template v-if="kind === 'text' && hasPreview">
          <button class="mode" :class="{ on: mdPreview }" @click="mdPreview = true">{{ t("filePreview.preview") }}</button>
          <button class="mode" :class="{ on: !mdPreview }" @click="mdPreview = false">{{ t("filePreview.source") }}</button>
        </template>
      </div>

      <div v-if="isHTML && kind === 'text'" class="html-toolbar">
        <button type="button" class="ghost" data-testid="html-open-current-browser" :disabled="htmlCurrentBrowserLoading || !sessionId || pathInvalidated" @click="openCurrentHTMLBrowser"><el-icon><Monitor /></el-icon>{{ t(htmlCurrentBrowserLoading ? 'filePreview.htmlOpeningBrowser' : 'filePreview.htmlOpenCurrentBrowser') }}</button>
        <button type="button" class="ghost" data-testid="html-open-browser" :disabled="htmlBrowserLoading || !sessionId || pathInvalidated" @click="openHTMLBrowser"><el-icon><Monitor /></el-icon>{{ t(htmlBrowserLoading ? 'filePreview.htmlOpeningBrowser' : 'filePreview.htmlOpenBrowser') }}</button>
      </div>
      <div v-if="isHTML && htmlError" class="note bad html-error" role="alert">
        <span>{{ t('filePreview.renderFailed') }}{{ htmlError }}</span>
      </div>

      <div v-if="citation || hasLineTarget" class="citation-note" role="status">
        <span>{{ t("citationFile.currentFile") }}</span>
        <span v-if="selectedRange" class="citation-location">
          {{ selectedRange.lineStart === selectedRange.lineEnd
            ? t("citationFile.line", { line: selectedRange.lineStart })
            : t("citationFile.lines", { start: selectedRange.lineStart, end: selectedRange.lineEnd }) }}
        </span>
        <span v-else-if="lineSelection.status === 'unavailable'" class="citation-warning">
          {{ t(`citationFile.${lineSelection.reason}`) }}
        </span>
      </div>

      <div ref="body" class="body">
        <div v-if="kind === 'image'" class="image-wrap">
          <img :src="fileRawURL(path)" :alt="path" />
        </div>
        <div v-else-if="kind === 'audio'" class="media-wrap">
          <audio :key="`${path}:${mediaNonce}`" controls preload="metadata" :src="fileRawURL(path)" :aria-label="t('filePreview.audioLabel', { name: baseName })" @loadedmetadata="mediaMetadata" @error="mediaFailed" />
        </div>
        <div v-else-if="kind === 'video'" class="media-wrap">
          <video :key="`${path}:${mediaNonce}`" controls playsinline preload="metadata" :src="fileRawURL(path)" :aria-label="t('filePreview.videoLabel', { name: baseName })" @loadedmetadata="mediaMetadata" @error="mediaFailed" />
        </div>
        <div v-if="(kind === 'audio' || kind === 'video') && (mediaLoading || mediaDuration)" class="media-meta" role="status">
          <span v-if="mediaLoading">{{ t('filePreview.rendering') }}</span>
          <span v-else>{{ t('filePreview.duration', { time: mediaDurationLabel }) }}</span>
        </div>
        <!-- The browser's own PDF viewer, through the sandboxed raw endpoint —
             search, zoom and pagination for free, no plugin of ours. -->
        <div v-else-if="kind === 'pdf'" class="pdf-wrap">
          <p v-if="pdfLoading" class="media-loading" role="status">{{ t('filePreview.rendering') }}</p>
          <p v-if="pdfError" class="media-error" role="alert">{{ pdfError }} <button type="button" @click="retryPDF">{{ t('filePreview.retry') }}</button></p>
          <iframe :key="`${path}:${pdfNonce}`" class="pdf-frame" :src="fileRawURL(path)" :title="path" referrerpolicy="no-referrer" @load="pdfLoaded" @error="pdfFailed" />
          <div class="media-fallback">
            <span>{{ t('filePreview.pdfFallback') }}</span>
            <a :href="fileRawURL(path)" target="_blank" rel="noopener noreferrer">{{ t('filePreview.openRaw') }}</a>
            <a :href="fileDownloadURL(path)" :download="baseName">{{ t('filePreview.download') }}</a>
          </div>
        </div>
        <p v-if="mediaError" class="media-error" role="alert">{{ mediaError }} <button type="button" @click="retryMedia">{{ t('filePreview.retry') }}</button> <a :href="fileDownloadURL(path)" :download="baseName">{{ t('filePreview.download') }}</a></p>
        <!-- xlsx/xlsm: the spreadsheet component — a real grid with sheet
             tabs, not a flattened table. -->
        <div v-else-if="kind === 'sheet'" class="sheet-wrap">
          <div v-if="renderError" class="note bad">{{ renderError }}</div>
          <div v-else-if="!sheetBytes" class="note">{{ t("filePreview.rendering") }}</div>
          <VueOfficeExcel v-else :src="sheetBytes" class="sheet-view" @error="sheetFailed" />
        </div>
        <!-- docx: the server's markdown conversion. A refusal (old binary
             formats, a broken zip) is stated in the endpoint's own words with
             the download button one row up as the way out. -->
        <template v-else-if="kind === 'office'">
          <div v-if="renderError" class="note bad">{{ renderError }}</div>
          <div v-else-if="!rendered" class="note">{{ t("filePreview.rendering") }}</div>
          <MarkdownView v-else class="md" :source="rendered.text" />
        </template>
        <div v-else-if="kind === 'binary'" class="note">
          {{ t("filePreview.binaryNoPreview", { mime: content.mime ?? "" }) }}
        </div>
        <MarkdownView
          v-else-if="isMd && mdPreview"
          class="md"
          :source="content.text ?? ''"
        />
        <MermaidDiagram
          v-else-if="isMermaid && mdPreview"
          class="md"
          :source="content.text ?? ''"
        />
        <CodeBlock
          v-else-if="kind === 'text'"
          :code="content.text ?? ''"
          :lang="ext"
          line-numbers
          :uncollapsed="hasLineTarget"
          :preserve-trailing-line="preservePreviewTrailingLine(content)"
          :highlight-start="selectedRange?.lineStart"
          :highlight-end="selectedRange?.lineEnd"
          :collapsed-height="520"
          :expanded-height="100000"
        />
      </div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.preview {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}
.office-notice { margin: 0; font-size: 11px; }
.office-checking { display: flex; flex-direction: column; gap: 6px; }.office-checking small { color: var(--el-text-color-secondary); font-size: 11px; }
.office-detail-path { display: block; overflow-wrap: anywhere; font-size: 11px; color: var(--el-text-color-regular); }
.office-detail-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.html-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 6px 10px; border-bottom: 1px solid var(--el-border-color-lighter); font-size: 11px; color: var(--el-text-color-secondary); }
.html-toolbar .ghost:disabled { cursor: default; opacity: .55; }
.html-error { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0; }

.bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.path {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--el-text-color-regular);
  direction: rtl; /* long paths clip on the left, keeping the file name visible */
  text-align: left;
}

.file-kind {
  flex: 0 0 auto;
  padding: 2px 6px;
  border-radius: 5px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
  font-size: 10px;
  white-space: nowrap;
}

.ghost {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  flex: 0 0 auto;
  text-decoration: none; /* the download affordance is an <a>, not a button */

  &:hover {
    background: var(--el-fill-color);
  }
}

.meta {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  border-bottom: 1px solid var(--el-border-color-lighter);

  .warn {
    color: var(--el-color-warning);
  }

  .spacer {
    flex: 1;
  }
}

.mode {
  border: 0;
  background: transparent;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.citation-note {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  color: var(--el-text-color-secondary);
  font-size: 11px;
  line-height: 1.6;
}

.citation-location {
  color: var(--el-color-primary);
}

.citation-warning {
  color: var(--el-color-warning);
}

.body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 8px;
}

.note {
  padding: 16px 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}

.saved {
  font-size: 11px;
  color: var(--el-color-success);
  flex: 0 0 auto;
}

.editor {
  flex: 1;
  min-height: 0;
  border: 0;
  resize: none;
  padding: 10px 12px;
  font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--el-text-color-primary);
  background: var(--el-bg-color);
  outline: none;
  tab-size: 4;
}

.editbar {
  display: flex;
  gap: 8px;
  padding: 8px 10px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.act {
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  padding: 4px 14px;
  border-radius: 6px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  cursor: pointer;

  &.primary {
    background: #000;
    border-color: #000;
    color: #fff;
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }
}

.conflict {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--el-color-warning);
  background: color-mix(in srgb, var(--el-color-warning) 8%, transparent);
  border-bottom: 1px solid var(--el-border-color-lighter);

  .link {
    border: 0;
    background: transparent;
    cursor: pointer;
    font-size: 12px;
    text-decoration: underline;
    color: var(--el-text-color-primary);
    padding: 0 2px;

    &.danger {
      color: var(--el-color-danger);
    }
  }
}

.image-wrap {
  display: flex;
  justify-content: center;

  img {
    max-width: 100%;
    border-radius: 6px;
  }
}

/* The PDF fills the viewer: its own toolbar (zoom, search, page) replaces
   anything we would draw around it. */
.pdf-wrap {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.pdf-frame {
  flex: 1 1 0;
  border: 0;
  border-radius: 6px;
  min-width: 0;
  min-height: 0;
}

.media-wrap {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  min-height: 120px;
}
.media-wrap audio { width: 100%; }
.media-wrap video { width: 100%; max-height: 70vh; border-radius: 8px; background: var(--el-color-black); }
.media-fallback {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px 12px;
  padding: 8px 2px 0;
  color: var(--el-text-color-secondary);
  font-size: 11px;
}
.media-fallback a, .media-error a { color: var(--el-color-primary); text-decoration: none; }
.media-fallback a:hover, .media-error a:hover { text-decoration: underline; }
.media-meta, .media-loading { margin: 0; padding: 6px 0; color: var(--el-text-color-secondary); font-size: 11px; }
.media-error { margin: 8px 0; color: var(--el-color-warning); font-size: 12px; }
.media-error a { margin-left: 6px; }
.media-error button { margin: 0 6px; padding: 0; border: 0; background: transparent; color: var(--el-color-primary); cursor: pointer; font: inherit; }
.media-error button:hover { text-decoration: underline; }

/* The spreadsheet component sizes itself to its container, so the chain from
   .body down needs a definite height. */
.sheet-wrap {
  height: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.sheet-view {
  flex: 1 1 0;
  min-height: 0;
}

/* Mirrors TurnCard's markdown rules, tables included — the UA stylesheet draws
   no table borders, so a GFM table would read as aligned text without them. */
.md {
  font-size: 13px;
  line-height: 1.7;

  :deep(p) {
    margin: 0.4em 0;
  }

  :deep(pre) {
    background: var(--el-fill-color-light);
    padding: 8px 10px;
    border-radius: 6px;
    overflow-x: auto;
    font: 12px/1.55 ui-monospace, monospace;
  }

  :deep(code) {
    font-family: ui-monospace, monospace;
    font-size: 0.92em;
  }

  :deep(ul),
  :deep(ol) {
    padding-left: 1.4em;
    margin: 0.4em 0;
  }

  :deep(table) {
    border-collapse: collapse;
    margin: 0.5em 0;
    font-size: 13px;
    display: block;
    width: max-content;
    max-width: 100%;
    overflow-x: auto;
  }

  :deep(th),
  :deep(td) {
    border: 1px solid var(--el-border-color);
    padding: 4px 10px;
    text-align: left;
  }

  :deep(th) {
    background: var(--el-fill-color-light);
  }
}
</style>
