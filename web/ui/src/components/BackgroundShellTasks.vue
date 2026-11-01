<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { api } from "@/api/client";
import type { ShellJobOutput, ShellJobRecord } from "@/api/types";
import { formatDuration } from "@/agent/timeline";
import { terminalIcon } from "./fileIcons";

const props = defineProps<{
  sessionId: string | null;
  displaySessionId: string | null;
  tasks: Record<string, ShellJobRecord>;
  connected: boolean;
  disabled?: boolean;
}>();
const emit = defineEmits<{ updated: [task: ShellJobRecord] }>();
const { t } = useI18n();
const expanded = ref(false), loading = ref(false), stopping = ref<string | null>(null);
const issue = ref(""), outputIssue = ref(""), selectedId = ref<string | null>(null);
const filter = ref<"recent" | "running" | "finished" | "all">("recent");
const page = ref<ShellJobOutput | null>(null), outputLoading = ref(false);
const validId = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,160}$/.test(value);
const available = computed(() => validId(props.sessionId) && props.sessionId === props.displaySessionId && !props.disabled);
const tasks = computed(() => Object.values(props.tasks).filter(task => validId(task.task_id) && task.background)
  .sort((a, b) => b.started_at - a.started_at || a.task_id.localeCompare(b.task_id)));
const running = computed(() => tasks.value.filter(task => task.status === "running").length);
const queued = computed(() => tasks.value.filter(task => task.status === "queued").length);
const visibleTasks = computed(() => {
  const filtered = tasks.value.filter(task => filter.value === "running" ? ["running", "queued"].includes(task.status) : filter.value === "finished" ? !["running", "queued"].includes(task.status) : true);
  return filter.value === "recent" ? filtered.slice(0, 12) : filtered;
});
const selected = computed(() => tasks.value.find(task => task.task_id === selectedId.value));
const output = computed(() => page.value?.output ?? selected.value?.output_tail ?? "");
const filters = ["recent", "running", "finished", "all"] as const;
let disposed = false, listTicket = 0, outputTicket = 0, contextVersion = 0;
let listAbort: AbortController | undefined, outputAbort: AbortController | undefined;

function current(sid: string) {
  return !disposed && available.value && props.sessionId === sid;
}
function resetOutput() {
  ++outputTicket;
  outputAbort?.abort();
  page.value = null;
  outputLoading.value = false;
  outputIssue.value = "";
}
function select(task: ShellJobRecord) {
  if (!available.value) return;
  resetOutput();
  selectedId.value = task.task_id;
}
async function loadTasks() {
  const sid = props.sessionId;
  if (!sid || !available.value) return;
  const ticket = ++listTicket;
  listAbort?.abort();
  const controller = new AbortController();
  listAbort = controller;
  loading.value = true;
  issue.value = "";
  try {
    const result = await api.shellTasks(sid, controller.signal);
    if (ticket !== listTicket || !current(sid)) return;
    if (!result || !Array.isArray(result.tasks)) throw new Error("Invalid command list");
    for (const task of result.tasks) emit("updated", task);
  } catch (error) {
    if (ticket === listTicket && current(sid) && (error as Error)?.name !== "AbortError") issue.value = t("backgroundShell.loadFailed");
  } finally {
    if (ticket === listTicket && current(sid)) loading.value = false;
  }
}
async function loadOutput(offset = 0) {
  const sid = props.sessionId, taskId = selected.value?.task_id;
  if (!sid || !taskId || !available.value) return;
  const ticket = ++outputTicket;
  outputAbort?.abort();
  const controller = new AbortController();
  outputAbort = controller;
  outputLoading.value = true;
  outputIssue.value = "";
  try {
    const result = await api.shellTaskOutput(sid, taskId, offset, 65536, controller.signal);
    if (ticket !== outputTicket || !current(sid) || selectedId.value !== taskId) return;
    if (result.task_id !== taskId || typeof result.output !== "string" || !Number.isSafeInteger(result.next_offset)) throw new Error("Invalid command output");
    page.value = result;
  } catch (error) {
    if (ticket === outputTicket && current(sid) && selectedId.value === taskId && (error as Error)?.name !== "AbortError") outputIssue.value = t("backgroundShell.outputFailed");
  } finally {
    if (ticket === outputTicket && current(sid) && selectedId.value === taskId) outputLoading.value = false;
  }
}
async function stop(task: ShellJobRecord) {
  const sid = props.sessionId;
  if (!sid || !available.value || !props.connected || stopping.value || !["running", "queued"].includes(task.status) || selected.value?.task_id !== task.task_id) return;
  const context = contextVersion;
  stopping.value = task.task_id;
  issue.value = "";
  try {
    const result = await api.stopShellTask(sid, task.task_id);
    if (!current(sid) || context !== contextVersion) return;
    if (result.task?.task_id !== task.task_id) throw new Error("Invalid command response");
    emit("updated", result.task);
  } catch {
    if (current(sid) && context === contextVersion) issue.value = t("backgroundShell.stopFailed");
  } finally {
    if (current(sid) && context === contextVersion && stopping.value === task.task_id) stopping.value = null;
  }
}
function duration(task: ShellJobRecord) {
  return task.ended_at ? formatDuration(Math.max(0, task.ended_at - task.started_at)) : "";
}
watch(expanded, value => {
  if (value) void loadTasks();
  else {
    ++listTicket;
    listAbort?.abort();
    loading.value = false;
    resetOutput();
  }
});
watch(() => [props.sessionId, props.displaySessionId, props.disabled], () => {
  ++contextVersion;
  ++listTicket;
  listAbort?.abort();
  expanded.value = false;
  loading.value = false;
  stopping.value = null;
  selectedId.value = null;
  issue.value = "";
  resetOutput();
});
watch(selected, task => { if (!task && selectedId.value) { selectedId.value = null; resetOutput(); } });
onBeforeUnmount(() => {
  disposed = true;
  ++listTicket;
  listAbort?.abort();
  resetOutput();
});
</script>

