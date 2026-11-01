<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref } from "vue";
import { useI18n } from "vue-i18n";
import { platformApi } from "@/api/platform";
import type { HumanFieldSpec, WorkflowRun } from "@/agent/workflowTypes";
import { workflowRunCan } from "@/agent/workflowPresentation";
import { watchWorkflowFormIdentity } from "@/agent/workflowFormIdentity";
import WorkflowApprovalAssignmentInfo from "@/components/workflow/WorkflowApprovalAssignmentInfo.vue";
const MarkdownView = defineAsyncComponent(() => import("@/components/MarkdownView.vue"));

/** The runtime approval drawer: opens on a waiting_human node and renders
    the wait's own self-contained record — the prompt as Markdown, the form,
    the action buttons, the deadline — no join against the definition. A
    settled wait shows the recorded decision instead. */
const props = defineProps<{
  open: boolean;
  nodeId: string;
  run: WorkflowRun | null;
  workflowId: string;
}>();

const emit = defineEmits<{ "update:open": [boolean]; resumed: [] }>();

const { t } = useI18n();

const record = computed(() => (props.nodeId ? props.run?.nodes[props.nodeId] : undefined));
const human = computed(() => record.value?.human);
const pending = computed(() => !!human.value && !human.value.action && record.value?.status === "waiting_human");
const canDecide = computed(() => workflowRunCan(props.run, "can_approve", true, props.nodeId));

/* ------------------------------ deadline ------------------------------ */

const now = ref(Date.now());
let clock: number | undefined;
clock = window.setInterval(() => {
  now.value = Date.now();
}, 30000);
onBeforeUnmount(() => window.clearInterval(clock));

const deadline = computed(() => {
  const dl = human.value?.deadline;
  if (!dl) return { passed: false, text: "" };
  const ms = new Date(dl).getTime() - now.value;
  if (Number.isNaN(ms)) return { passed: false, text: "" };
  if (ms <= 0) return { passed: true, text: "" };
  const minutes = Math.floor(ms / 60000);
  const d = Math.floor(minutes / 1440);
  const h = Math.floor((minutes % 1440) / 60);
  const m = minutes % 60;
  const parts: string[] = [];
  if (d) parts.push(`${d}d`);
  if (h) parts.push(`${h}h`);
  parts.push(`${m}m`);
  return { passed: false, text: parts.join(" ") };
});

/* ------------------------------ form & submit ------------------------------ */

const values = ref<Record<string, string>>({});
const busy = ref(false);
const error = ref("");

function fieldLabel(f: HumanFieldSpec): string {
  return f.label?.trim() || f.key;
}

async function submit(action: string) {
  if (busy.value || !props.run || props.run.persistence_error || !canDecide.value) return;
  for (const f of human.value?.fields ?? []) {
    if (f.required && !(values.value[f.key] ?? "").trim()) {
      error.value = t("workflow.approval.requiredField", { label: fieldLabel(f) });
      return;
    }
  }
  busy.value = true;
  error.value = "";
  try {
    await platformApi.workflowRuns.resume(props.workflowId, props.run.id, props.nodeId, { action, fields: { ...values.value } });
    emit("resumed");
    emit("update:open", false);
  } catch (e) {
    error.value = t("workflow.approval.failed", { msg: (e as Error).message });
  } finally {
    busy.value = false;
  }
}

watchWorkflowFormIdentity(
  [() => props.nodeId, () => props.run?.id],
  () => {
    values.value = {};
    error.value = "";
    busy.value = false;
  },
);
</script>

