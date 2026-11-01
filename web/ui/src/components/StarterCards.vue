<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ElMessage } from "element-plus";
import { Icon } from "@iconify/vue";
import StarterIconPicker from "./studio/StarterIconPicker.vue";
import { Plus, Upload, Download, EditPen, Close, ArrowLeft, ArrowRight } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { PanelInfo, StarterCard, Starters } from "@/api/types";
import { starterSample } from "@/components/starterSample";
import { starterIconFor as iconFor } from "@/components/starterIcons";
import InlineComposer from "@/components/InlineComposer.vue";
import type { OfficeReferenceSnapshot } from "@/api/types";
import OfficeReferenceDialog from "./OfficeReferenceDialog.vue";
import ShortcutReferenceDialog from "@/components/ShortcutReferenceDialog.vue";
import { saveHomepageStarters, type ShortcutSnapshot } from "@/api/shortcuts";
import { notifyStarterShortcutsChanged } from "@/agent/shortcutSources";
import { composerDisplayText, type ComposerPart } from "@/agent/composerParts";
import { cloneStarterDraft, prepareStableStarterDraft, starterCardComposerParts, starterDraftHasContent, starterDraftTitle, type PrepareStarterDraft, type StarterDraft } from "@/components/starterCardDraft";

// The empty conversation, which is the one screen where a general-purpose agent
// looks identical to every other one. A blank input signals infinite
// possibility and zero direction at the same time; a handful of concrete
// openings is what turns "ask me anything" into "here is what I am for".
//
// The content is the deployment's (a skill's starters.json), never pogo's:
// this component knows how to draw a card and nothing about what the cards say.
// The editor is the override layer: hover a card for edit/delete, the toolbar
// adds / imports / samples — the browser pane's shortcut-card grammar. The
// first save writes the user set (POGO_STARTERS_FILE), which then wins over
// the skill's; `set` carries the followups through so editing cards never
// drops them.
const props = defineProps<{
  heading?: string;
  cards: StarterCard[];
  fallback: string;
  // The full effective set (heading/send/followups included), for saves.
  set?: Starters | null;
  // Registered dock panels, for the panel action's picker. Selection-only:
  // the server skips cards naming unregistered panels, so a typed name would
  // be a dead card the dialog helped build.
  panels?: PanelInfo[];
  // Agent-owned starters persist onto the Agent definition, not the global
  // file: onSave takes the whole card list and the caller owns the write
  // (AgentView PATCHes the Agent). readOnly stays for viewers without the
  // admin grant.
  readOnly?: boolean;
  // The picker dialog also exposes downloads to viewers without edit rights.
  showDownloads?: boolean;
  onSave?: (cards: StarterCard[]) => Promise<void>;
  prepareDraft?: PrepareStarterDraft;
  agentId?: string;
  // Kept for restricted deployments; omitted or 0 means no count cap.
  maxCards?: number;
}>();

const emit = defineEmits<{ pick: [StarterCard]; saved: [] }>();

const { t } = useI18n();

// ── Editing ─────────────────────────────────────────────────────────────────
// Edits work on a local copy; a save is one PUT of the whole set, so the
// server file is never mid-edit. The copy resyncs whenever the effective set
// changes (reload after save, skill swap).
const working = ref<StarterCard[]>([...props.cards]);
watch(
  () => props.cards,
  (cs) => {
    working.value = [...cs];
  },
  { immediate: true },
);

const saving = ref(false);

// ── Pagination ───────────────────────────────────────────────────────────────
// Nine per page — a 3×3 grid. Past that the cards stop being a glance, but a
// set may legitimately keep growing, so it pages instead of scrolling:
// arrows plus a page indicator. Falls back to the last valid page when the
// set shrinks.
const PAGE_SIZE = 9;
const page = ref(1);
const pageCount = computed(() => Math.max(1, Math.ceil(working.value.length / PAGE_SIZE)));
const pagedCards = computed(() => {
  const from = (page.value - 1) * PAGE_SIZE;
  return working.value.slice(from, from + PAGE_SIZE);
});
watch(pageCount, (n) => {
  if (page.value > n) page.value = n;
});

// The loop index is page-local; saves and edits need the index in the whole
// set, which is where the array actually lives.
const globalIndex = (i: number) => (page.value - 1) * PAGE_SIZE + i;

