<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { MAX_WEBSITE_SECONDS } from "@/api/webServices";
const props = defineProps<{ modelValue: number; disabled?: boolean; label?: string }>();
const emit = defineEmits<{ "update:modelValue": [number] }>();
const { t } = useI18n();
const unit = ref(props.modelValue % 86400 === 0 ? 86400 : props.modelValue % 3600 === 0 ? 3600 : props.modelValue % 60 === 0 ? 60 : 1);
const amount = computed(() => props.modelValue / unit.value);
function update(value: number | undefined) {
  const seconds = Math.round(Number(value) * unit.value);
  emit("update:modelValue", Number.isSafeInteger(seconds) && seconds > 0 && seconds <= MAX_WEBSITE_SECONDS ? seconds : 0);
}
</script>
<template>
  <div class="website-duration">
    <el-input-number :model-value="amount" :min="1 / unit" :max="MAX_WEBSITE_SECONDS / unit" :disabled="disabled" :aria-label="label || t('websiteSettings.duration')" controls-position="right" @update:model-value="update" />
    <el-select v-model="unit" :disabled="disabled" :aria-label="t('websiteSettings.durationUnit')">
      <el-option v-for="option in [{ value: 1, key: 'seconds' }, { value: 60, key: 'minutes' }, { value: 3600, key: 'hours' }, { value: 86400, key: 'days' }]" :key="option.value" :value="option.value" :label="t(`websiteSettings.${option.key}`)" />
    </el-select>
  </div>
</template>
<style scoped>
.website-duration{display:flex;gap:8px;max-width:100%;align-items:center}.website-duration :deep(.el-input-number){width:180px;min-width:0}.website-duration :deep(.el-select){width:100px;flex-shrink:0}
</style>
