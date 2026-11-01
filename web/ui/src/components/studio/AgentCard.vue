<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import {
  ArrowRight,
  ChatDotRound,
  Delete,
  Edit,
  Link,
  MoreFilled,
} from "@element-plus/icons-vue";
import AgentAvatar from "@/components/AgentAvatar.vue";
import type { PlatformAgent } from "@/agent/platformTypes";
import { categoryLabel, formatAgentTime } from "@/agent/studioPresentation";

/**
 * One Agent as a card (grid) or row (list). The primary action is always the
 * obvious next step: 进入对话 for a published Agent, 继续配置 for a draft;
 * everything else lives in the overflow menu. 发布与嵌入 only appears for a
 * published Agent — a draft has no public URL to hand out.
 */
const props = withDefaults(
  defineProps<{
    agent: PlatformAgent;
    canManage: boolean;
    /** Pre-rendered "provider · model" label from the parent. */
    modelLabel: string;
    /** One concise capability count/inheritance line from the pure helper. */
    capabilitySummary?: string;
    layout?: "grid" | "list";
  }>(),
  { layout: "grid" },
);

const emit = defineEmits<{
  open: [agent: PlatformAgent];
  edit: [agent: PlatformAgent];
  publish: [agent: PlatformAgent];
  remove: [agent: PlatformAgent];
}>();

const menuOpen = ref(false);

function toggleMenu() {
  menuOpen.value = !menuOpen.value;
}

function onDocClick(e: MouseEvent) {
  if (!(e.target as HTMLElement | null)?.closest(".card-menu")) menuOpen.value = false;
}

watch(menuOpen, (v) => {
  if (v) document.addEventListener("click", onDocClick, true);
  else document.removeEventListener("click", onDocClick, true);
});

onBeforeUnmount(() => document.removeEventListener("click", onDocClick, true));

function primary() {
  if (props.agent.status === "draft") emit("edit", props.agent);
  else emit("open", props.agent);
}

function choose(action: "edit" | "publish" | "remove") {
  menuOpen.value = false;
  if (action === "edit") emit("edit", props.agent);
  else if (action === "publish") emit("publish", props.agent);
  else emit("remove", props.agent);
}
</script>

<template>
  <article class="card" :class="layout">
    <div class="card-top">
      <AgentAvatar :icon="agent.icon" :color="agent.color" :size="44" />
      <div class="card-head">
        <button
          type="button"
          class="card-name"
          :title="agent.status === 'draft' ? `继续配置 ${agent.name}` : `进入与 ${agent.name} 的对话`"
          @click="primary"
        >
          {{ agent.name }}
        </button>
        <div class="chip-line">
          <span class="st-chip" :class="agent.status">{{ agent.status === "ready" ? "已发布" : "草稿" }}</span>
          <span class="md-chip">{{ modelLabel }}</span>
          <span v-if="capabilitySummary" class="cap-chip" :title="capabilitySummary">{{ capabilitySummary }}</span>
          <span class="cat-chip">{{ categoryLabel(agent.category) }}</span>
        </div>
      </div>
      <div class="card-menu">
        <button
          type="button"
          class="iconbtn"
          aria-haspopup="menu"
          :aria-expanded="menuOpen"
          :aria-label="`${agent.name} 更多操作`"
          @click.stop="toggleMenu"
        >
          <el-icon aria-hidden="true"><MoreFilled /></el-icon>
        </button>
        <div v-if="menuOpen" class="menu" role="menu" @click.stop @keydown.esc.stop="menuOpen = false">
          <button v-if="canManage" type="button" role="menuitem" class="menu-item" @click="choose('edit')">
            <el-icon aria-hidden="true"><Edit /></el-icon>编辑
          </button>
          <button v-if="agent.status === 'ready'" type="button" role="menuitem" class="menu-item" @click="choose('publish')">
            <el-icon aria-hidden="true"><Link /></el-icon>发布与嵌入
          </button>
          <button v-if="canManage" type="button" role="menuitem" class="menu-item danger" @click="choose('remove')">
            <el-icon aria-hidden="true"><Delete /></el-icon>删除
          </button>
        </div>
      </div>
    </div>

    <p class="card-desc" :class="{ muted: !agent.description }">{{ agent.description || "暂无简介" }}</p>

    <div class="card-foot">
      <span class="card-stat">
        <el-icon class="stat-ico" aria-hidden="true"><ChatDotRound /></el-icon>
        {{ agent.conversation_count }} 次对话
        <template v-if="formatAgentTime(agent.updated_at)"> · 更新于 {{ formatAgentTime(agent.updated_at) }}</template>
      </span>
      <button type="button" class="enter" :class="{ solid: agent.status === 'ready' }" @click="primary">
        {{ agent.status === "draft" ? "继续配置" : "进入对话" }}
        <el-icon aria-hidden="true"><ArrowRight /></el-icon>
      </button>
    </div>
  </article>
