<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import Chevron from "./Chevron.vue";
import ElementPageIcon from "./ElementPageIcon.vue";
import type { ElementCardData } from "@/agent/elementPicker";
import { safeSourceURL } from "@/agent/citePreview";

// The sent-message half of the picker: the raw <page-element> block travels to
// the model untouched, and the bubble shows this card instead — the same
// envelope→card split the task notification uses. Collapsed it is one line of
// identity (which page, which element); expanded it carries the selector, the
// page link, and a live rendering of the pruned markup.

const props = defineProps<{ card: ElementCardData }>();
const { t } = useI18n();
const pageURL = computed(() => safeSourceURL({ locator: props.card.url }));
const authoredText = computed(() => {
  try { const value = JSON.parse(props.card.data); return value?.kind === "user-authored-web-reference" && typeof value.excerpt === "string" && value.excerpt === props.card.text; }
  catch { return false; }
});

const open = ref(false);
const height = ref(0);
const frameEl = ref<HTMLIFrameElement | null>(null);

// The preview frame. sandbox="allow-scripts" gives the content an opaque
// origin: the one script that runs is our own height reporter below, and
// nothing in the markup can reach the parent (no allow-same-origin — the
// combination that would break the sandbox open). The markup itself is safe
// HTML by construction: whitelisted attributes, escaped text, and it is
// embedded verbatim — never unescaped, which would be the actual bug.
const PREVIEW_BASE = `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;padding:8px;font:14px/1.6 system-ui,-apple-system,sans-serif;color:#303133;word-break:break-word}
img{max-width:100%}a{color:#409eff}
</style></head><body>`;
const PREVIEW_REPORT = `<script>
(function(){
  var post=function(){parent.postMessage({type:"pg-el-card-h",h:document.documentElement.scrollHeight},"*")};
  new ResizeObserver(post).observe(document.documentElement);
  post();
})();
<\/script></body></html>`;

// A computed, not an inline template expression: regenerating the string each
// render would repatch srcdoc and reload the frame for nothing.
const srcdoc = computed(() => PREVIEW_BASE + props.card.html + PREVIEW_REPORT);

// The page-volunteered data arrives as one line; people read it indented, and a
// parse failure falls back to the raw string rather than hiding the exhibit.
const prettyData = computed(() => {
  try {
    return JSON.stringify(JSON.parse(props.card.data), null, 2);
  } catch {
    return props.card.data;
  }
});

function onHeightMessage(e: MessageEvent) {
  // Same discipline as the panel bridge: origin is unknowable (opaque), so the
  // sender must be this card's own frame, and the payload is one number.
  const d = e.data as { type?: string; h?: number } | null;
  if (d?.type !== "pg-el-card-h" || !frameEl.value || e.source !== frameEl.value.contentWindow) {
    return;
  }
  if (typeof d.h === "number" && d.h > 0) height.value = Math.min(d.h, 320);
}

onMounted(() => window.addEventListener("message", onHeightMessage));
onBeforeUnmount(() => window.removeEventListener("message", onHeightMessage));

function openPage() {
  if (pageURL.value) window.open(pageURL.value, "_blank", "noopener");
}
</script>

<template>
  <div class="elcard" :class="{ open }">
    <button class="elcard-head" :aria-expanded="open" @click="open = !open">
      <ElementPageIcon :url="card.url" :icon="card.favicon" />
      <span class="page">{{ card.title || card.url }}</span>
      <span class="sep">·</span>
      <span class="what">{{ card.element || card.selector }}</span>
      <span v-if="card.position" class="pos">{{ card.position }}</span>
      <Chevron :open="open" />
    </button>
    <div v-if="open" class="elcard-body">
      <div class="row">
        <span class="k">selector</span>
        <span class="v mono">{{ card.selector }}</span>
      </div>
      <div v-if="card.text" class="row">
        <span class="k">text</span>
        <span class="v" :class="{ 'authored-quote': authoredText }">{{ card.text }}</span>
      </div>
      <div class="row">
        <span class="k">page</span>
        <a v-if="pageURL" class="v link" :href="pageURL" target="_blank" rel="noopener" @click.prevent="openPage">
          {{ card.url }} ↗
        </a>
        <span v-else class="v">{{ card.url }}</span>
      </div>
      <div v-if="card.data" class="row">
        <span class="k">data</span>
        <pre class="v mono data">{{ prettyData }}</pre>
      </div>
      <div v-if="card.html" class="preview" :title="t('agentView.elementCard.preview')">
        <!-- Height arrives from the frame's own reporter; until then it stays
             collapsed to a sliver rather than flashing tall. -->
        <iframe
          ref="frameEl"
          class="preview-frame"
          sandbox="allow-scripts"
          :srcdoc="srcdoc"
          :style="height ? { height: height + 'px' } : { height: '24px' }"
          :title="t('agentView.elementCard.preview')"
        />
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
/* Inside the user's bubble, so the card reads as an exhibit attached to the
   words above it: a translucent tile on the bubble's own colour, with the
   page identity as the loudest line. */
.elcard {
  margin-top: 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: color-mix(in srgb, var(--el-bg-color) 55%, transparent);
  overflow: hidden;
  text-align: left;
}

.elcard-head {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border: 0;
  background: transparent;
  font: inherit;
  font-size: 12px;
  color: var(--el-text-color-primary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.page {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
}

.sep {
  color: var(--el-text-color-secondary);
}

.what {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--el-font-family-mono, monospace);
  color: var(--el-text-color-regular);
}

.pos {
  margin-left: auto;
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.elcard-body {
  padding: 4px 8px 8px;
  border-top: 1px solid var(--el-border-color-extra-light);
}

.row {
  display: flex;
  gap: 8px;
  padding: 3px 0;
  font-size: 12px;
  line-height: 1.5;

  .k {
    flex: 0 0 52px;
    color: var(--el-text-color-secondary);
  }

  .v {
    min-width: 0;
    word-break: break-all;
  }

  .mono {
    font-family: var(--el-font-family-mono, monospace);
  }

  .authored-quote {
    white-space: pre-wrap;
  }

  .data {
    margin: 0;
    max-height: 200px;
    overflow: auto;
    white-space: pre-wrap;
    font-size: 11px;
    line-height: 1.5;
  }

  .link {
    color: var(--el-color-primary);
    text-decoration: none;
    cursor: pointer;

    &:hover {
      text-decoration: underline;
    }
  }
}

.preview {
  margin-top: 4px;
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 6px;
  overflow: hidden;
  background: #fff;
}

.preview-frame {
  display: block;
  width: 100%;
  border: 0;
}
</style>
