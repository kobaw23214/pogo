<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowDown, Check, Loading, WarningFilled } from "@element-plus/icons-vue";
import type { ImageDescribe } from "@/agent/useAgentStream";
import { formatDuration } from "@/agent/timeline";

// Step one of an image turn on a text-only conversation model: the vision
// helper describes each attached image before the main model answers. One row
// per image — thumbnail, name, and the step's state: a spinner while it runs
// (this is the operation that can take tens of seconds, so it gets a named,
// visible step), the description behind an expander once it lands, and the
// failure plus its fallback when it does not. The settled text also lives on
// as the bubble's image alt, so this card retires at run_end without losing
// the record.
defineProps<{ items: ImageDescribe[]; urlFor: (ref: string) => string }>();

const { t } = useI18n();
const open = ref<Record<string, boolean>>({});
function toggle(ref: string) {
  open.value = { ...open.value, [ref]: !open.value[ref] };
}
</script>

<template>
  <!-- role=status: step transitions announce politely, and unlike token
       streaming there are only a handful of them. -->
  <div class="describe-cards" role="status" aria-live="polite">
    <div class="describe-tag">{{ t("agentView.describe.stepTag") }}</div>
    <div v-for="d in items" :key="d.ref" class="describe-card" :class="d.status">
      <img class="describe-thumb" :src="urlFor(d.ref)" :alt="d.name" loading="lazy" />
      <div class="describe-main">
        <div class="describe-head">
          <span class="describe-name" :title="d.name">{{ d.name }}</span>
          <span v-if="d.status === 'running'" class="describe-state">
            <el-icon class="is-loading"><Loading /></el-icon>
            {{ t("agentView.describe.running") }}…
          </span>
          <span v-else-if="d.status === 'done'" class="describe-state ok">
            <el-icon><Check /></el-icon>
            {{ t("agentView.describe.done") }}<template v-if="d.durationMs"> · {{ formatDuration(d.durationMs) }}</template>
          </span>
          <span v-else class="describe-state bad">
            <el-icon><WarningFilled /></el-icon>
            {{ t("agentView.describe.failed") }}
          </span>
          <button v-if="d.text" class="describe-toggle" @click="toggle(d.ref)">
            {{ open[d.ref] ? t("agentView.describe.collapse") : t("agentView.describe.expand") }}
            <el-icon class="describe-caret" :class="{ flipped: open[d.ref] }"><ArrowDown /></el-icon>
          </button>
        </div>
        <div v-if="d.error" class="describe-error">{{ d.error }}，{{ t("agentView.describe.fallback") }}</div>
        <div v-if="d.text && open[d.ref]" class="describe-text">{{ d.text }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.describe-cards {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 6px 0 2px;
}

/* The step tag is the mental map (step 1 of 2), not a title: quiet caps that
   read as a label for the group below. */
.describe-tag {
  font-size: 11px;
  letter-spacing: 0.02em;
  color: var(--el-text-color-secondary);
}

.describe-card {
  display: flex;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-fill-color-lighter);

  &.failed {
    border-color: var(--el-color-warning-light-5);
  }
}

.describe-thumb {
  width: 44px;
  height: 44px;
  object-fit: cover;
  border-radius: 6px;
  flex: 0 0 auto;
}

.describe-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.describe-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 22px;
}

.describe-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: var(--el-text-color-regular);
}

.describe-state {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  &.ok {
    color: var(--el-color-success);
  }

  &.bad {
    color: var(--el-color-warning);
  }
}

.describe-toggle {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
  border: 0;
  background: transparent;
  padding: 2px 4px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  flex: 0 0 auto;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.describe-caret {
  transition: transform 0.15s ease;

  &.flipped {
    transform: rotate(180deg);
  }
}

.describe-error {
  font-size: 11px;
  color: var(--el-color-warning);
}

/* The description is progressive disclosure: collapsed behind the toggle,
   monospace-quiet when open, and capped so a long OCR does not take over the
   timeline — it scrolls inside. */
.describe-text {
  max-height: 180px;
  overflow: auto;
  padding: 6px 8px;
  border-radius: 6px;
  background: var(--el-bg-color);
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
