<script setup lang="ts">
// A dependency-free recursive JSON tree for log payloads. Design notes come
// from the vue-json-pretty issue trail: toggling one node must not re-render
// the whole tree (each node owns its open state), collapsed containers show
// an item count ("{…} 3 项"), and long scalar values truncate until clicked.
// aria-expanded lives on the toggle button (WAI-ARIA treeview convention).
import { computed, ref } from "vue";

const props = withDefaults(
  defineProps<{
    value: unknown;
    // Containers open while deep > 0; each level down decrements it, so a
    // root deep of 3 shows three levels and collapses the fourth.
    deep?: number;
    label?: string;
  }>(),
  { deep: 3, label: "" },
);

// Two kinds of nodes start collapsed: arrays at any depth (a long list
// floods the card, and the "[…] n" hint already carries its size) and any
// container past the `deep` limit.
const open = ref(!Array.isArray(props.value) && props.deep > 0);
const expandedValue = ref(false);

const type = computed(() => {
  const v = props.value;
  if (v === null) return "null";
  if (Array.isArray(v)) return "array";
  return typeof v;
});
const isContainer = computed(() => type.value === "object" || type.value === "array");
const entries = computed(() => {
  const v = props.value;
  if (type.value === "object") return Object.entries(v as Record<string, unknown>);
  if (type.value === "array") return (v as unknown[]).map((x, i) => [String(i), x] as [string, unknown]);
  return [];
});
// Collapsed shows "{…} n" (a hint of what is hidden); expanded shows just
// the opening brace — the children follow, and closeShape closes the block
// after them, so an open node reads like the JSON it is. Empty containers
// render as literal {} / [].
const collapsedShape = computed(() => (type.value === "array" ? "[…]" : "{…}"));
const openShape = computed(() => (type.value === "array" ? "[" : "{"));
const closeShape = computed(() => (type.value === "array" ? "]" : "}"));
const emptyShape = computed(() => (type.value === "array" ? "[]" : "{}"));
const scalar = computed(() => {
  const v = props.value;
  if (isContainer.value) return "";
  const s = type.value === "null" ? "null" : type.value === "string" ? `"${v}"` : String(v);
  return s.length > 160 && !expandedValue.value ? s.slice(0, 160) + "…" : s;
});
const longValue = computed(() => {
  const v = props.value;
  return !isContainer.value && type.value !== "null" && String(v).length > 160;
});
const depth = computed(() => props.deep);
</script>

<template>
  <div class="jt" :class="`jt-${type}`">
    <span v-if="label" class="jt-key">{{ label }}: </span>
    <template v-if="!isContainer">
      <span class="jt-val" :class="{ 'jt-long': longValue }" :title="longValue ? '' : undefined"
            @click="longValue && (expandedValue = !expandedValue)">{{ scalar }}</span>
    </template>
    <template v-else>
      <button v-if="entries.length" class="jt-toggle" type="button"
              :aria-expanded="open" @click="open = !open">
        <span class="jt-arrow" :class="{ open }">▸</span>
        <span class="jt-brace">{{ open ? openShape : `${collapsedShape} ${entries.length}` }}</span>
      </button>
      <span v-else class="jt-brace">{{ emptyShape }}</span>
      <div v-show="open" class="jt-children" role="group">
        <JsonTree v-for="[k, v] in entries" :key="k" :value="v" :label="k" :deep="depth - 1" />
        <span class="jt-brace">{{ closeShape }}</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.jt {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
.jt-children {
  margin-left: 14px;
  border-left: 1px solid var(--el-border-color-lighter, #303030);
  padding-left: 8px;
}
.jt-toggle {
  background: none;
  border: none;
  padding: 0;
  color: inherit;
  cursor: pointer;
  font: inherit;
  text-align: left;
}
.jt-arrow {
  display: inline-block;
  transition: transform 0.12s ease;
  color: var(--el-text-color-secondary, #909399);
  margin-right: 4px;
}
.jt-arrow.open {
  transform: rotate(90deg);
}
.jt-brace {
  color: var(--el-text-color-secondary, #909399);
}
.jt-key {
  color: var(--el-color-primary, #58a6ff);
}
.jt-val {
  color: var(--el-text-color-primary, #e6edf3);
}
.jt-string .jt-val {
  color: #7ee787;
}
.jt-number .jt-val {
  color: #d2a8ff;
}
.jt-boolean .jt-val,
.jt-null .jt-val {
  color: #ffa657;
}
.jt-long {
  cursor: pointer;
  text-decoration: underline dotted;
}
</style>
