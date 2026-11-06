<script setup lang="ts">
import { computed, defineAsyncComponent, h, nextTick, onBeforeUnmount, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { Close, Grid, List, Menu, Plus, Refresh, Search } from '@element-plus/icons-vue';
import StudioPreferences from '@/components/studio/StudioPreferences.vue';
import StudioEntrySkeleton from '@/components/studio/StudioEntrySkeleton.vue';
import { createStudioCatalogLoading } from '@/agent/studioEntry';
const InformationLibrary = defineAsyncComponent({
  loader: () => import('@/components/studio/InformationLibrary.vue'),
  loadingComponent: () => h(StudioEntrySkeleton, { area: 'settings' }),
  delay: 0,
});
const SettingsDialog = defineAsyncComponent({
  loader: () => import('@/components/SettingsDialog.vue'),
  loadingComponent: () => h(StudioEntrySkeleton, { area: 'settings' }),
  delay: 0,
});
import AgentCard from '@/components/studio/AgentCard.vue';
import AgentEditorDialog from '@/components/studio/AgentEditorDialog.vue';
import PublishDialog from '@/components/studio/PublishDialog.vue';
import StudioSidebar from '@/components/studio/StudioSidebar.vue';
import SkillLibrary from '@/components/studio/SkillLibrary.vue';
import SkillMonitorPanel from '@/components/SkillMonitorPanel.vue';
import type { AgentTemplate, PlatformAgent, PlatformAgentInput, PlatformCapabilities, PlatformModel } from '@/agent/platformTypes';
import { navigateStudio, platformApi } from '@/api/platform';
import {
  STUDIO_SECTION_DESCS,
  STUDIO_SECTION_LABELS,
  agentStats,
  agentCapabilitySummary,
  agentModelsLabel,
  filterAgents,
  managedChatPath,
  sectionForSettingsSection,
  settingsSectionFor,
  studioPathForSection,
  visibleSection,
  type AgentStatusFilter,
  type StudioSection,
} from '@/agent/studioPresentation';

/**
 * The Studio's management surface. It owns the shared shell — the native Pogo
 * sidebar, the page header, the module body — and renders one module at a
 * time: the Agent catalog for 我的 Agents, the shared resource libraries
 * (its editor is a full-screen route, not a module), the real SettingsDialog
 * inline for the configuration modules (模型配置 / MCP 管理 / 账号与安全 /
 * 设置), and the real Skill library plus its monitor under Skill 管理.
 * Templates are not a module; they are the creation dialog's starting points.
 */
const props = withDefaults(
  defineProps<{
    agents: PlatformAgent[];
    models: PlatformModel[];
    templates: AgentTemplate[];
    loading: boolean;
    catalogReady?: boolean;
    saving: boolean;
    error: string;
    canManage: boolean;
    accountName: string;
    /** Whether the users.json account system is on. Without it the account
     *  modules fall back to the Agents page instead of rendering orphans. */
    passwordEnabled: boolean;
    section?: StudioSection;
    memoryAgentId?: string;
  }>(),
  { section: 'agents', passwordEnabled: false, catalogReady: undefined },
);

const emit = defineEmits<{
  create: [input: PlatformAgentInput, enterChat: boolean];
  update: [id: string, input: PlatformAgentInput, revision: number];
  remove: [agent: PlatformAgent];
  createTemplate: [input: Omit<AgentTemplate, 'id'>];
  removeTemplate: [id: string];
  refresh: [];
  dismissError: [];
}>();

const { t: tr } = useI18n();

/* ---------------------------------- chrome ---------------------------------- */

const navOpen = ref(false);

/** The module that actually renders: the route's section, except that the
 *  account surfaces (账号与安全 / 设置) fall back to the Agents page on a
 *  deployment without the account system — the sidebar does not offer them
 *  there either, so a deep link must not land on an orphaned module. */
const catalogSucceeded = computed(() => props.catalogReady ?? !props.error);
const initialLoading = createStudioCatalogLoading(computed(() => props.loading), catalogSucceeded);
// Until the initial catalog/auth pass settles, false is an unknown permission,
// not proof that the requested account module should become the Agents page.
const activeSection = computed<StudioSection>(() => initialLoading.value || !catalogSucceeded.value
  ? props.section
  : visibleSection(props.section, props.passwordEnabled));

const title = computed(() => STUDIO_SECTION_LABELS[activeSection.value]);
const subtitle = computed(() => {
  if (activeSection.value !== 'agents') return STUDIO_SECTION_DESCS[activeSection.value];
  if (initialLoading.value) return tr('studioManagement.text574');
  if (!catalogSucceeded.value) return STUDIO_SECTION_DESCS.agents;
  const s = agentStats(props.agents, props.models);
  const parts = [tr('studioManagement.text575', { value0: s.total }), tr('studioManagement.text576', { value0: s.ready }), tr('studioManagement.text577', { value0: s.draft }), tr('studioManagement.text578', { value0: s.availableModels })];
  if (!props.canManage) parts.push(tr('studioManagement.text579'));
  return parts.join(' · ');
});

/** The module body: the catalog, or the real settings content for the module. */
const settingsSection = computed(() => settingsSectionFor(activeSection.value));

/** The embedded settings pages cross-link between modules (a skill's MCP
    trace, …); the answer is a Studio route change, not local state. */
function onSettingsNavigate(section: Parameters<typeof sectionForSettingsSection>[0]) {
  navigateStudio(studioPathForSection(sectionForSettingsSection(section)));
}

/* ------------------------------ agents module ------------------------------ */

const query = ref('');
const statusFilter = ref<AgentStatusFilter>('all');
const viewMode = ref<'grid' | 'list'>('grid');

const statusChips = [
  { value: 'all' as const, get label() { return tr('studioManagement.text581'); } },
  { value: 'ready' as const, get label() { return tr('studioManagement.text003'); } },
  { value: 'draft' as const, get label() { return tr('studioManagement.text004'); } },
];

const filtered = computed(() => filterAgents(props.agents, query.value, statusFilter.value));

function clearFilters() {
  query.value = '';
  statusFilter.value = 'all';
}

/* ------------------------------ dialogs & toast ------------------------------ */

const editor = ref<InstanceType<typeof AgentEditorDialog> | null>(null);
const publishDlg = ref<InstanceType<typeof PublishDialog> | null>(null);
/** Whether the editor dialog is up; the server error shows inside it then. */
const editorOpen = ref(false);

/** The checked-capability catalog. It is deliberately not loaded with the
 *  Agent list: only the admin editor needs it, and every create/edit open
 *  refetches so newly uploaded Skills or MCP changes are visible. */
const capabilities = ref<PlatformCapabilities | null>(null);
const capabilitiesLoading = ref(false);
const capabilitiesError = ref('');
let capabilitiesRequest = 0;

async function loadCapabilities() {
  const request = ++capabilitiesRequest;
  // Clear synchronously before the dialog opens: a stale catalog must never
  // seed an edit's legacy inheritance, and a fetch error must never look like
  // an authoritative empty catalog.
  capabilities.value = null;
  capabilitiesLoading.value = true;
  capabilitiesError.value = '';
  try {
    const catalog = await platformApi.capabilities();
    if (request !== capabilitiesRequest) return;
    capabilities.value = catalog;
  } catch (e) {
    if (request !== capabilitiesRequest) return;
    capabilitiesError.value = (e as Error).message || tr('studioManagement.text582');
  } finally {
    if (request === capabilitiesRequest) capabilitiesLoading.value = false;
  }
}

const toastMsg = ref('');
let toastTimer: ReturnType<typeof setTimeout> | undefined;

function toast(msg: string) {
  toastMsg.value = msg;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toastMsg.value = '';
  }, 2400);
}

