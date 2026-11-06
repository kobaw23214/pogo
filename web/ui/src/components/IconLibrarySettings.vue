<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { Icon } from '@iconify/vue';
import { Plus, Refresh, MagicStick, Close, Search } from '@element-plus/icons-vue';
import { api } from '@/api/client';
import { uiIconsApi, UIIconsAPIError, UI_ICON_MODULES, type UIIcon, type UIIconModule, type UIIconsView } from '@/api/uiIcons';
import { acceptUIIcons } from '@/agent/uiIcons';
import { starterIconFor, starterIconNames } from './starterIcons';
import AgentAvatar, { AVATAR_ICONS } from './AgentAvatar.vue';
import StudioEntrySkeleton from './studio/StudioEntrySkeleton.vue';

const props = defineProps<{active: boolean}>();
const emit = defineEmits<{dirty: [value: boolean]}>();
const {t} = useI18n();
const saved = ref<UIIconsView | null>(null);
const draft = ref<UIIconsView | null>(null);
const loading = ref(false), saving = ref(false), generating = ref(false);
const error = ref(''), notice = ref(''), prompt = ref(''), model = ref(''), count = ref(2);
const models = ref<{id: string; configured: boolean}[]>([]);
const module = ref<UIIconModule>('shortcuts');
const source = ref<'all'|'custom'|'builtin'|'archived'>('all');
const query = ref('');
const candidates = ref<UIIcon[]>([]);
const selected = ref<Set<string>>(new Set());
const generatorOpen = ref(false), editing = ref<UIIcon | null>(null);
let alive = true, generation = 0;
let generateAbort: AbortController | undefined;
const copy = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const dirty = computed(() => !!draft.value && JSON.stringify(draft.value) !== JSON.stringify(saved.value));
watch(dirty, value => emit('dirty', value), {immediate: true});
const canManage = computed(() => !!saved.value?.can_manage);
const builtin = computed(() => (module.value === 'agents' ? [...AVATAR_ICONS] : starterIconNames).filter(name => !query.value.trim() || name.toLowerCase().includes(query.value.trim().toLowerCase())));
const custom = computed(() => (draft.value?.icons ?? []).filter(icon => (!icon.modules.length || icon.modules.includes(module.value)) && (source.value === 'archived' ? icon.archived : !icon.archived) && (!query.value.trim() || icon.name.toLowerCase().includes(query.value.trim().toLowerCase()))));
const hidden = (name: string) => draft.value?.hidden_builtin[module.value].includes(name);
const moduleLabel = (m: UIIconModule) => t(`iconLibrary.module_${m}`);
const glyph = (icon: UIIcon) => ({body: `<path d="${icon.path}" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>`, width:16,height:16});
async function load() {
  if (loading.value) return;
  loading.value = true; error.value = '';
  try {
    const view = await uiIconsApi.list();
    if (!alive) return;
    const keepDraft = dirty.value;
    acceptUIIcons(view); saved.value = copy(view);
    if (!draft.value || !keepDraft) draft.value = copy(view);
    const out = await api.models();
    if (!alive) return;
    models.value = out.models;
    if (!model.value || !models.value.some(m => m.id === model.value && m.configured)) model.value = out.models.find(m => m.id === out.default && m.configured)?.id || out.models.find(m => m.configured)?.id || '';
  } catch (e) { if (alive) error.value = (e as Error).message; }
  finally { if (alive) loading.value = false; }
}
watch(() => props.active, active => { if (active && !saved.value) void load(); }, {immediate:true});
function toggleBuiltin(name: string) {
  if (!draft.value || !canManage.value) return;
  const list = draft.value.hidden_builtin[module.value];
  draft.value.hidden_builtin[module.value] = list.includes(name) ? list.filter(id => id !== name) : [...list, name];
}
async function save() {
  if (!draft.value || !canManage.value || saving.value) return;
  saving.value = true; error.value = ''; notice.value = '';
  try {
    const result = await uiIconsApi.save(copy(draft.value));
    if (!alive) return;
    acceptUIIcons(result); saved.value = copy(result); draft.value = copy(result); notice.value = t('iconLibrary.saved');
  } catch (e) {
    if (!alive) return;
    if (e instanceof UIIconsAPIError && e.status === 409 && e.current && draft.value) {
      // Preserve the full draft. A newer server version never gets overwritten
      // by retrying a stale whole-library request; the user must review it.
      saved.value = copy(e.current); acceptUIIcons(e.current); error.value = t('iconLibrary.conflict');
    } else error.value = (e as Error).message;
  } finally { if (alive) saving.value = false; }
}
const conflict = computed(() => !!draft.value && !!saved.value && draft.value.revision !== saved.value.revision);
function reset() { if (saved.value) draft.value = copy(saved.value); error.value = ''; editing.value = null; }
function cancelGeneration() { generation++; generateAbort?.abort(); generateAbort = undefined; generating.value = false; }
async function generate() {
  if (!canManage.value || !prompt.value.trim() || !model.value || generating.value) return;
  const run = ++generation;
  generateAbort = new AbortController(); generating.value = true; error.value = ''; notice.value = '';
  try {
    const result = await uiIconsApi.generate(prompt.value.trim(), model.value, count.value, generateAbort.signal);
    if (!alive || run !== generation) return;
    candidates.value = copy(result.icons); selected.value = new Set(result.icons.map(i => i.id));
  } catch (e) { if (alive && run === generation && (e as Error).name !== 'AbortError') error.value = (e as Error).message; }
  finally { if (alive && run === generation) { generating.value = false; generateAbort = undefined; } }
}
function selectCandidate(id: string) { const next = new Set(selected.value); next.has(id) ? next.delete(id) : next.add(id); selected.value = next; }
function addCandidates() {
  if (!draft.value) return;
  let added = 0;
  for (const icon of candidates.value) {
    if (!selected.value.has(icon.id)) continue;
    const existing = draft.value.icons.find(i => i.id === icon.id);
    if (existing) { existing.archived = false; existing.modules = [...new Set([...existing.modules, module.value])]; }
    else { draft.value.icons.push({...copy(icon), modules:[module.value]}); added++; }
  }
  candidates.value = []; selected.value = new Set(); notice.value = t('iconLibrary.added', {n:added});
}
function edit(icon: UIIcon) { editing.value = copy(icon); }
function applyEdit() {
  if (!draft.value || !editing.value?.name.trim() || [...editing.value.name].length > 80) return;
  const index = draft.value.icons.findIndex(i => i.id === editing.value!.id);
  if (index >= 0) draft.value.icons[index] = {...copy(editing.value), name:editing.value.name.trim()};
  editing.value = null;
}
function archive(icon: UIIcon) { if (canManage.value) icon.archived = !icon.archived; }
function editVisibility(value: boolean) { if (!value) editing.value = null; }
function unload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = ''; } }
if (typeof window !== 'undefined') window.addEventListener('beforeunload', unload);
onBeforeUnmount(() => { alive = false; cancelGeneration(); window.removeEventListener('beforeunload', unload); });
defineExpose({hasUnsavedChanges: () => dirty.value, refresh: load});
</script>

