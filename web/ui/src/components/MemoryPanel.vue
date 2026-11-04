<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Close, Plus, Refresh } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { MemoryCatalog, MemoryNote, MemoryNoteType } from "@/api/types";
import ConfirmDialog from "./ConfirmDialog.vue";

const props = defineProps<{
  active: boolean;
  sessionId: string | null;
}>();
const emit = defineEmits<{ close: [] }>();
const { t, locale } = useI18n();
const notes = ref<MemoryNote[]>([]);
const scope = ref<MemoryCatalog["scope"]>("agent");
const directory = ref("");
const loading = ref(false);
const loadError = ref("");
const filter = ref<"all" | MemoryNoteType>("all");
const selectedPath = ref("");
const creating = ref(false);
const newName = ref("");
const draft = ref("");
const draftType = ref<MemoryNoteType>("project");
const saving = ref(false);
const deleting = ref(false);
const notice = ref<{ kind: "success" | "error"; text: string } | null>(null);
const leaveConfirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
let generation = 0;

const selected = computed(() => notes.value.find(note => note.path === selectedPath.value) ?? null);
const filteredNotes = computed(() => filter.value === "all" ? notes.value : notes.value.filter(note => note.type === filter.value));
const editorOpen = computed(() => creating.value || !!selected.value);
const dirty = computed(() => {
  if (creating.value) return !!(newName.value || draft.value.trim());
  return !!selected.value && (draft.value !== selected.value.content || draftType.value !== selected.value.type);
});
const busy = computed(() => saving.value || deleting.value);

function reset() {
  generation++;
  notes.value = [];
  directory.value = "";
  selectedPath.value = "";
  creating.value = false;
  newName.value = "";
  draft.value = "";
  draftType.value = "project";
  loadError.value = "";
  notice.value = null;
}
function applySelection(note: MemoryNote) {
  creating.value = false;
  selectedPath.value = note.path;
  newName.value = note.path;
  draftType.value = note.type;
  draft.value = note.content;
  notice.value = null;
}
async function confirmLeave() {
  if (!dirty.value || saving.value) return true;
  return await leaveConfirm.value?.ask({
    title: t("memoryPanel.discardTitle"), body: t("memoryPanel.discardBody"),
    confirmText: t("memoryPanel.discard"), cancelText: t("memoryPanel.keepEditing"), danger: true,
  }) ?? false;
}
async function select(note: MemoryNote) {
  if (note.path === selectedPath.value && !creating.value) return;
  if (await confirmLeave()) applySelection(note);
}
async function createNote() {
  if (!(await confirmLeave())) return;
  selectedPath.value = "";
  creating.value = true;
  newName.value = "";
  draftType.value = "project";
  draft.value = "";
  notice.value = null;
}
async function load() {
  const sessionId = props.sessionId;
  if (!sessionId) return;
  const request = ++generation;
  loading.value = true;
  loadError.value = "";
  try {
    const result = await api.memoryNotes(sessionId);
    if (request !== generation || sessionId !== props.sessionId) return;
    // Compare against the previous server version before replacing the notes.
    // An external update must not turn an unchanged editor into a stale draft.
    const preserveDraft = dirty.value;
    notes.value = result.notes;
    scope.value = result.scope;
    directory.value = result.directory;
    const current = result.notes.find(note => note.path === selectedPath.value);
    if (current) {
      if (!creating.value && !preserveDraft) applySelection(current);
    } else if (!creating.value && result.notes.length) applySelection(result.notes[0]);
    else if (!creating.value) {
      selectedPath.value = "";
      draft.value = "";
    }
  } catch (error) {
    if (request === generation) loadError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (request === generation) loading.value = false;
  }
}
async function save() {
  if (busy.value) return;
  const path = creating.value ? newName.value.trim() : selected.value?.path;
  if (!path || !/^[a-z0-9][a-z0-9-]{0,47}\.md$/.test(path) || !draft.value.trim()) {
    notice.value = { kind: "error", text: t("memoryPanel.nameHint") };
    return;
  }
  saving.value = true;
  notice.value = null;
  try {
    const saved = await api.saveMemoryNote(props.sessionId!, { path, type: draftType.value, content: draft.value });
    const index = notes.value.findIndex(note => note.path === saved.path);
    if (index >= 0) notes.value[index] = saved;
    else notes.value.push(saved);
    applySelection(saved);
    notice.value = { kind: "success", text: t("memoryPanel.saved") };
  } catch (error) {
    notice.value = { kind: "error", text: error instanceof Error ? error.message : String(error) };
  } finally {
    saving.value = false;
  }
}
async function remove() {
  const note = selected.value;
  if (!note || busy.value) return;
  const confirmed = await leaveConfirm.value?.ask({
    title: t("memoryPanel.deleteTitle"), body: t("memoryPanel.deleteBody", { path: note.path }),
    confirmText: t("memoryPanel.confirmDelete"), cancelText: t("memoryPanel.keepEditing"), danger: true,
  });
  if (!confirmed) return;
  deleting.value = true;
  notice.value = null;
  try {
    await api.deleteMemoryNote(props.sessionId!, note.path);
    notes.value = notes.value.filter(item => item.path !== note.path);
    selectedPath.value = "";
    creating.value = false;
    draft.value = "";
    if (filteredNotes.value.length) applySelection(filteredNotes.value[0]);
    notice.value = { kind: "success", text: t("memoryPanel.deleted") };
  } catch (error) {
    notice.value = { kind: "error", text: error instanceof Error ? error.message : String(error) };
  } finally {
    deleting.value = false;
  }
}
watch(filter, async () => {
  if (selected.value && filteredNotes.value.some(note => note.path === selected.value?.path)) return;
  if (!(await confirmLeave())) return;
  if (filteredNotes.value.length) applySelection(filteredNotes.value[0]);
  else {
    selectedPath.value = "";
    creating.value = false;
    draft.value = "";
  }
});
watch([() => props.active, () => props.sessionId], ([active, sessionId], previous) => {
  if (previous?.[1] !== sessionId) reset();
  if (active && sessionId) void load();
  else generation++;
}, { immediate: true });