async function openCreate() {
  if (!props.canManage) return;
  void loadCapabilities();
  editorOpen.value = true;
  await nextTick();
  editor.value?.openCreate(props.agents);
}

async function startEdit(agent: PlatformAgent) {
  if (!props.canManage) return;
  void loadCapabilities();
  editorOpen.value = true;
  // Propagate the cleared catalog before opening; an older dialog's catalog
  // must not initialize this draft while the fresh request is still pending.
  await nextTick();
  editor.value?.openEdit(agent, props.agents);
}

async function createFromSkills(ids: string[], name: string) {
  if (!props.canManage) return;
  await loadCapabilities();
  if (!capabilities.value) { toast(capabilitiesError.value); return; }
  editorOpen.value = true;
  editor.value?.openCreate(props.agents, ids, name);
}
/** The admin's conversation preview stays inside the Studio, with a way back;
    the published /a/{id} URL is for visitors and is handed out by 发布与嵌入. */
function enterChat(agent: PlatformAgent) {
  if (agent.status !== 'ready') {
    startEdit(agent);
    return;
  }
  navigateStudio(managedChatPath(agent.id));
}

function showPublish(agent: PlatformAgent) {
  publishDlg.value?.open(agent);
}

function onEditorClosed() {
  editorOpen.value = false;
}

