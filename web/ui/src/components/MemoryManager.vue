<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Plus, Refresh, Search } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { ManagedMemoryNoteInput, ManagedMemoryScope, MemoryNote, MemoryNoteType } from "@/api/types";
import { randomUUID } from "@/utils/uuid";
import ConfirmDialog from "./ConfirmDialog.vue";
import MarkdownView from "./MarkdownView.vue";
import StudioEntrySkeleton from "./studio/StudioEntrySkeleton.vue";

const props = defineProps<{ active: boolean; initialAgentId?: string }>();
const emit = defineEmits<{ "dirty-change": [dirty: boolean]; "auto-change": [enabled: boolean] }>();
const { t, locale } = useI18n();
const scopes = ref<ManagedMemoryScope[]>([]);
const scopeFilter = ref("");
const typeFilter = ref<"all" | MemoryNoteType>("all");
const search = ref("");
const selectedKey = ref("");
const loading = ref(false);
const loadError = ref("");
const catalogReady = ref(false);
// Awaiting a leave guard can precede the request. Keep the first frame in
// the loading layout until the catalog has supplied real values.
const initialLoading = computed(() => props.active && !catalogReady.value && !loadError.value);
const notice = ref<{ kind: "success" | "error"; text: string } | null>(null);
const editing = ref(false);
const creating = ref(false);
const draftScope = ref("");
const draftPath = ref("");
const draft = ref("");
const draftType = ref<MemoryNoteType>("project");
const originalContent = ref("");
const originalType = ref<MemoryNoteType>("project");
const originalModified = ref("");
const conflicted = ref(false);
const conflictRefreshed = ref(false);
const saving = ref(false);
const deleting = ref(false);
const autoSaving = ref(false);
const autoGenerate = ref(false);
const autoEditable = ref(false);
const leaveConfirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
let loadGeneration = 0;
let lifetime = 0;
let alive = true;

const busy = computed(() => saving.value || deleting.value || autoSaving.value);
const dirty = computed(() => editing.value && (creating.value
  ? !!draft.value.trim()
  : draft.value !== originalContent.value || draftType.value !== originalType.value));
watch([dirty, busy], ([changed, pending]) => emit("dirty-change", changed || pending), { immediate: true });
type Entry = { key: string; scope: ManagedMemoryScope; note: MemoryNote };
function noteKey(scopeId: string, path: string) { return JSON.stringify([scopeId, path]); }
const entries = computed<Entry[]>(() => scopes.value.flatMap(scope => scope.notes.map(note => ({
  key: noteKey(scope.id, note.path), scope, note,
}))));
const selectedScope = computed(() => scopes.value.find(scope => scope.id === scopeFilter.value));
const filtered = computed(() => {
  const query = search.value.trim().toLocaleLowerCase(locale.value);
  return entries.value.filter(entry => (!scopeFilter.value || entry.scope.id === scopeFilter.value
      || (selectedScope.value?.scope === "agent" && entry.scope.scope !== "agent"))
    && (typeFilter.value === "all" || entry.note.type === typeFilter.value)
    && (!query || `${title(entry.note)}\n${entry.note.content}\n${scopeLabel(entry.scope)}`.toLocaleLowerCase(locale.value).includes(query)))
    .sort((a, b) => (Date.parse(b.note.modified) || 0) - (Date.parse(a.note.modified) || 0)
      || title(a.note).localeCompare(title(b.note), locale.value));
});
const selected = computed(() => entries.value.find(entry => entry.key === selectedKey.value) ?? null);
const editScope = computed(() => scopes.value.find(scope => scope.id === draftScope.value));
const latestStored = computed(() => editScope.value?.notes.find(note => note.path === draftPath.value) ?? null);
const writableScopes = computed(() => scopes.value.filter(scope => scope.writable));
const noteTypes: MemoryNoteType[] = ["user", "feedback", "project", "reference"];
function defaultSelection() {
  return (filtered.value.find(entry => entry.scope.id === scopeFilter.value) ?? filtered.value[0])?.key ?? "";
}

