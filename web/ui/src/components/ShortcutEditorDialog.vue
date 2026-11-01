<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, ref, useId, watch } from "vue";
import Icon from "./ShortcutEditorIcon.vue";
import { useI18n } from "vue-i18n";
import type { AttachmentMeta, MessageInputPart, OfficeReferenceSnapshot, SkillInfo } from "@/api/types";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import { shortcutDraftSourceKey, type ShortcutDraftSource } from "@/agent/shortcutDraftSource";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { composerInputParts, snapshotComposerElementIcons } from "@/agent/orderedInput";
import { composerOccurrenceID, composerTypedText, fileComposerPart, officeComposerPart, replaceComposerTypedText, shortcutComposerPart, skillComposerPart, type ComposerPart, type ComposerReferencePart, type ComposerElementPart, type ComposerSelection } from "@/agent/composerParts";
import { informationComposerParts } from "@/agent/informationComposer";
import { cloneInputParts, validateInputParts } from "@/agent/shortcutStructure";
import { shortcutLanguage } from "./shortcutPresentation";
import InlineComposer from "./InlineComposer.vue";
import ShortcutContent from "./ShortcutContent.vue";
import ShortcutLanguageSelect from "./ShortcutLanguageSelect.vue";
import StarterIconPicker from "./studio/StarterIconPicker.vue";
import OfficeReferenceDialog from "./OfficeReferenceDialog.vue";
import ShortcutReferenceDialog from "./ShortcutReferenceDialog.vue";
import ConfirmDialog from "./ConfirmDialog.vue";
import ShortcutSourcesPanel from "./ShortcutSourcesPanel.vue";
import WebReferenceCreateDialog from "./WebReferenceCreateDialog.vue";
import InformationOfficeReferenceDialog from "./InformationOfficeReferenceDialog.vue";
import { useWebReferenceBrowser } from "@/agent/useWebReferenceBrowser";
import { replaceShortcutOccurrence, shortcutOccurrenceEdit, shortcutSnapshotFromParts, validateShortcutSnapshot, type ShortcutOccurrenceEdit } from "@/agent/shortcutOccurrenceEdit";