async function persistSet(cards: StarterCard[]) {
  if (props.onSave) await props.onSave(cards);
  else {
    const next = { heading: props.set?.heading, send: props.set?.send, cards, followups: props.set?.followups };
    if (props.set?.revision) await saveHomepageStarters(next, props.set.revision);
    else await api.saveStarters(next);
  }
  // A parent's refetch can lag the save receipt. Commit the returned list now,
  // then allow that authoritative refetch to reconcile it without resetting page.
  working.value = [...cards];
  notifyStarterShortcutsChanged(props.agentId || "");
}

async function saveSet(cards: StarterCard[]): Promise<boolean> {
  if (props.readOnly || saving.value) return false;
  saving.value = true;
  try {
    await persistSet(cards);
    ElMessage.success(t("starterCards.saved"));
    emit("saved");
    return true;
  } catch (err) {
    ElMessage.error(t("starterCards.saveFailed", { msg: (err as Error).message }));
    return false;
  } finally {
    saving.value = false;
  }
}

function removeCard(i: number) {
  void saveSet(working.value.filter((_, j) => j !== i));
}

// ── The edit dialog ─────────────────────────────────────────────────────────

const cardCap = computed(() => props.maxCards ? props.maxCards : Number.POSITIVE_INFINITY);

const editing = ref(false);
const editIndex = ref(-1); // -1 = adding
const form = ref({
  title: "",
  label: "",
  icon: "",
  kind: "prompt" as "prompt" | "panel",
  prompt: "",
  panel: "",
  parts: [] as ComposerPart[],
  inputLayout: "top" as "top" | "inline",
});
const saveError = ref("");
const shortcutPreview = ref<ShortcutSnapshot | null>(null);
const officePreview = ref<OfficeReferenceSnapshot | null>(null);
function activateReference(part: ComposerPart) {
  if (part.type === "office") officePreview.value = JSON.parse(JSON.stringify(part.office));
  if (part.type === "shortcut") shortcutPreview.value = { id: part.shortcutId, title: part.title, content: part.text, ...(part.icon ? { icon: part.icon } : {}), ...(part.language ? { language: part.language } : {}), ...(part.parts ? { parts: JSON.parse(JSON.stringify(part.parts)) } : {}) };
}

function openAdd(draft?: StarterDraft) {
  if (props.readOnly || saving.value) return;
  editIndex.value = -1;
  const detached = cloneStarterDraft(draft ?? { parts: [], inputLayout: "top" });
  form.value = { title: draft ? starterDraftTitle(detached) : "", label: "", icon: "", kind: "prompt", prompt: composerDisplayText(detached.parts), panel: "", ...detached };
  saveError.value = "";
  editing.value = true;
}

function openEdit(i: number) {
  if (props.readOnly || saving.value) return;
  const c = working.value[i];
  editIndex.value = i;
  form.value = {
    title: c.title,
    label: c.label ?? "",
    icon: c.icon ?? "",
    kind: c.parts?.length || c.prompt ? "prompt" : "panel",
    prompt: c.prompt ?? "",
    panel: c.panel ?? "",
    parts: starterCardComposerParts(c),
    inputLayout: c.input_layout ?? "top",
  };
  saveError.value = "";
  editing.value = true;
}

const formError = computed(() => {
  const f = form.value;
  if (!f.title.trim()) return t("starterCards.errTitle");
  if (f.title.trim().length > 40) return t("starterCards.errTitleLong");
  if (f.kind === "prompt" && !starterDraftHasContent(f)) return t("starterCards.errAction");
  if (f.kind === "panel" && !f.panel.trim()) return t("starterCards.errAction");
  if (working.value.length >= cardCap.value && editIndex.value === -1) return t("starterCards.errMax", { n: cardCap.value });
  return "";
});

async function submitForm() {
  if (props.readOnly || formError.value || saving.value) return;
  const f = form.value;
  const draft = cloneStarterDraft(f);
  saving.value = true;
  saveError.value = "";
  try {
    const prepared = f.kind === "prompt" ? await (props.prepareDraft ? props.prepareDraft(draft) : prepareStableStarterDraft(draft)) : null;
    const card: StarterCard = {
      title: f.title.trim(),
      ...(f.label.trim() ? { label: f.label.trim() } : {}),
      ...(f.icon ? { icon: f.icon } : {}),
      ...(prepared ? { prompt: prepared.prompt || composerDisplayText(draft.parts), parts: prepared.parts, input_layout: prepared.input_layout } : { panel: f.panel.trim() }),
    };
    const next = [...working.value];
    const index = editIndex.value;
    if (index >= 0) next[index] = card;
    else next.push(card);
    await persistSet(next);
    page.value = Math.floor((index >= 0 ? index : next.length - 1) / PAGE_SIZE) + 1;
    editing.value = false;
    ElMessage.success(t("starterCards.saved"));
    emit("saved");
  } catch (err) {
    saveError.value = t("starterCards.saveFailed", { msg: (err as Error).message });
    // The mixed prompt and metadata stay in this dialog for an explicit retry.
  } finally {
    saving.value = false;
  }
}

