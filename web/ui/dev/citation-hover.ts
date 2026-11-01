import { createApp, h, nextTick, reactive } from "vue";
import type { Composer } from "vue-i18n";
import "element-plus/dist/index.css";
import "../src/styles.scss";
import AnswerText from "../src/components/AnswerText.vue";
import WorkLog from "../src/components/WorkLog.vue";
import { buildTimeline } from "../src/agent/timeline";
import { buildExchanges } from "../src/agent/exchanges";
import { canJumpToCite, jumpToCite } from "../src/agent/citeJump";
import { worklogExpandAll } from "../src/agent/worklogPrefs";
import { i18n } from "../src/i18n";
import { emptyLive, type CiteSource } from "../src/api/types";

// The fixture changes only its i18n instance, never the user's saved locale.
(i18n.global as unknown as Composer).locale.value = "zh-CN";

const pdfOutput = `# 演示分析报告.pdf · 第 1-6 页 / 共 6 页

===== Page 1 =====
这是虚构回归样例的封面摘要。本文讨论纸飞机的颜色分类与普通阅读体验。本段没有网络地址，也不能解释后面的测试配置。

===== Page 3 =====
彩色雨伞演示使用蓝色封面与橙色标签，便于人工确认片段来自第三页。这里没有任何实际安全事件或真实系统信息。

===== Page 6 =====
演示网络地址为 192.0.2.42:443，条目标识为 DEMO-ALPHA-42。该地址仅为测试表格和引用片段选择而填写。核验时应显示这一段与第 6 页输出标记，不能用首页的纸飞机简介替代。此页同时说明：演示服务使用本地工作区配置，超时为 30 秒，重试上限为 3 次。这些数值均为虚构材料，不对应任何运行中的系统。较长段落用于验证卡片的展开、复制、自动定位和缩小窗口后的内部滚动。来源中的每个字符应与所展示的原始工具输出一致，卡片不能把本段改写为新结论，也不应把词语匹配描述成事实已核验。最后一行保留演示标识 DEMO-ALPHA-42 供人工比对。`;

const sources: CiteSource[] = [
  { label: "S1", type: "command_exec", title: "pdf-text 演示分析报告.pdf", locator: "cmd://fixture-pdf-text",
    call_id: "fixture-pdf", excerpt: "这是虚构样例的首页简介：纸飞机颜色分类。", content: pdfOutput,
    hash: "fixture-pdf-snapshot", ts: 1789000000000, exit_code: 0 },
  { label: "S2", type: "file_read", title: "fixtures/demo-config.json", locator: "file://fixtures/demo-config.json#L10-14",
    call_id: "fixture-config", excerpt: "演示服务使用本地工作区配置；超时 30 秒，重试上限 3 次。",
    content: '{\n  "fixture": true,\n  "timeout_seconds": 30,\n  "retries": 3,\n  "note": "演示服务使用本地工作区配置"\n}',
    hash: "fixture-config-snapshot", ts: 1789000001000 },
  { label: "S3", type: "command_exec", title: "演示配置检查", locator: "cmd://fixture-config-check",
    call_id: "fixture-check", excerpt: "DEMO ONLY: configuration fixture checks passed.",
    content: "DEMO ONLY: configuration fixture checks passed.\n30-second timeout and 3 retries were observed in test data.\nExit code: 0. No actual command was run by this fixture.",
    hash: "fixture-command-snapshot", ts: 1789000002000, exit_code: 0 },
];
const secondSources: CiteSource[] = [{
  ...sources[0], title: "第二个独立回答的演示材料", type: "file_read", locator: "file://fixtures/second-answer.txt",
  call_id: "fixture-second", hash: "fixture-second-snapshot", excerpt: "SECOND ANSWER ONLY：这条来源属于第二个回答，主题为绿色风筝。",
  content: "SECOND ANSWER ONLY：这条来源属于第二个回答，主题为绿色风筝。它不包含第一个回答的地址或 PDF 内容。",
}];

