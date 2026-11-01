<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { Close, CopyDocument, Check } from "@element-plus/icons-vue";
import type { MessageInputPart } from "@/api/types";
import { inputPartsText } from "@/agent/shortcutStructure";
import ShortcutContent from "./ShortcutContent.vue";
import { starterIconFor } from "@/components/starterIcons";
import ShortcutLanguageSelect from "./ShortcutLanguageSelect.vue";
import { shortcutLanguage } from "./shortcutPresentation";

const props = defineProps<{ title: string; content: string; icon?: string; language?: string; parts?: MessageInputPart[]; closable?: boolean }>();
const emit = defineEmits<{ close: [] }>();
const { t } = useI18n();
const copyText = computed(() => props.parts?.length ? inputPartsText(props.parts) : props.content);
const iconName = computed(() => starterIconFor(props.icon));
// Read-only viewers may choose a different syntax without mutating the snapshot.
const displayLanguage = ref<string>(shortcutLanguage(props.language));
watch(() => [props.title, props.content, props.language], () => { displayLanguage.value = shortcutLanguage(props.language); });
const copyState = ref<"" | "copied" | "failed">("");
let resetTimer: ReturnType<typeof setTimeout> | undefined;
async function copy() {
  clearTimeout(resetTimer);
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(copyText.value);
    else {
      const selection = document.getSelection(), saved = selection?.rangeCount ? selection.getRangeAt(0).cloneRange() : undefined;
      const active = document.activeElement as HTMLElement | null;
      const text = document.createElement("textarea"); text.value = copyText.value; text.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0"; document.body.append(text); text.select();
      const copied = document.execCommand("copy"); text.remove();
      if (selection && saved) { selection.removeAllRanges(); selection.addRange(saved); }
      active?.focus({ preventScroll: true });
      if (!copied) throw new Error("Clipboard unavailable");
    }
    copyState.value = "copied";
  } catch { copyState.value = "failed"; }
  resetTimer = setTimeout(() => { copyState.value = ""; }, 2500);
}
onBeforeUnmount(() => clearTimeout(resetTimer));
</script>

<template>
  <div class="shortcut-details" data-testid="shortcut-details">
    <header class="shortcut-details-heading"><div><small>{{ t("shortcuts.referencedData") }}</small><strong><Icon v-if="iconName" :icon="iconName" aria-hidden="true" /><span>{{ title }}</span></strong></div><button v-if="closable" type="button" class="shortcut-details-close" :aria-label="t('shortcuts.close')" @click="emit('close')"><el-icon><Close /></el-icon></button></header>
    <div class="shortcut-details-toolbar"><ShortcutLanguageSelect v-model="displayLanguage" /></div>
    <div class="shortcut-details-body" tabindex="0" data-testid="shortcut-reference-body"><ShortcutContent :parts="parts" :content="content" :language="displayLanguage" /></div>
    <footer><p>{{ t("shortcuts.snapshotHint") }}</p><button type="button" class="shortcut-details-copy" :class="{ copied: copyState === 'copied' }" @click.stop="copy"><el-icon><Check v-if="copyState === 'copied'" /><CopyDocument v-else /></el-icon>{{ t(copyState === "copied" ? "shortcuts.copied" : "shortcuts.copy") }}</button><span v-if="copyState === 'failed'" class="shortcut-copy-error" role="status">{{ t("shortcuts.copyFailed") }}</span></footer>
  </div>
</template>

<style scoped>
.shortcut-details { display:flex; flex-direction:column; box-sizing:border-box; width:100%; max-width:100%; min-width:0; min-height:0; max-height:var(--shortcut-details-max-height,none); color:var(--el-text-color-primary); white-space:normal; text-align:left; }
.shortcut-details-heading { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; padding-bottom:12px; border-bottom:1px solid var(--el-border-color-lighter); }.shortcut-details-heading > div { display:flex; min-width:0; flex-direction:column; gap:5px; }.shortcut-details-heading small { color:var(--el-text-color-secondary); font-size:11px; }.shortcut-details-heading strong { display:flex; align-items:flex-start; gap:6px; font-size:14px; font-weight:550; line-height:1.5; overflow-wrap:anywhere; }.shortcut-details-heading strong svg { flex:none; width:17px; height:17px; margin-top:2px; color:var(--el-color-primary); }
.shortcut-details-close { display:grid; place-items:center; flex:none; width:24px; height:24px; padding:0; border:0; border-radius:5px; background:transparent; color:var(--el-text-color-secondary); cursor:pointer; }.shortcut-details-close:hover { background:var(--el-fill-color-light); }
.shortcut-details-heading,.shortcut-details-toolbar,footer { flex:none; }
.shortcut-details-heading strong { min-width:0; }
.shortcut-details-heading strong > span { min-width:0; white-space:normal; overflow-wrap:anywhere; }
.shortcut-details-toolbar { display:flex; justify-content:flex-end; flex-wrap:wrap; margin:10px 0; }
.shortcut-details-body { flex:0 1 auto; min-height:0; min-width:0; max-width:100%; max-height:min(460px,55dvh); margin:0 0 13px; overflow:auto; overscroll-behavior:contain; scrollbar-gutter:stable; font-size:12px; line-height:1.75; user-select:text; white-space:normal; overflow-wrap:anywhere; }
.shortcut-details-body :deep(.bar) { display:none; }
.shortcut-details-body :deep(.code-block) { box-sizing:border-box; min-width:0; max-width:100%; border:0; border-radius:6px; }
.shortcut-details-body :deep(.body) { min-width:0; max-width:100%; overflow:visible; }
.shortcut-details-body :deep(pre) { box-sizing:border-box; min-width:0; max-width:100%; white-space:pre-wrap; overflow-wrap:anywhere; }
/* CodeBlock renders each logical line as a flex row. Its .txt normally keeps
   preformatted code on one line; resetting only pre does not override that. */
.shortcut-details-body :deep(.row) { min-width:0; max-width:100%; }
.shortcut-details-body :deep(.txt) { flex:1 1 auto; min-width:0; max-width:100%; white-space:pre-wrap; overflow-wrap:anywhere; }
footer { display:flex; align-items:center; flex-wrap:wrap; gap:8px 12px; padding-top:10px; border-top:1px solid var(--el-border-color-lighter); }footer p { flex:1 1 180px; margin:0; color:var(--el-text-color-placeholder); font-size:10px; line-height:1.5; }
.shortcut-details-copy { display:inline-flex; align-items:center; justify-content:center; gap:5px; flex:none; min-height:28px; padding:4px 8px; border:1px solid var(--el-border-color-light); border-radius:6px; background:var(--el-bg-color); color:var(--el-text-color-regular); font:inherit; font-size:11px; cursor:pointer; }.shortcut-details-copy:hover { border-color:var(--el-color-primary); color:var(--el-color-primary); }.shortcut-details-copy.copied { color:var(--el-color-success); }.shortcut-copy-error { flex-basis:100%; color:var(--el-color-danger); font-size:11px; }
button:focus-visible,.shortcut-details-body:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
</style>
