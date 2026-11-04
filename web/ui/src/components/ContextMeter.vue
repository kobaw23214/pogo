<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight } from "@element-plus/icons-vue";
import { chooseLever, contextLevel, createContextEstimator } from "@/agent/contextEstimate";
import { createContextUsageReader, projectContextMessages } from "@/agent/contextUsage";
import { compactionReduction } from "@/agent/compactionPresentation";
import type { CompactionView, ContextMessage, Message, OverheadPart, ToolResult, Usage } from "@/api/types";
import { normalizeMessageContent } from "@/api/messageContent";

// Three layers now. The button answers "how much runway is left" at a glance; the
// popover answers "what is eating it"; and the footer answers "so what do I do",
// which the breakdown is the only thing in the app able to decide.
//
// That last layer is why the breakdown earns its complexity. Old tool output is
// dropped from the prompt automatically and can be fetched again, so a
// tools-dominated context is already being managed and compaction would buy
// little. Conversation text is the shape nothing handles by itself, and the one
// compaction exists for. Same numbers, opposite advice — see `lever`.
//
// The absolute number is the server's measured or estimated prompt size;
// character estimates only split it up.
const props = defineProps<{
  /** used is the latest turn's prompt size, not the session total. */
  used: number;
  /** A retained reading from a previous model is approximate until measured again. */
  estimated?: boolean;
  /** window is 0 for a model that is not in the catalog; the meter hides itself. */
  window: number;
  /** overhead is the server's fixed-cost estimate (system prompt + tool schemas). */
  overhead: number;
  /** What overhead is made of, itemized by the server in the same units. */
  overheadBreakdown?: OverheadPart[];
  messages: Message[];
  results: Record<string, ToolResult>;
  /** Compacted model-facing history; undefined for an ordinary session. */
  contextMessages?: ContextMessage[];
  latestCompaction?: CompactionView | null;
  /**
   * clearTrigger is the prompt size at which the server starts dropping old tool
   * results, 0 when clearing is off. The warning bands are measured against it — see
   * contextLevel for why fractions of the window are the wrong thing to use.
   */
  clearTrigger?: number;
  /** disabled while a run is in flight — compacting mid-run returns 409. */
  disabled?: boolean;
  /** compacting is true for the length of the summarising call, which is a turn. */
  compacting?: boolean;
  /** Session-cumulative billing, folded in from the old UsageMeter: the meter's
   * question is "how much runway is left", this section's is "what has the
   * session burned" — one entry point, two ledgers. Absent/zero hides it. */
  usage?: Usage;
}>();

const emit = defineEmits<{ compact: [] }>();

const { t, te } = useI18n();

const open = ref(false);
const expanded = reactive(new Set<string>());

// Settled messages and results are immutable (the stream appends, never edits
// in place), so the per-object cache turns each event's transcript rescan into
// lookups for everything but the newcomer.
const estimator = createContextEstimator();
const readContextUsage = createContextUsageReader();
const reading = computed(() => readContextUsage(props));

type CategoryKey = "overhead" | "user" | "assistant" | "tools";
interface Item {
  id: string;
  name: string;
  tokens: number;
  detail?: string;
  overheadKind?: OverheadPart["kind"];
  count?: number;
  callId?: string;
  isError?: boolean;
}
interface Cat {
  key: CategoryKey;
  label: string;
  color: string;
  tokens: number;
  note?: string;
  items?: Item[];
  moreCount?: number;
  limit?: number;
}

// Four categories, four skin tokens rather than four literals: the meter is
// rendered with inline styles, and var() works there, so the chart follows the
// skin without this file knowing which one is on.
const COLORS = {
  overhead: "var(--pg-chart-overhead)",
  user: "var(--pg-chart-user)",
  assistant: "var(--pg-chart-assistant)",
  tools: "var(--pg-chart-tools)",
};

function preview(text: string): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > 40 ? oneLine.slice(0, 40) + "…" : oneLine;
}

function messageDetail(message: Message): string {
  return normalizeMessageContent(message.content).flatMap(block => {
    if (block.type === "text") return [block.text];
    if (block.type === "tool_use") return [`${block.name}\n${JSON.stringify(block.input ?? null, null, 2)}`];
    return [];
  }).join("\n\n");
}

// The overhead's own items come from the server, which measured them; only the
// labels are made here, from stable keys. A section key this build does not
// know falls back to the key itself rather than to nothing.
function overheadLabel(p: OverheadPart): string {
  switch (p.kind) {
    case "prompt": {
      const key = `contextMeter.overheadParts.sections.${p.name}`;
      return t("contextMeter.overheadParts.prompt", { name: te(key) ? t(key) : p.name });
    }
    case "skill_tools":
      return t("contextMeter.overheadParts.skillTools", { name: p.name, n: p.count ?? 0 });
    case "mcp":
      return t("contextMeter.overheadParts.mcp", { name: p.name, n: p.count ?? 0 });
    default:
      return t("contextMeter.overheadParts.tool", { name: p.name });
  }
}

