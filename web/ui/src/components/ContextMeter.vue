<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight } from "@element-plus/icons-vue";
import { chooseLever, contextLevel, createContextEstimator } from "@/agent/contextEstimate";
import { createContextUsageReader } from "@/agent/contextUsage";
import type { Message, OverheadPart, ToolResult, Usage } from "@/api/types";

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
// The absolute number is always the server's measured prompt size; character
// estimates only split it up.
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

interface Item {
  name: string;
  tokens: number;
}
interface Cat {
  key: string;
  label: string;
  color: string;
  tokens: number;
  note?: string;
  items?: Item[];
  moreCount?: number;
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

function top5(items: Item[]): { items: Item[]; more: number } {
  items.sort((a, b) => b.tokens - a.tokens);
  return { items: items.slice(0, 5), more: Math.max(0, items.length - 5) };
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

const overheadItems = computed<{ items?: Item[]; moreCount?: number }>(() => {
  const parts = (props.overheadBreakdown ?? []).filter((p) => p.tokens > 0);
  if (!parts.length) return {};
  const items = parts.map((p) => ({ name: overheadLabel(p), tokens: p.tokens })).sort((a, b) => b.tokens - a.tokens);
  return { items: items.slice(0, OVERHEAD_ITEMS), moreCount: Math.max(0, items.length - OVERHEAD_ITEMS) };
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

  for (const m of props.messages) {
    const { tokens, first } = estimator.ofMessage(m);
    if (!tokens) continue;
    const item: Item = { name: preview(first) || t("contextMeter.toolCallFallback"), tokens };
    if (m.role === "user") {
      user += tokens;
      userItems.push(item);
    } else {
      assistant += tokens;
      asstItems.push(item);
    }
  }

  for (const r of Object.values(props.results)) {
    const tokens = estimator.ofResult(r);
    if (!tokens) continue;
    tools += tokens;
    toolItems.push({ name: r.name ?? "tool", tokens });
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
  const { items: top, more } = top5(items);
  return { items: top, moreCount: more };
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

function askCompact() {
  if (props.disabled || props.compacting || empty.value) return;
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

function toggle(key: string, hasItems: boolean) {
  if (!hasItems) return;
  if (expanded.has(key)) expanded.delete(key);
  else expanded.add(key);
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
</script>

<template>
  <el-popover
    v-if="window"
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="340"
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
          <div
            class="row"
            :class="{ clickable: c.items?.length }"
            @click="toggle(c.key, !!c.items?.length)"
          >
            <span class="dot" :style="{ background: c.color }" />
            <span class="name">{{ c.label }}</span>
            <span class="val">~{{ short(c.tokens) }} <em>{{ pctOf(c.tokens) }}</em></span>
            <el-icon v-if="c.items?.length" class="chev" :class="{ down: expanded.has(c.key) }">
              <ArrowRight />
            </el-icon>
          </div>
          <div v-if="c.note" class="note">{{ c.note }}</div>
          <div v-if="c.items?.length && expanded.has(c.key)" class="items">
            <div v-for="(it, i) in c.items" :key="i" class="item">
              <span class="iname" :title="it.name">{{ it.name }}</span>
              <span class="itok">~{{ short(it.tokens) }}</span>
            </div>
            <div v-if="c.moreCount" class="more">{{ t("contextMeter.moreItems", { n: c.moreCount }) }}</div>
          </div>
        </template>
        <div class="row">
          <span class="dot hollow" />
          <span class="name">{{ t("contextMeter.freeSpace") }}</span>
          <span class="val">{{ free === null ? '—' : usagePrefix + short(free) }} <em v-if="free !== null">{{ pctOf(free) }}</em></span>
        </div>
      </div>

      <div class="act">
        <!-- The advice comes before the button, because which lever to pull is the
             question and the button is only one of the two answers. -->
        <p v-if="lever" class="advice" :class="lever.kind">
          <template v-if="lever.kind === 'tools'">
            {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.toolWord") }}</strong>{{ t("contextMeter.advice.toolsPost", { share: lever.share }) }}
          </template>
          <template v-else>
            {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.convWord") }}</strong>{{ t("contextMeter.advice.convPost", { share: lever.share }) }}
          </template>
        </p>
        <button
          class="compact-btn"
          :class="{ suggested: !empty && lever?.kind === 'conversation' }"
          :disabled="disabled || compacting || empty"
          :title="
            disabled
              ? t('contextMeter.compact.disabledTitle')
              : empty
                ? t('contextMeter.compact.emptyTitle')
                : t('contextMeter.compact.title')
          "
          @click="askCompact"
        >
          {{
            compacting
              ? t("contextMeter.compact.compacting")
              : empty
                ? t("contextMeter.compact.emptyLabel")
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
  padding: 12px !important;
  border-radius: 10px !important;
  max-width: calc(100vw - 24px);
  box-sizing: border-box;
}

.ctx-pop-content {
  max-height: calc(100dvh - 48px);
  overflow-y: auto;
}
.reading-source { margin: -2px 0 10px; color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.5; }

.head {
  display: flex;
  align-items: baseline;
  gap: 4px;
  margin-bottom: 8px;

  .big {
    font-size: 18px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }

  .dim {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }

  .pct-tag {
    margin-left: auto;
    font-size: 11px;
    padding: 1px 7px;
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
  height: 10px;
  border-radius: 5px;
  overflow: hidden;
  background: var(--el-fill-color-light);
  margin-bottom: 10px;

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
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 6px;
  border-radius: 5px;
  font-size: 12px;

  &.clickable {
    cursor: pointer;

    &:hover {
      background: var(--el-fill-color-light);
    }
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
  }

  .val {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);

    em {
      font-style: normal;
      color: var(--el-text-color-secondary);
      margin-left: 4px;
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
  padding: 0 6px 4px 22px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.items {
  padding: 0 6px 4px 22px;

  .item {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 2px 0;
    font-size: 11px;
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

  .more {
    padding-top: 2px;
    font-size: 11px;
    color: var(--el-text-color-placeholder);
  }
}

.act {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
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
