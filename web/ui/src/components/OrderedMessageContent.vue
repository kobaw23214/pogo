<script setup lang="ts">
import { computed, inject } from "vue";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import OfficeReference from "./OfficeReference.vue";
import type { Block } from "@/api/types";
import { orderedDisplayParts } from "@/agent/orderedInput";
import { fileIcon } from "./fileIcons";
import { Icon } from "@iconify/vue";
import ElementCard from "./ElementCard.vue";
import ElementPageIcon from "./ElementPageIcon.vue";
import SkillBlock from "./SkillBlock.vue";
import CommandTokenIcon from "./CommandTokenIcon.vue";
import ShortcutReference from "./ShortcutReference.vue";
import { skillMessageBlock } from "@/agent/composerTokens";
import { mcpInvocationText } from "@/agent/mcpInvocation";

const props = withDefaults(defineProps<{ blocks: Block[]; parseSkill?: boolean }>(), { parseSkill: true });
const emit = defineEmits<{ openFile: [file: { path: string; name: string }] }>();
const navigation = inject(workspaceFileNavigationKey, null);
const fileAccess = computed(() => !!navigation?.scope());
const parts = computed(() => orderedDisplayParts(props.blocks));
const skill = computed(() => props.parseSkill && props.blocks[0]?.type === "text" && !props.blocks[0].input_element && !props.blocks[0].input_skill && !props.blocks[0].input_mcp && !props.blocks[0].input_shortcut ? skillMessageBlock(props.blocks[0].text) : null);
const taskBlocks = computed<Block[]>(() => skill.value ? [{ type: "text", text: skill.value.trailing }, ...props.blocks.slice(1)] : props.blocks);
</script>

<template>
  <SkillBlock v-if="skill" :block="skill"><template #task><OrderedMessageContent :blocks="taskBlocks" :parse-skill="false" @open-file="emit('openFile', $event)" /></template></SkillBlock>
  <span v-else class="ordered-message" data-testid="ordered-message-content"><template v-for="(part, index) in parts" :key="index"><span v-if="part.type === 'text'" class="ordered-prose">{{ part.text }}</span><ShortcutReference v-else-if="part.type === 'shortcut'" :title="part.title" :content="part.content" :icon="part.icon" :language="part.language" :parts="part.parts" /><OfficeReference v-else-if="part.type === 'office'" :office="part.office" /><span v-else-if="part.type === 'skill'" class="ordered-reference skill" data-reference-kind="skill" :title="`/skill:${part.name}`"><CommandTokenIcon kind="skill" /><span>{{ part.name }}</span></span><span v-else-if="part.type === 'mcp'" class="ordered-reference mcp" data-reference-kind="mcp" :title="mcpInvocationText(part.server, part.name)"><CommandTokenIcon kind="mcp" /><span>{{ part.server }}/{{ part.name }}</span></span><el-popover v-else-if="part.type === 'element'" trigger="click" placement="top" :width="360" popper-class="ordered-element-preview"><template #reference><button type="button" class="ordered-reference element" :title="`${part.card.title} · ${part.card.element}`" data-reference-kind="element"><ElementPageIcon :url="part.card.url" :icon="part.card.favicon" /><span>{{ part.card.element || part.card.selector || part.card.title }}</span></button></template><ElementCard :card="part.card" /></el-popover><button v-else type="button" class="ordered-reference file" :title="part.path" :data-reference-kind="part.type" :disabled="!part.path || !fileAccess || part.path.startsWith('shortcut-assets/') || part.path.startsWith('starter-assets/')" @click="emit('openFile', { path: part.path, name: part.name })"><Icon :icon="fileIcon(part.name)" /><span>{{ part.name }}</span></button></template></span>
</template>

<style scoped>
.ordered-message { white-space:pre-wrap; overflow-wrap:anywhere; line-height:1.85; }
.ordered-reference { display:inline-flex; align-items:center; vertical-align:baseline; gap:5px; max-width:min(100%,300px); margin:1px 3px; padding:1px 7px; border:1px solid var(--el-border-color-light); border-radius:7px; background:var(--el-fill-color-light); color:var(--el-text-color-primary); font:inherit; font-size:.9em; line-height:1.6; cursor:pointer; white-space:nowrap; }
.ordered-reference span { overflow:hidden; text-overflow:ellipsis; }
.ordered-reference :deep(svg) { width:15px; height:15px; flex:none; }
.ordered-reference:hover { border-color:var(--el-color-primary); color:var(--el-color-primary); }
.ordered-reference:focus-visible { outline:2px solid var(--el-color-primary); outline-offset:2px; }
</style>
