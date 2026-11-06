<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
import { defineAsyncComponent, h, onMounted, ref } from "vue";
const ManageView = defineAsyncComponent({
  loader: () => import("./ManageView.vue"),
  loadingComponent: () => h(StudioEntryPage, { kind: "management", section: props.section }),
  delay: 0,
});
const AgentView = defineAsyncComponent({
  loader: () => import("./AgentView.vue"),
  loadingComponent: () => h(StudioEntryPage, {
    kind: "chat",
    agentId: props.agentId,
    managedChat: props.managedChat,
    embedded: props.embedded,
  }),
  delay: 0,
});
import StudioLogin from "@/components/studio/StudioLogin.vue";
import StudioEntryPage from "@/components/studio/StudioEntryPage.vue";
import Logo from "@/components/Logo.vue";
import { api, setToken } from "@/api/client";
import { navigateStudio, platformApi } from "@/api/platform";
import type { AgentTemplate, PlatformAgent, PlatformAgentInput, PlatformModel } from "@/agent/platformTypes";
import { managedChatPath, type StudioSection } from "@/agent/studioPresentation";

/**
 * The Studio's route switch and its two gates.
 *
 * Management routes (/manage…) load the real catalog and render ManageView;
 * the route is not authentication — StudioLogin is the workspace's own
 * password/token gate, and the backend's can_manage decides what the catalog
 * allows. Chat routes (/a/{id}, /manage/agents/{id}/chat) render the original
 * full-Pogo AgentView directly: it owns its auth and agent gate, and a
 * published visitor never touches the admin management API.
 *
 * Everything here styles against the theme tokens, so Paper and Graphite (and
 * every other skin) inherit the shell whole.
 */
const props = withDefaults(
  defineProps<{
    agentId?: string;
    invalidRoute?: boolean;
    section?: StudioSection;
    memoryAgentId?: string;
    managedChat?: boolean;
    embedded?: boolean;
  }>(),
  { section: "agents" },
);

const agents = ref<PlatformAgent[]>([]);
const models = ref<PlatformModel[]>([]);
const templates = ref<AgentTemplate[]>([]);
const loading = ref(true);
const catalogReady = ref(false);
const saving = ref(false);
const error = ref("");
const canManage = ref(false);
const accountName = ref("");
const authRequired = ref(false);
const passwordEnabled = ref(false);
const loginBusy = ref(false);
const loginError = ref("");
const manager = ref<InstanceType<typeof ManageView> | null>(null);
const deleteDialog = ref<HTMLDialogElement | null>(null);
const deletingAgent = ref<PlatformAgent | null>(null);
const deleteError = ref("");
const deleteBusy = ref(false);
let loadRequest = 0;
let agentMutation = 0;

async function load() {
  const request = ++loadRequest;
  const mutation = agentMutation;
  loading.value = true;
  error.value = "";
  try {
    const auth = await api.authStatus();
    passwordEnabled.value = auth.password;
    accountName.value = auth.user || "";
    const [data, catalog] = await Promise.all([platformApi.list(), api.models()]);
    if (request !== loadRequest) return;
    // A refresh started before a successful save contains an older catalog.
    // The write response remains authoritative until a newer refresh begins.
    if (mutation === agentMutation) agents.value = data.agents;
    canManage.value = data.can_manage;
    models.value = catalog.models.map((model) => ({
      id: model.id,
      provider: model.provider,
      available: model.configured,
    }));
    catalogReady.value = true;
    authRequired.value = false;
    // Templates are dialog furniture, not catalog data: a failed fetch must
    // not blank the manage page, and the dialog surfaces its own retry.
    if (data.can_manage) {
      platformApi.templates.list().then(
        (out) => { templates.value = out.templates; },
        () => { templates.value = []; },
      );
    }
  } catch (e) {
    if (request !== loadRequest) return;
    if ((e as { status?: number }).status === 401) authRequired.value = true;
    else error.value = (e as Error).message || tr('studioManagement.text606');
  } finally {
    if (request === loadRequest) loading.value = false;
  }
}

