<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Delete, Plus } from "@element-plus/icons-vue";
import AgentAvatar from "@/components/AgentAvatar.vue";
import WorkflowVarInput from "@/components/workflow/WorkflowVarInput.vue";
import WorkflowApprovalPolicyEditor from "@/components/workflow/WorkflowApprovalPolicyEditor.vue";
import WorkflowApprovalAssignmentInfo from "@/components/workflow/WorkflowApprovalAssignmentInfo.vue";
import { platformApi } from "@/api/platform";
import type { BuiltinToolInfo, PlatformAgent, PlatformModel } from "@/agent/platformTypes";
import type { HumanFieldSpec, OrchestrateConfig, WorkflowNode, WorkflowRun } from "@/agent/workflowTypes";
import { upstreamClosure, workflowRunCan, WORKFLOW_LIMITS } from "@/agent/workflowPresentation";
import { modelLabel } from "@/agent/studioPresentation";
import { legacyShortcutManagementNames } from "@/agent/shortcutToolNames";
import { workflowAgentApprovalPatch, workflowNodeKindPatch } from "@/agent/workflowApprovalPolicy";

/** The right-hand drawer: one tab edits the node's step definition — agent
    and human kinds get their own forms, edits go back up as patches saved by
    the editor's debounced channel — the other shows this node's record in
    the current run: state, branch, template inputs, dispatches, artifact. */
const props = defineProps<{
  open: boolean;
  node: WorkflowNode | null;
  /** All draft nodes: the template autocomplete's closure is computed here. */
  nodes: WorkflowNode[];
  agents: PlatformAgent[];
  models: PlatformModel[];
  toolsCatalog: BuiltinToolInfo[];
  run: WorkflowRun | null;
  workflowId: string;
  canManage: boolean;
}>();

const emit = defineEmits<{
  "update:open": [boolean];
  patch: [id: string, patch: Partial<WorkflowNode>];
  remove: [id: string];
}>();

const { t, d } = useI18n();

const tab = ref<"config" | "result">("config");
const human = computed(() => props.node?.kind === "human");
const agent = computed(() => props.agents.find((a) => a.id === props.node?.agent));
const record = computed(() => (props.node ? props.run?.nodes[props.node.id] : undefined));

/** The autocomplete's legitimate references: the transitive upstream closure,
    with expect flags deciding whether artifact entries appear. */
const closureNodes = computed(() => {
  if (!props.node) return [];
  const byId = new Map(props.nodes.map((n) => [n.id, n]));
  return upstreamClosure(props.nodes, props.node.id).map((id) => ({
    id,
    hasExpect: !!byId.get(id)?.expect?.path,
  }));
});

const toolOptions = computed(() => {
  const options = props.toolsCatalog.map((tool) => ({
    value: tool.name,
    label: tool.description ? `${tool.name} — ${tool.description}` : tool.name,
  }));
  // A saved selection the catalog no longer knows stays visible, flagged.
  for (const name of props.node?.tools ?? []) {
    if (!options.some((o) => o.value === name)) options.push({ value: name, label: legacyShortcutManagementNames.some(legacy => legacy === name) ? `${name} — 保留原有的单项权限` : name });
  }
  return options;
});

/** tools rides as undefined (inherit the Agent) until something is picked;
    clearing the selection hands inheritance back. */
const toolsSelection = computed<string[]>({
  get: () => props.node?.tools ?? [],
  set: (ids) => patch({ tools: ids.length ? [...ids] : undefined }),
});

const modelSelection = computed<string>({
  get: () => props.node?.model ?? "",
  set: (id) => patch({ model: id || undefined }),
});

const actionsSelection = computed<string[]>({
  get: () => props.node?.actions ?? ["approve", "reject"],
  set: (list) => patch({ actions: list.length ? [...list] : undefined }),
});

function patch(p: Partial<WorkflowNode>) {
  if (!props.node || !props.canManage) return;
  emit("patch", props.node.id, p);
}

/* ------------------------------ orchestrate ------------------------------ */

function patchOrchestrate(mut: (o: OrchestrateConfig) => void) {
  if (!props.node) return;
  const next: OrchestrateConfig = {
    candidates: (props.node.orchestrate?.candidates ?? []).map((c) => ({ ...c })),
    max_dispatches: props.node.orchestrate?.max_dispatches,
    max_parallel: props.node.orchestrate?.max_parallel,
  };
  mut(next);
  patch({ orchestrate: next.candidates.length ? next : undefined });
}

