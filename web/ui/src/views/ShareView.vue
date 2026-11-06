<script setup lang="ts">
import { getLocale } from "@/i18n";
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Check, CopyDocument, InfoFilled } from "@element-plus/icons-vue";
import Logo from "@/components/Logo.vue";
import StudioEntrySkeleton from "@/components/studio/StudioEntrySkeleton.vue";
import AnswerText from "@/components/AnswerText.vue";
import WorkLog from "@/components/WorkLog.vue";
import TurnCard from "@/components/TurnCard.vue";
import OrderedMessageContent from "@/components/OrderedMessageContent.vue";
import { api } from "@/api/client";
import { normalizeMessages } from "@/api/messageContent";
import { emptyLive, type ShareEntry } from "@/api/types";
import { buildTimeline } from "@/agent/timeline";
import { buildExchanges, foldable, type Exchange } from "@/agent/exchanges";

/**
 * The public share page: /share/{id}, no login, no token.
 *
 * App.vue routes here by pathname before the agent view (and its gates) ever
 * mount, so this page is the whole app as far as an anonymous visitor knows.
 * The data comes from the one API path the server serves without
 * credentials — the snapshot, not the session, which may since have grown,
 * been renamed or been deleted. What was shared is what they see.
 *
 * The snapshot carries the exchange's timeline projection, so the page renders
 * with the session view's own components: the question as its bubble, the work
 * folded into the work log, the conclusion outside it. Shares older than that
 * projection fall back to the plain question/answer layout.
 */
const { t } = useI18n();

const entry = ref<ShareEntry | null>(null);
const failed = ref(false);
const copied = ref(false);

const id = computed(() => location.pathname.split("/")[2] ?? "");

onMounted(async () => {
  // A published page is public content; a stray crawler indexing it is not.
  document.title = "pogo · shared answer";
  const meta = document.createElement("meta");
  meta.name = "robots";
  meta.content = "noindex";
  document.head.appendChild(meta);
  try {
    const value = await api.shareView(id.value);
    entry.value = value.messages === undefined ? value : { ...value, messages: normalizeMessages(value.messages) };
    if (entry.value.title) document.title = `${entry.value.title} · pogo`;
  } catch {
    failed.value = true;
  }
});

// The one shared exchange, rebuilt through the same projection the session
// view uses — the two cannot drift over what a turn looks like.
const exchange = computed<Exchange | null>(() => {
  const e = entry.value;
  if (!e?.messages?.length) return null;
  const items = buildTimeline(e.messages, e.results ?? {}, emptyLive(), {});
  return buildExchanges(items, false)[0] ?? null;
});

const createdLabel = computed(() =>
  entry.value?.created_at ? new Date(entry.value.created_at).toLocaleString(getLocale()) : "",
);

async function copyAnswer() {
  if (!entry.value) return;
  try {
    await navigator.clipboard.writeText(entry.value.answer);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; selecting the text still works.
  }
}
</script>

<template>
  <main class="share-page">
    <header class="bar">
      <Logo />
      <span class="brand">pogo</span>
    </header>

    <div v-if="failed" class="state">{{ t("shareView.notFound") }}</div>
    <article v-else-if="!entry" class="sheet" role="status" :aria-label="t('shareView.loading')" aria-busy="true">
      <StudioEntrySkeleton area="document" />
    </article>

    <article v-else class="sheet">
      <div class="meta">
        <span v-if="entry.title" class="title">{{ entry.title }}</span>
        <span class="facts">
          <span v-if="entry.model">{{ entry.model }}</span>
          <span v-if="createdLabel">{{ createdLabel }}</span>
          <button class="copy" :title="t('turnCard.copyAnswer')" @click="copyAnswer">
            <el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>
          </button>
        </span>
      </div>

      <!-- The session view's own rendering: the question's bubble, the work
           folded into its log, the conclusion outside the fold. Read-only —
           a share has no gates to answer and no sheets to open. -->
      <template v-if="exchange">
        <div v-if="exchange.user" class="ask">
          <div class="ask-bubble"><OrderedMessageContent v-if="exchange.user.orderedContent" :blocks="exchange.user.orderedContent" /><template v-else>{{ exchange.user.text }}</template></div>
        </div>
        <WorkLog
          v-if="foldable(exchange)"
          :exchange="exchange"
          :run-active="false"
          :skills="[]"
          cwd=""
          :todo-pinned="false"
        />
        <template v-else>
          <TurnCard
            v-for="turn in exchange.turns"
            :key="turn.id"
            :turn="turn"
            :run-active="false"
            :skills="[]"
            cwd=""
            :todo-pinned="false"
          />
          <!-- Harness nudges, the session view's rule: quiet system lines in
               the flat rendering, folded away with the work log otherwise. -->
          <div v-for="n in exchange.notices" :key="n.id" class="notice" :title="n.text">
            <el-icon class="nico"><InfoFilled /></el-icon>
            <span class="ntext">{{ n.text }}</span>
          </div>
        </template>
      </template>

      <!-- Shares from before the snapshot carried the timeline: the plain
           question/answer layout they were created with. -->
      <template v-else>
        <section v-if="entry.question" class="question">
          <div class="label">{{ t("shareView.question") }}</div>
          <AnswerText :source="entry.question" />
        </section>
        <section class="answer">
          <div class="label">{{ t("shareView.answer") }}</div>
          <AnswerText :source="entry.answer" />
        </section>
      </template>

      <footer class="foot">{{ t("shareView.poweredBy") }}</footer>
    </article>
  </main>
</template>

<style scoped lang="scss">
.share-page {
  min-height: 100vh;
  background: var(--el-bg-color-page);
  padding: 0 16px 60px;
}

.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 780px;
  margin: 0 auto;
  padding: 18px 0 8px;
  color: var(--el-text-color-secondary);
}

.brand {
  font-size: 14px;
  font-weight: 600;
}

.state {
  max-width: 780px;
  margin: 120px auto 0;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.sheet {
  max-width: 780px;
  margin: 14px auto 0;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 14px;
  padding: 26px 30px;
  box-shadow: 0 1px 3px rgb(0 0 0 / 4%);
}

.meta {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
  margin-bottom: 16px;
}

.title {
  font-size: 15px;
  font-weight: 650;
  color: var(--el-text-color-primary);
}

.facts {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

/* The question, right-aligned like the session view's own bubble, and never
   truncated — the share exists to show it. */
.ask {
  display: flex;
  justify-content: flex-end;
  margin: 4px 0 6px;
}

.ask-bubble {
  max-width: 85%;
  padding: 9px 13px;
  border-radius: 12px 12px 4px;
  background: var(--el-color-primary-light-8);
  font-size: 13.5px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--el-text-color-primary);
}

/* A harness nudge: one quiet system line, icon + muted small text — it must
   not read as something anyone typed. */
.notice {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: 6px 0;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  .nico {
    font-size: 11px;
    flex: 0 0 auto;
    position: relative;
    top: 1px;
  }

  .ntext {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}

.question {
  margin: 0 0 6px;

  :deep(.answer) {
    font-size: 13.5px;
    color: var(--el-text-color-regular);
  }
}

.answer {
  margin-top: 18px;
}

.label {
  font-size: 11px;
  color: var(--el-text-color-placeholder);
  margin-bottom: 2px;
}

.copy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--el-text-color-placeholder);
  cursor: pointer;
  font-size: 13px;

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.foot {
  margin-top: 26px;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-extra-light);
  font-size: 11.5px;
  color: var(--el-text-color-placeholder);
}
</style>
