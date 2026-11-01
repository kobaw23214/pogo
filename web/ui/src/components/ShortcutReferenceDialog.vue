<script setup lang="ts">
import type { ShortcutSnapshot } from "@/api/shortcuts";
import ShortcutReference from "./ShortcutReference.vue";
import { useI18n } from "vue-i18n";
defineProps<{ editable?: boolean }>();
const emit = defineEmits<{ edit: [] }>();
const { t } = useI18n();
const reference = defineModel<ShortcutSnapshot | null>({ required: true });
function visibility(visible: boolean) { if (!visible) reference.value = null; }
</script>

<template>
  <el-dialog :model-value="!!reference" :title="reference?.title" width="min(820px, calc(100vw - 24px))" class="shortcut-reference-dialog" append-to-body data-testid="shortcut-reference-dialog" @update:model-value="visibility">
    <ShortcutReference v-if="reference" :title="reference.title" :content="reference.content" :icon="reference.icon" :language="reference.language" :parts="reference.parts" details-only />
    <template v-if="editable" #footer><div class="shortcut-local-edit"><span>{{ t('nestedShortcuts.editHereHint') }}</span><button type="button" data-testid="shortcut-edit-here" @click="emit('edit')">{{ t('nestedShortcuts.editHere') }}</button></div></template>
  </el-dialog>
</template>

<style>
.shortcut-reference-dialog.el-dialog { --shortcut-details-max-height:calc(90dvh - 96px); display:flex; flex-direction:column; max-height:90dvh; margin-top:5dvh; }
.shortcut-reference-dialog > .el-dialog__header { flex:none; min-width:0; }
.shortcut-reference-dialog .el-dialog__title { display:block; padding-right:22px; white-space:normal; overflow-wrap:anywhere; }
.shortcut-reference-dialog > .el-dialog__body { display:flex; flex:1 1 auto; flex-direction:column; min-width:0; min-height:0; overflow:hidden; }
.shortcut-reference-dialog > .el-dialog__body > .shortcut-details { flex:1 1 auto; }
.shortcut-reference-dialog > .el-dialog__body > .shortcut-details > .shortcut-details-heading { display:none; }
.shortcut-local-edit{display:flex;align-items:center;justify-content:space-between;gap:14px;padding-top:4px;font-size:12px;color:var(--el-text-color-secondary)}.shortcut-local-edit button{flex:none;border:1px solid var(--el-color-primary);border-radius:7px;background:var(--el-color-primary-light-9);color:var(--el-color-primary);padding:7px 12px;font:inherit;cursor:pointer}.shortcut-local-edit button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}
</style>