async function login(payload: { mode: "password" | "token"; username: string; password: string; accessKey: string }) {
  if (loginBusy.value) return;
  loginError.value = "";
  if (payload.mode === "token") {
    if (!payload.accessKey.trim()) {
      loginError.value = tr('studioManagement.text478');
      return;
    }
    loginBusy.value = true;
    try {
      // Validate before storing: a wrong key reloads into the same login
      // form with no explanation if it is saved blind.
      await api.verifyAccessKey(payload.accessKey.trim());
      setToken(payload.accessKey.trim());
      location.reload();
    } catch (e) {
      loginError.value =
        (e as { status?: number }).status === 401 ? tr('studioManagement.text607') : (e as Error).message;
    } finally {
      loginBusy.value = false;
    }
    return;
  }
  if (!payload.username.trim() || !payload.password) {
    loginError.value = tr('studioManagement.text477');
    return;
  }
  loginBusy.value = true;
  try {
    await api.login(payload.username.trim(), payload.password);
    await load();
  } catch (e) {
    loginError.value =
      (e as { status?: number }).status === 401 ? tr('studioManagement.text608') : (e as Error).message;
  } finally {
    loginBusy.value = false;
  }
}

async function createAgent(input: PlatformAgentInput, enterChat: boolean) {
  if (saving.value) return;
  saving.value = true;
  error.value = "";
  try {
    const created = await platformApi.create(input);
    agentMutation++;
    agents.value.unshift(created);
    manager.value?.creationSucceeded();
    // 创建并进入对话 lands on the managed preview, which carries the way back.
    if (enterChat) navigateStudio(managedChatPath(created.id));
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    saving.value = false;
  }
}

async function updateAgent(id: string, input: PlatformAgentInput, revision: number) {
  if (saving.value) return;
  const current = agents.value.find((agent) => agent.id === id);
  if (!current) return;
  saving.value = true;
  error.value = "";
  try {
    const updated = await platformApi.update(id, input, revision);
    agentMutation++;
    agents.value = agents.value.map((agent) =>
      agent.id === id ? { ...updated, conversation_count: agent.conversation_count, user_message_count: agent.user_message_count } : agent,
    );
    manager.value?.creationSucceeded();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    saving.value = false;
  }
}

function requestRemove(agent: PlatformAgent) {
  deletingAgent.value = agent;
  deleteError.value = "";
  deleteDialog.value?.showModal();
}

/** Template curation shares the agent save channel: one busy flag, one error
 *  banner — the dialog stays focused on the draft either way. */
async function createTemplate(input: Omit<AgentTemplate, "id">): Promise<AgentTemplate | null> {
  if (saving.value) return null;
  saving.value = true;
  error.value = "";
  try {
    const tpl = await platformApi.templates.create(input);
    templates.value = [...templates.value, tpl];
    return tpl;
  } catch (e) {
    error.value = (e as Error).message;
    return null;
  } finally {
    saving.value = false;
  }
}

async function removeTemplate(id: string): Promise<boolean> {
  if (saving.value) return false;
  saving.value = true;
  error.value = "";
  try {
    await platformApi.templates.remove(id);
    templates.value = templates.value.filter((tpl) => tpl.id !== id);
    return true;
  } catch (e) {
    error.value = (e as Error).message;
    return false;
  } finally {
    saving.value = false;
  }
}

async function removeAgent() {
  const target = deletingAgent.value;
  if (!target || deleteBusy.value) return;
  deleteBusy.value = true;
  try {
    await platformApi.remove(target.id);
    agentMutation++;
    agents.value = agents.value.filter((agent) => agent.id !== target.id);
    deleteDialog.value?.close();
    deletingAgent.value = null;
  } catch (e) {
    deleteError.value = (e as Error).message;
  } finally {
    deleteBusy.value = false;
  }
}

// Only management routes touch the management catalog; a chat route is the
// original workbench and gates itself.
onMounted(() => {
  if (!props.agentId && !props.invalidRoute) void load();
  else loading.value = false;
});
</script>

