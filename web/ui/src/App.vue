<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElConfigProvider } from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import en from "element-plus/es/locale/lang/en";
import { parseStudioRoute, workbenchRedirect } from "@/agent/studioRoutes";
import { confirmStudioNavigation } from "@/agent/studioNavigation";
import { clearUIIcons, refreshUIIcons } from "@/agent/uiIcons";
import { workflowEmbedId } from "@/agent/workflowEmbedProtocol";
const AgentView = defineAsyncComponent(() => import("@/views/AgentView.vue"));
const ShareView = defineAsyncComponent(() => import("@/views/ShareView.vue"));
const StudioShell = defineAsyncComponent(() => import("@/views/StudioShell.vue"));
const WorkflowEmbedView = defineAsyncComponent(() => import("@/views/WorkflowEmbedView.vue"));
const pathname = ref(location.pathname);
const search = ref(location.search);
// Agent-less workbench addresses are closed on the client too (the server
// hands the app shell to any path): a stray path would otherwise still mount
// the workbench and its no-Agent conversations. The query — the ?token=
// spelling above all — rides along to the home route.
function leaveWorkbenchPath() {
  const to = workbenchRedirect(pathname.value);
  if (!to) return;
  history.replaceState(null, "", to + search.value);
  pathname.value = to;
}
leaveWorkbenchPath();
const updatePath = async (event: Event) => {
  if (event.type === "popstate" && !(await confirmStudioNavigation())) {
    history.pushState(null, "", `${pathname.value}${search.value}`);
    return;
  }
  pathname.value = location.pathname;
  search.value = location.search;
  leaveWorkbenchPath();
};
window.addEventListener("popstate", updatePath);
window.addEventListener("pogo:studio-navigation", updatePath);
onBeforeUnmount(() => {
  window.removeEventListener("popstate", updatePath);
  window.removeEventListener("pogo:studio-navigation", updatePath);
});
const studioRoute = computed(() => parseStudioRoute(pathname.value, search.value));
const embeddedWorkflow = computed(() => workflowEmbedId(pathname.value));
const studioChat = computed(() => studioRoute.value?.kind === "chat" ? studioRoute.value : null);
const studioWorkflow = computed(() => studioRoute.value?.kind === "workflow" ? studioRoute.value : null);
const studioSection = computed(() => studioRoute.value?.kind === "manage" ? studioRoute.value.section : "agents");
// Published, managed and workflow-editor views never inherit each other's mounted UI state.
const studioViewKey = computed(() => studioChat.value
  ? `${studioChat.value.agentId}:${studioChat.value.managedChat}:${studioChat.value.embedded}`
  : studioWorkflow.value
    ? `workflow:${studioWorkflow.value.workflowId}`
    : "management");

// Element Plus's own built-in copy (dialog buttons, popconfirm, ...) follows
// the app locale; our own strings go through vue-i18n message files.
const { locale } = useI18n();
const elLocales = { "zh-CN": zhCn, en } as const;
const elLocale = computed(
  () => elLocales[locale.value as keyof typeof elLocales] ?? zhCn,
);

// /share/{id} is the public page: the server serves it without a token (it is
// not under /api), and routing it to its own view keeps every gate — token,
// login, workspace bootstrap — out of an anonymous visitor's way. The check
// is pathname-only, alongside the Studio's local history navigation.
const isSharePage = computed(() => pathname.value.startsWith("/share/"));
// Anonymous shares lazily resolve only the immutable icon IDs they render.
// Authenticated surfaces refresh the library without keeping it in storage.
const refreshIcons = () => { if (!isSharePage.value && !embeddedWorkflow.value) void refreshUIIcons(); };
const authChanged = (event: Event) => { clearUIIcons(); if ((event as CustomEvent<{authenticated?:boolean}>).detail?.authenticated !== false) refreshIcons(); };
onMounted(() => { refreshIcons(); window.addEventListener('focus', refreshIcons); window.addEventListener('pogo:auth-changed', authChanged); window.addEventListener('pagehide', clearUIIcons); });
onBeforeUnmount(() => { window.removeEventListener('focus', refreshIcons); window.removeEventListener('pogo:auth-changed', authChanged); window.removeEventListener('pagehide', clearUIIcons); clearUIIcons(); });

</script>

<template>
  <el-config-provider :locale="elLocale">
    <WorkflowEmbedView v-if="embeddedWorkflow" :key="embeddedWorkflow" :publication-id="embeddedWorkflow" />
    <StudioShell
      v-else-if="studioRoute"
      :key="studioViewKey"
      :agent-id="studioChat?.agentId"
      :managed-chat="studioChat?.managedChat ?? (studioRoute.kind === 'invalid' && studioRoute.management)"
      :embedded="studioChat?.embedded"
      :workflow-id="studioWorkflow?.workflowId"
      :section="studioSection"
      :invalid-route="studioRoute.kind === 'invalid'"
    />
    <ShareView v-else-if="isSharePage" />
    <AgentView v-else />
  </el-config-provider>
</template>
