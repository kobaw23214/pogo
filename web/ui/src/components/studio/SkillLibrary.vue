<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { builtinSkillDescription } from '@/agent/builtinToolCopy';
import { getLocale } from '@/i18n';
const { t: tr } = useI18n();
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { Box, Plus, Search, Upload, ArrowRight, Check } from '@element-plus/icons-vue';
import StudioEntrySkeleton from '@/components/studio/StudioEntrySkeleton.vue';
import SkillsPanel from '@/components/SkillsPanel.vue';
import SkillUploadDialog from './SkillUploadDialog.vue';
import SkillExecutionNotice from '@/components/SkillExecutionNotice.vue';
import SkillDocumentViewer from '@/components/SkillDocumentViewer.vue';
import SkillPackageBrowser from '@/components/SkillPackageBrowser.vue';
import type { SkillDocumentContent } from '@/api/types';
import { api } from '@/api/client';
import { skillIsReady, skillStateLabel } from '@/agent/skillSelection';
import { formatSkillBytes as bytes, skillError as errorText } from './skillManagement';
import type { PlatformSkill, SkillLibraryCatalog } from '@/agent/platformTypes';
import { registerStudioNavigationGuard } from '@/agent/studioNavigation';

const props = defineProps<{ canManage: boolean }>();
const emit = defineEmits<{ create: [skillIDs: string[], name: string] }>();
const catalog = ref<SkillLibraryCatalog | null>(null);
const loading = ref(false);
const error = ref('');
const query = ref('');
const filter = ref('all');
const uploadOpen = ref(false);
const selected = ref<PlatformSkill | null>(null);
const detailOpen = ref(false);
const instructions = ref<SkillDocumentContent | null>(null);
const packageBrowser = ref<InstanceType<typeof SkillPackageBrowser> | null>(null);
const contentLoading = ref(false);
const contentError = ref('');

const action = ref<'delete' | 'disable' | null>(null);
const actionTarget = ref<PlatformSkill | null>(null);
const actionBusy = ref(false);
const actionError = ref('');
// Set when the first delete attempt came back with who is still referencing
// the skill: the dialog then offers the force path (dynamic unload).
const forceAvailable = ref(false);
const notice = ref('');
let generation = 0;
let contentGeneration = 0;
let disposed = false;
const skills = computed(() => [...(catalog.value?.skills ?? [])].sort((a, b) => Number(unavailable(a)) - Number(unavailable(b)) || Number(!!a.builtin) - Number(!!b.builtin) || a.name.localeCompare(b.name, getLocale())));
function unavailable(skill: PlatformSkill) { return skill.state === 'revoked' || skill.state === 'unavailable'; }
const usable = computed(() => skills.value.filter(skillIsReady).length);
/* The three-kind badge answers "what does checking this skill grant": a
 * manifest/runtime bundle brings its own tools, a browser-task skill wakes
 * the built-in browser tool instead, and the rest are knowledge only. */
type SkillKind = 'tools' | 'booster' | 'knowledge';
const kindOf = (skill: PlatformSkill): SkillKind =>
  skill.browser_task ? 'booster' : skill.tool_count > 0 || (skill.runtime_count ?? 0) > 0 ? 'tools' : 'knowledge';
const kindLabels: Record<SkillKind, string> = { get tools() { return tr('studioManagement.text197'); }, get booster() { return tr('studioManagement.text198'); }, get knowledge() { return tr('studioManagement.text199'); } };
const filtered = computed(() => skills.value.filter(skill => {
  if (filter.value === 'installed' && skill.builtin || filter.value === 'builtin' && !skill.builtin || filter.value === 'disabled' && !unavailable(skill)) return false;
  return `${skill.name} ${skill.description} ${builtinSkillDescription(skill.name, skill.description, !!skill.builtin)}`.toLowerCase().includes(query.value.trim().toLowerCase());
}));
const selectedTools = computed(() => (catalog.value?.tools ?? []).filter(tool => tool.package.packageDigest === selected.value?.package?.packageDigest));
const canCreate = (skill: PlatformSkill) => !unavailable(skill);

