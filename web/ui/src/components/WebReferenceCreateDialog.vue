<script setup lang="ts">
import { computed, ref, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { elementComposerPart, type ComposerElementPart } from "@/agent/composerParts";
import { parsePickedCached, type ElementPick } from "@/agent/elementPicker";
import { nativeViewedTab, type NativeBrowserState } from "@/agent/nativeBrowser";
import { cloneWebReference, webReferenceURL } from "@/agent/webReference";
import NativeBrowserPane from "./NativeBrowserPane.vue";
import ElementCard from "./ElementCard.vue";
import ElementPageIcon from "./ElementPageIcon.vue";
import Icon from "./ShortcutEditorIcon.vue";

// Its owner creates a disposable authoring Page. Rendering, human input and
// picking are the same as in a conversation; only the insertion destination
// differs. This component cannot navigate the existing conversation's Page.
const props = withDefaults(defineProps<{
  modelValue: boolean; disabled?: boolean; sessionId?: string;
  observedReferences?: ComposerElementPart[]; error?: string;
  browserSessionId?: string; browserState?: NativeBrowserState | null;
  browserLoading?: boolean; browserError?: string;
}>(), {
  disabled: false, sessionId: "", observedReferences: () => [], error: "",
  browserSessionId: "", browserState: null, browserLoading: false, browserError: "",
});
const emit = defineEmits<{
  "update:modelValue": [value: boolean]; create: [part: ComposerElementPart];
  state: [state: NativeBrowserState]; recover: []; retry: []; switchTab: [tabId: string];
}>();
const { t } = useI18n();
const selected = ref<ComposerElementPart>();
const showExisting = ref(false), selectionError = ref("");
const existingReferences = computed(() => props.observedReferences.filter(value => !!cloneWebReference(value)));
const selectedCard = computed(() => {
  if (!selected.value) return;
  const card = parsePickedCached(selected.value.text).cards[0];
  return card && { ...card, ...(selected.value.favicon ? { favicon: selected.value.favicon } : {}) };
});
const pageBinding = computed(() => {
  const state = props.browserState;
  if (!props.browserSessionId || !state || !/^ctx_[a-f0-9]{24}$/.test(state.context_ref || "")) return;
  const tabId = nativeViewedTab(state);
  if (!/^t[1-9]\d*$/.test(tabId) || !state.tabs.some(tab => tab.id === tabId)) return;
  return { key: `${props.browserSessionId}:${state.context_ref}:${tabId}`, session: props.browserSessionId,
    target: { context_ref: state.context_ref!, tab_id: tabId }, state };
});
// Each event listener captures a keyed binding, rejecting a delayed event
// from a retired Page instead of trusting whichever Page is now visible.
const panes = shallowRef<NonNullable<typeof pageBinding.value>[]>([]);
watch(() => [props.modelValue, props.browserSessionId, pageBinding.value] as const, ([open, session, page]) => {
  if (!open || !session) { panes.value = []; return; }
  const retained = panes.value.filter(pane => pane.session === session
    && (!page || pane.target.context_ref === page.target.context_ref && page.state.tabs.some(tab => tab.id === pane.target.tab_id)));
  if (page) {
    const index = retained.findIndex(pane => pane.key === page.key);
    if (index >= 0) retained[index] = page; else retained.push(page);
  }
  panes.value = retained;
}, { immediate: true });
const active = computed(() => props.modelValue && !props.disabled && !props.browserLoading && !props.browserError);
const showStartup = computed(() => !pageBinding.value || !!props.browserError);
watch(() => [props.modelValue, props.sessionId, props.browserSessionId] as const, (next, before) => {
  if (!before || next[0] !== before[0] || next[1] !== before[1] || next[2] !== before[2]) {
    selected.value = undefined; selectionError.value = ""; showExisting.value = false;
  }
}, { immediate: true });
function acceptPick(pick: ElementPick, bindingKey: string) {
  if (!active.value || bindingKey !== pageBinding.value?.key) return;
  const part = cloneWebReference(elementComposerPart(pick));
  const card = part && parsePickedCached(part.text).cards[0];
  // NativeBrowserPane commits against the exact displayed frame and freezes
  // the icon. Keep that snapshot unchanged, with no manually rewritten quote.
  if (!part || !card || !pick.pageURL || webReferenceURL(pick.pageURL) !== webReferenceURL(card.url)
    || !pick.key.startsWith("native:") || !part.text.includes("reference_kind: read-only observation;")) {
    selectionError.value = t("webReference.invalidSelection"); return;
  }
  selected.value = part; selectionError.value = "";
}
function updateState(state: NativeBrowserState, bindingKey: string) {
  const page = pageBinding.value;
  if (!props.modelValue || !page || bindingKey !== page.key || state.context_ref !== page.target.context_ref
    || nativeViewedTab(state) !== page.target.tab_id) return;
  emit("state", state);
}
function chooseExisting(id: string) {
  if (!props.modelValue || props.disabled) return;
  const original = props.observedReferences.find(value => value.id === id), part = original && cloneWebReference(original);
  if (!part) return;
  selected.value = part; selectionError.value = ""; showExisting.value = false;
}
function switchTab(tabId: string) {
  if (!active.value || !props.browserState?.tabs.some(tab => tab.id === tabId) || tabId === pageBinding.value?.target.tab_id) return;
  emit("switchTab", tabId);
}
function retry() { if (props.modelValue && !props.disabled && !props.browserLoading) emit("retry"); }
function recover(bindingKey: string) { if (props.modelValue && !props.disabled && bindingKey === pageBinding.value?.key) emit("recover"); }
function close() { if (!props.disabled) emit("update:modelValue", false); }
function clearSelection() { if (!props.disabled) { selected.value = undefined; selectionError.value = ""; } }
function create() {
  if (!props.modelValue || props.disabled || !selected.value) return;
  const snapshot = cloneWebReference(selected.value);
  if (snapshot) emit("create", snapshot);
  // Only the parent closes after a successful insertion. Aggregate limits or
  // a changed draft leave the captured element and editor contents intact.
}
</script>

<template>
  <el-dialog :model-value="modelValue" :title="t('webReference.title')" fullscreen append-to-body destroy-on-close class="web-reference-create-dialog" :close-on-click-modal="false" :close-on-press-escape="false" :show-close="!disabled" @update:model-value="!$event && close()">
    <template #header>
      <div class="web-reference-heading">
        <div class="web-reference-heading-copy"><strong class="web-reference-heading-label">{{ t('webReference.title') }}</strong><p class="web-reference-subtitle">{{ t('webReference.subtitle') }}</p></div>
        <button v-if="existingReferences.length" type="button" class="web-reference-reuse" :disabled="disabled" :aria-expanded="showExisting" data-testid="web-reference-reuse" @click="showExisting = !showExisting"><Icon icon="lucide:history" />{{ t('webReference.existing', { count: existingReferences.length }) }}<Icon :icon="showExisting ? 'lucide:chevron-up' : 'lucide:chevron-down'" /></button>
      </div>
    </template>
    <div v-if="showExisting && existingReferences.length" class="web-existing-list">
      <button v-for="part in existingReferences" :key="part.id" type="button" :disabled="disabled" :data-testid="`web-reference-existing-${part.id}`" @click="chooseExisting(part.id)"><ElementPageIcon :url="part.pick?.pageURL || ''" :icon="part.favicon" /><span><strong>{{ part.title || part.label }}</strong><small>{{ part.label }}</small></span><Icon icon="lucide:plus" /></button>
    </div>
    <section class="web-authoring-browser" :class="{ loading: browserLoading }" data-testid="web-reference-browser" :aria-label="t('webReference.browser')" :aria-busy="browserLoading">
      <div class="web-browser-topline">
        <div class="web-browser-tabs" role="tablist" :aria-label="t('webReference.tabs')">
          <button v-for="tab in browserState?.tabs || []" :key="tab.id" type="button" role="tab" :aria-selected="tab.id === pageBinding?.target.tab_id" :disabled="disabled || browserLoading || !!browserError" :title="tab.url === 'about:blank' ? t('webReference.newTab') : tab.title || tab.url" :data-testid="`web-reference-tab-${tab.id}`" @click="switchTab(tab.id)"><ElementPageIcon :url="tab.url" :icon="tab.favicon" /><span>{{ tab.url === 'about:blank' ? t('webReference.newTab') : tab.title || tab.url }}</span></button>
          <span v-if="!browserState?.tabs.length" class="web-browser-caption"><Icon icon="lucide:globe" />{{ t('webReference.browser') }}</span>
        </div>
        <span class="web-browser-isolation"><Icon icon="lucide:copy-check" />{{ t('webReference.isolated') }}</span>
      </div>
      <div class="web-browser-surface">
        <NativeBrowserPane v-for="pane in panes" v-show="!showStartup && pane.key === pageBinding?.key" :key="pane.key" :session-id="pane.session" :active="active && pane.key === pageBinding?.key" :target="pane.target" :initial-state="pane.state" @pick="acceptPick($event, pane.key)" @state="updateState($event, pane.key)" @recover="recover(pane.key)" />
        <div v-if="showStartup" class="web-browser-startup" :class="{ failed: !!browserError }" :role="browserError ? 'alert' : 'status'" data-testid="web-reference-loading">
          <span v-if="browserLoading" class="web-browser-spinner" aria-hidden="true" /><Icon v-else :icon="browserError ? 'lucide:wifi-off' : 'lucide:globe'" />
          <strong>{{ t(browserError ? 'webReference.connectionFailed' : 'webReference.starting') }}</strong>
          <p>{{ browserError || t('webReference.startingHint') }}</p>
          <button v-if="browserError || !browserLoading" type="button" class="web-reference-retry" :disabled="disabled || browserLoading" data-testid="web-reference-retry" @click="retry"><Icon icon="lucide:refresh-cw" />{{ t('webReference.retry') }}</button>
        </div>
        <div v-else-if="browserLoading" class="web-browser-rebinding" role="status"><span class="web-browser-spinner" aria-hidden="true" />{{ t('webReference.connecting') }}</div>
      </div>
    </section>
    <p v-if="props.error || selectionError" class="web-reference-error" role="alert">{{ props.error || selectionError }}</p>
    <template #footer>
      <div class="web-reference-bottom">
        <div v-if="selected && selectedCard" class="web-reference-selected" data-testid="web-reference-selection" role="status">
          <div class="web-selection-label"><Icon icon="lucide:circle-check" /><strong>{{ t('webReference.selected') }}</strong></div>
          <ElementCard :key="selected.id" :card="selectedCard" />
          <button type="button" class="web-selection-clear" :disabled="disabled" :aria-label="t('webReference.clear')" :title="t('webReference.clear')" data-testid="web-reference-clear" @click="clearSelection"><Icon icon="lucide:x" /></button>
        </div>
        <ol v-else class="web-reference-steps" :aria-label="t('webReference.steps')"><li><span>1</span>{{ t('webReference.stepOpen') }}</li><li><span>2</span><Icon icon="lucide:mouse-pointer-2" />{{ t('webReference.stepPick') }}</li><li><span>3</span>{{ t('webReference.stepInsert') }}</li></ol>
      </div>
      <div class="web-reference-actions"><el-button :disabled="disabled" @click="close">{{ t('webReference.cancelled') }}</el-button><el-button type="primary" :disabled="disabled || !selected" data-testid="web-reference-create" :title="t('webReference.previewHint')" @click="create"><Icon icon="lucide:plus" />{{ t('webReference.create') }}</el-button></div>
    </template>
  </el-dialog>
</template>

<style scoped>
.web-reference-heading{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:14px}.web-reference-subtitle{margin:0;color:var(--el-text-color-secondary);font-size:13px;line-height:1.7}.web-reference-heading svg,.web-reference-bottom svg,.web-reference-save-hint svg,.web-browser-topline svg{width:15px;height:15px;flex:none}.web-reference-reuse{display:flex;align-items:center;gap:5px;padding:4px 0;background:transparent;border:0;color:var(--el-text-color-secondary);font:inherit;font-size:12px;cursor:pointer;white-space:nowrap}.web-reference-reuse:hover:not(:disabled){color:var(--el-color-primary)}.web-existing-list{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:8px;max-height:132px;overflow:auto;margin-bottom:14px}.web-existing-list>button{display:flex;gap:9px;align-items:center;text-align:left;background:var(--el-fill-color-lighter);border:1px solid var(--el-border-color-lighter);border-radius:8px;padding:10px;color:var(--el-text-color-primary);cursor:pointer}.web-existing-list>button:hover:not(:disabled){border-color:var(--pg-accent-line)}.web-existing-list>button>span{display:flex;flex:1;min-width:0;flex-direction:column;gap:4px}.web-existing-list strong,.web-existing-list small{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.web-existing-list strong{font-size:12px;font-weight:500}.web-existing-list small{font-size:11px;color:var(--el-text-color-secondary)}.web-existing-list svg{width:14px;height:14px;flex:none}
.web-authoring-browser{display:flex;flex-direction:column;border:1px solid var(--el-border-color);border-radius:12px;overflow:hidden;min-height:0;flex:1;background:var(--el-bg-color);box-shadow:0 3px 15px color-mix(in srgb,var(--el-text-color-primary) 3%,transparent)}.web-browser-topline{display:flex;align-items:center;justify-content:space-between;gap:12px;background:var(--el-fill-color-light);border-bottom:1px solid var(--el-border-color-lighter);padding:7px 10px 0;min-height:36px}.web-browser-tabs{display:flex;align-self:stretch;min-width:0;gap:4px;overflow:auto;scrollbar-width:thin;overscroll-behavior:contain}.web-browser-tabs button{display:flex;align-items:center;gap:7px;max-width:240px;min-width:110px;padding:8px 12px;border:1px solid transparent;border-bottom:0;border-radius:8px 8px 0 0;font:inherit;font-size:12px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.web-browser-tabs button[aria-selected=true]{background:var(--el-bg-color);color:var(--el-text-color-primary);border-color:var(--el-border-color-lighter)}.web-browser-tabs button:hover:not(:disabled):not([aria-selected=true]){background:var(--el-fill-color)}.web-browser-tabs button>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.web-browser-caption{display:flex;align-items:center;gap:7px;font-size:12px;color:var(--el-text-color-secondary);padding:0 8px 7px}.web-browser-isolation{display:flex;align-items:center;gap:5px;flex:none;color:var(--el-text-color-placeholder);font-size:11px;padding-bottom:7px}.web-browser-isolation svg{width:13px;height:13px}.web-browser-surface{display:flex;position:relative;flex:1;min-height:0;min-width:0}.web-browser-surface :deep(.native-browser){flex:1;width:100%;min-width:0;min-height:0}.web-browser-surface :deep(.native-page){min-height:0}.web-browser-startup{display:flex;flex:1;flex-direction:column;align-items:center;justify-content:center;gap:12px;text-align:center;padding:32px;color:var(--el-text-color-secondary)}.web-browser-startup>svg{width:32px;height:32px;color:var(--el-text-color-placeholder)}.web-browser-startup strong{font-size:15px;font-weight:500;color:var(--el-text-color-primary)}.web-browser-startup p{font-size:12px;line-height:1.7;margin:0;max-width:560px;overflow-wrap:anywhere}.web-browser-startup.failed>svg{color:var(--el-color-warning)}.web-reference-retry{display:flex;gap:6px;align-items:center;font:inherit;font-size:12px;background:var(--el-bg-color);color:var(--el-text-color-primary);border:1px solid var(--el-border-color);border-radius:7px;padding:8px 12px;cursor:pointer;margin-top:4px}.web-reference-retry svg{width:14px;height:14px}.web-browser-spinner{display:block;width:26px;height:26px;box-sizing:border-box;border:2px solid var(--el-border-color);border-top-color:var(--el-color-primary);border-radius:50%;animation:web-browser-spin .9s linear infinite}.web-browser-rebinding{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;gap:10px;background:color-mix(in srgb,var(--el-bg-color) 88%,transparent);font-size:12px;color:var(--el-text-color-secondary);z-index:4}.web-browser-rebinding .web-browser-spinner{width:18px;height:18px}
.web-reference-bottom{min-height:42px;margin-top:13px}.web-reference-steps{display:flex;justify-content:center;align-items:center;gap:28px;list-style:none;margin:0;padding:10px 8px;color:var(--el-text-color-secondary);font-size:12px;line-height:1.6}.web-reference-steps li{display:flex;align-items:center;gap:7px}.web-reference-steps li>span{display:flex;align-items:center;justify-content:center;flex:none;width:19px;height:19px;border-radius:50%;background:var(--el-fill-color);color:var(--el-text-color-secondary);font-size:10px}.web-reference-steps li>svg{width:14px;height:14px}.web-selection-label{display:flex;align-items:center;gap:6px;font-size:11px;color:var(--el-text-color-secondary);line-height:1.7}.web-selection-label>svg{color:var(--el-color-success);width:14px;height:14px}.web-selection-label strong{font-weight:500;color:var(--el-text-color-primary)}.web-selection-label>span{margin-left:8px}.web-selection-label>button{display:flex;align-items:center;justify-content:center;margin-left:auto;flex:none;border:0;background:transparent;color:var(--el-text-color-secondary);width:24px;height:24px;border-radius:5px;cursor:pointer}.web-selection-label>button:hover:not(:disabled){background:var(--el-fill-color)}.web-reference-selected :deep(.elcard){margin-top:5px}.web-reference-selected :deep(.elcard-body){max-height:160px;overflow:auto}.web-reference-error{margin:8px 0 0;padding:8px 12px;background:var(--el-color-danger-light-9);color:var(--el-color-danger);font-size:12px;line-height:1.7;border-radius:8px}.web-reference-save-hint{display:flex;align-items:center;gap:7px;color:var(--el-text-color-placeholder);font-size:11px;line-height:1.6}.web-reference-actions{display:flex;align-items:center;gap:10px}.web-reference-actions :deep(.el-button+.el-button){margin-left:0}.web-reference-actions svg{width:15px;height:15px;margin-right:5px}button:disabled{opacity:.5;cursor:default}@keyframes web-browser-spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.web-browser-spinner{animation:none;border-right-color:var(--el-color-primary)}}
@media(max-width:760px){.web-reference-heading{align-items:flex-start;gap:7px;flex-direction:column;margin-bottom:10px}.web-reference-subtitle{font-size:12px}.web-browser-topline{gap:4px;padding:5px 6px 0}.web-browser-isolation{font-size:0;gap:0}.web-browser-isolation svg{width:14px;height:14px}.web-browser-tabs button{max-width:180px;min-width:100px;padding:7px 9px}.web-authoring-browser{border-radius:9px}.web-reference-steps{gap:9px;align-items:flex-start;padding:5px 0;font-size:10px}.web-reference-steps li{gap:4px}.web-reference-steps li>span{width:16px;height:16px}.web-reference-steps li>svg{display:none}.web-selection-label>span{display:none}.web-reference-save-hint{display:none}.web-reference-actions{margin-left:auto}.web-existing-list{grid-template-columns:1fr;max-height:115px}.web-reference-selected :deep(.elcard-head){font-size:11px}.web-browser-surface :deep(.native-toolbar){flex-wrap:wrap;height:auto;min-height:74px;flex:0 0 auto}.web-browser-surface :deep(.native-location){order:-1;flex-basis:100%}}

/* Keep the page at immersive size. NativeBrowserPane already observes this
   canvas and resizes its remote viewport without remounting or navigation. */
.web-reference-heading{margin:0;min-width:0;padding-right:30px;gap:18px}
.web-reference-heading-copy{display:flex;align-items:baseline;gap:18px;min-width:0}
.web-reference-heading-label{font-size:15px;font-weight:600;white-space:nowrap;color:var(--el-text-color-primary)}
.web-reference-subtitle{font-size:12px;line-height:1.6}
.web-existing-list{position:absolute;z-index:8;top:8px;right:12px;width:min(680px,calc(100% - 24px));max-height:190px;margin:0;padding:12px;border:1px solid var(--el-border-color);border-radius:10px;background:var(--el-bg-color);box-shadow:var(--el-box-shadow-light)}
.web-authoring-browser{min-width:0;border-radius:10px;box-shadow:none}
.web-browser-topline{flex:none;min-height:32px;padding:4px 8px 0}
.web-browser-tabs button{padding:7px 10px}
.web-reference-bottom{flex:1;min-width:0;min-height:0;margin:0;text-align:left}
.web-reference-steps{justify-content:flex-start;gap:22px;padding:0;font-size:11px}
.web-reference-selected{display:flex;align-items:center;gap:10px;min-width:0}
.web-selection-label{flex:none}
.web-selection-clear{display:flex;align-items:center;justify-content:center;flex:none;border:0;background:transparent;color:var(--el-text-color-secondary);width:28px;height:28px;border-radius:6px;cursor:pointer}
.web-selection-clear:hover:not(:disabled){background:var(--el-fill-color)}
.web-selection-clear svg{width:15px;height:15px}
.web-reference-selected :deep(.elcard){position:relative;flex:1;min-width:0;margin-top:0;overflow:visible}
.web-reference-selected :deep(.elcard-head){min-height:32px;border-radius:7px}
/* Expanding a captured card overlays the page instead of changing its
   viewport and invalidating the coordinates of an active element picker. */
.web-reference-selected :deep(.elcard-body){position:absolute;left:-1px;right:-1px;bottom:calc(100% - 1px);z-index:6;max-height:min(38dvh,280px);overflow:auto;padding:12px;background:var(--el-bg-color);border:1px solid var(--el-border-color);border-radius:9px 9px 0 0;box-shadow:var(--el-box-shadow-light)}
.web-reference-error{flex:none;max-height:70px;overflow:auto;margin:6px 0 0;padding:6px 12px}
.web-reference-actions{flex:none;gap:8px}
@media(max-width:1050px){.web-reference-heading-copy{gap:10px}.web-reference-subtitle{font-size:11px}.web-reference-steps{gap:10px}.web-selection-label strong{display:none}}
@media(max-width:760px){.web-reference-heading{flex-direction:row;align-items:flex-start;gap:8px;margin:0}.web-reference-heading-copy{flex-direction:column;gap:3px}.web-reference-heading-label{font-size:14px}.web-reference-subtitle{font-size:10px}.web-reference-reuse{font-size:0;gap:3px}.web-reference-reuse svg{width:17px;height:17px}.web-browser-topline{padding:3px 6px 0}.web-browser-tabs button{padding:6px 9px}.web-reference-bottom{flex-basis:100%}.web-reference-steps{justify-content:center;gap:10px;font-size:10px}.web-existing-list{grid-template-columns:1fr;max-height:160px;right:6px;width:calc(100% - 12px)}.web-reference-selected{gap:5px}}
@media(max-height:600px) and (min-width:761px){.web-reference-subtitle{display:none}.web-browser-topline{min-height:28px}.web-browser-tabs button{padding-top:5px;padding-bottom:5px}.web-browser-surface :deep(.native-footer){display:none}}
</style>
<style>
.web-reference-create-dialog{display:flex;flex-direction:column;box-sizing:border-box;height:100dvh;max-height:100dvh;margin:0;overflow:hidden;border-radius:0;--el-dialog-padding-primary:12px}
.web-reference-create-dialog .el-dialog__header{flex:none;margin:0;padding:2px 0 10px}
.web-reference-create-dialog .el-dialog__headerbtn{top:0;right:0;height:42px;width:42px}
.web-reference-create-dialog .el-dialog__body{position:relative;display:flex;flex:1;flex-direction:column;min-height:0;min-width:0;padding:0;overflow:visible}
.web-reference-create-dialog .el-dialog__footer{display:flex;flex:none;align-items:center;justify-content:space-between;gap:16px;border-top:1px solid var(--el-border-color-lighter);margin-top:8px;padding:8px 0 0}
.web-reference-create-dialog .el-dialog__footer .el-button{height:34px;border-radius:7px}
@media(max-width:760px){.web-reference-create-dialog{--el-dialog-padding-primary:8px}.web-reference-create-dialog .el-dialog__header{padding-bottom:7px}.web-reference-create-dialog .el-dialog__footer{flex-wrap:wrap;gap:7px;margin-top:6px;padding-top:7px}.web-reference-create-dialog .el-dialog__footer .el-button{height:32px}}
@media(max-height:600px) and (min-width:761px){.web-reference-create-dialog{--el-dialog-padding-primary:8px}.web-reference-create-dialog .el-dialog__header{padding-bottom:6px}.web-reference-create-dialog .el-dialog__footer{margin-top:5px;padding-top:5px}}
</style>