<template>
  <!-- Chat: the full Pogo workbench, unchanged — managed preview or published
       page, with the embed variant AgentView already knows. -->
  <div v-if="props.agentId" class="studio-workbench">
    <AgentView
      :key="props.agentId"
      :studio-agent-id="props.agentId"
      :managed-chat="props.managedChat"
      :embedded="props.embedded"
    />
  </div>

  <div v-else class="studio-shell">
    <StudioLogin
      v-if="authRequired"
      :password-enabled="passwordEnabled"
      :busy="loginBusy"
      :error="loginError"
      @submit="login"
      @clear-error="loginError = ''"
    />

    <div v-else-if="props.invalidRoute" class="studio-fallback">
      <Logo :size="36" class="fallback-logo" />
      <h1>{{ tr('studioManagement.text609') }}</h1>
      <p>{{ props.managedChat ? tr('studioManagement.text610') : tr('studioManagement.text611') }}</p>
      <button v-if="props.managedChat" class="primary inline" @click="navigateStudio('/manage')">{{ tr('studioManagement.text612') }}</button>
    </div>

    <ManageView
      v-else
      ref="manager"
      :agents="agents"
      :models="models"
      :templates="templates"
      :loading="loading"
      :catalog-ready="catalogReady"
      :saving="saving"
      :error="error"
      :can-manage="canManage"
      :account-name="accountName || tr('studioManagement.text605')"
      :password-enabled="passwordEnabled"
      :section="props.section"
      :memory-agent-id="props.memoryAgentId"
      @create="createAgent"
      @update="updateAgent"
      @remove="requestRemove"
      @create-template="createTemplate"
      @remove-template="removeTemplate"
      @refresh="load"
      @dismiss-error="error = ''"
    />

    <dialog ref="deleteDialog" class="delete-dialog" @cancel="deleteBusy && $event.preventDefault()">
      <span class="delete-glyph" aria-hidden="true">−</span>
      <h2>{{ tr('studioManagement.text613') }}</h2>
      <p>{{ tr('studioSentences.removeAgent', { name: deletingAgent?.name ?? '' }) }}</p>
      <p v-if="deleteError" class="form-error" role="alert">{{ deleteError }}</p>
      <div class="dialog-actions">
        <button class="ghost" :disabled="deleteBusy" @click="deleteDialog?.close()">{{ tr('studioManagement.text615') }}</button>
        <button class="danger" :disabled="deleteBusy" @click="removeAgent">
          {{ deleteBusy ? tr('studioManagement.text616') : tr('studioManagement.text617') }}
        </button>
      </div>
    </dialog>
  </div>
</template>

<style scoped>
/* Tokens only: the shell inherits Paper/Graphite/every skin from the document
   theme instead of pinning its own palette. */
.studio-workbench {
  height: 100dvh;
  width: 100%;
}
.studio-shell {
  height: 100dvh;
  width: 100%;
  background: var(--el-bg-color-page);
  color: var(--el-text-color-primary);
}

/* The login gate lives in components/studio/StudioLogin.vue; what remains
   here is shared by the fallback and the delete dialog. */
.form-error {
  font-size: 12px;
  color: var(--el-color-danger);
  line-height: 1.7;
  margin: 4px 0 0;
}
.primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border: 0;
  background: var(--pg-solid);
  color: var(--pg-on-solid);
  border-radius: 8px;
  padding: 11px 18px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.primary:hover {
  background: var(--pg-solid-hover);
}
.primary:disabled {
  opacity: 0.55;
  cursor: wait;
}

/* invalid route */
.studio-fallback {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 24px;
  text-align: center;
}
.fallback-logo {
  border-radius: 9px;
  margin-bottom: 6px;
}
.studio-fallback h1 {
  font-size: 20px;
  margin: 4px 0 2px;
}
.studio-fallback p {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin: 0 0 16px;
}

/* delete confirmation */
.delete-dialog {
  width: min(400px, calc(100vw - 40px));
  box-sizing: border-box;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 26px;
  color: var(--el-text-color-primary);
  background: var(--el-bg-color);
  box-shadow: var(--el-box-shadow-dark);
}
.delete-dialog::backdrop {
  background: var(--el-mask-color);
}
.delete-glyph {
  display: grid;
  place-items: center;
  width: 36px;
  height: 36px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  border-radius: 10px;
  font-size: 22px;
}
.delete-dialog h2 {
  font-size: 17px;
  margin: 16px 0 8px;
}
.delete-dialog p {
  font-size: 13px;
  line-height: 1.8;
  color: var(--el-text-color-secondary);
  margin: 0 0 4px;
}
.dialog-actions {
  display: flex;
  gap: 10px;
  justify-content: flex-end;
  margin-top: 22px;
}
.dialog-actions button {
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  padding: 9px 15px;
  font: inherit;
  font-size: 12.5px;
  font-weight: 550;
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  cursor: pointer;
  transition: background var(--pg-transition), border-color var(--pg-transition);
}
.dialog-actions button.ghost:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.dialog-actions .danger {
  background: var(--el-color-danger);
  border-color: var(--el-color-danger);
  color: var(--el-color-white, #fff);
}
.dialog-actions .danger:hover {
  background: var(--el-color-danger-dark-2);
  border-color: var(--el-color-danger-dark-2);
}
.dialog-actions button:disabled {
  opacity: 0.5;
  cursor: wait;
}
</style>
