<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import AnsiText from "./AnsiText.vue";
import AnswerText from "./AnswerText.vue";
import BashResult from "./tools/BashResult.vue";
import EditResult from "./tools/EditResult.vue";
import LsResult from "./tools/LsResult.vue";
import McpResult from "./tools/McpResult.vue";
import ReadManyResult from "./tools/ReadManyResult.vue";
import ReadResult from "./tools/ReadResult.vue";
import SearchResult from "./tools/SearchResult.vue";
import SkillResult from "./tools/SkillResult.vue";
import SubagentResult from "./tools/SubagentResult.vue";
import TodoResult from "./tools/TodoResult.vue";
import UserInputResult from "./tools/UserInputResult.vue";
import WriteResult from "./tools/WriteResult.vue";
import ShortcutResult from "./tools/ShortcutResult.vue";
import ShortcutMutationResult from "./tools/ShortcutMutationResult.vue";
import SkillSavedResult from "./tools/SkillSavedResult.vue";
import AgentWorkflowResult from "./tools/AgentWorkflowResult.vue";
import { isAgentWorkflowDetails } from "@/agent/agentWorkflowNavigation";
import { isSavedSkillDetails } from "@/agent/skillAuthoringReceipt";
import { isSavedShortcutDetails, isShortcutMutationDetails } from "@/agent/shortcutReceipt";
import { shortcutCallTool } from "@/agent/shortcutToolNames";
import CodeBlock from "./CodeBlock.vue";
import GateCard from "./GateCard.vue";
import ToolCallHeader from "./ToolCallHeader.vue";
import IncomingArgsView from "./tools/IncomingArgs.vue";
import { presentToolCall, toolCallState, toolControlOutcome } from "@/agent/toolPresentation";
import { useToolIdentityCatalog } from "@/agent/toolIdentity";
import { useCardDisclosureState } from "@/agent/cardDisclosure";
import type { IncomingArgs } from "@/api/types";
import { extractPath } from "@/agent/argsPreview";
import {
  formatDuration,
  isBashDetails,
  isEditDetails,
  isLsDetails,
  isMCPDetails,
  isReadDetails,
  isReadManyDetails,
  isSearchDetails,
  isSkillDetails,
  isSubagentDetails,
  isTodoDetails,
  isWriteDetails,
  matchSkillFile,
  matchSkillRead,
  type TimelineCall,
} from "@/agent/timeline";
import type { OpenFileTarget, OpenSheetRequest } from "./dockSheets";

const props = defineProps<{
  call: TimelineCall;
  runActive: boolean;
  skills?: { name: string; path: string }[];
  cwd?: string;
  /** See TurnCard's prop of the same name; passed straight through to TodoResult. */
  todoPinned?: boolean;
  /** headerless drops the name + args line: a containing row (the work log's
   * command row) already carries the command as its own label — one that
   * completes from clipped to full on open — so repeating it here would say
   * the same thing twice. The body (output, gate, error) is unaffected. */
  headerless?: boolean;
  serverOffset?: number | null;
  /** Opt-in so standalone tools, receipts and child transcripts keep their
   * established presentation until their containing view enables layering. */
  layered?: boolean;
  fullDetails?: boolean;
  reveal?: boolean;
  defaultExpanded?: boolean;
  /** The same call host survives argument generation and tool_start. */
  incoming?: IncomingArgs;
}>();

const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string];
  thaw: [string];
  "open-sheet": [OpenSheetRequest];
  "open-file": [OpenFileTarget];
}>();

