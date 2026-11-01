<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { Plus, Search, EditPen, Delete, TopRight, DocumentCopy, Close, Folder, FolderAdd, ArrowRight } from "@element-plus/icons-vue";
import { shortcutsApi, shortcutAssetsApi, shortcutEntries, resolveStarterShortcut, ShortcutsAPIError, type ShortcutFolder, type ShortcutSnapshot, type ShortcutsView } from "@/api/shortcuts";
import { api } from "@/api/client";
import { resolveInformationEntry } from "@/api/information";
import { informationComposerParts } from "@/agent/informationComposer";
import { useInformationFilePicker } from "@/agent/useInformationFilePicker";
import type { MessageInputPart, SkillInfo, StarterCard } from "@/api/types";
import { observeStarterShortcutsChanged } from "@/agent/shortcutSources";
import { registerStudioNavigationGuard } from "@/agent/studioNavigation";
import { randomUUID } from "@/utils/uuid";
import { cloneInputParts, inputPartsHaveFiles, inputPartsText } from "@/agent/shortcutStructure";
import { shortcutFolderChildren, shortcutFolderNotEmpty, shortcutFolderPath, shortcutParent } from "@/agent/shortcutFolderTree";
import ShortcutEditorDialog from "./ShortcutEditorDialog.vue";
import ShortcutReference from "./ShortcutReference.vue";
import InformationPickerDialog from "./InformationPickerDialog.vue";
import ConfirmDialog from "./ConfirmDialog.vue";
import { shortcutSearchPreview, shortcutSearchSegments, shortcutTextMatches } from "./shortcutPresentation";
const props = withDefaults(defineProps<{ agentId?: string; sessionId?: string | null; disabled?: boolean; active?: boolean; displayTitle?: string; privateOnly?: boolean; sourceOnly?: boolean; readOnly?: boolean; folderId?: string; hideClose?: boolean }>(), { agentId: "", sessionId: "", disabled: false, active: true, privateOnly: false, sourceOnly: false, readOnly: false, folderId: "", hideClose: false });
const emit = defineEmits<{ insert: [shortcut: ShortcutSnapshot]; openStarter: [card: StarterCard]; manageStarters: []; close: []; openFolder: [id: string]; view: [view: ShortcutsView] }>();
const { t } = useI18n();
const scope = computed(() => props.agentId || "");
const view = ref<ShortcutsView | null>(null), loading = ref(false), loadError = ref(""), query = ref(""), saving = ref(false);
const editing = ref(false), editingExisting = ref(false), editingScope = ref(""), editingSession = ref(""), editingParent = ref("");
const draft = ref<ShortcutSnapshot>({ id: "", title: "", content: "", icon: "" });
const editor = ref<InstanceType<typeof ShortcutEditorDialog> | null>(null), leaveDialog = ref<InstanceType<typeof ConfirmDialog> | null>(null);
const skills = ref<SkillInfo[]>([]), deleteTarget = ref<ShortcutSnapshot | null>(null), deletingScope = ref(""), deleteError = ref("");
const folderDraft = ref<{ id: string; name: string; parent_id: string } | null>(null), folderMode = ref<"create" | "rename">("create"), folderScope = ref(""), folderInitial = ref(""), folderError = ref("");
const folderDelete = ref<ShortcutFolder | null>(null), folderDeleteScope = ref(""), folderDeleteError = ref("");
const limit = 256;
let sequence = 0, disposed = false, loadController: AbortController | undefined;
const editable = computed(() => !props.readOnly && !props.sourceOnly);
const needsAgent = computed(() => props.privateOnly && !props.agentId);
const folders = computed(() => view.value?.folders || []);
const referenceEntries = computed(() => shortcutEntries(view.value));
const allEntries = computed(() => props.sourceOnly ? (view.value?.starter_shortcuts || []).map(item => ({ ...item, source: "starter" as const })) : props.privateOnly ? view.value?.shortcuts || [] : referenceEntries.value);
const scopedEntries = computed(() => props.privateOnly ? allEntries.value.filter(item => shortcutParent(item) === (props.folderId || "")) : allEntries.value);
const visible = computed(() => scopedEntries.value.filter(item => shortcutTextMatches(`${item.title}\n${item.parts?.length ? inputPartsText(item.parts) : item.content}`, query.value)).map(item => ({ item, titleSegments: shortcutSearchSegments(item.title, query.value), previewSegments: shortcutSearchSegments(shortcutSearchPreview(query.value.trim() && item.parts?.length ? inputPartsText(item.parts) : item.content, query.value), query.value) })));
const currentFolders = computed(() => props.privateOnly ? shortcutFolderChildren(folders.value, props.folderId || "") : []);
const visibleFolders = computed(() => currentFolders.value.filter(folder => shortcutTextMatches(folder.name, query.value)).map(folder => ({ folder, segments: shortcutSearchSegments(folder.name, query.value) })));
// A source directory contains referenceable snapshots only. Panel actions must
// never execute merely because they were shown under "Quick Card".
const actions = computed(() => props.sourceOnly || props.privateOnly || !editable.value ? [] : (view.value?.starter_actions || []).filter(card => shortcutTextMatches(`${card.title}\n${card.label || ""}`, query.value)).map(card => ({ card, titleSegments: shortcutSearchSegments(card.title, query.value), previewSegments: shortcutSearchSegments(card.label || t("shortcuts.starterPanelHint"), query.value) })));
const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);
const clone = (item: ShortcutSnapshot): ShortcutSnapshot => ({ ...item, icon: item.icon || "", ...(item.parts ? { parts: cloneInputParts(item.parts) } : {}) });
watch(view, value => { if (value && props.privateOnly) emit("view", JSON.parse(JSON.stringify(value))); });
async function load() {
  const ticket = ++sequence, agent = scope.value; loadController?.abort();
  if (needsAgent.value) { view.value = null; loading.value = false; loadError.value = ""; return; }
  const controller = new AbortController(); loadController = controller; loading.value = true; loadError.value = "";
  try { const result = await shortcutsApi.list(agent, controller.signal); if (!disposed && ticket === sequence && scope.value === agent) view.value = result; }
  catch (error) { if (!disposed && ticket === sequence && !controller.signal.aborted) loadError.value = t("shortcuts.loadFailed", { message: errorText(error) }); }
  finally { if (!disposed && ticket === sequence) loading.value = false; }
}
watch(scope, () => { view.value = null; query.value = ""; void load(); }, { immediate: true });
watch(() => props.active, active => { if (active) void load(); });
watch(() => props.folderId, () => { query.value = ""; });
const stopObservingSources = observeStarterShortcutsChanged(() => scope.value, () => { if (!disposed) void load(); });
watch(() => [props.sessionId, props.agentId], async () => { const sid = props.sessionId, agent = props.agentId; skills.value = []; if (!sid || !editable.value) return; try { const result = await api.skills(sid); if (!disposed && sid === props.sessionId && agent === props.agentId) skills.value = result.skills; } catch { /* Text editing remains available without a skill catalog. */ } }, { immediate: true });
const canChange = () => !disposed && props.active !== false && editable.value && !props.disabled && !saving.value && !needsAgent.value && !!view.value && view.value.agent_id === scope.value;
function openAdd() {
  if (!canChange()) return;
  editingScope.value = scope.value; editingSession.value = props.sessionId || ""; editingParent.value = props.privateOnly ? props.folderId || "" : ""; editingExisting.value = false;
  draft.value = { id: randomUUID(), title: "", content: "", icon: "", ...(editingParent.value ? { parent_id: editingParent.value } : {}) }; editing.value = true;
}
function openEdit(item: ShortcutSnapshot) { if (!canChange() || item.source === "starter") return; editingScope.value = scope.value; editingSession.value = props.sessionId || ""; editingParent.value = shortcutParent(item); editingExisting.value = true; draft.value = clone(item); editing.value = true; }
function insert(item: ShortcutSnapshot) { if (!disposed && props.active !== false && !props.disabled && !saving.value && view.value?.agent_id === scope.value && allEntries.value.some(value => value.id === item.id)) emit("insert", clone(item)); }
function conflict(error: unknown, agent: string) { if (!(error instanceof ShortcutsAPIError) || error.status !== 409 || !error.current || error.current.agent_id !== agent || scope.value !== agent) return false; view.value = error.current; return true; }
async function prepareParts(parts: MessageInputPart[]) { if (!editable.value) throw new Error(t("shortcutWorkspace.readOnlySource")); if (!inputPartsHaveFiles(parts)) return parts; if (!editingSession.value || editingSession.value !== props.sessionId || editingScope.value !== scope.value) throw new Error(t("nestedShortcuts.sessionRequired")); return (await shortcutAssetsApi.save(editingSession.value, { parts, input_layout: "inline" })).parts; }
const uploadFile = computed(() => editable.value && props.sessionId ? (file: File) => api.uploadAttachment(props.sessionId!, file, file.name) : undefined);
const informationPicker = useInformationFilePicker({
  allowedKinds: ["file", "shortcut"], scope: () => JSON.stringify([scope.value, props.sessionId, editing.value, draft.value.id]),
  resolve: async entry => { const sid = editingSession.value; if (!editable.value || !sid || sid !== props.sessionId || editingScope.value !== scope.value) throw new Error(t("nestedShortcuts.sessionRequired")); const result = await resolveInformationEntry(sid, entry.id, entry.version); if (result.parts.length !== 1 || !["file", "shortcut"].includes(result.parts[0].type)) throw new Error(t("nestedShortcuts.invalid")); return informationComposerParts(result.parts, result.attachments); },
  errorText: value => t("nestedShortcuts.insertFailed", { message: errorText(value) }),
});
const { open: informationPickerOpen, busy: informationPickerBusy, error: informationPickerError } = informationPicker;
const pickInformation = computed(() => editable.value && props.sessionId ? informationPicker.choose : undefined);
async function resolveReference(id: string) {
  const item = referenceEntries.value.find(entry => entry.id === id); if (!item) throw new Error(t("shortcuts.itemDeleted")); if (item.source !== "starter") return clone(item);
  const sid = editingSession.value, agent = editingScope.value; if (!sid || sid !== props.sessionId || agent !== scope.value) throw new Error(t("nestedShortcuts.sessionRequired"));
  const result = await resolveStarterShortcut(sid, clone(item)); if (disposed || sid !== props.sessionId || agent !== scope.value) throw new Error(t("shortcuts.scopeChanged")); return result;
}
async function saveDraft(item: ShortcutSnapshot) {
  if (!canChange() || !view.value || editingScope.value !== scope.value || editingSession.value !== (props.sessionId || "")) throw new Error(t("shortcuts.scopeChanged"));
  const before = view.value, agent = scope.value, index = before.shortcuts.findIndex(entry => entry.id === item.id), parent = editingParent.value;
  if (editingExisting.value && index < 0) throw new Error(t("shortcuts.itemDeleted"));
  if (index >= 0 && shortcutParent(before.shortcuts[index]) !== parent || parent && !folders.value.some(folder => folder.id === parent)) throw new Error(t("shortcutWorkspace.locationChanged"));
  if (index < 0 && before.shortcuts.length >= limit) throw new Error(t("shortcuts.maxItems"));
  const { source: _source, ...saved } = clone(item); if (parent) saved.parent_id = parent; else delete saved.parent_id;
  const next = [...before.shortcuts]; if (index >= 0) next[index] = saved; else next.push(saved);
  saving.value = true;
  try { const result = await shortcutsApi.save(agent, { ...before, folders: [...(before.folders || [])], shortcuts: next }); if (disposed || scope.value !== agent) throw new Error(t("shortcuts.scopeChanged")); view.value = result; if (!editingExisting.value) query.value = ""; ElMessage.success(t("shortcuts.saved")); }
  catch (error) { throw new Error(conflict(error, agent) ? t("shortcuts.conflict") : t("shortcuts.saveFailed", { message: errorText(error) })); }
  finally { if (!disposed) saving.value = false; }
}
function askDelete(item: ShortcutSnapshot) { if (canChange() && item.source !== "starter") { deleteTarget.value = clone(item); deletingScope.value = scope.value; deleteError.value = ""; } }
async function remove() {
  if (!canChange() || !deleteTarget.value || !view.value) return;
  if (deletingScope.value !== scope.value) { deleteError.value = t("shortcuts.scopeChanged"); return; }
  const before = view.value, agent = scope.value, id = deleteTarget.value.id; saving.value = true; deleteError.value = "";
  try { const result = await shortcutsApi.save(agent, { ...before, folders: [...(before.folders || [])], shortcuts: before.shortcuts.filter(item => item.id !== id) }); if (disposed) return; if (scope.value !== agent) { deleteError.value = t("shortcuts.scopeChanged"); return; } view.value = result; deleteTarget.value = null; ElMessage.success(t("shortcuts.removed")); }
  catch (error) { if (!disposed) deleteError.value = conflict(error, agent) ? t("shortcuts.deleteConflict") : errorText(error); }
  finally { if (!disposed) saving.value = false; }
}
const deleteOpen = computed({ get: () => !!deleteTarget.value, set: value => { if (!value && !saving.value) deleteTarget.value = null; } });
function addFolder(parent = props.folderId || "") { if (!canChange() || !props.privateOnly || folders.value.length >= 256 || parent && !folders.value.some(folder => folder.id === parent)) return; folderMode.value = "create"; folderScope.value = scope.value; folderDraft.value = { id: "folder_" + randomUUID().replaceAll("-", ""), name: "", parent_id: parent }; folderInitial.value = ""; folderError.value = ""; }
function renameFolder(folder: ShortcutFolder) { if (!canChange() || !props.privateOnly) return; folderMode.value = "rename"; folderScope.value = scope.value; folderDraft.value = { ...folder, parent_id: shortcutParent(folder) }; folderInitial.value = folder.name; folderError.value = ""; }
const folderDraftDirty = computed(() => !!folderDraft.value && folderDraft.value.name !== folderInitial.value);
async function confirmFolderLeave() { if (saving.value) return false; if (!folderDraftDirty.value) return true; return await leaveDialog.value?.ask({ title: t("information.discardTitle"), body: t("information.discardBody"), confirmText: t("information.discard"), cancelText: t("shortcuts.cancel"), danger: true }) ?? false; }
async function closeFolder() { if (await confirmFolderLeave()) folderDraft.value = null; }
async function saveFolder() {
  if (!canChange() || !folderDraft.value || !view.value) return;
  const value = { ...folderDraft.value, name: folderDraft.value.name.trim() }, before = view.value, agent = scope.value;
  if (folderScope.value !== agent) { folderError.value = t("shortcuts.scopeChanged"); return; }
  if (!value.name || [...value.name].length > 120 || /[\u0000-\u001f\u007f-\u009f]/.test(value.name)) { folderError.value = t("shortcutWorkspace.folderNameInvalid"); return; }
  if (value.parent_id && !folders.value.some(folder => folder.id === value.parent_id)) { folderError.value = t("shortcutWorkspace.locationChanged"); return; }
  if (folderMode.value === "create" && shortcutFolderPath(folders.value, value.parent_id).length >= 16) { folderError.value = t("shortcutWorkspace.folderDepth"); return; }
  const next = [...(before.folders || [])], index = next.findIndex(folder => folder.id === value.id);
  if (folderMode.value === "rename" && index < 0) { folderError.value = t("shortcutWorkspace.locationChanged"); return; }
  if (index >= 0 && shortcutParent(next[index]) !== value.parent_id) { folderError.value = t("shortcutWorkspace.locationChanged"); return; }
  if (index >= 0) next[index] = value; else next.push(value);
  saving.value = true; folderError.value = "";
  try { const result = await shortcutsApi.save(agent, { ...before, folders: next, shortcuts: [...before.shortcuts] }); if (disposed || scope.value !== agent) return; view.value = result; folderDraft.value = null; ElMessage.success(t("shortcuts.saved")); }
  catch (error) { if (!disposed && scope.value === agent) folderError.value = conflict(error, agent) ? t("shortcuts.conflict") : errorText(error); }
  finally { if (!disposed) saving.value = false; }
}
const folderPathLabel = computed(() => shortcutFolderPath(folders.value, folderDraft.value?.parent_id || "").map(folder => folder.name).join(" / ") || t("shortcutWorkspace.rootFolder"));
function askDeleteFolder(folder: ShortcutFolder) { if (!canChange() || !props.privateOnly) return; folderDelete.value = { ...folder }; folderDeleteScope.value = scope.value; folderDeleteError.value = ""; }
const folderDeleteBlocked = computed(() => !!folderDelete.value && shortcutFolderNotEmpty(folders.value, view.value?.shortcuts || [], folderDelete.value.id));
async function removeFolder() {
  if (!canChange() || !folderDelete.value || !view.value || folderDeleteBlocked.value) return;
  if (folderDeleteScope.value !== scope.value) { folderDeleteError.value = t("shortcuts.scopeChanged"); return; }
  const before = view.value, agent = scope.value, id = folderDelete.value.id;
  const current = folders.value.find(folder => folder.id === id);
  if (!current || current.name !== folderDelete.value.name || shortcutParent(current) !== shortcutParent(folderDelete.value)) { folderDeleteError.value = t("shortcutWorkspace.locationChanged"); return; }
  saving.value = true;
  try { const result = await shortcutsApi.save(agent, { ...before, folders: (before.folders || []).filter(folder => folder.id !== id), shortcuts: [...before.shortcuts] }); if (disposed || scope.value !== agent) return; view.value = result; folderDelete.value = null; ElMessage.success(t("shortcuts.removed")); }
  catch (error) { if (!disposed && scope.value === agent) folderDeleteError.value = conflict(error, agent) ? t("shortcuts.deleteConflict") : errorText(error); }
  finally { if (!disposed) saving.value = false; }
}
async function confirmLeave() {
  if (saving.value || informationPickerBusy.value) return false;
  if (editing.value) { if (!editor.value || !await editor.value.confirmLeave()) return false; editing.value = false; }
  if (!await confirmFolderLeave()) return false; folderDraft.value = null; deleteTarget.value = null; folderDelete.value = null; return true;
}
const unregister = registerStudioNavigationGuard(async () => { if (!folderDraft.value) return true; return confirmFolderLeave(); });
function beforeUnload(event: BeforeUnloadEvent) { if (folderDraftDirty.value || saving.value) { event.preventDefault(); event.returnValue = ""; } }
window.addEventListener("beforeunload", beforeUnload);
onBeforeUnmount(() => { disposed = true; sequence++; loadController?.abort(); stopObservingSources(); unregister(); window.removeEventListener("beforeunload", beforeUnload); });
defineExpose({ refresh: load, confirmLeave, addFolder, renameFolder, askDeleteFolder });
</script>
<template>
  <section class="shortcuts-panel" :class="{ 'source-only': sourceOnly, 'private-panel': privateOnly }" data-testid="shortcuts-panel" :aria-label="displayTitle || t('shortcuts.title')">
    <header class="shortcuts-head"><div class="shortcuts-heading"><h2>{{ displayTitle || t('shortcuts.title') }}</h2><span v-if="view" class="shortcuts-count">{{ t('shortcuts.count', { n: scopedEntries.length + currentFolders.length + actions.length }) }}</span></div><div class="shortcuts-head-actions"><button v-if="editable && privateOnly" type="button" class="shortcut-folder-add" :disabled="disabled || saving || loading || !view || folders.length >= 256" data-testid="shortcut-add-folder" @click="addFolder()"><el-icon><FolderAdd /></el-icon><span>{{ t('shortcutWorkspace.addFolder') }}</span></button><button v-if="editable" type="button" class="shortcut-add" :disabled="disabled || saving || loading || !view || view.shortcuts.length >= limit" data-testid="shortcut-add" @click="openAdd"><el-icon><Plus /></el-icon><span>{{ t('shortcuts.add') }}</span></button><button v-if="!hideClose" type="button" class="shortcut-close-panel" :aria-label="t('shortcuts.closePanel')" @click="emit('close')"><el-icon><Close /></el-icon></button></div><p>{{ t(sourceOnly ? 'shortcutWorkspace.sourceOnlyHint' : privateOnly ? 'shortcutWorkspace.agentOnlyHint' : 'shortcuts.description') }}</p></header>
    <div class="shortcuts-search"><el-icon aria-hidden="true"><Search /></el-icon><input v-model="query" type="search" :placeholder="t(privateOnly ? 'shortcutWorkspace.searchCurrentFolder' : 'shortcuts.search')" :aria-label="t(privateOnly ? 'shortcutWorkspace.searchCurrentFolder' : 'shortcuts.search')" data-testid="shortcuts-search" /></div>
    <div class="shortcuts-body">
      <slot name="root-presets" />
      <div v-if="needsAgent" class="shortcut-state"><strong>{{ t('shortcutWorkspace.agentRequired') }}</strong></div>
      <div v-else-if="loading && !view" class="shortcut-state" role="status">{{ t('shortcuts.loading') }}</div>
      <div v-else-if="loadError" class="shortcut-state error" role="alert"><p>{{ loadError }}</p><button type="button" class="shortcut-link" @click="load">{{ t('shortcuts.retry') }}</button></div>
      <template v-else>
        <ul v-if="visibleFolders.length" class="shortcut-folder-list"><li v-for="{ folder, segments } in visibleFolders" :key="folder.id" class="shortcut-folder-item" data-testid="shortcut-private-folder"><button type="button" class="shortcut-folder-open" :disabled="disabled || saving" @click="emit('openFolder', folder.id)"><el-icon><Folder /></el-icon><span><strong><template v-for="(segment, index) in segments" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></strong><small>{{ t('shortcutWorkspace.privateFolder') }}</small></span><el-icon class="folder-enter"><ArrowRight /></el-icon></button><div v-if="editable" class="shortcut-folder-actions"><button type="button" :disabled="disabled || saving" :title="t('shortcutWorkspace.renameFolder')" :aria-label="`${t('shortcutWorkspace.renameFolder')} ${folder.name}`" data-testid="shortcut-folder-rename" @click="renameFolder(folder)"><el-icon><EditPen /></el-icon></button><button type="button" class="danger" :disabled="disabled || saving" :title="t('shortcuts.remove')" :aria-label="`${t('shortcuts.remove')} ${folder.name}`" data-testid="shortcut-folder-delete" @click="askDeleteFolder(folder)"><el-icon><Delete /></el-icon></button></div></li></ul>
        <div v-if="!scopedEntries.length && !currentFolders.length && !actions.length" class="shortcut-state empty"><el-icon class="empty-mark" aria-hidden="true"><DocumentCopy /></el-icon><strong>{{ t(sourceOnly ? 'shortcutWorkspace.noSourceCards' : 'shortcuts.empty') }}</strong><p>{{ t(sourceOnly ? 'shortcutWorkspace.noSourceCardsHint' : 'shortcuts.emptyHint') }}</p><button v-if="editable" type="button" class="shortcut-add" :disabled="disabled || !view" @click="openAdd"><el-icon><Plus /></el-icon>{{ t('shortcuts.add') }}</button></div>
        <div v-else-if="!visible.length && !visibleFolders.length && !actions.length" class="shortcut-state"><strong>{{ t('shortcuts.noResults') }}</strong><p>{{ t(privateOnly ? 'shortcutWorkspace.searchCurrentFolderHint' : 'shortcuts.noResultsHint') }}</p><button type="button" class="shortcut-link" @click="query = ''">{{ t('shortcuts.clearSearch') }}</button></div>
        <ul v-if="visible.length || actions.length" class="shortcut-list">
          <li v-for="{ item, previewSegments } in visible" :key="item.id" class="shortcut-item" data-testid="shortcut-item"><div class="shortcut-entry-heading"><ShortcutReference :title="item.title" :content="item.content" :parts="item.parts" :language="item.language" :icon="item.icon" :highlight="query" /><div v-if="editable && item.source !== 'starter'" class="shortcut-inline-actions"><button type="button" :disabled="disabled || saving" :aria-label="`${t('shortcuts.edit')} ${item.title}`" :title="t('shortcuts.edit')" data-testid="shortcut-edit" @click="openEdit(item)"><el-icon><EditPen /></el-icon></button><button type="button" class="danger" :disabled="disabled || saving" :aria-label="`${t('shortcuts.remove')} ${item.title}`" :title="t('shortcuts.remove')" data-testid="shortcut-delete" @click="askDelete(item)"><el-icon><Delete /></el-icon></button></div></div><button type="button" class="shortcut-content" :disabled="disabled || saving" :aria-label="t('shortcuts.insertTitle', { title: item.title })" @click="insert(item)"><span class="shortcut-summary"><template v-for="(segment, index) in previewSegments" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></span><span class="shortcut-card-foot"><span v-if="item.source === 'starter'" class="shortcut-source-label">{{ t('shortcuts.starterSource') }}</span><span class="shortcut-insert-hint"><el-icon><TopRight /></el-icon>{{ t('shortcuts.insert') }}</span></span></button></li>
          <li v-for="{ card, titleSegments, previewSegments } in actions" :key="`action:${card.panel}:${card.title}`" class="shortcut-item" data-testid="starter-action"><button class="shortcut-content" type="button" :disabled="disabled || saving" @click="emit('openStarter', card)"><strong><template v-for="(segment, index) in titleSegments" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></strong><span class="shortcut-summary"><template v-for="(segment, index) in previewSegments" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></span><span class="shortcut-source-label">{{ t('shortcuts.starterAction') }}</span></button></li>
        </ul>
      </template>
    </div>
    <ShortcutEditorDialog v-if="editable" ref="editor" v-model="editing" :item="draft" :existing="editingExisting" :scope-key="scope" :session-id="sessionId" :library="view?.shortcuts || []" :reference-choices="referenceEntries" :resolve-reference="resolveReference" :skills="skills" :disabled="disabled" :save="saveDraft" :prepare-parts="prepareParts" :upload-file="uploadFile" :pick-information="pickInformation" />
    <InformationPickerDialog v-if="editable && informationPickerOpen" :model-value="informationPickerOpen" :disabled="informationPickerBusy" :error="informationPickerError" @update:model-value="!$event && informationPicker.cancel()" @select="informationPicker.select" />
    <el-dialog v-model="deleteOpen" :title="t('shortcuts.deleteTitle')" width="min(420px, calc(100vw - 32px))" append-to-body :show-close="!saving" :close-on-click-modal="!saving" :close-on-press-escape="!saving" data-testid="shortcut-delete-confirm"><p class="shortcut-delete-copy">{{ t('shortcuts.deleteHint', { title: deleteTarget?.title }) }}</p><template #footer><p v-if="deleteError" class="shortcut-form-error" role="alert">{{ deleteError }}</p><el-button :disabled="saving" @click="deleteTarget = null">{{ t('shortcuts.cancel') }}</el-button><el-button type="danger" :loading="saving" :disabled="disabled || !editable || deletingScope !== scope || !view" data-testid="shortcut-delete-yes" @click="remove">{{ t('shortcuts.deleteConfirm') }}</el-button></template></el-dialog>
    <el-dialog :model-value="!!folderDraft" :title="t(folderMode === 'create' ? 'shortcutWorkspace.addFolder' : 'shortcutWorkspace.renameFolder')" width="min(520px, calc(100vw - 28px))" append-to-body :show-close="!saving" :close-on-click-modal="false" :close-on-press-escape="!saving" :before-close="closeFolder" data-testid="shortcut-folder-editor"><form v-if="folderDraft" class="shortcut-folder-form" @submit.prevent="saveFolder"><p>{{ t('shortcutWorkspace.folderLocation', { path: folderPathLabel }) }}</p><label><span>{{ t('shortcutWorkspace.folderName') }}</span><el-input v-model="folderDraft.name" :disabled="saving" :placeholder="t('shortcutWorkspace.folderNamePlaceholder')" data-testid="shortcut-folder-name" /></label></form><template #footer><p v-if="folderError || folderScope !== scope" class="shortcut-form-error" role="alert">{{ folderScope !== scope ? t('shortcuts.scopeChanged') : folderError }}</p><el-button :disabled="saving" @click="closeFolder">{{ t('shortcuts.cancel') }}</el-button><el-button type="primary" :disabled="disabled || !editable || folderScope !== scope || !folderDraft?.name.trim()" :loading="saving" data-testid="shortcut-folder-save" @click="saveFolder">{{ t('shortcuts.save') }}</el-button></template></el-dialog>
    <el-dialog :model-value="!!folderDelete" :title="t('shortcutWorkspace.deleteFolder')" width="min(460px, calc(100vw - 28px))" append-to-body :show-close="!saving" :close-on-click-modal="!saving" :close-on-press-escape="!saving" data-testid="shortcut-folder-delete-confirm" @update:model-value="!saving && !$event && (folderDelete = null)"><p class="shortcut-delete-copy">{{ t(folderDeleteBlocked ? 'shortcutWorkspace.folderNotEmpty' : 'shortcutWorkspace.deleteFolderHint', { name: folderDelete?.name }) }}</p><template #footer><p v-if="folderDeleteError" class="shortcut-form-error" role="alert">{{ folderDeleteError }}</p><el-button :disabled="saving" @click="folderDelete = null">{{ t('shortcuts.cancel') }}</el-button><el-button v-if="!folderDeleteBlocked" type="danger" :disabled="disabled || !editable || folderDeleteScope !== scope" :loading="saving" data-testid="shortcut-folder-delete-yes" @click="removeFolder">{{ t('shortcuts.remove') }}</el-button></template></el-dialog>
    <ConfirmDialog ref="leaveDialog" />
  </section>