<template>
  <section class="icon-library" data-testid="icon-library-settings">
    <header class="library-head"><div><h3>{{t('iconLibrary.title')}}</h3><p>{{t('iconLibrary.description')}}</p></div><el-button v-if="canManage" :icon="MagicStick" type="primary" plain @click="generatorOpen = !generatorOpen">{{t('iconLibrary.generate')}}</el-button></header>
    <p v-if="!canManage && saved" class="library-note">{{t('iconLibrary.readonly')}}</p>
    <div v-if="error" role="alert" class="library-alert bad">{{error}} <el-button text :icon="Refresh" @click="load">{{t('iconLibrary.retry')}}</el-button></div>
    <div v-if="notice" role="status" class="library-alert">{{notice}}</div>
    <div v-if="loading && !saved" role="status" :aria-label="t('iconLibrary.loading')" aria-busy="true"><StudioEntrySkeleton area="cards" style="padding: 0" /></div>
    <section v-if="generatorOpen && canManage" class="generator">
      <div class="section-title"><span><el-icon><MagicStick /></el-icon>{{t('iconLibrary.aiTitle')}}</span><el-button text :icon="Close" :aria-label="t('iconLibrary.close')" @click="cancelGeneration(); generatorOpen = false" /></div>
      <p>{{t('iconLibrary.aiHint')}}</p>
      <el-input v-model="prompt" type="textarea" :rows="3" maxlength="2000" show-word-limit :placeholder="t('iconLibrary.promptPlaceholder')" :aria-label="t('iconLibrary.promptLabel')" />
      <div class="generate-controls"><el-select v-model="model" :placeholder="t('iconLibrary.model')" :aria-label="t('iconLibrary.model')"><el-option v-for="m in models" :key="m.id" :label="m.id" :value="m.id" :disabled="!m.configured" /></el-select><el-input-number v-model="count" :min="1" :max="4" :aria-label="t('iconLibrary.count')" /><el-button v-if="generating" @click="cancelGeneration">{{t('iconLibrary.cancel')}}</el-button><el-button v-else type="primary" :disabled="!prompt.trim() || !model" :icon="MagicStick" @click="generate">{{t('iconLibrary.generate')}}</el-button></div>
      <div v-if="generating" class="generation-progress" role="status"><el-icon class="spin"><Refresh /></el-icon>{{t('iconLibrary.generating')}}</div>
      <div v-if="candidates.length" class="candidate-grid"><button v-for="icon in candidates" :key="icon.id" class="candidate" :class="{selected:selected.has(icon.id)}" :aria-pressed="selected.has(icon.id)" type="button" @click="selectCandidate(icon.id)"><Icon :icon="glyph(icon)" /><span>{{icon.name}}</span><span class="candidate-state">{{selected.has(icon.id) ? '✓' : '+'}}</span></button></div>
      <div v-if="candidates.length" class="candidate-actions"><span>{{t('iconLibrary.previewOnly')}}</span><el-button :disabled="!selected.size" :icon="Plus" @click="addCandidates">{{t('iconLibrary.addSelected')}}</el-button></div>
    </section>
    <template v-if="draft">
      <div class="library-toolbar"><div class="module-tabs" role="group" :aria-label="t('iconLibrary.module')"><button v-for="m in UI_ICON_MODULES" :key="m" type="button" :class="{active:module===m}" :aria-pressed="module===m" @click="module=m">{{moduleLabel(m)}}</button></div><el-input v-model="query" clearable :prefix-icon="Search" :placeholder="t('iconLibrary.search')" :aria-label="t('iconLibrary.search')" /></div>
      <div class="source-tabs"><button v-for="s in ['all','custom','builtin','archived'] as const" :key="s" type="button" :class="{active:source===s}" :aria-pressed="source===s" @click="source=s">{{t(`iconLibrary.${s}`)}}</button></div>
      <template v-if="source!=='builtin'">
        <h4>{{source==='archived' ? t('iconLibrary.archived') : t('iconLibrary.custom')}}</h4>
        <div v-if="custom.length" class="custom-grid"><article v-for="icon in custom" :key="icon.id" class="custom-tile" :class="{archived:icon.archived}"><span class="custom-glyph"><Icon :icon="glyph(icon)" /></span><div class="custom-label"><strong>{{icon.name}}</strong><small>{{icon.modules.map(moduleLabel).join(' · ') || t('iconLibrary.noModules')}}</small></div><div v-if="canManage" class="tile-actions"><el-button text size="small" @click="edit(icon)">{{t('iconLibrary.edit')}}</el-button><el-button text size="small" @click="archive(icon)">{{icon.archived ? t('iconLibrary.restore') : t('iconLibrary.archive')}}</el-button></div></article></div>
        <div v-else class="empty-custom"><span>◇</span><p>{{source==='archived' ? t('iconLibrary.noArchived') : t('iconLibrary.empty')}}</p><el-button v-if="canManage && source!=='archived'" text type="primary" :icon="MagicStick" @click="generatorOpen=true">{{t('iconLibrary.generate')}}</el-button></div>
      </template>
      <template v-if="source==='all' || source==='builtin'"><h4>{{t('iconLibrary.builtin')}}<small>{{t('iconLibrary.builtinHint')}}</small></h4><div class="builtin-grid"><button v-for="name in builtin" :key="name" type="button" class="builtin-tile" :class="{hidden:hidden(name)}" :disabled="!canManage" :aria-pressed="!hidden(name)" :aria-label="`${name} · ${hidden(name) ? t('iconLibrary.hidden') : t('iconLibrary.visible')}`" @click="toggleBuiltin(name)"><AgentAvatar v-if="module==='agents'" :icon="name" color="#5965e8" :size="28" /><Icon v-else :icon="starterIconFor(name)!" /><span>{{name}}</span><small v-if="hidden(name)">{{t('iconLibrary.hidden')}}</small></button></div></template>
      <footer v-if="canManage" class="library-footer"><span>{{dirty ? t('iconLibrary.unsaved') : t('iconLibrary.currentSaved')}}</span><el-button :disabled="!dirty || saving" @click="reset">{{conflict ? t('iconLibrary.loadLatest') : t('iconLibrary.reset')}}</el-button><el-button type="primary" :loading="saving" :disabled="!dirty || conflict || !!editing" @click="save">{{t('iconLibrary.save')}}</el-button></footer>
    </template>
    <el-dialog :model-value="!!editing" @update:model-value="editVisibility" :title="t('iconLibrary.edit')" width="460px" :close-on-click-modal="false" append-to-body @closed="editing=null"><template v-if="editing"><el-form label-position="top"><el-form-item :label="t('iconLibrary.name')"><el-input v-model="editing.name" maxlength="80" show-word-limit /></el-form-item><el-form-item :label="t('iconLibrary.module')"><el-checkbox-group v-model="editing.modules"><el-checkbox v-for="m in UI_ICON_MODULES" :key="m" :value="m">{{moduleLabel(m)}}</el-checkbox></el-checkbox-group></el-form-item></el-form><p class="library-note">{{t('iconLibrary.editHint')}}</p></template><template #footer><el-button @click="editing=null">{{t('iconLibrary.cancel')}}</el-button><el-button type="primary" :disabled="!editing?.name.trim()" @click="applyEdit">{{t('iconLibrary.apply')}}</el-button></template></el-dialog>
  </section>