async function reload() {
  if (!props.canManage || actionBusy.value) return;
  const request = ++generation; loading.value = true; error.value = '';
  try {
    const next = await api.skillLibrary();
    if (disposed || request !== generation) return;
    catalog.value = next;
    if (selected.value) {
      const nextSelected = next.skills.find(s => s.id === selected.value?.id) ?? null;
      if (!nextSelected && detailOpen.value && !(await confirmDetailLeave())) return;
      if (disposed || request !== generation) return;
      selected.value = nextSelected;
      if (!selected.value) detailOpen.value = false;
    }
  } catch (e) { if (!disposed && request === generation) error.value = errorText(e); }
  finally { if (!disposed && request === generation) loading.value = false; }
}
async function show(skill: PlatformSkill) {
  if (detailOpen.value && !(await confirmDetailLeave())) return;
  selected.value = skill; detailOpen.value = true;
  instructions.value = null; contentError.value = ''; contentLoading.value = true;
  const request = ++contentGeneration;
  try {
    const content = await api.managedSkillContent(skill.id);
    if (disposed || request !== contentGeneration) return;
    instructions.value = content;
  } catch (e) { if (!disposed && request === contentGeneration) contentError.value = errorText(e); }
  finally { if (!disposed && request === contentGeneration) contentLoading.value = false; }
}
async function create(skill: PlatformSkill) { if (detailOpen.value && !(await confirmDetailLeave())) return; detailOpen.value = false; emit('create', [skill.id], skill.name); }
function createUploaded(ids: string[], name: string) { uploadOpen.value = false; emit('create', ids, name); }
async function askAction(kind: 'delete' | 'disable', skill: PlatformSkill) { if (detailOpen.value && !(await confirmDetailLeave())) return; actionTarget.value = skill; action.value = kind; actionError.value = ''; forceAvailable.value = false; }
async function performAction(force = false) {
  const target = actionTarget.value; const kind = action.value;
  if (!target || !kind || actionBusy.value) return;
  actionBusy.value = true; actionError.value = '';
  try {
    if (kind === 'delete') {
      const result = await api.deleteSkill(target.id, force);
      if (!result.deleted) throw new Error(tr('studioManagement.text200'));
      notice.value = result.cleanup_pending ? tr('studioManagement.text201', { value0: target.name }) : tr('studioManagement.text202', { value0: target.name });
      detailOpen.value = false; selected.value = null;
    } else {
      await api.disableSkill(target.id); notice.value = tr('studioManagement.text203', { value0: target.name });
    }
    action.value = null;
    window.dispatchEvent(new CustomEvent('pogo-skills-changed'));
  } catch (e) {
    if (disposed) return;
    actionError.value = errorText(e);
    // A 409 names the referencing agents/sessions: offer the force path
    // (keep them, they just lose the Skill) instead of a dead end.
    if (kind === 'delete' && !force && (e as { status?: number }).status === 409) forceAvailable.value = true;
  } finally { actionBusy.value = false; if (!disposed) void reload(); }
}
function changed() { void reload(); }
async function confirmDetailLeave() { return !actionBusy.value && await (packageBrowser.value?.confirmLeave() ?? true); }
async function closeDetail(done: () => void) { if (await confirmDetailLeave()) done(); }
function documentSaved(document: SkillDocumentContent) {
  instructions.value = document;
  notice.value = tr('studioManagement.text204', { value0: selected.value?.name || 'Skill' });
  // Refresh descriptions and all scoped completion catalogs after the server
  // has atomically accepted this document revision. A draft never emits this.
  window.dispatchEvent(new CustomEvent('pogo-skills-changed'));
}
const removeGuard = registerStudioNavigationGuard(confirmDetailLeave);
watch(() => props.canManage, allowed => { if (allowed) void reload(); }, { immediate: true });
onMounted(() => window.addEventListener('pogo-skills-changed', changed));
onBeforeUnmount(() => { disposed = true; generation++; contentGeneration++; removeGuard(); window.removeEventListener('pogo-skills-changed', changed); });
defineExpose({ reload });
</script>

