<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Icon from "./ShortcutEditorIcon.vue";
import { useI18n } from "vue-i18n";
import type { SkillInfo } from "@/api/types";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import type { ComposerReferencePart } from "@/agent/composerParts";
import { shortcutSearchSegments } from "./shortcutPresentation";
import OfficeReference from "./OfficeReference.vue";
import ElementCard from "./ElementCard.vue";
import { parsePickedCached } from "@/agent/elementPicker";

type Source = "information" | "element" | "office" | "skill";
const props = withDefaults(defineProps<{
  references: ComposerReferencePart[]; shortcuts: { id: string; title: string }[];
  library: ShortcutSnapshot[]; skills: SkillInfo[]; currentId: string;
  disabled?: boolean; canPickInformation?: boolean; canRefresh?: boolean;
}>(), { disabled: false, canPickInformation: false, canRefresh: false });
const emit = defineEmits<{
  close: []; refresh: []; information: []; insertReference: [id: string];
  insertShortcut: [id: string]; insertSkill: [name: string]; createWeb: []; createOffice: [];
}>();
const { t } = useI18n();
const source = ref<Source>("information"), query = ref(""), expanded = ref("");
const sources: { id: Source; icon: string }[] = [
  { id: "information", icon: "lucide:folder-open" }, { id: "element", icon: "lucide:globe" },
  { id: "office", icon: "lucide:table-2" }, { id: "skill", icon: "lucide:box" },
];
const normalizedQuery = computed(() => query.value.trim().toLocaleLowerCase());
const matches = (...values: (string | undefined)[]) => !normalizedQuery.value || values.some(value => value?.toLocaleLowerCase().includes(normalizedQuery.value));
const referenceText = (part: ComposerReferencePart) => part.type === "office" ? part.office.text : part.type === "element" ? part.text : part.type === "file" ? part.path || "" : "";
const visibleReferences = computed(() => props.references.filter(part => (source.value !== "information" && part.type === source.value) && matches(part.label, referenceText(part))));
const visibleSkills = computed(() => props.skills.filter(skill => matches(skill.name, skill.description)));
const elementCard = (part: ComposerReferencePart) => part.type === "element" && parsePickedCached(part.text).cards[0] ? { ...parsePickedCached(part.text).cards[0], favicon: part.favicon } : null;
const count = (id: Source) => id === "information" ? 0 : id === "skill" ? props.skills.length : props.references.filter(part => part.type === id).length;
const resultCount = computed(() => source.value === "skill" ? visibleSkills.value.length : visibleReferences.value.length);
const hasReusableReferences = computed(() => source.value !== "information" && source.value !== "skill" && count(source.value) > 0);
function createReference() { if (props.disabled) return; if (source.value === "element") emit("createWeb"); else if (source.value === "office") emit("createOffice"); }
const segments = (value: string) => shortcutSearchSegments(value, query.value.trim());
watch(source, () => { query.value = ""; expanded.value = ""; });
function toggle(id: string) { expanded.value = expanded.value === id ? "" : id; }
</script>

