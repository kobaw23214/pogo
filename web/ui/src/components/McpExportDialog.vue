<script setup lang="ts">
// The MCP export dialog: one component behind both entries — the client
// sheet's "导出连接" (selected saved servers) and the serving sheet's
// "导出本机接入配置" (one connection entry for calling *this* machine). All
// mapping rules live in mcpExport.ts as pure functions; here is only
// selection state and wiring.
//
// Two invariants the component does not re-implement but depends on: the
// source data is the SAVED config (the caller passes config/server_state from
// GET /api/mcp, never the half-edited form), and the preview text is the one
// and only artifact — copy and download serialize exactly what is shown.

import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import type { MCPServerConfig, MCPServerState } from "@/api/types";
import { MCP_ENV_NAME_RE, MCP_NAME_RE } from "./mcpImport";
import {
  MCP_EXPORT_TARGETS,
  buildServeExportEntry,
  exportMCPServers,
  exportConnectionLabel,
  suggestServeURL,
  type MCPExportTarget,
  type MCPExportWarning,
} from "./mcpExport";
import { serveStatusParts } from "./mcpServe";

const props = defineProps<{
  modelValue: boolean;
  mode: "client" | "serve";
  /** client mode: the saved servers map, flattened for display order. */
  servers?: { name: string; config: MCPServerConfig }[];
  /** serve mode: the effective runtime state (GET /api/mcp's server_state). */
  serveState?: MCPServerState | null;
  /** serve mode: the saved token_env name, when the file names one. */
  serveTokenEnv?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [boolean] }>();

const { t } = useI18n();

const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit("update:modelValue", v),
});

const target = ref<MCPExportTarget>("pogo");
// client mode: the checkbox selection (all saved servers by default).
const selected = ref<string[]>([]);
// serve mode: the connection definition's editable fields.
const expName = ref("pogo-local");
const expUrl = ref("");
const expTokenVar = ref("POGO_MCP_TOKEN");

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return;
    target.value = "pogo";
    selected.value = (props.servers ?? []).map((s) => s.name);
    expName.value = "pogo-local";
    expUrl.value = suggestServeURL(props.serveState?.endpoints);
    expTokenVar.value = props.serveTokenEnv?.trim() || "POGO_MCP_TOKEN";
  },
  { immediate: true },
);

const serveParts = computed(() => serveStatusParts(props.serveState));

