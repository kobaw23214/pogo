<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { createWorkflowEmbedApi } from "@/api/workflowEmbed";
import { createWorkflowEmbedState } from "@/agent/useWorkflowEmbed";
import { runDisplayStatus, runLive, workflowRunCan } from "@/agent/workflowPresentation";
import { WORKFLOW_EMBED_PROTOCOL, workflowEmbedLocation, workflowLaunchMessage, workflowParentOrigin } from "@/agent/workflowEmbedProtocol";
import WorkflowApprovalAssignmentInfo from "@/components/workflow/WorkflowApprovalAssignmentInfo.vue";

const props = defineProps<{ publicationId: string }>();
const { t, d } = useI18n();
const origin = workflowParentOrigin(new URLSearchParams(location.search).get("parent_origin"));
const framed = window.parent !== window;
const waiting = ref(true);
const exchanging = ref(false);
const formError = ref("");
const decisions = ref<Record<string, string>>(Object.create(null));
let requestId = "";
let disposed = false;
let confirmed = false;
let lastEvent = "";
let handshakeTimer: ReturnType<typeof setTimeout> | undefined;
const state = createWorkflowEmbedState(createWorkflowEmbedApi(props.publicationId), rid => {
  history.replaceState(null, "", workflowEmbedLocation(location.href, rid));
}, record => {
  const status = runDisplayStatus(record);
  const signature = `${record.id}:${status}`;
  if (confirmed && origin && signature !== lastEvent) {
    lastEvent = signature;
    window.parent.postMessage({ type: "pogo.workflow.run", protocol: WORKFLOW_EMBED_PROTOCOL, publication_id: props.publicationId, run_id: record.id, status }, origin);
  }
});
const { session, run, history: records, inputs, artifacts, error, unavailable, busy, loading } = state;
const nodes = computed(() => Object.entries(run.value?.nodes ?? {}));
const selectedRun = computed(() => run.value?.id ?? "");
const live = computed(() => runLive(run.value));
const canStart = computed(() => session.value?.can_invoke === true && !live.value && !busy.value);
function date(value: string) { return Number.isFinite(Date.parse(value)) ? d(new Date(value), "short") : value; }
function humanAction(action: string) { return ["approve", "reject"].includes(action) ? t(`workflowEmbed.${action}`) : action; }
function requestLaunch() {
  if (!origin || !framed || disposed || exchanging.value) return;
  state.invalidate(); confirmed = false; lastEvent = ""; waiting.value = true; formError.value = "";
  requestId = crypto.randomUUID();
  window.parent.postMessage({ type: "pogo.workflow.ready", protocol: WORKFLOW_EMBED_PROTOCOL, publication_id: props.publicationId, request_id: requestId }, origin);
  if (handshakeTimer !== undefined) clearTimeout(handshakeTimer);
  handshakeTimer = setTimeout(() => { if (!disposed && waiting.value) { waiting.value = false; state.invalidate(t("workflowEmbed.waitingTimeout")); } }, 20000);
}
async function receive(event: MessageEvent) {
  if (!origin || !framed || !waiting.value || exchanging.value) return;
  const code = workflowLaunchMessage(event, window.parent, origin, props.publicationId, requestId);
  if (!code) return;
  if (handshakeTimer !== undefined) clearTimeout(handshakeTimer);
  exchanging.value = true; waiting.value = false;
  try {
    confirmed = true;
    await state.authenticate(code, origin, new URLSearchParams(location.search).get("run"));
  } finally { if (!disposed) exchanging.value = false; }
}
async function start() {
  formError.value = "";
  const missing = session.value?.publication.input_fields.find(field => field.required && !inputs[field.key]?.trim());
  if (missing) { formError.value = t("workflowEmbed.required", { label: missing.label || missing.key }); return; }
  await state.start();
}
async function decide(nodeId: string) {
  formError.value = "";
  const human = run.value?.nodes[nodeId]?.human;
  const missing = human?.fields?.find(field => field.required && !state.approvalValues(nodeId)[field.key]?.trim());
  if (missing) { formError.value = t("workflowEmbed.required", { label: missing.label || missing.key }); return; }
  await state.decide(nodeId, decisions.value[nodeId] || human?.actions[0] || "approve");
}
watch(() => run.value?.id, () => { formError.value = ""; decisions.value = Object.create(null); });
onMounted(() => { window.addEventListener("message", receive); requestLaunch(); });
onBeforeUnmount(() => { disposed = true; if (handshakeTimer !== undefined) clearTimeout(handshakeTimer); window.removeEventListener("message", receive); state.invalidate(); });
</script>