function addCandidate() {
  patchOrchestrate((o) => o.candidates.push({ agent: "", hint: "" }));
}

/* ------------------------------ human fields ------------------------------ */

function patchFields(mut: (fields: HumanFieldSpec[]) => void) {
  if (!props.node) return;
  const fields = (props.node.fields ?? []).map((f) => ({ ...f, options: f.options ? [...f.options] : undefined }));
  mut(fields);
  patch({ fields: fields.length ? fields : undefined });
}

function fieldOptionsText(f: HumanFieldSpec): string {
  return (f.options ?? []).join(", ");
}

function setFieldOptions(index: number, text: string) {
  patchFields((fields) => {
    const f = fields[index];
    if (f) f.options = text.split(/[,，]/).map((o) => o.trim()).filter(Boolean);
  });
}

/* ------------------------------ artifact ------------------------------ */

const artifactText = ref("");
const artifactLoading = ref(false);
const artifactFailed = ref(false);
const artifactShown = ref(false);

async function loadArtifact() {
  if (!props.run || !props.node || artifactLoading.value || !workflowRunCan(props.run, "can_read_artifacts", true, props.node.id)) return;
  artifactLoading.value = true;
  artifactFailed.value = false;
  try {
    artifactText.value = await platformApi.workflowRuns.artifact(props.workflowId, props.run.id, props.node.id);
    artifactShown.value = true;
  } catch {
    artifactFailed.value = true;
  } finally {
    artifactLoading.value = false;
  }
}

watch(
  () => props.node?.id,
  () => {
    tab.value = props.canManage ? "config" : "result";
    artifactText.value = "";
    artifactShown.value = false;
    artifactFailed.value = false;
  },
);
watch(
  () => props.run?.id,
  () => {
    artifactText.value = "";
    artifactShown.value = false;
    artifactFailed.value = false;
  },
);
</script>

