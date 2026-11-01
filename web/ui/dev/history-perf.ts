import { computed, createApp, h, nextTick, ref, type App } from "vue";
import MarkdownIt from "markdown-it";
import WorkLog from "../src/components/WorkLog.vue";
import TurnCard from "../src/components/TurnCard.vue";
import { buildTimeline } from "../src/agent/timeline";
import { buildExchanges, foldable } from "../src/agent/exchanges";
import { emptyLive, type Message } from "../src/api/types";
import { i18n } from "../src/i18n";
import { worklogExpandAll } from "../src/agent/worklogPrefs";
import "element-plus/dist/index.css";
import "../src/styles.scss";

// Only this isolated page installs instrumentation. Production source is untouched.
type ParseStats = { count: number; history: number; live: number; other: number; characters: number; durationMs: number; historyDurationMs: number; perHistory: Record<string, number> };
type Phase = { name: string; startedAt: number; endedAt: number; parse: ParseStats; timeline: { count: number; durationMs: number }; exchanges: { count: number; durationMs: number } };
type TimedEntry = { name: string; startTime: number; duration: number; processingStart?: number; processingEnd?: number; interactionId?: number };
const parseStats = (): ParseStats => ({ count: 0, history: 0, live: 0, other: 0, characters: 0, durationMs: 0, historyDurationMs: 0, perHistory: {} });
let phase: Phase | null = null;
const begin = (name: string): Phase => phase = { name, startedAt: performance.now(), endedAt: 0, parse: parseStats(), timeline: { count: 0, durationMs: 0 }, exchanges: { count: 0, durationMs: 0 } };
const originalParse = MarkdownIt.prototype.parse;
MarkdownIt.prototype.parse = function (...args: Parameters<typeof originalParse>) {
  const active = phase;
  if (!active) return originalParse.apply(this, args);
  const started = performance.now();
  try { return originalParse.apply(this, args); }
  finally {
    const elapsed = performance.now() - started;
    const source = args[0];
    const history = /^## 历史回答 (\d{4})/.exec(source)?.[1];
    active.parse.count++;
    active.parse.characters += source.length;
    active.parse.durationMs += elapsed;
    if (history) {
      active.parse.history++; active.parse.historyDurationMs += elapsed;
      active.parse.perHistory[history] = (active.parse.perHistory[history] ?? 0) + 1;
    } else if (source.startsWith("## 当前流式回答")) active.parse.live++;
    else active.parse.other++;
  }
};

const longTasks: TimedEntry[] = [];
const inputEvents: TimedEntry[] = [];
const supportedEntries = PerformanceObserver.supportedEntryTypes ?? [];
if (supportedEntries.includes("longtask")) new PerformanceObserver(list => {
  for (const entry of list.getEntries()) longTasks.push({ name: entry.name, startTime: entry.startTime, duration: entry.duration });
  if (longTasks.length > 2000) longTasks.splice(0, longTasks.length - 2000);
}).observe({ type: "longtask", buffered: true });
if (supportedEntries.includes("event")) new PerformanceObserver(list => {
  for (const raw of list.getEntries()) {
    const entry = raw as PerformanceEntry & { target?: Element; processingStart: number; processingEnd: number; interactionId: number };
    if (entry.target?.id !== "history-perf-composer") continue;
    inputEvents.push({ name: entry.name, startTime: entry.startTime, duration: entry.duration, processingStart: entry.processingStart, processingEnd: entry.processingEnd, interactionId: entry.interactionId });
  }
}).observe({ type: "event", buffered: true, durationThreshold: 16 } as PerformanceObserverInit);
const keyHandlers: { at: number; eventTime: number; eventToHandlerMs: number; isTrusted: boolean }[] = [];
document.getElementById("history-perf-composer")!.addEventListener("keydown", event => {
  const at = performance.now();
  keyHandlers.push({ at, eventTime: event.timeStamp, eventToHandlerMs: at - event.timeStamp, isTrusted: event.isTrusted });
});

