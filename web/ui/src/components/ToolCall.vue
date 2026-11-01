<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AnsiText from "./AnsiText.vue";
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
import WorkflowAuthoringResult from "./tools/WorkflowAuthoringResult.vue";
import AgentWorkflowResult from "./tools/AgentWorkflowResult.vue";
import { isAgentWorkflowDetails } from "@/agent/agentWorkflowNavigation";
import { isWorkflowAuthoringDetails } from "@/agent/workflowAuthoringReceipt";
import { isSavedSkillDetails } from "@/agent/skillAuthoringReceipt";
import { isSavedShortcutDetails, isShortcutMutationDetails } from "@/agent/shortcutReceipt";
import { shortcutCallTool } from "@/agent/shortcutToolNames";
import CodeBlock from "./CodeBlock.vue";
import GateCard from "./GateCard.vue";
import ToolCallHeader from "./ToolCallHeader.vue";
import { presentToolCall, toolCallState } from "@/agent/toolPresentation";
import {
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
  matchSkillRead,
  type TimelineCall,
} from "@/agent/timeline";
import type { OpenSheetRequest } from "./dockSheets";

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
}>();

const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string];
  thaw: [string];
  "open-sheet": [OpenSheetRequest];
}>();

const { t } = useI18n();

const inputInvalid = computed(() => props.call.inputInvalid === true);
const failed = computed(() => inputInvalid.value || props.call.result?.is_error === true);
const rawArguments = computed(() => typeof props.call.args === "string" ? props.call.args : "");
const isSubagent = computed(() => props.call.name === "subagent");

const state = computed(() => toolCallState(props.call));
const presentation = computed(() => presentToolCall(props.call));

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
watch(
  () => props.call.liveOutput,
  async () => {
    const el = liveEl.value;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (!atBottom) return;
    await nextTick();
    el.scrollTop = el.scrollHeight;
  },
);
</script>

<template>
  <!-- data-cite is the anchor an answer's citation chips jump to (agent/
       citeJump.ts); absent when the call carried no stamp. -->
  <div class="call" :class="state" :data-cite="call.result?.source?.label">
    <ToolCallHeader v-if="!headerless && (!isSubagent || inputInvalid)" :presentation="presentation">
      <span v-if="skillName" class="badge skill">skill {{ skillName }}</span>
    </ToolCallHeader>

    <!-- The correction hint is a heuristic (same tool, same target, succeeded
         after a failure), so it stays a soft note rather than a claim. -->
    <div v-if="call.corrects" class="corrects">↳ {{ t("toolCall.correctsHint") }}</div>

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

    <!-- Output from a command that is still running. Replaced by the settled
         result on tool_end, so nothing is rendered twice. AnsiText honours the
         command's own colours, same as the settled BashResult does. -->
    <div v-else-if="call.liveOutput && !inputInvalid" class="body">
      <div class="live" ref="liveEl"><AnsiText :text="call.liveOutput" wrap /></div>
    </div>

    <!-- The gate still shows for a subagent: delegating is itself a call that may
         need approval, and hiding the card would leave nothing to approve with.
         A subagent's card is live regardless of runActive: a background task's
         approval outlives every run, and rendering it stale would strand the
         child on a card nobody can answer. -->
    <div v-if="inputInvalid" class="body argument-error" data-testid="invalid-tool-arguments">
      <p>{{ t("toolCall.invalidArgumentsHint") }}</p>
      <div v-if="call.inputError" class="argument-reason">{{ call.inputError }}</div>
      <details v-if="rawArguments" class="raw-arguments">
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
      <WorkflowAuthoringResult v-else-if="call.name.startsWith('workflow_') && isWorkflowAuthoringDetails(details)" :details="details" />
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
      <AgentWorkflowResult v-if="call.name.startsWith('agent_workflow_') && isAgentWorkflowDetails(details)" :details="details" />
      <WorkflowAuthoringResult v-else-if="call.name.startsWith('workflow_') && isWorkflowAuthoringDetails(details)" :details="details" />
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
      <CodeBlock v-else :code="call.result.text" />
    </div>
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
