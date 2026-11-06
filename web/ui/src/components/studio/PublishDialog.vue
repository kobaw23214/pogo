<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
import { computed, ref } from "vue";
import { Check, Close, CopyDocument, Key, TopRight } from "@element-plus/icons-vue";
import AgentAvatar from "@/components/AgentAvatar.vue";
import type { PlatformAgent } from "@/agent/platformTypes";
import { embedSnippet, publishedAgentUrl } from "@/agent/studioPresentation";
import { token } from "@/api/client";

/**
 * 发布与嵌入 — the Agent's page and iframe snippet, derived from the
 * origin alone. The page uses existing workspace login; a draft never
 * reaches this dialog — the card menu only offers it for a published
 * Agent. The with-key variant is the admin's convenience copy: it embeds
 * this browser's own workspace key (?token=…, note the question mark —
 * "&token=" after a bare path is part of the path and 404s), so whoever
 * opens it lands inside without typing the key. Share it only with people
 * the key was already meant for.
 */
const dlg = ref<HTMLDialogElement | null>(null);
const agent = ref<PlatformAgent | null>(null);
const copiedKind = ref<"link" | "link-key" | "iframe" | null>(null);
const copyError = ref("");
let copiedTimer: ReturnType<typeof setTimeout> | undefined;

const origin = () => window.location.origin;

const url = computed(() => (agent.value ? publishedAgentUrl(origin(), agent.value.id) : ""));
/** Same page, pre-authorized with the key this admin session holds. */
const urlWithKey = computed(() => (url.value && token ? `${url.value}?token=${encodeURIComponent(token)}` : ""));
const snippet = computed(() => (agent.value ? embedSnippet(origin(), agent.value.id) : ""));

/** Published agents only: a draft has no public page, so there is nothing to
    share — say so instead of producing a link that 404s into the draft gate. */
function open(target: PlatformAgent) {
  if (target.status !== "ready") return;
  agent.value = target;
  copiedKind.value = null;
  copyError.value = "";
  dlg.value?.showModal();
}

function close() {
  dlg.value?.close();
}

function onClosed() {
  agent.value = null;
  copiedKind.value = null;
  copyError.value = "";
  clearTimeout(copiedTimer);
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      return ok;
    } catch {
      return false;
    }
  }
}

/** Clipboard failure is honest: the text stays selected-able in the readonly
 * field and the error says exactly what to do, rather than faking success. */
async function copy(kind: "link" | "link-key" | "iframe") {
  const text = kind === "link" ? url.value : kind === "link-key" ? urlWithKey.value : snippet.value;
  if (!text) return;
  copyError.value = "";
  const ok = await copyText(text);
  if (ok) {
    copiedKind.value = kind;
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => (copiedKind.value = null), 2000);
  } else {
    copyError.value = tr('studioManagement.text187');
  }
}

defineExpose({ open, close });
</script>