const table = `

| 演示条目 | 测试值 | 出处 |
| --- | --- | --- |
| 演示网络地址 | \`192.0.2.42:443\` · DEMO-ALPHA-42 | [1](#cite:S1) |
| 配置超时 | 30 秒 | [1](#cite:S2) |
`;
const grouped = "演示材料与本地工作区配置共同描述同一个模拟流程。[1](#cite:S1) [1](#cite:S2)";
const sceneText = {
  normal: grouped + table + "\n\n演示配置检查已通过。[1](#cite:S3)",
  pending: "等待来源的演示材料。[1](#cite:S1) [1](#cite:S2)\n\n后续独立引用应保持可用。[1](#cite:S3)",
  stream: grouped,
  two: "第一个回答：演示网络地址为 192.0.2.42:443。[1](#cite:S1)",
  focus: "快速经过这个引用，或用 Tab 聚焦它。[1](#cite:S2)",
  lazy: "引用应打开已折叠的虚构执行记录。[1](#cite:S1)\n\n配置读取也应定位到对应行。[1](#cite:S2)",
};
type Scene = keyof typeof sceneText;
const sceneNames: Record<Scene, string> = {
  normal: "正常分组与表格", pending: "等待来源元数据", stream: "流式追加与分组合并",
  two: "两个独立回答", focus: "短悬浮与键盘焦点",
  lazy: "折叠执行记录定位",
};
const chunks = [" [1](#cite:S3)", "\n\n正在追加演示表格。", table,
  "\n\n第三页的彩色雨伞采用蓝色封面与橙色标签。[1](#cite:S1)",
  "\n\n演示流式输出结束；卡片的来源页和焦点应保持稳定。"];
const state = reactive({ scene: "normal" as Scene, text: sceneText.normal, sources: [...sources], streaming: false, chunk: 0, revision: 0 });
let streamTimer: ReturnType<typeof setInterval> | undefined;
let metadataTimer: ReturnType<typeof setTimeout> | undefined;
let metadataPending = false;
const status = document.getElementById("fixture-status")!;

function updateControls() {
  document.querySelectorAll<HTMLButtonElement>("[data-scene]").forEach((button) => button.setAttribute("aria-pressed", String(button.dataset.scene === state.scene)));
  (document.getElementById("append-chunk") as HTMLButtonElement).disabled = state.chunk >= chunks.length;
  (document.getElementById("start-stream") as HTMLButtonElement).disabled = streamTimer !== undefined || state.chunk >= chunks.length;
  status.textContent = `${sceneNames[state.scene]} · 来源 ${state.sources.length}/3 · ${state.streaming ? "流式中" : "已结束"} · 追加 ${state.chunk}/${chunks.length}${metadataPending ? " · 3 秒后补齐来源" : ""}`;
}
function stop() {
  clearInterval(streamTimer);
  streamTimer = undefined;
  state.streaming = false;
  updateControls();
}
function reset(scene = state.scene) {
  stop();
  clearTimeout(metadataTimer);
  metadataPending = false;
  state.scene = scene;
  state.text = sceneText[scene];
  state.sources = scene === "pending" ? [] : [...sources];
  state.streaming = scene === "pending" || scene === "stream";
  state.chunk = 0;
  state.revision++;
  if (scene === "lazy") worklogExpandAll.value = false;
  updateControls();
}
function append() {
  if (state.chunk >= chunks.length) return;
  state.streaming = true;
  state.text += chunks[state.chunk++];
  if (state.chunk === chunks.length) stop();
  else updateControls();
}
function resolveMetadata() {
  clearTimeout(metadataTimer);
  metadataPending = false;
  state.sources = [...sources];
  updateControls();
}
document.querySelectorAll<HTMLButtonElement>("[data-scene]").forEach((button) => button.addEventListener("click", () => reset(button.dataset.scene as Scene)));
document.getElementById("append-chunk")!.addEventListener("click", append);
document.getElementById("start-stream")!.addEventListener("click", () => {
  if (streamTimer !== undefined) return;
  state.streaming = true;
  streamTimer = setInterval(append, 2000);
  updateControls();
});
document.getElementById("stop-stream")!.addEventListener("click", stop);
document.getElementById("resolve-metadata")!.addEventListener("click", resolveMetadata);
document.getElementById("resolve-later")!.addEventListener("click", () => {
  clearTimeout(metadataTimer);
  metadataPending = true;
  metadataTimer = setTimeout(resolveMetadata, 3000);
  updateControls();
});
document.getElementById("reset-scene")!.addEventListener("click", () => reset());