<template>
  <el-drawer
    :model-value="open"
    :title="human ? t('workflow.kind.human') : (agent?.name ?? node?.id ?? '')"
    direction="rtl"
    size="400px"
    :with-header="true"
    @update:model-value="emit('update:open', $event)"
  >
    <div v-if="node" class="wf-drawer">
      <el-tabs v-model="tab" class="wf-drawer-tabs">
        <el-tab-pane :label="t('workflow.drawer.configTab')" name="config">
          <div class="wf-form">
            <div class="wf-field">
              <label>{{ t("workflow.drawer.kindLabel") }}</label>
              <el-radio-group
                :model-value="human ? 'human' : 'agent'"
                :disabled="!canManage"
                @update:model-value="patch(workflowNodeKindPatch(node, String($event) === 'human' ? 'human' : 'agent'))"
              >
                <el-radio-button value="agent">{{ t("workflow.kind.agent") }}</el-radio-button>
                <el-radio-button value="human">{{ t("workflow.kind.human") }}</el-radio-button>
              </el-radio-group>
            </div>

            <!-- ── agent kind ─────────────────────────────────────────── -->
            <template v-if="!human">
              <div class="wf-field">
                <label>{{ t("workflow.drawer.agent") }}</label>
                <div class="wf-agent-row">
                  <AgentAvatar v-if="agent" :icon="agent.icon" :color="agent.color" :size="28" />
                  <strong :class="{ unknown: !agent }">{{ agent?.name ?? t("workflow.errors.agent_unknown") }}</strong>
                </div>
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.task") }}</label>
                <WorkflowVarInput
                  :model-value="node.task"
                  :closure-nodes="closureNodes"
                  :rows="6"
                  :maxlength="WORKFLOW_LIMITS.task"
                  :placeholder="t('workflow.drawer.taskPlaceholder')"
                  :disabled="!canManage"
                  @update:model-value="patch({ task: String($event ?? '') })"
                />
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.expectPath") }}</label>
                <el-input
                  :model-value="node.expect?.path ?? ''"
                  :placeholder="t('workflow.drawer.expectPlaceholder')"
                  :disabled="!canManage"
                  @update:model-value="patch({ expect: String($event ?? '').trim() ? { path: String($event).trim() } : undefined })"
                />
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.tools") }}</label>
                <el-select
                  v-model="toolsSelection"
                  multiple
                  clearable
                  collapse-tags
                  collapse-tags-tooltip
                  :placeholder="t('workflow.drawer.toolsPlaceholder')"
                  :disabled="!canManage"
                >
                  <el-option v-for="opt in toolOptions" :key="opt.value" :value="opt.value" :label="opt.label" />
                </el-select>
                <p class="wf-field-hint">{{ t("workflow.drawer.toolsInherit") }}</p>
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.model") }}</label>
                <el-select v-model="modelSelection" clearable :placeholder="t('workflow.drawer.modelInherit')" :disabled="!canManage">
                  <el-option v-for="m in models" :key="m.id" :value="m.id" :label="modelLabel(models, m.id)" />
                </el-select>
              </div>

              <div class="wf-field">
                <el-checkbox
                  :model-value="!!node.approval"
                  :disabled="!canManage"
                  @update:model-value="patch(workflowAgentApprovalPatch(node, $event === true))"
                >
                  {{ t("workflow.drawer.approval") }}
                </el-checkbox>
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.orch") }}</label>
                <p class="wf-field-hint">{{ t("workflow.drawer.orchHint") }}</p>
                <div v-for="(c, i) in node.orchestrate?.candidates ?? []" :key="i" class="wf-candidate">
                  <el-select
                    :model-value="c.agent"
                    :placeholder="t('workflow.drawer.orchAgent')"
                    :disabled="!canManage"
                    @update:model-value="patchOrchestrate((o) => { const x = o.candidates[i]; if (x) x.agent = String($event ?? ''); })"
                  >
                    <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="a.name" />
                  </el-select>
                  <div class="wf-candidate-row">
                    <el-input
                      :model-value="c.hint ?? ''"
                      :placeholder="t('workflow.drawer.orchCandidateHint')"
                      :disabled="!canManage"
                      @update:model-value="patchOrchestrate((o) => { const x = o.candidates[i]; if (x) x.hint = String($event ?? ''); })"
                    />
                    <button
                      v-if="canManage"
                      type="button"
                      class="wf-mini-btn"
                      :aria-label="t('workflow.list.remove')"
                      @click="patchOrchestrate((o) => o.candidates.splice(i, 1))"
                    >
                      <el-icon aria-hidden="true"><Delete /></el-icon>
                    </button>
                  </div>
                </div>
                <div class="wf-candidate-row">
                  <button v-if="canManage" type="button" class="wf-mini-add" @click="addCandidate">
                    <el-icon aria-hidden="true"><Plus /></el-icon>
                    {{ t("workflow.drawer.orchAdd") }}
                  </button>
                </div>
                <div v-if="node.orchestrate" class="wf-nums">
                  <label class="wf-num">
                    {{ t("workflow.drawer.orchMaxDispatches") }}
                    <el-input-number
                      :model-value="node.orchestrate.max_dispatches ?? 5"
                      :min="1"
                      :max="20"
                      size="small"
                      :disabled="!canManage"
                      @update:model-value="patchOrchestrate((o) => { o.max_dispatches = Number($event) || undefined; })"
                    />
                  </label>
                  <label class="wf-num">
                    {{ t("workflow.drawer.orchMaxParallel") }}
                    <el-input-number
                      :model-value="node.orchestrate.max_parallel ?? 1"
                      :min="1"
                      :max="4"
                      size="small"
                      :disabled="!canManage"
                      @update:model-value="patchOrchestrate((o) => { o.max_parallel = Number($event) || undefined; })"
                    />
                  </label>
                </div>
              </div>
            </template>

            <!-- ── human kind ─────────────────────────────────────────── -->
            <template v-else>
              <div class="wf-field">
                <label>{{ t("workflow.drawer.prompt") }}</label>
                <WorkflowVarInput
                  :model-value="node.prompt ?? ''"
                  :closure-nodes="closureNodes"
                  :rows="6"
                  :placeholder="t('workflow.drawer.promptPlaceholder')"
                  :disabled="!canManage"
                  @update:model-value="patch({ prompt: String($event ?? '') })"
                />
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.fields") }}</label>
                <div v-for="(f, i) in node.fields ?? []" :key="i" class="wf-spec">
                  <div class="wf-spec-row">
                    <el-input
                      :model-value="f.key"
                      :placeholder="t('workflow.drawer.fieldKey')"
                      :disabled="!canManage"
                      @update:model-value="patchFields((fs) => { const x = fs[i]; if (x) x.key = String($event ?? '').trim(); })"
                    />
                    <el-input
                      :model-value="f.label ?? ''"
                      :placeholder="t('workflow.drawer.fieldLabel')"
                      :disabled="!canManage"
                      @update:model-value="patchFields((fs) => { const x = fs[i]; if (x) x.label = String($event ?? ''); })"
                    />
                    <button
                      v-if="canManage"
                      type="button"
                      class="wf-mini-btn"
                      :aria-label="t('workflow.list.remove')"
                      @click="patchFields((fs) => fs.splice(i, 1))"
                    >
                      <el-icon aria-hidden="true"><Delete /></el-icon>
                    </button>
                  </div>
                  <div class="wf-spec-row">
                    <el-select
                      :model-value="f.type ?? 'text'"
                      :disabled="!canManage"
                      @update:model-value="patchFields((fs) => { const x = fs[i]; if (x) x.type = String($event) === 'select' ? 'select' : 'text'; })"
                    >
                      <el-option value="text" :label="t('workflow.drawer.fieldTypeText')" />
                      <el-option value="select" :label="t('workflow.drawer.fieldTypeSelect')" />
                    </el-select>
                    <el-checkbox
                      :model-value="!!f.required"
                      :disabled="!canManage"
                      @update:model-value="patchFields((fs) => { const x = fs[i]; if (x) x.required = $event === true; })"
                    >
                      {{ t("workflow.drawer.fieldRequired") }}
                    </el-checkbox>
                  </div>
                  <el-input
                    v-if="f.type === 'select'"
                    :model-value="fieldOptionsText(f)"
                    :placeholder="t('workflow.drawer.fieldOptions')"
                    :disabled="!canManage"
                    @update:model-value="setFieldOptions(i, String($event ?? ''))"
                  />
                </div>
                <div>
                  <button v-if="canManage" type="button" class="wf-mini-add" @click="patchFields((fs) => fs.push({ key: '', label: '', type: 'text', required: false }))">
                    <el-icon aria-hidden="true"><Plus /></el-icon>
                    {{ t("workflow.drawer.fieldAdd") }}
                  </button>
                </div>
              </div>

              <div class="wf-field">
                <label>{{ t("workflow.drawer.actions") }}</label>
                <el-select
                  v-model="actionsSelection"
                  multiple
                  filterable
                  allow-create
                  default-first-option
                  :placeholder="t('workflow.drawer.actionsPlaceholder')"
                  :disabled="!canManage"
                >
                  <el-option value="approve" label="approve" />
                  <el-option value="reject" label="reject" />
                </el-select>
                <p class="wf-field-hint">{{ t("workflow.drawer.actionsHint") }}</p>
              </div>

              <div class="wf-field wf-nums">
                <label class="wf-num grow">
                  {{ t("workflow.drawer.timeout") }}
                  <el-input
                    :model-value="node.timeout ?? ''"
                    :placeholder="t('workflow.drawer.timeoutPlaceholder')"
                    :disabled="!canManage"
                    @update:model-value="patch({ timeout: String($event ?? '').trim() || undefined })"
                  />
                </label>
                <label class="wf-num grow">
                  {{ t("workflow.drawer.onTimeout") }}
                  <el-select
                    :model-value="node.on_timeout ?? 'reject'"
                    :disabled="!canManage"
                    @update:model-value="patch({ on_timeout: String($event) === 'fail' ? 'fail' : 'reject' })"
                  >
                    <el-option value="reject" :label="t('workflow.drawer.onTimeoutReject')" />
                    <el-option value="fail" :label="t('workflow.drawer.onTimeoutFail')" />
                  </el-select>
                </label>
              </div>
            </template>

            <WorkflowApprovalPolicyEditor
              v-if="human || node.approval"
              :model-value="node.approval_policy"
              :workflow-id="workflowId"
              :active="open && tab === 'config'"
              :disabled="!canManage"
              @update:model-value="patch({ approval_policy: $event })"
            />

            <button
              v-if="canManage"
              type="button"
              class="wf-remove"
              @click="emit('remove', node.id); emit('update:open', false)"
            >
              {{ t("workflow.node.deleteNode") }}
            </button>
          </div>
        </el-tab-pane>

        <el-tab-pane :label="t('workflow.drawer.resultTab')" name="result">
          <div v-if="!record" class="wf-result-empty">{{ t("workflow.drawer.noRun") }}</div>
          <div v-else class="wf-result">
            <WorkflowApprovalAssignmentInfo :assignment="run?.approvals?.[node.id]" :pending="record.status === 'waiting_human'" />
            <div class="wf-result-row">
              <span class="wf-result-label">{{ t("workflow.drawer.result.status") }}</span>
              <span class="wf-result-state" :class="record.status">{{ t(`workflow.state.${record.status}`) }}</span>
            </div>
            <p v-if="record.branch" class="wf-result-meta">
              {{ t("workflow.drawer.result.branch") }}：{{ t(`workflow.edge.when.${record.branch}`) }}
            </p>
            <p v-if="record.skipped_by" class="wf-result-meta">
              {{ t("workflow.drawer.result.skippedBy") }}：{{ t("workflow.node.skippedBy", { id: record.skipped_by }) }}
            </p>
            <p v-if="record.started_at" class="wf-result-meta">{{ t("workflow.drawer.result.startedAt", { time: d(new Date(record.started_at), "short") }) }}</p>
            <p v-if="record.ended_at" class="wf-result-meta">{{ t("workflow.drawer.result.endedAt", { time: d(new Date(record.ended_at), "short") }) }}</p>
            <p v-if="record.usage" class="wf-result-meta">{{ t("workflow.drawer.result.usage", record.usage) }}</p>
            <div v-if="record.error" class="wf-result-error" role="alert">{{ record.error }}</div>

            <template v-if="record.summary">
              <span class="wf-result-label">{{ t("workflow.drawer.result.summary") }}</span>
              <p class="wf-result-text">{{ record.summary }}</p>
            </template>

            <template v-if="record.inputs && Object.keys(record.inputs).length">
              <span class="wf-result-label">{{ t("workflow.drawer.result.inputs") }}</span>
              <div class="wf-kv">
                <div v-for="(v, k) in record.inputs" :key="k" class="wf-kv-row">
                  <code>{{ k }}</code>
                  <span :title="v">{{ v }}</span>
                </div>
              </div>
            </template>

            <template v-if="record.human?.action">
              <span class="wf-result-label">{{ t("workflow.drawer.result.humanAction") }}</span>
              <div class="wf-kv">
                <div class="wf-kv-row">
                  <code>action</code>
                  <span>{{ record.human.action }}</span>
                </div>
                <div v-for="(v, k) in record.human.values ?? {}" :key="k" class="wf-kv-row">
                  <code>{{ k }}</code>
                  <span :title="v">{{ v }}</span>
                </div>
              </div>
            </template>

            <template v-if="record.dispatches?.length">
              <span class="wf-result-label">{{ t("workflow.drawer.result.dispatches") }}</span>
              <div v-for="(dp, i) in record.dispatches" :key="i" class="wf-dispatch">
                <div class="wf-dispatch-head">
                  <strong>{{ dp.candidate }}</strong>
                  <span class="wf-result-state" :class="dp.status">{{ t(`workflow.state.${dp.status}`) }}</span>
                </div>
                <p class="wf-dispatch-task" :title="dp.task">{{ dp.task }}</p>
                <p v-if="dp.summary" class="wf-result-text">{{ dp.summary }}</p>
                <p v-if="dp.error" class="wf-result-error" role="alert">{{ dp.error }}</p>
                <code v-if="dp.artifact" class="wf-result-path">{{ dp.artifact }}</code>
              </div>
            </template>

            <template v-if="record.artifact">
              <div class="wf-result-row">
                <span class="wf-result-label">{{ t("workflow.drawer.result.artifact") }}</span>
                <code class="wf-result-path">{{ record.artifact }}</code>
              </div>
              <button
                v-if="!artifactShown && workflowRunCan(run, 'can_read_artifacts', true, node.id)"
                type="button"
                class="wf-artifact-btn"
                :disabled="artifactLoading"
                @click="loadArtifact"
              >
                {{ artifactLoading ? t("workflow.drawer.result.artifactLoading") : t("workflow.drawer.result.loadArtifact") }}
              </button>
              <p v-if="artifactFailed" class="wf-result-error" role="alert">{{ t("workflow.drawer.result.artifactFailed") }}</p>
              <pre v-if="artifactShown" class="wf-artifact">{{ artifactText }}</pre>
            </template>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>
  </el-drawer>
