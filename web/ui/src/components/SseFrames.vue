<script setup lang="ts">
// Renders a captured SSE response body as one card per frame — the shape
// every protocol debugger converged on (HTTP Debugger's event list, MCP
// Inspector's Protocol cards; see MCP-日志方案.md). A card's header line is
// the classification: kind badge (response/error/notification/request),
// method or json-rpc id, the error message when there is one, and the data
// size. The body is the collapsible JSON tree. A Pretty/Raw segmented toggle
// sits at the section head, same place Postman puts it, and the choice is
// remembered (localStorage) because the people who want Raw always want Raw.
//
// Everything here is view-layer: the server stores raw text and the parser
// (sseFrames.ts) does the splitting, so a partial frame cut by the capture
// cap still shows, flagged, instead of silently disappearing.
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import JsonTree from "@/components/JsonTree.vue";
import { parseSseFrames, summarizeFrame, type FrameSummary } from "./sseFrames";

const props = defineProps<{ text: string; truncated?: boolean }>();

const { t } = useI18n();

type View = "pretty" | "raw";
const VIEW_KEY = "mcpLogs.respView";
const view = ref<View>(
  ((): View => {
    try {
      return localStorage.getItem(VIEW_KEY) === "raw" ? "raw" : "pretty";
    } catch {
      return "pretty";
    }
  })(),
);
function setView(v: View) {
  view.value = v;
  try {
    localStorage.setItem(VIEW_KEY, v);
  } catch {
    /* storage disabled — session-only preference */
  }
}

const frames = computed(() => parseSseFrames(props.text));

const kindLabel: Record<FrameSummary["kind"], string> = {
  response: "mcpLogs.frameResponse",
  error: "mcpLogs.frameError",
  notification: "mcpLogs.frameNotification",
  request: "mcpLogs.frameRequest",
  text: "mcpLogs.frameText",
};

const open = ref<number[]>([]);
function toggle(i: number) {
  open.value = open.value.includes(i) ? open.value.filter((x) => x !== i) : [...open.value, i];
}

function fmtBytes(n: number): string {
  return n < 1024 ? `${n}B` : `${(n / 1024).toFixed(1)}KB`;
}
</script>

<template>
  <div class="sf">
    <div class="sf-head">
      <span class="sf-count">{{ t("mcpLogs.frameCount", { n: frames.length }) }}</span>
      <div class="sf-view" role="group" :aria-label="t('mcpLogs.viewLabel')">
        <button type="button" :class="{ on: view === 'pretty' }" @click="setView('pretty')">
          {{ t("mcpLogs.viewPretty") }}
        </button>
        <button type="button" :class="{ on: view === 'raw' }" @click="setView('raw')">
          {{ t("mcpLogs.viewRaw") }}
        </button>
      </div>
    </div>

    <!-- Raw: the wire text, verbatim, for取证. -->
    <div v-if="view === 'raw'" class="ml-raw">
      <pre class="mono">{{ text }}</pre>
    </div>

    <template v-else>
      <button
        v-for="(f, i) in frames"
        :key="i"
        type="button"
        class="sf-card"
        :class="summarizeFrame(f).kind"
        :aria-expanded="open.includes(i)"
        @click="toggle(i)"
      >
        <span class="sf-idx">{{ i + 1 }}</span>
        <span class="sf-badge" :class="summarizeFrame(f).kind">{{ t(kindLabel[summarizeFrame(f).kind]) }}</span>
        <span class="sf-mono">
          <template v-if="summarizeFrame(f).method">{{ summarizeFrame(f).method }}</template>
          <template v-else-if="summarizeFrame(f).id !== undefined">id={{ summarizeFrame(f).id }}</template>
          <template v-else>{{ f.event }}</template>
        </span>
        <span v-if="summarizeFrame(f).kind === 'error'" class="sf-err">
          {{ summarizeFrame(f).message ?? "error" }}
        </span>
        <span class="sf-spacer" />
        <span v-if="f.partial" class="sf-partial">{{ t("mcpLogs.framePartial") }}</span>
        <span class="sf-size">{{ fmtBytes(f.data.length) }}</span>
        <span class="sf-arrow" :class="{ open: open.includes(i) }">▸</span>
        <div v-if="open.includes(i)" class="sf-body" @click.stop>
          <JsonTree v-if="f.json !== undefined" :value="f.json" :deep="3" />
          <pre v-else class="mono">{{ f.data }}</pre>
        </div>
      </button>
    </template>
  </div>
</template>

<style scoped>
.sf { display: flex; flex-direction: column; gap: 4px; }
.sf-head { display: flex; align-items: center; justify-content: space-between; }
.sf-count { color: var(--el-text-color-secondary); font-size: 11px; }
.sf-view { display: inline-flex; border: 1px solid var(--el-border-color); border-radius: 4px; overflow: hidden; }
.sf-view button {
  border: none; background: none; color: var(--el-text-color-secondary);
  font-size: 11px; padding: 1px 8px; cursor: pointer; font: inherit;
}
.sf-view button.on { background: var(--el-fill-color); color: var(--el-text-color-primary); }
.sf-card {
  display: flex; align-items: center; flex-wrap: wrap; gap: 6px; width: 100%;
  border: 1px solid var(--el-border-color-lighter); border-left: 3px solid var(--el-border-color);
  border-radius: 4px; background: var(--el-fill-color-blank); padding: 3px 8px;
  cursor: pointer; font: inherit; font-size: 12px; text-align: left;
  color: var(--el-text-color-primary);
}
.sf-card:hover { background: var(--el-fill-color-light); }
.sf-card.response { border-left-color: var(--el-color-success); }
.sf-card.error { border-left-color: var(--el-color-error); }
.sf-card.notification { border-left-color: #58a6ff; }
.sf-idx { color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; width: 1.4em; }
.sf-badge {
  font-size: 10px; line-height: 16px; padding: 0 5px; border-radius: 3px;
  border: 1px solid var(--el-border-color); color: var(--el-text-color-secondary);
}
.sf-badge.response { color: var(--el-color-success); border-color: var(--el-color-success-light-5); }
.sf-badge.error { color: var(--el-color-error); border-color: var(--el-color-error-light-5); }
.sf-badge.notification { color: #58a6ff; border-color: #58a6ff66; }
.sf-mono { font-family: var(--font-mono, monospace); max-width: 180px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sf-err { color: var(--el-color-error); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.sf-spacer { flex: 1; }
.sf-partial { color: var(--el-color-warning); font-size: 10px; }
.sf-size { color: var(--el-text-color-secondary); }
.sf-arrow { display: inline-block; color: var(--el-text-color-secondary);
  transition: transform 0.12s ease; }
.sf-arrow.open { transform: rotate(90deg); }
.sf-body {
  flex-basis: 100%; margin: 4px 0 2px; padding: 6px 8px;
  background: var(--el-fill-color-darker, #1b1b1b); border-radius: 4px; cursor: default;
}
.sf-body pre { margin: 0; font-size: 11px; white-space: pre-wrap; word-break: break-all; }
.mono { font-family: var(--font-mono, ui-monospace, monospace); }
</style>