const renderAnswer = (second = false) => h("article", { class: "fixture-answer conv-inner", "aria-label": second ? "第二个独立回答" : "主要回答" }, [
  h("h3", second ? "第二个独立回答 · S1 属于另一份材料" : sceneNames[state.scene]),
  h("button", { class: "focus-start", type: "button" }, "焦点起点（接着按 Tab）"),
  h(AnswerText, {
    key: `${state.revision}-${second ? "second" : "first"}`,
    source: second ? "绿色风筝属于第二个回答专用的演示材料。[1](#cite:S1)" : state.text,
    sources: second ? secondSources : state.sources,
    streaming: second ? false : state.streaming,
  }),
  h("button", { class: "focus-end", type: "button", style: "margin-top:16px" }, "焦点终点 / 卡片外部区域"),
  h("section", { class: "fixture-records", "aria-label": "虚构执行记录" }, [
    h("h3", "虚构执行记录（仅供定位，不执行命令）"),
    ...(second ? secondSources : sources).map((src) => h("div", { class: "call", "data-cite": src.label }, `${src.label} · ${src.title}\n${src.content ?? src.excerpt}`)),
  ]),
]);
const lazyExchange = buildExchanges(buildTimeline([
  { id: "fixture-lazy-user", role: "user", content: [{ type: "text", text: "虚构执行记录定位测试" }] },
  { id: "fixture-lazy-tools", role: "assistant", content: [
    { type: "tool_use", id: "fixture-pdf", name: "bash", input: { command: "fixture-pdf-text (does not execute)" } },
    { type: "tool_use", id: "fixture-config", name: "read", input: { path: "fixtures/demo-config.json" } },
  ] },
  { id: "fixture-lazy-answer", role: "assistant", content: [{ type: "text", text: sceneText.lazy }] },
], {
  "fixture-pdf": { call_id: "fixture-pdf", text: pdfOutput, source: sources[0], details: { command: "fixture-pdf-text (does not execute)", exit_code: 0, duration_ms: 10 } },
  "fixture-config": { call_id: "fixture-config", text: sources[1].content!, source: sources[1] },
}, emptyLive()), false)[0];
const renderLazyLogs = () => ["conv-inner", "subsheet"].map((scope, index) => h("article", {
  class: `fixture-answer ${scope}`, "data-lazy-pane": String(index), key: `lazy-pane-${index}`,
}, [
  h("h3", index ? "独立子面板：相同问答和工具 ID，应保持折叠" : "主回答：执行记录尚未挂载，引用仍应可定位"),
  h(WorkLog, { key: `lazy-${state.revision}-${index}`, exchange: lazyExchange, runActive: false }),
]));
const app = createApp({ render: () => h("section", { class: "fixture-viewport", "aria-label": "可滚动回答区域", tabindex: -1 },
  state.scene === "lazy" ? renderLazyLogs() : [renderAnswer(), ...(state.scene === "two" ? [renderAnswer(true)] : [])]) });
app.use(i18n).mount("#fixture-app");
updateControls();