<template>
  <aside class="shortcut-sources" :aria-label="t('shortcutEditor.sources')" data-testid="shortcut-sources" @keydown.esc.stop.prevent="emit('close')">
    <header class="sources-heading">
      <div><Icon icon="lucide:panels-top-left" /><strong>{{ t('shortcutEditor.sources') }}</strong></div>
    </header>
    <nav class="source-tabs" :aria-label="t('shortcutEditor.sourceType')">
      <button v-for="entry in sources" :key="entry.id" type="button" :class="{ active: source === entry.id }" :aria-pressed="source === entry.id" :data-testid="`shortcut-source-${entry.id}`" @click="source = entry.id">
        <Icon :icon="entry.icon" /><span>{{ t(`shortcutEditor.source.${entry.id}`) }}</span><small v-if="count(entry.id)">{{ count(entry.id) }}</small>
      </button>
    </nav>
    <Transition name="source-section" mode="out-in">
      <section :key="source" class="source-section" :aria-label="t(`shortcutEditor.source.${source}`)">
        <div class="source-intro"><strong>{{ t(`shortcutEditor.source.${source}`) }}</strong><p>{{ t(`shortcutEditor.sourceHint.${source}`) }}</p></div>
        <div v-if="source === 'information'" class="source-action-area">
          <button type="button" class="source-create-action" :disabled="disabled || !canPickInformation" data-testid="shortcut-add-information" @mousedown.prevent @click="emit('information')"><Icon icon="lucide:folder-search" /><span>{{ t('nestedShortcuts.fromInformation') }}</span><Icon icon="lucide:arrow-up-right" /></button>
        </div>
        <div v-else-if="source === 'element' || source === 'office'" class="source-action-area">
          <button type="button" class="source-create-action" :disabled="disabled" :data-testid="source === 'element' ? 'shortcut-create-web' : 'shortcut-create-office'" @mousedown.prevent @click="createReference"><Icon icon="lucide:plus" /><span>{{ t(source === 'element' ? 'shortcutEditor.createWeb' : 'shortcutEditor.createOffice') }}</span><Icon icon="lucide:arrow-up-right" /></button>
        </div>
        <template v-if="source !== 'information'">
          <div v-if="hasReusableReferences" class="source-reuse-heading"><span>{{ t('shortcutEditor.reusableReferences') }}</span><button v-if="canRefresh" type="button" :disabled="disabled" :aria-label="t('shortcutEditor.refreshSources')" :title="t('shortcutEditor.refreshSources')" @click="emit('refresh')"><Icon icon="lucide:refresh-cw" /></button></div>
          <div v-if="source === 'skill' || hasReusableReferences" class="source-search-row"><label class="source-search"><Icon icon="lucide:search" /><input v-model="query" :placeholder="t('shortcutEditor.searchSources')" :aria-label="t('shortcutEditor.searchSources')" /><button v-if="query" type="button" :aria-label="t('shortcuts.clearSearch')" @click="query = ''"><Icon icon="lucide:x" /></button></label></div>
          <div class="source-results" aria-live="polite">
            <div v-if="!resultCount && (query || source === 'skill')" class="source-empty"><Icon :icon="sources.find(entry => entry.id === source)?.icon || 'lucide:search'" /><strong>{{ t(query ? 'shortcutEditor.noResults' : 'shortcutEditor.sourceEmpty.skill') }}</strong><p v-if="!query">{{ t('shortcutEditor.sourceEmptyHint.skill') }}</p></div>
            <template v-if="source === 'skill'">
              <article v-for="skill in visibleSkills" :key="skill.name" class="source-card">
                <button type="button" class="source-card-preview" :aria-expanded="expanded === skill.name" @click="toggle(skill.name)"><Icon icon="lucide:box" /><span><strong><template v-for="(segment, index) in segments(skill.name)" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></strong><small>{{ skill.description }}</small></span><Icon :icon="expanded === skill.name ? 'lucide:chevron-up' : 'lucide:chevron-down'" /></button>
                <div v-if="expanded === skill.name" class="source-card-detail"><p><template v-for="(segment, index) in segments(skill.description)" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></p></div>
                <button type="button" class="source-insert" :disabled="disabled" @mousedown.prevent @click="emit('insertSkill', skill.name)"><Icon icon="lucide:plus" />{{ t('shortcuts.insert') }}</button>
              </article>
            </template>
            <template v-else>
              <article v-for="part in visibleReferences" :key="part.id" class="source-card">
                <button type="button" class="source-card-preview" :aria-expanded="expanded === part.id" @click="toggle(part.id)"><Icon :icon="sources.find(entry => entry.id === source)?.icon || 'lucide:file'" /><span><strong><template v-for="(segment, index) in segments(part.label)" :key="index"><mark v-if="segment.match">{{ segment.text }}</mark><template v-else>{{ segment.text }}</template></template></strong><small>{{ part.type === 'office' ? [part.office.sheet, part.office.range].filter(Boolean).join(' · ') : part.type === 'element' ? part.title || part.pick?.pageURL : '' }}</small></span><Icon :icon="expanded === part.id ? 'lucide:chevron-up' : 'lucide:chevron-down'" /></button>
                <div v-if="expanded === part.id" class="source-card-detail"><OfficeReference v-if="part.type === 'office'" :office="part.office" details-only /><ElementCard v-else-if="part.type === 'element' && elementCard(part)" :card="elementCard(part)!" /><pre v-else>{{ referenceText(part) }}</pre></div>
                <button type="button" class="source-insert" :disabled="disabled" @mousedown.prevent @click="emit('insertReference', part.id)"><Icon icon="lucide:plus" />{{ t('shortcuts.insert') }}</button>
              </article>
            </template>
          </div>
        </template>
      </section>
    </Transition>
    <footer v-if="source !== 'information'" class="sources-footer"><Icon icon="lucide:mouse-pointer-2" />{{ t('shortcutEditor.cursorHint') }}</footer>
  </aside>
