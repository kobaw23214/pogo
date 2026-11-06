<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { platformApi } from '@/api/platform';
import SkillExecutionNotice from '@/components/SkillExecutionNotice.vue';
import { knownHostReason } from '@/agent/hostStatusCopy';
import type { PlatformCapabilities, SkillSelection, SkillSelectionPlan } from '@/agent/platformTypes';

const props = defineProps<{ selection: SkillSelection; catalog: PlatformCapabilities }>();
const emit = defineEmits<{
  validation: [state: { pending: boolean; error: string; plan: SkillSelectionPlan | null }];
}>();
const plan = ref<SkillSelectionPlan | null>(null);
const pending = ref(false);
const error = ref('');
let generation = 0;
let disposed = false;
const active = computed(() => props.selection.skill_ids.length > 0);
const bundledResourceCount = computed(() => new Set(plan.value?.tools.flatMap(tool => (tool.asset_ids ?? []).map(id => `${tool.package.packageDigest}/${id}`)) ?? []).size);
const snapshot = computed(() => JSON.stringify(props.selection));

async function resolve() {
  const request = ++generation;
  plan.value = null; error.value = ''; pending.value = active.value;
  emit('validation', { pending: pending.value, error: '', plan: null });
  if (!active.value) return;
  try {
    const next = await platformApi.skillSelection(props.selection);
    if (disposed || request !== generation) return;
    plan.value = next;
  } catch (e) {
    if (disposed || request !== generation) return;
    error.value = (e as Error).message;
  } finally {
    if (!disposed && request === generation) {
      pending.value = false;
      emit('validation', { pending: false, error: error.value, plan: plan.value });
    }
  }
}
watch([snapshot, () => props.catalog], () => void resolve(), { immediate: true });

onBeforeUnmount(() => { disposed = true; generation++; });
</script>

<template>
  <div class="skill-config">
    <div v-if="active" class="dependency-card" :aria-label="tr('studioManagement.text254')">
      <div class="dependency-head"><strong>{{ tr('studioManagement.text255') }}</strong><span :class="{ ready: plan?.ready }">{{ pending ? tr('studioManagement.text256') : plan?.ready ? tr('studioManagement.text257') : tr('studioManagement.text258') }}</span></div>
      <p class="hint">{{ tr('studioManagement.text259') }}</p>
      <p v-if="plan?.execution?.label" class="hint">{{ tr('studioSentences.executionMode', { label: knownHostReason(plan.execution.label), limits: knownHostReason(plan.execution.resource_limits) }) }}</p>
      <p v-if="error" class="error" role="alert">{{ error }} <button type="button" @click="resolve">{{ tr('studioManagement.text261') }}</button></p>
      <SkillExecutionNotice :execution="plan?.execution" :issues="plan?.issues" />
      <div v-if="plan?.tools.length" class="tool-chips"><span v-for="tool in plan.tools" :key="tool.id" :title="tool.description">{{ tool.provider_id }}<small>{{ tool.package_name }}</small></span></div>
      <div v-if="bundledResourceCount" class="resource"><strong>{{ tr('studioManagement.text262') }}</strong><span class="scope">{{ tr('studioSentences.bundledResources', { count: bundledResourceCount }) }}</span></div>
      <div v-for="resource in plan?.resources ?? []" :key="resource.packageId + resource.slotId" class="resource">
        <strong>{{ resource.description }}</strong>
        <span class="scope">{{ resource.kind === 'files' ? tr('studioManagement.text265') : tr('studioManagement.text266') }}</span>
      </div>
    </div>

  </div>
</template>

<style scoped>
.skill-config{margin-top:20px;font-size:13px}.dependency-card{padding:18px;border:1px solid var(--el-border-color);border-radius:10px;background:var(--el-fill-color-lighter)}.dependency-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.dependency-head>span{font-size:12px;color:var(--el-color-warning)}.dependency-head>span.ready{color:var(--el-color-success)}.hint{color:var(--el-text-color-secondary);line-height:1.7;margin:10px 0}.issues{margin:12px 0;padding:12px 12px 12px 28px;background:var(--el-color-warning-light-9);border-radius:6px;line-height:1.8}.tool-chips{display:flex;flex-wrap:wrap;gap:8px;margin:14px 0}.tool-chips>span{background:var(--el-bg-color);border:1px solid var(--el-border-color-lighter);padding:7px 10px;border-radius:6px}.tool-chips small{display:block;font-size:10px;color:var(--el-text-color-secondary);margin-top:3px}.resource{margin-top:14px;display:flex;gap:8px;align-items:center;flex-wrap:wrap}.resource>label{width:100%;font-weight:550}.scope{color:var(--el-text-color-secondary)}select{min-width:210px;max-width:100%;padding:8px;border:1px solid var(--el-border-color);border-radius:5px;color:inherit;background:var(--el-bg-color)}button{font:inherit;color:var(--el-color-primary);border:0;background:transparent;cursor:pointer;padding:5px}.error{color:var(--el-color-danger);line-height:1.7}
</style>