// ── Import / sample ─────────────────────────────────────────────────────────

const fileInput = ref<HTMLInputElement | null>(null);

function pickImport() {
  if (props.readOnly) return;
  fileInput.value?.click();
}

async function onImportFile(e: Event) {
  if (props.readOnly) return;
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = ""; // allows picking the same file again after a failure
  if (!file) return;
  const text = await file.text().catch(() => "");
  let parsed: { heading?: string; cards?: StarterCard[] };
  try {
    parsed = JSON.parse(text);
  } catch {
    ElMessage.error(t("starterCards.importBadFile"));
    return;
  }
  if (!Array.isArray(parsed.cards)) {
    ElMessage.error(t("starterCards.importNoCards"));
    return;
  }
  saving.value = true;
  try {
    // An owned set (onSave) keeps its owner's heading — only the card list
    // travels; the file's heading applies to the global set alone.
    if (props.onSave) await props.onSave(parsed.cards);
    else {
      const next = {
        heading: typeof parsed.heading === "string" && parsed.heading ? parsed.heading : props.set?.heading,
        send: props.set?.send,
        cards: parsed.cards,
        followups: props.set?.followups,
      };
      if (props.set?.revision) await saveHomepageStarters(next, props.set.revision);
      else await api.saveStarters(next);
    }
    working.value = [...parsed.cards];
    notifyStarterShortcutsChanged(props.agentId || "");
    page.value = 1;
    ElMessage.success(t("starterCards.imported", { n: parsed.cards.length }));
    emit("saved");
  } catch (err) {
    ElMessage.error(t("starterCards.saveFailed", { msg: (err as Error).message }));
  } finally {
    saving.value = false;
  }
}

function downloadJSON(payload: unknown, filename: string) {
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = filename;
  a.click();
  URL.revokeObjectURL(a.href);
}

function exportCards() {
  // Export the entire saved set, not just the current page, in the same
  // shape accepted by import. Keep heading/send/followups for a round trip.
  downloadJSON({
    heading: props.set?.heading ?? props.heading,
    send: props.set?.send,
    cards: working.value,
    followups: props.set?.followups,
  }, "starters.json");
}

function downloadSample() {
  // A fixed template rather than the live set: the sample is what a person who
  // has never seen the format starts from, and the built-in 20-card set (10
  // medium, 10 complex, 3 driving the browser) is a better starting point than
  // whatever happens to be on screen. Export is the separate live-set action.
  const payload = { note: t("starterCards.sampleNote"), ...starterSample };
  downloadJSON(payload, "starters.sample.json");
}

defineExpose({ openAdd });
</script>

