<script setup lang="ts">
import { computed } from "vue";
import { tokenize, type Token } from "../highlight";

const props = defineProps<{
  text: string;
  /** Validate the complete source before colouring a shortened preview. */
  jsonSource?: string;
  error?: boolean;
}>();

function validJSON(text: string): boolean {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

const jsonEnd = computed(() => {
  const source = props.jsonSource ?? props.text;
  if (validJSON(source)) return source.length;
  // A saved-result footer is separate display text, never proof of origin or
  // completeness. Keep it verbatim and uncoloured after a valid JSON payload.
  const footer = source.lastIndexOf("\n[Stored result: ");
  if (footer >= 0 && source.endsWith("]") && validJSON(source.slice(0, footer))) return footer;
  return null;
});
const tokens = computed<Token[]>(() => {
  const end = jsonEnd.value;
  if (end === null) return [{ text: props.text, kind: "plain" }];
  // Tokenize the original bytes: parse/stringify would round large numeric IDs
  // and alter the raw response. A preview remains a literal source prefix.
  const result = tokenize(props.text.slice(0, end), "json");
  if (props.text.length > end) result.push({ text: props.text.slice(end), kind: "plain" });
  return result;
});
</script>

<template>
  <pre class="result-content" :class="{ json: jsonEnd !== null, error }" tabindex="0"><span v-for="(token, i) in tokens" :key="i" :class="token.kind">{{ token.text }}</span></pre>
</template>

<style scoped>
.result-content {
  box-sizing: border-box;
  min-width: 0;
  max-width: 100%;
  max-height: 420px;
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-regular);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: 12px/1.6 var(--pg-mono);
}
.result-content:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.result-content.error {
  max-height: 320px;
  border-color: var(--el-color-danger-light-5);
  border-left: 2px solid var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.string { color: var(--pg-syn-string); }
.number { color: var(--pg-syn-number); }
.keyword { color: var(--pg-syn-keyword); }
</style>