<template>
  <el-popover v-model:visible="expanded" trigger="click" placement="top-end" :width="560" popper-class="background-shell-popover">
    <template #reference>
      <button type="button" class="shell-access" :class="{ active: available && (running > 0 || queued > 0) }" :disabled="!available" :aria-expanded="expanded" :aria-label="t('backgroundShell.open')" data-testid="background-shell-access">
        <Icon :icon="terminalIcon" width="14" /><span class="shell-label">{{ t('backgroundShell.label') }}</span>
        <span class="shell-count">{{ t('backgroundShell.runningCount', { count: available ? running : 0 }) }}</span>
        <span v-if="queued" class="shell-count">{{ t('backgroundShell.queuedCount', { count: queued }) }}</span>
      </button>
    </template>
    <section class="shell-tasks" data-testid="background-shell-tasks">
      <header class="shell-heading">
        <strong>{{ t('backgroundShell.heading') }}</strong>
        <button type="button" class="shell-action" :disabled="loading || !available" @click="loadTasks">{{ t('backgroundShell.refresh') }}</button>
      </header>
      <p v-if="!connected" class="shell-notice" role="status">{{ t('backgroundShell.disconnected') }}</p>
      <p v-if="issue" class="shell-error" role="alert">{{ issue }}</p>
      <nav class="shell-filters" :aria-label="t('backgroundShell.filter')">
        <button v-for="value in filters" :key="value" type="button" :class="{ selected: filter === value }" :aria-pressed="filter === value" @click="filter = value">{{ t(`backgroundShell.filter_${value}`) }}</button>
      </nav>
      <p v-if="!visibleTasks.length" class="shell-empty">{{ t(loading ? 'backgroundShell.loading' : 'backgroundShell.empty') }}</p>
      <div class="shell-list">
        <button v-for="task in visibleTasks" :key="task.task_id" type="button" class="shell-row" :class="{ selected: selectedId === task.task_id }" :aria-pressed="selectedId === task.task_id" :data-task-id="task.task_id" @click="select(task)">
          <span class="shell-state-dot" :class="task.status" aria-hidden="true">{{ task.status === 'running' ? '●' : task.status === 'completed' ? '✓' : '!' }}</span>
          <span class="shell-copy"><span :title="task.command">{{ task.description || task.command }}</span><small v-if="task.description" :title="task.command">$ {{ task.command }}</small></span>
          <span class="shell-state" :class="task.status">{{ t(`backgroundShell.state_${task.status}`) }}<small v-if="duration(task)">{{ duration(task) }}</small></span>
        </button>
      </div>
      <section v-if="selected" class="shell-detail" :aria-label="t('backgroundShell.output')">
        <div class="shell-detail-heading">
          <span>{{ t('backgroundShell.state_' + selected.status) }}<template v-if="typeof selected.exit_code === 'number'"> · exit {{ selected.exit_code }}</template></span>
          <button v-if="['running', 'queued'].includes(selected.status)" type="button" class="shell-stop" :disabled="!connected || !!stopping || !available" @click="stop(selected)">{{ t(stopping === selected.task_id ? 'backgroundShell.stopping' : 'backgroundShell.stop') }}</button>
        </div>
        <pre class="shell-command">$ {{ selected.command }}</pre>
        <p class="shell-directory" :title="selected.workdir">{{ selected.workdir }}</p>
        <p v-if="selected.error" class="shell-error">{{ selected.error }}</p>
        <p v-if="selected.persistence_error" class="shell-error">{{ selected.persistence_error }}</p>
        <div class="shell-log-actions">
          <span>{{ page ? t('backgroundShell.logRange', { start: page.offset, end: page.next_offset, total: page.total_bytes }) : t('backgroundShell.liveTail') }}</span>
          <div>
            <button v-if="page" type="button" class="shell-action" @click="resetOutput">{{ t('backgroundShell.followTail') }}</button>
            <button type="button" class="shell-action" :disabled="outputLoading || !available" @click="loadOutput(page?.offset ?? 0)">{{ t(page ? 'backgroundShell.refresh' : 'backgroundShell.readLog') }}</button>
            <button v-if="page && !page.eof && page.next_offset < page.total_bytes" type="button" class="shell-action" :disabled="outputLoading || !available" @click="loadOutput(page.next_offset)">{{ t('backgroundShell.nextPage') }}</button>
          </div>
        </div>
        <p v-if="outputIssue" class="shell-error" role="alert">{{ outputIssue }}</p>
        <pre class="shell-output" tabindex="0">{{ output || t(outputLoading ? 'backgroundShell.loading' : 'backgroundShell.noOutput') }}</pre>
        <p v-if="!page && selected.output_truncated" class="shell-notice">{{ t('backgroundShell.tailHint') }}</p>
      </section>
      <p class="shell-footnote">{{ t('backgroundShell.closeHint') }}</p>
    </section>
  </el-popover>
