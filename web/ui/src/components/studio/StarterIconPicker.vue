<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue';
import { Icon } from '@iconify/vue';
import AgentAvatar, { AVATAR_ICONS } from '@/components/AgentAvatar.vue';
import { customUIIcons, hiddenUIBuiltins, refreshUIIcons, uiIconName } from '@/agent/uiIcons';
import type { UIIconModule } from '@/api/uiIcons';
import { starterIconFor, starterIconNames } from '@/components/starterIcons';

const props = withDefaults(defineProps<{ modelValue: string; label: string; module?: UIIconModule; allowNone?: boolean; disabled?: boolean }>(), {module: 'starters', allowNone: true});
const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
const root = ref<HTMLElement | null>(null);
const trigger = ref<HTMLButtonElement | null>(null);
const open = ref(false);
const active = ref(0);
const options = computed(() => {
  const hidden = hiddenUIBuiltins(props.module);
  const builtin = props.module === 'agents' ? [...AVATAR_ICONS] : starterIconNames;
  const values = [...(props.allowNone ? [''] : []), ...builtin.filter(name => !hidden.includes(name)), ...customUIIcons(props.module).map(i => i.id)];
  if (props.modelValue && !values.includes(props.modelValue)) values.push(props.modelValue);
  return values;
});
const id = `starter-icons-${useId()}`;
const labels: Record<string, string> = {
  search: '搜索', filter: '筛选', eye: '查看', target: '目标', scan: '扫描', fingerprint: '识别',
  compare: '比较', graph: '图表', trend: '趋势', clock: '时间', list: '列表', table: '表格',
  tag: '标签', flag: '标记', code: '代码', terminal: '终端', bug: '排查', shield: '安全',
  lock: '锁定', key: '钥匙', mask: '隐私', file: '文件', folder: '文件夹', archive: '归档',
  box: '收纳', book: '阅读', layers: '图层', database: '数据', 'hard-drive': '存储', cpu: '计算',
  robot: '助手', network: '连接', share: '分享', link: '链接', globe: '网页', cloud: '云端',
  mail: '邮件', message: '对话', bell: '提醒', user: '个人', users: '团队', puzzle: '拼图',
  wand: '生成', spark: '灵感', bolt: '快捷', fire: '热门', warn: '注意', refresh: '刷新',
  history: '历史', download: '下载',
};
function iconLabel(name: string) { return uiIconName(name) || labels[name] || ({pen: '写作', compass: '探索', chart: '图表'} as Record<string,string>)[name] || (name ? '已保存图标' : '无图标'); }
const selectedLabel = computed(() => iconLabel(props.modelValue));
watch(options, () => { active.value = Math.min(active.value, options.value.length - 1); });
function focusActive() {
  void nextTick(() => root.value?.querySelector<HTMLButtonElement>(`[data-icon-index="${active.value}"]`)?.focus());
}
function show() {
  if (props.disabled) return;
  void refreshUIIcons();
  active.value = Math.max(0, options.value.indexOf(props.modelValue));
  open.value = true;
  focusActive();
}
function close(restoreFocus = false) {
  open.value = false;
  if (restoreFocus) trigger.value?.focus();
}
function choose(value: string) { emit('update:modelValue', value); close(true); }
function onKey(event: KeyboardEvent) {
  const move: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -6, ArrowDown: 6 };
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); return; }
  if (event.key === 'Home' || event.key === 'End' || event.key in move) {
    event.preventDefault();
    active.value = event.key === 'Home' ? 0 : event.key === 'End' ? options.value.length - 1
      : Math.max(0, Math.min(options.value.length - 1, active.value + (move[event.key] ?? 0)));
    focusActive();
  }
}
function outside(event: PointerEvent) { if (!root.value?.contains(event.target as Node)) close(); }
function focusOut(event: FocusEvent) {
  if (event.relatedTarget && !root.value?.contains(event.relatedTarget as Node)) close();
}
onMounted(() => { void refreshUIIcons(); document.addEventListener('pointerdown', outside); });
onBeforeUnmount(() => document.removeEventListener('pointerdown', outside));
</script>

<template>
  <span ref="root" class="starter-icon-picker" @focusout="focusOut">
    <el-popover :visible="open" :teleported="false" :persistent="false" :popper-options="{ strategy: 'fixed' }" :popper-style="{ '--el-bg-color-overlay': 'var(--el-bg-color)' }" :width="300" placement="bottom-start">
      <template #reference>
        <button ref="trigger" type="button" class="starter-icon-trigger" :aria-label="label" aria-haspopup="dialog"
          :disabled="disabled" :aria-expanded="open" :aria-controls="id" @click="open ? close() : show()"
          @keydown.down.prevent="show" @keydown.up.prevent="show" @keydown.esc.stop.prevent="close()">
          <AgentAvatar v-if="module === 'agents' && modelValue" :icon="modelValue" color="#5965e8" :size="22" />
          <Icon v-else-if="starterIconFor(modelValue)" aria-hidden="true" :icon="starterIconFor(modelValue)!" />
          <span v-else class="empty-icon" aria-hidden="true">∅</span>
          <span>{{ selectedLabel }}</span><span aria-hidden="true" class="chevron">⌄</span>
        </button>
      </template>
      <div :id="id" class="starter-icon-popup" role="dialog" :aria-label="`${label}选择`" @keydown="onKey">
        <div class="starter-icon-grid" role="group" aria-label="图标选择">
          <button v-for="(name, i) in options" :key="name" type="button" class="starter-icon-tile"
            :class="{ selected: name === modelValue }" :data-icon-index="i" :tabindex="i === active ? 0 : -1"
            :aria-label="name ? `选择图标 ${iconLabel(name)}` : '不使用图标'" :aria-pressed="name === modelValue"
            @focus="active = i" @click="choose(name)">
            <AgentAvatar v-if="module === 'agents' && name" :icon="name" color="#5965e8" :size="24" />
            <Icon v-else-if="name && starterIconFor(name)" aria-hidden="true" :icon="starterIconFor(name)!" />
            <span v-else class="empty-icon" aria-hidden="true">∅</span>
            <span class="icon-caption">{{ iconLabel(name) }}</span>
          </button>
        </div>
      </div>
    </el-popover>
  </span>
</template>

<style scoped>
.starter-icon-picker { display: block; width: 100%; }
.starter-icon-trigger { display: flex; align-items: center; gap: 6px; width: 100%; height: 34px; padding: 0 9px; border: 1px solid var(--el-border-color); border-radius: 6px; color: var(--el-text-color-regular); background: var(--el-bg-color); font: inherit; font-size: 12px; cursor: pointer; }
.starter-icon-trigger:hover { border-color: var(--el-border-color-hover); }
.starter-icon-trigger svg { width: 18px; height: 18px; flex: none; }
.chevron { margin-left: auto; color: var(--el-text-color-secondary); }
.starter-icon-popup { max-height: min(330px, 60dvh); overflow: auto; overscroll-behavior: contain; }
.starter-icon-grid { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 4px; }
.starter-icon-tile { min-width: 0; min-height: 44px; border: 1px solid transparent; border-radius: 6px; background: transparent; color: var(--el-text-color-regular); display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 3px; cursor: pointer; }
.starter-icon-tile:hover, .starter-icon-tile:focus-visible { background: var(--el-fill-color-light); outline-offset: -2px; }
.starter-icon-tile.selected { color: var(--el-color-primary); border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.starter-icon-tile svg { width: 20px; height: 20px; }
.icon-caption { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 10px; line-height: 1.2; }
.empty-icon { font-size: 18px; line-height: 20px; }
</style>
