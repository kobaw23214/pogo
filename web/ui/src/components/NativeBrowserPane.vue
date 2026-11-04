<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import { nativeConfirmedClick, nativeContentRect, nativeDisplayPoint, nativeDisplayRect, nativeDocumentStamp, nativeDocumentUnavailable, nativeElementPick, nativeFrameMatches, nativeLeaseRejected, nativeNavigationURL, nativeNewTabPage, nativePageLoading, nativeReadFailure, nativeViewportPoint, nativeViewedTab, ownsNativeControl, type NativeBrowserInput, type NativeBrowserRect, type NativeBrowserState, type NativeBrowserTarget } from "@/agent/nativeBrowser";
import { browserControlMemory, clearBrowserLease, installBrowserLease, observeBrowserControl, targetMatches } from "@/agent/browserSheets";
import { browserPickerTransport } from "@/agent/browserPickerTransport";
import { createBrowserPicker, pickPreviewMatches, type BrowserPickPreview, type BrowserPickerFrame } from "@/agent/browserPicker";
import type { ElementPick } from "@/agent/elementPicker";
import { browserCaptureMatches, browserCaptureResolution, desktopBrowserViewport } from "@/agent/browserViewport";
import { browserLiveURL, createBrowserLive, liveDisplayAuthority, liveFrameMatches, type BrowserCodecPreference, type BrowserLiveAuthority, type BrowserLiveEvent, type BrowserLiveFrame } from "@/agent/browserLive";
import { liveButton, liveKeyKind, liveModifiers, LiveTextCommit } from "@/agent/browserLiveInput";
import { browserHeldFrameMatches, browserPixelBridgeTransform, browserPresentedPixelRect, cancelBrowserPixelGeometry, captureBrowserHeldFrame, type BrowserHeldFrame, type BrowserPixelRect, type BrowserPresentationBinding, type BrowserPresentedVideo } from "@/agent/browserPresentation";
import BrowserBookmarks from "./BrowserBookmarks.vue";
import BrowserNewTab from "./BrowserNewTab.vue";
import BrowserControlButton from "./BrowserControlButton.vue";
import BrowserDownloads from "./BrowserDownloads.vue";
import { observeBrowserDownloads } from "@/agent/browserDownloads";
import type { BrowserBookmark } from "@/agent/browserBookmarks";
import { useAccountBrowserBookmarks } from "@/agent/accountBrowserBookmarks";
import { browserRecoveryReason } from "@/agent/browserRecovery";
import { snapshotReferenceFavicon } from "@/agent/referenceFavicon";
import { browserHumanAdoptions, type BrowserHumanAdoption } from "@/agent/browserHumanAdoption";
import type { BrowserPerformance } from "@/agent/browserPerformance";
import { registerBrowserWarmView } from "@/agent/browserWarmViews";
import { acceptsImmersivePaneLayout, browserLayoutSize, IMMERSIVE_LAYOUT_EVENT, notifyImmersivePresented, type ImmersiveLayoutDetail } from "@/agent/immersiveViewportMotion";
import { AGENT_MARK_HOLD_MS, AGENT_PENDING_MARK_MS, browserAgentOverlayGeometry, browserAgentOverlayMatches, type BrowserAgentOverlay } from "@/agent/browserAgentOverlay";

const props = defineProps<{
  sessionId: string;
  active: boolean;
  initialState: NativeBrowserState;
  initialUrl?: string;
  target: NativeBrowserTarget;
  closing?: boolean;
}>();
const emit = defineEmits<{ title: [string]; navigate: [string]; pick: [ElementPick]; state: [NativeBrowserState]; recover: []; openFile: [string] }>();
const { t } = useI18n();
const state = ref<NativeBrowserState>(props.initialState);
const stopDownloadUpdates = observeBrowserDownloads(props.sessionId, snapshot => {
  if (props.closing) return;
  state.value = { ...state.value, downloads: snapshot.downloads, downloads_rejected_count: snapshot.rejected_count, downloads_last_rejection: snapshot.last_rejection, downloads_max_file_bytes: snapshot.max_file_bytes };
});
onBeforeUnmount(stopDownloadUpdates);
// Share a lease only between views of this exact Page in the same window.
// Reading a sibling tab never grants it human input authority.
const controlMemory = browserControlMemory(props.sessionId, props.target.context_ref, props.target.tab_id);
const lease = computed(() => controlMemory.context === props.target.context_ref && controlMemory.tab === props.target.tab_id ? controlMemory.token : "");
const leaseEpoch = computed(() => controlMemory.epoch);
const busy = ref(false);
const connected = ref(true);
const navigationPending = ref(false);
const initialFramePending = ref(true);
const slowLoading = ref(false);
let loadingTimer: ReturnType<typeof setTimeout> | undefined;
const error = ref("");
const recovering = ref(false);
const frameIssue = ref<"changing" | "unavailable" | "view-only" | "">("");
const frameError = ref("");
const actionError = ref("");
const liveFailureMessage = ref("");
const displayError = ref("");
const displayRecovering = ref(false);
const resetNotice = ref<string | null>(props.initialState.reset ? props.initialState.reset_reason ?? "" : null);
const frameURL = ref("");
const frameVersion = ref("");
const frameIdentity = ref("");
const frameReady = ref(false);
const frameViewport = ref(props.initialState.viewport);
const liveOverlay = shallowRef<BrowserAgentOverlay | null>(null);
const canvasSize = ref({ width: 0, height: 0 });
const ripple = ref("");
const ripplePoint = ref<{ x: number; y: number } | null>(null);
const pickFrameEl = ref<HTMLImageElement | null>(null);
const videoEl = ref<HTMLVideoElement | null>(null);
const heldCanvasEl = ref<HTMLCanvasElement | null>(null);
const heldFrame = shallowRef<BrowserHeldFrame>();
const mediaTransition = ref(false);
const layoutTransition = ref(false);
let activeLayoutId: string | undefined;
let viewportHandoff: {
  transitionId: string; dock: HTMLElement; binding: BrowserPresentationBinding;
  viewport: { width: number; height: number }; ready: boolean; finished: boolean; resizeAllowed: boolean;
} | undefined;
let heldFade: Animation | undefined;
const pixelBridge = shallowRef<{ transitionId: string; source: BrowserPixelRect; sourcePage: BrowserPixelRect; target?: BrowserPixelRect; duration: number; phase: "captured" | "moving" | "waiting" | "fading" | "live-moving" }>();
let pixelBridgeAnimation: Animation | undefined;
let pixelPageAnimation: Animation | undefined;
let pixelPage: HTMLElement | undefined;
let pixelPageOrigin = "";
let pixelVideoAnimation: Animation | undefined;
let pixelVideo: HTMLVideoElement | undefined;
let suppressedPixelVideo: { video: HTMLVideoElement; opacity: string } | undefined;
let pixelBridgeFrames: Keyframe[] = [];
const presentedVideo = shallowRef<BrowserPresentedVideo>();
const frameEl = computed(() => picking.value ? pickFrameEl.value : videoEl.value);
const mediaFrame = shallowRef<BrowserLiveFrame>();
let mediaStream: MediaStream | undefined;
let presentationCallback: number | undefined;
const pageEl = ref<HTMLElement | null>(null);
const initialAddress = props.initialUrl || props.initialState.url || "";
const address = ref(initialAddress === "about:blank" ? "" : initialAddress);
const editingAddress = ref(false);
const addressBeforeEdit = ref(address.value);
const addressComposing = ref(false);
const inputEl = ref<HTMLTextAreaElement | null>(null);
const inputPosition = ref({ x: 8, y: 8 });
const liveReady = ref(false);
const viewVisibilitySupported = ref(false);
const viewResumePending = ref(false);
const inputDraining = ref(false);
const mediaConnected = ref(true);
const diagnosticsOpen = ref(false);
// Pane instances are keyed by session/context/tab and survive hidden tabs.
// This preference is local to that Page, never persisted or shared by origin.
const codecPreference = ref<BrowserCodecPreference>("standard");
const responsiveSupported = ref(false);
const browserRuntime = ref<"wayland">();
const renderScaleLimit = ref<number>();
const composingInput = ref(false);
const codecSwitch = ref<"idle" | "draining" | "connecting">("idle");
const browserMenuEl = ref<HTMLDetailsElement | null>(null);
let codecSwitchAbort: AbortController | undefined;
const canChangeCodec = computed(() => !props.closing && codecSwitch.value === "idle" && !composingInput.value && !inputDraining.value && !viewResumePending.value && visible.value && liveReady.value && !busy.value && !picking.value && !pageLoading.value && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked);
const diagnostics = shallowRef<BrowserPerformance | null>(null);
const takeoverMs = ref<number>();
let takeoverStarted: number | undefined;
let diagnosticsTimer: ReturnType<typeof setTimeout> | undefined;
let diagnosticsEpoch = 0;
const formatMetric = (value: number | undefined, suffix = "ms", digits = 0) => value === undefined || !Number.isFinite(value) ? "—" : `${value.toFixed(digits)} ${suffix}`;
const viewportReady = ref(false);
const textCommit = new LiveTextCommit();
const promptText = ref("");
const picking = ref(false);
const pickLoading = ref(false);
const pickError = ref("");
const picked = ref<BrowserPickPreview | null>(null);
const pickPending = ref(false);
const pickerButton = ref<HTMLButtonElement | null>(null);
let pickRecoveryTimer: ReturnType<typeof setTimeout> | undefined;
let pickRecoveryEpoch = 0;
let faviconCommitEpoch = 0;
const faviconCommitting = ref(false);
let pickRecoveryFlight: { epoch: number; controller: AbortController } | undefined;
let pickRecoveryAttempts = 0;
let pickPointer: { x: number; y: number } | undefined;
const pickCommitting = ref(false);
const bookmarksManager = ref<InstanceType<typeof BrowserBookmarks>>();
const pendingBookmark = ref<{ url: string; fromURL: string; fromDocument: string; fromGeneration: number | undefined; sessionId: string; target: NativeBrowserTarget } | null>(null);
const bookmarkStore = useAccountBrowserBookmarks();
const bookmarks = bookmarkStore.bookmarks;
watch(bookmarks, () => cancelBookmarkSelection());
const pageVisible = ref(!document.hidden);
const visible = computed(() => props.active && pageVisible.value && !recovering.value);
const owned = computed(() => ownsNativeControl(state.value, lease.value, leaseEpoch.value));
const controlReady = computed(() => !props.closing && visible.value && !viewResumePending.value && !inputDraining.value && owned.value && connected.value && !busy.value && !picking.value);
const documentUnavailable = computed(() => nativeDocumentUnavailable(state.value));
const writable = computed(() => !displayRecovering.value && !layoutTransition.value && mediaConnected.value && !mediaTransition.value && !heldFrameVisible.value && !documentUnavailable.value && controlReady.value && !pageLoading.value && frameReady.value && liveReady.value && viewportReady.value && !frameIssue.value && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked);
const canPick = computed(() => !props.closing && !displayRecovering.value && !layoutTransition.value && !mediaTransition.value && !heldFrameVisible.value && !documentUnavailable.value && visible.value && connected.value && !pageLoading.value && frameReady.value && !busy.value && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked);
const canNavigate = computed(() => controlReady.value && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked);
const canGoBack = computed(() => canNavigate.value && !pageLoading.value && state.value.can_go_back === true);
const addressChanged = computed(() => editingAddress.value && address.value.trim() !== addressBeforeEdit.value.trim());
const controlLabel = computed(() => t(`nativeBrowser.${owned.value ? "human" : state.value.control === "human" ? "other" : "agent"}`));
const actionFailed = computed(() => !!displayError.value || !!actionError.value && actionError.value !== liveFailureMessage.value);
// A healthy retained receiver resumes its presentation authority, not the web
// document. Do not show a navigation spinner for this ordinary Sheet switch.
const healthyWarmResume = computed(() => viewResumePending.value && viewVisibilitySupported.value && liveReady.value && connected.value && mediaConnected.value &&
  !!mediaFrame.value && liveFrameMatches(mediaFrame.value, state.value, props.target) && presentedVideo.value?.video === videoEl.value &&
  presentedVideo.value?.media_epoch === mediaFrame.value.media_epoch && presentedVideo.value?.frame_id === mediaFrame.value.frame_id);
const pageLoading = computed(() => nativePageLoading(state.value, (initialFramePending.value || displayRecovering.value && !displayError.value || mediaTransition.value && !healthyWarmResume.value) && frameIssue.value !== "unavailable" && !actionFailed.value, navigationPending.value && !actionFailed.value, !!error.value));
watch(pageLoading, loading => {
  clearTimeout(loadingTimer);
  slowLoading.value = false;
  if (loading) loadingTimer = setTimeout(() => { slowLoading.value = true; }, 4000);
}, { immediate: true });
const showNewTab = computed(() => connected.value && !error.value && !frameIssue.value && !pageLoading.value && nativeNewTabPage(state.value));
const heldFrameVisible = computed(() => visible.value && !picking.value && state.value.url !== "about:blank" && browserHeldFrameMatches(heldFrame.value, presentationBinding()));
const livePresentation = computed(() => heldFrameVisible.value ? "held" : picking.value ? "reference" : mediaFrame.value && presentedVideo.value?.video === videoEl.value ? "live" : "pending");
const connectionLabel = computed(() => `${controlLabel.value} · ${error.value ? t("nativeBrowser.offline") : connected.value ? t("nativeBrowser.connected") : t("nativeBrowser.connecting")}`);
const showActivity = computed(() => !error.value && !actionError.value && !displayError.value && (!!state.value.dialog || state.value.debugger_paused || state.value.devtools_blocked || activity.value?.phase === "failed" || (!pageLoading.value && (state.value.busy || activity.value?.phase === "started"))));
const activity = computed(() => { const action = state.value.activity?.last_action; return action?.tab_id === nativeViewedTab(state.value) ? action : null; });
const actionNames = new Set(["snapshot", "find", "click", "type", "text", "navigate", "back", "wait", "extract", "scroll", "select", "press", "key", "hover", "drag", "upload", "screenshot", "tabs", "tab", "dialog", "devtools"]);
const activityText = computed(() => {
  if (state.value.dialog) return t("nativeBrowser.waitingDialog");
  if (state.value.devtools_blocked) return t("nativeBrowser.debugBlocked");
  if (state.value.debugger_paused) return t("nativeBrowser.debugPaused");
  const action = activity.value;
  if (state.value.busy && action?.phase !== "started") return t("nativeBrowser.operationPending");
  if (!action) return t("nativeBrowser.ready");
  const name = action.action === "input_text" ? "text" : action.action === "input_scroll" ? "scroll" : action.action === "input_back" ? "back" : action.action === "upload_file" ? "upload" : action.action;
  return `${t(`nativeBrowser.actor.${action.actor === "human" ? "human" : "agent"}`)} · ${t(`nativeBrowser.action.${actionNames.has(name) ? name : "other"}`)} · ${t(`nativeBrowser.phase.${["started", "succeeded", "failed"].includes(action.phase) ? action.phase : "started"}`)}`;
});
const overlay = computed(() => {
  const marks = liveOverlay.value;
  if (state.value.control !== "agent" || picking.value || documentUnavailable.value || !visible.value || !connected.value || !mediaConnected.value ||
      layoutTransition.value || mediaTransition.value || heldFrameVisible.value || !frameReady.value || !browserAgentOverlayMatches(marks, mediaFrame.value, state.value, props.target)) return null;
  return marks.overlay;
});
const agentGeometry = computed(() => browserAgentOverlayGeometry(overlay.value ? liveOverlay.value : null, canvasSize.value));
const pointer = computed(() => agentGeometry.value.pointer);
const targetRect = computed(() => agentGeometry.value.target);
const focusRect = computed(() => agentGeometry.value.focus);
const pickRect = computed(() => picking.value && canPick.value && picked.value?.element && pickPreviewMatches(picked.value, pickerFrame()) ? nativeDisplayRect(picked.value.element.rect, canvasSize.value, frameViewport.value) : null);
const pickLabel = computed(() => picked.value?.element ? [picked.value.element.role || picked.value.element.tag].filter(Boolean).join(" · ").slice(0, 100) : "");
const rectStyle = (rect: NativeBrowserRect) => ({ left: `${rect.x}px`, top: `${rect.y}px`, width: `${rect.width}px`, height: `${rect.height}px` });
const pointerStyle = (point: { x: number; y: number }) => ({ transform: `translate3d(${point.x}px, ${point.y}px, 0)` });
let disposed = false;
let lifecycleEpoch = 0;
let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
let live: ReturnType<typeof createBrowserLive> | undefined;
let liveConnecting: Promise<void> | undefined;
let liveAbort: AbortController | undefined;
let liveEpoch = 0;
let liveConnectionTimer: ReturnType<typeof setTimeout> | undefined;
let frameDecodeEpoch = 0;
let lastLiveFrame: BrowserLiveFrame | undefined;
let viewportTimer: ReturnType<typeof setTimeout> | undefined;
let selectionRefreshTimer: ReturnType<typeof setTimeout> | undefined;
let requestedViewport = "";
let requestedViewportSeq: number | undefined;
let requestedViewportFrame: BrowserLiveFrame | undefined;
let viewportRetryAt = 0;
let displayRecoveryTimer: ReturnType<typeof setTimeout> | undefined;
let displayRecoveryRead: Promise<void> | undefined;
let displayRecovery: {
  sessionId: string; context: string; tab: string; generation: number | undefined; controlEpoch: number | undefined;
  previousMediaEpoch?: string; previousDocument: string; viewport?: { width: number; height: number };
  message: string; deadline: number; failures: number; observations: number;
} | undefined;
let pointerId: number | undefined;
let pointerButtons = 0;
let gestureClickCount = 1;
let lastClick = { at: 0, x: 0, y: 0, button: -1, count: 0 };
let lastPointer = { x: 0, y: 0 };
const pressedKeys = new Set<string>();
let readController: AbortController | undefined;
let readEpoch = 0;
let observationSequence = 0;
let reportedTitle = "";
let reportedURL = "";
let resizeObserver: ResizeObserver | undefined;
let inputReleaseAbort: AbortController | undefined;
let inputReleaseFlight: Promise<boolean> | undefined;
let visibilityFlight: Promise<void> | undefined;
const warmView = registerBrowserWarmView(props.sessionId, {}, { evict: () => {
  if (!visible.value) { closeLive(); connected.value = false; frameReady.value = false; }
} });
let rippleTimer: ReturnType<typeof setTimeout> | undefined;
let overlayTimer: ReturnType<typeof setTimeout> | undefined;
const playedClicks = new Set<string>();