<template>
  <SkillsPanel v-if="!canManage" embedded read-only />
  <div v-else class="skill-library">
    <header class="library-head"><div><strong>{{ tr('studioManagement.text205') }}</strong><StudioEntrySkeleton v-if="loading && !catalog" area="agent" /><span v-else>{{ tr('studioSentences.readySkills', { ready: usable, total: skills.length }) }}</span></div><button type="button" class="button primary" :disabled="loading && !catalog" @click="uploadOpen = true"><el-icon><Upload /></el-icon>{{ tr('studioManagement.text208') }}</button></header>
    <p class="intro">{{ tr('studioManagement.text209') }}</p>
    <SkillExecutionNotice :execution="catalog?.execution" />
    <div class="toolbar"><label class="search"><el-icon><Search /></el-icon><input v-model="query" type="search" :placeholder="tr('studioManagement.text210')" :aria-label="tr('studioManagement.text211')" /></label><div class="filters" role="group" :aria-label="tr('studioManagement.text212')"><button v-for="item in [{id:'all',name:tr('studioManagement.text213')},{id:'installed',name:tr('studioManagement.text214')},{id:'builtin',name:tr('studioManagement.text039')},{id:'disabled',name:tr('studioManagement.text049')}]" :key="item.id" type="button" :class="{ active: filter === item.id }" @click="filter = item.id">{{ item.name }}</button></div></div>
    <p v-if="notice" class="notice" role="status"><el-icon><Check /></el-icon>{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }} <button type="button" class="text-button" @click="reload">{{ tr('studioManagement.text104') }}</button></p>
    <div v-if="loading && !catalog" role="status" :aria-label="tr('studioManagement.text215')" aria-busy="true"><StudioEntrySkeleton area="cards" /></div>
    <div v-else-if="!error && !filtered.length" class="empty"><strong>{{ skills.length ? tr('studioManagement.text216') : tr('studioManagement.text217') }}</strong><p>{{ skills.length ? tr('studioManagement.text218') : tr('studioManagement.text219') }}</p><button v-if="!skills.length" class="button" type="button" @click="uploadOpen = true">{{ tr('studioManagement.text220') }}</button></div>
    <div v-else class="skill-grid">
      <article v-for="skill in filtered" :key="skill.id" class="skill-card" :class="{ unavailable: unavailable(skill) }">
        <div class="card-top"><span class="icon"><el-icon><Box /></el-icon></span><span class="badges"><span class="badge kind" :class="kindOf(skill)">{{ kindLabels[kindOf(skill)] }}</span><span class="badge" :class="skill.state">{{ skill.builtin ? tr('studioManagement.text039') : skillStateLabel(skill.state) || tr('studioManagement.text214') }}</span></span></div>
        <button type="button" class="skill-title" @click="show(skill)">{{ skill.name }}</button>
        <p class="description">{{ builtinSkillDescription(skill.name, skill.description, !!skill.builtin) || tr('studioManagement.text221') }}</p>
        <div class="facts"><span>{{ skill.tool_count ? tr('studioManagement.text222', { value0: skill.tool_count }) : tr('studioManagement.text223') }}</span><span v-if="skill.package">v{{ skill.package.version }}</span><span v-if="skill.asset_count">{{ tr('studioSentences.assetCount', { count: skill.asset_count }) }}</span></div>
        <p v-if="skill.issues?.length" class="state-note">{{ skill.issues[0]?.message }}</p>
        <footer><button type="button" class="text-button quiet" @click="show(skill)">{{ tr('studioManagement.text225') }}<el-icon><ArrowRight /></el-icon></button><button type="button" class="text-button" :disabled="!canCreate(skill)" @click="create(skill)"><el-icon><Plus /></el-icon>{{ tr('studioManagement.text226') }}</button></footer>
      </article>
    </div>
    <el-drawer v-model="detailOpen" :title="tr('studioManagement.text227')" size="100%" append-to-body :before-close="closeDetail">
      <div v-if="selected" class="detail" :class="{ 'package-detail': !selected.builtin }">
        <template v-if="selected.builtin">
        <div class="detail-title"><span class="icon"><el-icon><Box /></el-icon></span><div><h2>{{ selected.name }}</h2><span class="source">{{ selected.builtin ? tr('studioManagement.text228') : tr('studioManagement.text229') }}<template v-if="selected.package"> · v{{ selected.package.version }}</template></span></div></div>
        <p class="intro">{{ builtinSkillDescription(selected.name, selected.description, !!selected.builtin) }}</p>
        <div class="detail-stats"><span><strong>{{ selected.tool_count }}</strong> {{ tr('studioManagement.text230') }}</span><span v-if="selected.asset_count"><strong>{{ selected.asset_count }}</strong> {{ tr('studioManagement.text231') }}</span><span v-if="selected.package_size_bytes">{{ bytes(selected.package_size_bytes) }}</span></div>
        <SkillExecutionNotice :execution="selected.runtime_count ? catalog?.execution : undefined" :issues="selected.issues" />
        <div class="detail-actions"><button class="button primary" type="button" :disabled="!canCreate(selected)" @click="create(selected)">{{ tr('studioManagement.text232') }}</button><button v-if="selected.package && selected.state !== 'revoked'" class="button" type="button" @click="askAction('disable', selected)">{{ tr('studioManagement.text233') }}</button><button v-if="selected.removable" class="button danger" type="button" @click="askAction('delete', selected)">{{ tr('studioManagement.text234') }}</button></div>
        <section v-if="selectedTools.length" class="detail-section"><h3>{{ tr('studioManagement.text235') }}</h3><div class="tool-list"><div v-for="tool in selectedTools" :key="tool.id"><strong>{{ tool.provider_id }}</strong><p>{{ tool.description }}</p></div></div></section>
        <section v-if="selected.runtime_count" class="detail-section"><h3>{{ tr('studioManagement.text236') }}</h3><p class="intro">{{ tr('studioManagement.text237') }} {{ selected.asset_count ? tr('studioManagement.text238') : '' }}</p></section>
        </template>
        <template v-else>
          <header class="package-detail-head">
            <div class="detail-title"><span class="icon"><el-icon><Box /></el-icon></span><div><h2>{{ selected.name }}</h2><span class="source">{{ tr('studioManagement.text229') }}<template v-if="selected.package"> · v{{ selected.package.version }}</template></span></div></div>
            <div class="detail-actions"><button class="button primary" type="button" :disabled="!canCreate(selected)" @click="create(selected)">{{ tr('studioManagement.text232') }}</button><button v-if="selected.package && selected.state !== 'revoked'" class="button" type="button" @click="askAction('disable', selected)">{{ tr('studioManagement.text233') }}</button><button v-if="selected.removable" class="button danger" type="button" @click="askAction('delete', selected)">{{ tr('studioManagement.text234') }}</button></div>
          </header>
          <details class="package-overview"><summary>{{ tr('studioManagement.text239') }} <span>{{ selected.tool_count }} {{ tr('studioManagement.text240') }}<template v-if="selected.package_size_bytes"> · {{ bytes(selected.package_size_bytes) }}</template></span></summary><p class="intro">{{ builtinSkillDescription(selected.name, selected.description, !!selected.builtin) }}</p><SkillExecutionNotice :execution="selected.runtime_count ? catalog?.execution : undefined" :issues="selected.issues" /><div v-if="selectedTools.length" class="tool-list"><div v-for="tool in selectedTools" :key="tool.id"><strong>{{ tool.provider_id }}</strong><p>{{ tool.description }}</p></div></div><p v-if="selected.runtime_count" class="intro">{{ tr('studioManagement.text241') }} {{ selected.asset_count ? tr('studioManagement.text238') : '' }}</p></details>
        </template>
        <section class="detail-section">
          <h3>{{ selected.builtin ? tr('studioManagement.text242') : tr('studioManagement.text243') }}</h3>
          <div v-if="contentLoading" class="skill-detail-loading" :class="{ 'package-loading': !selected.builtin }" role="status" :aria-label="tr('studioManagement.text244')" aria-busy="true">
            <div v-if="!selected.builtin" class="package-loading-sidebar"><StudioEntrySkeleton area="sessions" /></div>
            <div class="package-loading-document"><StudioEntrySkeleton area="document" /></div>
          </div>
          <p v-else-if="contentError" class="error" role="alert">{{ contentError }} <button type="button" class="text-button" @click="show(selected)">{{ tr('studioManagement.text104') }}</button></p>
          <template v-else-if="instructions"><SkillDocumentViewer v-if="selected.builtin" :key="selected.id" :scope-key="selected.id" :root-document="instructions" :load-document="path => api.managedSkillContent(selected!.id, path)" managed /><SkillPackageBrowser v-else :key="selected.id" ref="packageBrowser" :scope-key="selected.id" :root-document="instructions" :load-document="path => api.managedSkillContent(selected!.id, path)" :load-files="path => api.managedSkillFiles(selected!.id, path)" :save-document="input => api.saveManagedSkillContent(selected!.id, input.content, input.expected_revision)" @saved="documentSaved" @reloaded="instructions = $event" /></template>
        </section>
      </div>
    </el-drawer>
    <SkillUploadDialog v-if="uploadOpen" :max-bytes="catalog?.max_upload_bytes ?? 0" :execution="catalog?.execution" @close="uploadOpen = false" @create="createUploaded" />
    <el-dialog :model-value="!!action" :title="action === 'delete' ? tr('studioManagement.text234') : tr('studioManagement.text245')" width="min(520px, 94vw)" append-to-body :close-on-click-modal="!actionBusy" :close-on-press-escape="!actionBusy" :show-close="!actionBusy" @close="action = null">
      <p>{{ tr(action === 'delete' ? 'studioSentences.removeSkill' : 'studioSentences.disableSkill', { name: actionTarget?.name ?? '', version: actionTarget?.package ? ` v${actionTarget.package.version}` : '' }) }}</p>
      <p class="intro">{{ action === 'delete' ? tr('studioManagement.text247') : tr('studioManagement.text248') }}</p>
      <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
      <p v-if="action === 'delete' && forceAvailable" class="intro">{{ tr('studioManagement.text249') }}</p>
      <template #footer>
        <div class="dialog-actions">
          <button class="button" type="button" :disabled="actionBusy" @click="action = null">{{ tr('studioManagement.text067') }}</button>
          <button v-if="action === 'delete' && forceAvailable" class="button danger" type="button" :disabled="actionBusy" @click="performAction(true)">{{ actionBusy ? tr('studioManagement.text250') : tr('studioManagement.text251') }}</button>
          <button class="button danger" type="button" :disabled="actionBusy" @click="performAction(false)">{{ actionBusy ? tr('studioManagement.text250') : action === 'delete' ? tr('studioManagement.text252') : tr('studioManagement.text253') }}</button>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.skill-detail-loading{min-height:350px}
