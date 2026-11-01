<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import { randomUUID } from "@/utils/uuid";
import { createDesktopSurface, desktopSurfaceGeometry, desktopElementPick, desktopEventMatches, type DesktopBrowserBridge, type DesktopBrowserClip, type DesktopBrowserEvent } from "@/agent/desktopBrowser";
import { browserControlMemory, clearBrowserLease, installBrowserLease, observeBrowserControl, targetMatches } from "@/agent/browserSheets";
import { nativeDocumentStamp, nativeDocumentUnavailable, nativeLeaseRejected, nativeNavigationURL, nativeNewTabPage, ownsNativeControl, type NativeBrowserState, type NativeBrowserTarget } from "@/agent/nativeBrowser";
import type { BrowserBookmark } from "@/agent/browserBookmarks";
import { useAccountBrowserBookmarks } from "@/agent/accountBrowserBookmarks";
import type { ElementPick } from "@/agent/elementPicker";
import BrowserBookmarks from "./BrowserBookmarks.vue";
import BrowserNewTab from "./BrowserNewTab.vue";
import BrowserControlButton from "./BrowserControlButton.vue";
import { browserRecoveryReason } from "@/agent/browserRecovery";
import { snapshotReferenceFavicon } from "@/agent/referenceFavicon";
import { IMMERSIVE_LAYOUT_EVENT } from "@/agent/immersiveViewportMotion";

const props = defineProps<{ sessionId: string; active: boolean; initialState: NativeBrowserState; initialUrl?: string; target: NativeBrowserTarget; bridge: DesktopBrowserBridge }>();
const emit = defineEmits<{ title: [string]; navigate: [string]; state: [NativeBrowserState]; pick: [ElementPick]; recover: [] }>();
const { t } = useI18n();
const state = ref(props.initialState);
const pageEl = ref<HTMLElement>();
const attached = ref(false);
const connected = ref(true);
const interactive = ref(false);
const busy = ref(false);
const error = ref("");
const recovering = ref(false);
const surfaceError = ref("");
const actionError = ref("");
const pageVisible = ref(!document.hidden);
const occluded = ref(false);
const visible = computed(() => props.active && pageVisible.value && !recovering.value);
const control = browserControlMemory(props.sessionId, props.target.context_ref, props.target.tab_id);
const lease = computed(() => control.context === props.target.context_ref && control.tab === props.target.tab_id ? control.token : "");
const owned = computed(() => ownsNativeControl(state.value, lease.value, control.epoch));
const idle = computed(() => connected.value && !busy.value && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked);
const picking = ref(false);
const pickerPending = ref(false);
const toolsReady = computed(() => visible.value && attached.value && owned.value && idle.value && !state.value.page_loading && !nativeDocumentUnavailable(state.value) && !nativeNewTabPage(state.value) && !occluded.value && !surfaceError.value);
const canNavigate = computed(() => owned.value && idle.value && !picking.value && !pickerPending.value);
const canGoBack = computed(() => canNavigate.value && !state.value.page_loading && !navigationPending.value && state.value.can_go_back === true);
const navigationPending = ref(false);
const loading = computed(() => !error.value && !state.value.page_load_error && (state.value.page_loading || navigationPending.value));
const showNewTab = computed(() => connected.value && !error.value && !navigationPending.value && nativeNewTabPage(state.value));
const controlLabel = computed(() => t(`nativeBrowser.${owned.value ? "human" : state.value.control === "human" ? "other" : "agent"}`));
const address = ref((props.initialUrl || state.value.url) === "about:blank" ? "" : props.initialUrl || state.value.url || "");
const editingAddress = ref(false);
const addressBeforeEdit = ref("");
const composingAddress = ref(false);
const promptText = ref("");
const bookmarkStore = useAccountBrowserBookmarks();
const bookmarks = bookmarkStore.bookmarks;
watch(bookmarks, () => cancelBookmarkSelection());
const bookmarksManager = ref<InstanceType<typeof BrowserBookmarks>>();
const pendingBookmark = ref<{ url: string; fromURL: string; fromDocument: string; fromGeneration: number | undefined; sessionId: string; target: NativeBrowserTarget } | null>(null);
const binding = { session_id: props.sessionId, context_ref: props.target.context_ref, tab_id: props.target.tab_id };
const surface = createDesktopSurface(props.bridge, binding, randomUUID(), {
  interactive: value => { interactive.value = value; },
  error: cause => { if (!disposed) surfaceError.value = cause instanceof Error ? cause.message : String(cause); },
});
let disposed = false;
let poll: ReturnType<typeof setTimeout> | undefined;
let reader: AbortController | undefined;
let readEpoch = 0;
let observation = 0;
let layoutFrame = 0;
let layoutTail = 0;
let lastUsableBounds: { x: number; y: number; width: number; height: number } | undefined;
const motionTargets = new Set<Element>();
let resize: ResizeObserver | undefined;
let mutations: MutationObserver | undefined;
let unsubscribe: (() => void) | undefined;
let pickEpoch = 0;
let pickCommit = 0;
let pickReader: AbortController | undefined;
let reportedTitle = "";
let reportedURL = "";