const props = withDefaults(defineProps<{
  purpose?: "shortcut" | "schedule"; externalDirty?: boolean;
  modelValue: boolean; item: ShortcutSnapshot; existing?: boolean; library?: ShortcutSnapshot[];
  referenceChoices?: { id: string; title: string }[]; resolveReference?: (id: string) => Promise<ShortcutSnapshot>;
  skills?: SkillInfo[]; sessionId?: string | null; scopeKey: string; disabled?: boolean;
  save: (item: ShortcutSnapshot) => Promise<void>;
  prepareParts?: (parts: MessageInputPart[]) => Promise<MessageInputPart[]>;
  uploadFile?: (file: File) => Promise<AttachmentMeta>;
  pickInformation?: () => Promise<ComposerPart[] | null>;
  /** Internal recursive editor: apply only to the enclosing draft occurrence. */
  snapshotOnly?: boolean; ancestorIds?: string[];
  validateSnapshot?: (item: ShortcutSnapshot) => string;
}>(), { purpose: "shortcut", externalDirty: false, existing: false, library: () => [], skills: () => [], sessionId: "", disabled: false, snapshotOnly: false, ancestorIds: () => [] });
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useI18n();
const uid = `shortcut-editor-${useId()}`;
const draft = ref<ShortcutSnapshot>({ id: "", title: "", content: "", icon: "" });
const draftParts = ref<ComposerPart[]>([]);
const draftEditor = ref<InstanceType<typeof InlineComposer> | null>(null);
const saving = ref(false), uploading = ref(false), saveError = ref("");
const pickingInformation = ref(false);
const contentMode = ref<"edit" | "preview">("edit");
const sourcesOpen = ref(true);
const sourceToggle = ref<HTMLButtonElement | null>(null);
const sourceShell = ref<HTMLElement | null>(null);
const webCreateOpen = ref(false), officeCreateOpen = ref(false), createReferenceError = ref("");
// Start on first use, then retain this draft's owned Page while collecting
// several references. Closing the maker pauses its media/input through
// modelValue; only ending this editor or changing scope retires the resource.
const webBrowserStarted = ref(false);
let creationOrigin: { id: string; scope: string; epoch: number; before: string; selection?: { from: number; to: number } } | null = null;
const sourceSnapshot = ref<ShortcutDraftSource | null>(null);
const sourceReferences = computed(() => sourceSnapshot.value?.parts.filter((part): part is ComposerReferencePart => part.type !== "text") || []);
const editingScope = ref(""), editingSession = ref(""), initial = ref("");
let disposed = false, editorEpoch = 0;
const readSourceDraft = inject(shortcutDraftSourceKey, null);
const nestedPreview = ref<ShortcutSnapshot | null>(null), officePreview = ref<OfficeReferenceSnapshot | null>(null);
interface NestedEditOrigin { parent: string; scope: string; session: string; epoch: number; selection?: ComposerSelection; before: string; occurrence?: ShortcutOccurrenceEdit }
const nestedDraft = ref<ShortcutSnapshot | null>(null), nestedNew = ref(false);
const nestedEditor = ref<{ confirmLeave: () => Promise<boolean>; busy: boolean } | null>(null);
let nestedOrigin: NestedEditOrigin | null = null, previewOrigin: NestedEditOrigin | null = null;
const ancestorChain = computed(() => [...(props.ancestorIds || []), draft.value.id]);
const canAddNested = computed(() => ancestorChain.value.length < 5);
const leaveConfirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
const editScopeCurrent = computed(() => editingScope.value === props.scopeKey && editingSession.value === (props.sessionId || ""));
const references = computed(() => props.referenceChoices || props.library);
const structured = computed(() => draftParts.value.some(part => part.type !== "text"));
const draftText = computed({ get: () => composerTypedText(draftParts.value), set: text => { draftParts.value = replaceComposerTypedText(draftParts.value, text); } });
const draftLanguage = computed({ get: () => shortcutLanguage(draft.value.language), set: language => { draft.value.language = shortcutLanguage(language); } });
function wireDraft(parts = draftParts.value) { return composerInputParts(parts, key => { const file = parts.find(part => part.type === "file" && part.key === key); if (!file || file.type !== "file" || !file.path || file.status !== "ready") throw new Error(t("nestedShortcuts.fileNotReady")); return [file.path]; }); }
function draftSnapshot(parts = draftParts.value) { return shortcutSnapshotFromParts(draft.value, parts, wireDraft(parts)); }
function validateDraftParts(parts: ComposerPart[]) { const item = draftSnapshot(parts); return validateShortcutSnapshot(item, props.ancestorIds) || props.validateSnapshot?.(item) || ""; }
function validationMessage(code: string) { return t(code === "scopeChanged" ? "shortcuts.scopeChanged" : `nestedShortcuts.${code}`); }
const draftPreviewParts = computed(() => { try { return wireDraft(); } catch { return []; } });
const structureError = computed(() => { try { return validateDraftParts(draftParts.value); } catch { return "fileNotReady"; } });
const contentBytes = computed(() => { try { return new TextEncoder().encode(structured.value ? JSON.stringify(wireDraft()) : draftText.value).length; } catch { return 0; } });
const formError = computed(() => {
  if (!draft.value.title.trim()) return t("shortcuts.titleRequired");
  if ([...draft.value.title.trim()].length > 120) return t("shortcuts.titleLong");
  if (!draftParts.value.some(part => part.type !== "text" || part.text.trim())) return t("shortcuts.contentRequired");
  if (!structured.value && new TextEncoder().encode(draftText.value).length > 65536) return t("shortcuts.contentLong");
  return structureError.value ? validationMessage(structureError.value) : "";
});
const state = () => JSON.stringify([draft.value, draftParts.value]);
const dirty = computed(() => props.modelValue && (props.externalDirty || state() !== initial.value));
watch(() => props.modelValue, open => {
  editorEpoch++;
  nestedDraft.value = null; nestedPreview.value = null; nestedOrigin = previewOrigin = null;
  webBrowserStarted.value = false;
  webCreateOpen.value = false; officeCreateOpen.value = false; creationOrigin = null; createReferenceError.value = "";
  if (!open) return;
  saving.value = false; uploading.value = false; pickingInformation.value = false;
  editingScope.value = props.scopeKey; editingSession.value = props.sessionId || "";
  draft.value = JSON.parse(JSON.stringify(props.item));
  draft.value.icon ||= "";
  draftParts.value = draft.value.parts?.length ? informationComposerParts(draft.value.parts, []) : [{ type: "text", text: draft.value.content }];
  contentMode.value = "edit"; sourcesOpen.value = true; saveError.value = ""; initial.value = state(); refreshSources();
}, { immediate: true });
function errorText(error: unknown) { return error instanceof Error ? error.message : String(error); }
function current(id: string, scope: string) { return !disposed && props.modelValue && draft.value.id === id && props.scopeKey === scope && editScopeCurrent.value; }
function refreshSources() {
  const source = readSourceDraft?.();
  sourceSnapshot.value = source && source.sessionId === editingSession.value ? { ...source, parts: JSON.parse(JSON.stringify(source.parts)) } : null;
}
function insertParts(parts: ComposerPart[], selection = draftEditor.value?.getSelection()) {
  const candidate = [...draftParts.value, ...parts];
  try {
    const error = validateDraftParts(candidate);
    if (error) throw new Error(validationMessage(error));
  } catch (error) { saveError.value = errorText(error); return false; }
  saveError.value = "";
  contentMode.value = "edit";
  if (draftEditor.value) {
    if (selection) draftEditor.value.setSelection(selection);
    draftEditor.value.insertParts(JSON.parse(JSON.stringify(parts)));
  } else draftParts.value = candidate;
  return true;
}
function insertReference(part: ComposerReferencePart) {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  insertParts([part]);
}
function setSourcesOpen(open: boolean) {
  const restoreFocus = !open && typeof document !== "undefined" && !!sourceShell.value?.contains(document.activeElement);
  sourcesOpen.value = open;
  if (restoreFocus) sourceToggle.value?.focus({ preventScroll: true });
}
function openReferenceCreator(kind: "web" | "office") {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  creationOrigin = { id: draft.value.id, scope: props.scopeKey, epoch: editorEpoch, before: state(), selection: draftEditor.value?.getSelection() };
  createReferenceError.value = "";
  if (kind === "web") { webBrowserStarted.value = true; webCreateOpen.value = true; }
  else officeCreateOpen.value = true;
}
function acceptCreatedReference(part: ComposerReferencePart, kind: "web" | "office") {
  const origin = creationOrigin;
  const creatorOpen = kind === "web" ? webCreateOpen.value : officeCreateOpen.value;
  if (!creatorOpen || part.type !== (kind === "web" ? "element" : "office")) return;
  if (!origin || props.disabled || saving.value || uploading.value || origin.epoch !== editorEpoch || !current(origin.id, origin.scope)) { createReferenceError.value = t("shortcuts.scopeChanged"); return; }
  if (origin.before !== state()) { createReferenceError.value = t("nestedShortcuts.changedDraft"); return; }
  if (!insertParts([part], origin.selection)) { createReferenceError.value = saveError.value; return; }
  if (kind === "web") webCreateOpen.value = false;
  else officeCreateOpen.value = false;
  creationOrigin = null; createReferenceError.value = "";
}
const reusableWebReferences = computed(() => sourceReferences.value.filter((part): part is ComposerElementPart => part.type === "element"));
const webBrowser = useWebReferenceBrowser({
  active: () => props.modelValue && webBrowserStarted.value && editScopeCurrent.value,
  sourceSession: () => props.sessionId,
  scope: () => JSON.stringify([props.scopeKey, props.sessionId, draft.value.id]),
  errorText,
});
const { sessionId: webBrowserSession, state: webBrowserState, loading: webBrowserLoading, error: webBrowserError } = webBrowser;
watch(() => [props.scopeKey, props.sessionId], () => { webBrowserStarted.value = false; webCreateOpen.value = false; officeCreateOpen.value = false; creationOrigin = null; nestedDraft.value = null; nestedPreview.value = null; nestedOrigin = previewOrigin = null; });

