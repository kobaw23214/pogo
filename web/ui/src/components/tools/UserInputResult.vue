<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { ToolResult, UserInputQuestion } from "@/api/types";

const props = defineProps<{ args: unknown; result?: ToolResult }>();
const { t } = useI18n();
const questions = computed<UserInputQuestion[]>(() => {
  const source = props.result?.details && typeof props.result.details === "object" ? props.result.details : props.args;
  if (!source || typeof source !== "object" || !("questions" in source) || !Array.isArray(source.questions)) return [];
  return source.questions as UserInputQuestion[];
});
const answers = computed<Record<string, string[]>>(() => {
  const source = props.result?.details && typeof props.result.details === "object" ? props.result.details : undefined;
  if (!source || !("answers" in source) || !source.answers || typeof source.answers !== "object") return {};
  return source.answers as Record<string, string[]>;
});
const status = computed(() => {
  const details = props.result?.details;
  return details && typeof details === "object" && "status" in details && typeof details.status === "string" ? details.status : "";
});
const asynchronous = computed(() => {
  const details = props.result?.details;
  return !!(details && typeof details === "object" && "async" in details && details.async === true);
});
</script>

<template>
  <section v-if="questions.length" class="user-input-result" :aria-label="t('toolCall.names.request_user_input')">
    <div v-for="question in questions" :key="question.id" class="answered-question">
      <span class="header">{{ question.header }}</span>
      <p class="question">{{ question.question }}</p>
      <div v-if="answers[question.id]?.length" class="answers">
        <span class="answer-label">{{ t("userInput.answered") }}</span>
        <span v-for="answer in answers[question.id]" :key="answer" class="answer">{{ answer }}</span>
      </div>
      <p v-else-if="!result || status === 'pending'" class="not-answered">{{ asynchronous ? t("userInput.asyncWaiting") : t("userInput.waiting") }}</p>
      <p v-else class="not-answered">{{ t("userInput.notAnswered") }}</p>
    </div>
  </section>
</template>

<style scoped lang="scss">
.user-input-result { display: grid; gap: 9px; padding: 11px 12px; border: 1px solid var(--el-border-color-light); border-radius: 10px; background: var(--el-fill-color-lighter); }
.answered-question + .answered-question { padding-top: 9px; border-top: 1px solid var(--el-border-color-lighter); }
.header { display: inline-block; max-width: 100%; color: var(--el-text-color-secondary); font-size: 10px; overflow-wrap: anywhere; }
.question { margin: 3px 0 0; color: var(--el-text-color-primary); font-size: 12px; line-height: 1.55; overflow-wrap: anywhere; }
.answers { display: flex; align-items: baseline; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.answer-label { color: var(--el-text-color-secondary); font-size: 11px; }
.answer { padding: 2px 7px; border-radius: 5px; background: var(--el-bg-color); color: var(--el-color-primary); font-size: 11px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
.not-answered { margin: 5px 0 0; color: var(--el-text-color-secondary); font-size: 11px; }
</style>