<template>
  <div class="starters" data-testid="starter-cards">
    <p class="heading">
      {{ heading || fallback }}
    </p>
    <div v-if="working.length" class="cards">
      <div v-for="(c, i) in pagedCards" :key="`${c.title}-${i}`" class="card-wrap">
        <button class="card" type="button" :title="[c.title, c.label].filter(Boolean).join('\n')" :disabled="saving" @click="emit('pick', c)">
          <span class="card-icon-slot" aria-hidden="true"><Icon v-if="iconFor(c.icon)" class="card-icon" :icon="iconFor(c.icon)!" width="16" /></span>
          <span class="card-title">{{ c.title }}</span>
          <span v-if="c.label" class="card-label">{{ c.label }}</span>
        </button>
        <button
          v-if="!readOnly"
          class="fab edit"
          :disabled="saving"
          :title="t('starterCards.edit')"
          :aria-label="`${t('starterCards.edit')}：${c.title}`"
          @click.stop="openEdit(globalIndex(i))"
        >
          <el-icon><EditPen /></el-icon>
        </button>
        <button
          v-if="!readOnly"
          class="fab del"
          :disabled="saving"
          :title="t('common.delete')"
          :aria-label="`${t('common.delete')}：${c.title}`"
          @click.stop="removeCard(globalIndex(i))"
        >
          <el-icon><Close /></el-icon>
        </button>
      </div>
    </div>
    <p v-else-if="!readOnly" class="none">{{ t("starterCards.none") }}</p>
    <!-- The pager only exists when there is more than one page; a single page
         of nine or fewer is just the grid. -->
    <nav v-if="pageCount > 1" class="pager" data-testid="starter-pager" :aria-label="t('starterCards.pagination')">
      <button type="button" class="page-btn" :disabled="page <= 1 || saving" :title="t('starterCards.prevPage')" :aria-label="t('starterCards.prevPage')" data-testid="starter-prev" @click="page--">
        <el-icon><ArrowLeft /></el-icon>
      </button>
      <span class="page-of">{{ t("starterCards.pageOf", { cur: page, total: pageCount }) }}</span>
      <button
        class="page-btn"
        type="button"
        :disabled="page >= pageCount || saving"
        :title="t('starterCards.nextPage')"
        :aria-label="t('starterCards.nextPage')"
        data-testid="starter-next"
        @click="page++"
      >
        <el-icon><ArrowRight /></el-icon>
      </button>
    </nav>
    <!-- The browser home's bottom toolbar: add / import / sample are one layer
         of action, centered under the grid, present with or without cards. -->
    <div v-if="!readOnly || showDownloads" class="toolbar">
      <button v-if="!readOnly" class="tool" data-testid="starter-add" :disabled="saving || working.length >= cardCap" @click="openAdd()">
        <el-icon><Plus /></el-icon>
        <span>{{ t("starterCards.add") }}</span>
      </button>
      <button v-if="!readOnly" class="tool" data-testid="starter-import" :disabled="saving" @click="pickImport">
        <el-icon><Upload /></el-icon>
        <span>{{ t("starterCards.import") }}</span>
      </button>
      <button class="tool" data-testid="starter-export" :disabled="saving || !working.length" @click="exportCards">
        <el-icon><Download /></el-icon>
        <span>{{ t("starterCards.export") }}</span>
      </button>
      <button class="tool" data-testid="starter-sample" @click="downloadSample">
        <el-icon><Download /></el-icon>
        <span>{{ t("starterCards.sample") }}</span>
      </button>
      <span v-if="saving" class="busy">{{ t("common.loading") }}</span>
      <input
        v-if="!readOnly"
        ref="fileInput"
        type="file"
        accept="application/json,.json"
        style="display: none"
        @change="onImportFile"
      />
    </div>

    <el-dialog
      v-model="editing"
      :title="editIndex >= 0 ? t('starterCards.editTitle') : t('starterCards.add')"
      width="min(820px, calc(100vw - 32px))"
      append-to-body
      class="sc-edit-dialog"
      data-testid="starter-edit-dialog"
      :close-on-click-modal="!saving"
      :close-on-press-escape="!saving"
      :show-close="!saving"
    >
      <el-form class="starter-edit-form" label-position="top" :disabled="saving" @submit.prevent>
        <el-form-item class="starter-title-field" :label="t('starterCards.fTitle')" :error="formError && !form.title.trim() ? formError : ''">
          <el-input v-model="form.title" maxlength="40" data-testid="starter-form-title" :placeholder="t('starterCards.fTitlePh')" />
        </el-form-item>
        <el-form-item class="starter-label-field" :label="t('starterCards.fLabel')">
          <el-input v-model="form.label" maxlength="30" :placeholder="t('starterCards.fLabelPh')" />
        </el-form-item>
        <el-form-item v-if="form.kind === 'prompt'" class="starter-prompt-field" :label="t('starterCards.fPrompt')">
          <InlineComposer
            v-model="form.parts"
            class="starter-prompt-editor"
            :disabled="saving"
            :submit-on-enter="false"
            data-testid="starter-form-prompt"
            :placeholder="t('starterCards.fPromptPh')"
            :aria-label="t('starterCards.fPrompt')"
            @activate-reference="activateReference"
          />
          <p v-if="form.parts.some(part => part.type !== 'text')" class="prompt-hint">{{ t("starterCards.referenceHint") }}</p>
        </el-form-item>
        <el-form-item class="starter-icon-field" :label="t('starterCards.fIcon')">
          <StarterIconPicker v-model="form.icon" module="starters" :label="t('starterCards.fIcon')" :disabled="saving" />
        </el-form-item>
        <el-form-item v-if="panels?.length || form.kind === 'panel'" class="starter-kind-field" :label="t('starterCards.fKind')">
          <el-radio-group v-model="form.kind">
            <el-radio value="prompt">{{ t("starterCards.kindPrompt") }}</el-radio>
            <!-- No registered panels, no panel action: offering it would build
                 a card the server skips anyway. -->
            <el-radio value="panel" :disabled="!(panels ?? []).length">{{ t("starterCards.kindPanel") }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <!-- Pick-only: the server skips cards naming unregistered panels, so a
             typable name would be a dead card the dialog helped build. The
             schema keeps `at` (deep links stay importable via JSON); the
             dialog just doesn't offer it. -->
        <el-form-item v-if="form.kind === 'panel'" class="starter-panel-field" :label="t('starterCards.fPanel')">
          <el-select
            v-model="form.panel"
            class="panel-select"
            filterable
            :placeholder="t('starterCards.fPanelPh')"
          >
            <el-option v-for="p in panels ?? []" :key="p.name" :value="p.name" :label="p.name" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <p v-if="saveError" role="alert" class="save-error" data-testid="starter-save-error">{{ saveError }}</p>
        <el-button :disabled="saving" @click="editing = false">{{ t("common.cancel") }}</el-button>
        <el-button type="primary" :disabled="!!formError || saving" :loading="saving" data-testid="starter-form-save" @click="submitForm">
          {{ t("common.save") }}
        </el-button>
      </template>
    </el-dialog>
    <OfficeReferenceDialog v-model="officePreview" /><ShortcutReferenceDialog v-model="shortcutPreview" />
  </div>
</template>

<style scoped lang="scss">
.starters {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 18px;
  padding: 56px 16px 8px;
}

/* Match the dialog's other fields: a select that stayed inline-width beside
   full-width inputs reads as the odd one out. */
.panel-select {
  width: 100%;
}

/* The one line of type in this UI allowed to be large. It is the only screen with
   no content of its own to compete with, and a 15px sentence floating in an empty
   column reads as a placeholder rather than a welcome. */
.heading {
  margin: 0;
  font-size: 21px;
  font-weight: 600;
  letter-spacing: -0.2px;
  color: var(--el-text-color-primary);
  text-align: center;
}

/* Three fixed columns: a page is a 3×3 grid, and a column count that
   reflowed with the conversation width would make "nine per page" a soft
   promise. */
.cards {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
  width: 100%;
  max-width: 680px;
}

.card-wrap {
  position: relative;
  min-width: 0;
}

.card {
  // Keep all five rows (icon, three title lines and label) the same size.
  // Short cards reserve the title space instead of changing the grid rhythm.
  display: grid;
  grid-template-rows: 18px 57px 16px;
  align-items: start;
  gap: 6px;
  box-sizing: border-box;
  height: 133px;
  min-height: 133px;
  width: 100%;
  padding: 14px;
  text-align: left;
  border: 1px solid var(--el-border-color-light);
  border-radius: 12px;
  background: var(--el-bg-color);
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background 0.15s ease,
    box-shadow 0.15s ease,
    transform 0.15s ease;

  /* Lifts rather than only tinting: these are the only cards in the app that are
     entirely a click target, and the movement is what says so. */
  &:hover {
    border-color: var(--pg-accent-line);
    box-shadow: var(--el-box-shadow-light);
    transform: translateY(-1px);
  }

  &:active {
    transform: translateY(0);
  }
}

.card-icon-slot {
  display: flex;
  align-items: center;
  height: 18px;
  color: var(--el-color-primary);
}

.card-title {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  line-clamp: 3;
  min-width: 0;
  max-height: 57px;
  overflow: hidden;
  overflow-wrap: anywhere;
  font-size: 13px;
  font-weight: 550;
  line-height: 19px;
  color: var(--el-text-color-primary);
}

.card-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  line-height: 16px;
  color: var(--el-text-color-secondary);
}