function typeLabel(type: MemoryNoteType) { return t(`memoryPanel.types.${type}`); }
function originLabel(note: MemoryNote) { return t(note.origin === "auto-distill" ? "memoryPanel.originAuto" : "memoryPanel.originManual"); }
function modified(note: MemoryNote) {
  const date = new Date(note.modified);
  if (date.getFullYear() <= 1 || Number.isNaN(date.getTime())) return t("memoryPanel.unknownTime");
  return new Intl.DateTimeFormat(locale.value, { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}
</script>

<template>
  <section class="memory-sheet">
    <header class="head">
      <span class="head-name">{{ t("memoryPanel.title") }}</span>
      <span class="scope">{{ t(scope === "agent" ? "memoryPanel.scopeAgent" : "memoryPanel.scopeUser") }}</span>
      <button class="ghost" :title="t('memoryPanel.refresh')" :aria-label="t('memoryPanel.refresh')" :disabled="loading" @click="load"><el-icon><Refresh /></el-icon></button>
      <button class="ghost" :title="t('common.close')" :aria-label="t('common.close')" @click="emit('close')"><el-icon><Close /></el-icon></button>
    </header>
    <div class="toolbar">
      <button type="button" class="new-note" :disabled="busy" @click="createNote"><el-icon><Plus /></el-icon>{{ t("memoryPanel.newNote") }}</button>
      <select v-model="filter" class="type-filter" :aria-label="t('memoryPanel.allTypes')">
        <option value="all">{{ t("memoryPanel.allTypes") }}</option>
        <option value="user">{{ t("memoryPanel.types.user") }}</option>
        <option value="feedback">{{ t("memoryPanel.types.feedback") }}</option>
        <option value="project">{{ t("memoryPanel.types.project") }}</option>
        <option value="reference">{{ t("memoryPanel.types.reference") }}</option>
      </select>
    </div>
    <div v-if="notice" class="notice" :class="notice.kind" role="status">{{ notice.text }}</div>
    <div v-if="loadError" class="state error" role="alert">{{ t("memoryPanel.loadFailed") }} · {{ loadError }}<button type="button" @click="load">{{ t("memoryPanel.retry") }}</button></div>
    <div v-else-if="loading && !notes.length" class="state" role="status">{{ t("skillsPanel.loading") }}</div>
    <div v-else class="memory-layout">
      <div class="note-list" role="listbox" :aria-label="t('memoryPanel.title')">
        <p v-if="!notes.length" class="empty">{{ t("memoryPanel.empty") }}</p>
        <p v-else-if="!filteredNotes.length" class="empty">{{ t("memoryPanel.noMatch") }}</p>
        <button v-for="note in filteredNotes" :key="note.path" type="button" class="note-row" :class="{ selected: note.path === selectedPath && !creating }" role="option" :aria-selected="note.path === selectedPath && !creating" @click="select(note)">
          <span class="note-path" :title="note.path">{{ note.path }}</span>
          <span class="note-facts"><span class="note-type" :data-type="note.type">{{ typeLabel(note.type) }}</span><span>{{ modified(note) }}</span></span>
        </button>
      </div>
      <div v-if="editorOpen" class="editor">
        <div class="editor-heading">
          <strong>{{ creating ? t("memoryPanel.create") : newName }}</strong>
          <span v-if="dirty" class="dirty">{{ t("memoryPanel.unsaved") }}</span>
        </div>
        <label v-if="creating" class="field">
          <span>{{ t("memoryPanel.nameLabel") }}</span>
          <input v-model="newName" :placeholder="t('memoryPanel.nameHint')" :disabled="busy" spellcheck="false" />
        </label>
        <label class="field">
          <span>{{ t("memoryPanel.typeLabel") }}</span>
          <select v-model="draftType" :disabled="busy">
            <option value="user">{{ t("memoryPanel.types.user") }}</option>
            <option value="feedback">{{ t("memoryPanel.types.feedback") }}</option>
            <option value="project">{{ t("memoryPanel.types.project") }}</option>
            <option value="reference">{{ t("memoryPanel.types.reference") }}</option>
          </select>
        </label>
        <div v-if="selected" class="provenance">
          <span>{{ originLabel(selected) }}</span>
          <span>{{ modified(selected) }}</span>
          <span v-if="selected.source_session" :title="selected.source_session">{{ t("memoryPanel.source") }} · {{ selected.source_session }}</span>
        </div>
        <label class="field content-field">
          <span>{{ t("memoryPanel.contentLabel") }}</span>
          <textarea v-model="draft" :disabled="busy" spellcheck="false" />
        </label>
        <div class="editor-actions">
          <button v-if="selected" type="button" class="delete" :disabled="busy" @click="remove">{{ t(deleting ? "memoryPanel.deleting" : "memoryPanel.delete") }}</button>
          <button type="button" class="save" :disabled="busy || !dirty" @click="save">{{ t(saving ? "memoryPanel.saving" : "memoryPanel.save") }}</button>
        </div>
      </div>
      <div v-else class="editor-placeholder">{{ t("memoryPanel.empty") }}</div>
    </div>
    <footer v-if="directory" class="directory" :title="directory"><span>{{ t("memoryPanel.directory") }}</span><code>{{ directory }}</code></footer>
    <ConfirmDialog ref="leaveConfirm" />
  </section>
</template>

<style scoped>
.memory-sheet{display:flex;flex-direction:column;height:100%;min-height:0;background:var(--el-bg-color);color:var(--el-text-color-primary)}
.head{display:flex;align-items:center;gap:8px;min-height:44px;padding:6px 10px;border-bottom:1px solid var(--el-border-color-lighter)}
.head-name{flex:1;font-size:14px;font-weight:600}.scope{padding:2px 7px;border-radius:999px;background:var(--el-fill-color-light);color:var(--el-text-color-secondary);font-size:10px;white-space:nowrap}
.ghost{display:inline-flex;align-items:center;justify-content:center;width:28px;height:28px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary);cursor:pointer}.ghost:hover{background:var(--el-fill-color-light);color:var(--el-color-primary)}.ghost:disabled{opacity:.45;cursor:default}
.toolbar{display:flex;align-items:center;gap:8px;padding:10px;border-bottom:1px solid var(--el-border-color-lighter)}
.new-note,.save{display:inline-flex;align-items:center;justify-content:center;gap:5px;padding:6px 10px;border:1px solid var(--el-color-primary);border-radius:6px;background:var(--el-color-primary);color:var(--el-color-white);font:inherit;font-size:12px;cursor:pointer}.new-note:disabled,.save:disabled{opacity:.45;cursor:default}
.type-filter,.field select,.field input{box-sizing:border-box;padding:6px 8px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;font-size:12px}.type-filter{margin-left:auto;max-width:150px}
.notice{padding:8px 12px;border-bottom:1px solid var(--el-border-color-lighter);font-size:12px;line-height:1.5}.notice.success{background:var(--el-color-success-light-9);color:var(--el-color-success)}.notice.error,.state.error{background:var(--el-color-danger-light-9);color:var(--el-color-danger)}
.state{margin:auto;padding:24px;color:var(--el-text-color-secondary);font-size:13px;text-align:center}.state button{display:block;margin:10px auto 0;padding:5px 10px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:inherit;cursor:pointer}
.memory-layout{display:grid;grid-template-columns:minmax(145px,190px) minmax(0,1fr);flex:1;min-height:0;overflow:hidden}
.note-list{min-height:0;overflow-y:auto;border-right:1px solid var(--el-border-color-lighter);padding:6px;background:var(--el-bg-color-page)}
.note-row{display:flex;flex-direction:column;align-items:flex-start;gap:5px;width:100%;padding:9px 8px;border:0;border-radius:7px;background:transparent;color:var(--el-text-color-regular);text-align:left;cursor:pointer}.note-row:hover{background:var(--el-fill-color-light)}.note-row.selected{background:var(--el-color-primary-light-9);color:var(--el-color-primary)}
.note-path{max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;font-weight:550}.note-facts{display:flex;align-items:center;gap:6px;max-width:100%;color:var(--el-text-color-secondary);font-size:10px}.note-facts>span:last-child{white-space:nowrap}.note-type{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.empty{padding:12px 8px;color:var(--el-text-color-secondary);font-size:12px;line-height:1.7}
.editor{display:flex;flex-direction:column;gap:12px;min-width:0;min-height:0;overflow-y:auto;padding:16px}.editor-heading{display:flex;align-items:center;gap:8px;min-width:0}.editor-heading strong{min-width:0;overflow-wrap:anywhere;font-size:14px}.dirty{color:var(--el-color-warning);font-size:11px;white-space:nowrap}
.field{display:flex;flex-direction:column;gap:5px;color:var(--el-text-color-secondary);font-size:11px}.field input,.field select{width:100%}.content-field{flex:1;min-height:180px}.content-field textarea{box-sizing:border-box;width:100%;flex:1;min-height:180px;resize:vertical;padding:12px;border:1px solid var(--el-border-color);border-radius:8px;outline:none;background:var(--el-bg-color);color:var(--el-text-color-primary);font:12px/1.8 var(--pg-mono,monospace)}.content-field textarea:focus{border-color:var(--el-color-primary)}
.provenance{display:flex;flex-wrap:wrap;gap:6px;color:var(--el-text-color-secondary);font-size:10px}.provenance span{padding:3px 6px;border-radius:5px;background:var(--el-fill-color-light);overflow-wrap:anywhere}
.editor-actions{display:flex;align-items:center;justify-content:flex-end;gap:8px}.delete{margin-right:auto;padding:6px 8px;border:0;border-radius:6px;background:transparent;color:var(--el-color-danger);font:inherit;font-size:12px;cursor:pointer}.delete:hover{background:var(--el-color-danger-light-9)}.delete:disabled{opacity:.45}
.editor-placeholder{display:flex;align-items:center;justify-content:center;padding:24px;color:var(--el-text-color-secondary);font-size:12px;line-height:1.8;text-align:center}
.directory{display:flex;align-items:flex-start;gap:8px;padding:8px 12px;border-top:1px solid var(--el-border-color-lighter);color:var(--el-text-color-secondary);font-size:10px;line-height:1.5}.directory span{flex:0 0 auto}.directory code{min-width:0;overflow-wrap:anywhere;font:10px/1.5 var(--pg-mono,monospace)}
@media(max-width:560px){.memory-layout{grid-template-columns:1fr;grid-template-rows:minmax(100px,180px) minmax(0,1fr);overflow-y:auto}.note-list{border-right:0;border-bottom:1px solid var(--el-border-color-lighter)}.editor{overflow:visible}}
</style>
