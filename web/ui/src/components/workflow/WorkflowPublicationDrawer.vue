<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage, ElMessageBox } from "element-plus";
import { platformApi } from "@/api/platform";
import { workflowPublications, type WorkflowCallbackDelivery, type WorkflowPublication, type WorkflowPublicationInput, type WorkflowPublicationList } from "@/api/workflowPublication";
import type { WorkflowVersion } from "@/agent/workflowTypes";
import { workflowEmbedExample } from "@/agent/workflowEmbedProtocol";

const props = defineProps<{ open: boolean; workflowId: string; name: string; publishedVersion?: string }>();
const emit = defineEmits<{ "update:open": [boolean] }>();
const { t } = useI18n();
const data = ref<WorkflowPublicationList>({ publications: [], subjects: [] });
const versions = ref<WorkflowVersion[]>([]);
const loading = ref(false); const busy = ref(false); const error = ref(""); const secret = ref("");
const callbackSecret = ref("");
const deliveries = ref<WorkflowCallbackDelivery[]>([]);
const deliveriesLoading = ref(false); const deliveriesError = ref("");
const editing = ref(false); const selectedId = ref("");
const form = reactive<WorkflowPublicationInput>({ name: "", version_id: "", enabled: true, history_scope: "business", callback: { enabled: false, url: "" }, allowed_origins: [], mappings: [], input_fields: [] });
const origins = ref("");
let epoch = 0;
let selectionEpoch = 0;
let deliveriesEpoch = 0;
const selected = computed(() => data.value.publications.find(item => item.id === selectedId.value));
const example = computed(() => selected.value ? workflowEmbedExample(location.origin, selected.value.id, selected.value.allowed_origins[0] || "https://business.example.com") : "");
function clearForm(publication?: WorkflowPublication) {
  editing.value = true;
  secret.value = ""; callbackSecret.value = "";
  selectedId.value = publication?.id ?? "";
  Object.assign(form, { name: publication?.name ?? props.name, version_id: publication?.version_id ?? props.publishedVersion ?? "", enabled: publication?.enabled ?? true, history_scope: publication ? publication.history_scope ?? "workflow" : "business", callback: { enabled: publication?.callback?.enabled ?? false, url: publication?.callback?.url ?? "" }, allowed_origins: [...(publication?.allowed_origins ?? [])], mappings: publication?.mappings.map(item => ({ ...item })) ?? [{ subject: "", username: "" }], input_fields: publication?.input_fields.map(item => ({ ...item })) ?? [] });
  origins.value = form.allowed_origins.join("\n");
  error.value = ""; editing.value = true;
}
async function load() {
  const request = ++epoch; loading.value = true; error.value = "";
  try {
    const [entries, published] = await Promise.all([workflowPublications.list(props.workflowId), platformApi.workflows.versions(props.workflowId)]);
    if (request !== epoch) return;
    data.value = entries; versions.value = published.versions;
    if (!selectedId.value) selectedId.value = entries.publications[0]?.id ?? "";
  } catch (failure) { if (request === epoch) error.value = (failure as { status?: number }).status === 403 ? t("workflowEmbed.adminOnly") : (failure as Error).message; }
  finally { if (request === epoch) loading.value = false; }
}
async function save() {
  if (busy.value) return;
  const body: WorkflowPublicationInput = { ...form, callback: { enabled: form.callback?.enabled === true, url: form.callback?.url.trim() || "" }, allowed_origins: origins.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean), mappings: form.mappings.map(item => ({ subject: item.subject.trim(), username: item.username })), input_fields: form.input_fields.map(item => ({ key: item.key.trim(), label: item.label.trim(), required: item.required })) };
  if (!body.name.trim() || !body.version_id || !body.allowed_origins.length || !body.mappings.length || body.mappings.some(item => !item.subject || !item.username)) { error.value = t("workflowEmbed.requiredConfig"); return; }
  if (body.callback?.enabled && !body.callback.url) { error.value = t("workflowEmbed.callbackUrlRequired"); return; }
  busy.value = true; error.value = ""; const request = epoch;
  try {
    if (selected.value) {
      const result = await workflowPublications.update(props.workflowId, selected.value.id, { ...body, revision: selected.value.revision });
      if (request !== epoch) return;
      const { callback_secret, ...publication } = result;
      data.value.publications = data.value.publications.map(item => item.id === publication.id ? publication : item);
      callbackSecret.value = callback_secret || "";
    } else {
      const result = await workflowPublications.create(props.workflowId, body);
      if (request !== epoch) return;
      data.value.publications.unshift(result.publication); selectedId.value = result.publication.id; secret.value = result.client_secret || ""; callbackSecret.value = result.callback_secret || "";
    }
    editing.value = false; void loadDeliveries(); ElMessage.success(t("workflowEmbed.saved"));
  } catch (failure) { if (request === epoch) error.value = (failure as Error).message; }
  finally { if (request === epoch) busy.value = false; }
}
async function rotate() {
  if (!selected.value || busy.value) return;
  const id = selected.value.id; const selection = selectionEpoch;
  try { await ElMessageBox.confirm(t("workflowEmbed.rotateConfirm"), t("workflowEmbed.rotate"), { type: "warning", confirmButtonText: t("workflowEmbed.rotate"), cancelButtonText: t("workflowEmbed.cancelEdit") }); }
  catch { return; }
  if (selection !== selectionEpoch) return;
  busy.value = true; error.value = ""; const request = epoch;
  try {
    const result = await workflowPublications.rotate(props.workflowId, id);
    if (request !== epoch || selection !== selectionEpoch) return;
    data.value.publications = data.value.publications.map(item => item.id === result.publication.id ? result.publication : item);
    secret.value = result.client_secret;
  } catch (failure) { if (request === epoch) error.value = (failure as Error).message; }
  finally { if (request === epoch) busy.value = false; }
}
async function rotateCallback() {
  if (!selected.value || busy.value) return;
  const id = selected.value.id; const selection = selectionEpoch;
  try { await ElMessageBox.confirm(t("workflowEmbed.callbackRotateConfirm"), t("workflowEmbed.callbackRotate"), { type: "warning", confirmButtonText: t("workflowEmbed.callbackRotate"), cancelButtonText: t("workflowEmbed.cancelEdit") }); }
  catch { return; }
  if (selection !== selectionEpoch) return;
  busy.value = true; error.value = ""; const request = epoch;
  try {
    const result = await workflowPublications.rotateCallback(props.workflowId, id);
    if (request !== epoch || selection !== selectionEpoch) return;
    data.value.publications = data.value.publications.map(item => item.id === result.publication.id ? result.publication : item);
    callbackSecret.value = result.callback_secret || "";
    void loadDeliveries();
  } catch (failure) { if (request === epoch && selection === selectionEpoch) error.value = (failure as Error).message; }
  finally { if (request === epoch) busy.value = false; }
}
async function loadDeliveries() {
  const id = selectedId.value;
  if (!props.open || !id) return;
  const request = ++deliveriesEpoch; const panel = epoch;
  deliveriesLoading.value = true; deliveriesError.value = "";
  try {
    const result = await workflowPublications.deliveries(props.workflowId, id);
    if (request === deliveriesEpoch && panel === epoch && id === selectedId.value) deliveries.value = result.deliveries;
  } catch (failure) { if (request === deliveriesEpoch && panel === epoch && id === selectedId.value) deliveriesError.value = (failure as Error).message; }
  finally { if (request === deliveriesEpoch && panel === epoch) deliveriesLoading.value = false; }
}
async function retryDelivery(delivery: WorkflowCallbackDelivery) {
  if (!selected.value || busy.value) return;
  const id = selected.value.id; const selection = selectionEpoch;
  try { await ElMessageBox.confirm(t("workflowEmbed.deliveryRetryConfirm"), t("workflowEmbed.deliveryRetry"), { type: "warning", confirmButtonText: t("workflowEmbed.deliveryRetry"), cancelButtonText: t("workflowEmbed.cancelEdit") }); }
  catch { return; }
  if (selection !== selectionEpoch) return;
  busy.value = true; deliveriesError.value = ""; const request = epoch;
  try { await workflowPublications.retryDelivery(props.workflowId, id, delivery.id); if (request === epoch && selection === selectionEpoch) await loadDeliveries(); }
  catch (failure) { if (request === epoch && selection === selectionEpoch) deliveriesError.value = (failure as Error).message; }
  finally { if (request === epoch) busy.value = false; }
}
function deliveryTime(value?: string | number) {
  if (!value) return "—";
  const time = new Date(value);
  return Number.isFinite(time.getTime()) ? time.toLocaleString() : String(value);
}
function retryable(delivery: WorkflowCallbackDelivery) { return ["failed", "blocked", "paused"].includes(delivery.status); }
async function copy(value: string) {
  try { await navigator.clipboard.writeText(value); ElMessage.success(t("workflowEmbed.copied")); }
  catch { ElMessage.warning(t("workflowEmbed.copyFailed")); }
}
watch([() => props.open, () => props.workflowId], () => {
  ++epoch; ++deliveriesEpoch; secret.value = ""; callbackSecret.value = ""; selectedId.value = ""; editing.value = false; data.value = { publications: [], subjects: [] }; deliveries.value = []; deliveriesLoading.value = false; deliveriesError.value = ""; loading.value = false; busy.value = false; error.value = "";
  if (props.open) void load();
}, { immediate: true });
watch(selectedId, () => {
  ++selectionEpoch; ++deliveriesEpoch; secret.value = ""; callbackSecret.value = ""; deliveries.value = []; deliveriesError.value = ""; deliveriesLoading.value = false;
  if (!editing.value) void loadDeliveries();
}, { flush: "sync" });
</script>

