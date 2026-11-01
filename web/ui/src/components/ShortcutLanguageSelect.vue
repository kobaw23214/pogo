<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { shortcutLanguages } from "./shortcutPresentation";
const value = defineModel<string>({ default: "text" });
defineProps<{ disabled?: boolean }>();
const { t } = useI18n();
</script>

<template>
  <label class="shortcut-language-select"><span>{{ t("shortcuts.highlightType") }}</span><select v-model="value" :disabled="disabled" :aria-label="t('shortcuts.highlightType')" data-testid="shortcut-language"><option v-for="language in shortcutLanguages" :key="language" :value="language">{{ t(`shortcuts.languages.${language}`) }}</option></select></label>
</template>

<style scoped>
.shortcut-language-select { display:inline-flex; align-items:center; gap:7px; min-width:0; color:var(--el-text-color-secondary); font-size:11px; }
select { max-width:180px; min-height:30px; border:1px solid var(--el-border-color-light); border-radius:7px; padding:3px 23px 3px 8px; background:var(--el-bg-color); color:var(--el-text-color-primary); font:inherit; font-size:12px; cursor:pointer; }
select:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
select:disabled { cursor:not-allowed; opacity:.5; }
</style>