.package-loading{display:grid;grid-template-columns:260px minmax(0,1fr);height:calc(100dvh - 245px);border:1px solid var(--el-border-color-lighter);border-radius:10px;overflow:hidden}
.package-loading-sidebar{padding:12px;border-right:1px solid var(--el-border-color-lighter);background:var(--el-fill-color-lighter)}
.package-loading-document{padding:16px;min-width:0}
@media(max-width:1000px){.package-loading{grid-template-columns:220px minmax(0,1fr)}}
@media(max-width:680px){.package-loading{grid-template-columns:1fr;grid-template-rows:140px minmax(0,1fr);height:calc(100dvh - 280px);min-height:320px}.package-loading-sidebar{border-right:0;border-bottom:1px solid var(--el-border-color-lighter);overflow:hidden}}
.skill-library{padding:24px 28px;color:var(--el-text-color-primary);font-size:13px}.library-head,.library-head>div,.toolbar,.filters,.card-top,.skill-card footer,.button,.text-button,.facts,.detail-title,.detail-stats,.detail-actions{display:flex;align-items:center;gap:10px}.library-head,.card-top,.skill-card footer{justify-content:space-between}.library-head strong{font-size:17px;font-weight:550}.library-head span,.intro,.source{color:var(--el-text-color-secondary)}.intro{line-height:1.8;margin:12px 0 20px}.button{padding:9px 13px;background:var(--el-bg-color);border:1px solid var(--el-border-color);border-radius:6px;color:inherit;cursor:pointer;font:inherit;justify-content:center}.primary{background:var(--el-color-primary);color:var(--el-color-white);border-color:transparent}.danger{color:var(--el-color-danger)}.toolbar{justify-content:space-between;margin:22px 0;flex-wrap:wrap;gap:14px}.search{display:flex;align-items:center;gap:9px;border:1px solid var(--el-border-color);border-radius:6px;padding:10px 12px;flex:1;max-width:380px}.search input{border:0;outline:0;background:none;color:inherit;width:100%;font:inherit}.filters{gap:4px}.filters button{font:inherit;border:0;border-radius:5px;padding:7px 11px;color:var(--el-text-color-secondary);background:none;cursor:pointer}.filters button.active{background:var(--el-fill-color);color:var(--el-text-color-primary)}.skill-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px}.skill-card{display:flex;flex-direction:column;padding:20px;border:1px solid var(--el-border-color-lighter);border-radius:12px;background:var(--el-bg-color);transition:border-color .15s,box-shadow .15s}.skill-card:hover{border-color:var(--el-border-color);box-shadow:0 4px 16px #00000006}.icon{width:36px;height:36px;flex-shrink:0;border-radius:9px;background:var(--el-fill-color-light);display:grid;place-items:center;font-size:19px}.badge{font-size:11px;border-radius:4px;padding:4px 7px;background:var(--el-fill-color);color:var(--el-text-color-secondary)}.badge.needs_configuration{color:var(--el-color-warning);background:var(--el-color-warning-light-9)}.badge.available{color:var(--el-color-success);background:var(--el-color-success-light-9)}.badges{display:flex;align-items:center;gap:6px}.badge.kind.tools{color:var(--el-color-success);background:var(--el-color-success-light-9)}.badge.kind.booster{color:var(--el-color-primary);background:var(--pg-accent-wash);box-shadow:inset 0 0 0 1px var(--pg-accent-line)}.skill-title{background:none;border:0;color:inherit;font:inherit;font-size:17px;font-weight:550;text-align:left;padding:18px 0 0;cursor:pointer}.description{line-height:1.8;color:var(--el-text-color-regular);margin:10px 0 18px;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;min-height:70px}.facts{margin-top:auto;gap:7px;flex-wrap:wrap;color:var(--el-text-color-secondary);font-size:11px}.facts span{padding:4px 7px;border-radius:4px;background:var(--el-fill-color-lighter)}.state-note{color:var(--el-color-warning);font-size:12px;line-height:1.6;margin:12px 0 0}.skill-card footer{border-top:1px solid var(--el-border-color-lighter);margin-top:18px;padding-top:13px;font-size:12px}.text-button{border:0;background:none;color:var(--el-color-primary);cursor:pointer;font:inherit;padding:2px 0}.quiet{color:var(--el-text-color-secondary)}.unavailable{opacity:.65}button:disabled{opacity:.45;cursor:not-allowed}.empty{text-align:center;padding:65px 12px;color:var(--el-text-color-secondary)}.empty strong{font-size:18px;color:var(--el-text-color-primary);font-weight:500}.empty .button{margin:20px auto}.error{color:var(--el-color-danger);line-height:1.8;overflow-wrap:anywhere}.notice{display:flex;align-items:center;gap:8px;color:var(--el-color-success);line-height:1.7}.runtime-note,.issues{padding:14px 16px;border-radius:8px;background:var(--el-color-warning-light-9);line-height:1.8}.runtime-note span{display:block;color:var(--el-text-color-secondary);margin-top:5px}.detail{line-height:1.8;max-width:960px;margin:0 auto}.detail-title h2{font-size:23px;font-weight:550;margin:0 0 2px;line-height:1.4}.detail-title .icon{width:44px;height:44px;font-size:23px}.source{font-size:12px}.detail-stats{gap:20px;padding:14px 0;border-top:1px solid var(--el-border-color-lighter);border-bottom:1px solid var(--el-border-color-lighter);color:var(--el-text-color-secondary)}.detail-stats strong{font-size:20px;font-weight:500;color:var(--el-text-color-primary);margin-right:3px}.detail-actions{margin:20px 0;flex-wrap:wrap}.detail-section{margin-top:26px}.detail-section h3{font-size:14px;font-weight:550;margin:0 0 12px}.tool-list{border:1px solid var(--el-border-color-lighter);border-radius:8px;overflow:hidden}.tool-list>div{padding:12px 14px;border-bottom:1px solid var(--el-border-color-lighter)}.tool-list>div:last-child{border:0}.tool-list strong{font-size:12px;font-weight:550}.tool-list p{color:var(--el-text-color-secondary);font-size:12px;margin:4px 0 0}.instructions{overflow-wrap:anywhere;max-height:65vh;overflow:auto;padding:4px 16px;border-radius:8px;background:var(--el-fill-color-lighter)}
/* The dialog teleports to body, so :deep(.el-dialog__footer) never matched
 * and the display:flex buttons stacked one full-width row each. Slot content
 * keeps this component's scope attribute, so an own wrapper is the reliable
 * place for the row layout. */
.dialog-actions{display:flex;justify-content:flex-end;align-items:center;gap:10px;flex-wrap:wrap}
.dialog-actions .button{flex:0 0 auto}
.package-detail{max-width:none}.package-detail-head{display:flex;align-items:center;justify-content:space-between;gap:20px}.package-detail-head .detail-actions{margin:0}.package-detail-head .detail-title{min-width:0}.package-detail-head h2{overflow-wrap:anywhere}.package-overview{font-size:12px;color:var(--el-text-color-secondary);margin:16px 0 0;border-bottom:1px solid var(--el-border-color-lighter);padding-bottom:12px}.package-overview summary{cursor:pointer;user-select:none}.package-overview summary>span{margin-left:10px;color:var(--el-text-color-placeholder)}.package-overview[open] summary{margin-bottom:10px}.package-detail>.detail-section:last-child{margin-top:14px}.package-detail :deep(.skill-package-browser){height:calc(100dvh - 245px);min-height:350px}.package-detail :deep(.package-tree){max-height:none;min-height:0}.package-detail :deep(.package-preview .document-content){max-height:none;min-height:0;flex:1}.package-detail :deep(.package-preview .skill-document-viewer){display:flex;flex-direction:column}.package-detail :deep(.package-sidebar){overflow:hidden}@media(max-width:680px){.package-detail-head{align-items:flex-start;flex-wrap:wrap;gap:12px}.package-detail-head .detail-actions{gap:6px}.package-detail-head .detail-actions .button{font-size:12px;padding:6px 9px}.package-detail :deep(.skill-package-browser){height:calc(100dvh - 280px);min-height:320px}.package-detail :deep(.package-tree){max-height:180px}.package-detail :deep(.package-preview){min-height:0;overflow:hidden}}
.skill-library :deep(.entry-cards){padding:0;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px}
@media(max-width:600px){.skill-library{padding:18px 16px}.skill-grid{grid-template-columns:1fr}.library-head{align-items:flex-start}.library-head>div{flex-wrap:wrap}.search{max-width:none;min-width:180px}.detail-actions .button{flex:1;white-space:nowrap}}
</style>
