<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, useId, watch, type Ref } from "vue";
import { useI18n } from "vue-i18n";
import ApprovalBar from "@/components/ApprovalBar.vue";
import UserInputBar from "@/components/UserInputBar.vue";
import { approvalUiKey } from "@/agent/approvalState";
import { provideCardDisclosure } from "@/agent/cardDisclosure";
import { provideToolIdentityCatalog } from "@/agent/toolIdentity";
import { provideActivityReading, activityContainsSelection } from "@/agent/activityReading";
import { createActivityExecutionLedger } from "@/agent/activityExecution";
import { provideActivityExecution } from "@/agent/activityExecutionContext";
import { useActivityAttention } from "@/agent/activityAttention";
import { useResizeObserver } from "@vueuse/core";
import { ElMessageBox } from "element-plus";
import {
  AlarmClock,
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  CaretRight,
  Check,
  CircleCloseFilled,
  Close,
  CopyDocument,
  Delete,
  Download,
  EditPen,
  Expand,
  Fold,
  Folder,
  FolderOpened,
  Grid,
  Loading,
  MagicStick,
  MoreFilled,
  RefreshLeft,
  Search,
  Share,
  Top,
  Upload,
} from "@element-plus/icons-vue";
import DocumentsIcon from "@/components/DocumentsIcon.vue";
import ShortcutReference from "@/components/ShortcutReference.vue";
import InformationEntryDialog from "@/components/InformationEntryDialog.vue";
import { resolveInformationEntry, type InformationEntry } from "@/api/information";
import { informationComposerParts, informationInsertionCurrent } from "@/agent/informationComposer";
import { shortcutAssetsApi, resolveStarterShortcut, type ShortcutSnapshot } from "@/api/shortcuts";
import { cloneInputParts, inputPartsHaveFiles, inputPartsSkills, inputPartsMcps, validateInputParts } from "@/agent/shortcutStructure";
import StarterCardsDialog from "@/components/StarterCardsDialog.vue";
import StarterCardsTrigger from "@/components/StarterCardsTrigger.vue";
import { cloneStarterDraft, starterCardComposerParts, type StarterDraft, type PreparedStarterDraft } from "@/components/starterCardDraft";
import { captureStarterFiles, prepareStarterFileParts, type StarterDraftFile } from "@/agent/starterDraftFiles";
import UploadIcon from "@/components/UploadIcon.vue";
import FolderUploadIcon from "@/components/FolderUploadIcon.vue";
import { immersiveWorkspaceAvailable, officeRevealStillSelected, type OfficeRevealRequest, type OfficeViewContext } from "@/agent/officeImmersion";
import { isAgentWorkflowWorkspace, shouldRevealCreatedAgentWorkflow } from "@/agent/agentWorkflowWorkspace";
import { agentWorkflowUpdatesKey } from "@/agent/agentWorkflowUpdates";
import { attachmentWorkspacePath, isEditableOfficeFile, sameWorkspaceFile } from "@/agent/officeFiles";
import AskFileTree from "@/components/AskFileTree.vue";
import AskTreeNode from "@/components/AskTreeNode.vue";
import AskUploadCard from "@/components/AskUploadCard.vue";
import TaskNotice from "@/components/TaskNotice.vue";
import ElementCard from "@/components/ElementCard.vue";
import ElementPageIcon from "@/components/ElementPageIcon.vue";
import RailGlyph from "@/components/RailGlyph.vue";
import { packRail, type RailEntry } from "@/agent/railPacking";
import WorkLog from "@/components/WorkLog.vue";
import ConversationInbox from "@/components/ConversationInbox.vue";
import { createInboxDraftLedger, inboxDraftParts } from "@/agent/conversationInbox";
import type { InboxItem } from "@/api/types";
import DescribeCards from "@/components/DescribeCards.vue";
import SessionPulse from "@/components/SessionPulse.vue";
import TodoBar from "@/components/TodoBar.vue";
import ManagedWebServices from "@/components/ManagedWebServices.vue";
import { webServiceLink } from "@/agent/webServiceLinks";
import { useWebServiceLinkReveal, type WebServiceRevealHandle } from "@/agent/useWebServiceLinkReveal";
import type { OpenWebServiceRequest } from "@/api/webServices";
import { isWebServiceRecord } from "@/api/webServices";
import { websiteBrowserRequestCurrent, type WebsiteBrowserRequest } from "@/agent/browserPreviewNavigation";
import TaskTodoCard from "@/components/TaskTodoCard.vue";
import ContextMeter from "@/components/ContextMeter.vue";
import CompactionCard from "@/components/CompactionCard.vue";
import ImmersiveChatControls from "@/components/ImmersiveChatControls.vue";
import MinimizedSessionBar from "@/components/MinimizedSessionBar.vue";
import DockArea from "@/components/DockArea.vue";
import { agentWorkflowNavigationKey, validAgentWorkflowId, type AgentWorkflowLocation } from "@/agent/agentWorkflowNavigation";
import { officeComposerPart } from "@/agent/composerParts";
import { builtinSkillDescription } from "@/agent/builtinToolCopy";
import type { OfficeReferenceSnapshot } from "@/api/types";
import OfficeReference from "@/components/OfficeReference.vue";
import { shortcutDraftSourceKey } from "@/agent/shortcutDraftSource";
import Unreachable from "@/components/Unreachable.vue";
import TokenGate from "@/components/TokenGate.vue";
import ModelPicker from "@/components/ModelPicker.vue";
import PolicyPicker from "@/components/PolicyPicker.vue";
import SlashCommandMenu from "@/components/SlashCommandMenu.vue";
import { composerSuggestions, composerSuggestionAction, moveComposerSuggestion, type ComposerMcpMethod } from "@/agent/composerSuggestions";
import { composerSlashQuery, completeComposerTextSkill, completeComposerTextMcp, type ComposerSlashQuery } from "@/agent/composerSlash";
import { parseMcpInvocation, validMcpReference } from "@/agent/mcpInvocation";
import ComposerToken from "@/components/ComposerToken.vue";
import InlineComposer from "@/components/InlineComposer.vue";
import OrderedMessageContent from "@/components/OrderedMessageContent.vue";
import { composerDisplayText, composerOccurrenceID, composerTypedText, elementComposerPart, fileComposerPart, shortcutComposerPart, normalizeComposerParts, replaceComposerTypedText, type ComposerPart, type ComposerReferencePart } from "@/agent/composerParts";
import { blocksComposerParts, composerInputParts, inputPartsBlocks, orderedDisplayParts, pickFromDisplay, snapshotComposerElementIcons } from "@/agent/orderedInput";
import { canonicalUserSkillText, composerDraftWithToken, normalizeComposerTokenInput, readComposerToken, readMcpComposerToken, replaceComposerToken, skillMessageBlock } from "@/agent/composerTokens";
import { createContextUsageReader } from "@/agent/contextUsage";
import SkillBlock from "@/components/SkillBlock.vue";
import WorkspacePicker from "@/components/WorkspacePicker.vue";
import AnswerFilePicker from "@/components/AnswerFilePicker.vue";
import BackgroundTasksButton, { type BackgroundTaskSelection } from "@/components/BackgroundTasksButton.vue";
import BackgroundShellTasks from "@/components/BackgroundShellTasks.vue";
import ThemePicker from "@/components/ThemePicker.vue";
import TaskCenterDialog from "@/components/TaskCenterDialog.vue";
import { conversationPlanDraft, messagePlanDraft, type ConversationPlanDraft } from "@/agent/scheduleConversation";
import { taskCenterApi } from "@/api/taskCenterApi";
import { taskCenterNeedsAttention } from "@/agent/taskCenterPresentation";
import ShareDialog from "@/components/ShareDialog.vue";
import CodeBlock from "@/components/CodeBlock.vue";

// The spreadsheet viewer for the chip quick-look's xlsx mode: same async
// chunk FilePreview uses, so a spreadsheet click is the only thing that ever
// pays for exceljs.
const VueOfficeExcel = defineAsyncComponent(async () => {
  await import("@vue-office/excel/lib/index.css");
  const mod = await import("@vue-office/excel");
  return mod.default ?? mod;
});
import Logo from "@/components/Logo.vue";
import { Icon } from "@iconify/vue";
import { baseName, fileIcon, folderIcon, messageIcon, sessionIcon } from "@/components/fileIcons";
import { attachTree, type AttachFile } from "@/agent/attachTree";
import type { UploadCardState } from "@/agent/uploadCard";
import { useAgentStream, type ImageDescribe } from "@/agent/useAgentStream";
import { useImmersiveBrowser } from "@/agent/useImmersiveBrowser";
import { prepareImage } from "@/agent/attachImage";
import { BrowserImmersionPolicy, setEnsurePaneHook, type BrowserRevealRequest } from "@/agent/browserView";
import { BrowserNewOperations, browserControlMemory, browserOperationRetrySame, browserSheetID, browserSheetVisible, completeBrowserState, installBrowserLease, observeBrowserControl, reconcileBrowserSheets, reconciledBrowserSelection, retireBrowserControls, targetMatches } from "@/agent/browserSheets";
import { BrowserFocusPolicy, setBrowserFocusHook } from "@/agent/browserFocus";
import { safeSourceURL } from "@/agent/citePreview";
import { BrowserTabClosures } from "@/agent/browserTabClose";
import type { NativeBrowserControl, NativeBrowserState } from "@/agent/nativeBrowser";
import { citeFaviconKey, createCiteFavicons } from "@/agent/citeFavicon";
import { browserBookmarkAgentKey } from "@/agent/accountBrowserBookmarks";
import { BROWSER_VIEW_ID, migrateBrowserStorage, readBookmarks } from "@/agent/browserBookmarks";
import { buildTimeline, formatDuration, isTaskNotification, parseTaskNotification, projectTaskTodos, subagentLiveness } from "@/agent/timeline";
import { describeSource, numberCitations, rewriteCitations } from "@/agent/cite";
import { citationFileTarget, informationCitationTarget, citeNavigationKey } from "@/agent/citeNavigation";
import { workspaceFileNavigationKey, type WorkspaceFileScope } from "@/agent/workspaceFileNavigation";
import { webArtifactNavigationKey } from "@/agent/webArtifactNavigation";
import type { WorkspaceFileTarget } from "@/agent/workspaceFileLinks";
import { createAnswerFileNavigation } from "@/agent/answerFileNavigation";
import { requestedSentinelId, sentinelDeepLink, sentinelTab } from "@/agent/sentinelNavigation";
import { sentinelSessionsApi } from "@/api/sentinelSessions";
import { buildConversationExchanges, type Exchange } from "@/agent/exchanges";
import { announcement } from "@/agent/announcer";
import { invalidateIndex, invalidateTree } from "@/components/fileTreeStore";
import {
  closeTab,
  migrateSheet,
  openTab,
  PREVIEW_PREFIX,
  SKILL_PREFIX,
  HTML_PREFIX,
  WEBSITE_PREFIX,
  restoreTabs,
  SHEET_KEY,
  SUBAGENT_PREFIX,
  TABS_KEY,
  TENANT_KEY,
  type DockTab,
  type OpenFileTarget,
  type OpenSheetRequest,
  type StoredTabs,
} from "@/components/dockSheets";
import StarterCards from "@/components/StarterCards.vue";
import FollowupChips from "@/components/FollowupChips.vue";
import { planIntent } from "@/agent/composerIntent";
import { parsePickedCached, type ElementPick } from "@/agent/elementPicker";
import { baseName as dirBaseName, fmtRel } from "@/agent/fmtRel";
import { pickFollowups } from "@/agent/followupPick";
import { trackFollowup } from "@/agent/followupTelemetry";
import { applySearchHighlight, clearSearchHighlight, findAnchorMessages, firstHighlightRangeIn } from "@/agent/searchHighlight";
import { worklogRevealId } from "@/agent/worklogPrefs";
import { api, token, sessionExportMarkdownURL } from "@/api/client";
import { isDelegatedEmbed, getDelegatedUi } from "@/api/delegated";
import { emptyLive } from "@/api/types";
import type {
  Block,
  InputLayout,
  MessageInputPart,
  CiteSource,
  ModelInfo,
  PanelInfo,
  PolicyMode,
  SearchBlock,
  SessionHit,
  SessionInfo,
  SkillInfo,
  StarterCard,
  Starters,
} from "@/api/types";
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale } from "@/i18n";
import type { Locale } from "@/i18n";
import { navigateStudio, platformApi } from "@/api/platform";
import type { PlatformAgent, PlatformAgentInput } from "@/agent/platformTypes";
import { starterToWire } from "@/agent/studioPresentation";
import { canSelectStudioModel, studioSessionModels, type StudioSessionCapabilities } from "@/agent/studioSessionCapabilities";
import { createStudioToolSync } from "@/agent/studioToolSync";
import { studioCapabilityFingerprint, syncStudioMcpCapabilities } from "@/agent/studioCapabilitySync";
import StudioToolPolicyNotice from "@/components/StudioToolPolicyNotice.vue";
import StudioEntrySkeleton from "@/components/studio/StudioEntrySkeleton.vue";
import { createStudioEntryState } from "@/agent/studioEntry";
import { STUDIO_LAST_SESSION, chooseBootSession, requestedSessionId, studioKey, studioPath, studioStarters } from "@/agent/studioContext";

// Studio previews and published links mount this same Pogo workbench. The
// classic "/" workbench passes no Agent binding. The session list and
// the browser's session/dock memory are scoped to the one bound Agent (see
// agent/studioContext.ts for the persistence policy); the workspace itself —
// files, dock panels and terminal — remains the signed-in user's shared one.
// Models, Skills and MCP are resolved from each conversation's Agent binding.
const props = defineProps<{ studioAgentId?: string; managedChat?: boolean; embedded?: boolean }>();
const studioAgentId = props.studioAgentId?.trim() ?? "";
provide(browserBookmarkAgentKey, studioAgentId);
// Constant for the life of the instance: StudioShell keys the component by
// agentId, so switching Agents remounts rather than mutating this prop.
const isStudio = studioAgentId !== "";
const managedChat = isStudio && props.managedChat === true;
const embedded = isStudio && !managedChat && props.embedded === true;
const conversationPath = (sid?: string | null) => studioPath(studioAgentId, sid, { managedChat, embedded });
const studioAgent = ref<PlatformAgent | null>(null);
// Boot failure that must surface as a page, not silently fall back to the
// generic workbench: Agent unavailable/draft/forbidden. Auth failures instead
// go through the ordinary gate (tokenRejected) below.
const studioError = ref<string | null>(null);
// Iframes remember their compact layout independently from the full workbench.
// Each surface still restores only sessions owned by this same Agent.
const storeKey = (base: string) => isStudio
  ? studioKey(studioAgentId, embedded ? `pogo:embed:${base.replace(/^pogo:/, "")}` : base)
  : base;

const stream = useAgentStream();
provide(agentWorkflowUpdatesKey, stream.agentWorkflowUpdate);
provideCardDisclosure(() => stream.displaySessionId.value ?? undefined);
const { t, d, locale } = useI18n();
const currentLocale = computed(() => locale.value as Locale);
const delegatedUi = ref<Awaited<ReturnType<typeof getDelegatedUi>> | null>(null);
const delegatedUiError = ref<string | null>(null);
let delegatedBootEpoch = 0;
let viewClosed = false;
type OptionalUiFeature = "conversation" | "settings" | "workspaces" | "terminal" | "attachments" | "schedules" | "sharing" | "model_selection";
function canUse(feature: OptionalUiFeature): boolean {
  return !isDelegatedEmbed || delegatedUi.value?.capabilities[feature] === true;
}
const unavailableTip = computed(() => t("uiCoverage.app.unavailable"));
function unsupported(): void {
  showFlash(unavailableTip.value, "error");
}

const sessions = ref<SessionInfo[]>([]);
const models = ref<ModelInfo[]>([]);
const studioCapabilities = ref<StudioSessionCapabilities | null>(null);
const studioCapabilitiesError = ref("");
const studioModelSyncing = ref(false);
const studioModelDeferred = ref(false);
let studioCapabilitiesRequest = 0;
const studioToolSync = createStudioToolSync(sid => api.syncStudioToolConfig(studioAgentId, sid));
const studioToolState = studioToolSync.state, studioToolSyncing = studioToolSync.syncing, studioToolRevision = studioToolSync.catalogVersion;
const studioCapabilityRevision = ref(0);
let studioCapabilitySnapshot = "";
const skillConfigError = ref("");
const studioToolPending = computed(() => studioToolState.value?.pending === true || skillConfigDeferred.value
  || studioCapabilities.value?.skills_pending === true || studioCapabilities.value?.mcp_pending === true);
const studioConfigurationError = computed(() => skillConfigError.value || studioCapabilitiesError.value || studioToolState.value?.error || "");
const studioConfigurationSyncing = computed(() => studioToolSyncing.value || studioModelSyncing.value || skillCatalogState.value === "loading");
const pickerModels = computed(() => isStudio ? studioSessionModels(models.value, studioCapabilities.value) : models.value);
const studioModelTip = computed(() => t(studioCapabilities.value ? "uiCoverage.app.singleModel" : "uiCoverage.app.loadingModels"));
const cwd = ref("");
const current = ref<string | null>(null);
const studioEntryResolving = ref(isStudio || isDelegatedEmbed);
const studioEntry = createStudioEntryState({
  enabled: isStudio || isDelegatedEmbed,
  selected: current,
  displayed: stream.displaySessionId,
  resolving: studioEntryResolving,
  switching: stream.switching,
});
const studioEntryLoading = studioEntry.loading;
const studioEmptyReady = studioEntry.emptyReady;
const initialWebsiteLink = webServiceLink(location.search);
const managedWebsites = ref<WebServiceRevealHandle | null>(null);
let websiteOpenSequence = 0;
provideToolIdentityCatalog(() => stream.displaySessionId.value ?? undefined, () => `${studioToolRevision.value}:${studioCapabilityRevision.value}`);
useActivityAttention(() => ({
  sessionId: stream.displaySessionId.value ?? undefined,
  enabled: !stream.switching.value,
  approvalIds: stream.live.value.pending_gates.map(gate => gate.gate_id),
  answerIds: stream.live.value.pending_user_inputs.map(question => question.request_id),
}));
const NEW_SESSION_DRAFT_KEY = "__new_session__";
const pendingWorkspace = ref<string | null>(localStorage.getItem(storeKey("pogo:new-session-workspace")));
const draftPolicyMode = ref<PolicyMode>("standard");
const inboxChanges = ref<string[]>([]);
const inboxErrors = ref<Record<string, string>>({});
// Keep the same id after an uncertain HTTP result: retry must not duplicate a
// message already accepted by the server. Snapshot/SSE owns the visible queue.
const inboxDrafts = createInboxDraftLedger();
watch(current, () => { inboxChanges.value = []; inboxErrors.value = {}; });
function restoreWithdrawnDrafts() {
  if (!current.value || stream.switching.value) return;
  const drafts = inboxDrafts.restoredMessages(current.value, stream.inbox.value);
  for (const draft of drafts) {
    if (draft.parts) {
      const restored = inboxDraftParts(draft);
      updateComposerParts([...composerParts.value, ...(composerParts.value.length ? [{ type: "text" as const, text: "\n" }] : []), ...restored]);
      composerLayout.value = draft.input_layout ?? "inline";
    } else input.value = [input.value, draft.text].filter(Boolean).join("\n");
  }
}
watch([current, () => stream.inbox.value, () => stream.switching.value], restoreWithdrawnDrafts);
const input = ref("");
const sending = ref(false);
const scroller = ref<HTMLElement | null>(null);
const inputBox = ref<HTMLTextAreaElement | null>(null);
const inlineComposer = ref<InstanceType<typeof InlineComposer> | null>(null);
const composerLayout = ref<InputLayout>("top");
// Textarea and inline editor are two views over the same ordered document.
const composerParts = ref<ComposerPart[]>([]);
let syncingComposer = false;

function focusComposer() {
  if (composerLayout.value === "inline" && composerToken.value?.kind !== "command") inlineComposer.value?.focus();
  else inputBox.value?.focus();
}

// Attachments waiting in the composer. A chip is LOCAL until send: dropping
// a file on the composer never touches the workspace — the bytes sit in
// `file` (images already downscaled by prepareImage) and the upload happens
// as part of send(), which is when `path` (and uploads/) comes to exist.
// kind comes from the filename while local and from the server's
// classification after the upload; it picks the chip's icon.
interface Attachment {
  key: string;
  name: string;
  kind: string;
  status: "uploading" | "ready" | "error";
  error?: string;
  path?: string;
  file?: Blob;
  preview?: string;
  // Folder upload: the tree is ONE chip no matter how many files it holds.
  // Members carry their own bytes plus the subdir they land under (relative
  // to the drop root); `folder` is the chip's live progress while send()
  // uploads them. Members that already carry a path were uploaded by an
  // earlier send attempt that failed afterwards — a retry sends only the
  // rest, matching how plain chips keep their path. Rewind-restored members
  // carry ONLY a path (the bytes stayed on the server); send() skips them
  // the same way.
  members?: { file?: File; name: string; dir: string; path?: string }[];
  folder?: { total: number; done: number; failed: number; skipped: number };
}
const attachments = ref<Attachment[]>([]);
// Undo may restore a local file after its last visible occurrence was removed.
const composerFileCatalog = new Map<string, Attachment>();
interface ComposerDraftSnapshot {
  input: string; parts: ComposerPart[]; layout: InputLayout;
  attachments: Attachment[]; picks: ElementPick[]; catalog: Map<string, Attachment>;
}
interface StoredComposerDraftSnapshot extends Omit<ComposerDraftSnapshot, "catalog"> { catalog: [string, Attachment][] }
const composerDraftStorageKey = storeKey("pogo:composer-drafts");
function readComposerDrafts(): Map<string, ComposerDraftSnapshot> {
  try {
    const parsed = JSON.parse(localStorage.getItem(composerDraftStorageKey) ?? "{}") as Record<string, StoredComposerDraftSnapshot>;
    return new Map(Object.entries(parsed).map(([key, draft]) => [key, { ...draft, catalog: new Map(draft.catalog ?? []) }]));
  } catch { return new Map(); }
}
const composerDrafts = readComposerDrafts();
let composerDraftTimer: ReturnType<typeof setTimeout> | undefined;
function activeDraftKey(): string | null { return current.value ?? (pendingWorkspace.value !== null ? NEW_SESSION_DRAFT_KEY : null); }
function durableAttachment(a: Attachment): Attachment | null {
  if (!a.path && !a.members?.some(member => member.path)) return null;
  const durable = { ...a };
  delete durable.file;
  delete durable.preview;
  if (durable.members) durable.members = durable.members.filter(member => member.path).map(member => {
    const copy = { ...member };
    delete copy.file;
    return copy;
  });
  return durable;
}
function durableDraft(draft: ComposerDraftSnapshot): StoredComposerDraftSnapshot {
  const attachments = draft.attachments.map(durableAttachment).filter((a): a is Attachment => a !== null);
  const catalog = [...draft.catalog.entries()].flatMap(([key, file]) => {
    const durable = durableAttachment(file);
    return durable ? [[key, durable] as [string, Attachment]] : [];
  });
  const keys = new Set(catalog.map(([key]) => key));
  return {
    input: draft.input,
    parts: draft.parts.filter(part => part.type !== "file" || keys.has(String(part.key))),
    layout: draft.layout,
    attachments,
    picks: draft.picks,
    catalog,
  };
}
function persistComposerDrafts() {
  if (composerDraftTimer !== undefined) { clearTimeout(composerDraftTimer); composerDraftTimer = undefined; }
  try {
    const stored = Object.fromEntries([...composerDrafts.entries()].map(([key, draft]) => [key, durableDraft(draft)]));
    localStorage.setItem(composerDraftStorageKey, JSON.stringify(stored));
  } catch { /* storage quota/privacy mode: keep the in-memory draft */ }
}
function saveComposerDraft(key = activeDraftKey()) {
  if (!key) return;
  composerDrafts.set(key, { input: input.value, parts: normalizeComposerParts(composerParts.value), layout: composerLayout.value, attachments: [...attachments.value], picks: [...elementPicks.value], catalog: new Map(composerFileCatalog) });
  if (composerDraftTimer !== undefined) clearTimeout(composerDraftTimer);
  composerDraftTimer = setTimeout(persistComposerDrafts, 150);
}
function restoreComposerDraft(key = activeDraftKey()) {
  const draft = key ? composerDrafts.get(key) : undefined;
  syncingComposer = true;
  try {
    input.value = draft?.input ?? "";
    composerParts.value = normalizeComposerParts(draft?.parts ?? []);
    composerLayout.value = draft?.layout ?? "top";
    attachments.value = [...(draft?.attachments ?? [])];
    elementPicks.value = [...(draft?.picks ?? [])];
    composerFileCatalog.clear();
    for (const [fileKey, file] of draft?.catalog ?? []) composerFileCatalog.set(fileKey, file);
  } finally { syncingComposer = false; }
}
const attachInput = ref<HTMLInputElement | null>(null);
const folderInput = ref<HTMLInputElement | null>(null);
// The attach menu's open state: the single attach button asks "file or
// folder?" here before tripping the matching hidden input below.
const attachMenu = ref(false);
// The floating workspace-attachments popover's open state: a row click closes
// it, because the click's meaning was "open this in the dock", not "keep the
// list up over the thing it just opened".
const attachPop = ref(false);
// Optimistic bubbles' upload waiting states, keyed by the message's client
// id: while an entry lives, the bubble renders the bytes-weighted upload card
// (AskUploadCard) instead of attachments that don't have server paths yet.
// An entry dies with its send — success swaps the real blocks in, failure
// drops the bubble and hands the draft back to the composer.
const uploadStates = reactive<Record<string, UploadCardState>>({});
// Chips that can ride the next send: prepared locally (file), already
// uploaded by an earlier send attempt that failed afterwards (path), or a
// folder chip with at least one member not yet uploaded. Error chips stay
// out — the user fixes or removes them first.
const sendableAttachments = computed(() =>
  attachments.value.filter(
    (a) => a.status !== "error" && (a.file || a.path || (a.members?.length ?? 0)),
  ),
);

// The session's workspace attachments, newest last, deduped by path: the
// floating button above the composer opens onto this list (the Gemini
// reading — a quiet handle on everything this conversation has accumulated).
// Two witnesses, unioned: the stream's uploads list (server-attributed, so a
// file uploaded but never sent still counts) and the timeline's message
// blocks (which covers sessions written before the ledger existed).
const sessionAttachments = computed(() => {
  const seen = new Set<string>();
  const out: { path: string; name: string; image: boolean }[] = [];
  const push = (path: string, name: string, image: boolean) => {
    if (seen.has(path)) return;
    seen.add(path);
    out.push({ path, name, image });
  };
  for (const u of stream.uploads.value) push(u.path, u.name, u.kind === "image");
  for (const it of timeline.value) {
    if (it.kind !== "user") continue;
    for (const p of it.images ?? []) push(p, p.split("/").pop() || p, true);
    for (const f of it.files ?? []) push(f.path, f.name, false);
  }
  return out;
});

// The popover's shape: the same trie the sent bubbles build from their files[]
// (agent/attachTree), fed the session's whole attachment list, so a folder
// upload keeps the folder it was uploaded as. The hasDir rule is the bubble's
// — a list of loose files has no tree to show and keeps its flat rows.
const sessionTree = computed(() => attachTree(sessionAttachments.value));

// workspaceFileUrl builds the raw-bytes URL for a workspace-relative path
// (attachment images in bubbles and the floating panel's thumbnails).
function workspaceFileUrl(path: string): string {
  const ws = currentWorkspace.value;
  return api.attachmentUrl(ws ? `${ws}/${path}` : path);
}

// The workspace-attachments popover and the bubbles' file cards open their
// file as a preview tab in the dock's browser area — the way a browser opens a
// PDF or an image as a tab. The path is timeline-shaped (workspace-relative),
// so the session's workspace is rejoined the same way workspaceFileUrl does;
// the id carries the full path, which is what makes a second click focus the
// tab instead of stacking a duplicate.
let cancelPendingAnswerFile = () => {};
let cancelPendingSentinelNavigation = () => {};
let cancelPendingTaskNavigation = () => {};
function openAttachmentPreview(a: OpenFileTarget & { lineStart?: number; lineEnd?: number; citation?: boolean }) {
  cancelPendingAnswerFile();
  if (!canUse("attachments")) return unsupported();
  browserFocusPolicy.manual();
  if (a.skill) {
    const sessionId = current.value;
    if (!sessionId) return;
    const path = a.skillPath || "SKILL.md";
    const id = `${SKILL_PREFIX}${sessionId}:${a.skill}:${path}`;
    hubTabs.value = openTab(hubTabs.value, {
      id,
      label: a.name,
      kind: "skill",
      skill: a.skill,
      path,
      parentSession: sessionId,
      closable: true,
    });
    hubActiveTab.value = id;
    hubAt.value = null;
    activeSheet.value = "hub";
    attachPop.value = false;
    return;
  }
  const ws = currentWorkspace.value;
  const full = attachmentWorkspacePath(ws, a.path);
  const existing = hubTabs.value.find(tab => tab.kind === "preview" && sameWorkspaceFile(tab.path, full));
  const id = existing?.id || `${PREVIEW_PREFIX}${full}`;
  hubTabs.value = openTab(hubTabs.value, {
    id,
    label: a.name,
    kind: "preview",
    path: full,
    citation: a.citation,
    lineStart: a.lineStart,
    lineEnd: a.lineEnd,
    closable: true,
  });
  hubActiveTab.value = id;
  hubAt.value = null;
  activeSheet.value = "hub";
  attachPop.value = false;
}

// askTreeHasDir picks the bubble's attachment rendering: the tree only earns
// its keep when the entries' paths hold a directory (a folder upload); a
// message of loose files keeps the flat chips. attachTree memoizes on the
// files[] reference, so asking twice per render (chips guard + tree mount)
// builds the trie once.
function askTreeHasDir(files?: AttachFile[]): boolean {
  return files?.length ? attachTree(files).hasDir : false;
}

// A composer chip's quick look, rendered from the chip's own bytes — nothing
// has left for the workspace yet, so the preview is local by construction:
// object URLs for images and PDFs, FileReader text for code and data, the
// spreadsheet component for xlsx. docx has no client-side renderer here and
// says so instead.
interface ChipPreviewEntry {
  name: string;
  ext: string;
  url?: string; // image or pdf
  urlOwned?: boolean; // pdf: created for the dialog, revoked on close
  text?: string; // text-ish files
  bytes?: ArrayBuffer; // xlsx, for the spreadsheet component
  unsupported?: boolean;
}
const chipPreview = ref<ChipPreviewEntry | null>(null);
const chipPreviewOpen = computed({
  get: () => chipPreview.value !== null,
  set: (v) => {
    if (!v) closeChipPreview();
  },
});
function closeChipPreview() {
  if (chipPreview.value?.urlOwned && chipPreview.value.url) URL.revokeObjectURL(chipPreview.value.url);
  chipPreview.value = null;
}
const TEXT_PREVIEW_EXTS = new Set([
  "txt", "md", "json", "xml", "html", "htm", "yaml", "yml", "log", "ini",
  "conf", "py", "go", "js", "ts", "sh", "sql", "csv", "tsv",
]);
async function previewAttachment(a: Attachment) {
  if (!canUse("attachments")) return unsupported();
  // A staged chip may retain local bytes after uploading. Its persisted Office
  // file is still the one editable document, never another local grid copy.
  if (a.path && isEditableOfficeFile(a.path)) { openAttachmentPreview({ path: a.path, name: a.name }); return; }
  // A rewind-restored chip has a path but no local bytes: its preview is the
  // dock's preview tab, the same door the bubble's file cards use.
  if (!a.file) {
    if (a.path) openAttachmentPreview({ path: a.path, name: a.name });
    return;
  }
  if (a.status !== "ready") return;
  const ext = a.name.slice(a.name.lastIndexOf(".") + 1).toLowerCase();
  const entry: ChipPreviewEntry = { name: a.name, ext };
  if (a.kind === "image") entry.url = a.preview ?? URL.createObjectURL(a.file);
  else if (ext === "pdf") {
    entry.url = URL.createObjectURL(a.file);
    entry.urlOwned = true;
  } else if (ext === "xlsx" || ext === "xlsm") entry.bytes = await a.file.arrayBuffer();
  else if (TEXT_PREVIEW_EXTS.has(ext)) entry.text = await a.file.text();
  else entry.unsupported = true;
  chipPreview.value = entry;
}

// Sidebar collapse, persisted: anyone who folds it away wants it to stay that
// way on the next visit.
const sidebarPreference = embedded ? storeKey("pogo:sidebar-collapsed") : "pogo:sidebar-collapsed";
const narrowAgentPage = isStudio && window.matchMedia("(max-width: 720px)").matches;
const collapsed = ref(narrowAgentPage || (embedded ? localStorage.getItem(sidebarPreference) !== "0" : localStorage.getItem(sidebarPreference) === "1"));
watch(collapsed, (v) => localStorage.setItem(sidebarPreference, v ? "1" : "0"));

const sidebarEl = ref<HTMLElement | null>(null);
const sidebarFullEl = ref<HTMLElement | null>(null);
const mainEl = ref<HTMLElement | null>(null);
const immersiveChatId = useId();
const immersive = useImmersiveBrowser(mainEl, dismissAutomaticImmersion);
const workflowFloating = useImmersiveBrowser(mainEl, exitWorkflowImmersive, {
  fullscreen: false,
  focusSurface: () => mainEl.value?.closest(".shell")?.querySelector<HTMLElement>('[data-testid="agent-workflow-canvas"]') ?? null,
});
const { active: browserImmersive, motionPhase: immersiveMotionPhase, interacting: immersiveInteracting, fullscreen: browserFullscreen } = immersive;
const immersiveTitle = computed(() => sessions.value.find(s => s.id === current.value)?.title || studioAgent.value?.name || t("immersiveBrowser.chat"));
watch(current, () => immersive.exit(false));

// Both directions get a 0.3s slide — as a FLIP transform via WAAPI, never a
// width transition: animating width/flex-basis re-lays out the whole
// unvirtualized conversation every frame (that was the expand stutter).
// Both directions flip the layout FIRST (one reflow, on the click frame) and
// then replay the motion into place with transforms only. Collapsing used to
// do it the other way around (slide, then flip) and the end-of-slide flip
// snapped the main column 194px wider in one frame — the visible "卡一下".
const SLIDE_MS = 300;
const SLIDE_DX = 250 - 56; // sidebar width minus the collapsed rail width
const SLIDE_OPTS: KeyframeAnimationOptions = { duration: SLIDE_MS, easing: "ease-in-out" };

async function expandSidebar() {
  if (!collapsed.value) return;
  collapsed.value = false;
  await nextTick();
  const from = { transform: `translateX(${-SLIDE_DX}px)` };
  const to = { transform: "translateX(0)" };
  sidebarEl.value?.animate([from, to], SLIDE_OPTS);
  mainEl.value?.animate([from, to], SLIDE_OPTS);
}

async function collapseSidebar() {
  if (collapsed.value) return;
  // Flip to the collapsed layout first (one reflow, on the click frame). The
  // wide layer keeps its fixed 250px box and the aside no longer clips it, so
  // right after the flip it still covers exactly the pixels it did before the
  // click — the flip frame is visually identical to the old layout.
  collapsed.value = true;
  await nextTick();
  // Then replay the motion with transforms only: the wide layer slides off to
  // the left while crossfading out (the CSS opacity transition started at the
  // flip), revealing the rail underneath; the main column replays from
  // +SLIDE_DX, its right edge clipped by the shell, so it visibly follows the
  // sidebar's trailing edge into place. The animation's end state IS the
  // layout, so nothing snaps at the end; nothing re-lays out per frame.
  sidebarFullEl.value?.animate(
    [{ transform: "translateX(0)" }, { transform: `translateX(${-SLIDE_DX}px)` }],
    SLIDE_OPTS,
  );
  mainEl.value?.animate(
    [{ transform: `translateX(${SLIDE_DX}px)` }, { transform: "translateX(0)" }],
    SLIDE_OPTS,
  );
}

// The right dock holds workspace files, the tabbed hub, shortcuts, background
// tasks, workflows and skills. Which sheet is open persists; the hub's strip
// (pinned shell/panel tabs plus any opened subagent tabs) persists alongside it. migrateSheet and
// restoreTabs fold every id older builds wrote onto this pair, both
// idempotent so downgrades cannot strand anyone.
const stored = migrateSheet({
  sheet: localStorage.getItem(storeKey(SHEET_KEY)),
  // The legacy keys are account-wide, so Studio does not even read them: an
  // old build's dock state belongs to the classic workbench, not to an Agent.
  legacyFiles: !isStudio && localStorage.getItem("pogo:files-open") === "1",
  legacyShell: !isStudio && localStorage.getItem("pogo:shell-open") === "1",
});
const activeSheet = ref<string | null>(isDelegatedEmbed ? null : stored.sheet);
const internalWorkflowSelection = ref<AgentWorkflowLocation & { sequence: number }>();
let internalWorkflowSequence = 0;
provide(agentWorkflowNavigationKey, {
  agentId: () => studioAgentId || undefined,
  open(location) {
    if (!isStudio || !validAgentWorkflowId(location.workflowId) || location.runId && !validAgentWorkflowId(location.runId)) return;
    internalWorkflowSelection.value = { ...location, sequence: ++internalWorkflowSequence };
    selectActiveSheet("workflows");
  },
});
watch(stream.agentWorkflowUpdate, update => {
  if (!update || !isStudio || !shouldRevealCreatedAgentWorkflow(update, activeSheet.value, studioAgentId, current.value)) return;
  internalWorkflowSelection.value = { workflowId: update.workflowId, sequence: ++internalWorkflowSequence };
  selectActiveSheet("workflows");
});
// Deliberately not persisted — see DockArea's maximized comment.
const hubMaximized = ref(false);
// The conversation stays beside the workflow by default. Explicit immersion
// only repositions the same main element; its draft and stream stay mounted.
const workflowWorkspaceOpen = ref(false), workflowImmersiveRequested = ref(false);
const workflowCanvasActive = computed(() => isAgentWorkflowWorkspace(activeSheet.value, workflowWorkspaceOpen.value, studioAgentId, browserImmersive.value, workflowWorkspaceOpen.value));
const floatingConversationActive = computed(() => browserImmersive.value || workflowCanvasActive.value);
const activeConversation = computed(() => workflowCanvasActive.value ? workflowFloating : immersive);
const chatMinimized = computed(() => activeConversation.value.minimized.value);
const chatVisible = computed(() => activeConversation.value.chatVisible.value);
const restoreVisible = computed(() => activeConversation.value.restoreVisible.value);
const chatAnimating = computed(() => activeConversation.value.chatAnimating.value);
const chatMotionPhase = computed(() => activeConversation.value.chatMotionPhase.value);
const chatManipulating = computed(() => activeConversation.value.manipulating.value);
const floatingChatStyle = computed(() => activeConversation.value.style.value);
const workflowConversationOpen = computed(() => workflowCanvasActive.value && workflowFloating.active.value && !workflowFloating.minimized.value);
function exitWorkflowImmersive() { workflowImmersiveRequested.value = false; }
function setWorkflowConversationOpen(open: boolean) {
  if (!workflowCanvasActive.value) {
    if (open && activeSheet.value === "workflows") void nextTick(focusComposer);
    return;
  }
  if (open) void workflowFloating.restore();
  else workflowFloating.minimize();
}
function restoreFloatingConversation(focus = false) {
  if (floatingConversationActive.value) void activeConversation.value.restore(focus);
}
async function restoreWebsiteConversation() {
  if (floatingConversationActive.value) await activeConversation.value.restore(false);
  await nextTick();
}
const websiteLinkRevealed = useWebServiceLinkReveal(initialWebsiteLink, () => ({
  sessionId: current.value, displaySessionId: stream.displaySessionId.value, switching: stream.switching.value, widget: managedWebsites.value,
  surfaceReady: !floatingConversationActive.value || chatVisible.value && !chatMinimized.value && !chatAnimating.value && !activeConversation.value.motionPhase.value,
  restoreSurface: restoreWebsiteConversation,
}));
watch(workflowCanvasActive, active => {
  if (active) workflowFloating.enter();
  else if (workflowFloating.active.value || workflowFloating.motionPhase.value === "entering") workflowFloating.exit(false);
});
watch(current, () => { workflowImmersiveRequested.value = false; workflowFloating.exit(false); });
watch(() => props.studioAgentId, () => { workflowWorkspaceOpen.value = false; workflowImmersiveRequested.value = false; workflowFloating.exit(false); });

function readStoredTabs(): StoredTabs {
  try {
    return JSON.parse(localStorage.getItem(storeKey(TABS_KEY)) ?? "{}") as StoredTabs;
  }
  catch {
    return {};
  }
}

// The workspace's whole strip is user state now — nothing is auto-pinned, so
// what was open comes back as it was (panel tabs validate against what the
// server still registers). Restored in restoreDock, after panels load.
const hubTabs = ref<DockTab[]>([]);
const hubActiveTab = ref<string | null>(stored.tab);
const visibleHubTabs = computed(() => hubTabs.value.filter(tab => browserSheetVisible(tab, current.value) &&
  (tab.kind !== "sentinel" || !isStudio || tab.agentId === studioAgentId) &&
  (tab.kind !== "task" || isStudio && tab.agentId === studioAgentId)));
const browserSelections = ref<Record<string, string | null>>({});
const browserBusy = ref(false);
const browserClosingTabs = reactive(new Map<string, string>());
const browserClosingTabIds = computed(() => visibleHubTabs.value.filter(tab => {
  const binding = tab.browserBinding;
  return binding && browserClosingTabs.has(JSON.stringify([binding.sessionId, binding.context_ref, binding.tab_id]));
}).map(tab => tab.id));
const browserMutationBusy = computed(() => browserBusy.value || [...browserClosingTabs.values()].some(sid => sid === current.value));
const browserContexts = new Map<string, string>();
const browserGenerations = new Map<string, { context: string; generation: number }>();
const browserReadySession = ref<string | null>(null);
const browserRestoreError = ref("");
const browserRestoring = computed(() => !!current.value && browserReadySession.value !== current.value);
let browserRestoreAttempts = 0;
let browserSessionEpoch = 0;
let manuallySelectedRecovery: string | null = null;
let browserReadEpoch = 0;
let browserReadController: AbortController | undefined;
let browserPoll: ReturnType<typeof setTimeout> | undefined;
let pendingBrowserNew: BrowserNewOperations | undefined;
const browserTabClosures = new BrowserTabClosures();
const browserImmersionPolicy = new BrowserImmersionPolicy();
const browserFocusPolicy = new BrowserFocusPolicy();
const pendingBrowserReveal = ref<BrowserRevealRequest>();
const fileOfficeView = ref<OfficeViewContext | null>(null);
let officeImmersionEpoch = 0;
let dockRestored = false;

function dismissAutomaticImmersion() {
  officeImmersionEpoch += 1;
  browserFocusPolicy.manual();
  browserImmersionPolicy.manualExit(current.value, stream.run.value.run_id);
  pendingBrowserReveal.value = undefined;
}

watch([pendingBrowserReveal, hubTabs, hubActiveTab, activeSheet, current, immersiveInteracting, browserImmersive, immersiveMotionPhase, () => stream.run.value.run_id], () => {
  const request = pendingBrowserReveal.value;
  if (!request) return;
  if (viewClosed || !browserImmersionPolicy.isAllowed(request, current.value, stream.run.value.run_id)) {
    pendingBrowserReveal.value = undefined;
    if (!browserImmersive.value && immersiveMotionPhase.value === "entering") immersive.exit(false);
    return;
  }
  if (browserImmersive.value) { pendingBrowserReveal.value = undefined; return; }
  if (activeSheet.value !== "hub") { dismissAutomaticImmersion(); return; }
  // Let pointer capture and IME composition finish before moving their surface.
  if (immersiveInteracting.value) return;
  if (immersiveMotionPhase.value === "entering") return;
  const selected = visibleHubTabs.value.find(tab => tab.id === hubActiveTab.value);
  const browser = selected?.kind === "browser" && selected.browserBinding && !selected.browserUnavailable ? selected
    : visibleHubTabs.value.find(tab => tab.kind === "browser" && tab.browserBinding && !tab.browserUnavailable);
  // The tool can start before discovery has bound a real Page. Wait for that
  // binding rather than presenting an empty immersive workspace.
  if (!browser) return;
  hubActiveTab.value = browser.id;
  toggleImmersive(true);
}, { flush: "post" });

function toggleImmersive(value: boolean) {
  if (!value) { immersive.exit(); return; }
  if (!immersiveWorkspaceAvailable(activeSheet.value, visibleHubTabs.value, hubActiveTab.value, fileOfficeView.value, current.value)) return;
  hubMaximized.value = false;
  immersive.enter();
}
async function officeOpened(request: OfficeRevealRequest) {
  const epoch = officeImmersionEpoch;
  // The same existing preview/iframe stays in its sheet. Wait for the selected
  // sheet props to settle without reacting again to saves or status polling.
  await nextTick();
  if (epoch !== officeImmersionEpoch || viewClosed || !officeRevealStillSelected(request, current.value, activeSheet.value, hubActiveTab.value, visibleHubTabs.value, fileOfficeView.value)) return;
  pendingBrowserReveal.value = undefined;
  toggleImmersive(true);
}
watch([activeSheet, hubActiveTab, hubTabs, fileOfficeView, immersiveMotionPhase], () => {
  if ((browserImmersive.value || immersiveMotionPhase.value === "entering") && !immersiveWorkspaceAvailable(activeSheet.value, visibleHubTabs.value, hubActiveTab.value, fileOfficeView.value, current.value)) {
    dismissAutomaticImmersion();
    immersive.exit(false);
  }
});
async function toggleBrowserFullscreen() {
  if (!(await immersive.toggleFullscreen())) showFlash(t("immersiveBrowser.fullscreenFailed"));
}

async function restoreDock() {
  if (isDelegatedEmbed) return;
  dockRestored = false;
  let storedTabs = readStoredTabs();
  try {
    storedTabs = await migrateBrowserStorage(localStorage, storeKey(TABS_KEY), panels.value, window.location.origin, api.migrateBrowserURLs, stored.tab ?? (isStudio ? null : localStorage.getItem(TENANT_KEY)));
    dockRestored = true;
  } catch (err) {
    // Do not overwrite the original entrypoints when migration/storage failed.
    showFlash(t("nativeBrowser.migrationFailed", { message: err instanceof Error ? err.message : String(err) }));
  }
  const { tabs, active } = restoreTabs({ ...storedTabs, legacyTenant: stored.tab ?? (isStudio ? null : localStorage.getItem(TENANT_KEY)) }, panels.value);
  const backupKey = `${storeKey(TABS_KEY)}:native-sheets:v2:backup`;
  if (!localStorage.getItem(backupKey)) localStorage.setItem(backupKey, JSON.stringify(storedTabs));
  hubTabs.value = tabs.filter(tab => tab.kind !== "task" || isStudio && tab.agentId === studioAgentId);
  browserSelections.value = storedTabs.browserActive ?? {};
  hubActiveTab.value = current.value ? visibleHubTabs.value.find(tab => tab.id === browserSelections.value[current.value!])?.id ?? visibleHubTabs.value.find(tab => tab.id === active)?.id ?? visibleHubTabs.value[0]?.id ?? null : hubTabs.value.find(tab => tab.id === active)?.id ?? hubTabs.value[0]?.id ?? null;
  if (dockRestored && !isStudio) localStorage.removeItem(TENANT_KEY);
}

watch(activeSheet, (v, previous) => {
  if (isDelegatedEmbed) return;
  if (v) localStorage.setItem(storeKey(SHEET_KEY), v);
  else localStorage.removeItem(storeKey(SHEET_KEY));
  // Expanded workspaces are temporary and never carry into another sheet.
  if (v !== previous) hubMaximized.value = false;
});
watch([hubTabs, hubActiveTab], () => {
  if (isDelegatedEmbed || !dockRestored) return;
  if (current.value) browserSelections.value[current.value] = hubActiveTab.value;
  // Favicon bytes are transient display metadata supplied by live state.
  // Keeping them out of browser storage avoids accumulating image payloads
  // for every conversation in the persisted tab strip.
  const out: StoredTabs = { open: hubTabs.value.map(({ favicon: _favicon, htmlPreviewId: _htmlPreviewId, websiteOpenRequest: _websiteOpenRequest, ...tab }) => tab.kind === "html" || tab.kind === "website" ? { ...tab, url: undefined } : tab), active: hubActiveTab.value, browserActive: browserSelections.value };
  localStorage.setItem(storeKey(TABS_KEY), JSON.stringify(out));
});
// Rewrite the migrated sheet id now rather than waiting for the first change,
// so a session that never touches the dock still leaves the new keys behind.
if (!isDelegatedEmbed) {
  if (activeSheet.value) localStorage.setItem(storeKey(SHEET_KEY), activeSheet.value);
  else localStorage.removeItem(storeKey(SHEET_KEY));
  if (!isStudio) {
    localStorage.removeItem("pogo:files-open");
    localStorage.removeItem("pogo:shell-open");
  }
}

// A subagent card's anchor opens (or focuses) the run's tab and surfaces the
// hub. The same anchor twice must not stack tabs — openTab dedups by id.
function openSubagentSheet(req: OpenSheetRequest) {
  if (isDelegatedEmbed) return unsupported();
  browserFocusPolicy.manual();
  const id = SUBAGENT_PREFIX + req.childSession;
  hubTabs.value = openTab(hubTabs.value, {
    id,
    label: req.label,
    kind: "subagent",
    childSession: req.childSession,
    ...(req.agentId && current.value ? { agentId: req.agentId, parentSession: current.value } : {}),
    closable: true,
  });
  hubActiveTab.value = id;
  hubAt.value = null;
  activeSheet.value = "hub";
}

// The outer strip is the browser's tab shell. A Sheet binds one Page in the
// same context; selecting it is an observation, never a tab-switch command.
const citationFavicons = createCiteFavicons();
provide(citeFaviconKey, { ...citationFavicons, get: source => stream.switching.value ? undefined : citationFavicons.get(source) });
let citationIconController: AbortController | undefined;
watch(current, sid => {
  browserSessionEpoch++;
  browserFocusPolicy.reset();
  citationIconController?.abort();
  citationFavicons.clear();
  browserReadySession.value = null;
  browserRestoreError.value = "";
  browserRestoreAttempts = 0;
  manuallySelectedRecovery = null;
  if (!sid) return;
  const controller = new AbortController(); citationIconController = controller;
  // This metadata read works even when Chromium has expired or is offline.
  const timer = setTimeout(() => controller.abort(), 10000);
  void api.browserIcons(sid, controller.signal).then(result => {
    if (current.value === sid && citationIconController === controller && !controller.signal.aborted && Array.isArray(result.icons)) citationFavicons.hydrate(result.icons);
  }).catch(() => { /* Optional display metadata never blocks conversation reads. */ }).finally(() => clearTimeout(timer));
}, { flush: "sync" });
onBeforeUnmount(() => citationIconController?.abort());
function reconcileBrowserState(state: NativeBrowserState, sid = current.value, removeMissing = false) {
  if (!sid || current.value !== sid || !completeBrowserState(state)) return false;
  const contextRef = state.context_ref!;
  // Only an ordered full discovery may replace a context; late hidden Pane
  // events cannot adopt an old context after a session switch or reconnect.
  if (!removeMissing && (browserReadySession.value !== sid || browserContexts.get(sid) !== contextRef)) return false;
  const known = browserGenerations.get(sid);
  if (known?.context === contextRef && state.generation !== undefined && state.generation < known.generation) return false;
  const filtered = browserTabClosures.filterState(sid, state, removeMissing);
  if (!filtered?.context_ref) return false;
  state = filtered;
  if (removeMissing) retireBrowserControls(sid, contextRef!);
  citationFavicons.observe(state.tabs);
  if (state.generation !== undefined) browserGenerations.set(sid, { context: contextRef, generation: state.generation });
  browserContexts.set(sid, contextRef!);
  observeBrowserControl(sid, state);
  hubTabs.value = reconcileBrowserSheets(hubTabs.value, sid, state, t("nativeBrowser.newTabPageTitle"), removeMissing);
  const focus = !immersiveInteracting.value && removeMissing ? browserFocusPolicy.selection(hubTabs.value, sid, state) : undefined;
  hubActiveTab.value = focus ?? reconciledBrowserSelection(hubTabs.value, hubActiveTab.value, sid, state, manuallySelectedRecovery === hubActiveTab.value);
  if (focus) { hubAt.value = null; manuallySelectedRecovery = null; activeSheet.value = "hub"; }
  return true;
}
function stopBrowserDiscovery() {
  browserReadEpoch++;
  clearTimeout(browserPoll);
  browserReadController?.abort();
}
async function discoverBrowser() {
  stopBrowserDiscovery();
  const sid = current.value;
  const selected = hubTabs.value.find(tab => tab.id === hubActiveTab.value);
  if (!sid || isDelegatedEmbed || activeSheet.value !== "hub" || selected?.kind === "task" || selected?.kind === "sentinel" || document.hidden || viewClosed || browserMutationBusy.value) return;
  const epoch = browserReadEpoch;
  const controller = new AbortController(); browserReadController = controller;
  const timeout = setTimeout(() => controller.abort(), 15000);
  try {
    const state = await api.browserState(sid, controller.signal);
    if (epoch !== browserReadEpoch || current.value !== sid) return;
    if (state.backend !== "native") throw new Error("Browser service unavailable");
    if (!reconcileBrowserState(state, sid, true)) throw new Error("Incomplete browser discovery");
    browserReadySession.value = sid;
    browserRestoreError.value = "";
    browserRestoreAttempts = 0;
  } catch {
    if (epoch === browserReadEpoch && current.value === sid && browserRestoring.value) {
      browserRestoreAttempts++;
      if (browserRestoreAttempts >= 3) browserRestoreError.value = t("nativeBrowser.restoreFailed");
    }
  }
  finally {
    clearTimeout(timeout);
    if (epoch === browserReadEpoch && current.value === sid && activeSheet.value === "hub" && !viewClosed && !browserRestoreError.value) browserPoll = setTimeout(() => void discoverBrowser(), 2000);
  }
}
function recoverBrowser() {
  browserReadySession.value = null;
  browserRestoreError.value = "";
  browserRestoreAttempts = 0;
  if (!browserMutationBusy.value) void discoverBrowser();
}
function selectHubTab(id: string) {
  cancelPendingAnswerFile();
  cancelPendingTaskNavigation();
  browserFocusPolicy.manual();
  manuallySelectedRecovery = hubTabs.value.find(tab => tab.id === id)?.browserUnavailable ? id : null;
  hubActiveTab.value = id;
  hubAt.value = null;
  void discoverBrowser();
}
function selectActiveSheet(value: string | null) {
  cancelPendingAnswerFile();
  cancelPendingTaskNavigation();
  browserFocusPolicy.manual();
  activeSheet.value = value;
}
watch(current, (sid, previous) => {
  if (previous) browserSelections.value[previous] = hubActiveTab.value;
  stopBrowserDiscovery();
  const retained = visibleHubTabs.value.find(tab => tab.id === hubActiveTab.value && (tab.kind === "task" || tab.kind === "sentinel"));
  const wanted = sid ? browserSelections.value[sid] : null;
  hubActiveTab.value = retained?.id ?? visibleHubTabs.value.find(tab => tab.id === wanted)?.id ?? visibleHubTabs.value[0]?.id ?? null;
  void discoverBrowser();
});
watch(activeSheet, () => { void discoverBrowser(); });
const browserVisibilityChanged = () => { void discoverBrowser(); };
onMounted(() => document.addEventListener("visibilitychange", browserVisibilityChanged));
onBeforeUnmount(() => { stopBrowserDiscovery(); document.removeEventListener("visibilitychange", browserVisibilityChanged); });

async function addHubTab(tab: DockTab): Promise<DockTab | undefined> {
  cancelPendingTaskNavigation();
  cancelPendingAnswerFile();
  browserFocusPolicy.manual();
  if (tab.kind !== "browser") { hubTabs.value = openTab(hubTabs.value, tab); hubActiveTab.value = tab.id; return; }
  const sid = current.value;
  const sessionEpoch = browserSessionEpoch;
  if (!sid) { showFlash(t("nativeBrowser.noSession")); return; }
  if (browserMutationBusy.value) return;
  browserBusy.value = true;
  stopBrowserDiscovery();
  let operationContext: string | undefined;
  try {
    const state = await api.browserState(sid);
    if (current.value !== sid || sessionEpoch !== browserSessionEpoch) return;
    if (state.backend !== "native" || !state.context_ref) throw new Error(state.backend !== "native" ? state.error : t("nativeBrowser.unavailable"));
    if (!reconcileBrowserState(state, sid, true)) throw new Error(t("nativeBrowser.restoreFailed"));
    browserReadySession.value = sid;
    browserRestoreError.value = "";
    operationContext = state.context_ref;
    pendingBrowserNew ??= new BrowserNewOperations(sessionStorage);
    const result = await api.browserTabs(sid, { action: "new", context_ref: operationContext, op_id: pendingBrowserNew.get(sid, operationContext) });
    pendingBrowserNew.complete(sid, operationContext);
    if (current.value !== sid || sessionEpoch !== browserSessionEpoch) return;
    if (result.context_ref !== operationContext || result.state.context_ref !== operationContext || !completeBrowserState(result.state)
      || !result.state.tabs.some(page => page.id === result.tab_id) || result.state.viewed_tab !== result.tab_id
      || result.state.control !== "human" || !result.lease_token || !Number.isInteger(result.state.control_epoch)) throw new Error(t("nativeBrowser.restoreFailed"));
    // The explicit + response grants only its newly created Page. A delayed
    // receipt cannot overwrite a newer Agent claim or human takeover.
    installBrowserLease(sid, result.context_ref, result.tab_id, result.state.control_epoch, result.lease_token);
    if (!reconcileBrowserState(result.state, sid, true)) throw new Error(t("nativeBrowser.restoreFailed"));
    const binding = { sessionId: sid, context_ref: result.context_ref, tab_id: result.tab_id };
    let created = hubTabs.value.find(item => item.browserBinding?.sessionId === sid && item.browserBinding.context_ref === binding.context_ref && item.browserBinding.tab_id === binding.tab_id);
    if (!created) { created = { ...tab, id: browserSheetID(), browserBinding: binding, url: "about:blank" }; hubTabs.value = [...hubTabs.value, created]; }
    // A starter/bookmark only fills an address draft, never navigates on restore.
    if (tab.url) created.browserDraft = tab.url;
    hubActiveTab.value = created.id;
    return created;
  } catch (err) {
    if (!browserOperationRetrySame(err) && operationContext) pendingBrowserNew?.complete(sid, operationContext);
    if (current.value === sid && sessionEpoch === browserSessionEpoch) showFlash(t("nativeBrowser.sheetCreateFailed", { message: err instanceof Error ? err.message : String(err) }), "error");
  } finally { browserBusy.value = false; void discoverBrowser(); }
}

async function reopenBrowserTab(id: string, rawURL: string) {
  const previous = hubTabs.value.find(tab => tab.id === id);
  const url = safeSourceURL({ locator: rawURL });
  if (!current.value || !url || !previous?.browserUnavailable || previous.browserBinding?.sessionId !== current.value || browserMutationBusy.value) return;
  const opened = await openBrowserURL(url);
  if (opened) {
    hubTabs.value = hubTabs.value.filter(tab => tab.id !== id);
    hubActiveTab.value = opened.id;
    manuallySelectedRecovery = null;
  }
}

// Both browser recovery and explicit HTML preview opening create one real Page
// and navigate it once using that Page's current human lease.
async function openBrowserURL(url: string): Promise<DockTab | undefined> {
  const sid = current.value;
  const sessionEpoch = browserSessionEpoch;
  if (!sid || browserMutationBusy.value) return;
  // Explicit user intent creates a fresh Page. Never restore an old lease or
  // replay the old task; navigation below is dispatched exactly once.
  const opened = await addHubTab({ id: browserSheetID(), kind: "browser", label: t("nativeBrowser.newTabPageTitle"), closable: true });
  const binding = opened?.browserBinding;
  if (!opened || !binding || current.value !== sid || sessionEpoch !== browserSessionEpoch) return;
  const closedOrClosing = () => browserClosingTabs.has(JSON.stringify([binding.sessionId, binding.context_ref, binding.tab_id])) || browserTabClosures.isClosed(binding);
  if (closedOrClosing()) return;
  browserBusy.value = true;
  stopBrowserDiscovery();
  try {
    const remembered = browserControlMemory(sid, binding.context_ref, binding.tab_id);
    const token = remembered.token, epoch = remembered.epoch;
    let control: NativeBrowserControl;
    if (token) {
      // Creating the replacement Page already granted this window its lease.
      // Read only its current generation instead of taking over it again.
      const observed = await api.browserState(sid, undefined, { context_ref: binding.context_ref, tab_id: binding.tab_id });
      if (current.value !== sid || sessionEpoch !== browserSessionEpoch || closedOrClosing()) return;
      if (observed.backend !== "native" || !targetMatches(observed, binding) || observed.control !== "human" || observed.control_epoch !== epoch
        || remembered.token !== token || remembered.epoch !== epoch) throw new Error(t("nativeBrowser.restoreFailed"));
      control = { ...observed, lease_token: token };
    } else {
      control = await api.browserControl(sid, "human", binding);
    }
    if (current.value !== sid || sessionEpoch !== browserSessionEpoch || closedOrClosing()) return;
    if (!targetMatches(control, binding) || control.control !== "human" || !control.lease_token || !Number.isSafeInteger(control.generation) || control.generation! < 1
      || !Number.isSafeInteger(control.control_epoch) || control.control_epoch! < 0) throw new Error(t("nativeBrowser.restoreFailed"));
    if (!installBrowserLease(sid, binding.context_ref, binding.tab_id, control.control_epoch, control.lease_token)) throw new Error(t("nativeBrowser.restoreFailed"));
    const next = await api.browserInput(sid, { type: "navigate", url }, control.lease_token, { ...binding, generation: control.generation! });
    // Explicit closure wins over a late navigation receipt. It must neither
    // restore focus to that Page nor turn its cancellation into a load error.
    if (current.value !== sid || sessionEpoch !== browserSessionEpoch || closedOrClosing()) return;
    if (!targetMatches(next, binding) || !reconcileBrowserState(next, sid)) throw new Error(t("nativeBrowser.restoreFailed"));
    return opened;
  } catch {
    if (current.value === sid && sessionEpoch === browserSessionEpoch && !closedOrClosing()) showFlash(t("nativeBrowser.restoreFailed"), "error");
  } finally { browserBusy.value = false; void discoverBrowser(); }
}


type HTMLBrowserPreviewRequest = { url: string; id: string; sessionId: string };
const htmlBrowserCapabilities = new Map<string, HTMLBrowserPreviewRequest>();
function releaseHTMLBrowserCapability(tabID: string) {
  const request = htmlBrowserCapabilities.get(tabID);
  if (!request) return;
  htmlBrowserCapabilities.delete(tabID);
  void api.revokeHTMLPreview(request.sessionId, request.id).catch(() => {});
}
function claimHTMLBrowserCapability(tabID: string, capabilityID: string) {
  const tab = hubTabs.value.find(item => item.id === tabID && item.kind === "html");
  if (!tab || tab.htmlPreviewId !== capabilityID) return;
  htmlBrowserCapabilities.delete(tabID);
  hubTabs.value = hubTabs.value.map(item => {
    if (item.id !== tabID) return item;
    const { url: _url, htmlPreviewId: _htmlPreviewId, ...rest } = item;
    return rest;
  });
}
async function openCurrentHTMLPreviewBrowser(request: HTMLBrowserPreviewRequest & { path: string }) {
  let accepted = false;
  try {
    if (viewClosed || request.sessionId !== current.value || !/^[a-f0-9]{32}$/.test(request.id)
      || !/^\/api\/html-previews\/[a-f0-9]{64}\//.test(request.url) || !request.path) return;
    browserFocusPolicy.manual();
    const id = HTML_PREFIX + request.sessionId + ':' + request.path;
    const existing = hubTabs.value.find(tab => tab.kind === 'html' && tab.id === id);
    if (!existing) {
      const tab: DockTab = { id, kind: 'html', label: request.path.split('/').pop() || request.path, path: request.path, parentSession: request.sessionId, url: request.url, htmlPreviewId: request.id, closable: true };
      htmlBrowserCapabilities.set(id, request);
      hubTabs.value = openTab(hubTabs.value, tab);
      accepted = true;
    }
    hubAt.value = null; hubActiveTab.value = id; activeSheet.value = 'hub';
  } finally {
    if (!accepted) void api.revokeHTMLPreview(request.sessionId, request.id).catch(() => {});
  }
}
async function openHTMLPreviewBrowser(request: HTMLBrowserPreviewRequest) {
  let accepted = false;
  try {
    const url = new URL(request.url);
    // Only the authenticated file preview's dedicated capability is handed to
    // this route. It never accepts an app credential or an arbitrary local file.
    if (!/^[a-f0-9]{32}$/.test(request.id) || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash
      || !/^\/api\/html-previews\/[a-f0-9]{64}\//.test(url.pathname) || request.sessionId !== current.value || viewClosed) return;
    const opened = await openBrowserURL(url.href);
    if (!opened || current.value !== request.sessionId || viewClosed) return;
    htmlBrowserCapabilities.set(opened.id, request);
    accepted = true;
    hubActiveTab.value = opened.id;
    activeSheet.value = 'hub';
  } catch (error) {
    if (request.sessionId === current.value && !viewClosed) showFlash(error instanceof Error ? error.message : String(error), 'error');
  } finally {
    // A stale session, cancelled create, or failed navigation must not leak a
    // second live capability. Successful Pages own theirs until close/expiry.
    if (!accepted) void api.revokeHTMLPreview(request.sessionId, request.id).catch(() => {});
  }
}
async function openWebsiteBrowser(request: WebsiteBrowserRequest) {
  const sid = current.value, epoch = browserSessionEpoch;
  const valid = () => !viewClosed && !isDelegatedEmbed && sid === current.value && epoch === browserSessionEpoch && websiteBrowserRequestCurrent(request, current.value, hubTabs.value);
  if (!valid() || browserMutationBusy.value) return;
  try {
    // Recheck status, not open: the existing lifecycle URL remains stable and
    // a native view must never start or renew a service as a side effect.
    const record = await api.webService(request.sessionId, request.serviceId);
    if (!valid() || !isWebServiceRecord(record) || record.service_id !== request.serviceId || record.state !== "ready" || Date.parse(record.lease_expires_at) <= Date.now()) return;
    const opened = await openBrowserURL(request.url);
    if (opened && valid()) { hubActiveTab.value = opened.id; activeSheet.value = "hub"; }
  } catch (error) {
    if (valid()) showFlash(error instanceof Error ? error.message : t("webServices.actionFailed"), "error");
  }
}
watch(() => hubTabs.value.map(tab => tab.id), ids => {
  const live = new Set(ids);
  for (const id of htmlBrowserCapabilities.keys()) if (!live.has(id)) releaseHTMLBrowserCapability(id);
});

if (!isDelegatedEmbed) {
  const disposeBrowserFocus = setBrowserFocusHook(request => {
    if (viewClosed || request.sessionId !== current.value) return;
    browserFocusPolicy.observe(request);
    if (request.phase === "complete") void discoverBrowser();
  });
  onBeforeUnmount(disposeBrowserFocus);
  const disposeBrowserReveal = setEnsurePaneHook(request => {
    if (viewClosed || request.sessionId !== current.value || request.runId !== stream.run.value.run_id || !browserImmersionPolicy.request(request)) return;
    // The first live browser step requests immersion. Discovery binds the
    // actual Page before the layout moves; no extra browser action is sent.
    hubAt.value = null;
    pendingBrowserReveal.value = request;
    const alreadyOpen = activeSheet.value === "hub";
    activeSheet.value = "hub";
    if (alreadyOpen) void discoverBrowser();
  });
  onBeforeUnmount(disposeBrowserReveal);
}

function renameHubTab(id: string, label: string) {
  const at = hubTabs.value.findIndex((t) => t.id === id);
  if (at >= 0 && label.trim()) hubTabs.value[at].label = label.trim().slice(0, 48);
  // Re-seal the array so the persistence watch sees the rename — an in-place
  // property write never reaches it, and a title is worth keeping.
  hubTabs.value = [...hubTabs.value];
}

// A browser tab's committed URL, remembered on the tab: the strip's persisted
// state then restores the page the tab was actually on across a reload, not
// the one it was opened with. Panel tabs are skipped — their path is the
// reverse-proxied prefix they mount from, not a place they travelled to.
function navigateHubTab(id: string, url: string) {
  const at = hubTabs.value.findIndex((t) => t.id === id);
  if (at < 0 || hubTabs.value[at].kind !== "browser") return;
  if (hubTabs.value[at].url === url) return;
  hubTabs.value[at].url = url;
  delete hubTabs.value[at].browserDraft;
  hubTabs.value = [...hubTabs.value];
}

async function closeHubTab(id: string) {
  cancelPendingAnswerFile();
  browserFocusPolicy.manual();
  const sessionEpoch = browserSessionEpoch;
  const tab = hubTabs.value.find(item => item.id === id);
  if (!tab) return;
  const removeClosedTab = () => {
    const clearTaskLink = tab.kind === "task" && (hubActiveTab.value === id || tab.taskRunSession === requestedTaskSessionId());
    const { tabs, active } = closeTab(visibleHubTabs.value, hubActiveTab.value, id);
    hubTabs.value = hubTabs.value.filter(item => item.id !== id);
    hubActiveTab.value = tabs.find(item => item.id === active)?.id ?? tabs[0]?.id ?? null;
    if (clearTaskLink) taskResultLink();
  };
  const binding = tab.browserBinding;
  if (binding && !tab.browserUnavailable) {
    if (binding.sessionId !== current.value) return;
    // A stale context binding can be removed locally; never redirect a close
    // to a new context's coincidentally reused tN identifier.
    if (browserContexts.get(binding.sessionId) === binding.context_ref) {
      // Closing a Page can interrupt its navigation, including a restore that
      // still holds browserBusy. Keep its flight separate from create/restore.
      const closing = JSON.stringify([binding.sessionId, binding.context_ref, binding.tab_id]);
      if (browserClosingTabs.has(closing)) return;
      browserClosingTabs.set(closing, binding.sessionId); stopBrowserDiscovery();
      try {
        const state = await browserTabClosures.close(binding, api);
        if (current.value !== binding.sessionId || sessionEpoch !== browserSessionEpoch) return;
        // A retried operation can return its original cached snapshot. Only
        // fresh discovery may remove unrelated pages from the strip.
        if (state) reconcileBrowserState(state, binding.sessionId);
        // Retire the pane before clearing its closing state. A normal socket
        // close can arrive while the server is still cleaning up this Page.
        removeClosedTab();
      } catch (err) {
        if (current.value === binding.sessionId && sessionEpoch === browserSessionEpoch) showFlash(t("nativeBrowser.sheetCloseFailed", { message: err instanceof Error ? err.message : String(err) }), "error");
      } finally { browserClosingTabs.delete(closing); void discoverBrowser(); }
      return;
    } else if (!browserContexts.has(binding.sessionId)) { void discoverBrowser(); showFlash(t("nativeBrowser.connecting")); return; }
  }
  removeClosedTab();
}

function openManagedWebsite(request: OpenWebServiceRequest) {
  if (isDelegatedEmbed || !current.value || request.sessionId !== current.value || stream.switching.value) return;
  const id = `${WEBSITE_PREFIX}${request.sessionId}:${request.serviceId}`;
  const tab: DockTab = { id, kind: "website", parentSession: request.sessionId, webServiceId: request.serviceId, label: request.label, closable: true,
    ...(request.open ? { websiteOpenRequest: ++websiteOpenSequence } : {}) };
  hubTabs.value = openTab(hubTabs.value, tab); hubActiveTab.value = id; selectActiveSheet("hub");
}
function deletedManagedWebsite(request: { sessionId: string; serviceId: string }) {
  if (isDelegatedEmbed || request.sessionId !== current.value || stream.switching.value) return;
  const link = webServiceLink(location.search);
  if (link.sessionId === request.sessionId && link.serviceId === request.serviceId) {
    const url = new URL(location.href);
    url.searchParams.delete("web_service");
    history.replaceState(history.state, "", url.pathname + url.search + url.hash);
  }
  const tab = hubTabs.value.find(item => item.kind === "website" && item.parentSession === request.sessionId && item.webServiceId === request.serviceId);
  if (!tab) return;
  const { active } = closeTab(visibleHubTabs.value, hubActiveTab.value, tab.id);
  hubTabs.value = hubTabs.value.filter(item => item.id !== tab.id);
  hubActiveTab.value = active;
}
function openManagedWebsiteEntry() {
  if (!initialWebsiteLink.present) return false;
  if (!initialWebsiteLink.sessionId || !initialWebsiteLink.serviceId || !sessions.value.some(s => s.id === initialWebsiteLink.sessionId)) {
    showFlash(t("webServices.notFound"), "error"); return true;
  }
  pendingWorkspace.value = null; open(initialWebsiteLink.sessionId); return true;
}

// Which delegations are still running, off the conversation's own timeline:
// the strip's status dots and each sheet's polling both read this.
const liveTabs = computed(() => {
  const out: Record<string, boolean> = {};
  for (const [id, running] of Object.entries(subagentLiveness(timeline.value))) {
    out[SUBAGENT_PREFIX + id] = running;
  }
  return out;
});

// External panels registered with -web-panel, fetched once at boot (the set is
// fixed for the life of the server, like skills).
const panels = ref<PanelInfo[]>([]);

// A hash route to open the current panel tenant at, set by a starter card that
// deep-links into it. Not persisted and cleared as soon as the user switches
// tenant by hand: it describes one navigation, not a preference.
const hubAt = ref<string | null>(null);

// Empty-state cards contributed by the loaded skills. Absent for a plain pogo,
// which then keeps its one-line hint.
const starters = ref<Starters | null>(null);
const starterCards = computed(() => starters.value?.cards ?? []);
const starterDialogOpen = ref(false);
const starterCardsDialog = ref<InstanceType<typeof StarterCardsDialog> | null>(null);
const savingStarterDraft = ref(false);
const pickingStarter = ref(false);
let starterDraftSession: string | null = null;
let starterDraftFiles = new Map<string, StarterDraftFile>();
const canSaveStarterDraft = computed(() => !!current.value && isAdmin.value && !sending.value && !savingStarterDraft.value && !composerComposing.value
  && composerToken.value?.kind !== "command" && composerParts.value.some(part => part.type !== "text" || part.text.trim())
  && !attachments.value.some(file => file.status !== "ready"));

async function openStarterCards() {
  try {
    if (isStudio) studioAgent.value = await platformApi.agent(studioAgentId);
    await loadStarters();
  } catch (error) { showFlash(error instanceof Error ? error.message : String(error), "error"); return; }
  starterDraftSession = current.value;
  starterDraftFiles = new Map();
  starterDialogOpen.value = true;
}

async function saveCurrentStarterDraft() {
  if (!canSaveStarterDraft.value) return;
  const draft = cloneStarterDraft({ inputLayout: composerLayout.value, parts: [
    ...composerDraftWithToken(composerParts.value, composerToken.value),
  ] });
  const sid = current.value;
  const files = captureStarterFiles(draft.parts, attachments.value);
  savingStarterDraft.value = true;
  try {
    draft.parts = await snapshotComposerElementIcons(draft.parts, citationFavicons);
    if (viewClosed || current.value !== sid) return;
    starterDraftSession = sid;
    starterDraftFiles = files;
    starterCardsDialog.value?.openAdd(draft);
  } finally { savingStarterDraft.value = false; }
}

async function prepareStarterDraft(draft: StarterDraft): Promise<PreparedStarterDraft> {
  const sid = starterDraftSession ?? current.value;
  const files = starterDraftFiles;
  if (!sid || viewClosed) throw new Error(t("nativeBrowser.noSession"));
  savingStarterDraft.value = true;
  try {
    const iconParts = await snapshotComposerElementIcons(draft.parts, current.value === sid ? citationFavicons : null);
    const parts = await prepareStarterFileParts(iconParts, files, (file, name, dir) => api.uploadAttachment(sid, file, name, dir));
    // Assets are copied into the shortcut's own durable scope; the live draft
    // keeps its bytes and is neither sent nor cleared by this save.
    const saved = await api.saveStarterAssets(sid, { parts, input_layout: draft.inputLayout });
    if (viewClosed) throw new Error(t("nativeBrowser.noSession"));
    return { parts: saved.parts, input_layout: saved.input_layout ?? draft.inputLayout, prompt: saved.prompt };
  } finally { savingStarterDraft.value = false; }
}
const focusAfterStarterPick = ref(false);

// Sheets always dock on the right at full height with the conversation on
// the left — the split workflow and shortcut sheets already established.
// The stacked "bottom" dock tore the composer out of the conversation and
// shrank the sheet into the bottom-right corner, so it is retired; clear
// the legacy preference so it cannot resurrect that layout.
localStorage.removeItem("pogo:files-layout");

// stickToBottom follows new output only while the user is already at the bottom.
// Scrolling up to read history and being yanked back down is the single most
// annoying thing a streaming UI can do.
const stickToBottom = ref(true);
let scrollIntentUntil = 0;
watch(stickToBottom, value => { if (!value) scrollIntentUntil = 0; }, { flush: "sync" });
provideActivityReading({
  following: computed(() => stickToBottom.value && !stream.switching.value),
  pause: () => { scrollIntentUntil = 0; stickToBottom.value = false; },
});

const timeline = computed(() => {
  // liveVersion is throttled by the stream, so streaming deltas rebuild this at
  // a bounded rate instead of once per token.
  void stream.liveVersion.value;
  // pendingUser trails the settled messages: the stream's optimistic bubbles,
  // drawn on click and retired when the server echoes their client id.
  return buildTimeline(
    [...stream.messages.value, ...stream.pendingUser.value],
    stream.results.value,
    stream.live.value,
    stream.bgTasks.value,
    stream.answerReview.value,
    stream.shellTasks.value,
  );
});

// An empty array is not evidence of an empty conversation. Only a deliberate
// draft or the selected session's committed snapshot may show starter cards.
const conversationLoading = computed(() => studioEntryLoading.value || (
  !timeline.value.length && !stream.compactions.value.length && (
    stream.switching.value || !studioEmptyReady.value
  )
));

// Plans belong to accepted user tasks, not to the entire conversation. Upload
// placeholders and queued inbox messages have not started a task: only the
// server's user_message can move ownership; steering keeps its parent plan.
const todoState = computed(() => projectTaskTodos(timeline.value, new Set(stream.pendingUser.value.map(item => item.id))));
const todos = computed(() => todoState.value.current?.todos.length ? todoState.value.current.todos : undefined);

const busy = computed(() => stream.busy.value);
const policy = computed(() => !current.value && pendingWorkspace.value !== null
  ? { mode: draftPolicyMode.value }
  : stream.policy.value);
const immersiveAttention = computed(() => stream.live.value.pending_gates.length > 0);
watch(immersiveAttention, (waiting) => {
  // A collapsed transcript must not hide the decision that is holding up a run.
  if (waiting) restoreFloatingConversation(false);
});

// Next-step chips under the latest turn: the server's generated row when it
// names that turn, else the skills' rule matches (agent/followupPick.ts).
// Hidden while a run is in flight or a gate is waiting: a suggestion competing
// with an approval card is asking the user to do two things at once, and one of
// them blocks the agent. Lives below busy/timeline on purpose: the watch below
// evaluates it once during setup, and a computed that reads a later-declared
// const is a blank app (TDZ).
const followupPick = computed(() => {
  if (busy.value || !timeline.value.length) return null;
  if (stream.live.value.pending_gates.length > 0) return null;
  return pickFollowups(stream.followups.value, timeline.value, starters.value?.followups ?? []);
});
const followupChips = computed(() => followupPick.value?.chips ?? []);

// Coarse local counters (agent/followupTelemetry.ts): was the row shown at all?
// Once per turn per session — message ids restart per session, hence the prefix —
// so streaming recomputes and reconnects cannot inflate the count.
const followupsShownFor = new Set<string>();
watch(followupPick, (pick) => {
  if (!pick) return;
  const key = `${current.value ?? ""}:${pick.messageId}`;
  if (followupsShownFor.has(key)) return;
  followupsShownFor.add(key);
  trackFollowup("followup.shown");
});

// The exchange grouping is the foldable view of the same timeline: finished
// exchanges with a conclusion render as a work log (folded line + answer),
// the live one stays flat. The timeline itself is untouched — the rail, the
// todo bar and the follow-up chips all still read it.
const exchanges = computed(() => buildConversationExchanges(timeline.value, stream.compactions.value, busy.value));
const executionLedger = createActivityExecutionLedger();
const executionEvidence = ref<ReturnType<typeof executionLedger.update>>({ callOrigins: new Map(), endReasons: new Map() });
provideActivityExecution(executionEvidence);
watch([exchanges, stream.run, stream.liveVersion, stream.displaySessionId, stream.switching], () => {
  executionEvidence.value = executionLedger.update({
    sessionId: stream.switching.value ? undefined : stream.displaySessionId.value ?? undefined,
    exchanges: exchanges.value, live: stream.live.value, runInfo: stream.run.value,
  });
}, { immediate: true });
const waitingForModel = computed(() => {
  const live = stream.live.value;
  return busy.value && stream.connected.value && !stream.switching.value && !stream.retry.value && !stream.streamError.value &&
    !live.pending_tools.length && !live.pending_gates.length && !live.pending_user_inputs.length && !live.incoming?.length &&
    !live.text && !live.thinking && stream.answerReview.value?.state !== "running" &&
    !compacting.value;
});

// runStartedAt backs the live header's clock for exchanges whose user message
// carries no ts; the exchange's own startedAt wins whenever it exists.
const runStartedAt = ref<number | undefined>(undefined);
const liveExchangeId = computed(() =>
  busy.value ? exchanges.value[exchanges.value.length - 1]?.id : null,
);
const liveState = computed(() => stream.live.value);
// The live header's clock runs on estimated server time, so a skewed server
// clock cancels out instead of showing runs as pre-aged.
const serverClockOffset = computed(() => stream.serverOffset.value);
const approvalPending = ref<Record<string, boolean>>({});
const approvalErrors = ref<Record<string, string>>({});
let approvalScope = 0;
const approvalGateIds = computed<ReadonlySet<string>>(() => new Set(stream.switching.value ? [] : stream.live.value.pending_gates.map(g => g.gate_id)));
provide(approvalUiKey, { pending: approvalPending, errors: approvalErrors, activeGateIds: approvalGateIds,
  serverOffset: serverClockOffset, sessionId: current, canDecide: !isDelegatedEmbed });
watch(current, () => { approvalScope++; approvalPending.value = {}; approvalErrors.value = {}; }, { flush: "sync" });
// The vision helper's per-image describes for the running exchange: step one
// of an image turn on a text-only conversation model (see DescribeCards).
const liveDescribes = computed(() => stream.describes.value);
watch([busy, stream.displaySessionId], ([now, sid], [before, previousSid]) => {
  if (sid !== previousSid || (now && !before)) runStartedAt.value = now ? stream.serverNow() : undefined;
});

// The sidebar's working indicators: which sessions have a run in flight, keyed
// by session id. Three sources keep it honest — loadSessions seeds it (the
// list carries the server's busy flags), the open session's own stream updates
// its row with no polling delay, and a slow poll reconciles runs started
// elsewhere (a second tab, a script). Anything that leaves the sidebar a few
// seconds stale is only ever the poll's fault, and only for other tabs' work.
const busyIds = ref<Record<string, boolean>>({});
const isBusy = (id: string) => busyIds.value[id] === true;

// adoptActivity replaces the map from the server's ids. The open session's
// stream is the fresher source for its own row, so a poll that raced that
// run's start must not switch the indicator back off.
function adoptActivity(ids: string[]) {
  const next: Record<string, boolean> = {};
  for (const id of ids) next[id] = true;
  if (current.value && busy.value) next[current.value] = true;
  busyIds.value = next;
}

async function refreshActivity() {
  if (isDelegatedEmbed) return;
  try {
    adoptActivity((await api.sessionsActivity()).busy);
    // A background child can outlive its parent's busy indicator. Retry only
    // while a capability update is explicitly deferred, without changing the draft.
    if (isStudio && studioToolPending.value && !busy.value && !stream.switching.value && !studioConfigurationSyncing.value) void refreshStudioConfiguration();
  } catch {
    // Non-fatal: the indicators simply lag one cycle.
  }
}

watch(busy, (now) => {
  // switching means the value still belongs to the session being left; the
  // row it lands on after the snapshot is the new session's own business.
  if (!current.value || stream.switching.value) return;
  busyIds.value = { ...busyIds.value, [current.value]: now };
});

// Slow on purpose: everything the user does themselves lands through the two
// sources above; this only chases other tabs' work. Hidden tabs do not poll.
const activityTimer = window.setInterval(() => {
  if (!isDelegatedEmbed && !document.hidden) void refreshActivity();
}, 4000);
const onActivityVisible = () => {
  if (!isDelegatedEmbed && !document.hidden) void refreshActivity();
};
document.addEventListener("visibilitychange", onActivityVisible);
onBeforeUnmount(() => {
  window.clearInterval(activityTimer);
  document.removeEventListener("visibilitychange", onActivityVisible);
  viewClosed = true;
  delegatedBootEpoch++;
  // Studio unmounts on the way back to /manage (the classic "/" workbench
  // lives as long as the app, which is why this used to be embed-only):
  // leaving the stream open would keep a dead SSE fetch running against the
  // Agent's session.
  if (isDelegatedEmbed || isStudio) stream.disconnect();
});

// waiting covers the gap between sending and the first token, and the same
// gap between turns: the run is active but nothing new is on screen for it
// yet. Tools and gate cards are their own feedback, so they suppress it.
const waiting = computed(() => {
  const l = stream.live.value;
  return (
    busy.value &&
    !(l.text ?? "") &&
    !(l.thinking ?? "") &&
    l.pending_tools.length === 0 &&
    l.pending_gates.length === 0 &&
    (l.incoming?.length ?? 0) === 0
  );
});

// Context occupancy for the warning strip. The meter itself lives in the
// composer bar; this is only the "act now" threshold.
const ctxWindow = computed(() => stream.run.value.context_window ?? 0);
const readContextUsage = createContextUsageReader();
const contextReading = computed(() => readContextUsage({ used: stream.contextTokens.value, window: ctxWindow.value,
  overhead: stream.overheadTokens.value, estimated: stream.contextEstimated.value, messages: stream.messages.value, results: stream.results.value }));
const ctxPct = computed(() => Math.round(contextReading.value.percent ?? 0));
const ctxHigh = computed(() => (contextReading.value.percent ?? 0) >= 85);

// The gate: without credentials every API call is a 401 by design, so boot
// stops at a gate page instead of flashing errors out of each loader. Which
// gate depends on the deployment (fetched from the public /api/auth/status):
// "token" is the classic token page; "password" is the login form, and in
// that mode a token-less browser is NOT gated — the session cookie rides
// along on every fetch, and a 401 later (session expired) re-opens the form.
// null while the mode is loading: no gate flashes in the meantime.
const tokenRejected = ref(false);
const authMode = ref<"token" | "password" | "delegated" | null>(null);
// Whether the deployment also offers username/password on the gate (a
// users.json exists server-side); the gate renders its second form from this.
const authPassword = ref(false);
// The admin grant decides whether an Agent's own starter cards are editable
// in place: the PATCH behind it is operator-gated, so anyone else keeps the
// read-only grid.
const isAdmin = ref(false);
const gated = computed(() => {
  if (authMode.value === null) return false;
  if (authMode.value === "delegated") return tokenRejected.value || delegatedUiError.value !== null;
  if (authMode.value === "password") return tokenRejected.value;
  return !token || tokenRejected.value;
});

async function loadDelegatedPage() {
  const epoch = ++delegatedBootEpoch;
  studioEntryResolving.value = true;
  studioEntry.reset();
  authMode.value = "delegated";
  tokenRejected.value = false;
  delegatedUiError.value = null;
  delegatedUi.value = null;
  try {
    const ui = await getDelegatedUi();
    if (viewClosed || epoch !== delegatedBootEpoch) return;
    delegatedUi.value = ui;
    models.value = ui.models.models;
    if (!ui.capabilities.conversation) throw new Error(unavailableTip.value);
    const res = await api.sessions();
    if (viewClosed || epoch !== delegatedBootEpoch) return;
    sessions.value = res.sessions;
    cwd.value = res.cwd;
    adoptActivity(res.sessions.filter((s) => s.busy).map((s) => s.id));
    if (pendingWorkspace.value === null && sessions.value.length > 0) open(sessions.value[0].id);
    else if (sessions.value.length === 0 && pendingWorkspace.value === null) await createSession();
    else studioEntry.finishWithoutSession();
  } catch (err) {
    if (!viewClosed && epoch === delegatedBootEpoch) delegatedUiError.value = (err as Error).message;
  } finally {
    if (!viewClosed && epoch === delegatedBootEpoch) studioEntryResolving.value = false;
  }
}

// Studio boot: the bound Agent's definition is the page's identity — name,
// welcome, default model. A 401 rides the ordinary token gate; anything else
// (missing, draft, forbidden) becomes a page-level error with a way back to
// Agent Studio, never a silent fallback to the generic agent.
async function loadStudioAgent(): Promise<boolean> {
  try {
    const agent = await platformApi.agent(studioAgentId);
    if (viewClosed) return false;
    if (agent.status !== "ready") {
      studioError.value =
        t(managedChat ? "uiCoverage.app.draftAgent" : "uiCoverage.app.unpublishedAgent", { name: agent.name });
      return false;
    }
    studioAgent.value = agent;
    return true;
  } catch (err) {
    const e = err as Error & { status?: number };
    if (e.status === 401) {
      tokenRejected.value = true;
      return false;
    }
    studioError.value =
      e.message || t("uiCoverage.app.cannotOpenAgent");
    return false;
  }
}

function backToStudio() {
  navigateStudio("/manage");
}

// The Studio error page's retry: repeat the boot steps that could have been
// the failure — the Agent fetch, the ordinary loaders, the session pick.
// Everything here is idempotent (loaders overwrite; open() no-ops on an
// unchanged session), so a retry after a transient error just lands where a
// fresh load would have.
async function retryStudio() {
  if (!isStudio || viewClosed) return;
  studioEntryResolving.value = true;
  try {
    await retryStudioEntry();
  } finally {
    studioEntryResolving.value = false;
  }
}

async function retryStudioEntry() {
  studioEntry.reset();
  studioError.value = null;
  if (!(await loadStudioAgent())) return;
  const [sessionsLoaded] = await Promise.all([loadSessions(), loadModels(), loadSkills(), loadMcpMethods(), loadPanels(), loadStarters()]);
  if (viewClosed || !sessionsLoaded) return;
  if (tokenRejected.value) return;
  await restoreDock();
  if (openManagedWebsiteEntry()) { studioEntry.finishWithoutSession(); return; }
  const requestedTaskResult = requestedTaskSessionId();
  if (requestedTaskResult) {
    await openTaskResultEntry(requestedTaskResult);
    studioEntry.finishWithoutSession();
    return;
  }
  if (pendingWorkspace.value === null) {
    const sid = chooseBootSession(
      sessions.value,
      requestedSessionId(location.search),
      localStorage.getItem(storeKey(STUDIO_LAST_SESSION)),
    );
    if (await openLegacySentinelEntry(requestedSessionId(location.search) || localStorage.getItem(storeKey(STUDIO_LAST_SESSION)))) {
      studioEntry.finishWithoutSession();
      return;
    }
    if (sid) open(sid);
    else await createSession();
  } else studioEntry.finishWithoutSession();
  const sentinel = requestedSentinelId(location.search);
  if (sentinel) {
    await openSentinelSession(sentinel);
    studioEntry.finishWithoutSession();
  }
}

async function initializeConversation() {
  if (isDelegatedEmbed) {
    await loadDelegatedPage();
    return;
  }
  window.addEventListener("pogo-skills-changed", onSkillsChanged);
  onBeforeUnmount(() => window.removeEventListener("pogo-skills-changed", onSkillsChanged));
  // webkitdirectory is a prefixed attribute, so it is set on the element
  // rather than bound in the template; presence is what the browser reads.
  folderInput.value?.setAttribute("webkitdirectory", "");
  folderInput.value?.setAttribute("directory", "");
  try {
    const st = await api.authStatus();
    authMode.value = st.mode;
    authPassword.value = st.password;
    isAdmin.value = !!st.admin;
  } catch {
    authMode.value = "token";
  }
  if (gated.value) return;
  if (isStudio && !(await loadStudioAgent())) return;
  const [sessionsLoaded] = await Promise.all([loadSessions(), loadModels(), loadSkills(), loadMcpMethods(), loadPanels(), loadStarters()]);
  if (viewClosed || !sessionsLoaded) return;
  // The attachment allowlist the door check and the picker's accept attribute
  // read. Seeded with the server's default so both work before (or without)
  // the fetch; the server stays the authority and re-reads it per upload.
  api.getAttachmentPolicy().then(
    (p) => {
      attachAllowed.value = new Set(p.allowed_exts);
      attachMaxMB.value = p.max_mb;
    },
    () => {},
  );
  if (tokenRejected.value) return;
  // The red dot's first reading; the 60s poll keeps it honest from here.
  void refreshScheduleAttention();
  // Panels are in now: the strip can be rebuilt with the full pinned set.
  await restoreDock();
  if (isStudio) {
    if (openManagedWebsiteEntry()) { studioEntry.finishWithoutSession(); return; }
    const requestedTaskResult = requestedTaskSessionId();
    if (requestedTaskResult) {
      await openTaskResultEntry(requestedTaskResult);
      studioEntry.finishWithoutSession();
      return;
    }
    // Deep link (?c=) → this Agent's remembered session → the list's first
    // row. A candidate absent from THIS Agent's list is ignored (never adopt
    // another Agent's memory), and a requested one that failed validation is
    // said out loud instead of silently opening an unrelated conversation.
    if (pendingWorkspace.value === null) {
      const requested = requestedSessionId(location.search);
      const remembered = localStorage.getItem(storeKey(STUDIO_LAST_SESSION));
      if (await openLegacySentinelEntry(requested || remembered)) {
        studioEntry.finishWithoutSession();
        return;
      }
      const sid = chooseBootSession(sessions.value, requested, remembered);
      if (requested && requested !== sid) {
        showFlash(
          t("uiCoverage.app.foreignConversation"),
          "error",
        );
        if (!sid) history.replaceState(null, "", conversationPath());
      }
      if (sid) open(sid);
      else await createSession();
    } else studioEntry.finishWithoutSession();
    const sentinel = requestedSentinelId(location.search);
    if (sentinel) {
      await openSentinelSession(sentinel);
      studioEntry.finishWithoutSession();
    }
  } else if (pendingWorkspace.value === null && sessions.value.length > 0) open(sessions.value[0].id);
  else if (sessions.value.length === 0 && pendingWorkspace.value === null) await createSession();
}

onMounted(async () => {
  try {
    await initializeConversation();
  } finally {
    // Agent identity, catalog and dock restoration are intermediate steps.
    // The entry placeholder also waits for the final session selection.
    studioEntryResolving.value = false;
  }
});

// A session's workspace relative to the server root; "" means the root
// itself, which gets no chip — the common case stays quiet.
function wsOf(s: SessionInfo): string {
  if (!s.cwd || !cwd.value) return "";
  const prefix = cwd.value.endsWith("/") ? cwd.value : cwd.value + "/";
  return s.cwd.startsWith(prefix) ? s.cwd.slice(prefix.length) : "";
}

// ── Sidebar by project ─────────────────────────────────────────────────────
// ChatGPT's organization, mapped onto what pogo already has: the "project"
// is the workspace (the session's cwd), so grouping needs no new data. Each
// group is a disclosure — basename label, count badge, a "+" that starts a
// session inside that project — and the active session's group opens itself
// even when the user closed it, because watching a click open nothing is the
// failure mode that made Ant Design's submenu issue famous.
interface ProjectGroup {
  cwd: string;
  name: string;
  sessions: SessionInfo[];
  latest: number;
}

const projectGroups = computed<ProjectGroup[]>(() => {
  const by = new Map<string, SessionInfo[]>();
  for (const s of sessions.value) {
    const key = s.cwd || cwd.value || "/";
    const arr = by.get(key) ?? [];
    arr.push(s);
    by.set(key, arr);
  }
  const groups: ProjectGroup[] = [];
  for (const [key, list] of by) {
    const sorted = [...list].sort((a, b) => {
      if (!!b.pinned !== !!a.pinned) return b.pinned ? 1 : -1;
      return (b.updated ?? 0) - (a.updated ?? 0);
    });
    groups.push({
      cwd: key,
      name: dirBaseName(key),
      sessions: sorted,
      latest: Math.max(...sorted.map((s) => s.updated ?? 0)),
    });
  }
  // Groups ordered by their most recent activity — the projects you touch
  // sit at the top, without anyone maintaining an order by hand.
  groups.sort((a, b) => b.latest - a.latest);
  return groups;
});

const CLOSED_KEY = "pogo:closed-projects";
// Per-Agent in Studio: a fold remembered in one Agent's sidebar must not
// collapse (or reopen) another Agent's project groups.
const closedProjects = ref(new Set<string>(JSON.parse(localStorage.getItem(storeKey(CLOSED_KEY)) ?? "[]") as string[]));
watch(closedProjects, (v) => localStorage.setItem(storeKey(CLOSED_KEY), JSON.stringify([...v])), { deep: true });

function groupOpen(g: ProjectGroup): boolean {
  // The toggle is the only authority: a group is open unless it was closed,
  // including the one holding the active session — force-opening it is what
  // made the chevron look broken.
  return !closedProjects.value.has(g.cwd);
}
function toggleGroup(g: ProjectGroup) {
  const next = new Set(closedProjects.value);
  if (next.has(g.cwd)) next.delete(g.cwd);
  else next.add(g.cwd);
  closedProjects.value = next;
}

// Entering a session re-opens the project it lives in: the group you just
// landed in must not eat the click that got you there. Collapsing afterwards
// is a free choice and persists across reloads.
watch(current, (id) => {
  if (!id) return;
  const g = projectGroups.value.find((x) => x.sessions.some((s) => s.id === id));
  if (g && closedProjects.value.has(g.cwd)) {
    const next = new Set(closedProjects.value);
    next.delete(g.cwd);
    closedProjects.value = next;
  }
});

// "+" on a group header: a session in that project. wsOf-style relativity —
// when the group's cwd is not under the server root, the picker is the honest
// answer rather than a guessed workspace string.
function newInProject(g: ProjectGroup) {
  if (isDelegatedEmbed) { void createSession(); return; }
  const rel = wsOf({ cwd: g.cwd } as SessionInfo);
  if (g.cwd === (cwd.value || "/")) {
    void createSession("");
  } else if (rel) {
    void createSession(rel);
  } else {
    pickerOpen.value = true;
  }
}

// The bottom strip: every session, activity-ordered across projects. No cap —
// the list itself is height-capped (about five rows) and scrolls inside, so
// the strip's footprint stays fixed no matter how many sessions exist. Clicking
// a recent row opens the session where it lives.
const recentSessions = computed(() =>
  [...sessions.value].sort((a, b) => (b.updated ?? 0) - (a.updated ?? 0)),
);

// The recents strip crosses projects, so a row names the one it lives in —
// two "(empty session)" rows are indistinguishable without it. The basename
// for the eye, the full workspace path for the hover.
const recentProject = (s: SessionInfo) => dirBaseName(s.cwd || cwd.value || "/");
const recentTip = (s: SessionInfo) =>
  `${s.title || t("agentView.sidebar.emptySession")} · ${s.cwd || cwd.value}`;

// The strip folds down to just its header when the column needs the room —
// the header's hover button is the fold's switch, and the choice persists
// like the sidebar's other folds.
const recentCollapsed = ref(localStorage.getItem("pogo:recent-collapsed") === "1");
watch(recentCollapsed, (v) => localStorage.setItem("pogo:recent-collapsed", v ? "1" : "0"));

// The open session's workspace, root-relative ("" = the root): the file
// panel scopes its tree and the changes view's workspace scope to it.
const currentWorkspace = computed(() => {
  const s = sessions.value.find((x) => x.id === current.value);
  return s ? wsOf(s) : "";
});

// Citation locators are relative to their session, so never fall back to the
// server root while that session's workspace is still unknown or outside it.
const citationWorkspaceReady = computed(() => {
  const s = sessions.value.find((x) => x.id === current.value);
  if (!s?.cwd || !cwd.value) return false;
  const root = cwd.value.replace(/\/$/, "");
  return s.cwd.replace(/\/$/, "") === root || currentWorkspace.value !== "";
});
const informationSource = ref<{ id: string; version: number } | null>(null);
provide(citeNavigationKey, {
  canOpen: (source) => !isDelegatedEmbed && !!current.value && (informationCitationTarget(source) !== null || citationWorkspaceReady.value && citationFileTarget(source) !== null),
  open: (source) => {
    if (isDelegatedEmbed || !current.value || stream.switching.value) return;
    const information = informationCitationTarget(source);
    if (information) { informationSource.value = information; return; }
    if (!citationWorkspaceReady.value) return;
    const target = citationFileTarget(source);
    if (target) openAttachmentPreview({ ...target, citation: true });
  },
});

function answerFileScope(): WorkspaceFileScope | null {
  const session = sessions.value.find(item => item.id === current.value);
  if (!session?.cwd || !citationWorkspaceReady.value || stream.switching.value || !canUse("attachments")) return null;
  return { conversationId: session.id, cwd: session.cwd, workspace: currentWorkspace.value, root: cwd.value };
}
const answerFileNavigation = createAnswerFileNavigation({
  scope: answerFileScope,
  resolve: api.resolveSessionFileLink,
  openFile: openAttachmentPreview,
  failureMessage: () => t("workspaceFiles.resolveFailed"),
});
const answerFileChoice = answerFileNavigation.choice;
cancelPendingAnswerFile = () => { answerFileNavigation.cancel(); cancelPendingSentinelNavigation(); cancelPendingTaskNavigation(); };
async function openAnswerHTMLPreview(target: WorkspaceFileTarget, origin: Pick<WorkspaceFileScope, "conversationId" | "cwd">) {
  const scope = answerFileScope();
  if (!scope || scope.conversationId !== origin.conversationId || scope.cwd !== origin.cwd || !/\.html?$/i.test(target.path)) return;
  const fullPath = attachmentWorkspacePath(scope.workspace, target.path);
  const preview = await api.createHTMLPreview(scope.conversationId, fullPath);
  await openCurrentHTMLPreviewBrowser({ url: preview.url, id: preview.id, sessionId: scope.conversationId, path: fullPath });
}
provide(workspaceFileNavigationKey, { scope: answerFileScope, open: answerFileNavigation.open, openHTML: openAnswerHTMLPreview });
provide(webArtifactNavigationKey, {
  scope: () => !isDelegatedEmbed && !viewClosed && !stream.switching.value && current.value && stream.displaySessionId.value === current.value ? { sessionId: current.value } : null,
  openDock: openManagedWebsite,
  openBrowser: async request => {
    // Reuse the existing website navigation and its status/ownership recheck.
    // The inline pane obtained this lifecycle URL from the authenticated API.
    openManagedWebsite({ sessionId: request.sessionId, serviceId: request.serviceId, label: request.title, open: false });
    await openWebsiteBrowser(request);
  },
});
watch(() => {
  const scope = answerFileScope();
  return scope ? JSON.stringify([scope.conversationId, scope.cwd, scope.workspace, scope.root]) : "";
}, () => answerFileNavigation.cancel(), { flush: "sync" });
onBeforeUnmount(answerFileNavigation.cancel);

async function loadSessions(): Promise<boolean> {
  try {
    if (isStudio) {
      // Agent scope comes from the platform endpoint and from nowhere else:
      // no account-wide list is ever merged in, and on failure the previous
      // agent-scoped list stays — there is no all-session fallback.
      const res = await platformApi.sessions(studioAgentId);
      if (viewClosed) return false;
      sessions.value = res.sessions;
      if (res.cwd) cwd.value = res.cwd;
      adoptActivity(res.sessions.filter((s) => s.busy).map((s) => s.id));
      return true;
    }
    const res = await api.sessions();
    if (viewClosed) return false;
    sessions.value = res.sessions;
    cwd.value = res.cwd;
    adoptActivity(res.sessions.filter((s) => s.busy).map((s) => s.id));
    return true;
  } catch (err) {
    if (viewClosed) return false;
    if ((err as { status?: number }).status === 401 || (isDelegatedEmbed && (err as { status?: number }).status === 403)) {
      tokenRejected.value = true;
      return false;
    }
    const message = t("agentView.flash.loadSessionsFailed", { msg: (err as Error).message });
    // A failed catalog is unknown, not empty. Never turn an entry failure
    // into a new draft while the user's saved sessions are still unresolved.
    if (isStudio && studioEntryLoading.value) studioError.value = message;
    else showFlash(message, "error");
    return false;
  }
}

// The sidebar's search box: full-text across every saved transcript, answered
// server-side by the session index. Debounced with the codebase's plain-timer
// pattern; a sequence number drops stale responses, so a slow earlier query
// cannot overwrite a newer one's results. A null hits list is the box's "no
// query" state — an empty array is "queried, nothing matched".
const sessionQuery = ref("");
const sessionHits = ref<SessionHit[] | null>(null);
let searchSeq = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;

watch(sessionQuery, (q) => {
  clearTimeout(searchTimer);
  const seq = ++searchSeq;
  const trimmed = q.trim();
  if (!trimmed) {
    sessionHits.value = null;
    return;
  }
  if (isDelegatedEmbed || isStudio) {
    // Studio searches locally over the Agent's own list: the global
    // server-side index would answer across every Agent's sessions, so the
    // scoped list IS the search corpus (title match, same as the embed).
    const query = trimmed.toLocaleLowerCase();
    sessionHits.value = sessions.value.filter((s) => s.title.toLocaleLowerCase().includes(query)).map((s) => ({ ...s, score: 0 }));
    return;
  }
  searchTimer = setTimeout(async () => {
    try {
      const res = await api.sessionSearch(trimmed);
      if (seq === searchSeq) sessionHits.value = res.hits;
    } catch {
      if (seq === searchSeq) sessionHits.value = [];
    }
  }, 250);
});

// A hit's project, for the same "which workspace was this" cue the recents
// strip gives — a search crosses projects by design, the project groups don't.
function hitProject(h: SessionHit): string {
  return h.cwd ? dirBaseName(h.cwd) : "";
}

// Splits text into plain and matched runs for the current query, so titles
// and snippets can paint their keywords with <mark> without v-html. Matching
// is case-insensitive per whitespace-separated term, mirroring the backend's
// trigram/LIKE behaviour.
function hiParts(text: string): { t: string; m: boolean }[] {
  const terms = sessionQuery.value
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  if (!terms.length || !text) return [{ t: text, m: false }];
  const re = new RegExp(`(${terms.join("|")})`, "gi");
  const out: { t: string; m: boolean }[] = [];
  // split with one capture group alternates plain/match, matches at odd idx.
  text.split(re).forEach((p, i) => {
    if (p) out.push({ t: p, m: i % 2 === 1 });
  });
  return out;
}

// ── Search-hit locating ────────────────────────────────────────────────────
// A block's anchor is a verbatim cut of one message's text, so finding the
// message is a substring test over the loaded conversation — no id plumbing
// between the transcript and the stream. The jump itself waits for the
// session's snapshot when the click crossed sessions: open() only starts the
// stream, and doLocate runs once switching flips back to false.
const pendingLocate = ref<{ sid: string; anchor: string } | null>(null);

function locateBlock(h: SessionHit, b: SearchBlock) {
  pendingLocate.value = { sid: h.id, anchor: b.anchor };
  if (h.id !== current.value) {
    open(h.id);
    return;
  }
  void nextTick(doLocate);
}

watch(
  () => stream.switching.value,
  (sw) => {
    if (!sw && pendingLocate.value) void nextTick(doLocate);
  },
);

// A reveal signal only fires its WorkLog watchers on a CHANGE of value, and
// nothing ever resets them — so a second locate into the same fold, after the
// reader re-folded it by hand, would set the id the ref already holds, no
// watcher would run, and the jump would land on a still-collapsed fold. An
// unchanged id therefore goes through a "" reset first, re-arming the fire.
function revealSignal(signal: Ref<string>, id: string) {
  if (!id) return;
  if (signal.value !== id) {
    signal.value = id;
    return;
  }
  signal.value = "";
  void nextTick(() => {
    signal.value = id;
  });
}

// Prose stays visible in ActivityList. This also handles hidden legacy anchors
// and process targets without depending on the old animated fold markup.
function isFoldedAway(el: HTMLElement): boolean {
  return el.getClientRects().length === 0;
}

function doLocate() {
  const p = pendingLocate.value;
  pendingLocate.value = null;
  if (!p || p.sid !== current.value) return;
  const body = scroller.value;
  if (!body || !convInner.value) return;
  // The same words can appear twice — narrated mid-work and repeated in the
  // conclusion. Walk every match and land on the first one that is actually
  // showing: the text is identical either way, and a folded duplicate reads
  // as "the locate missed" even when it scrolled to the right string.
  let el: HTMLElement | null = null;
  for (const mid of findAnchorMessages(stream.messages.value, p.anchor)) {
    const cand = convInner.value.querySelector<HTMLElement>(`[data-prose-mid="${CSS.escape(mid)}"]`) ??
      convInner.value.querySelector<HTMLElement>(`[data-mid="${CSS.escape(mid)}"]`);
    if (!cand) continue;
    el ??= cand;
    if (!isFoldedAway(cand)) {
      el = cand;
      break;
    }
  }
  // No match at all means the message left the live branch (rewound) — there
  // is nothing to land on.
  if (!el) return;
  // Locating pulls the reader up the page, so the stream must stop dragging
  // them back down: the stick-to-bottom watch would win the next tick.
  stickToBottom.value = false;
  // Only a hidden target needs a local reveal. Searching visible prose leaves
  // the reader's process disclosure choices untouched.
  const wl = el.closest<HTMLElement>(".worklog[data-xid]");
  const concealed = isFoldedAway(el);
  if (concealed && wl) revealSignal(worklogRevealId, wl.dataset.revealId || wl.dataset.xid || "");
  // The event opens the precise thinking/tool group; it does not execute it.
  if (concealed) el.dispatchEvent(new CustomEvent("pogo-reveal-tool", { bubbles: true }));
  const jump = () => {
    // Paint before measuring, so the keyword's own range exists to land on:
    // when the located element is taller than the viewport (a long
    // conclusion), scrolling the element's TOP to the scroller's top leaves
    // the keyword itself below the fold — found, but not on screen. Newly
    // unfolded rows are fresh DOM either way, so the repaint belongs here.
    if (sessionQuery.value.trim()) applySearchHighlight(convInner.value, sessionQuery.value);
    const kw = firstHighlightRangeIn(el)?.getBoundingClientRect();
    const top =
      (kw ? kw.top : el.getBoundingClientRect().top) - body.getBoundingClientRect().top + body.scrollTop;
    body.scrollTo({ top: Math.max(0, top - 12), behavior: "smooth" });
    scheduleRailCompute();
  };
  // Activity groups have no completion-time height animation. Wait only for
  // the local reveal to paint before measuring the matching text.
  if (concealed) void nextTick(jump);
  else jump();
}

// While the search box holds a query the open conversation keeps every
// occurrence painted; clearing the box is what ends it. Ranges die with the
// nodes they point at, so the paint is reapplied (debounced) whenever the
// conversation re-renders.
let hlTimer: ReturnType<typeof setTimeout> | undefined;
watch([sessionQuery, timeline, () => stream.liveVersion.value, current], () => {
  clearTimeout(hlTimer);
  hlTimer = setTimeout(() => {
    if (!sessionQuery.value.trim()) clearSearchHighlight();
    else applySearchHighlight(convInner.value, sessionQuery.value);
  }, 200);
});
onBeforeUnmount(() => {
  clearTimeout(hlTimer);
  clearSearchHighlight();
});


async function loadModels() {
  try {
    models.value = (await api.models()).models;
  } catch {
    // Non-fatal: the picker just stays empty.
  }
}

async function loadSkills() {
  const sid = current.value;
  const request = ++skillCatalogRequest;
  skillCatalogState.value = "loading";
  skillConfigError.value = "";
  if (isStudio && !sid) { skills.value = []; return; }
  try {
    if (isStudio) {
      const synced = await api.syncStudioSkillConfig(studioAgentId, sid!);
      if (viewClosed || request !== skillCatalogRequest || current.value !== sid) return;
      skillConfigDeferred.value = synced.deferred;
    }
    const result = await api.skills(isStudio ? sid! : undefined);
    if (viewClosed || request !== skillCatalogRequest || (isStudio && current.value !== sid)) return;
    skills.value = result.skills;
    skillCatalogState.value = "ready";
  } catch (error) {
    // No fallback to a global list when a scoped request fails.
    if (request === skillCatalogRequest && (!isStudio || current.value === sid)) {
      skillConfigError.value = error instanceof Error ? error.message : String(error);
      skillCatalogState.value = "failed";
    }
  }
}

// The invocation catalog contains only methods callable under the effective
// conversation policy; the MCP panel's display catalog is intentionally broader.
async function loadMcpMethods() {
  if (isDelegatedEmbed) return;
  const sid = current.value;
  const request = ++mcpCatalogRequest;
  mcpCatalogState.value = "loading";
  try {
    const result = await api.tools(sid ?? undefined, isStudio ? studioAgentId : undefined);
    if (viewClosed || request !== mcpCatalogRequest || current.value !== sid) return;
    // An older host has no invocation catalog. Do not suggest methods from a
    // broader display catalog that may include disabled or conflicting entries.
    if (!Array.isArray(result.mcp_methods)) throw new Error("MCP invocation catalog unavailable");
    mcpMethods.value = result.mcp_methods.filter(validMcpReference).map(method => ({
      server: method.server, name: method.name, description: typeof method.description === "string" ? method.description : "",
    }));
    mcpCatalogState.value = "ready";
  } catch {
    if (viewClosed || request !== mcpCatalogRequest || current.value !== sid) return;
    mcpMethods.value = [];
    mcpCatalogState.value = "failed";
  }
}

async function loadStudioCapabilities() {
  if (!isStudio || !current.value || viewClosed) return;
  const sid = current.value;
  const request = ++studioCapabilitiesRequest;
  const isCurrent = () => !viewClosed && current.value === sid && request === studioCapabilitiesRequest;
  studioCapabilitiesError.value = "";
  studioModelSyncing.value = true;
  try {
    let result = await api.studioSessionCapabilities(studioAgentId, sid);
    if (!isCurrent()) return;
    if (result.model_sync) {
      const synced = await api.syncStudioModelConfig(studioAgentId, sid);
      if (!isCurrent()) return;
      // The response reports effective state, never a busy run's desired model.
      result = { ...result, model: synced.model, models: synced.models };
      studioModelDeferred.value = synced.deferred;
    } else studioModelDeferred.value = false;
    result = await syncStudioMcpCapabilities(result, () => api.syncStudioMcpConfig(studioAgentId, sid));
    if (!isCurrent()) return;
    studioToolSync.observeCapabilities(sid, result);
    if (result.tools_sync) await studioToolSync.sync();
    if (!isCurrent()) return;
    const toolState = studioToolState.value;
    if (toolState?.sessionId === sid) result = { ...result, tools: toolState.tools, tools_revision: toolState.revision, tools_pending: toolState.pending, tools_desired_revision: toolState.desiredRevision };
    const fingerprint = studioCapabilityFingerprint(result);
    if (fingerprint !== studioCapabilitySnapshot) {
      studioCapabilitySnapshot = fingerprint;
      studioCapabilityRevision.value++;
    }
    studioCapabilities.value = result;
  } catch (err) {
    if (!isCurrent()) return;
    studioCapabilitiesError.value = (err as Error).message;
  } finally {
    if (isCurrent()) studioModelSyncing.value = false;
  }
}

// Saves may emit one signal per committed policy. Coalesce them while retaining
// a trailing refresh, so a commit during an HTTP request cannot be lost.
let studioConfigurationRefresh: { sessionId: string; again: boolean; work: Promise<void> } | null = null;
function refreshStudioConfiguration(): Promise<void> {
  const sid = current.value;
  if (!isStudio || !sid || viewClosed) return Promise.resolve();
  if (studioConfigurationRefresh?.sessionId === sid) {
    studioConfigurationRefresh.again = true;
    return studioConfigurationRefresh.work;
  }
  const refresh = { sessionId: sid, again: false, work: Promise.resolve() };
  studioConfigurationRefresh = refresh;
  refresh.work = (async () => {
    do {
      refresh.again = false;
      await loadSkills();
      if (viewClosed || current.value !== sid) return;
      await loadStudioCapabilities();
      if (viewClosed || current.value !== sid) return;
      await loadMcpMethods();
    } while (refresh.again && !viewClosed && current.value === sid);
  })().finally(() => { if (studioConfigurationRefresh === refresh) studioConfigurationRefresh = null; });
  return refresh.work;
}

// Refresh on return to a conversation and after a busy run. The server applies
// model, MCP, Skill and native-tool policies at idle boundaries; instructions remain pinned.
watch([busy, () => stream.switching.value], ([running, switching], [wasRunning, wasSwitching]) => {
  if (isStudio && !running && !switching && (wasRunning || wasSwitching)) {
    void refreshStudioConfiguration();
  }
});
watch(stream.modelChangeVersion, () => {
  // run_end may precede the server clearing its active slot. The subsequent
  // model event is the commit signal for both the model and its allowlist.
  if (current.value && !viewClosed && !stream.switching.value) {
    void loadModels();
    if (isStudio) {
      void refreshStudioConfiguration();
    }
  }
});
watch(stream.toolConfig, receipt => {
  if (!isStudio || viewClosed || !receipt || current.value !== receipt.sessionId) return;
  if (!studioToolSync.accept(receipt.sessionId, receipt.config)) return;
  const tools = studioToolState.value;
  if (tools && studioCapabilities.value) studioCapabilities.value = { ...studioCapabilities.value, tools: tools.tools, tools_revision: tools.revision, tools_pending: tools.pending, tools_desired_revision: tools.desiredRevision };
});
watch(stream.capabilitiesChanged, change => {
  if (isStudio && !viewClosed && change?.sessionId === current.value && !stream.switching.value) void refreshStudioConfiguration();
});
watch(stream.connected, connected => {
  // Replayed policy events are not current authority; reconcile after reconnect.
  if (connected && isStudio && current.value && !viewClosed) void refreshStudioConfiguration();
  else if (connected && !isStudio && !viewClosed) void loadMcpMethods();
});
function refreshStudioModelOnFocus() {
  if (isStudio && current.value && !viewClosed && !stream.switching.value && document.visibilityState === "visible") {
    void refreshStudioConfiguration();
  }
  else if (!viewClosed && !isDelegatedEmbed && document.visibilityState === "visible") void loadMcpMethods();
}
onMounted(() => { window.addEventListener("focus", refreshStudioModelOnFocus); document.addEventListener("visibilitychange", refreshStudioModelOnFocus); });
onBeforeUnmount(() => { studioCapabilitiesRequest++; studioToolSync.select(null); window.removeEventListener("focus", refreshStudioModelOnFocus); document.removeEventListener("visibilitychange", refreshStudioModelOnFocus); });

async function loadPanels() {
  try {
    panels.value = (await api.panels()).panels;
  } catch {
    // Non-fatal: the hub just has no external tenants.
  }
}

async function loadStarters() {
  // Studio's welcome belongs to the Agent definition — its description as
  // the heading, its starter prompts as cards — not the deployment-wide
  // starter file, so the server fetch is skipped entirely here.
  if (isStudio) {
    starters.value = studioAgent.value ? studioStarters(studioAgent.value) : null;
    return;
  }
  try {
    const res = await api.starters();
    const s = res.starters;
    starters.value = s && (s.cards?.length || s.followups?.length) ? s : null;
  } catch {
    // Non-fatal: the empty state falls back to its built-in hint.
  }
}

// StarterCards' save hook for an Agent's own set. The PATCH replaces the
// whole definition under a revision lock, so the write refetches first and
// changes starters alone — description keeps carrying the heading, and a 409
// surfaces through the caller's ordinary save-failed toast.
async function saveStudioStarters(cards: StarterCard[]) {
  const fresh = await platformApi.agent(studioAgentId);
  if (JSON.stringify(fresh.starters) !== JSON.stringify(studioAgent.value?.starters)) throw new Error(t("shortcuts.starterConflict"));
  const input: PlatformAgentInput = {
    name: fresh.name,
    description: fresh.description,
    instructions: fresh.instructions,
    model: fresh.model,
    models: fresh.models,
    skills: fresh.skills,
    mcp_servers: fresh.mcp_servers,
    tools: fresh.tools,
    color: fresh.color,
    icon: fresh.icon,
    category: fresh.category,
    // The Agent wire shape has no panel action; studio cards are prompts.
    starters: cards.map((c) => starterToWire({ title: c.title, label: c.label ?? "", icon: c.icon ?? "", prompt: c.prompt ?? "",
      ...(c.parts ? { parts: c.parts } : {}), ...(c.input_layout ? { input_layout: c.input_layout } : {}) })),
    status: fresh.status,
  };
  studioAgent.value = await platformApi.update(studioAgentId, input, fresh.revision);
}

// A starter card does one of two things, and neither of them is "run something
// the user did not read". A panel card navigates; a prompt card goes through
// planIntent, which fills the composer unless the deployment opted into sending.
async function onStarter(c: StarterCard) {
  if (c.panel) {
    let bookmark;
    try { bookmark = readBookmarks(localStorage).find(card => card.id === `legacy-panel:${c.panel}`); }
    catch (err) { showFlash(t("nativeBrowser.migrationFailed", { message: String(err) })); return; }
    let url = bookmark && !bookmark.needsRepair ? bookmark.url : undefined;
    if (url && c.at) { const target = new URL(url); target.hash = c.at; url = target.href; }
    addHubTab({ id: BROWSER_VIEW_ID, kind: "browser", label: t("agentView.dock.browser"), url, closable: true });
    showFlash(t(url ? "nativeBrowser.bookmarkTakeover" : "nativeBrowser.bookmarkRepairHint"));
    hubAt.value = null;
    activeSheet.value = "hub";
    return;
  }
  if (c.parts?.length) {
    const filled = await fillSavedStarter(c, false);
    if (filled?.allowSend && starters.value?.send) void send();
    return;
  }
  // The server validated this already; re-checking here keeps the composer's
  // one entry point authoritative rather than trusting the wire.
  const plan = planIntent({ text: c.prompt ?? "", send: starters.value?.send });
  if (plan.kind === "rejected") return;
  input.value = plan.text;
  if (plan.kind === "send") void send();
  else nextTick(() => focusComposer());
}

// Reusing a shortcut is a draft action, including during an active run. Keep
// existing text and leave sending to the composer, even for send-enabled sets.
async function onShortcutPick(c: StarterCard) {
  if (c.panel) {
    starterDialogOpen.value = false;
    onStarter(c);
    return;
  }
  if (c.parts?.length) {
    if (!await fillSavedStarter(c, true)) return;
    focusAfterStarterPick.value = true;
    starterDialogOpen.value = false;
    return;
  }
  const plan = planIntent({ text: c.prompt ?? "" });
  if (plan.kind === "rejected") return;
  input.value = input.value.trim() ? `${input.value}\n\n${plan.text}` : plan.text;
  focusAfterStarterPick.value = true;
  starterDialogOpen.value = false;
}

async function fillSavedStarter(card: StarterCard, append: boolean): Promise<{ allowSend: boolean } | null> {
  const sid = current.value;
  if (!sid || pickingStarter.value || !card.parts?.length) return null;
  const initialDraft = JSON.stringify([input.value, composerParts.value, composerLayout.value]);
  pickingStarter.value = true;
  try {
    const resolved = inputPartsHaveFiles(card.parts)
      ? await api.resolveStarterAssets(sid, { prompt: card.prompt, parts: card.parts, input_layout: card.input_layout })
      : { parts: card.parts, attachments: [] };
    if (viewClosed || current.value !== sid) return null;
    const restored = starterCardComposerParts({ ...card, parts: resolved.parts });
    const metadata = new Map(resolved.attachments.map(file => [file.path, file]));
    const keys = new Map<string, string>();
    for (const part of restored) {
      if (part.type !== "file" || !part.path) continue;
      const existing = attachments.value.find(file => file.path === part.path);
      const key = keys.get(part.path) ?? existing?.key ?? composerOccurrenceID();
      keys.set(part.path, key);
      part.key = key;
      const file = metadata.get(part.path);
      part.label = file?.name || part.label;
      composerFileCatalog.set(key, existing ?? { key, name: part.label, kind: file?.kind || kindOfName(part.label), status: "ready", path: part.path });
    }
    const draftChanged = initialDraft !== JSON.stringify([input.value, composerParts.value, composerLayout.value]);
    const keepDraft = append || draftChanged;
    const existingParts = keepDraft ? composerDraftWithToken(composerParts.value, composerToken.value) : [];
    const gap = existingParts.length ? [{ type: "text" as const, text: "\n\n" }] : [];
    // File copies may take a moment: append to the latest draft rather than
    // overwriting typing that happened while the request was in flight.
    if (!keepDraft) input.value = "";
    composerLayout.value = existingParts.length && composerLayout.value === "inline" ? "inline" : card.input_layout ?? "inline";
    updateComposerParts([...existingParts, ...gap, ...restored]);
    void nextTick(focusComposer);
    // An explicit send-enabled welcome card cannot also send text entered
    // while its assets were being copied. Leave that combined draft editable.
    return { allowSend: !draftChanged };
  } catch (err) {
    showFlash(t("starterCards.restoreFailed", { msg: err instanceof Error ? err.message : String(err) }), "error");
    return null;
  } finally { pickingStarter.value = false; }
}

function onStarterDialogClosed() {
  starterDraftSession = null;
  starterDraftFiles = new Map();
  // Wait for the dialog's focus trap to release before returning to the draft.
  if (!focusAfterStarterPick.value) return;
  focusAfterStarterPick.value = false;
  focusComposer();
}

// A picked chip only fills the composer, never sends: a follow-up sits under an
// answer the user just read, and a click there is "put this where I can edit
// it", not "spend a model call". The starters' send flag governs the empty-state
// cards alone. The handler stays separate so the cards sharing onStarter never
// count as follow-up picks.
function onFollowupPick(c: StarterCard) {
  const pick = followupPick.value;
  if (pick) {
    const index = pick.chips.indexOf(c);
    trackFollowup("followup.pick", { source: pick.source, index: index >= 0 ? index : undefined });
  }
  const plan = planIntent({ text: c.prompt ?? "" });
  if (plan.kind === "rejected") return;
  input.value = plan.text;
  nextTick(() => focusComposer());
}

// The slash-command list, in the order the hint row prints it. The web keeps
// exactly one command: everything the others did has a dedicated control here
// (policy and model pickers, the context meter's usage popover), so they lived
// on only as TUI habits — the TUI's own table in tui/lineedit.go is untouched.
const COMMANDS: { name: string; labelKey: string; hintKey: string; noAbbrev?: boolean }[] = [
  { name: "/compact", labelKey: "agentView.commands.compactName", hintKey: "agentView.commands.compact", noAbbrev: true },
];

// resolveCommand maps what was typed to a command: exact match wins, else the
// first command the prefix matches. A lone "/" resolves to nothing — running
// the first command on it would make one stray keystroke dangerous.
//
// noAbbrev entries are exempt from the prefix pass for the same reason, one step
// further: "/c" matches nothing else, so without this it would expand to /compact
// and replace model context off two keystrokes. They still appear in the hint
// row, which is the version of the shortcut that shows the full name first.
function resolveCommand(word: string): string | null {
  const exact = COMMANDS.find((c) => c.name === word);
  if (exact) return exact.name;
  if (word.length < 2) return null;
  return COMMANDS.find((c) => !c.noAbbrev && c.name.startsWith(word))?.name ?? null;
}

// Skill completion for the composer. Suggestions appear once the line is on
// the way to the word ("/skill" without the colon counts, so the list greets
// the typist instead of demanding exact grammar), while a path or a sentence
// is never interrupted.
const skills = ref<SkillInfo[]>([]);
const skillCatalogState = ref<"loading" | "ready" | "failed">("loading");
const mcpMethods = ref<ComposerMcpMethod[]>([]);
const mcpCatalogState = ref<"loading" | "ready" | "failed">("loading");
let mcpCatalogRequest = 0;
const skillConfigDeferred = ref(false);
let skillCatalogRequest = 0;
const composerTokenControl = ref<InstanceType<typeof ComposerToken> | null>(null);
const tokenPickerQuery = ref<string | null>(null);
const tokenPickerInput = ref<HTMLInputElement | null>(null);
const tokenPickerFocused = ref(false);
const tokenPickerComposing = ref(false);
const editingSkillID = ref<string | null>(null);
const editingMcpID = ref<string | null>(null);
const suggestionPopover = ref<HTMLElement | null>(null);
let tokenPickerSelection = [0, 0];
watch(current, () => {
  tokenPickerQuery.value = null;
  tokenPickerComposing.value = false;
  editingSkillID.value = null;
  editingMcpID.value = null;
  mcpMethods.value = [];
  mcpCatalogState.value = "loading";
  mcpCatalogRequest++;
  if (!isStudio || !current.value) void loadMcpMethods();
  if (!isStudio) return;
  studioCapabilities.value = null;
  studioCapabilitySnapshot = "";
  studioToolSync.select(current.value);
  studioCapabilitiesError.value = "";
  studioModelDeferred.value = false;
  studioModelSyncing.value = false;
  studioCapabilitiesRequest++;
  skills.value = [];
  skillConfigDeferred.value = false;
  skillConfigError.value = "";
  void refreshStudioConfiguration();
}, { flush: "sync" });
const suggestionMenuId = `composer-suggestions-${useId()}`;
const composerCard = ref<HTMLElement | null>(null);
const composerFocused = ref(false);
const composerComposing = ref(false);
const dismissedSuggestionInput = ref<string | null>(null);
const selectedSuggestionId = ref<string | null>(null);
const suggestionPopupStyle = ref<Record<string, string>>({});
const compactSuggestionDescription = computed(() => t("agentView.commands.compactContext", {
  used: contextReading.value.source === "unknown" ? "—" : `${contextReading.value.source === "estimated" ? t("uiCoverage.app.approximateValue", { value: contextReading.value.used.toLocaleString(locale.value) }) : contextReading.value.used.toLocaleString(locale.value)}`,
  total: ctxWindow.value > 0 ? ctxWindow.value.toLocaleString(locale.value) : "—",
}));
// The canonical draft remains plain text for starters, retries and the server.
// Only the native textarea's displayed value excludes the selected prefix.
// A typed MCP identity survives a catalog outage or revocation. Availability
// controls validation below, rather than changing the editable draft's shape.
const composerToken = computed(() => composerParts.value.some(part => part.type === "skill" || part.type === "mcp") ? null
  : readMcpComposerToken(input.value) ?? readComposerToken(input.value, skills.value, COMMANDS.map(command => command.name), mcpMethods.value));
provide(shortcutDraftSourceKey, () => {
  const sid = current.value;
  const parts = JSON.parse(JSON.stringify(composerDraftWithToken(composerParts.value, composerToken.value))) as ComposerPart[];
  const files = captureStarterFiles(parts, attachments.value);
  return {
    sessionId: sid,
    parts,
    async prepare() {
      if (!sid || viewClosed || current.value !== sid) throw new Error(t("shortcuts.scopeChanged"));
      const icons = await snapshotComposerElementIcons(parts, citationFavicons);
      const wire = await prepareStarterFileParts(icons, files, (file, name, dir) => {
        if (viewClosed || current.value !== sid) throw new Error(t("shortcuts.scopeChanged"));
        return api.uploadAttachment(sid, file, name, dir);
      });
      if (viewClosed || current.value !== sid) throw new Error(t("shortcuts.scopeChanged"));
      const prepared = blocksComposerParts(inputPartsBlocks(wire));
      const fileParts = wire.filter(part => part.type === "file");
      let fileIndex = 0;
      for (const part of prepared) if (part.type === "file") part.asFile = fileParts[fileIndex++]?.as_file;
      return prepared;
    },
  };
});
const composerSkills = computed(() => composerParts.value.filter(part => part.type === "skill"));
const composerMcps = computed(() => composerParts.value.filter(part => part.type === "mcp"));
const composerShortcuts = computed(() => composerParts.value.filter(part => part.type === "shortcut"));
const composerOffices = computed(() => composerParts.value.filter(part => part.type === "office"));
const officePreview = ref<OfficeReferenceSnapshot | null>(null);
const shortcutPreview = ref<ShortcutSnapshot | null>(null);
function updateShortcutPreview(visible: boolean) { if (!visible) shortcutPreview.value = null; }
const inlineSlashQuery = ref<{ text: string; from: number; to: number } | null>(null);
function updateInlineSelection() { inlineSlashQuery.value = inlineComposer.value?.getSlashQuery() ?? null; }
const textareaSlashQuery = ref<ComposerSlashQuery | null>(null);
function updateTextareaSelection() {
  const editor = inputBox.value;
  textareaSlashQuery.value = composerLayout.value === "top" && editor && !editor.readOnly
    ? composerSlashQuery(editor.value, editor.selectionStart, editor.selectionEnd) : null;
}
const composerBody = computed({
  get: () => composerToken.value?.body ?? input.value,
  set: (body: string) => { input.value = composerToken.value ? replaceComposerToken(composerToken.value, body) : body; },
});
const composerSkillDescription = (name: string) => {
  const skill = skills.value.find(skill => skill.name === name || skill.aliases?.includes(name));
  return skill ? builtinSkillDescription(skill.name, skill.description, skill.source === "embedded") : "";
};
const composerSuggestionSkills = computed(() => skills.value.map(skill => ({ ...skill, description: composerSkillDescription(skill.name) })));
const composerTokenLabel = computed(() => composerToken.value?.kind === "command" ? t("agentView.commands.compactTokenLabel") : composerToken.value?.name ?? "");
const composerTokenDescription = computed(() => composerToken.value?.kind === "command" ? compactSuggestionDescription.value
  : composerToken.value?.kind === "mcp" ? mcpMethods.value.find(method => method.server === composerToken.value?.mcp?.server && method.name === composerToken.value?.mcp?.name)?.description ?? ""
  : composerSkillDescription(composerToken.value?.name ?? ""));
const nestedSkillNames = computed(() => composerShortcuts.value.flatMap(part => inputPartsSkills(part.parts ?? [])));
const skillInvocation = computed(() => composerSkills.value.length > 0 || input.value.trimStart().startsWith("/skill:"));
const skillInvocationName = computed(() => /^\/skill:([^ \t\r\n\f\v]+)(?:[ \t\r\n\f\v]|$)/.exec(input.value.trimStart())?.[1] ?? "");
const composerSkillError = computed(() => {
  if (!skillInvocation.value && !nestedSkillNames.value.length) return "";
  if (skillCatalogState.value === "loading") return t("agentView.commands.skillsLoading");
  if (skillCatalogState.value === "failed") return t("agentView.commands.skillsFailed");
  if (composerSkills.value.length || nestedSkillNames.value.length) {
    const unavailable = [...composerSkills.value.map(part => part.name), ...nestedSkillNames.value].find(name => !skills.value.some(skill => skill.name === name || skill.aliases?.includes(name)));
    return unavailable ? t("agentView.commands.unknownSkill", { name: unavailable }) : "";
  }
  if (!skillInvocationName.value) return t("agentView.commands.selectSkill");
  if (!skills.value.some(skill => skill.name === skillInvocationName.value || skill.aliases?.includes(skillInvocationName.value))) return t("agentView.commands.unknownSkill", { name: skillInvocationName.value });
  return "";
});
const composerSkillWaiting = computed(() => skillInvocation.value && busy.value);
const nestedMcpMethods = computed(() => composerShortcuts.value.flatMap(part => inputPartsMcps(part.parts ?? [])));
const mcpInvocation = computed(() => composerMcps.value.length > 0 || nestedMcpMethods.value.length > 0 || input.value.trimStart().startsWith("/mcp:"));
const composerMcpError = computed(() => {
  if (!mcpInvocation.value) return "";
  if (mcpCatalogState.value === "loading") return t("agentView.commands.mcpLoading");
  if (mcpCatalogState.value === "failed") return t("agentView.commands.mcpFailed");
  if (!mcpMethods.value.length) return t("agentView.commands.noMcp");
  const references = [...composerMcps.value, ...nestedMcpMethods.value];
  if (input.value.trimStart().startsWith("/mcp:")) {
    const invocation = parseMcpInvocation(input.value.trimStart());
    if (!invocation) return t("agentView.commands.selectMcpMethod");
    references.push(invocation);
  }
  const unavailable = references.find(reference => !mcpMethods.value.some(method => method.server === reference.server && method.name === reference.name));
  return unavailable ? t("agentView.commands.unknownMcpMethod", { name: `${unavailable.server}/${unavailable.name}` }) : "";
});
const composerMcpWaiting = computed(() => mcpInvocation.value && busy.value);
const composerInvocationError = computed(() => composerSkillError.value || composerMcpError.value);
const composerInvocationWaiting = computed(() => composerSkillWaiting.value || composerMcpWaiting.value);
const composerWaitingMessage = computed(() => t(composerMcpWaiting.value ? "agentView.commands.mcpWaiting" : "agentView.commands.skillWaiting"));
watch([input, skills, composerComposing], () => {
  if (composerComposing.value) return;
  const normalized = normalizeComposerTokenInput(input.value, skills.value);
  if (normalized !== input.value) input.value = normalized;
}, { flush: "post" });
const suggestionQuery = computed(() => tokenPickerQuery.value !== null
  ? `${composerToken.value?.kind === "command" ? "/" : editingMcpID.value || composerToken.value?.kind === "mcp" ? "/mcp:" : "/skill:"}${tokenPickerQuery.value}`
  : composerLayout.value === "inline" ? inlineSlashQuery.value?.text ?? "" : textareaSlashQuery.value?.text ?? "");
const suggestionItems = computed(() => isDelegatedEmbed ? [] : composerSuggestions(suggestionQuery.value,
  (tokenPickerQuery.value !== null
    ? composerToken.value?.kind === "command" ? COMMANDS : []
    : composerToken.value || composerParts.value.some(part => part.type !== "text") || input.value.trim() !== suggestionQuery.value ? [] : COMMANDS).map(command => ({ name: command.name, label: t(command.labelKey), description: command.name === "/compact" ? compactSuggestionDescription.value : t(command.hintKey) })), composerSuggestionSkills.value,
  { label: t("agentView.commands.skillEntryName"), description: t("agentView.commands.skillEntry") }, mcpMethods.value,
  { label: t("agentView.commands.mcpEntryName"), description: t("agentView.commands.mcpEntry") }));
watch(suggestionQuery, (query, previous) => {
  // Connections may finish after the initial catalog request. Opening the
  // method picker reconciles cached capabilities once, rather than on every key.
  if (query.startsWith("/mcp:") && !previous.startsWith("/mcp:")) void loadMcpMethods();
});
const suggestionMenuVisible = computed(() => tokenPickerQuery.value !== null
  ? tokenPickerFocused.value
  : !composerComposing.value && composerFocused.value && dismissedSuggestionInput.value !== input.value && suggestionItems.value.length > 0);
const mcpPickerActive = computed(() => !!editingMcpID.value || composerToken.value?.kind === "mcp");
const emptySuggestionMessage = computed(() => mcpPickerActive.value
  ? mcpCatalogState.value === "loading" ? t("agentView.commands.mcpLoading")
    : mcpCatalogState.value === "failed" ? t("agentView.commands.mcpFailed")
    : !mcpMethods.value.length ? t("agentView.commands.noMcp") : t("agentView.commands.noMatchingSkill")
  : t("agentView.commands.noMatchingSkill"));
const selectedSuggestionOption = computed(() => suggestionMenuVisible.value && selectedSuggestionId.value ? `${suggestionMenuId}-${selectedSuggestionId.value}` : undefined);
watch([input, tokenPickerQuery], () => {
  dismissedSuggestionInput.value = null;
  selectedSuggestionId.value = suggestionItems.value[0]?.id ?? null;
}, { flush: "sync" });
watch(suggestionItems, items => {
  if (!items.some(item => item.id === selectedSuggestionId.value)) selectedSuggestionId.value = items[0]?.id ?? null;
}, { flush: "sync" });
watch([input, composerToken, composerLayout], () => { void nextTick(updateTextareaSelection); }, { flush: "post" });

function highlightSuggestion(id: string) {
  if (suggestionItems.value.some(item => item.id === id)) selectedSuggestionId.value = id;
}
function completeSuggestion(id = selectedSuggestionId.value) {
  if (tokenPickerComposing.value) return;
  const item = suggestionItems.value.find(item => item.id === id);
  if (!item) return;
  if (editingMcpID.value && item.kind === "mcp" && item.mcp) {
    const id = editingMcpID.value, method = item.mcp;
    updateComposerParts(composerParts.value.map(part => part.type === "mcp" && part.id === id
      ? { ...part, server: method.server, name: method.name, label: `${method.server}/${method.name}` } : part));
    editingMcpID.value = null; tokenPickerQuery.value = null; tokenPickerFocused.value = false;
    void nextTick(focusComposer); return;
  }
  if (editingSkillID.value && item.kind === "skill") {
    const id = editingSkillID.value, name = item.value.slice(7).trim();
    updateComposerParts(composerParts.value.map(part => part.type === "skill" && part.id === id ? { ...part, name, label: name } : part));
    editingSkillID.value = null; tokenPickerQuery.value = null; tokenPickerFocused.value = false;
    void nextTick(focusComposer); return;
  }
  if (composerLayout.value === "inline" && tokenPickerQuery.value === null && item.kind !== "command") {
    if (inlineComposer.value?.completeSlash(item.value, item.kind === "skill" ? item.value.slice(7).trim() : undefined, item.mcp)) {
      dismissedSuggestionInput.value = null;
      void nextTick(updateInlineSelection);
      return;
    }
  }
  if (composerLayout.value === "top" && tokenPickerQuery.value === null && item.kind !== "command") {
    const query = textareaSlashQuery.value;
    if (!query) return;
    let caret = query.from;
    if (item.kind === "skill" || item.kind === "mcp" && item.mcp) {
      const parts = composerDraftWithToken(replaceComposerTypedText(composerParts.value, composerBody.value), composerToken.value);
      updateComposerParts(item.kind === "mcp" && item.mcp ? completeComposerTextMcp(parts, query, item.mcp)
        : completeComposerTextSkill(parts, query, item.value.slice(7).trim()));
    } else {
      composerBody.value = composerBody.value.slice(0, query.from) + item.value + composerBody.value.slice(query.to);
      caret += item.value.length;
    }
    dismissedSuggestionInput.value = null;
    void nextTick(() => { focusComposer(); inputBox.value?.setSelectionRange(caret, caret); updateTextareaSelection(); });
    return;
  }
  const replacing = tokenPickerQuery.value !== null;
  const body = replacing ? composerBody.value : "";
  input.value = item.value + (replacing && (item.kind === "skill" || item.kind === "mcp") ? body : "");
  tokenPickerQuery.value = null;
  tokenPickerFocused.value = false;
  tokenPickerComposing.value = false;
  void nextTick(() => {
    focusComposer();
    const length = composerBody.value.length;
    inputBox.value?.setSelectionRange(replacing ? Math.min(tokenPickerSelection[0], length) : length, replacing ? Math.min(tokenPickerSelection[1], length) : length);
  });
}
function changeComposerToken() {
  editingSkillID.value = null;
  editingMcpID.value = null;
  if (composerToken.value?.kind === "mcp") void loadMcpMethods();
  tokenPickerSelection = [inputBox.value?.selectionStart ?? 0, inputBox.value?.selectionEnd ?? 0];
  tokenPickerQuery.value = "";
  tokenPickerFocused.value = true;
  tokenPickerComposing.value = false;
  void nextTick(() => tokenPickerInput.value?.focus());
}
function removeComposerToken() {
  const token = composerToken.value;
  if (!token) return;
  input.value = token.body;
  tokenPickerQuery.value = null;
  tokenPickerFocused.value = false;
  tokenPickerComposing.value = false;
  editingSkillID.value = null;
  editingMcpID.value = null;
  void nextTick(() => { focusComposer(); inputBox.value?.setSelectionRange(0, 0); });
}
function dismissSuggestions() {
  if (tokenPickerQuery.value !== null) {
    tokenPickerQuery.value = null;
    editingSkillID.value = null;
    editingMcpID.value = null;
    tokenPickerFocused.value = false;
    tokenPickerComposing.value = false;
    void nextTick(() => focusComposer());
  } else dismissedSuggestionInput.value = input.value;
}
function onTokenPickerBlur() {
  tokenPickerFocused.value = false;
  tokenPickerComposing.value = false;
  void nextTick(() => {
    if (!suggestionPopover.value?.contains(document.activeElement)) {
      tokenPickerQuery.value = null;
      editingSkillID.value = null;
      editingMcpID.value = null;
    }
  });
}
function measureSuggestionPopup() {
  if (!suggestionMenuVisible.value || !composerCard.value) return;
  const rect = composerCard.value.getBoundingClientRect();
  const viewport = window.visualViewport;
  const left = viewport?.offsetLeft ?? 0, top = viewport?.offsetTop ?? 0;
  const width = viewport?.width ?? window.innerWidth, height = viewport?.height ?? window.innerHeight;
  const gap = 8, edge = 8;
  const above = rect.top - top - gap - edge;
  const below = top + height - rect.bottom - gap - edge;
  const placeAbove = above >= 120 || above >= below;
  suggestionPopupStyle.value = {
    left: `${Math.max(left + edge, Math.min(rect.left, left + width - Math.min(rect.width, width - 2 * edge) - edge))}px`,
    width: `${Math.min(rect.width, width - 2 * edge)}px`,
    ...(placeAbove ? { bottom: `${window.innerHeight - rect.top + gap}px` } : { top: `${rect.bottom + gap}px` }),
    "--composer-menu-height": `${Math.max(60, placeAbove ? above : below)}px`,
  };
}
watch([suggestionMenuVisible, input, tokenPickerQuery, floatingConversationActive], () => { void nextTick(measureSuggestionPopup); }, { flush: "post" });
useResizeObserver(composerCard, measureSuggestionPopup);
onMounted(() => {
  window.addEventListener("resize", measureSuggestionPopup);
  window.addEventListener("scroll", measureSuggestionPopup, true);
  window.visualViewport?.addEventListener("resize", measureSuggestionPopup);
  window.visualViewport?.addEventListener("scroll", measureSuggestionPopup);
});
onBeforeUnmount(() => {
  window.removeEventListener("resize", measureSuggestionPopup);
  window.removeEventListener("scroll", measureSuggestionPopup, true);
  window.visualViewport?.removeEventListener("resize", measureSuggestionPopup);
  window.visualViewport?.removeEventListener("scroll", measureSuggestionPopup);
});

function open(sid: string) {
  if (viewClosed) return;
  if (current.value === sid) return;
  // Studio connects only to sessions the bound Agent actually owns: every
  // caller passes an id from the agent-scoped list, and this guard is what
  // keeps a stray id (a stale schedule row, a forged ?c=) from ever
  // reaching the stream.
  if (isStudio && !sessions.value.some((s) => s.id === sid)) return;
  pendingWorkspace.value = null;
  current.value = sid;
  stream.connect(sid);
  if (isStudio) {
    // Remembered per Agent, and the deep link stays honest: /a/{id}?c={sid}.
    localStorage.setItem(storeKey(STUDIO_LAST_SESSION), sid);
    const websiteQuery = initialWebsiteLink.present && !websiteLinkRevealed.value && sid === initialWebsiteLink.sessionId && initialWebsiteLink.serviceId;
    const target = new URL(conversationPath(sid), location.origin);
    if (websiteQuery) target.searchParams.set("web_service", initialWebsiteLink.serviceId!);
    history.replaceState(null, "", target.pathname + target.search);
  }
  stickToBottom.value = true;
  // The rail's dots are measured from the DOM, so they have to wait for the
  // new session's first render.
  void nextTick(scheduleRailCompute);
}

// A new session starts as a local draft at the workspace root. The real session
// is created only after the first question is accepted, so no empty transcript
// appears just because someone clicked the button. The workspace choice stays explicit.
const pickerOpen = ref(false);
function startNewSession() {
  if (!canUse("conversation") || studioEntryLoading.value) return;
  if (!isStudio && canUse("workspaces")) pickerOpen.value = true;
  else void createSession();
}
// Shared models and extension libraries are managed in Studio. The dock's
// Skill sheet only inspects the capabilities selected for this conversation.

async function onCreateWorkspace(workspace: string) {
  if (!canUse("workspaces")) return unsupported();
  pickerOpen.value = false;
  await createSession(workspace);
}

async function createSession(workspace = pendingWorkspace.value ?? "") {
  if (viewClosed || !canUse("conversation")) return;
  current.value = null;
  pendingWorkspace.value = workspace;
  draftPolicyMode.value = "standard";
  stream.disconnect();
  stream.pendingUser.value = [];
  stream.uploads.value = [];
  stream.apply({ seq: 0, type: "snapshot", ts: Date.now(), snapshot: {
    seq: 0, messages: [], results: {}, live: emptyLive(), run: { active: false },
    policy: { mode: "standard" }, usage: { input: 0, output: 0 }, context_tokens: 0,
  } });
  studioEntry.finishWithoutSession();
  if (isStudio) history.replaceState(null, "", conversationPath());
}

async function createSessionForFirstMessage(): Promise<string | null> {
  if (current.value) return current.value;
  const workspace = pendingWorkspace.value;
  if (workspace === null) return null;
  const approvalMode = draftPolicyMode.value === "standard" ? undefined : draftPolicyMode.value;
  sending.value = true;
  try {
    const sid = isStudio
      ? (await platformApi.createSession(studioAgentId, {
        ...(workspace ? { workspace } : {}),
        ...(approvalMode ? { approvalMode } : {}),
      })).id
      : (await api.createSession(undefined, workspace, approvalMode)).session_id;
    if (viewClosed || current.value || pendingWorkspace.value !== workspace) return null;
    await loadSessions();
    if (viewClosed || current.value || pendingWorkspace.value !== workspace) return null;
    open(sid);
    return sid;
  } catch (err) {
    if ((err as { status?: number }).status === 401 || (isDelegatedEmbed && (err as { status?: number }).status === 403)) {
      tokenRejected.value = true;
      return null;
    }
    showFlash(t("agentView.flash.createFailed", { msg: (err as Error).message }), "error");
    return null;
  } finally {
    sending.value = false;
  }
}

async function removeSession(sid: string) {
  // Deleting removes the JSONL transcript, which is the only record of the
  // conversation. There is no undo, so it gets a confirmation naming what goes.
  const title = sessions.value.find((s) => s.id === sid)?.title;
  deleteTarget.value = { sid, title: title ?? "" };
}

const deleteTarget = ref<{ sid: string; title: string } | null>(null);
const deleteBusy = ref(false);

const deleteOpen = computed({
  get: () => deleteTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !deleteBusy.value) deleteTarget.value = null;
  },
});

async function confirmDelete() {
  const target = deleteTarget.value;
  if (!target) return;
  deleteBusy.value = true;
  try {
    await api.deleteSession(target.sid);
    // The delete collects the session's unreferenced uploads in the
    // workspace; the tree should not keep showing files that just went away.
    invalidateTree();
    deleteTarget.value = null;
    if (current.value === target.sid) {
      stream.disconnect();
      current.value = null;
      if (isStudio) {
        // The remembered session just died with the delete; the deep link
        // goes back to the bare Agent URL until the next open() re-arms it.
        localStorage.removeItem(storeKey(STUDIO_LAST_SESSION));
        history.replaceState(null, "", conversationPath());
      }
    }
    await loadSessions();
    if (!current.value && sessions.value.length) open(sessions.value[0].id);
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.deleteBusy")
        : t("agentView.flash.deleteFailed", { msg: e.message }),
      "error",
    );
  } finally {
    deleteBusy.value = false;
  }
}

// The ⋮ menu per sidebar item. Its button is hover-revealed, so while the menu
// is open we remember which item it belongs to — otherwise the button would
// vanish under the cursor the moment the menu opened.
const menuOpenFor = ref("");

function onSessionMenu(cmd: string, s: SessionInfo) {
  if (cmd === "pin") void pinSession(s);
  else if (cmd === "rename") void renameSession(s);
  else if (cmd === "export") exportSession(s);
  else if (cmd === "delete") void removeSession(s.id);
}

async function pinSession(s: SessionInfo) {
  if (isDelegatedEmbed) return unsupported();
  try {
    await api.updateSession(s.id, { pinned: !s.pinned });
    await loadSessions();
  } catch (err) {
    showFlash(t("agentView.flash.pinFailed", { msg: (err as Error).message }), "error");
  }
}

// ── Session import/export ──────────────────────────────────────────────────
// Export is the ⋮ menu's "导出会话": the server renders the whole live branch
// (envelope, every message with its blocks, per-turn stats, totals) as one
// Markdown document. A plain anchor click is enough — the endpoint answers
// with Content-Disposition: attachment so the browser saves instead of
// navigating, and the token rides the query the way every other download
// URL in the app does. Import below is the way back.
function exportSession(s: SessionInfo) {
  if (isDelegatedEmbed) return unsupported();
  const a = document.createElement("a");
  a.href = sessionExportMarkdownURL(s.id);
  a.download = "";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// Import is the group header's other hover button: pick the file a previous
// "导出会话" produced and the conversation comes back as a NEW session inside
// that project — the original file is never touched. The Markdown reading
// copy is the format the ⋮ menu hands out, so it is the default; a ".json"
// portable backup takes the verbatim-records path instead.
const importInput = ref<HTMLInputElement | null>(null);
// The project the picker was opened for, root-relative ("" = the root):
// settled at click time because the file dialog outlives the hover.
const importWorkspace = ref("");

function importInProject(g: ProjectGroup) {
  // Import creates an account-wide session, which no Agent can own — Studio's
  // answer to "bring a transcript in" is therefore "not here", same as the
  // delegated embed's.
  if (isDelegatedEmbed || isStudio) return unsupported();
  importWorkspace.value = g.cwd === (cwd.value || "/") ? "" : wsOf({ cwd: g.cwd } as SessionInfo);
  importInput.value?.click();
}

async function onImportFile(ev: Event) {
  if (isDelegatedEmbed || isStudio) return unsupported();
  const input = ev.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = ""; // picking the same file twice must fire "change" again
  if (!file) return;
  try {
    const text = await file.text();
    const res = file.name.toLowerCase().endsWith(".json")
      ? await api.importSessions(text)
      : await api.importSessionMarkdown(text, importWorkspace.value);
    await loadSessions();
    if (res.session_ids.length > 0) open(res.session_ids[0].id);
    showFlash(t("agentView.flash.imported", { n: res.imported }));
    if (res.skipped?.length) {
      showFlash(t("agentView.flash.importSkipped", { n: res.skipped.length }));
    }
  } catch (err) {
    if ((err as { status?: number }).status === 401 || (isDelegatedEmbed && (err as { status?: number }).status === 403)) {
      tokenRejected.value = true;
      return;
    }
    showFlash(t("agentView.flash.importFailed", { msg: (err as Error).message }), "error");
  }
}

// ── Shared answers ──────────────────────────────────────────────────────────
// The exchange's action row: one pair of buttons per question, shown once the
// run has settled — copy takes the whole reply's markdown (every turn's text,
// what the model wrote, not the rendered HTML), share snapshots that same
// question+answer onto a public page and shows the link. The dialog owns
// revocation — it stays open until closed, which is the commitment moment, and
// its "stop sharing" is the only revocation UI.
const shareOpen = ref(false);
const shareLink = ref<{ id: string; url: string } | null>(null);
const sharingBusy = ref(false);
const copiedExchange = ref<string | null>(null);
let copiedExTimer: ReturnType<typeof setTimeout> | undefined;

// exchangeAnswerText is the whole reply to the exchange's question, in turn
// order — the same span the server snapshots for a share, so copy and share
// can never disagree about what "the answer" was.
function exchangeAnswerText(ex: Exchange): string {
  return ex.turns
    .map((t) => t.text.trim())
    .filter((t) => t !== "")
    .join("\n\n");
}

// exchangeWorkMarkdown renders the whole exchange as a document: the
// question, every turn's thinking and tool traffic, then the reply. The
// copy button's contract is "everything this question produced" — an answer
// without the commands it rested on answers nothing later. Four-backtick
// fences so a tool dump containing ``` cannot eat the sections around it
// (the same rule the server-side export in sessionmd.go follows).
function exchangeWorkMarkdown(ex: Exchange, answer: string): string {
  const out: string[] = [];
  const q = ex.user?.text.trim();
  if (q) out.push(`## Q\n\n${q}`);
  const work: string[] = [];
  for (const turn of ex.turns) {
    const th = turn.thinking.trim();
    if (th) work.push(`**Thinking**\n\n${"````"}text\n${th}\n${"````"}`);
    for (const call of turn.calls) {
      const args = call.args as { command?: unknown; path?: unknown } | null;
      if (call.name === "bash" && args && typeof args.command === "string") {
        const err = call.result?.is_error ? " (error)" : "";
        const parts = [`**bash${err}**\n\n${"````"}bash\n$ ${args.command}`];
        const output = call.result?.text ?? call.liveOutput ?? "";
        if (output.trim()) parts.push(output.trim());
        parts.push("````");
        work.push(parts.join("\n"));
      } else {
        const err = call.result?.is_error ? " (error)" : "";
        const path = args && typeof args.path === "string" ? args.path : "";
        const where = path ? ` \`${path}\`` : "";
        const parts = [`**${call.name}${err}**${where}`];
        const argsText = JSON.stringify(call.args ?? {}, null, 2);
        if (argsText !== "{}") parts.push(`${"````"}json\n${argsText}\n${"````"}`);
        const output = (call.result?.text ?? "").trim();
        if (output) parts.push(`${"````"}text\n${output}\n${"````"}`);
        work.push(parts.join("\n\n"));
      }
    }
  }
  if (work.length) out.push(`## Work\n\n${work.join("\n\n")}`);
  if (answer.trim()) out.push(`## Answer\n\n${answer.trim()}`);
  return out.join("\n\n");
}

// The exchange action row's download: the server's export.md is the whole
// live branch (envelope, every message and block, per-turn stats, totals) —
// the same document the sidebar ⋮ menu hands out, one click closer.
function downloadCurrentMarkdown() {
  const sid = current.value;
  if (!sid) return;
  const a = document.createElement("a");
  a.href = sessionExportMarkdownURL(sid);
  a.download = "";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

async function copyExchange(ex: Exchange) {
  try {
    let text = exchangeAnswerText(ex);
    // The exchange's stamps, deduped by label across turns: copy numbering is
    // per document (the joined text), so the pasted artifact agrees with
    // itself even where two turns reused a number on screen. Anchors degrade
    // to bare display numbers before the document is assembled.
    const sources = new Map<string, CiteSource>();
    for (const turn of ex.turns) {
      for (const s of turn.sources ?? []) if (!sources.has(s.label)) sources.set(s.label, s);
    }
    let refs = "";
    if (sources.size > 0) {
      const { nums } = numberCitations(text, new Set(sources.keys()));
      const used = [...nums.entries()].sort((a, b) => a[1] - b[1]);
      refs = used
        .map(([label, n]) => {
          const s = sources.get(label);
          return s ? `[${n}] ${describeSource(s)}` : `[${n}] ${label}`;
        })
        .join("\n");
      text = rewriteCitations(text, nums);
    }
    // The full document, not the answer alone: question, every turn's
    // thinking and tool traffic, then the reply.
    const doc = exchangeWorkMarkdown(ex, text);
    let plain = doc;
    let md = doc;
    if (refs) {
      plain = `${doc}\n\n${refs}`;
      md = `${doc}\n\n## References\n\n${refs.replace(/^/gm, "- ")}`;
    }
    // Both flavors at once where the clipboard allows it: plain text for chat
    // boxes, markdown for editors. The anchors degrade to bare display
    // numbers in both, with the evidence list appended. A browser that
    // refuses the multi-format write still gets the markdown text.
    let done = false;
    if (typeof ClipboardItem !== "undefined" && navigator.clipboard.write) {
      try {
        await navigator.clipboard.write([
          new ClipboardItem({
            "text/plain": new Blob([plain], { type: "text/plain" }),
            "text/markdown": new Blob([md], { type: "text/markdown" }),
          }),
        ]);
        done = true;
      } catch {
        done = false;
      }
    }
    if (!done) await navigator.clipboard.writeText(md);
    copiedExchange.value = ex.id;
    clearTimeout(copiedExTimer);
    copiedExTimer = setTimeout(() => (copiedExchange.value = null), 1500);
  } catch {
    showFlash(t("agentView.flash.copyFailed"), "error");
  }
}

async function onShareExchange(ex: Exchange) {
  if (!canUse("sharing")) return unsupported();
  if (!current.value || !ex.user || sharingBusy.value) return;
  sharingBusy.value = true;
  try {
    shareLink.value = await api.createShare(current.value, ex.user.id);
    shareOpen.value = true;
  } catch (err) {
    showFlash(t("agentView.flash.shareFailed", { msg: (err as Error).message }), "error");
  } finally {
    sharingBusy.value = false;
  }
}

// ── Scheduled jobs ───────────────────────────────────────────────────────────
// The clock panel (web/scheduleapi.go's UI). While it is closed, a light 60s
// poll feeds the rail button's red dot; once open, the sheet polls itself
// (15s, 3s while a job runs) and reports the same flag up through @attention.
// Attention covers failures, unavailable Agents and requests for human action.
const scheduleOpen = computed({
  get: () => activeSheet.value === "tasks",
  set: (open: boolean) => {
    if (open) activeSheet.value = "tasks";
    else if (activeSheet.value === "tasks") activeSheet.value = null;
  },
});
let taskEntryEpoch = 0;
const taskOpening = ref(false);
let taskNavigationEpoch = 0;
let taskNavigationController: AbortController | undefined;
const scheduleAttention = ref(false);
const scheduleDraft = ref<ConversationPlanDraft | null>(null);
const preparingScheduleDraft = ref(false);

function openScheduleCenter() {
  if (!studioAgentId) return;
  cancelPendingTaskNavigation();
  scheduleDraft.value = null;
  scheduleOpen.value = true;
}

function scheduleMessage(message: NonNullable<Exchange["user"]>) {
  if (!canUse("schedules") || !studioAgentId || !current.value || stream.switching.value) return;
  scheduleDraft.value = messagePlanDraft(message, current.value, studioAgentId, composerOccurrenceID());
  scheduleOpen.value = true;
}

async function scheduleCurrentDraft() {
  if (!canUse("schedules") || !studioAgentId || !current.value || stream.switching.value || preparingScheduleDraft.value) return;
  if (composerToken.value?.kind === "command") {
    showFlash(t("agentView.ask.scheduleCommand"), "error");
    return;
  }
  const sid = current.value;
  const parts = composerDraftWithToken(composerParts.value, composerToken.value);
  if (!parts.some(part => part.type !== "text" || part.text.trim())) {
    showFlash(t("agentView.ask.scheduleEmpty"));
    return;
  }
  const layout = composerLayout.value;
  const files = captureStarterFiles(parts, attachments.value);
  preparingScheduleDraft.value = true;
  try {
    const icons = await snapshotComposerElementIcons(parts, citationFavicons);
    const wire = await prepareStarterFileParts(icons, files, async (file, name, dir) => {
      if (viewClosed || current.value !== sid) throw new Error(t("shortcuts.scopeChanged"));
      return api.uploadAttachment(sid, file, name, dir);
    });
    if (viewClosed || current.value !== sid) throw new Error(t("shortcuts.scopeChanged"));
    scheduleDraft.value = conversationPlanDraft(wire, sid, studioAgentId, layout, composerOccurrenceID());
    scheduleOpen.value = true;
  } catch (err) {
    showFlash(err instanceof Error ? err.message : String(err), "error");
  } finally { preparingScheduleDraft.value = false; }
}

async function refreshScheduleAttention() {
  if (!canUse("schedules") || !studioAgentId) { scheduleAttention.value = false; return; }
  try {
    const info = await taskCenterApi.list(studioAgentId);
    scheduleAttention.value = !!info.queue_error || taskCenterNeedsAttention(info.tasks, info.runs);
  } catch {
    // Non-fatal: 503 means no backend on this platform (the panel says so when
    // opened); anything else just leaves the dot one cycle stale.
  }
}

const scheduleTimer = window.setInterval(() => {
  if (canUse("schedules") && !document.hidden && !scheduleOpen.value && !gated.value) void refreshScheduleAttention();
}, 60_000);
onBeforeUnmount(() => window.clearInterval(scheduleTimer));

function requestedTaskSessionId(): string | null {
  const id = new URLSearchParams(location.search).get("task_session")?.trim();
  return id && /^[A-Za-z0-9_-]{1,160}$/.test(id) ? id : null;
}
function taskResultLink(id?: string) {
  if (!isStudio) return;
  const url = new URL(conversationPath(current.value), location.origin);
  if (id) url.searchParams.set("task_session", id);
  history.replaceState(null, "", url.pathname + url.search);
}
function restoreOrdinaryTaskContext(excluded: string) {
  // A task link never creates or adopts an ordinary conversation. Keep the
  // current chat, then this Agent's remembered/most recent normal chat.
  if (current.value && current.value !== excluded && sessions.value.some(session => session.id === current.value)) return;
  const remembered = localStorage.getItem(storeKey(STUDIO_LAST_SESSION));
  const ordinary = sessions.value.filter(session => session.id !== excluded).sort((a, b) => (b.updated ?? 0) - (a.updated ?? 0));
  const requested = requestedSessionId(location.search);
  const fallback = chooseBootSession(ordinary, requested === excluded ? null : requested, remembered === excluded ? null : remembered);
  if (fallback && fallback !== excluded) open(fallback);
}
function cancelTaskNavigation() { ++taskNavigationEpoch; ++taskEntryEpoch; taskNavigationController?.abort(); taskNavigationController = undefined; taskOpening.value = false; }
cancelPendingTaskNavigation = cancelTaskNavigation;
onBeforeUnmount(cancelTaskNavigation);
function revealTaskTab(tab: DockTab, sessionId?: string) {
  stopBrowserDiscovery(); browserFocusPolicy.manual();
  hubTabs.value = openTab(hubTabs.value, tab); hubActiveTab.value = tab.id; hubAt.value = null;
  activeSheet.value = "hub"; scheduleOpen.value = false;
  taskResultLink(sessionId);
}
async function openTaskSheet(taskId: string, label = "", agentId = studioAgentId, requestedSession?: string) {
  if (!isStudio || isDelegatedEmbed || viewClosed || agentId !== studioAgentId || !agentId) return;
  const match = /^schedule:([A-Za-z0-9][A-Za-z0-9._-]{0,39})$/.exec(taskId);
  if (!match || requestedSession !== undefined && !/^[A-Za-z0-9_-]{1,160}$/.test(requestedSession)) return;
  cancelTaskNavigation(); cancelPendingAnswerFile();
  const mine = taskNavigationEpoch, controller = new AbortController(); taskNavigationController = controller; taskOpening.value = true;
  const valid = () => !viewClosed && !controller.signal.aborted && mine === taskNavigationEpoch;
  const sourceId = match[1]; let sessionId = requestedSession, title = label;
  try {
    if (!sessionId) {
      const result = await taskCenterApi.list(agentId, controller.signal); if (!valid()) return;
      const task = result.tasks.find(item => item.id === taskId && item.source === "schedule" && item.source_id === sourceId && item.agent_id === agentId);
      if (task && (task.executor !== "agent" || task.workflow_id)) { openScheduleCenter(); return; }
      const retained = result.runs.filter(run => run.task_id === taskId && run.source === "schedule" && run.source_id === sourceId && run.agent_id === agentId && !run.workflow_id)
        .sort((a, b) => Number(b.status === "running" || b.waiting_human) - Number(a.status === "running" || a.waiting_human) || b.created_at - a.created_at)[0];
      sessionId = retained?.session_id || task?.session_id;
      title = task?.title || retained?.title || title;
      if (!sessionId) {
        if (!task && !retained) throw new Error("Task identity unavailable");
        if (task) {
          const definition = await api.scheduleJob(sourceId); if (!valid()) return;
          if (definition.job.id !== sourceId || definition.job.agent_id !== agentId || definition.job.workflow_id) throw new Error("Task scope changed");
          title = definition.job.title || title;
        }
      }
    }
    if (sessionId) {
      const result = await api.session(sessionId, controller.signal); if (!valid()) return;
      const role = result.background_task;
      if (result.session_id !== sessionId || role?.source !== "schedule" || role.agent_id !== agentId || role.source_id !== sourceId || role.task_id !== taskId) throw new Error("Task execution scope changed");
    }
    if (!valid()) return;
    revealTaskTab({ id: `task:${agentId}:${taskId}`, kind: "task", taskId, taskSource: "schedule", taskSourceId: sourceId, agentId,
      ...(sessionId ? { taskRunSession: sessionId } : {}), label: title || t("backgroundAccess.fallback"), closable: true }, sessionId);
  } catch (error) { if (valid() && (error as Error)?.name !== "AbortError") showFlash(t("backgroundAccess.unavailable"), "error"); }
  finally { if (mine === taskNavigationEpoch) { taskOpening.value = false; taskNavigationController = undefined; } }
}
async function openIndependentSentinelTask(watchId: string, agentId: string) {
  if (!isStudio || isDelegatedEmbed || viewClosed || agentId !== studioAgentId || !/^[A-Za-z0-9_-]{1,160}$/.test(watchId)) return;
  cancelTaskNavigation(); cancelPendingAnswerFile();
  const mine = taskNavigationEpoch, controller = new AbortController(); taskNavigationController = controller; taskOpening.value = true;
  const valid = () => !viewClosed && !controller.signal.aborted && mine === taskNavigationEpoch;
  try {
    const detail = await sentinelSessionsApi.detail(watchId, controller.signal); if (!valid()) return;
    const tab = detail.watch.id === watchId ? sentinelTab(detail.watch, agentId, false) : null;
    if (!tab) throw new Error("Sentinel scope changed");
    revealTaskTab(tab);
  } catch (error) { if (valid() && (error as Error)?.name !== "AbortError") showFlash(t("backgroundAccess.unavailable"), "error"); }
  finally { if (mine === taskNavigationEpoch) { taskOpening.value = false; taskNavigationController = undefined; } }
}
function openBackgroundTask(selection: BackgroundTaskSelection) {
  if (selection.agentId !== studioAgentId || selection.taskId !== `${selection.taskSource}:${selection.taskSourceId}`) return;
  if (selection.taskSource === "sentinel") void openIndependentSentinelTask(selection.taskSourceId, selection.agentId);
  else if (selection.taskSource === "schedule") void openTaskSheet(selection.taskId, selection.label, selection.agentId, selection.taskRunSession);
}
async function openTaskResultEntry(sid: string) {
  if (!isStudio || viewClosed || !/^[A-Za-z0-9_-]{1,160}$/.test(sid)) return;
  restoreOrdinaryTaskContext(sid);
  cancelTaskNavigation();
  const mine = ++taskEntryEpoch, navigation = taskNavigationEpoch, controller = new AbortController();
  taskNavigationController = controller; taskOpening.value = true;
  const valid = () => !viewClosed && !controller.signal.aborted && mine === taskEntryEpoch && navigation === taskNavigationEpoch;
  try {
    const result = await api.session(sid, controller.signal); if (!valid()) return;
    const role = result.background_task;
    if (result.session_id !== sid || role?.source !== "schedule" || role.agent_id !== studioAgentId || role.task_id !== `schedule:${role.source_id}`) throw new Error("Task execution scope changed");
    await openTaskSheet(role.task_id, "", role.agent_id, sid);
  } catch { if (valid()) showFlash(t("backgroundAccess.unavailable"), "error"); }
  finally { if (navigation === taskNavigationEpoch) { taskOpening.value = false; taskNavigationController = undefined; } }
}

let sentinelNavigationEpoch = 0;
let sentinelNavigationController: AbortController | null = null;
cancelPendingSentinelNavigation = () => {
  ++sentinelNavigationEpoch;
  sentinelNavigationController?.abort();
  sentinelNavigationController = null;
};
watch(current, () => cancelPendingSentinelNavigation(), { flush: "sync" });
onBeforeUnmount(() => cancelPendingSentinelNavigation());

async function openSentinelSession(watchId: string, _label = "", agentId = studioAgentId, _parentHint?: string) {
  if (isDelegatedEmbed || !/^[A-Za-z0-9_-]{1,160}$/.test(watchId) || !agentId) return;
  cancelPendingAnswerFile();
  const mine = sentinelNavigationEpoch;
  const from = current.value;
  const controller = new AbortController();
  sentinelNavigationController = controller;
  scheduleOpen.value = false;
  try {
    const detail = await sentinelSessionsApi.detail(watchId, controller.signal);
    if (viewClosed || controller.signal.aborted || mine !== sentinelNavigationEpoch || current.value !== from) return;
    const watch = detail.watch;
    if (watch.id !== watchId || !sentinelTab(watch, agentId)) throw new Error("Invalid Sentinel binding");
    if (isStudio && agentId !== studioAgentId) {
      await navigateStudio(sentinelDeepLink(studioPath(agentId, watch.parent_session_id, { managedChat: isAdmin.value }), watchId));
      return;
    }
    await loadSessions();
    if (viewClosed || controller.signal.aborted || mine !== sentinelNavigationEpoch || current.value !== from) return;
    const parent = watch.parent_session_id;
    const available = !!parent && sessions.value.some(session => session.id === parent);
    const tab = sentinelTab(watch, agentId, available);
    if (!tab) return;
    // The parent switch and sheet reveal are one synchronous navigation.
    // No ordinary chat is opened for the retained worker itself.
    if (available && parent !== current.value) open(parent!);
    browserFocusPolicy.manual();
    hubTabs.value = openTab(hubTabs.value, tab);
    hubActiveTab.value = tab.id;
    hubAt.value = null;
    activeSheet.value = "hub";
  } catch {
    if (!controller.signal.aborted && mine === sentinelNavigationEpoch) showFlash(t("sentinelSessions.unavailable"), "error");
  } finally {
    if (mine === sentinelNavigationEpoch) sentinelNavigationController = null;
  }
}

async function openLegacySentinelEntry(sid: string | null): Promise<boolean> {
  if (!isStudio || !sid) return false;
  const from = current.value, mine = ++taskEntryEpoch;
  try {
    const result = await api.session(sid);
    if (viewClosed || mine !== taskEntryEpoch || current.value !== from) return true;
    if (result.agent_workflow) {
      restoreOrdinaryTaskContext(sid);
      const identity = result.agent_workflow;
      if (result.session_id !== sid || identity.agent_id !== studioAgentId || !validAgentWorkflowId(identity.workflow_id) || !validAgentWorkflowId(identity.run_id)) {
        showFlash(t("agentWorkflow.scopeChanged"), "error");
        return true;
      }
      internalWorkflowSelection.value = { workflowId: identity.workflow_id, runId: identity.run_id, sequence: ++internalWorkflowSequence };
      activeSheet.value = "workflows";
      return true;
    }
    if (result.background_task) {
      restoreOrdinaryTaskContext(sid);
      const identity = result.background_task;
      if (result.session_id !== sid || identity.source !== "schedule" || identity.agent_id !== studioAgentId || !identity.source_id || identity.task_id !== `schedule:${identity.source_id}`) {
        showFlash(t("taskSessionResult.scopeChanged"), "error");
        return true;
      }
      await openTaskResultEntry(sid);
      return true;
    }
    const link = result.sentinel;
    if (viewClosed || !link || link.agent_id !== studioAgentId || !/^[A-Za-z0-9_-]{1,160}$/.test(link.watch_id)) return false;
    const parent = link.parent_session_id && sessions.value.find(session => session.id === link.parent_session_id);
    const fallback = parent || sessions.value[0];
    if (fallback) open(fallback.id);
    await openSentinelSession(link.watch_id, link.title, link.agent_id, link.parent_session_id);
    return true;
  } catch { return false; }
}

function openSentinelFile(file: { path: string; name: string; lineStart?: number; lineEnd?: number; sourceSessionId?: string; parentSessionId?: string }) {
  if (!file.parentSessionId || current.value !== file.parentSessionId || stream.switching.value) return;
  openAttachmentPreview(file);
}

// Reload the conversation's effective catalog after imports or configuration changes.
function onSkillsChanged(event?: Event) {
  const agentId = (event as CustomEvent<{ agentId?: string }> | undefined)?.detail?.agentId;
  if (agentId && agentId !== studioAgentId) return;
  void loadSkills();
  void loadMcpMethods();
}

async function renameSession(s: SessionInfo) {
  let name: string;
  try {
    const { value } = await ElMessageBox.prompt(t("agentView.rename.promptMessage"), t("common.rename"), {
      inputValue: s.title,
      confirmButtonText: t("common.save"),
      cancelButtonText: t("common.cancel"),
      inputValidator: (v: string) => (v.trim().length > 0 ? true : t("agentView.rename.nameRequired")),
    });
    name = value.trim();
  } catch {
    return; // cancelled
  }
  if (name === s.title) return;
  try {
    await api.updateSession(s.id, { title: name });
    await loadSessions();
  } catch (err) {
    showFlash(t("agentView.flash.renameFailed", { msg: (err as Error).message }), "error");
  }
}

async function send() {
  if (sending.value || studioEntryLoading.value || stream.switching.value) return;
  if (isDelegatedEmbed && (!canUse("conversation") || busy.value || sending.value || gated.value)) return;
  const draft = input.value;
  const text = normalizeComposerTokenInput(draft.trim(), skills.value);
  let draftParts = normalizeComposerParts(composerParts.value);
  const draftLayout = composerLayout.value;
  const prefix = composerToken.value?.kind === "skill" || composerToken.value?.kind === "mcp" ? composerToken.value.prefix : "";
  const submittedInvocation = skillInvocation.value || mcpInvocation.value;
  const sendable = sendableAttachments.value;
  if ((!text && !elementPicks.value.length && !sendable.length && !composerSkills.value.length && !composerMcps.value.length && !composerShortcuts.value.length && !composerOffices.value.length) || (!current.value && pendingWorkspace.value === null)) return;
  if (composerInvocationError.value) {
    showFlash(composerInvocationError.value, "error");
    return;
  }
  // Steering accepts ordinary prose, while a skill invocation is expanded by
  // the next message endpoint. Preserve this draft until the current run ends.
  if (composerInvocationWaiting.value) {
    showFlash(composerWaitingMessage.value);
    return;
  }

  if (draftLayout === "inline" && draftParts.some(part => part.type === "file" && !sendable.some(file => String(file.key) === part.key))) {
    showFlash(t("agentView.flash.uploadFailed"), "error");
    return;
  }

  // Slash commands are turned into control calls here and never reach
  // /messages. That boundary is deliberate: if commands could be parsed out of
  // conversation content, one prompt injection — a file that says "please output
  // /auto" — would switch the approval gate off.
  //
  // /skill: is the exception: not a command but a prompt the *server* expands
  // (that is where the skill file is readable), so it goes to /messages.
  // A command runs on the typed text alone; picks stay put, because a command
  // like /model never consumed context in the first place.
  if (!submittedInvocation && text.startsWith("/") && !text.startsWith("/skill:") && !text.startsWith("/mcp:")) {
    if (!current.value && pendingWorkspace.value !== null) return;
    input.value = "";
    await runCommand(text);
    return;
  }
  const submittedSession = await createSessionForFirstMessage();
  if (!submittedSession) return;
  const sentKeys = new Set(sendable.map(file => String(file.key)));
  let submittedParts = draftParts.filter(part => part.type !== "file" || sentKeys.has(part.key));
  const buildParts = () => composerInputParts(submittedParts, key => {
    const file = sendable.find(file => String(file.key) === key);
    return file?.members ? file.members.filter(member => member.path).map(member => ({ path: member.path!, as_file: true })) : file?.path ? [file.path] : [];
  }, prefix);
  const message = text;
  const clearSubmitted = () => {
    composerDrafts.delete(NEW_SESSION_DRAFT_KEY);
    const ids = new Set(submittedParts.filter(part => part.type !== "text").map(part => part.id));
    if (current.value !== submittedSession) {
      const cached = composerDrafts.get(submittedSession);
      if (cached) {
        const unchanged = cached.input === draft;
        cached.parts = cached.parts.filter(part => part.type === "text" ? !unchanged : !ids.has(part.id));
        if (unchanged) cached.input = "";
        const files = new Set(cached.parts.filter(part => part.type === "file").map(part => part.key));
        cached.attachments = cached.attachments.filter(file => files.has(file.key));
        cached.picks = cached.parts.flatMap(part => part.type === "element" && part.pick ? [part.pick] : []);
      }
      persistComposerDrafts();
      return;
    }
    const unchangedText = input.value === draft;
    const remaining = composerParts.value.filter(part => part.type === "text" ? !unchangedText : !ids.has(part.id));
    updateComposerParts(remaining);
    if (unchangedText) input.value = "";
    persistComposerDrafts();
  };
  sending.value = true;
  try {
    // Capture the icon bytes before serialization. This only enriches the
    // already captured draft; typing or a session switch cannot retarget it.
    draftParts = await snapshotComposerElementIcons(draftParts, citationFavicons);
    submittedParts = draftParts.filter(part => part.type !== "file" || sentKeys.has(part.key));
    // One submission path for prose, even when the live busy flag has not yet
    // caught up with the server. Idle submissions start normally; busy ones
    // queue durably. Attachments and explicit skill expansion keep /messages.
    if (!isDelegatedEmbed && !submittedInvocation && sendable.length === 0) {
      const sid = submittedSession;
      const parts = buildParts();
      const signature = JSON.stringify({ parts, input_layout: draftLayout });
      const id = inboxDrafts.submission(sid, signature);
      const receipt = await api.inboxSend(sid, id, message, "queued", undefined, parts, draftLayout);
      inboxDrafts.acknowledge(sid, signature, id);
      clearSubmitted();
      if (current.value === sid) {
        stream.acceptInboxItem(receipt.item);
        stickToBottom.value = true;
        void loadSessions();
      }
      return;
    }
    // The upload is part of sending, not of attaching: only now do the bytes
    // leave for the workspace, and uploads/ gains exactly what was sent.
    // Chips already carrying a path — an earlier attempt failed after their
    // upload — skip straight to the message; folder chips count as pending
    // while any member still lacks its path.
    const sid = submittedSession;
    const pending = sendable.filter((a) =>
      a.members ? a.members.some((m) => !m.path) : !a.path,
    );
    // The client id ties the optimistic bubble to the server's user_message
    // echo either way; with bytes to move it is minted now, so the bubble can
    // open the send instead of waiting on the upload.
    const clientId = `c${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
    const sentAt = Date.now();
    // restoreDraft hands a failed send's composer content back: the chips
    // (keeping whatever paths landed, so a resend skips re-uploading) ahead
    // of anything pasted since, the words ahead of anything typed since.
    const restoreDraft = () => {
      if (current.value !== sid) {
        const newer = composerDrafts.get(sid);
        const parts = [...draftParts, ...(newer?.parts.length ? [{ type: "text" as const, text: "\n" }, ...newer.parts] : [])];
        composerDrafts.set(sid, { input: prefix + composerTypedText(parts), parts, layout: draftLayout,
          attachments: [...sendable, ...(newer?.attachments ?? []).filter(file => !sentKeys.has(String(file.key)))],
          picks: parts.flatMap(part => part.type === "element" && part.pick ? [part.pick] : []), catalog: new Map([...(newer?.catalog ?? new Map()), ...sendable.map(file => [String(file.key), file] as const)]) });
        persistComposerDrafts();
        return;
      }
      syncingComposer = true;
      attachments.value = [...sendable, ...attachments.value.filter(file => !sentKeys.has(String(file.key)))];
      syncingComposer = false;
      const restored = [...draftParts, ...(composerParts.value.length ? [{ type: "text" as const, text: "\n" }, ...composerParts.value] : [])];
      updateComposerParts(restored);
      input.value = prefix + composerTypedText(restored);
      composerLayout.value = draftLayout;
    };
    if (pending.length) {
      // Flatten chips into per-file tasks: a plain chip is one task, a folder
      // chip is its members, each under the subdir the tree gave it. Tasks
      // only ever hold bytes-in-hand members/chips (path-only rewind chips
      // are filtered out on both branches) — send() has nothing to upload
      // for those.
      const tasks: { chip: Attachment; member?: { file?: File; name: string; dir: string; path?: string } }[] =
        pending.flatMap((a) =>
          a.members
            ? a.members.filter((m) => !m.path).map((m) => ({ chip: a, member: m }))
            : a.file
              ? [{ chip: a }]
              : [],
        );
      for (const a of pending) {
        a.status = "uploading";
        if (a.folder) {
          a.folder.done = 0;
          a.folder.failed = 0;
        }
      }
      // Bubble first, bytes behind it: the user's words land on the timeline
      // the instant send is clicked and the composer frees for the next
      // message. The bubble opens text-only — the server names each file's
      // final path (collision suffixes), so the attachment blocks swap in
      // once the upload says what they are — and its card counts the bytes.
      const sizes = tasks.map((tk) => ((tk.member ? tk.member.file : tk.chip.file) as File).size);
      const loaded = new Array<number>(tasks.length).fill(0);
      // The card itself must be reactive, not just the record holding it: the
      // worker below mutates through this local, and writes to a plain object
      // bypass the record's proxy — the bar pinned at 0% until the card died.
      // (Reading reactive(record)[id] hands back this same proxy.)
      const card = reactive<UploadCardState>({
        total: sizes.reduce((s, n) => s + n, 0),
        loaded: 0,
        count: tasks.length,
        done: 0,
        phase: "uploading",
      });
      uploadStates[clientId] = card;
      if (current.value === sid) stream.sendOptimistic({
        id: clientId,
        role: "user",
        content: submittedParts.flatMap((part): Block[] => part.type === "file"
          ? [{ type: "file", file_path: part.path ?? "", file_name: part.label }]
          : part.type === "shortcut" ? inputPartsBlocks([{ type: "shortcut", shortcut_id: part.shortcutId, title: part.title, text: part.text, icon: part.icon, language: part.language, parts: part.parts }])
          : part.type === "office" ? inputPartsBlocks([{ type: "office", office: part.office }])
          : part.type === "skill" ? [{ type: "text", text: `/skill:${part.name} `, input_skill: part.name }]
          : part.type === "mcp" ? inputPartsBlocks([{ type: "mcp", server: part.server, name: part.name }])
          : [{ type: "text", text: part.text, ...(part.type === "element" ? { input_element: true, ...(part.favicon ? { input_element_favicon: part.favicon } : {}) } : {}) }]),
        input_layout: draftLayout,
        ts: sentAt,
      });
      clearSubmitted();
      if (current.value === sid) stickToBottom.value = true;
      // A bounded pool rather than one request per file at once: a
      // 500-member tree would otherwise open 500 sockets simultaneously and
      // starve everything else the composer is doing.
      let ok = true;
      let next = 0;
      const worker = async () => {
        for (;;) {
          const i = next++;
          if (i >= tasks.length) return;
          const tk = tasks[i];
          // Tasks never hold a path-only chip (the task build filters those
          // out), so the bytes are there; the cast only satisfies the
          // optional member type rewind-restored chips introduced.
          const file = (tk.member ? tk.member.file : tk.chip.file) as File;
          const name = tk.member ? tk.member.name : tk.chip.name;
          const dir = tk.member?.dir || "";
          const size = sizes[i];
          try {
            // XHR's progress counts the multipart envelope too; clamping to
            // the file's size keeps the bar honest about what it weighs.
            const meta = await api.uploadAttachment(sid, file, name, dir || undefined, (b) => {
              const v = Math.min(b, size);
              card.loaded += v - loaded[i];
              loaded[i] = v;
            });
            card.loaded += size - loaded[i];
            loaded[i] = size;
            card.done++;
            if (tk.member) tk.member.path = meta.path;
            else {
              tk.chip.path = meta.path;
              tk.chip.name = meta.name || tk.chip.name;
              tk.chip.kind = meta.kind || tk.chip.kind;
            }
          } catch (err) {
            ok = false;
            if (tk.chip.folder) tk.chip.folder.failed++;
            else {
              tk.chip.status = "error";
              tk.chip.error = err instanceof Error ? err.message : String(err);
            }
          }
          if (tk.chip.folder) tk.chip.folder.done++;
        }
      };
      await Promise.all(Array.from({ length: Math.min(4, tasks.length) }, worker));
      // One failure stops the send: the bubble leaves the timeline, the draft
      // returns to the composer with the red chip naming why, the rest keep
      // their uploaded paths, and nothing half-sent reaches the timeline.
      // A folder chip that only partly failed returns to ready with the
      // failed count on it — the succeeded members keep their paths, so
      // removing the bad apples and re-sending sends only those.
      if (!ok) {
        if (current.value === sid) stream.dropOptimistic(clientId);
        delete uploadStates[clientId];
        for (const a of pending) {
          if (a.status !== "uploading") continue;
          if (a.folder?.failed) {
            a.status = a.folder.failed >= a.folder.total ? "error" : "ready";
            a.error = t("agentView.attach.folderPartial", {
              failed: a.folder.failed,
              total: a.folder.total,
            });
          } else {
            a.status = "ready";
          }
        }
        restoreDraft();
        showFlash(t("agentView.flash.uploadFailed"), "error");
        return;
      }
      for (const a of pending) if (a.status === "uploading") a.status = "ready";
      card.phase = "sending";
    }
    // What the message carries: one entry per uploaded file. Plain image
    // chips go in as pixels (today's behavior); folder members are ALWAYS
    // file notes — a folder of screenshots is ls-able workspace material,
    // not 200 inline image parts in one model request.
    const sent: { path: string; name: string; image: boolean }[] = [];
    for (const a of sendable) {
      if (a.members) {
        for (const m of a.members) if (m.path) sent.push({ path: m.path, name: m.name, image: false });
      } else if (a.path) {
        sent.push({ path: a.path, name: a.name, image: a.kind === "image" });
      }
    }
    const paths = sent.map((s) => s.path);
    const parts = buildParts();
    const fileInfo = new Map(sent.map(file => [file.path, file]));
    // Optimistic bubble: the user's words and attachment chips render now,
    // not whenever the server's user_message event happens to arrive. The
    // client id ties the two together — the event echoes it and the stream
    // retires this placeholder (see useAgentStream). Blocks mirror the
    // server's own ordering: images, then file notes, then the text. A send
    // that opened ahead of its upload swaps its text-only placeholder for
    // the full content in place; every other send draws the bubble here.
    const outgoing = {
      id: clientId,
      role: "user" as const,
      content: inputPartsBlocks(parts, fileInfo),
      input_layout: draftLayout,
      ts: sentAt,
    };
    if (current.value === sid) {
      if (pending.length) stream.updateOptimistic(clientId, outgoing);
      else stream.sendOptimistic(outgoing);
    }
    try {
      if (!isDelegatedEmbed && !submittedInvocation) {
        const signature = JSON.stringify({ parts, input_layout: draftLayout });
        const id = inboxDrafts.submission(sid, signature);
        const receipt = await api.inboxSend(sid, id, message, "queued", undefined, parts, draftLayout);
        inboxDrafts.acknowledge(sid, signature, id);
        if (current.value === sid) stream.acceptInboxItem(receipt.item);
        if (current.value === sid) stream.dropOptimistic(clientId);
      } else await api.send(sid, message, paths, clientId, isDelegatedEmbed ? undefined : parts, draftLayout);
    } catch (err) {
      // The server never accepted it: the placeholder must not stay on the
      // timeline claiming otherwise, and an upload that preceded it hands
      // its draft back to the composer.
      if (current.value === sid) stream.dropOptimistic(clientId);
      delete uploadStates[clientId];
      if (pending.length) restoreDraft();
      throw err;
    }
    // Accepted: the bubble already wears its final blocks, so the waiting
    // card retires here rather than waiting on the echo.
    delete uploadStates[clientId];
    if (pending.length) {
      // The composer was freed up front — clearing it again would clobber a
      // next message typed during the upload. Just retire the sent chips'
      // previews.
      for (const a of sendable) if (a.preview) URL.revokeObjectURL(a.preview);
    } else {
      for (const a of attachments.value) {
        if (sendable.includes(a) && a.preview) URL.revokeObjectURL(a.preview);
      }
      clearSubmitted();
    }
    if (current.value === sid) stickToBottom.value = true;
    // The list is ordered by activity and titled from the first prompt.
    void loadSessions();
  } catch (err) {
    if (current.value !== submittedSession) return;
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.alreadyRunning")
        : t("agentView.flash.sendFailed", { msg: e.message }),
      "error",
    );
  } finally {
    sending.value = false;
  }
}

async function changeInbox(item: InboxItem, action: "steer" | "edit" | "withdraw", text?: string, parts?: MessageInputPart[], layout?: InputLayout) {
  const sid = current.value;
  if (!sid || isDelegatedEmbed || inboxChanges.value.includes(item.id)) return;
  inboxChanges.value = [...inboxChanges.value, item.id];
  inboxErrors.value = { ...inboxErrors.value, [item.id]: "" };
  if (action === "withdraw") inboxDrafts.withdrawing(sid, item);
  try {
    const receipt = action === "steer" ? await api.inboxSteer(sid, item)
      : action === "edit" ? await api.inboxEdit(sid, item, text ?? "", parts, layout)
      : await api.inboxWithdraw(sid, item);
    if (current.value !== sid) return;
    stream.acceptInboxItem(receipt.item);
    if (action === "withdraw") {
      restoreWithdrawnDrafts();
      await nextTick(); focusComposer();
    }
  } catch (error) {
    if (current.value !== sid) return;
    inboxErrors.value = { ...inboxErrors.value, [item.id]: t("conversationInbox.failed", { message: (error as Error).message }) };
    // Another tab may have edited or delivered the item already. Reconcile
    // without replacing live messages or accepting an older receipt.
    try {
      const fresh = await api.session(sid);
      if (current.value === sid) for (const entry of fresh.snapshot.inbox ?? []) stream.acceptInboxItem(entry);
    } catch { /* Keep the actionable error and the last known pending item. */ }
    if (current.value === sid) {
      restoreWithdrawnDrafts();
      if (action !== "withdraw" || !stream.inbox.value.some(entry => entry.id === item.id && entry.status === "withdrawn")) {
        showFlash(t("conversationInbox.failed", { message: (error as Error).message }), "error");
      }
    }
  } finally {
    if (current.value === sid) inboxChanges.value = inboxChanges.value.filter(id => id !== item.id);
  }
}

// --- attachments -------------------------------------------------------------

// pickAttach answers the attach menu's question and trips the matching door.
// The menu closes first: the OS dialog takes the stage alone, and a popover
// left open behind it would be stale by the time the dialog is dismissed.
function pickAttach(door: "files" | "folder") {
  if (!canUse("attachments")) return unsupported();
  attachMenu.value = false;
  (door === "files" ? attachInput : folderInput).value?.click();
}

// attachFiles takes files from any of the three doors (button, paste, drop)
// and makes each a LOCAL chip: validated at the door, prepared (images
// downscaled, exotic image types re-encoded to jpeg) — and nothing more. A
// file in the composer is a draft, not an upload; the workspace's uploads/
// gains bytes only when a message is actually sent (see send()).
function attachFiles(files: File[] | FileList) {
  if (!canUse("attachments")) return unsupported();
  for (const f of Array.from(files)) {
    const isImage = f.type.startsWith("image/");
    const key = composerOccurrenceID();
    const preview = isImage ? URL.createObjectURL(f) : undefined;
    const chip: Attachment = reactive({
      key,
      name: f.name || "attachment",
      kind: isImage ? "image" : kindOfName(f.name),
      status: "ready",
      preview,
    });
    attachments.value.push(chip);
    // The door check runs at attach so a mis-aimed drag fails now, not at
    // send time; the server stays the backstop for everything it checks.
    const bad = attachmentDoorError(f);
    if (bad) {
      chip.status = "error";
      chip.error = bad;
      continue;
    }
    if (isImage) {
      // The server's image allowlist; anything else image-flavoured (a pasted
      // gif, say) is re-encoded to jpeg on the canvas before it is kept.
      const acceptableImage = ["image/png", "image/jpeg", "image/webp"].includes(f.type);
      chip.status = "uploading"; // local preparation, reads as busy honestly
      void (async () => {
        const prepared = await prepareImage(f, chip.name, !acceptableImage);
        const a = composerFileCatalog.get(key) ?? chip;
        if (!a) return; // removed while preparing
        a.file = prepared.blob;
        a.name = prepared.name;
        a.status = "ready";
      })();
      continue;
    }
    chip.file = f;
  }
}

// attachAllowed is the effective allowlist from /api/attachments/policy —
// the same list saveAttachment enforces. Seeded with the server default
// (image + office + media groups) so the door works before the fetch lands or
// when it cannot; the server re-reads prefs per upload and stays the backstop.
const attachAllowed = ref(
  new Set([".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".pdf", ".doc", ".docx", ".xlsx", ".csv", ".tsv", ".mp3", ".wav", ".ogg", ".m4a", ".mp4", ".webm", ".mov"]),
);

// The per-file cap, from the same policy as the list above. Seeded with the
// built-in default so the door works before the fetch lands.
const attachMaxMB = ref(25);

// The file picker's accept attribute, derived from the same list: what the
// dialog offers is what the door admits is what the server keeps.
const attachAccept = computed(() => [...attachAllowed.value].sort().join(","));

// attachmentDoorError mirrors the server's allowlist and size cap
// (web/attachments.go saveAttachment) so the chip can refuse instantly; the
// list itself is fetched (attachAllowed), never duplicated here.
function attachmentDoorError(f: File): string {
  if (f.size > attachMaxMB.value << 20) return t("agentView.attach.tooLarge", { mb: attachMaxMB.value });
  const dot = f.name.lastIndexOf(".");
  const ext = dot < 0 ? "" : f.name.slice(dot).toLowerCase();
  return attachAllowed.value.has(ext) ? "" : t("agentView.attach.unsupported", { ext: ext.slice(1) || f.name });
}

// kindOfName mirrors the server's extension classes for the chip's icon while
// the upload is still in flight; the server's answer is authoritative after.
function kindOfName(name: string): string {
  const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
  switch (ext) {
    case "pdf":
      return "pdf";
    case "doc":
    case "docx":
      return "word";
    case "xlsx":
      return "excel";
    case "csv":
    case "tsv":
      return "csv";
  }
  return "text";
}

// onPaste keeps text pastes text: only clipboard items that are files are
// intercepted. A pasted FOLDER joins here too — Chromium exposes it both as
// a zero-byte pseudo-file in .files (what the door used to refuse as
// "unsupported type") and as a real directory entry on items: measured on
// the real thing, webkitGetAsEntry() works on paste in Chromium even though
// the specs only promise it for drops. So the entries are captured
// synchronously (they die at the first await) and the folder walks the same
// walkEntry the drop channel uses. With a folder aboard, .files is ignored
// wholesale — its folder entry is the bogus pseudo-file, not data.
function onPaste(e: ClipboardEvent) {
  if (!canUse("attachments")) {
    if (e.clipboardData?.files.length) { e.preventDefault(); unsupported(); }
    return;
  }
  const dt = e.clipboardData;
  if (!dt) return;
  // Synchronous capture, before any await: the entries handed out here stay
  // usable, the DataTransfer itself does not.
  const entries = dt.items
    ? Array.from(dt.items)
        .filter((it) => it.kind === "file")
        .map((it) => it.webkitGetAsEntry?.() ?? null)
        .filter((en): en is FileSystemEntry => !!en)
    : [];
  const hasDir = entries.some((en) => en.isDirectory);
  if (!hasDir) {
    // Plain files: .files on Chromium; the items walk is the fallback for
    // browsers that only populate it. No type filter here: the door
    // (attachAllowed, the server's own list) decides, and a file it refuses
    // shows as an error chip saying why — silently swallowing a paste reads
    // as "paste does not work".
    const files: File[] = Array.from(dt.files ?? []);
    if (!files.length) {
      for (const it of dt.items ?? []) {
        if (it.kind === "file") {
          const f = it.getAsFile();
          if (f) files.push(f);
        }
      }
    }
    if (files.length) {
      e.preventDefault();
      attachFiles(files);
    }
    return;
  }
  e.preventDefault();
  void (async () => {
    const limit = { count: 0, truncated: false };
    for (const en of entries) {
      if (en.isDirectory) {
        const members: { file: File; name: string; dir: string }[] = [];
        let skipped = 0;
        await walkEntry(en as FileSystemDirectoryEntry, en.name, members, () => skipped++, limit);
        attachFolderChip(en.name, members, skipped);
      } else if (en.isFile) {
        const f = await entryFile(en as FileSystemFileEntry);
        if (f) attachFiles([f]);
      }
    }
    if (limit.truncated) showFlash(t("agentView.attach.tooMany", { max: FOLDER_MAX_FILES }), "error");
  })();
}

// --- folder uploads ------------------------------------------------------------
//
// Two doors bring a tree in, both standard-but-prefixed platform APIs (this
// is the same pair Cloudflare Pages' drag-deploy and every folder-uploading
// product uses; the newer showDirectoryPicker() is Chromium-only and so not
// a door at all):
//   * drop: DataTransferItem.webkitGetAsEntry() + recursive readEntries()
//   * picker: a second hidden input with the webkitdirectory attribute,
//     whose Files carry webkitRelativePath
// Paste can never join them: the OS clipboard carries files, not folders.
//
// The walk is bounded (count and depth), dot-dirs and dependency dirs are
// pruned whole (the Netlify rule — nobody means .git or node_modules by
// "upload this folder"), and the tree becomes ONE chip whose members upload
// under their subdir, preserving structure server-side.

const FOLDER_MAX_FILES = 500;
const FOLDER_MAX_DEPTH = 12;
const FOLDER_SKIP_DIRS = new Set(["node_modules", "__pycache__", "venv", ".venv"]);

// readAllEntries drains a directory reader. readEntries hands out at most
// ~100 entries per call and tells nobody there are more — stopping at the
// first batch is the classic way to lose nine tenths of a folder.
function readAllEntries(dir: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = dir.createReader();
  const out: FileSystemEntry[] = [];
  return new Promise((resolve, reject) => {
    const readBatch = () =>
      reader.readEntries(
        (batch) => {
          if (!batch.length) resolve(out);
          else {
            out.push(...batch);
            readBatch();
          }
        },
        (err) => reject(err),
      );
    readBatch();
  });
}

// entryFile wraps the callback-only entry.file() in a promise; a file the
// browser cannot hand over (locked, vanished) is null, not a rejection —
// the walk counts it as skipped and moves on.
function entryFile(en: FileSystemFileEntry): Promise<File | null> {
  return new Promise((res) => en.file((f) => res(f), () => res(null)));
}

// walkEntry collects one dropped tree into members. relPath is the path from
// the drop root down to (not including) the file — the subdir the upload
// lands under. Dot-named and skipped directories are pruned without counting
// (a node_modules is not a skipped file, it is a non-event); unreadable
// files count as skipped so the chip can say so.
async function walkEntry(
  dir: FileSystemDirectoryEntry,
  relPath: string,
  members: { file: File; name: string; dir: string }[],
  onSkip: () => void,
  limit: { count: number; truncated: boolean },
  depth = 0,
): Promise<void> {
  if (depth > FOLDER_MAX_DEPTH) {
    onSkip();
    return;
  }
  let entries: FileSystemEntry[];
  try {
    entries = await readAllEntries(dir);
  } catch {
    onSkip();
    return;
  }
  for (const en of entries) {
    if (en.isDirectory) {
      if (FOLDER_SKIP_DIRS.has(en.name) || en.name.startsWith(".")) continue;
      await walkEntry(
        en as FileSystemDirectoryEntry,
        relPath ? `${relPath}/${en.name}` : en.name,
        members,
        onSkip,
        limit,
        depth + 1,
      );
    } else if (en.isFile) {
      if (limit.count >= FOLDER_MAX_FILES) {
        limit.truncated = true;
        continue;
      }
      const f = await entryFile(en as FileSystemFileEntry);
      if (!f) {
        onSkip();
        continue;
      }
      limit.count++;
      members.push({ file: f, name: f.name, dir: relPath });
    }
  }
}

// attachFolderChip runs the door check over a walked tree and makes the
// folder ONE chip. Members that fail it (type outside the server allowlist,
// over the per-file cap) are counted as skipped, not silently dropped — a
// drag of a 600-file mixed tree should say what it did, not half-happen.
function attachFolderChip(
  root: string,
  collected: { file: File; name: string; dir: string }[],
  unreadable: number,
) {
  const members: { file: File; name: string; dir: string }[] = [];
  let skipped = unreadable;
  for (const m of collected) {
    if (attachmentDoorError(m.file)) skipped++;
    else members.push(m);
  }
  if (!members.length && !skipped) return;
  attachments.value.push({
    key: composerOccurrenceID(),
    name: root ? `${root}/` : "files/",
    kind: "folder",
    status: members.length ? "ready" : "error",
    error: members.length ? undefined : t("agentView.attach.noneAccepted"),
    members,
    folder: { total: members.length, done: 0, failed: 0, skipped },
  });
  if (skipped) showFlash(t("agentView.attach.folderSkipped", { n: skipped }), "error");
}

// attachRewindDrafts puts a withdrawn message's attachments back as PATH
// chips — the same shapes the drop/paste doors build: members sharing a
// top-level folder regroup into ONE folder chip, files uploaded at the
// uploads/ root stay plain chips. No bytes ride along (the uploads stayed on
// the server), so send() reuses the paths and uploads nothing — which is
// also why the rewind itself no longer waits on anything.
const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "webp", "gif", "bmp"]);
function attachRewindDrafts(items: { path: string; name: string }[]) {
  const byRoot = new Map<string, { path: string; name: string; dir: string }[]>();
  const loose: { path: string; name: string }[] = [];
  for (const it of items) {
    // The dir the upload recorded — uploads/ off, basename off — regroups a
    // folder's members instead of raining them back as loose files.
    const rel = it.path.replace(/^uploads\//, "");
    const slash = rel.lastIndexOf("/");
    const dir = slash < 0 ? "" : rel.slice(0, slash);
    if (!dir) {
      loose.push(it);
      continue;
    }
    const root = dir.split("/")[0];
    const list = byRoot.get(root) ?? [];
    if (!list.length) byRoot.set(root, list);
    list.push({ ...it, dir });
  }
  for (const l of loose) {
    const ext = l.name.slice(l.name.lastIndexOf(".") + 1).toLowerCase();
    const kind = IMAGE_EXTS.has(ext) ? "image" : kindOfName(l.name);
    attachments.value.push({
      key: composerOccurrenceID(),
      name: l.name,
      kind,
      status: "ready",
      path: l.path,
      // Image chips thumbnail by URL; blob: previews are the local-file path.
      preview: kind === "image" ? workspaceFileUrl(l.path) : undefined,
    });
  }
  for (const [root, members] of byRoot) {
    attachments.value.push({
      key: composerOccurrenceID(),
      name: root ? `${root}/` : "files/",
      kind: "folder",
      status: "ready",
      members,
      // done starts full: nothing will upload — a re-send reuses the paths.
      folder: { total: members.length, done: members.length, failed: 0, skipped: 0 },
    });
  }
}

// onDrop: entries must be grabbed synchronously — the DataTransfer dies when
// the handler returns, the entries it handed out do not, so the async walk
// runs on those. Files at the top level of the drop ride as plain chips;
// folders become one chip each. Chromium sometimes mirrors a dropped folder
// into .files as a bogus zero-byte File; deriving everything from entries
// when any exist sidesteps that without special cases.
async function onDrop(e: DragEvent) {
  if (!canUse("attachments")) { e.preventDefault(); return unsupported(); }
  const dt = e.dataTransfer;
  if (!dt) return;
  const plain = Array.from(dt.files ?? []);
  const entries = dt.items
    ? Array.from(dt.items)
        .filter((it) => it.kind === "file")
        .map((it) => it.webkitGetAsEntry?.() ?? null)
        .filter((en): en is FileSystemEntry => !!en)
    : [];
  if (!entries.length) {
    if (plain.length) attachFiles(plain);
    return;
  }
  const limit = { count: 0, truncated: false };
  for (const en of entries) {
    if (en.isFile) {
      const f = await entryFile(en as FileSystemFileEntry);
      if (f) attachFiles([f]);
    } else if (en.isDirectory) {
      const members: { file: File; name: string; dir: string }[] = [];
      let skipped = 0;
      // relPath starts at the root's own name so the tree lands whole:
      // uploads/<root>/<sub>/file, and two folders that both contain a
      // src/ never fold into one.
      await walkEntry(en as FileSystemDirectoryEntry, en.name, members, () => skipped++, limit);
      attachFolderChip(en.name, members, skipped);
    }
  }
  if (limit.truncated) showFlash(t("agentView.attach.tooMany", { max: FOLDER_MAX_FILES }), "error");
}

// onFolderPicked handles the webkitdirectory input. Its Files carry
// webkitRelativePath ("<root>/sub/file") — the root segment included, which
// is what keeps two same-named folders from two picks apart on disk.
function onFolderPicked(e: Event) {
  if (!canUse("attachments")) return unsupported();
  const input = e.target as HTMLInputElement;
  const files = Array.from(input.files ?? []);
  input.value = "";
  if (!files.length) return;
  const groups = new Map<string, { members: { file: File; name: string; dir: string }[]; skipped: number }>();
  let count = 0;
  let truncated = false;
  for (const f of files) {
    const rel = f.webkitRelativePath || f.name;
    const segs = rel.split("/");
    // The whole path below the root is pruned by the same rule the drop
    // walk applies: dot-dirs and dependency dirs are non-events.
    if (segs.length > 1 && segs.slice(0, -1).some((s) => FOLDER_SKIP_DIRS.has(s) || s.startsWith("."))) {
      continue;
    }
    if (count >= FOLDER_MAX_FILES) {
      truncated = true;
      continue;
    }
    count++;
    const root = segs.length > 1 ? segs[0] : "";
    // dir includes the root, matching the drop channel's tree.
    const dir = segs.length > 1 ? segs.slice(0, -1).join("/") : "";
    let g = groups.get(root);
    if (!g) groups.set(root, (g = { members: [], skipped: 0 }));
    if (attachmentDoorError(f)) g.skipped++;
    else g.members.push({ file: f, name: f.name, dir });
  }
  for (const [root, g] of groups) attachFolderChip(root, g.members, g.skipped);
  if (truncated) showFlash(t("agentView.attach.tooMany", { max: FOLDER_MAX_FILES }), "error");
}

// folderChipTitle is the folder chip's hover text: the tree's shape at a
// glance, plus everything that did not ride along.
function folderChipTitle(a: Attachment): string {
  const f = a.folder!;
  const bits = [t("agentView.attach.folderCount", { n: f.total })];
  if (f.skipped) bits.push(t("agentView.attach.folderSkipped", { n: f.skipped }));
  if (a.error) bits.push(a.error);
  return `${a.name} · ${bits.join(" · ")}`;
}

function removeAttachment(key: string) {
  const i = attachments.value.findIndex((a) => a.key === key);
  if (i >= 0) {
    const a = attachments.value[i];
    if (a.status === "uploading" && a.preview) URL.revokeObjectURL(a.preview);
    attachments.value.splice(i, 1);
  }
}

async function runCommand(line: string) {
  if (isDelegatedEmbed) return unsupported();
  const [word] = line.split(/\s+/);
  const name = resolveCommand(word);
  if (!name) {
    showFlash(
      word === "/"
        ? COMMANDS.map((c) => c.name).join("  ")
        : t("agentView.flash.unknownCommand", { cmd: word }),
      "error",
    );
    return;
  }

  try {
    switch (name) {
      case "/compact":
        return void (await compactSession(line.slice(word.length).trim()));
    }
  } catch (err) {
    showFlash(t("agentView.flash.commandFailed", { msg: (err as Error).message ?? err }), "error");
  }
}

// compacting is exposed to the meter so the button can show progress: this is a
// model call on the whole conversation and takes about as long as a turn does.
const compacting = ref(false);
interface CompactionView {
  sessionId: string;
  state: "running" | "done" | "info" | "error";
  startedAt: number;
  finishedAt?: number;
  instructions?: string;
  result?: string;
  message?: string;
  summary?: string;
}
const compaction = ref<CompactionView | null>(null);

function compactionResult(c: NonNullable<Exchange["compaction"]>): string | undefined {
  if (!c.before) return undefined;
  return t("agentView.compact.result", { before: c.before });
}

async function compactSession(instructions = "") {
  if (isDelegatedEmbed) return unsupported();
  if (!current.value || compacting.value) return;
  const sessionId = current.value;
  const focus = instructions.trim();
  const transcriptLength = stream.messages.value.length;
  const card: CompactionView = { sessionId, state: "running", startedAt: Date.now(), instructions: focus || undefined };
  compaction.value = card;
  compacting.value = true;
  stickToBottom.value = true;
  try {
    const res = await api.compact(sessionId, focus);
    const persisted = stream.compactions.value.some(c => !c.id.startsWith("local-") && c.after === transcriptLength && c.summary === res.summary);
    if (!persisted) {
      const finishedAt = Date.now();
      stream.acceptCompaction({
        id: `local-${card.startedAt}`,
        after: transcriptLength,
        ts: finishedAt,
        summary: res.summary,
        instructions: card.instructions,
        before: res.before,
        before_tokens: res.before_tokens,
        after_tokens: res.after_tokens,
        started_at: card.startedAt,
        finished_at: finishedAt,
      });
    }
    compaction.value = null;
  } catch (err) {
    const e = err as Error & { status?: number };
    compaction.value = {
      ...card,
      state: e.status === 422 ? "info" : "error",
      finishedAt: Date.now(),
      message: e.status === 409
        ? t("agentView.compact.busy")
        : e.status === 422
          ? e.message
          : t("agentView.compact.failed", { msg: e.message }),
    };
  } finally {
    compacting.value = false;
  }
}

async function applyPolicy(mode: PolicyMode, turns?: number) {
  if (isDelegatedEmbed) return unsupported();
  if (!current.value) {
    if (pendingWorkspace.value !== null) draftPolicyMode.value = mode;
    return;
  }
  // No success feedback of its own: the picker button already says which mode
  // is on (完全访问 in orange), which is exactly where the user is looking.
  await api.setPolicy(current.value, mode, turns);
}

async function pickPolicy(mode: PolicyMode) {
  if (mode === policy.value.mode) return;
  try {
    await applyPolicy(mode);
  } catch (err) {
    showFlash(t("agentView.flash.switchFailed", { msg: (err as Error).message }), "error");
  }
}

// Switching models keeps the conversation: every provider here speaks the same
// wire format and thinking is not replayed, so there is nothing model-specific in
// the transcript to translate.
async function switchModel(id: string) {
  if (!current.value || viewClosed) return;
  // The server validates the conversation's effective model policy. Other
  // tool permissions remain tied to the conversation's own capability binding.
  if (isStudio && !canSelectStudioModel(studioCapabilities.value, id)) {
    showFlash(
      t("uiCoverage.app.modelNotSelected"),
      "error",
    );
    return;
  }
  if (!canUse("model_selection")) return unsupported();
  try {
    const sid = current.value!;
    await api.setModel(sid, id);
    if (isStudio && current.value === sid && studioCapabilities.value) {
      studioCapabilities.value = { ...studioCapabilities.value, model: id };
    }
    // No success toast: the picker button shows the new model id.
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.modelBusy")
        : t("agentView.flash.switchFailed", { msg: e.message }),
      "error",
    );
  }
}

async function stop() {
  try {
    await api.cancel(current.value!);
  } catch (err) {
    showFlash(t("agentView.flash.cancelFailed", { msg: (err as Error).message }), "error");
  }
}

const userInputPending = ref<Record<string, boolean>>({});
const userInputErrors = ref<Record<string, string>>({});
const userInputRequestIds = computed<ReadonlySet<string>>(() => new Set(stream.switching.value ? [] : stream.live.value.pending_user_inputs.map(item => item.request_id)));
let userInputScope = 0;
watch(current, () => { userInputScope++; userInputPending.value = {}; userInputErrors.value = {}; }, { flush: "sync" });

async function answerUserInput(e: { requestId: string; revision: number; answers?: Record<string, string[]> }) {
  if (isDelegatedEmbed) return unsupported();
  const sid = current.value;
  const scope = userInputScope;
  if (!sid || stream.switching.value || userInputPending.value[e.requestId] || !userInputRequestIds.value.has(e.requestId)) return;
  userInputPending.value = { ...userInputPending.value, [e.requestId]: true };
  userInputErrors.value = { ...userInputErrors.value, [e.requestId]: "" };
  try {
    if (e.answers) await api.answerUserInput(sid, e.requestId, e.revision, e.answers);
    else await api.skipUserInput(sid, e.requestId, e.revision);
  } catch (err) {
    if (current.value === sid && scope === userInputScope) {
      userInputErrors.value = { ...userInputErrors.value, [e.requestId]: t("userInput.failed", { msg: (err as Error).message }) };
    }
  } finally {
    if (current.value === sid && scope === userInputScope) userInputPending.value = { ...userInputPending.value, [e.requestId]: false };
  }
}

async function decide(e: { gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }) {
  if (isDelegatedEmbed) return unsupported();
  const sid = current.value;
  const scope = approvalScope;
  if (!sid || stream.switching.value || approvalPending.value[e.gateId] || !approvalGateIds.value.has(e.gateId)) return;
  approvalPending.value = { ...approvalPending.value, [e.gateId]: true };
  approvalErrors.value = { ...approvalErrors.value, [e.gateId]: "" };
  try {
    await api.decideGate(sid, e.gateId, e.allow, { args: e.args, remember: e.remember });
  } catch (err) {
    if (current.value === sid && scope === approvalScope) {
      approvalErrors.value = { ...approvalErrors.value, [e.gateId]: t("gateCard.decisionFailed", { msg: (err as Error).message }) };
    }
  } finally {
    if (current.value === sid && scope === approvalScope) approvalPending.value = { ...approvalPending.value, [e.gateId]: false };
  }
}

async function updateGateClock(gateId: string, frozen: boolean) {
  if (isDelegatedEmbed) return unsupported();
  const sid = current.value;
  const scope = approvalScope;
  if (!sid || stream.switching.value || approvalPending.value[gateId] || !approvalGateIds.value.has(gateId)) return;
  approvalPending.value = { ...approvalPending.value, [gateId]: true };
  approvalErrors.value = { ...approvalErrors.value, [gateId]: "" };
  try {
    if (frozen) await api.freezeGate(sid, gateId);
    else await api.thawGate(sid, gateId);
  } catch (err) {
    if (current.value === sid && scope === approvalScope) approvalErrors.value = { ...approvalErrors.value, [gateId]: (err as Error).message };
  } finally {
    if (current.value === sid && scope === approvalScope) approvalPending.value = { ...approvalPending.value, [gateId]: false };
  }
}

// A steering message the run accepted but ended before delivering. Rather than
// let it vanish, put the text back in the composer and say why it is there.
watch(
  () => stream.undelivered.value,
  (texts) => {
    if (texts.length === 0) return;
    input.value = [input.value, ...texts].filter(Boolean).join("\n");
    stream.undelivered.value = [];
    showFlash(t("agentView.flash.steerUndelivered"));
    focusComposer();
  },
);

// A dock panel asked the conversation something. Unlike a starter card this
// always fills and never sends, whatever the deployment configured: a panel is
// content the server hands out without the token because it is content, and
// content that could spend a model call on its own would stop being that.
//
// A hub returns to the normal conversation; the workflow opens the same
// conversation as a floating panel while retaining its canvas and selection.
function onPanelIntent(text: string) {
  const plan = planIntent({ text });
  if (plan.kind === "rejected") return;
  if (workflowCanvasActive.value) setWorkflowConversationOpen(true);
  else hubMaximized.value = false;
  restoreFloatingConversation(false);
  suggest(plan.text);
}

// ── Picked page elements ───────────────────────────────────────────────────
// The picker's counterpart in the composer: picked elements wait as chips, are
// never sent by themselves (filling, not sending — the composerIntent rule),
// and join the message as labelled blocks after the user's own words.
const elementPicks = ref<ElementPick[]>([]);

function syncComposerReferences() {
  if (syncingComposer) return;
  for (const file of attachments.value) composerFileCatalog.set(String(file.key), file);
  const files = new Map(attachments.value.map(file => [String(file.key), file]));
  const picks = new Map(elementPicks.value.map(pick => [pick.key, pick]));
  let next = composerParts.value.filter(part => part.type === "text" || part.type === "skill" || part.type === "mcp" || part.type === "shortcut" || part.type === "office" ||
    (part.type === "file" ? files.has(part.key) : !!part.pick && picks.has(part.pick.key)));
  next = next.map(part => {
    if (part.type !== "file") return part;
    const file = files.get(part.key)!;
    return { ...part, label: file.name, path: file.path, status: file.status, error: file.error };
  });
  const missingPicks = elementPicks.value.filter(pick => !next.some(part => part.type === "element" && part.pick?.key === pick.key));
  const missingFiles = attachments.value.filter(file => !next.some(part => part.type === "file" && part.key === String(file.key)));
  composerParts.value = normalizeComposerParts(next);
  for (const pick of missingPicks) composerParts.value = [...composerParts.value, elementComposerPart(pick)];
  for (const file of missingFiles) {
    const part = fileComposerPart({ key: String(file.key), path: file.path, label: file.name, status: file.status, error: file.error });
    if (composerLayout.value === "inline" && inlineComposer.value) inlineComposer.value.insertAttachment(part);
    else {
      const first = composerParts.value.findIndex(part => part.type !== "file");
      const index = first < 0 ? composerParts.value.length : first;
      composerParts.value = [...composerParts.value.slice(0, index), part, ...composerParts.value.slice(index)];
    }
  }
}

function updateComposerParts(parts: ComposerPart[]) {
  const prefix = parts.some(part => part.type === "skill" || part.type === "mcp") ? "" : composerToken.value?.prefix ?? "";
  syncingComposer = true;
  try {
    composerParts.value = normalizeComposerParts(parts);
    input.value = prefix + composerTypedText(composerParts.value);
    if (!composerComposing.value) composerParts.value = replaceComposerTypedText(composerParts.value, composerBody.value);
    const picks = new Map<string, ElementPick>();
    const keys = new Set<string>();
    const restoredKeys = new Map<string, string>();
    for (const part of composerParts.value) {
      if (part.type === "element") {
        const display = orderedDisplayParts([{ type: "text", text: part.text }]).find(part => part.type === "element");
        const pick = part.pick ?? (display?.type === "element" ? pickFromDisplay(display) : null);
        if (pick) { part.pick = pick; picks.set(pick.key, pick); }
      } else if (part.type === "file") {
        if (restoredKeys.has(part.key)) part.key = restoredKeys.get(part.key)!;
        keys.add(part.key);
        const candidate = composerFileCatalog.get(part.key);
        const cached = candidate && (!part.path || part.path === candidate.path) ? candidate : undefined;
        if (candidate && !cached) part.key = composerOccurrenceID();
        if (cached && !attachments.value.some(file => String(file.key) === part.key)) attachments.value.push(cached);
        if (!attachments.value.some(file => String(file.key) === part.key) && part.path) {
          const oldKey = part.key;
          const existing = attachments.value.find(file => file.path === part.path);
          const key = existing?.key ?? composerOccurrenceID();
          if (!existing) attachments.value.push({ key, name: part.label, path: part.path, kind: kindOfName(part.label), status: "ready" });
          part.key = String(key);
          restoredKeys.set(oldKey, part.key);
          keys.delete(oldKey); keys.add(part.key);
        }
      }
    }
    elementPicks.value = [...picks.values()];
    keys.clear();
    for (const part of composerParts.value) if (part.type === "file") keys.add(part.key);
    attachments.value = attachments.value.filter(file => keys.has(String(file.key)));
    for (const file of attachments.value) composerFileCatalog.set(String(file.key), file);
  } finally { syncingComposer = false; }
}

function toggleComposerLayout() {
  if (composerComposing.value) return;
  const selection = composerLayout.value === "top" ? { from: inputBox.value?.selectionStart ?? 0, to: inputBox.value?.selectionEnd ?? 0 } : null;
  composerLayout.value = composerLayout.value === "top" ? "inline" : "top";
  void nextTick(() => {
    if (selection && composerLayout.value === "inline") inlineComposer.value?.setTextSelection(selection.from, selection.to);
    focusComposer();
  });
}

function activateComposerReference(part: ComposerReferencePart) {
  if (part.type === "file") {
    const attachment = attachments.value.find(file => String(file.key) === part.key);
    if (attachment) previewAttachment(attachment);
  } else if (part.type === "shortcut") {
    shortcutPreview.value = { id: part.shortcutId, title: part.title, content: part.text, icon: part.icon, language: part.language, ...(part.parts ? { parts: cloneInputParts(part.parts) } : {}) };
  } else if (part.type === "office") {
    officePreview.value = { ...part.office, ...(part.office.locator ? { locator: { ...part.office.locator } } : {}) };
  } else if (part.type === "skill") {
    editingMcpID.value = null;
    editingSkillID.value = part.id;
    tokenPickerQuery.value = ""; tokenPickerFocused.value = true; tokenPickerComposing.value = false;
    void nextTick(() => tokenPickerInput.value?.focus());
  } else if (part.type === "mcp") {
    editingSkillID.value = null;
    editingMcpID.value = part.id;
    tokenPickerQuery.value = ""; tokenPickerFocused.value = true; tokenPickerComposing.value = false;
    void loadMcpMethods();
    void nextTick(() => tokenPickerInput.value?.focus());
  }
}

watch([input, composerComposing], () => {
  if (!syncingComposer && !composerComposing.value) composerParts.value = replaceComposerTypedText(composerParts.value, composerBody.value);
}, { flush: "sync" });
watch([composerLayout, composerToken, composerComposing], () => {
  const token = composerToken.value;
  if (composerLayout.value !== "inline" || !token || token.kind === "command" || composerComposing.value) return;
  // The selected prefix becomes a movable atom exactly once. Text/file/page
  // positions already in the draft stay anchored when changing presentation.
  updateComposerParts(composerDraftWithToken(composerParts.value, token));
  void nextTick(updateInlineSelection);
}, { flush: "post" });
watch([attachments, elementPicks], syncComposerReferences, { deep: true, flush: "sync" });
watch(current, (sid, previous) => {
  if (previous) saveComposerDraft(previous);
  else if (pendingWorkspace.value !== null) saveComposerDraft(NEW_SESSION_DRAFT_KEY);
  restoreComposerDraft(sid ?? (pendingWorkspace.value !== null ? NEW_SESSION_DRAFT_KEY : null));
}, { flush: "sync" });
watch(pendingWorkspace, (workspace, previous) => {
  if (previous != null) saveComposerDraft(NEW_SESSION_DRAFT_KEY);
  if (workspace === null) localStorage.removeItem(storeKey("pogo:new-session-workspace"));
  else localStorage.setItem(storeKey("pogo:new-session-workspace"), workspace);
  restoreComposerDraft(workspace !== null ? NEW_SESSION_DRAFT_KEY : current.value);
}, { flush: "sync", immediate: true });
watch([input, composerParts, composerLayout, attachments, elementPicks], () => saveComposerDraft(), { deep: true, flush: "post" });
onBeforeUnmount(() => { saveComposerDraft(); persistComposerDrafts(); });

function restoreOrderedDraft(blocks: Block[], layout: InputLayout = "inline") {
  const parts = blocksComposerParts(blocks);
  composerLayout.value = layout;
  // Expanded skill documents become the editable invocation, never prompt text.
  if (parts[0]?.type === "text") parts[0].text = canonicalUserSkillText(parts[0].text);
  input.value = "";
  updateComposerParts(parts);
  composerParts.value = replaceComposerTypedText(composerParts.value, composerBody.value);
  void nextTick(focusComposer);
}

const informationInserting = ref(false);
let informationInsertSequence = 0;
type InformationInsertionOrigin = { session: string; draft: string; selection?: { from: number; to: number } };
const informationDraft = () => JSON.stringify([composerParts.value, composerLayout.value]);
async function insertDockInformation(entry: InformationEntry) {
  if (isDelegatedEmbed || !current.value || stream.switching.value || composerComposing.value || activeSheet.value !== "shortcuts") return;
  if (composerToken.value?.kind === "command") { showFlash(t("shortcuts.exitCommand"), "error"); return; }
  const origin = { session: current.value, draft: informationDraft(), selection: inlineComposer.value?.getSelection() };
  await insertInformationAt(entry, origin, () => activeSheet.value === "shortcuts");
}
async function insertInformationAt(entry: InformationEntry, origin: InformationInsertionOrigin, sourceCurrent: () => boolean) {
  if (informationInserting.value || entry.kind === "folder" || !sourceCurrent()) return;
  const ticket = ++informationInsertSequence;
  informationInserting.value = true;
  try {
    if (!informationInsertionCurrent(origin, current.value, informationDraft(), viewClosed) || stream.switching.value) {
      showFlash(t("nestedShortcuts.changedDraft")); return;
    }
    const resolved = await resolveInformationEntry(origin.session, entry.id, entry.version);
    if (ticket !== informationInsertSequence || !sourceCurrent() || !informationInsertionCurrent(origin, current.value, informationDraft(), viewClosed) || stream.switching.value) {
      if (!viewClosed) showFlash(t("nestedShortcuts.changedDraft")); return;
    }
    const parts = informationComposerParts(resolved.parts, resolved.attachments);
    restoreFloatingConversation(false);
    if (composerLayout.value === "inline" && inlineComposer.value) {
      if (origin.selection) inlineComposer.value.setSelection(origin.selection);
      inlineComposer.value.insertParts(parts);
    } else updateComposerParts([...composerParts.value, ...parts]);
    void nextTick(focusComposer);
  } catch (error) {
    if (!viewClosed && current.value === origin.session) showFlash(t("nestedShortcuts.insertFailed", { message: error instanceof Error ? error.message : String(error) }), "error");
  } finally { if (ticket === informationInsertSequence) informationInserting.value = false; }
}
watch(current, () => { informationSource.value = null; informationInsertSequence++; informationInserting.value = false; });

let shortcutInsertSequence = 0;
async function insertShortcut(value: ShortcutSnapshot) {
  if (isDelegatedEmbed) return;
  if (composerToken.value?.kind === "command") { showFlash(t("shortcuts.exitCommand"), "error"); return; }
  const ticket = ++shortcutInsertSequence;
  const snapshot = { ...value, ...(value.parts ? { parts: cloneInputParts(value.parts) } : {}) };
  if (snapshot.parts?.length) {
    const error = validateInputParts(snapshot.parts, [snapshot.id]);
    if (error) { showFlash(t(`nestedShortcuts.${error}`), "error"); return; }
    if (inputPartsHaveFiles(snapshot.parts)) {
      const sid = current.value;
      if (!sid) { showFlash(t("nestedShortcuts.sessionRequired"), "error"); return; }
      const before = JSON.stringify([composerParts.value, composerLayout.value]);
      try {
        const resolved = snapshot.source === "starter"
          ? await resolveStarterShortcut(sid, snapshot)
          : await shortcutAssetsApi.resolve(sid, { parts: snapshot.parts, input_layout: "inline" });
        if (viewClosed || ticket !== shortcutInsertSequence || current.value !== sid) return;
        // Resolving files must never replace later typing or retarget a caret.
        if (before !== JSON.stringify([composerParts.value, composerLayout.value])) { showFlash(t("nestedShortcuts.changedDraft")); return; }
        snapshot.parts = resolved.parts;
      } catch (error) { if (!viewClosed && current.value === sid) showFlash(t("nestedShortcuts.insertFailed", { message: error instanceof Error ? error.message : String(error) }), "error"); return; }
    }
  }
  const part = shortcutComposerPart(snapshot);
  restoreFloatingConversation(false);
  if (composerLayout.value === "inline" && inlineComposer.value) inlineComposer.value.insertReference(part);
  else updateComposerParts([...composerParts.value, part]);
  nextTick(focusComposer);
}

function onOfficeReference(snapshot: OfficeReferenceSnapshot) {
  if (isDelegatedEmbed || !current.value || stream.switching.value) return;
  if (snapshot.source_session && snapshot.source_session !== current.value) {
    showFlash(t("officeEditor.selection.conversationChanged"), "error");
    return;
  }
  if (composerToken.value?.kind === "command") { showFlash(t("shortcuts.exitCommand"), "error"); return; }
  const part = officeComposerPart(snapshot);
  restoreFloatingConversation(false);
  if (composerLayout.value === "inline" && inlineComposer.value) inlineComposer.value.insertReference(part);
  else updateComposerParts([...composerParts.value, part]);
  nextTick(focusComposer);
}

function onElementPick(p: ElementPick) {
  restoreFloatingConversation(false);
  if (composerLayout.value === "inline") {
    if (inlineComposer.value) inlineComposer.value.insertElement(p);
    else updateComposerParts([...composerParts.value, elementComposerPart(p)]);
    nextTick(focusComposer);
    return;
  }
  const at = elementPicks.value.findIndex((x) => x.key === p.key);
  // Clicking the same element again takes it back out — the toggle is cheaper
  // than a "you already picked this" message and needs no extra UI.
  if (at >= 0) elementPicks.value.splice(at, 1);
  else elementPicks.value.push(p);
  // The pick click happened inside the iframe; focus stays there, and a
  // Backspace typed then never reaches the composer's handler. Hand focus back
  // so the chip convention (below) and typing both work from the first
  // keystroke after a pick.
  nextTick(focusComposer);
}

function suggest(text: string) {
  input.value = text;
  focusComposer();
}

function onScroll() {
  const el = scroller.value;
  if (!el) return;
  // Only a user scroll can resume following after reading or a disclosure.
  // Folding content can itself produce a scroll event near the bottom.
  if ((stickToBottom.value || Date.now() <= scrollIntentUntil) && !activityContainsSelection(el)) {
    stickToBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }
  updateRail();
}
function onConversationScrollIntent(event: Event) {
  if (typeof KeyboardEvent !== "undefined" && event instanceof KeyboardEvent && !["ArrowDown", "ArrowUp", "PageDown", "PageUp", "Home", "End", " "].includes(event.key)) return;
  if (typeof PointerEvent !== "undefined" && event instanceof PointerEvent && event.target !== scroller.value) return;
  scrollIntentUntil = Date.now() + 700;
}

// jumpToLatest is the pill's click: smooth rather than instant, because the
// travel is what tells the reader where "the latest" sits relative to what
// they were reading. Landing inside the 80px threshold re-arms stickToBottom
// through the ordinary onScroll path — no separate flag to keep in sync.
function jumpToLatest() {
  const el = scroller.value;
  if (!el) return;
  scrollIntentUntil = Date.now() + 700;
  el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
}

// ── Turn rail ─────────────────────────────────────────────────────────────
// One marker per user message on a right-edge track (after the YX agent-qa
// reference). Positions are measured from the rendered .ask elements rather
// than estimated from message counts, because a turn's height is whatever the
// tools made it. The marker carries the entry's type — an `.ask` is not
// always something the user typed: a /skill invocation is a document and a
// background task's completion notice is harness-made — and the rail tells
// them apart by glyph (VS Code's scrollbar-marker model: the common case is a
// quiet dot, the rare cases get shapes).
type RailMarker = "question" | "skill" | "notice";
interface RailDot {
  id: string;
  index: number;
  /** topPct is the dot's position on the rail; topPx its scroll offset. */
  topPct: number;
  topPx: number;
  question: string;
  marker: RailMarker;
  /** Send time of the user message, epoch ms; absent on old transcripts. */
  ts?: number;
}

// Which glyph an entry gets, from the same parsers the transcript uses — the
// rail and the card above it must agree on what a message is.
function railMarker(text: string): RailMarker {
  if (isTaskNotification(text)) return "notice";
  if (skillMessageBlock(text)) return "skill";
  return "question";
}

const convInner = ref<HTMLElement | null>(null);
const railDots = ref<RailDot[]>([]);
const railActive = ref(-1);
const railFill = ref(0);
const contentOverflow = ref(false);
const railVisible = computed(() => railDots.value.length >= 1 && contentOverflow.value);

// A burst of subagent completions lands dots within one dot-height of each
// other and they stack into a smear. Consecutive notices that close collapse
// into one entry with a count badge; the tip lists what is inside.
const railClusterGap = ref(3);

const railEntries = computed(() => packRail(railDots.value, railClusterGap.value));

// A long preview may fit neither side of a narrow conversation. Try above and
// below too, then allow an unarrowed panel to shift fully inside the viewport.
const railPopoverOptions = {
  modifiers: [
    { name: "flip", options: { fallbackPlacements: ["top", "bottom", "right"], padding: 12 } },
    { name: "preventOverflow", options: { altAxis: true, tether: false, padding: 12 } },
  ],
};

function entryActive(e: RailEntry<RailDot>): boolean {
  if (e.dot.index === railActive.value) return true;
  return !!e.cluster?.some((d: RailDot) => d.index === railActive.value);
}

function railKindKey(m: RailMarker): string {
  if (m === "notice") return "agentView.rail.subagentMsg";
  if (m === "skill") return "agentView.rail.skillMsg";
  return "agentView.rail.userMsg";
}

let railRaf = 0;
// rAF batching: streaming and card expansion change heights far more often
// than the eye needs.
function scheduleRailCompute() {
  if (railRaf) return;
  railRaf = requestAnimationFrame(() => {
    railRaf = 0;
    computeRail();
  });
}

function railQuestion(text: string): string {
  // A task notice's raw text is the whole <task-notification> envelope; the
  // useful preview is its brief line, the same one the card shows.
  const notice = parseTaskNotification(text);
  if (notice) return notice.brief;
  // A /skill invocation's raw text is the whole instruction sheet; the useful
  // preview is the invocation line.
  const skill = skillMessageBlock(text);
  const plain = skill ? `/skill:${skill.name} ${skill.trailing}` : text;
  return plain.replace(/\s+/g, " ").trim();
}

function computeRail() {
  const body = scroller.value;
  if (!body) return;
  const bodyRect = body.getBoundingClientRect();
  const total = body.scrollHeight || 1;
  contentOverflow.value = body.scrollHeight > body.clientHeight * 1.2;
  // The cluster gap is one dot (plus a little) measured against the rail's
  // own height — "overlapping" has to survive window resizes.
  railClusterGap.value = (26 / (body.clientHeight || 1)) * 100;
  const users = new Map(timeline.value.filter((i) => i.kind === "user").map(item => [item.id, item]));
  const list: RailDot[] = [];
  body.querySelectorAll<HTMLElement>(".ask").forEach((el, index) => {
    const top = el.getBoundingClientRect().top - bodyRect.top + body.scrollTop;
    const user = users.get(el.dataset.mid ?? "");
    const text = user?.text ?? "";
    list.push({
      id: user?.id ?? `u${index}`,
      index,
      topPx: top,
      topPct: Math.min(100, Math.max(0, (top / total) * 100)),
      question: railQuestion(user?.orderedContent ? composerDisplayText(blocksComposerParts(user.orderedContent)) : text),
      marker: railMarker(text),
      ts: user?.ts,
    });
  });
  railDots.value = list;
  updateRail();
}

// The fill tracks the viewport's bottom edge; the active dot is the last one
// at or above a line 80px below the viewport top.
function updateRail() {
  const body = scroller.value;
  if (!body) return;
  const st = body.scrollTop;
  railFill.value = Math.min(100, ((st + body.clientHeight) / (body.scrollHeight || 1)) * 100);
  let ai = -1;
  for (const d of railDots.value) {
    if (d.topPx <= st + 80) ai = d.index;
    else break;
  }
  railActive.value = ai;
}

function jumpToTurn(d: RailDot) {
  scroller.value?.scrollTo({ top: Math.max(0, d.topPx - 12), behavior: "smooth" });
}

// New items move every dot below them; everything else (streaming text, card
// folds, window resizes) shows up as a change in the inner content's height.
watch(
  () => timeline.value.length,
  () => nextTick(scheduleRailCompute),
);
useResizeObserver(convInner, scheduleRailCompute);
onBeforeUnmount(() => {
  if (railRaf) cancelAnimationFrame(railRaf);
});

function activityReadingProtected(): boolean {
  return activityContainsSelection(scroller.value) || !!scroller.value?.querySelector("[data-reading-output='true']");
}
watch([timeline, compaction, () => stream.liveVersion.value], async () => {
  if (activityReadingProtected()) { stickToBottom.value = false; return; }
  if (!stickToBottom.value || (floatingConversationActive.value && chatMinimized.value)) return;
  await nextTick();
  // The follow-scroll must not win against whoever claimed the position while
  // this waited out the render: a search-hit locate jumps in the same tick the
  // new session's snapshot triggers this watcher, and the check above ran
  // before doLocate cleared the flag. Re-check after the await or the jump is
  // scrolled back to the bottom before the reader ever sees it.
  if (!stickToBottom.value || (floatingConversationActive.value && chatMinimized.value)) return;
  const el = scroller.value;
  if (activityReadingProtected()) { stickToBottom.value = false; return; }
  if (el) el.scrollTop = el.scrollHeight;
});
watch([floatingConversationActive, chatMinimized], async () => {
  await nextTick();
  if (floatingConversationActive.value && chatMinimized.value) return;
  if (stickToBottom.value && scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight;
  scheduleRailCompute();
});

function handleSuggestionKey(e: KeyboardEvent): boolean {
  const suggestionAction = composerSuggestionAction(e, suggestionMenuVisible.value);
  if (suggestionAction) {
    if (suggestionAction === "complete" && !suggestionItems.value.length && e.key === "Tab") return false;
    e.preventDefault();
    e.stopPropagation();
    if (suggestionAction === "next" || suggestionAction === "previous") selectedSuggestionId.value = moveComposerSuggestion(suggestionItems.value, selectedSuggestionId.value, suggestionAction === "next" ? 1 : -1);
    else if (suggestionAction === "complete") completeSuggestion();
    else if (suggestionAction === "dismiss") dismissSuggestions();
    return true;
  }
  return false;
}
function onTokenPickerKeydown(e: KeyboardEvent) {
  if (e.isComposing || tokenPickerComposing.value || e.keyCode === 229) return;
  handleSuggestionKey(e);
}
function onInlineKeydown(e: KeyboardEvent) {
  if (e.isComposing || composerComposing.value || e.keyCode === 229) return;
  handleSuggestionKey(e);
}
function onKeydown(e: KeyboardEvent) {
  if (e.isComposing || composerComposing.value || e.keyCode === 229) return;
  if (handleSuggestionKey(e)) return;
  if (e.key === "Backspace" && !e.repeat && !e.ctrlKey && !e.metaKey && !e.altKey && composerToken.value && inputBox.value?.selectionStart === 0 && inputBox.value?.selectionEnd === 0) {
    e.preventDefault();
    composerTokenControl.value?.focus();
    return;
  }
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    void send();
  }
  // The chip convention: Backspace on an empty composer deletes the last chip
  // instead of doing nothing. One press, one chip — the Cursor backspace
  // incidents came from a shortcut that cleared them all at once, and a held
  // key (keydown repeats) would machine-gun the same way, so repeat is exempt.
  // Element picks go first (they predate attachments and stay the inner row),
  // then attachments.
  if (e.key === "Backspace" && !e.repeat && !input.value) {
    if (elementPicks.value.length) {
      e.preventDefault();
      elementPicks.value.pop();
      nextTick(() => focusComposer());
    } else if (attachments.value.length) {
      e.preventDefault();
      const last = attachments.value[attachments.value.length - 1];
      removeAttachment(last.key);
      nextTick(() => focusComposer());
    }
  }
}

// A focused chip deletes itself — the keyboard path the × button already
// covers, on the tag-input convention that Backspace/Delete remove the
// focused chip. Focus then returns to the composer, where the next Backspace
// would take the (new) last chip: deliberate presses, one chip each.
function onChipKey(e: KeyboardEvent, i: number) {
  if (e.key !== "Backspace" && e.key !== "Delete") return;
  e.preventDefault();
  elementPicks.value.splice(i, 1);
  nextTick(() => focusComposer());
}

// fmtTime renders a message's send time via the locale's short date-time
// format (full date plus HH:mm). The date is always shown — a bare HH:mm says
// nothing once the transcript outlives the day it was written on.
function fmtTime(ts: number) {
  return d(new Date(ts), "short");
}

// No floating alerts in this UI: transient feedback (command output, action
// failures) shows as one inline strip next to the retry/error notices, and
// state feedback shows in the control that changed (picker labels, the copy
// icon below). The strip self-clears; sticky is reserved for the fatal ones.
const flash = ref<{ kind: "info" | "error"; text: string } | null>(null);
let flashTimer: ReturnType<typeof setTimeout> | undefined;

function showFlash(text: string, kind: "info" | "error" = "info", sticky = false) {
  flash.value = { kind, text };
  clearTimeout(flashTimer);
  if (!sticky) {
    flashTimer = setTimeout(() => (flash.value = null), kind === "error" ? 6000 : 4000);
  }
}

// Copy feedback is the icon itself turning into a check for a moment.
const copiedAsk = ref<string | null>(null);

// Describe entries for one exchange's work area: the stream's live list while
// it has any, else the user message's settled image alts. The live list wins
// mid-run because it alone carries the running/failed states; the alts are the
// settled record — and the only one a refresh has, since the snapshot rebuilds
// them from the transcript/fold while describes reset with the stream. The
// step belongs to the work log region — it was the run's job, not part of the
// question.
function exchangeDescribes(ex: Exchange): ImageDescribe[] {
  const live = ex.id === liveExchangeId.value ? liveDescribes.value : [];
  if (live.length) return live;
  const alts = ex.user?.imageAlts;
  if (!alts) return [];
  return Object.entries(alts).map(([ref, text]) => ({
    ref,
    name: ref.split("/").pop() || ref,
    status: "done" as const,
    startedAt: 0,
    text,
  }));
}
let copiedTimer: ReturnType<typeof setTimeout> | undefined;

async function copyAsk(id: string, text: string, blocks?: Block[]) {
  try {
    const editable = blocks?.map(block => block.type === "text" && !block.input_element ? { ...block, text: canonicalUserSkillText(block.text) } : block);
    await navigator.clipboard.writeText(editable ? composerDisplayText(blocksComposerParts(editable)) : canonicalUserSkillText(text));
    copiedAsk.value = id;
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => (copiedAsk.value = null), 1500);
  } catch {
    showFlash(t("agentView.flash.copyFailed"), "error");
  }
}

// Edit refills the composer and focuses it; what happens next is the user's
// call, same rule as the read-continuation buttons.
function editAsk(text: string) {
  input.value = canonicalUserSkillText(text);
  tokenPickerQuery.value = null;
  nextTick(() => focusComposer());
}

// Rewind is edit-with-history: the transcript forks away from the message —
// the message itself and everything after it leaves the branch — and the
// withdrawn text lands back in the composer for a resend. The dialog offers
// restoring the files too when the message's checkpoint has a non-empty diff,
// the same rule as Claude Code's rewind menu: no tracked changes, no option.
const rewindTarget = ref<{
  id: string;
  text: string;
  images?: string[];
  files?: { path: string; name: string }[];
  orderedContent?: Block[];
  inputLayout?: InputLayout;
} | null>(null);
const rewindPreview = ref<{
  available: boolean;
  changes: { path: string; status: string; added: number; removed: number }[];
} | null>(null);
const rewindMode = ref<"both" | "files" | "chat">("both");
// Paths the user unticked, so they stay out of the restore. Tracking exclusions
// rather than inclusions means a preview that lands late (or refreshes) has every
// new file already selected, which is the safer default for a restore.
const rewindExcluded = ref(new Set<string>());
const rewindBusy = ref(false);

const rewindOpen = computed({
  get: () => rewindTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !rewindBusy.value) rewindTarget.value = null;
  },
});
const rewindQuestion = computed(() => {
  const target = rewindTarget.value;
  return target?.orderedContent ? composerDisplayText(blocksComposerParts(target.orderedContent)) : target?.text ?? "";
});

// The file option's precondition: a checkpoint exists AND restoring it would
// change something. An empty diff offers conversation-only, like Claude Code.
const rewindChanges = computed(() => (rewindPreview.value?.available ? rewindPreview.value.changes : []));

// The paths a restore will actually touch. Empty array means "all of them", which
// is what the API's absent `paths` means too, so the two agree by construction.
const rewindSelected = computed(() =>
  rewindChanges.value.map((c) => c.path).filter((p) => !rewindExcluded.value.has(p)),
);

function rewindToggle(path: string) {
  const next = new Set(rewindExcluded.value);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  rewindExcluded.value = next;
}

function rewindAsk(item: { id: string; text: string; images?: string[]; files?: { path: string; name: string }[]; orderedContent?: Block[]; inputLayout?: InputLayout }) {
  if (isDelegatedEmbed) return unsupported();
  if (!current.value || busy.value) return;
  rewindMode.value = "both";
  rewindPreview.value = null;
  rewindExcluded.value = new Set();
  rewindTarget.value = item;
  api
    .rewindPreview(current.value, item.id)
    .then((res) => {
      // The dialog may already target a different message by the time this
      // lands — a late preview must not describe the wrong rewind.
      if (rewindTarget.value?.id === item.id) rewindPreview.value = res;
    })
    .catch(() => {
      // The preview is advisory; conversation-only rewind is always offerable.
    });
}

// rewindDraftItems flattens the withdrawn message's attachments into the
// path list attachRewindDrafts restores from. No bytes move anywhere: the
// rewind leaves the uploads on disk (they are message-bound, not workspace
// state; the session's own delete cleans them), so the chips keep their
// workspace paths and a re-send uploads nothing.
function rewindDraftItems(target: {
  images?: string[];
  files?: { path: string; name: string }[];
}): { path: string; name: string }[] {
  return [
    ...(target.images ?? []).map((ref) => ({ path: ref, name: ref.split("/").pop() || ref })),
    ...(target.files ?? []),
  ];
}

async function confirmRewind() {
  const target = rewindTarget.value;
  if (!target || !current.value) return;
  // With nothing to restore, the two file modes have no work to do; the dialog
  // does not offer them in that case, but a stale preview must not send one.
  const wantsFiles = rewindMode.value !== "chat" && rewindChanges.value.length > 0;
  const mode = wantsFiles ? rewindMode.value : "chat";
  rewindBusy.value = true;
  try {
    const draft = mode === "files" ? [] : rewindDraftItems(target);
    await api.rewind(current.value, target.id, mode, wantsFiles ? rewindSelected.value : undefined);
    rewindTarget.value = null;
    if (mode === "files") {
      // The transcript did not move, so there is no branch to reload and no
      // withdrawn text to put back in the composer — reconnecting the stream
      // here would be asking for a snapshot identical to the one on screen.
      // Only the files changed, so only the file surfaces are invalidated.
      invalidateTree();
      invalidateIndex();
      showFlash(t("agentView.flash.rewindFilesDone", { n: rewindSelected.value.length }), "info");
      return;
    }
    // open() early-returns on an unchanged sid, so the reload goes straight
    // through the stream: the snapshot that lands is the rewound branch.
    stream.connect(current.value);
    // The sidebar's count and title follow the live branch; the file tree
    // follows the restored workspace.
    await loadSessions();
    if (wantsFiles) invalidateTree();
    if (target.orderedContent) restoreOrderedDraft(target.orderedContent, target.inputLayout);
    else {
      if (draft.length) attachRewindDrafts(draft);
      editAsk(target.text);
    }
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.rewindBusy")
        : t("agentView.flash.rewindFailed", { msg: e.message }),
      "error",
    );
  } finally {
    rewindBusy.value = false;
  }
}

// What a status letter means in the restore preview: it describes what the
// restore WILL do, not what the abandoned run did.
function rewindStatusLabel(status: string) {
  switch (status) {
    case "M":
      return t("agentView.rewind.statusRestore");
    case "D":
      return t("agentView.rewind.statusRecover");
    default:
      return t("agentView.rewind.statusDelete");
  }
}
</script>

<template>
  <!-- Studio's hard gate is reserved for an Agent that failed to load.
       Ordinary entry keeps the workbench frame visible and fills its data
       regions with skeletons until the selected snapshot is known. -->
  <div v-if="isStudio && studioError" class="studio-gate">
    <div class="studio-gate-card">
      <span class="studio-gate-eyebrow">{{ managedChat ? "AGENT STUDIO" : "POGO" }}</span>
      <h1>{{ t("uiCoverage.app.agentUnavailable") }}</h1>
      <p>{{ studioError }}</p>
      <div class="studio-gate-actions">
        <button v-if="managedChat" class="studio-gate-btn" @click="backToStudio">{{ t("uiCoverage.app.backToStudio") }}</button>
        <button class="studio-gate-btn primary" @click="retryStudio">{{ t("common.retry") }}</button>
      </div>
    </div>
  </div>
  <div v-else class="shell" :class="{ 'browser-immersive': browserImmersive, 'hub-max': hubMaximized && activeSheet === 'hub' && !browserImmersive, 'workflow-workspace': workflowCanvasActive, 'workflow-conversation-open': workflowCanvasActive && workflowConversationOpen, 'agent-embedded': embedded, 'agent-surface': isStudio }" :aria-busy="studioEntryLoading || undefined">
    <!-- Announce milestones or entry loading, never the streaming transcript. -->
    <div class="sr-only" role="status" aria-live="polite">{{ studioEntryLoading ? t("uiCoverage.app.openingAgent") : announcement }}</div>
    <ShareDialog v-if="canUse('sharing')" v-model="shareOpen" :path="shareLink?.url" />
    <aside ref="sidebarEl" class="sidebar" :class="{ collapsed }">
      <!-- Both states stay mounted and crossfade (opacity, same 0.3s as the
           slide) instead of a v-if swap on the first frame — the icon that
           was just clicked used to vanish instantly, which read as a cut.
           The hidden layer is inert via pointer-events: none. -->
      <div ref="sidebarFullEl" class="sidebar-full">
        <!-- A Studio agent page is furniture for someone else's product: no
             brand of ours, and the new-session button grows into the space
             the mark leaves behind. -->
        <div class="brand" :class="{ unbranded: isStudio }">
          <Logo v-if="!isStudio" />
          <strong v-if="!isStudio">pogo</strong>
          <button class="new" :class="{ wide: isStudio }" :disabled="studioEntryLoading" @click="startNewSession">
            <el-icon><EditPen /></el-icon>
            {{ t("agentView.sidebar.newSession") }}
          </button>
          <!-- Gemini pattern: the collapse control only shows itself while the
               cursor is over the sidebar, so it costs no attention otherwise. -->
          <button class="collapse" :title="t('agentView.sidebar.collapse')" @click="collapseSidebar">
            <el-icon><Fold /></el-icon>
          </button>
        </div>
        <!-- The search box: full-text across every saved transcript (the
             session index answers server-side), so "which session discussed
             X" is a query, not a scroll. While a query is active the results
             replace the project groups — search crosses projects, so the
             grouping would only chop up the answer. -->
        <div class="sess-search">
          <el-icon class="s-icon"><Search /></el-icon>
          <input
            v-model="sessionQuery"
            class="s-input"
            :placeholder="isDelegatedEmbed || isStudio ? t('uiCoverage.app.searchConversations') : t('agentView.sidebar.searchPh')"
            :disabled="studioEntryLoading"
            spellcheck="false"
            type="search"
          />
          <button v-if="sessionQuery" class="s-clear" :title="t('common.close')" @click="sessionQuery = ''">
            <el-icon><Close /></el-icon>
          </button>
        </div>
        <!-- Projects, ChatGPT-style: each workspace is a disclosure group —
             basename label, count, a "+" that starts a session inside it —
             and the sessions live under their project. The scroll stops at
             the recents strip: two independent regions, one column. -->
        <div class="sessions-scroll">
          <StudioEntrySkeleton v-if="studioEntryLoading" area="sessions" />
          <template v-else-if="sessionHits !== null">
            <div v-if="!sessionHits.length" class="s-empty">
              {{ t("agentView.sidebar.searchNoResults", { q: sessionQuery.trim() }) }}
            </div>
            <ul v-else class="sessions">
              <li
                v-for="h in sessionHits"
                :key="h.id"
                :class="{ active: h.id === current, working: isBusy(h.id) }"
                @click="open(h.id)"
              >
                <div class="title">
                  <SessionPulse v-if="isBusy(h.id)" />
                  <span class="title-text"><template v-for="(p, i) in hiParts(h.title || t('agentView.sidebar.emptySession'))" :key="i"><mark v-if="p.m">{{ p.t }}</mark><template v-else>{{ p.t }}</template></template></span>
                </div>
                <div class="sub">
                  <span v-if="hitProject(h)" class="s-proj">{{ hitProject(h) }}</span>
                  <span v-if="h.updated" :title="new Date(h.updated).toLocaleString(locale)">{{ fmtRel(h.updated, Date.now(), locale) }}</span>
                </div>
                <!-- What matched, not just which session: each block quotes
                     one cluster of hits — near matches share a block, distant
                     ones quote separately — and a click lands on the message
                     that said it. -->
                <div v-if="h.blocks?.length" class="s-blocks">
                  <button
                    v-for="(b, bi) in h.blocks"
                    :key="bi"
                    class="s-block"
                    :title="t('agentView.sidebar.locateTip')"
                    @click.stop="locateBlock(h, b)"
                  >
                    <template v-for="(p, i) in hiParts(b.text)" :key="i"><mark v-if="p.m">{{ p.t }}</mark><template v-else>{{ p.t }}</template></template>
                  </button>
                </div>
              </li>
            </ul>
          </template>
          <template v-else>
            <section v-for="g in projectGroups" :key="g.cwd" class="pgroup">
            <div class="pgroup-head">
              <button class="pgroup-toggle" :aria-expanded="groupOpen(g)" :title="g.cwd" @click="toggleGroup(g)">
                <el-icon class="pchev" :class="{ open: groupOpen(g) }"><CaretRight /></el-icon>
                <el-icon class="picon"><FolderOpened /></el-icon>
                <span class="pgroup-name">{{ g.name }}</span>
                <span class="pgroup-count">{{ g.sessions.length > 99 ? "99+" : g.sessions.length }}</span>
              </button>
              <button
                class="pgroup-new"
                :title="t('agentView.sidebar.newInProject', { name: g.name })"
                @click.stop="newInProject(g)"
              >
                <el-icon><EditPen /></el-icon>
              </button>
              <button
                class="pgroup-new pgroup-import"
                :disabled="isDelegatedEmbed || isStudio"
                :title="isDelegatedEmbed || isStudio ? unavailableTip : t('agentView.sidebar.importInProject', { name: g.name })"
                @click.stop="importInProject(g)"
              >
                <el-icon><Upload /></el-icon>
              </button>
            </div>
            <ul v-if="groupOpen(g)" class="sessions">
              <li
                v-for="s in g.sessions"
                :key="s.id"
                :class="{ active: s.id === current, working: isBusy(s.id) }"
                :title="isBusy(s.id) ? t('agentView.sidebar.workingTip') : undefined"
                @click="open(s.id)"
              >
                <div class="title">
                  <SessionPulse v-if="isBusy(s.id)" />
                  <span class="title-text">{{ s.title || t("agentView.sidebar.emptySession") }}</span>
                  <span v-if="isBusy(s.id)" class="sr-only">{{ t("agentView.sidebar.working") }}</span>
                </div>
                <div class="sub">
                  <span v-if="s.pinned" class="pinned-tag">{{ t("agentView.sidebar.pinned") }}</span>
                  <span v-if="!isDelegatedEmbed || s.messages > 0">{{ t("agentView.sidebar.messageCount", { n: s.messages }) }}</span>
                </div>
                <!-- Gemini pattern: the ⋮ shows itself on hover and holds the
                     item's actions. Delete moved in here; the bare × is gone. -->
                <el-dropdown
                  class="sess-menu"
                  trigger="click"
                  placement="right-start"
                  @command="(cmd: string) => onSessionMenu(cmd, s)"
                  @visible-change="
                    (v: boolean) => (menuOpenFor = v ? s.id : menuOpenFor === s.id ? '' : menuOpenFor)
                  "
                >
                  <button
                    class="menu-btn"
                    :class="{ open: menuOpenFor === s.id }"
                    :title="t('agentView.sidebar.more')"
                    @click.stop
                  >
                    <el-icon><MoreFilled /></el-icon>
                  </button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item command="pin" :disabled="isDelegatedEmbed" :title="isDelegatedEmbed ? unavailableTip : undefined">
                        <el-icon><Top /></el-icon>{{ s.pinned ? t("agentView.sidebar.unpin") : t("agentView.sidebar.pin") }}
                      </el-dropdown-item>
                      <el-dropdown-item command="rename">
                        <el-icon><EditPen /></el-icon>{{ t("common.rename") }}
                      </el-dropdown-item>
                      <el-dropdown-item command="export" :disabled="isDelegatedEmbed" :title="isDelegatedEmbed ? unavailableTip : undefined">
                        <el-icon><Download /></el-icon>{{ t("agentView.sidebar.exportSession") }}
                      </el-dropdown-item>
                      <el-dropdown-item command="delete" divided>
                        <el-icon><Delete /></el-icon>{{ t("common.delete") }}
                      </el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
              </li>
            </ul>
            </section>
          </template>
        </div>
        <!-- The recents strip: activity-ordered across projects, pinned to the
             bottom of the sidebar. No count cap — the list box is height-capped
             and scrolls inside. A row is the same session as its project row —
             a shortcut, not a second copy of anything. -->
        <div v-if="recentSessions.length" class="recent">
          <div class="recent-head">
            <span class="recent-label">
              {{ t("agentView.sidebar.recent") }}
              <!-- Folded, the rows are gone and the count is the only hint of
                   what the fold hides — the same pill the project headers use. -->
              <span
                v-if="recentCollapsed"
                class="recent-count"
                :title="t('agentView.sidebar.recentCount', { n: recentSessions.length })"
              >{{ recentSessions.length > 99 ? "99+" : recentSessions.length }}</span>
            </span>
            <button
              class="recent-fold"
              :class="{ folded: recentCollapsed }"
              :title="t(recentCollapsed ? 'agentView.sidebar.recentExpand' : 'agentView.sidebar.recentCollapse')"
              :aria-expanded="!recentCollapsed"
              @click="recentCollapsed = !recentCollapsed"
            >
              <el-icon class="rchev" :class="{ folded: recentCollapsed }"><ArrowDown /></el-icon>
            </button>
          </div>
          <div v-if="!recentCollapsed" class="recent-list">
          <button
            v-for="s in recentSessions"
            :key="'r:' + s.id"
            class="recent-row"
            :class="{ active: s.id === current, working: isBusy(s.id) }"
            :title="recentTip(s)"
            @click="open(s.id)"
          >
            <span class="recent-title">
              <SessionPulse v-if="isBusy(s.id)" />
              <span class="title-text">{{ s.title || t("agentView.sidebar.emptySession") }}</span>
              <span v-if="isBusy(s.id)" class="sr-only">{{ t("agentView.sidebar.working") }}</span>
            </span>
            <span
              v-if="s.updated"
              class="recent-time"
              :title="new Date(s.updated).toLocaleString(locale)"
            >{{ fmtRel(s.updated, Date.now(), locale) }}</span>
            <span class="recent-proj">
              <el-icon><Folder /></el-icon>
              <span class="recent-proj-name">{{ recentProject(s) }}</span>
            </span>
          </button>
          </div>
        </div>
        <!-- Reader preferences stay local; administration lives in Studio. -->
        <div class="side-foot">
          <ThemePicker />
          <el-dropdown trigger="click" placement="top-start" @command="(l: Locale) => setLocale(l)">
            <button class="lang-btn" :title="t('agentView.lang.label')">
              <el-icon
                ><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><path d="M2 12h20" /><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" /></svg
              ></el-icon>
              <span>{{ LOCALE_LABELS[currentLocale] }}</span>
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item v-for="l in SUPPORTED_LOCALES" :key="l" :command="l">
                  {{ l === currentLocale ? "✓ " + LOCALE_LABELS[l] : LOCALE_LABELS[l] }}
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <button v-if="!isStudio && !isDelegatedEmbed" class="lang-btn" title="Agent Studio" @click="backToStudio">
            <el-icon><Grid /></el-icon><span>Studio</span>
          </button>
        </div>
      </div>
      <!-- Collapsed: Gemini's icon rail — just the two things you still want
           one click away: reopening the list and starting a new session. -->
      <div class="rail-icons">
        <Logo v-if="!isStudio" :size="24" class="rail-logo" />
        <button class="rail-icon" :title="t('agentView.sidebar.expand')" @click="expandSidebar">
          <el-icon><Expand /></el-icon>
        </button>
        <button class="rail-icon" :title="t('agentView.sidebar.newSession')" :disabled="studioEntryLoading" @click="startNewSession">
          <el-icon><EditPen /></el-icon>
        </button>
        <ThemePicker class="rail-theme" icon placement="right-start" />
        <button v-if="!isStudio && !isDelegatedEmbed" class="rail-icon" title="Agent Studio" @click="backToStudio">
          <el-icon><Grid /></el-icon>
        </button>
        <el-dropdown trigger="click" placement="right-start" @command="(l: Locale) => setLocale(l)">
          <button class="rail-icon" :title="t('agentView.lang.label')">
            <el-icon
              ><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><path d="M2 12h20" /><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" /></svg
            ></el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="l in SUPPORTED_LOCALES" :key="l" :command="l">
                {{ l === currentLocale ? "✓ " + LOCALE_LABELS[l] : LOCALE_LABELS[l] }}
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </aside>

    <main :id="immersiveChatId" ref="mainEl" v-show="!floatingConversationActive || chatVisible" class="main" :class="{ 'floating-chat': floatingConversationActive, manipulating: chatManipulating, 'chat-animating': chatAnimating }" :data-chat-motion="chatMotionPhase" :data-testid="workflowCanvasActive ? 'workflow-conversation-overlay' : undefined" :inert="floatingConversationActive && chatMinimized" :aria-hidden="floatingConversationActive && chatMinimized ? true : undefined" :style="floatingChatStyle" :aria-label="floatingConversationActive ? t('immersiveBrowser.chat') : undefined">
      <header v-if="floatingConversationActive" class="immersive-chat-head" tabindex="0" :title="t('immersiveBrowser.move')" :aria-label="t('immersiveBrowser.move')" aria-describedby="immersive-chat-help" @pointerdown="activeConversation.begin($event)" @keydown="activeConversation.adjustKey($event)">
        <span id="immersive-chat-help" class="sr-only">{{ t(workflowCanvasActive ? 'agentWorkflow.switchFloatingFocus' : 'immersiveBrowser.switchFocus') }} · {{ t('immersiveBrowser.exitTip') }}</span>
        <span class="immersive-status-dot" :class="{ working: busy, attention: immersiveAttention }" aria-hidden="true" />
        <div class="immersive-chat-title"><strong>{{ immersiveTitle }}</strong><span>{{ t(immersiveAttention ? 'immersiveBrowser.attention' : busy ? 'immersiveBrowser.working' : 'immersiveBrowser.ready') }}</span></div>
        <ImmersiveChatControls :controls-id="immersiveChatId" :minimize-tip="workflowCanvasActive ? t('agentWorkflow.minimizeConversationTip') : undefined" @minimize="activeConversation.minimize" @exit="activeConversation.exit()" />
      </header>
      <StarterCardsTrigger
        v-if="!isDelegatedEmbed && (timeline.length > 0 || canSaveStarterDraft) && !floatingConversationActive"
        :count="starterCards.length"
        :open="starterDialogOpen"
        :can-save-draft="isAdmin ? canSaveStarterDraft : undefined"
        :saving-draft="savingStarterDraft"
        @click="openStarterCards"
        @save-draft="saveCurrentStarterDraft"
      />
      <header v-show="!floatingConversationActive" class="topbar">
        <!-- Published/embed pages identify the Agent without management links. -->
        <div v-if="isStudio" class="studio-context">
          <button
            v-if="managedChat"
            class="studio-back"
            :title="t('uiCoverage.app.backToStudio')"
            @click="backToStudio"
          >
            <el-icon><ArrowLeft /></el-icon>
            <span>{{ t("uiCoverage.app.backToStudio") }}</span>
          </button>
          <span class="studio-agent" :title="studioAgent?.name">
            <template v-if="studioAgent">
              <span class="studio-agent-dot" :style="{ background: studioAgent.color || 'var(--el-color-primary)' }" />
              {{ studioAgent.name }}
            </template>
            <StudioEntrySkeleton v-else area="agent" />
          </span>
        </div>
        <!-- Server root moved here from the sidebar: it describes the whole
             window, not the session list, and stays visible when the
             sidebar is collapsed. -->
        <span v-if="!isStudio || managedChat" class="cwd" :title="cwd">{{ cwd }}</span>
      </header>

      <div v-if="flash" class="notice" :class="{ bad: flash.kind === 'error' }">{{ flash.text }}</div>
      <div v-if="stream.policyReverted.value" class="notice">
        {{ t("agentView.notice.policyReverted", { reason: stream.policyReverted.value }) }}
      </div>
      <div v-if="stream.retry.value" class="notice">
        {{
          t("agentView.notice.retrying", {
            attempt: stream.retry.value.attempt,
            max: stream.retry.value.max,
            delay: formatDuration(stream.retry.value.delayMs),
            reason: stream.retry.value.reason,
          })
        }}
      </div>
      <div v-if="stream.streamError.value" class="notice bad">{{ stream.streamError.value }}</div>

      <!-- Conversation above, composer below: a plain vertical stack. -->
      <div class="chat">
      <div class="conv-wrap">
        <div ref="scroller" class="conversation" :aria-busy="studioEntryLoading || stream.switching.value || undefined" @scroll.passive="onScroll" @wheel.passive="onConversationScrollIntent"
          @touchmove.passive="onConversationScrollIntent" @keydown="onConversationScrollIntent" @pointerdown="onConversationScrollIntent">
          <StudioEntrySkeleton v-if="conversationLoading" area="conversation" />
          <!-- Keep the old conversation readable as context, but inert, until
               the new snapshot replaces it. Entry never guesses emptiness. -->
          <div v-else ref="convInner" class="conv-inner" :class="{ switching: stream.switching.value }" :inert="stream.switching.value || undefined">
            <!-- The empty state is where a deployment says what it is for. With
                 no skill-provided cards this is the original one-line hint, so
                 a plain pogo looks exactly as before. -->
            <template v-if="studioEmptyReady && !timeline.length && !stream.compactions.value.length">
              <!-- The card grid is always mounted now: with no cards at all
                   it still carries the add/import/sample toolbar, which is the
                   way back from an emptied set. -->
              <!-- Agent starters edit in place for the admin: saves PATCH the
                   Agent definition (saveStudioStarters), anyone without the
                   grant keeps the read-only grid. No panels prop — the Agent
                   wire shape has no panel action, so the dialog's panel kind
                   stays disabled. -->
              <div v-if="isStudio" class="studio-starters">
                <StarterCards
                  :agent-id="studioAgentId"
                  :read-only="!isAdmin"
                  :on-save="isAdmin ? saveStudioStarters : undefined"
                  :prepare-draft="prepareStarterDraft"
                  :max-cards="0"
                  :heading="starters?.heading"
                  :cards="starterCards"
                  :fallback="t('agentView.empty.hint')"
                  :set="starters"
                  @pick="onStarter"
                  @saved="loadStarters"
                />
              </div>
              <StarterCards
                :prepare-draft="prepareStarterDraft"
                v-else
                :read-only="!isAdmin"
                :heading="starters?.heading"
                :cards="starterCards"
                :fallback="t('agentView.empty.hint')"
                :set="starters"
                :panels="panels"
                @pick="onStarter"
                @saved="loadStarters"
              />
            </template>
            <template v-for="ex in exchanges" :key="`${stream.displaySessionId.value ?? 'unbound'}:${ex.id}`">
              <CompactionCard
                v-if="ex.compaction"
                state="done"
                :started-at="ex.compaction.started_at ?? ex.compaction.ts"
                :finished-at="ex.compaction.finished_at ?? ex.compaction.ts"
                :instructions="ex.compaction.instructions"
                :result="compactionResult(ex.compaction)"
                :summary="ex.compaction.summary"
                :before-tokens="ex.compaction.before_tokens"
                :after-tokens="ex.compaction.after_tokens"
              />
              <template v-else>
              <div v-if="ex.user" class="ask" :data-mid="ex.user.id">
                <!-- WeChat/iMessage pattern: the send time sits centred above
                     the message. The row below the bubble carries
                     hover-revealed copy/edit actions. Edit refills the
                     composer instead of resending — never speak for the user. -->
                <span v-if="ex.user.ts" class="ask-time">{{ fmtTime(ex.user.ts) }}</span>
                <!-- A background subagent's completion arrives as a harness-made
                     user message: not something the user wrote, so no bubble and
                     no copy/edit/rewind actions — a structured card instead. -->
                <TaskNotice
                  v-if="isTaskNotification(ex.user.text)"
                  :text="ex.user.text"
                  @open-sheet="openSubagentSheet"
                />
                <!-- An ordinary message: the prose, with any picked elements as
                     cards in the bubble instead of the raw <page-element> blocks
                     the model receives. The blocks stay in the message; the
                     bubble shows the exhibit. Attachments render as thumbnails
                     (images) and type-icon cards (files) above the text — and a
                     folder upload as the collapsible tree it was picked as. -->
                <div v-else class="ask-bubble">
                  <OrderedMessageContent v-if="ex.user.orderedContent" :blocks="ex.user.orderedContent" @open-file="openAttachmentPreview" />
                  <template v-else>
                  <div
                    v-if="ex.user.images?.length || (ex.user.files?.length && !askTreeHasDir(ex.user.files))"
                    class="ask-images"
                  >
                    <el-image
                      v-for="ref in ex.user.images"
                      :key="ref"
                      class="ask-image"
                      :src="workspaceFileUrl(ref)"
                      :preview-src-list="(ex.user.images ?? []).map((r) => workspaceFileUrl(r))"
                      :alt="t('agentView.attach.alt')"
                      fit="cover"
                      loading="lazy"
                      preview-teleported
                      hide-on-click-modal
                    />
                    <template v-if="!askTreeHasDir(ex.user.files)">
                      <button
                        v-for="f in ex.user.files ?? []"
                        :key="f.path"
                        class="ask-file"
                        :title="t('agentView.attach.open')"
                        @click="openAttachmentPreview({ path: f.path, name: f.name })"
                      >
                        <Icon class="ask-file-icon" :icon="fileIcon(f.name)" />
                        <span class="ask-file-name">{{ f.name }}</span>
                      </button>
                    </template>
                  </div>
                  <AskFileTree
                    v-if="askTreeHasDir(ex.user.files)"
                    :files="ex.user.files ?? []"
                    @open="openAttachmentPreview"
                  />
                  <!-- The bubble's waiting state while its attachments are
                       still uploading: one bytes-weighted bar standing where
                       the attachments will land. The real blocks swap in when
                       the bytes do. -->
                  <AskUploadCard v-if="uploadStates[ex.user.id]" :state="uploadStates[ex.user.id]" />
                  <template v-for="block in [skillMessageBlock(ex.user.text)]" :key="ex.user.id">
                    <SkillBlock v-if="block" :block="block">
                      <template #task>
                        <template v-if="parsePickedCached(block.trailing).display">{{ parsePickedCached(block.trailing).display }}</template>
                        <ElementCard v-for="(c, ci) in parsePickedCached(block.trailing).cards" :key="ci" :card="c" />
                      </template>
                    </SkillBlock>
                    <template v-else>
                      <template v-if="parsePickedCached(ex.user.text).display">{{ parsePickedCached(ex.user.text).display }}</template>
                      <ElementCard v-for="(c, ci) in parsePickedCached(ex.user.text).cards" :key="ci" :card="c" />
                    </template>
                  </template>
                  </template>
                  <AskUploadCard v-if="ex.user.orderedContent && uploadStates[ex.user.id]" :state="uploadStates[ex.user.id]" />
                  <!-- Derived execution history stays with its original question;
                       it is not part of the text copied, edited or sent. -->
                  <TaskTodoCard v-if="todoState.byUser.has(ex.user.id)" :plan="todoState.byUser.get(ex.user.id)!" inline />
                </div>
                <!-- The action row serves every message the user typed — a
                     /skill invocation included: its raw text copies, refills
                     the composer, and rewinds exactly like a bubble's. Only
                     harness-made notices stay out. -->
                <div v-if="!isTaskNotification(ex.user.text)" class="ask-meta">
                  <button class="ask-act" :title="t('common.copy')" @click="copyAsk(ex.user.id, ex.user.text, ex.user.orderedContent)">
                    <el-icon><Check v-if="copiedAsk === ex.user.id" /><CopyDocument v-else /></el-icon>
                  </button>
                  <button class="ask-act" :title="t('agentView.ask.edit')" @click="ex.user.orderedContent ? restoreOrderedDraft(ex.user.orderedContent, ex.user.inputLayout) : editAsk(ex.user.text)">
                    <el-icon><EditPen /></el-icon>
                  </button>
                  <button
                    v-if="canUse('schedules') && studioAgentId"
                    class="ask-act"
                    :title="t('agentView.ask.schedule')"
                    :aria-label="t('agentView.ask.schedule')"
                    :disabled="stream.switching.value"
                    data-testid="schedule-from-message"
                    @click="scheduleMessage(ex.user)"
                  >
                    <el-icon><AlarmClock /></el-icon>
                  </button>
                  <!-- Rewind is disabled mid-run rather than hidden: a control
                       that vanishes exactly when it cannot act reads as a bug,
                       a greyed one reads as "wait for the run". It is also the
                       one action here that is always visible — copy and edit
                       stay hover-revealed because they are cheap and
                       reversible, while rewind is the safety net, and every
                       product that hid its restore entry behind a hover
                       (Cursor, Zed's early builds) shipped S2-severity
                       "the button is missing" reports for it. -->
                  <button
                    class="ask-act stay"
                    :title="isDelegatedEmbed ? unavailableTip : t('agentView.ask.rewindTitle')"
                    :disabled="busy || isDelegatedEmbed"
                    @click="rewindAsk(ex.user)"
                  >
                    <el-icon><RefreshLeft /></el-icon>
                  </button>
                </div>
              </div>
              <!-- Step one of an image turn on a text-only model: the vision
                   helper's per-image descriptions. It lives in the work area —
                   ahead of the work log or the turn cards — because it was the
                   run's job, never part of the question. -->
              <DescribeCards
                v-if="exchangeDescribes(ex).length"
                :items="exchangeDescribes(ex)"
                :url-for="workspaceFileUrl"
              />
              <!-- One stable activity host for both running and completed work.
                   Completion changes status only; it never remounts the process. -->
              <WorkLog
                :exchange="ex"
                :run-active="busy"
                :active="ex.id === liveExchangeId"
                :live="ex.id === liveExchangeId ? liveState : undefined"
                :run-started-at="runStartedAt"
                :waiting-for-model="ex.id === liveExchangeId && waitingForModel"
                :end-reason="executionEvidence.endReasons.get(ex.id)"
                :server-offset="serverClockOffset"
                :skills="skills"
                :cwd="cwd"
                :todo-pinned="!!todos && todoState.currentUserId === ex.id"
                @suggest="suggest"
                @decide="decide"
                @freeze="(id: string) => updateGateClock(id, true)"
                @thaw="(id: string) => updateGateClock(id, false)"
                @open-sheet="openSubagentSheet"
                @open-file="openAttachmentPreview"
              />
              <!-- The exchange's actions, once per question and only after the
                   run settles: copy and share both take the whole reply — every
                   turn's text — never one turn of it. A task-notification
                   "exchange" was opened by the harness, not asked by anyone, so
                   it gets neither. -->
              <div
                v-if="ex.finished && ex.answer && ex.user && !isTaskNotification(ex.user.text)"
                class="ex-actions"
              >
                <button
                  class="act"
                  :title="copiedExchange === ex.id ? t('common.copied') : t('turnCard.copyAnswer')"
                  @click.stop="copyExchange(ex)"
                >
                  <el-icon><Check v-if="copiedExchange === ex.id" /><CopyDocument v-else /></el-icon>
                </button>
                <button
                  class="act"
                  :title="t('turnCard.downloadMd')"
                  :disabled="!current"
                  @click.stop="downloadCurrentMarkdown"
                >
                  <el-icon><Download /></el-icon>
                </button>
                <button class="act" :title="t('turnCard.shareAnswer')" v-if="canUse('sharing')" @click.stop="onShareExchange(ex)">
                  <el-icon><Share /></el-icon>
                </button>
              </div>
              </template>
            </template>
            <CompactionCard
              v-if="compaction && compaction.sessionId === current && compaction.state !== 'done'"
              :key="`${compaction.sessionId}:${compaction.startedAt}`"
              :state="compaction.state"
              :started-at="compaction.startedAt"
              :finished-at="compaction.finishedAt"
              :instructions="compaction.instructions"
              :result="compaction.result"
              :message="compaction.message"
              :summary="compaction.summary"
            />
            <!-- The gap between sending and the first token, and the same gap
                 between turns: the run is active but nothing new is on screen
                 yet. Tools and gate cards are their own feedback. -->
            <div v-if="waiting && !liveExchangeId" class="waiting">
              <span class="pend-dots"><i /><i /><i /></span>
              {{ t("agentView.waiting") }}
            </div>
            <!-- Next-step chips, after the answer they follow from. Generated
                 by the server when its event names this turn, else a skill's
                 rule match; see agent/followupPick.ts for the precedence and
                 why silence is the default. -->
            <FollowupChips v-if="followupChips.length" :chips="followupChips" @pick="onFollowupPick" />
          </div>
        </div>

        <!-- Turn rail (after the YX agent-qa reference): one dot per user
             message on a right-edge track. The fill is scroll progress, the
             enlarged dot is the turn the viewport is in, hover previews the
             question, click jumps. It appears only once the conversation
             actually overflows — a short one needs no map. -->
        <div v-if="railVisible" class="turn-rail">
          <div class="rail-track">
            <div class="rail-fill" :style="{ height: railFill + '%' }" />
          </div>
          <el-popover
            v-for="e in railEntries"
            :key="e.dot.id"
            :trigger="['hover', 'focus']"
            :trigger-keys="[]"
            placement="left"
            :width="420"
            :show-arrow="false"
            :show-after="100"
            :hide-after="160"
            :enterable="true"
            :persistent="false"
            popper-class="rail-message-popover"
            :popper-options="railPopoverOptions"
          >
            <template #reference>
                <button
                  class="rail-dot"
                  :class="[e.dot.marker, { active: entryActive(e) }]"
                  :style="{ top: e.dot.topPct + '%' }"
                  :aria-label="
                    e.cluster
                      ? `${t('agentView.rail.subagentMsg')} × ${e.cluster.length}`
                      : t('agentView.rail.jumpTo', { q: e.dot.question })
                  "
                  @click="jumpToTurn(e.dot)"
                >
                  <el-icon v-if="e.dot.marker === 'skill'" class="glyph"><MagicStick /></el-icon>
                  <RailGlyph v-else :marker="e.dot.marker" />
                  <!-- The count rides the merged dot's corner: one glyph says "some
                       subagent activity here", the number says how much. -->
                  <span v-if="e.cluster" class="rail-badge">{{ e.cluster.length }}</span>
                </button>
            </template>
            <div class="rail-tip" data-testid="rail-message-preview" :data-user-id="e.dot.id">
              <span class="rail-tip-kind">
                <el-icon v-if="e.dot.marker === 'skill'" class="tip-glyph"><MagicStick /></el-icon>
                <RailGlyph v-else :marker="e.dot.marker" class="tip-glyph" />
                <span>{{ t(railKindKey(e.dot.marker)) }}</span>
                <span v-if="e.cluster" class="rail-tip-count">× {{ e.cluster.length }}</span>
              </span>
              <template v-if="e.cluster">
                <span v-for="d in e.cluster" :key="d.id" class="rail-tip-row">
                  <span class="rail-tip-question">{{ d.question }}</span>
                  <span v-if="d.ts" class="rail-tip-time">{{ fmtTime(d.ts) }}</span>
                </span>
              </template>
              <template v-else>
                <span class="rail-tip-question">{{ e.dot.question }}</span>
                <span v-if="e.dot.ts" class="rail-tip-time">{{ fmtTime(e.dot.ts) }}</span>
                <TaskTodoCard v-if="todoState.byUser.has(e.dot.id)" :plan="todoState.byUser.get(e.dot.id)!" inline />
              </template>
            </div>
          </el-popover>
        </div>

        <!-- Jump to bottom: the escape hatch for a disengaged follow. Codex
             (centered "Jump to bottom" pill) and ZCode (round button docked
             above the composer) both land on the same grammar — a pill
             centered over the conversation's bottom edge, visible exactly
             while auto-follow is off, click scrolls home and follow resumes
             through onScroll. -->
        <button v-if="!stickToBottom" class="jump-bottom" @click="jumpToLatest">
          <el-icon><ArrowDown /></el-icon>
          <span>{{ t("agentView.conversation.jumpToBottom") }}</span>
        </button>
      </div>

      <div class="compose-col">
      <UserInputBar v-if="!isDelegatedEmbed && current && !stream.switching.value && stream.live.value.pending_user_inputs.length"
        :key="current" :requests="stream.live.value.pending_user_inputs" :pending="userInputPending" :errors="userInputErrors"
        @answer="answerUserInput" @skip="answerUserInput" />
      <ApprovalBar v-if="!isDelegatedEmbed && current && !stream.switching.value && stream.live.value.pending_gates.length"
        :key="current" :gates="stream.live.value.pending_gates" @decide="decide"
        @freeze="id => updateGateClock(id, true)" @thaw="id => updateGateClock(id, false)" />
      <!-- Auxiliary work stays in a bounded stack. The input is its own
           sibling, so a long plan or queue never covers or scrolls it away. -->
      <div class="compose-support" data-testid="composer-support">
      <StudioToolPolicyNotice v-if="isStudio && current && !studioEntryLoading" :pending="studioToolPending" :error="studioConfigurationError" :syncing="studioConfigurationSyncing" @retry="refreshStudioConfiguration()" @inspect="selectActiveSheet('skills')" />
      <TodoBar v-if="todos" :key="todoState.currentUserId" :todos="todos" :busy="busy" data-testid="current-task-todo" :data-user-id="todoState.currentUserId" />
      <ConversationInbox
        v-if="!isDelegatedEmbed && !stream.switching.value"
        :items="stream.inbox.value"
        :working="busy"
        :disabled="sending"
        :changing="inboxChanges"
        :errors="inboxErrors"
        @steer="item => changeInbox(item, 'steer')"
        @edit="(item, text, parts, layout) => changeInbox(item, 'edit', text, parts, layout)"
        @open-file="openAttachmentPreview"
        @withdraw="item => changeInbox(item, 'withdraw')"
      />
      </div>
      <Teleport to="body">
        <div v-if="suggestionMenuVisible" ref="suggestionPopover" class="composer-command-popover" :class="{ 'with-token-search': tokenPickerQuery !== null }" :style="suggestionPopupStyle">
          <div v-if="tokenPickerQuery !== null" class="composer-token-search">
            <input ref="tokenPickerInput" v-model="tokenPickerQuery" data-testid="composer-token-search" :placeholder="t('agentView.commands.searchTokens')" :aria-label="t('agentView.commands.searchTokens')" aria-autocomplete="list" :aria-controls="suggestionItems.length ? suggestionMenuId : undefined" :aria-activedescendant="selectedSuggestionOption" @focus="tokenPickerFocused = true" @blur="onTokenPickerBlur" @keydown="onTokenPickerKeydown" @compositionstart="tokenPickerComposing = true" @compositionend="tokenPickerComposing = false" />
          </div>
          <SlashCommandMenu :id="suggestionMenuId" :items="suggestionItems" :context-percent="contextReading.percent" :selected-id="selectedSuggestionId" data-testid="composer-command-menu" @pick="completeSuggestion" @highlight="highlightSuggestion" />
          <div v-if="tokenPickerQuery !== null && !suggestionItems.length" class="composer-token-empty" role="status">
            {{ emptySuggestionMessage }}
            <button v-if="mcpPickerActive && (mcpCatalogState === 'failed' || mcpCatalogState === 'ready' && !mcpMethods.length)" type="button" @mousedown.prevent @click="loadMcpMethods">{{ t('common.retry') }}</button>
          </div>
        </div>
      </Teleport>

      <!-- Context warning lives above the composer rather than in the topbar:
           "should I still send this" is decided here, so the warning is too.
           Non-dismissible while it applies: forgetting it is how a turn dies. -->
      <div v-if="ctxHigh" class="ctx-warn">
        {{ t("agentView.ctxWarn.text", { pct: ctxPct }) }}
        <button @click="startNewSession">{{ t("agentView.ctxWarn.newSession") }}</button>
      </div>

      <!-- Drop targets the card, not the textarea: a file dragged anywhere
           over the composer lands as a chip. dragover must be prevented for
           drop to fire at all. -->
      <div ref="composerCard" class="input-card" :class="{ 'entry-loading': studioEntryLoading }" :aria-busy="studioEntryLoading || undefined" :inert="studioEntryLoading || stream.switching.value || undefined" @dragover.prevent @drop.prevent="onDrop">
          <StudioEntrySkeleton v-if="studioEntryLoading" area="composer" class="composer-entry" />
          <div class="composer-file-actions">
          <BackgroundShellTasks
            v-if="!isDelegatedEmbed"
            :session-id="current"
            :display-session-id="stream.displaySessionId.value"
            :tasks="stream.shellTasks.value"
            :connected="stream.connected.value"
            :disabled="stream.switching.value"
            @updated="stream.acceptShellTask"
          />
          <ManagedWebServices
            v-if="!isDelegatedEmbed"
            ref="managedWebsites"
            :session-id="current"
            :display-session-id="stream.displaySessionId.value"
            :disabled="stream.switching.value"
            :connected="stream.connected.value"
            :change="stream.webServiceChanged.value"
            @open="openManagedWebsite"
            @deleted="deletedManagedWebsite"
          />
          <BackgroundTasksButton
            :agent-id="studioAgentId"
            :enabled="isStudio && !isDelegatedEmbed && !studioEntryLoading"
            :opening="taskOpening"
            @open="openBackgroundTask"
            @center="openScheduleCenter"
          />
          <!-- The floating session-files button: a compact chip hovering off
               the card's top-right corner — icon + label + inline count (the
               "Filters · 3" reading: the count lives inside the button, not
               as a floating corner dot). The glyph is documents, never a
               paperclip — attaching lives in the toolbar. -->
          <el-popover
            v-model:visible="attachPop"
            placement="top-end"
            :width="340"
            :show-after="150"
            popper-class="attach-pop"
          >
            <template #reference>
              <button
                class="attach-fab"
                :title="t('agentView.attach.workspaceTitle')"
                :aria-label="t('agentView.attach.workspaceTitle')"
              >
                <DocumentsIcon />
                <span class="attach-fab-label">{{ t("agentView.attach.workspaceShort") }}</span>
                <span v-if="sessionAttachments.length" :key="sessionAttachments.length" class="attach-fab-badge">{{ sessionAttachments.length }}</span>
              </button>
            </template>
            <div class="attach-pop-list">
              <p class="attach-pop-head">
                <span>{{ t("agentView.attach.workspaceTitle") }}</span>
                <span v-if="sessionAttachments.length" class="attach-pop-count">{{ sessionAttachments.length }}</span>
              </p>
              <div v-if="!sessionAttachments.length" class="attach-pop-empty">
                <DocumentsIcon class="attach-pop-empty-icon" />
                <p>{{ t("agentView.attach.empty") }}</p>
              </div>
              <!-- Folder uploads render as the folders they were: the same
                   tree rows the sent bubbles use, mounted here without
                   AskFileTree's panel chrome so the popover stays a popover.
                   Loose-file-only sessions have no tree to show and keep the
                   thumbnail rows below. -->
              <template v-else-if="sessionTree.hasDir">
                <AskTreeNode
                  v-for="n in sessionTree.roots"
                  :key="`${n.dir ? 'd' : 'f'}:${n.path}`"
                  :node="n"
                  @open="openAttachmentPreview"
                />
              </template>
              <template v-else>
                <button
                  v-for="a in sessionAttachments"
                  :key="a.path"
                  class="attach-pop-row"
                  :title="t('agentView.attach.open')"
                  @click="openAttachmentPreview(a)"
                >
                  <img v-if="a.image" class="attach-pop-thumb" :src="workspaceFileUrl(a.path)" alt="" loading="lazy" />
                  <span v-else class="attach-pop-tile"><Icon class="attach-pop-kind" :icon="fileIcon(a.name)" /></span>
                  <span class="attach-pop-name">{{ a.name }}</span>
                  <el-icon class="attach-pop-go"><ArrowRight /></el-icon>
                </button>
              </template>
            </div>
          </el-popover>
          <!-- Attachments live in the card for the same reason the picked
               elements do: what will be sent sits in the box it will be sent
               from. Image chips are thumbnails; file chips carry the type's
               icon. Backspace on the empty composer removes the last chip. -->
          </div>
          <div v-if="composerLayout === 'top' && attachments.length" class="attach-chips">
            <div
              v-for="a in attachments"
              :key="a.key"
              class="attach-chip"
              :class="{ error: a.status === 'error', ready: a.status === 'ready' }"
              :title="a.folder ? folderChipTitle(a) : a.status === 'error' ? a.error : a.name"
              @click="previewAttachment(a)"
            >
              <img v-if="a.kind === 'image' && a.preview" class="attach-thumb" :src="a.preview" :alt="a.name" />
              <Icon v-else-if="a.kind === 'folder'" class="attach-kind-icon" :icon="folderIcon" />
              <Icon v-else class="attach-kind-icon" :icon="fileIcon(a.name)" />
              <span v-if="a.status === 'uploading'" class="attach-spin"><el-icon class="is-loading"><Loading /></el-icon></span>
              <span v-else-if="a.status === 'error'" class="attach-err-mark"><el-icon><CircleCloseFilled /></el-icon></span>
              <span class="attach-name">{{ a.name }}</span>
              <!-- The folder chip's counter doubles as its progress: k/N
                   while the pool uploads, N once the tree has landed. -->
              <span v-if="a.folder" class="attach-count">
                {{ a.status === "uploading" ? `${a.folder.done}/${a.folder.total}` : a.folder.total }}
              </span>
              <button class="attach-x" :aria-label="t('common.close')" :title="t('common.close')" @click.stop="removeAttachment(a.key)">
                <el-icon><Close /></el-icon>
              </button>
            </div>
          </div>
          <!-- Picked elements live INSIDE the composer card — the Slack/ChatGPT
               attachment reading: what will be sent sits in the box it will be
               sent from, and the card's focus ring frames chips and textarea as
               one thing. Backspace on the empty textarea removes the last chip;
               a focused chip removes itself. -->
          <div v-if="composerLayout === 'top' && elementPicks.length" class="element-chips">
            <el-tooltip
              v-for="(p, i) in elementPicks"
              :key="p.key"
              :content="p.preview || p.label"
              placement="top"
              :show-after="150"
              popper-class="echip-tip"
            >
              <span
                class="echip"
                :class="{ 'has-data': p.hasData }"
                tabindex="0"
                :aria-label="`${p.pageTitle ? p.pageTitle + ' ' : ''}${p.label}`"
                @keydown="onChipKey($event, i)"
              >
                <ElementPageIcon class="echip-icon" :url="p.pageURL || parsePickedCached(p.block).cards[0]?.url" :icon="p.icon" />
                <!-- The data badge: this pick brings the page's own numbers. -->
                <svg v-if="p.hasData" class="echip-data" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true">
                  <path d="M18 20V10M12 20V4M6 20v-6" />
                </svg>
                <span class="echip-page">{{ p.pageTitle || p.label }}</span>
                <span v-if="p.pageTitle && p.label !== p.pageTitle" class="echip-sep">·</span>
                <span v-if="p.pageTitle && p.label !== p.pageTitle" class="echip-label">{{ p.label }}</span>
                <button
                  class="echip-x"
                  :aria-label="t('common.close')"
                  :title="t('common.close')"
                  @click="elementPicks.splice(i, 1)"
                >
                  <el-icon><Close /></el-icon>
                </button>
              </span>
            </el-tooltip>
          </div>
          <div v-if="composerLayout === 'top' && composerSkills.length" class="composer-token-row">
            <ComposerToken v-for="part in composerSkills" :key="part.id" kind="skill" :name="part.name" :label="part.label" :type-label="t('agentView.commands.skillTokenLabel')" :description="composerSkillDescription(part.name)" :disabled="isDelegatedEmbed" @change="activateComposerReference(part)" @remove="updateComposerParts(composerParts.filter(item => item.type === 'text' || item.id !== part.id))" />
          </div>
          <div v-if="composerLayout === 'top' && composerMcps.length" class="composer-token-row">
            <ComposerToken v-for="part in composerMcps" :key="part.id" kind="mcp" :name="`${part.server}/${part.name}`" :label="part.label" :type-label="t('agentView.commands.mcpTokenLabel')" :description="mcpMethods.find(method => method.server === part.server && method.name === part.name)?.description || ''" :disabled="isDelegatedEmbed" @change="activateComposerReference(part)" @remove="updateComposerParts(composerParts.filter(item => item.type === 'text' || item.id !== part.id))" />
          </div>
          <div v-if="composerLayout === 'top' && composerShortcuts.length" class="composer-shortcut-row">
            <span v-for="part in composerShortcuts" :key="part.id" class="composer-shortcut-chip">
              <ShortcutReference :title="part.title" :content="part.text" :icon="part.icon" :language="part.language" :parts="part.parts" />
              <button type="button" :aria-label="t('shortcuts.removeReference', { title: part.title })" @click="updateComposerParts(composerParts.filter(item => item.type === 'text' || item.id !== part.id))">×</button>
            </span>
          </div>
          <div v-if="composerLayout === 'top' && composerOffices.length" class="composer-shortcut-row">
            <span v-for="part in composerOffices" :key="part.id" class="composer-shortcut-chip">
              <OfficeReference :office="part.office" />
              <button type="button" :aria-label="t('shortcuts.removeReference', { title: part.office.title })" @click="updateComposerParts(composerParts.filter(item => item.type === 'text' || item.id !== part.id))">×</button>
            </span>
          </div>
          <div v-if="composerToken" class="composer-token-row">
            <ComposerToken ref="composerTokenControl" :kind="composerToken.kind" :context-percent="contextReading.percent" :name="composerToken.kind === 'command' ? composerToken.name.slice(1) : composerToken.name" :label="composerTokenLabel" :type-label="t(composerToken.kind === 'mcp' ? 'agentView.commands.mcpTokenLabel' : composerToken.kind === 'skill' ? 'agentView.commands.skillTokenLabel' : 'agentView.commands.commandTokenLabel')" :description="composerTokenDescription" :disabled="isDelegatedEmbed" @change="changeComposerToken" @remove="removeComposerToken" />
          </div>
          <div v-if="composerToken?.kind === 'command'" class="composer-command-context">{{ compactSuggestionDescription }}</div>
          <InlineComposer
            v-if="composerLayout === 'inline' && composerToken?.kind !== 'command'"
            ref="inlineComposer"
            :model-value="composerParts"
            :placeholder="busy ? t('agentView.composer.placeholderBusy') : t('agentView.composer.inlinePlaceholder')"
            :aria-label="t('agentView.composer.inlineMode')"
            @update:model-value="updateComposerParts"
            @selection-change="updateInlineSelection"
            @submit="send"
            @keydown="onInlineKeydown"
            @focus="composerFocused = true"
            @blur="composerFocused = false"
            @compositionstart="composerComposing = true"
            @compositionend="composerComposing = false"
            @paste-files="attachFiles"
            @drop-files="attachFiles"
            @activate-reference="activateComposerReference"
          />
          <textarea
            v-else
            ref="inputBox"
            v-model="composerBody"
            data-testid="composer-input"
            aria-autocomplete="list"
            :aria-haspopup="suggestionMenuVisible ? 'listbox' : undefined"
            :aria-controls="suggestionMenuVisible && tokenPickerQuery === null ? suggestionMenuId : undefined"
            :aria-activedescendant="tokenPickerQuery === null ? selectedSuggestionOption : undefined"
            :aria-describedby="composerInvocationError || composerInvocationWaiting ? `${suggestionMenuId}-status` : undefined"
            :placeholder="
              composerToken?.kind === 'command' ? t('agentView.commands.compactFocusPlaceholder') : composerToken?.kind === 'mcp' ? t('agentView.commands.mcpTaskPlaceholder') : composerToken?.kind === 'skill' ? t('agentView.commands.skillTaskPlaceholder') : busy
                ? t('agentView.composer.placeholderBusy')
                : t('agentView.composer.placeholder')
            "
            :rows="composerToken?.kind === 'command' ? 2 : 3"
            @focus="composerFocused = true; updateTextareaSelection()"
            @input="updateTextareaSelection"
            @select="updateTextareaSelection"
            @click="updateTextareaSelection"
            @keyup="updateTextareaSelection"
            @blur="composerFocused = false"
            @compositionstart="composerComposing = true"
            @compositionend="composerComposing = false"
            @keydown="onKeydown"
            @paste="onPaste"
          />
          <div v-if="(composerInvocationError && !suggestionMenuVisible) || composerInvocationWaiting" :id="`${suggestionMenuId}-status`" class="composer-token-status" :class="{ invalid: !!composerInvocationError }" data-testid="composer-token-status" :role="composerInvocationError ? 'alert' : 'status'">
            <span>{{ composerInvocationError || composerWaitingMessage }}</span>
            <button v-if="composerSkillError && skillCatalogState === 'failed'" type="button" @click="loadSkills">{{ t('common.retry') }}</button>
            <button v-else-if="composerMcpError && (mcpCatalogState === 'failed' || mcpCatalogState === 'ready' && !mcpMethods.length)" type="button" @click="loadMcpMethods">{{ t('common.retry') }}</button>
          </div>
          <div v-if="isStudio && skillConfigDeferred" class="composer-token-status" role="status" data-testid="studio-skill-deferred">{{ t('agentView.commands.skillConfigDeferred') }}</div>
          <div class="input-bar">
            <ModelPicker
              :models="pickerModels"
              :current="stream.run.value.model || (isStudio ? studioCapabilities?.model || studioAgent?.model : delegatedUi?.models.default)"
              :disabled="busy || compacting || studioModelSyncing || !current || !canUse('model_selection') || (isStudio && (!studioCapabilities || pickerModels.length < 2))"
              :disabled-tip="isStudio ? studioModelTip : undefined"
              @change="switchModel"
            />
            <button v-if="isStudio && studioCapabilitiesError" class="studio-model-retry" :title="studioCapabilitiesError" @click="loadStudioCapabilities">
              {{ t('uiCoverage.app.retryModels') }}
            </button>
            <span v-if="stream.run.value.model_config_pending ?? (isStudio && studioModelDeferred)" class="studio-model-sync-note" role="status" data-testid="studio-model-deferred">{{ t('agentView.modelSync.deferred') }}</span>
            <ContextMeter
              :used="stream.contextTokens.value"
              :estimated="stream.contextEstimated.value"
              :window="stream.run.value.context_window ?? 0"
              :overhead="stream.overheadTokens.value"
              :overhead-breakdown="stream.overheadBreakdown.value"
              :messages="stream.messages.value"
              :results="stream.results.value"
              :context-messages="stream.contextPrompt.value?.messages"
              :latest-compaction="stream.compactions.value[stream.compactions.value.length - 1] ?? null"
              :clear-trigger="stream.clearTrigger.value"
              :usage="stream.usage.value"
              :disabled="busy || isDelegatedEmbed"
              :compacting="compacting"
              @compact="compactSession"
            />
            <PolicyPicker
              v-if="!isDelegatedEmbed"
              :mode="policy.mode"
              :remaining="policy.remaining_turns"
              @change="pickPolicy"
            />
            <div class="spacer" />
            <!-- The wrapper is transparent (display: contents) in the right
                 dock; in the bottom dock it floats the actions to the card's
                 top-right corner. -->
            <div class="actions">
              <!-- The attach door: ONE button — file and folder are two moods
                   of one intent, and the popover above asks which before
                   tripping the matching hidden input. The menu closes before
                   the OS dialog opens, so nothing stale lingers behind it;
                   paste and drop stay direct doors that skip the ask
                   entirely. -->
              <el-popover
                v-if="canUse('attachments')"
                v-model:visible="attachMenu"
                placement="top-end"
                trigger="click"
                :width="272"
                :teleported="false"
                popper-class="attach-menu-pop"
              >
                <template #reference>
                  <button
                    class="attach-btn"
                    :title="t('agentView.attach.buttonTitle')"
                    :aria-label="t('agentView.attach.buttonTitle')"
                    :aria-expanded="attachMenu"
                  >
                    <UploadIcon />
                  </button>
                </template>
                <div class="attach-menu">
                  <div class="attach-menu-title">{{ t("agentView.attach.menuTitle") }}</div>
                  <button class="attach-option" @click="pickAttach('files')">
                    <span class="option-icon files"><UploadIcon /></span>
                    <span class="option-text">
                      <span class="option-label">{{ t("agentView.attach.filesLabel") }}</span>
                      <span class="option-desc">{{ t("agentView.attach.filesDesc") }}</span>
                    </span>
                  </button>
                  <button class="attach-option" @click="pickAttach('folder')">
                    <span class="option-icon folder"><FolderUploadIcon /></span>
                    <span class="option-text">
                      <span class="option-label">{{ t("agentView.attach.folderLabel") }}</span>
                      <span class="option-desc">{{ t("agentView.attach.folderDesc") }}</span>
                    </span>
                  </button>
                  <div class="attach-menu-hint">{{ t("agentView.attach.menuHint") }}</div>
                </div>
              </el-popover>
              <!-- The file door's input: the accept list is the server's
                   attachment allowlist. -->
              <button
                v-if="!isDelegatedEmbed"
                type="button"
                class="composer-layout-toggle"
                :class="{ active: composerLayout === 'inline' }"
                data-testid="composer-layout-toggle"
                :aria-pressed="composerLayout === 'inline'"
                :aria-label="t(composerLayout === 'inline' ? 'agentView.composer.switchTop' : 'agentView.composer.switchInline')"
                :title="t(composerLayout === 'inline' ? 'agentView.composer.switchTop' : 'agentView.composer.switchInline')"
                :disabled="composerComposing || composerToken?.kind === 'command'"
                @mousedown.prevent
                @click="toggleComposerLayout"
              ><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M3 5h18M3 19h18M3 12h4m10 0h4" /><rect x="9" y="9" width="6" height="6" rx="1.5" /></svg><span v-if="composerLayout === 'inline'">{{ t('agentView.composer.inlineMode') }}</span></button>
              <input
                v-if="canUse('attachments')"
                ref="attachInput"
                type="file"
                :accept="attachAccept"
                multiple
                hidden
                @change="attachFiles(($event.target as HTMLInputElement).files!); attachInput && (attachInput.value = '')"
              />
              <!-- The folder door: a separate input, because webkitdirectory
                   makes a picker folder-ONLY (no mixing in single files), and
                   accept is ignored in directory mode anyway — the door check
                   filters per file once the tree is in hand. webkitdirectory
                   works in Chrome, Edge, Firefox and desktop Safari; it is set
                   imperatively because it is a prefixed attribute Vue does
                   not know (see onMounted). -->
              <input v-if="canUse('attachments')" ref="folderInput" type="file" multiple hidden @change="onFolderPicked" />
              <button v-if="busy" class="stop" @click="stop">{{ t("agentView.composer.stop") }}</button>
              <!-- Enabled while a run is in flight: the message joins that run
                   instead of being refused. The steering variant turns the
                   button blue so the changed action gets a changed look. -->
              <button
                class="send-btn"
                :class="{ steering: busy && !isDelegatedEmbed && !skillInvocation && !mcpInvocation }"
                :disabled="studioEntryLoading || stream.switching.value || sending || !!composerInvocationError || composerInvocationWaiting || (isDelegatedEmbed && (busy || gated || !canUse('conversation'))) || (!input.trim() && !elementPicks.length && !sendableAttachments.length && !composerShortcuts.length && !composerSkills.length && !composerMcps.length && !composerOffices.length)"
                :title="composerInvocationWaiting ? composerWaitingMessage : busy && !isDelegatedEmbed ? t('conversationInbox.sendTitle') : t('common.send')"
                @click="send"
              >
                <span>{{ busy && !isDelegatedEmbed && !skillInvocation && !mcpInvocation ? t("conversationInbox.sendLabel") : t("common.send") }}</span>
                <el-icon><ArrowUp /></el-icon>
              </button>
            </div>
          </div>
        </div>
      </div>
      </div>
      <button v-if="floatingConversationActive" class="immersive-chat-resize" :title="t('immersiveBrowser.resize')" :aria-label="t('immersiveBrowser.resize')" @pointerdown="activeConversation.begin($event, true)" @keydown="activeConversation.adjustKey($event, true)"><svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true"><path d="M3 10l7-7M7 10l3-3" stroke="currentColor"/></svg></button>
    </main>

    <MinimizedSessionBar v-if="floatingConversationActive && restoreVisible" :title="immersiveTitle" :controls-id="immersiveChatId"
      :events="stream.progressEvents.value" :busy="busy" :attention="immersiveAttention" :disconnected="stream.unreachable.value"
      @restore="activeConversation.restore()" @exit="activeConversation.exit()" />

    <!-- The dock is a sheet container docked on the right at full height:
         workspace files, skills, and the hub — a browser-style tab strip
         holding the shell, each registered -web-panel, and every subagent
         transcript opened from the timeline. -->
    <DockArea
      v-if="(!isStudio || studioAgent) && (canUse('workspaces') || canUse('terminal'))"
      :immersive="browserImmersive"
      :fullscreen="browserFullscreen"
      :fullscreen-available="immersive.fullscreenAvailable"
      :active="activeSheet"
      :tabs="visibleHubTabs"
      :browser-busy="browserMutationBusy"
      :browser-closing-tab-ids="browserClosingTabIds"
      :browser-restoring="browserRestoring"
      :browser-restore-error="browserRestoreError"
      :panels="panels"
      :active-tab="hubActiveTab"
      :live-tabs="liveTabs"
      :at="hubAt"
      :maximized="hubMaximized"
      :items="timeline"
      :workspace="currentWorkspace"
      :session-id="current"
      :file-revision="stream.workspaceRevision.value"
      :studio-bound="isStudio"
      :tool-revision="studioToolRevision"
      :capability-revision="studioCapabilityRevision"
      :tools-pending="studioToolPending"
      :agent-id="studioAgentId || undefined"
      :workflow-selection="internalWorkflowSelection"
      :workflow-conversation-open="workflowConversationOpen"
      v-model:workflow-immersive="workflowImmersiveRequested"
      :shortcuts-disabled="isDelegatedEmbed"
      :information-busy="informationInserting"
      @insert-shortcut="insertShortcut"
      @insert-information="insertDockInformation"
      @open-starter="onStarter"
      @manage-starters="openStarterCards"
      @office-reference="onOfficeReference"
      :schedule-attention="scheduleAttention"
      @update:active="selectActiveSheet"
      @update:active-tab="selectHubTab"
      @navigate-file="cancelPendingAnswerFile"
      @open-sentinel-file="openSentinelFile"
      @close-tab="closeHubTab"
      @update:maximized="hubMaximized = $event"
      @workflow-workspace="workflowWorkspaceOpen = $event"
      @workflow-conversation="setWorkflowConversationOpen"
      @update:immersive="toggleImmersive"
      @file-office-editing="fileOfficeView = $event"
      @office-opened="officeOpened"
      @fullscreen="toggleBrowserFullscreen"
      @intent="onPanelIntent"
      @pick="onElementPick"
      @add-tab="addHubTab"
      @rename-tab="renameHubTab"
      @navigate-tab="navigateHubTab"
      @browser-state="reconcileBrowserState"
      @browser-recover="recoverBrowser"
      @browser-reopen="reopenBrowserTab"
      @open-html-browser="openHTMLPreviewBrowser"
      @open-website-browser="openWebsiteBrowser"
      @open-current-html-browser="openCurrentHTMLPreviewBrowser"
      @claim-html-capability="claimHTMLBrowserCapability"
    >
      <template #tasks>
        <TaskCenterDialog
          v-if="canUse('schedules') && studioAgentId"
          v-model="scheduleOpen"
          :agent-id="studioAgentId"
          :agent-name="studioAgent?.name"
          :agents="studioAgent ? [studioAgent] : []"
          :session-id="current"
          :models="pickerModels"
          :is-admin="isAdmin"
          :initial-draft="scheduleDraft"
          :draft-busy="preparingScheduleDraft"
          @attention="scheduleAttention = $event"
          @open-task="openTaskSheet"
          @open-sentinel="openSentinelSession"
          @request-draft="scheduleCurrentDraft"
        />
      </template>
    </DockArea>

    <WorkspacePicker v-if="canUse('workspaces') && pickerOpen" :cwd="cwd" @create="onCreateWorkspace" @close="pickerOpen = false" />
    <AnswerFilePicker :choice="answerFileChoice" @choose="answerFileNavigation.choose" @close="answerFileNavigation.cancel" />
    <!-- The import "dialog" is a bare file picker; importInProject remembers
         which project's hover button opened it. -->
    <input
      v-if="!isDelegatedEmbed"
      ref="importInput"
      type="file"
      accept=".md,.markdown,.json"
      class="sr-only"
      tabindex="-1"
      aria-hidden="true"
      @change="onImportFile"
    />

    <InformationEntryDialog v-if="informationSource" :model-value="true" :entry-id="informationSource.id" :version="informationSource.version" @update:model-value="value => { if (!value) informationSource = null; }" />
    <el-dialog :model-value="!!shortcutPreview" :title="shortcutPreview?.title" width="min(660px, calc(100vw - 32px))" append-to-body @update:model-value="updateShortcutPreview">
      <ShortcutReference v-if="shortcutPreview" :title="shortcutPreview.title" :content="shortcutPreview.content" :icon="shortcutPreview.icon" :language="shortcutPreview.language" :parts="shortcutPreview.parts" details-only />
    </el-dialog>
    <StarterCardsDialog
      :agent-id="studioAgentId || undefined"
      fullscreen
      ref="starterCardsDialog"
      v-model="starterDialogOpen"
      :cards="starterCards"
      :set="starters"
      :read-only="!isAdmin"
      :on-save="isStudio && isAdmin ? saveStudioStarters : undefined"
      :prepare-draft="prepareStarterDraft"
      :max-cards="isStudio ? 0 : undefined"
      :panels="isStudio ? undefined : panels"
      @pick="onShortcutPick"
      @saved="loadStarters"
      @closed="onStarterDialogClosed"
    />

    <!-- Rewind: conversation fork, optionally with the workspace restored to
         the checkpoint taken when that message was sent. The file option
         only exists when restoring would change something — the Claude Code
         rule that an empty diff gets no code choice. The dialog shows the
         message it targets and, per file, the line counts the restore itself
         would add and remove — the rewind's impact, never the content. -->
    <!-- The shared confirm-dialog design: icon + title + consequence, the
         object in a quote block, cancel ghost + solid destructive CTA. -->
    <el-dialog
      v-model="deleteOpen"
      :title="t('agentView.deleteDialog.title')"
      width="440px"
      class="confirm-dialog"
      :show-close="false"
      :close-on-click-modal="!deleteBusy"
    >
      <div class="rewind-body">
        <div class="rw-head">
          <span class="rw-icon"><Icon :icon="sessionIcon" /></span>
          <div class="rw-headtext">
            <h3>{{ t("agentView.deleteDialog.title") }}</h3>
            <p>{{ t("agentView.deleteDialog.desc") }}</p>
          </div>
          <button class="rw-x" :title="t('common.cancel')" :disabled="deleteBusy" @click="deleteOpen = false">
            <el-icon><Close /></el-icon>
          </button>
        </div>

        <div v-if="deleteTarget" class="rw-quote" :title="deleteTarget.title">
          {{ deleteTarget.title || t("agentView.deleteDialog.untitled") }}
        </div>
      </div>
      <template #footer>
        <div class="rw-foot">
          <el-button :disabled="deleteBusy" @click="deleteOpen = false">{{ t("common.cancel") }}</el-button>
          <el-button type="danger" :loading="deleteBusy" @click="confirmDelete">{{ t("common.delete") }}</el-button>
        </div>
      </template>
    </el-dialog>

    <!-- A composer chip's quick look, rendered from the chip's own bytes:
         nothing has been uploaded yet, so the preview is local by
         construction. Renders in place (not teleported), so the scoped
         :deep() styles reach its chrome; the v-if unmounts the body with the
         dialog. -->
    <el-dialog
      v-model="chipPreviewOpen"
      width="880px"
      class="chip-preview-dialog"
      :show-close="false"
      :title="chipPreview?.name ?? ''"
    >
      <div v-if="chipPreview" class="chip-preview">
        <div class="chip-preview-bar">
          <span class="chip-preview-name" :title="chipPreview.name">{{ chipPreview.name }}</span>
          <button class="chip-preview-x" :title="t('common.close')" :aria-label="t('common.close')" @click="closeChipPreview">
            <el-icon><Close /></el-icon>
          </button>
        </div>
        <div class="chip-preview-body">
          <div v-if="chipPreview.ext !== 'pdf' && chipPreview.url" class="chip-preview-image">
            <img :src="chipPreview.url" :alt="chipPreview.name" />
          </div>
          <iframe v-else-if="chipPreview.url" class="chip-preview-frame" :src="chipPreview.url" :title="chipPreview.name" />
          <VueOfficeExcel v-else-if="chipPreview.bytes" :src="chipPreview.bytes" class="chip-preview-sheet" />
          <CodeBlock
            v-else-if="chipPreview.text !== undefined"
            :code="chipPreview.text"
            :lang="chipPreview.ext"
            line-numbers
            :collapsed-height="520"
            :expanded-height="100000"
          />
          <div v-else class="chip-preview-note">{{ t("agentView.attach.previewUnsupported") }}</div>
        </div>
      </div>
    </el-dialog>

    <el-dialog :model-value="!!officePreview" :title="officePreview?.title" width="min(620px, calc(100vw - 32px))" append-to-body @update:model-value="(visible: boolean) => { if (!visible) officePreview = null; }">
      <OfficeReference v-if="officePreview" :office="officePreview" details-only />
    </el-dialog>

    <el-dialog
      v-model="rewindOpen"
      :title="t('agentView.rewind.title')"
      width="520px"
      class="confirm-dialog"
      :show-close="false"
      :close-on-click-modal="!rewindBusy"
    >
      <div class="rewind-body">
        <div class="rw-head">
          <span class="rw-icon"><el-icon><RefreshLeft /></el-icon></span>
          <div class="rw-headtext">
            <h3>{{ t("agentView.rewind.title") }}</h3>
            <p>{{ t("agentView.rewind.desc") }}</p>
          </div>
          <button class="rw-x" :title="t('common.cancel')" :disabled="rewindBusy" @click="rewindOpen = false">
            <el-icon><Close /></el-icon>
          </button>
        </div>

        <div v-if="rewindTarget" class="rw-quote" :title="rewindQuestion">
          <Icon class="qicon" :icon="messageIcon" />
          <span class="qtext">{{ rewindQuestion }}</span>
        </div>

        <template v-if="rewindChanges.length">
          <button class="rw-opt" :class="{ on: rewindMode === 'both' }" @click="rewindMode = 'both'">
            <span class="rw-radio" />
            <span class="rw-opttext">
              <span class="rw-title">{{ t("agentView.rewind.bothTitle") }}</span>
              <span class="rw-desc">{{ t("agentView.rewind.bothDesc") }}</span>
            </span>
          </button>

          <div v-if="rewindMode !== 'chat'" class="rw-files">
            <div class="rw-files-head">
              {{ t("agentView.rewind.filesHead", { n: rewindSelected.length }) }}
              <span class="rw-hint">{{ t("agentView.rewind.pickHint") }}</span>
            </div>
            <div class="rw-files-list">
              <!-- Each row is a toggle, so a restore can be narrowed to one file
                   without leaving this dialog. Exclusions are tracked, not
                   inclusions: everything the preview lists starts selected, which
                   is the answer that matches the button's label. -->
              <button
                v-for="c in rewindChanges.slice(0, 30)"
                :key="c.path"
                type="button"
                class="rw-file"
                :class="{ off: rewindExcluded.has(c.path) }"
                :title="t('agentView.rewind.pickTitle')"
                @click="rewindToggle(c.path)"
              >
                <span class="pick" :class="{ on: !rewindExcluded.has(c.path) }" aria-hidden="true" />
                <Icon class="ficon" :icon="fileIcon(baseName(c.path))" />
                <span class="st" :data-s="c.status">{{ rewindStatusLabel(c.status) }}</span>
                <span class="p" :title="c.path">{{ c.path }}</span>
                <span class="stat">
                  <template v-if="c.added >= 0 && c.removed >= 0">
                    <span class="plus">+{{ c.added }}</span>
                    <span class="minus">-{{ c.removed }}</span>
                  </template>
                  <span v-else class="bin">{{ t("agentView.rewind.binary") }}</span>
                </span>
              </button>
              <div v-if="rewindChanges.length > 30" class="rw-more">
                {{ t("agentView.rewind.moreFiles", { n: rewindChanges.length - 30 }) }}
              </div>
            </div>
            <p class="rw-warn">{{ t("agentView.rewind.warn") }}</p>
          </div>

          <!-- The third mode. Not a weaker "both": the case it serves is a turn
               whose reasoning was worth keeping and whose edit was not. Claude
               Code's rewind menu and Roo's restore dialog both offer it. -->
          <button class="rw-opt" :class="{ on: rewindMode === 'files' }" @click="rewindMode = 'files'">
            <span class="rw-radio" />
            <span class="rw-opttext">
              <span class="rw-title">{{ t("agentView.rewind.filesTitle") }}</span>
              <span class="rw-desc">{{ t("agentView.rewind.filesDesc") }}</span>
            </span>
          </button>

          <button class="rw-opt" :class="{ on: rewindMode === 'chat' }" @click="rewindMode = 'chat'">
            <span class="rw-radio" />
            <span class="rw-opttext">
              <span class="rw-title">{{ t("agentView.rewind.chatTitle") }}</span>
              <span class="rw-desc">{{ t("agentView.rewind.chatDesc") }}</span>
            </span>
          </button>
        </template>
        <!-- The preview is still in flight or found nothing: say which. -->
        <p v-else-if="!rewindPreview" class="rw-warn">{{ t("agentView.rewind.checking") }}</p>
        <p v-else class="rw-warn">{{ t("agentView.rewind.noChanges") }}</p>
      </div>
      <template #footer>
        <div class="rw-foot">
          <el-button :disabled="rewindBusy" @click="rewindOpen = false">{{ t("common.cancel") }}</el-button>
          <el-button type="danger" :loading="rewindBusy" @click="confirmRewind">{{ t("agentView.rewind.confirm") }}</el-button>
        </div>
      </template>
    </el-dialog>

    <!-- Past the grace window with no event stream, the whole page gives way
         to the browser-style cannot-connect screen; the background backoff
         loop still closes it by itself the moment a reconnect lands. -->
    <Unreachable
      v-if="stream.unreachable.value || (isDelegatedEmbed && gated)"
      :outage="isDelegatedEmbed && gated ? { attempts: 1, gaveUp: true, message: delegatedUiError || t('uiCoverage.app.parentSignInExpired') } : stream.outage.value"
      @retry="isDelegatedEmbed && gated ? loadDelegatedPage() : stream.retryNow()"
    />

    <!-- No usable token: the API answers 401 by design, so the page gives way
         to the token gate until one is entered (see client.ts setToken). -->
    <TokenGate v-if="gated && !isDelegatedEmbed" :rejected="tokenRejected && (!isStudio || !!token)" :password="authPassword" :published="isStudio && !managedChat" />
  </div>
</template>

<style scoped lang="scss">
/* The standard visually-hidden-but-announced clip, for the status region. */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
  border: 0;
}

.shell {
  position: relative;
  display: flex;
  height: 100dvh;
  overflow: hidden;
  background: var(--el-bg-color-page);

  /* While the hub is maximized it owns the whole post-sidebar area and the
     conversation underneath is fully covered — but a zero-width flex child
     still paints its positioned pieces (sticky bar, notices) through the
     sheet, so it leaves the layout entirely until the dock shrinks back. */
  &.hub-max .main {
    display: none;
  }
}

.sidebar {
  width: 250px;
  flex: 0 0 250px;
  border-right: 1px solid var(--el-border-color-lighter);
  /* Both states are absolute layers inside. No overflow: hidden here: while
     collapsing, the full layer keeps its 250px box and slides out to the
     left of the 56px-wide aside (collapseSidebar) — clipping would cut that
     ghost off. In the steady collapsed state the full layer is opacity: 0 +
     pointer-events: none, so nothing leaks visually or interactively. */
  position: relative;
  /* Paper, one step below the conversation's white. That surface step is what
     separates chrome from content here; the border beside it only has to be
     strong enough to hold the edge while the sidebar is sliding. */
  background: var(--el-bg-color-page);
  /* No width transition here, on purpose: width/flex-basis are layout
     properties, so animating them re-lays out the whole page every frame —
     and the unvirtualized conversation re-wraps with it while the rail's
     ResizeObserver forces a synchronous layout read on top. That was the
     visible stutter on expand. The slide in both directions is a
     transform-only WAAPI animation (expandSidebar/collapseSidebar); the
     layout itself flips instantly. */

  &.collapsed {
    width: 56px;
    flex-basis: 56px;
  }
}

/* The two sidebar states are both mounted as absolute layers and crossfade
   (same 0.3s as the slide); the inactive one is transparent and inert. */
.sidebar-full,
.rail-icons {
  position: absolute;
  inset: 0;
  transition: opacity 0.3s ease-in-out;
}

.sidebar-full {
  display: flex;
  flex-direction: column;
  /* Fixed expanded width, never squashed by the collapsed 56px aside: on
     collapse this layer is the wide sidebar's ghost that slides off to the
     left. The border rides along, so the separator slides away with the
     sidebar instead of teleporting from 250px to 56px on the click frame.
     (Expanded it overlaps the aside's own border exactly — same 1px line.) */
  width: 250px;
  border-right: 1px solid var(--el-border-color-lighter);
}

.sidebar:not(.collapsed) .rail-icons,
.sidebar.collapsed .sidebar-full {
  opacity: 0;
  pointer-events: none;
}

/* Collapsed icon rail (Gemini pattern): expand + new session, nothing else. */
.rail-icons {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 12px 0;
}

.rail-logo {
  margin-bottom: 6px;
}

.rail-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition: background 0.15s;
  position: relative;

  .el-icon {
    font-size: 18px;
  }

  &:hover {
    background: var(--el-fill-color);
  }
}

/* The schedule clock's attention dot: any job with consecutive failures
   lights it until the failures clear. Shared by the footer and the rail. */
/* Two controls on one row, the skin first: it is the one people go looking for,
   and the language is set once. Both shrink rather than wrap — the sidebar is
   250px and a wrapped preferences block reads as a second section. */
.side-foot {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 8px;
  border-top: 1px solid var(--el-border-color-lighter);
  min-width: 0;

  // The gear is pushed to the row's right edge rather than merely ordered
  // last, so the everyday cluster (skin, language) stays visually grouped.
  // (The clock moved to the dock's rail, under the skills button.)
  > * {
    min-width: 0;
  }
}

.lang-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 6px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-regular);
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s;
  overflow: hidden;
  position: relative;

  .el-icon {
    font-size: 14px;
    flex: 0 0 auto;
  }

  span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.brand {
  display: flex;
  align-items: center;
  padding: 14px 12px 10px;
  gap: 8px;

  strong {
    font-size: 15px;
    font-weight: 650;
    letter-spacing: -0.1px;
  }
}

.new {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  margin-left: auto;
  /* The theme's own solid ink, not a white pill: the button reads as the
     primary action it is, under every skin. */
  border: 1px solid var(--pg-solid);
  background: var(--pg-solid);
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  padding: 4px 10px;
  color: var(--pg-on-solid);
  cursor: pointer;
  box-shadow: var(--el-box-shadow-lighter);
  transition:
    border-color var(--pg-transition),
    color var(--pg-transition),
    background var(--pg-transition);

  .el-icon {
    font-size: 12px;
  }

  &:hover {
    border-color: var(--pg-solid-hover);
    background: var(--pg-solid-hover);
    color: var(--pg-on-solid);
  }
}

/* The unbranded Studio page grows the button into the mark's place: same
   pill, full row, the label centered — the one affordance in that corner. */
.brand.unbranded .new.wide {
  flex: 1;
  justify-content: center;
  padding: 6px 12px;
  font-size: 12px;
}

/* Revealed on sidebar hover (or keyboard focus), per the Gemini pattern. */
.collapse {
  display: inline-flex;
  align-items: center;
  border: 0;
  background: transparent;
  color: var(--el-text-color-secondary);
  border-radius: 4px;
  padding: 3px;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .sidebar:hover &,
  &:focus-visible {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

/* The session search box: one quiet row between the brand and the projects.
   A plain input rather than a component — it has no state worth keeping
   beyond the query string, and the native search affordances (Esc clears,
   webkit's ×) fight our own clear button, so the webkit one is suppressed. */
.sess-search {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 12px 6px;
  padding: 0 8px;
  height: 28px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
  transition: border-color var(--pg-transition), background var(--pg-transition);

  &:focus-within {
    border-color: var(--pg-accent-line);
    background: var(--el-bg-color);
  }

  .s-icon {
    font-size: 13px;
    flex: none;
  }

  .s-input {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: none;
    background: transparent;
    font-size: 12px;
    color: var(--el-text-color-primary);

    &::-webkit-search-cancel-button {
      display: none;
    }
  }

  .s-clear {
    display: inline-flex;
    align-items: center;
    border: 0;
    background: transparent;
    color: var(--el-text-color-secondary);
    padding: 2px;
    border-radius: 4px;
    cursor: pointer;
    font-size: 12px;

    &:hover {
      color: var(--el-text-color-primary);
      background: var(--el-fill-color);
    }
  }
}

/* The search's two non-answer states, and the project tag on a hit row — a
   search crosses projects, so which workspace a hit lives in is the one
   piece of context its row owes the reader. */
.s-empty {
  padding: 18px 16px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: center;
}

.s-proj {
  max-width: 40%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The matched-text blocks under a search hit: one quiet quoted line per
   cluster of matches, keywords picked out with the accent tint rather than a
   shouty yellow. Each block is a button — the click jumps into the session at
   the message that said it. */
.s-blocks {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 3px;
}

.s-block {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
  padding: 1px 6px;
  border: 0;
  border-left: 2px solid var(--el-border-color);
  border-radius: 4px;
  background: transparent;
  font: inherit;
  font-size: 11px;
  line-height: 1.5;
  text-align: left;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-regular);
  }
}

.sessions mark {
  background: color-mix(in srgb, var(--el-color-primary) 28%, transparent);
  color: inherit;
  border-radius: 2px;
  padding: 0 1px;
}

/* Lives in the topbar now: left-aligned and bold, ellipsis when the path
   outruns the bar, full path in the tooltip. */
/* Lives in the topbar: a quiet monospace pill, ellipsis when the path outruns the
   bar, full path in the tooltip. The tile is what keeps it from reading as a
   heading — it is a fact about the process, not a title. */
.cwd {
  min-width: 0;
  max-width: 60%;
  padding: 2px 9px;
  border-radius: 999px;
  background: var(--el-fill-color-lighter);
  font: 500 11px var(--pg-mono);
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── Agent Studio context ─────────────────────────────────────────────────
   The topbar's left side at /a/{id}: the way back to Agent Studio and the
   bound Agent's name in its own color. "/" never renders these. */
.studio-context {
  display: flex;
  align-items: center;
  gap: 14px;
  min-width: 0;
}

.studio-model-sync-note { font-size: 12px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }
.studio-model-retry {
  border: 0;
  border-radius: 6px;
  padding: 6px 8px;
  background: var(--el-fill-color-light);
  color: var(--el-color-danger);
  font: inherit;
  font-size: 12px;
  cursor: pointer;

  &:focus-visible { outline: 2px solid var(--el-color-primary); }
}

.studio-back {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 11px;
  border: 1px solid var(--el-border-color);
  border-radius: 999px;
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 11px;
  cursor: pointer;
  flex: none;

  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }
}

.studio-agent {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  min-height: 19px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.studio-agent-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}

/* Agent failures replace the workbench; ordinary loading never reaches this gate. */
.studio-gate {
  height: 100dvh;
  display: grid;
  place-items: center;
  background: var(--el-bg-color-page, #f7f8fc);
  padding: 24px;
  box-sizing: border-box;
}

.studio-gate-card {
  width: min(430px, 100%);
  text-align: center;

  h1 {
    font-size: 21px;
    margin: 14px 0 10px;
    color: var(--el-text-color-primary);
  }

  p {
    font-size: 13px;
    line-height: 1.9;
    color: var(--el-text-color-secondary);
    margin: 0 0 8px;
  }
}

.studio-gate-eyebrow {
  font-size: 10px;
  font-weight: 650;
  letter-spacing: 2.5px;
  color: var(--el-text-color-secondary);
}

.studio-gate-actions {
  display: flex;
  gap: 12px;
  justify-content: center;
  margin-top: 22px;
}

.studio-gate-btn {
  border: 1px solid var(--el-border-color);
  border-radius: 9px;
  padding: 9px 16px;
  font: inherit;
  font-size: 12px;
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  cursor: pointer;

  &.primary {
    background: var(--el-color-primary);
    border-color: var(--el-color-primary);
    color: #fff;
  }
}

/* Rows are pills inset from the sidebar's edge, rather than full-bleed bands with
   an accent stripe on the active one. Two reasons: a stripe against the panel
   border draws a second vertical line 2px from the first, and a full-bleed
   highlight makes the active session look like a section header rather than a
   selected item. */
/* The projects column scrolls; the recents strip below it does not — two
   independent regions in one column, the sidebar pattern from the research. */
.sessions-scroll {
  flex: 1 1 0;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 0 8px;
}


.pgroup {
  margin-bottom: 2px;
}

.pgroup-head {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 0 4px 0 8px;
}

.pgroup-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: 1 1 0;
  min-width: 0;
  border: 0;
  background: transparent;
  padding: 5px 4px 5px 0;
  font: inherit;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-regular);
  cursor: pointer;
  border-radius: 6px;

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.pchev {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  transition: transform 0.18s cubic-bezier(0.2, 0, 0, 1);

  &.open {
    transform: rotate(90deg);
  }
}

.picon {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.pgroup-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pgroup-count {
  flex: 0 0 auto;
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  font-size: 10px;
  line-height: 16px;
  text-align: center;
}

.pgroup-new {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  cursor: pointer;
  opacity: 0;

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-color-primary);
  }
}

.pgroup-head:hover .pgroup-new {
  opacity: 1;
}

/* The recents strip: pinned to the sidebar's bottom, its own scroll never —
   five rows is the cap, the projects column carries the scrolling. */
.recent {
  flex: 0 0 auto;
  /* A grey card set into the sidebar's paper. fill-color is the theme's own
     ink-on-canvas step (~9%), so every skin greys the card from its own
     palette — a true grey in light skins, a lifted panel in dark ones —
     instead of a hard-coded hex that only suits one. Radius and margin lift
     it off the footer as its own block. */
  margin: 4px 8px 8px;
  padding: 6px 8px 8px;
  border-radius: 10px;
  background: var(--el-fill-color);
}

.recent-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 2px 6px 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.recent-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

/* The folded strip's count: the same pill the project headers use (see
   pgroup-count), so "a number in a soft capsule" reads as one idiom
   everywhere in the sidebar. */
.recent-count {
  min-width: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  font-size: 10px;
  line-height: 15px;
  text-align: center;
}

/* Height-capped, not count-capped: about five rows show, the rest scroll
   inside. Fewer rows than the cap and the box shrinks to fit — an empty half
   strip would be padding pretending to be information. The global thin
   scrollbar (styles.scss) appears on hover only, so the strip stays quiet
   until it is touched. */
.recent-list {
  max-height: 208px;
  overflow-y: auto;
}

/* Same hover-reveal grammar as the project header's "+": the fold switch
   shows itself under the cursor, and stays shown while folded so the way
   back is visible. */
.recent-fold {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  cursor: pointer;
  opacity: 0;

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-color-primary);
  }
}

.recent:hover .recent-fold,
.recent-fold.folded {
  opacity: 1;
}

.recent-fold .rchev {
  transition: transform 0.15s ease;

  &.folded {
    transform: rotate(180deg);
  }
}

.recent-row {
  /* Two lines: the session on top, the project it belongs to under it — the
     strip crosses projects and the row has to say which one it came from. */
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  row-gap: 1px;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 4px 6px;
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-regular);
  text-align: left;
  cursor: pointer;
  border-radius: 6px;

  &:hover {
    /* One fill step above the card's own fill, so a hovered row still reads
       against the grey band. */
    background: var(--el-fill-color-dark);
  }

  &.active {
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary-dark-2);
  }
}

.recent-title {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.recent-time {
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.recent-proj {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  font-size: 10px;
  color: var(--el-text-color-secondary);

  .el-icon {
    flex: 0 0 auto;
    font-size: 10px;
  }

  .recent-proj-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.sessions {
  list-style: none;
  margin: 0;
  padding: 0 8px 4px 22px;

  li {
    position: relative;
    padding: 7px 10px;
    margin-bottom: 1px;
    border-radius: 9px;
    cursor: pointer;
    transition: background var(--pg-transition);

    &:hover {
      background: var(--el-fill-color-light);
    }

    &.active {
      background: var(--el-bg-color);
      box-shadow:
        var(--el-box-shadow-lighter),
        inset 0 0 0 1px var(--el-border-color-lighter);

      .title {
        color: var(--el-text-color-primary);
        font-weight: 600;
      }
    }
  }
}

.title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  line-height: 1.45;
  color: var(--el-text-color-regular);
}

// The pulse dot is fixed; the text does the shrinking. min-width is what lets
// a flex child ellipsize instead of pushing the row wider.
.title-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* A working row says so in two channels: the sonar dot (shape + motion), and
   a slow sheen crossing the title — the same "something is alive here" cue
   the live work header carries, quiet enough to sit inside a list. */
.sessions li.working .title-text,
.recent-row.working .title-text {
  background: linear-gradient(
    100deg,
    var(--el-text-color-regular) 30%,
    var(--el-color-primary) 50%,
    var(--el-text-color-regular) 70%
  );
  background-size: 220% 100%;
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  animation: title-sheen 2.6s linear infinite;
}

@keyframes title-sheen {
  from {
    background-position: 180% 0;
  }

  to {
    background-position: -120% 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .sessions li.working .title-text,
  .recent-row.working .title-text {
    animation: none;
    background: none;
    color: var(--el-color-primary);
  }
}

.sub {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-top: 3px;
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.pinned-tag {
  padding: 0 5px;
  border-radius: 3px;
  background: color-mix(in srgb, var(--el-color-primary) 12%, transparent);
  color: var(--el-color-primary);
}

.ws-tag {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 0 5px;
  border-radius: 3px;
  background: var(--el-fill-color);
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;

  .el-icon {
    font-size: 10px;
    flex: 0 0 auto;
  }
}

/* The ⋮ menu anchor: hover-revealed like Gemini's, held visible while its
   menu is open (menuOpenFor in the script). */
.sess-menu {
  position: absolute;
  right: 6px;
  top: 50%;
  transform: translateY(-50%);
}

.menu-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .el-icon {
    font-size: 14px;
    transform: rotate(90deg);
  }

  .sessions li:hover &,
  &.open,
  &:focus-visible {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color-darker);
    color: var(--el-text-color-primary);
  }
}

.main {
  position: relative;
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

/* The topbar carries one fact, so it is a hairline rather than a bar: taller
   chrome here would be height taken from the conversation to say something that
   does not change. */
.topbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  background: var(--el-bg-color);
  font-size: 12px;
}

.notice {
  padding: 7px 20px;
  font-size: 12px;
  color: var(--el-color-warning);
  background: var(--el-color-warning-light-9);
  border-bottom: 1px solid color-mix(in srgb, var(--el-color-warning) 18%, transparent);

  &.bad {
    color: var(--el-color-danger);
    background: var(--el-color-danger-light-9);
    border-bottom-color: color-mix(in srgb, var(--el-color-danger) 18%, transparent);
  }
}

/* Inside the composer column, so it is inset like the card below it rather than a
   full-bleed band: it belongs to the decision being made here. */
.ctx-warn {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0 25px 8px;
  padding: 8px 12px;
  border-radius: 10px;
  font-size: 12px;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  border: 1px solid color-mix(in srgb, var(--el-color-danger) 20%, transparent);

  button {
    margin-left: auto;
    flex: none;
    border: 1px solid currentcolor;
    background: transparent;
    color: inherit;
    border-radius: 999px;
    font-size: 11px;
    padding: 3px 10px;
    cursor: pointer;
    transition: background var(--pg-transition);

    &:hover {
      background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
    }
  }
}

.chat {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--el-bg-color);
}

/* Everything in this column is capped at the same measure as the transcript and
   centred on it, so the answer and the box you reply in share one left edge. The
   +50px pairs with the column's own 25px padding below, which keeps the cards'
   *content* on the measure rather than their borders.
   The padding is the column's outer margin: children are width:100%, so an
   inset on them instead would overflow by its own width in the half-width
   browser layout — margin on the container, never on the children. */
.compose-col {
  display: flex;
  flex: 0 1 auto;
  flex-direction: column;
  align-items: center;
  min-width: 0;
  min-height: 0;
  max-height: 68%;
  box-sizing: border-box;
  padding: 0 25px;
  // The files chip rises 15px above the input card. Reserve its space rather
  // than letting it sit on the last queued message or its actions.
  gap: 16px;

  > * {
    width: 100%;
    max-width: calc(var(--pg-measure) + 50px);
    box-sizing: border-box;
  }
}

.compose-support {
  display: flex;
  flex: 0 1 auto;
  flex-direction: column;
  min-height: 0;
  max-height: min(34dvh, 300px);
  gap: 8px;
  padding: 2px;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  // A tiny floating chat can exhaust both child minima. This outer scroll is
  // the fallback that keeps every control reachable without overlapping input.
  &:empty { display: none; }
  > .todo-bar { flex: 0 0 auto; width: 100%; margin: 0; --todo-list-max-height: min(12dvh, 96px); }
  > .conversation-inbox { flex: 1 1 auto; width: 100%; margin: 0; }
}
.compose-col > .ctx-warn { flex: 0 0 auto; margin: 0; }
.compose-col > .input-card { flex: 0 0 auto; }

.conv-wrap {
  flex: 1;
  min-height: 0;
  position: relative;
  display: flex;
  flex-direction: column;
}

.conversation {
  flex: 1;
  overflow-y: auto;
  padding: 20px 28px 28px;
}

/* The jump-to-bottom pill: centered over the conversation's bottom edge —
   Codex's centered pill and ZCode's composer-docked round button both land on
   this spot. Visible exactly while auto-follow is disengaged; the surface is
   the composer's own grammar (solid card, hairline, soft shadow) so it reads
   as chrome, not as conversation content. */
.jump-bottom {
  position: absolute;
  left: 50%;
  bottom: 12px;
  transform: translateX(-50%);
  z-index: 20;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 6px 13px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 999px;
  background: var(--el-bg-color);
  box-shadow: var(--el-box-shadow-light);
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition:
    color var(--pg-transition),
    border-color var(--pg-transition);
  animation: jump-bottom-in 0.18s ease-out;

  .el-icon {
    font-size: 13px;
  }

  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }
}

@keyframes jump-bottom-in {
  from {
    opacity: 0;
    transform: translateX(-50%) translateY(6px);
  }

  to {
    opacity: 1;
    transform: translateX(-50%) translateY(0);
  }
}

@media (prefers-reduced-motion: reduce) {
  .jump-bottom {
    animation: none;
  }
}

/* Preserve the transcript's visual context during a session switch. Never
   animate layout or hide it completely while the next snapshot is pending. */
.conv-inner {
  /* A reading column, not a window-wide one: prose set across 1900px is
     unreadable — the eye loses the line on the way back — and this transcript is
     mostly prose. The measure is shared with the composer column, and the
     scroller around this keeps the full width so the rail stays on the window
     edge and the scrollbar cannot move as the layout changes. */
  max-width: var(--pg-measure);
  margin: 0 auto;
  animation: conversation-reveal 0.18s ease-out;
  transition:
    opacity 0.16s ease,
    filter 0.16s ease;

  &.switching {
    opacity: 0.42;
    filter: blur(1px);
  }
}
@keyframes conversation-reveal {
  from { opacity: 0.6; }
  to { opacity: 1; }
}
@media (prefers-reduced-motion: reduce) {
  .conv-inner {
    animation: none;
    transition: none;

    &.switching {
      filter: none;
    }
  }
}

.turn-rail {
  position: absolute;
  top: 12px;
  right: 4px;
  bottom: 12px;
  z-index: 5;
  /* Wide enough that the active dot (24px, centred on the track) stays fully
     inside the window edge instead of clipping against it. */
  width: 20px;
}

.rail-track {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 50%;
  width: 2px;
  overflow: hidden;
  background: var(--el-border-color-lighter);
  border-radius: 2px;
  transform: translateX(-50%);
}

.rail-fill {
  width: 100%;
  background: color-mix(in srgb, var(--el-color-primary) 45%, transparent);
  border-radius: 2px;
}

.rail-dot {
  position: absolute;
  left: 50%;
  width: 20px;
  height: 20px;
  padding: 0;
  cursor: pointer;
  background: var(--el-bg-color);
  border: none;
  border-radius: 999px;
  color: var(--el-text-color-secondary);
  transform: translate(-50%, -50%);
  transition: all 0.15s;
  display: flex;
  align-items: center;
  justify-content: center;

  /* The pad is the point: the glyphs are outline art (the bubble especially),
     and without a solid backdrop the rail's vertical line shows straight
     through their hollow interiors. The pad wears the page's own background,
     so it reads as a gap in the line, not a chip on it. */

  /* Enlarged hit area: the visual glyph is 15px, the clickable one is not. */
  &::before {
    position: absolute;
    inset: -5px;
    content: "";
  }

  .glyph {
    width: 15px;
    height: 15px;
    font-size: 15px;
  }

  &:hover {
    color: var(--el-color-primary);
    transform: translate(-50%, -50%) scale(1.15);
  }

  /* The located turn — the dot the fill has reached, and where a jump lands.
     Size plus a ring and a tinted chip: it must read from across the room,
     not just on close inspection. The chip is still an opaque pad, so the
     rail line stays masked behind it. */
  &.active {
    width: 24px;
    height: 24px;
    color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
    border: 1.5px solid var(--el-color-primary);

    .glyph {
      width: 17px;
      height: 17px;
      font-size: 17px;
    }

    /* The anchor shouldn't wobble under the pointer; the tip card is its
       hover feedback. */
    &:hover {
      transform: translate(-50%, -50%);
    }
  }
}

/* The merged run's count, riding the dot's top-right corner. */
.rail-badge {
  position: absolute;
  top: -4px;
  right: -7px;
  min-width: 13px;
  height: 13px;
  padding: 0 3px;
  border-radius: 8px;
  background: var(--el-color-primary);
  color: #fff;
  font-size: 9px;
  line-height: 13px;
  text-align: center;
  pointer-events: none;
}

/* The preview shares the message's Todo card. Popper keeps the whole panel
   inside the viewport; one scroll area lets long plans remain fully readable. */
:global(.rail-message-popover.el-popover.el-popper) {
  max-width: calc(100vw - 24px);
  padding: 12px;
  box-sizing: border-box;
  border-radius: 10px;
}

.rail-tip {
  display: block;
  max-height: min(480px, calc(100dvh - 56px));
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-primary);
  text-align: start;
  overflow-wrap: anywhere;
  word-break: break-word;
  white-space: normal;
}

/* The card's first line names what the dot is — the glyphs alone asked too
   much of shape memory once two kinds share a rail. */
.rail-tip-kind {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-bottom: 4px;
  padding-bottom: 4px;
  font-size: 10px;
  color: var(--el-text-color-secondary);
  border-bottom: 1px solid var(--el-border-color-extra-light);

  .tip-glyph {
    width: 11px;
    height: 11px;
    font-size: 11px;
    flex: 0 0 auto;
  }

  .rail-tip-count {
    margin-left: auto;
    padding-left: 10px;
    color: var(--el-color-primary);
    font-weight: 600;
  }
}

/* A merged run's members stack as rows; the card is a list, not one blob. */
.rail-tip-row {
  display: block;
  padding: 3px 0;

  & + .rail-tip-row {
    border-top: 1px dashed var(--el-border-color-extra-light);
  }
}

.rail-tip-question {
  display: block;
}

.rail-tip-time {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.empty {
  color: var(--el-text-color-secondary);
  font-size: 13.5px;
  line-height: 1.7;
  padding: 48px 0 24px;
  text-align: center;
}

.composer-shortcut-row { display:flex; flex-wrap:wrap; gap:6px; margin:0 0 10px; }
.composer-shortcut-chip { display:inline-flex; align-items:center; max-width:100%; }
.composer-shortcut-chip > button { border:0; background:none; color:var(--el-text-color-secondary); cursor:pointer; font-size:17px; padding:3px; }

.ask {
  margin: 24px 0 6px;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.ask-bubble {
  max-width: 76%;
  padding: 11px 16px;
  /* ChatGPT's user bubble: a quiet tile, not a coloured one. Warm rather than
     grey, so it sits on the same paper as the rest of the interface — a neutral
     grey tile on a warm page reads as a screenshot pasted in. */
  background: var(--el-fill-color-light);
  border: 1px solid var(--el-border-color-extra-light);
  color: var(--el-text-color-primary);
  border-radius: var(--pg-radius-bubble);
  border-bottom-right-radius: 6px;
  font-size: 14px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}

/* Sent attachments: thumbnails in a row above the words, click-to-enlarge via
   el-image's built-in viewer. 96px keeps a bubble with three screenshots
   conversation-shaped rather than gallery-shaped. */
.ask-images {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}

.ask-image {
  width: 96px;
  height: 96px;
  border-radius: 8px;
  border: 1px solid var(--el-border-color-extra-light);
  cursor: zoom-in;
}

/* A file attachment in a bubble: the type icon and the name, a quiet tile
   that opens the file as a preview tab in the dock's browser area. */
.ask-file {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px 4px 6px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-regular);
  cursor: pointer;

  &:hover {
    border-color: var(--el-border-color);
    background: var(--el-fill-color-light);
  }
}

.ask-file-icon {
  width: 18px;
  height: 18px;
  flex: none;
}

.ask-file-name {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ask-skill {
  width: 100%;
}

/* A harness nudge (soft-cap checkpoint, todo nudge): one quiet system line,
   icon + muted small text — it must not read as something anyone typed. */
.sys-notice {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: 4px 0;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  .nico {
    font-size: 11px;
    flex: 0 0 auto;
    position: relative;
    top: 1px;
  }

  .ntext {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.ask-meta {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-top: 4px;
  min-height: 20px;
}

/* The exchange's copy/share row: one pair per question, always visible once
   the answer has settled — hiding the share entry behind a hover is how a
   feature goes unfound, and unlike the ask bubble's edit, neither action
   edits anything. */
.ex-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  margin: 2px 0 6px;
}

.ex-actions .act {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-placeholder);
  cursor: pointer;
  font-size: 13px;
  transition:
    background var(--pg-transition),
    color var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.ask-time {
  /* Centred above the bubble inside the right-aligned column. */
  align-self: center;
  margin-bottom: 4px;
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}

/* Copy/edit icons cost no attention until the cursor is over the message.
   Rewind opts out with .stay — see the template note at its button. */
.ask-act {
  display: inline-flex;
  align-items: center;
  border: 0;
  background: transparent;
  padding: 3px;
  border-radius: 4px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .ask:hover &,
  &:focus-visible,
  &.stay {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  &:disabled {
    cursor: default;

    /* The hover-reveal still applies, only dimmer: a control that shows
       itself greyed exactly when it cannot act reads as "wait for the run".
       The always-visible rewind gets the same dimming without the hover, or
       its stillness mid-run would read as enabled-and-idle. */
    .ask:hover &,
    &:focus-visible,
    &.stay {
      opacity: 0.35;
    }

    &:hover {
      background: transparent;
      color: var(--el-text-color-secondary);
    }
  }
}

/* A pill rather than a bare line: this is the only thing on screen during the gap
   before the first token, and a floating grey sentence in the middle of the
   transcript reads as an answer that failed to render. */
.waiting {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin: 10px 0;
  padding: 5px 12px;
  border-radius: 999px;
  background: var(--el-fill-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* Three bouncing dots, after the YX agent-qa reference. */
.pend-dots {
  display: inline-flex;
  gap: 3px;
  align-items: center;

  i {
    width: 4px;
    height: 4px;
    background: currentcolor;
    border-radius: 50%;
    animation: pend-bounce 1s ease-in-out infinite;

    &:nth-child(2) {
      animation-delay: 0.15s;
    }

    &:nth-child(3) {
      animation-delay: 0.3s;
    }
  }
}

@keyframes pend-bounce {
  0%,
  100% {
    opacity: 0.5;
    transform: translateY(0);
  }

  50% {
    opacity: 1;
    transform: translateY(-3px);
  }
}

/* Picked elements: one chip per pick, living inside the composer card. The
   shape is the Material 3 input-chip reading — a compact rounded *rectangle*
   (8px on a ~24px body), not a pill: M3 itself dropped the stadium for input
   chips, and a pill this wide reads as a slender bar rather than a tag. The
   hover tooltip previews what the model will receive (identity + data/text). */
/* The floating session-files button: a compact chip hovering off the composer
   card's top-right corner. Upright rounded rectangle — the earlier sheared
   (parallelogram) tab traded legibility for style: a 15px stroke glyph inside
   a 26px skewed box read as a smudge, and every hover/active/pop frame had to
   carry the skew correction. Now: icon + label + inline count, the
   "Filters · 3" reading — the count lives inside the button as a pill, not
   floating beside it as a corner dot. */
.input-card {
  position: relative;

  // Keep the real composer's dimensions while its session/model are unknown.
  // Hiding its children also prevents default model labels flashing at entry.
  &.entry-loading > :not(.composer-entry) {
    visibility: hidden;
  }
  > .composer-entry {
    position: absolute;
    inset: 14px 12px 10px;
    pointer-events: none;
  }
}

.composer-file-actions {
  position: absolute;
  top: -15px;
  right: 14px;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: 7px;
  max-width: calc(100% - 28px);
}

@media (max-width: 600px) {
  .composer-file-actions {
    left: 8px;
    right: 8px;
    max-width: calc(100% - 16px);
    overflow-x: auto;
    overflow-y: hidden;
    white-space: nowrap;
    padding-bottom: 3px;
    scrollbar-width: thin;
    overscroll-behavior-x: contain;
  }
  .composer-file-actions > :deep(*),
  .composer-file-actions :deep(button) {
    flex-shrink: 0;
    white-space: nowrap;
  }
}

.attach-fab {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-bg-color);
  color: var(--el-text-color-secondary);
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
  transition: color var(--pg-transition), border-color var(--pg-transition),
    background-color var(--pg-transition), transform var(--pg-transition), box-shadow var(--pg-transition);

  > svg {
    width: 16px;
    height: 16px;
    flex: 0 0 auto;
  }

  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary-light-5);
    background: var(--el-color-primary-light-9);
    transform: translateY(-1px);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
  }

  &:active {
    transform: translateY(0);
  }
}

.attach-fab-label {
  flex: 0 0 auto;
}

/* The count is an inline pill — part of the button, not a sticker on it. The
   key on the span re-mounts it when the count changes, replaying the pop. */
.attach-fab-badge {
  min-width: 16px;
  height: 16px;
  padding: 0 5px;
  border-radius: 8px;
  background: var(--el-color-primary);
  color: #fff;
  font-size: 10px;
  font-weight: 600;
  line-height: 16px;
  text-align: center;
  animation: attach-badge-pop 0.32s cubic-bezier(0.34, 1.56, 0.64, 1);
}

@keyframes attach-badge-pop {
  0% {
    transform: scale(0.4);
  }
  60% {
    transform: scale(1.18);
  }
  100% {
    transform: scale(1);
  }
}

@media (prefers-reduced-motion: reduce) {
  .attach-fab,
  .attach-fab-badge {
    transition: none;
    animation: none;
  }
}

/* Attachments: same chip reading as the picks — a small thumbnail (images)
   or the file type's icon on a rectangle, the upload state as a spinner
   overlay, errors as a red ring. Sits in its own row above the picks because
   a picture and an element pick are different kinds of exhibit. */
.attach-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 8px;
  max-height: 64px;
  overflow-y: auto;
}

.attach-chip {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 240px;
  padding: 2px 4px 2px 2px;
  font-size: 12px;
  line-height: 20px;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;

  &.error {
    border-color: var(--el-color-danger-light-5);
    color: var(--el-color-danger);
  }

  /* Ready chips open the quick-look dialog: the affordance is a pointer plus
     a barely-there lift, like the session-files popover's rows. */
  &.ready {
    cursor: pointer;

    &:hover {
      background: var(--el-fill-color);
      border-color: var(--el-border-color);
    }
  }
}

.attach-thumb {
  width: 30px;
  height: 30px;
  object-fit: cover;
  border-radius: 6px;
  display: block;
}

/* The type icon a document chip carries instead of a thumbnail — the same
   vscode-icons set the file tree uses, so a docx reads the same here and
   there. */
.attach-kind-icon {
  width: 22px;
  height: 22px;
  margin: 4px;
  flex: none;
}

.attach-spin {
  position: absolute;
  top: 6px;
  left: 6px;
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: rgba(0, 0, 0, 0.45);
  color: #fff;
}

.attach-err-mark {
  color: var(--el-color-danger);
  display: inline-flex;
}

.attach-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The folder chip's file counter / upload progress. Quieter than the name —
   it is metadata, not the thing itself. */
.attach-count {
  flex: none;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color);
  border-radius: 8px;
  padding: 1px 6px;
}

.attach-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: 0;
  background: transparent;
  color: var(--el-text-color-secondary);
  border-radius: 50%;
  cursor: pointer;
  padding: 0;

  &:hover {
    background: var(--el-fill-color-dark);
    color: var(--el-text-color-primary);
  }
}

/* The attach door in the actions row: quiet like the other composer
   furniture, brighter on hover. */
.attach-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 0;
  background: transparent;
  color: var(--el-text-color-secondary);
  border-radius: 6px;
  cursor: pointer;
  font-size: 15px;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}


.composer-layout-toggle {
  display:inline-flex; align-items:center; gap:5px; min-height:32px; padding:4px 7px;
  border:1px solid transparent; border-radius:7px; background:transparent;
  color:var(--el-text-color-secondary); font:inherit; font-size:12px; cursor:pointer; white-space:nowrap;
  &:hover:not(:disabled) { background:var(--el-fill-color); color:var(--el-text-color-primary); }
  &.active { background:var(--pg-accent-wash); color:var(--el-color-primary); border-color:var(--el-border-color-light); }
  &:disabled { opacity:.5; cursor:default; }
  &:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
}

/* The attach menu: the policy menu's shell (6px pad, 12px radius) with the
   two doors as two large option rows — tinted icon squares carry the choice
   at a glance, and the hint line teaches the doors that skip the menu. */
:deep(.attach-menu-pop) {
  padding: 6px !important;
  border-radius: 12px !important;
}

.attach-menu-title {
  padding: 8px 12px 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.attach-option {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s;

  &:hover,
  &:focus-visible {
    background: var(--el-fill-color-light);
  }
}

.option-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  font-size: 16px;
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);

  /* The folder door reads as its own action, not a second file icon: the
     warning tint is the same semantic accent the policy menu gives its
     "careful" mode. */
  &.folder {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning);
  }
}

.option-text {
  flex: 1;
  min-width: 0;
}

.option-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--el-text-color-primary);
}

.option-desc {
  display: block;
  margin-top: 2px;
  font-size: 12px;
  line-height: 1.35;
  color: var(--el-text-color-secondary);
}

.attach-menu-hint {
  margin-top: 6px;
  padding: 8px 12px 4px;
  border-top: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
}

.element-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 8px;
  /* Two rows are the ceiling before the chip area scrolls on its own — the
     composer must not be pushed off-screen by its own attachments. */
  max-height: 64px;
  overflow-y: auto;
}

.echip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 220px;
  padding: 2px 4px 2px 8px;
  font-size: 12px;
  line-height: 20px;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  cursor: default;
  transition:
    background var(--pg-transition),
    border-color var(--pg-transition);

  /* States as tint layers (the M3 recipe: deepen the same surface, never swap
     it): hover one step, the pressed moment one more. */
  &:hover {
    background: var(--el-fill-color);
    border-color: var(--el-color-primary-light-5);
  }

  &:active {
    background: var(--el-fill-color-dark);
    border-color: var(--el-color-primary-light-3);
  }

  /* Focusable on purpose: Backspace/Delete on a focused chip removes it, and
     the ring is what says "this chip, not the composer, has the keys". */
  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }

  &.has-data {
    border-color: color-mix(in srgb, var(--el-color-success) 45%, var(--el-border-color-lighter));
  }
}

.echip-icon {
  flex: 0 0 auto;
  width: 12px;
  height: 12px;
  color: var(--el-text-color-secondary);
}

/* The data badge rides after the favicon: same slot, different colour, so the
   scan order stays "which page → does it carry numbers → which element". */
.echip-data {
  flex: 0 0 auto;
  color: var(--el-color-success);
}

/* The page identity leads: it is the question the chip exists to answer, and
   the element after the dot is the qualifier. */
.echip-page {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}

.echip-sep {
  color: var(--el-text-color-secondary);
}

.echip-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--el-font-family-mono, monospace);
}

.echip-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.composer-command-popover { position: fixed; z-index: 2100; min-width: 0; }
.composer-token-row { display: flex; margin: 2px 0 9px; min-width: 0; }
.composer-command-context { margin-bottom: 6px; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
.composer-token-status { display: flex; flex-wrap: wrap; align-items: center; gap: 5px 8px; margin: 6px 0 0; color: var(--el-text-color-secondary); font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }
.composer-token-status.invalid { color: var(--el-color-danger); }
.composer-token-status button { padding: 2px 6px; border: 1px solid var(--el-border-color); border-radius: 6px; background: var(--el-bg-color); color: var(--el-text-color-regular); font: inherit; cursor: pointer; }
.composer-token-search { padding: 10px 12px; border: 1px solid var(--el-border-color-lighter); border-bottom: 0; border-radius: 20px 20px 0 0; background: var(--el-bg-color-overlay); }
.composer-token-search input { width: 100%; box-sizing: border-box; border: 1px solid var(--el-border-color-light); border-radius: 8px; padding: 7px 9px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: inherit; font-size: 14px; }
.composer-token-search input::placeholder { color: var(--el-text-color-placeholder); }
.with-token-search :deep(.slash-command-menu) { border-top: 0; border-top-left-radius: 0; border-top-right-radius: 0; max-height: min(360px, 45dvh, calc(var(--composer-menu-height, 360px) - 60px)); }
.composer-token-empty { padding: 10px 12px; border: 1px solid var(--el-border-color-lighter); border-radius: 0 0 16px 16px; background: var(--el-bg-color-overlay); color: var(--el-text-color-secondary); font-size: 12px; }
@media (max-width: 480px) { .composer-token-search { border-radius: 16px 16px 0 0; } .composer-token-search input { font-size: 16px; } }

/* The composer card floats directly in the layout — no wrapping strip around
   it (that padded wrapper was the "parent rectangle"). The gap is the card's
   own margin; one focus ring around the whole card instead of one around
   just the textarea. */
.input-card {
  display: flex;
  flex-direction: column;
  margin: 0 0 16px;
  border: 1px solid var(--el-border-color-light);
  border-radius: var(--pg-radius-card);
  padding: 10px 12px 8px;
  background: var(--el-bg-color);
  /* Resting elevation: the composer is the one thing on the page that is always
     interactive, and a flat outline puts it on the same plane as the transcript
     it sits under. */
  box-shadow: var(--el-box-shadow-light);
  transition:
    border-color 0.2s,
    box-shadow 0.2s;

  &:focus-within {
    border-color: var(--pg-accent-line);
    box-shadow: var(--el-box-shadow-light), var(--pg-ring);
  }

  textarea {
    display: block;
    width: 100%;
    resize: none;
    border: 0;
    background: transparent;
    padding: 2px 0;
    font: 14px/1.65 inherit;
    color: var(--el-text-color-primary);
    outline: none;

    &::placeholder {
      color: var(--el-text-color-placeholder);
    }
  }
}

.input-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}

/* Transparent wrapper: the send/stop actions participate in the input bar's
   flex row directly; the floating chat re-boxes them (see its own rules). */
.actions {
  display: contents;
}

.spacer {
  flex: 1;
}

/* Send/stop buttons after the YX agent-qa reference: one shared box, the
   primary variant carries hover/active feedback and a real disabled look. */
.send-btn,
.stop {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 32px;
  padding: 0 15px;
  border-radius: 6px;
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s,
    transform 0.15s;

  &:disabled {
    cursor: not-allowed;
  }
}

.stop {
  color: var(--el-color-danger);
  border-color: color-mix(in srgb, var(--el-color-danger) 28%, var(--el-border-color));

  &:hover {
    background: var(--el-color-danger-light-9);
    border-color: color-mix(in srgb, var(--el-color-danger) 45%, transparent);
  }
}

.send-btn {
  /* The skin's solid: on a light skin that is its own ink, because black on paper
     is the strongest contrast available and the accent is spent on live state
     rather than on actions. On a dark skin an ink button would be invisible, so
     the solid is the accent there — see theme/build.ts. */
  color: var(--pg-on-solid);
  background: var(--pg-solid);
  border-color: var(--pg-solid);

  .el-icon {
    font-size: 15px;
  }

  /* Mid-run the action changes from "start a run" to "join this one", so the
     look changes with it: primary blue instead of black. Declared before
     :disabled — equal specificity, so the grey still wins when both apply. */
  &.steering {
    background: var(--el-color-primary);
    border-color: var(--el-color-primary);

    &:hover:not(:disabled) {
      background: var(--el-color-primary-light-3);
      border-color: var(--el-color-primary-light-3);
    }
  }

  &:hover:not(:disabled) {
    background: var(--pg-solid-hover);
    border-color: var(--pg-solid-hover);
  }

  &:active:not(:disabled) {
    transform: scale(0.92);
  }

  &:disabled {
    color: var(--el-text-color-placeholder);
    background: var(--el-fill-color-light);
    border-color: var(--el-border-color);
  }
}

/* The confirm dialogs render in place (not teleported), so scoped styles
   reach them; :deep() is for the el-dialog chrome itself. */
:deep(.confirm-dialog) {
  border-radius: 12px;

  .el-dialog__header {
    display: none; // the body carries its own head
  }

  .el-dialog__body {
    padding: 18px 20px 4px;
  }

  .el-dialog__footer {
    padding: 10px 20px 16px;
  }
}

/* The chip quick-look: the bar carries the file's name and close, the body
   the local rendering — image, PDF frame, spreadsheet, or code. */
:deep(.chip-preview-dialog) {
  border-radius: 12px;
  max-width: 94vw;

  .el-dialog__header {
    display: none;
  }

  .el-dialog__body {
    height: 74vh;
    padding: 0;
  }
}

.chip-preview {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.chip-preview-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.chip-preview-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: var(--el-text-color-primary);
}

.chip-preview-x {
  display: inline-flex;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }
}

.chip-preview-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 10px;
  display: flex;
  flex-direction: column;
}

.chip-preview-image {
  display: flex;
  justify-content: center;

  img {
    max-width: 100%;
    max-height: 100%;
    border-radius: 6px;
  }
}

.chip-preview-frame {
  flex: 1 1 0;
  border: 0;
  border-radius: 6px;
  min-width: 0;
}

.chip-preview-sheet {
  flex: 1 1 0;
  min-height: 0;
}

.chip-preview-note {
  padding: 24px 12px;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.rewind-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.rw-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.rw-icon {
  flex: 0 0 auto;
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 17px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.rw-headtext {
  flex: 1;
  min-width: 0;

  h3 {
    margin: 2px 0 4px;
    font-size: 15px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  p {
    margin: 0;
    font-size: 12px;
    line-height: 1.5;
    color: var(--el-text-color-secondary);
  }
}

.rw-x {
  flex: 0 0 auto;
  border: none;
  background: none;
  padding: 4px;
  border-radius: 5px;
  cursor: pointer;
  color: var(--el-text-color-secondary);

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }
}

/* The message this rewind targets: the dialog's object, not just its act.
   A quiet grey tile echoing the user's own chat bubble. */
.rw-quote {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  background: var(--el-fill-color);
  border-radius: 10px;
  padding: 8px 12px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-regular);

  .qicon {
    flex: 0 0 auto;
    margin-top: 2px;
    font-size: 14px;
    color: var(--el-text-color-secondary);
  }

  .qtext {
    min-width: 0;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    word-break: break-all;
  }
}

/* Options are cards, not bare radios: title + consequence per card. */
.rw-opt {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  text-align: left;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  padding: 10px 12px;
  cursor: pointer;
  transition: border-color 0.12s, background 0.12s;

  &:hover {
    border-color: var(--el-border-color-hover);
  }

  &.on {
    border-color: var(--el-color-danger);
    background: var(--el-color-danger-light-9);

    .rw-radio {
      border-color: var(--el-color-danger);

      &::after {
        content: "";
        position: absolute;
        inset: 3px;
        border-radius: 50%;
        background: var(--el-color-danger);
      }
    }
  }
}

.rw-radio {
  flex: 0 0 auto;
  position: relative;
  width: 14px;
  height: 14px;
  margin-top: 2px;
  border-radius: 50%;
  border: 1.5px solid var(--el-border-color);
  transition: border-color 0.12s;
}

.rw-opttext {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.rw-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.rw-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* The file impact nests under the option it belongs to, and its rows reuse
   the changes panel's anatomy: icon, badge, path, +N/-N. */
.rw-files {
  margin-left: 24px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  overflow: hidden;
}

.rw-files-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);

  .rw-hint {
    margin-left: auto;
    opacity: 0.75;
  }
}

.rw-files-list {
  max-height: 180px;
  overflow: auto;
  padding: 4px 0;
}

/* A row is a toggle now, so the button defaults have to go: full width, left
   aligned, no chrome — it has to keep reading as a list row. */
.rw-file {
  width: 100%;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 3px 10px;
  font-size: 12px;
  color: inherit;

  &:hover {
    background: var(--el-fill-color-light);
  }

  /* Excluded: still listed, because "which files did I leave out" is a question
     asked after the fact, and a row that vanished could not answer it. */
  &.off {
    opacity: 0.45;

    .p {
      text-decoration: line-through;
    }
  }

  .pick {
    flex: 0 0 auto;
    width: 12px;
    height: 12px;
    border: 1px solid var(--el-border-color);
    border-radius: 3px;
    background: var(--el-bg-color);

    &.on {
      border-color: var(--el-color-danger);
      background: var(--el-color-danger);
      /* The tick, drawn rather than glyphed so it cannot pick up a font that
         does not have it. */
      background-image: linear-gradient(
          to bottom right,
          transparent 44%,
          var(--el-bg-color) 44%,
          var(--el-bg-color) 56%,
          transparent 56%
        ),
        linear-gradient(to bottom left, transparent 62%, var(--el-bg-color) 62%, var(--el-bg-color) 74%, transparent 74%);
    }
  }

  .ficon {
    flex: 0 0 auto;
    font-size: 14px;
  }

  .st {
    flex: 0 0 auto;
    font-size: 11px;
    padding: 0 5px;
    border-radius: 4px;
    background: var(--el-fill-color);
    color: var(--el-text-color-secondary);
  }

  /* A = created afterwards, so the restore deletes it; D = deleted, so the
     restore brings it back. The colors follow the consequence, not the act. */
  .st[data-s="A"] {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }

  .st[data-s="D"] {
    background: var(--el-color-success-light-9);
    color: var(--el-color-success);
  }

  .p {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-text-color-regular);
  }

  .stat {
    flex: 0 0 auto;
    display: flex;
    gap: 6px;
    font-size: 11px;
    font-variant-numeric: tabular-nums;

    .plus {
      color: var(--el-color-success);
    }

    .minus {
      color: var(--el-color-danger);
    }

    .bin {
      color: var(--el-text-color-secondary);
    }
  }
}

.rw-more {
  padding: 2px 10px 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.rw-warn {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.rw-files .rw-warn {
  padding: 6px 10px 8px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.rw-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
/* Immersion changes only geometry: the browser, draft and live transcript keep
   their original DOM nodes and subscriptions. App dialogs remain above this. */
.shell[data-immersive-motion] :deep(.dock) {
  /* The compositor moves live surfaces. Their real layout reaches the final
     size once, so the remote viewport does not follow intermediate widths. */
  transition: none;
}
.shell.browser-immersive {
  position: fixed;
  inset: 0;
  z-index: 100;
  height: 100dvh;
  display: block;

  > .sidebar { display: none; }
}
.shell:is(.browser-immersive, .workflow-workspace) {
  > .main.floating-chat {
    container-type: inline-size;
    container-name: pogo-floating-chat;
    position: fixed;
    z-index: 120;
    display: flex;
    min-width: 0;
    min-height: 0;
    border: 1px solid color-mix(in srgb, var(--el-text-color-primary) 16%, transparent);
    border-radius: 20px;
    overflow: hidden;
    background: var(--el-bg-color);
    box-shadow: 0 20px 72px #0004, 0 3px 12px #0002;
    .conversation { padding: 14px 18px 10px; }
    .compose-col { flex: 0 1 auto; width: 100%; box-sizing: border-box; padding: 8px 12px 10px; max-height: calc(100% - 48px); overflow: visible; }
    .compose-support { max-height: min(28dvh, 240px); }
    .compose-support > .todo-bar { --todo-list-max-height: min(10dvh, 64px); }
    .compose-support:has(.conversation-inbox) > .todo-bar { --todo-list-max-height: 48px; }
    .input-card { margin: 0; width: 100%; box-sizing: border-box; border-radius: 14px; }
    .input-card textarea { min-height: 56px; max-height: 140px; }
    .notice { padding: 7px 16px; }
    .ctx-warn { margin: 0; }
    .input-bar > .spacer { display: none; }
    .actions { display: flex; align-items: center; justify-content: flex-end; gap: 6px; margin-left: auto; flex: 1 0 auto; }
    .input-bar { flex-wrap: wrap; }
    &.manipulating { user-select: none; }
  }
}
.immersive-chat-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 0 0 auto;
  min-height: 60px;
  padding: 0 13px 0 18px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  cursor: grab;
  touch-action: none;
  user-select: none;
  &:active { cursor: grabbing; }
  &:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: -3px; }
}
.immersive-chat-title {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  strong { font-size: 13px; font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  > span { font-size: 11px; color: var(--el-text-color-secondary); }
}
@container pogo-floating-chat (max-width: 400px) {
  .immersive-chat-head { flex-wrap: wrap; gap: 10px; padding: 12px 14px; }
  .immersive-chat-title { flex-basis: calc(100% - 24px); }
  .immersive-chat-actions { flex-basis: 100%; }
  .shell:is(.browser-immersive, .workflow-workspace) > .main.floating-chat .input-card textarea { height: 44px; min-height: 44px; }
  .shell:is(.browser-immersive, .workflow-workspace) > .main.floating-chat .input-bar { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: 5px; }
  .input-bar > .actions { grid-column: 1 / -1; width: 100%; }
  .input-bar :deep(.picker-btn) { min-width: 0; max-width: 100%; }
  .input-bar :deep(.picker-btn), .input-bar :deep(.meter-btn), .input-bar :deep(.policy-btn) { padding-left: 5px; padding-right: 5px; }
  .input-bar :deep(.picker-btn .name) { max-width: 90px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .input-bar :deep(.meter-btn .mini) { width: 28px; }
}
.immersive-status-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 50%;
  background: var(--el-text-color-placeholder);
  &.working { background: var(--el-color-primary); box-shadow: 0 0 0 4px color-mix(in srgb, var(--el-color-primary) 12%, transparent); }
  &.attention { background: var(--el-color-warning); box-shadow: 0 0 0 4px var(--el-color-warning-light-9); }
}
.immersive-chat-resize {
  position: absolute;
  bottom: 0;
  right: 0;
  width: 19px;
  height: 19px;
  display: grid;
  place-items: center;
  border: 0;
  background: transparent;
  color: var(--el-text-color-placeholder);
  cursor: nwse-resize;
  touch-action: none;
}
@media (max-width: 720px) {
  .shell:is(.browser-immersive, .workflow-workspace) > .main.floating-chat { border-radius: 18px; }
  /* A portrait viewer otherwise centers the landscape screenshot behind the
     floating sheet. Size the actual frame box, preserving the same contain
     geometry for remote clicks and annotations. */
  .shell.browser-immersive :deep(.native-page) { flex: 0 0 auto; min-height: 0; aspect-ratio: 8 / 5; width: 100%; }
  .immersive-chat-head { min-height: 48px; cursor: default; padding-left: 14px; }
  .immersive-chat-resize { display: none; }
}
</style>

<!-- Unscoped because the chip tooltip's popper is teleported to <body>, out of
     the scoped style's reach — same arrangement as the follow-up chip tips. -->
<style lang="scss">
/* The floating attachments popover teleports to body, so its styles live
   here, outside the scoped block — same rule as echip-tip below. */
.attach-pop-list {
  max-height: 300px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.attach-pop-head {
  margin: 0 0 6px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.attach-pop-count {
  min-width: 18px;
  padding: 1px 6px;
  border-radius: 9px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  font-size: 10px;
  font-weight: 600;
  line-height: 16px;
  text-align: center;
}

.attach-pop-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 18px 12px 14px;
  text-align: center;

  p {
    margin: 0;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }
}

.attach-pop-empty-icon {
  font-size: 34px;
  color: var(--el-text-color-placeholder);
  opacity: 0.7;
}

.attach-pop-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 6px;
  border: 0;
  border-radius: 8px;
  background: none;
  text-align: left;
  font-size: 12px;
  color: var(--el-text-color-regular);
  cursor: pointer;
  transition: background-color var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.attach-pop-thumb {
  width: 28px;
  height: 28px;
  object-fit: cover;
  border-radius: 6px;
  border: 1px solid var(--el-border-color-lighter);
  flex: none;
}

/* A non-image file sits its type icon on a quiet tinted tile the same size
   as the image thumb, so every row's leading edge aligns. */
.attach-pop-tile {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
  flex: none;
}

.attach-pop-kind {
  width: 18px;
  height: 18px;
}

.attach-pop-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Revealed on hover: the row opens a preview, the arrow says so. */
.attach-pop-go {
  flex: none;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  opacity: 0;
  transition: opacity var(--pg-transition);

  .attach-pop-row:hover & {
    opacity: 1;
  }
}

.echip-tip {
  max-width: 420px;
  white-space: pre-wrap;
  word-break: break-all;
  line-height: 1.5;
  font-family: var(--el-font-family-mono, monospace);
  font-size: 11px;
}

/* The published workbench must also fit a narrow iframe. Keep the same Pogo
   controls, give the prompts one readable column, and wrap composer actions. */
@media (max-width: 720px) {
  .agent-surface {
    .topbar { padding: 10px 14px; }
    .cwd { display: none; }
    .studio-context { gap: 8px; }
    .studio-back { padding: 4px 8px; }
    .conversation { padding: 18px 14px; }
    .compose-col { padding: 0 12px; }
    .input-bar { flex-wrap: wrap; gap: 7px; }
    .input-bar > .spacer { display: none; }
    .input-bar .actions { display: flex; flex: 1 0 100%; justify-content: flex-end; align-items: center; gap: 8px; }
    .input-bar .policy-btn, .send-btn, .stop { flex-shrink: 0; white-space: nowrap; }
    .input-bar .meter-btn { margin-left: auto; }
    .studio-starters .starters { padding: 30px 0 8px; gap: 18px; }
    .studio-starters .heading { font-size: 18px; line-height: 1.5; }
    .studio-starters .cards { grid-template-columns: 1fr; }
    .studio-starters .card { min-height: 48px; padding: 12px; }
    .sidebar:not(.collapsed) {
      position: absolute;
      inset: 0 auto 0 0;
      z-index: 70;
      height: 100dvh;
      box-shadow: 12px 0 36px color-mix(in srgb, var(--el-text-color-primary) 12%, transparent);
    }
  }
}
</style>
