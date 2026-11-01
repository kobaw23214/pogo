<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { Close, Grid, List, Menu, Plus, Refresh, Search } from '@element-plus/icons-vue';
import StudioPreferences from '@/components/studio/StudioPreferences.vue';
const InformationLibrary = defineAsyncComponent(() => import('@/components/studio/InformationLibrary.vue'));
const SettingsDialog = defineAsyncComponent(() => import('@/components/SettingsDialog.vue'));
import AgentCard from '@/components/studio/AgentCard.vue';
import AgentEditorDialog from '@/components/studio/AgentEditorDialog.vue';
import PublishDialog from '@/components/studio/PublishDialog.vue';
import StudioSidebar from '@/components/studio/StudioSidebar.vue';
import SkillLibrary from '@/components/studio/SkillLibrary.vue';
import SkillMonitorPanel from '@/components/SkillMonitorPanel.vue';
import WorkflowList from '@/components/workflow/WorkflowList.vue';
import type { AgentTemplate, PlatformAgent, PlatformAgentInput, PlatformCapabilities, PlatformModel } from '@/agent/platformTypes';
import { navigateStudio, platformApi } from '@/api/platform';
import {
  STUDIO_SECTION_DESCS,
  STUDIO_SECTION_LABELS,
  agentStats,
  agentCapabilitySummary,
  agentModelsLabel,
  categoryChips,
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
 * time: the Agent catalog for 我的 Agents, the workflow list for 平台工作流
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
    saving: boolean;
    error: string;
    canManage: boolean;
    accountName: string;
    /** Whether the users.json account system is on. Without it the account
     *  modules fall back to the Agents page instead of rendering orphans. */
    passwordEnabled: boolean;
    section?: StudioSection;
  }>(),
  { section: 'agents', passwordEnabled: false },
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

const { t } = useI18n();

/* ---------------------------------- chrome ---------------------------------- */

const navOpen = ref(false);

/** The module that actually renders: the route's section, except that the
 *  account surfaces (账号与安全 / 设置) fall back to the Agents page on a
 *  deployment without the account system — the sidebar does not offer them
 *  there either, so a deep link must not land on an orphaned module. */
const activeSection = computed<StudioSection>(() => visibleSection(props.section, props.passwordEnabled));