const { t } = useI18n();
const catalog = useToolIdentityCatalog();
const disclosure = useCardDisclosureState();
const bodyId = `call-details-${useId()}`;
const callEl = ref<HTMLElement | null>(null);
const bodyEl = ref<HTMLElement | null>(null);
const localChoices = ref(new Map<string, boolean>());
const locallyRevealed = ref(false);
const revealDismissed = ref(false);
const disclosureKey = () => `call:${props.call.callId}:details`;
const choice = computed(() => disclosure?.available() ? disclosure.choice(disclosureKey()) : localChoices.value.get(props.call.callId));
const detailsOpen = computed(() => !props.layered || props.headerless || props.fullDetails || props.reveal && !revealDismissed.value || locallyRevealed.value || !!props.call.gate || props.call.inputInvalid === true || (choice.value ?? props.defaultExpanded ?? false));
const bodyMounted = ref(false);
watch(detailsOpen, open => { if (open) bodyMounted.value = true; }, { immediate: true, flush: "sync" });
watch(() => props.call.callId, () => { locallyRevealed.value = false; revealDismissed.value = false; bodyMounted.value = !!detailsOpen.value; });
watch(() => props.reveal, value => { revealDismissed.value = false; if (!value) locallyRevealed.value = false; });
function dismissReveal() { revealDismissed.value = true; locallyRevealed.value = false; }
function toggleDetails() {
  if (!props.layered || props.fullDetails || props.call.gate || props.call.inputInvalid) return;
  if (typeof window !== "undefined" && window.getSelection?.()?.toString()) return;
  const open = !detailsOpen.value;
  dismissReveal();
  if (disclosure?.available()) disclosure.set(disclosureKey(), open);
  else localChoices.value = new Map(localChoices.value).set(props.call.callId, open);
}
function revealDetails() { revealDismissed.value = false; locallyRevealed.value = true; }
function focusDisclosure() {
  const target = callEl.value?.querySelector?.<HTMLButtonElement>("button.tool-label") ?? callEl.value;
  target?.focus?.();
}
watch(() => props.call.gate, (gate, previous) => {
  if (previous && !gate && typeof document !== "undefined" && document.activeElement && bodyEl.value?.contains?.(document.activeElement)) focusDisclosure();
});

const inputInvalid = computed(() => props.call.inputInvalid === true);
// Some native tools encode failure in structured details (browser.ok or a
// command exit code), so disclosure and the shared header use one verdict.
// Invalid arguments and control receipts have their own non-execution UI.
const failed = computed(() => !inputInvalid.value && toolCallState(props.call) === "error" && !toolControlOutcome(props.call));
const rawArguments = computed(() => typeof props.call.args === "string" ? props.call.args : "");
const isSubagent = computed(() => props.call.name === "subagent");

const state = computed(() => toolCallState(props.call));
const now = ref(Date.now());
let clock: ReturnType<typeof setInterval> | undefined;
watch(() => [props.layered, props.call.running, props.call.startedAt, props.incoming] as const, ([layered, running, started, incoming]) => {
  if (clock) clearInterval(clock);
  clock = undefined;
  if (layered && running && started !== undefined && !incoming && typeof window !== "undefined") {
    now.value = Date.now();
    clock = setInterval(() => { now.value = Date.now(); }, 1000);
  }
}, { immediate: true });
onBeforeUnmount(() => { if (clock) clearInterval(clock); });
const presentation = computed(() => {
  const value = presentToolCall(props.call, catalog.value);
  if (props.layered && props.call.running && props.call.startedAt !== undefined) {
    const elapsed = now.value + (props.serverOffset ?? 0) - props.call.startedAt;
    if (Number.isFinite(elapsed) && elapsed >= 3000) return { ...value, meta: formatDuration(elapsed) };
  }
  return value;
});
const errorSummary = computed(() => (props.call.result?.text || t("toolDisclosure.errorSummary")).replace(/\s+/g, " ").slice(0, 120));
const liveText = ref(props.call.liveOutput ?? "");
const liveTail = computed(() => liveText.value.split("\n").slice(-5).join("\n"));
const hadLiveOutput = ref(!!props.call.liveOutput);
const liveHeld = ref(false);
const fileTarget = computed<OpenFileTarget | undefined>(() => {
  if (typeof props.call.args === "string") {
    if (!["read", "write", "edit"].includes(props.call.name)) return undefined;
    const path = extractPath(props.call.args);
    if (!path?.trim()) return undefined;
    return { path, name: path.split(/[\\/]/).pop() || path };
  }
  if (typeof props.call.args !== "object" || props.call.args === null || Array.isArray(props.call.args)) return undefined;
  if (props.call.name === "analyze_image") {
    const details = props.call.result?.details;
    const resolved = details && "path" in details && typeof details.path === "string" ? details.path : "";
    const input = (props.call.args as { image?: unknown }).image;
    const path = resolved || (typeof input === "string" && /[\\/]/.test(input) ? input : "");
    if (!path) return undefined;
    return { path, name: path.split(/[\\/]/).pop() || path };
  }
  if (!["read", "write", "edit"].includes(props.call.name)) return undefined;
  const path = (props.call.args as { path?: unknown }).path;
  if (typeof path !== "string" || !path.trim()) return undefined;
  const skill = matchSkillFile(props.call.name, props.call.args, props.skills ?? [], props.cwd ?? "");
  return { path, name: path.split(/[\\/]/).pop() || path, ...(skill ?? {}) };
});

