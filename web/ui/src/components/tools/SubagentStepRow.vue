<script setup lang="ts">
import { computed } from "vue";
import CodeBlock from "../CodeBlock.vue";
import ToolCallHeader from "../ToolCallHeader.vue";
import BashResult from "./BashResult.vue";
import EditResult from "./EditResult.vue";
import WriteResult from "./WriteResult.vue";
import ReadResult from "./ReadResult.vue";
import ReadManyResult from "./ReadManyResult.vue";
import LsResult from "./LsResult.vue";
import SearchResult from "./SearchResult.vue";
import McpResult from "./McpResult.vue";
import SkillResult from "./SkillResult.vue";
import { isBashDetails, isEditDetails, isLsDetails, isMCPDetails, isReadDetails, isReadManyDetails, isSearchDetails, isSkillDetails, isWriteDetails, type SubagentStep } from "@/agent/timeline";
import { presentToolCall } from "@/agent/toolPresentation";
import type { ToolName, ToolResult } from "@/api/types";

const props = defineProps<{ step: SubagentStep; open: boolean; disclosureId?: string }>();
const emit = defineEmits<{ toggle: [SubagentStep] }>();
const canOpen = computed(() => props.step.done === true && (props.step.text !== undefined || props.step.details !== undefined));
const result = computed<ToolResult>(() => ({ call_id: props.step.callId ?? "", name: props.step.name as ToolName | undefined,
  text: props.step.text ?? "", is_error: props.step.bad || undefined, details: props.step.details }));
const presentation = computed(() => presentToolCall({ callId: props.step.callId ?? "", name: (props.step.name ?? "tool") as ToolName,
  args: props.step.args, running: !props.step.done, orphaned: false, ...(props.step.done ? { result: result.value } : {}) }));
</script>

<template>
  <div class="child-tool" :class="{ failed: step.bad }" :data-child-call="step.callId" :data-child-turn="step.turn">
    <ToolCallHeader :presentation="presentation" :expandable="canOpen" :open="open" @toggle="emit('toggle', step)" />
    <!-- Errors remain readable even when their ordinary result is collapsed. -->
    <div v-if="step.bad && !isMCPDetails(step.details) && !isSkillDetails(step.details)" class="error-text">{{ step.text }}</div>
    <div v-if="open || step.bad && (isMCPDetails(step.details) || isSkillDetails(step.details))" class="row-result">
      <McpResult v-if="isMCPDetails(step.details)" :result="result" :details="step.details" :args="step.args" headerless :disclosure-key="disclosureId ? `${disclosureId}:params` : null" />
      <SkillResult v-else-if="isSkillDetails(step.details)" :result="result" :details="step.details" :args="step.args" headerless :disclosure-key="disclosureId ? `${disclosureId}:params` : null" />
      <ReadResult v-else-if="step.name === 'read' && isReadDetails(step.details)" :result="result" :details="step.details" />
      <ReadManyResult v-else-if="step.name === 'read' && isReadManyDetails(step.details)" :result="result" :details="step.details" />
      <LsResult v-else-if="step.name === 'ls' && isLsDetails(step.details)" :result="result" :details="step.details" />
      <SearchResult v-else-if="(step.name === 'find' || step.name === 'grep') && isSearchDetails(step.details)" :result="result" :details="step.details" :kind="step.name" />
      <BashResult v-else-if="isBashDetails(step.details)" :result="result" :details="step.details" />
      <EditResult v-else-if="isEditDetails(step.details)" :details="step.details" />
      <WriteResult v-else-if="isWriteDetails(step.details)" :result="result" :details="step.details" />
      <CodeBlock v-else-if="!step.bad" :code="step.text ?? ''" />
    </div>
  </div>
</template>

<style scoped>
.child-tool { margin: 6px 0; min-width: 0; }
.row-result { margin: 4px 0 8px 22px; min-width: 0; }
/* These suggestions target the parent's composer, whose execution context is
   different. Keep the child's recorded content, not a dead continuation UI. */
.row-result :deep(button.more) { display: none; }
.error-text { margin: 4px 0 8px 22px; padding: 6px 8px; border-left: 2px solid var(--el-color-danger); color: var(--el-color-danger); background: var(--el-color-danger-light-9); border-radius: 5px; font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
