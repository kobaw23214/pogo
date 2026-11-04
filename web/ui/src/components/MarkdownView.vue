<script setup lang="ts">
import { computed, defineAsyncComponent, inject, ref } from "vue";
import MarkdownIt from "markdown-it";
import { ElImageViewer } from "element-plus";
import "katex/dist/katex.min.css";
import CiteCard from "./CiteCard.vue";
import CodeBlock from "./CodeBlock.vue";
import EChartsDiagram from "./EChartsDiagram.vue";
import MermaidDiagram from "./MermaidDiagram.vue";
import { installMarkdownExtensions } from "./markdownExtensions";
import { installMarkdownMath } from "./markdownMath";

const DataTable = defineAsyncComponent(() => import("./DataTable.vue"));
const MathBlock = defineAsyncComponent(() => import("./MathBlock.vue"));
const HTMLArtifact = defineAsyncComponent(() => import("./HTMLArtifact.vue"));
import { canonicalCiteLabel, planCitations, type CitePlanItem } from "@/agent/cite";
import { citeIconSvg } from "@/agent/citeIcons";
import { citePresentation } from "@/agent/citePresentation";
import { useCitePopover } from "@/agent/useCitePopover";
import { useInlineCiteFavicons } from "@/agent/useInlineCiteFavicons";
import { installMarkdownFileLinks, workspaceFileRenderScope, workspaceFileTargetFromButtonData, workspaceMarkdownImageURL } from "@/agent/markdownFileLinks";
import { workspaceFileNavigationKey } from "@/agent/workspaceFileNavigation";
import { gt } from "@/i18n";
import type { CiteSource } from "@/api/types";

// A fenced block is mounted as a component rather than left to markdown-it's
// <pre><code>: that is what gives an answer's code the same syntax colouring,
// language label and copy button as the file viewer, with the text still
// rendered as text nodes (never v-html).
//
// The source is therefore split into segments — runs of markdown rendered with
// v-html, and top-level fences rendered as components — instead of rendering one
// v-html blob and teleporting components into placeholder slots. A Teleport
// resolves its target once and only re-resolves when `to` changes, so with a
// single v-html blob every source update (i.e. every streamed token batch)
// replaced the placeholder divs and orphaned the already-mounted blocks: the new
// slots stayed empty and the diagram was never visible in the document.
interface Segment {
  id: string;
  kind: "html" | "mermaid" | "echarts" | "math" | "table" | "html-preview" | "code";
  // Set for kind "html"; empty otherwise.
  html: string;
  // Set for the fence kinds; empty otherwise.
  source: string;
  lang: string;
}

type Token = ReturnType<InstanceType<typeof MarkdownIt>["parse"]>[number];

const props = defineProps<{
  source: string;
  streaming?: boolean;
  /**
   * The citation stamps this text's `[1](#cite:S3)` anchors resolve to.
   * Legacy contexts may lack metadata; their citations still open a clear
   * unavailable state instead of silently ignoring hover and click.
   */
  sources?: CiteSource[];
  /** Enable the containing conversation's file preview for answer prose. */
  fileLinks?: boolean;
  /** Saved material preview: formatting only, without diagrams, remote images,
   * citation interactions or conversation-file actions. Defaults to false. */
  passive?: boolean;
  /** Skill package preview only: emit internal document links instead of
   * resolving them against the application's current browser URL. */
  documentLinks?: boolean;
}>();
const emit = defineEmits<{ documentLink: [href: string] }>();

const md = new MarkdownIt({ html: false, linkify: true, breaks: true });
installMarkdownMath(md);
installMarkdownExtensions(md, {
  toc: () => gt("markdownRich.toc"),
  details: () => gt("markdownRich.details"),
  completed: () => gt("markdownRich.completed"),
  uncompleted: () => gt("markdownRich.uncompleted"),
  backToText: () => gt("markdownRich.backToText"),
  headingLink: () => gt("markdownRich.headingLink"),
  footnote: () => gt("markdownRich.footnote"),
});