// Explicit developer-only DOM regression harness. Events go through the real
// mounted component's public DOM, never through its internal state or methods.
const pause = (ms: number) => new Promise<void>((done) => setTimeout(done, ms));
const flush = async () => { await nextTick(); await pause(35); };
const cardNodes = () => [...document.querySelectorAll<HTMLElement>(".cite-card")];
const anchors = () => [...document.querySelectorAll<HTMLAnchorElement>("#fixture-app a[data-cite-anchor]")];
const assert = (condition: unknown, message: string) => { if (!condition) throw new Error(message); };
const mouse = (target: Element, type: string, relatedTarget: EventTarget | null = document.body) =>
  target.dispatchEvent(new MouseEvent(type, { bubbles: !["mouseenter", "mouseleave"].includes(type), relatedTarget }));
async function prepare(scene: Scene) {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  reset(scene);
  await flush();
  document.querySelectorAll<HTMLElement>(".fixture-records").forEach((record) => { record.style.display = ""; });
  const a = anchors()[0];
  assert(a, "没有找到实际渲染的引用标签");
  a.scrollIntoView({ block: "center", behavior: "auto" });
  await pause(20);
  return a;
}
async function hover(a: HTMLAnchorElement) { mouse(a, "mouseover"); await pause(290); await nextTick(); }