</template>

<style scoped>
.wf-drawer {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.wf-drawer-tabs {
  flex: 1;
  min-height: 0;
}
.wf-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 4px 2px 16px;
}
.wf-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wf-field > label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-regular);
}
.wf-field-hint {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--el-text-color-placeholder);
}
.wf-agent-row {
  display: flex;
  align-items: center;
  gap: 9px;
  font-size: 13px;
  color: var(--el-text-color-primary);
}
.wf-agent-row .unknown {
  color: var(--el-color-danger);
  font-weight: 550;
}

.wf-candidate,
.wf-spec {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  padding: 8px;
}
.wf-candidate-row,
.wf-spec-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.wf-candidate-row > *,
.wf-spec-row > * {
  min-width: 0;
  flex: 1;
}
.wf-mini-btn {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.wf-mini-btn:hover {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.wf-mini-add {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  padding: 6px 12px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: border-color var(--pg-transition), color var(--pg-transition);
}
.wf-mini-add:hover {
  border-color: var(--el-border-color-hover);
  color: var(--el-text-color-primary);
}
.wf-nums {
  display: flex;
  gap: 12px;
}
.wf-num {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--el-text-color-secondary);
}
.wf-num.grow {
  flex: 1;
  min-width: 0;
}

.wf-remove {
  align-self: flex-start;
  margin-top: 4px;
  border: 1px solid var(--el-color-danger-light-5);
  background: transparent;
  color: var(--el-color-danger);
  border-radius: 8px;
  padding: 7px 14px;
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-remove:hover {
  background: var(--el-color-danger-light-9);
}

.wf-result {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 2px 16px;
}
.wf-result-empty {
  padding: 36px 8px;
  text-align: center;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}
.wf-result-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.wf-result-label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-regular);
}
.wf-result-state {
  font-size: 12px;
  font-weight: 600;
  border-radius: 999px;
  padding: 2px 10px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}
