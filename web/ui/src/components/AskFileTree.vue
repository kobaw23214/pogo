<script setup lang="ts">
// The sent bubble's attachment tree: a folder upload renders as the folder it
// was — one collapsible branch per picked root — instead of a flat row of
// chips. The tree itself is rebuilt from the entries' workspace paths (see
// agent/attachTree); AgentView only mounts this when the paths actually hold
// a directory, so plain file uploads keep their chip row.
import { computed } from "vue";
import { attachTree, type AttachFile } from "@/agent/attachTree";
import AskTreeNode from "@/components/AskTreeNode.vue";

const props = defineProps<{ files: AttachFile[] }>();
const emit = defineEmits<{ open: [file: AttachFile] }>();

const tree = computed(() => attachTree(props.files));
const nodeKey = (n: (typeof tree.value.roots)[number]) => `${n.dir ? "d" : "f"}:${n.path}`;
</script>

<template>
  <div class="ask-tree">
    <AskTreeNode v-for="n in tree.roots" :key="nodeKey(n)" :node="n" @open="(f) => emit('open', f)" />
  </div>
</template>

<style scoped lang="scss">
/* One quiet panel per uploaded tree — the chip row's tile, grown into a
   disclosure block. Full bubble width so long nested names have room. */
.ask-tree {
  width: 100%;
  min-width: 240px;
  margin-bottom: 6px;
  padding: 4px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  display: flex;
  flex-direction: column;
}
</style>