// More rows than the conversation categories get: the point of this list is
// finding the one part that grew, and it is rarely in the top five alone.
const OVERHEAD_ITEMS = 8;
const CONVERSATION_ITEMS = 5;

const overheadItems = computed<{ items?: Item[]; moreCount?: number }>(() => {
  const parts = (props.overheadBreakdown ?? []).filter((p) => p.tokens > 0);
  if (!parts.length) return {};
  const items: Item[] = parts.map(p => ({
    id: `${p.kind}:${p.name}`,
    name: overheadLabel(p),
    tokens: p.tokens,
    overheadKind: p.kind,
    count: p.count,
  })).sort((a, b) => b.tokens - a.tokens);
  return { items, moreCount: Math.max(0, items.length - OVERHEAD_ITEMS) };
});

// Raw character estimates per category. Thinking blocks are excluded on
// purpose: they are not replayed to the model, so they occupy nothing.
const raw = computed(() => {
  let user = 0;
  let assistant = 0;
  let tools = 0;
  const userItems: Item[] = [];
  const asstItems: Item[] = [];
  const toolItems: Item[] = [];
  const projected = projectContextMessages(props.contextMessages);
  const messages = projected?.messages ?? props.messages;
  const results = projected?.results ?? Object.values(props.results);

  for (const m of messages) {
    const { tokens, first } = estimator.ofMessage(m);
    if (!tokens) continue;
    const item: Item = { id: m.id, name: preview(first) || t("contextMeter.toolCallFallback"), tokens, detail: messageDetail(m) };
    if (m.role === "user") {
      user += tokens;
      userItems.push(item);
    } else {
      assistant += tokens;
      asstItems.push(item);
    }
  }

  for (const r of results) {
    const tokens = estimator.ofResult(r);
    if (!tokens) continue;
    tools += tokens;
    toolItems.push({ id: r.call_id, name: r.name ?? "tool", tokens, detail: r.text ?? "", callId: r.call_id, isError: r.is_error });
  }

  return { user, assistant, tools, userItems, asstItems, toolItems };
});

// The measured total is the authority. Estimates are normalised so the slices
// sum to it: proportions from characters, magnitude from the server.
const cats = computed<Cat[]>(() => {
  const overhead = Math.min(Math.max(0, props.overhead), reading.value.used);
  const r = raw.value;
  const convEst = r.user + r.assistant + r.tools;
  // No measurement yet (fresh session, or a reload before the first run):
  // fall back to raw estimates for everything.
  const convMeasured = Math.max(reading.value.used - overhead, 0);
  const scale = convEst > 0 ? convMeasured / convEst : 0;
  const at = (n: number) => Math.round(n * scale);

  return [
    {
      key: "overhead",
      label: t("contextMeter.cats.overhead"),
      color: COLORS.overhead,
      tokens: overhead,
      note: t("contextMeter.overheadNote"),
      limit: OVERHEAD_ITEMS,
      ...overheadItems.value,
    },
    {
      key: "user",
      label: t("contextMeter.cats.user"),
      color: COLORS.user,
      tokens: at(r.user),
      ...itemsOf(r.userItems),
    },
    {
      key: "assistant",
      label: t("contextMeter.cats.assistant"),
      color: COLORS.assistant,
      tokens: at(r.assistant),
      ...itemsOf(r.asstItems),
    },
    {
      key: "tools",
      label: t("contextMeter.cats.tools"),
      color: COLORS.tools,
      tokens: at(r.tools),
      ...itemsOf(r.toolItems),
    },
  ];
});

function itemsOf(items: Item[]): { items?: Item[]; moreCount?: number } {
  if (!items.length) return {};
  const sorted = [...items].sort((a, b) => b.tokens - a.tokens);
  return { items: sorted, moreCount: Math.max(0, sorted.length - CONVERSATION_ITEMS) };
}

const total = computed(() => reading.value.used);

const percent = computed(() => {
  return reading.value.percent;
});

const free = computed(() => reading.value.free);
const usagePrefix = computed(() => reading.value.source === "estimated" ? "~" : "");
const sourceNote = computed(() => t(`contextMeter.sources.${reading.value.source}`));

const level = computed(() => contextLevel(total.value, props.window, props.clearTrigger));