async function chooseNested(id: string) {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value || id === draft.value.id) return;
  const scope = props.scopeKey, draftID = draft.value.id, epoch = editorEpoch;
  const before = state(), selection = draftEditor.value?.getSelection();
  uploading.value = true; saveError.value = "";
  try {
    const item = props.library.find(entry => entry.id === id) || await props.resolveReference?.(id);
    if (!item || epoch !== editorEpoch || !current(draftID, scope) || props.disabled) return;
    if (before !== state()) throw new Error(t("nestedShortcuts.changedDraft"));
    insertParts([shortcutComposerPart(item)], selection);
  } catch (error) { if (epoch === editorEpoch && current(draftID, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) uploading.value = false; }
}
async function insertSourceReference(id: string) {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  const source = sourceSnapshot.value;
  const index = source?.parts.findIndex(part => part.type !== "text" && part.id === id) ?? -1;
  if (!source || index < 0 || source.sessionId !== editingSession.value) return;
  const original = source.parts[index];
  if (original.type === "text") return;
  const draftID = draft.value.id, scope = props.scopeKey, epoch = editorEpoch;
  const before = state(), selection = draftEditor.value?.getSelection();
  uploading.value = true; saveError.value = "";
  try {
    let part: ComposerPart = original;
    if (original.type === "file" && (!original.path || original.status !== "ready")) {
      if (!source.prepare) throw new Error(t("nestedShortcuts.fileNotReady"));
      const prepared = await source.prepare();
      const match = prepared.find(candidate => candidate.type !== "text" && candidate.id === id);
      if (!match || match.type !== original.type) throw new Error(t("nestedShortcuts.fileNotReady"));
      part = match;
    }
    if (props.disabled || epoch !== editorEpoch || !current(draftID, scope)) return;
    if (before !== state()) throw new Error(t("nestedShortcuts.changedDraft"));
    insertParts([JSON.parse(JSON.stringify(part))], selection);
  } catch (error) { if (epoch === editorEpoch && current(draftID, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) uploading.value = false; }
}
function activateReference(part: ComposerReferencePart) {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  if (part.type === "office") officePreview.value = JSON.parse(JSON.stringify(part.office));
  if (part.type === "shortcut") {
    previewOrigin = captureNestedOrigin(shortcutOccurrenceEdit(part));
    nestedPreview.value = { id: part.shortcutId, title: part.title, content: part.text, icon: part.icon, language: part.language, ...(part.parts ? { parts: cloneInputParts(part.parts) } : {}) };
  }
}
function captureNestedOrigin(occurrence?: ShortcutOccurrenceEdit): NestedEditOrigin {
  return { parent: draft.value.id, scope: props.scopeKey, session: props.sessionId || "", epoch: editorEpoch, selection: draftEditor.value?.getSelection(), before: state(), occurrence };
}
function nestedCandidate(item: ShortcutSnapshot): ComposerPart[] {
  const origin = nestedOrigin;
  if (!origin || origin.epoch !== editorEpoch || origin.session !== (props.sessionId || "") || props.item.id !== origin.parent || !current(origin.parent, origin.scope) || props.disabled || saving.value || uploading.value) throw new Error("scopeChanged");
  if (origin.occurrence) return replaceShortcutOccurrence(draftParts.value, origin.occurrence, item);
  if (origin.before !== state() || item.id !== nestedDraft.value?.id) throw new Error("changedDraft");
  return [...draftParts.value, shortcutComposerPart(item)];
}
function validateNestedSnapshot(item: ShortcutSnapshot): string {
  try { return validateDraftParts(nestedCandidate(item)); }
  catch (error) { return error instanceof Error && ["scopeChanged", "changedDraft"].includes(error.message) ? error.message : "invalid"; }
}
function editNestedHere() {
  if (!nestedPreview.value || !previewOrigin || props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  nestedOrigin = previewOrigin; nestedNew.value = false;
  const snapshot = JSON.parse(JSON.stringify(nestedPreview.value)) as ShortcutSnapshot;
  try { nestedCandidate(snapshot); }
  catch (error) { saveError.value = validationMessage(error instanceof Error ? error.message : "changedDraft"); return; }
  nestedDraft.value = snapshot; nestedPreview.value = null;
}
function newNested() {
  if (!canAddNested.value || props.disabled || saving.value || uploading.value || !editScopeCurrent.value || nestedDraft.value) return;
  nestedOrigin = captureNestedOrigin(); nestedNew.value = true;
  nestedDraft.value = { id: `snapshot_${composerOccurrenceID()}`, title: t("nestedShortcuts.newTitle"), content: "", icon: "", language: "text" };
}
async function applyNestedSnapshot(item: ShortcutSnapshot) {
  let candidate: ComposerPart[];
  try { candidate = nestedCandidate(item); } catch (error) { throw new Error(validationMessage(error instanceof Error ? error.message : "changedDraft")); }
  const origin = nestedOrigin!;
  const error = validateDraftParts(candidate); if (error) throw new Error(validationMessage(error));
  if (origin.occurrence) {
    const replacement = candidate.find(part => part.type === "shortcut" && part.id === origin.occurrence!.occurrenceId) as ComposerReferencePart;
    if (draftEditor.value?.replaceReference) {
      if (!draftEditor.value.replaceReference(replacement, origin.occurrence.fingerprint)) throw new Error(t("nestedShortcuts.changedDraft"));
    } else draftParts.value = candidate;
  } else if (!insertParts([candidate[candidate.length - 1]], origin.selection)) throw new Error(saveError.value);
  saveError.value = "";
}
async function nestedVisibility(open: boolean) {
  if (open) return;
  const origin = nestedOrigin; nestedDraft.value = null; nestedOrigin = null;
  await nextTick();
  if (origin && origin.epoch === editorEpoch && current(origin.parent, origin.scope) && origin.session === (props.sessionId || "")) {
    if (origin.selection) draftEditor.value?.setSelection(origin.selection);
    draftEditor.value?.focus();
  }
}
async function importSourceDraft() {
  if (props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  const source = readSourceDraft?.();
  if (!source?.parts.length) { saveError.value = t("nestedShortcuts.emptySource"); return; }
  if (source.sessionId !== editingSession.value) { saveError.value = t("shortcuts.scopeChanged"); return; }
  const id = draft.value.id, scope = props.scopeKey, epoch = editorEpoch;
  const before = state(), selection = draftEditor.value?.getSelection();
  try {
    const wire = composerInputParts(source.parts, key => { const file = source.parts.find(p => p.type === "file" && p.key === key); if (!file || file.type !== "file" || file.status === "error" || file.status === "uploading" || !file.path && !source.prepare) throw new Error(t("nestedShortcuts.fileNotReady")); return [file.path || "uploads/pending-shortcut-file"]; });
    const error = validateInputParts(wire, ancestorChain.value); if (error) { saveError.value = t(`nestedShortcuts.${error}`); return; }
  } catch (error) { saveError.value = errorText(error); return; }
  uploading.value = true; saveError.value = "";
  try {
    const parts = source.prepare ? await source.prepare() : JSON.parse(JSON.stringify(source.parts));
    if (props.disabled || epoch !== editorEpoch || !current(id, scope)) return;
    if (before !== state()) throw new Error(t("nestedShortcuts.changedDraft"));
    insertParts(parts, selection);
  } catch (error) { if (epoch === editorEpoch && current(id, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) uploading.value = false; }
}

async function uploadFiles(files: File[]) {
  if (!props.uploadFile || props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  const id = draft.value.id, scope = props.scopeKey, epoch = editorEpoch;
  const before = state(), selection = draftEditor.value?.getSelection();
  uploading.value = true; saveError.value = "";
  try {
    const parts: ComposerPart[] = [];
    for (const file of files) {
      const result = await props.uploadFile(file);
      if (props.disabled || epoch !== editorEpoch || !current(id, scope)) return;
      parts.push(fileComposerPart({ key: result.path, path: result.path, label: result.name, mime: result.mime, size: result.size, status: "ready", asFile: true }));
    }
    if (before !== state()) throw new Error(t("nestedShortcuts.changedDraft"));
    insertParts(parts, selection);
  } catch (error) { if (epoch === editorEpoch && current(id, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) uploading.value = false; }
}
async function addInformation() {
  if (!props.pickInformation || props.disabled || saving.value || uploading.value || !editScopeCurrent.value) return;
  const id = draft.value.id, scope = props.scopeKey, epoch = editorEpoch;
  const before = state(), selection = draftEditor.value?.getSelection();
  uploading.value = true; pickingInformation.value = true; saveError.value = "";
  try {
    const selected = await props.pickInformation();
    if (!selected?.length || props.disabled || epoch !== editorEpoch || !current(id, scope)) return;
    if (before !== state()) { saveError.value = t("nestedShortcuts.changedDraft"); return; }
    const parts = JSON.parse(JSON.stringify(selected)) as ComposerPart[];
    const candidate = [...draftParts.value, ...parts];
    const check = validateDraftParts(candidate);
    if (check) throw new Error(validationMessage(check));
    if (draftEditor.value) {
      if (selection) draftEditor.value.setSelection(selection);
      draftEditor.value.insertParts(parts);
    } else draftParts.value.push(...parts);
  } catch (error) { if (epoch === editorEpoch && current(id, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) { uploading.value = false; pickingInformation.value = false; } }
}
async function saveDraft() {
  if (saving.value || uploading.value || props.disabled || formError.value || nestedDraft.value) return;
  if (!editScopeCurrent.value) { saveError.value = t("shortcuts.scopeChanged"); return; }
  const id = draft.value.id, scope = props.scopeKey, epoch = editorEpoch; saving.value = true; saveError.value = "";
  try {
    const icons = await snapshotComposerElementIcons(draftParts.value);
    if (epoch !== editorEpoch || !current(id, scope)) return;
    let parts = composerInputParts(icons, key => { const file = icons.find(p => p.type === "file" && p.key === key); return file?.type === "file" && file.path ? [file.path] : []; });
    if (props.prepareParts) parts = await props.prepareParts(parts);
    if (epoch !== editorEpoch || !current(id, scope)) return;
    const prepared = informationComposerParts(parts, []).map((part, index) => {
      const original = draftParts.value[index];
      return part.type !== "text" && original?.type === part.type ? { ...part, id: original.id } : part;
    });
    const item = shortcutSnapshotFromParts(draft.value, prepared, parts);
    const error = validateShortcutSnapshot(item, props.ancestorIds) || props.validateSnapshot?.(item);
    if (error) throw new Error(validationMessage(error));
    draftParts.value = prepared;
    await props.save(item);
    if (epoch === editorEpoch && current(id, scope)) { initial.value = state(); emit("update:modelValue", false); }
  } catch (error) { if (epoch === editorEpoch && current(id, scope)) saveError.value = errorText(error); }
  finally { if (!disposed && epoch === editorEpoch) saving.value = false; }
}
async function confirmLeave() {
  if (saving.value || uploading.value) return false;
  if (nestedDraft.value && !(await nestedEditor.value?.confirmLeave())) return false;
  if (!dirty.value) return true;
  return await leaveConfirm.value?.ask({ title: t(props.snapshotOnly ? "nestedShortcuts.discardTitle" : props.purpose === "schedule" ? "scheduleDialog.discardTitle" : "information.discardTitle"), body: t(props.snapshotOnly ? "nestedShortcuts.discardBody" : props.purpose === "schedule" ? "scheduleDialog.discardBody" : "information.discardBody"), confirmText: t(props.purpose === "schedule" ? "scheduleDialog.discard" : "information.discard"), cancelText: t("shortcuts.cancel"), danger: true }) ?? false;
}
async function close() { if (await confirmLeave()) emit("update:modelValue", false); }
function editorKeydown(event: KeyboardEvent) {
  if (event.defaultPrevented || nestedDraft.value) return;
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") { event.preventDefault(); void saveDraft(); }
  if (event.key === "Escape") { event.preventDefault(); if (sourcesOpen.value) setSourcesOpen(false); else void close(); }
}
// The root checks descendants first; separate global guards could dismiss a
// parent before an unsaved child gets a chance to veto navigation.
const unregister = props.snapshotOnly ? () => {} : registerStudioNavigationGuard(async () => { if (!props.modelValue) return true; if (!(await confirmLeave())) return false; emit("update:modelValue", false); return true; });
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || saving.value || uploading.value) { event.preventDefault(); event.returnValue = ""; } }
window.addEventListener("beforeunload", beforeUnload);
onBeforeUnmount(() => { disposed = true; unregister(); window.removeEventListener("beforeunload", beforeUnload); });
defineExpose({ dirty, busy: computed(() => saving.value || uploading.value), confirmLeave });
</script>
<template>
  <el-dialog :model-value="modelValue" :title="t(snapshotOnly ? (existing ? 'nestedShortcuts.editHere' : 'nestedShortcuts.newChild') : purpose === 'schedule' ? 'scheduleDialog.stepContentTitle' : existing ? 'shortcuts.editTitle' : 'shortcuts.addTitle')" fullscreen class="shortcut-editor-dialog" append-to-body :show-close="false" :close-on-click-modal="false" :close-on-press-escape="false" :before-close="close" :data-testid="snapshotOnly ? 'shortcut-snapshot-editor' : purpose === 'schedule' ? 'schedule-editor' : 'shortcut-editor'">
    <template #header><header class="editor-heading" @keydown="editorKeydown">
      <button type="button" class="editor-back" :disabled="saving || uploading" :aria-label="t(snapshotOnly ? 'nestedShortcuts.backToParent' : 'shortcutEditor.back')" @click="close"><Icon icon="lucide:arrow-left" /></button>
      <div class="editor-heading-title"><span class="editor-breadcrumb">{{ t(purpose === 'schedule' ? 'scheduleDialog.stepContentLabel' : 'shortcuts.title') }}</span><h1>{{ t(snapshotOnly ? (existing ? 'nestedShortcuts.editHere' : 'nestedShortcuts.newChild') : purpose === 'schedule' ? 'scheduleDialog.stepContentTitle' : existing ? 'shortcuts.editTitle' : 'shortcuts.addTitle') }}</h1></div>
      <span class="editor-draft-state" :class="{ dirty }"><span v-if="dirty" />{{ t(dirty ? 'shortcutEditor.unsaved' : 'shortcutEditor.snapshotHint') }}</span>
      <div class="editor-heading-actions"><el-button type="primary" :loading="saving" :disabled="disabled || uploading || !!formError || !editScopeCurrent || !!nestedDraft" :data-testid="purpose === 'schedule' ? 'schedule-next' : 'shortcut-save'" @click="saveDraft">{{ t(snapshotOnly ? 'nestedShortcuts.apply' : purpose === 'schedule' ? 'scheduleDialog.continueTiming' : 'shortcuts.save') }}</el-button></div>
    </header></template>
    <div class="editor-workspace" :class="{ 'with-sources': sourcesOpen }" @keydown="editorKeydown">
      <button type="button" ref="sourceToggle" class="editor-source-toggle" :class="{ active: sourcesOpen }" :aria-expanded="sourcesOpen" :aria-controls="`${uid}-sources`" :aria-label="t(sourcesOpen ? 'shortcutEditor.collapseSources' : 'shortcutEditor.expandSources')" :title="t(sourcesOpen ? 'shortcutEditor.collapseSources' : 'shortcutEditor.expandSources')" data-testid="shortcut-toggle-sources" @click="setSourcesOpen(!sourcesOpen)"><Icon :icon="sourcesOpen ? 'lucide:chevron-left' : 'lucide:chevron-right'" /></button>
      <div :id="`${uid}-sources`" ref="sourceShell" class="source-panel-shell" :inert="!sourcesOpen" :aria-hidden="!sourcesOpen"><ShortcutSourcesPanel class="editor-source-panel" :references="sourceReferences" :shortcuts="references" :library="library" :skills="skills" :current-id="draft.id" :disabled="disabled || saving || uploading || !editScopeCurrent" :can-pick-information="!!pickInformation" :can-refresh="!!readSourceDraft" @close="setSourcesOpen(false)" @refresh="refreshSources" @information="addInformation" @insert-reference="insertSourceReference" @insert-shortcut="chooseNested" @insert-skill="insertReference(skillComposerPart($event))" @create-web="openReferenceCreator('web')" @create-office="openReferenceCreator('office')" /></div>
      <form class="shortcut-editor-form" @submit.prevent="saveDraft"><div class="editor-document">
        <div class="editor-document-meta" :class="{ 'task-meta': purpose === 'schedule' }"><label class="shortcut-field shortcut-title-field" :for="`${uid}-title`"><span>{{ t(purpose === 'schedule' ? 'scheduleDialog.taskName' : 'shortcuts.titleLabel') }}</span><el-input :id="`${uid}-title`" v-model="draft.title" :disabled="saving" :placeholder="t(purpose === 'schedule' ? 'scheduleDialog.taskNamePlaceholder' : 'shortcuts.titlePlaceholder')" :data-testid="purpose === 'schedule' ? 'schedule-title' : 'shortcut-title'" /></label><div v-if="purpose !== 'schedule'" class="shortcut-icon-field"><span class="shortcut-field-label">{{ t('shortcuts.iconLabel') }} <small>{{ t('shortcuts.optional') }}</small></span><div class="shortcut-icon-picker" :inert="saving"><StarterIconPicker v-model="draft.icon!" module="shortcuts" :disabled="saving" :label="t('shortcuts.iconLabel')" /></div></div></div>
        <p v-if="snapshotOnly" class="nested-edit-notice" data-testid="shortcut-snapshot-hint">{{ t('nestedShortcuts.editHereHint') }} · {{ t('nestedShortcuts.editingLevel', { level: ancestorChain.length }) }}</p>
        <slot name="context" />
        <section class="shortcut-content-field" :aria-label="t(purpose === 'schedule' ? 'scheduleDialog.instructions' : 'shortcuts.contentLabel')">
          <div class="shortcut-content-toolbar"><div class="editor-section-title"><label :for="contentMode === 'edit' ? `${uid}-content` : undefined">{{ t(purpose === 'schedule' ? 'scheduleDialog.instructions' : 'shortcuts.contentLabel') }}</label><span>{{ t(purpose === 'schedule' ? 'scheduleDialog.stepContentHint' : 'shortcutEditor.contentHint') }}</span></div><div class="shortcut-content-controls"><div class="shortcut-content-modes" role="group" :aria-label="t('shortcuts.contentMode')"><button type="button" :class="{ active: contentMode === 'edit' }" :aria-pressed="contentMode === 'edit'" data-testid="shortcut-mode-edit" @click="contentMode = 'edit'"><Icon icon="lucide:pencil-line" />{{ t('shortcuts.editMode') }}</button><button type="button" :class="{ active: contentMode === 'preview' }" :aria-pressed="contentMode === 'preview'" data-testid="shortcut-mode-preview" @click="contentMode = 'preview'"><Icon icon="lucide:eye" />{{ t('shortcuts.previewMode') }}</button></div><ShortcutLanguageSelect v-if="purpose !== 'schedule'" v-model="draftLanguage" :disabled="saving" /></div></div>
          <div class="shortcut-insert-tools"><button type="button" data-testid="shortcut-new-child" :disabled="disabled || saving || uploading || !editScopeCurrent || !canAddNested" :title="!canAddNested ? t('nestedShortcuts.depth') : undefined" @mousedown.prevent @click="newNested"><Icon icon="lucide:plus" />{{ t('nestedShortcuts.newChild') }}</button><button v-if="readSourceDraft" type="button" :disabled="saving || uploading" @mousedown.prevent @click="importSourceDraft"><Icon icon="lucide:message-square-plus" />{{ t('nestedShortcuts.fromComposer') }}</button><small>{{ t('shortcutEditor.orderHint') }}</small></div>
          <InlineComposer v-show="contentMode === 'edit'" :id="`${uid}-content`" ref="draftEditor" v-model="draftParts" :disabled="saving" :submit-on-enter="false" :placeholder="t(purpose === 'schedule' ? 'scheduleDialog.instructionsPlaceholder' : 'shortcuts.contentPlaceholder')" :aria-label="t(purpose === 'schedule' ? 'scheduleDialog.instructions' : 'shortcuts.contentLabel')" :data-testid="purpose === 'schedule' ? 'schedule-instructions' : 'shortcut-content-input'" class="shortcut-ordered-editor" @paste-files="uploadFiles" @drop-files="uploadFiles" @activate-reference="activateReference" />
          <div v-if="contentMode === 'preview'" class="shortcut-content-preview" data-testid="shortcut-content-preview"><ShortcutContent v-if="structured || draftText" :parts="structured ? draftPreviewParts : undefined" :content="draftText" :language="draftLanguage" /><p v-else>{{ t('shortcuts.emptyPreview') }}</p></div>
          <div class="editor-content-foot"><span><Icon icon="lucide:shield-check" />{{ t(purpose === 'schedule' ? 'scheduleDialog.stepContentFootnote' : 'shortcutEditor.contentFootnote') }}</span><span class="shortcut-byte-count">{{ Math.ceil(contentBytes / 1024) }} / {{ structured ? 256 : 64 }} KiB</span></div>
        </section>
      </div></form>
      <div class="source-panel-backdrop" :class="{ visible: sourcesOpen }" aria-hidden="true" @click="setSourcesOpen(false)" />

    </div>
    <template #footer><footer class="editor-statusbar"><p v-if="!editScopeCurrent || saveError" class="shortcut-form-error" role="alert">{{ !editScopeCurrent ? t('shortcuts.scopeChanged') : saveError }}</p><p v-else-if="formError" class="shortcut-form-note">{{ formError }}</p><p v-else class="shortcut-form-note">{{ t(pickingInformation ? 'nestedShortcuts.addingInformation' : uploading ? 'nestedShortcuts.uploading' : snapshotOnly ? 'nestedShortcuts.pendingApply' : purpose === 'schedule' ? 'scheduleDialog.contentReady' : 'shortcutEditor.readyHint') }}</p><span class="editor-save-hint">{{ t(snapshotOnly ? 'nestedShortcuts.apply' : purpose === 'schedule' ? 'scheduleDialog.nextKeyboard' : 'shortcutEditor.saveKeyboard') }} <kbd>⌘ / Ctrl</kbd><kbd>↵</kbd></span></footer></template>
  </el-dialog>
  <WebReferenceCreateDialog v-model="webCreateOpen" :disabled="disabled || saving || uploading || !editScopeCurrent" :session-id="sessionId || ''" :observed-references="reusableWebReferences" :error="createReferenceError" :browser-session-id="webBrowserSession" :browser-state="webBrowserState" :browser-loading="webBrowserLoading" :browser-error="webBrowserError" @create="acceptCreatedReference($event, 'web')" @state="webBrowser.observe" @retry="webBrowser.retry" @recover="webBrowser.recover" @switch-tab="webBrowser.switchTab" />
  <InformationOfficeReferenceDialog v-model="officeCreateOpen" :disabled="disabled || saving || uploading || !editScopeCurrent" :error="createReferenceError" @select="acceptCreatedReference(officeComposerPart($event), 'office')" />
  <OfficeReferenceDialog v-model="officePreview" /><ShortcutReferenceDialog v-model="nestedPreview" :editable="!disabled && !saving && !uploading && editScopeCurrent" @edit="editNestedHere" /><ConfirmDialog ref="leaveConfirm" />
  <ShortcutEditorDialog v-if="nestedDraft" ref="nestedEditor" :model-value="true" :item="nestedDraft" :existing="!nestedNew" snapshot-only :ancestor-ids="ancestorChain" :validate-snapshot="validateNestedSnapshot" :scope-key="scopeKey" :session-id="sessionId" :disabled="disabled || !editScopeCurrent" :library="library" :reference-choices="referenceChoices" :resolve-reference="resolveReference" :skills="skills" :upload-file="uploadFile" :pick-information="pickInformation" :save="applyNestedSnapshot" @update:model-value="nestedVisibility" />
</template>
<style scoped>
.nested-edit-notice{margin:0 0 18px;padding:10px 12px;border:1px solid var(--el-color-primary-light-7);border-radius:8px;background:var(--el-color-primary-light-9);color:var(--el-text-color-secondary);font-size:12px;line-height:1.6}
button:disabled{cursor:not-allowed;opacity:.5}button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}button,input{font:inherit}svg{width:16px;height:16px;flex:none}.editor-heading{display:flex;align-items:center;gap:13px;min-height:75px;padding:0 28px;color:var(--el-text-color-primary)}.editor-back{display:grid;place-items:center;width:34px;height:34px;padding:0;border:1px solid var(--el-border-color-light);border-radius:9px;background:var(--el-bg-color);color:var(--el-text-color-regular);cursor:pointer}.editor-back:hover:not(:disabled){background:var(--el-fill-color-light)}.editor-heading-title{display:flex;align-items:center;gap:12px;min-width:0}.editor-breadcrumb{color:var(--el-text-color-secondary);font-size:13px;white-space:nowrap}.editor-breadcrumb::after{content:'/';margin-left:12px;color:var(--el-border-color)}h1{margin:0;font-size:15px;line-height:1.5;font-weight:600;white-space:nowrap}.editor-draft-state{display:flex;align-items:center;gap:5px;margin-left:6px;min-width:0;font-size:11px;color:var(--el-text-color-placeholder);overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.editor-draft-state>span{width:5px;height:5px;border-radius:50%;background:var(--el-color-warning)}.editor-draft-state.dirty{color:var(--el-text-color-secondary)}.editor-heading-actions{display:flex;align-items:center;gap:12px;margin-left:auto}.editor-source-toggle{display:flex;align-items:center;gap:6px;border:1px solid var(--el-border-color-light);border-radius:8px;padding:8px 10px;color:var(--el-text-color-secondary);background:var(--el-bg-color);font-size:12px;white-space:nowrap;cursor:pointer}.editor-source-toggle.active{background:var(--el-fill-color-light);color:var(--el-text-color-primary)}.editor-heading-actions :deep(.el-button){min-width:82px;height:34px;border-radius:8px}.editor-workspace{display:grid;grid-template-columns:minmax(0,1fr);flex:1;min-height:0;position:relative;background:var(--el-fill-color-lighter);overflow:hidden}.editor-workspace.with-sources{grid-template-columns:minmax(0,1fr) 360px}.shortcut-editor-form{min-width:0;min-height:0;height:100%;overflow:auto;overscroll-behavior:contain;scrollbar-gutter:stable}.editor-document{display:flex;flex-direction:column;width:min(100%,1040px);box-sizing:border-box;min-height:100%;margin:0 auto;padding:38px clamp(24px,4vw,64px) 30px}.editor-document-meta.task-meta{grid-template-columns:minmax(0,1fr)}.editor-document-meta{display:grid;grid-template-columns:minmax(0,1fr) 160px;gap:28px;padding-bottom:28px;border-bottom:1px solid var(--el-border-color-light)}.shortcut-field{display:flex;flex-direction:column;gap:10px;min-width:0;font-size:12px;color:var(--el-text-color-secondary)}.shortcut-title-field :deep(.el-input__wrapper){min-height:38px;padding:0 12px;border-radius:8px;background:var(--el-bg-color)}.shortcut-title-field :deep(.el-input__inner){font-size:16px;font-weight:550;letter-spacing:-.2px}.shortcut-icon-field{display:flex;flex-direction:column;gap:11px;font-size:12px;color:var(--el-text-color-secondary)}.shortcut-field-label small{font-size:10px;color:var(--el-text-color-placeholder);margin-left:4px}.shortcut-icon-picker{min-width:0}.shortcut-content-field{display:flex;flex-direction:column;flex:1;min-height:0;padding-top:27px}.shortcut-content-toolbar{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:16px;margin-bottom:17px}.editor-section-title{display:flex;flex-direction:column;gap:6px}.editor-section-title label{font-size:13px;font-weight:550;color:var(--el-text-color-primary)}.editor-section-title>span{font-size:11px;color:var(--el-text-color-secondary)}.shortcut-content-controls{display:flex;align-items:center;flex-wrap:wrap;gap:12px}.shortcut-content-modes{display:inline-flex;padding:2px;border:1px solid var(--el-border-color-light);border-radius:8px;background:var(--el-fill-color-light)}.shortcut-content-modes button{display:flex;align-items:center;gap:5px;min-height:27px;padding:3px 9px;border:0;border-radius:5px;background:transparent;color:var(--el-text-color-secondary);font-size:11px;cursor:pointer}.shortcut-content-modes button svg{width:13px;height:13px}.shortcut-content-modes button.active{background:var(--el-bg-color);color:var(--el-color-primary);box-shadow:0 1px 3px rgb(0 0 0 / 8%)}.shortcut-insert-tools{display:flex;align-items:center;flex-wrap:wrap;gap:8px;margin-bottom:12px}.shortcut-insert-tools button{display:flex;align-items:center;gap:5px;padding:6px 8px;border:1px solid var(--el-border-color-light);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-regular);font-size:11px;cursor:pointer}.shortcut-insert-tools button:hover:not(:disabled){border-color:var(--pg-accent-line);color:var(--el-color-primary)}.shortcut-insert-tools button svg{width:13px;height:13px}.shortcut-insert-tools small{margin-left:auto;font-size:10px;line-height:1.6;color:var(--el-text-color-placeholder)}.shortcut-ordered-editor,.shortcut-content-preview{box-sizing:border-box;flex:1;min-height:300px;border:1px solid var(--el-border-color-light);border-radius:12px;padding:22px clamp(16px,2vw,28px);background:var(--el-bg-color);font-size:14px;line-height:1.95;box-shadow:0 3px 16px rgb(0 0 0 / 2%)}.shortcut-ordered-editor:focus-within{border-color:var(--pg-accent-line);box-shadow:0 0 0 2px color-mix(in srgb,var(--el-color-primary) 5%,transparent)}.shortcut-ordered-editor :deep(.ProseMirror){min-height:260px}.shortcut-content-preview :deep(.bar){display:none}.shortcut-content-preview :deep(.code-block){border:0;background:transparent}.shortcut-content-preview :deep(.body){overflow:visible}.shortcut-content-preview>p{margin:16px 0;color:var(--el-text-color-placeholder);font-size:12px}.editor-content-foot{display:flex;align-items:flex-start;gap:15px;padding:12px 1px 0;color:var(--el-text-color-placeholder);font-size:10px;line-height:1.7}.editor-content-foot>span:first-child{display:flex;align-items:center;gap:5px;min-width:0}.editor-content-foot svg{width:12px;height:12px}.shortcut-byte-count{margin-left:auto;white-space:nowrap}.editor-source-panel{min-width:0;min-height:0}.source-panel-backdrop{display:none}.editor-statusbar{display:flex;justify-content:space-between;align-items:center;gap:20px;min-height:42px;padding:0 28px;color:var(--el-text-color-secondary)}.shortcut-form-error,.shortcut-form-note{margin:0;text-align:left;font-size:11px;line-height:1.6;overflow-wrap:anywhere}.shortcut-form-error{color:var(--el-color-danger)}.editor-save-hint{display:flex;align-items:center;gap:4px;white-space:nowrap;font-size:10px;color:var(--el-text-color-placeholder)}kbd{padding:1px 4px;border:1px solid var(--el-border-color-light);border-radius:4px;font:10px/1.5 inherit;background:var(--el-fill-color-lighter)}
@media(min-width:1500px){.editor-workspace.with-sources{grid-template-columns:minmax(0,1fr) 400px}}@media(max-width:1050px){.editor-heading{padding:0 20px;gap:10px}.editor-heading-title{gap:8px}.editor-draft-state:not(.dirty){display:none}.editor-document{padding:28px 24px}.editor-document-meta{gap:16px;grid-template-columns:minmax(0,1fr) 135px}.shortcut-insert-tools small{display:none}.shortcut-content-controls{gap:8px}.editor-section-title>span{max-width:260px}.editor-breadcrumb{display:none}}@media(max-width:800px){.editor-workspace.with-sources{grid-template-columns:minmax(0,1fr)}.editor-source-panel{position:absolute;z-index:2;right:0;top:0;bottom:0;width:min(380px,100%);box-shadow:-12px 0 50px rgb(0 0 0 / 12%)}.source-panel-backdrop{display:block;position:absolute;inset:0;z-index:1;background:rgb(0 0 0 / 12%)}.editor-document{padding:24px 20px}.editor-heading{padding:0 16px;min-height:65px}.editor-draft-state{display:none}.editor-heading-actions{gap:8px}.editor-source-toggle{padding:8px}.editor-source-toggle>span{display:none}.editor-statusbar{padding:9px 18px;min-height:24px}.editor-save-hint{display:none}.shortcut-content-toolbar{gap:12px}.shortcut-content-controls{justify-content:space-between;width:100%}.editor-document-meta{grid-template-columns:minmax(0,1fr) 125px}}@media(max-width:440px){.editor-heading{gap:9px;padding:0 12px}h1{font-size:13px}.editor-heading-actions :deep(.el-button){min-width:62px;margin:0}.editor-back{width:30px;height:30px}.editor-document{padding:22px 14px}.editor-document-meta{grid-template-columns:minmax(0,1fr);gap:15px}.shortcut-icon-field{flex-direction:row;align-items:center;gap:12px}.shortcut-icon-picker{width:150px}.shortcut-field-label{white-space:nowrap}.shortcut-ordered-editor,.shortcut-content-preview{padding:17px 14px}.editor-content-foot>span:first-child{display:none}}

/* Keep the source panel mounted: closing preserves its category and animates
   the actual editor width instead of removing a column in one frame. */
.editor-workspace{--source-panel-width:440px;grid-template-columns:40px minmax(0,1fr);transition:grid-template-columns 260ms cubic-bezier(.2,.7,.2,1)}
.editor-workspace.with-sources{grid-template-columns:var(--source-panel-width) minmax(0,1fr)}
.source-panel-shell{grid-column:1;grid-row:1;min-width:0;min-height:0;overflow:hidden;opacity:0;transform:translateX(-12px);pointer-events:none;transition:opacity 180ms ease,transform 260ms cubic-bezier(.2,.7,.2,1)}
.with-sources>.source-panel-shell{opacity:1;transform:translateX(0);pointer-events:auto}
.editor-workspace>.source-panel-shell>.editor-source-panel{position:relative;inset:auto;width:var(--source-panel-width);max-width:none;height:100%;box-shadow:none;box-sizing:border-box}
.shortcut-editor-form{grid-column:2;grid-row:1}
.editor-workspace>.editor-source-toggle{position:absolute;z-index:3;left:8px;top:16px;display:grid;place-items:center;box-sizing:border-box;width:28px;height:30px;padding:0;border-radius:7px;background:var(--el-bg-color);box-shadow:0 2px 8px rgb(0 0 0 / 6%);transition:left 260ms cubic-bezier(.2,.7,.2,1),background .15s ease}.editor-workspace.with-sources>.editor-source-toggle{left:calc(var(--source-panel-width) - 14px)}.editor-workspace>.editor-source-toggle:hover{border-color:var(--pg-accent-line);color:var(--el-color-primary)}
@media(min-width:1500px){.editor-workspace{--source-panel-width:480px}}
@media(min-width:801px) and (max-width:1100px){.editor-workspace{--source-panel-width:400px}}
@media(max-width:800px){.editor-workspace,.editor-workspace.with-sources{--source-panel-width:min(440px,calc(100vw - 28px));grid-template-columns:minmax(0,1fr);transition:none}.shortcut-editor-form{grid-column:1;box-sizing:border-box;padding-left:40px}.source-panel-shell{position:absolute;left:0;top:0;bottom:0;z-index:2;grid-column:auto;grid-row:auto;width:var(--source-panel-width);transform:translateX(-100%);background:var(--el-bg-color);box-shadow:14px 0 45px rgb(0 0 0 / 12%)}.with-sources>.source-panel-shell{transform:translateX(0)}.source-panel-backdrop{display:block;opacity:0;visibility:hidden;pointer-events:none;transition:opacity 200ms ease,visibility 0s 260ms}.source-panel-backdrop.visible{opacity:1;visibility:visible;pointer-events:auto;transition:opacity 200ms ease}}
@media(prefers-reduced-motion:reduce){.editor-workspace,.source-panel-shell,.source-panel-backdrop,.source-panel-backdrop.visible,.editor-workspace>.editor-source-toggle{transition:none!important}}
</style>
<style>
.shortcut-editor-dialog.el-dialog.is-fullscreen{display:flex;flex-direction:column;height:100dvh;max-height:100dvh;margin:0;padding:0;border-radius:0;overflow:hidden}.shortcut-editor-dialog>.el-dialog__header{flex:none;padding:0;margin:0;border-bottom:1px solid var(--el-border-color-light)}.shortcut-editor-dialog>.el-dialog__body{display:flex;flex:1;min-height:0;padding:0;overflow:hidden}.shortcut-editor-dialog>.el-dialog__footer{flex:none;margin:0;padding:0;border-top:1px solid var(--el-border-color-light);background:var(--el-bg-color)}
</style>
