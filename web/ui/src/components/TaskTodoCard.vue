<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { List } from "@element-plus/icons-vue";
import type { TaskTodoPlan } from "@/agent/timeline";
import { taskTodoCounts } from "@/agent/taskTodoSummary";
import { markOf } from "./tools/todoMarks";

const props = defineProps<{ plan: TaskTodoPlan; inline?: boolean }>();
const { t } = useI18n();
const counts = computed(() => taskTodoCounts(props.plan.todos));
</script>

<template>
  <section class="task-todo-card" :class="{ 'is-inline': inline }" data-testid="task-todo-card" :data-user-id="plan.userId" :data-call-id="plan.callId" :aria-label="t('taskTodo.title')">
    <header><el-icon class="heading-icon" aria-hidden="true"><List /></el-icon><strong>{{ t('taskTodo.shortTitle') }}</strong><span>{{ counts.total ? t('taskTodo.completed', { n: counts.completed, total: counts.total }) : t('taskTodo.cleared') }}</span></header>
    <div v-if="counts.blocked || counts.cancelled || counts.pending" class="outcomes">
      <span v-if="counts.blocked" class="blocked">{{ t('taskTodo.blocked', { n: counts.blocked }) }}</span>
      <span v-if="counts.cancelled">{{ t('taskTodo.cancelled', { n: counts.cancelled }) }}</span>
      <span v-if="counts.pending">{{ t('taskTodo.pending', { n: counts.pending }) }}</span>
    </div>
    <ol v-if="plan.todos.length">
      <li v-for="(item, index) in plan.todos" :key="index" :class="item.status">
        <span class="mark" :aria-label="t(`taskTodo.status.${item.status}`)">{{ markOf(item.status) }}</span><span>{{ item.task }}</span>
      </li>
    </ol>
    <p v-else class="cleared">{{ t('taskTodo.clearedHint') }}</p>
  </section>
</template>

<style scoped lang="scss">
.task-todo-card { color: var(--el-text-color-primary); font-size: 12px; }
header { display: flex; align-items: center; gap: 12px; padding-bottom: 9px; border-bottom: 1px solid var(--el-border-color-extra-light); }
header strong { font-size: 13px; }
.heading-icon { flex: none; font-size: 14px; color: var(--el-text-color-secondary); }
header > span { margin-left: auto; white-space: nowrap; color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; }
.outcomes { display: flex; gap: 10px; flex-wrap: wrap; padding-top: 8px; color: var(--el-text-color-secondary); font-size: 11px; }
.blocked { color: var(--el-color-warning); }
ol { list-style: none; padding: 8px 0 0; margin: 0; max-height: min(320px, 48dvh); overflow: auto; overscroll-behavior: contain; }
li { display: flex; gap: 8px; padding: 5px 0; line-height: 1.6; overflow-wrap: anywhere; }
.mark { flex: none; width: 1em; color: var(--el-text-color-secondary); }
li.completed { color: var(--el-text-color-secondary); .mark { color: var(--el-color-success); } }
li.in_progress { font-weight: 600; .mark { color: var(--el-color-primary); } }
li.blocked .mark { color: var(--el-color-warning); }
li.cancelled { color: var(--el-text-color-secondary); > span:last-child { text-decoration: line-through; } }
.is-inline {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color);
  min-width: 0;
  white-space: normal;
  text-align: start;
  header { gap: 7px; padding-bottom: 0; border-bottom: 0; flex-wrap: wrap; }
  header > span { margin-left: auto; font-size: 11px; }
  ol { max-height: none; overflow: visible; padding-top: 7px; }
  li { align-items: baseline; padding: 4px 0; }
  li > span:last-child { min-width: 0; white-space: pre-wrap; }
}
.cleared { color: var(--el-text-color-secondary); margin: 10px 0 0; }
</style>