<template>
  <dialog ref="dlg" class="publish" aria-labelledby="publish-title" @close="onClosed">
    <div class="pub-shell">
      <header class="pub-head">
        <h2 id="publish-title">{{ tr('studioManagement.text007') }}</h2>
        <button type="button" class="iconbtn" :aria-label="tr('studioManagement.text054')" @click="close">
          <el-icon aria-hidden="true"><Close /></el-icon>
        </button>
      </header>

      <div v-if="agent" class="pub-body">
        <div class="pub-agent">
          <AgentAvatar :icon="agent.icon" :color="agent.color" :size="38" />
          <span class="pub-agent-name">{{ agent.name }}</span>
          <span class="pub-status">{{ tr('studioManagement.text003') }}</span>
        </div>

        <section class="pub-section" aria-labelledby="pub-link-label">
          <span id="pub-link-label" class="pub-label">{{ tr('studioManagement.text188') }}</span>
          <div class="pub-field">
            <input class="input" type="text" readonly :value="url" :aria-label="tr('studioManagement.text189')" @focus="($event.target as HTMLInputElement).select()" />
          </div>
          <div class="pub-actions">
            <a class="btn solid" :href="url" target="_blank" rel="noopener noreferrer">
              <el-icon aria-hidden="true"><TopRight /></el-icon>{{ tr('studioManagement.text190') }} </a>
            <button type="button" class="btn soft" @click="copy('link')">
              <el-icon aria-hidden="true"><component :is="copiedKind === 'link' ? Check : CopyDocument" /></el-icon>
              {{ copiedKind === "link" ? tr('studioManagement.text191') : tr('studioManagement.text192') }}
            </button>
            <button v-if="urlWithKey" type="button" class="btn soft" @click="copy('link-key')">
              <el-icon aria-hidden="true"><component :is="copiedKind === 'link-key' ? Check : Key" /></el-icon>
              {{ copiedKind === "link-key" ? tr('studioManagement.text191') : tr('studioManagement.text193') }}
            </button>
          </div>
        </section>

        <section class="pub-section" aria-labelledby="pub-embed-label">
          <span id="pub-embed-label" class="pub-label">{{ tr('studioManagement.text194') }}</span>
          <textarea class="input code" rows="3" readonly :value="snippet" :aria-label="tr('studioManagement.text195')" @focus="($event.target as HTMLTextAreaElement).select()" />
          <div class="pub-actions">
            <button type="button" class="btn soft" @click="copy('iframe')">
              <el-icon aria-hidden="true"><component :is="copiedKind === 'iframe' ? Check : CopyDocument" /></el-icon>
              {{ copiedKind === "iframe" ? tr('studioManagement.text191') : tr('studioManagement.text196') }}
            </button>
          </div>
        </section>

        <p v-if="copyError" class="pub-error" role="alert">{{ copyError }}</p>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.publish {
  width: min(560px, calc(100vw - 32px));
  box-sizing: border-box;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 0;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
  box-shadow: var(--el-box-shadow-dark);
}
.publish::backdrop {
  background: var(--el-mask-color);
}
.publish[open] {
  animation: pub-in 0.16s ease;
}
@keyframes pub-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

.pub-shell {
  display: flex;
  flex-direction: column;
}
.pub-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px 12px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}
.pub-head h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 700;
}

.pub-body {
  padding: 16px 20px 20px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  max-height: min(72dvh, 640px);
  overflow-y: auto;
}

.pub-agent {
  display: flex;
  align-items: center;
  gap: 10px;
}
.pub-agent-name {
  font-size: 14.5px;
  font-weight: 650;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pub-status {
  font-size: 11.5px;
  font-weight: 600;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
  border-radius: 999px;
  padding: 2px 9px;
  flex: none;
}

.pub-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pub-label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.pub-field {
  display: flex;
  gap: 8px;
}

.input {
  width: 100%;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  padding: 9px 12px;
  font: inherit;
  font-size: 12.8px;
  color: var(--el-text-color-regular);
  box-sizing: border-box;
}
.input:focus {
  outline: 0;
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
textarea.code {
  font-family: var(--pg-mono);
  font-size: 12px;
  line-height: 1.6;
  resize: vertical;
}

.pub-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 8px 14px;
  font: inherit;
  font-size: 12.5px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  text-decoration: none;
  transition: background var(--pg-transition), border-color var(--pg-transition);
}
.btn.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.btn.solid:hover {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.btn.soft {
  background: var(--el-fill-color-light);
  border-color: var(--el-border-color-light);
  color: var(--el-text-color-primary);
}
.btn.soft:hover {
  background: var(--el-fill-color);
}

.pub-error {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  border-radius: 8px;
  padding: 9px 12px;
}

.pub-note {
  margin: 0;
  display: flex;
  gap: 8px;
  align-items: flex-start;
  font-size: 12px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-extra-light);
  border-radius: 8px;
  padding: 10px 12px;
}
.note-ico {
  flex: none;
  margin-top: 2px;
  color: var(--el-text-color-placeholder);
}

.iconbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.iconbtn:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
</style>