</template>

<style scoped lang="scss">
.shell-access { display: inline-flex; align-items: center; gap: 5px; height: 30px; padding: 0 9px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-secondary); cursor: pointer; font: inherit; font-size: 12px; box-shadow: 0 2px 8px rgb(0 0 0 / 8%); white-space: nowrap; }
.shell-access.active { color: var(--el-color-primary); border-color: var(--el-color-primary-light-5); }
.shell-access:disabled { cursor: default; opacity: .55; }
.shell-count { font-size: 10px; }
.shell-tasks { font-size: 12px; color: var(--el-text-color-regular); }
.shell-heading, .shell-detail-heading, .shell-log-actions { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.shell-heading strong { font-weight: 600; }
.shell-action, .shell-stop { border: 0; border-radius: 5px; background: transparent; padding: 3px 6px; font: inherit; font-size: 11px; cursor: pointer; color: var(--el-color-primary); }
.shell-action:hover { background: var(--el-fill-color-light); }
.shell-action:disabled, .shell-stop:disabled { opacity: .5; cursor: default; }
.shell-stop { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.shell-filters { display: flex; gap: 4px; margin: 10px 0 7px; }
.shell-filters button { padding: 3px 8px; border: 0; border-radius: 5px; background: transparent; color: var(--el-text-color-secondary); font: inherit; font-size: 11px; cursor: pointer; }
.shell-filters button.selected { background: var(--el-fill-color); color: var(--el-text-color-primary); }
.shell-list { max-height: 220px; overflow-y: auto; }
.shell-row { display: flex; align-items: center; gap: 9px; width: 100%; padding: 8px 5px; border: 0; border-radius: 6px; background: transparent; text-align: left; color: inherit; font: inherit; cursor: pointer; }
.shell-row:hover, .shell-row.selected { background: var(--el-fill-color-light); }
.shell-state-dot { width: 14px; flex: 0 0 14px; color: var(--el-color-warning); text-align: center; }
.shell-state-dot.completed { color: var(--el-color-success); }
.shell-state-dot.running { color: var(--el-color-primary); }
.shell-state-dot.failed, .shell-state-dot.timed_out { color: var(--el-color-danger); }
.shell-copy { display: flex; flex-direction: column; gap: 3px; flex: 1; min-width: 0; }
.shell-copy > span, .shell-copy > small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.shell-copy small, .shell-state { color: var(--el-text-color-secondary); font-size: 10px; }
.shell-state { flex: 0 0 auto; text-align: right; }
.shell-state small { display: block; margin-top: 3px; }
.shell-state.running { color: var(--el-color-primary); }
.shell-detail { border-top: 1px solid var(--el-border-color-lighter); margin-top: 8px; padding-top: 10px; }
.shell-command, .shell-output { white-space: pre-wrap; overflow-wrap: anywhere; font: 11px/1.6 var(--pg-mono); }
.shell-command { margin: 6px 0 0; max-height: 72px; overflow: auto; }
.shell-directory { margin: 3px 0 9px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--el-text-color-secondary); font: 10px var(--pg-mono); }
.shell-log-actions { font-size: 10px; color: var(--el-text-color-secondary); flex-wrap: wrap; gap: 4px; }
.shell-output { max-height: min(36dvh, 300px); min-height: 45px; overflow: auto; padding: 9px; border-radius: 6px; margin: 5px 0; background: var(--el-fill-color-light); }
.shell-empty { margin: 18px 0; text-align: center; color: var(--el-text-color-secondary); }
.shell-notice, .shell-footnote { margin: 6px 0 0; font-size: 10px; color: var(--el-text-color-secondary); line-height: 1.5; }
.shell-error { margin: 7px 0; color: var(--el-color-danger); font-size: 11px; overflow-wrap: anywhere; }
@media (max-width: 600px) { .shell-count { display: none; } }
@media (max-width: 420px) { .shell-label { max-width: 70px; overflow: hidden; text-overflow: ellipsis; } }
</style>

<style>
.background-shell-popover.el-popover { max-width: calc(100vw - 24px); }
</style>