const details = computed(() => props.call.result?.details);
const shortcutTool = computed(() => shortcutCallTool(props.call.name, props.call.args));

// A read that lands on a SKILL.md is the agent loading a skill. Labelling the
// read itself, rather than emitting a separate event, keeps one fact in one place
// on the timeline.
const skillName = computed(() =>
  matchSkillRead(props.call.name, props.call.args, props.skills ?? [], props.cwd ?? ""),
);

// Live output is a terminal: new lines belong at the bottom and the view should
// follow them. Only while it is the newest content, though — scrolling up to read
// something must not be undone on the next fragment, which is the usual way an
// auto-scrolling log becomes unusable.
const liveEl = ref<HTMLElement | null>(null);
let followingLive = true;
let selectingLive = false;
let liveInteractionVersion = 0;
function selectedLiveText(): boolean {
  const selection = typeof window !== "undefined" ? window.getSelection?.() : undefined;
  return !!(selection?.toString() && (
    selection.anchorNode && liveEl.value?.contains?.(selection.anchorNode) ||
    selection.focusNode && liveEl.value?.contains?.(selection.focusNode)));
}
function readingLive(): boolean {
  return !!props.layered && (selectingLive || !followingLive || selectedLiveText() ||
    typeof document !== "undefined" && !!document.activeElement && !!liveEl.value?.contains?.(document.activeElement));
}
function syncLiveText() {
  if (props.call.liveOutput) hadLiveOutput.value = true;
  const reading = readingLive();
  if (!reading && props.call.liveOutput !== undefined) liveText.value = props.call.liveOutput;
  liveHeld.value = hadLiveOutput.value && props.call.liveOutput === undefined && reading;
}
function onLiveScroll() {
  if (!liveEl.value || !detailsOpen.value) return;
  const el = liveEl.value;
  followingLive = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  liveInteractionVersion++;
  syncLiveText();
}
function onLiveWheel(event: WheelEvent) {
  if (props.layered && detailsOpen.value && event.deltaY < 0) { followingLive = false; liveInteractionVersion++; }
}
function onLiveKey(event: KeyboardEvent) {
  if (["ArrowUp", "PageUp", "Home"].includes(event.key)) { followingLive = false; liveInteractionVersion++; }
}
function startLiveSelection() { if (props.layered) selectingLive = true; }
function finishLiveSelection() { if (!selectingLive) return; selectingLive = false; liveInteractionVersion++; syncLiveText(); }
function viewSettledResult() {
  if (selectedLiveText()) window.getSelection?.()?.removeAllRanges();
  followingLive = true; selectingLive = false; liveHeld.value = false;
  if (disclosure?.available()) disclosure.set(disclosureKey(), true);
  else localChoices.value = new Map(localChoices.value).set(props.call.callId, true);
  nextTick(focusDisclosure);
}
watch(() => [props.call.liveOutput, props.call.result] as const, syncLiveText, { flush: "sync" });
watch(() => props.call.callId, () => { followingLive = true; selectingLive = false; liveHeld.value = false; hadLiveOutput.value = !!props.call.liveOutput; liveText.value = props.call.liveOutput ?? ""; });
let mounted = false;
let listening = false;
function bindLiveReading() {
  if (typeof document === "undefined") return;
  const needed = mounted && !!props.layered && hadLiveOutput.value && (!props.call.result || liveHeld.value);
  if (needed === listening) return;
  listening = needed;
  if (needed) {
    document.addEventListener?.("selectionchange", syncLiveText);
    document.addEventListener?.("pointerup", finishLiveSelection);
    document.addEventListener?.("pointercancel", finishLiveSelection);
  } else {
    document.removeEventListener?.("selectionchange", syncLiveText);
    document.removeEventListener?.("pointerup", finishLiveSelection);
    document.removeEventListener?.("pointercancel", finishLiveSelection);
  }
}
watch([() => props.layered, hadLiveOutput, () => props.call.result, liveHeld], bindLiveReading, { flush: "post" });
onMounted(() => { mounted = true; bindLiveReading(); });
onBeforeUnmount(() => { mounted = false; bindLiveReading(); });
watch(
  liveText,
  async () => {
    const el = liveEl.value;
    if (!el) return;
    const atBottom = !detailsOpen.value || el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    const version = liveInteractionVersion;
    if (!atBottom) return;
    await nextTick();
    if (version === liveInteractionVersion && !readingLive() && liveEl.value === el) el.scrollTop = el.scrollHeight;
  },
);
</script>

