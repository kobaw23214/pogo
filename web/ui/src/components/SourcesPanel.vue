<script setup lang="ts">
import { computed, inject, ref, useId } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "./Chevron.vue";
import CiteSourceIcon from "./CiteSourceIcon.vue";
import CiteSourceRow from "./CiteSourceRow.vue";
import { numberCitations } from "@/agent/cite";
import { groupSourceOverview } from "@/agent/citeOverview";
import { citePresentation } from "@/agent/citePresentation";
import { citeFaviconKey, citeFaviconURL, citeSourceFavicon } from "@/agent/citeFavicon";
import type { CiteSource } from "@/api/types";

// The same entry stays under the answer while its execution log is folded.
// Sources are evidence readers can inspect here; opening a tool record is an
// optional next step, not the only way to discover what a source contains.
const props = defineProps<{
  sources: CiteSource[];
  text: string;
}>();

const { t } = useI18n();
const open = ref(false);
const otherOpen = ref(false);
const faviconCache = inject(citeFaviconKey, null);
const bodyId = `sources-${useId()}`;
const otherBodyId = `${bodyId}-other`;
const groups = computed(() => {
  const known = new Set(props.sources.map((src) => src.label));
  return groupSourceOverview(props.sources, numberCitations(props.text, known).nums);
});
const primary = computed(() => groups.value.cited.length ? groups.value.cited : groups.value.others);
const showOthers = computed(() => !groups.value.cited.length || otherOpen.value);
const iconSources = computed(() => {
  const sites = new Map<string, CiteSource>();
  for (const { src: source } of primary.value) {
    const url = citeFaviconURL(source);
    // The overview represents websites instead of collapsing every webpage
    // to a single globe. Rows still retain every individual source.
    const key = url ? new URL(url).origin : citePresentation(source).iconType;
    const previous = sites.get(key);
    if (!previous || (!citeSourceFavicon(previous, faviconCache) && citeSourceFavicon(source, faviconCache))) sites.set(key, source);
  }
  return [...sites.values()].slice(0, 3);
});
</script>

<template>
  <div v-if="primary.length" class="sources-panel" role="complementary" :aria-label="t('cite.panelLabel')">
    <button type="button" class="trigger" :aria-expanded="open" :aria-controls="bodyId" @click="open = !open">
      <span v-if="iconSources.length" class="source-icons" aria-hidden="true">
        <span v-for="source in iconSources" :key="source.label" class="source-icon"><CiteSourceIcon :source="source" /></span>
      </span>
      {{ t(groups.cited.length ? "cite.sources" : "cite.consulted", { n: primary.length }) }}
      <Chevron class="chevron" :open="open" />
    </button>

    <div v-if="open" :id="bodyId" class="source-groups">
      <section v-if="groups.cited.length" class="source-group source-group-cited" :aria-label="t('cite.cited')">
        <div class="group-label">{{ t("cite.cited") }}</div>
        <ul class="source-list">
          <li v-for="item in groups.cited" :key="item.src.label">
            <CiteSourceRow :source="item.src" :num="item.num" :context="text" />
          </li>
        </ul>
      </section>
      <section v-if="groups.others.length" class="source-group source-group-other" :aria-label="t('cite.others')">
        <button v-if="groups.cited.length" type="button" class="other-trigger" :aria-expanded="showOthers" :aria-controls="otherBodyId" @click="otherOpen = !otherOpen">
          {{ t('cite.otherMaterials', { n: groups.others.length }) }}
          <Chevron class="chevron" :open="showOthers" />
        </button>
        <p v-else class="group-note">{{ t('cite.noInlineCitations') }}</p>
        <ul v-if="showOthers" :id="otherBodyId" class="source-list">
          <li v-for="item in groups.others" :key="item.src.label">
            <CiteSourceRow :source="item.src" />
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>

<style scoped lang="scss">
.sources-panel { margin-top: 12px; min-width: 0; }
.trigger {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 32px;
  padding: 4px 10px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 999px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
  &:hover { background: var(--el-fill-color-light); color: var(--el-text-color-primary); }
  &:focus-visible { outline: 2px solid var(--pg-link-color, var(--el-color-primary)); outline-offset: 3px; }
}
.chevron { width: 11px; height: 11px; }
.source-icons { display: inline-flex; align-items: center; }
.source-icon {
  display: inline-flex;
  padding: 2px;
  border-radius: 50%;
  background: var(--el-bg-color);
  color: var(--el-text-color-secondary);
  & + & { margin-left: -3px; }
  :deep(.cite-source-icon) { width: 12px; height: 12px; }
}
.source-groups { margin-top: 10px; max-width: 720px; }
.source-group + .source-group { margin-top: 15px; }
.group-label { margin: 0 0 5px; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; }
.group-note { margin: 0 0 7px; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.6; }
.other-trigger {
  display: inline-flex; align-items: center; gap: 7px; min-height: 32px;
  margin: 0 0 4px; padding: 4px 0; border: 0; border-radius: 4px;
  background: transparent; color: var(--el-text-color-secondary); font: inherit; font-size: 11px; cursor: pointer;
  &:hover { color: var(--el-text-color-primary); }
  &:focus-visible { outline: 2px solid var(--pg-link-color, var(--el-color-primary)); outline-offset: 3px; }
}
.source-list { list-style: none; margin: 0; padding: 0; }
.source-list > li + li { border-top: 1px solid var(--el-border-color-extra-light); }
</style>
