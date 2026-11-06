<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import StudioEntrySkeleton from "@/components/studio/StudioEntrySkeleton.vue";
import Logo from "@/components/Logo.vue";
import { studioKey } from "@/agent/studioContext";
import { STUDIO_SECTION_LABELS, type StudioSection } from "@/agent/studioPresentation";

const props = withDefaults(defineProps<{
  kind?: "chat" | "management" | "share";
  section?: StudioSection;
  agentId?: string;
  managedChat?: boolean;
  embedded?: boolean;
}>(), { kind: "management", section: "agents" });
const { t } = useI18n();
const bodyArea = computed(() => props.kind === "chat" ? "conversation"
  : props.section === "agents" || props.section === "skills" ? "cards" : "settings");
// Match AgentView's initial sidebar state before its chunk is available, so
// the transcript does not shift sideways when the real shell mounts.
const sidebarCollapsed = computed(() => {
  const agentId = props.agentId?.trim();
  if (agentId && window.matchMedia("(max-width: 720px)").matches) return true;
  const embedded = !!agentId && !props.managedChat && props.embedded === true;
  const preference = embedded ? studioKey(agentId!, "pogo:embed:sidebar-collapsed") : "pogo:sidebar-collapsed";
  try {
    return embedded ? localStorage.getItem(preference) !== "0" : localStorage.getItem(preference) === "1";
  } catch {
    return embedded;
  }
});
</script>

<template>
  <main v-if="kind === 'share'" class="share-entry" aria-busy="true">
    <span class="sr-only" role="status">{{ t("common.loading") }}</span>
    <header class="share-bar">
      <Logo />
      <span class="share-brand">pogo</span>
    </header>
    <article class="share-sheet">
      <StudioEntrySkeleton area="document" />
    </article>
  </main>
  <div v-else class="studio-entry-page" :class="[`${kind}-layout`, { 'sidebar-collapsed': kind === 'chat' && sidebarCollapsed, 'managed-chat': managedChat, 'information-layout': kind === 'management' && section === 'information' }]" aria-busy="true">
    <span class="sr-only" role="status">{{ t(kind === "chat" ? "uiCoverage.app.openingAgent" : "common.loading") }}</span>
    <aside v-if="kind === 'chat'" class="entry-sidebar" aria-hidden="true">
      <template v-if="sidebarCollapsed">
        <span v-for="i in 2" :key="i" class="entry-rail-icon" />
      </template>
      <template v-else>
        <div class="entry-new" />
        <div class="entry-search" />
        <StudioEntrySkeleton area="sessions" />
      </template>
    </aside>
    <aside v-else class="management-sidebar" aria-hidden="true">
      <div class="management-brand">
        <Logo :size="30" />
        <span><strong>pogo</strong><em>Agent Studio</em></span>
      </div>
      <div v-for="i in 7" :key="i" class="management-nav">
        <span class="management-nav-icon" />
        <span class="management-nav-line" />
      </div>
    </aside>
    <main class="entry-main">
      <header v-if="kind === 'chat' || section !== 'information'" class="entry-topbar">
        <template v-if="kind === 'management'">
          <h1>{{ STUDIO_SECTION_LABELS[section] }}</h1>
          <StudioEntrySkeleton area="agent" />
        </template>
        <template v-else>
          <span v-if="managedChat" class="entry-back" aria-hidden="true" />
          <StudioEntrySkeleton area="agent" />
        </template>
      </header>
      <div class="entry-body">
        <div v-if="kind === 'management' && section === 'agents'" class="entry-toolbar" aria-hidden="true">
          <span class="entry-search" />
          <span class="entry-filter" />
        </div>
        <StudioEntrySkeleton :area="bodyArea" :class="{ 'agent-catalog-skeleton': kind === 'management' && section === 'agents' }" />
      </div>
      <div v-if="kind === 'chat'" class="entry-composer" aria-hidden="true">
        <StudioEntrySkeleton area="composer" class="entry-composer-content" />
      </div>
    </main>
  </div>
</template>