// ── Inline citations ────────────────────────────────────────────────────────
// `[1](#cite:S3)` is an ordinary markdown link, so three renderer rules turn
// it into a readable source link — no text preprocessing, which is
// exactly why an anchor inside a code span stays literal: no link token is
// ever produced there. Source names and recorded locations are visible inline;
// display numbers remain in the accessible label, card and copy path. Adjacent
// anchors fold into one link with a "+M" tail using the shared citation plan.
interface CiteEnv {
  [key: string | symbol]: unknown;
  citeProvided: boolean;
  citeSources: Map<string, CiteSource>;
  citeNums: Map<string, number>;
  /** The anchors' plan for this render; link_open consumes it in order. */
  citePlan: CitePlanItem[];
  citeCursor: number;
  citeAnchorIndex: number;
  // One entry per open link: the kind of chip it became, or null for an
  // ordinary link. citePending swallows the model's literal "1" between
  // link_open and link_close — for folded members too, which emit no markup
  // but still own that text token.
  citeStack: ("known" | "unknown" | "member" | "document" | null)[];
  citePending: boolean;
}

const esc = md.utils.escapeHtml;
const defaultText = md.renderer.rules.text;
const defaultImage = md.renderer.rules.image;
md.renderer.rules.image = (tokens, idx, options, env, self) => {
  if (props.passive) return `<span class="markdown-image-alt">${esc(tokens[idx].content)}</span>`;
  const source = String(tokens[idx].attrGet("src") ?? "");
  const workspaceSource = workspaceMarkdownImageURL(source, fileScope());
  if (workspaceSource) tokens[idx].attrSet("src", workspaceSource);
  tokens[idx].attrSet("loading", "lazy");
  tokens[idx].attrSet("decoding", "async");
  tokens[idx].attrJoin("class", "markdown-image");
  tokens[idx].attrSet("data-markdown-image-preview", "true");
  tokens[idx].attrSet("tabindex", "0");
  return defaultImage ? defaultImage(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options);
};