<template>
  <main class="workflow-embed" :aria-busy="loading || exchanging">
    <section v-if="!origin || !framed" class="embed-gate" role="alert">
      <h1>{{ t("workflowEmbed.title") }}</h1><p>{{ t("workflowEmbed.hostRequired") }}</p>
    </section>
    <section v-else-if="!session" class="embed-gate">
      <h1>{{ t("workflowEmbed.title") }}</h1>
      <p v-if="exchanging || loading" role="status">{{ t("workflowEmbed.connecting") }}</p>
      <p v-else-if="unavailable && !waiting" role="alert">{{ t("workflowEmbed.unavailable") }}</p>
      <p v-else role="status">{{ t("workflowEmbed.waiting") }}</p>
      <p v-if="error" role="alert" class="embed-error">{{ error }}</p>
      <button v-if="!exchanging && !waiting" type="button" @click="requestLaunch">{{ t("workflowEmbed.reconnect") }}</button>
    </section>
    <template v-else>
      <header class="embed-header">
        <div><h1>{{ session.publication.name }}</h1><p>{{ t("workflowEmbed.signedIn", { name: session.subject }) }}</p></div>
        <span class="embed-version">{{ t("workflowEmbed.version", { version: session.publication.version_id }) }}</span>
      </header>
      <p v-if="session.publication.history_scope === 'business'" class="embed-business-scope" role="note">{{ t("workflowEmbed.businessScopeHint", { id: session.business_id || '—' }) }}</p>
      <p v-if="error || formError" role="alert" class="embed-error">{{ formError || error }}</p>
      <section class="embed-toolbar">
        <label for="embed-run-history">{{ t("workflowEmbed.history") }}</label>
        <select id="embed-run-history" :value="selectedRun" :disabled="busy || loading" @change="state.select(($event.target as HTMLSelectElement).value || null)">
          <option value="">{{ t("workflowEmbed.newRun") }}</option>
          <option v-for="item in records" :key="item.id" :value="item.id">{{ date(item.created_at) }} · {{ t(`workflow.run.${runDisplayStatus(item)}`) }} · {{ item.id.slice(-8) }}</option>
        </select>
        <button type="button" :disabled="busy || loading" @click="state.refresh">{{ t("workflowEmbed.refresh") }}</button>
      </section>
      <section v-if="!run" class="embed-start">
        <h2>{{ t("workflowEmbed.startTitle") }}</h2>
        <p>{{ t("workflowEmbed.startHint") }}</p>
        <form @submit.prevent="start">
          <label v-for="field in session.publication.input_fields" :key="field.key" class="embed-field">
            <span>{{ field.label || field.key }}{{ field.required ? ' *' : '' }}</span>
            <textarea v-model="inputs[field.key]" :required="field.required" :disabled="busy || session.can_invoke !== true" maxlength="16384" rows="2" />
          </label>
          <p v-if="session.can_invoke !== true">{{ t("workflowEmbed.noInvoke") }}</p>
          <button type="submit" class="primary" :disabled="!canStart">{{ busy ? t("workflowEmbed.working") : t("workflowEmbed.start") }}</button>
        </form>
      </section>
      <section v-else class="embed-run">
        <div class="embed-run-heading">
          <div><h2>{{ t(`workflow.run.${runDisplayStatus(run)}`) }}</h2><p>{{ t("workflowEmbed.runId") }} {{ run.id }}</p></div>
          <div class="embed-actions">
            <button v-if="workflowRunCan(run, 'can_cancel', false)" type="button" class="danger" :disabled="busy" @click="state.cancel">{{ t("workflowEmbed.cancel") }}</button>
            <button v-if="workflowRunCan(run, 'can_retry', false)" type="button" :disabled="busy" @click="state.retry">{{ t("workflowEmbed.retry") }}</button>
            <button v-if="!live" type="button" :disabled="busy" @click="state.select(null)">{{ t("workflowEmbed.newRun") }}</button>
          </div>
        </div>
        <p v-if="run.origin?.publication_id" class="embed-origin">
          {{ t("workflowEmbed.publicationSource", { id: run.origin.publication_id }) }}
          <template v-if="run.origin.business_id"> · {{ t("workflowEmbed.businessId", { id: run.origin.business_id }) }}</template>
          <template v-if="run.origin.version_id"> · {{ t("workflowEmbed.version", { version: run.origin.version_id }) }}</template>
        </p>
        <p v-if="run.persistence_error" role="alert" class="embed-error">{{ t("workflow.run.persistenceHint") }}</p>
        <p v-if="run.error" class="embed-error">{{ run.error }}</p>
        <p v-if="!nodes.length">{{ t("workflowEmbed.queued") }}</p>
        <article v-for="[nodeId, node] in nodes" :key="`${run.id}:${nodeId}`" class="embed-node">
          <div class="embed-node-heading"><h3>{{ nodeId }}</h3><span>{{ t(`workflow.state.${node.status}`) }}</span></div>
          <WorkflowApprovalAssignmentInfo :assignment="run.approvals?.[nodeId]" :pending="node.status === 'waiting_human'" />
          <p v-if="node.summary" class="embed-content">{{ node.summary }}</p>
          <p v-if="node.error" class="embed-error">{{ node.error }}</p>
          <template v-if="node.human">
            <p class="embed-content">{{ node.human.prompt }}</p>
            <form v-if="node.status === 'waiting_human' && !node.human.action" @submit.prevent="decide(nodeId)">
              <p v-if="!workflowRunCan(run, 'can_approve', false, nodeId) && !run.approvals?.[nodeId]?.blocked_reason">{{ t("workflowEmbed.noApproval") }}</p>
              <label v-for="field in node.human.fields ?? []" :key="field.key" class="embed-field">
                <span>{{ field.label || field.key }}{{ field.required ? ' *' : '' }}</span>
                <select v-if="field.type === 'select'" :value="state.approvalValues(nodeId)[field.key] || ''" :required="field.required" :disabled="busy || !workflowRunCan(run, 'can_approve', false, nodeId)" @change="state.approvalValues(nodeId)[field.key] = ($event.target as HTMLSelectElement).value">
                  <option value="" disabled>{{ t("workflowEmbed.choose") }}</option><option v-for="option in field.options ?? []" :key="option" :value="option">{{ option }}</option>
                </select>
                <textarea v-else :value="state.approvalValues(nodeId)[field.key] || ''" :required="field.required" :disabled="busy || !workflowRunCan(run, 'can_approve', false, nodeId)" rows="2" @input="state.approvalValues(nodeId)[field.key] = ($event.target as HTMLTextAreaElement).value" />
              </label>
              <div class="embed-actions"><button v-for="action in node.human.actions" :key="action" type="submit" :class="{ danger: action === 'reject', primary: action === 'approve' }" :disabled="busy || !!run.persistence_error || !workflowRunCan(run, 'can_approve', false, nodeId)" @click="decisions[nodeId] = action">{{ humanAction(action) }}</button></div>
            </form>
            <dl v-else class="embed-values"><template v-for="(value, key) in node.human.values" :key="key"><dt>{{ node.human.fields?.find(field => field.key === key)?.label || key }}</dt><dd>{{ value }}</dd></template></dl>
          </template>
          <template v-if="node.artifact && workflowRunCan(run, 'can_read_artifacts', false, nodeId)">
            <button type="button" :disabled="busy" @click="state.readArtifact(nodeId)">{{ t("workflowEmbed.artifact") }}</button><pre v-if="artifacts[nodeId] !== undefined">{{ artifacts[nodeId] }}</pre>
          </template>
        </article>
      </section>
      <footer>{{ t("workflowEmbed.expires", { time: date(session.expires_at) }) }}</footer>
    </template>
  </main>
