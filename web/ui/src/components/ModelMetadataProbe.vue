<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { api } from "@/api/client";
import type { ConfigMetadataProbeRequest, ConfigProbeResult } from "@/api/types";
import { highlightLines } from "./highlight";
import { extractModelMetadata, modelMetadataRequest, type ModelMetadataPreset } from "./modelMetadata";

interface MetadataProvider {
  name: string; baseUrl: string; keyEnv: string; keyValue: string;
  resolvedKey?: string; headers: { k: string; v: string }[];
}
// Provider and model props are read-only inputs. Detection owns only ephemeral
// query/results state and has no settings mutation or save events.
const props = defineProps<{ provider: MetadataProvider; model: string }>();
const { t } = useI18n();
const preset = ref<ModelMetadataPreset>("vllm"), queryModel = ref("");
const busy = ref(false), result = ref<ConfigProbeResult | null>(null), error = ref("");
let disposed = false, sequence = 0, controller: AbortController | undefined;
const presets: ModelMetadataPreset[] = ["vllm", "ollama-running", "ollama-show", "llamacpp"];
watch(() => props.model, model => { queryModel.value = model === "MODEL_ID" ? "" : model; }, { immediate: true });
const query = computed(() => {
  try { return { request: modelMetadataRequest(props.provider.baseUrl, queryModel.value.trim(), preset.value), error: "" }; }
  catch { return { request: null, error: t(preset.value === "ollama-show" && !queryModel.value.trim() ? "modelMetadata.modelRequired" : "modelMetadata.invalidURL") }; }
});
const canSend = computed(() => !!query.value.request && !busy.value);
function sq(value: string) { return "'" + value.replace(/'/g, "'\\''") + "'"; }
const curl = computed(() => {
  const request = query.value.request;
  if (!request) return "";
  const provider = props.provider, parts = [`curl -X ${request.method} ${sq(request.url)}`];
  parts.push(`-H ${sq("Accept: application/json")}`);
  const key = provider.keyValue.trim() || provider.resolvedKey || "";
  const hintedEnvironment = provider.keyEnv.trim() || (provider.name.trim().toUpperCase().replace(/[^A-Z0-9_]/g, "_") + "_API_KEY");
  const environment = /^[A-Za-z_][A-Za-z0-9_]*$/.test(hintedEnvironment) ? hintedEnvironment : "API_KEY";
  parts.push(key ? `-H ${sq("Authorization: Bearer " + key)}` : `-H "Authorization: Bearer $${environment}"`);
  if (request.body) parts.push(`-H ${sq("Content-Type: application/json")}`);
  for (const header of provider.headers) if (header.k.trim()) parts.push(`-H ${sq(`${header.k.trim()}: ${header.v}`)}`);
  if (request.body) parts.push(`-d ${sq(JSON.stringify(request.body))}`);
  return parts.join(" \\\n  ");
});
const curlTokens = computed(() => highlightLines(curl.value, "shell"));
const fingerprint = computed(() => JSON.stringify([props.provider.name, props.provider.baseUrl, props.provider.keyEnv,
  props.provider.keyValue, props.provider.resolvedKey, props.provider.headers, queryModel.value, preset.value]));
watch(fingerprint, () => { sequence++; controller?.abort(); busy.value = false; result.value = null; error.value = ""; }, { flush: "sync" });
const detected = computed(() => result.value?.ok && !result.value.truncated && result.value.body
  ? extractModelMetadata(result.value.body, preset.value) : { rows: [], validJSON: false });
const prettyBody = computed(() => {
  const body = result.value?.body || "";
  if (!result.value?.truncated) { try { return JSON.stringify(JSON.parse(body), null, 2); } catch { /* Display non-JSON responses as inert text. */ } }
  return body;
});
const showUnknown = computed(() => !!result.value?.ok && !result.value.truncated && !detected.value.rows.length);
async function send() {
  if (!canSend.value) return;
  const ticket = ++sequence, identity = fingerprint.value, current = new AbortController();
  controller?.abort(); controller = current; busy.value = true; result.value = null; error.value = "";
  const provider = props.provider, headers: Record<string, string> = {};
  for (const header of provider.headers) if (header.k.trim()) headers[header.k.trim()] = header.v;
  const body: ConfigMetadataProbeRequest = { name: provider.name, base_url: provider.baseUrl.trim(), key_env: provider.keyEnv.trim(),
    key: provider.keyValue.trim(), model: queryModel.value.trim(), headers, preset: preset.value };
  const valid = () => !disposed && ticket === sequence && fingerprint.value === identity && !current.signal.aborted;
  try { const response = await api.probeConfigMetadata(body, current.signal); if (valid()) result.value = response; }
  catch (value) { if (valid()) error.value = value instanceof Error ? value.message : String(value); }
  finally { if (!disposed && ticket === sequence) busy.value = false; }
}
async function copy() {
  if (!curl.value) return;
  try { await navigator.clipboard.writeText(curl.value); ElMessage.success(t("common.copied")); }
  catch { ElMessage.error(t("settingsDialog.copyFailed")); }
}
onBeforeUnmount(() => { disposed = true; sequence++; controller?.abort(); });
defineExpose({ send, copy, busy, canSend });
</script>

<template>
  <section class="model-metadata-probe" data-testid="model-metadata-probe">
    <div class="metadata-fields">
      <label><span>{{ t('modelMetadata.service') }}</span><select v-model="preset" data-testid="metadata-preset"><option v-for="item in presets" :key="item" :value="item">{{ t(`modelMetadata.presets.${item}`) }}</option></select></label>
      <label v-if="preset === 'ollama-show'"><span>{{ t('modelMetadata.queryModel') }}</span><input v-model="queryModel" data-testid="metadata-model" :placeholder="t('modelMetadata.modelPlaceholder')" spellcheck="false" /></label>
      <span class="metadata-readonly">{{ t('modelMetadata.readonly') }}</span>
    </div>
    <p class="metadata-hint">{{ t(`modelMetadata.hints.${preset}`) }}</p>
    <p v-if="query.error" class="metadata-error" role="alert">{{ query.error }}</p>
    <pre v-else class="curl-pre" data-testid="metadata-curl"><code><span v-for="(line, i) in curlTokens" :key="i" class="row"><span class="txt"><span v-for="(token, j) in line" :key="j" :class="token.kind">{{ token.text }}</span></span></span></code></pre>
    <p class="metadata-disclosure">{{ t('modelMetadata.notice') }}</p>
    <p v-if="busy" class="metadata-hint" role="status">{{ t('modelMetadata.loading') }}</p>
    <p v-if="error" class="metadata-error" role="alert">{{ error }}</p>
    <section v-if="result" class="metadata-result" :class="{ failed: !result.ok }" data-testid="metadata-result">
      <header><strong>{{ result.ok ? t('modelMetadata.received') : t('settingsDialog.probeFailed') }}</strong><span>{{ result.status_text || result.status }}</span><small v-if="result.elapsed_ms !== undefined">{{ result.elapsed_ms }} ms</small></header>
      <p v-if="result.error" class="metadata-error">{{ result.error }}</p>
      <p v-if="result.truncated" class="metadata-hint" role="status">{{ t('modelMetadata.truncated') }}</p>
      <div v-if="detected.rows.length" class="metadata-table-wrap"><table><thead><tr><th>{{ t('modelMetadata.model') }}</th><th>{{ t('modelMetadata.context') }}</th><th>{{ t('modelMetadata.output') }}</th><th>{{ t('modelMetadata.source') }}</th></tr></thead><tbody><tr v-for="(row, index) in detected.rows" :key="index"><td>{{ row.model || (preset === 'ollama-show' ? queryModel : model) }}</td><td>{{ row.contextWindow === undefined ? '—' : row.contextWindow.toLocaleString() }}</td><td>{{ row.maxOutputTokens === undefined ? '—' : row.maxOutputTokens.toLocaleString() }}</td><td><span>{{ t(`modelMetadata.kinds.${row.kind}`) }}</span><code>{{ row.source }}</code></td></tr></tbody></table></div>
      <p v-if="showUnknown" class="metadata-hint">{{ t(detected.validJSON ? 'modelMetadata.unknown' : 'modelMetadata.notJSON') }}</p>
      <details v-if="prettyBody" class="metadata-raw" :open="!result.ok || !detected.rows.length"><summary>{{ t('modelMetadata.raw') }}</summary><pre>{{ prettyBody }}</pre></details>
    </section>
  </section>
</template>

<style scoped>
.metadata-fields{display:flex;align-items:flex-end;flex-wrap:wrap;gap:12px}.metadata-fields>label{display:flex;flex-direction:column;gap:6px;min-width:190px;flex:1;font-size:12px;color:var(--el-text-color-secondary)}.metadata-fields select,.metadata-fields input{box-sizing:border-box;min-height:35px;width:100%;border:1px solid var(--el-border-color);border-radius:7px;padding:7px 9px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit}.metadata-readonly{align-self:center;margin-left:auto;padding:4px 8px;border-radius:5px;background:var(--el-fill-color-light);font-size:11px;color:var(--el-text-color-secondary)}.metadata-hint,.metadata-disclosure{font-size:12px;line-height:1.7;color:var(--el-text-color-secondary);margin:10px 0}.metadata-disclosure{padding-left:10px;border-left:2px solid var(--el-color-primary-light-5)}.metadata-error{font-size:12px;line-height:1.7;color:var(--el-color-danger);overflow-wrap:anywhere}.metadata-result{margin-top:14px;border:1px solid var(--el-border-color);border-radius:8px;overflow:hidden}.metadata-result>header{display:flex;align-items:center;gap:12px;padding:10px 12px;background:var(--el-fill-color-lighter);font-size:12px}.metadata-result>header>strong{font-weight:500}.metadata-result>header>small{margin-left:auto;color:var(--el-text-color-secondary)}.metadata-result.failed>header{color:var(--el-color-danger)}.metadata-result>p{margin:10px 12px}.metadata-table-wrap{overflow-x:auto}.metadata-result table{width:100%;border-collapse:collapse;font-size:12px;text-align:left}.metadata-result th{font-weight:500;color:var(--el-text-color-secondary);background:var(--el-fill-color-extra-light)}.metadata-result th,.metadata-result td{padding:9px 12px;border-bottom:1px solid var(--el-border-color-lighter);vertical-align:top}.metadata-result td:first-child{overflow-wrap:anywhere;min-width:100px}.metadata-result td:nth-child(2),.metadata-result td:nth-child(3){font-variant-numeric:tabular-nums;white-space:nowrap}.metadata-result td:last-child>code{display:block;font-size:10px;overflow-wrap:anywhere;color:var(--el-text-color-secondary);margin-top:4px}.metadata-raw{padding:10px 12px;font-size:12px}.metadata-raw summary{cursor:pointer;color:var(--el-text-color-secondary)}.metadata-raw pre{margin:10px 0 0;white-space:pre-wrap;overflow-wrap:anywhere;max-height:250px;overflow:auto;font:11px/1.7 var(--pg-mono,monospace)}
</style>
