<script setup lang="ts">
import { computed, defineAsyncComponent, h, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ElConfigProvider } from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import en from "element-plus/es/locale/lang/en";
import zhTw from "element-plus/es/locale/lang/zh-tw";
import ja from "element-plus/es/locale/lang/ja";
import ko from "element-plus/es/locale/lang/ko";
import de from "element-plus/es/locale/lang/de";
import fr from "element-plus/es/locale/lang/fr";
import es from "element-plus/es/locale/lang/es";
import { parseStudioRoute, workbenchRedirect } from "@/agent/studioRoutes";
import { confirmStudioNavigation } from "@/agent/studioNavigation";
import { clearUIIcons, refreshUIIcons } from "@/agent/uiIcons";
import StudioEntryPage from "@/components/studio/StudioEntryPage.vue";
const AgentView = defineAsyncComponent({
  loader: () => import("@/views/AgentView.vue"),
  loadingComponent: () => h(StudioEntryPage, { kind: "chat" }),
  delay: 0,
});
const ShareView = defineAsyncComponent({
  loader: () => import("@/views/ShareView.vue"),
  loadingComponent: () => h(StudioEntryPage, { kind: "share" }),
  delay: 0,
});
const StudioShell = defineAsyncComponent({
  loader: () => import("@/views/StudioShell.vue"),
  loadingComponent: () => h(StudioEntryPage, {
    kind: studioChat.value ? "chat" : "management",
    section: studioSection.value,
    agentId: studioChat.value?.agentId,
    managedChat: studioChat.value?.managedChat,
    embedded: studioChat.value?.embedded,
  }),
  delay: 0,
});
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
const studioChat = computed(() => studioRoute.value?.kind === "chat" ? studioRoute.value : null);
const studioSection = computed(() => studioRoute.value?.kind === "manage" ? studioRoute.value.section : "agents");
const memoryAgentId = computed(() => studioSection.value === "memory" ? new URLSearchParams(search.value).get("agent") || undefined : undefined);
// Published and managed views never inherit each other's mounted UI state.
const studioViewKey = computed(() => studioChat.value
  ? `${studioChat.value.agentId}:${studioChat.value.managedChat}:${studioChat.value.embedded}`
  : "management");

// Element Plus's own built-in copy (dialog buttons, popconfirm, ...) follows
// the app locale; our own strings go through vue-i18n message files.
const { locale } = useI18n();
const elLocales = { "zh-CN": zhCn, "zh-TW": zhTw, en, ja, ko, de, fr, es } as const;
const elLocale = computed(
  () => elLocales[locale.value as keyof typeof elLocales] ?? en,
);

// /share/{id} is the public page: the server serves it without a token (it is
// not under /api), and routing it to its own view keeps every gate — token,
// login, workspace bootstrap — out of an anonymous visitor's way. The check
// is pathname-only, alongside the Studio's local history navigation.
const isSharePage = computed(() => pathname.value.startsWith("/share/"));
// Anonymous shares lazily resolve only the immutable icon IDs they render.
// Authenticated surfaces refresh the library without keeping it in storage.
const refreshIcons = () => { if (!isSharePage.value) void refreshUIIcons(); };
const authChanged = (event: Event) => { clearUIIcons(); if ((event as CustomEvent<{authenticated?:boolean}>).detail?.authenticated !== false) refreshIcons(); };
onMounted(() => { refreshIcons(); window.addEventListener('focus', refreshIcons); window.addEventListener('pogo:auth-changed', authChanged); window.addEventListener('pagehide', clearUIIcons); });
onBeforeUnmount(() => { window.removeEventListener('focus', refreshIcons); window.removeEventListener('pogo:auth-changed', authChanged); window.removeEventListener('pagehide', clearUIIcons); clearUIIcons(); });

</script>

<template>
  <el-config-provider :locale="elLocale">
    <StudioShell
      v-if="studioRoute"
      :key="studioViewKey"
      :agent-id="studioChat?.agentId"
      :managed-chat="studioChat?.managedChat ?? (studioRoute.kind === 'invalid' && studioRoute.management)"
      :embedded="studioChat?.embedded"
      :section="studioSection"
      :memory-agent-id="memoryAgentId"
      :invalid-route="studioRoute.kind === 'invalid'"
    />
    <ShareView v-else-if="isSharePage" :key="pathname" />
    <AgentView v-else />
  </el-config-provider>
</template>
