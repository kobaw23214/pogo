<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
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
  get search() { return tr('studioManagement.text309'); }, get filter() { return tr('studioManagement.text310'); }, get eye() { return tr('studioManagement.text311'); }, get target() { return tr('studioManagement.text312'); }, get scan() { return tr('studioManagement.text313'); }, get fingerprint() { return tr('studioManagement.text314'); },
  get compare() { return tr('studioManagement.text315'); }, get graph() { return tr('studioManagement.text316'); }, get trend() { return tr('studioManagement.text317'); }, get clock() { return tr('studioManagement.text318'); }, get list() { return tr('studioManagement.text319'); }, get table() { return tr('studioManagement.text320'); },
  get tag() { return tr('studioManagement.text156'); }, get flag() { return tr('studioManagement.text321'); }, get code() { return tr('studioManagement.text322'); }, get terminal() { return tr('studioManagement.text323'); }, get bug() { return tr('studioManagement.text324'); }, get shield() { return tr('studioManagement.text325'); },
  get lock() { return tr('studioManagement.text326'); }, get key() { return tr('studioManagement.text327'); }, get mask() { return tr('studioManagement.text328'); }, get file() { return tr('studioManagement.text329'); }, get folder() { return tr('studioManagement.text330'); }, get archive() { return tr('studioManagement.text331'); },
  get box() { return tr('studioManagement.text332'); }, get book() { return tr('studioManagement.text333'); }, get layers() { return tr('studioManagement.text334'); }, get database() { return tr('studioManagement.text335'); }, get 'hard-drive'() { return tr('studioManagement.text336'); }, get cpu() { return tr('studioManagement.text337'); },
  get robot() { return tr('studioManagement.text338'); }, get network() { return tr('studioManagement.text339'); }, get share() { return tr('studioManagement.text340'); }, get link() { return tr('studioManagement.text341'); }, get globe() { return tr('studioManagement.text342'); }, get cloud() { return tr('studioManagement.text343'); },
  get mail() { return tr('studioManagement.text344'); }, get message() { return tr('studioManagement.text345'); }, get bell() { return tr('studioManagement.text346'); }, get user() { return tr('studioManagement.text347'); }, get users() { return tr('studioManagement.text348'); }, get puzzle() { return tr('studioManagement.text349'); },
  get wand() { return tr('studioManagement.text350'); }, get spark() { return tr('studioManagement.text351'); }, get bolt() { return tr('studioManagement.text352'); }, get fire() { return tr('studioManagement.text353'); }, get warn() { return tr('studioManagement.text354'); }, get refresh() { return tr('studioManagement.text355'); },
  get history() { return tr('studioManagement.text356'); }, get download() { return tr('studioManagement.text357'); },
};
function iconLabel(name: string) { return uiIconName(name) || labels[name] || ({pen: tr('studioManagement.text358'), compass: tr('studioManagement.text359'), chart: tr('studioManagement.text316')} as Record<string,string>)[name] || (name ? tr('studioManagement.text360') : tr('studioManagement.text361')); }
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
      <div :id="id" class="starter-icon-popup" role="dialog" :aria-label="tr('studioManagement.text362', { value0: label })" @keydown="onKey">
        <div class="starter-icon-grid" role="group" :aria-label="tr('studioManagement.text363')">
          <button v-for="(name, i) in options" :key="name" type="button" class="starter-icon-tile"
            :class="{ selected: name === modelValue }" :data-icon-index="i" :tabindex="i === active ? 0 : -1"
            :aria-label="name ? tr('studioManagement.text364', { value0: iconLabel(name) }) : tr('studioManagement.text365')" :aria-pressed="name === modelValue"
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
