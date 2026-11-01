<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { InformationEntry } from "@/api/information";
import InformationLibrary from "./studio/InformationLibrary.vue";
withDefaults(defineProps<{ modelValue: boolean; disabled?: boolean; filesOnly?: boolean; error?: string }>(), { disabled: false, filesOnly: false, error: "" });
const emit = defineEmits<{ "update:modelValue": [value: boolean]; select: [entry: InformationEntry] }>();
const { t } = useI18n();
</script>
<template>
  <el-dialog :model-value="modelValue" :title="t('information.pickerTitle')" width="min(1220px, calc(100vw - 28px))" class="information-picker-dialog" append-to-body destroy-on-close :close-on-click-modal="!disabled" :close-on-press-escape="!disabled" :show-close="!disabled" @update:model-value="!disabled && emit('update:modelValue', $event)">
    <p class="information-picker-hint">{{ t(filesOnly ? 'information.filePickerHint' : 'information.pickerHint') }}</p><p v-if="error" class="information-picker-error" role="alert">{{ error }}</p><InformationLibrary picker :files-only="filesOnly" :active="modelValue" :disabled="disabled" @select="emit('select', $event)" />
    <template #footer><el-button :disabled="disabled" @click="emit('update:modelValue', false)">{{ t('information.cancel') }}</el-button></template>
  </el-dialog>
</template>
<style scoped>.information-picker-hint{margin:0 0 15px;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.information-picker-error{margin:0 0 12px;font-size:12px;line-height:1.7;overflow-wrap:anywhere;color:var(--el-color-danger)}</style>
<style>.information-picker-dialog{margin-top:5dvh;max-height:90dvh}.information-picker-dialog .el-dialog__body{padding-top:4px}.information-picker-dialog .el-dialog__footer{padding-top:12px}</style>
