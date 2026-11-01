<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { platformApi } from "@/api/platform";
const props = defineProps<{ workflowId: string; revision: number; inputKeys: string[] }>();
const emit = defineEmits<{ close: []; started: [id: string] }>();
const { t } = useI18n();
const inputs = reactive<Record<string, string>>(Object.fromEntries(props.inputKeys.map(k => [k, ""])));
const busy = ref(false), error = ref("");
const check = ref<Awaited<ReturnType<typeof platformApi.workflows.validate>> | null>(null);
const ready = computed(() => check.value?.valid === true && check.value.ready_for_test !== false);
const requestId = crypto.randomUUID();
async function validate() {
  busy.value = true; error.value = "";
  try { check.value = await platformApi.workflows.validate(props.workflowId, props.revision, { ...inputs }); }
  catch (e) { error.value = (e as Error).message; check.value = null; }
  finally { busy.value = false; }
}
async function start() {
  if (busy.value) return;
  await validate();
  if (!ready.value || !check.value) return;
  busy.value = true;
  try {
    const result = await platformApi.workflows.test(props.workflowId, props.revision, check.value.definition_hash, { ...inputs }, requestId);
    emit("started", result.id);
  } catch (e) { error.value = (e as Error).message; }
  finally { busy.value = false; }
}
</script>
<template>
  <el-dialog :model-value="true" :title="t('workflowAuthoring.trial')" width="min(640px, 94vw)" :close-on-click-modal="false" :close-on-press-escape="!busy" :show-close="!busy" @close="!busy && emit('close')">
    <p class="trial-hint">{{ t('workflowAuthoring.trialHint') }}</p>
    <p>{{ t('workflowAuthoring.revision', { n: revision }) }}</p>
    <form @submit.prevent="start">
      <fieldset :disabled="busy"><legend>{{ t('workflowAuthoring.inputs') }}</legend>
        <p v-if="!inputKeys.length">{{ t('workflowAuthoring.noInputs') }}</p>
        <label v-for="key in inputKeys" :key="key">{{ key }}<el-input v-model="inputs[key]" :aria-label="key" :maxlength="32000" @input="check = null" /></label>
      </fieldset>
      <p v-if="error" role="alert" class="trial-error">{{ error }}</p>
      <ul v-if="check?.diagnostics.length"><li v-for="(d, i) in check.diagnostics" :key="i">{{ d.message }}<small v-if="d.fix_hint">{{ d.fix_hint }}</small></li></ul>
      <p v-else-if="check" role="status">{{ t(check.valid ? 'workflowAuthoring.valid' : 'workflowAuthoring.invalid') }}</p>
      <div class="trial-actions"><el-button :disabled="busy" @click="emit('close')">{{ t('workflowAuthoring.close') }}</el-button><el-button :loading="busy" :disabled="busy" @click="validate">{{ t('workflowAuthoring.check') }}</el-button><el-button type="primary" native-type="submit" :disabled="busy" :loading="busy">{{ t('workflowAuthoring.start') }}</el-button></div>
    </form>
  </el-dialog>
</template>
<style scoped>
.trial-hint{line-height:1.7;color:var(--el-text-color-secondary)}fieldset{border:1px solid var(--el-border-color);border-radius:8px;padding:12px}label{display:flex;flex-direction:column;gap:5px;margin:10px 0}.trial-actions{display:flex;justify-content:flex-end;margin-top:20px;gap:8px}.trial-error{color:var(--el-color-danger)}li{margin:8px 0;overflow-wrap:anywhere}small{display:block;color:var(--el-text-color-secondary)}
</style>