</template>
<style scoped>
.shortcuts-panel { display:flex; flex-direction:column; height:100%; min-height:0; color:var(--el-text-color-primary); background:var(--el-bg-color); }
.shortcuts-head { display:grid; grid-template-columns:minmax(0,1fr) auto; align-items:center; gap:8px 12px; padding:22px 22px 14px; }
.shortcuts-heading { display:flex; min-width:0; align-items:center; gap:9px; }
.shortcuts-heading h2 { margin:0; font-size:17px; font-weight:600; letter-spacing:-.2px; }
.shortcuts-count { padding:2px 6px; border-radius:6px; background:var(--el-fill-color-light); color:var(--el-text-color-secondary); font-size:11px; white-space:nowrap; }
.shortcuts-head p { grid-column:1/-1; margin:0; font-size:12px; line-height:1.6; color:var(--el-text-color-secondary); }
.shortcuts-head-actions { display:flex; align-items:center; gap:8px; }.shortcut-close-panel { display:grid; place-items:center; width:28px; height:28px; padding:0; border:0; border-radius:7px; background:transparent; color:var(--el-text-color-secondary); cursor:pointer; }.shortcut-close-panel:hover { background:var(--el-fill-color-light); color:var(--el-text-color-primary); }
.shortcut-add { display:inline-flex; align-items:center; justify-content:center; gap:6px; min-height:32px; padding:6px 10px; border:1px solid var(--pg-accent-line); border-radius:8px; background:var(--el-color-primary-light-9); color:var(--el-color-primary); font:inherit; font-size:12px; cursor:pointer; }
button:disabled { cursor:not-allowed; opacity:.5; }
.shortcut-add:hover:not(:disabled) { background:var(--el-color-primary-light-8); }
.shortcuts-search { display:flex; align-items:center; gap:7px; margin:0 22px 14px; padding:8px 10px; border:1px solid var(--el-border-color-light); border-radius:9px; background:var(--el-fill-color-lighter); color:var(--el-text-color-placeholder); }
.shortcuts-search:focus-within { border-color:var(--el-color-primary); }
.shortcuts-search input { width:100%; min-width:0; padding:0; border:0; outline:0; background:transparent; color:var(--el-text-color-primary); font:inherit; font-size:12px; }
.shortcuts-search input::placeholder { color:var(--el-text-color-placeholder); }
.shortcuts-body { flex:1; min-height:0; overflow:auto; overscroll-behavior:contain; padding:0 22px 22px; }
.shortcut-list { display:flex; flex-direction:column; gap:10px; margin:0; padding:0; list-style:none; }
.shortcut-item { position:relative; border:1px solid var(--el-border-color-light); border-radius:12px; background:var(--el-bg-color); transition:border-color 120ms,box-shadow 120ms; overflow:hidden; }
.shortcut-item:hover, .shortcut-item:focus-within { border-color:var(--pg-accent-line); box-shadow:0 3px 12px rgb(0 0 0 / 4%); }
.shortcut-content { display:flex; flex-direction:column; align-items:stretch; gap:7px; width:100%; padding:14px; border:0; background:transparent; text-align:left; color:inherit; font:inherit; cursor:pointer; }
.shortcut-content strong { display:flex; align-items:flex-start; gap:8px; padding-right:55px; font-size:13px; font-weight:550; line-height:1.5; overflow-wrap:anywhere; }
.shortcut-content strong svg { width:17px; height:17px; flex:0 0 auto; margin-top:1px; color:var(--el-color-primary); }
.shortcut-summary { display:-webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow:hidden; min-height:36px; color:var(--el-text-color-secondary); font-size:12px; line-height:1.55; overflow-wrap:anywhere; }
mark { padding:0 1px; border-radius:2px; background:color-mix(in srgb,var(--el-color-primary) 22%,transparent); color:var(--el-text-color-primary); font-weight:650; }
.shortcut-source-label { align-self:flex-start; padding:2px 6px; border-radius:5px; color:var(--el-text-color-secondary); background:var(--el-fill-color-light); font-size:10px; }.shortcut-manage-source { display:block; margin:0 14px 12px; padding:0; border:0; background:transparent; color:var(--el-color-primary); font:inherit; font-size:11px; cursor:pointer; }
.shortcut-ordered-editor { min-height:286px; max-height:50dvh; overflow:auto; border:1px solid var(--el-border-color); border-radius:8px; padding:12px; font-size:13px; line-height:1.8; background:var(--el-bg-color); }.shortcut-insert-tools { display:flex; align-items:center; flex-wrap:wrap; gap:7px; }.shortcut-insert-tools button { border:1px solid var(--el-border-color-light); border-radius:6px; padding:5px 8px; color:var(--el-text-color-regular); background:var(--el-bg-color); cursor:pointer; }.shortcut-insert-tools small { flex:1 1 180px; color:var(--el-text-color-placeholder); font-size:11px; }.shortcut-content-toolbar { display:flex; align-items:center; justify-content:space-between; flex-wrap:wrap; gap:8px; }
.shortcut-content-controls { display:flex; align-items:center; flex-wrap:wrap; gap:10px; }
.shortcut-content-modes { display:inline-flex; padding:2px; border:1px solid var(--el-border-color-light); border-radius:8px; background:var(--el-fill-color-lighter); }
.shortcut-content-modes button { min-height:26px; padding:3px 10px; border:0; border-radius:5px; background:transparent; color:var(--el-text-color-secondary); font:inherit; font-size:12px; cursor:pointer; }
.shortcut-content-modes button.active { background:var(--el-bg-color); color:var(--el-color-primary); box-shadow:0 1px 3px rgb(0 0 0 / 8%); }
.shortcut-content-preview { box-sizing:border-box; padding:12px; font-size:13px; font-weight:400; line-height:1.85; min-height:286px; max-height:50dvh; overflow:auto; border:1px solid var(--el-border-color); border-radius:6px; background:var(--el-fill-color-lighter); }
.shortcut-content-preview :deep(.bar) { display:none; }
.shortcut-content-preview :deep(.code-block) { border:0; }
.shortcut-content-preview :deep(.body) { overflow:visible; }
.shortcut-content-preview > p { margin:20px; color:var(--el-text-color-placeholder); font-size:12px; }
.shortcut-insert-hint { display:flex; align-items:center; gap:4px; color:var(--el-color-primary); font-size:11px; opacity:0; transition:opacity 120ms; }
.shortcut-item:hover .shortcut-insert-hint,.shortcut-item:focus-within .shortcut-insert-hint { opacity:1; }
.shortcut-actions { position:absolute; display:flex; gap:2px; top:10px; right:8px; opacity:0; transition:opacity 120ms; }
.shortcut-item:hover .shortcut-actions,.shortcut-item:focus-within .shortcut-actions { opacity:1; }
.shortcut-actions button { display:grid; place-items:center; width:27px; height:27px; padding:0; border:0; border-radius:7px; background:var(--el-bg-color); color:var(--el-text-color-secondary); cursor:pointer; }
.shortcut-actions button:hover { background:var(--el-fill-color-light); color:var(--el-color-primary); }.shortcut-actions .danger:hover { color:var(--el-color-danger); }
button:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:-2px; }
.shortcut-state { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:12px; min-height:220px; padding:32px 12px; text-align:center; color:var(--el-text-color-secondary); font-size:13px; line-height:1.7; }
.shortcut-state strong { color:var(--el-text-color-primary); font-weight:550; }.shortcut-state p { max-width:280px; margin:0; font-size:12px; overflow-wrap:anywhere; }.shortcut-state.error { color:var(--el-color-danger); }
.empty-mark { display:grid; place-items:center; width:46px; height:46px; border:1px solid var(--el-border-color-light); border-radius:14px; background:var(--el-fill-color-lighter); color:var(--el-text-color-placeholder); font-size:22px; }
.shortcut-link { padding:3px 8px; border:0; border-radius:5px; background:transparent; color:var(--el-color-primary); font:inherit; font-size:12px; cursor:pointer; }
.shortcut-editor-form { display:flex; flex-direction:column; gap:19px; }.shortcut-field { display:flex; flex-direction:column; gap:8px; min-width:0; font-size:13px; font-weight:500; }
.shortcut-field :deep(textarea) { font-family:inherit; line-height:1.7; min-height:200px; max-height:45dvh; }.shortcut-byte-count { align-self:flex-end; color:var(--el-text-color-placeholder); font-size:11px; font-weight:400; }
.shortcut-icon-field { display:flex; flex-wrap:wrap; align-items:center; gap:8px 12px; font-size:12px; }.shortcut-field-label small { margin-left:4px; color:var(--el-text-color-placeholder); font-size:11px; }.shortcut-icon-picker { width:160px; }.shortcut-icon-note { color:var(--el-text-color-placeholder); font-size:11px; }
.shortcut-form-error,.shortcut-form-note { margin:0 0 10px; font-size:12px; line-height:1.6; text-align:left; overflow-wrap:anywhere; }.shortcut-form-error { color:var(--el-color-danger); }.shortcut-form-note { color:var(--el-text-color-secondary); }.shortcut-delete-copy { margin:0; line-height:1.8; overflow-wrap:anywhere; }
@media(hover:none),(pointer:coarse) { .shortcut-actions,.shortcut-insert-hint { opacity:1; } }
@media(max-width:440px) { .shortcuts-head { padding:16px 14px 12px; }.shortcuts-search { margin:0 14px 12px; }.shortcuts-body { padding:0 14px 14px; } }
</style>
<style scoped>
.shortcuts-panel{min-width:0}.shortcuts-head{padding:18px 22px 12px}.shortcuts-heading h2{font-size:15px}.shortcuts-head-actions{flex-wrap:wrap;justify-content:flex-end}.shortcut-folder-add{display:inline-flex;align-items:center;gap:5px;min-height:32px;padding:6px 9px;border:1px solid var(--el-border-color-light);border-radius:8px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:12px;cursor:pointer}.shortcut-folder-add:hover:not(:disabled){border-color:var(--pg-accent-line);color:var(--el-color-primary)}.shortcut-folder-list{display:flex;flex-direction:column;gap:8px;margin:0 0 14px;padding:0;list-style:none}.shortcut-folder-item{display:flex;align-items:center;gap:8px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-fill-color-lighter);min-width:0}.shortcut-folder-open{display:flex;align-items:center;gap:11px;min-width:0;flex:1;padding:13px 14px;border:0;background:none;color:var(--el-text-color-primary);text-align:left;cursor:pointer}.shortcut-folder-open>.el-icon:first-child{font-size:25px;color:var(--el-color-primary);flex:none}.shortcut-folder-open>span{display:flex;flex-direction:column;gap:4px;min-width:0}.shortcut-folder-open strong{font-size:13px;font-weight:550;overflow-wrap:anywhere}.shortcut-folder-open small{font-size:10px;color:var(--el-text-color-secondary)}.folder-enter{margin-left:auto;color:var(--el-text-color-placeholder);flex:none;font-size:12px}.shortcut-folder-actions,.shortcut-inline-actions{display:flex;gap:2px;flex:none;padding:0 8px 0 0}.shortcut-folder-actions button,.shortcut-inline-actions button{display:grid;place-items:center;width:28px;height:28px;padding:0;border:0;border-radius:6px;background:none;color:var(--el-text-color-secondary);cursor:pointer}.shortcut-folder-actions button:hover,.shortcut-inline-actions button:hover{background:var(--el-fill-color);color:var(--el-color-primary)}.shortcut-folder-actions .danger:hover,.shortcut-inline-actions .danger:hover{color:var(--el-color-danger)}.shortcut-entry-heading{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:12px 12px 0;min-width:0}.shortcut-entry-heading :deep(.shortcut-reference){min-width:0;flex:1}.shortcut-entry-heading :deep(.shortcut-reference-trigger){border:0;background:none;font-size:13px;font-weight:550;max-width:100%;margin:0;padding:0;color:var(--el-text-color-primary);line-height:1.7}.shortcut-entry-heading :deep(.shortcut-reference-trigger:hover){color:var(--el-color-primary)}.shortcut-content{padding:9px 14px 13px;gap:9px}.shortcut-summary{min-height:35px}.shortcut-card-foot{display:flex;align-items:center;justify-content:space-between;gap:10px;min-height:19px}.shortcut-insert-hint{opacity:1;margin-left:auto}.source-only .shortcut-entry-heading{padding-top:14px}.shortcut-folder-form{display:flex;flex-direction:column;gap:14px}.shortcut-folder-form>p{margin:0;color:var(--el-text-color-secondary);font-size:12px;line-height:1.7;overflow-wrap:anywhere}.shortcut-folder-form label{display:flex;flex-direction:column;gap:9px;font-size:12px}.shortcut-inline-actions{padding:0}.shortcuts-body:has(.workspace-presets){padding-top:0}@media(max-width:600px){.shortcuts-head{grid-template-columns:minmax(0,1fr);gap:11px}.shortcuts-head-actions{justify-content:flex-start}.shortcuts-head p{grid-column:1}.shortcut-entry-heading{align-items:flex-start}.shortcuts-head-actions .shortcut-add,.shortcut-folder-add{font-size:11px}.shortcuts-search{margin-inline:14px}.shortcuts-body{padding-inline:14px}}
</style>
