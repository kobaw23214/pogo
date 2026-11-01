<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { Check, CopyDocument, Folder } from "@element-plus/icons-vue";
import type { ShortcutMutationReceipt, ShortcutMergePreview } from "@/agent/shortcutReceipt";
const props = defineProps<{ details: ShortcutMutationReceipt | ShortcutMergePreview }>();
const { t } = useI18n();
const preview = computed(() => props.details.saved === false);
const title = computed(() => t(`shortcutTools.${preview.value ? "mergePreview" : { folder_create: "folderCreate", update: "update", folder_rename: "folderRename", move: "move", delete: "delete", merge: "merge" }[props.details.action]}`));
const summary = computed(() => {
  const d = props.details;
  if (!d.saved) return t(d.plan.removals ? "shortcutTools.previewCount" : "shortcutTools.noDuplicates", { groups: d.plan.groups.length, count: d.plan.removals });
  if (d.folder) return d.folder.name;
  if (d.reused && !d.changes.length) return t("shortcutTools.acknowledged");
  return t(`shortcutTools.${d.action === "move" ? "moved" : d.action === "delete" ? "deleted" : d.action === "merge" ? "merged" : "affected"}`, { count: d.changes.length });
});
const location = (id?: string) => id || t("shortcutTools.root");
</script>

<template>
  <section class="shortcut-operation" :class="{ preview }" data-testid="shortcut-management-result" :data-action="details.action" :data-saved="details.saved">
    <header class="operation-header">
      <span class="operation-icon"><CopyDocument v-if="preview" /><Folder v-else-if="details.folder" /><Check v-else /></span>
      <div class="operation-main"><strong>{{ title }}</strong><span class="operation-summary">{{ summary }}</span></div>
      <span class="scope">{{ t('shortcutTools.private') }}</span>
    </header>
    <p class="operation-status" :class="{ notice: preview }">{{ t(preview ? 'shortcutTools.previewOnly' : details.reused ? 'shortcutTools.reused' : details.changed ? 'shortcutTools.applied' : 'shortcutTools.unchanged') }}</p>
    <details v-if="preview && details.plan?.groups.length" class="operation-disclosure" data-testid="shortcut-merge-plan">
      <summary>{{ t('shortcutTools.inspectPlan') }}</summary>
      <ul class="plan-groups">
        <li v-for="group in details.plan!.groups" :key="group.keep_id" class="plan-group">
          <strong class="item-title">{{ group.title }}</strong>
          <div class="plan-row keep"><span>{{ t('shortcutTools.keep') }}</span><code>{{ group.keep_id }}</code></div>
          <div v-for="id in group.duplicate_ids" :key="id" class="plan-row remove"><span>{{ t('shortcutTools.remove') }}</span><code>{{ id }}</code></div>
          <small>{{ t('shortcutTools.destination') }}: <code>{{ location(group.parent_id) }}</code></small>
        </li>
      </ul>
    </details>
    <details v-else-if="details.saved && details.changes.length" class="operation-disclosure" data-testid="shortcut-change-list">
      <summary>{{ t('shortcutTools.changeCount', { count: details.changes.length }) }}</summary>
      <ul class="change-list">
        <li v-for="change in details.changes" :key="change.id">
          <div class="change-title"><span class="kind">{{ t(`shortcutTools.${change.kind}`) }}</span><strong class="item-title">{{ change.title }}</strong></div>
          <code class="item-id">{{ change.id }}</code>
          <div v-if="details.action === 'move'" class="location"><span>{{ t('shortcutTools.source') }}: <code>{{ location(change.from_parent_id) }}</code></span><span aria-hidden="true">→</span><span>{{ t('shortcutTools.destination') }}: <code>{{ location(change.to_parent_id) }}</code></span></div>
        </li>
      </ul>
    </details>
    <footer><span>{{ t('shortcutTools.libraryRevision', { revision: details.revision }) }}</span><span v-if="details.saved && details.reused && details.applied_revision !== details.revision">{{ t('shortcutTools.originalRevision', { revision: details.applied_revision }) }}</span></footer>
    <p v-if="preview" class="preview-hint">{{ t('shortcutTools.previewHint') }}</p>
  </section>
</template>

<style scoped>
.shortcut-operation{max-width:620px;padding:14px 16px;border:1px solid var(--el-border-color);border-radius:12px;background:var(--el-bg-color);color:var(--el-text-color-primary);font-size:13px}.operation-header{display:flex;align-items:center;gap:11px}.operation-icon{display:grid;place-items:center;flex:none;width:30px;height:30px;border-radius:9px;color:var(--el-color-success);background:var(--el-color-success-light-9)}.operation-icon svg{width:17px}.preview .operation-icon{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.operation-main{display:flex;flex-direction:column;flex:1;gap:4px;min-width:0}.operation-main>strong{font-size:14px}.operation-summary,.item-title{overflow-wrap:anywhere;white-space:pre-wrap}.scope{color:var(--el-text-color-secondary);font-size:11px;flex:none}.operation-status{margin:10px 0 0 41px;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.operation-status.notice{color:var(--el-color-primary)}.operation-disclosure{margin-top:12px;border-top:1px solid var(--el-border-color-lighter);padding-top:10px}.operation-disclosure summary{cursor:pointer;color:var(--el-text-color-regular);font-size:12px;line-height:1.8}.operation-disclosure summary:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:3px;border-radius:3px}.plan-groups,.change-list{list-style:none;padding:0;margin:10px 0 0;max-height:320px;overflow-y:auto;overscroll-behavior:contain}.plan-group,.change-list>li{padding:10px;background:var(--el-fill-color-lighter);border-radius:7px;margin-top:7px}.plan-row{display:flex;gap:12px;align-items:baseline;margin-top:7px;font-size:12px}.plan-row>span{flex:none;color:var(--el-text-color-secondary)}.keep>span{color:var(--el-color-success)}code{font-family:var(--font-mono,monospace);font-size:11px;overflow-wrap:anywhere;white-space:normal}.plan-group small{display:block;color:var(--el-text-color-secondary);margin-top:8px}.change-title{display:flex;align-items:baseline;gap:7px}.kind{color:var(--el-text-color-secondary);font-size:11px;flex:none}.item-id{display:block;margin-top:5px;color:var(--el-text-color-secondary)}.location{display:flex;gap:7px;flex-wrap:wrap;margin-top:7px;color:var(--el-text-color-secondary);font-size:11px}footer{display:flex;gap:10px;flex-wrap:wrap;margin-top:11px;color:var(--el-text-color-placeholder);font-size:11px}.preview-hint{margin:8px 0 0;color:var(--el-text-color-secondary);font-size:11px;line-height:1.7}@media(max-width:480px){.shortcut-operation{padding:12px}.operation-header{flex-wrap:wrap}.scope{margin-left:41px}.operation-status{margin-left:0}.plan-row{gap:8px}}
</style>