const css = document.createElement("style");
css.textContent = `body{margin:0;background:#f7f8fa;color:#273442;font:14px/1.6 -apple-system,BlinkMacSystemFont,sans-serif}#history-perf-shell{max-width:980px;margin:auto;padding:20px}header{display:flex;justify-content:space-between;gap:16px}.scope{font-size:12px;color:#627381}#history-perf-root{height:640px;overflow:auto;background:white;border:1px solid #dce3e9;border-radius:12px;padding:24px}.perf-exchange{margin:0 0 28px}.perf-user{padding:12px 16px;background:#f1f4f7;border-radius:12px;white-space:pre-wrap}.perf-exchange .worklog{margin-top:14px}footer{margin-top:16px}footer label{display:block;font-size:12px}footer input{box-sizing:border-box;width:100%;padding:12px;border:1px solid #bdcbd5;border-radius:8px;font:inherit}`;
document.head.append(css);
worklogExpandAll.value = false;
const root = document.getElementById("history-perf-root")!;
const status = document.getElementById("history-perf-status")!;
const messages = ref<Message[]>([]);
const live = ref(emptyLive());
const liveVersion = ref(0);
const busy = ref(false);
const results = {};
const skills: { name: string; path: string }[] = [];
let app: App | null = null;
let mountedCount = 0;
let streaming = false;
const raf = () => new Promise<number>(resolve => requestAnimationFrame(resolve));
const pause = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));
const body = [
  "我们先核对请求范围、页面状态与已经得到的证据，再决定下一步。这里只使用固定生成的文字，不读取真实会话或任何用户文件。",
  "这段回答包含 **重点说明**、`inline_code` 和一个[普通链接](https://example.invalid/reference)，用于覆盖日常回答的基础 Markdown 结构。",
  "- 保留每一轮的问题与结论。\n- 单独记录流式更新的实际开销。\n- 区分组件挂载、布局和真实屏幕显示。",
  "| 项目 | 状态 | 说明 |\n| --- | --- | --- |\n| 时间线 | 已生成 | 固定数据 |\n| Markdown | 正常 | 包含代码与表格 |\n| 验收 | 待测量 | 不冒充真实产品全链路 |",
  "```ts\nexport function describeTask(id: string) {\n  const status = 'ready';\n  return { id, status, retries: 0 };\n}\n```",
  "> 测量范围必须清楚，组件基线不能直接当成用户操作系统的输入延迟。",
  "下一步将检查历史内容是否重复解析，并报告每个规模的样本数和重复测量结果。实际页面可能还有附件、工具卡片和图表，这些负载没有包含在本样例中。"
].join("\n\n");
export const historyText = (index: number) => `## 历史回答 ${String(index).padStart(4, "0")}\n\n${body}`;
function generate(count: number): Message[] {
  return Array.from({ length: count }, (_, i) => {
    const id = String(i + 1).padStart(4, "0");
    return [{ id: `u${id}`, role: "user" as const, content: [{ type: "text" as const, text: `问题 ${id}：请分析固定样例，给出结论与下一步。` }], ts: 1000 + i * 2000 }, { id: `a${id}`, role: "assistant" as const, content: [{ type: "text" as const, text: historyText(i + 1) }], ts: 2000 + i * 2000 }];
  }).flat();
}
function dom() {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ALL);
  let nodes = 0;
  while (walker.nextNode()) nodes++;
  return { elements: root.querySelectorAll("*").length, nodes, markdownViews: root.querySelectorAll(".markdown-view").length, workLogs: root.querySelectorAll(".worklog").length, turnCards: root.querySelectorAll(".turn").length, renderedExchanges: root.querySelectorAll(".perf-exchange").length };
}
async function finish(active: Phase) {
  active.endedAt = performance.now();
  phase = null;
  // Observer delivery and DOM census are outside the measured phase.
  await pause(0);
  const observed = longTasks.filter(entry => entry.startTime < active.endedAt && entry.startTime + entry.duration > active.startedAt);
  return { ...active, elapsedMs: active.endedAt - active.startedAt, longTasks: observed, longTaskCount: observed.length, longTaskDurationMs: observed.reduce((sum, entry) => sum + entry.duration, 0) };
}
function reset() {
  if (streaming) throw new Error("Stream is still running");
  phase = null; app?.unmount(); app = null; root.replaceChildren();
  messages.value = []; live.value = emptyLive(); busy.value = false; liveVersion.value = 0;
  keyHandlers.length = 0; inputEvents.length = 0; longTasks.length = 0;
  (document.getElementById("history-perf-composer") as HTMLInputElement).value = "";
  status.textContent = "Ready";
}
async function mount(count: number) {
  if (![10, 100, 500].includes(count)) throw new Error("Expected 10, 100 or 500 exchanges");
  reset();
  // Generate and make the transcript reactive before the timed fresh component mount.
  messages.value = generate(count); mountedCount = count;
  const active = begin("fresh-component-mount");
  app = createApp({ setup() {
    const timeline = computed(() => {
      void liveVersion.value;
      const start = performance.now();
      const value = buildTimeline([...messages.value], results, live.value, {});
      if (phase) { phase.timeline.count++; phase.timeline.durationMs += performance.now() - start; }
      return value;
    });
    const exchanges = computed(() => {
      // Read timeline first so exchange projection timing excludes its dependency.
      const items = timeline.value;
      const start = performance.now();
      const value = buildExchanges(items, busy.value);
      if (phase) { phase.exchanges.count++; phase.exchanges.durationMs += performance.now() - start; }
      return value;
    });
    return () => exchanges.value.map(ex => h("section", { key: ex.id, class: "perf-exchange", "data-exchange-id": ex.id }, [
      ex.user ? h("div", { class: "perf-user" }, ex.user.text) : null,
      foldable(ex) ? h(WorkLog, { exchange: ex, runActive: busy.value, animateFold: false, skills }) : ex.turns.map(turn => h(TurnCard, { key: turn.id, turn, runActive: busy.value, skills }))
    ]));
  } });
  app.use(i18n); app.mount(root);
  await nextTick();
  const flushMs = performance.now() - active.startedAt;
  await raf(); await raf();
  const measurement = await finish(active);
  root.scrollTop = root.scrollHeight;
  status.textContent = `${count} exchanges`;
  return { count, answerCharacters: historyText(1).length, transcriptCharacters: messages.value.reduce((sum, message) => sum + message.content.reduce((n, block) => n + (block.type === "text" ? block.text.length : 0), 0), 0), mountToNextTickMs: flushMs, mountToSecondRafMs: measurement.elapsedMs, ...measurement, dom: dom() };
}
async function stream(options: { chunks?: number; intervalMs?: number } = {}) {
  if (!app || streaming) throw new Error("Mount first; only one stream can run");
  const chunks = Math.max(1, Math.min(60, options.chunks ?? 24));
  const intervalMs = Math.max(0, Math.min(1000, options.intervalMs ?? 80));
  streaming = true;
  try {
    const setup = begin("stream-setup");
    messages.value.push({ id: "u-live", role: "user", content: [{ type: "text", text: "继续分析，并逐批输出当前回答。" }] });
    live.value = { ...emptyLive(), active: true, message_id: "a-live", text: "## 当前流式回答\n\n" };
    busy.value = true; liveVersion.value++;
    await nextTick(); await raf();
    const setupResult = await finish(setup);
    const active = begin("stream-updates");
    const updates: { index: number; scheduleLagMs: number; flushMs: number; nextRafMs: number; historyParses: number; liveParses: number; timelineMs: number }[] = [];
    for (let i = 0; i < chunks; i++) {
      const expected = active.startedAt + (i + 1) * intervalMs;
      await pause(Math.max(0, expected - performance.now()));
      const at = performance.now();
      const before = { history: active.parse.history, live: active.parse.live, timeline: active.timeline.durationMs };
      live.value.text += `批次 ${String(i + 1).padStart(3, "0")}：保留已经验证的事实，逐步更新当前结论。**固定内容**用于对比三个历史规模。\n\n`;
      liveVersion.value++;
      await nextTick();
      const flushMs = performance.now() - at;
      await raf();
      updates.push({ index: i + 1, scheduleLagMs: Math.max(0, at - expected), flushMs, nextRafMs: performance.now() - at, historyParses: active.parse.history - before.history, liveParses: active.parse.live - before.live, timelineMs: active.timeline.durationMs - before.timeline });
    }
    const measurement = await finish(active);
    const keys = keyHandlers.filter(key => key.at >= active.startedAt && key.at <= active.endedAt);
    return { count: mountedCount, chunks, intervalMs, setup: setupResult, ...measurement, updates, dom: dom(), keyboard: { source: "Playwright/Chromium synthetic keyboard dispatch; not OS keyboard or IME", handlers: keys, eventTiming: inputEvents.filter(entry => entry.startTime >= active.startedAt && entry.startTime <= active.endedAt), eventTimingThresholdMs: 16, eventTimingDurationQuantizationMs: 8 }, finalLiveTextLength: live.value.text?.length ?? 0 };
  } finally { streaming = false; phase = null; }
}
const api = {
  ready: true, mount, stream, reset, inspect: dom,
  workload: { historyText: historyText(1), historyAnswerCharacters: historyText(1).length, profiles: [10, 100, 500], scope: "buildTimeline → buildExchanges → real WorkLog/TurnCard → AnswerText/MarkdownView/CodeBlock; unvirtualized fixed text", excluded: ["full AgentView and composer reactivity", "SSE transport and throttler", "stick-to-bottom watchers", "attachments/tool-result bodies/diagrams", "OS keyboard/IME/input-to-photon", "app startup and resource download"] },
  supportedEntries
};
(window as unknown as { __historyPerf: typeof api }).__historyPerf = api;
status.textContent = "Ready · awaiting explicit measurement";
