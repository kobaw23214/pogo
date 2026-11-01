<script setup lang="ts">
// One node of a sent message's attachment tree. A directory row is a
// disclosure button (caret + folder + name + descendant count, toggling on
// any click); a file row keeps the flat chip's behaviour — click opens the
// file as a preview tab. Nodes hold their own open state, keyed by path in
// the parent's v-for, so expanding one folder never disturbs another.
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { CaretRight } from "@element-plus/icons-vue";
import { attachFolderIcon, fileIcon } from "@/components/fileIcons";
import type { AttachFile, AttachNode } from "@/agent/attachTree";

const props = defineProps<{ node: AttachNode }>();
const emit = defineEmits<{ open: [file: AttachFile] }>();
const { t } = useI18n();

const open = ref(false);
const nodeKey = (n: AttachNode) => `${n.dir ? "d" : "f"}:${n.path}`;
</script>

<template>
  <button
    v-if="props.node.dir"
    class="ask-tree-row dir"
    :class="{ open }"
    :aria-expanded="open"
    :title="`${props.node.name} · ${t('agentView.attach.folderCount', { n: props.node.count })}`"
    @click="open = !open"
  >
    <el-icon class="ask-tree-caret"><CaretRight /></el-icon>
    <Icon class="ask-tree-icon" :icon="attachFolderIcon" />
    <span class="ask-tree-name">{{ props.node.name }}</span>
    <span class="ask-tree-count">{{ props.node.count }}</span>
  </button>
  <button
    v-else
    class="ask-tree-row file"
    :title="t('agentView.attach.open')"
    @click="emit('open', props.node.file!)"
  >
    <!-- The empty caret slot keeps leaf labels aligned with branch labels. -->
    <span class="ask-tree-caret" />
    <Icon class="ask-tree-icon" :icon="fileIcon(props.node.name)" />
    <span class="ask-tree-name">{{ props.node.name }}</span>
  </button>
  <div v-if="props.node.dir && open" class="ask-tree-kids">
    <AskTreeNode v-for="c in props.node.children" :key="nodeKey(c)" :node="c" @open="(f) => emit('open', f)" />
  </div>
</template>

<style scoped lang="scss">
.ask-tree-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 3px 6px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
  cursor: pointer;
  text-align: left;

  &:hover {
    background: var(--el-fill-color);
  }
}

.ask-tree-caret {
  width: 12px;
  flex: none;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  transition: transform 0.15s ease;
}

.ask-tree-row.dir.open .ask-tree-caret {
  transform: rotate(90deg);
}

.ask-tree-icon {
  flex: none;
  font-size: 18px;
}

.ask-tree-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The count pill doubles as the collapsed folder's whole summary. */
.ask-tree-count {
  margin-left: auto;
  flex: none;
  padding: 0 7px;
  border-radius: 999px;
  background: var(--el-fill-color);
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

/* Indent guide: the guide line runs through the caret's centre of the level
   above, VS Code explorer style. */
.ask-tree-kids {
  margin-left: 11px;
  padding-left: 7px;
  border-left: 1px solid var(--el-border-color-extra-light);
}
</style>