/** Called by the parent after a successful create/update. Never on failure. */
function creationSucceeded() {
  editor.value?.close();
  toast(tr('studioManagement.text583'));
  if (activeSection.value !== 'agents') void navigateStudio('/manage');
}
defineExpose({ creationSucceeded });

function onCreate(input: PlatformAgentInput, enterChat: boolean) {
  emit('create', input, enterChat);
}

function onUpdate(id: string, input: PlatformAgentInput, revision: number) {
  emit('update', id, input, revision);
}

/* ------------------------------ skills module ------------------------------ */

const skillsTab = ref<'library' | 'monitor'>('library');
const skillsRefreshKey = ref(0);
const skillsPanel = ref<InstanceType<typeof SkillLibrary> | null>(null);

function refreshSkills() {
  // A key remount is the honest refresh for both tabs: SkillsPanel refetches
  // its real catalog and the monitor restarts its own polling gate.
  if (skillsTab.value === 'library') void skillsPanel.value?.reload();
  else skillsRefreshKey.value++;
}

function gotoMcpTrace(traceId: string) {
  // The dedicated Skill page is no longer inside SettingsDialog, but the MCP
  // trace target still is; hand the trace over through the same one-shot key.
  sessionStorage.setItem('pogo:skill-mcp-trace', traceId);
  navigateStudio('/manage/mcp');
}


onBeforeUnmount(() => clearTimeout(toastTimer));
</script>