md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const e = env as CiteEnv | undefined;
  const href = String(tokens[idx].attrGet("href") ?? "");
  if (props.documentLinks) {
    if (!/^https?:\/\//i.test(href)) {
      e?.citeStack?.push("document");
      return `<button type="button" class="markdown-document-link" data-document-href="${esc(href)}">`;
    }
    e?.citeStack?.push(null);
    tokens[idx].attrSet("target", "_blank");
    tokens[idx].attrSet("rel", "noopener noreferrer");
    return self.renderToken(tokens, idx, options);
  }
  if (props.passive) {
    e?.citeStack?.push(null);
    if (!href.startsWith("#")) { tokens[idx].attrSet("target", "_blank"); tokens[idx].attrSet("rel", "noopener noreferrer"); }
    return self.renderToken(tokens, idx, options);
  }
  if (!e?.citePlan || !href.startsWith("#cite:")) {
    e?.citeStack?.push(null);
    return self.renderToken(tokens, idx, options);
  }
  const label = canonicalCiteLabel(href.slice("#cite:".length));
  const anchorIndex = e.citeAnchorIndex++;
  const src = e.citeSources?.get(label);
  e.citePending = true;

  // The k-th cite link consumes the k-th plan entry: plan and parse walk the
  // same anchors in the same order. A disagreement (should never happen)
  // degrades the anchor to a lone chip rather than breaking the row.
  const peek = e.citeCursor < e.citePlan.length ? e.citePlan[e.citeCursor] : undefined;
  const item = peek !== undefined && peek.label === label ? peek : undefined;
  if (item) e.citeCursor += 1;

  // A folded member renders nothing: its group's head pill already counts it
  // as +M, and the hover card paginates to it.
  if (item && !item.head) {
    e.citeStack!.push("member");
    return "";
  }

  let n = item?.num;
  if (n === undefined) {
    n = e.citeNums.get(label);
    if (n === undefined) {
      n = e.citeNums.size + 1;
      e.citeNums.set(label, n);
    }
  }
  const size = item?.size ?? 1;
  // The pill stands for the whole group: the wrapper names every member
  // (space-separated — a label can never contain whitespace) so the hover
  // card can paginate across them.
  const group =
    item && size > 1
      ? ` data-cite-group="${esc(
          e.citePlan
            .filter((p) => p.group === item.group)
            .map((p) => p.label)
            .join(" "),
        )}"`
      : "";
  const presentation = src ? citePresentation(src) : undefined;
  const desc = presentation
    ? presentation.title + (presentation.location ?? "")
    : gt(props.streaming ? "cite.loadingTitle" : "cite.unavailableTitle");
  const aria = gt("cite.refLabel", { n: src ? n : "?", desc });
  const members = item
    ? e.citePlan.flatMap((p, i) => p.group === item.group ? [i] : []).join(" ")
    : String(anchorIndex);
  e.citeStack!.push("known");
  return (
    `<span class="cite${src ? "" : " cite-unavailable"}" data-cite="${esc(label)}"${group}>` +
    `<a href="#cite:${esc(label)}" data-cite-anchor="${anchorIndex}" data-cite-members="${members}" title="${esc(desc)}" aria-label="${esc(aria)}" aria-keyshortcuts="ArrowDown" aria-description="${esc(gt('cite.keyboardPreview'))}" aria-haspopup="dialog" aria-controls="${esc(cardId)}" aria-expanded="false">` +
    (presentation
      ? `<span class="cite-source-icon" data-cite-icon="${esc(label)}" aria-hidden="true">${citeIconSvg(presentation.iconType)}</span>` +
        `<span class="cite-label">${esc(presentation.label)}</span>` +
        (presentation.location ? `<span class="cite-location">${esc(presentation.location)}</span>` : "")
      : `<span class="cite-num">${props.streaming ? "…" : "?"}</span>`) +
    (size > 1 ? `<span class="cite-more">+${size - 1}</span>` : "")
  );
};

md.renderer.rules.text = (tokens, idx, options, env, self) => {
  const e = env as CiteEnv | undefined;
  if (e?.citePending) {
    e.citePending = false;
    return "";
  }
  return defaultText ? defaultText(tokens, idx, options, env, self) : esc(tokens[idx].content);
};

md.renderer.rules.link_close = (tokens, idx, options, env, self) => {
  const e = env as CiteEnv | undefined;
  const kind = e?.citeStack?.pop() ?? null;
  if (e && e.citePending !== undefined) e.citePending = false;
  if (kind === "known") return "</a></span>";
  if (kind === "unknown") return "</span>";
  if (kind === "member") return "";
  if (kind === "document") return "</button>";
  return self.renderToken(tokens, idx, options);
};

const fileNavigation = inject(workspaceFileNavigationKey, null);
const markdownRoot = ref<HTMLElement | null>(null);
const previewImage = ref("");
function fileScope() {
  const element = markdownRoot.value;
  if (props.passive || !props.fileLinks || !element?.closest(".conv-inner") || element.closest(".subsheet, .share-page")) return null;
  return fileNavigation?.scope() ?? null;
}
installMarkdownFileLinks(md, {
  scope: fileScope,
  openLabel: name => gt("workspaceFiles.openLabel", { name }),
  tooltip: path => gt("workspaceFiles.tooltip", { path }),
  cardKind: kind => gt(`workspaceFiles.cardKinds.${kind}`),
  cardAction: gt("workspaceFiles.openCard"),
});