<template>
  <el-drawer
    :model-value="open"
    :title="t('workflow.approval.title')"
    direction="rtl"
    size="440px"
    @update:model-value="emit('update:open', $event)"
  >
    <div v-if="human" class="wf-approval">
      <WorkflowApprovalAssignmentInfo :assignment="run?.approvals?.[nodeId]" :pending="pending" />
      <p v-if="run?.persistence_error" class="wf-approval-error" role="alert">{{ t("workflow.run.persistenceHint") }}</p>
      <template v-else-if="pending">
        <p class="wf-approval-waiting">{{ t("workflow.approval.waiting") }}</p>
        <p v-if="!canDecide && !run?.approvals?.[nodeId]?.blocked_reason" class="wf-approval-waiting">{{ t("workflow.approval.noPermission") }}</p>
        <p v-if="deadline.passed" class="wf-approval-deadline passed" role="alert">{{ t("workflow.approval.deadlinePassed") }}</p>
        <p v-else-if="deadline.text" class="wf-approval-deadline">{{ t("workflow.approval.deadline", { time: deadline.text }) }}</p>

        <div class="wf-approval-prompt">
          <MarkdownView :source="human.prompt" />
        </div>

        <div v-if="human.fields?.length" class="wf-approval-fields">
          <div v-for="f in human.fields" :key="f.key" class="wf-approval-field">
            <label>
              {{ fieldLabel(f) }}
              <em v-if="f.required" aria-hidden="true">*</em>
            </label>
            <el-select
              v-if="f.type === 'select'"
              v-model="values[f.key]"
              :placeholder="fieldLabel(f)"
              :disabled="!canDecide || busy"
            >
              <el-option v-for="o in f.options ?? []" :key="o" :value="o" :label="o" />
            </el-select>
            <el-input v-else v-model="values[f.key]" :placeholder="fieldLabel(f)" :disabled="!canDecide || busy" />
          </div>
        </div>

        <p v-if="error" class="wf-approval-error" role="alert">{{ error }}</p>

        <div class="wf-approval-actions">
          <button
            v-for="a in human.actions"
            :key="a"
            type="button"
            class="wf-approval-btn"
            :class="{ danger: a === 'reject' }"
            :disabled="busy || !canDecide"
            @click="submit(a)"
          >
            {{ busy ? t("workflow.approval.submitting") : a }}
          </button>
        </div>
      </template>

      <template v-else-if="human.action">
        <p class="wf-approval-decided">{{ t("workflow.approval.decided", { action: human.action }) }}</p>
        <p class="wf-approval-decided-hint">{{ t("workflow.approval.decidedAt") }}</p>
        <div class="wf-approval-prompt settled">
          <MarkdownView :source="human.prompt" />
        </div>
        <div v-if="human.values && Object.keys(human.values).length" class="wf-approval-fields">
          <div v-for="(v, k) in human.values" :key="k" class="wf-approval-kv">
            <code>{{ k }}</code>
            <span>{{ v }}</span>
          </div>
        </div>
      </template>
      <template v-else>
        <p class="wf-approval-decided">{{ t(`workflow.state.${record?.status || 'cancelled'}`) }}</p>
        <p class="wf-approval-decided-hint">{{ t('workflowAuthoring.noDecision') }}</p>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.wf-approval {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 2px 2px 16px;
}
.wf-approval-waiting {
  margin: 0;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}
.wf-approval-deadline {
  margin: 0;
  align-self: flex-start;
  font-size: 12px;
  font-weight: 600;
  border-radius: 999px;
  padding: 3px 11px;
  background: color-mix(in srgb, var(--pg-wf-state-waiting-human) 15%, transparent);
  color: var(--pg-wf-state-waiting-human);
}
.wf-approval-deadline.passed {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.wf-approval-prompt {
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 10px;
  background: var(--el-fill-color-extra-light);
  padding: 12px 14px;
  font-size: 13px;
  max-height: 42vh;
  overflow-y: auto;
}
.wf-approval-prompt.settled {
  opacity: 0.75;
}
.wf-approval-fields {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.wf-approval-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wf-approval-field label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-regular);
}
.wf-approval-field em {
  color: var(--el-color-danger);
  font-style: normal;
}
.wf-approval-error {
  margin: 0;
  border-radius: 8px;
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  color: var(--el-color-danger);
  font-size: 12px;
  padding: 8px 12px;
}
.wf-approval-actions {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.wf-approval-btn {
  flex: 1;
  min-width: 120px;
  border: 1px solid var(--pg-solid);
  background: var(--pg-solid);
  color: var(--pg-on-solid);
  border-radius: 8px;
  padding: 9px 16px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  transition: background var(--pg-transition), border-color var(--pg-transition);
}
.wf-approval-btn:hover:not(:disabled) {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.wf-approval-btn.danger {
  background: transparent;
  border-color: var(--el-color-danger-light-5);
  color: var(--el-color-danger);
}
.wf-approval-btn.danger:hover:not(:disabled) {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}
.wf-approval-btn:disabled {
  opacity: 0.55;
  cursor: wait;
}
.wf-approval-decided {
  margin: 0;
  font-size: 14px;
  font-weight: 650;
  color: var(--el-text-color-primary);
}
.wf-approval-decided-hint {
  margin: -8px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.wf-approval-kv {
  display: flex;
  gap: 8px;
  font-size: 12px;
}
.wf-approval-kv code {
  font-family: var(--pg-mono);
  color: var(--el-color-primary);
}
.wf-approval-kv span {
  color: var(--el-text-color-regular);
}
</style>