<template>
  <div class="studio">
    <StudioSidebar
      :open="navOpen"
      :section="activeSection"
      :agent-count="agents.length"
      :account-name="accountName"
      :password-enabled="props.passwordEnabled"
      @update:open="navOpen = $event"
    />

    <div class="page">
      <header v-if="activeSection !== 'information'" class="page-head">
        <button type="button" class="iconbtn nav-toggle" :aria-label="tr('studioManagement.text584')" @click="navOpen = true">
          <el-icon aria-hidden="true"><Menu /></el-icon>
        </button>
        <div class="head-text">
          <h1>{{ title }}</h1>
          <p>{{ subtitle }}</p>
        </div>
        <div v-if="activeSection === 'agents'" class="head-actions">
          <button
            type="button"
            class="iconbtn tip"
            :class="{ spinning: loading && !initialLoading }"
            :data-tip="tr('studioManagement.text585')"
            :aria-label="tr('studioManagement.text585')"
            :disabled="loading"
            @click="emit('refresh')"
          >
            <el-icon aria-hidden="true"><Refresh /></el-icon>
          </button>
          <button v-if="canManage" type="button" class="btn solid" @click="openCreate">
            <el-icon aria-hidden="true"><Plus /></el-icon> {{ tr('studioManagement.text050') }} </button>
        </div>
        <div v-else-if="activeSection === 'skills'" class="head-actions">
          <button
            type="button"
            class="iconbtn tip"
            :data-tip="tr('studioManagement.text586')"
            :aria-label="tr('studioManagement.text586')"
            @click="refreshSkills"
          >
            <el-icon aria-hidden="true"><Refresh /></el-icon>
          </button>
        </div>
      </header>

      <div v-if="error && !editorOpen" class="errorbar" role="alert">
        <span class="errorbar-msg">{{ error }}</span>
        <button type="button" class="btn ghost sm" @click="emit('refresh')">{{ tr('studioManagement.text104') }}</button>
        <button type="button" class="iconbtn sm" :aria-label="tr('studioManagement.text055')" @click="emit('dismissError')">
          <el-icon aria-hidden="true"><Close /></el-icon>
        </button>
      </div>

      <!-- ── 我的 Agents ─────────────────────────────────────────────── -->
      <section v-show="activeSection === 'agents'" class="page-body">
        <div class="toolbar">
          <div class="searchbox">
            <el-icon class="search-ico" aria-hidden="true"><Search /></el-icon>
            <input v-model="query" type="search" :placeholder="tr('studioManagement.text587')" :aria-label="tr('studioManagement.text588')" />
          </div>

          <div class="chip-row" role="group" :aria-label="tr('studioManagement.text589')">
            <button
              v-for="c in statusChips"
              :key="c.value"
              type="button"
              class="chip"
              :class="{ on: statusFilter === c.value }"
              :aria-pressed="statusFilter === c.value"
              @click="statusFilter = c.value"
            >
              {{ c.label }}
            </button>
          </div>

          <div class="viewtoggle" role="group" :aria-label="tr('studioManagement.text591')">
            <!-- tip-down: the toggle lives at the page's top, and an upward
                 bubble would cover the header actions above it. -->
            <button
              type="button"
              class="tip tip-down"
              :class="{ on: viewMode === 'grid' }"
              :data-tip="tr('studioManagement.text592')"
              :aria-label="tr('studioManagement.text592')"
              @click="viewMode = 'grid'"
            >
              <el-icon aria-hidden="true"><Grid /></el-icon>
            </button>
            <button
              type="button"
              class="tip tip-down"
              :class="{ on: viewMode === 'list' }"
              :data-tip="tr('studioManagement.text593')"
              :aria-label="tr('studioManagement.text593')"
              @click="viewMode = 'list'"
            >
              <el-icon aria-hidden="true"><List /></el-icon>
            </button>
          </div>
        </div>

        <div v-if="initialLoading" role="status" :aria-label="tr('studioManagement.text574')" aria-busy="true">
          <StudioEntrySkeleton area="cards" class="agent-catalog-skeleton" :class="viewMode" />
        </div>

        <div v-else-if="catalogReady !== false && !error && !agents.length" class="empty">
          <h2>{{ tr('studioManagement.text594') }}</h2>
          <p>{{ tr('studioManagement.text595') }}</p>
          <div class="empty-actions">
            <button v-if="canManage" type="button" class="btn solid" @click="openCreate">
              <el-icon aria-hidden="true"><Plus /></el-icon> {{ tr('studioManagement.text596') }} </button>
          </div>
        </div>

        <div v-else-if="catalogReady !== false && !error && !filtered.length" class="empty small">
          <h2>{{ tr('studioManagement.text597') }}</h2>
          <p>{{ tr('studioManagement.text598') }}</p>
          <div class="empty-actions">
            <button type="button" class="btn ghost" @click="clearFilters">{{ tr('studioManagement.text599') }}</button>
          </div>
        </div>

        <div v-else class="cards" :class="viewMode">
          <AgentCard
            v-for="a in filtered"
            :key="a.id"
            :agent="a"
            :can-manage="canManage"
            :model-label="agentModelsLabel(models, a)"
            :capability-summary="agentCapabilitySummary(a)"
            :layout="viewMode"
            @open="enterChat"
            @edit="startEdit"
            @publish="showPublish"
            @remove="(agent) => emit('remove', agent)"
          />
        </div>
      </section>


      <section v-show="activeSection === 'information'" class="page-body information-module" :aria-label="tr('studioManagement.text600')">
        <div v-if="initialLoading" role="status" :aria-label="tr('common.loading')" aria-busy="true"><StudioEntrySkeleton area="settings" /></div>
        <InformationLibrary v-else :active="activeSection === 'information'" :agents="agents"><template #navigation><button type="button" class="iconbtn nav-toggle information-nav-toggle" :aria-label="tr('studioManagement.text584')" @click="navOpen = true"><el-icon aria-hidden="true"><Menu /></el-icon></button></template></InformationLibrary>
      </section>

      <!-- ── Skill 管理：真实技能库 + 原调用监控 ───────────────────── -->
      <section v-show="activeSection === 'skills'" class="page-body skills-module" :aria-label="tr('studioManagement.text601')">
        <div v-if="initialLoading" role="status" :aria-label="tr('common.loading')" aria-busy="true"><StudioEntrySkeleton area="cards" /></div>
        <template v-else>
          <div class="module-tabs" role="tablist" :aria-label="tr('studioManagement.text602')">
            <button
              type="button"
              role="tab"
              class="module-tab"
              :class="{ on: skillsTab === 'library' }"
              :aria-selected="skillsTab === 'library'"
              @click="skillsTab = 'library'"
            > {{ tr('studioManagement.text603') }} </button>
            <button
              type="button"
              role="tab"
              class="module-tab"
              :class="{ on: skillsTab === 'monitor' }"
              :aria-selected="skillsTab === 'monitor'"
              @click="skillsTab = 'monitor'"
            > {{ tr('studioManagement.text604') }} </button>
          </div>

          <div v-if="skillsTab === 'library'" class="skills-holder">
            <SkillLibrary :key="`library-${skillsRefreshKey}`" ref="skillsPanel" :can-manage="canManage" @create="createFromSkills" />
          </div>
          <div v-else class="monitor-holder">
            <SkillMonitorPanel :key="`monitor-${skillsRefreshKey}`" :active="true" @show-mcp-trace="gotoMcpTrace" />
          </div>
        </template>
      </section>

      <!-- ── 配置模块：真实设置内容，内联而非弹窗 ────────────────────── -->
      <section v-show="activeSection !== 'agents' && activeSection !== 'skills' && activeSection !== 'information'" class="page-body module">
        <div v-if="initialLoading" role="status" :aria-label="tr('common.loading')" aria-busy="true"><StudioEntrySkeleton area="settings" /></div>
        <template v-else>
          <div v-show="activeSection === 'settings'" class="preferences-holder"><StudioPreferences /></div>
          <SettingsDialog
            :model-value="true"
            embedded
            :section="settingsSection"
            :memory-agent-id="memoryAgentId"
            @saved="emit('refresh')"
            @navigate-section="onSettingsNavigate"
          />
        </template>
      </section>
    </div>

    <AgentEditorDialog
      ref="editor"
      :models="models"
      :templates="templates"
      :capabilities="capabilities"
      :capabilities-loading="capabilitiesLoading"
      :capabilities-error="capabilitiesError"
      :saving="saving"
      :error="editorOpen ? error : ''"
      @create="onCreate"
      @update="onUpdate"
      @create-template="emit('createTemplate', $event)"
      @remove-template="emit('removeTemplate', $event)"
      @dismiss-error="emit('dismissError')"
      @retry-capabilities="loadCapabilities"
      @closed="onEditorClosed"
    />
    <PublishDialog ref="publishDlg" />

    <transition name="toast">
      <div v-if="toastMsg" class="toast" role="status">{{ toastMsg }}</div>
    </transition>
  </div>