</template>

<style scoped>
.shortcut-sources{display:flex;flex-direction:column;height:100%;min-height:0;background:var(--el-bg-color);border-right:1px solid var(--el-border-color-light);font-size:12px;color:var(--el-text-color-primary)}button,input{font:inherit}button{cursor:pointer}button:disabled{opacity:.45;cursor:not-allowed}button:focus-visible,input:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:-2px}svg{width:16px;height:16px;flex:none}.sources-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:19px 20px 14px}.sources-heading>div{display:flex;gap:8px;align-items:center}.sources-heading strong{font-size:13px;font-weight:600}.sources-heading button,.source-refresh,.source-search button{display:grid;place-items:center;border:0;background:transparent;color:var(--el-text-color-secondary);border-radius:6px;padding:5px}.sources-heading button:hover,.source-refresh:hover,.source-search button:hover{background:var(--el-fill-color)}.source-tabs{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:7px;padding:0 16px 16px;border-bottom:1px solid var(--el-border-color-lighter)}.source-tabs button{display:flex;align-items:center;gap:5px;border:1px solid transparent;border-radius:7px;padding:9px 10px;background:var(--el-fill-color-lighter);color:var(--el-text-color-secondary)}.source-tabs button.active{color:var(--el-color-primary);border-color:var(--pg-accent-line);background:var(--el-color-primary-light-9)}.source-tabs svg{width:14px;height:14px}.source-tabs small{margin-left:auto;font-size:10px;color:inherit;opacity:.75}.source-intro{padding:23px 22px 14px}.source-intro strong{font-size:14px;font-weight:600}.source-intro p{margin:8px 0 0;color:var(--el-text-color-secondary);line-height:1.75}.source-section{display:flex;flex:1;min-height:0;flex-direction:column;overflow:hidden}.source-action-area{padding:0 22px 22px}.source-create-action{display:flex;align-items:center;gap:10px;min-height:46px;box-sizing:border-box;width:100%;padding:12px 14px;border:1px solid var(--pg-accent-line);border-radius:10px;background:var(--el-color-primary-light-9);color:var(--el-color-primary);text-align:left;transition:background .15s ease,border-color .15s ease,box-shadow .15s ease}.source-create-action:hover:not(:disabled){background:color-mix(in srgb,var(--el-color-primary) 13%,var(--el-bg-color));box-shadow:0 2px 10px color-mix(in srgb,var(--el-color-primary) 8%,transparent)}.source-create-action>span{flex:1;font-weight:550}.source-create-action svg:last-child{width:14px;height:14px}.source-reuse-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:0 22px 10px;padding-top:16px;border-top:1px solid var(--el-border-color-lighter);font-size:11px;color:var(--el-text-color-secondary)}.source-reuse-heading>button{display:grid;place-items:center;padding:4px;border:0;border-radius:6px;background:transparent;color:var(--el-text-color-secondary)}.source-reuse-heading>button:hover:not(:disabled){background:var(--el-fill-color)}.source-reuse-heading svg{width:13px;height:13px}.source-search-row{display:flex;gap:6px;padding:0 22px 13px}.source-search{display:flex;align-items:center;gap:7px;min-width:0;flex:1;border:1px solid var(--el-border-color-light);border-radius:8px;padding:7px 9px;color:var(--el-text-color-placeholder);background:var(--el-fill-color-lighter)}.source-search:focus-within{border-color:var(--el-color-primary)}.source-search input{min-width:0;width:100%;border:0;outline:0;background:transparent;color:var(--el-text-color-primary)}.source-search button{padding:0}.source-results{flex:1;min-height:0;overflow:auto;overscroll-behavior:contain;padding:0 22px 20px}.source-empty{display:flex;flex-direction:column;align-items:center;gap:12px;text-align:center;margin:38px 5px;color:var(--el-text-color-secondary);line-height:1.8}.source-empty>svg{width:30px;height:30px;color:var(--el-text-color-placeholder)}.source-empty strong{font-weight:500;color:var(--el-text-color-primary)}.source-empty p{margin:0}.source-card{margin-bottom:9px;border:1px solid var(--el-border-color-light);border-radius:10px;background:var(--el-bg-color);overflow:hidden}.source-card:hover{border-color:var(--pg-accent-line)}.source-card-preview{display:flex;align-items:flex-start;gap:9px;min-width:0;width:100%;padding:12px;border:0;color:inherit;background:transparent;text-align:left}.source-card-preview>svg{margin-top:2px;color:var(--el-text-color-secondary)}.source-card-preview>svg:last-child{width:13px;margin-left:auto}.source-card-preview>span{display:flex;flex-direction:column;gap:5px;min-width:0;flex:1}.source-card-preview strong{font-weight:550;overflow-wrap:anywhere;font-size:12px;line-height:1.5}.source-card-preview small{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;color:var(--el-text-color-secondary);font-size:11px;line-height:1.65;overflow-wrap:anywhere}.source-card-detail{border-top:1px solid var(--el-border-color-lighter);padding:12px;max-height:38dvh;overflow:auto;overscroll-behavior:contain;font-size:12px;line-height:1.8}.source-card-detail p{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}.source-card-detail pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}.source-card-detail :deep(pre){font-size:11px}.source-insert{display:flex;align-items:center;gap:5px;justify-content:center;width:100%;padding:7px;border:0;border-top:1px solid var(--el-border-color-lighter);background:var(--el-fill-color-lighter);color:var(--el-color-primary);font-size:11px}.source-insert:hover:not(:disabled){background:var(--el-color-primary-light-9)}.source-insert svg{width:13px;height:13px}.sources-footer{display:flex;align-items:flex-start;gap:7px;padding:12px 22px;border-top:1px solid var(--el-border-color-light);color:var(--el-text-color-placeholder);font-size:11px;line-height:1.6}.sources-footer svg{width:14px;height:14px;margin-top:1px}mark{background:color-mix(in srgb,var(--el-color-primary) 20%,transparent);color:inherit;border-radius:2px;padding:0 1px}
.source-tabs button{transition:color .15s ease,background .15s ease,border-color .15s ease}.source-section-enter-active,.source-section-leave-active{transition:opacity .12s ease,transform .12s ease}.source-section-enter-from{opacity:0;transform:translateY(5px)}.source-section-leave-to{opacity:0;transform:translateY(-4px)}@media(prefers-reduced-motion:reduce){.source-section-enter-active,.source-section-leave-active,.source-tabs button,.source-create-action{transition:none}}
</style>
