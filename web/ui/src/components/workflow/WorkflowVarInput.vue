<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";

/** A textarea with {{ }} template autocomplete: typing "{{" opens a dropdown
    of the references this node may legitimately name (upstream-closure nodes
    × artifact/summary, plus run.workspace), arrow keys walk it, Enter/Tab
    inserts, Esc closes. Deliberately dependency-free — a Tiptap-grade editor
    is not worth its weight for one trigger glyph. */
const props = withDefaults(
  defineProps<{
    modelValue: string;
    /** The upstream closure as autocomplete candidates. */
    closureNodes: { id: string; hasExpect: boolean }[];
    placeholder?: string;
    rows?: number;
    maxlength?: number;
    disabled?: boolean;
  }>(),
  { rows: 5, disabled: false, placeholder: "", maxlength: undefined },
);

const emit = defineEmits<{ "update:modelValue": [string] }>();

const { t } = useI18n();

interface Suggestion {
  insert: string;
  label: string;
  desc: string;
}

const all = computed<Suggestion[]>(() => {
  const out: Suggestion[] = [];
  for (const n of props.closureNodes) {
    if (n.hasExpect) {
      out.push({
        insert: `{{ nodes.${n.id}.artifact }}`,
        label: `nodes.${n.id}.artifact`,
        desc: t("workflow.var.artifact", { id: n.id }),
      });
    }
    out.push({
      insert: `{{ nodes.${n.id}.summary }}`,
      label: `nodes.${n.id}.summary`,
      desc: t("workflow.var.summary", { id: n.id }),
    });
  }
  out.push({ insert: "{{ run.workspace }}", label: "run.workspace", desc: t("workflow.var.workspace") });
  return out;
});

const area = ref<HTMLTextAreaElement | null>(null);
/** The index where the active "{{" started; -1 = dropdown closed. */
const triggerAt = ref(-1);
const caret = ref(0);
const highlight = ref(0);

const query = computed(() => (triggerAt.value < 0 ? "" : props.modelValue.slice(triggerAt.value + 2, caret.value)));
const suggestions = computed(() => {
  if (triggerAt.value < 0) return [];
  const q = query.value.trim().toLowerCase();
  const list = q ? all.value.filter((s) => s.label.toLowerCase().includes(q)) : all.value;
  return list.slice(0, 8);
});
const open = computed(() => suggestions.value.length > 0);

/** The caret is in suggestion mode when the last "{{" before it has no "}}"
    between it and the caret. */
function track() {
  const el = area.value;
  if (!el) return;
  caret.value = el.selectionStart ?? 0;
  const upto = props.modelValue.slice(0, caret.value);
  const at = upto.lastIndexOf("{{");
  triggerAt.value = at >= 0 && !upto.slice(at + 2).includes("}}") ? at : -1;
  if (triggerAt.value < 0) highlight.value = 0;
}

function onInput(event: Event) {
  emit("update:modelValue", (event.target as HTMLTextAreaElement).value);
  track();
}

function pick(s: Suggestion) {
  const at = triggerAt.value;
  const next = `${props.modelValue.slice(0, at)}${s.insert}${props.modelValue.slice(caret.value)}`;
  emit("update:modelValue", next);
  triggerAt.value = -1;
  // Caret lands after the inserted reference on the next tick.
  requestAnimationFrame(() => {
    const el = area.value;
    if (!el) return;
    el.focus();
    const caretPos = at + s.insert.length;
    el.setSelectionRange(caretPos, caretPos);
  });
}

function onKeydown(event: KeyboardEvent) {
  if (!open.value) return;
  if (event.key === "ArrowDown") {
    event.preventDefault();
    highlight.value = (highlight.value + 1) % suggestions.value.length;
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    highlight.value = (highlight.value - 1 + suggestions.value.length) % suggestions.value.length;
  } else if (event.key === "Enter" || event.key === "Tab") {
    event.preventDefault();
    const s = suggestions.value[highlight.value] ?? suggestions.value[0];
    if (s) pick(s);
  } else if (event.key === "Escape") {
    triggerAt.value = -1;
  }
}

function onBlur() {
  // A mousedown on the dropdown lands before blur; give it one tick.
  window.setTimeout(() => {
    triggerAt.value = -1;
  }, 120);
}
</script>

<template>
  <div class="wf-var">
    <textarea
      ref="area"
      class="wf-var-area"
      :value="modelValue"
      :rows="rows"
      :maxlength="maxlength"
      :placeholder="placeholder"
      :disabled="disabled"
      @input="onInput"
      @keydown="onKeydown"
      @click="track"
      @keyup="track"
      @blur="onBlur"
    />
    <div v-if="open" class="wf-var-pop" role="listbox">
      <button
        v-for="(s, i) in suggestions"
        :key="s.label"
        type="button"
        role="option"
        class="wf-var-item"
        :class="{ on: i === highlight }"
        :aria-selected="i === highlight"
        @mousedown.prevent="pick(s)"
        @mouseenter="highlight = i"
      >
        <code>{{ s.insert }}</code>
        <span>{{ s.desc }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.wf-var {
  position: relative;
}
.wf-var-area {
  width: 100%;
  box-sizing: border-box;
  resize: vertical;
  border: 1px solid var(--el-border-color);
  border-radius: var(--el-border-radius-base);
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
  font: inherit;
  font-size: 13px;
  line-height: 1.6;
  padding: 8px 11px;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.wf-var-area:hover {
  border-color: var(--el-border-color-hover);
}
.wf-var-area:focus {
  outline: none;
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.wf-var-area::placeholder {
  color: var(--el-text-color-placeholder);
}
.wf-var-area:disabled {
  background: var(--el-disabled-bg-color);
  color: var(--el-disabled-text-color);
}
.wf-var-pop {
  position: absolute;
  left: 0;
  right: 0;
  top: calc(100% + 4px);
  z-index: 60;
  background: var(--el-bg-color-overlay);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  box-shadow: var(--el-box-shadow);
  padding: 4px;
  max-height: 240px;
  overflow-y: auto;
}
.wf-var-item {
  display: flex;
  flex-direction: column;
  gap: 1px;
  width: 100%;
  border: 0;
  border-radius: 7px;
  background: transparent;
  padding: 7px 10px;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.wf-var-item.on {
  background: var(--pg-accent-wash);
}
.wf-var-item code {
  font-family: var(--pg-mono);
  font-size: 12px;
  color: var(--el-color-primary);
}
.wf-var-item span {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