<template>
  <el-drawer :model-value="open" :title="t('workflowEmbed.manageTitle')" direction="rtl" size="min(660px, 100vw)" @update:model-value="emit('update:open', $event)">
    <div class="publication-panel">
      <p>{{ t("workflowEmbed.manageHint") }}</p>
      <p v-if="error" class="publication-error" role="alert">{{ error }}</p>
      <p v-if="loading">{{ t("workflowEmbed.loading") }}</p>
      <template v-else>
        <section v-if="secret" class="publication-secret">
          <strong>{{ t("workflowEmbed.secretTitle") }}</strong><p>{{ t("workflowEmbed.secretHint") }}</p>
          <textarea readonly :value="secret" :aria-label="t('workflowEmbed.secretTitle')" rows="2" spellcheck="false" />
          <div class="publication-actions"><button type="button" @click="copy(secret)">{{ t("workflowEmbed.copy") }}</button><button type="button" @click="secret = ''">{{ t("workflowEmbed.closeSecret") }}</button></div>
        </section>
        <section v-if="callbackSecret" class="publication-secret">
          <strong>{{ t("workflowEmbed.callbackSecretTitle") }}</strong><p>{{ t("workflowEmbed.callbackSecretHint") }}</p>
          <textarea readonly :value="callbackSecret" :aria-label="t('workflowEmbed.callbackSecretTitle')" rows="2" spellcheck="false" />
          <div class="publication-actions"><button type="button" @click="copy(callbackSecret)">{{ t("workflowEmbed.copy") }}</button><button type="button" @click="callbackSecret = ''">{{ t("workflowEmbed.closeCallbackSecret") }}</button></div>
        </section>
        <label v-if="data.publications.length" class="publication-field"><span>{{ t("workflowEmbed.selectEntry") }}</span><select v-model="selectedId" :disabled="busy || editing"><option v-for="entry in data.publications" :key="entry.id" :value="entry.id">{{ entry.name }} · {{ t(entry.enabled ? 'workflowEmbed.enabledStatus' : 'workflowEmbed.disabledStatus') }}</option></select></label>
        <p v-else-if="!editing">{{ t("workflowEmbed.empty") }}</p>
        <div v-if="!editing" class="publication-actions">
          <button type="button" class="primary" :disabled="!publishedVersion || busy" @click="clearForm()">{{ t("workflowEmbed.create") }}</button>
          <button v-if="selected" type="button" :disabled="busy" @click="clearForm(selected)">{{ t("workflowEmbed.edit") }}</button>
          <button v-if="selected" type="button" :disabled="busy" @click="rotate">{{ t("workflowEmbed.rotate") }}</button>
        </div>
        <p v-if="!publishedVersion">{{ t("workflowEmbed.unpublished") }}</p>
        <p v-if="selected && !editing" class="publication-meta">{{ selected.id }} · {{ selected.version_id }}<br>{{ t(selected.history_scope === 'business' ? 'workflowEmbed.scopeBusiness' : 'workflowEmbed.scopeWorkflow') }}<br>{{ t("workflowEmbed.rotateHint") }}</p>
        <form v-if="editing" class="publication-form" @submit.prevent="save">
          <label class="publication-field"><span>{{ t("workflowEmbed.name") }}</span><input v-model="form.name" :disabled="busy" required maxlength="80"></label>
          <label class="publication-field"><span>{{ t("workflowEmbed.versionLabel") }}</span><select v-model="form.version_id" :disabled="busy" required><option v-for="version in versions" :key="version.version_id" :value="version.version_id">{{ version.name || version.version_id }} · {{ version.version_id }}</option></select></label>
          <label class="publication-field"><span>{{ t("workflowEmbed.historyScope") }}</span><select v-model="form.history_scope" :disabled="busy"><option value="business">{{ t("workflowEmbed.scopeBusiness") }}</option><option value="workflow">{{ t("workflowEmbed.scopeWorkflow") }}</option></select></label><p class="publication-meta">{{ t(form.history_scope === 'business' ? 'workflowEmbed.scopeBusinessConfigHint' : 'workflowEmbed.scopeWorkflowHint') }}</p>
          <label class="publication-field"><span>{{ t("workflowEmbed.origins") }}</span><textarea v-model="origins" :disabled="busy" rows="3" required placeholder="https://business.example.com" /></label><p class="publication-meta">{{ t("workflowEmbed.originsHint") }}</p>
          <h3>{{ t("workflowEmbed.mappings") }}</h3><p class="publication-meta">{{ t("workflowEmbed.mappingHint") }}</p>
          <div v-for="(mapping, index) in form.mappings" :key="index" class="publication-row">
            <input v-model="mapping.subject" :disabled="busy" :aria-label="t('workflowEmbed.externalSubject')" :placeholder="t('workflowEmbed.externalSubject')" required maxlength="256">
            <select v-model="mapping.username" :disabled="busy" :aria-label="t('workflowEmbed.localUser')" required><option value="" disabled>{{ t("workflowEmbed.localUser") }}</option><option v-for="subject in data.subjects" :key="subject.username" :value="subject.username">{{ subject.display_name || subject.username }}</option></select>
            <button type="button" :disabled="busy" @click="form.mappings.splice(index, 1)">{{ t("workflowEmbed.remove") }}</button>
          </div>
          <button type="button" :disabled="busy" @click="form.mappings.push({ subject: '', username: '' })">{{ t("workflowEmbed.addMapping") }}</button>
          <h3>{{ t("workflowEmbed.inputs") }}</h3><p class="publication-meta">{{ t("workflowEmbed.inputHint") }}</p>
          <div v-for="(field, index) in form.input_fields" :key="index" class="publication-row inputs-row">
            <input v-model="field.key" :disabled="busy" :aria-label="t('workflowEmbed.key')" :placeholder="t('workflowEmbed.key')" required pattern="[A-Za-z_][A-Za-z0-9_]*" maxlength="64">
            <input v-model="field.label" :disabled="busy" :aria-label="t('workflowEmbed.label')" :placeholder="t('workflowEmbed.label')" maxlength="80">
            <label><input v-model="field.required" type="checkbox" :disabled="busy">{{ t("workflowEmbed.requiredLabel") }}</label><button type="button" :disabled="busy" @click="form.input_fields.splice(index, 1)">{{ t("workflowEmbed.remove") }}</button>
          </div>
          <button type="button" :disabled="busy" @click="form.input_fields.push({ key: '', label: '', required: true })">{{ t("workflowEmbed.addInput") }}</button>
          <h3>{{ t("workflowEmbed.callbackTitle") }}</h3>
          <p class="publication-meta">{{ t("workflowEmbed.callbackHint") }}</p>
          <label><input v-if="form.callback" v-model="form.callback.enabled" type="checkbox" :disabled="busy"> {{ t("workflowEmbed.callbackEnabled") }}</label>
          <label v-if="form.callback" class="publication-field"><span>{{ t("workflowEmbed.callbackUrl") }}</span><input v-model="form.callback.url" type="url" :disabled="busy" :required="form.callback.enabled" placeholder="https://business.example.com/pogo/callback"></label>
          <label><input v-model="form.enabled" type="checkbox" :disabled="busy"> {{ t("workflowEmbed.enabled") }}</label>
          <div class="publication-actions"><button type="submit" class="primary" :disabled="busy">{{ busy ? t("workflowEmbed.working") : t("workflowEmbed.save") }}</button><button type="button" :disabled="busy" @click="editing = false">{{ t("workflowEmbed.cancelEdit") }}</button></div>
        </form>
        <section v-if="selected && !editing" class="publication-deliveries">
          <h3>{{ t("workflowEmbed.callbackTitle") }}</h3>
          <p>{{ t(!selected.callback?.url ? 'workflowEmbed.callbackNotConfigured' : selected.callback.enabled ? 'workflowEmbed.callbackOn' : 'workflowEmbed.callbackOff') }}</p>
          <p v-if="selected.callback?.url" class="publication-meta">{{ selected.callback.url }}</p>
          <div class="publication-actions"><button type="button" :disabled="busy || !selected.callback?.url" @click="rotateCallback">{{ t("workflowEmbed.callbackRotate") }}</button><button type="button" :disabled="busy || deliveriesLoading" @click="loadDeliveries">{{ t("workflowEmbed.deliveryRefresh") }}</button></div>
          <p class="publication-meta">{{ t("workflowEmbed.deliveryHint") }}</p>
          <p v-if="deliveriesError" role="alert" class="publication-error">{{ deliveriesError }}</p>
          <p v-if="deliveriesLoading">{{ t("workflowEmbed.loading") }}</p>
          <p v-else-if="!deliveries.length">{{ t("workflowEmbed.deliveryEmpty") }}</p>
          <article v-for="delivery in deliveries" :key="delivery.id" class="publication-delivery">
            <div class="publication-delivery-heading"><strong>{{ delivery.event_type || '—' }}</strong><span>{{ t(`workflowEmbed.deliveryStatus.${delivery.status}`) }}</span></div>
            <p class="publication-meta">{{ t("workflowEmbed.deliveryEventId") }} {{ delivery.id }}<br>{{ t("workflowEmbed.runId") }} {{ delivery.run_id }}<template v-if="delivery.business_id"><br>{{ t("workflowEmbed.businessId", { id: delivery.business_id }) }}</template></p>
            <p>{{ t("workflowEmbed.deliveryAttempts", { count: delivery.attempts }) }}<template v-if="delivery.last_status"> · HTTP {{ delivery.last_status }}</template></p>
            <p v-if="delivery.target_url" class="publication-meta">{{ delivery.target_url }}</p>
            <p v-if="delivery.delivered_at">{{ t("workflowEmbed.deliveryDeliveredAt", { time: deliveryTime(delivery.delivered_at) }) }}</p>
            <p v-else-if="delivery.status === 'pending' && delivery.next_attempt_at">{{ t("workflowEmbed.deliveryNextAt", { time: deliveryTime(delivery.next_attempt_at) }) }}</p>
            <p v-if="delivery.blocked_reason" class="publication-error">{{ delivery.blocked_reason }}</p>
            <p v-if="delivery.last_error && delivery.last_error !== delivery.blocked_reason" class="publication-error">{{ delivery.last_error }}</p>
            <button v-if="retryable(delivery)" type="button" :disabled="busy || !selected.callback?.enabled" @click="retryDelivery(delivery)">{{ t("workflowEmbed.deliveryRetry") }}</button>
          </article>
        </section>
        <section v-if="selected && !editing" class="publication-integration"><h3>{{ t("workflowEmbed.integration") }}</h3><p>{{ t("workflowEmbed.integrationHint") }}</p><textarea readonly :value="example" :aria-label="t('workflowEmbed.integration')" rows="14" spellcheck="false" /><button type="button" @click="copy(example)">{{ t("workflowEmbed.copy") }}</button></section>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.publication-panel,.publication-form,.publication-field,.publication-secret,.publication-integration { display:flex;flex-direction:column;gap:12px; }.publication-panel { font-size:13px; }.publication-panel p { margin:0;color:var(--el-text-color-secondary);line-height:1.6; }.publication-panel h3 { margin:12px 0 0;font-size:14px; }.publication-panel input,.publication-panel textarea,.publication-panel select,.publication-panel button { font:inherit;padding:9px 10px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-primary);min-width:0; }.publication-panel input[type=checkbox] { padding:0; }.publication-panel textarea { width:100%;box-sizing:border-box;resize:vertical; }.publication-panel button { cursor:pointer;align-self:flex-start; }.publication-panel button:disabled { opacity:.5;cursor:default; }.publication-panel .primary { background:var(--el-color-primary);border-color:var(--el-color-primary);color:white; }.publication-actions { display:flex;flex-wrap:wrap;gap:8px; }.publication-row { display:grid;grid-template-columns:1fr 1fr auto;gap:8px;align-items:center; }.publication-row.inputs-row { grid-template-columns:1fr 1fr auto auto; }.publication-row label { white-space:nowrap; }.publication-secret { padding:16px;border:1px solid var(--el-color-warning);border-radius:8px; }.publication-secret textarea,.publication-integration textarea { font-family:monospace;font-size:12px; }.publication-panel .publication-error { color:var(--el-color-danger); }.publication-meta { font-size:12px;overflow-wrap:anywhere; }.publication-integration { border-top:1px solid var(--el-border-color-lighter);padding-top:16px;margin-top:8px; }
.publication-deliveries { display:flex;flex-direction:column;gap:12px;border-top:1px solid var(--el-border-color-lighter);padding-top:12px;margin-top:8px; }.publication-delivery { display:flex;flex-direction:column;gap:8px;border:1px solid var(--el-border-color-lighter);padding:12px;border-radius:8px; }.publication-delivery-heading { display:flex;justify-content:space-between;gap:12px; }.publication-delivery-heading span { color:var(--el-text-color-secondary); }
@media(max-width:500px) { .publication-row,.publication-row.inputs-row { grid-template-columns:1fr; }.publication-row button { justify-self:end; } }
</style>
