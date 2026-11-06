<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  CaretTop,
  ChatDotRound,
  Collection,
  FolderOpened,
  Connection,
  Cpu,
  Document,
  Lock,
  Setting,
  SwitchButton,
} from "@element-plus/icons-vue";
import type { Component } from "vue";
import Logo from "@/components/Logo.vue";
import ThemePicker from "@/components/ThemePicker.vue";
import { navigateStudio } from "@/api/platform";
import { api, setToken } from "@/api/client";
import { visibleStudioNav, type StudioSection } from "@/agent/studioPresentation";

/**
 * The Studio's navigation rail: the same chrome surface the workbench sidebar
 * uses (paper behind chrome, canvas behind content), so moving between the
 * conversation and the Studio never changes what the window frame looks like.
 * Fixed 224px on desktop; an overlay with a backdrop on narrow screens.
 */
const props = withDefaults(
  defineProps<{
    section: StudioSection;
    /** Mobile overlay visibility, v-model:open. */
    open?: boolean;
    agentCount?: number;
    accountName?: string;
    /** Whether the users.json account system is on. Without it the account
     *  surfaces (账号与安全 / 设置) are not this deployment's to show. */
    passwordEnabled?: boolean;
  }>(),
  { open: false, agentCount: 0, accountName: "", passwordEnabled: false },
);

const emit = defineEmits<{ "update:open": [boolean] }>();
const { t: tr } = useI18n();

const SECTION_ICONS: Record<StudioSection, Component> = {
  agents: ChatDotRound,
  information: FolderOpened,
  models: Cpu,
  mcp: Connection,
  skills: Collection,
  memory: Document,
  account: Lock,
  settings: Setting,
};

const accountInitial = computed(() => {
  const n = (props.accountName ?? "").trim();
  return n ? n.charAt(0).toUpperCase() : "P";
});

/** The account chip's own menu — today just the way out. */
const accountMenuOpen = ref(false);

async function logout() {
  accountMenuOpen.value = false;
  // A ?token= still in the address bar would log straight back in on the
  // reload below; the query string goes before anything else does.
  if (location.search) history.replaceState(null, "", location.pathname);
  try {
    await api.logout();
  } catch {
    // The cookie is best-effort: the local token is cleared either way, and
    // the reload lands on the login gate.
  }
  setToken("");
  location.reload();
}

function go(path: string) {
  emit("update:open", false);
  navigateStudio(path);
}
</script>

<template>
  <transition name="fade">
    <div v-if="open" class="rail-backdrop" aria-hidden="true" @click="emit('update:open', false)" />
  </transition>

  <aside class="rail" :class="{ open }" :aria-label="tr('studioManagement.text504')">
    <div class="brand">
      <Logo :size="30" class="brand-logo" />
      <span class="brand-text">
        <strong>pogo</strong>
        <em>Agent Studio</em>
      </span>
    </div>

    <nav class="rail-nav">
      <button
        v-for="item in visibleStudioNav(passwordEnabled)"
        :key="item.section"
        type="button"
        class="rail-tab"
        :class="{ active: section === item.section }"
        :aria-current="section === item.section ? 'page' : undefined"
        @click="go(item.path)"
      >
        <el-icon class="rail-ico" aria-hidden="true"><component :is="SECTION_ICONS[item.section]" /></el-icon>
        <span class="rail-label">{{ item.label }}</span>
        <span v-if="item.section === 'agents' && agentCount > 0" class="rail-count">{{ agentCount }}</span>
      </button>
    </nav>

    <div class="rail-foot">
      <div class="rail-foot-row">
        <div class="account-wrap">
          <transition name="fade">
            <div v-if="accountMenuOpen" class="account-backdrop" aria-hidden="true" @click="accountMenuOpen = false" />
          </transition>
          <button
            type="button"
            class="account"
            :aria-expanded="accountMenuOpen"
            aria-haspopup="menu"
            :title="accountName || tr('studioManagement.text505')"
            @click="accountMenuOpen = !accountMenuOpen"
            @keydown.esc="accountMenuOpen = false"
          >
            <span class="account-initial" aria-hidden="true">{{ accountInitial }}</span>
            <span class="account-meta">
              <strong>{{ accountName || tr('studioManagement.text506') }}</strong>
              <em>{{ tr('studioManagement.text505') }}</em>
            </span>
            <el-icon class="account-caret" aria-hidden="true"><CaretTop /></el-icon>
          </button>
          <transition name="pop">
            <div v-if="accountMenuOpen" class="account-pop" role="menu" :aria-label="tr('studioManagement.text507')">
              <button type="button" role="menuitem" class="account-pop-item" @click="logout">
                <el-icon aria-hidden="true"><SwitchButton /></el-icon> {{ tr('studioManagement.text508') }} </button>
            </div>
          </transition>
        </div>
        <!-- The skin switcher, the same window preference the workbench sidebar
             keeps beside the account. -->
        <ThemePicker icon placement="top-start" />
      </div>
    </div>
  </aside>
