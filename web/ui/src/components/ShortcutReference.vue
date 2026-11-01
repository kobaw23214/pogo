<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import type { MessageInputPart } from "@/api/types";
import { starterIconFor } from "@/components/starterIcons";
import ShortcutDetails from "./ShortcutDetails.vue";
import { shortcutSearchSegments } from "./shortcutPresentation";

const props = defineProps<{ title: string; content: string; icon?: string; language?: string; parts?: MessageInputPart[]; detailsOnly?: boolean; highlight?: string }>();
const { t } = useI18n();
const trigger = ref<HTMLButtonElement>();
const panel = ref<HTMLDivElement>();
const popover = ref<{ popperRef?: { popperInstanceRef?: { update: () => unknown } } }>();
const visible = ref(false);
const pinned = ref(false);
const iconName = computed(() => starterIconFor(props.icon));
const titleSegments = computed(() => shortcutSearchSegments(props.title, props.highlight || ""));
let closeTimer: ReturnType<typeof setTimeout> | undefined;
let skipRestoredFocus = false;
function enter() { clearTimeout(closeTimer); visible.value = true; }
function leave() { clearTimeout(closeTimer); if (!pinned.value) closeTimer = setTimeout(() => { visible.value = false; }, 180); }
function focus() { if (skipRestoredFocus) { skipRestoredFocus = false; return; } enter(); }
function close(restoreFocus = false) { clearTimeout(closeTimer); visible.value = false; pinned.value = false; if (restoreFocus && trigger.value && document.activeElement !== trigger.value) { skipRestoredFocus = true; trigger.value.focus(); } }
function toggle() { pinned.value = !pinned.value; visible.value = true; if (!pinned.value) leave(); }
function outside(event: PointerEvent) { const target = event.target as Node; if (!trigger.value?.contains(target) && !panel.value?.contains(target)) close(); }
function escape(event: KeyboardEvent) { if (event.key === "Escape" && visible.value) { event.preventDefault(); event.stopPropagation(); close(true); } }
watch(visible, open => { if (open) document.addEventListener("pointerdown", outside, true); else document.removeEventListener("pointerdown", outside, true); });
// With lazy popover mounting the positioning layer may first see visible=true.
// Observe our mounted panel as well so expanding a nested snapshot or changing
// syntax repositions the larger surface instead of leaving its footer offscreen.
watch(panel, (element, _previous, cleanup) => {
  if (!element || typeof ResizeObserver === "undefined") return;
  const observer = new ResizeObserver(() => { popover.value?.popperRef?.popperInstanceRef?.update(); });
  observer.observe(element);
  cleanup(() => observer.disconnect());
}, { flush: "post" });
onBeforeUnmount(() => { clearTimeout(closeTimer); document.removeEventListener("pointerdown", outside, true); });
</script>

<template>
  <ShortcutDetails v-if="detailsOnly" :title="title" :content="content" :icon="icon" :language="language" :parts="parts" />
  <span v-else class="shortcut-reference" data-testid="shortcut-reference" @keydown="escape">
    <el-popover ref="popover" :visible="visible" :width="'min(600px, calc(100vw - 24px))'" placement="top-start" popper-class="shortcut-reference-popover" :show-arrow="true" :persistent="false" :popper-options="{ strategy: 'fixed', modifiers: [{ name: 'preventOverflow', options: { padding: 12, altAxis: true, tether: false } }, { name: 'flip', options: { padding: 12 } }] }">
      <template #reference><button ref="trigger" type="button" class="shortcut-reference-trigger" :class="{ active: visible }" :aria-label="t('shortcuts.preview', { title })" aria-haspopup="dialog" :aria-expanded="visible" data-reference-kind="shortcut" @mouseenter="enter" @mouseleave="leave" @focus="focus" @blur="leave" @click.stop="toggle"><Icon v-if="iconName" :icon="iconName" aria-hidden="true" /><span><template v-for="(segment, index) in titleSegments" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></span></button></template>
      <div ref="panel" class="shortcut-reference-preview" role="dialog" :aria-label="title" @mouseenter="enter" @mouseleave="leave" @focusin="enter" @focusout="leave" @keydown="escape">
        <ShortcutDetails :title="title" :content="content" :icon="icon" :language="language" :parts="parts" closable @close="close(true)" />
      </div>
    </el-popover>
  </span>
</template>

<style scoped>
.shortcut-reference { display:inline; min-width:0; max-width:100%; font:inherit; vertical-align:baseline; }
.shortcut-reference-trigger { display:inline-flex; align-items:center; vertical-align:baseline; gap:5px; min-width:0; max-width:min(100%,300px); margin:1px 3px; padding:1px 7px; border:1px solid color-mix(in srgb,var(--el-color-primary) 28%,transparent); border-radius:7px; background:color-mix(in srgb,var(--el-color-primary) 8%,transparent); color:var(--el-color-primary); font:inherit; font-size:.9em; line-height:1.65; white-space:nowrap; cursor:pointer; }
.shortcut-reference-trigger span { overflow:hidden; text-overflow:ellipsis; }.shortcut-reference-trigger svg { width:15px; height:15px; flex:none; }.shortcut-reference-trigger:hover,.shortcut-reference-trigger.active { border-color:var(--el-color-primary); background:var(--el-color-primary-light-9); }
.shortcut-reference-preview { display:flex; flex-direction:column; color:var(--el-text-color-primary); min-width:0; min-height:0; max-width:100%; white-space:normal; }
.shortcut-reference-trigger:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
mark { padding:0 1px; border-radius:2px; background:color-mix(in srgb,var(--el-color-primary) 22%,transparent); color:inherit; }
</style>

<style>
.shortcut-reference-popover.el-popover { --shortcut-details-max-height:min(640px,calc(100dvh - 64px)); box-sizing:border-box; max-width:calc(100vw - 24px); padding:14px 16px; border-radius:12px; box-shadow:0 8px 30px rgb(0 0 0 / 12%); white-space:normal; }
@media(max-width:480px) { .shortcut-reference-popover.el-popover { padding:12px; } }
</style>