function previewMarkdownImage(target: EventTarget | null): boolean {
  const image = (target as Element | null)?.closest?.("img[data-markdown-image-preview]");
  if (!(image instanceof HTMLImageElement) || image.closest("a, button") || !markdownRoot.value?.contains(image)) return false;
  const source = image.getAttribute("src");
  if (!source) return false;
  previewImage.value = source;
  return true;
}
function onMarkdownImageKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  if (previewMarkdownImage(event.target)) event.preventDefault();
}
function onMarkdownClick(event: MouseEvent) {
  if (previewMarkdownImage(event.target)) {
    event.preventDefault();
    return;
  }
  if (props.documentLinks) {
    const link = (event.target as Element | null)?.closest?.("button[data-document-href]");
    if (link instanceof HTMLButtonElement && markdownRoot.value?.contains(link)) {
      event.preventDefault();
      emit("documentLink", link.dataset.documentHref || "");
      return;
    }
  }
  const button = (event.target as Element | null)?.closest?.("button[data-workspace-file]");
  if (!(button instanceof HTMLButtonElement) || button.disabled || !markdownRoot.value?.contains(button)) return;
  const current = fileScope();
  if (!current || button.dataset.fileConversation !== current.conversationId || button.dataset.fileCwd !== current.cwd) return;
  event.preventDefault();
  const target = workspaceFileTargetFromButtonData(button.dataset, current);
  if (!target) return;
  const opened = fileNavigation?.open(target, { conversationId: current.conversationId, cwd: current.cwd });
  if (opened) {
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    button.classList.add("is-resolving");
    const settled = () => {
      button.disabled = false;
      button.removeAttribute("aria-busy");
      button.classList.remove("is-resolving");
    };
    void opened.then(settled, settled);
  }
}

// The plan is computed once per source version and shared two ways: the
// renderer above consumes it to fold adjacent citations into one pill, and
// the interaction layer below reads it to number a group member's card page.
const citePlan = computed(() =>
  props.passive ? [] : planCitations(props.source, props.sources === undefined ? null : new Set(props.sources.map((s) => s.label))),
);
const citeNums = computed(() => {
  const m = new Map<string, number>();
  for (const item of citePlan.value) {
    if (item.num !== undefined && !m.has(item.label)) m.set(item.label, item.num);
  }
  return m;
});

const segments = computed<Segment[]>(() => {
  // Fresh per render, shared by every segment's render call below: an answer
  // is append-only while streaming, so a chip's number is stable across the
  // batches that add text after it.
  const environment: CiteEnv = {
    citeProvided: props.sources !== undefined,
    citeSources: new Map((props.sources ?? []).map((s) => [s.label, s])),
    citeNums: new Map(citeNums.value),
    citePlan: citePlan.value,
    citeCursor: 0,
    citeAnchorIndex: 0,
    citeStack: [],
    citePending: false,
    [workspaceFileRenderScope]: fileScope(),
  };
  const tokens = md.parse(props.source, environment);
  const out: Segment[] = [];
  let pending: Token[] = [];

  // Ids are positional so a growing answer keeps the same component instances:
  // appending text does not renumber the segments that precede it, which is what
  // lets a rendered diagram survive the next streamed batch.
  const flush = () => {
    if (pending.length === 0) return;
    out.push({
      id: `s${out.length}`,
      kind: "html",
      html: md.renderer.render(pending, md.options, environment),
      source: "",
      lang: "",
    });
    pending = [];
  };

  for (const token of tokens) {
    // Nested fences (inside a list item or quote) keep markdown-it's own
    // <pre><code>: pulling them out of the segment would drop their container.
    if (token.type !== "fence" || token.level !== 0) {
      pending.push(token);
      continue;
    }
    flush();
    const lang = token.info.trim().split(/\s+/, 1)[0]?.toLowerCase() ?? "";
    let kind: Segment["kind"] = "code";
    if (!props.passive) {
      if (lang === "mermaid") kind = "mermaid";
      else if (lang === "echarts") kind = "echarts";
      else if (lang === "math") kind = "math";
      else if (lang === "csv" || lang === "table" || lang === "json-table") kind = "table";
      else if (lang === "html-preview") kind = "html-preview";
    }
    out.push({
      id: `s${out.length}`,
      kind,
      html: "",
      source: token.content.replace(/\n$/, ""),
      lang,
    });
  }
  flush();
  return out;
});