function clearOverlay() {
  liveOverlay.value = null;
  ripple.value = "";
  ripplePoint.value = null;
  clearTimeout(rippleTimer);
  clearTimeout(overlayTimer);
}

function acceptLiveOverlay(marks: BrowserAgentOverlay | null) {
  if (!marks?.overlay?.last_action || !visible.value || picking.value || state.value.control !== "agent" || !browserAgentOverlayMatches(marks, mediaFrame.value, state.value, props.target)) { clearOverlay(); return; }
  liveOverlay.value = marks;
  clearTimeout(overlayTimer);
  const action = marks.overlay.last_action;
  // A missing/stopped producer cannot leave a permanent ghost pointer.
  const remaining = action.phase === "started" ? AGENT_PENDING_MARK_MS : Math.max(0, AGENT_MARK_HOLD_MS - (marks.overlay.sampled_at - action.updated_at));
  overlayTimer = setTimeout(clearOverlay, remaining);
  if (!marks.overlay || !nativeConfirmedClick(marks.overlay)) return;
  const click = marks.overlay.pointer!;
  const key = `${marks.context_ref}:${marks.tab_id}:${marks.generation}:${marks.control_epoch}:${marks.frame_identity}:${click.seq}:${click.action_seq}`;
  if (playedClicks.has(key)) return;
  playedClicks.add(key);
  if (playedClicks.size > 64) playedClicks.delete(playedClicks.values().next().value!);
  ripplePoint.value = { x: click.x, y: click.y };
  ripple.value = key;
  clearTimeout(rippleTimer);
  rippleTimer = setTimeout(() => { ripple.value = ""; ripplePoint.value = null; }, 420);
}
const displayRipple = computed(() => overlay.value && ripplePoint.value && liveOverlay.value ? nativeDisplayPoint(ripplePoint.value, canvasSize.value, liveOverlay.value.viewport) : null);

function frameLoaded(event: Event) {
  if (props.closing || (event.target as HTMLImageElement).src !== frameURL.value || !visible.value || frameIssue.value || !nativeFrameMatches(frameIdentity.value, state.value, state.value)) return;
  frameReady.value = true;
  if (picking.value && pickPointer) queuePick(pickPointer.x, pickPointer.y);
}

function measureCanvas(immediate = false) {
  const rect = pageEl.value && browserLayoutSize(pageEl.value);
  if (rect && (rect.width !== canvasSize.value.width || rect.height !== canvasSize.value.height)) {
    clearOverlay();
    canvasSize.value = { width: rect.width, height: rect.height };
  }
  queueViewport(immediate);
}
function resetImmersiveLayout() {
  activeLayoutId = undefined;
  viewportHandoff = undefined;
  layoutTransition.value = false;
  clearPixelBridge();
}
function immersiveLayout(event: Event) {
  if (props.closing) return;
  const element = pageEl.value;
  if (!element || !(event.target instanceof Element) || !event.target.contains(element)) return;
  const detail = (event as CustomEvent<ImmersiveLayoutDetail>).detail;
  if (!detail || !acceptsImmersivePaneLayout(visible.value, activeLayoutId, detail)) return;
  if (detail.phase === "start") {
    activeLayoutId = detail.transitionId;
    layoutTransition.value = detail.animated;
    viewportHandoff = undefined;
    clearOverlay();
    capturePixelBridge(detail);
  } else if (detail.phase === "commit") {
    if (activeLayoutId !== detail.transitionId) return;
    // Measure final CSS layout even while its screen rectangle is FLIP-scaled.
    measureCanvas(true);
    const resizeAllowed = !(state.value.control === "human" && !owned.value) && liveReady.value && connected.value && mediaConnected.value && !documentUnavailable.value;
    const viewport = resizeAllowed ? desiredViewport() : state.value.viewport;
    const binding = presentationBinding(), dock = event.target as HTMLElement;
    viewportHandoff = detail.animated && viewport && binding ? { transitionId: detail.transitionId, dock, binding, viewport,
      ready: false, finished: false, resizeAllowed } : undefined;
    if (!detail.animated) return;
    if (viewport) commitPixelBridge(detail, viewport);
    if (showNewTab.value) {
      viewportHandoff = undefined;
      notifyImmersivePresented(dock, { transitionId: detail.transitionId, status: "ready", reason: "local-new-tab" });
    } else if (!liveReady.value || !connected.value || !mediaConnected.value || documentUnavailable.value || !viewportHandoff) {
      notifyImmersivePresented(dock, { transitionId: detail.transitionId, status: "unavailable", reason: "unavailable" });
    } else if (lastLiveFrame && presentedVideo.value?.video === videoEl.value) {
      // An already-presented frame is sufficient when this layout keeps the
      // same viewport; do not wait for a media replacement that will not occur.
      revealPresentedFrame(lastLiveFrame);
    }
  } else if (detail.phase === "finish") {
    if (activeLayoutId !== detail.transitionId) return;
    activeLayoutId = undefined;
    layoutTransition.value = false;
    // Interruption is local geometry cleanup even if no live handoff could
    // be created (for example a disconnected peer retaining old pixels).
    if (detail.reason === "cancelled") cancelPixelBridgeGeometry();
    if (viewportHandoff?.transitionId === detail.transitionId) {
      viewportHandoff.finished = true;
      if (viewportHandoff.ready && lastLiveFrame) revealPresentedFrame(lastLiveFrame);
    }
    if (!heldFrame.value) clearPixelBridge();
    else if (pixelBridge.value?.transitionId === detail.transitionId && pixelBridge.value.phase !== "fading")
      pixelBridge.value = { ...pixelBridge.value, phase: "waiting" };
    measureCanvas(true);
  }
}
function cancelPixelBridgeGeometry() {
  cancelBrowserPixelGeometry([pixelBridgeAnimation, pixelVideoAnimation, pixelPageAnimation], pixelPage, pixelPageOrigin);
  pixelBridgeAnimation = undefined; pixelVideoAnimation = undefined; pixelPageAnimation = undefined; pixelBridgeFrames = [];
  pixelPage = undefined;
}
function restorePixelVideo() {
  if (!suppressedPixelVideo) return;
  suppressedPixelVideo.video.style.opacity = suppressedPixelVideo.opacity;
  delete suppressedPixelVideo.video.dataset.bridgeSuppressed;
  suppressedPixelVideo = undefined;
}
function suppressPixelVideo(video: HTMLVideoElement) {
  if (suppressedPixelVideo?.video === video) return;
  restorePixelVideo();
  suppressedPixelVideo = { video, opacity: video.style.opacity };
  // Opacity keeps the decoder and requestVideoFrameCallback active. Hiding
  // the element would risk withholding the matching presentation forever.
  video.style.opacity = "0"; video.dataset.bridgeSuppressed = "true";
}
function clearPixelBridge() {
  cancelPixelBridgeGeometry();
  restorePixelVideo();
  for (const element of [heldCanvasEl.value, pixelVideo]) {
    if (!element) continue;
    for (const property of ["left", "top", "width", "height", "object-fit", "transform-origin"]) element.style.removeProperty(property);
    delete element.dataset.pixelBridge;
  }
  pixelVideo = undefined; pixelBridge.value = undefined;
}
function capturePixelBridge(detail: ImmersiveLayoutDetail) {
  const canvas = heldCanvasEl.value, video = videoEl.value, binding = presentationBinding();
  const page = pageEl.value?.getBoundingClientRect();
  const sourcePage = page && { x: page.x, y: page.y, width: page.width, height: page.height };
  let source: BrowserPixelRect | null = null, retained = false;
  if (detail.animated && canvas && binding && state.value.url !== "about:blank") {
    if (heldFrameVisible.value && !heldFade && browserHeldFrameMatches(heldFrame.value, binding)) {
      source = browserPresentedPixelRect(canvas.getBoundingClientRect(), { width: canvas.clientWidth, height: canvas.clientHeight },
        heldFrame.value!, getComputedStyle(canvas).objectFit === "fill" ? "fill" : "contain");
      retained = !!source;
    } else if (video) {
      source = browserPresentedPixelRect(video.getBoundingClientRect(), { width: video.clientWidth, height: video.clientHeight },
        { width: video.videoWidth, height: video.videoHeight }, getComputedStyle(video).objectFit === "fill" ? "fill" : "contain");
      const captured = source && captureBrowserHeldFrame(presentedVideo.value, video, canvas, binding);
      if (captured) { heldFrame.value = captured; retained = true; }
    }
  }
  // Read the old painted rectangle before removing either compositor pose.
  heldFade?.cancel(); heldFade = undefined;
  clearPixelBridge();
  if (source && sourcePage && retained) {
    pixelBridge.value = { transitionId: detail.transitionId, source, sourcePage, duration: detail.duration, phase: "captured" };
    mediaTransition.value = true;
    if (video) suppressPixelVideo(video);
  }
}
function setPixelBox(element: HTMLElement, rect: BrowserPixelRect) {
  Object.assign(element.style, { left: `${rect.x}px`, top: `${rect.y}px`, width: `${rect.width}px`, height: `${rect.height}px`, objectFit: "fill", transformOrigin: "0 0" });
  element.dataset.pixelBridge = "true";
}
function commitPixelBridge(detail: ImmersiveLayoutDetail, viewport: { width: number; height: number }) {
  const bridge = pixelBridge.value, canvas = heldCanvasEl.value, page = pageEl.value;
  if (!bridge || bridge.transitionId !== detail.transitionId || !canvas || !page) return;
  const layout = browserLayoutSize(page);
  const pageTransform = browserPixelBridgeTransform(bridge.sourcePage, page.getBoundingClientRect(), layout);
  if (!pageTransform) { clearPixelBridge(); return; }
  // The dock's rail and toolbar reflow immediately. Compensate the actual
  // page box too, so its clipping boundary starts at the old painted page,
  // rather than exposing a wider empty canvas before the dock has moved.
  pixelPage = page; pixelPageOrigin = page.style.transformOrigin;
  page.style.transformOrigin = "0 0"; page.dataset.pixelPageBridge = "true";
  try {
    pixelPageAnimation = page.animate([{ transform: pageTransform }, { transform: "none" }],
      { duration: detail.duration, easing: "cubic-bezier(.22, 1, .36, 1)", fill: "both" });
  } catch { /* The frame bridge can still preserve the source pixels. */ }
  const target = browserPresentedPixelRect({ x: 0, y: 0, ...layout }, layout, viewport);
  if (!target) { clearPixelBridge(); return; }
  // Target exactly the future video's normal contain rectangle. A decoded
  // replacement can crossfade without changing the painted box a second time.
  setPixelBox(canvas, target);
  const transform = browserPixelBridgeTransform(bridge.source, canvas.getBoundingClientRect(), { width: target.width, height: target.height });
  if (!transform) { clearPixelBridge(); return; }
  pixelBridge.value = { ...bridge, target, phase: "moving" };
  pixelBridgeFrames = [{ transform }, { transform: "none" }];
  try {
    pixelBridgeAnimation = canvas.animate(pixelBridgeFrames, { duration: detail.duration, easing: "cubic-bezier(.22, 1, .36, 1)", fill: "both" });
  } catch { /* The retained pixels remain usable without compositor animation. */ }
}
function alignPresentedPixelBridge(video: HTMLVideoElement) {
  const bridge = pixelBridge.value;
  if (!bridge?.target || pixelVideo === video) return;
  pixelVideoAnimation?.cancel();
  pixelVideo = video; setPixelBox(video, bridge.target);
  const source = pixelBridgeAnimation;
  if (!source || source.playState === "finished" || !layoutTransition.value) return;
  try {
    pixelVideoAnimation = video.animate(pixelBridgeFrames, { duration: bridge.duration, easing: "cubic-bezier(.22, 1, .36, 1)", fill: "both" });
    if (source.startTime !== null) pixelVideoAnimation.startTime = source.startTime;
    else if (source.currentTime !== null) pixelVideoAnimation.currentTime = source.currentTime;
  } catch { /* A later normal contain frame uses the same final target box. */ }
}