const serveNameBad = computed(() => props.mode === "serve" && !MCP_NAME_RE.test(expName.value.trim()));
const serveUrlBad = computed(() => props.mode === "serve" && !/^https?:\/\//.test(expUrl.value.trim()));
const serveVarBad = computed(
  () => props.mode === "serve" && serveParts.value.tokenRequired && !MCP_ENV_NAME_RE.test(expTokenVar.value.trim()),
);

// sourceMap is the export's input: the selection in client mode, one
// synthesized remote entry in serve mode. Invalid serve inputs feed nothing
// in — the inline error says why, and copy/download stay disabled.
const sourceMap = computed<Record<string, MCPServerConfig>>(() => {
  if (props.mode === "serve") {
    if (serveNameBad.value || serveUrlBad.value || serveVarBad.value) return {};
    return buildServeExportEntry({
      name: expName.value,
      url: expUrl.value,
      tokenRequired: serveParts.value.tokenRequired,
      tokenVar: expTokenVar.value,
    });
  }
  const picked = new Set(selected.value);
  const out: Record<string, MCPServerConfig> = Object.create(null);
  for (const s of props.servers ?? []) if (picked.has(s.name)) out[s.name] = s.config;
  return out;
});

const result = computed(() => exportMCPServers(sourceMap.value, target.value));
const canExport = computed(() => result.value.servers.length > 0);

function warnText(w: MCPExportWarning): string {
  return t(`settingsDialog.mcpExpWarn_${w.code}`, w.params ?? {});
}

function toggleAll(on: boolean) {
  selected.value = on ? (props.servers ?? []).map((s) => s.name) : [];
}

async function copyExport() {
  if (!canExport.value) return;
  try {
    await navigator.clipboard.writeText(result.value.text);
    ElMessage.success(t("settingsDialog.mcpExpCopied"));
  } catch {
    ElMessage.error(t("settingsDialog.copyFailed"));
  }
}

function downloadExport() {
  if (!canExport.value) return;
  const base = props.mode === "serve" ? `mcp-${expName.value.trim()}` : "pogo-mcp";
  const blob = new Blob([result.value.text], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `${base}-${target.value}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Leave the Blob alive until the browser has consumed the download.
  window.setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t(mode === 'serve' ? 'settingsDialog.mcpExpTitleServe' : 'settingsDialog.mcpExpTitleClient')"
    width="720px"
    append-to-body
    class="settings-mcp-export-dialog"
  >
    <!-- Scope: which connections are exported. Client mode picks from the
         saved list; serve mode names one connection to this machine. -->
    <template v-if="mode === 'client'">
      <p class="hint">{{ t("settingsDialog.mcpExpScopeHint") }}</p>
      <div class="mcp-export-scope">
        <label v-for="s in servers ?? []" :key="s.name" class="mcp-export-pick">
          <el-checkbox
            :model-value="selected.includes(s.name)"
            @change="(v: boolean) => (selected = v ? [...selected, s.name] : selected.filter((n) => n !== s.name))"
          >
            <span class="mcp-export-pick-name">{{ s.name }}</span>
            <span v-if="s.config.enabled === false" class="mcp-export-off">{{ t("settingsDialog.mcpExpDisabledTag") }}</span>
            <span class="mcp-export-pick-sum">{{ exportConnectionLabel(s.config) }}</span>
          </el-checkbox>
        </label>
        <p v-if="!(servers ?? []).length" class="hint">{{ t("settingsDialog.mcpExpEmpty") }}</p>
      </div>
      <div class="hint-row">
        <p class="hint" />
        <div class="hint-actions">
          <button class="mini" @click="toggleAll(true)">{{ t("settingsDialog.mcpExpAll") }}</button>
          <button class="mini" @click="toggleAll(false)">{{ t("settingsDialog.mcpExpNone") }}</button>
        </div>
      </div>
    </template>

    <template v-else>
      <!-- The serving half's effective state, stated before the form: the
           export describes it but cannot change the authorization scope. -->
      <div class="mcp-export-serve-status">
        <span class="mcp-export-dot" :class="{ on: serveParts.on }" />
        <span>{{
          serveParts.on
            ? t("settingsDialog.mcpSrvStatusOn", {
                token: serveParts.tokenRequired ? t("settingsDialog.mcpSrvStatusToken") : t("settingsDialog.mcpSrvStatusOpen"),
                policy: serveParts.policy,
              })
            : t("settingsDialog.mcpSrvStatusOff")
        }}</span>
      </div>
      <p class="hint">
        {{
          t("settingsDialog.mcpExpServePolicy", {
            policy: serveParts.policy,
            tools: props.serveState?.include_tools?.length
              ? props.serveState.include_tools.join(", ")
              : t("settingsDialog.mcpExpServePolicyAll"),
          })
        }}
      </p>
      <p class="hint">{{ t("settingsDialog.mcpExpScopeReadonly") }}</p>
      <p v-if="!serveParts.on" class="note bad">{{ t("settingsDialog.mcpExpServeOff") }}</p>
      <p v-else-if="serveParts.restart" class="note bad">{{ t("settingsDialog.mcpExpServeRestart") }}</p>
      <p v-if="serveParts.source === 'env'" class="note bad">{{ t("settingsDialog.mcpExpServeEnvTaken") }}</p>

      <div class="prow-line">
        <label class="fld">
          <span>{{ t("settingsDialog.mcpExpServeName") }}</span>
          <input v-model="expName" type="text" spellcheck="false" placeholder="pogo-local" />
        </label>
        <label class="fld grow">
          <span>{{ t("settingsDialog.mcpExpServeUrl") }}</span>
          <input v-model="expUrl" type="text" spellcheck="false" placeholder="https://host/mcp" />
        </label>
      </div>
      <p v-if="serveNameBad" class="note bad">{{ t("settingsDialog.mcpExpBadName") }}</p>
      <p v-if="serveUrlBad" class="note bad">{{ t("settingsDialog.mcpExpBadUrl") }}</p>
      <template v-if="serveParts.tokenRequired">
        <div class="prow-line">
          <label class="fld grow">
            <span>{{ t("settingsDialog.mcpExpServeTokenVar") }}</span>
            <input v-model="expTokenVar" type="text" spellcheck="false" placeholder="POGO_MCP_TOKEN" />
          </label>
        </div>
        <p v-if="serveVarBad" class="note bad">{{ t("settingsDialog.mcpImportErr_bearer") }}</p>
        <p class="hint">{{ t("settingsDialog.mcpExpServeTokenHint") }}</p>
      </template>
      <p v-else class="hint">{{ t("settingsDialog.mcpExpServeNoToken") }}</p>
    </template>

    <!-- Target format. -->
    <div class="prow-line">
      <div class="fld grow">
        <span>{{ t("settingsDialog.mcpExpTarget") }}</span>
        <el-radio-group v-model="target" size="small">
          <el-radio-button v-for="tg in MCP_EXPORT_TARGETS" :key="tg" :value="tg">
            {{ t(`settingsDialog.mcpExpTarget_${tg}`) }}
          </el-radio-button>
        </el-radio-group>
      </div>
    </div>

    <!-- Warnings: every entry left out or setting dropped is named here. -->
    <template v-if="result.warnings.length">
      <p v-for="(w, i) in result.warnings" :key="i" class="note bad">{{ warnText(w) }}</p>
    </template>

    <!-- The variables the receiver must set in the target environment. -->
    <div class="mcp-export-vars">
      <span class="lbl">{{ t("settingsDialog.mcpExpVars") }}</span>
      <template v-if="result.vars.length">
        <div v-for="v in result.vars" :key="v.name" class="mcp-export-var-row">
          <code class="mcp-export-var-name">{{ v.name }}</code>
          <span class="mcp-export-var-why">
            {{ t(`settingsDialog.mcpExpVarWhy_${v.why}`, { server: v.server, detail: v.detail ?? "" }) }}
          </span>
        </div>
      </template>
      <p v-else class="hint">{{ t("settingsDialog.mcpExpNoVars") }}</p>
    </div>

    <!-- The one validated artifact; copy and download use this exact text. -->
    <div class="mcp-export-preview">
      <span class="lbl">{{ t("settingsDialog.mcpExpPreview") }}</span>
      <pre v-if="canExport" class="mcp-export-pre">{{ result.text }}</pre>
      <p v-else class="note bad">{{ t("settingsDialog.mcpExpEmpty") }}</p>
    </div>

    <template #footer>
      <el-button @click="visible = false">{{ t("common.cancel") }}</el-button>
      <el-button :disabled="!canExport" @click="copyExport">{{ t("settingsDialog.mcpExpCopy") }}</el-button>
      <el-button type="primary" :disabled="!canExport" @click="downloadExport">
        {{ t("settingsDialog.mcpExpDownload") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style lang="scss">
/* Unscoped: el-dialog teleports to body (append-to-body), so scoped rules
   would never reach it. Class names stay prefixed to avoid collisions. */
.settings-mcp-export-dialog {
  /* Local copies of the settings dialog's shared primitives — its rules are
     scoped under .settings-dialog, which this teleported dialog is not. */
  .hint {
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    margin: 0;
    line-height: 1.5;
  }

  .note {
    font-size: 12px;
    color: var(--el-text-color-secondary);

    &.bad {
      color: var(--el-color-danger);
    }
  }

  .mini {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: 1px dashed var(--el-border-color);
    background: transparent;
    padding: 3px 10px;
    border-radius: 6px;
    font: inherit;
    font-size: 11.5px;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
      border-color: var(--el-color-primary);
    }
  }

  .hint-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .hint-actions {
    display: flex;
    gap: 6px;
    flex: 0 0 auto;
  }

  .prow-line {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    margin: 6px 0;
  }

  .fld {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;

    &.grow {
      flex: 1 1 0;
    }

    span {
      font-size: 11px;
      color: var(--el-text-color-secondary);
    }

    input {
      border: 1px solid var(--el-border-color-lighter);
      border-radius: 6px;
      background: var(--el-fill-color-blank);
      color: var(--el-text-color-primary);
      font: inherit;
      font-size: 12px;
      padding: 4px 8px;
      outline: none;
      min-width: 0;

      &:focus {
        border-color: var(--el-color-primary);
      }
    }
  }

  .mcp-export-scope {
    max-height: 180px;
    overflow: auto;
    border: 1px solid var(--el-border-color);
    border-radius: 6px;
    padding: 4px 10px;
    margin: 6px 0;
  }

  .mcp-export-pick {
    display: block;
    padding: 2px 0;
  }

  .mcp-export-pick-name {
    font-weight: 600;
  }

  .mcp-export-pick-sum {
    margin-left: 8px;
    opacity: 0.65;
    font-size: 12px;
  }

  .mcp-export-off {
    margin-left: 6px;
    font-size: 11px;
    color: var(--el-color-warning);
    border: 1px solid var(--el-color-warning);
    border-radius: 4px;
    padding: 0 4px;
  }

  .mcp-export-serve-status {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 4px 0;
  }

  .mcp-export-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--el-color-info);
    flex: none;

    &.on {
      background: var(--el-color-success);
    }
  }

  .mcp-export-vars {
    margin: 10px 0;

    .lbl {
      display: block;
      font-size: 12px;
      opacity: 0.7;
      margin-bottom: 4px;
    }
  }

  .mcp-export-var-row {
    display: flex;
    gap: 10px;
    align-items: baseline;
    padding: 2px 0;
  }

  .mcp-export-var-name {
    font-family: var(--el-font-family-monospace, monospace);
    color: var(--el-color-primary);
    white-space: nowrap;
  }

  .mcp-export-var-why {
    font-size: 12px;
    opacity: 0.75;
  }

  .mcp-export-preview {
    margin-top: 10px;

    .lbl {
      display: block;
      font-size: 12px;
      opacity: 0.7;
      margin-bottom: 4px;
    }
  }

  .mcp-export-pre {
    max-height: 300px;
    overflow: auto;
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color);
    border-radius: 6px;
    padding: 10px;
    font-size: 12px;
    line-height: 1.5;
    white-space: pre;
    margin: 0;
  }
}
</style>