</template>

<style scoped>
/* The Studio styles against the theme tokens only — Paper and Graphite (and
   every borrowed skin) inherit it whole, because nothing here names a colour
   the theme did not provide. */
.studio {
  display: flex;
  min-height: 100dvh;
  background: var(--el-bg-color-page);
  color: var(--el-text-color-primary);
  font-size: 14px;
  line-height: 1.55;
}

.page {
  flex: 1;
  min-width: 0;
  height: 100dvh;
  overflow-x: clip;
  display: flex;
  flex-direction: column;
}

/* One concise header: what this module is, and its primary action. */
.page-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 18px 28px 14px;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.head-text {
  min-width: 0;
  flex: 1;
}
.head-text h1 {
  margin: 0;
  font-size: 17px;
  font-weight: 700;
  letter-spacing: -0.01em;
}
.head-text p {
  margin: 3px 0 0;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
}

.nav-toggle {
  display: none;
}

.errorbar {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 14px 28px 0;
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  color: var(--el-color-danger);
  border-radius: 10px;
  padding: 10px 14px;
  font-size: 13px;
}
.errorbar-msg {
  flex: 1;
  min-width: 0;
}
.errorbar .iconbtn {
  color: var(--el-color-danger);
}

/* The content region scrolls; the header and the rail do not. */
.page-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 18px 28px 40px;
}
.page-body.information-module {
  display: flex;
  padding: 12px;
  overflow: hidden;
}
.page-body.information-module > :deep(.information-library) {
  flex: 1;
  min-height: 0;
  height: 100%;
  max-height: none;
}
.page-body.information-module .information-nav-toggle { display: none; }
.page-body.module {
  padding: 0;
  display: flex;
  flex-direction: column;
}
.page-body.module > * {
  flex: 1;
  min-height: 0;
}
.page-body.module > .preferences-holder { flex: 0 0 auto; }
.page-body.skills-module {
  padding: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.module-tabs {
  flex: none;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
}
.module-tab {
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font: inherit;
  font-size: 12.5px;
  padding: 6px 12px;
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.module-tab:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.module-tab.on {
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  font-weight: 600;
}
.skills-holder,
.monitor-holder {
  flex: 1;
  min-width: 0;
  min-height: 0;
}
.skills-holder {
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.monitor-holder {
  overflow-x: hidden;
  overflow-y: auto;
  padding: 14px 24px 30px;
  box-sizing: border-box;
}

/* The one toolbar: find, narrow, and choose the shape of the list. */
.toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 16px;
}
.searchbox {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1 1 200px;
  max-width: 300px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 0 12px;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.searchbox:focus-within {
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.search-ico {
  font-size: 15px;
  color: var(--el-text-color-placeholder);
  flex: none;
}
.searchbox input {
  border: 0;
  outline: 0;
  background: transparent;
  font: inherit;
  font-size: 13.5px;
  color: var(--el-text-color-primary);
  height: 35px;
  width: 100%;
  min-width: 0;
}
.searchbox input::placeholder {
  color: var(--el-text-color-placeholder);
}

.chip-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  border: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  border-radius: 999px;
  padding: 5px 13px;
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition), border-color var(--pg-transition);
}
.chip:hover {
  border-color: var(--el-border-color-hover);
}
.chip.on {
  background: var(--pg-accent-wash);
  border-color: var(--pg-accent-line);
  color: var(--el-color-primary);
  font-weight: 600;
}

.viewtoggle {
  margin-left: auto;
  display: flex;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 3px;
  gap: 2px;
}
.viewtoggle button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 27px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.viewtoggle button:hover {
  color: var(--el-text-color-primary);
}
.viewtoggle button.on {
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
}

.cards.grid,
.agent-catalog-skeleton.grid {
  display: grid;
  /* Three cards per row at most, however wide the window — wider cards carry
     more of each Agent's story than a fifth narrow column would. The max()
     floor keeps cards readable on narrower panes, where the fill drops to
     two columns, then one, on its own. */
  grid-template-columns: repeat(auto-fill, minmax(max(300px, calc((100% - 28px) / 3)), 1fr));
  gap: 14px;
}
.cards.list,
.agent-catalog-skeleton.list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.agent-catalog-skeleton.grid,
.agent-catalog-skeleton.list {
  padding: 0;
}
@media (min-width: 721px) {
  .agent-catalog-skeleton.list :deep(.entry-card) {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) minmax(0, 1.8fr) auto;
    align-items: center;
    gap: 16px;
    min-height: 0;
    padding: 13px 16px;
  }
  .agent-catalog-skeleton.list :deep(.entry-card-head) {
    min-width: 0;
    margin-bottom: 0;
  }
  .agent-catalog-skeleton.list :deep(.entry-card > .entry-line:not(:first-of-type):not(:last-child)) {
    display: none;
  }
  .agent-catalog-skeleton.list :deep(.entry-card > .entry-line:last-child) {
    width: 96px;
    height: 30px;
    border-radius: 6px;
  }
}

/* empty states: no illustration, one line of help and the one next action. */
.empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 72px 24px 80px;
  background: var(--el-bg-color);
  border: 1px dashed var(--el-border-color);
  border-radius: var(--pg-radius-card);
}
.empty.small {
  padding: 48px 24px 52px;
}
.empty h2 {
  margin: 0 0 8px;
  font-size: 17px;
  font-weight: 700;
}
.empty p {
  margin: 0 0 22px;
  color: var(--el-text-color-regular);
  font-size: 13.5px;
  max-width: 380px;
}
.empty-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  justify-content: center;
}