// ── Session-cumulative ledger (merged UsageMeter) ──────────────────────────
// cache_read is a slice of input, not an addition (llm.Usage on the Go side):
// the rows below keep that arithmetic visible instead of letting the reader
// add them up wrong.
const uTotal = computed(() => (props.usage ? props.usage.input + props.usage.output : 0));
const cachePct = computed(() => {
  const u = props.usage;
  if (!u?.cache_read || !u.input) return 0;
  return Math.round((u.cache_read / u.input) * 100);
});
const fresh = computed(() =>
  props.usage ? Math.max(props.usage.input - (props.usage.cache_read ?? 0), 0) : 0,
);
const grouped = (n: number) => n.toLocaleString("en-US");

// The button shows only the icon and the bar, so the numbers live in the
// tooltip; the breakdown is a click away.
// With clearing on, amber means the mechanism has engaged and is holding — not that
// something is wrong. Saying otherwise would train people to ignore it, since that is
// where a busy session normally sits.
const hint = computed(() => {
  const nums = `${total.value ? usagePrefix.value + short(total.value) : "—"}/${short(props.window)}${percent.value !== null ? ` · ${Math.round(percent.value)}%` : ""}`;
  const clearing = (props.clearTrigger ?? 0) > 0;
  let base: string;
  if (level.value === "high")
    base = clearing
      ? t("contextMeter.hint.highClearing", { nums })
      : t("contextMeter.hint.high", { nums });
  else if (level.value === "mid")
    base = clearing
      ? t("contextMeter.hint.midClearing", { nums })
      : t("contextMeter.hint.mid", { nums });
  else base = t("contextMeter.hint.low", { nums });
  // The folded-in usage keeps its at-a-glance slot in the tooltip, so merging
  // the buttons costs nothing on the bar itself.
  base += ` · ${sourceNote.value}`;
  if (uTotal.value > 0) base += ` · ${t("contextMeter.usageTitle")} ${short(uTotal.value)}`;
  return base;
});

// Which mechanism this shape calls for. Extracted rather than inlined because it is
// the one piece of judgement on this panel, and advice that is wrong is worse than
// no advice — see chooseLever for the asymmetry it encodes.
const lever = computed(() => {
  const r = raw.value;
  return chooseLever(r.user, r.assistant, r.tools);
});

// An empty session has nothing a summary could replace: the button says so
// instead of offering a no-op that would still spend a model call.
const empty = computed(() => props.messages.length === 0);
const compactionStatus = computed(() => {
  const compaction = props.latestCompaction;
  if (!compaction) return null;
  const current = compaction.after >= props.messages.length;
  const reduction = compactionReduction(compaction.before_tokens, compaction.after_tokens);
  const key = reduction === null ? "statusSummary" : current ? "statusCurrent" : "statusPrevious";
  return { current, text: t(`contextMeter.compact.${key}`, { n: compaction.before, pct: reduction ?? 0 }) };
});
const alreadyCompacted = computed(() => compactionStatus.value?.current === true && props.contextMessages?.length === 1);

function askCompact() {
  if (props.disabled || props.compacting || empty.value || alreadyCompacted.value) return;
  open.value = false;
  emit("compact");
}

// Segment widths are shares of the window, not of the used part: the free
// space is the point of the bar.
const segments = computed(() =>
  cats.value
    .filter((c) => c.tokens > 0)
    .map((c) => ({
      ...c,
      w: props.window ? Math.min((c.tokens / props.window) * 100, 100) : 0,
    })),
);

const freeW = computed(() =>
  props.window && free.value !== null ? Math.max((free.value / props.window) * 100, 0) : 0,
);

const showAll = reactive(new Set<string>());
const selected = ref<{ category: CategoryKey; id: string } | null>(null);

function toggle(key: CategoryKey, hasItems: boolean) {
  if (!hasItems) return;
  if (expanded.has(key)) {
    expanded.delete(key);
    if (selected.value?.category === key) selected.value = null;
  } else expanded.add(key);
}

function visibleItems(cat: Cat): Item[] {
  const items = cat.items ?? [];
  return showAll.has(cat.key) ? items : items.slice(0, cat.limit ?? CONVERSATION_ITEMS);
}

function toggleMore(key: CategoryKey) {
  if (showAll.has(key)) showAll.delete(key);
  else showAll.add(key);
  if (selected.value?.category === key) selected.value = null;
}

function toggleDetail(cat: Cat, item: Item) {
  selected.value = selected.value?.category === cat.key && selected.value.id === item.id
    ? null
    : { category: cat.key, id: item.id };
}

const pctOf = (tokens: number) => {
  if (!props.window || !tokens) return "0%";
  const p = (tokens / props.window) * 100;
  return (p < 10 ? p.toFixed(1) : p.toFixed(0)) + "%";
};