function cancelBookmarkSelection() {
  if (!pendingBookmark.value) return;
  pendingBookmark.value = null; editingAddress.value = false;
  address.value = state.value.url === "about:blank" ? "" : state.value.url;
}
function applyState(next: NativeBrowserState) {
  if (!targetMatches(next, props.target) || (next.generation ?? 0) < (state.value.generation ?? 0)) return;
  const previous = state.value;
  observation++;
  if (next.url !== previous.url || next.frame_identity !== previous.frame_identity || next.page_load_error) navigationPending.value = false;
  if (pendingBookmark.value && (next.url !== pendingBookmark.value.fromURL || nativeDocumentStamp(next) !== pendingBookmark.value.fromDocument || next.generation !== pendingBookmark.value.fromGeneration || next.page_load_error)) cancelBookmarkSelection();
  if (nativeDocumentStamp(next) !== nativeDocumentStamp(previous)) { pickCommit++; pickReader?.abort(); void stopPicker(); }
  state.value = next;
  observeBrowserControl(props.sessionId, next);
  connected.value = true; error.value = "";
  if (!editingAddress.value && !navigationPending.value && next.url) address.value = next.url === "about:blank" ? "" : next.url;
  const title = next.url === "about:blank" ? t("nativeBrowser.newTabPageTitle") : next.title;
  if (title && title !== reportedTitle) { reportedTitle = title; emit("title", title); }
  if (next.url && next.url !== "about:blank" && next.url !== reportedURL) { reportedURL = next.url; emit("navigate", next.url); }
  emit("state", next);
  if (!owned.value || next.busy || next.dialog || next.debugger_paused || next.devtools_blocked || nativeDocumentUnavailable(next)) void stopPicker();
  scheduleLayout();
}
function stopRead() { readEpoch++; clearTimeout(poll); reader?.abort(); reader = undefined; }
function recoverIdentity(cause?: unknown) {
  if (disposed || recovering.value || cause !== undefined && !browserRecoveryReason(cause)) return false;
  recovering.value = true; connected.value = false;
  cancelBookmarkSelection(); stopRead(); pickEpoch++; pickCommit++; pickReader?.abort();
  clearBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, lease.value, control.epoch);
  error.value = ""; actionError.value = "";
  void syncSurface(); emit("recover"); return true;
}
async function readTargetState(signal?: AbortSignal): Promise<NativeBrowserState> {
  const next = await api.browserState(props.sessionId, signal, props.target);
  if (next.backend !== "native") throw new Error(next.error || t("nativeBrowser.unavailable"));
  if (!targetMatches(next, props.target)) throw new Error(t("nativeBrowser.tabUnavailable"));
  return next;
}
async function refresh() {
  stopRead();
  if (disposed || !visible.value) return;
  const epoch = readEpoch;
  const controller = new AbortController(); reader = controller;
  const timeout = setTimeout(() => controller.abort(), 15000);
  try {
    const next = await readTargetState(controller.signal);
    if (disposed || epoch !== readEpoch || !visible.value) return;
    applyState(next);
  } catch (cause) {
    if (!disposed && epoch === readEpoch) {
      if (recoverIdentity(cause)) return;
      connected.value = false; error.value = cause instanceof Error ? cause.message : String(cause); void syncSurface();
    }
  } finally {
    clearTimeout(timeout);
    if (!disposed && epoch === readEpoch && visible.value) poll = setTimeout(() => void refresh(), error.value ? 3000 : state.value.page_loading || state.value.busy ? 750 : 1500);
  }
}
function overlayIntersects(rect: DOMRect) {
  // Native views do not share CSS z-index with the shell. Hide behind visible
  // modal/popover/floating-chat rectangles rather than covering their controls.
  return [...document.querySelectorAll<HTMLElement>('.el-overlay, .el-popper, [role="dialog"], [role="alertdialog"], .floating-chat, [data-desktop-browser-overlay]')].some(element => {
    if (pageEl.value?.contains(element)) return false;
    const style = getComputedStyle(element);
    if (style.visibility === "hidden" || style.display === "none" || style.opacity === "0") return false;
    const box = element.getBoundingClientRect();
    return box.width > 0 && box.height > 0 && box.left < rect.right && box.right > rect.left && box.top < rect.bottom && box.bottom > rect.top;
  });
}
async function syncSurface() {
  if (disposed) return;
  const rect = pageEl.value?.getBoundingClientRect();
  const clips: DesktopBrowserClip[] = [];
  for (let ancestor = pageEl.value?.parentElement; ancestor && ancestor !== document.body; ancestor = ancestor.parentElement) {
    const style = getComputedStyle(ancestor);
    const clipX = /^(hidden|clip|scroll|auto)$/.test(style.overflowX), clipY = /^(hidden|clip|scroll|auto)$/.test(style.overflowY);
    if (clipX || clipY) { const box = ancestor.getBoundingClientRect(); clips.push({ x: box.x, y: box.y, width: box.width, height: box.height, clipX, clipY }); }
  }
  const geometry = rect ? desktopSurfaceGeometry(rect, { width: document.documentElement.clientWidth, height: document.documentElement.clientHeight }, clips) : null;
  if (geometry?.visible && geometry.bounds) lastUsableBounds = geometry.bounds;
  occluded.value = !!rect && overlayIntersects(rect);
  // Hide without replacing the last real viewport with a transient sliver.
  // Before the first usable layout, attach remains hidden in the native host.
  if (!lastUsableBounds) { interactive.value = false; return; }
  const shown = visible.value && geometry?.visible === true && !showNewTab.value && !occluded.value && !state.value.dialog && !error.value && !surfaceError.value;
  await surface.update({
    bounds: lastUsableBounds, visible: shown,
    blocked: !shown || !owned.value || !idle.value || nativeDocumentUnavailable(state.value),
    ...(owned.value ? { lease_token: lease.value, control_epoch: control.epoch, generation: state.value.generation } : {}),
  });
}
function hasLayoutMotion() {
  const targets = new Set(motionTargets);
  for (let element: Element | null | undefined = pageEl.value; element; element = element.parentElement) targets.add(element);
  let running = false;
  for (const target of targets) {
    const moving = target.getAnimations().some(animation => {
      if (animation.playState !== "running" && !animation.pending) return false;
      const effect = animation.effect;
      return effect instanceof KeyframeEffect && effect.getKeyframes().some(frame => Object.keys(frame).some(key => /^(transform|translate|scale|rotate|width|height|minWidth|maxWidth|minHeight|maxHeight|left|right|top|bottom|margin.*|padding.*|flex.*|gap|columnGap|rowGap|gridTemplate.*)$/.test(key)));
    });
    if (moving) running = true;
    else motionTargets.delete(target);
  }
  return running;
}
function layoutTick() {
  layoutFrame = 0;
  if (disposed || !visible.value) return;
  void syncSurface();
  if (hasLayoutMotion() || layoutTail-- > 0) layoutFrame = requestAnimationFrame(layoutTick);
}
function scheduleLayout() {
  if (disposed || !visible.value) return;
  // A short tail covers layout settling; active CSS/WAAPI geometry animations
  // keep tracking every frame through their final position. Spinner/color
  // animations are deliberately excluded so idle panes do not poll per frame.
  layoutTail = 2;
  if (!layoutFrame) layoutFrame = requestAnimationFrame(layoutTick);
}
function geometryMotion(event: Event) {
  const target = event.target;
  if (!(target instanceof Element) || !pageEl.value || !(target.contains(pageEl.value) || target.parentElement?.contains(pageEl.value))) return;
  motionTargets.add(target);
  scheduleLayout();
}
async function stopPicker(force = false) {
  const wasPicking = picking.value || pickerPending.value;
  pickEpoch++; picking.value = false; pickerPending.value = false;
  if ((wasPicking || force) && surface.id) try { await props.bridge.setPicker({ surface_id: surface.id, enabled: false }); } catch { /* A hidden or closed surface is already non-interactive. */ }
}
async function togglePicker() {
  if (picking.value || pickerPending.value) { pickCommit++; await stopPicker(); return; }
  if (!toolsReady.value) return;
  const epoch = ++pickEpoch; pickerPending.value = true; actionError.value = "";
  try {
    await syncSurface();
    await props.bridge.setPicker({ surface_id: surface.id, enabled: true });
    if (disposed || epoch !== pickEpoch || !toolsReady.value) { await stopPicker(true); return; }
    picking.value = true;
  } catch (cause) { if (!disposed && epoch === pickEpoch) actionError.value = t("desktopBrowser.inspectFailed", { message: String(cause) }); }
  finally { if (epoch === pickEpoch) pickerPending.value = false; }
}
async function openDevTools() {
  if (!toolsReady.value || picking.value || pickerPending.value) return;
  actionError.value = "";
  try { await syncSurface(); await props.bridge.openDevTools({ surface_id: surface.id }); }
  catch (cause) { if (!disposed) actionError.value = t("desktopBrowser.inspectFailed", { message: String(cause) }); }
}
async function onSurfaceEvent(event: DesktopBrowserEvent) {
  if (disposed || !desktopEventMatches(event, binding, surface.id)) return;
  if (event.type === "picker-ended") { pickEpoch++; picking.value = false; pickerPending.value = false; return; }
  if (event.type === "state") {
    if (!visible.value) return;
    stopRead(); applyState(event.state); poll = setTimeout(() => void refresh(), 1500); return;
  }
  if (event.type === "error") { surfaceError.value = event.message; void syncSurface(); return; }
  if (event.type !== "pick" || !visible.value || !owned.value) return;
  pickEpoch++; picking.value = false; pickerPending.value = false;
  const commit = ++pickCommit, previousObservation = observation;
  pickReader?.abort(); const controller = new AbortController(); pickReader = controller;
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const fresh = await readTargetState(controller.signal);
    if (disposed || !visible.value || commit !== pickCommit || !owned.value || nativeDocumentUnavailable(fresh)) return;
    if (observation !== previousObservation && fresh.version !== state.value.version) return;
    const pick = desktopElementPick(event.pick, fresh, binding);
    if (pick) {
      const stamp = nativeDocumentStamp(fresh), session = props.sessionId;
      const icon = await snapshotReferenceFavicon(pick.icon);
      if (disposed || !visible.value || commit !== pickCommit || session !== props.sessionId || nativeDocumentStamp(state.value) !== stamp || state.value.url !== fresh.url) return;
      const { icon: _displayIcon, ...captured } = pick;
      emit("pick", { ...captured, ...(icon ? { icon } : {}) });
    }
    else actionError.value = t("desktopBrowser.pickStale");
  } catch (cause) { if (!disposed && visible.value && commit === pickCommit) actionError.value = t("desktopBrowser.inspectFailed", { message: String(cause) }); }
  finally { clearTimeout(timeout); if (pickReader === controller) pickReader = undefined; }
}
async function action(work: () => Promise<NativeBrowserState>, canStart?: () => boolean) {
  if (busy.value || disposed || recovering.value) return false;
  const sequence = observation;
  const sent = { context: props.target.context_ref, tab: props.target.tab_id, token: lease.value, epoch: control.epoch };
  busy.value = true; actionError.value = ""; await stopPicker(); await syncSurface();
  try {
    if (disposed || recovering.value || (canStart && !canStart())) return false;
    const next = await work();
    if (disposed || recovering.value) return false;
    if (visible.value && sequence === observation && targetMatches(next, props.target)) applyState(next);
    return true;
  } catch (cause) {
    if (!disposed) {
      if (recoverIdentity(cause)) return false;
      if (nativeLeaseRejected((cause as { status?: number }).status)) clearBrowserLease(props.sessionId, sent.context, sent.tab, sent.token, sent.epoch);
      actionError.value = cause instanceof Error ? cause.message : String(cause);
    }
    return false;
  } finally {
    busy.value = false; navigationPending.value = false;
    if (!disposed && !recovering.value) { await syncSurface(); await refresh(); }
  }
}
async function takeControl(force = false) {
  if (!visible.value || !connected.value) return false;
  return action(async () => {
    const next = await api.browserControl(props.sessionId, "human", { force, context_ref: props.target.context_ref, tab_id: props.target.tab_id });
    if (!targetMatches(next, props.target) || !next.lease_token) throw new Error(t("nativeBrowser.lostControl"));
    if (!disposed) installBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, next.control_epoch, next.lease_token);
    return readTargetState();
  });
}
async function returnControl() {
  if (!owned.value) return;
  const token = lease.value, epoch = control.epoch;
  await action(async () => {
    await api.browserControl(props.sessionId, "agent", { lease_token: token, context_ref: props.target.context_ref, tab_id: props.target.tab_id });
    clearBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, token, epoch);
    return readTargetState();
  });
}
async function navigate(value = address.value, canStart?: () => boolean) {
  if (!canNavigate.value || (canStart && !canStart())) return false;
  const url = nativeNavigationURL(value, state.value.url, location.origin);
  if (!url) { actionError.value = t("nativeBrowser.invalidUrl"); return false; }
  editingAddress.value = false; address.value = url; navigationPending.value = true;
  return action(() => api.browserInput(props.sessionId, { type: "navigate", url }, lease.value, { ...props.target, generation: state.value.generation! }), canStart);
}
async function answerDialog(accept: boolean) {
  if (!owned.value || busy.value) return;
  await action(() => api.browserInput(props.sessionId, { type: "dialog", accept, text: promptText.value }, lease.value, { ...props.target, generation: state.value.generation! }));
}
async function goBack() {
  if (!canGoBack.value) return false;
  cancelBookmarkSelection(); editingAddress.value = false; navigationPending.value = true;
  return action(() => api.browserInput(props.sessionId, { type: "back" }, lease.value, { ...props.target, generation: state.value.generation! }));
}
function openBookmarks() { cancelBookmarkSelection(); bookmarksManager.value?.add(); }
function editBookmark(card: BrowserBookmark) { cancelBookmarkSelection(); bookmarksManager.value?.edit(card); }
function removeBookmark(card: BrowserBookmark) { cancelBookmarkSelection(); bookmarksManager.value?.remove(card); }
async function selectBookmark(card: BrowserBookmark) {
  if (busy.value || pendingBookmark.value || disposed || !visible.value || !connected.value) return;
  const url = nativeNavigationURL(card.url, state.value.url, location.origin);
  if (card.needsRepair || !url) { cancelBookmarkSelection(); bookmarksManager.value?.repair(card); return; }
  const stamp = nativeDocumentStamp(state.value);
  if (!stamp) { actionError.value = t("desktopBrowser.pickStale"); return; }
  pendingBookmark.value = { url, fromURL: state.value.url, fromDocument: stamp, fromGeneration: state.value.generation, sessionId: props.sessionId, target: { ...props.target } };
  const chosen = pendingBookmark.value;
  const current = () => !disposed && visible.value && connected.value && owned.value && pendingBookmark.value === chosen && props.sessionId === chosen.sessionId && props.target.context_ref === chosen.target.context_ref && props.target.tab_id === chosen.target.tab_id && targetMatches(state.value, chosen.target) && nativeDocumentStamp(state.value) === chosen.fromDocument && state.value.generation === chosen.fromGeneration && state.value.url === chosen.fromURL;
  addressBeforeEdit.value = state.value.url === "about:blank" ? "" : state.value.url;
  address.value = url; editingAddress.value = true;
  try {
    if (!owned.value && !(await takeControl(state.value.control === "human"))) return;
    // Takeover may finish after this card, Page, or document was discarded.
    // Keep its lease, but never navigate for a cancelled or different choice.
    if (!current() || !canNavigate.value) return;
    await navigate(chosen.url, current);
  } finally {
    if (pendingBookmark.value === chosen) cancelBookmarkSelection();
  }
}
function focusAddress(event: FocusEvent) { cancelBookmarkSelection(); editingAddress.value = canNavigate.value; addressBeforeEdit.value = state.value.url === "about:blank" ? "" : state.value.url; (event.target as HTMLInputElement).select(); }
function addressKey(event: KeyboardEvent) {
  if (event.isComposing || composingAddress.value || event.keyCode === 229) return;
  if (event.key === "Escape") { address.value = addressBeforeEdit.value; editingAddress.value = false; (event.target as HTMLInputElement).blur(); }
  else if (event.key === "Enter" && canNavigate.value) { event.preventDefault(); void navigate(); }
}
function visibilityChanged() { pageVisible.value = !document.hidden; }
function escapePicker(event: KeyboardEvent) { if (event.key === "Escape" && (picking.value || pickerPending.value)) { event.preventDefault(); pickCommit++; void stopPicker(); } }
watch(() => JSON.stringify(state.value.dialog ?? null), () => { promptText.value = state.value.dialog?.default_value ?? ""; });
watch([visible, owned, busy, idle, showNewTab, lease, () => control.epoch, () => state.value.generation, () => state.value.page_load_error], () => { void syncSurface(); }, { flush: "sync" });
watch(visible, shown => {
  if (!shown) { cancelBookmarkSelection(); pickCommit++; pickReader?.abort(); void stopPicker(); stopRead(); cancelAnimationFrame(layoutFrame); layoutFrame = 0; motionTargets.clear(); }
  else { scheduleLayout(); void refresh(); }
}, { flush: "sync" });
watch(() => props.initialUrl, url => { if (url && url !== "about:blank" && url !== state.value.url) { address.value = url; editingAddress.value = true; } });
onMounted(async () => {
  applyState(props.initialState);
  unsubscribe = props.bridge.onEvent(event => void onSurfaceEvent(event));
  resize = new ResizeObserver(scheduleLayout); if (pageEl.value) resize.observe(pageEl.value);
  mutations = new MutationObserver(scheduleLayout); mutations.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["style", "class", "hidden", "aria-hidden", "open"] });
  window.addEventListener("resize", scheduleLayout); window.addEventListener("scroll", scheduleLayout, true);
  window.addEventListener(IMMERSIVE_LAYOUT_EVENT, scheduleLayout);
  for (const name of ["transitionrun", "transitionstart", "transitionend", "transitioncancel", "animationstart", "animationend", "animationcancel"]) document.addEventListener(name, geometryMotion, true);
  document.addEventListener("visibilitychange", visibilityChanged); window.addEventListener("keydown", escapePicker, true);
  await surface.attach();
  if (disposed) return;
  attached.value = !!surface.id; await nextTick(); await syncSurface();
  void refresh();
});
onBeforeUnmount(() => {
  disposed = true; cancelBookmarkSelection(); stopRead(); pickCommit++; pickReader?.abort(); cancelAnimationFrame(layoutFrame);
  resize?.disconnect(); mutations?.disconnect(); unsubscribe?.();
  window.removeEventListener("resize", scheduleLayout); window.removeEventListener("scroll", scheduleLayout, true);
  window.removeEventListener(IMMERSIVE_LAYOUT_EVENT, scheduleLayout);
  for (const name of ["transitionrun", "transitionstart", "transitionend", "transitioncancel", "animationstart", "animationend", "animationcancel"]) document.removeEventListener(name, geometryMotion, true);
  document.removeEventListener("visibilitychange", visibilityChanged); window.removeEventListener("keydown", escapePicker, true);
  void surface.close().catch(() => {});
});
</script>