// The popover owns transient interaction state; Markdown updates only replace
// its anchor, which is re-resolved by stable occurrence after each render.
const {
  bindRoot, card, cardId, dismissCard, cancelHide, scheduleHide, onCardBlur,
  onLocate, canLocate, canOpenFile, onOpenFile, positionCard,
} = useCitePopover(props, citeNums);
function bindMarkdownRoot(element: unknown) {
  bindRoot(props.passive ? null : element);
  markdownRoot.value = element instanceof HTMLElement ? element : null;
}
useInlineCiteFavicons(markdownRoot, () => props.passive ? undefined : props.sources, segments);
</script>

<template>
  <div :ref="bindMarkdownRoot" class="markdown-view" @click="onMarkdownClick" @keydown="onMarkdownImageKeydown">
    <template v-for="segment in segments" :key="segment.id">
      <!-- html:false escapes source HTML; fences never reach this branch. -->
      <div v-if="segment.kind === 'html'" class="markdown-content" v-html="segment.html" />

      <!-- A mermaid or echarts fence is almost certainly incomplete while the
           answer is still streaming, and both components re-render
           asynchronously on every source change — so mid-stream they would
           thrash between "rendering…" and a parse error. Defer the diagram
           until the stream settles; show the source as code in the meantime. -->
      <CodeBlock
        v-else-if="segment.kind === 'mermaid' && streaming"
        class="block"
        :code="segment.source"
        lang="mermaid"
        :collapsed-height="420"
      />
      <MermaidDiagram v-else-if="segment.kind === 'mermaid'" class="block" :source="segment.source" />
      <CodeBlock
        v-else-if="segment.kind === 'echarts' && streaming"
        class="block"
        :code="segment.source"
        lang="json"
        :collapsed-height="420"
      />
      <EChartsDiagram v-else-if="segment.kind === 'echarts'" class="block" :source="segment.source" />
      <CodeBlock
        v-else-if="segment.kind === 'math' && streaming"
        class="block"
        :code="segment.source"
        lang="latex"
        :collapsed-height="420"
      />
      <MathBlock v-else-if="segment.kind === 'math'" class="block" :source="segment.source" />
      <CodeBlock
        v-else-if="segment.kind === 'table' && streaming"
        class="block"
        :code="segment.source"
        :lang="segment.lang === 'csv' ? 'csv' : 'json'"
        :collapsed-height="420"
      />
      <DataTable
        v-else-if="segment.kind === 'table'"
        class="block"
        :source="segment.source"
        :format="segment.lang === 'csv' ? 'csv' : 'json'"
      />
      <CodeBlock
        v-else-if="segment.kind === 'html-preview' && streaming"
        class="block"
        :code="segment.source"
        lang="json"
        :collapsed-height="420"
      />
      <HTMLArtifact
        v-else-if="segment.kind === 'html-preview'"
        class="block"
        :source="segment.source"
        :scope="fileScope()"
      />
      <CodeBlock
        v-else
        class="block"
        :code="segment.source"
        :lang="segment.lang"
        :line-numbers="!passive"
        :uncollapsed="passive"
        :collapsed-height="420"
        :expanded-height="4000"
      />
    </template>

    <CiteCard
      v-if="!passive && card"
      :id="cardId"
      :items="card.items"
      :left="card.left"
      :top="card.top"
      :above="card.above"
      :pinned="card.pinned"
      :can-open-file="canOpenFile"
      :can-locate="canLocate"
      @enter="cancelHide"
      @leave="scheduleHide"
      @blur="onCardBlur"
      @close="dismissCard"
      @locate="onLocate"
      @open-file="onOpenFile"
      @resize="positionCard"
    />
    <ElImageViewer
      v-if="previewImage"
      :url-list="[previewImage]"
      :infinite="false"
      hide-on-click-modal
      teleported
      @close="previewImage = ''"
    />
  </div>
