<script setup lang="ts">
import MarkdownView from "./MarkdownView.vue";
import type { CiteSource } from "@/api/types";

/**
 * The assistant's prose answer, with the reading rules every answer shares.
 *
 * This used to live as scoped styles on TurnCard, until the work log needed
 * the identical block for the conclusion it keeps outside the fold — one
 * answer style, whether or not the work above it is folded. Markdown itself
 * stays in MarkdownView; this is only the typography around it.
 */
defineProps<{
  source: string;
  streaming?: boolean;
  /** See MarkdownView's prop of the same name; threaded straight through. */
  sources?: CiteSource[];
  /** Formatting for saved materials, without active Markdown integrations. */
  passive?: boolean;
}>();
</script>

<template>
  <MarkdownView class="answer" :source="source" :streaming="streaming ?? false" :sources="sources" :passive="passive" :file-links="!passive" />
</template>

<style scoped lang="scss">
.answer {
  margin-top: 8px;
  font-size: 14px;
  line-height: 1.75;
  color: var(--el-text-color-primary);

  :deep(p) {
    margin: 0.55em 0;
  }

  /* Headings inside an answer are section markers in a paragraph of chat, not page
     titles: they get weight and space above, not size. Left at the framework's
     default they came out larger than the composer's own labels, which made a
     three-heading answer look like a document someone pasted in. */
  :deep(h1),
  :deep(h2),
  :deep(h3),
  :deep(h4) {
    margin: 1.1em 0 0.4em;
    font-size: 1em;
    font-weight: 650;
    line-height: 1.5;
  }

  :deep(h1) {
    font-size: 1.12em;
  }

  :deep(h2) {
    font-size: 1.06em;
  }

  :deep(pre) {
    background: var(--el-fill-color-lighter);
    border: 1px solid var(--el-border-color-extra-light);
    padding: 10px 12px;
    border-radius: 10px;
    overflow-x: auto;
    font: 12px/1.6 var(--pg-mono);
  }

  :deep(code) {
    font-family: var(--pg-mono);
    font-size: 0.9em;
  }

  /* Inline code only: a tinted tile inside a code block would draw a second box
     around every line. */
  :deep(:not(pre) > code) {
    padding: 1px 5px;
    border-radius: 5px;
    background: var(--el-fill-color-light);
    color: var(--el-color-primary-dark-2);
  }

  :deep(a) {
    color: var(--el-color-primary-dark-2);
    text-decoration-color: var(--pg-accent-line);
    text-underline-offset: 2px;
  }

  /* A source reads as part of the sentence: identity first, then its recorded
     position. Only the name may shrink; line ranges and folded counts remain
     visible even in a narrow answer. */
  :deep(.cite) {
    display: inline-flex;
    align-items: center;
    max-width: min(100%, 30em);
    margin: 0 0.16em;
    border-radius: 3px;
    font-size: 0.96em;
    line-height: 1.55;
    vertical-align: -0.2em;
    white-space: nowrap;
    cursor: pointer;
    transition: background var(--pg-transition);

    .cite-type-icon {
      width: 1em;
      height: 1em;
      flex: 0 0 auto;
    }

    a {
      position: relative;
      display: inline-flex;
      align-items: center;
      gap: 0.2em;
      max-width: 100%;
      padding: 0 0.12em;
      color: var(--pg-link-color, var(--pg-cite-fg));
      text-decoration: none;
      min-width: 0;
      font-variant-numeric: tabular-nums;
      font-weight: 500;

      /* Add vertical hit area without covering adjacent prose or links. */
      &::after {
        content: "";
        position: absolute;
        inset: -3px 0;
      }
    }

    .cite-label {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .cite-location,
    .cite-more {
      flex: 0 0 auto;
      font-weight: 400;
    }

    .cite-location {
      margin-left: -0.2em;
    }

    .cite-more {
      margin-left: 0.15em;
      color: var(--el-text-color-regular);
    }

    &:hover {
      background: var(--el-fill-color-light);
      .cite-label {
        text-decoration: underline;
        text-underline-offset: 2px;
      }
    }

    &:has(a[aria-expanded="true"]) {
      background: var(--pg-cite-bg-hover);
    }

    &:has(a:focus-visible) {
      outline: 2px solid var(--pg-link-color, var(--el-color-primary));
      outline-offset: 2px;
    }

    &.cite-unavailable {
      min-width: 1.2em;
      justify-content: center;
      background: var(--el-fill-color-light);

      a {
        color: var(--el-text-color-regular);
        padding: 0 0.25em;
      }
    }

    @media (max-width: 600px) {
      max-width: min(100%, 22em);
    }
  }

  :deep(blockquote) {
    margin: 0.6em 0;
    padding: 2px 0 2px 12px;
    border-left: 2px solid var(--el-border-color-light);
    color: var(--el-text-color-regular);
  }

  :deep(hr) {
    margin: 1.2em 0;
    border: 0;
    border-top: 1px solid var(--el-border-color-extra-light);
  }

  :deep(ul),
  :deep(ol) {
    padding-left: 1.4em;
    margin: 0.5em 0;
  }

  :deep(li) {
    margin: 0.2em 0;
  }

  /* markdown-it renders GFM tables but the UA stylesheet draws no borders on
     them, so without these rules a table reads as plain aligned text. */
  :deep(table) {
    border-collapse: collapse;
    margin: 0.5em 0;
    font-size: 13px;
    /* GitHub's pattern: let a wide table scroll instead of blowing out the
       column, without stretching narrow ones. */
    display: block;
    width: max-content;
    max-width: 100%;
    overflow-x: auto;
  }

  :deep(th),
  :deep(td) {
    border: 1px solid var(--el-border-color-light);
    padding: 5px 11px;
    text-align: left;
  }

  :deep(th) {
    background: var(--el-fill-color-lighter);
    font-weight: 600;
  }
}
</style>