<template>
  <!-- data-cite is the anchor an answer's citation chips jump to (agent/
       citeJump.ts); absent when the call carried no stamp. -->
  <div ref="callEl" class="call" :class="[state, { layered, 'details-open': detailsOpen }]" :tabindex="layered ? -1 : undefined" :data-call-id="call.callId" :data-reading-output="liveHeld || undefined" :data-cite="call.result?.source?.label" @pogo-reveal-tool="revealDetails">
    <IncomingArgsView v-if="incoming" :incoming="incoming" :compact="layered" @open-file="emit('open-file', $event)" />
    <template v-else>
    <ToolCallHeader v-if="!headerless && (!isSubagent || inputInvalid)" :presentation="presentation" :file-target="fileTarget" :layered="layered"
      :expandable="layered && !fullDetails && !call.gate && !inputInvalid" :open="detailsOpen" :controls="bodyId"
      @toggle="toggleDetails" @open-file="emit('open-file', $event)">
      <span v-if="skillName" class="badge skill">skill {{ skillName }}</span>
    </ToolCallHeader>

    <!-- The correction hint is a heuristic (same tool, same target, succeeded
         after a failure), so it stays a soft note rather than a claim. -->
    <div v-if="call.corrects" class="corrects">↳ {{ t("toolCall.correctsHint") }}</div>

    <!-- Live content stays outside result disclosure: collapsing a row never
         conceals the running work. Expanding changes only the shown window. -->
    <div v-if="hadLiveOutput && !inputInvalid && !isSubagent" v-show="!!call.liveOutput || call.running || liveHeld" class="body live-body">
      <div ref="liveEl" class="live" :class="{ tail: layered && !detailsOpen }" @scroll="onLiveScroll" @wheel.passive="onLiveWheel" @keydown="onLiveKey" @pointerdown="startLiveSelection" @pointerup="finishLiveSelection" @pointercancel="finishLiveSelection"><AnsiText :text="layered && !detailsOpen ? liveTail : liveText" wrap /></div>
      <div v-if="liveHeld" class="retained-live-note">{{ t("toolDisclosure.outputEnded") }} <button type="button" @click="viewSettledResult">{{ t("toolDisclosure.viewResult") }}</button></div>
    </div>
    <div v-else-if="layered && call.running && !call.result && !call.gate && !inputInvalid && !isSubagent" class="running-empty" role="status">{{ t("toolDisclosure.runningNoOutput") }}</div>
    <div v-if="layered && failed && !inputInvalid && !detailsOpen" class="error-summary" :title="call.result?.text">{{ errorSummary }}</div>

    <!-- Mount once when needed; v-show keeps a reviewed result and its internal
         scroll state alive across folds, mode changes and later calls. -->
    <div v-show="detailsOpen && !liveHeld" :id="bodyId" ref="bodyEl" class="call-details">
    <template v-if="bodyMounted">
    <!-- A subagent owns the whole body, running or settled: it is another agent's
         run, not output, so it does not switch representation halfway through the
         way a command does. -->
    <div v-if="isSubagent && !inputInvalid" class="body">
      <SubagentResult
        :call-id="call.callId"
        :frames="call.liveFrames"
        :result="call.result"
        :args="call.args"
        :details="isSubagentDetails(details) ? details : undefined"
        :running="call.running"
        :bg-task="call.bgTask"
        :started-at="call.startedAt"
        :gates="[...(call.gate ? [call.gate] : []), ...(call.childGates ?? [])]"
        :notice="call.taskNotice"
        :orphaned="call.orphaned"
        :server-offset="serverOffset"
        @open-sheet="emit('open-sheet', $event)"
      />
    </div>

    <!-- The gate still shows for a subagent: delegating is itself a call that may
         need approval, and hiding the card would leave nothing to approve with.
         A subagent's card is live regardless of runActive: a background task's
         approval outlives every run, and rendering it stale would strand the
         child on a card nobody can answer. -->
    <div v-if="inputInvalid" class="body argument-error" data-testid="invalid-tool-arguments">
      <p>{{ t("toolCall.invalidArgumentsHint") }}</p>
      <div v-if="call.inputError" class="argument-reason">{{ call.inputError }}</div>
      <details v-if="rawArguments" :open="layered || undefined" class="raw-arguments">
        <summary>{{ t("toolCall.originalArguments") }}</summary>
        <pre>{{ rawArguments }}</pre>
      </details>
    </div>
    <div v-else-if="call.gate" class="body">
      <GateCard
        :gate="call.gate"
        :live="runActive || !!call.gate.origin"
        @decide="emit('decide', { gateId: call.gate!.gate_id, ...$event })"
        @freeze="emit('freeze', call.gate!.gate_id)"
        @thaw="emit('thaw', call.gate!.gate_id)"
      />
    </div>

    <!-- A subagent's own card renders its result, including the failure, so these
         two branches skip it rather than showing it a second time. -->
    <div v-else-if="failed && !isSubagent" class="body">
      <!-- An MCP failure carries its details like a bash one does, and the
           card's own error bar says what the generic line would — so the card
           replaces the line rather than repeating it. -->
      <UserInputResult v-if="call.name === 'request_user_input' || call.name === 'request_user_input_async'" :args="call.args" :result="call.result" />
      <AgentWorkflowResult v-else-if="call.name.startsWith('agent_workflow_') && isAgentWorkflowDetails(details)" :details="details" />
      <McpResult v-else-if="isMCPDetails(details)" :result="call.result!" :details="details" :args="call.args" headerless />
      <!-- A failed skill call still carries its capability identity, so it
           renders as a skill card (arguments included) rather than a bare
           error line. -->
      <SkillResult v-else-if="isSkillDetails(details)" :result="call.result!" :details="details" :args="call.args" headerless />
      <template v-else>
        <div class="error-text">{{ call.result?.text }}</div>
        <!-- A failure can still carry details: a non-zero exit code comes with the
             command's output, which is exactly what you need to see. -->
        <BashResult v-if="isBashDetails(details)" :result="call.result!" :details="details" />
      </template>
    </div>

    <div v-else-if="call.result && !isSubagent" class="body">
      <UserInputResult v-if="call.name === 'request_user_input' || call.name === 'request_user_input_async'" :args="call.args" :result="call.result" />
      <AgentWorkflowResult v-else-if="call.name.startsWith('agent_workflow_') && isAgentWorkflowDetails(details)" :details="details" />
      <SkillSavedResult v-else-if="call.name === 'skill_save' && isSavedSkillDetails(details)" :details="details" :live="runActive" />
      <ShortcutResult v-else-if="isSavedShortcutDetails(details) && shortcutTool === `shortcut_${details.action}`" :details="details" />
      <ShortcutMutationResult v-else-if="isShortcutMutationDetails(details, shortcutTool)" :details="details" />
      <ReadResult
        v-else-if="call.name === 'read' && isReadDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <!-- After the single-file branch, matching the guards' own discrimination:
           ReadManyDetails deliberately has no total_lines, so the two cannot both
           match and the order is a statement of intent rather than a dependency. -->
      <ReadManyResult
        v-else-if="call.name === 'read' && isReadManyDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <LsResult
        v-else-if="call.name === 'ls' && isLsDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <SearchResult
        v-else-if="(call.name === 'find' || call.name === 'grep') && isSearchDetails(details)"
        :result="call.result"
        :details="details"
        :kind="call.name"
        @suggest="emit('suggest', $event)"
      />
      <EditResult v-else-if="call.name === 'edit' && isEditDetails(details)" :details="details" />
      <WriteResult v-else-if="call.name === 'write' && isWriteDetails(details)" :result="call.result" :details="details" />
      <div v-else-if="call.name === 'analyze_image'" class="analyze-image-result">
        <AnswerText :source="call.result.text" passive />
      </div>
      <BashResult v-else-if="call.name === 'bash' && isBashDetails(details)" :result="call.result" :details="details" />
      <!-- MCP is a family, not a name: the guard on the details' server+tool
           pair is the discriminator, not the (possibly hash-truncated) wire
           name. Calls old enough to carry no details keep the CodeBlock
           fallback below. -->
      <McpResult v-else-if="isMCPDetails(details)" :result="call.result" :details="details" :args="call.args" headerless />
      <!-- Same details-shape discrimination as MCP: a RuntimeSkill call's
           CallResult keys mark the family; the wire name is display only. -->
      <SkillResult v-else-if="isSkillDetails(details)" :result="call.result" :details="details" :args="call.args" headerless />
      <TodoResult
        v-else-if="call.name === 'todo' && isTodoDetails(details)"
        :details="details"
        :superseded="call.superseded"
        :pinned="todoPinned"
      />
      <div v-else-if="layered && !call.result.text && !details" class="empty-result">{{ t("toolDisclosure.emptyResult") }}</div>
      <CodeBlock v-else :code="call.result.text" />
    </div>
    <div v-else-if="layered && !isSubagent && !call.running && !call.result && !call.gate && !inputInvalid" class="body waiting-result">{{ t("toolDisclosure.waitingResult") }}</div>
    </template>
    </div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.call {
  margin: 7px 0;
  /* The citation jump's landing spot: keep it clear of any overlaying chrome,
     and give the focus flash a shape. */
  scroll-margin-top: 12px;
  border-radius: 8px;
  transition: background 0.4s ease;
}
.call-details { min-width: 0; }
.layered .body { margin-left: 22px; }
.running-empty, .error-summary, .waiting-result, .empty-result {
  margin: 3px 0 7px 22px;
  font-size: 12px;
  color: var(--pg-activity-text, var(--el-text-color-regular));
  overflow-wrap: anywhere;
}
.error-summary { border-left: 2px solid var(--pg-activity-error, var(--el-color-danger)); padding-left: 8px; }
.call.layered:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.retained-live-note { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 5px; color: var(--pg-activity-muted, var(--el-text-color-regular)); font-size: 12px; }
.retained-live-note button { border: 0; border-radius: 3px; padding: 2px 0; background: transparent; color: var(--pg-activity-link, var(--pg-link-color)); font: inherit; cursor: pointer; }
.retained-live-note button:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.live.tail { height: calc(5 * 1.7em); max-height: calc(5 * 1.7em); overflow: hidden; border-radius: 6px; font-size: 12px; }
.live.tail :deep(pre) { margin: 0; }