</template>

<style scoped lang="scss">
:deep(.markdown-document-link) {
  display: inline;
  padding: 0;
  border: 0;
  background: none;
  color: var(--pg-link-color, var(--el-color-primary-dark-2));
  font: inherit;
  text-align: inherit;
  text-decoration: underline;
  text-underline-offset: 2px;
  cursor: pointer;
  &:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
}
.markdown-view > .block {
  min-width: 0;
  margin: 0.6em 0;
}

:deep(.workspace-file-link) {
  display: inline-flex;
  align-items: center;
  gap: 0.32em;
  max-width: 100%;
  padding: 0.12em 0.38em;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
  color: var(--pg-link-color, var(--el-color-primary-dark-2));
  font: inherit;
  line-height: 1.45;
  vertical-align: baseline;
  cursor: pointer;
  text-align: left;

  &:hover {
    background: var(--el-fill-color-light);
    .workspace-file-name { text-decoration: underline; text-underline-offset: 2px; }
  }
  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }
  &.is-resolving {
    cursor: progress;
    .workspace-file-icon { animation: workspace-file-pulse 0.85s ease-in-out infinite alternate; }
  }
}
@keyframes workspace-file-pulse { from { opacity: 0.35; } to { opacity: 1; } }
@media (prefers-reduced-motion: reduce) {
  :deep(.workspace-file-link.is-resolving .workspace-file-icon) { animation: none; opacity: 0.55; }
}
:deep(.workspace-file-icon) { width: 1em; height: 1em; flex: 0 0 auto; }
:deep(.workspace-file-name) { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
:deep(.workspace-file-card) {
  display: flex;
  align-items: center;
  gap: 12px;
  width: min(100%, 520px);
  margin: 0.75em 0;
  padding: 13px 14px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease, background 0.15s ease, box-shadow 0.15s ease;
}
:deep(.workspace-file-card:hover) {
  border-color: var(--el-color-primary);
  background: var(--pg-accent-wash);
  box-shadow: var(--el-box-shadow-lighter);
}
:deep(.workspace-file-card:focus-visible) { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
:deep(.workspace-file-card.is-resolving) { cursor: progress; opacity: 0.7; }
:deep(.workspace-file-card > .workspace-file-icon) { width: 28px; height: 28px; flex: 0 0 auto; color: var(--el-color-primary); }
:deep(.workspace-file-card-text) { display: flex; flex: 1; flex-direction: column; gap: 3px; min-width: 0; }
:deep(.workspace-file-card-text strong) { font-size: 13px; font-weight: 600; line-height: 1.45; overflow-wrap: anywhere; }
:deep(.workspace-file-card-kind) { color: var(--el-text-color-secondary); font-size: 11px; }
:deep(.workspace-file-card-action) { flex: 0 0 auto; margin-left: auto; color: var(--el-color-primary); font-size: 12px; white-space: nowrap; }

:deep(.markdown-content) {
  .markdown-image {
    max-width: 100%;
    max-height: 560px;
    object-fit: contain;
    border-radius: 8px;
    cursor: zoom-in;
    &:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
  }
  h1, h2, h3, h4, h5, h6 { scroll-margin-top: 24px; }
  .heading-anchor {
    margin-left: 0.45em;
    color: var(--el-color-primary);
    font-size: 0.72em;
    font-weight: 400;
    opacity: 0;
    text-decoration: none;
    transition: opacity 0.15s ease;
  }
  h1:hover .heading-anchor, h2:hover .heading-anchor, h3:hover .heading-anchor,
  h4:hover .heading-anchor, h5:hover .heading-anchor, h6:hover .heading-anchor,
  .heading-anchor:focus-visible { opacity: 1; }
  .markdown-toc {
    margin: 1em 0;
    padding: 14px 18px;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 10px;
    background: var(--el-fill-color-lighter);
  }
  .markdown-toc > strong { display: block; margin-bottom: 7px; font-size: 12px; }
  .markdown-toc ol { margin: 0; padding: 0; list-style: none; }
  .markdown-toc li { margin: 4px 0; line-height: 1.55; }
  .markdown-toc a { color: var(--el-text-color-regular); text-decoration: none; }
  .markdown-toc a:hover { color: var(--el-color-primary); text-decoration: underline; text-underline-offset: 3px; }
  .markdown-toc .toc-level-3 { padding-left: 1.25em; }
  .markdown-toc .toc-level-4 { padding-left: 2.5em; font-size: 0.92em; }
  .task-list-item { list-style: none; }
  .task-list-checkbox {
    width: 1em;
    height: 1em;
    margin: 0 0.45em 0 0;
    accent-color: var(--el-color-primary);
    vertical-align: -0.12em;
  }
  .task-list-item:has(.task-list-checkbox:checked) > p { color: var(--el-text-color-secondary); }
  .callout {
    margin: 1em 0;
    padding: 12px 16px;
    border: 1px solid var(--el-border-color-lighter);
    border-left: 4px solid var(--el-color-primary);
    border-radius: 8px;
    background: var(--el-fill-color-lighter);
  }
  .callout > p:first-child { margin-top: 0; }
  .callout > p:last-child { margin-bottom: 0; }
  .callout-title {
    display: block;
    margin-bottom: 5px;
    color: var(--el-color-primary);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.06em;
  }
  .callout-tip { border-left-color: var(--el-color-success); }
  .callout-tip .callout-title { color: var(--el-color-success); }
  .callout-important { border-left-color: var(--el-color-primary); }
  .callout-warning { border-left-color: var(--el-color-warning); }
  .callout-warning .callout-title { color: var(--el-color-warning); }
  .callout-caution { border-left-color: var(--el-color-danger); }
  .callout-caution .callout-title { color: var(--el-color-danger); }
  .markdown-details {
    margin: 1em 0;
    overflow: hidden;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 8px;
    background: var(--el-bg-color);
  }
  .markdown-details > summary {
    padding: 10px 14px;
    background: var(--el-fill-color-lighter);
    color: var(--el-text-color-regular);
    cursor: pointer;
    font-weight: 550;
    user-select: none;
  }
  .markdown-details > summary:hover { color: var(--el-color-primary); }
  .markdown-details > summary:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: -2px; }
  .markdown-details[open] > summary { border-bottom: 1px solid var(--el-border-color-lighter); }
  .markdown-details > :not(summary) { margin-left: 14px; margin-right: 14px; }
  .markdown-details > :last-child { margin-bottom: 14px; }
  .footnote-ref { margin-left: 0.12em; font-size: 0.78em; }
  .footnote-ref a { color: var(--el-color-primary); text-decoration: none; }
  .footnotes { margin-top: 2em; color: var(--el-text-color-secondary); font-size: 12px; }
  .footnotes hr { border: 0; border-top: 1px solid var(--el-border-color-lighter); }
  .footnotes ol { padding-left: 1.8em; }
  .footnotes li { margin: 6px 0; padding-left: 3px; overflow-wrap: anywhere; }
  .footnotes li:target { border-radius: 5px; background: var(--pg-accent-wash); }
  .footnote-backref { margin-left: 5px; color: var(--el-color-primary); text-decoration: none; }
  .math-block { overflow-x: auto; padding: 10px 4px; }
  .math-block-error { color: var(--el-color-warning); white-space: pre-wrap; }
  .math-inline-error { color: var(--el-color-warning); }
}
</style>
