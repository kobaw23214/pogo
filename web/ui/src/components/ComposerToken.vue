<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import CommandTokenIcon from "./CommandTokenIcon.vue";
import "./commandToken.css";

const props = defineProps<{
  kind: "skill" | "command";
  name: string;
  label: string;
  typeLabel: string;
  description?: string;
  contextPercent?: number | null;
  disabled?: boolean;
}>();
const emit = defineEmits<{ change: []; remove: [] }>();
const { t } = useI18n();
const primary = ref<HTMLButtonElement>();
const invocation = computed(() => props.kind === "skill" ? `/skill:${props.name}` : `/${props.name}`);
const title = computed(() => [invocation.value, props.description].filter(Boolean).join("\n"));

function onKeydown(event: KeyboardEvent) {
  if (event.isComposing || event.keyCode === 229 || event.ctrlKey || event.metaKey || event.altKey) return;
  if (event.key !== "Backspace" && event.key !== "Delete") return;
  // The parent first moves focus here from the start of the task. Holding that
  // same key must never turn that focus change into a destructive action.
  event.preventDefault();
  event.stopPropagation();
  if (!event.repeat && !props.disabled) emit("remove");
}

defineExpose({ focus: () => primary.value?.focus() });
</script>

<template>
  <span class="composer-token command-token-surface command-token-editable" :class="[kind, { disabled }]" :data-kind="kind" :data-name="name" data-testid="composer-token" @keydown="onKeydown">
    <button ref="primary" type="button" class="token-primary command-token-primary" data-testid="composer-token-change" :disabled="disabled" :title="title" :aria-label="t('agentView.commands.changeToken', { label })" @click="emit('change')">
      <span class="command-token-copy">
        <CommandTokenIcon :kind="kind" :context-percent="contextPercent" />
        <span class="command-token-type">{{ typeLabel }}</span>
        <span class="command-token-name">{{ label }}</span>
      </span>
    </button>
    <button type="button" class="token-remove command-token-remove" data-testid="composer-token-remove" :disabled="disabled" :title="t('agentView.commands.removeToken', { label })" :aria-label="t('agentView.commands.removeToken', { label })" @click="emit('remove')">
      <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><path d="m6 6 8 8m0-8-8 8" /></svg>
    </button>
  </span>
</template>

<style scoped>
.composer-token { vertical-align: middle; }
</style>