/* Added by citeJump for 1.5s after a citation chip sends the reader here. */
.call.cite-focused {
  background: color-mix(in srgb, var(--el-color-primary) 18%, transparent);
}

.badge.skill {
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
  flex: 0 0 auto;
}

.corrects {
  margin: 2px 0 4px 14px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  border-left: 1px dashed var(--el-border-color);
  padding-left: 8px;
}

.body {
  margin: 6px 0 10px 14px;
}

.analyze-image-result {
  margin-top: 8px;
  padding: 12px 16px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 10px;
  background: var(--el-fill-color-lighter);
  overflow-wrap: anywhere;
}
.analyze-image-result :deep(.answer) {
  margin-top: 0;
  font-size: 13px;
  line-height: 1.75;
}

.argument-error {
  padding: 10px 12px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-regular);
  p { margin: 0; }
}

.argument-reason {
  margin-top: 6px;
  color: var(--el-text-color-secondary);
  overflow-wrap: anywhere;
}

.raw-arguments {
  margin-top: 8px;
  summary { cursor: pointer; color: var(--el-text-color-secondary); }
  pre {
    max-height: 240px;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-family: var(--pg-mono);
    margin: 8px 0 0;
  }
}

/* Terminal styling, and deliberately not syntax highlighted: command output is
   not code, and colouring keywords in a stack trace is noise. Font and padding
   live in AnsiText; this box provides the dark shell. Colours match the settled
   CodeBlock terminal so the view does not change appearance when the call
   settles. */
/* The terminal surface is its own token, not a fill: every skin keeps command
   output on a dark block — that is the convention output is written for — and each
   one borrows its own family's dark surface for it. See theme/build.ts. */
.live {
  margin: 0;
  max-height: 300px;
  overflow: auto;
  border-radius: 10px;
  background: var(--pg-term-bg);
  color: var(--pg-term-fg);
}

.error-text {
  font-size: 12px;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  border: 1px solid color-mix(in srgb, var(--el-color-danger) 18%, transparent);
  border-radius: 8px;
  padding: 7px 10px;
  margin-bottom: 6px;
  white-space: pre-wrap;
}
</style>