const short = (n: number) => {
  if (n >= 1_000_000)
    return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K`;
  return String(Math.round(n));
};

interface DetailView {
  category: CategoryKey;
  id: string;
  kind: string;
  body: string;
  facts: string[];
  content?: string;
  contentLabel?: string;
  isError?: boolean;
}

const selectedDetail = computed<DetailView | null>(() => {
  if (!selected.value) return null;
  const cat = cats.value.find(c => c.key === selected.value?.category);
  const item = cat?.items?.find(it => it.id === selected.value?.id);
  if (!cat || !item) return null;
  const detail: DetailView = {
    category: cat.key,
    id: item.id,
    kind: "",
    body: "",
    facts: [t("contextMeter.details.windowShare", { pct: pctOf(item.tokens) })],
    isError: item.isError,
  };
  if (cat.key === "overhead") {
    detail.facts.push(t("contextMeter.details.everyRequest"));
    if ((item.count ?? 0) > 1) detail.facts.push(t("contextMeter.details.declarationCount", { n: item.count }));
    switch (item.overheadKind) {
      case "prompt":
        detail.kind = t("contextMeter.details.promptPart");
        detail.body = t("contextMeter.details.promptBody");
        break;
      case "skill_tools":
        detail.kind = t("contextMeter.details.skillDefinitions");
        detail.body = t("contextMeter.details.skillBody", { n: item.count ?? 0 });
        break;
      case "mcp":
        detail.kind = t("contextMeter.details.mcpDefinitions");
        detail.body = t("contextMeter.details.mcpBody", { n: item.count ?? 0 });
        break;
      default:
        detail.kind = t("contextMeter.details.toolDefinition");
        detail.body = t("contextMeter.details.toolBody", { name: item.name });
    }
    return detail;
  }
  detail.facts.push(t("contextMeter.details.inPrompt"));
  const text = item.detail?.trim() ?? "";
  detail.content = text || t("contextMeter.details.emptyContent");
  if (cat.key === "tools") {
    detail.kind = t("contextMeter.details.toolResult");
    detail.body = t("contextMeter.details.resultBody");
    detail.contentLabel = t("contextMeter.details.resultContent");
    if (item.callId) detail.facts.push(`ID ${item.callId}`);
    if (item.isError) detail.facts.push(t("contextMeter.details.failedCall"));
  } else {
    detail.kind = t(cat.key === "user" ? "contextMeter.details.userMessage" : "contextMeter.details.assistantMessage");
    detail.body = t(cat.key === "user" ? "contextMeter.details.userBody" : "contextMeter.details.assistantBody");
    detail.contentLabel = t("contextMeter.details.messageContent");
  }
  return detail;
});

const DETAIL_PAGE_SIZE = 4000;
const detailLimit = ref(DETAIL_PAGE_SIZE);
watch(selected, () => { detailLimit.value = DETAIL_PAGE_SIZE; });
const visibleDetailContent = computed(() => selectedDetail.value?.content?.slice(0, detailLimit.value) ?? "");
const detailHasMore = computed(() => (selectedDetail.value?.content?.length ?? 0) > detailLimit.value);
function appendDetailPage() {
  if (detailHasMore.value) detailLimit.value += DETAIL_PAGE_SIZE;
}
function onDetailScroll(event: Event) {
  const el = event.currentTarget as HTMLElement;
  if (el.scrollHeight - el.scrollTop - el.clientHeight <= 32) appendDetailPage();
}
</script>

<template>
  <el-popover
    v-if="window"
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="580"
    teleported
    :popper-options="{ modifiers: [{ name: 'preventOverflow', options: { altAxis: true, tether: false, padding: 12 } }] }"
    popper-class="ctx-pop"
  >
    <template #reference>
      <button class="meter-btn tip" :class="level" :data-tip="open ? undefined : hint">
        <el-icon>
          <!-- Stacked-layers context icon (~/Downloads/上下文.svg);
               currentColor keeps it in step with the bar beside it. -->
          <svg viewBox="0 0 1024 1024" fill="currentColor" aria-hidden="true">
            <path
              d="M909.0048 797.2352a32 32 0 1 0-26.0096-58.4704l26.0096 58.4704z m-768-58.4704a32 32 0 1 0-26.0096 58.4704l26.0096-58.4704z m768-154.88a32 32 0 0 0-26.0096-58.4704l26.0096 58.4704z m-768-58.4704a32 32 0 1 0-26.0096 58.4704l26.0096-58.4704z m6.3488-159.9488l312.1664 156.0576 28.672-57.2416L175.9232 308.224l-28.672 57.2416z m417.1264 156.0576l312.1664-156.0576-28.6208-57.2416-312.1664 156.0576 28.6208 57.2416z m312.1664-289.6384L564.48 75.776l-28.672 57.2416 312.2176 156.1088 28.672-57.2416zM459.52 75.776L147.3536 231.936l28.6208 57.2416 312.1664-156.1088-28.6208-57.2416z m104.96 0a117.3504 117.3504 0 0 0-104.96 0l28.672 57.2416c14.9504-7.4752 32.6656-7.4752 47.616 0l28.672-57.2416z m312.1664 289.6896c55.04-27.5456 55.04-106.0864 0-133.632l-28.6208 57.2928a10.6496 10.6496 0 0 1 0 19.0976l28.672 57.2416z m-417.1264 156.0576a117.2992 117.2992 0 0 0 104.96 0l-28.672-57.2416a53.3504 53.3504 0 0 1-47.616 0l-28.672 57.2416zM175.9744 308.224a10.6496 10.6496 0 0 1 0-19.0976l-28.672-57.2416c-54.9888 27.4944-54.9888 106.0352 0 133.5808l28.672-57.2416z m383.6928 644.3008l349.3376-155.2896-26.0096-58.4704-349.3376 155.2384 26.0096 58.5216zM114.9952 797.184l349.3376 155.2896 26.0096-58.5216-349.3376-155.2384-26.0096 58.4704z m418.6624 96.768c-13.824 6.144-29.5424 6.144-43.3152 0l-26.0096 58.5216a117.2992 117.2992 0 0 0 95.3344 0l-26.0096-58.5216z m26.0096-154.8288l349.3376-155.2896-26.0096-58.4704-349.3376 155.2896 26.0096 58.4704z m-444.672-155.2896l349.3376 155.2896 26.0096-58.4704-349.3376-155.2896-26.0096 58.4704z m418.6624 96.768c-13.824 6.144-29.5424 6.144-43.3152 0l-26.0096 58.5216a117.3504 117.3504 0 0 0 95.3344 0l-26.0096-58.4704z"
            />
          </svg>
        </el-icon>
        <span class="mini">
          <span
            class="seg oh"
            :style="{ width: `${window ? Math.min((Math.min(overhead, total) / window) * 100, 100) : 0}%` }"
          />
          <span class="seg conv" :style="{ width: `${Math.max(Math.min(percent ?? 0, 100) - (Math.min(overhead, total) / window) * 100, 0)}%` }" />
        </span>
      </button>
    </template>

    <div class="ctx-pop-content">
      <div class="panel-heading">
        <span class="panel-title">{{ t("contextMeter.title") }}</span>
        <span class="panel-hint">{{ t("contextMeter.interactionHint") }}</span>
      </div>
      <div class="head">
        <span class="big">{{ reading.source === 'unknown' ? '—' : usagePrefix + short(total) }}</span>
        <span class="dim">/ {{ short(window) }}</span>
        <span class="pct-tag" :class="level">{{ percent === null ? '—' : `${Math.round(percent)}%` }}</span>
      </div>
      <p class="reading-source" data-testid="context-reading-source">{{ sourceNote }}</p>

      <div class="stack">
        <div
          v-for="seg in segments"
          :key="seg.key"
          class="seg"
          :style="{ width: `${seg.w}%`, background: seg.color }"
          :title="t('contextMeter.segTitle', { label: seg.label, tokens: short(seg.tokens), pct: pctOf(seg.tokens) })"
        />
        <div
          v-if="freeW > 0"
          class="seg free"
          :style="{ width: `${freeW}%` }"
          :title="t('contextMeter.freeSegTitle', { tokens: short(free ?? 0), pct: pctOf(free ?? 0) })"
        />
      </div>

      <div class="rows">
        <template v-for="c in cats" :key="c.key">
          <button
            type="button"
            class="row"
            :class="{ clickable: c.items?.length }"
            :aria-expanded="c.items?.length ? expanded.has(c.key) : undefined"
            @click="toggle(c.key, !!c.items?.length)"
          >
            <span class="dot" :style="{ background: c.color }" />
            <span class="name">{{ c.label }}</span>
            <span class="val">~{{ short(c.tokens) }} <em>{{ pctOf(c.tokens) }}</em></span>
            <el-icon v-if="c.items?.length" class="chev" :class="{ down: expanded.has(c.key) }">
              <ArrowRight />
            </el-icon>
          </button>
          <div v-if="c.note" class="note">{{ c.note }}</div>
          <div v-if="c.items?.length && expanded.has(c.key)" class="items">
            <template v-for="it in visibleItems(c)" :key="it.id">
              <button
                type="button"
                class="item"
                :class="{ selected: selectedDetail?.category === c.key && selectedDetail.id === it.id }"
                :aria-expanded="selectedDetail?.category === c.key && selectedDetail.id === it.id"
                @click="toggleDetail(c, it)"
              >
                <span class="iname" :title="it.name">{{ it.name }}</span>
                <span class="itok">~{{ short(it.tokens) }}</span>
                <el-icon class="item-chev"><ArrowRight /></el-icon>
              </button>
              <div v-if="selectedDetail?.category === c.key && selectedDetail.id === it.id" class="item-detail">
                <div class="detail-head">
                  <span class="detail-kind">{{ selectedDetail.kind }}</span>
                  <button type="button" class="detail-close" :aria-label="t('contextMeter.details.close')" @click="selected = null">×</button>
                </div>
                <div class="detail-title" :title="it.name">{{ it.name }}</div>
                <p class="detail-body">{{ selectedDetail.body }}</p>
                <div class="detail-facts">
                  <span v-for="fact in selectedDetail.facts" :key="fact">{{ fact }}</span>
                </div>
                <template v-if="selectedDetail.contentLabel">
                  <div class="detail-content-label">{{ selectedDetail.contentLabel }}</div>
                  <pre class="detail-content" :class="{ error: selectedDetail.isError }" @scroll.passive="onDetailScroll">{{ visibleDetailContent }}</pre>
                  <button v-if="detailHasMore" type="button" class="detail-load-more" @click="appendDetailPage">{{ t("contextMeter.details.loadMore") }}</button>
                </template>
              </div>
            </template>
            <button v-if="c.moreCount" type="button" class="more" :aria-expanded="showAll.has(c.key)" @click="toggleMore(c.key)">
              {{ showAll.has(c.key) ? t("contextMeter.lessItems") : t("contextMeter.moreItems", { n: c.moreCount }) }}
              <el-icon class="chev" :class="{ down: showAll.has(c.key) }"><ArrowRight /></el-icon>
            </button>
          </div>
        </template>
        <div class="row">
          <span class="dot hollow" />
          <span class="name">{{ t("contextMeter.freeSpace") }}</span>
          <span class="val">{{ free === null ? '—' : usagePrefix + short(free) }} <em v-if="free !== null">{{ pctOf(free) }}</em></span>
        </div>
      </div>

      <div class="act">
        <div v-if="compactionStatus" class="compaction-status" data-testid="context-compaction-status">
          <span class="status-mark" aria-hidden="true" />
          <span>{{ compactionStatus.text }}</span>
        </div>
        <!-- The advice comes before the button, because which lever to pull is the
             question and the button is only one of the two answers. -->
        <p v-if="compactionStatus?.current" class="advice compacted">{{ t("contextMeter.compact.currentAdvice") }}</p>
        <p v-else-if="lever" class="advice" :class="lever.kind">
          <template v-if="lever.kind === 'tools'">
            {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.toolWord") }}</strong>{{ t("contextMeter.advice.toolsPost", { share: lever.share }) }}
          </template>
          <template v-else>
            {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.convWord") }}</strong>{{ t("contextMeter.advice.convPost", { share: lever.share }) }}
          </template>
        </p>
        <button
          class="compact-btn"
          :class="{ suggested: !empty && !compactionStatus?.current && lever?.kind === 'conversation' }"
          :disabled="disabled || compacting || empty || alreadyCompacted"
          :title="
            disabled
              ? t('contextMeter.compact.disabledTitle')
              : empty
                ? t('contextMeter.compact.emptyTitle')
                : alreadyCompacted
                  ? t('contextMeter.compact.doneTitle')
                  : t('contextMeter.compact.title')
          "
          @click="askCompact"
        >
          {{
            compacting
              ? t("contextMeter.compact.compacting")
              : empty
                ? t("contextMeter.compact.emptyLabel")
                : alreadyCompacted
                  ? t("contextMeter.compact.doneLabel")
                  : compactionStatus?.current
                    ? t("contextMeter.compact.again")
                    : t("contextMeter.compact.label")
          }}
        </button>
      </div>

      <!-- The folded-in UsageMeter: a second ledger under the same roof. Above
           the foot because it is data, not advice; below the act block because
           the runway question outranks the spend question. -->
      <div v-if="usage && uTotal > 0" class="usage-sec">
        <div class="sec-head">
          <span class="sec-big">{{ short(uTotal) }}</span>
          <span class="dim">{{ t("contextMeter.usageTitle") }}</span>
          <span v-if="cachePct" class="cache-tag">{{ cachePct }}%</span>
        </div>
        <div class="urows">
          <div class="urow">
            <span class="name">{{ t("contextMeter.usageRows.inputTotal") }}</span>
            <span class="val">{{ grouped(usage.input) }}</span>
          </div>
          <div v-if="usage.cache_read" class="urow sub">
            <span class="name">{{ t("contextMeter.usageRows.cacheHit") }}</span>
            <span class="val cache">{{ grouped(usage.cache_read) }}</span>
          </div>
          <div class="urow" :class="{ sub: usage.cache_read }">
            <span class="name">{{ t("contextMeter.usageRows.billedInput") }}</span>
            <span class="val">{{ grouped(fresh) }}</span>
          </div>
          <div class="urow">
            <span class="name">{{ t("contextMeter.usageRows.output") }}</span>
            <span class="val">{{ grouped(usage.output) }}</span>
          </div>
          <div v-if="usage.reasoning" class="urow sub">
            <span class="name">{{ t("contextMeter.usageRows.reasoning") }}</span>
            <span class="val">{{ grouped(usage.reasoning) }}</span>
          </div>
        </div>
      </div>

      <div class="foot">
        {{ t("contextMeter.foot") }}
      </div>
    </div>
  </el-popover>
</template>

<style scoped lang="scss">
.meter-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 11px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  font-size: 12px;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 16px;
    color: var(--el-text-color-primary);
  }

  &:hover {
    background: var(--el-fill-color-light);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.mini {
  position: relative;
  width: 48px;
  height: 4px;
  border-radius: 2px;
  background: var(--el-fill-color-dark);
  overflow: hidden;
  display: inline-flex;

  .seg {
    height: 100%;

    &.oh {
      background: var(--el-text-color-placeholder);
    }

    &.conv {
      background: var(--el-color-success);
    }
  }
}

.mid .mini .seg.conv {
  background: var(--el-color-warning);
}

.high .mini .seg.conv {
  background: var(--el-color-danger);
}

/* The menu lives outside the composer's clipped scrolling container. */
:global(.ctx-pop) {
  padding: 16px !important;
  border-radius: 14px !important;
  max-width: calc(100vw - 24px);
  box-sizing: border-box;
}

.ctx-pop-content {
  max-height: min(720px, calc(100dvh - 48px));
  overflow-y: auto;
  scrollbar-gutter: stable;
}
.panel-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 10px; }
.panel-title { color: var(--el-text-color-primary); font-size: 13px; font-weight: 600; }
.panel-hint { color: var(--el-text-color-placeholder); font-size: 11px; text-align: right; }
.reading-source { margin: -2px 0 10px; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.5; }

.head {
  display: flex;
  align-items: baseline;
  gap: 5px;
  margin-bottom: 6px;

  .big {
    font-size: 22px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    letter-spacing: -.02em;
  }

  .dim {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .pct-tag {
    margin-left: auto;
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    color: var(--el-color-success);

    &.mid {
      background: color-mix(in srgb, var(--el-color-warning) 14%, transparent);
      color: var(--el-color-warning);
    }

    &.high {
      background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
      color: var(--el-color-danger);
    }
  }
}

.stack {
  display: flex;
  height: 12px;
  border-radius: 6px;
  overflow: hidden;
  background: var(--el-fill-color-light);
  margin: 12px 0 14px;

  .seg {
    height: 100%;

    &.free {
      background: repeating-linear-gradient(
        -45deg,
        transparent 0 3px,
        var(--el-fill-color) 3px 6px
      );
    }
  }
}

.rows {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.row {
  display: flex;
  align-items: center;
  gap: 9px;
  width: 100%;
  min-height: 38px;
  padding: 8px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  font-size: 12px;
  text-align: left;

  &.clickable {
    cursor: pointer;

    &:hover, &[aria-expanded="true"] {
      background: var(--el-fill-color-light);
    }
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }

  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.hollow {
      border: 1.5px solid var(--el-text-color-placeholder);
      background: transparent;
    }
  }

  .name {
    color: var(--el-text-color-primary);
    font-weight: 500;
  }

  .val {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);

    em {
      font-style: normal;
      color: var(--el-text-color-secondary);
      margin-left: 5px;
    }
  }

  .chev {
    flex: none;
    color: var(--el-text-color-secondary);
    transition: transform 0.2s;

    &.down {
      transform: rotate(90deg);
    }
  }
}

.note {
  padding: 0 10px 6px 30px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

.items {
  margin: 0 0 7px 14px;
  padding: 4px 0 4px 14px;
  border-left: 1px solid var(--el-border-color-lighter);

  .item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 8px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    font-size: 12px;
    text-align: left;
    cursor: pointer;

    &:hover, &.selected {
      background: var(--el-fill-color-light);
    }

    &.selected .iname {
      color: var(--el-color-primary);
    }

    &:focus-visible {
      outline: 2px solid var(--el-color-primary);
      outline-offset: 1px;
    }
  }

  .iname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-text-color-regular);
  }

  .itok {
    flex: none;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-secondary);
  }

  .item-chev {
    flex: none;
    width: 12px;
    color: var(--el-text-color-placeholder);
    transition: transform .18s;
  }

  .item.selected .item-chev {
    color: var(--el-color-primary);
    transform: rotate(90deg);
  }

  .more {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    margin: 4px 0 0 8px;
    padding: 5px 7px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: var(--el-color-primary);
    font-size: 11px;
    cursor: pointer;

    &:hover { background: var(--el-color-primary-light-9, var(--el-fill-color-light)); }
    &:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
    .chev { transition: transform .18s; }
    .chev.down { transform: rotate(90deg); }
  }
}

.item-detail {
  margin: 4px 6px 8px 8px;
  padding: 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
  box-shadow: 0 4px 14px #00000008;
}
.detail-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.detail-kind { padding: 2px 7px; border-radius: 5px; background: var(--el-color-primary-light-9, var(--el-fill-color-light)); color: var(--el-color-primary); font-size: 11px; }
.detail-close { width: 24px; height: 24px; padding: 0; border: 0; border-radius: 5px; background: transparent; color: var(--el-text-color-secondary); font-size: 18px; line-height: 1; cursor: pointer; }
.detail-close:hover { background: var(--el-fill-color-light); color: var(--el-text-color-primary); }
.detail-close:focus-visible { outline: 2px solid var(--el-color-primary); }
.detail-title { margin-top: 9px; color: var(--el-text-color-primary); font-size: 13px; font-weight: 600; line-height: 1.5; overflow-wrap: anywhere; }
.detail-body { margin: 6px 0 0; color: var(--el-text-color-regular); font-size: 12px; line-height: 1.65; }
.detail-facts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 10px; }
.detail-facts > span { padding: 3px 7px; border-radius: 5px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 11px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.detail-content-label { margin-top: 12px; color: var(--el-text-color-secondary); font-size: 11px; }
.detail-content { max-height: 220px; margin: 6px 0 0; padding: 10px; overflow: auto; border-radius: 6px; background: var(--el-fill-color-light); color: var(--el-text-color-regular); font-family: var(--pg-mono, monospace); font-size: 11px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.detail-content.error { border-left: 2px solid var(--el-color-danger); }
.detail-load-more { display: block; margin: 7px auto 0; padding: 4px 8px; border: 0; border-radius: 5px; background: transparent; color: var(--el-color-primary); font-size: 11px; cursor: pointer; }
.detail-load-more:hover { background: var(--el-color-primary-light-9, var(--el-fill-color-light)); }
.detail-load-more:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }

@media (max-width: 640px) {
  .panel-hint { display: none; }
  .items { margin-left: 4px; padding-left: 8px; }
  .item-detail { margin-left: 0; }
}

@media (prefers-reduced-motion: reduce) {
  .row .chev, .items .item-chev, .items .more .chev { transition: none; }
}

.act {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.compaction-status {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0 0 10px;
  padding: 9px 10px;
  border: 1px solid var(--el-color-success-light-7);
  border-radius: 8px;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
  font-size: 11px;
  line-height: 1.6;
}
.status-mark {
  flex: 0 0 6px;
  width: 6px;
  height: 6px;
  margin-top: 5px;
  border-radius: 50%;
  background: currentColor;
}

.advice {
  margin: 0 0 8px;
  font-size: 11px;
  line-height: 1.6;
  color: var(--el-text-color-regular);

  strong {
    font-weight: 600;
    color: var(--el-text-color-primary);
  }
}

.compact-btn {
  width: 100%;
  height: 28px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s;

  &:hover:not(:disabled) {
    background: var(--el-fill-color-light);
  }

  /* Suggested only when the breakdown says so: a button that always looks like
     the recommended action stops carrying information. */
  &.suggested {
    border-color: var(--el-color-primary);
    color: var(--el-color-primary);
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.foot {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

.usage-sec {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.sec-head {
  display: flex;
  align-items: baseline;
  gap: 5px;
  margin-bottom: 4px;

  .sec-big {
    font-size: 15px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }

  .cache-tag {
    margin-left: auto;
    font-size: 11px;
    padding: 1px 7px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    color: var(--el-color-success);
  }
}

.urow {
  display: flex;
  align-items: center;
  padding: 3px 2px;
  font-size: 12px;

  .name {
    color: var(--el-text-color-regular);
  }

  &.sub .name {
    padding-left: 12px;
    color: var(--el-text-color-secondary);
  }

  .val {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);

    &.cache {
      color: var(--el-color-success);
    }
  }
}
</style>
