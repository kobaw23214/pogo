<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import { MAX_WEBSITE_SECONDS, websiteError, type WebsitePolicy, type WebsiteRestart, type WebsiteSettingsState } from "@/api/webServices";
import WebsiteDurationInput from "./WebsiteDurationInput.vue";
import StudioEntrySkeleton from "./studio/StudioEntrySkeleton.vue";
const props = defineProps<{ active: boolean }>();
const emit = defineEmits<{ dirty: [boolean] }>();
const { t } = useI18n();
const state = ref<WebsiteSettingsState>(), form = ref<WebsitePolicy>();
const snapshot = ref(""), loading = ref(false), saving = ref(false), loadError = ref(""), saveError = ref(""), notice = ref("");
const error = computed(() => saveError.value || loadError.value);
let timer: ReturnType<typeof setTimeout> | undefined, serial = 0, disposed = false;
const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== snapshot.value);
watch(dirty, value => emit("dirty", value));
const restarts = computed(() => state.value?.restarting || []);
const pending = (row: WebsiteRestart) => ["accepted", "running"].includes(row.state);
const runtimeChanged = computed(() => {
  if (!form.value || !snapshot.value) return false;
  const previous = JSON.parse(snapshot.value) as WebsitePolicy;
  return (["internal_port", "cpus", "memory_mib"] as const).some(key => previous[key] !== form.value![key]);
});
const valid = computed(() => {
  const p = form.value;
  return p && Number.isSafeInteger(p.default_lease_seconds) && p.default_lease_seconds > 0 && p.default_lease_seconds <= MAX_WEBSITE_SECONDS &&
    [p.max_per_session, p.max_per_user, p.max_total].every(v => Number.isInteger(v) && v >= 1 && v <= 10000) &&
    Number.isInteger(p.internal_port) && p.internal_port >= 1024 && p.internal_port <= 65535 &&
    Number.isFinite(p.cpus) && p.cpus >= .1 && p.cpus <= 64 && Number.isInteger(p.memory_mib) && p.memory_mib >= 64 && p.memory_mib <= 65536;
});
function schedule() {
  clearTimeout(timer);
  if (!disposed && !saving.value && props.active && restarts.value.some(pending)) timer = setTimeout(() => { void load(); }, 1500);
}
async function load() {
  const ticket = ++serial;
  loading.value = true;
  loadError.value = "";
  try {
    const result = await api.websiteSettings();
    if (disposed || ticket !== serial) return;
    state.value = result.state;
    if (!form.value || !dirty.value) { form.value = { ...result.state.settings }; snapshot.value = JSON.stringify(form.value); }
    loadError.value = "";
  } catch (cause) {
    if (!disposed && ticket === serial) loadError.value = (cause as { status?: number }).status === 403 ? t("websiteSettings.adminOnly") : websiteError(cause);
  } finally { if (!disposed && ticket === serial) { loading.value = false; schedule(); } }
}
async function save() {
  if (!form.value || !valid.value || saving.value) return;
  serial++; clearTimeout(timer); loading.value = false;
  saving.value = true; saveError.value = ""; notice.value = "";
  // Freeze the submitted draft: edits cannot change an in-flight request.
  const submitted = { ...form.value };
  try {
    const result = await api.saveWebsiteSettings(submitted);
    if (disposed) return;
    form.value = { ...result.settings }; snapshot.value = JSON.stringify(form.value);
    if (state.value) state.value = { ...state.value, settings: result.settings, restarting: result.restarted };
    notice.value = result.restarted.some(pending) ? t("websiteSettings.savedRestarting", { count: result.restarted.filter(pending).length }) : t("websiteSettings.saved");
    await load();
  } catch (cause) { if (!disposed) saveError.value = websiteError(cause); }
  finally { if (!disposed) { saving.value = false; schedule(); } }
}
watch(() => props.active, active => { if (active && !saving.value) void load(); else clearTimeout(timer); }, { immediate: true });
function discard() { if (state.value) { form.value = { ...state.value.settings }; snapshot.value = JSON.stringify(form.value); } }
defineExpose({ discard });
onScopeDispose(() => { disposed = true; serial++; clearTimeout(timer); });
</script>
<template>
  <section class="website-settings" data-testid="website-settings">
    <header><h2>{{ t('websiteSettings.title') }}</h2><p>{{ t('websiteSettings.description') }}</p></header>
    <p v-if="error" class="website-settings-error" role="alert">{{ error }}</p>
    <div v-if="loading && !form" role="status" :aria-label="t('webServices.loading')" aria-busy="true"><StudioEntrySkeleton area="form" style="padding: 0" /></div>
    <el-button v-else-if="loadError && !form" @click="load">{{ t('common.retry') }}</el-button>
    <template v-if="form">
      <p class="website-settings-meta">{{ t('websiteSettings.profiles') }}：{{ state?.profiles.map(p => p === 'node-web' ? 'Node.js' : 'Python').join(' / ') }}</p>
      <fieldset :disabled="saving" class="website-settings-card">
        <legend>{{ t('websiteSettings.immediateTitle') }}</legend><p class="website-settings-hint">{{ t('websiteSettings.immediateHint') }}</p>
        <label>{{ t('websiteSettings.defaultLifetime') }}<WebsiteDurationInput v-model="form.default_lease_seconds" :disabled="saving" /></label>
        <label>{{ t('websiteSettings.maxSession') }}<el-input-number v-model="form.max_per_session" :min="1" :max="10000" :step="1" :precision="0" :disabled="saving" :aria-label="t('websiteSettings.maxSession')" /></label>
        <label>{{ t('websiteSettings.maxUser') }}<el-input-number v-model="form.max_per_user" :min="1" :max="10000" :step="1" :precision="0" :disabled="saving" :aria-label="t('websiteSettings.maxUser')" /></label>
        <label>{{ t('websiteSettings.maxTotal') }}<el-input-number v-model="form.max_total" :min="1" :max="10000" :step="1" :precision="0" :disabled="saving" :aria-label="t('websiteSettings.maxTotal')" /></label>
      </fieldset>
      <fieldset :disabled="saving" class="website-settings-card">
        <legend>{{ t('websiteSettings.restartTitle') }}</legend><p class="website-settings-hint">{{ t('websiteSettings.restartHint') }}</p>
        <label>{{ t('websiteSettings.internalPort') }}<el-input-number v-model="form.internal_port" :min="1024" :max="65535" :precision="0" :disabled="saving" :aria-label="t('websiteSettings.internalPort')" /></label>
        <p class="website-settings-hint">{{ t('websiteSettings.internalPortHint') }}</p>
        <label>{{ t('websiteSettings.cpu') }}<el-input-number v-model="form.cpus" :min=".1" :max="64" :step=".1" :precision="1" :disabled="saving" :aria-label="t('websiteSettings.cpu')" /></label>
        <label>{{ t('websiteSettings.memory') }}<el-input-number v-model="form.memory_mib" :min="64" :max="65536" :step="64" :precision="0" :disabled="saving" :aria-label="t('websiteSettings.memory')" /></label>
      </fieldset>
      <p v-if="notice" role="status">{{ notice }}</p>
      <ul v-if="restarts.length" class="website-settings-restarts" aria-live="polite"><li v-for="row in restarts" :key="row.service_id" :class="{ failed: row.state === 'failed' }"><strong>{{ row.name }}</strong> · {{ t(`websiteSettings.restart_${row.state}`) }}<span v-if="row.error"> · {{ row.error.message }}</span></li></ul>
      <footer><el-button type="primary" :disabled="!dirty || !valid || saving" :loading="saving" @click="save">{{ t(runtimeChanged ? 'websiteSettings.saveRestart' : 'websiteSettings.save') }}</el-button><span>{{ t('websiteSettings.usage', { count: state?.usage.total || 0 }) }}</span></footer>
    </template>
  </section>
</template>
<style scoped>
.website-settings{padding:24px;max-width:850px;margin:0 auto;color:var(--el-text-color-primary)}h2{margin:0 0 8px;font-size:20px}.website-settings header p,.website-settings-hint,.website-settings-meta,footer span{font-size:12px;color:var(--el-text-color-secondary);line-height:1.7}.website-settings-card{margin:20px 0;padding:18px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-fill-color-extra-light)}legend{padding:0 6px;font-size:14px;font-weight:600}.website-settings-card label{display:flex;align-items:center;justify-content:space-between;gap:20px;margin:16px 0;font-size:13px}.website-settings-error,.failed{color:var(--el-color-danger);overflow-wrap:anywhere}.website-settings-restarts{padding:12px 12px 12px 28px;border:1px solid var(--el-border-color-light);border-radius:8px;font-size:12px;line-height:1.9}footer{display:flex;gap:14px;align-items:center;margin:20px 0;flex-wrap:wrap}@media(max-width:580px){.website-settings{padding:16px}.website-settings-card label{align-items:flex-start;flex-direction:column;gap:8px}}
</style>
