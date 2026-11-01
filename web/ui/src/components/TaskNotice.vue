<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { TopRight } from "@element-plus/icons-vue";
import AnswerText from "./AnswerText.vue";
import Chevron from "./Chevron.vue";
import { childSessionId, parseTaskNotification } from "@/agent/timeline";
import type { OpenSheetRequest } from "./dockSheets";

/**
 * A background subagent's completion, as a card rather than a bubble.
 *
 * The notification is a harness-written user-role message (the one legal
 * injection point), and it used to render as the raw envelope — Status:,
 * Full transcript:, Tokens: and all — which read as noise next to real
 * conversation. The envelope is for the model; this card is for the person:
 * one status line, the outcome folded behind it, and the metadata that
 * answers "which task, what did it cost, where is the detail" without
 * opening anything. Status is icon + text + colour together, never colour
 * alone.
 */
const props = defineProps<{
  /** The full user-role notification text, envelope and all. */
  text: string;
}>();

const emit = defineEmits<{
  /** "查看过程": open the child transcript as a dock tab. */
  "open-sheet": [OpenSheetRequest];
}>();

const { t } = useI18n();

const notice = computed(() => parseTaskNotification(props.text));
const title = computed(() => {
  const value = notice.value;
  return value?.kind === "shell" ? `${t("backgroundShell.label")} · ${t(`backgroundShell.state_${value.status}`)}`
    : t(value?.failed ? "taskNotice.failed" : "taskNotice.completed");
});
const open = ref(false);

const transcriptId = computed(() => childSessionId(notice.value?.transcript));

function viewProcess() {
  if (!notice.value || !transcriptId.value) return;
  emit("open-sheet", { childSession: transcriptId.value, label: notice.value.brief, ...(notice.value.agentId ? { agentId: notice.value.agentId } : {}) });
}

// Token counts are orientation, not billing: compact thousands, the same way
// the usage meter prints them.
function compact(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}K` : String(n);
}
</script>

<template>
  <!-- parse failure degrades to the raw text: a malformed envelope is still
       information, and hiding it would look like a lost notification. -->
  <div v-if="!notice" class="tn raw">{{ text }}</div>
  <div v-else class="tn" :class="{ failed: notice.failed, open }">
    <button class="head" :aria-expanded="open" @click="open = !open">
      <span class="st" :class="{ bad: notice.failed }" aria-hidden="true">{{ notice.failed ? "✕" : "✓" }}</span>
      <span class="ttl">{{ title }}</span>
      <span class="brief">{{ notice.brief }}</span>
      <span v-if="notice.turns !== undefined" class="meta">{{ t("taskNotice.turns", { n: notice.turns }) }}</span>
      <Chevron class="chev" :open="open" />
    </button>
    <div v-if="open" class="body">
      <div v-if="notice.error" class="err">{{ notice.error }}</div>
      <template v-if="notice.kind === 'shell'">
        <pre v-if="notice.command" class="shell-command">$ {{ notice.command }}</pre>
        <p v-if="notice.workdir" class="shell-workdir">{{ notice.workdir }}</p>
        <pre v-if="notice.result !== undefined" class="shell-output">{{ notice.result || t('backgroundShell.noOutput') }}</pre>
      </template>
      <AnswerText v-else-if="!notice.error && notice.result" :source="notice.result" />
      <div class="foot">
        <span v-if="notice.kind === 'shell'" class="shell-id">{{ notice.id }}</span>
        <span v-if="notice.kind === 'shell' && typeof notice.exitCode === 'number'" class="tok">exit {{ notice.exitCode }}</span>
        <span v-if="notice.mode" class="chip">{{ notice.mode }}</span>
        <span v-if="notice.inputTok !== undefined" class="tok">
          {{ compact(notice.inputTok) }}↑ / {{ compact(notice.outTok ?? 0) }}↓
        </span>
        <button v-if="transcriptId" class="view" @click.stop="viewProcess">
          {{ t("taskNotice.viewProcess") }}
          <el-icon><TopRight /></el-icon>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
/* A system lane, not a bubble: full width, quiet border, nothing about it may
   read as a message someone typed. */
.tn {
  align-self: stretch;
  border: 1px solid var(--el-border-color-lighter);
  border-left: 2px solid var(--el-color-success);
  border-radius: 8px;
  margin: 4px 0;
  font-size: 12px;
  background: var(--el-bg-color);

  &.failed {
    border-left-color: var(--el-color-danger);
  }

  &.raw {
    padding: 8px 12px;
    white-space: pre-wrap;
    color: var(--el-text-color-secondary);
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 7px 10px;
  font: inherit;
  color: inherit;
  cursor: pointer;
  text-align: left;

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.st {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-color-success);

  &.bad {
    color: var(--el-color-danger);
  }
}

.ttl {
  flex: 0 0 auto;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.brief {
  flex: 1 1 0;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-secondary);
}

.meta {
  flex: 0 0 auto;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.chev {
  flex: 0 0 auto;
  color: var(--el-text-color-secondary);
}

.body {
  padding: 0 10px 8px 30px;
}

.err {
  color: var(--el-color-danger);
  white-space: pre-wrap;
  margin-bottom: 6px;
}

.foot {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
  padding-top: 6px;
  border-top: 1px dashed var(--el-border-color-extra-light);
  color: var(--el-text-color-secondary);
  font-size: 11px;
  flex-wrap: wrap;
}

.shell-command, .shell-output { white-space: pre-wrap; overflow-wrap: anywhere; font: 11px/1.6 var(--pg-mono); }
.shell-command { margin: 5px 0; }
.shell-workdir, .shell-id { font-size: 10px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; }
.shell-workdir { margin: 3px 0 7px; }
.shell-output { max-height: 300px; overflow: auto; padding: 8px; border-radius: 5px; background: var(--el-fill-color-light); margin: 5px 0; }

.chip {
  padding: 0 5px;
  border-radius: 3px;
  background: var(--el-fill-color);
}

.tok {
  font-variant-numeric: tabular-nums;
}

.view {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 0;
  background: transparent;
  padding: 2px 6px;
  border-radius: 5px;
  font: inherit;
  color: var(--el-color-primary);
  cursor: pointer;

  &:hover {
    background: color-mix(in srgb, var(--el-color-primary) 10%, transparent);
  }
}
</style>