/* Shortcut cards' corner-circle grammar: 18px rounds that appear on hover, edit
   at the top-left, delete at the top-right. */
.fab {
  position: absolute;
  top: -7px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: 0;
  border-radius: 50%;
  background: var(--el-bg-color);
  color: var(--el-text-color-secondary);
  font-size: 11px;
  cursor: pointer;
  opacity: 0;
  box-shadow: var(--el-box-shadow-lighter);
  transition: opacity var(--pg-transition);
  padding: 0;

  &:hover {
    color: #fff;
  }
}

.card-wrap:hover .fab,
.card-wrap:focus-within .fab {
  opacity: 1;
}

.fab.edit {
  left: -7px;

  &:hover {
    background: var(--el-color-primary);
  }
}

.fab.del {
  right: -7px;

  &:hover {
    background: var(--el-color-danger);
  }
}

.none {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

.pager {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  justify-content: center;
  flex: 0 0 auto;
  padding: 8px 0;
  border-radius: 8px;
  background: var(--el-bg-color-overlay);
}

.page-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 7px;
  background: var(--el-bg-color);
  color: var(--el-text-color-secondary);
  font-size: 13px;
  cursor: pointer;
  padding: 0;
  transition:
    color var(--pg-transition),
    border-color var(--pg-transition);

  &:hover:not(:disabled) {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }

  &:disabled {
    opacity: 0.4;
    cursor: default;
  }
}

