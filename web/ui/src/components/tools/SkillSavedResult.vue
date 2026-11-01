<script setup lang="ts">
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Check, ArrowRight } from "@element-plus/icons-vue";
import type { SkillAuthoringDetails } from "@/api/types";
import { notifyLiveAuthoredSkillSaved } from "@/agent/skillAuthoringReceipt";
import AuthoredSkillDialog from "../AuthoredSkillDialog.vue";
const props = defineProps<{ details: SkillAuthoringDetails; live?: boolean }>();
const open = ref(false);
const { t } = useI18n();
watch(() => [props.details, props.live] as const, ([details, live]) => { if (live) notifyLiveAuthoredSkillSaved(details); }, { immediate: true });
</script>

<template>
  <button type="button" class="saved-skill" data-testid="skill-saved-result" @click="open = true">
    <span class="saved-check"><Check /></span>
    <span class="saved-body"><strong>{{ details.skill.name }} <span class="version">v{{ details.skill.version }}</span></strong><span class="saved-state">{{ t(details.reused ? 'skillAuthoring.reused' : 'skillAuthoring.saved') }} · {{ t('skillAuthoring.private') }}</span><small>{{ t(details.pending ? 'skillAuthoring.pending' : details.enabled ? 'skillAuthoring.enabled' : 'skillAuthoring.draft') }}</small></span>
    <span class="saved-open">{{ t('skillAuthoring.open') }} <ArrowRight /></span>
  </button>
  <p class="verification">{{ t('skillAuthoring.structural') }} · {{ t('skillAuthoring.behavior') }}</p>
  <AuthoredSkillDialog v-if="open" v-model="open" :agent-id="details.agent_id" :skill="details.skill" />
</template>

<style scoped>
.saved-skill{width:100%;max-width:620px;display:flex;align-items:center;gap:12px;padding:14px 16px;border:1px solid var(--el-border-color);border-radius:12px;text-align:left;color:var(--el-text-color-primary);background:var(--el-bg-color);font:inherit;cursor:pointer}.saved-skill:hover{border-color:var(--el-color-primary);background:var(--el-fill-color-light)}.saved-skill:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.saved-check{display:grid;place-items:center;width:32px;height:32px;flex:none;border-radius:9px;background:var(--el-color-success-light-9);color:var(--el-color-success)}.saved-check svg{width:18px}.saved-body{display:flex;min-width:0;flex:1;flex-direction:column;gap:4px}.saved-body strong{font-size:14px;overflow-wrap:anywhere}.version{font-size:11px;font-weight:400;color:var(--el-text-color-secondary);margin-left:4px}.saved-state{font-size:12px;color:var(--el-text-color-secondary)}.saved-body small{font-size:11px;line-height:1.6;color:var(--el-text-color-secondary)}.saved-open{display:flex;align-items:center;gap:4px;flex:none;font-size:12px;color:var(--el-color-primary)}.saved-open svg{width:13px}.verification{margin:6px 0 0 44px;font-size:11px;line-height:1.6;color:var(--el-text-color-placeholder)}@media(max-width:480px){.saved-open{font-size:0}.saved-skill{padding:11px}.verification{margin-left:0}}
</style>
