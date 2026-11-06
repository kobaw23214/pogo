<script setup lang="ts">
import { computed, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import type { AgentWorkflowDefinition, AgentWorkflowDiagnostic, AgentWorkflowRun, AgentWorkflowVersionSummary } from "@/api/agentWorkflows";
import AgentWorkflowRunBadge from './AgentWorkflowRunBadge.vue';
import { workflowToolApprovalMode } from '@/agent/agentWorkflowEditor';
import WorkflowDiagnostics from '@/components/WorkflowDiagnostics.vue';

type ControlTab = "overview" | "versions" | "runs";
type ControlAction = { command: string; label: string; disabled: boolean; hint?: string };
const props = defineProps<{
  workflow: AgentWorkflowDefinition;
  run?: AgentWorkflowRun | null;
  preview?: AgentWorkflowVersionSummary | null;
  previewDefinition?: AgentWorkflowDefinition | null;
  versions: AgentWorkflowVersionSummary[];
  enabledVersion?: AgentWorkflowVersionSummary | null;
  versionsLoading: boolean;
  versionsError: string;
  hasMoreVersions: boolean;
  runs: AgentWorkflowRun[];
  tab: ControlTab;
  dirty: boolean;
  saving: boolean;
  busy: boolean;
  actions: ControlAction[];
  canRestore: boolean;
  operationError?: string;
  operationNotice?: string;
  diagnostics?: AgentWorkflowDiagnostic[];
}>();
const emit = defineEmits<{
  close: [];
  tab: [ControlTab];
  action: [string];
  version: [AgentWorkflowVersionSummary];
  restore: [];
  run: [string];
  draft: [];
  loadMore: [];
  retryVersions: [];
}>();
const { t, locale } = useI18n();
const tr = (key: string, values: Record<string, string | number> = {}) => t(`agentWorkflow.${key}`, values);
const panelId = useId();
const tabs = ["overview", "versions", "runs"] as const;
const tabLabels: Record<ControlTab, string> = { overview: "controlOverview", versions: "controlVersions", runs: "controlRuns" };
const tablist = ref<HTMLElement | null>(null);
const enabled = computed(() => {
  const active = props.workflow.enabled_version;
  if (!active) return undefined;
  return props.enabledVersion?.version_id === active ? props.enabledVersion : props.versions.find(version => version.is_enabled && version.version_id === active);
});
const approvalDefinition = computed(() => props.run?.definition || props.previewDefinition || props.workflow);
const approvalMode = computed(() => workflowToolApprovalMode(approvalDefinition.value));
const unpublished = computed(() => !!props.workflow.enabled_version && props.workflow.enabled_version !== props.workflow.definition_hash);
const primary = computed(() => ["trial", "enable"].flatMap(command => props.actions.find(action => action.command === command) || []));
const secondary = computed(() => ["settings", "edit", "validate", "start"].flatMap(command => props.actions.find(action => action.command === command) || []));
const management = computed(() => props.actions.filter(action => ["disable", "archive", "delete"].includes(action.command)));
const activeRecords = computed(() => props.runs.filter(record => ['queued', 'running', 'waiting_confirmation'].includes(record.status)));
const attentionRecord = computed(() => activeRecords.value.find(record => !record.cancel_requested && record.status === 'waiting_confirmation') || activeRecords.value.find(record => !record.cancel_requested && record.status === 'running') || activeRecords.value[0]);
const sourceLabel = (version: AgentWorkflowVersionSummary) => version.sources.includes("saved") ? tr("versionSourceSaved") : version.sources.includes("enabled") ? tr("versionSourceEnabled") : version.sources.includes("run") ? tr("versionSourceRun") : tr("versionSourceDraft");
const previewing = (version: AgentWorkflowVersionSummary) => props.preview?.revision === version.revision && props.preview.version_id === version.version_id;
const currentDraft = (version: AgentWorkflowVersionSummary) => version.revision === props.workflow.revision && version.version_id === props.workflow.definition_hash;
const currentEnabled = (version: AgentWorkflowVersionSummary) => enabled.value?.revision === version.revision && enabled.value.version_id === version.version_id;
const date = (value?: number) => {
  if (!value || !Number.isFinite(value)) return tr("versionTimeUnknown");
  return new Intl.DateTimeFormat(locale.value, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
};
const iconPaths: Record<string, string> = {
  settings: "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M9.5 3l-.6 2.2-2 .9-2-.7-2.5 4.2 1.6 1.5v2.2l-1.6 1.5L5 19l2-.7 2 .9.6 2.2h5l.6-2.2 2-.9 2 .7 2.5-4.2-1.6-1.5v-2.2l1.6-1.5L19 5.4l-2 .7-2-.9L14.5 3z",
  edit: "M14 5H5a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h3l4 3v-3h7a2 2 0 0 0 2-2v-5M15 3l3-1 3 3-1 3-7 7-4 1 1-4z",
  validate: "M9 4H5v17h14V4h-4M9 2h6v5H9zM8 13l3 3 5-6",
  start: "M8 4l12 8-12 8z",
  disable: "M8 5v14M16 5v14",
  archive: "M3 4h18v5H3zM5 9v12h14V9M9 13h6",
  delete: "M4 7h16M9 3h6l1 4H8l1-4ZM6 7l1 14h10l1-14M10 11v6M14 11v6",
};
function selectAction(action: ControlAction) { if (!action.disabled && !props.busy) emit("action", action.command); }
function tabKey(event: KeyboardEvent) {
  const current = tabs.indexOf(props.tab);
  const next = event.key === "ArrowRight" ? (current + 1) % tabs.length : event.key === "ArrowLeft" ? (current + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1;
  if (next < 0) return;
  event.preventDefault();
  emit("tab", tabs[next]!);
  tablist.value?.querySelectorAll<HTMLButtonElement>("[role=tab]")[next]?.focus();
}
function escape(event: KeyboardEvent) {
  if (event.key !== "Escape" || event.defaultPrevented || event.isComposing) return;
  event.preventDefault(); event.stopPropagation(); emit("close");
}
</script>

<template>
  <section class="aw-control-card" :aria-labelledby="`${panelId}-title`" data-testid="workflow-control-card" @wheel.stop @pointerdown.stop @keydown="escape">
    <header class="aw-control-head">
      <div class="aw-control-title">
        <span class="aw-control-mark" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3l9 5-9 5-9-5zM3 12l9 5 9-5M3 16l9 5 9-5" /></svg></span>
        <div><h3 :id="`${panelId}-title`">{{ tr('controlTitle') }}</h3><p :title="workflow.name">{{ workflow.name }}</p></div>
      </div>
      <button type="button" class="aw-control-icon-button" :aria-label="tr('controlClose')" :title="tr('controlClose')" @click="emit('close')"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg></button>
    </header>

    <div ref="tablist" class="aw-control-tabs" role="tablist" :aria-label="tr('controlTabs')" @keydown="tabKey">
      <button v-for="item in tabs" :id="`${panelId}-${item}`" :key="item" type="button" role="tab" :aria-selected="tab === item" :aria-controls="`${panelId}-panel`" :tabindex="tab === item ? 0 : -1" :data-testid="`tab-${item}`" @click="emit('tab', item)">{{ tr(tabLabels[item]) }}<span v-if="item === 'runs' && runs.length" class="aw-tab-count">{{ runs.length }}</span></button>
    </div>

    <div v-if="preview || run" class="aw-view-context">
      <div><span class="aw-context-dot" /><span>{{ preview ? tr('versionPreviewLabel', { n: preview.revision }) : tr('runSnapshotLabel', { n: run!.definition.revision }) }}</span></div>
      <button type="button" :disabled="busy" @click="emit('draft')">{{ tr(workflow.deleted ? 'deletedDefinition' : 'backToDraft') }}<span aria-hidden="true">↗</span></button>
    </div>

    <div :id="`${panelId}-panel`" class="aw-control-body" role="tabpanel" :aria-labelledby="`${panelId}-${tab}`" tabindex="0">
      <p v-if="operationError" class="aw-operation-feedback error" role="alert">{{ operationError }}</p>
      <p v-else-if="operationNotice" class="aw-operation-feedback" role="status">{{ operationNotice }}</p>
      <WorkflowDiagnostics v-if="diagnostics?.length" class="aw-operation-diagnostics" :diagnostics="diagnostics" domain="agentWorkflow" />
      <template v-if="tab === 'overview'">
        <button v-if="attentionRecord" type="button" class="aw-active-run-summary" :disabled="busy" data-testid="workflow-control-active-run" @click="emit('run', attentionRecord.id)"><AgentWorkflowRunBadge :status="attentionRecord.cancel_requested ? 'cancelling' : attentionRecord.status" :count="activeRecords.length" /><span>{{ tr('viewRun') }} ↗</span></button>
        <p v-if="workflow.deleted" class="aw-panel-intro">{{ tr('deletedHint') }}</p>
        <section class="aw-draft-summary" :aria-label="tr(workflow.deleted ? 'deletedDefinition' : 'currentDraft')">
          <div class="aw-summary-heading"><span>{{ tr(workflow.deleted ? 'deletedDefinition' : 'currentDraft') }}</span><span v-if="!workflow.deleted" class="aw-save-state" :class="{ changed: dirty }"><span class="aw-state-dot" />{{ t(saving ? 'agentWorkflowCanvas.saving' : dirty ? 'agentWorkflowCanvas.unsaved' : 'agentWorkflowCanvas.saved') }}</span></div>
          <div class="aw-revision-row"><strong>{{ tr(workflow.deleted ? 'deleted' : 'versionShort', { n: workflow.revision }) }}</strong><span>{{ tr('versionNodeCount', { n: workflow.steps.length }) }}</span></div>
          <p class="aw-summary-time">{{ date(workflow.updated_at) }}</p>
          <p v-if="unpublished || dirty" class="aw-unpublished"><span />{{ tr(dirty ? 'draftNeedsSave' : 'draftNotEnabled') }}</p>
          <p v-else-if="workflow.enabled_version" class="aw-matching"><span />{{ tr('draftMatchesEnabled') }}</p>
        </section>

        <section class="aw-approval-summary" data-testid="workflow-control-approval-mode">
          <span>{{ t(run || preview ? 'agentWorkflowCanvas.toolApprovalFrozenTitle' : 'agentWorkflowCanvas.toolApprovalSavedDraftTitle') }}</span>
          <strong>{{ t(`policyPicker.modes.${approvalMode}.label`) }}</strong>
          <p>{{ t(`agentWorkflowCanvas.toolApprovalDescriptions.${approvalMode}`) }}</p>
          <small>{{ t('agentWorkflowCanvas.toolApprovalInheritance') }}</small>
          <small>{{ t(run || preview ? 'agentWorkflowCanvas.toolApprovalFrozen' : 'agentWorkflowCanvas.toolApprovalSavedDraftHint') }}</small>
          <button v-if="!run && !preview && !workflow.deleted" type="button" :disabled="busy" @click="emit('action', 'settings')">{{ tr('settings') }} ↗</button>
        </section>

        <button v-if="enabled" type="button" class="aw-enabled-summary" :disabled="busy" @click="emit('version', enabled)">
          <span class="aw-enabled-dot" /><span class="aw-enabled-label">{{ tr('currentEnabled') }}<strong>{{ tr('versionShort', { n: enabled.revision }) }}</strong></span><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 5 7 7-7 7" /></svg>
        </button>
        <div v-else class="aw-enabled-summary is-empty"><span class="aw-enabled-dot muted" /><span class="aw-enabled-label">{{ tr('currentEnabled') }}<strong>{{ tr(workflow.enabled_version ? 'enabledVersionUnresolved' : 'notEnabled') }}</strong></span></div>
        <p v-if="workflow.enabled_version" class="aw-small-hint">{{ t('agentWorkflowCanvas.toolApprovalEnabledHint') }}</p>

        <div v-if="primary.length" class="aw-primary-actions">
          <button v-for="action in primary" :key="action.command" type="button" :class="{ 'is-primary': action.command === 'enable' }" :disabled="action.disabled || busy" :title="dirty ? tr('saveBeforeAction') : action.label" :data-command="action.command" @click="selectAction(action)">
            <svg v-if="action.command === 'trial'" viewBox="0 0 24 24" aria-hidden="true"><path d="m8 4 12 8-12 8z" /></svg><svg v-else viewBox="0 0 24 24" aria-hidden="true"><path d="m7 11 5-5 5 5M12 6v13M4 4h16" /></svg>
            <span>{{ action.label }}</span>
          </button>
        </div>
        <p v-if="dirty" class="aw-small-hint">{{ tr('saveBeforeAction') }}</p>
        <div v-if="secondary.length" class="aw-secondary-actions">
          <button v-for="action in secondary" :key="action.command" type="button" :disabled="action.disabled || busy" :data-command="action.command" @click="selectAction(action)"><svg viewBox="0 0 24 24" aria-hidden="true"><path :d="iconPaths[action.command]" /></svg><span>{{ action.label }}</span></button>
        </div>
        <details v-if="management.length" class="aw-management">
          <summary>{{ tr('managementActions') }}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m7 10 5 5 5-5" /></svg></summary>
          <div><button v-for="action in management" :key="action.command" type="button" :class="{ 'is-danger': ['archive', 'delete'].includes(action.command) }" :disabled="action.disabled || busy" :title="action.hint || action.label" :data-command="action.command" @click="selectAction(action)"><svg viewBox="0 0 24 24" aria-hidden="true"><path :d="iconPaths[action.command]" /></svg><span>{{ action.label }}</span></button></div>
        </details>
      </template>

      <template v-else-if="tab === 'versions'">
        <p class="aw-panel-intro">{{ tr('versionHistoryHint') }}</p>
        <div v-if="versionsError" class="aw-control-error" role="alert"><p>{{ versionsError }}</p><button type="button" :disabled="versionsLoading || busy" @click="emit('retryVersions')">{{ tr('retryVersions') }}</button></div>
        <div v-if="versionsLoading && !versions.length" class="aw-loading-state" role="status"><span class="aw-loading-dot" />{{ tr('versionsLoading') }}</div>
        <div v-else-if="!versions.length && !versionsError" class="aw-control-empty"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 11a9 9 0 1 1 2 7M3 5v6h6M12 7v5l3 2" /></svg><strong>{{ tr('versionsEmpty') }}</strong><p>{{ tr('versionsEmptyHint') }}</p></div>
        <div v-else class="aw-version-list">
          <article v-for="version in versions" :key="`${version.revision}:${version.version_id}`" class="aw-version-card" :class="{ selected: previewing(version), enabled: currentEnabled(version) }" data-testid="version-card" :data-revision="version.revision">
            <div class="aw-version-heading"><strong>{{ tr('versionShort', { n: version.revision }) }}</strong><span v-if="currentEnabled(version)" class="aw-version-badge is-enabled">{{ tr('currentEnabled') }}</span><span v-if="currentDraft(version)" class="aw-version-badge">{{ tr('currentDraft') }}</span></div>
            <p class="aw-version-name" :title="version.name">{{ version.name }}</p>
            <div class="aw-version-meta"><time>{{ date(version.updated_at) }}</time><span>{{ tr('versionNodeCount', { n: version.step_count }) }}</span></div>
            <div class="aw-version-bottom"><span>{{ sourceLabel(version) }}</span><button type="button" :disabled="busy" :aria-label="tr('previewVersionAction', { n: version.revision })" :aria-pressed="previewing(version)" data-testid="workflow-preview-version" @click="emit('version', version)">{{ tr(previewing(version) ? 'viewingVersion' : 'previewVersion') }}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 5 7 7-7 7" /></svg></button></div>
          </article>
        </div>
        <button v-if="hasMoreVersions" type="button" class="aw-load-more" :disabled="versionsLoading || busy" @click="emit('loadMore')">{{ tr(versionsLoading ? 'versionsLoading' : 'loadEarlierVersions') }}</button>
      </template>

      <template v-else>
        <p class="aw-panel-intro">{{ tr('runHistoryHint') }}</p>
        <section v-if="run" class="aw-run-details">
          <span>{{ tr('runId') }}</span><code>{{ run.id }}</code>
          <p v-if="run.node_test">{{ tr('nodeTestTarget', { name: run.definition.steps.find(step => step.id === run!.node_test!.step_id)?.title || run.node_test.step_id }) }}</p>
          <p v-if="run.retry_of">{{ tr('retryOf') }} <button type="button" :disabled="busy" @click="emit('run', run.retry_of!)">{{ run.retry_of.slice(-8) }}</button></p>
          <details v-if="run.inputs && Object.keys(run.inputs).length"><summary>{{ tr('inputsSnapshot') }}</summary><dl><div v-for="(value,key) in run.inputs" :key="key"><dt>{{ key }}</dt><dd>{{ value }}</dd></div></dl></details>
        </section>
        <div v-if="!runs.length" class="aw-control-empty"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 7 8 5-8 5zM21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0" /></svg><strong>{{ tr('noRuns') }}</strong><p>{{ tr('runsEmptyHint') }}</p></div>
        <div v-else class="aw-run-list">
          <button v-for="record in runs" :key="record.id" type="button" class="aw-run-card" :class="{ selected: run?.id === record.id }" :aria-pressed="run?.id === record.id" :disabled="busy" :data-run-id="record.id" data-testid="workflow-run-card" @click="emit('run', record.id)"><span class="aw-run-heading"><strong>{{ tr(record.node_test ? 'nodeTestLabel' : record.trial ? 'testLabel' : 'formalRun') }}</strong><AgentWorkflowRunBadge :status="record.cancel_requested && ['queued', 'running', 'waiting_confirmation'].includes(record.status) ? 'cancelling' : record.status" /></span><span class="aw-run-meta"><time>{{ date(record.created_at) }}</time><span>{{ tr('versionShort', { n: record.definition.revision }) }}</span></span><span class="aw-run-view">{{ tr(run?.id === record.id ? 'viewingRun' : 'viewRun') }}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 5 7 7-7 7" /></svg></span></button>
        </div>
      </template>
    </div>

    <footer v-if="tab === 'versions' && preview && !workflow.deleted" class="aw-restore-footer">
      <div><span>{{ tr('versionPreviewLabel', { n: preview.revision }) }}</span><span class="aw-readonly-tag">{{ tr('readOnlyVersion') }}</span></div>
      <button type="button" class="aw-restore-button" :disabled="!canRestore || busy" data-testid="workflow-restore-version" @click="emit('restore')"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 11a9 9 0 1 1 2 7M3 5v6h6M12 7v5l3 2" /></svg>{{ tr('restoreAsDraft') }}</button>
      <p>{{ tr('restoreDraftHint') }}</p>
    </footer>
  </section>
</template>

<style scoped>
.aw-control-card{width:300px;max-width:calc(100% - 92px);max-height:calc(100% - var(--af-top-inset,88px) - 20px);display:flex;flex-direction:column;min-height:0;box-sizing:border-box;overflow:hidden;background:var(--el-bg-color-overlay);color:var(--el-text-color-primary);border:1px solid var(--el-border-color-light);border-radius:14px;box-shadow:0 10px 38px rgb(0 0 0 / 13%),0 2px 7px rgb(0 0 0 / 5%);font-size:12px;line-height:1.5}
.aw-control-card *{box-sizing:border-box}
.aw-control-card svg{width:16px;height:16px;flex:none;fill:none;stroke:currentColor;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
.aw-control-card button{appearance:none;display:inline-flex;align-items:center;justify-content:center;gap:6px;min-width:0;font:inherit;cursor:pointer;color:inherit;border:1px solid var(--el-border-color);border-radius:8px;background:var(--el-bg-color);transition:background-color 120ms,border-color 120ms,color 120ms}
.aw-control-card button:hover:not(:disabled){background:var(--el-fill-color-light);border-color:var(--el-color-primary-light-5);color:var(--el-color-primary)}
.aw-control-card button:disabled{opacity:.45;cursor:default}
.aw-control-card button:focus-visible,.aw-management>summary:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}
.aw-control-card p{margin:0}
.aw-control-head{display:flex;align-items:flex-start;gap:8px;flex:none;padding:14px 13px 11px}
.aw-control-title{display:flex;align-items:center;gap:9px;flex:1;min-width:0}
.aw-control-title>div{min-width:0}
.aw-control-title h3{font-size:13px;line-height:1.4;font-weight:650;margin:0 0 2px}
.aw-control-title p{font-size:11px;color:var(--el-text-color-secondary);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.aw-control-mark{display:grid;place-items:center;flex:none;width:32px;height:32px;border-radius:9px;background:var(--el-color-primary-light-9);color:var(--el-color-primary)}
.aw-control-mark svg{width:19px;height:19px}
.aw-control-card .aw-control-icon-button{border:0;background:none;padding:5px;width:26px;height:26px;color:var(--el-text-color-secondary);border-radius:7px}
.aw-control-icon-button svg{width:15px;height:15px}
.aw-control-tabs{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:3px;flex:none;margin:0 13px 12px;padding:3px;background:var(--el-fill-color-light);border-radius:9px}
.aw-control-tabs button{position:relative;min-height:31px;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary);font-size:12px}
.aw-control-tabs button[aria-selected=true]{background:var(--el-bg-color);color:var(--el-text-color-primary);box-shadow:0 1px 4px rgb(0 0 0 / 8%);font-weight:600}
.aw-tab-count{display:inline-grid;place-items:center;min-width:16px;height:16px;padding:0 4px;border-radius:5px;background:var(--el-fill-color);color:var(--el-text-color-secondary);font-size:10px}
.aw-view-context{display:flex;align-items:center;justify-content:space-between;gap:8px;flex:none;margin:0 13px 10px;padding:8px 9px;border:1px solid var(--el-color-info-light-7);border-radius:8px;background:var(--el-color-info-light-9);font-size:10px}
.aw-view-context>div{display:flex;align-items:center;gap:5px;min-width:0}
.aw-context-dot{width:5px;height:5px;border-radius:50%;background:var(--el-color-info);flex:none}
.aw-view-context button{border:0;background:none;padding:0;font-size:10px;color:var(--el-color-primary);white-space:nowrap}
.aw-control-body{min-height:0;overflow:auto;overscroll-behavior:contain;scrollbar-width:thin;padding:0 13px 13px;outline:none}
.aw-operation-feedback{margin:0 0 10px!important;padding:8px 9px;border:1px solid var(--el-color-success-light-7);border-radius:7px;background:var(--el-color-success-light-9);color:var(--el-color-success);font-size:11px;line-height:1.6}.aw-operation-feedback.error{border-color:var(--el-color-danger-light-7);background:var(--el-color-danger-light-9);color:var(--el-color-danger)}.aw-operation-diagnostics{padding:0 0 0 16px;margin:0 0 12px;color:var(--el-color-danger);font-size:11px;line-height:1.7}.aw-operation-diagnostics li+li{margin-top:7px}.aw-operation-diagnostics p{font-weight:400}
.aw-draft-summary{padding:11px 12px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-fill-color-extra-light)}
.aw-control-card .aw-active-run-summary{display:flex;align-items:center;justify-content:space-between;gap:8px;width:100%;padding:8px;margin-bottom:10px;border-color:var(--el-border-color-light);background:var(--el-fill-color-extra-light);font-size:10px}.aw-active-run-summary>span:last-child{color:var(--el-color-primary)}
.aw-summary-heading{display:flex;align-items:center;justify-content:space-between;gap:8px;font-size:11px;color:var(--el-text-color-secondary)}
.aw-save-state{display:flex;align-items:center;gap:4px;font-size:10px;white-space:nowrap}
.aw-state-dot,.aw-unpublished>span,.aw-matching>span{width:5px;height:5px;flex:none;border-radius:50%;background:var(--el-color-success)}
.aw-save-state.changed .aw-state-dot,.aw-unpublished>span{background:var(--el-color-warning)}
.aw-revision-row{display:flex;align-items:baseline;justify-content:space-between;gap:8px;margin-top:5px}
.aw-revision-row strong{font-size:23px;line-height:1.35;font-weight:650;letter-spacing:-.7px}
.aw-revision-row>span{font-size:10px;color:var(--el-text-color-secondary)}
.aw-control-card .aw-summary-time{margin-top:3px;font-size:10px;color:var(--el-text-color-placeholder)}
.aw-control-card .aw-unpublished,.aw-control-card .aw-matching{display:flex;align-items:center;gap:5px;margin-top:9px;padding-top:8px;border-top:1px solid var(--el-border-color-extra-light);font-size:10px;color:var(--el-text-color-secondary)}
.aw-control-card .aw-unpublished{color:var(--el-color-warning)}
.aw-enabled-summary{display:flex!important;align-items:center;gap:8px!important;width:100%;margin-top:8px;padding:9px 10px!important;min-height:42px;border-color:var(--el-border-color-extra-light)!important;text-align:left!important}
.aw-enabled-dot{width:6px;height:6px;background:var(--el-color-success);border-radius:50%;flex:none}
.aw-enabled-dot.muted{background:var(--el-text-color-placeholder)}
.aw-enabled-label{display:flex;align-items:center;justify-content:space-between;gap:10px;flex:1;font-size:10px;color:var(--el-text-color-secondary)}
.aw-enabled-label strong{color:var(--el-text-color-primary);font-size:11px;font-weight:600}
.aw-enabled-summary>svg{width:12px;height:12px;color:var(--el-text-color-placeholder)}
.aw-enabled-summary.is-empty{border:1px solid var(--el-border-color-extra-light);border-radius:8px;background:var(--el-bg-color)}
.aw-primary-actions{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:7px;margin-top:13px}
.aw-primary-actions button{min-height:36px;padding:7px 8px;font-size:11px;font-weight:550}
.aw-primary-actions .is-primary,.aw-restore-button{background:var(--el-color-primary)!important;border-color:var(--el-color-primary)!important;color:var(--el-color-white)!important}
.aw-primary-actions .is-primary:hover:not(:disabled),.aw-restore-button:hover:not(:disabled){background:var(--el-color-primary-light-3)!important;border-color:var(--el-color-primary-light-3)!important;color:var(--el-color-white)!important}
.aw-small-hint{font-size:10px;color:var(--el-text-color-secondary);margin-top:7px!important}
.aw-secondary-actions{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:6px;margin-top:10px;padding-top:11px;border-top:1px solid var(--el-border-color-extra-light)}
.aw-secondary-actions button{justify-content:flex-start;min-height:35px;padding:7px 8px;border-color:transparent;background:transparent;font-size:11px;text-align:left}
.aw-secondary-actions button svg{width:14px;height:14px;color:var(--el-text-color-secondary)}
.aw-approval-summary{display:flex;flex-direction:column;gap:6px;margin:10px 0;padding:11px;border:1px solid var(--el-border-color-lighter);border-radius:8px;background:var(--el-fill-color-extra-light);font-size:11px}.aw-approval-summary>span,.aw-approval-summary small{color:var(--el-text-color-secondary)}.aw-approval-summary strong{color:var(--el-text-color-primary);font-size:12px}.aw-approval-summary p{margin:0;line-height:1.6;color:var(--el-text-color-regular)}.aw-approval-summary small{line-height:1.6}.aw-approval-summary button{align-self:flex-start;font-size:11px;padding:4px 7px}
.aw-management{margin-top:10px;padding-top:9px;border-top:1px solid var(--el-border-color-extra-light)}
.aw-management>summary{display:flex;align-items:center;justify-content:space-between;min-height:29px;cursor:pointer;list-style:none;color:var(--el-text-color-secondary);font-size:11px;border-radius:5px}
.aw-management>summary::-webkit-details-marker{display:none}
.aw-management>summary svg{width:13px;height:13px;transition:transform 120ms}
.aw-management[open]>summary svg{transform:rotate(180deg)}
.aw-management>div{display:grid;gap:4px;padding-top:4px}
.aw-management button{justify-content:flex-start;padding:7px 8px;border-color:transparent;background:none;font-size:11px;text-align:left}
.aw-management .is-danger{color:var(--el-color-danger)}
.aw-management .is-danger:hover:not(:disabled){border-color:var(--el-color-danger-light-7);background:var(--el-color-danger-light-9);color:var(--el-color-danger)}
.aw-panel-intro{font-size:11px;color:var(--el-text-color-secondary);line-height:1.65;margin:0 1px 10px!important}
.aw-control-error{padding:10px;border:1px solid var(--el-color-danger-light-7);border-radius:8px;background:var(--el-color-danger-light-9);color:var(--el-color-danger);font-size:11px;margin-bottom:10px}
.aw-control-error button{margin-top:8px;padding:4px 9px;font-size:10px}
.aw-loading-state{display:flex;align-items:center;justify-content:center;gap:8px;min-height:124px;font-size:11px;color:var(--el-text-color-secondary)}
.aw-loading-dot{width:7px;height:7px;background:var(--el-color-primary);border-radius:50%;animation:aw-loading-pulse 1s ease-in-out infinite alternate}
.aw-control-empty{display:flex;flex-direction:column;align-items:center;gap:8px;min-height:156px;padding:25px 13px;text-align:center;color:var(--el-text-color-secondary)}
.aw-control-empty>svg{width:28px;height:28px;color:var(--el-text-color-placeholder);stroke-width:1.4}
.aw-control-empty strong{font-size:12px;color:var(--el-text-color-regular);font-weight:550}
.aw-control-empty p{max-width:210px;font-size:11px;line-height:1.7}
.aw-version-list,.aw-run-list{display:flex;flex-direction:column;gap:8px}
.aw-version-card{padding:10px 11px;border:1px solid var(--el-border-color-light);border-radius:9px;background:var(--el-bg-color);transition:border-color 120ms,box-shadow 120ms}
.aw-version-card.selected{border-color:var(--el-color-primary);box-shadow:0 0 0 1px var(--el-color-primary-light-8)}
.aw-version-heading{display:flex;align-items:center;gap:5px;flex-wrap:wrap}
.aw-version-heading>strong{margin-right:auto;font-size:15px;font-weight:650;letter-spacing:-.3px}
.aw-version-badge{padding:1px 5px;border-radius:4px;color:var(--el-text-color-secondary);background:var(--el-fill-color);font-size:9px;line-height:1.6;white-space:nowrap}
.aw-version-badge.is-enabled{color:var(--el-color-success);background:var(--el-color-success-light-9)}
.aw-control-card .aw-version-name{margin-top:5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--el-text-color-regular);font-size:11px}
.aw-version-meta{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-top:4px;font-size:10px;color:var(--el-text-color-placeholder)}
.aw-version-bottom{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-top:9px;padding-top:8px;border-top:1px solid var(--el-border-color-extra-light);font-size:10px;color:var(--el-text-color-secondary)}
.aw-version-bottom button{padding:0;border:0;background:none;font-size:10px;color:var(--el-color-primary)}
.aw-version-bottom button svg{width:11px;height:11px}
.aw-version-bottom button[aria-pressed=true]{font-weight:600}
.aw-load-more{width:100%;min-height:32px;margin-top:10px;padding:5px 8px;color:var(--el-text-color-secondary)!important;font-size:11px!important}
.aw-run-card{display:flex!important;flex-direction:column;align-items:stretch!important;gap:0!important;width:100%;padding:10px 11px!important;border-color:var(--el-border-color-light)!important;text-align:left}
.aw-run-card.selected{border-color:var(--el-color-primary)!important;box-shadow:0 0 0 1px var(--el-color-primary-light-8)}
.aw-run-heading,.aw-run-meta{display:flex;align-items:center;justify-content:space-between;gap:8px}
.aw-run-heading strong{font-size:12px;font-weight:600}
.aw-run-status{padding:1px 5px;border-radius:4px;background:var(--el-fill-color);font-size:10px;font-weight:400;color:var(--el-text-color-secondary)}
.aw-run-status.completed{color:var(--el-color-success);background:var(--el-color-success-light-9)}
.aw-run-status.running,.aw-run-status.queued{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}
.aw-run-status.waiting_confirmation{color:var(--el-color-warning);background:var(--el-color-warning-light-9)}
.aw-run-status.failed,.aw-run-status.interrupted{color:var(--el-color-danger);background:var(--el-color-danger-light-9)}
.aw-run-meta{margin-top:7px;font-size:10px;color:var(--el-text-color-placeholder)}
.aw-run-details{padding:10px;margin-bottom:12px;border:1px solid var(--el-border-color-extra-light);border-radius:8px;font-size:10px;color:var(--el-text-color-secondary)}.aw-run-details>span{display:block;margin-bottom:4px}.aw-run-details code{font-size:9px;overflow-wrap:anywhere}.aw-run-details>p{margin-top:7px}.aw-run-details>p button{border:0;background:none;padding:0;color:var(--el-color-primary)}.aw-run-details details{margin-top:8px}.aw-run-details summary{cursor:pointer}.aw-run-details dl{margin-bottom:0}.aw-run-details dl>div+div{margin-top:6px}.aw-run-details dt{font-weight:600}.aw-run-details dd{margin:2px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}
.aw-run-view{display:flex;align-items:center;justify-content:flex-end;gap:3px;margin-top:9px;padding-top:8px;border-top:1px solid var(--el-border-color-extra-light);font-size:10px;color:var(--el-color-primary)}
.aw-run-view svg{width:11px;height:11px}
.aw-restore-footer{flex:none;padding:11px 13px 12px;background:var(--el-bg-color);border-top:1px solid var(--el-border-color-light)}
.aw-restore-footer>div{display:flex;align-items:center;justify-content:space-between;gap:6px;font-size:11px;color:var(--el-text-color-secondary);margin-bottom:8px}
.aw-readonly-tag{font-size:9px;padding:1px 5px;background:var(--el-fill-color);border-radius:4px}
.aw-control-card .aw-restore-button{width:100%;min-height:35px;padding:6px 10px;font-size:11px;font-weight:550}
.aw-restore-footer p{margin-top:7px;font-size:10px;color:var(--el-text-color-secondary);line-height:1.6}
@keyframes aw-loading-pulse{to{opacity:.3;transform:scale(.75)}}
@media(prefers-reduced-motion:reduce){.aw-control-card button,.aw-version-card,.aw-management>summary svg{transition:none}
.aw-loading-dot{animation:none}}
</style>