<style scoped>
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}
.studio-entry-page {
  display: grid;
  grid-template-columns: 250px minmax(0, 1fr);
  width: 100%;
  height: 100dvh;
  overflow: hidden;
  background: var(--el-bg-color);
}
.entry-sidebar {
  padding: 12px 10px;
  background: var(--el-bg-color-page);
  border-right: 1px solid var(--el-border-color-lighter);
}
.chat-layout.sidebar-collapsed {
  grid-template-columns: 56px minmax(0, 1fr);
}
.sidebar-collapsed .entry-sidebar {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 12px 0;
}
.entry-rail-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: var(--el-fill-color-light);
}
.entry-new,
.entry-search {
  height: 40px;
  margin: 0 4px 15px;
  border-radius: 8px;
  background: var(--el-fill-color-light);
}
.entry-search {
  height: 34px;
  margin-bottom: 17px;
}
.entry-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.entry-topbar {
  display: flex;
  align-items: center;
  flex: none;
  gap: 12px;
  min-height: 37px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}
.entry-back {
  width: 92px;
  height: 26px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
}
.entry-body {
  flex: 1;
  min-height: 0;
  padding: 20px 28px 28px;
  overflow: hidden;
}
.entry-composer {
  box-sizing: border-box;
  flex: none;
  width: calc(100% - 50px);
  max-width: calc(var(--pg-measure) + 50px);
  height: 104px;
  margin: 16px auto;
  padding: 14px 12px 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  background: var(--el-bg-color);
}
.entry-composer-content {
  height: 100%;
}
.management-layout {
  grid-template-columns: 224px minmax(0, 1fr);
  background: var(--el-bg-color-page);
}
.management-layout .entry-body {
  padding: 0;
}
.management-layout .entry-topbar {
  align-items: flex-start;
  flex-direction: column;
  gap: 5px;
  min-height: 81px;
  padding: 18px 28px 14px;
  background: var(--el-bg-color);
}
.entry-topbar h1 {
  margin: 0;
  font-size: 17px;
  font-weight: 700;
  line-height: 1.55;
  letter-spacing: -0.01em;
}
.entry-toolbar {
  display: flex;
  align-items: center;
  gap: 18px;
  height: 72px;
  padding: 18px 28px;
}
.entry-toolbar .entry-search {
  flex: 0 1 320px;
  height: 36px;
  margin: 0;
}
.entry-filter {
  width: 180px;
  height: 28px;
  border-radius: 7px;
  background: var(--el-fill-color-light);
}
.management-layout .agent-catalog-skeleton {
  grid-template-columns: repeat(auto-fill, minmax(max(300px, calc((100% - 28px) / 3)), 1fr));
  padding-top: 0;
}
.information-layout .entry-body {
  margin: 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  background: var(--el-bg-color);
}
.management-sidebar {
  padding: 18px 12px;
  border-right: 1px solid var(--el-border-color-lighter);
}
.management-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2px 8px 25px;
}
.management-brand span {
  display: flex;
  flex-direction: column;
}
.management-brand strong {
  font-size: 16px;
}
.management-brand em {
  color: var(--el-text-color-secondary);
  font-size: 11px;
  font-style: normal;
}
.management-nav {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 42px;
  padding: 0 10px;
}
.management-nav-icon {
  width: 17px;
  height: 17px;
  border-radius: 5px;
  background: var(--el-fill-color);
}
.management-nav-line {
  width: 52%;
  height: 11px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
}
.management-nav:nth-child(3n) .management-nav-line {
  width: 66%;
}
.share-entry {
  min-height: 100dvh;
  padding: 0 16px 60px;
  background: var(--el-bg-color-page);
}
.share-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 780px;
  margin: 0 auto;
  padding: 18px 0 8px;
  color: var(--el-text-color-secondary);
}
.share-brand {
  font-size: 14px;
  font-weight: 600;
}
.share-sheet {
  max-width: 780px;
  min-height: 340px;
  box-sizing: border-box;
  margin: 14px auto 0;
  padding: 26px 30px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 14px;
  background: var(--el-bg-color);
}
@media (max-width: 860px) {
  .management-layout {
    grid-template-columns: minmax(0, 1fr);
  }
  .management-sidebar {
    display: none;
  }
  .management-layout .entry-topbar {
    min-height: 73px;
    padding: 13px 16px 11px;
  }
  .entry-toolbar {
    padding-inline: 16px;
  }
  .management-layout .agent-catalog-skeleton {
    padding-inline: 16px;
  }
  .information-layout .entry-body {
    margin: 8px;
  }
}
@media (max-width: 720px) {
  .chat-layout .entry-topbar {
    padding: 10px 14px;
  }
  .chat-layout .entry-body {
    padding: 18px 14px;
  }
  .entry-composer {
    width: calc(100% - 24px);
  }
}
@media (max-width: 700px) {
  .share-sheet {
    padding: 22px 20px;
  }
}
</style>