/* buttons & icon buttons, shared with the header and the empty states */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 8px 15px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  transition: background var(--pg-transition), border-color var(--pg-transition), color var(--pg-transition);
}
.btn.sm {
  padding: 6px 12px;
  font-size: 12.5px;
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
.btn.ghost {
  background: transparent;
  border-color: var(--el-border-color);
  color: var(--el-text-color-regular);
}
.btn.ghost:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.btn:disabled {
  opacity: 0.55;
  cursor: wait;
}

.iconbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.iconbtn.sm {
  width: 28px;
  height: 28px;
}
.iconbtn:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.iconbtn.spinning .el-icon {
  animation: spin 0.9s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .iconbtn.spinning .el-icon {
    animation: none;
  }
}

/* toast */
.toast {
  position: fixed;
  left: 50%;
  bottom: 34px;
  transform: translateX(-50%);
  z-index: 90;
  background: var(--pg-tooltip-bg);
  color: var(--pg-tooltip-fg);
  font-size: 13px;
  padding: 9px 18px;
  border-radius: 999px;
  box-shadow: var(--el-box-shadow);
}
.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translate(-50%, 8px);
}

@media (max-width: 860px) {
  .nav-toggle {
    display: inline-flex;
  }
  .page-head {
    padding: 13px 16px 11px;
  }
  .head-text p {
    white-space: normal;
  }
  .errorbar {
    margin: 10px 16px 0;
  }
  .page-body {
    padding: 14px 16px 32px;
  }
  .page-body.module {
    padding: 0;
  }
  .page-body.information-module { padding: 8px; }
  .page-body.information-module .information-nav-toggle { display: inline-flex; }
  .module-tabs {
    padding: 8px 14px;
    overflow-x: auto;
  }
  .monitor-holder {
    padding: 12px 14px 24px;
  }
  .viewtoggle {
    display: none;
  }
}

@media (max-width: 520px) {
  .head-actions .btn {
    padding: 8px 11px;
  }
}
</style>