</template>

<style scoped>
.rail {
  width: 224px;
  flex: none;
  display: flex;
  flex-direction: column;
  /* Chrome sits one surface below the content canvas — the workbench's own
     arrangement, so no border has to carry the separation alone. */
  background: var(--el-bg-color-page);
  border-right: 1px solid var(--el-border-color-lighter);
  padding: 18px 12px 12px;
  position: sticky;
  top: 0;
  height: 100dvh;
  z-index: 60;
  box-sizing: border-box;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2px 8px 18px;
}
.brand-logo {
  border-radius: 8px;
  box-shadow: var(--el-box-shadow-lighter);
}
.brand-text {
  display: flex;
  flex-direction: column;
  line-height: 1.2;
  min-width: 0;
}
.brand-text strong {
  color: var(--el-text-color-primary);
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.01em;
}
.brand-text em {
  font-style: normal;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  letter-spacing: 0.05em;
  margin-top: 2px;
}

.rail-nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 4px;
}
.rail-tab {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 13.5px;
  cursor: pointer;
  text-align: left;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.rail-tab:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.rail-tab.active {
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  font-weight: 600;
}
.rail-ico {
  font-size: 16px;
  flex: none;
  color: var(--el-text-color-secondary);
}
.rail-tab:hover .rail-ico {
  color: var(--el-text-color-regular);
}
.rail-tab.active .rail-ico {
  color: var(--el-color-primary);
}
.rail-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rail-count {
  margin-left: auto;
  flex: none;
  font-size: 11.5px;
  font-weight: 500;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  border-radius: 999px;
  padding: 1px 8px;
}
.rail-tab.active .rail-count {
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  box-shadow: inset 0 0 0 1px var(--pg-accent-line);
}

.rail-foot {
  margin-top: auto;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.rail-foot-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.rail-foot-row .account-wrap {
  flex: 1;
}
.account-wrap {
  position: relative;
  min-width: 0;
}
.account-backdrop {
  position: fixed;
  inset: 0;
  /* Under the rail (z-60) so the chip and menu stay clickable, over the page
     so any click outside closes the menu. */
  z-index: 59;
}
.account {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 0 6px;
  min-width: 0;
  border: 0;
  border-radius: 8px;
  background: transparent;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.account:hover {
  background: var(--el-fill-color-light);
}
.account:focus-visible {
  outline: none;
  box-shadow: var(--pg-ring);
}
.account-caret {
  flex: none;
  margin-left: auto;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  transition: transform var(--pg-transition);
}
.account[aria-expanded="true"] .account-caret {
  transform: rotate(180deg);
}
.account-pop {
  position: absolute;
  left: 0;
  right: 0;
  bottom: calc(100% + 8px);
  z-index: 61;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  box-shadow: var(--el-box-shadow-light);
  padding: 5px;
}
.account-pop-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  border: 0;
  border-radius: 7px;
  background: transparent;
  padding: 8px 10px;
  font: inherit;
  font-size: 12.8px;
  color: var(--el-text-color-regular);
  cursor: pointer;
  text-align: left;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.account-pop-item:hover {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.account-pop-item .el-icon {
  font-size: 14px;
}
.pop-enter-active,
.pop-leave-active {
  transition: opacity 0.14s ease, transform 0.14s ease;
}
.pop-enter-from,
.pop-leave-to {
  opacity: 0;
  transform: translateY(4px);
}
.account-initial {
  width: 30px;
  height: 30px;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  font-size: 12.5px;
  font-weight: 600;
  box-shadow: inset 0 0 0 1px var(--pg-accent-line);
}
.account-meta {
  display: flex;
  flex-direction: column;
  line-height: 1.3;
  min-width: 0;
}
.account-meta strong {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.account-meta em {
  font-style: normal;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

/* Skin and language, the same pair the workbench sidebar footer keeps: both
   are window preferences, so they live together here too. */
.prefs {
  display: flex;
  align-items: center;
  gap: 4px;
}
.lang-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-regular);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: background var(--pg-transition);
}
.lang-btn:hover {
  background: var(--el-fill-color-light);
}
.lang-btn .el-icon {
  font-size: 14px;
}

.rail-backdrop {
  display: none;
}

@media (max-width: 860px) {
  .rail {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    transform: translateX(-102%);
    visibility: hidden;
    transition: transform 0.24s ease, visibility 0.24s;
    box-shadow: none;
  }
  .rail.open {
    transform: translateX(0);
    visibility: visible;
    box-shadow: var(--el-box-shadow-dark);
  }
  .rail-backdrop {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 55;
    background: var(--el-mask-color);
  }
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