function title(note: MemoryNote): string {
  const lines = note.content.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  const heading = lines.find(line => /^#{1,6}\s+\S/.test(line));
  const text = (heading ?? lines[0] ?? note.path.replace(/\.md$/, "")).replace(/^#{1,6}\s+/, "").replace(/[*_`]/g, "");
  return text.length > 100 ? `${text.slice(0, 100)}…` : text;
}
function scopeLabel(scope: ManagedMemoryScope): string {
  if (scope.scope === "user") return t("memoryPanel.personalScope");
  if (scope.scope === "project") return scope.label || t("memoryPanel.projectScope");
  return scope.label || scope.agent_id || t("memoryPanel.agentScope");
}
function modified(note: MemoryNote): string {
  const value = new Date(note.modified);
  if (!Number.isFinite(value.getTime()) || value.getFullYear() <= 1) return t("memoryPanel.unknownTime");
  return new Intl.DateTimeFormat(locale.value, { year: "numeric", month: "short", day: "numeric" }).format(value);
}
function stopEditing() {
  editing.value = false;
  creating.value = false;
  draft.value = "";
  draftPath.value = "";
  originalModified.value = "";
  conflicted.value = false;
  conflictRefreshed.value = false;
}
function isVersionConflict(error: unknown): boolean {
  return !!error && typeof error === "object" && "status" in error && error.status === 409;
}
async function confirmLeave(): Promise<boolean> {
  if (busy.value) return false;
  if (dirty.value) {
    const discard = await leaveConfirm.value?.ask({
      title: t("memoryPanel.discardTitle"), body: t("memoryPanel.discardBody"),
      confirmText: t("memoryPanel.discard"), cancelText: t("memoryPanel.keepEditing"), danger: true,
    });
    if (!discard) return false;
  }
  stopEditing();
  return true;
}
defineExpose({ confirmLeave });

async function load() {
  if (busy.value || !props.active) return;
  const request = ++loadGeneration;
  const epoch = lifetime;
  loading.value = true;
  loadError.value = "";
  try {
    const result = await api.managedMemoryCatalog();
    // A catalog read started before a save must not replace the scope object
    // that the pending write will update, or restore a just-deleted memory.
    if (!alive || request !== loadGeneration || epoch !== lifetime || !props.active || busy.value) return;
    scopes.value = result.scopes;
    autoGenerate.value = result.auto_generate;
    autoEditable.value = result.auto_generate_editable;
    catalogReady.value = true;
    if (conflicted.value && editing.value) conflictRefreshed.value = true;
    if (scopeFilter.value && !result.scopes.some(scope => scope.id === scopeFilter.value)) scopeFilter.value = "";
    if (!editing.value && !entries.value.some(entry => entry.key === selectedKey.value)) selectedKey.value = defaultSelection();
  } catch (error) {
    if (alive && request === loadGeneration && epoch === lifetime) loadError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (alive && request === loadGeneration && epoch === lifetime) loading.value = false;
  }
}
async function select(entry: Entry) {
  if (entry.key === selectedKey.value && !creating.value) return;
  if (!(await confirmLeave())) return;
  selectedKey.value = entry.key;
  notice.value = null;
}
async function changeScope(value: string) {
  if (!(await confirmLeave())) return;
  scopeFilter.value = value;
  selectedKey.value = defaultSelection();
  notice.value = null;
}
async function onScopeChange(event: Event) {
  const select = event.target as HTMLSelectElement;
  await changeScope(select.value);
  // A cancelled asynchronous guard must restore the native select too: its
  // displayed value has already changed before Vue sees a new reactive value.
  select.value = scopeFilter.value;
}
async function createNote() {
  if (!(await confirmLeave())) return;
  const scope = writableScopes.value.find(item => item.id === scopeFilter.value)
    ?? writableScopes.value.find(item => item.agent_id === props.initialAgentId)
    ?? writableScopes.value.find(item => item.scope === "user") ?? writableScopes.value[0];
  if (!scope) return;
  draftScope.value = scope.id;
  draftPath.value = `memory-${randomUUID().replace(/-/g, "").slice(0, 24)}.md`;
  draftType.value = scope.scope === "user" ? "user" : "project";
  draft.value = "";
  creating.value = true;
  editing.value = true;
  notice.value = null;
}
function editNote() {
  const entry = selected.value;
  if (!entry?.scope.writable || busy.value) return;
  draftScope.value = entry.scope.id;
  draftPath.value = entry.note.path;
  draft.value = entry.note.content;
  draftType.value = entry.note.type;
  originalContent.value = entry.note.content;
  originalType.value = entry.note.type;
  originalModified.value = entry.note.modified;
  conflicted.value = false;
  conflictRefreshed.value = false;
  creating.value = false;
  editing.value = true;
  notice.value = null;
}
async function save() {
  const scope = editScope.value;
  if (busy.value || !editing.value || !scope?.writable || conflicted.value) return;
  if (!draft.value.trim()) {
    notice.value = { kind: "error", text: t("memoryPanel.contentRequired") };
    return;
  }
  saving.value = true;
  loadGeneration++;
  loading.value = false;
  notice.value = null;
  const epoch = lifetime;
  const input: ManagedMemoryNoteInput = { path: draftPath.value, type: draftType.value, content: draft.value,
    ...(creating.value ? { create_only: true } : { expected_modified: originalModified.value }),
  };
  let refreshConflict = false;
  try {
    const result = await api.saveManagedMemoryNote(scope.id, input);
    if (!alive || epoch !== lifetime) return;
    const index = scope.notes.findIndex(note => note.path === result.path);
    if (index >= 0) scope.notes[index] = result;
    else scope.notes.push(result);
    scopeFilter.value = scope.id;
    typeFilter.value = "all";
    search.value = "";
    selectedKey.value = noteKey(scope.id, result.path);
    stopEditing();
    notice.value = { kind: "success", text: t("memoryPanel.saved") };
  } catch (error) {
    if (alive && epoch === lifetime) {
      const conflict = isVersionConflict(error);
      notice.value = { kind: "error", text: conflict ? t("memoryPanel.conflictHint") : error instanceof Error ? error.message : String(error) };
      if (conflict) {
        conflicted.value = true;
        conflictRefreshed.value = false;
        refreshConflict = true;
      }
    }
  } finally {
    if (alive) {
      saving.value = false;
      if ((epoch !== lifetime || refreshConflict) && props.active) await load();
    }
  }
}
function continueWithLatest() {
  const current = latestStored.value;
  if (busy.value || !conflicted.value || !conflictRefreshed.value || creating.value || !current) return;
  // Explicitly acknowledge the reviewed version without changing or writing
  // the user's draft. A later save still checks this exact token on the host.
  originalModified.value = current.modified;
  originalContent.value = current.content;
  originalType.value = current.type;
  conflicted.value = false;
  conflictRefreshed.value = false;
  notice.value = null;
}
async function saveDraftAsNew() {
  if (busy.value || !conflicted.value || !conflictRefreshed.value || !editScope.value?.writable
    || (!creating.value && latestStored.value)) return;
  draftPath.value = `memory-${randomUUID().replace(/-/g, "").slice(0, 24)}.md`;
  originalModified.value = "";
  creating.value = true;
  conflicted.value = false;
  conflictRefreshed.value = false;
  await save();
}
async function forget() {
  const entry = selected.value;
  if (!entry?.scope.writable || busy.value) return;
  const epoch = lifetime;
  const expectedModified = entry.note.modified;
  const confirmed = await leaveConfirm.value?.ask({
    title: t("memoryPanel.forgetTitle"), body: t("memoryPanel.forgetBody"),
    confirmText: t("memoryPanel.forgetAction"), cancelText: t("memoryPanel.cancel"), danger: true,
  });
  if (!confirmed || busy.value || epoch !== lifetime || selected.value?.key !== entry.key) return;
  deleting.value = true;
  loadGeneration++;
  loading.value = false;
  notice.value = null;
  let refreshConflict = false;
  try {
    await api.deleteManagedMemoryNote(entry.scope.id, entry.note.path, expectedModified);
    if (!alive || epoch !== lifetime) return;
    entry.scope.notes = entry.scope.notes.filter(note => note.path !== entry.note.path);
    stopEditing();
    selectedKey.value = defaultSelection();
    notice.value = { kind: "success", text: t("memoryPanel.forgot") };
  } catch (error) {
    if (alive && epoch === lifetime) {
      refreshConflict = isVersionConflict(error);
      notice.value = { kind: "error", text: refreshConflict ? t("memoryPanel.conflictHint") : error instanceof Error ? error.message : String(error) };
    }
  } finally {
    if (alive) {
      deleting.value = false;
      if ((epoch !== lifetime || refreshConflict) && props.active) await load();
    }
  }
}
async function setAuto(value: boolean | string | number) {
  if (busy.value || !autoEditable.value) return;
  autoSaving.value = true;
  loadGeneration++;
  loading.value = false;
  notice.value = null;
  const epoch = lifetime;
  try {
    const result = await api.setMemoryAuto(value === true);
    if (alive) {
      emit("auto-change", result.memory_auto);
      if (epoch === lifetime) autoGenerate.value = result.memory_auto;
    }
  } catch (error) {
    if (alive && epoch === lifetime) notice.value = { kind: "error", text: error instanceof Error ? error.message : String(error) };
  } finally {
    if (alive) {
      autoSaving.value = false;
      if (epoch !== lifetime && props.active) void load();
    }
  }
}
watch([() => props.active, () => props.initialAgentId], async ([active, agentId], old) => {
  lifetime++;
  loadGeneration++;
  loading.value = false;
  if (!active) return;
  if (old?.[1] !== agentId) {
    if (!(await confirmLeave())) return;
    selectedKey.value = "";
    scopeFilter.value = "";
  }
  await load();
  if (props.active && props.initialAgentId === agentId && agentId && !dirty.value) {
    const scope = scopes.value.find(item => item.agent_id === agentId);
    if (scope) {
      scopeFilter.value = scope.id;
      selectedKey.value = defaultSelection();
    }
  }
}, { immediate: true });
onBeforeUnmount(() => { alive = false; lifetime++; loadGeneration++; emit("dirty-change", false); });
</script>

<template>
  <section class="memory-manager" data-testid="memory-manager" :aria-busy="initialLoading">
    <div class="auto-row">
      <div>
        <strong>{{ t("memoryPanel.autoTitle") }}</strong>
        <p>{{ t("memoryPanel.autoHint") }}</p>
        <p v-if="catalogReady && !autoEditable" class="muted">{{ t("memoryPanel.autoAdminOnly") }}</p>
      </div>
      <StudioEntrySkeleton v-if="initialLoading" class="auto-placeholder" area="agent" />
      <el-switch v-else-if="catalogReady" :model-value="autoGenerate" :disabled="!autoEditable || busy || loading" :loading="autoSaving" :aria-label="t('memoryPanel.autoTitle')" @change="setAuto" />
    </div>
    <div class="memory-toolbar">
      <label class="scope-picker"><span>{{ t("memoryPanel.selectScope") }}</span>
        <select :value="scopeFilter" :disabled="busy || !catalogReady" @change="onScopeChange">
          <option value="">{{ t("memoryPanel.allScopes") }}</option>
          <option v-for="scope in scopes" :key="scope.id" :value="scope.id">{{ scopeLabel(scope) }}</option>
        </select>
      </label>
      <label class="search-box"><el-icon><Search /></el-icon><input v-model="search" :placeholder="t('memoryPanel.searchHint')" :aria-label="t('memoryPanel.search')" :disabled="busy" /></label>
      <select v-model="typeFilter" :disabled="busy" :aria-label="t('memoryPanel.allTypes')">
        <option value="all">{{ t("memoryPanel.allTypes") }}</option>
        <option v-for="kind in noteTypes" :key="kind" :value="kind">{{ t(`memoryPanel.types.${kind}`) }}</option>
      </select>
      <button type="button" class="icon-button" :title="t('memoryPanel.refresh')" :aria-label="t('memoryPanel.refresh')" :disabled="loading || busy" @click="load"><el-icon><Refresh /></el-icon></button>
      <button type="button" class="solid-button" :disabled="busy || loading || !writableScopes.length" @click="createNote"><el-icon><Plus /></el-icon>{{ t("memoryPanel.newNote") }}</button>
    </div>
    <p v-if="selectedScope?.scope === 'agent'" class="shared-hint">{{ t("memoryPanel.agentIncludesShared") }}</p>
    <div v-if="notice" class="notice" :class="notice.kind" role="status">{{ notice.text }}</div>
    <div v-if="loadError" class="load-error" role="alert">{{ t("memoryPanel.loadFailed") }} · {{ loadError }} <button type="button" @click="load">{{ t("memoryPanel.retry") }}</button></div>
    <div v-if="initialLoading" class="memory-content" role="status" :aria-label="t('skillsPanel.loading')">
      <div class="memory-list"><StudioEntrySkeleton area="sessions" /></div>
      <div class="memory-detail"><StudioEntrySkeleton area="document" /></div>
    </div>
    <div v-else-if="catalogReady" class="memory-content">
      <div class="memory-list" :aria-label="t('memoryPanel.title')">
        <p class="count">{{ t("memoryPanel.rememberedCount", { count: filtered.length }) }}</p>
        <p v-if="!filtered.length" class="empty">{{ t(entries.length ? "memoryPanel.noResults" : "memoryPanel.noMemories") }}</p>
        <button v-for="entry in filtered" :key="entry.key" type="button" class="memory-card" :class="{ selected: selectedKey === entry.key && !creating }" :disabled="busy" :aria-pressed="selectedKey === entry.key && !creating" @click="select(entry)">
          <strong>{{ title(entry.note) }}</strong>
          <span>{{ scopeLabel(entry.scope) }}</span>
          <span class="card-meta">{{ t(`memoryPanel.types.${entry.note.type}`) }} · {{ modified(entry.note) }}</span>
        </button>
      </div>
      <div v-if="editing" class="memory-detail editor">
        <div class="detail-head"><h4>{{ t(creating ? "memoryPanel.newNote" : "memoryPanel.editAction") }}</h4><span v-if="dirty" class="dirty">{{ t("memoryPanel.unsaved") }}</span></div>
        <label v-if="creating" class="field"><span>{{ t("memoryPanel.selectScope") }}</span>
          <select v-model="draftScope" :disabled="busy"><option v-for="scope in writableScopes" :key="scope.id" :value="scope.id">{{ scopeLabel(scope) }}</option></select>
        </label>
        <p v-else-if="editScope" class="scope-badge">{{ scopeLabel(editScope) }}</p>
        <label class="field"><span>{{ t("memoryPanel.typeLabel") }}</span><select v-model="draftType" :disabled="busy"><option v-for="kind in noteTypes" :key="kind" :value="kind">{{ t(`memoryPanel.types.${kind}`) }}</option></select></label>
        <div v-if="conflicted" class="conflict-review">
          <div v-if="conflictRefreshed && latestStored" class="latest-version">
            <strong>{{ t("memoryPanel.latestSaved") }}</strong>
            <MarkdownView :source="latestStored.content" passive />
          </div>
          <p v-if="!conflictRefreshed && loading" class="muted" role="status">{{ t("skillsPanel.loading") }}</p>
          <div class="conflict-actions">
            <button type="button" class="plain-button" :disabled="busy || loading" @click="load">{{ t("memoryPanel.refresh") }}</button>
            <button v-if="conflictRefreshed && latestStored && !creating" type="button" class="plain-button" :disabled="busy || loading" @click="continueWithLatest">{{ t("memoryPanel.continueWithLatest") }}</button>
            <button v-if="conflictRefreshed && (!latestStored || creating)" type="button" class="plain-button" :disabled="busy || loading || !editScope?.writable" @click="saveDraftAsNew">{{ t("memoryPanel.saveDraftAsNew") }}</button>
          </div>
        </div>
        <label class="field content-field"><span>{{ t("memoryPanel.contentLabel") }}</span><textarea v-model="draft" :placeholder="t('memoryPanel.contentHint')" :disabled="busy" /></label>
        <div class="detail-actions"><button type="button" class="plain-button" :disabled="busy" @click="confirmLeave">{{ t("memoryPanel.cancel") }}</button><button type="button" class="solid-button" :disabled="busy || conflicted || !draft.trim() || (!creating && !dirty)" @click="save">{{ t(saving ? "memoryPanel.saving" : "memoryPanel.save") }}</button></div>
      </div>
      <article v-else-if="selected" class="memory-detail">
        <div class="detail-head"><h4>{{ title(selected.note) }}</h4></div>
        <div class="provenance"><span class="scope-badge">{{ scopeLabel(selected.scope) }}</span><span>{{ t(`memoryPanel.types.${selected.note.type}`) }}</span><span>{{ modified(selected.note) }}</span><span>{{ t(selected.note.origin === "auto-distill" ? "memoryPanel.originAuto" : "memoryPanel.originManual") }}</span></div>
        <MarkdownView :source="selected.note.content" passive />
        <p class="source"><span>{{ t("memoryPanel.source") }}:</span> <code v-if="selected.note.source_session">{{ selected.note.source_session }}</code><span v-else>{{ t("memoryPanel.sourceUnavailable") }}</span></p>
        <div v-if="selected.scope.writable" class="detail-actions"><button type="button" class="danger-button" :disabled="busy" @click="forget">{{ t(deleting ? "memoryPanel.deleting" : "memoryPanel.forgetAction") }}</button><button type="button" class="plain-button" :disabled="busy" @click="editNote">{{ t("memoryPanel.editAction") }}</button></div>
        <p v-else class="muted">{{ t("memoryPanel.readOnly") }}</p>
        <details class="storage-details"><summary>{{ t("memoryPanel.details") }}</summary><dl><dt>{{ t("memoryPanel.fileLabel") }}</dt><dd><code>{{ selected.note.path }}</code></dd><dt>{{ t("memoryPanel.directory") }}</dt><dd><code>{{ selected.scope.directory }}</code></dd></dl></details>
      </article>
      <p v-else class="empty detail-empty">{{ t("memoryPanel.chooseMemory") }}</p>
    </div>
    <ConfirmDialog ref="leaveConfirm" />
  </section>
</template>

<style scoped>
.memory-manager{display:flex;flex-direction:column;gap:16px;min-height:420px;color:var(--el-text-color-primary)}
.auto-placeholder{flex:none;width:40px;height:20px;margin-block:6px;border-radius:10px}
.auto-row{display:flex;align-items:flex-start;justify-content:space-between;gap:24px;padding:16px;border:1px solid var(--el-border-color-lighter);border-radius:10px}.auto-row strong{font-size:13px}.auto-row p{margin:6px 0 0;font-size:12px;line-height:1.7;color:var(--el-text-color-secondary)}.auto-row .el-switch{flex-shrink:0}
.memory-toolbar{display:flex;align-items:flex-end;flex-wrap:wrap;gap:8px}.scope-picker{display:flex;flex-direction:column;gap:5px;min-width:150px;font-size:11px;color:var(--el-text-color-secondary)}
.shared-hint{margin:-8px 0 0;color:var(--el-text-color-secondary);font-size:11px;line-height:1.7}
select,input,textarea{box-sizing:border-box;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;font-size:12px}select{min-height:34px;max-width:100%;padding:5px 9px}.search-box{display:flex;align-items:center;gap:6px;flex:1;min-width:160px;height:34px;border:1px solid var(--el-border-color);border-radius:7px;padding:0 9px;color:var(--el-text-color-secondary)}.search-box input{width:100%;min-width:0;height:100%;padding:0;border:0;outline:0}
button{font:inherit;font-size:12px;cursor:pointer}button:disabled,select:disabled,input:disabled,textarea:disabled{opacity:.5;cursor:default}.solid-button,.plain-button,.danger-button,.icon-button{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:34px;padding:6px 12px;border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);color:var(--el-text-color-primary)}.solid-button{background:var(--pg-solid);border-color:var(--pg-solid);color:var(--pg-on-solid)}.danger-button{color:var(--el-color-danger);margin-right:auto}.icon-button{width:34px;padding:6px}.plain-button:hover,.icon-button:hover{background:var(--el-fill-color-light)}
.memory-content{display:grid;grid-template-columns:minmax(180px,270px) minmax(0,1fr);border:1px solid var(--el-border-color-lighter);border-radius:10px;overflow:hidden;min-height:360px}.memory-list{padding:10px;background:var(--el-bg-color-page);border-right:1px solid var(--el-border-color-lighter);max-height:680px;overflow-y:auto}.count{margin:0;padding:5px 8px 10px;font-size:11px;color:var(--el-text-color-secondary)}.memory-card{display:flex;flex-direction:column;align-items:flex-start;gap:6px;width:100%;padding:12px 10px;margin-bottom:5px;text-align:left;border:1px solid transparent;border-radius:8px;background:transparent;color:var(--el-text-color-regular);overflow-wrap:anywhere}.memory-card strong{font-size:12px;line-height:1.6;font-weight:600}.memory-card>span{font-size:11px;color:var(--el-text-color-secondary)}.memory-card:hover{background:var(--el-fill-color-light)}.memory-card.selected{border-color:var(--el-color-primary-light-7);background:var(--el-color-primary-light-9)}.memory-card.selected strong{color:var(--el-color-primary)}.memory-card .card-meta{font-size:10px}
.memory-detail{min-width:0;padding:20px;overflow-wrap:anywhere}.detail-head{display:flex;align-items:center;gap:8px;margin-bottom:12px}.detail-head h4{margin:0;font-size:15px;line-height:1.6}.provenance{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin-bottom:18px;color:var(--el-text-color-secondary);font-size:10px}.scope-badge{display:inline-block;padding:3px 7px;border-radius:5px;background:var(--el-fill-color-light);font-size:11px;color:var(--el-text-color-regular)}.source{margin-top:20px;color:var(--el-text-color-secondary);font-size:11px}.source code{font-size:10px;overflow-wrap:anywhere}.detail-actions{display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-top:18px}.storage-details{margin-top:22px;padding-top:12px;border-top:1px solid var(--el-border-color-lighter);font-size:11px;color:var(--el-text-color-secondary)}.storage-details summary{cursor:pointer}.storage-details dl{display:grid;grid-template-columns:auto 1fr;gap:8px;margin:14px 0 0}.storage-details dd{margin:0;min-width:0;overflow-wrap:anywhere}.storage-details code{font-size:10px}
.editor{display:flex;flex-direction:column;gap:12px}.editor .detail-head{margin-bottom:0}.field{display:flex;flex-direction:column;gap:6px;font-size:11px;color:var(--el-text-color-secondary)}.content-field{flex:1;min-height:200px}.content-field textarea{width:100%;min-height:200px;flex:1;resize:vertical;padding:12px;line-height:1.8}.field select{width:100%}.dirty{color:var(--el-color-warning);font-size:10px}.empty{padding:22px 10px;color:var(--el-text-color-secondary);font-size:12px;line-height:1.8}.detail-empty{margin:auto;text-align:center}.notice,.load-error{padding:10px 12px;border-radius:7px;font-size:12px;line-height:1.6}.notice.success{background:var(--el-color-success-light-9);color:var(--el-color-success)}.notice.error,.load-error{background:var(--el-color-danger-light-9);color:var(--el-color-danger)}.load-error button{border:0;background:transparent;color:inherit;text-decoration:underline}.muted{color:var(--el-text-color-secondary);font-size:11px;line-height:1.7}
.conflict-review{padding:12px;border:1px solid var(--el-color-warning-light-5);border-radius:8px;background:var(--el-color-warning-light-9)}.latest-version{max-height:230px;overflow-y:auto}.latest-version>strong{font-size:12px}.conflict-actions{display:flex;flex-wrap:wrap;gap:8px;margin-top:10px}.conflict-actions button{max-width:100%;text-align:left;line-height:1.5;white-space:normal}
@media(max-width:720px){.memory-content{grid-template-columns:1fr}.memory-list{border-right:0;border-bottom:1px solid var(--el-border-color-lighter);max-height:260px}.memory-detail{padding:16px}.auto-row{gap:12px}.scope-picker{flex:1}.memory-toolbar>.solid-button{margin-left:auto}}
</style>
