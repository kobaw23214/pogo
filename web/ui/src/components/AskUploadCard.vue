<script setup lang="ts">
// The sent bubble's waiting state while its attachments upload: one
// aggregate bar (a folder of 200 gets one calm bar, not 200 twitchy ones)
// with the file counts and byte sizes as the label. The fill is the MAX of
// two progress measures: bytes count honestly only when the network is the
// bottleneck — against a local proxy the body is buffered near-instantly and
// xhr.upload.onprogress never fires mid-file, so byte progress sits at 0%
// until each file lands. File completions always tick, whatever the link.
// The bar never walks backwards; phase "sending" holds it full while the
// message POST finishes. Bar idiom mirrors TodoBar's track/fill.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { UploadCardState } from "@/agent/uploadCard";

const props = defineProps<{ state: UploadCardState }>();
const { t } = useI18n();

const pct = computed(() => {
  const byBytes = props.state.total > 0 ? props.state.loaded / props.state.total : 1;
  const byFiles = props.state.count > 0 ? props.state.done / props.state.count : 1;
  return Math.min(100, Math.round(Math.max(byBytes, byFiles) * 100));
});

// Same B/KB/MB shape FilePreview's fmtSize uses.
function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
const bytes = computed(() => `${fmtSize(props.state.loaded)} / ${fmtSize(props.state.total)}`);
</script>

<template>
  <div class="ask-upload">
    <div class="ask-upload-head">
      <span class="ask-upload-label">
        {{
          state.phase === "sending"
            ? t("agentView.attach.uploadSending")
            : t("agentView.attach.uploading", { done: state.done, count: state.count })
        }}
      </span>
      <span class="ask-upload-bytes">{{ bytes }}</span>
      <span class="ask-upload-pct">{{ pct }}%</span>
    </div>
    <span
      class="ask-upload-track"
      role="progressbar"
      :aria-label="t('agentView.attach.uploadProgress')"
      :aria-valuenow="pct"
      aria-valuemin="0"
      aria-valuemax="100"
    >
      <span class="ask-upload-fill" :style="{ width: pct + '%' }" />
    </span>
  </div>
</template>

<style scoped lang="scss">
.ask-upload {
  width: 100%;
  min-width: 240px;
  margin-bottom: 6px;
  padding: 8px 10px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
}

.ask-upload-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.ask-upload-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* tabular-nums so the ticking numbers don't jog the row sideways. */
.ask-upload-bytes {
  margin-left: auto;
  flex: none;
  font-variant-numeric: tabular-nums;
}

.ask-upload-pct {
  flex: none;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-regular);
}

.ask-upload-track {
  display: block;
  height: 4px;
  border-radius: 999px;
  background: var(--el-fill-color);
  overflow: hidden;
}

.ask-upload-fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--el-color-primary);
  /* Fast enough to follow the network, slow enough to read as motion. */
  transition: width 0.25s ease;
}
</style>