.page-of {
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  flex-wrap: wrap;
}

.tool {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition:
    color var(--pg-transition),
    border-color var(--pg-transition);

  &:hover:not(:disabled) {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }

  &:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
}

.busy {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.starter-prompt-editor {
  padding: 14px 16px;
  border: 1px solid var(--el-border-color);
  border-radius: 10px;
  background: var(--el-fill-color-blank);

  :deep(.inline-composer-document) {
    min-height: 220px;
    max-height: min(42dvh, 400px);
    font-size: 14px;
    line-height: 1.8;
  }

  &:focus-within { border-color: var(--el-color-primary); }
}
.prompt-hint, .save-error { margin: 6px 0 0; font-size: 12px; line-height: 1.6; }
.prompt-hint { color: var(--el-text-color-secondary); }
.save-error { margin: 0 0 8px; text-align: left; overflow-wrap: anywhere; color: var(--el-color-danger); }
</style>

<style lang="scss">
/* Unscoped: the dialog teleports to body (append-to-body). */
.sc-edit-dialog {
  margin-top: 4vh;
  margin-top: 4dvh;
  max-height: 92vh;
  max-height: 92dvh;
  display: flex;
  flex-direction: column;
  padding: 24px;
  border-radius: 16px;

  .el-dialog__header { margin-right: 24px; padding-bottom: 20px; }
  .el-dialog__title { font-size: 19px; font-weight: 600; }
  .el-dialog__body { min-height: 0; overflow-y: auto; padding: 0 2px; }
  .el-dialog__footer {
    flex: 0 0 auto;
    margin-top: 8px;
    padding-top: 18px;
    border-top: 1px solid var(--el-border-color-lighter);
  }
  .starter-edit-form {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    column-gap: 20px;
    align-items: start;
  }
  .el-form-item { min-width: 0; margin-bottom: 20px; }
  .el-form-item__label { padding-bottom: 8px; font-weight: 550; }
  .starter-prompt-field, .starter-icon-field { grid-column: 1 / -1; }
  .el-radio-group {
    flex-wrap: wrap;
  }

  .icon-picker {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    width: 100%;
    /* Fifty tiles plus "no icon" would stretch the dialog past the viewport;
       the picker scrolls on its own instead. */
    max-height: 120px;
    overflow-y: auto;
    padding: 4px;
  }

  .icon-tile {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    color: var(--el-text-color-secondary);
    cursor: pointer;
    padding: 0;
    transition:
      color var(--pg-transition),
      border-color var(--pg-transition),
      background var(--pg-transition);

    &:hover {
      color: var(--el-color-primary);
      border-color: var(--el-color-primary-light-5);
    }

    &.on {
      color: var(--el-color-primary);
      border-color: var(--el-color-primary);
      background: var(--el-color-primary-light-9);
    }
  }

  /* "No icon": a crossed circle, the shape every UI reads as "off". */
  .icon-slash {
    width: 16px;
    height: 16px;
    border: 1.6px solid currentColor;
    border-radius: 50%;
    position: relative;

    &::after {
      content: "";
      position: absolute;
      left: 50%;
      top: -2.5px;
      bottom: -2.5px;
      width: 1.6px;
      background: currentColor;
      transform: translateX(-50%) rotate(45deg);
    }
  }
}

@media (max-width: 560px) {
  .sc-edit-dialog {
    margin-top: 12px;
    max-height: calc(100dvh - 24px);
    padding: 18px;
    border-radius: 13px;

    .starter-edit-form { grid-template-columns: minmax(0, 1fr); }
    .el-form-item { margin-bottom: 16px; }
    .starter-prompt-editor .inline-composer-document { min-height: 180px; }
    .icon-picker { max-height: 100px; }
  }
}
</style>
