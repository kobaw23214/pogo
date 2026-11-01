<script setup lang="ts">
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { platformApi } from "@/api/platform";
import type { WorkflowAccess, WorkflowSpaceRoles } from "@/agent/workflowTypes";

const props = defineProps<{ open: boolean; workflowId: string }>();
const emit = defineEmits<{ "update:open": [boolean]; changed: [] }>();
const { t } = useI18n();
const data = ref<WorkflowAccess>({ grants: [], subjects: [] });
const roles = ref<WorkflowSpaceRoles | null>(null);
const principal = ref("");
const rolePrincipal = ref("");
const role = ref("user");
const actions = ref<string[]>(["workflow.read", "workflow.invoke"]);
const loading = ref(false);
const busy = ref(false);
const error = ref("");
let epoch = 0;
const actionNames = ["workflow.read", "workflow.edit", "workflow.publish", "workflow.invoke", "workflow.delete", "workflow.schedule", "workflow.manage_access"];
const roleNames = ["user", "builder", "space_admin", "disabled"];
const actionLabel = (action: string) => t(`workflow.access.actions.${action.replaceAll(".", "_")}`);

async function load() {
  const request = ++epoch;
  loading.value = true;
  error.value = "";
  const [grants, spaceRoles] = await Promise.allSettled([platformApi.workflows.access(props.workflowId), platformApi.workflows.roles()]);
  if (request !== epoch) return;
  if (grants.status === "fulfilled") data.value = grants.value;
  else error.value = grants.reason instanceof Error ? grants.reason.message : String(grants.reason);
  roles.value = spaceRoles.status === "fulfilled" ? spaceRoles.value : null;
  loading.value = false;
}

async function saveAccess(principalId = principal.value, inherit = false) {
  if (!principalId || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await platformApi.workflows.setAccess(props.workflowId, { principal_id: principalId, ...(inherit ? { inherit: true } : { actions: [...actions.value] }) });
    await load();
    emit("changed");
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

async function saveRole() {
  if (!rolePrincipal.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await platformApi.workflows.setRole({ principal_id: rolePrincipal.value, role: role.value });
    await load();
    emit("changed");
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

function selectAccount(principalId: string) {
  principal.value = principalId;
  actions.value = [...(data.value.grants.find(item => item.principal_id === principalId)?.actions ?? ["workflow.read", "workflow.invoke"])];
}

function selectRoleAccount(principalId: string) {
  rolePrincipal.value = principalId;
  role.value = roles.value?.roles.find(item => item.principal_id === principalId)?.role || "user";
}

watch(() => [props.open, props.workflowId], () => {
  ++epoch;
  data.value = { grants: [], subjects: [] };
  roles.value = null;
  principal.value = "";
  rolePrincipal.value = "";
  if (props.open) void load();
});
</script>

<template>
  <el-drawer :model-value="open" :title="t('workflow.access.title')" direction="rtl" size="460px" @update:model-value="emit('update:open', $event)">
    <div class="wf-access">
      <p>{{ t("workflow.access.hint") }}</p>
      <p v-if="error" role="alert" class="wf-access-error">{{ error }}</p>
      <p v-if="loading">{{ t("workflow.participants.loading") }}</p>
      <template v-else>
        <div v-for="grant in data.grants" :key="grant.principal_id" class="wf-access-grant">
          <strong>{{ grant.display_name || grant.principal_id }}</strong>
          <span>{{ grant.actions.length ? grant.actions.map(actionLabel).join(" · ") : t("workflow.access.noActions") }}</span>
          <button type="button" :disabled="busy" @click="saveAccess(grant.principal_id, true)">{{ t("workflow.access.restore") }}</button>
        </div>
        <form class="wf-access-form" @submit.prevent="saveAccess()">
          <label>{{ t("workflow.participants.account") }}</label>
          <el-select :model-value="principal" filterable :disabled="busy" @update:model-value="selectAccount(String($event))">
            <el-option v-for="subject in data.subjects" :key="subject.principal_id" :value="subject.principal_id" :label="subject.display_name" />
          </el-select>
          <el-checkbox-group v-model="actions" :disabled="busy">
            <el-checkbox v-for="action in actionNames" :key="action" :value="action">{{ actionLabel(action) }}</el-checkbox>
          </el-checkbox-group>
          <p>{{ t("workflow.access.emptyHint") }}</p>
          <button type="submit" :disabled="busy || !principal">{{ t("workflow.participants.save") }}</button>
        </form>
        <form v-if="roles" class="wf-access-form" @submit.prevent="saveRole">
          <strong>{{ t("workflow.access.spaceRoles") }}</strong>
          <p>{{ t("workflow.access.rolesHint") }}</p>
          <el-select :model-value="rolePrincipal" filterable :disabled="busy" :placeholder="t('workflow.participants.account')" @update:model-value="selectRoleAccount(String($event))">
            <el-option v-for="subject in roles.subjects" :key="subject.principal_id" :value="subject.principal_id" :label="subject.display_name" />
          </el-select>
          <el-select v-model="role" :disabled="busy">
            <el-option v-for="value in roleNames" :key="value" :value="value" :label="t(`workflow.access.roles.${value}`)" />
          </el-select>
          <button type="submit" :disabled="busy || !rolePrincipal">{{ t("workflow.participants.save") }}</button>
        </form>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped>
.wf-access, .wf-access-grant, .wf-access-form { display: flex; flex-direction: column; gap: 10px; font-size: 13px; }
.wf-access p { margin: 0 0 8px; color: var(--el-text-color-secondary); }
.wf-access .wf-access-error { color: var(--el-color-danger); }
.wf-access-grant { padding: 12px; border-radius: 8px; background: var(--el-fill-color-light); }
.wf-access-grant span { overflow-wrap: anywhere; font-size: 12px; }
.wf-access-form { margin-top: 14px; padding-top: 16px; border-top: 1px solid var(--el-border-color); }
.wf-access button { border: 1px solid var(--el-border-color); background: var(--el-fill-color-blank); color: var(--el-text-color-primary); padding: 8px 12px; border-radius: 6px; cursor: pointer; }
.wf-access button:disabled { opacity: .5; cursor: default; }
</style>