document.getElementById("run-interaction-check")!.addEventListener("click", async () => {
  const output = document.getElementById("interaction-check-results")!;
  const controls = [...document.querySelectorAll<HTMLButtonElement>(".fixture-controls button")];
  controls.forEach((button) => { button.disabled = true; });
  output.replaceChildren();
  const hoverAvailable = matchMedia("(hover: hover) and (pointer: fine)").matches;
  const started = performance.now();
  let passed = 0;
  let failed = 0;
  let skipped = 0;
  const run = async (name: string, check: () => Promise<void>, requiresHover = false) => {
    const row = document.createElement("li");
    row.textContent = `运行中：${name}`;
    output.appendChild(row);
    if (requiresHover && !hoverAvailable) {
      skipped++;
      row.textContent = `SKIP：${name}（当前设备没有 hover/fine pointer 能力）`;
      return;
    }
    try {
      await check();
      passed++;
      row.textContent = `PASS：${name}`;
      row.style.color = "#246d48";
    } catch (error) {
      failed++;
      row.textContent = `FAIL：${name} — ${error instanceof Error ? error.message : String(error)}`;
      row.style.color = "#b3392d";
    }
  };
  try {
    await run("短悬浮后离开，不留下卡片", async () => {
      const a = await prepare("focus");
      mouse(a, "mouseover");
      await pause(55);
      mouse(a, "mouseout");
      await pause(365);
      assert(cardNodes().length === 0, `离开后仍有 ${cardNodes().length} 张卡片`);
    }, true);
    await run("持续悬浮打开一张卡片", async () => {
      const a = await prepare("normal");
      await hover(a);
      assert(cardNodes().length === 1, `持续悬浮后卡片数为 ${cardNodes().length}`);
    }, true);
    await run("从引用移入 Teleport 卡片，卡片保持", async () => {
      const a = await prepare("normal");
      await hover(a);
      const card = cardNodes()[0];
      assert(card, "悬浮卡片未打开");
      mouse(card, "mouseenter", a);
      mouse(a, "mouseout", card);
      await pause(365);
      assert(cardNodes().length === 1 && cardNodes()[0] === card, "移动到卡片后，原卡片消失或被替换");
    }, true);
    await run("离开卡片及移出键盘焦点后关闭", async () => {
      const a = await prepare("focus");
      await hover(a);
      const card = cardNodes()[0];
      assert(card, "悬浮卡片未打开");
      mouse(card, "mouseenter", a);
      mouse(a, "mouseout", card);
      mouse(card, "mouseleave");
      await pause(365);
      assert(cardNodes().length === 0, "离开卡片后仍显示");
      a.focus();
      await flush();
      assert(cardNodes().length === 1, "聚焦引用没有打开卡片");
      document.querySelector<HTMLElement>(".fixture-answer .focus-end")!.focus();
      await pause(365);
      assert(cardNodes().length === 0, "焦点移出后仍显示");
    }, true);
    await run("流式 DOM 替换保留引用焦点和同一张卡片", async () => {
      const oldAnchor = await prepare("focus");
      state.streaming = true;
      await flush();
      oldAnchor.focus();
      await flush();
      const card = cardNodes()[0];
      assert(card, "聚焦引用没有打开卡片");
      state.text += "\n\n新增一个普通演示段落，触发真实 v-html 更新。";
      await flush();
      assert(anchors()[0] !== oldAnchor && !oldAnchor.isConnected, "样例没有触发锚点 DOM 替换");
      assert(document.activeElement === anchors()[0], "焦点未恢复到替换后的引用标签");
      assert(cardNodes().length === 1 && cardNodes()[0] === card, "流式更新关闭或重新创建了卡片");
      mouse(anchors()[0], "mouseout");
      await pause(365);
      assert(cardNodes().length === 1, "光标移开后关闭了仍持有键盘焦点的卡片");
    });
    await run("来源补齐后引用分组合并，保留已选来源", async () => {
      await prepare("pending");
      const pendingAnchor = anchors()[1];
      pendingAnchor.focus();
      await flush();
      const card = cardNodes()[0];
      assert(card?.getAttribute("aria-busy") === "true", "没有显示等待来源状态");
      assert(anchors().length === 3, "等待来源场景应有三个独立标签");
      resolveMetadata();
      await flush();
      assert(anchors().length === 2, "前两个引用未合并");
      assert(cardNodes().length === 1 && cardNodes()[0] === card, "来源补齐关闭或替换了卡片");
      assert(card.getAttribute("aria-busy") === "false", "补齐后仍显示等待状态");
      assert(card.querySelector(".cc-title")?.textContent?.includes("demo-config.json"), "分组合并后没有保留原先选中的 S2 来源");
    });
    await run("追加相邻引用扩大分组，并保留分页选择", async () => {
      const a = await prepare("stream");
      a.focus();
      await flush();
      const card = cardNodes()[0];
      assert(card, "分组卡片未打开");
      const next = card.querySelector<HTMLButtonElement>(".cc-pager button:last-child");
      assert(next && !next.disabled, "找不到可用的下一条来源按钮");
      next!.click();
      await flush();
      assert(card.querySelector(".cc-title")?.textContent?.includes("demo-config.json"), "未切到 S2 来源");
      append();
      await flush();
      assert(cardNodes().length === 1 && cardNodes()[0] === card, "分组扩大关闭或替换了卡片");
      assert(card.querySelector(".cc-title")?.textContent?.includes("demo-config.json"), "追加相邻引用重置了分页");
      assert(card.querySelector(".cc-position")?.textContent?.replace(/\D/g, "") === "23", "分页没有显示第 2 条 / 共 3 条");
    });
    await run("两个独立回答的同名来源互斥且内容正确", async () => {
      await prepare("two");
      // Keep both real AnswerText instances in view without fake scrolling.
      document.querySelectorAll<HTMLElement>(".fixture-records").forEach((record) => { record.style.display = "none"; });
      const [first, second] = anchors();
      first.focus();
      await flush();
      const oldCard = cardNodes()[0];
      assert(oldCard?.textContent?.includes("演示分析报告.pdf"), "第一个回答来源不正确");
      second.focus();
      await flush();
      assert(cardNodes().length === 1 && !oldCard.isConnected, "两个回答同时保留了卡片");
      assert(cardNodes()[0].textContent?.includes("第二个独立回答的演示材料"), "第二个回答错误复用了第一个回答的 S1");
    });
    await run("Escape 关闭卡片并把焦点返回引用", async () => {
      const a = await prepare("normal");
      a.focus();
      await flush();
      const button = cardNodes()[0]?.querySelector<HTMLButtonElement>("button");
      assert(button, "卡片中没有可聚焦按钮");
      button!.focus();
      button!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      await pause(365);
      assert(cardNodes().length === 0, "Escape 后卡片未关闭或立即重开");
      assert(document.activeElement === a, "Escape 后没有返回引用标签");
    });
    await run("懒加载执行记录可展开定位，重新折叠后可重复，且不影响另一面板", async () => {
      const a = await prepare("lazy");
      const scope = document.querySelector<HTMLElement>('[data-lazy-pane="0"]')!;
      const other = document.querySelector<HTMLElement>('[data-lazy-pane="1"]')!;
      assert(scope.querySelectorAll(".call").length === 0, "初始记录不应已挂载");
      assert(canJumpToCite("S1", scope), "折叠行的引用未被识别为可定位");
      assert(!canJumpToCite("S99", scope), "不存在的引用被识别为可定位");
      a.focus();
      await flush();
      const action = [...(cardNodes()[0]?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
        .find((button) => button.textContent?.includes("完整记录"));
      assert(action, "折叠记录的卡片没有查看完整记录按钮");
      action!.click();
      await pause(500);
      const log = scope.querySelector<HTMLElement>(".worklog")!;
      const row = scope.querySelector<HTMLElement>('[data-rid][data-cite-labels~="S1"]')!;
      assert(log.classList.contains("open"), "工作记录没有展开");
      assert(row.querySelector(".rhead")?.getAttribute("aria-expanded") === "true", "目标工具行没有展开");
      assert(row.querySelector('.call[data-cite="S1"] .body')?.textContent?.includes("192.0.2.42:443"), "原始工具结果没有挂载并显示");
      assert(row.querySelector('.call[data-cite="S1"]')?.classList.contains("cite-focused"), "没有定位到实际工具记录");
      assert(!other.querySelector(".worklog")?.classList.contains("open") && !other.querySelector(".call"), "同名 ID 导致另一个面板被展开");
      row.querySelector<HTMLButtonElement>(".rhead")!.click();
      log.querySelector<HTMLButtonElement>(".head")!.click();
      await flush();
      assert(!row.querySelector(".call"), "重新折叠后工具记录仍已挂载");
      assert(jumpToCite("S1", scope), "重复定位被拒绝");
      await pause(500);
      assert(row.querySelector('.call[data-cite="S1"] .body'), "重新折叠后无法再次定位");
      assert(jumpToCite("S2", scope), "文件读取行无法定位");
      await flush();
      assert(scope.querySelector('.call[data-cite="S2"] .body')?.textContent?.includes("timeout_seconds"), "读取工具正文没有挂载");
    });
  } finally {
    stop();
    controls.forEach((button) => { button.disabled = false; });
    updateControls();
    const summary = document.createElement("li");
    summary.textContent = `自检结束：${passed} PASS，${failed} FAIL，${skipped} SKIP；耗时 ${((performance.now() - started) / 1000).toFixed(1)} 秒。仅使用本页 DOM 与虚构数据。`;
    output.appendChild(summary);
    status.textContent = summary.textContent;
    output.scrollIntoView({ block: "center", behavior: "auto" });
  }
});

// Read-only diagnostics stay outside the Vue answer tree and never move focus.
const diagnostic = document.getElementById("fixture-diagnostics")!;
const diagnosticTimer = setInterval(() => {
  const focused = document.activeElement;
  const focusLabel = focused?.getAttribute("aria-label") || focused?.textContent?.trim().slice(0, 52) || focused?.tagName || "无";
  const cards = document.querySelectorAll(".cite-card");
  const text = `可见卡片：${cards.length} | 焦点：${focusLabel} | 正文字符：${state.text.length}`;
  if (diagnostic.textContent !== text) diagnostic.textContent = text;
}, 250);
window.addEventListener("pagehide", () => {
  clearInterval(streamTimer);
  clearTimeout(metadataTimer);
  clearInterval(diagnosticTimer);
  app.unmount();
}, { once: true });
