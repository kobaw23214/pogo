<script setup lang="ts">
import { useI18n } from "vue-i18n";
import CodeBlock from "../CodeBlock.vue";
import { formatDuration } from "@/agent/timeline";
import type { BashDetails, ToolResult } from "@/api/types";

defineProps<{ result: ToolResult; details: BashDetails }>();

const { t } = useI18n();
</script>

<template>
  <div class="bash">
    <div class="meta">
      <span v-if="details.background && (!details.status || details.status === 'running')" class="background-status">{{ t(details.status === 'running' ? "bashResult.backgroundRunning" : "bashResult.backgroundDispatched") }}</span>
      <span v-else-if="typeof details.exit_code === 'number'" class="code" :class="{ bad: details.exit_code !== 0 }">{{ t("bashResult.exitCode", { code: details.exit_code }) }}</span>
      <span v-else-if="details.status" :class="{ bad: details.status !== 'completed' }">{{ t(`backgroundShell.state_${details.status}`) }}</span>
      <span v-if="typeof details.duration_ms === 'number' && Number.isFinite(details.duration_ms) && details.duration_ms >= 0 && (!details.background || (details.status && details.status !== 'running'))">{{ formatDuration(details.duration_ms) }}</span>
      <!-- Only present when the command ran somewhere other than the session's own
           directory. Worth a line of its own: reading an exit code without knowing
           which directory produced it is the confusion the tool's description exists
           to prevent, and the same output can be right in one tree and wrong in
           another. -->
      <span v-if="details.workdir" class="where" :title="details.workdir">
        {{ t("bashResult.inDirectory", { dir: details.workdir }) }}
      </span>
      <span v-if="details.timed_out" class="bad">{{ t("bashResult.timedOut") }}</span>
      <!-- Facts of the run, not decoration: confinement changes what a failure
           means (a "permission denied" from a sandboxed command is the boundary
           working, not the tool breaking). -->
      <span v-if="details.sandboxed" class="sandboxed">{{ t("bashResult.sandboxed") }}</span>
      <span v-if="details.truncated" class="warn" :title="details.full_output_path">
        {{ details.full_output_path ? t("bashResult.truncated", { path: details.full_output_path }) : t("backgroundShell.tailHint") }}
      </span>
    </div>
    <!-- No lang: command output is not code, and highlighting a stack trace's
         keywords is noise. -->
    <CodeBlock :code="result.text || t('bashResult.noOutput')" terminal />
  </div>
</template>

<style scoped lang="scss">
.meta {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 6px;
  flex-wrap: wrap;
}

.background-status { color: var(--el-color-primary); }

.code {
  font-family: ui-monospace, monospace;
  color: var(--el-color-success);
}

/* Clipped from the left: the tail of a path is the part that identifies it. */
.where {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: rtl;
  text-align: left;
  font-family: ui-monospace, monospace;
}

.bad {
  color: var(--el-color-danger);
}

.sandboxed {
  color: var(--el-color-info);
  border: 1px solid var(--el-border-color);
  border-radius: 3px;
  padding: 0 4px;
  font-size: 10px;
}

.warn {
  color: var(--el-color-warning);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
