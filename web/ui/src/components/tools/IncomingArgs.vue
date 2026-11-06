<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { incomingPath, previewLines } from "@/agent/argsPreview";
import { createIncomingSampler } from "@/agent/incomingSampler";
import { resolveToolIdentity, useToolIdentityCatalog } from "@/agent/toolIdentity";
import type { ToolPresentation } from "@/agent/toolPresentation";
import type { IncomingArgs } from "@/api/types";
import type { OpenFileTarget } from "../dockSheets";
import ToolCallHeader from "../ToolCallHeader.vue";

const props = defineProps<{ incoming: IncomingArgs; compact?: boolean }>();
const emit = defineEmits<{ "open-file": [OpenFileTarget] }>();

const { t } = useI18n();
const catalog = useToolIdentityCatalog();
const sampled = ref({ ...props.incoming });
const sampler = createIncomingSampler<IncomingArgs>(value => { sampled.value = value; });
watch(
  () => [props.incoming.head, props.incoming.tail, props.incoming.bytes, props.incoming.lines, props.incoming.preview_added, props.incoming.preview_removed, props.incoming.name],
  () => sampler.push({ ...props.incoming }),
  { immediate: true },
);
onBeforeUnmount(sampler.cancel);

const fileTool = computed(() => props.incoming.name === "write" || props.incoming.name === "edit");
const path = computed(() => incomingPath(sampled.value));
function openPath() {
  const target = path.value;
  if (target) emit("open-file", { path: target, name: target.split(/[\\/]/).pop() || target });
}
const kb = computed(() => (sampled.value.bytes / 1024).toFixed(1));
const lines = computed(() => sampled.value.lines ?? 0);
const added = computed(() => sampled.value.preview_added ?? (props.incoming.name === "write" ? lines.value : 0));
const removed = computed(() => sampled.value.preview_removed ?? 0);
// Plain text on purpose: syntax-highlighting a partial document on every
// fragment is the O(n²) trap the raw tail window exists to avoid.
const preview = computed(() => previewLines(sampled.value.tail ?? "", 10).join("\n"));
const presentation = computed<ToolPresentation>(() => {
  const identity = resolveToolIdentity(props.incoming.name, path.value ? { path: path.value } : undefined, undefined, catalog.value);
  return {
    ...identity,
    name: t("toolDisclosure.generatingArguments"),
    secondary: identity.name,
    target: path.value || identity.target || t("toolDisclosure.objectPending"),
    protocolName: props.incoming.name,
    state: "done",
    statusLabel: t("toolDisclosure.notExecuted"),
  };
});
const fileTarget = computed<OpenFileTarget | undefined>(() => path.value ? { path: path.value, name: path.value.split(/[\\/]/).pop() || path.value } : undefined);
</script>

<template>
  <div class="incoming" :class="{ file: fileTool, compact }">
    <ToolCallHeader v-if="compact && !fileTool" layered :presentation="presentation" :file-target="fileTarget" @open-file="emit('open-file', $event)" />
    <!-- Active file generation keeps the original live row, diff counts and
         tail preview. Result disclosure must not hide work still streaming. -->
    <div v-else class="line">
      <span class="dot" />
      <span class="name">{{ incoming.name || t("incomingArgs.toolFallback") }}</span>
      <button v-if="path" type="button" class="path file-link" :title="t('toolCall.openFile', { path })" :data-open-file="path" @click="openPath">{{ path }}</button>
      <span v-else class="path">…</span>
      <span v-if="fileTool" class="diffstats">
        <span class="added">+{{ added }}</span>
        <span v-if="incoming.name === 'edit'" class="removed">-{{ removed }}</span>
      </span>
      <span class="badges">
        <span class="badge">{{ t(fileTool ? "incomingArgs.generating" : "incomingArgs.received", { kb, lines }) }}</span>
        <span v-if="fileTool" class="badge pending">{{ t("incomingArgs.notWritten") }}</span>
      </span>
    </div>
    <pre v-if="(fileTool || !compact) && preview" class="preview">{{ preview }}</pre>
  </div>
</template>

<style scoped lang="scss">
/* Quieter than a running tool card on purpose: the call has not started yet,
   its arguments are still being generated, so everything here is muted. */
.incoming {
  margin: 6px 0;
}
.incoming.compact:not(.file) { margin: 0; }

.line {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--el-text-color-secondary);
  flex: 0 0 auto;
  animation: pulse 1.6s ease-in-out infinite;
}

@keyframes pulse {
  50% {
    opacity: 0.25;
  }
}

.name {
  font-family: ui-monospace, monospace;
  font-weight: 600;
  color: var(--el-text-color-regular);
}

.path {
  min-width: 0;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.file-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--pg-link-color);
  text-align: left;
  cursor: pointer;
}
.file-link:hover { text-decoration: underline; text-underline-offset: 2px; }
.file-link:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; border-radius: 2px; }

.diffstats {
  display: inline-flex;
  gap: 4px;
  flex: 0 0 auto;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  .added { color: var(--el-color-success); }
  .removed { color: var(--el-color-danger); }
}

.badges {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-left: auto;
  flex: 0 0 auto;
}

.badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  flex: 0 0 auto;
}

.badge.pending {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.preview {
  margin: 6px 0 10px 14px;
  max-height: 220px;
  overflow: auto;
  font: 12px/1.55 ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  white-space: pre-wrap;
  word-break: break-all;
  border-left: 1px dashed var(--el-border-color);
  padding-left: 10px;
}
</style>