</template>

<style scoped>
.workflow-embed { min-height: 100dvh; max-width: 1040px; margin: 0 auto; padding: 24px; color: var(--el-text-color-primary); background: var(--el-bg-color); font-size: 14px; box-sizing: border-box; }
.workflow-embed h1, .workflow-embed h2, .workflow-embed h3, .workflow-embed p { margin: 0; }
.workflow-embed h1 { font-size: 22px; }.workflow-embed h2 { font-size: 18px; }.workflow-embed h3 { font-size: 14px; }
.embed-header, .embed-run-heading, .embed-node-heading { display: flex; justify-content: space-between; align-items: center; gap: 16px; }
.embed-header { padding-bottom: 20px; border-bottom: 1px solid var(--el-border-color-lighter); }
.embed-header p, footer, .embed-start>p, .embed-run-heading p { color: var(--el-text-color-secondary); margin-top: 8px; font-size: 12px; }
.embed-version { color: var(--el-text-color-secondary); font-size: 12px; }.embed-toolbar { display: flex; gap: 10px; align-items: center; margin: 20px 0; }
.embed-toolbar select { flex: 1; min-width: 0; }.embed-start, .embed-node { border: 1px solid var(--el-border-color-lighter); border-radius: 12px; padding: 20px; }
.embed-start form, .embed-node form { margin-top: 18px; display: flex; flex-direction: column; gap: 14px; }.embed-field { display: flex; flex-direction: column; gap: 7px; }
.workflow-embed button, .workflow-embed select, .workflow-embed textarea { font: inherit; border: 1px solid var(--el-border-color); border-radius: 7px; background: var(--el-bg-color); color: inherit; padding: 9px 12px; box-sizing: border-box; }
.workflow-embed textarea { width: 100%; resize: vertical; }.workflow-embed button { cursor: pointer; align-self: flex-start; }.workflow-embed button:disabled, .workflow-embed select:disabled, .workflow-embed textarea:disabled { cursor: default; opacity: .5; }
.workflow-embed .primary { background: var(--el-color-primary); border-color: var(--el-color-primary); color: white; }.workflow-embed .danger, .workflow-embed .embed-error { color: var(--el-color-danger); }
.embed-error { margin: 14px 0 !important; white-space: pre-wrap; overflow-wrap: anywhere; }.embed-node { margin-top: 16px; }.embed-content { white-space: pre-wrap; overflow-wrap: anywhere; margin: 12px 0 !important; line-height: 1.6; }
.embed-node-heading span { font-size: 12px; color: var(--el-text-color-secondary); }.embed-actions { display: flex; gap: 8px; flex-wrap: wrap; }.embed-run-heading p { overflow-wrap: anywhere; }
.embed-values { display: grid; grid-template-columns: minmax(70px,auto) 1fr; gap: 8px 18px; }.embed-values dt { color: var(--el-text-color-secondary); }.embed-values dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
pre { overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 420px; font-size: 12px; background: var(--el-fill-color-light); padding: 12px; border-radius: 6px; }footer { margin-top: 22px; }.embed-gate { max-width: 520px; margin: 12vh auto; display: flex; flex-direction: column; gap: 20px; }
.embed-origin { font-size: 12px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; margin-top: 10px !important; }
.embed-business-scope { font-size: 13px; color: var(--el-text-color-secondary); overflow-wrap: anywhere; margin-top: 14px !important; }
@media(max-width:600px) { .workflow-embed { padding: 16px; }.embed-header,.embed-run-heading { align-items: flex-start; flex-direction: column; }.embed-version { display: none; }.embed-toolbar { flex-wrap: wrap; }.embed-toolbar select { flex-basis: 75%; }.embed-node,.embed-start { padding: 16px; } }
</style>