<template>
  <section class="desktop-browser" :data-browser-tab-id="target.tab_id" :data-browser-context-ref="target.context_ref" :aria-label="t('desktopBrowser.title')" data-testid="desktop-browser-pane">
    <div class="desktop-toolbar" role="toolbar" :aria-label="t('nativeBrowser.navigation')">
      <button data-testid="browser-back" :disabled="!canGoBack" :aria-label="t('nativeBrowser.back')" :title="t('nativeBrowser.back')" @click="goBack"><svg viewBox="0 0 20 20" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M16 10H4m5-5-5 5 5 5" /></svg></button>
      <button :disabled="!canNavigate || state.url === 'about:blank'" :aria-label="t('desktopBrowser.refresh')" :title="t('desktopBrowser.refresh')" @click="navigate(state.url)">↻</button>
      <div class="desktop-location"><span class="status-dot" :class="{ loading, owned }" :title="controlLabel" /><input v-model="address" :readonly="!canNavigate" :aria-label="t('nativeBrowser.url')" :placeholder="t('nativeBrowser.url')" autocomplete="off" spellcheck="false" @focus="focusAddress" @input="editingAddress = canNavigate" @blur="editingAddress = false" @keydown="addressKey" @compositionstart="composingAddress = true" @compositionend="composingAddress = false" /></div>
      <BrowserControlButton v-if="owned" mode="return" :disabled="busy" :title="t('nativeBrowser.controlHint')" :aria-label="t('nativeBrowser.giveBack')" @click="returnControl">{{ t('nativeBrowser.giveBackShort') }}</BrowserControlButton>
      <BrowserControlButton v-else mode="takeover" :disabled="busy || !connected" :title="t('nativeBrowser.controlHint')" :aria-label="t(state.control === 'human' ? 'nativeBrowser.reclaim' : 'nativeBrowser.takeover')" @click="takeControl(state.control === 'human')">{{ t(busy ? 'nativeBrowser.takingShort' : state.control === 'human' ? 'nativeBrowser.reclaimShort' : 'nativeBrowser.takeoverShort') }}</BrowserControlButton>
      <button :disabled="!picking && (!toolsReady || pickerPending)" :aria-label="t('nativeBrowser.pick')" :aria-pressed="picking" :title="t(toolsReady ? 'desktopBrowser.picker' : 'desktopBrowser.takeoverTools')" @click="togglePicker">{{ t('nativeBrowser.pickShort') }}</button>
      <button :disabled="!toolsReady || picking || pickerPending" :aria-label="t('desktopBrowser.devtools')" :title="t(toolsReady ? 'desktopBrowser.devtools' : 'desktopBrowser.takeoverTools')" @click="openDevTools">⌘<span class="devtools-label"> {{ t('desktopBrowser.devtools') }}</span></button>
    </div>
    <div class="desktop-status" role="status"><span>{{ controlLabel }}</span><span v-if="loading">{{ t('nativeBrowser.pageLoading') }}</span><span v-else-if="state.busy">{{ t('nativeBrowser.operationPending') }}</span><span v-else-if="picking">{{ t('desktopBrowser.picker') }}</span><span v-else-if="interactive">{{ t('desktopBrowser.nativeInput') }}</span><span v-else-if="!showNewTab">{{ t('desktopBrowser.readonly') }}</span></div>
    <div v-if="error || surfaceError || actionError || state.page_load_error || state.debugger_paused || state.devtools_blocked" class="desktop-notice failure" role="alert">
      <span v-if="error">{{ t('nativeBrowser.connectionFailed') }}<details><summary>{{ t('nativeBrowser.connectionDetails') }}</summary>{{ error }}</details></span><span v-if="surfaceError">{{ t('desktopBrowser.surfaceFailed', { message: surfaceError }) }}</span><span v-if="actionError">{{ actionError }}</span><span v-if="state.page_load_error" :title="state.page_load_error">{{ t('nativeBrowser.pageLoadFailed') }}</span><span v-if="state.debugger_paused">{{ t('nativeBrowser.debugPaused') }}</span><span v-if="state.devtools_blocked">{{ t('nativeBrowser.debugBlocked') }}</span><button v-if="error || surfaceError" data-testid="browser-recover" @click="recoverIdentity()">{{ t('nativeBrowser.reconnect') }}</button>
    </div>
    <BrowserBookmarks ref="bookmarksManager" :current-url="state.url" :current-title="showNewTab ? '' : state.title" />
    <div v-if="state.dialog" class="desktop-dialog" role="alertdialog" :aria-label="t('nativeBrowser.dialog', { type: state.dialog.type })"><strong>{{ t('nativeBrowser.dialog', { type: state.dialog.type }) }}</strong><p>{{ state.dialog.message }}</p><input v-if="state.dialog.type === 'prompt'" v-model="promptText" :disabled="!owned || busy" :aria-label="t('nativeBrowser.prompt')" /><button :disabled="!owned || busy" @click="answerDialog(false)">{{ t('nativeBrowser.dismiss') }}</button><button :disabled="!owned || busy" @click="answerDialog(true)">{{ t('nativeBrowser.accept') }}</button></div>
    <div ref="pageEl" class="desktop-page" data-testid="desktop-browser-slot" :data-interactive="interactive">
      <BrowserNewTab v-if="showNewTab" :bookmarks="bookmarks" :bookmark-status="bookmarkStore.state" @retry="bookmarkStore.refresh()" @sync="bookmarksManager?.syncLocalBookmarks()" :busy="busy || !!pendingBookmark" @select="selectBookmark" @add="openBookmarks()" @edit="editBookmark" @remove="removeBookmark" @import="bookmarksManager?.importBookmarks()" @export="bookmarksManager?.exportBookmarks()" />
      <p v-else-if="occluded" class="surface-notice">{{ t('desktopBrowser.hiddenForOverlay') }}</p>
      <p v-else-if="!attached" class="surface-notice">{{ t('desktopBrowser.connecting') }}</p>
    </div>
  </section>
