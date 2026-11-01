<script setup lang="ts">
import { computed } from "vue";
import { Handle, Position } from "@vue-flow/core";
import { useI18n } from "vue-i18n";
import type { AgentWorkflowCanvasNodeData } from "@/agent/agentWorkflowGraph";

const props = defineProps<{ data: AgentWorkflowCanvasNodeData }>();
const emit = defineEmits<{ insert: [id: string]; inspect: [id: string] }>();
const { t, locale } = useI18n();
const title = computed(() => props.data.title || t("agentWorkflowGraph." + props.data.kind));
const kind = computed(() => t("agentWorkflowGraph." + props.data.kind));
const description = computed(() => props.data.repeatProgress ? t(props.data.kind === 'loop' ? 'agentWorkflowGraph.loopProgress' : 'agentWorkflowGraph.iterationProgress', {completed:props.data.repeatProgress.completed,total:props.data.repeatProgress.total,failed:props.data.repeatProgress.failed}) : (props.data.kind === 'code' && props.data.status === 'running' ? t('agentWorkflowGraph.codeRunning', { language:props.data.codeLanguage === 'javascript' ? 'JavaScript' : 'Python' }) : props.data.description) || t("agentWorkflowGraph." + (['input', 'output', 'condition', 'parallel', 'join', 'iteration', 'loop'].includes(props.data.kind) ? props.data.kind + 'Hint' : "emptyTask")));
const duration = computed(() => {
  if (props.data.elapsedMs == null) return "";
  const seconds = Math.round(props.data.elapsedMs / 1000);
  return seconds < 60 ? seconds + "s" : Math.floor(seconds / 60) + "m " + seconds % 60 + "s";
});
const activity = computed(() => props.data.activity);
const needsAction = computed(() => props.data.status === 'waiting_confirmation' || props.data.status === 'running' && activity.value?.phase === 'approval');
const stateLabel = computed(() => props.data.status === 'running' && activity.value?.phase === 'approval' ? t('agentWorkflowGraph.activityPhases.approval') : props.data.status ? t(`agentWorkflow.status.${props.data.status}`) : '');
const activityToolKey = computed(() => {
  const name = activity.value?.tool_name;
  if (!name) return '';
  return ['read', 'write', 'edit', 'bash', 'browser'].includes(name) ? name : name.startsWith('docs_') || name === 'office' ? 'office' : name.startsWith('mcp') ? 'mcp' : '';
});
const activityTool = computed(() => activityToolKey.value ? t(`agentWorkflowGraph.activityTools.${activityToolKey.value}`) : activity.value?.tool_name || '');
const activityLabel = computed(() => {
  const current = activity.value;
  if (!current) return t('agentWorkflowGraph.activityAwaiting');
  if (!current.observed && !['queued', 'preparing', 'completed', 'failed', 'interrupted', 'cancelled', 'skipped'].includes(current.phase)) return t('agentWorkflowGraph.activityAwaiting');
  if (current.phase === 'tool' && activityToolKey.value) return t(`agentWorkflowGraph.activityDoingTools.${activityToolKey.value}`);
  if (current.phase === 'tool' && activityTool.value) return t('agentWorkflowGraph.activityUsingTool', { tool: activityTool.value });
  if (current.phase === 'tool_input' && activityTool.value) return t('agentWorkflowGraph.activityPreparingTool', { tool: activityTool.value });
  if (current.phase === 'approval' && current.approval_count) return t('agentWorkflowGraph.activityApprovals', { n: current.approval_count });
  const known = ['queued', 'preparing', 'model', 'thinking', 'answer', 'tool_input', 'tool', 'approval', 'completed', 'failed', 'interrupted', 'cancelled', 'skipped'];
  return known.includes(current.phase) ? t(`agentWorkflowGraph.activityPhases.${current.phase}`) : t('agentWorkflowGraph.activityAwaiting');
});
const activityMetrics = computed(() => {
  const current = activity.value;
  if (!current?.observed) return [];
  const result: string[] = [];
  if (current.turn && current.turn > 0) result.push(t('agentWorkflowGraph.activityTurn', { n: current.turn }));
  if (current.tool_count !== undefined) result.push(t('agentWorkflowGraph.activityToolCount', { n: current.tool_count }));
  if (current.pending_tool_count) result.push(t('agentWorkflowGraph.activityToolsPending', { n: current.pending_tool_count }));
  return result;
});
const activityTime = computed(() => {
  const value = activity.value;
  if (!value?.observed || !value.updated_at || !Number.isFinite(value.updated_at)) return '';
  return new Intl.DateTimeFormat(locale.value, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(new Date(value.updated_at));
});
</script>

<template>
  <article class="aw-canvas-node" :class="[data.kind, data.status, { selected: data.active, updated: data.updated, 'aw-node-needs-action': needsAction, 'aw-node-stale': activity?.connected === false }]" :data-step-id="data.id" :aria-label="stateLabel ? `${title} · ${stateLabel}` : title">
    <Handle v-if="data.kind !== 'input'" id="in" type="target" :position="Position.Left" :connectable="data.editable" class="aw-port" :title="t('agentWorkflowGraph.inputPortHint')" :aria-label="t('agentWorkflowGraph.inputPortHint')" />
    <div class="aw-node-top">
      <span class="aw-node-icon" aria-hidden="true">
        <svg v-if="data.kind === 'confirm'" viewBox="0 0 24 24"><circle cx="12" cy="8" r="3" /><path d="M5 20v-2a7 7 0 0 1 14 0v2M9 15l3 3 5-5" /></svg>
        <svg v-else-if="data.kind === 'agent'" viewBox="0 0 24 24"><rect x="4" y="6" width="16" height="14" rx="4" /><path d="M12 2v4M8 11h.01M16 11h.01M8 16h8" /></svg>
        <svg v-else-if="data.kind === 'code'" viewBox="0 0 24 24"><path d="m8 5-6 7 6 7m8-14 6 7-6 7m-3-16-2 18" /></svg>
        <svg v-else-if="data.kind === 'iteration'" viewBox="0 0 24 24"><path d="M4 5h12l4 4-4 4M20 19H8l-4-4 4-4M4 5v4M20 19v-4" /></svg>
        <svg v-else-if="data.kind === 'loop'" viewBox="0 0 24 24"><path d="M19 8a8 8 0 1 0 1 8M19 3v6h-6" /></svg>
        <svg v-else-if="data.kind === 'condition'" viewBox="0 0 24 24"><path d="m12 2 10 10-10 10L2 12zM9 9a3 3 0 1 1 4 3v2M12 17h.01" /></svg>
        <svg v-else-if="data.kind === 'parallel'" viewBox="0 0 24 24"><path d="M3 12h7V5h10M10 12v7h10M17 2l3 3-3 3M17 16l3 3-3 3" /></svg>
        <svg v-else-if="data.kind === 'join'" viewBox="0 0 24 24"><path d="M3 5h7v14H3M10 12h11M18 9l3 3-3 3" /></svg>
        <svg v-else-if="data.kind === 'input'" viewBox="0 0 24 24"><path d="M4 12h12m-5-5 5 5-5 5M17 4h3v16h-3" /></svg>
        <svg v-else viewBox="0 0 24 24"><path d="M14 3H6v18h12V7zM14 3v5h5M9 12h6M9 16h6" /></svg>
        <span v-if="data.status === 'running' || needsAction || data.status === 'failed' || data.status === 'interrupted'" class="aw-node-icon-status" :class="{ spinning: data.status === 'running' && !needsAction && activity?.connected !== false }" :title="stateLabel"><svg v-if="needsAction" viewBox="0 0 12 12"><path d="M4 2v3m2-4v4m2-3v4l1-1 1 1-2 4H5L2 6l1-1 1 1V2" /></svg><svg v-else-if="data.status === 'failed' || data.status === 'interrupted'" viewBox="0 0 12 12"><path d="m3 3 6 6M9 3l-6 6" /></svg><i v-else /></span>
      </span>
      <span class="aw-node-kind"><span v-if="data.index" class="aw-node-index">{{ data.index }}</span>{{ kind }}</span>
      <span v-if="data.updated" class="aw-node-update">{{ t('agentWorkflowGraph.updated') }}</span>
      <span v-if="data.status" class="aw-node-state"><i aria-hidden="true" />{{ stateLabel }}</span>
    </div>
    <strong class="aw-node-title" :title="title">{{ title }}</strong>
    <div v-if="data.inspectable" class="aw-node-activity" :class="{ approval: activity?.phase === 'approval', disconnected: activity?.connected === false }" :title="description" data-testid="workflow-node-activity">
      <span class="aw-activity-phase" :title="activityLabel">{{ activityLabel }}</span>
      <span v-if="activityMetrics.length" class="aw-activity-metrics">{{ activityMetrics.join(' · ') }}</span>
      <span v-if="activity?.connected === false" class="aw-activity-disconnected">{{ t('agentWorkflowGraph.activityDisconnected') }}</span>
      <time v-else-if="activityTime" class="aw-activity-time">{{ t('agentWorkflowGraph.activityUpdatedAt', { time: activityTime }) }}</time>
    </div>
    <p v-else class="aw-node-description" :title="description">{{ description }}</p>
    <div v-if="(data.incomingCount || 0) > 1 || (data.outgoingCount || 0) > 1" class="aw-node-connections" data-testid="workflow-node-connections"><span v-if="(data.incomingCount || 0) > 1" :title="t('agentWorkflowGraph.mergeHint')">{{ t('agentWorkflowGraph.incomingCount', { count: data.incomingCount! }) }}</span><span v-if="(data.outgoingCount || 0) > 1" :title="t(data.kind === 'condition' ? 'agentWorkflowGraph.conditionHint' : 'agentWorkflowGraph.fanoutHint')">{{ t(data.kind === 'condition' ? 'agentWorkflowGraph.conditionOutputs' : 'agentWorkflowGraph.outgoingCount', { count: data.outgoingCount! }) }}</span></div>
    <small v-if="data.branch" class="aw-branch-result">{{ t('agentWorkflowGraph.selectedBranch', { branch: t('agentWorkflowGraph.branch_' + data.branch) }) }}</small>
    <div v-if="data.outputFile || duration" class="aw-node-detail">
      <span v-if="data.outputFile" class="aw-node-file" :title="data.outputFile">{{ data.outputFile }}</span>
      <span v-if="duration" class="aw-node-duration" :title="t('agentWorkflowGraph.duration', { value: duration })">{{ duration }}</span>
    </div>
    <button v-if="data.editable && data.kind !== 'output' && data.canInsert !== false" type="button" class="aw-node-insert nodrag nopan" :aria-label="t('agentWorkflowGraph.insertAfter', { name: title })" @click.stop="emit('insert', data.id)"><span aria-hidden="true">＋</span>{{ t('agentWorkflowGraph.next') }}</button>
    <button v-else-if="data.inspectable" type="button" class="aw-node-inspect nodrag nopan" :aria-label="t('agentWorkflowGraph.inspectNodeProcess', { name: title })" data-testid="workflow-node-inspect" @click.stop="emit('inspect', data.id)">{{ t('agentWorkflowGraph.inspectProcess') }}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 5 7 7-7 7" /></svg></button>
    <template v-for="(port, index) in data.ports || (data.kind === 'output' ? [] : [{ id: 'out', label: '' }])" :key="port.id">
      <Handle :id="port.id" type="source" :position="Position.Right" :connectable="data.editable" class="aw-port" :title="t('agentWorkflowGraph.outputPortHint')" :aria-label="t('agentWorkflowGraph.outputPortHint')" :style="{ top: `${((index + 1) / ((data.ports?.length || 1) + 1)) * 100}%` }" />
      <span v-if="(data.ports?.length || 0) > 1" class="aw-port-label" :style="{ top: `${((index + 1) / ((data.ports?.length || 1) + 1)) * 100}%` }">{{ port.id === 'true' || port.id === 'false' ? t('agentWorkflowGraph.branch_' + port.id) : index + 1 }}</span>
    </template>
  </article>
</template>

<style scoped>
.aw-canvas-node { --aw-node-accent: var(--el-color-primary); width: 248px; min-height: 130px; box-sizing: border-box; padding: 13px 15px 11px; border: 1px solid var(--el-border-color); border-top: 3px solid var(--aw-node-accent); border-radius: 12px; background: var(--el-bg-color); color: var(--el-text-color-primary); box-shadow: 0 3px 12px rgb(0 0 0 / 7%); transition: border-color .15s, box-shadow .15s; }
.aw-canvas-node.selected { border-color: var(--el-color-primary); box-shadow: 0 0 0 3px color-mix(in srgb, var(--el-color-primary) 18%, transparent), 0 5px 16px rgb(0 0 0 / 8%); }
.aw-canvas-node.updated { border-color: var(--el-color-success); box-shadow: 0 0 0 3px color-mix(in srgb, var(--el-color-success) 22%, transparent), 0 5px 16px rgb(0 0 0 / 8%); }
.aw-node-update { margin-left: auto; color: var(--el-color-success); font-size: 10px; white-space: nowrap; }
.aw-canvas-node.input, .aw-canvas-node.output { --aw-node-accent: var(--el-color-info); }
.aw-canvas-node.confirm { --aw-node-accent: var(--el-color-warning); }
.aw-canvas-node.iteration { --aw-node-accent: #438c9c; }
.aw-canvas-node.loop { --aw-node-accent: #9673bc; }
.aw-canvas-node.code { --aw-node-accent: #6e85cf; }
.aw-canvas-node.condition { --aw-node-accent: #9d72d1; } .aw-canvas-node.parallel, .aw-canvas-node.join { --aw-node-accent: #4b9fbd; }
.aw-port-label { position: absolute; right: 9px; transform: translateY(-50%); font-size: 9px; background: var(--el-bg-color); color: var(--el-text-color-secondary); padding: 1px 2px; pointer-events: none; }
.aw-branch-result { display: block; margin-top: 5px; font-size: 10px; color: var(--el-color-primary); }
.aw-canvas-node.completed { --aw-node-accent: var(--el-color-success); }
.aw-canvas-node.failed, .aw-canvas-node.interrupted { --aw-node-accent: var(--el-color-danger); }
.aw-canvas-node.waiting_confirmation, .aw-canvas-node.waiting { --aw-node-accent: var(--el-color-warning); }
.aw-canvas-node.cancelled, .aw-canvas-node.skipped { --aw-node-accent: var(--el-color-info); }
.aw-canvas-node.running{--aw-node-accent:var(--el-color-primary);border-color:var(--el-color-primary-light-5);box-shadow:0 0 0 2px color-mix(in srgb,var(--el-color-primary) 12%,transparent),0 5px 18px rgb(0 0 0 / 9%)}.aw-canvas-node.queued{border-style:dashed}.aw-canvas-node.failed,.aw-canvas-node.interrupted{border-color:var(--el-color-danger-light-5)}.aw-canvas-node.aw-node-needs-action{--aw-node-accent:var(--el-color-warning);border-color:var(--el-color-warning-light-5);box-shadow:0 0 0 2px color-mix(in srgb,var(--el-color-warning) 15%,transparent),0 5px 18px rgb(0 0 0 / 9%)}
.aw-node-top { display: flex; align-items: center; gap: 7px; min-width: 0; margin-bottom: 8px; }
.aw-node-icon { position:relative;display: flex; flex: none; width: 23px; height: 23px; align-items: center; justify-content: center; border-radius: 6px; background: color-mix(in srgb, var(--aw-node-accent) 12%, transparent); color: var(--aw-node-accent); }
.aw-node-icon svg { width: 16px; height: 16px; fill: none; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.aw-node-icon-status{position:absolute;right:-5px;bottom:-4px;display:grid;place-items:center;box-sizing:border-box;width:14px;height:14px;border:2px solid var(--el-bg-color);border-radius:50%;background:var(--aw-node-accent);color:var(--el-bg-color)}.aw-node-icon-status svg{width:10px;height:10px;stroke-width:1.2}.aw-node-icon-status i{display:block;box-sizing:border-box;width:7px;height:7px;border:1.3px solid currentColor;border-right-color:transparent;border-radius:50%}.aw-node-icon-status.spinning i{animation:aw-icon-spin 1s linear infinite}
.aw-node-kind { display: flex; align-items: center; gap: 5px; font-size: 10px; color: var(--el-text-color-secondary); }
.aw-node-index { font-variant-numeric: tabular-nums; font-weight: 700; }
.aw-node-state { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; font-size: 10px; white-space: nowrap; color: var(--aw-node-accent); }
.aw-node-state i { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.running .aw-node-state i { animation: aw-pulse 1.2s ease-in-out infinite; }
.aw-node-needs-action .aw-node-state i{animation:none}.aw-node-needs-action .aw-node-state{font-weight:650}
.aw-node-stale .aw-node-state i{animation:none}.aw-canvas-node.aw-node-stale.running{border-style:dashed}
.aw-node-title { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; line-height: 1.4; font-weight: 650; }
.aw-node-description { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; margin: 7px 0 0; min-height: 34px; font-size: 11px; line-height: 1.55; color: var(--el-text-color-secondary); white-space: pre-wrap; }
.aw-node-connections{display:flex;flex-wrap:wrap;gap:4px;margin-top:7px;font-size:9px;line-height:1.4;color:var(--el-text-color-secondary)}.aw-node-connections span{padding:2px 4px;border-radius:4px;background:var(--el-fill-color-light)}
.aw-node-activity { display: flex; flex-direction: column; gap: 3px; margin-top: 7px; min-height: 34px; min-width: 0; }
.aw-activity-phase { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--el-color-primary); font-size: 11px; line-height: 1.4; }
.completed .aw-activity-phase { color: var(--el-color-success); }.failed .aw-activity-phase,.interrupted .aw-activity-phase { color: var(--el-color-danger); }.aw-node-activity.approval .aw-activity-phase { color: var(--el-color-warning); }
.aw-activity-metrics,.aw-activity-time,.aw-activity-disconnected { font-size: 9px; color: var(--el-text-color-secondary); line-height: 1.4; }.aw-activity-time { color: var(--el-text-color-placeholder); }.aw-activity-disconnected { color: var(--el-color-warning); }
.aw-node-detail { display: flex; align-items: center; gap: 8px; margin-top: 8px; min-width: 0; font-size: 10px; color: var(--el-text-color-secondary); }
.aw-node-file { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; border-radius: 4px; background: var(--el-fill-color-light); padding: 3px 5px; }
.aw-node-duration { margin-left: auto; font-variant-numeric: tabular-nums; }
.aw-node-insert { width: 100%; display: flex; justify-content: center; align-items: center; gap: 5px; margin-top: 10px; border: 0; border-top: 1px solid var(--el-border-color-lighter); padding: 7px 0 0; background: transparent; color: var(--el-text-color-secondary); font: inherit; font-size: 10px; cursor: pointer; }
.aw-node-insert:hover, .aw-node-insert:focus-visible { color: var(--el-color-primary); }
.aw-node-inspect { display: flex; align-items: center; justify-content: flex-end; gap: 3px; width: 100%; margin-top: 8px; padding: 6px 0 0; border: 0; border-top: 1px solid var(--el-border-color-lighter); background: transparent; color: var(--el-color-primary); font: inherit; font-size: 10px; cursor: pointer; }.aw-node-inspect svg { width: 12px; height: 12px; fill: none; stroke: currentColor; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; }.aw-node-inspect:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 3px; }
.aw-port { width: 9px; height: 9px; border: 2px solid var(--el-bg-color); background: var(--aw-node-accent); }
@keyframes aw-pulse { 50% { opacity: .3; } }
@keyframes aw-icon-spin{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion: reduce) { .running .aw-node-state i,.aw-node-icon-status.spinning i { animation: none; } .aw-canvas-node { transition: none; } }
</style>
