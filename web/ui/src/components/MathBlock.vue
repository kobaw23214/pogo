<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { Check, CopyDocument } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";
import CodeBlock from "./CodeBlock.vue";
import { renderMath } from "./markdownMath";

const props = defineProps<{ source: string }>();
const { t } = useI18n();
const copied = ref(false);
let copyTimer: ReturnType<typeof setTimeout> | undefined;

const rendered = computed(() => {
  try {
    return { html: renderMath(props.source, true), error: false };
  } catch {
    return { html: "", error: true };
  }
});

async function copySource() {
  try {
    await navigator.clipboard.writeText(props.source);
    copied.value = true;
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => { copied.value = false; }, 1200);
  } catch {
    copied.value = false;
  }
}
onBeforeUnmount(() => clearTimeout(copyTimer));
</script>

<template>
  <figure class="math-card" :aria-label="t('mathBlock.label')">
    <figcaption class="bar">
      <span class="label"><span class="symbol" aria-hidden="true">fx</span>{{ t('mathBlock.label') }}</span>
      <button type="button" class="action" @click="copySource">
        <el-icon aria-hidden="true"><Check v-if="copied" /><CopyDocument v-else /></el-icon>
        <span>{{ copied ? t('mathBlock.copied') : t('mathBlock.copy') }}</span>
      </button>
    </figcaption>
    <div v-if="!rendered.error" class="math-content" tabindex="0" :aria-label="t('mathBlock.scrollLabel')" v-html="rendered.html" />
    <div v-else class="math-fallback">
      <p role="alert">{{ t('mathBlock.failed') }}</p>
      <CodeBlock :code="source" lang="latex" :collapsed-height="240" />
    </div>
  </figure>
</template>

<style scoped>
.math-card {
  position: relative;
  min-width: 0;
  margin: 0.85em 0;
  overflow: hidden;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 34px;
  padding: 4px 8px 4px 12px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  background: var(--el-fill-color-lighter);
}
.label {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  font-weight: 600;
}
.symbol {
  color: var(--el-color-primary);
  font-family: ui-serif, Georgia, serif;
  font-size: 13px;
  font-style: italic;
  font-weight: 700;
}
.action {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 7px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}
.action:hover { background: var(--el-fill-color); color: var(--el-text-color-primary); }
.math-content {
  overflow-x: auto;
  overflow-y: hidden;
  padding: 22px 24px;
  color: var(--el-text-color-primary);
  line-height: 1.7;
  text-align: center;
  scrollbar-width: thin;
}
.math-content:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: -2px; }
.math-content :deep(.katex-display) { margin: 0; }
.math-content :deep(.katex) { font-size: 1.08em; }
.math-fallback { padding: 12px; }
.math-fallback > p { margin: 0 0 8px; color: var(--el-color-warning); font-size: 12px; }
@media (max-width: 600px) {
  .math-content { padding: 18px 14px; text-align: left; }
}
</style>