</template>

<style scoped>
.desktop-browser { display:flex; flex-direction:column; height:100%; min-height:0; min-width:0; color:var(--el-text-color-primary); background:var(--el-bg-color); }
.desktop-toolbar { display:flex; align-items:center; gap:6px; padding:8px; border-bottom:1px solid var(--el-border-color-lighter); flex:none; }
button,input { font:inherit; font-size:12px; color:inherit; background:var(--el-bg-color); border:1px solid var(--el-border-color); border-radius:6px; padding:6px 8px; }
button { cursor:pointer; white-space:nowrap; } button:disabled { opacity:.5; cursor:default; } button:focus-visible,input:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
.desktop-location { flex:1; min-width:80px; display:flex; align-items:center; gap:7px; border:1px solid var(--el-border-color); border-radius:7px; padding-left:8px; }
.desktop-location input { min-width:0; flex:1; border:0; background:transparent; }
.status-dot { width:7px; height:7px; flex:none; border-radius:50%; background:var(--el-text-color-placeholder); }.status-dot.owned { background:var(--el-color-success); }.status-dot.loading { width:12px; height:12px; border:2px solid var(--el-border-color); border-top-color:var(--el-color-primary); background:transparent; animation:desktop-spin 1s linear infinite; }
.desktop-status { display:flex; gap:10px; padding:5px 10px; color:var(--el-text-color-secondary); font-size:11px; flex-wrap:wrap; }
.desktop-page { flex:1; min-height:160px; position:relative; overflow:hidden; background:var(--el-bg-color); }
.surface-notice { margin:14px; color:var(--el-text-color-secondary); font-size:12px; }
.desktop-notice,.desktop-dialog { padding:10px; display:flex; gap:8px; flex-wrap:wrap; align-items:center; font-size:12px; background:var(--el-fill-color-light); }.failure { color:var(--el-color-danger); }.desktop-dialog p { width:100%; margin:0; white-space:pre-wrap; }
@keyframes desktop-spin { to { transform:rotate(360deg); } }
@media(max-width:650px) { .devtools-label { display:none; }.desktop-toolbar { gap:4px; padding:6px; }.desktop-toolbar button { padding:6px; } }
</style>
