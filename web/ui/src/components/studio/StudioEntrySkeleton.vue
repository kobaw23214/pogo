<script setup lang="ts">
defineProps<{ area: "agent" | "sessions" | "conversation" | "composer" | "cards" | "settings" | "form" | "document" }>();
</script>

<template>
  <span v-if="area === 'agent'" class="entry-block entry-agent" aria-hidden="true" />
  <div v-else-if="area === 'sessions'" class="entry-sessions" aria-hidden="true" data-testid="studio-session-skeleton">
    <div v-for="i in 5" :key="i" class="entry-session">
      <span class="entry-block entry-session-title" />
      <span class="entry-block entry-session-meta" />
    </div>
  </div>
  <div v-else-if="area === 'cards'" class="entry-cards" aria-hidden="true" data-testid="studio-card-skeleton">
    <div v-for="i in 6" :key="i" class="entry-card">
      <div class="entry-card-head">
        <span class="entry-block entry-avatar" />
        <div class="entry-card-lines">
          <span class="entry-block entry-line w45" />
          <span class="entry-block entry-line w70" />
        </div>
      </div>
      <span class="entry-block entry-line w90" />
      <span class="entry-block entry-line w70" />
      <span class="entry-block entry-line w45" />
    </div>
  </div>
  <div v-else-if="area === 'settings' || area === 'form'" class="entry-settings" :class="{ 'entry-form': area === 'form' }" aria-hidden="true" data-testid="studio-settings-skeleton">
    <div v-if="area === 'settings'" class="entry-settings-nav">
      <span v-for="i in 4" :key="i" class="entry-block entry-line" :class="i % 2 ? 'w70' : 'w45'" />
    </div>
    <div class="entry-settings-content">
      <span class="entry-block entry-line w45" />
      <div v-for="i in 3" :key="i" class="entry-settings-group">
        <span class="entry-block entry-line w55" />
        <span class="entry-block entry-field" />
        <span class="entry-block entry-field short" />
      </div>
    </div>
  </div>
  <div v-else-if="area === 'composer'" class="entry-composer-skeleton" aria-hidden="true">
    <span class="entry-block entry-line w45" />
    <div class="entry-composer-tools">
      <span class="entry-block entry-composer-tool" />
      <span class="entry-block entry-composer-tool wide" />
      <span class="entry-block entry-composer-send" />
    </div>
  </div>
  <div v-else-if="area === 'document'" class="entry-document" aria-hidden="true" data-testid="studio-document-skeleton">
    <div class="entry-document-meta">
      <span class="entry-block entry-line w45" />
      <span class="entry-block entry-line w70" />
    </div>
    <span class="entry-block entry-document-question" />
    <div class="entry-document-answer">
      <span class="entry-block entry-line w90" />
      <span class="entry-block entry-line w80" />
      <span class="entry-block entry-line w70" />
      <span class="entry-block entry-line w45" />
    </div>
  </div>
  <div v-else class="entry-conversation" aria-hidden="true" data-testid="studio-conversation-skeleton">
    <div class="entry-answer">
      <span class="entry-block entry-line w70" />
      <span class="entry-block entry-line w90" />
      <span class="entry-block entry-line w45" />
    </div>
    <div class="entry-question">
      <span class="entry-block entry-line w60" />
    </div>
    <div class="entry-answer second">
      <span class="entry-block entry-line w80" />
      <span class="entry-block entry-line w55" />
    </div>
  </div>
</template>

<style scoped>
.entry-block {
  display: block;
  background: linear-gradient(
    90deg,
    var(--el-fill-color-light) 25%,
    var(--el-fill-color-lighter) 50%,
    var(--el-fill-color-light) 75%
  );
  background-size: 200% 100%;
  animation: studio-entry-shimmer 1.8s ease-in-out infinite;
}
.entry-agent {
  width: 76px;
  height: 12px;
  border-radius: 6px;
}
.entry-sessions {
  padding: 3px 8px;
}
.entry-session {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 11px 8px 13px;
}
.entry-session-title {
  width: 72%;
  height: 12px;
  border-radius: 6px;
}
.entry-session-meta {
  width: 38%;
  height: 9px;
  border-radius: 5px;
}
.entry-session:nth-child(2n) .entry-session-title {
  width: 56%;
}
.entry-session:nth-child(3n) .entry-session-title {
  width: 82%;
}
.entry-conversation {
  box-sizing: border-box;
  width: 100%;
  max-width: var(--pg-measure);
  margin: 0 auto;
  padding: 48px 0;
}
.entry-answer,
.entry-question {
  display: flex;
  flex-direction: column;
  gap: 13px;
  margin-bottom: 40px;
}
.entry-question {
  align-items: flex-end;
  margin: 48px 0;
}
.entry-question .entry-line {
  height: 42px;
  border-radius: 18px 18px 4px 18px;
}
.entry-answer.second {
  margin-bottom: 0;
}
.entry-line {
  height: 13px;
  border-radius: 7px;
}
.w45 { width: 45%; }
.w55 { width: 55%; }
.w60 { width: 60%; }
.w70 { width: 70%; }
.w80 { width: 80%; }
.w90 { width: 90%; }
.entry-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 14px;
  padding: 22px 28px;
}
.entry-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-height: 148px;
  padding: 18px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  background: var(--el-bg-color);
}
.entry-card-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 4px;
}
.entry-avatar {
  flex: none;
  width: 44px;
  height: 44px;
  border-radius: 13px;
}
.entry-card-lines {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 9px;
}
.entry-settings {
  box-sizing: border-box;
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr);
  gap: 34px;
  padding: 26px 28px;
}
.entry-settings.entry-form {
  grid-template-columns: minmax(0, 1fr);
}
.entry-composer-skeleton {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: 20px;
}
.entry-composer-tools {
  display: flex;
  align-items: center;
  gap: 10px;
}
.entry-composer-tool {
  width: 28px;
  height: 24px;
  border-radius: 7px;
}
.entry-composer-tool.wide {
  width: 78px;
}
.entry-composer-send {
  width: 32px;
  height: 28px;
  margin-left: auto;
  border-radius: 9px;
}
.entry-settings-nav,
.entry-settings-content,
.entry-settings-group {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.entry-settings-content {
  max-width: 760px;
}
.entry-settings-group {
  gap: 12px;
  margin-top: 16px;
}
.entry-field {
  width: 100%;
  height: 40px;
  border-radius: 7px;
}
.entry-field.short {
  width: 72%;
}
.entry-document {
  display: flex;
  flex-direction: column;
  gap: 20px;
  padding: 8px 0;
}
.entry-document-meta,
.entry-document-answer {
  display: flex;
  flex-direction: column;
  gap: 13px;
}
.entry-document-meta {
  padding-bottom: 18px;
  border-bottom: 1px solid var(--el-border-color-extra-light);
}
.entry-document-meta .w70 {
  width: 28%;
  height: 10px;
}
.entry-document-question {
  align-self: flex-end;
  width: 58%;
  height: 42px;
  border-radius: 12px 12px 4px;
}
.entry-document-answer {
  margin-top: 12px;
}
@keyframes studio-entry-shimmer {
  from { background-position: 180% 0; }
  to { background-position: -20% 0; }
}
@media (max-width: 700px) {
  .entry-conversation {
    padding-top: 28px;
  }
  .entry-cards,
  .entry-settings {
    padding: 18px 16px;
  }
  .entry-cards {
    grid-template-columns: minmax(0, 1fr);
  }
  .entry-settings {
    grid-template-columns: minmax(0, 1fr);
  }
  .entry-settings-nav {
    display: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .entry-block {
    animation: none;
  }
}
</style>