let densityQuery: MediaQueryList | undefined;
function observeScreenDensity() {
  densityQuery?.removeEventListener("change", screenDensityChanged);
  if (typeof window.matchMedia === "function") {
    densityQuery = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`);
    densityQuery.addEventListener("change", screenDensityChanged);
  }
}
function screenDensityChanged() { observeScreenDensity(); measureCanvas(); }

function discardFrame() {
  viewportHandoff = undefined;
  clearHeldFrame();
  frameDecodeEpoch++;
  if (frameURL.value) URL.revokeObjectURL(frameURL.value);
  frameURL.value = "";
  frameVersion.value = "";
  frameIdentity.value = "";
  frameReady.value = false;
  lastLiveFrame = undefined;
  clearOverlay();
}

function applyState(next: NativeBrowserState) {
  if (props.closing) return;
  const previous = state.value;
  if (next.context_ref === previous.context_ref && typeof next.generation === "number" && typeof previous.generation === "number" && (next.generation < previous.generation || next.generation === previous.generation && (Number(next.control_epoch) < Number(previous.control_epoch) || Number(next.document_version) < Number(previous.document_version)))) return;
  if (displayRecovery && (next.context_ref !== displayRecovery.context || nativeViewedTab(next) !== displayRecovery.tab || next.generation !== displayRecovery.generation || next.control_epoch !== displayRecovery.controlEpoch)) clearDisplayRecovery();
  if (next.context_ref !== previous.context_ref || nativeViewedTab(next) !== nativeViewedTab(previous) || next.generation !== previous.generation) resetImmersiveLayout();
  if (next.url === "about:blank" || next.context_ref !== previous.context_ref || nativeViewedTab(next) !== nativeViewedTab(previous) || next.generation !== previous.generation) clearHeldFrame();
  const previousURL = state.value.url;
  const nextAction = next.activity?.last_action;
  if (nativeViewedTab(next) !== nativeViewedTab(previous) || next.generation !== previous.generation) {
    discardFrame();
    initialFramePending.value = true;
  }
  if (next.page_load_error || next.frame_identity !== previous.frame_identity || nativeViewedTab(next) !== nativeViewedTab(previous) || next.url !== previous.url) navigationPending.value = false;
  if (nativeViewedTab(next) !== nativeViewedTab(previous) || next.generation !== previous.generation || next.frame_identity !== previous.frame_identity || (picking.value && next.document_version !== previous.document_version) || next.control_epoch !== previous.control_epoch || next.viewport.width !== previous.viewport.width || next.viewport.height !== previous.viewport.height || (picking.value && frameVersion.value && next.version && next.version !== frameVersion.value)) {
    invalidatePick(true);
    clearOverlay();
    frameReady.value = false;
    frameIdentity.value = "";
    if (picking.value) { clearTimeout(selectionRefreshTimer); selectionRefreshTimer = setTimeout(() => { if (picking.value && !disposed && visible.value) void refresh(); }, 200); }
  } else if (next.control !== "agent" || next.document_version !== previous.document_version || nextAction && nextAction.seq !== previous.activity?.last_action?.seq) clearOverlay();
  if (pendingBookmark.value && (next.generation !== pendingBookmark.value.fromGeneration || next.url !== pendingBookmark.value.fromURL || nativeDocumentStamp(next) !== pendingBookmark.value.fromDocument || !!next.page_load_error)) cancelBookmarkSelection();
  // Video frames carry page state; preserve host download delivery metadata
  // until another host snapshot arrives for this same context.
  if (next.context_ref === previous.context_ref && next.downloads === undefined) next = {
    ...next, downloads: previous.downloads,
    downloads_rejected_count: previous.downloads_rejected_count,
    downloads_last_rejection: previous.downloads_last_rejection,
    downloads_max_file_bytes: previous.downloads_max_file_bytes,
  };
  state.value = next;
  observeBrowserControl(props.sessionId, next);
  emit("state", next);
  if (next.busy || next.dialog || nativeDocumentUnavailable(next)) invalidatePick(true);
  if (nativeDocumentUnavailable(next)) clearOverlay();
  if (next.dialog || next.debugger_paused || next.devtools_blocked) frameReady.value = false;
  if (next.reset) { resetNotice.value = next.reset_reason ?? ""; }
  if (picker.pending && next.pick_pending === false) schedulePickRecovery(0);

  if (!editingAddress.value && !navigationPending.value && next.url) {
    if (next.url !== "about:blank") address.value = next.url;
    else if (previousURL !== "about:blank") address.value = "";
  }
  const title = next.url === "about:blank" ? t("nativeBrowser.newTabPageTitle") : next.title;
  if (title && title !== reportedTitle) { reportedTitle = title; emit("title", title); }
  if (next.url && next.url !== "about:blank" && next.url !== reportedURL) { reportedURL = next.url; emit("navigate", next.url); }
  recoverPresentedDisplay();
}

function stopRead() {
  readEpoch++;

  readController?.abort();
  readController = undefined;
}

function authority(): BrowserLiveAuthority | null {
  const frame = lastLiveFrame;
  if (!owned.value || !frame || !liveFrameMatches(frame, state.value, props.target)) return null;
  return { generation: frame.generation, control_epoch: frame.control_epoch, lease_token: lease.value, frame_id: frame.frame_id, media_epoch: frame.media_epoch };
}
function sendLive(event: BrowserLiveEvent): boolean {
  if (props.closing || recovering.value) return false;
  const binding = authority();
  return !!binding && !!live?.input(event, binding);
}
function releaseInput() {
  inputReleaseAbort?.abort(); inputReleaseAbort = undefined; inputReleaseFlight = undefined; inputDraining.value = false;
  const remotePressed = live?.clearPendingInput();
  if (remotePressed || pointerId !== undefined || pressedKeys.size) sendLive({ type: "reset" });
  releaseLocalInput();
  queueViewport();
}
function releaseLocalInput() {
  const capturedPointer = pointerId;
  pointerId = undefined; pointerButtons = 0; pressedKeys.clear();
  textCommit.reset(); composingInput.value = false;
  if (inputEl.value) inputEl.value.value = "";
  if (capturedPointer !== undefined && frameEl.value?.hasPointerCapture(capturedPointer)) frameEl.value.releasePointerCapture(capturedPointer);
}
function releaseInputReliably(): Promise<boolean> {
  if (props.closing) { releaseLocalInput(); return Promise.resolve(false); }
  if (inputReleaseFlight) return inputReleaseFlight;
  const connection = live, binding = authority();
  releaseLocalInput();
  if (!connection) return Promise.resolve(true);
  const controller = new AbortController(); inputReleaseAbort = controller; inputDraining.value = true;
  // Bound a stalled drain. Closing its worker connection releases held input;
  // accepted actions are never replayed after an unknown outcome.
  const timeout = setTimeout(() => controller.abort(), 30000);
  const flight = (async () => {
    try {
      if (!await connection.waitForAcceptedInput(controller.signal) || connection !== live || controller.signal.aborted || props.closing) return false;
      if (binding && !connection.releaseHeldInput(binding)) return false;
      return await connection.waitForInputIdle(controller.signal);
    } finally {
      clearTimeout(timeout);
      if (inputReleaseAbort === controller) { inputReleaseAbort = undefined; inputDraining.value = false; }
    }
  })();
  inputReleaseFlight = flight;
  void flight.finally(() => { if (inputReleaseFlight === flight) { inputReleaseFlight = undefined; queueViewport(); } });
  return flight;
}
function cancelCodecSwitch() { codecSwitchAbort?.abort(); codecSwitchAbort = undefined; codecSwitch.value = "idle"; }
function closeLive(preserveCodecSwitch = false, localOnly = false) {
  warmView.show();
  visibilityFlight = undefined;
  if (!preserveCodecSwitch) cancelCodecSwitch();
  if (localOnly) { live?.clearPendingInput(); releaseLocalInput(); } else releaseInput();
  liveEpoch++;
  clearTimeout(reconnectTimer); clearTimeout(viewportTimer); viewportTimer = undefined; clearTimeout(selectionRefreshTimer); clearTimeout(liveConnectionTimer);
  clearTimeout(displayRecoveryTimer); displayRecoveryTimer = undefined;
  liveAbort?.abort(); liveAbort = undefined;
  live?.close(); live = undefined; liveConnecting = undefined;
  clearMedia();
  liveReady.value = false; viewportReady.value = false; lastLiveFrame = undefined; requestedViewport = ""; requestedViewportSeq = undefined; requestedViewportFrame = undefined; viewportRetryAt = 0;
  renderScaleLimit.value = undefined;
  mediaConnected.value = false; diagnostics.value = null;
  viewVisibilitySupported.value = false; viewResumePending.value = false;
  if (!props.closing && !disposed && visible.value) scheduleDisplayRecovery();
}
function recoverIdentity(cause?: unknown) {
  if (props.closing || disposed || recovering.value || cause !== undefined && !browserRecoveryReason(cause)) return false;
  // Revoke local input before closing sockets. In particular, do not send a
  // reset, return a lease or replay a picker/input command to a retired Page.
  recovering.value = true;
  clearDisplayRecovery();
  cancelBookmarkSelection(); stopPicking(); picker.close(); stopRead(); closeLive();
  clearBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, lease.value, leaseEpoch.value);
  connected.value = false; frameReady.value = false; initialFramePending.value = false;
  error.value = ""; actionError.value = ""; frameIssue.value = ""; frameError.value = "";
  discardFrame();
  emit("recover");
  return true;
}
function desiredViewport() {
  return desktopBrowserViewport(canvasSize.value, browserRuntime.value === "wayland" ? 8 : 1);
}
function desiredCapture(viewport: { width: number; height: number }) {
  return browserRuntime.value === "wayland" && renderScaleLimit.value !== undefined
    ? browserCaptureResolution(canvasSize.value, viewport, window.devicePixelRatio || 1, renderScaleLimit.value) : null;
}
function clearDisplayRecovery() {
  clearTimeout(displayRecoveryTimer); displayRecoveryTimer = undefined;
  displayRecovery = undefined; displayRecovering.value = false; displayError.value = "";
}
function currentDisplayRecovery() {
  const recovery = displayRecovery;
  return recovery && recovery.sessionId === props.sessionId && recovery.context === props.target.context_ref && recovery.tab === props.target.tab_id &&
    targetMatches(state.value, props.target) && recovery.generation === state.value.generation && recovery.controlEpoch === state.value.control_epoch ? recovery : undefined;
}
function scheduleDisplayRecovery() {
  if (displayRecoveryTimer !== undefined || props.closing || disposed || !visible.value) return;
  const recovery = currentDisplayRecovery();
  if (!recovery) return;
  displayRecoveryTimer = setTimeout(() => {
    displayRecoveryTimer = undefined;
    if (props.closing || disposed || !visible.value || currentDisplayRecovery() !== recovery) return;
    if (Date.now() >= recovery.deadline) {
      // A persistent display failure remains visible. It can only be retired
      // by verified pixels, a changed Page authority, or an explicit refresh.
      displayError.value = recovery.message;
      return;
    }
    queueViewport();
    // Retry observation only, with one outstanding read and a fixed budget.
    // No click, key, text, navigation or form submission is retained here.
    if (recovery.observations < 8 && !displayRecoveryRead && !liveConnecting && !busy.value && !picking.value) {
      recovery.observations++;
      const reading = refresh(); displayRecoveryRead = reading;
      void reading.finally(() => { if (displayRecoveryRead === reading) displayRecoveryRead = undefined; });
    }
    scheduleDisplayRecovery();
  }, Math.max(0, Math.min(500, recovery.deadline - Date.now())));
}
function recoverDisplayNavigation(failure: Error & { code?: string; seq?: number; requestKind?: "input" | "display" }): boolean {
  if (failure.requestKind === "input") return false;
  const viewportFailure = failure.seq !== undefined && failure.seq === requestedViewportSeq;
  const classified = failure.code === "LIVE_NAVIGATION_TRANSITION";
  const legacyViewport = (viewportFailure || failure.requestKind === "display") && /Execution context was destroyed|Cannot find context with specified id/i.test(failure.message);
  if (!classified && !legacyViewport) return false;
  // A late error for an older display request must not invalidate a newer
  // receiver. Only the worker's unsequenced media event is global to this view.
  if (failure.seq !== undefined && !viewportFailure) return true;
  const previous = viewportFailure ? requestedViewportFrame : mediaFrame.value ?? lastLiveFrame;
  let recovery = currentDisplayRecovery();
  if (!recovery) {
    recovery = displayRecovery = {
      sessionId: props.sessionId, context: props.target.context_ref, tab: props.target.tab_id,
      generation: state.value.generation, controlEpoch: state.value.control_epoch,
      previousMediaEpoch: previous?.media_epoch, previousDocument: previous?.frame_identity ?? state.value.frame_identity ?? "",
      ...(viewportFailure ? { viewport: desiredViewport() ?? undefined } : {}),
      message: failure.message, deadline: Date.now() + 8000, failures: 0, observations: 0,
    };
  }
  recovery.message = failure.message;
  recovery.failures++;
  displayRecovering.value = true;
  frameReady.value = false; viewportReady.value = false;
  stopPicking(); invalidatePick(true); clearOverlay(); releaseInput();
  if (viewportFailure && recovery.failures < 6 && Date.now() < recovery.deadline) {
    requestedViewport = ""; requestedViewportSeq = undefined; requestedViewportFrame = undefined;
    viewportRetryAt = Date.now() + 500;
    clearTimeout(viewportTimer); viewportTimer = undefined;
  }
  observeMediaPresentation(); scheduleDisplayRecovery();
  return true;
}
function liveRecovered(frame?: BrowserLiveFrame) {
  const recovery = currentDisplayRecovery();
  if (recovery && !state.value.page_loading && !nativeDocumentUnavailable(state.value) && !state.value.busy && !state.value.dialog && !state.value.debugger_paused && !state.value.devtools_blocked) {
    // State polls and old healthy video cannot prove recovery. Require an
    // actually presented current-document receiver, including the requested
    // viewport/capture geometry when the failed task was a display resize.
    const presented = frame && liveFrameMatches(frame, state.value, props.target) &&
      (!recovery.previousMediaEpoch || frame.media_epoch !== recovery.previousMediaEpoch || frame.frame_identity !== recovery.previousDocument) &&
      (!recovery.viewport || frame.viewport.width === recovery.viewport.width && frame.viewport.height === recovery.viewport.height && browserCaptureMatches(frame, desiredCapture(recovery.viewport)));
    if (presented || !frame && state.value.url === "about:blank") clearDisplayRecovery();
  }
  if (liveFailureMessage.value && actionError.value === liveFailureMessage.value) actionError.value = "";
  liveFailureMessage.value = "";
}
function recoverPresentedDisplay() {
  const frame = mediaFrame.value, presented = presentedVideo.value, binding = presentationBinding();
  if (!displayRecovery || props.closing || disposed || !visible.value || !mediaConnected.value || !frameReady.value ||
      !frame || !presented || !binding || lastLiveFrame !== frame || presented.video !== videoEl.value ||
      presented.media_epoch !== frame.media_epoch || presented.frame_id !== frame.frame_id ||
      presented.binding.sessionId !== binding.sessionId || presented.binding.context_ref !== binding.context_ref ||
      presented.binding.tab_id !== binding.tab_id || presented.binding.generation !== binding.generation ||
      !liveFrameMatches(frame, state.value, props.target)) return;
  // A static page may have painted its verified new receiver while still
  // loading/busy. A later idle state can reuse that presentation proof, but
  // never authorize an unpresented offer or an earlier document's pixels.
  liveRecovered(frame);
}
function queueViewport(immediate = false) {
  if (props.closing || !visible.value || picking.value || !liveReady.value || state.value.control === "human" && !owned.value) return;
  if (displayRecovery && (displayRecovery.failures >= 6 || Date.now() >= displayRecovery.deadline)) return;
  const current = desiredViewport();
  if (current && displayRecovery?.viewport) displayRecovery.viewport = current;
  if (current && state.value.viewport.width === current.width && state.value.viewport.height === current.height && browserCaptureMatches(lastLiveFrame, desiredCapture(current)) && liveDisplayAuthority(lastLiveFrame, state.value, props.target, lease.value, leaseEpoch.value)) {
    viewportReady.value = true; clearTimeout(viewportTimer); viewportTimer = undefined; return;
  }
  if (immediate) { clearTimeout(viewportTimer); viewportTimer = undefined; }
  if (viewportTimer !== undefined) return;
  viewportTimer = setTimeout(() => {
    viewportTimer = undefined;
    if (props.closing || !visible.value || picking.value || busy.value || state.value.busy || pointerButtons || pressedKeys.size || textCommit.composing || state.value.dialog || state.value.debugger_paused || state.value.devtools_blocked || state.value.control === "human" && !owned.value) return;
    const desired = desiredViewport();
    if (!desired) return;
    const { width, height } = desired;
    const capture = desiredCapture(desired);
    const size = `${width}:${height}:${capture?.preferredScale ?? "legacy"}`;
    const binding = liveDisplayAuthority(lastLiveFrame, state.value, props.target, lease.value, leaseEpoch.value);
    if (!binding) return;
    if (state.value.viewport.width === width && state.value.viewport.height === height && browserCaptureMatches(lastLiveFrame, capture)) { viewportReady.value = true; return; }
    // Resizing the remote page invalidates its coordinate frame. Do it before
    // first input, or after keyboard focus leaves, never in the middle of an
    // IME commit/typing sequence and never while a pointer is pressed.
    if (viewportReady.value && document.activeElement === inputEl.value) return;
    if (size !== requestedViewport) {
      const seq = live?.viewport(width, height, binding, capture?.preferredScale);
      if (seq) { requestedViewport = size; requestedViewportSeq = seq; requestedViewportFrame = lastLiveFrame; viewportReady.value = false; }
      else if (live?.ready) queueViewport();
    }
  }, Math.max(immediate ? 0 : 160, viewportRetryAt - Date.now()));
}
function presentationBinding(): BrowserPresentationBinding | null {
  if (!targetMatches(state.value, props.target) || !Number.isSafeInteger(state.value.generation) || Number(state.value.generation) < 1) return null;
  return { sessionId: props.sessionId, context_ref: props.target.context_ref, tab_id: props.target.tab_id, generation: state.value.generation! };
}
function clearHeldFrame(preserveBridge = false) {
  heldFade?.cancel(); heldFade = undefined;
  if (!preserveBridge) clearPixelBridge();
  if (!heldFrame.value && !mediaTransition.value) return;
  heldFrame.value = undefined;
  mediaTransition.value = false;
  if (heldCanvasEl.value) { heldCanvasEl.value.width = 0; heldCanvasEl.value.height = 0; }
}
function revealPresentedFrame(frame: BrowserLiveFrame) {
  if (heldFade) return;
  const handoff = viewportHandoff;
  // After an early fade, recurring callbacks still use the live video's
  // synchronized local transform until the one shell animation completes.
  if (!handoff && !heldFrame.value && layoutTransition.value && pixelBridge.value?.phase === "live-moving" && pixelVideo === videoEl.value) return;
  const matches = handoff && visible.value && handoff.dock.isConnected &&
    browserHeldFrameMatches({ binding: handoff.binding, width: 1, height: 1 }, presentationBinding()) &&
    liveFrameMatches(frame, state.value, props.target) && frame.viewport.width === handoff.viewport.width &&
    frame.viewport.height === handoff.viewport.height && (!handoff.resizeAllowed || browserCaptureMatches(frame, desiredCapture(handoff.viewport)));
  if (matches && !handoff.ready) {
    if (videoEl.value) alignPresentedPixelBridge(videoEl.value);
    restorePixelVideo();
    handoff.ready = true;
    notifyImmersivePresented(handoff.dock, { transitionId: handoff.transitionId, status: "ready", reason: "decoded-frame",
      sessionId: handoff.binding.sessionId, context_ref: handoff.binding.context_ref, tab_id: handoff.binding.tab_id,
      generation: handoff.binding.generation, viewport: { ...frame.viewport }, media_epoch: frame.media_epoch, frame_id: frame.frame_id });
  }
  // Intermediate/stale offers cannot replace this transition's retained pixels.
  // Readiness never starts or resumes a shell geometry animation.
  if (handoff && !matches) return;
  if (heldFade) return;
  const smooth = !!matches;
  const canvas = heldCanvasEl.value;
  if (!smooth || !heldFrameVisible.value || !canvas || typeof canvas.animate !== "function"
    || window.matchMedia("(prefers-reduced-motion: reduce)").matches) { clearHeldFrame(); return; }
  // Fade only after the matching real video frame has been decoded and
  // acknowledged. The retained pixels never authorize webpage input.
  if (videoEl.value) alignPresentedPixelBridge(videoEl.value);
  restorePixelVideo();
  mediaTransition.value = false;
  if (pixelBridge.value) pixelBridge.value = { ...pixelBridge.value, phase: "fading" };
  const animation = canvas.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 120, easing: "ease-out", fill: "both" });
  heldFade = animation;
  void animation.finished.then(() => {
    if (heldFade !== animation) return;
    if (viewportHandoff === handoff) viewportHandoff = undefined;
    clearHeldFrame(layoutTransition.value);
    if (pixelBridge.value) pixelBridge.value = { ...pixelBridge.value, phase: "live-moving" };
  }, () => {});
}
function retainPresentedFrame() {
  heldFade?.cancel(); heldFade = undefined;
  const binding = presentationBinding();
  if (disposed || state.value.url === "about:blank" || !binding) return;
  const captured = captureBrowserHeldFrame(presentedVideo.value, videoEl.value, heldCanvasEl.value, binding);
  if (captured) heldFrame.value = captured;
  mediaTransition.value = browserHeldFrameMatches(heldFrame.value, binding);
}
function pauseMediaPresentation() {
  if (presentationCallback !== undefined) videoEl.value?.cancelVideoFrameCallback(presentationCallback);
  presentationCallback = undefined;
  videoEl.value?.pause();
}
function syncSheetVisibility() {
  if (props.closing) return;
  const connection = live;
  if (!connection || !connection.viewVisibilitySupported || disposed || !pageVisible.value) return;
  if (visible.value) { warmView.show(); viewResumePending.value = state.value.url !== "about:blank"; }
  else { retainPresentedFrame(); pauseMediaPresentation(); connection.suspendPresentation(); }
  frameReady.value = false;
  if (visibilityFlight) return;
  const epoch = liveEpoch;
  let submitted: boolean | undefined;
  const flight = (async () => {
    const drained = await releaseInputReliably();
    if (props.closing || disposed || epoch !== liveEpoch || connection !== live || !pageVisible.value) return;
    if (!drained) {
      closeLive(); connected.value = false;
      if (visible.value) void refresh();
      return;
    }
    // Rapid tab changes need only the latest desired state; a show can supersede
    // an unacknowledged hide without waiting for that older network reply.
    const shown = visible.value;
    if (!connection.setViewVisibility(shown)) {
      closeLive(); connected.value = false;
      if (shown) void refresh();
      return;
    }
    submitted = shown;
    if (shown) warmView.show(); else warmView.hide();
  })();
  visibilityFlight = flight;
  void flight.finally(() => {
    if (visibilityFlight !== flight) return;
    visibilityFlight = undefined;
    if (submitted !== undefined && submitted !== visible.value && epoch === liveEpoch && connection === live && pageVisible.value && !disposed) syncSheetVisibility();
  });
}
function clearMedia() {
  if (props.closing && !disposed) return;
  heldFade?.cancel(); heldFade = undefined;
  clearOverlay();
  const video = videoEl.value, binding = presentationBinding();
  // Called before the protocol stops the old track. Preserve only pixels
  // already presented in this exact visible session/context/tab/generation.
  const preserve = !disposed && state.value.url !== "about:blank" && !!binding;
  if (preserve) {
    // The bridge already captured the exact pixels before CSS changed. A
    // retiring peer must not replace them with a differently fitted video.
    if (!pixelBridge.value || !browserHeldFrameMatches(heldFrame.value, binding)) {
      const captured = captureBrowserHeldFrame(presentedVideo.value, video, heldCanvasEl.value, binding);
      if (captured) heldFrame.value = captured;
      else if (!browserHeldFrameMatches(heldFrame.value, binding)) clearHeldFrame();
    }
  } else clearHeldFrame();
  mediaTransition.value = preserve;
  if (presentationCallback !== undefined) video?.cancelVideoFrameCallback(presentationCallback);
  presentationCallback = undefined;
  if (video) { video.pause(); video.srcObject = null; }
  presentedVideo.value = undefined;
  mediaStream = undefined; mediaFrame.value = undefined;
  lastLiveFrame = undefined; frameReady.value = false; viewportReady.value = false;
}
function observeMediaPresentation() {
  const video = videoEl.value, frame = mediaFrame.value;
  if (props.closing || !video || !frame || !mediaStream || disposed || !visible.value) return;
  if (presentationCallback !== undefined) video.cancelVideoFrameCallback(presentationCallback);
  if (typeof video.requestVideoFrameCallback !== "function") {
    frameError.value = t("nativeBrowser.liveVideoUnsupported"); frameIssue.value = "unavailable"; return;
  }
  const presented = (_now: number, metadata: VideoFrameCallbackMetadata) => {
    presentationCallback = undefined;
    if (props.closing || disposed || videoEl.value !== video || mediaFrame.value !== frame || !visible.value) return;
    if (!picking.value && !showNewTab.value && liveFrameMatches(frame, state.value, props.target) && live?.present(frame, metadata.width, metadata.height)) {
      frameViewport.value = frame.viewport; frameIdentity.value = frame.frame_identity;
      frameVersion.value = ""; // Video is not a versioned DOM observation.
      const binding = presentationBinding();
      if (!binding) return;
      if (presentedVideo.value?.video !== video) presentedVideo.value = { binding, video, media_epoch: frame.media_epoch, frame_id: frame.frame_id };
      lastLiveFrame = frame; frameReady.value = true;
      viewResumePending.value = false;
      revealPresentedFrame(frame);
      if (codecSwitch.value === "connecting") codecSwitch.value = "idle";
      const desired = desiredViewport();
      if (desired && frame.viewport.width === desired.width && frame.viewport.height === desired.height && browserCaptureMatches(frame, desiredCapture(desired))) viewportReady.value = true;
      initialFramePending.value = false; frameIssue.value = ""; frameError.value = "";
      liveRecovered(frame);
      queueViewport();
    }
    presentationCallback = video.requestVideoFrameCallback(presented);
  };
  presentationCallback = video.requestVideoFrameCallback(presented);
}
async function receiveMedia(stream: MediaStream, frame: BrowserLiveFrame) {
  if (props.closing || disposed || !liveFrameMatches(frame, state.value, props.target)) return;
  const lifecycle = lifecycleEpoch;
  // A changed document/control/viewport cannot retain an earlier citation
  // selection. Resume visible video so this new epoch can actually present.
  if (picking.value) stopPicking();
  clearMedia(); mediaStream = stream; mediaFrame.value = frame;
  await nextTick();
  const video = videoEl.value;
  if (props.closing || lifecycle !== lifecycleEpoch || !video || disposed || mediaFrame.value !== frame) return;
  if (pixelBridge.value && heldFrameVisible.value) suppressPixelVideo(video);
  video.srcObject = stream;
  if (visible.value && (!viewVisibilitySupported.value || live?.viewAcknowledged)) {
    observeMediaPresentation();
    await video.play();
  }
}
async function connectLive() {
  if (props.closing || live || liveConnecting || disposed || !visible.value) return liveConnecting;
  const epoch = liveEpoch;
  const controller = new AbortController(); liveAbort = controller;
  liveConnectionTimer = setTimeout(() => controller.abort(), 10000);
  const opening = (async () => {
    try {
      const { ticket } = await api.browserLiveTicket(props.sessionId, props.target, controller.signal);
      if (disposed || !visible.value || epoch !== liveEpoch) return;
      clearTimeout(liveConnectionTimer);
      const socket = new WebSocket(browserLiveURL(window.location.origin, props.sessionId, props.target, ticket));
      liveConnectionTimer = setTimeout(() => {
        if (epoch !== liveEpoch || disposed || liveReady.value) return;
        closeLive(); connected.value = false; error.value = t("nativeBrowser.liveDisconnected");
        if (visible.value) reconnectTimer = setTimeout(() => void refresh(), 2000);
      }, 10000);
      live = createBrowserLive(socket, props.target, {
        configured(configuration) {
          if (epoch !== liveEpoch) return;
          responsiveSupported.value = configuration.codecPreferences.includes("responsive");
          browserRuntime.value = configuration.runtime;
          renderScaleLimit.value = configuration.renderScaleLimit;
          codecPreference.value = configuration.codecPreference;
          viewVisibilitySupported.value = configuration.viewVisibility === true;
        },
        ready(next) {
          if (epoch !== liveEpoch) return;
          clearTimeout(liveConnectionTimer);
          observationSequence++; applyState(next); liveReady.value = true; connected.value = true; error.value = "";
          if (viewVisibilitySupported.value && visible.value) { viewResumePending.value = next.url !== "about:blank"; live?.setViewVisibility(true); }
          if (next.url === "about:blank") { liveRecovered(); initialFramePending.value = false; if (codecSwitch.value === "connecting") codecSwitch.value = "idle"; }
          scheduleDisplayRecovery();
        },
        state(next) {
          if (epoch !== liveEpoch) return;
          observationSequence++; applyState(next);
          if (next.url === "about:blank") { discardFrame(); initialFramePending.value = false; stopPicking(); }
          if (!owned.value || next.busy || next.dialog || next.debugger_paused || next.devtools_blocked) releaseInput();
          if (!next.busy) queueViewport();
        },
        async media(stream, frame) { if (epoch === liveEpoch && !props.closing) return receiveMedia(stream, frame); },
        mediaClosed() { if (epoch === liveEpoch && !props.closing) clearMedia(); },
        overlay(marks) { if (epoch === liveEpoch) acceptLiveOverlay(marks); },
        visibility(shown, revision) {
          if (epoch !== liveEpoch || revision !== live?.viewRevision || !shown || !visible.value) return;
          // Cancel callbacks registered before the ACK. Only a new frame
          // callback after the current show acknowledgement can authorize input.
          void nextTick(() => {
            if (epoch !== liveEpoch || !visible.value || revision !== live?.viewRevision || !live.viewAcknowledged) return;
            observeMediaPresentation(); void videoEl.value?.play().catch(() => {});
          });
          if (state.value.url === "about:blank") viewResumePending.value = false;
        },
        connection(available) {
          if (epoch !== liveEpoch) return;
          mediaConnected.value = available;
          if (!available) { clearOverlay(); releaseInput(); }
        },
        error(failure) {
          if (epoch !== liveEpoch) return;
          if (recoverIdentity(failure)) return;
          if (recoverDisplayNavigation(failure)) return;
          if (failure.code === "LIVE_FRAME_STALE") {
            // The protocol already retired only the affected old receiver.
            // The worker releases that old gesture; never send or replay it.
            releaseInput(); invalidatePick(true);
            frameReady.value = false; viewportReady.value = false;
            mediaTransition.value = state.value.url !== "about:blank";
            return;
          }
          if (failure.code === "LIVE_BUSY" && failure.seq !== undefined && failure.seq === requestedViewportSeq) {
            // Only the display request is retryable. Input is never replayed.
            requestedViewport = ""; requestedViewportSeq = undefined; requestedViewportFrame = undefined; viewportRetryAt = Date.now() + 500;
            clearTimeout(viewportTimer); viewportTimer = undefined; queueViewport();
            return;
          }
          releaseInput();
          actionError.value = failure.message;
          liveFailureMessage.value = failure.seq === undefined ? failure.message : "";
          if (failure.code === "LIVE_FORBIDDEN") { clearBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, lease.value, leaseEpoch.value); releaseInput(); }
          if (failure.code === "LIVE_FENCED") {
            closeLive(); frameReady.value = false; connected.value = false;
            if (visible.value) reconnectTimer = setTimeout(() => void refresh(), 300);
          }
        },
        close() {
          if (epoch !== liveEpoch || disposed) return;
          closeLive(); connected.value = false; frameReady.value = false; invalidatePick(true);
          error.value = t("nativeBrowser.liveDisconnected");
          if (visible.value) reconnectTimer = setTimeout(() => void refresh(), 1500);
        },
      }, {
        codecPreference: codecPreference.value,
        relayOrigin: window.location.origin,
        resolveRelayConfiguration: (frame, signal) => api.browserMediaRelayTicket(props.sessionId, {
          context_ref: frame.context_ref, tab_id: frame.tab_id, generation: frame.generation,
          media_epoch: frame.media_epoch, live_ticket: ticket,
        }, signal),
      });
    } catch (failure) {
      clearTimeout(liveConnectionTimer);
      if (disposed || epoch !== liveEpoch) return;
      if (recoverIdentity(failure)) return;
      cancelCodecSwitch();
      connected.value = false; error.value = failure instanceof Error ? failure.message : String(failure);
      if (visible.value) reconnectTimer = setTimeout(() => void refresh(), 2000);
    }
  })();
  liveConnecting = opening;
  await opening;
  if (liveConnecting === opening) liveConnecting = undefined;
}

async function changeCodecPreference(preference: BrowserCodecPreference) {
  const connection = live;
  if (!connection || !canChangeCodec.value || preference === codecPreference.value || preference === "responsive" && !responsiveSupported.value) return;
  const controller = new AbortController(); codecSwitchAbort = controller; codecSwitch.value = "draining";
  // Keep the existing page active while its wheel/text/key-up queue drains.
  // A button click never discards accepted input to speed up this transition.
  while (!controller.signal.aborted && connection === live) {
    const drained = await connection.waitForInputIdle(controller.signal);
    if (codecSwitchAbort !== controller) return;
    if (!drained || disposed || !visible.value || live !== connection || busy.value || picking.value || pageLoading.value || state.value.busy || state.value.dialog || state.value.debugger_paused || state.value.devtools_blocked || composingInput.value) { cancelCodecSwitch(); return; }
    // A blur release can enqueue a reset in the same stack that resolved the
    // waiter. Recheck before closing; no new JS event can interleave below.
    if (!connection.inputIdle) continue;
    codecSwitchAbort = undefined; codecPreference.value = preference; codecSwitch.value = "connecting";
    closeLive(true);
    void connectLive();
    return;
  }
}
function inputBlur(event: FocusEvent) {
  if (props.closing) return;
  // Moving keyboard focus into the mode menu must preserve queued reliable
  // input. Its keyup listener still forwards releases of already held keys.
  if (event.relatedTarget instanceof Node && browserMenuEl.value?.contains(event.relatedTarget)) return;
  const connection = live;
  void releaseInputReliably().then(drained => {
    if (!props.closing && !drained && connection === live) { closeLive(); connected.value = false; if (visible.value) void refresh(); }
  });
}

function refreshView() {
  if (props.closing) return;
  clearDisplayRecovery();
  if (error.value) { recoverIdentity(); return; }
  if (!picking.value) { closeLive(); frameReady.value = false; }
  void refresh();
}

// Ordinary observation arrives on the live socket. The versioned screenshot is
// only sampled on entering read-only element selection, not after each input.
async function refresh(initial?: NativeBrowserState) {
  stopRead();
  if (props.closing || !visible.value || disposed) return;
  clearTimeout(reconnectTimer);
  const epoch = readEpoch;
  const controller = new AbortController(); readController = controller;
  const timeout = setTimeout(() => controller.abort(), 15000);
  let stage: "state" | "frame" = "state";
  try {
    const sequence = observationSequence;
    const next = initial ?? await api.browserState(props.sessionId, controller.signal, props.target);
    if (disposed || epoch !== readEpoch) return;
    if (next.backend !== "native") throw new Error(next.error || t("nativeBrowser.unavailable"));
    if (!targetMatches(next, props.target)) throw Object.assign(new Error(t("nativeBrowser.tabUnavailable")), { status: 409 });
    if (sequence === observationSequence) { observationSequence++; applyState(next); }
    connected.value = true; error.value = "";
    if (next.url === "about:blank") { discardFrame(); initialFramePending.value = false; stopPicking(); }
    else if (picking.value && !next.dialog && !next.debugger_paused && !next.devtools_blocked) {
      stage = "frame";
      const frame = await api.browserFrame(props.sessionId, controller.signal, props.target);
      if (disposed || epoch !== readEpoch || !picking.value) return;
      if (frame.context_ref !== props.target.context_ref || frame.viewed_tab !== props.target.tab_id || !nativeFrameMatches(frame.identity, next, state.value)) throw Object.assign(new Error("Browser selection frame changed"), { status: 409 });
      const url = URL.createObjectURL(frame.blob);
      try { const image = new Image(); image.src = url; await image.decode(); }
      catch (failure) { URL.revokeObjectURL(url); throw failure; }
      if (disposed || epoch !== readEpoch || !picking.value || !nativeFrameMatches(frame.identity, next, state.value)) { URL.revokeObjectURL(url); return; }
      const previous = frameURL.value;
      frameReady.value = false; clearOverlay();
      frameURL.value = url; frameViewport.value = frame.overlay?.viewport ?? next.viewport;
      frameVersion.value = frame.version; frameIdentity.value = frame.identity;
      initialFramePending.value = false;
      if (previous) URL.revokeObjectURL(previous);
    }
    frameIssue.value = picking.value && frameURL.value && !frameVersion.value ? "view-only" : ""; frameError.value = "";
    await connectLive();
  } catch (failure) {
    if (disposed || epoch !== readEpoch) return;
    if (recoverIdentity(failure)) return;
    frameReady.value = false; invalidatePick(true); clearOverlay();
    const kind = nativeReadFailure(stage, (failure as { status?: number }).status);
    if (kind === "connection") { connected.value = false; error.value = failure instanceof Error ? failure.message : String(failure); }
    else { frameIssue.value = kind; frameError.value = failure instanceof Error ? failure.message : String(failure); }
    if (visible.value) reconnectTimer = setTimeout(() => void refresh(), 2000);
  } finally { clearTimeout(timeout); }
}

async function action(work: () => Promise<NativeBrowserState>, observe = false, mergeHandoff?: (next: NativeBrowserState) => boolean): Promise<boolean> {
  if (props.closing || busy.value || recovering.value || !visible.value || inputDraining.value || viewResumePending.value) return false;
  const lifecycle = lifecycleEpoch;
  stopPicking();
  stopRead();
  clearOverlay();
  releaseInput();
  busy.value = true;
  frameReady.value = false;
  frameIdentity.value = "";
  navigationPending.value = observe;
  clearDisplayRecovery();
  actionError.value = "";
  liveFailureMessage.value = "";
  const sequence = observationSequence;
  const sentLease = { token: lease.value, epoch: leaseEpoch.value, context: props.target.context_ref, tab: props.target.tab_id };
  try {
    // Dispatch the one requested action, then observe independently. A late
    // POST result cannot roll state back past a newer successful observation.
    const pending = work();
    if (observe) void refresh();
    const next = await pending;
    if (props.closing || lifecycle !== lifecycleEpoch || disposed || recovering.value) return false;
    const freshHandoff = mergeHandoff?.(next) === true;
    if (visible.value && (sequence === observationSequence || freshHandoff) && targetMatches(next, props.target)) {
      // A completed handoff's bound GET may follow an older live observation.
      // applyState still rejects older generations, epochs and documents.
      if (freshHandoff) observationSequence++;
      applyState(next);
      connected.value = true;
      error.value = "";
    }
    return true;
  } catch (err) {
    if (props.closing || lifecycle !== lifecycleEpoch || disposed) return false;
    if (recoverIdentity(err)) return false;
    if (nativeLeaseRejected((err as { status?: number }).status)) clearBrowserLease(props.sessionId, sentLease.context, sentLease.tab, sentLease.token, sentLease.epoch);
    actionError.value = err instanceof Error ? err.message : String(err);
    return false;
  } finally {
    // Observations made while the POST was pending can predate its final
    // document. Do not unlock unversioned keyboard/text/scroll input until
    // the follow-up GET and frame commit; even a late img.load is fenced.
    if (!props.closing && lifecycle === lifecycleEpoch && !disposed) {
      frameReady.value = false;
      frameIdentity.value = "";
      invalidatePick(true);
      clearOverlay();
      busy.value = false;
      navigationPending.value = false;
      if (!recovering.value) { observeMediaPresentation(); void refresh(); }
    }
  }
}

async function takeControl(force = false, adoption?: BrowserHumanAdoption) {
  if (props.closing || !connected.value || !targetMatches(state.value, props.target)) return;
  const lifecycle = lifecycleEpoch;
  takeoverStarted = performance.now(); takeoverMs.value = undefined;
  const sessionId = props.sessionId, target = { ...props.target };
  let acquired: NativeBrowserState | undefined;
  const result = await action(async () => {
    // The server hands over this Page only. Never cancel the whole session
    // or grant its sibling tabs the same input lease.
    let next: Awaited<ReturnType<typeof api.browserControl>>;
    try { next = await api.browserControl(sessionId, "human", adoption ? { ...target, ...adoption } : { force, ...target }); }
    catch (error) {
      if (props.closing || disposed || lifecycle !== lifecycleEpoch) return state.value;
      if (!adoption || (error as { status?: number }).status !== 409) throw error;
      // Another owner or generation won the race. Observe once; never force
      // a handoff or replay the initialization with a new request identity.
      const viewed = await api.browserState(sessionId, undefined, target);
      if (viewed.backend !== "native") throw new Error(viewed.error);
      if (!targetMatches(viewed, target)) throw new Error(t("nativeBrowser.tabUnavailable"));
      return viewed;
    }
    if (props.closing || disposed || lifecycle !== lifecycleEpoch) return state.value;
    if (!targetMatches(next, target)) throw new Error(t("nativeBrowser.tabUnavailable"));
    if (adoption && (next.control !== "human" || next.control_epoch !== 1 || next.generation !== adoption.generation)) throw new Error(t("nativeBrowser.lostControl"));
    if (!next.lease_token) throw new Error(t("nativeBrowser.lostControl"));
    acquired = next;
    if (!disposed) installBrowserLease(sessionId, target.context_ref, target.tab_id, next.control_epoch, next.lease_token);
    const viewed = await api.browserState(sessionId, undefined, target);
    if (viewed.backend !== "native") throw new Error(viewed.error);
    if (!targetMatches(viewed, target)) throw new Error(t("nativeBrowser.tabUnavailable"));
    return viewed;
  }, false, next => !!acquired && props.sessionId === sessionId && props.target.context_ref === target.context_ref && props.target.tab_id === target.tab_id && targetMatches(next, target) && next.generation === acquired.generation && next.control_epoch === acquired.control_epoch && ownsNativeControl(next, lease.value, leaseEpoch.value));
  if (!result) takeoverStarted = undefined;
  return result;
}

async function returnControl() {
  if (props.closing || !owned.value) return;
  const lifecycle = lifecycleEpoch;
  // Clicking the header first blurs the input capture. Join that reliable
  // release before action's drain guard, including keyboard focus changes.
  if (!await releaseInputReliably() || props.closing || disposed || lifecycle !== lifecycleEpoch || !owned.value) return;
  await action(async () => {
    const sentToken = lease.value, sentEpoch = leaseEpoch.value;
    await api.browserControl(props.sessionId, "agent", { lease_token: sentToken, context_ref: props.target.context_ref, tab_id: props.target.tab_id });
    if (props.closing || disposed || lifecycle !== lifecycleEpoch) return state.value;
    clearBrowserLease(props.sessionId, props.target.context_ref, props.target.tab_id, sentToken, sentEpoch);
    const viewed = await api.browserState(props.sessionId, undefined, props.target);
    if (viewed.backend !== "native") throw new Error(viewed.error);
    return viewed;
  });
}

async function input(value: NativeBrowserInput) {
  const allowed = value.type === "dialog" ? controlReady.value : value.type === "back" ? canGoBack.value : value.type === "navigate" || value.type === "tab" ? canNavigate.value : writable.value;
  if (!allowed) return false;
  return action(() => api.browserInput(props.sessionId, value, lease.value, { ...props.target, generation: state.value.generation! }), value.type === "navigate" || value.type === "back" || value.type === "tab");
}

async function goBack() {
  if (!canGoBack.value) return;
  cancelBookmarkSelection();
  editingAddress.value = false;
  return await input({ type: "back" });
}

async function navigate(value = address.value) {
  if (!canNavigate.value) return;
  const url = nativeNavigationURL(value, state.value.url, window.location.origin);
  if (!url) { actionError.value = t("nativeBrowser.invalidUrl"); return; }
  editingAddress.value = false;
  address.value = url;
  return await input({ type: "navigate", url });
}

function cancelBookmarkSelection() {
  if (!pendingBookmark.value) return;
  pendingBookmark.value = null;
  editingAddress.value = false;
  address.value = state.value.url === "about:blank" ? "" : state.value.url;
}
function openBookmarks() { cancelBookmarkSelection(); bookmarksManager.value?.add(); }
function editBookmark(card: BrowserBookmark) { cancelBookmarkSelection(); bookmarksManager.value?.edit(card); }
function removeBookmark(card: BrowserBookmark) { cancelBookmarkSelection(); bookmarksManager.value?.remove(card); }
function repairBookmark(card: BrowserBookmark) { cancelBookmarkSelection(); bookmarksManager.value?.repair(card); }
async function selectBookmark(card: BrowserBookmark) {
  if (busy.value || pendingBookmark.value || disposed || !visible.value || !connected.value) return;
  if (card.needsRepair) { void repairBookmark(card); return; }
  const url = nativeNavigationURL(card.url, state.value.url, window.location.origin);
  if (!url) { void repairBookmark(card); return; }
  const document = nativeDocumentStamp(state.value);
  if (!document) { actionError.value = t("nativeBrowser.stale"); void refresh(); return; }
  pendingBookmark.value = { url, fromURL: state.value.url, fromDocument: document, fromGeneration: state.value.generation, sessionId: props.sessionId, target: { ...props.target } };
  const chosen = pendingBookmark.value;
  try {
    // Clicking a favorite requests control of this Page and one navigation.
    // A late handoff must never reopen a hidden, changed, or cancelled Page.
    if (!owned.value && !(await takeControl(state.value.control === "human"))) return;
    if (disposed || !visible.value || pendingBookmark.value !== chosen || props.sessionId !== chosen.sessionId || props.target.context_ref !== chosen.target.context_ref || props.target.tab_id !== chosen.target.tab_id || !targetMatches(state.value, chosen.target) || state.value.generation !== chosen.fromGeneration || state.value.url !== chosen.fromURL || nativeDocumentStamp(state.value) !== chosen.fromDocument || !canNavigate.value) return;
    await navigate(chosen.url);
  } finally {
    if (pendingBookmark.value === chosen) cancelBookmarkSelection();
  }
}

function focusAddress(event: FocusEvent) {
  cancelBookmarkSelection();
  editingAddress.value = canNavigate.value;
  addressBeforeEdit.value = state.value.url === "about:blank" ? "" : state.value.url;
  (event.target as HTMLInputElement).select();
}

function editAddress() {
  // Enter navigation leaves focus here. The next edit must start a fresh draft
  // even without another focus event, before polling can replace the value.
  if (!canNavigate.value || editingAddress.value) return;
  editingAddress.value = true;
  addressBeforeEdit.value = state.value.url === "about:blank" ? "" : state.value.url;
}

function addressKey(event: KeyboardEvent) {
  if (event.isComposing || addressComposing.value || event.keyCode === 229) return;
  if (event.key === "Escape") {
    address.value = addressBeforeEdit.value;
    editingAddress.value = false;
    (event.target as HTMLInputElement).blur();
    event.stopPropagation();
  } else if (event.key === "Enter" && canNavigate.value) {
    event.preventDefault();
    event.stopPropagation();
    void navigate();
  }
}

function pickerFrame(): BrowserPickerFrame | null {
  if (!picking.value || !canPick.value || !frameVersion.value) return null;
  return { context_ref: props.target.context_ref, tab_id: props.target.tab_id, version: frameVersion.value, viewport: { ...frameViewport.value }, pixels: frameURL.value };
}
const picker = createBrowserPicker({
  frame: pickerFrame,
  preview: request => browserPickerTransport(signal => api.browserPickPreview(props.sessionId, request, signal), "preview"),
  commit: request => browserPickerTransport(signal => api.browserPick(props.sessionId, request, signal), "commit"),
  previewed(preview) { if (!props.closing && !disposed) picked.value = preview; },
  committed(result) {
    if (props.closing || disposed || !picking.value) return;
    const frame = pickerFrame(), identity = frameIdentity.value;
    if (!frame || !identity) return;
    const selection = nativeElementPick(result, state.value, { ...frame, identity });
    if (!selection) return;
    const session = props.sessionId, epoch = pickRecoveryEpoch, conversion = ++faviconCommitEpoch;
    faviconCommitting.value = true; pickCommitting.value = true;
    void snapshotReferenceFavicon(selection.icon).then(icon => {
      const current = pickerFrame();
      if (disposed || !picking.value || conversion !== faviconCommitEpoch || session !== props.sessionId || epoch !== pickRecoveryEpoch || !current || frameIdentity.value !== identity || frame.context_ref !== current.context_ref || frame.tab_id !== current.tab_id || frame.version !== current.version || state.value.url !== result.url) return;
      const { icon: _displayIcon, ...captured } = selection;
      stopPicking(); emit("pick", { ...captured, ...(icon ? { icon } : {}) });
    }).finally(() => {
      if (conversion === faviconCommitEpoch) { faviconCommitting.value = false; pickCommitting.value = false; }
    });
  },
  status(status) {
    if (props.closing || disposed) return;
    pickLoading.value = status.loading;
    pickCommitting.value = status.committing || faviconCommitting.value;
    pickPending.value = status.pending;
  },
  error(cause) {
    if (props.closing || disposed || !picking.value) return;
    if (recoverIdentity(cause)) return;
    const failure = cause as { code?: string; status?: number };
    if (failure.code === "PICK_OBSERVATION_PENDING") {
      pickError.value = t("nativeBrowser.pickPending"); pickRecoveryAttempts = 0; schedulePickRecovery(0); return;
    }
    pickError.value = failure.code === "PICK_EMPTY" ? t("nativeBrowser.pickEmpty") : failure.status === 409 ? t("nativeBrowser.pickStale") : t("nativeBrowser.pickFailed", { message: cause instanceof Error ? cause.message : String(cause) });
    if (failure.status === 409) void refresh();
  },
});
function schedulePickRecovery(delay = 500) {
  if (props.closing || disposed || !picking.value || !picker.pending || pickRecoveryFlight || pickRecoveryTimer !== undefined) return;
  pickRecoveryTimer = setTimeout(() => { pickRecoveryTimer = undefined; void recoverPickObservation(); }, delay);
}
async function recoverPickObservation() {
  if (props.closing || disposed || !picking.value || !picker.pending || pickRecoveryFlight) return;
  const flight = { epoch: pickRecoveryEpoch, controller: new AbortController() };
  pickRecoveryFlight = flight; pickRecoveryAttempts++;
  const current = () => !props.closing && !disposed && picking.value && picker.pending && pickRecoveryFlight === flight && flight.epoch === pickRecoveryEpoch;
  try {
    // A false cached before the timed-out read is not an acknowledgement.
    // Only this fresh, bound HTTP read can authorize the next observation.
    const next = await browserPickerTransport(signal => api.browserState(props.sessionId, signal, props.target), "state", { signal: flight.controller.signal });
    if (!current()) return;
    if (next.backend !== "native" || !targetMatches(next, props.target)) throw new Error(t("nativeBrowser.tabUnavailable"));
    observationSequence++; applyState(next);
    if (next.pick_pending === false) {
      pickError.value = ""; picker.drained();
      if (!frameReady.value || !frameVersion.value) void refresh(next);
    }
  } catch (cause) {
    if (current()) {
      if (recoverIdentity(cause)) return;
      pickError.value = (cause as { code?: string }).code === "PICK_STATE_TIMEOUT" ? t("nativeBrowser.pickStateTimeout") : t("nativeBrowser.pickFailed", { message: cause instanceof Error ? cause.message : String(cause) });
    }
  } finally {
    if (pickRecoveryFlight === flight) {
      pickRecoveryFlight = undefined;
      // Short recovery polling closes a missed WS transition; a longer pending
      // read then waits for a new state event or an explicit mode re-entry.
      if (flight.epoch === pickRecoveryEpoch && pickRecoveryAttempts < 10) schedulePickRecovery();
    }
  }
}
function invalidatePick(changed = false) {
  if (changed && pickCommitting.value) pickError.value = t("nativeBrowser.pickStale");
  picker.invalidate();
}

function stopPicking() {
  const lifecycle = lifecycleEpoch;
  faviconCommitEpoch++; faviconCommitting.value = false; pickCommitting.value = false;
  const wasPicking = picking.value;
  clearTimeout(selectionRefreshTimer); clearTimeout(pickRecoveryTimer); pickRecoveryTimer = undefined;
  pickRecoveryEpoch++;
  const recovery = pickRecoveryFlight; pickRecoveryFlight = undefined; recovery?.controller.abort();
  picking.value = false;
  invalidatePick();
  pickPointer = undefined;
  pickError.value = "";
  if (wasPicking) {
    frameReady.value = false; frameVersion.value = "";
    void nextTick(() => {
      if (props.closing || lifecycle !== lifecycleEpoch || disposed || !visible.value || viewVisibilitySupported.value && !live?.viewAcknowledged) return;
      observeMediaPresentation(); void videoEl.value?.play().catch(() => {});
    });
  }
}

function togglePicking() {
  if (props.closing) return;
  if (picking.value) { stopPicking(); return; }
  if (!canPick.value) return;
  releaseInput();
  picking.value = true;
  frameReady.value = false;
  frameVersion.value = "";
  if (picker.pending) { pickRecoveryAttempts = 0; schedulePickRecovery(0); } else void refresh();
  pickError.value = "";
  void nextTick(() => pageEl.value?.focus({ preventScroll: true }));
}

function pickerEscape(event: KeyboardEvent) {
  if (!picking.value || event.key !== "Escape") return;
  // The immersive window listener may be registered after this one. Leave
  // Shift+Esc unclaimed so it can also exit; propagation still stops before
  // any remote page key handler can see the event.
  if (!event.shiftKey) event.preventDefault();
  event.stopPropagation();
  stopPicking();
  void nextTick(() => pickerButton.value?.focus({ preventScroll: true }));
}

function pickPoint(x: number, y: number) {
  if (!frameEl.value) return null;
  return nativeViewportPoint(x, y, frameEl.value.getBoundingClientRect(), frameViewport.value);
}
function queuePick(x: number, y: number) {
  pickPointer = { x, y };
  const point = pickPoint(x, y);
  if (!point || !pickerFrame()) { picker.invalidate(); return; }
  pickError.value = picker.pending ? t("nativeBrowser.pickPending") : "";
  picker.hover(point);
}
function hoverPick(event: PointerEvent) {
  if (!picking.value || event.pointerType === "touch") return;
  queuePick(event.clientX, event.clientY);
}
function leavePick() {
  if (!picking.value) return;
  pickPointer = undefined; picker.invalidate();
}
function clickPage(event: MouseEvent) {
  if (!picking.value) return;
  event.preventDefault(); event.stopPropagation();
  const point = pickPoint(event.clientX, event.clientY);
  if (!point || !pickerFrame()) return;
  pickPointer = { x: event.clientX, y: event.clientY };
  pickError.value = picker.pending ? t("nativeBrowser.pickPending") : "";
  picker.click(point);
}

function remotePoint(x: number, y: number, clamp = false) {
  if (!frameEl.value) return null;
  const rect = frameEl.value.getBoundingClientRect();
  if (!clamp) return nativeViewportPoint(x, y, rect, frameViewport.value);
  const content = nativeContentRect(rect, frameViewport.value);
  if (!content) return null;
  return nativeViewportPoint(Math.max(rect.left + content.x, Math.min(rect.left + content.x + content.width - .1, x)), Math.max(rect.top + content.y, Math.min(rect.top + content.y + content.height - .1, y)), rect, frameViewport.value);
}
function pointerEvent(event: PointerEvent, phase: "down" | "move" | "up" | "cancel") {
  if (phase === "move" && picking.value) { hoverPick(event); return; }
  if (picking.value || !writable.value || !event.isPrimary) return;
  const point = remotePoint(event.clientX, event.clientY, pointerId === event.pointerId);
  if (!point) return;
  if (phase === "down") {
    textCommit.freshGesture();
    const repeated = Date.now() - lastClick.at < 500 && lastClick.button === event.button && Math.hypot(point.x - lastClick.x, point.y - lastClick.y) < 5;
    gestureClickCount = repeated ? Math.min(3, lastClick.count + 1) : 1;
    lastClick = { at: Date.now(), ...point, button: event.button, count: gestureClickCount };
    pointerId = event.pointerId;
    frameEl.value?.setPointerCapture(event.pointerId);
    const rect = pageEl.value!.getBoundingClientRect();
    inputPosition.value = { x: Math.max(0, event.clientX - rect.left), y: Math.max(0, event.clientY - rect.top) };
    inputEl.value?.focus({ preventScroll: true });
    actionError.value = "";
  }
  if ((phase === "up" || phase === "cancel") && pointerId !== event.pointerId) return;
  event.preventDefault(); event.stopPropagation();
  lastPointer = point; pointerButtons = event.buttons;
  const message: BrowserLiveEvent = { type: "pointer", phase, ...point, button: liveButton(event.button), buttons: event.buttons, modifiers: liveModifiers(event), click_count: gestureClickCount };
  sendLive(message);
  if (phase === "up" || phase === "cancel") {
    pointerId = undefined; pointerButtons = 0;
    if (frameEl.value?.hasPointerCapture(event.pointerId)) frameEl.value.releasePointerCapture(event.pointerId);
    queueViewport();
  }
}
function lostPointer() {
  if (pointerId === undefined) return;
  if (!visible.value) releaseLocalInput(); else releaseInput();
}
function keyMessage(event: KeyboardEvent, phase: "down" | "up"): BrowserLiveEvent {
  const meta = event.key === "Meta";
  return { type: "key", phase, key: meta ? "Control" : event.key, code: meta ? "ControlLeft" : event.code, key_code: meta ? 17 : event.keyCode, modifiers: liveModifiers(event), repeat: event.repeat };
}
function focusInput() { if (writable.value) inputEl.value?.focus({ preventScroll: true }); }
function pageKey(event: KeyboardEvent) {
  if (picking.value) { if (event.key !== "Tab") event.preventDefault(); event.stopPropagation(); return; }
  if (!writable.value || state.value.dialog) return;
  if (!event.isComposing) textCommit.freshGesture();
  const kind = liveKeyKind(event);
  if (kind === "local" || kind === "composition") return;
  event.stopPropagation();
  if (kind === "text") return; // beforeinput owns printable text and dead keys.
  event.preventDefault();
  pressedKeys.add(event.code || event.key);
  sendLive(keyMessage(event, "down"));
}
function pageKeyUp(event: KeyboardEvent) {
  const key = event.code || event.key;
  if (!pressedKeys.delete(key)) return;
  event.preventDefault(); event.stopPropagation();
  if (writable.value) sendLive(keyMessage(event, "up"));
}
function beforeText(event: InputEvent) {
  if (!writable.value || picking.value) { event.preventDefault(); return; }
  const value = textCommit.input(event.data, event.inputType, event.isComposing);
  if (event.isComposing || textCommit.composing) return;
  event.preventDefault();
  if (value) sendLive({ type: "text", text: value });
  if (inputEl.value) inputEl.value.value = "";
}
function compositionStart() {
  if (!writable.value) return;
  // A composition started while a switch is draining wins over that pending
  // preference; its uncommitted text must not be destroyed by a reconnect.
  if (codecSwitch.value === "draining") cancelCodecSwitch();
  composingInput.value = true; textCommit.start();
}
function compositionEnd(event: CompositionEvent) {
  composingInput.value = false;
  const value = textCommit.end(event.data);
  if (writable.value && value) sendLive({ type: "text", text: value });
  if (inputEl.value) inputEl.value.value = "";
}
function paste(event: ClipboardEvent) {
  if (!writable.value || picking.value) return;
  const value = event.clipboardData?.getData("text/plain");
  event.preventDefault(); event.stopPropagation(); textCommit.reset(); composingInput.value = false;
  if (value && value.length > 65536) { actionError.value = t("nativeBrowser.livePasteTooLarge"); return; }
  if (value) sendLive({ type: "text", text: value });
}
function wheel(event: WheelEvent) {
  if (picking.value) { event.preventDefault(); return; }
  if (!writable.value || state.value.dialog) return;
  const point = remotePoint(event.clientX, event.clientY) ?? lastPointer;
  event.preventDefault();
  const units = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? state.value.viewport.height : 1;
  sendLive({ type: "wheel", ...point, delta_y: event.deltaY * units, delta_x: event.deltaX * units, modifiers: liveModifiers(event) });
}

function sampleDiagnostics() {
  const epoch = ++diagnosticsEpoch;
  clearTimeout(diagnosticsTimer);
  if (props.closing || !visible.value || !diagnosticsOpen.value || disposed) return;
  const sample = async () => {
    const connection = live;
    const result = await connection?.diagnostics().catch(() => null);
    if (props.closing || epoch !== diagnosticsEpoch || disposed || !visible.value || !diagnosticsOpen.value) return;
    diagnostics.value = connection === live ? result ?? null : null;
    diagnosticsTimer = setTimeout(() => void sample(), 2000);
  };
  void sample();
}
function toggleDiagnostics(event: MouseEvent) {
  diagnosticsOpen.value = !diagnosticsOpen.value;
  (event.currentTarget as HTMLElement).closest("details")?.removeAttribute("open");
}
watch([diagnosticsOpen, visible], sampleDiagnostics);
watch(visible, value => { if (!value) takeoverStarted = undefined; });

// Closing the Page normally retires its socket before the close receipt.
// Retain the last pixels and revoke this view's work before that event arrives.
watch(() => props.closing, (closing, previous) => {
  if (closing) {
    lifecycleEpoch++; liveEpoch++;
    clearDisplayRecovery();
    stopRead(); liveAbort?.abort(); liveAbort = undefined;
    clearTimeout(reconnectTimer); clearTimeout(viewportTimer); viewportTimer = undefined;
    clearTimeout(selectionRefreshTimer); clearTimeout(liveConnectionTimer); clearTimeout(loadingTimer);
    clearTimeout(pickRecoveryTimer); pickRecoveryTimer = undefined;
    pickRecoveryEpoch++; const recovery = pickRecoveryFlight; pickRecoveryFlight = undefined; recovery?.controller.abort();
    faviconCommitEpoch++; faviconCommitting.value = false; pickCommitting.value = false; picker.invalidate();
    diagnosticsEpoch++; clearTimeout(diagnosticsTimer);
    inputReleaseAbort?.abort(); inputReleaseAbort = undefined; inputReleaseFlight = undefined; inputDraining.value = false;
    cancelCodecSwitch(); cancelBookmarkSelection();
    retainPresentedFrame(); pauseMediaPresentation();
    live?.clearPendingInput(); releaseLocalInput(); clearOverlay();
    busy.value = false; navigationPending.value = false; takeoverStarted = undefined;
  } else if (previous) {
    // A failed close resumes observation only. Never replay an old action,
    // drain/reset old input, or return this Page's lease implicitly.
    closeLive(false, true); connected.value = false;
    if (visible.value) void refresh();
    sampleDiagnostics();
  }
}, { flush: "sync", immediate: true });

// State pushes create a new dialog object each time; don't overwrite text the user
// is typing until the dialog itself changes.
// Watch sources evaluate immediately during setup, after every computed
// dependency (including heldFrameVisible and pageLoading) is initialized.
watch(writable, value => { if (value && takeoverStarted !== undefined) { takeoverMs.value = performance.now() - takeoverStarted; takeoverStarted = undefined; } });
watch(() => props.initialUrl, url => {
  if (!url || url === state.value.url) return;
  addressBeforeEdit.value = state.value.url;
  address.value = url;
  editingAddress.value = true;
});
watch(() => JSON.stringify(state.value.dialog ?? null), () => { promptText.value = state.value.dialog?.default_value ?? ""; }, { immediate: true });
watch(visible, shown => {
  if (props.closing) return;
  clearOverlay();
  if (shown) {
    warmView.show();
    if (live?.ready && viewVisibilitySupported.value) syncSheetVisibility();
    else void refresh();
    scheduleDisplayRecovery();
  } else {
    clearTimeout(displayRecoveryTimer); displayRecoveryTimer = undefined;
    resetImmersiveLayout();
    cancelCodecSwitch(); cancelBookmarkSelection(); stopPicking(); stopRead();
    if (pageVisible.value && !recovering.value && live?.ready && viewVisibilitySupported.value) syncSheetVisibility();
    else { closeLive(); connected.value = false; frameReady.value = false; }
  }
});
// Compare the binding values independently. Parent state refreshes replace the
// target object without changing this page; a tuple-returning getter would
// treat every replacement as a new array and cancel its pending handoff.
watch([() => props.sessionId, () => props.target.context_ref, () => props.target.tab_id], () => { clearDisplayRecovery(); resetImmersiveLayout(); }, { flush: "sync" });
watch(owned, value => { if (props.closing) return; viewportReady.value = false; if (!value) releaseInput(); else queueViewport(); });
watch([visible, connected, busy, lease, leaseEpoch, () => state.value], () => {
  if (props.closing || disposed || picking.value || recovering.value) return;
  const request = browserHumanAdoptions.request({ sessionId: props.sessionId, target: props.target, state: state.value,
    visible: visible.value, connected: connected.value, busy: busy.value, lease: lease.value, knownEpoch: leaseEpoch.value });
  if (request) void takeControl(false, request);
}, { flush: "post", immediate: true });
const visibilityChanged = () => { pageVisible.value = !document.hidden; };
onMounted(() => {
  document.addEventListener("visibilitychange", visibilityChanged);
  window.addEventListener("keydown", pickerEscape, true);
  window.addEventListener("blur", releaseInput);
  window.addEventListener("resize", screenDensityChanged);
  window.addEventListener(IMMERSIVE_LAYOUT_EVENT, immersiveLayout);
  observeScreenDensity();
  resizeObserver = new ResizeObserver(() => measureCanvas());
  if (pageEl.value) resizeObserver.observe(pageEl.value);
  measureCanvas();
  if (visible.value) void refresh(props.initialState);
});
onBeforeUnmount(() => {
  disposed = true;
  clearDisplayRecovery();
  resetImmersiveLayout();
  diagnosticsEpoch++; clearTimeout(diagnosticsTimer);
  cancelBookmarkSelection();
  clearTimeout(loadingTimer);
  stopPicking();
  picker.close();
  clearTimeout(pickRecoveryTimer);
  stopRead();
  closeLive();
  warmView.dispose();
  clearOverlay();
  resizeObserver?.disconnect();
  document.removeEventListener("visibilitychange", visibilityChanged);
  window.removeEventListener("keydown", pickerEscape, true);
  window.removeEventListener("blur", releaseInput);
  window.removeEventListener("resize", screenDensityChanged);
  window.removeEventListener(IMMERSIVE_LAYOUT_EVENT, immersiveLayout);
  densityQuery?.removeEventListener("change", screenDensityChanged);
  if (frameURL.value) URL.revokeObjectURL(frameURL.value);
  // Never auto-return a lease on unload: a second observing window or an
  // interrupted request must not silently re-arm the agent. Explicit reclaim
  // handles a closed controlling window.
});
</script>

<template>
  <section class="native-browser" :inert="closing" :data-closing="closing || undefined" :data-browser-runtime="browserRuntime" :data-live-presentation="livePresentation" :data-codec-preference="codecPreference" :data-codec-switch="codecSwitch" :aria-label="t('nativeBrowser.title')">
    <div class="native-toolbar" role="toolbar" :aria-label="t('nativeBrowser.navigation')">
      <button class="chrome-icon" type="button" data-testid="browser-back" :disabled="!canGoBack" :title="t('nativeBrowser.back')" :aria-label="t('nativeBrowser.back')" @click="goBack">
        <svg viewBox="0 0 20 20" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M16 10H4m5-5-5 5 5 5" /></svg>
      </button>
      <button class="chrome-icon" type="button" :disabled="closing || busy" :title="t(error ? 'nativeBrowser.reconnect' : 'nativeBrowser.refresh')" :aria-label="t(error ? 'nativeBrowser.reconnect' : 'nativeBrowser.refresh')" @click="refreshView">
        <svg viewBox="0 0 20 20" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M16 6.5a6.5 6.5 0 1 0 .3 6M16 2.5v4.5h-4.5" /></svg>
      </button>
      <button ref="pickerButton" class="chrome-icon picker-toggle" :class="{ selected: picking }" type="button" :disabled="!picking && !canPick" :aria-label="t('nativeBrowser.pick')" :aria-pressed="picking" :title="t(picking ? 'nativeBrowser.pickCancel' : canPick ? 'nativeBrowser.pickHint' : 'nativeBrowser.pickUnavailable')" @click="togglePicking">
        <svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 12V4.5a1.5 1.5 0 0 1 3 0V11m0-2.5a1.5 1.5 0 0 1 3 0V12m0-2.5a1.5 1.5 0 0 1 3 0V13m0-2.5a1.5 1.5 0 0 1 3 0V16c0 3.5-2 5.5-5.5 5.5h-1C11 21.5 9.5 20 8 18l-3.5-4.5a1.6 1.6 0 0 1 2.3-2.2L8 12Z" /></svg>
        <span class="picker-label">{{ t('nativeBrowser.pickShort') }}</span>
      </button>
      <div class="native-location" :class="{ editing: editingAddress && canNavigate }">
        <span class="connection-dot" :class="{ online: connected, offline: !closing && !!error, human: owned, loading: !closing && pageLoading }" role="img" :aria-label="connectionLabel" :title="pageLoading ? t('nativeBrowser.pageLoading') : connectionLabel" />
        <input v-model="address" :aria-label="t('nativeBrowser.url')" :title="address || t('nativeBrowser.url')" :placeholder="t('nativeBrowser.url')" :readonly="!canNavigate" spellcheck="false" autocomplete="off" @focus="focusAddress" @input="editAddress" @blur="editingAddress = false" @keydown="addressKey" @compositionstart="addressComposing = true" @compositionend="addressComposing = false" />
        <button v-if="canNavigate && addressChanged" class="location-go" type="button" :title="t('nativeBrowser.go')" :aria-label="t('nativeBrowser.go')" @pointerdown.prevent @click="navigate()">
          <svg viewBox="0 0 20 20" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 10h12m-5-5 5 5-5 5" /></svg>
        </button>
      </div>
      <BrowserDownloads :items="state.downloads || []" :active="visible && !closing" :rejected="state.downloads_rejected_count" :rejection="state.downloads_last_rejection" :max-file-bytes="state.downloads_max_file_bytes" @open-file="emit('openFile', $event)" />
      <BrowserControlButton v-if="owned" mode="return" :disabled="closing || busy" :aria-label="t('nativeBrowser.giveBack')" :title="t('nativeBrowser.controlHint')" @click="returnControl">{{ t('nativeBrowser.giveBackShort') }}</BrowserControlButton>
      <BrowserControlButton v-else mode="takeover" :disabled="closing || busy || !connected" :aria-label="t(busy ? 'nativeBrowser.takingControl' : state.control === 'human' ? 'nativeBrowser.reclaim' : 'nativeBrowser.takeover')" :title="state.control === 'human' ? t('nativeBrowser.reclaimHint') : t('nativeBrowser.controlHint')" @click="takeControl(state.control === 'human')">{{ t(busy ? 'nativeBrowser.takingShort' : state.control === 'human' ? 'nativeBrowser.reclaimShort' : 'nativeBrowser.takeoverShort') }}</BrowserControlButton>
      <details ref="browserMenuEl" class="native-menu" @pointerdown.prevent @keyup="pageKeyUp">
        <summary class="chrome-icon" :aria-label="t('nativeBrowser.menu')" :title="t('nativeBrowser.menu')">•••</summary>
        <div class="native-menu-options">
          <template v-if="browserRuntime !== 'wayland'">
          <span class="native-menu-heading">{{ t('nativeBrowser.browserMode') }}</span>
          <button type="button" data-testid="browser-mode-standard" :aria-pressed="codecPreference === 'standard'" :disabled="!canChangeCodec" @click="changeCodecPreference('standard')">{{ t('nativeBrowser.modeStandard') }}<span v-if="codecPreference === 'standard'" aria-hidden="true">✓</span></button>
          <button type="button" data-testid="browser-mode-responsive" :aria-pressed="codecPreference === 'responsive'" :disabled="!canChangeCodec || !responsiveSupported" @click="changeCodecPreference('responsive')">{{ t('nativeBrowser.modeResponsive') }}<span v-if="codecPreference === 'responsive'" aria-hidden="true">✓</span></button>
          <p>{{ t(responsiveSupported ? 'nativeBrowser.modeHint' : 'nativeBrowser.modeUnsupported') }}</p>
          <p v-if="codecSwitch !== 'idle'" role="status">{{ t(codecSwitch === 'draining' ? 'nativeBrowser.modeDraining' : 'nativeBrowser.modeSwitching') }}</p>
          </template>
          <button type="button" :aria-pressed="diagnosticsOpen" @click="toggleDiagnostics">{{ t('nativeBrowser.performanceTitle') }}</button>
        </div>
      </details>
    </div>
    <BrowserBookmarks ref="bookmarksManager" :current-url="state.url" :current-title="state.url === 'about:blank' ? '' : state.title" />
    <div ref="pageEl" class="native-page" :class="{ readonly: !writable, picking, 'new-tab': showNewTab }" tabindex="0" :aria-label="t('nativeBrowser.page')" @focus.self="focusInput" @keydown="pageKey" @keyup="pageKeyUp" @wheel="wheel">
      <BrowserNewTab v-if="showNewTab" :agent-scoped="!!bookmarkStore.agentId" @import-account="bookmarksManager?.importAccountBookmarks()" :bookmarks="bookmarks" :bookmark-status="bookmarkStore.state" @retry="bookmarkStore.refresh()" @sync="bookmarksManager?.syncLocalBookmarks()" :busy="busy || !!pendingBookmark" @select="selectBookmark" @add="openBookmarks()" @edit="editBookmark" @remove="removeBookmark" @import="bookmarksManager?.importBookmarks()" @export="bookmarksManager?.exportBookmarks()" />
      <template v-else>
        <video v-if="mediaFrame" v-show="!picking" :key="mediaFrame.media_epoch" ref="videoEl" data-testid="browser-live-video" :data-media-epoch="mediaFrame.media_epoch" :data-frame-id="mediaFrame.frame_id" :aria-label="t('nativeBrowser.page')" autoplay muted playsinline disablepictureinpicture @error="if (!closing) { frameReady = false; frameIssue = 'unavailable'; }" @pointerdown="pointerEvent($event, 'down')" @pointermove="pointerEvent($event, 'move')" @pointerup="pointerEvent($event, 'up')" @pointercancel="pointerEvent($event, 'cancel')" @lostpointercapture="lostPointer" @pointerleave="leavePick" @contextmenu.prevent />
        <canvas ref="heldCanvasEl" v-show="heldFrameVisible" class="native-held-frame" data-testid="browser-live-held-frame" data-stale="true" :data-session-id="heldFrame?.binding.sessionId" :data-context-ref="heldFrame?.binding.context_ref" :data-tab-id="heldFrame?.binding.tab_id"
          :data-transition-id="pixelBridge?.transitionId" :data-bridge-phase="pixelBridge?.phase" :data-bridge-source-rect="pixelBridge ? JSON.stringify(pixelBridge.source) : undefined" :data-bridge-source-page-rect="pixelBridge ? JSON.stringify(pixelBridge.sourcePage) : undefined" :data-bridge-target-rect="pixelBridge?.target ? JSON.stringify(pixelBridge.target) : undefined" :data-bridge-duration="pixelBridge?.duration" aria-hidden="true" />
        <img v-if="picking && frameURL" ref="pickFrameEl" :src="frameURL" :alt="t('nativeBrowser.page')" draggable="false" @load="frameLoaded" @error="frameReady = false; clearOverlay(); invalidatePick(true)" @pointerdown="pointerEvent($event, 'down')" @pointermove="pointerEvent($event, 'move')" @pointerup="pointerEvent($event, 'up')" @pointercancel="pointerEvent($event, 'cancel')" @lostpointercapture="lostPointer" @pointerleave="leavePick" @contextmenu.prevent @click="clickPage" />
      </template>
      <textarea v-if="owned && !showNewTab" ref="inputEl" class="native-input-capture" :style="{ left: `${inputPosition.x}px`, top: `${inputPosition.y}px` }" :aria-label="t('nativeBrowser.liveInput')" autocomplete="off" autocapitalize="off" spellcheck="false" :disabled="!writable" @beforeinput="beforeText" @compositionstart="compositionStart" @compositionend="compositionEnd" @paste="paste" @blur="inputBlur" />
      <!-- Transient feedback must not resize the canvas or its remote viewport. -->
      <div v-if="!closing" class="canvas-feedback">
        <div v-if="error || actionError || displayError || frameIssue || showActivity || resetNotice !== null || state.page_load_error || slowLoading" class="native-statusbar" aria-live="polite">
          <span v-if="state.page_load_error" class="native-load-error" role="alert" :title="state.page_load_error">{{ t('nativeBrowser.pageLoadFailed') }}</span>
          <span v-else-if="slowLoading && pageLoading" class="native-loading-status" role="status">{{ t('nativeBrowser.loadingSlow') }}</span>
          <div v-if="error" class="native-error" role="alert">{{ t('nativeBrowser.connectionFailed') }} <button data-testid="browser-recover" @click="recoverIdentity()">{{ t('nativeBrowser.reconnect') }}</button><details><summary>{{ t('nativeBrowser.connectionDetails') }}</summary>{{ error }}</details></div>
          <div v-if="frameIssue" class="native-frame-status" :class="{ unavailable: frameIssue === 'unavailable' }" :title="frameIssue === 'changing' ? t('nativeBrowser.frameUpdating') : frameIssue === 'view-only' ? t('nativeBrowser.frameViewOnly') : t('nativeBrowser.frameUnavailable', { message: frameError })" role="status">{{ frameIssue === 'changing' ? t('nativeBrowser.frameUpdatingShort') : frameIssue === 'view-only' ? t('nativeBrowser.frameViewOnlyShort') : t('nativeBrowser.frameUnavailableShort') }}</div>
          <div v-if="actionError || displayError" class="native-error" role="alert">{{ t('nativeBrowser.actionFailed', { message: actionError || displayError }) }}</div>
          <div v-if="showActivity" class="native-activity" :title="activityText" :class="state.dialog ? 'waiting' : activity?.phase" role="status"><span class="activity-dot" aria-hidden="true" /><span>{{ activityText }}</span></div>
          <div v-if="resetNotice !== null" class="native-reset" role="status">{{ t('nativeBrowser.reset') }} {{ resetNotice }}</div>
        </div>
        <div v-if="picking" class="native-pick-status" :class="{ failed: !!pickError }" :role="pickError ? 'alert' : 'status'" aria-live="polite">{{ pickError || t(pickPending ? 'nativeBrowser.pickPending' : !canPick ? 'nativeBrowser.pickUnavailable' : pickCommitting ? 'nativeBrowser.pickConfirming' : pickLoading ? 'nativeBrowser.pickLoading' : picked?.element?.fallback ? 'nativeBrowser.pickFallback' : 'nativeBrowser.pickHint') }}</div>
      </div>
      <div v-if="state.dialog && !closing" class="native-dialog-layer">
        <div class="native-dialog" role="alertdialog" :aria-label="t('nativeBrowser.dialog', { type: state.dialog.type })" @keydown.stop @keyup.stop @wheel.stop>
          <strong>{{ t('nativeBrowser.dialog', { type: state.dialog.type }) }}</strong>
          <p>{{ state.dialog.message }}</p>
          <input v-if="state.dialog.type === 'prompt'" v-model="promptText" :disabled="!controlReady" :aria-label="t('nativeBrowser.prompt')" />
          <div class="native-dialog-actions">
            <button :disabled="!controlReady" @click="input({ type: 'dialog', accept: false })">{{ t('nativeBrowser.dismiss') }}</button>
            <button :disabled="!controlReady" @click="input({ type: 'dialog', accept: true, text: promptText })">{{ t('nativeBrowser.accept') }}</button>
          </div>
          <small v-if="!owned">{{ t('nativeBrowser.dialogWait') }}</small>
        </div>
      </div>
      <div class="native-overlay" aria-hidden="true">
        <div v-if="pickRect" class="pick-mark" :style="rectStyle(pickRect)" data-testid="browser-pick-target"><span class="mark-label" :class="{ inside: pickRect.y < 22 }">{{ pickLabel }}</span></div>
        <div v-if="!picking && targetRect" class="target-mark" :style="rectStyle(targetRect)" data-testid="browser-target">
          <span class="mark-label" :class="{ inside: targetRect.y < 22 }">{{ t('nativeBrowser.target') }}</span>
        </div>
        <div v-if="!picking && focusRect" class="focus-mark" :class="{ editable: overlay?.focus?.editable }" :style="rectStyle(focusRect)" data-testid="browser-focus">
          <span class="mark-label" :class="{ inside: focusRect.y < 22 }">{{ t(overlay?.focus?.editable ? 'nativeBrowser.inputFocus' : 'nativeBrowser.keyboardFocus') }}</span>
        </div>
        <div v-if="ripple && displayRipple" :key="ripple" class="click-feedback" :style="pointerStyle(displayRipple)" data-testid="browser-click-feedback"><span class="click-wave" /></div>
        <div v-if="!picking && pointer" class="pointer-mark" :class="{ 'near-right': pointer.x > canvasSize.width - 75, 'near-bottom': pointer.y > canvasSize.height - 45 }" :style="pointerStyle(pointer)" data-testid="browser-pointer" data-actor="agent">
          <svg viewBox="0 0 20 24" width="20" height="24"><path d="M1 1v18l5-5 4 8 3-1-4-8h8z" /></svg>
          <span class="pointer-label">{{ t('nativeBrowser.actor.agent') }}</span>
        </div>
      </div>
      <aside v-if="diagnosticsOpen" class="native-performance" data-testid="browser-performance" :data-input-queue="diagnostics?.scheduler ? JSON.stringify(diagnostics.scheduler) : undefined" :aria-label="t('nativeBrowser.performanceTitle')" @keydown.stop @keyup.stop @wheel.stop>
        <header><strong>{{ t('nativeBrowser.performanceTitle') }}</strong><button type="button" :aria-label="t('nativeBrowser.performanceClose')" @click="diagnosticsOpen = false">×</button></header>
        <dl>
          <dt>{{ t('nativeBrowser.performanceInput') }}</dt><dd data-testid="browser-performance-input">{{ formatMetric(diagnostics?.input.p50_ms) }} / {{ formatMetric(diagnostics?.input.p95_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceQueue') }}</dt><dd>{{ formatMetric(diagnostics?.input.queue_ms) }} / {{ formatMetric(diagnostics?.input.dispatch_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceClientQueue') }}</dt><dd>{{ formatMetric(diagnostics?.scheduler.last_wait_ms) }} / {{ formatMetric(diagnostics?.scheduler.oldest_pending_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceInputQueue') }}</dt><dd>{{ diagnostics ? `${diagnostics.scheduler.pending} / ${diagnostics.scheduler.in_flight} / ${diagnostics.scheduler.window}` : '—' }}</dd>
          <dt>{{ t('nativeBrowser.performanceNetwork') }}</dt><dd>{{ formatMetric(diagnostics?.metrics.network_rtt_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceVideo') }}</dt><dd>{{ formatMetric(diagnostics?.metrics.fps, 'fps') }} · {{ formatMetric(diagnostics?.metrics.bitrate_mbps, 'Mbps', 2) }}</dd>
          <dt>{{ t('nativeBrowser.performanceCapture') }}</dt><dd>{{ formatMetric(typeof diagnostics?.server?.capture_fps === 'number' ? diagnostics.server.capture_fps : undefined, 'fps') }} / {{ formatMetric(typeof diagnostics?.server?.capture_min_fps === 'number' ? diagnostics.server.capture_min_fps : undefined, 'fps') }}</dd>
          <dt>{{ t('nativeBrowser.performanceCaptureMode') }}</dt><dd>{{ diagnostics?.server?.content_hint === 'motion' ? t('nativeBrowser.performanceMotion') : diagnostics?.server?.content_hint === 'text' ? t('nativeBrowser.performanceText') : '—' }} · {{ typeof diagnostics?.server?.capture_tuning_supported === 'boolean' ? t(diagnostics.server.capture_tuning_supported ? 'nativeBrowser.performanceCaptureAdaptive' : 'nativeBrowser.performanceCaptureFixed') : '—' }}</dd>
          <dt>{{ t('nativeBrowser.performanceResolution') }}</dt><dd>{{ diagnostics?.metrics.width && diagnostics.metrics.height ? `${diagnostics.metrics.width} × ${diagnostics.metrics.height}` : '—' }}</dd>
          <dt>{{ t('nativeBrowser.performanceDecode') }}</dt><dd>{{ formatMetric(diagnostics?.metrics.jitter_buffer_ms) }} / {{ formatMetric(diagnostics?.metrics.decode_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceCodec') }}</dt><dd data-testid="browser-performance-codec">{{ diagnostics?.server?.codec || '—' }}</dd>
          <dt>{{ t('nativeBrowser.performanceEncode') }}</dt><dd>{{ formatMetric(typeof diagnostics?.server?.encode_ms_per_frame === 'number' ? diagnostics.server.encode_ms_per_frame : undefined) }}</dd>
          <dt>{{ t('nativeBrowser.performanceTakeover') }}</dt><dd>{{ formatMetric(takeoverMs) }}</dd>
          <dt>{{ t('nativeBrowser.performanceFirstFrame') }}</dt><dd>{{ formatMetric(diagnostics?.first_frame_ms) }}</dd>
          <dt>{{ t('nativeBrowser.performanceScroll') }}</dt><dd>{{ diagnostics ? `${diagnostics.wheel.dispatched} / ${diagnostics.wheel.received}` : '—' }}</dd>
          <dt>{{ t('nativeBrowser.performanceTransport') }}</dt><dd>{{ diagnostics?.metrics.candidate_type === 'relay' ? t('nativeBrowser.performanceRelay') : diagnostics?.metrics.candidate_type ? t('nativeBrowser.performanceDirect') : '—' }} {{ diagnostics?.metrics.protocol?.toUpperCase() }}</dd>
        </dl>
        <p>{{ t('nativeBrowser.performanceHint') }}</p>
      </aside>
    </div>
    <div class="native-footer">
      <span class="native-hint">{{ t(showNewTab ? bookmarkStore.agentId ? 'nativeBrowser.newTabAgentFooter' : 'nativeBrowser.newTabFooter' : picking ? 'nativeBrowser.pickHint' : owned ? 'nativeBrowser.liveInputHint' : 'nativeBrowser.readonly') }}</span>
    </div>
  </section>
</template>

<style scoped>
.native-browser { display:flex; flex-direction:column; gap:8px; min-width:0; min-height:0; flex:1; padding:8px; overflow:auto; background:var(--el-bg-color); color:var(--el-text-color-primary); }
.native-text,.native-dialog-actions { display:flex; align-items:center; gap:6px; flex-wrap:wrap; }
button,input,textarea { font:inherit; color:inherit; background:var(--el-bg-color); border:1px solid var(--el-border-color); border-radius:5px; padding:6px 8px; min-width:0; }
button { cursor:pointer; font-size:12px; }
button:disabled,input:disabled { opacity:.5; cursor:default; }
button:not(:disabled):hover { border-color:var(--el-color-primary); color:var(--el-color-primary); }
button:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
.native-toolbar { display:flex; align-items:center; gap:6px; height:34px; flex:0 0 34px; min-width:0; }
.native-menu { position:relative; flex-shrink:0; z-index:6; }
.native-menu summary { cursor:pointer; list-style:none; font-size:13px; letter-spacing:1px; border-radius:5px; }
.native-menu summary::-webkit-details-marker { display:none; }
.native-menu-options { position:absolute; top:36px; right:0; padding:5px; min-width:130px; background:var(--el-bg-color-overlay); border:1px solid var(--el-border-color); border-radius:6px; box-shadow:0 4px 16px rgb(0 0 0 / 15%); }
.native-menu-options { width:230px; }
.native-menu-options button { display:flex; justify-content:space-between; gap:12px; width:100%; white-space:nowrap; border:0; text-align:left; }
.native-menu-heading { display:block; padding:5px 8px; font-size:11px; color:var(--el-text-color-secondary); }
.native-menu-options p { margin:6px 8px; font-size:11px; line-height:1.5; color:var(--el-text-color-secondary); }
.native-performance { position:absolute; top:10px; right:10px; z-index:5; width:min(330px, calc(100% - 20px)); max-height:calc(100% - 20px); overflow:auto; box-sizing:border-box; padding:12px; border:1px solid var(--el-border-color); border-radius:8px; background:var(--el-bg-color-overlay); box-shadow:0 4px 18px rgb(0 0 0 / 18%); font-size:11px; }
.native-performance header { display:flex; align-items:center; justify-content:space-between; font-size:12px; }
.native-performance header button { border:0; padding:0 4px; font-size:18px; }
.native-performance dl { display:grid; grid-template-columns:1fr auto; gap:7px 12px; margin:12px 0; }
.native-performance dt { color:var(--el-text-color-secondary); }.native-performance dd { margin:0; font-variant-numeric:tabular-nums; text-align:right; }
.native-performance p { color:var(--el-text-color-secondary); line-height:1.5; margin:0; }
.chrome-icon { width:32px; height:32px; padding:0; display:flex; align-items:center; justify-content:center; flex-shrink:0; border-color:transparent; background:transparent; color:var(--el-text-color-regular); }
.chrome-icon:not(:disabled):hover { background:var(--el-fill-color); border-color:transparent; }
.picker-toggle.selected { color:var(--el-color-primary); background:var(--el-color-primary-light-9); border-color:var(--el-color-primary-light-5); }
.picker-toggle { width:auto; padding:0 7px; gap:5px; }
.picker-label { white-space:nowrap; font-size:12px; }
.native-location { display:flex; align-items:center; gap:7px; flex:1; min-width:0; height:34px; padding:0 9px; border:1px solid var(--el-border-color-lighter); border-radius:7px; background:var(--el-fill-color-light); box-sizing:border-box; }
.native-location:focus-within { border-color:var(--el-color-primary); }
.native-location input { flex:1; width:0; min-width:0; padding:6px 0; border:0; outline:none; background:transparent; font-size:12px; line-height:20px; text-overflow:ellipsis; }
.native-location input:read-only { color:var(--el-text-color-regular); cursor:text; opacity:1; }
.connection-dot { width:6px; height:6px; flex-shrink:0; border-radius:50%; background:var(--el-text-color-placeholder); }
.connection-dot.online { background:var(--el-color-success); }.connection-dot.human { background:var(--el-color-primary); }.connection-dot.offline { background:var(--el-color-danger); }
.location-go { display:flex; align-items:center; justify-content:center; flex-shrink:0; width:24px; height:26px; padding:0; border:0; background:transparent; color:var(--el-color-primary); }
.native-page.new-tab { display:block; overflow:auto; }
.native-tabs { display:flex; gap:4px; overflow:auto; flex-shrink:0; scrollbar-width:thin; }
.native-tabs button { flex-shrink:0; max-width:200px; padding:4px 8px; text-overflow:ellipsis; overflow:hidden; white-space:nowrap; background:var(--el-fill-color-light); border-color:transparent; }
.native-tabs .selected { border-color:var(--el-color-primary); color:var(--el-color-primary); }
.canvas-feedback { position:absolute; top:10px; left:10px; right:10px; z-index:3; display:flex; align-items:flex-start; flex-direction:column; gap:6px; pointer-events:none; }
.native-activity { display:flex; align-items:center; gap:7px; max-width:100%; box-sizing:border-box; padding:6px 9px; border:1px solid var(--el-border-color-lighter); border-radius:6px; background:var(--el-bg-color-overlay); box-shadow:0 1px 5px rgb(0 0 0 / 10%); font-size:11px; line-height:1.4; color:var(--el-text-color-regular); }
.activity-dot { width:6px; height:6px; flex-shrink:0; border-radius:50%; background:var(--el-text-color-placeholder); }
.native-activity.started .activity-dot { background:var(--el-color-primary); }
.native-activity.failed { color:var(--el-color-danger); }.native-activity.failed .activity-dot { background:currentColor; }
.native-activity.waiting .activity-dot { background:var(--el-color-warning); }
.native-pick-status { max-width:100%; padding:6px 9px; box-sizing:border-box; border:1px solid var(--el-color-primary-light-5); border-radius:6px; background:var(--el-bg-color-overlay); color:var(--el-color-primary); font-size:11px; line-height:1.5; box-shadow:0 1px 5px rgb(0 0 0 / 10%); overflow-wrap:anywhere; }
.native-pick-status.failed { color:var(--el-color-danger); border-color:var(--el-color-danger-light-5); }
.native-input-capture { position:absolute; width:1px; height:20px; padding:0; border:0; border-radius:0; opacity:.01; pointer-events:none; resize:none; overflow:hidden; font-size:16px; caret-color:transparent; z-index:1; }
.native-hint { margin:0; font-size:11px; line-height:1.5; color:var(--el-text-color-secondary); }
.native-error,.native-reset { padding:7px 9px; max-width:100%; box-sizing:border-box; border:1px solid var(--el-color-danger-light-7); border-radius:6px; background:var(--el-color-danger-light-9); color:var(--el-color-danger); font-size:12px; line-height:1.5; overflow-wrap:anywhere; box-shadow:0 1px 5px rgb(0 0 0 / 10%); }
.native-frame-status,.native-load-error,.native-loading-status { padding:7px 9px; max-width:100%; box-sizing:border-box; border:1px solid var(--el-border-color); border-radius:6px; background:var(--el-bg-color-overlay); color:var(--el-text-color-regular); font-size:12px; line-height:1.5; overflow-wrap:anywhere; }
.native-frame-status.unavailable { border-color:var(--el-color-warning-light-7); color:var(--el-color-warning); }
.native-reset { border-color:var(--el-color-warning-light-7); background:var(--el-color-warning-light-9); color:var(--el-color-warning); }
.native-statusbar { display:flex; flex-direction:column; align-items:flex-start; gap:6px; min-width:0; max-width:100%; }
.native-activity>span:last-child { min-width:0; overflow-wrap:anywhere; }
.native-load-error { color:var(--el-color-danger); }
.connection-dot.loading,.tab-spinner { box-sizing:border-box; width:12px; height:12px; border:1.5px solid var(--el-border-color); border-top-color:var(--el-color-primary); border-radius:50%; background:transparent; animation:browser-spin .8s linear infinite; }
.tab-spinner { display:inline-block; vertical-align:-2px; margin-right:5px; }
@keyframes browser-spin { to { transform:rotate(360deg); } }
.native-dialog-layer { position:absolute; inset:0; z-index:4; display:flex; align-items:center; justify-content:center; padding:12px; box-sizing:border-box; pointer-events:none; }
.native-dialog { width:min(480px, 100%); max-height:100%; box-sizing:border-box; overflow:auto; padding:12px; border:1px solid var(--el-color-warning); border-radius:6px; background:var(--el-bg-color-overlay); box-shadow:0 4px 18px rgb(0 0 0 / 18%); font-size:13px; overflow-wrap:anywhere; pointer-events:auto; }
.native-dialog p { white-space:pre-wrap; }.native-dialog-actions { margin-top:8px; }
.native-page { flex:1; min-height:220px; background:var(--el-bg-color); display:flex; align-items:center; justify-content:center; position:relative; outline:none; overflow:hidden; border-radius:7px; }
.native-page:focus { box-shadow:inset 0 0 0 2px var(--el-color-primary); }
.native-page img,.native-page video,.native-held-frame { display:block; width:100%; height:100%; object-fit:contain; position:absolute; inset:0; user-select:none; cursor:default; touch-action:none; }
.native-held-frame { pointer-events:none; }
.native-page.readonly img { cursor:default; }
.native-page.picking img { cursor:crosshair; touch-action:none; }
.native-overlay { position:absolute; inset:0; z-index:2; pointer-events:none; overflow:hidden; }
.target-mark,.focus-mark,.pick-mark { position:absolute; box-sizing:border-box; border-radius:4px; }
.pick-mark { border:2px solid #2974e8; background:rgb(41 116 232 / 12%); box-shadow:0 0 0 1px rgb(255 255 255 / 80%); }
.pick-mark .mark-label { color:#fff; background:#2974e8; max-width:min(260px, 65vw); overflow:hidden; text-overflow:ellipsis; }
.target-mark { border:1px solid rgb(76 139 245 / 75%); color:#fff; background:rgb(76 139 245 / 7%); box-shadow:0 0 0 2px rgb(255 255 255 / 35%); }
.focus-mark { border:2px solid #15966b; box-shadow:0 0 0 2px rgb(255 255 255 / 75%); color:#fff; }
.focus-mark:not(.editable) { border-style:dashed; }
.mark-label { position:absolute; left:-2px; bottom:calc(100% + 3px); padding:2px 5px; border-radius:3px; font:500 10px/1.3 system-ui; white-space:nowrap; background:#f1b44c; }
.focus-mark .mark-label { background:#168461; }.mark-label.inside { top:3px; bottom:auto; left:3px; }
.target-mark .mark-label { color:#fff; background:#2974e8; }
.pointer-mark { position:absolute; left:0; top:0; color:#2974e8; width:20px; height:24px; will-change:transform; }
.pointer-mark svg { display:block; filter:drop-shadow(0 1px 2px rgb(0 0 0 / 45%)); }
.pointer-mark path { fill:currentColor; stroke:#fff; stroke-width:1.4px; stroke-linejoin:round; }
.pointer-label { position:absolute; left:16px; top:16px; background:currentColor; border-radius:4px; padding:2px 5px; font:500 10px/1.3 system-ui; white-space:nowrap; }
.pointer-label { color:#fff; background:#2974e8; box-shadow:0 1px 5px rgb(0 0 0 / 20%); }
.pointer-mark.near-right .pointer-label { left:auto; right:15px; }.pointer-mark.near-bottom .pointer-label { top:-21px; }
.click-feedback { position:absolute; left:0; top:0; color:#2974e8; width:1px; height:1px; }
.click-wave { position:absolute; left:0; top:0; width:24px; height:24px; margin:-12px; box-sizing:border-box; border:2px solid currentColor; background:rgb(41 116 232 / 14%); border-radius:50%; animation:click-wave .42s ease-out forwards; }
.native-footer { display:flex; align-items:flex-start; gap:12px; }.native-footer>.native-hint { flex:1; }
@keyframes click-wave { from { transform:scale(.25); opacity:1; } to { transform:scale(2); opacity:0; } }
@keyframes activity-pulse { 50% { opacity:.35; } }
@media (max-width:600px) { .native-browser { gap:6px; padding:6px; }.native-toolbar { gap:4px; }.native-location { gap:6px; padding:0 7px; }.native-footer { flex-wrap:wrap; }.native-text textarea { min-width:90px; }.target-mark .mark-label { display:none; } }
@media (prefers-reduced-motion:reduce) { .click-wave { animation:none; opacity:.55; }.connection-dot.loading,.tab-spinner { animation:none; border-right-color:var(--el-color-primary); } }
</style>
