<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { platformApi } from '@/api/platform';
import SkillExecutionNotice from '@/components/SkillExecutionNotice.vue';
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
    <div v-if="active" class="dependency-card" aria-label="Skill 依赖与就绪状态">
      <div class="dependency-head"><strong>工具与资源</strong><span :class="{ ready: plan?.ready }">{{ pending ? '正在检查…' : plan?.ready ? '配置已就绪' : '待完成配置' }}</span></div>
      <p class="hint">由所选 Skill 的声明自动关联；保存后固定版本，新配置对新的对话生效。</p>
      <p v-if="plan?.execution?.label" class="hint">执行方式：{{ plan.execution.label }}。{{ plan.execution.resource_limits }}</p>
      <p v-if="error" class="error" role="alert">{{ error }} <button type="button" @click="resolve">重新检查</button></p>
      <SkillExecutionNotice :execution="plan?.execution" :issues="plan?.issues" />
      <div v-if="plan?.tools.length" class="tool-chips"><span v-for="tool in plan.tools" :key="tool.id" :title="tool.description">{{ tool.provider_id }}<small>{{ tool.package_name }}</small></span></div>
      <div v-if="bundledResourceCount" class="resource"><strong>Skill内置资源</strong><span class="scope">已自动关联 {{ bundledResourceCount }} 项随包数据或规则，无需单独导入。</span></div>
      <div v-for="resource in plan?.resources ?? []" :key="resource.packageId + resource.slotId" class="resource">
        <strong>{{ resource.description }}</strong>
        <span class="scope">{{ resource.kind === 'files' ? '仅限当前对话上传的文件' : '当前对话的私有分析产物' }}</span>
      </div>
    </div>

  </div>
</template>

<style scoped>
.skill-config{margin-top:20px;font-size:13px}.dependency-card{padding:18px;border:1px solid var(--el-border-color);border-radius:10px;background:var(--el-fill-color-lighter)}.dependency-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.dependency-head>span{font-size:12px;color:var(--el-color-warning)}.dependency-head>span.ready{color:var(--el-color-success)}.hint{color:var(--el-text-color-secondary);line-height:1.7;margin:10px 0}.issues{margin:12px 0;padding:12px 12px 12px 28px;background:var(--el-color-warning-light-9);border-radius:6px;line-height:1.8}.tool-chips{display:flex;flex-wrap:wrap;gap:8px;margin:14px 0}.tool-chips>span{background:var(--el-bg-color);border:1px solid var(--el-border-color-lighter);padding:7px 10px;border-radius:6px}.tool-chips small{display:block;font-size:10px;color:var(--el-text-color-secondary);margin-top:3px}.resource{margin-top:14px;display:flex;gap:8px;align-items:center;flex-wrap:wrap}.resource>label{width:100%;font-weight:550}.scope{color:var(--el-text-color-secondary)}select{min-width:210px;max-width:100%;padding:8px;border:1px solid var(--el-border-color);border-radius:5px;color:inherit;background:var(--el-bg-color)}button{font:inherit;color:var(--el-color-primary);border:0;background:transparent;cursor:pointer;padding:5px}.error{color:var(--el-color-danger);line-height:1.7}
</style>