</template>

<style scoped>
.icon-library{max-width:1120px;margin:0 auto;color:var(--el-text-color-primary)}.library-head{display:flex;gap:20px;justify-content:space-between;align-items:flex-start;margin-bottom:24px}.library-head h3{font-size:21px;margin:0 0 8px}.library-head p,.generator>p{font-size:13px;line-height:1.7;color:var(--el-text-color-secondary);margin:0;max-width:680px}.library-note{font-size:12px;color:var(--el-text-color-secondary);line-height:1.7}.library-alert{padding:10px 12px;border-radius:8px;background:var(--el-fill-color-light);font-size:13px;margin:10px 0}.library-alert.bad{color:var(--el-color-danger);background:var(--el-color-danger-light-9)}.generator{padding:20px;border:1px solid var(--el-border-color);border-radius:14px;background:var(--el-fill-color-lighter);margin:20px 0 26px}.section-title{display:flex;justify-content:space-between;align-items:center;font-size:14px;font-weight:600}.section-title>span{display:flex;align-items:center;gap:8px}.generator>p{margin:0 0 14px}.generate-controls{display:flex;gap:10px;align-items:center;margin-top:12px}.generate-controls .el-select{max-width:260px;flex:1}.candidate-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin-top:18px}.candidate{position:relative;display:flex;flex-direction:column;align-items:center;gap:12px;min-height:116px;padding:18px 12px;border:1px solid var(--el-border-color);border-radius:10px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;font-size:12px;cursor:pointer}.candidate svg{width:32px;height:32px}.candidate.selected{color:var(--el-color-primary);border-color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.candidate-state{position:absolute;top:8px;right:8px}.candidate-actions{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-top:12px;font-size:12px;color:var(--el-text-color-secondary)}.generation-progress{display:flex;align-items:center;gap:8px;padding-top:16px;font-size:12px;color:var(--el-color-primary)}.spin{animation:icon-spin 1s linear infinite}@keyframes icon-spin{to{transform:rotate(360deg)}}.library-toolbar{display:flex;align-items:center;gap:16px;justify-content:space-between}.library-toolbar>.el-input{max-width:240px}.module-tabs{display:flex;gap:4px;padding:4px;border:1px solid var(--el-border-color-lighter);border-radius:9px;background:var(--el-fill-color-lighter)}.module-tabs button,.source-tabs button{font:inherit;white-space:nowrap;font-size:12px;border:0;cursor:pointer;color:var(--el-text-color-secondary);background:transparent;border-radius:6px;padding:7px 12px}.module-tabs button.active{color:var(--el-text-color-primary);background:var(--el-bg-color);box-shadow:0 1px 4px #0000000d}.source-tabs{display:flex;gap:8px;margin:18px 0}.source-tabs button{padding:5px 8px}.source-tabs button.active{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}h4{margin:22px 0 12px;font-size:13px;font-weight:600;display:flex;align-items:center;flex-wrap:wrap;gap:10px}h4 small{font-size:11px;font-weight:400;color:var(--el-text-color-secondary)}.custom-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}.custom-tile{display:flex;align-items:center;gap:12px;padding:14px;border:1px solid var(--el-border-color-lighter);border-radius:10px;flex-wrap:wrap}.custom-glyph{display:flex;align-items:center;justify-content:center;width:36px;height:36px;flex:none;border-radius:9px;background:var(--el-fill-color-light);color:var(--el-color-primary)}.custom-glyph svg{width:22px;height:22px}.custom-label{min-width:0;flex:1}.custom-label strong{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.custom-label small{display:block;font-size:10px;color:var(--el-text-color-secondary);margin-top:5px}.tile-actions{display:flex;gap:0;width:100%;justify-content:flex-end}.tile-actions .el-button+.el-button{margin-left:0}.empty-custom{display:flex;align-items:center;gap:10px;border:1px dashed var(--el-border-color);border-radius:10px;padding:20px;color:var(--el-text-color-secondary);font-size:12px}.empty-custom>span{font-size:24px;color:var(--el-text-color-placeholder)}.empty-custom>p{flex:1;margin:0}.builtin-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(80px,1fr));gap:7px}.builtin-tile{min-height:76px;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:8px;padding:10px 5px;border:1px solid var(--el-border-color-lighter);border-radius:9px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:10px;cursor:pointer}.builtin-tile>svg{width:22px;height:22px}.builtin-tile:hover{border-color:var(--el-color-primary-light-5);background:var(--el-fill-color-lighter)}.builtin-tile.hidden{opacity:.45;border-style:dashed}.builtin-tile small{font-size:9px}.builtin-tile:disabled{cursor:default}.library-footer{display:flex;gap:10px;align-items:center;position:sticky;bottom:-24px;padding:14px 0;margin-top:24px;border-top:1px solid var(--el-border-color-lighter);background:var(--el-bg-color);z-index:1}.library-footer>span{flex:1;font-size:12px;color:var(--el-text-color-secondary)}@media(max-width:760px){.library-head,.library-toolbar{flex-wrap:wrap}.library-toolbar>.el-input{max-width:none}.candidate-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.custom-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.generate-controls{flex-wrap:wrap}.generate-controls .el-select{max-width:none;min-width:160px}.candidate-actions{align-items:flex-start;flex-direction:column}}@media(max-width:480px){.custom-grid{grid-template-columns:1fr}.library-footer{flex-wrap:wrap}.library-footer>span{flex-basis:100%}.module-tabs{width:100%;justify-content:space-between}}
</style>