.wf-result-state.running {
  background: color-mix(in srgb, var(--pg-wf-state-running) 14%, transparent);
  color: var(--pg-wf-state-running);
}
.wf-result-state.waiting_human {
  background: color-mix(in srgb, var(--pg-wf-state-waiting-human) 16%, transparent);
  color: var(--pg-wf-state-waiting-human);
}
.wf-result-state.success {
  background: color-mix(in srgb, var(--pg-wf-state-success) 14%, transparent);
  color: var(--pg-wf-state-success);
}
.wf-result-state.failed {
  background: color-mix(in srgb, var(--pg-wf-state-failed) 12%, transparent);
  color: var(--pg-wf-state-failed);
}
.wf-result-state.rejected {
  background: color-mix(in srgb, var(--pg-wf-state-rejected) 12%, transparent);
  color: var(--pg-wf-state-rejected);
}
.wf-result-meta {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.wf-result-text {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.65;
  color: var(--el-text-color-regular);
  white-space: pre-wrap;
  word-break: break-word;
}
.wf-result-error {
  border-radius: 8px;
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  color: var(--el-color-danger);
  font-size: 12px;
  line-height: 1.6;
  padding: 9px 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
.wf-kv {
  display: flex;
  flex-direction: column;
  gap: 4px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  padding: 8px 10px;
}
.wf-kv-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
  font-size: 11.5px;
}
.wf-kv-row code {
  flex: none;
  font-family: var(--pg-mono);
  color: var(--el-color-primary);
}
.wf-kv-row span {
  min-width: 0;
  color: var(--el-text-color-regular);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-dispatch {
  display: flex;
  flex-direction: column;
  gap: 5px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  padding: 9px 11px;
}
.wf-dispatch-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 12.5px;
  color: var(--el-text-color-primary);
}
.wf-dispatch-task {
  margin: 0;
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-result-path {
  font-family: var(--pg-mono);
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-radius: 6px;
  padding: 2px 7px;
  align-self: flex-start;
}
.wf-artifact-btn {
  align-self: flex-start;
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  border-radius: 8px;
  padding: 6px 13px;
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-artifact-btn:hover {
  background: var(--el-fill-color-light);
}
.wf-artifact-btn:disabled {
  opacity: 0.6;
  cursor: wait;
}
.wf-artifact {
  margin: 0;
  max-height: 46vh;
  overflow: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-fill-color-extra-light);
  padding: 10px 12px;
  font-family: var(--pg-mono);
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