</template>

<style scoped>
.card {
  position: relative;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.card:hover,
.card:focus-within {
  border-color: var(--el-border-color);
  box-shadow: var(--el-box-shadow-light);
}

.card-top {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-width: 0;
}
.card-head {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding-top: 1px;
}
.card-name {
  align-self: flex-start;
  border: 0;
  background: none;
  padding: 0;
  margin: 0;
  font: inherit;
  font-size: 15px;
  font-weight: 650;
  color: var(--el-text-color-primary);
  cursor: pointer;
  text-align: left;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  transition: color var(--pg-transition);
}
.card-name:hover {
  color: var(--el-color-primary);
}

.chip-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.md-chip,
.cat-chip,
.cap-chip,
.st-chip {
  font-size: 11.5px;
  border-radius: 999px;
  padding: 2px 9px;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.md-chip {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}
.cat-chip {
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
}
.cap-chip {
  max-width: 170px;
  background: var(--el-fill-color-extra-light);
  color: var(--el-text-color-secondary);
}
.st-chip {
  font-weight: 600;
}
.st-chip.ready {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}
.st-chip.draft {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}

.card-desc {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 13px;
  line-height: 1.6;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  min-height: 41px;
}
.card-desc.muted {
  color: var(--el-text-color-placeholder);
}

.card-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: auto;
  padding-top: 11px;
  border-top: 1px solid var(--el-border-color-extra-light);
}
.card-stat {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  min-width: 0;
}
.stat-ico {
  font-size: 14px;
  flex: none;
}

/* The card's primary action: quiet for drafts (the work is unfinished), the
   one solid button per card for a published Agent's conversation. */
.enter {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
  border-radius: 8px;
  padding: 6px 12px;
  font: inherit;
  font-size: 12.5px;
  font-weight: 550;
  cursor: pointer;
  transition: background var(--pg-transition), border-color var(--pg-transition), color var(--pg-transition);
}
.enter:hover {
  border-color: var(--el-border-color-hover);
  background: var(--el-fill-color-light);
}
.enter.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.enter.solid:hover {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}

/* overflow menu */
.card-menu {
  position: relative;
  flex: none;
}
.iconbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.iconbtn:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.menu {
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  z-index: 40;
  min-width: 152px;
  background: var(--el-bg-color-overlay);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  box-shadow: var(--el-box-shadow);
  padding: 4px;
}
.menu-item {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  border: 0;
  background: none;
  border-radius: 6px;
  padding: 8px 10px;
  font: inherit;
  font-size: 13px;
  color: var(--el-text-color-regular);
  cursor: pointer;
  text-align: left;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.menu-item .el-icon {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}
.menu-item:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.menu-item.danger {
  color: var(--el-color-danger);
}
.menu-item.danger .el-icon {
  color: var(--el-color-danger);
}
.menu-item.danger:hover {
  background: var(--el-color-danger-light-9);
}

/* Rows share the card's parts; the grid switches, the parts do not. */
.card.list {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1.8fr) auto;
  align-items: center;
  gap: 16px;
  padding: 13px 16px;
}
.card.list .card-top {
  align-items: center;
}
.card.list .card-desc {
  -webkit-line-clamp: 1;
  min-height: 0;
}
.card.list .card-foot {
  border-top: 0;
  padding-top: 0;
  margin-top: 0;
}
@media (max-width: 720px) {
  .card.list {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }
  .card.list .card-foot {
    border-top: 1px solid var(--el-border-color-extra-light);
    padding-top: 11px;
  }
}
</style>
