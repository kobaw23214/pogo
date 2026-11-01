<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { ToolCatalogEntry } from "@/api/types";
import HiText from "./HiText.vue";

// One declared tool: the card both views of the skills panel share. `showSkill`
// adds the origin badge, which the by-tool view needs and the by-skill view
// would repeat once per row. `q` is the panel's live filter: its literal run
// is highlighted in the name, title, description and origin.
//
// The badges are the gate's vocabulary, not decoration: 只读 says the call
// skips review, 破坏性 says it always asks, 仅目录 says the model cannot call
// it at all (the skill's docs teach it; the model reaches it through bash).
const props = defineProps<{ tool: ToolCatalogEntry; showSkill?: boolean; q?: string }>();

const { t } = useI18n();

const params = computed(() => {
  const p = props.tool.input_schema?.properties ?? {};
  const required = new Set(props.tool.input_schema?.required ?? []);
  return Object.entries(p).map(([name, spec]) => ({
    name,
    type: spec.type ?? "any",
    required: required.has(name),
    enum: spec.enum ?? [],
    desc: spec.description ?? "",
  }));
});

const hasParams = computed(() => params.value.length > 0);
</script>

<template>
  <div class="tool-card">
    <div class="tool-head">
      <span class="tool-name"><HiText :text="tool.wire_name" :q="q" /></span>
      <span v-if="showSkill && tool.skill" class="tag skill-tag" :title="t('skillsPanel.fromSkill', { skill: tool.skill })">
        <HiText :text="tool.skill" :q="q" />
      </span>
      <span v-if="tool.read_only" class="tag ok">{{ t("skillsPanel.readOnly") }}</span>
      <span v-if="tool.destructive" class="tag danger">{{ t("skillsPanel.destructive") }}</span>
      <!-- Only the manifest half knows registration; an MCP tool (no field) is
           callable through its connection, never "catalog only". -->
      <span v-if="tool.registered === false" class="tag">{{ t("skillsPanel.catalogOnly") }}</span>
    </div>
    <div v-if="tool.title" class="tool-title"><HiText :text="tool.title" :q="q" /></div>
    <div class="tool-desc"><HiText :text="tool.description" :q="q" /></div>
    <!-- The parameter table reads from the schema the way the model sees it:
         name, type, required. Enum values are spelled out rather than hinted
         at, because a closed set is exactly the thing worth seeing whole. -->
    <table v-if="hasParams" class="params">
      <thead>
        <tr>
          <th>{{ t("skillsPanel.param") }}</th>
          <th>{{ t("skillsPanel.type") }}</th>
          <th>{{ t("skillsPanel.options") }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in params" :key="p.name">
          <td>
            <span class="p-name">{{ p.name }}</span>
            <span v-if="p.required" class="req" :title="t('skillsPanel.required')">*</span>
          </td>
          <td>{{ p.type }}</td>
          <td>
            <span v-if="p.enum.length" class="enums">{{ p.enum.join(" · ") }}</span>
            <span v-else-if="p.desc" class="p-desc">{{ p.desc }}</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped lang="scss">
.tool-card {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 9px 11px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-bg-color);
}

.tool-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-width: 0;
}

.tool-name {
  font-size: 12.5px;
  font-weight: 600;
  font-family: var(--el-font-family-mono, ui-monospace, monospace);
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tool-title {
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
}

.tool-desc {
  font-size: 12px;
  color: var(--el-text-color-regular);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.tag {
  flex: 0 0 auto;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--el-fill-color-dark);
  color: var(--el-text-color-secondary);
  font-size: 10.5px;

  &.ok {
    background: var(--el-color-success-light-9);
    color: var(--el-color-success);
  }

  &.danger {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }
}

.params {
  margin-top: 4px;
  border-collapse: collapse;
  font-size: 11.5px;
  color: var(--el-text-color-regular);

  th {
    text-align: left;
    font-weight: 500;
    color: var(--el-text-color-secondary);
    padding: 2px 10px 2px 0;
  }

  td {
    padding: 2px 10px 2px 0;
    vertical-align: top;
  }

  .p-name {
    font-family: var(--el-font-family-mono, ui-monospace, monospace);
  }

  .req {
    color: var(--el-color-danger);
    margin-left: 2px;
  }

  .enums {
    font-family: var(--el-font-family-mono, ui-monospace, monospace);
    font-size: 11px;
  }

  .p-desc {
    color: var(--el-text-color-secondary);
    font-size: 11px;
  }
}
</style>