const title = computed(() => activeSection.value === 'workflows' ? t('workflow.list.title') : STUDIO_SECTION_LABELS[activeSection.value]);
const subtitle = computed(() => {
  if (activeSection.value === 'workflows') return t('workflow.list.subtitle');
  if (activeSection.value !== 'agents') return STUDIO_SECTION_DESCS[activeSection.value];
  if (props.loading && !props.agents.length) return '正在载入你的工作空间…';
  const s = agentStats(props.agents, props.models);
  const parts = [`${s.total} 个 Agent`, `${s.ready} 已发布`, `${s.draft} 草稿`, `${s.availableModels} 个可用模型`];
  if (!props.canManage) parts.push('只读权限');
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
const categoryFilter = ref('all');
const statusFilter = ref<AgentStatusFilter>('all');
const viewMode = ref<'grid' | 'list'>('grid');

/* markdown role-card import: paste one document, get one Agent (server maps
   frontmatter → identity, body → instructions); success refreshes the list. */
const importOpen = ref(false);
const importText = ref('');
const importBusy = ref(false);
const importError = ref('');

function openImport() {
  importText.value = '';
  importError.value = '';
  importOpen.value = true;
}

async function submitImport() {
  if (importBusy.value || !importText.value.trim()) return;
  importBusy.value = true;
  importError.value = '';
  try {
    await platformApi.importAgent(importText.value);
    importOpen.value = false;
    toast('已导入');
    emit('refresh');
  } catch (e) {
    importError.value = (e as Error).message;
  } finally {
    importBusy.value = false;
  }
}

const statusChips = [
  { value: 'all' as const, label: '全部状态' },
  { value: 'ready' as const, label: '已发布' },
  { value: 'draft' as const, label: '草稿' },
];

const categories = computed(() => categoryChips(props.agents));
const filtered = computed(() => filterAgents(props.agents, query.value, statusFilter.value, categoryFilter.value));

function clearFilters() {
  query.value = '';
  categoryFilter.value = 'all';
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
    capabilitiesError.value = (e as Error).message || '技能与 MCP 清单加载失败';
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
  toast('已保存');
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

/* ------------------------------ workflows module ------------------------------ */

/** The workflows page owns its data (the shell's catalog is the Agents one);
    the header's refresh/create buttons reach in through this ref. */
const workflowPanel = ref<InstanceType<typeof WorkflowList> | null>(null);

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
        <button type="button" class="iconbtn nav-toggle" aria-label="打开导航" @click="navOpen = true">
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
            :class="{ spinning: loading }"
            data-tip="刷新列表"
            aria-label="刷新列表"
            :disabled="loading"
            @click="emit('refresh')"
          >
            <el-icon aria-hidden="true"><Refresh /></el-icon>
          </button>
          <button v-if="canManage" type="button" class="btn ghost" @click="openImport">
            {{ t("workflow.import.button") }}
          </button>
          <button v-if="canManage" type="button" class="btn solid" @click="openCreate">
            <el-icon aria-hidden="true"><Plus /></el-icon>
            创建 Agent
          </button>
        </div>
        <div v-else-if="activeSection === 'workflows'" class="head-actions">
          <button
            type="button"
            class="iconbtn tip"
            :data-tip="t('workflow.list.refresh')"
            :aria-label="t('workflow.list.refresh')"
            @click="workflowPanel?.reload()"
          >
            <el-icon aria-hidden="true"><Refresh /></el-icon>
          </button>
          <button v-if="canManage" type="button" class="btn solid" @click="workflowPanel?.openCreate()">
            <el-icon aria-hidden="true"><Plus /></el-icon>
            {{ t('workflow.list.create') }}
          </button>
        </div>
        <div v-else-if="activeSection === 'skills'" class="head-actions">
          <button
            type="button"
            class="iconbtn tip"
            data-tip="刷新 Skill 数据"
            aria-label="刷新 Skill 数据"
            @click="refreshSkills"
          >
            <el-icon aria-hidden="true"><Refresh /></el-icon>
          </button>
        </div>
      </header>

      <div v-if="error && !editorOpen" class="errorbar" role="alert">
        <span class="errorbar-msg">{{ error }}</span>
        <button type="button" class="btn ghost sm" @click="emit('refresh')">重试</button>
        <button type="button" class="iconbtn sm" aria-label="关闭提示" @click="emit('dismissError')">
          <el-icon aria-hidden="true"><Close /></el-icon>
        </button>
      </div>

      <!-- ── 我的 Agents ─────────────────────────────────────────────── -->
      <section v-show="activeSection === 'agents'" class="page-body">
        <div class="toolbar">
          <div class="searchbox">
            <el-icon class="search-ico" aria-hidden="true"><Search /></el-icon>
            <input v-model="query" type="search" placeholder="搜索名称、简介或模型" aria-label="搜索 Agent" />
          </div>

          <div class="chip-row" role="group" aria-label="按状态筛选">
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

          <div v-if="categories.length > 2" class="chip-row" role="group" aria-label="按分类筛选">
            <button
              v-for="c in categories"
              :key="c.value"
              type="button"
              class="chip"
              :class="{ on: categoryFilter === c.value }"
              :aria-pressed="categoryFilter === c.value"
              @click="categoryFilter = c.value"
            >
              {{ c.label }}
            </button>
          </div>

          <div class="viewtoggle" role="group" aria-label="切换视图">
            <!-- tip-down: the toggle lives at the page's top, and an upward
                 bubble would cover the header actions above it. -->
            <button
              type="button"
              class="tip tip-down"
              :class="{ on: viewMode === 'grid' }"
              data-tip="卡片视图"
              aria-label="卡片视图"
              @click="viewMode = 'grid'"
            >
              <el-icon aria-hidden="true"><Grid /></el-icon>
            </button>
            <button
              type="button"
              class="tip tip-down"
              :class="{ on: viewMode === 'list' }"
              data-tip="列表视图"
              aria-label="列表视图"
              @click="viewMode = 'list'"
            >
              <el-icon aria-hidden="true"><List /></el-icon>
            </button>
          </div>
        </div>

        <div v-if="loading && !agents.length" class="cards grid skel" aria-hidden="true">
          <div v-for="i in 6" :key="i" class="skel-card">
            <div class="skel-row">
              <span class="skel-block skel-avatar" />
              <div class="skel-lines">
                <span class="skel-block line w60" />
                <span class="skel-block line w40" />
              </div>
            </div>
            <span class="skel-block line w100" />
            <span class="skel-block line w80" />
          </div>
        </div>

        <div v-else-if="!agents.length" class="empty">
          <h2>还没有 Agent</h2>
          <p>从一个好名字和一段清晰的指令开始，几分钟后它就能陪你工作。</p>
          <div class="empty-actions">
            <button v-if="canManage" type="button" class="btn solid" @click="openCreate">
              <el-icon aria-hidden="true"><Plus /></el-icon>
              创建第一个 Agent
            </button>
          </div>
        </div>

        <div v-else-if="!filtered.length" class="empty small">
          <h2>没有匹配的 Agent</h2>
          <p>换个关键词，或清除当前筛选条件试试。</p>
          <div class="empty-actions">
            <button type="button" class="btn ghost" @click="clearFilters">清除筛选</button>
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

      <!-- ── 平台工作流：卡片列表 + 新建对话框，编辑器走全屏路由 ────── -->
      <section v-show="activeSection === 'workflows'" class="page-body" :aria-label="t('workflow.list.title')">
        <WorkflowList ref="workflowPanel" :can-manage="canManage" />
      </section>

      <section v-if="activeSection === 'information'" class="page-body information-module" aria-label="信息库"><InformationLibrary :agents="agents"><template #navigation><button type="button" class="iconbtn nav-toggle information-nav-toggle" aria-label="打开导航" @click="navOpen = true"><el-icon aria-hidden="true"><Menu /></el-icon></button></template></InformationLibrary></section>

      <!-- ── Skill 管理：真实技能库 + 原调用监控 ───────────────────── -->
      <section v-show="activeSection === 'skills'" class="page-body skills-module" aria-label="Skill 管理">
        <div class="module-tabs" role="tablist" aria-label="Skill 管理视图">
          <button
            type="button"
            role="tab"
            class="module-tab"
            :class="{ on: skillsTab === 'library' }"
            :aria-selected="skillsTab === 'library'"
            @click="skillsTab = 'library'"
          >
            技能库
          </button>
          <button
            type="button"
            role="tab"
            class="module-tab"
            :class="{ on: skillsTab === 'monitor' }"
            :aria-selected="skillsTab === 'monitor'"
            @click="skillsTab = 'monitor'"
          >
            调用监控
          </button>
        </div>

        <div v-if="skillsTab === 'library'" class="skills-holder">
          <SkillLibrary :key="`library-${skillsRefreshKey}`" ref="skillsPanel" :can-manage="canManage" @create="createFromSkills" />
        </div>
        <div v-else class="monitor-holder">
          <SkillMonitorPanel :key="`monitor-${skillsRefreshKey}`" :active="true" @show-mcp-trace="gotoMcpTrace" />
        </div>
      </section>

      <!-- ── 配置模块：真实设置内容，内联而非弹窗 ────────────────────── -->
      <section v-show="activeSection !== 'agents' && activeSection !== 'workflows' && activeSection !== 'skills' && activeSection !== 'information'" class="page-body module">
        <div v-show="activeSection === 'settings'" class="preferences-holder"><StudioPreferences /></div>
        <SettingsDialog
          :model-value="true"
          embedded
          :section="settingsSection"
          @saved="emit('refresh')"
          @navigate-section="onSettingsNavigate"
        />
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

    <el-dialog
      :model-value="importOpen"
      :title="t('workflow.import.title')"
      width="min(560px, 94vw)"
      :close-on-click-modal="false"
      @update:model-value="importOpen = $event"
    >
      <div class="import-form">
        <p class="import-hint">{{ t("workflow.import.hint") }}</p>
        <el-input
          v-model="importText"
          type="textarea"
          :rows="12"
          :placeholder="t('workflow.import.placeholder')"
        />
        <p v-if="importError" class="import-error" role="alert">{{ t("workflow.import.failed", { msg: importError }) }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn ghost" :disabled="importBusy" @click="importOpen = false">{{ t("workflow.list.cancel") }}</button>
        <button type="button" class="btn solid" :disabled="importBusy || !importText.trim()" @click="submitImport">
          {{ importBusy ? t("workflow.import.busy") : t("workflow.import.confirm") }}
        </button>
      </template>
    </el-dialog>

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

.cards.grid {
  display: grid;
  /* Three cards per row at most, however wide the window — wider cards carry
     more of each Agent's story than a fifth narrow column would. The max()
     floor keeps cards readable on narrower panes, where the fill drops to
     two columns, then one, on its own. */
  grid-template-columns: repeat(auto-fill, minmax(max(300px, calc((100% - 28px) / 3)), 1fr));
  gap: 14px;
}
.cards.list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

/* skeleton */
.skel {
  pointer-events: none;
}
.skel-card {
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.skel-row {
  display: flex;
  gap: 12px;
  align-items: center;
}
.skel-lines {
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: 1;
}
.skel-block {
  display: block;
  background: linear-gradient(
    90deg,
    var(--el-fill-color-light) 25%,
    var(--el-fill-color-lighter) 50%,
    var(--el-fill-color-light) 75%
  );
  background-size: 200% 100%;
  animation: shimmer 1.4s ease-in-out infinite;
}
.skel-avatar {
  width: 44px;
  height: 44px;
  border-radius: 13px;
  flex: none;
}
.skel-block.line {
  height: 12px;
  border-radius: 6px;
}
.w40 {
  width: 40%;
}
.w60 {
  width: 60%;
}
.w80 {
  width: 80%;
}
.w100 {
  width: 100%;
}
@keyframes shimmer {
  0% {
    background-position: 180% 0;
  }
  100% {
    background-position: -20% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .skel-block {
    animation: none;
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

/* markdown import dialog */
.import-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.import-hint {
  margin: 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}
.import-error {
  margin: 0;
  font-size: 12px;
  color: var(--el-color-danger);
  line-height: 1.6;
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
