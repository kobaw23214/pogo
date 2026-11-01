<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { PendingUserInput, UserInputQuestion } from "@/api/types";
import { collectUserInputAnswers, emptyUserInputDraft, hasUserInputAnswer, type UserInputDraft } from "@/agent/userInputDraft";

const props = defineProps<{
  requests: PendingUserInput[];
  pending: Record<string, boolean>;
  errors: Record<string, string>;
}>();
const emit = defineEmits<{
  answer: [{ requestId: string; revision: number; answers: Record<string, string[]> }];
  skip: [{ requestId: string; revision: number }];
}>();

const { t } = useI18n();
const uid = useId();
const active = ref(0);
const selectedRequestId = ref("");
const draft = ref<Record<string, UserInputDraft>>({});
const validationError = ref("");
const heading = ref<HTMLElement | null>(null);
const customInput = ref<HTMLTextAreaElement | null>(null);
const request = computed(() => props.requests.find(item => item.request_id === selectedRequestId.value));
const question = computed(() => request.value?.questions[active.value]);
const submitting = computed(() => !!request.value && props.pending[request.value.request_id]);
const requestError = computed(() => request.value ? props.errors[request.value.request_id] : undefined);

watch(() => props.requests.map(item => item.request_id), ids => {
  if (!ids.includes(selectedRequestId.value)) selectedRequestId.value = ids[0] ?? "";
}, { immediate: true });
watch(() => request.value?.request_id, () => {
  active.value = 0;
  validationError.value = "";
  draft.value = Object.fromEntries(request.value?.questions.map(q => [q.id, emptyUserInputDraft()]) ?? []);
  focusHeading();
}, { immediate: true });
watch(active, () => { validationError.value = ""; focusHeading(); });

function focusHeading() {
  nextTick(() => heading.value?.focus({ preventScroll: true }));
}
function currentDraft(q: UserInputQuestion): UserInputDraft {
  return draft.value[q.id] ??= emptyUserInputDraft();
}
function isSelected(q: UserInputQuestion, label: string): boolean {
  return currentDraft(q).selected.includes(label);
}
function choose(q: UserInputQuestion, label: string) {
  const current = currentDraft(q);
  if (q.multi_select) {
    current.selected = current.selected.includes(label) ? current.selected.filter(value => value !== label) : [...current.selected, label];
  } else {
    current.selected = [label];
    current.other = false;
  }
  validationError.value = "";
}
function chooseOther(q: UserInputQuestion) {
  const current = currentDraft(q);
  current.other = q.multi_select ? !current.other : true;
  if (!q.multi_select) current.selected = [];
  validationError.value = "";
  if (current.other) nextTick(() => customInput.value?.focus({ preventScroll: true }));
}
function continueOrSubmit() {
  if (!request.value || !question.value || submitting.value) return;
  if (!hasUserInputAnswer(currentDraft(question.value))) {
    validationError.value = t("userInput.answerRequired");
    return;
  }
  if (active.value < request.value.questions.length - 1) {
    active.value++;
    return;
  }
  const collected = collectUserInputAnswers(request.value.questions, draft.value);
  if (collected.missing >= 0) {
    active.value = collected.missing;
    validationError.value = t("userInput.answerRequired");
    return;
  }
  emit("answer", { requestId: request.value.request_id, revision: request.value.revision, answers: collected.answers });
}
</script>

<template>
  <section v-if="request && question" class="user-input-bar" :class="{ asynchronous: request.async }" role="region" :aria-label="request.async ? t('userInput.asyncTitle') : t('userInput.title')" data-testid="user-input-bar">
    <div class="input-heading">
      <span class="question-mark" aria-hidden="true">?</span>
      <strong>{{ request.async ? t("userInput.asyncTitle") : t("userInput.title") }}</strong>
      <span v-if="request.async" class="async-mode">{{ t("userInput.asyncBadge") }}</span>
      <span v-if="request.questions.length > 1" class="step-count">{{ active + 1 }} / {{ request.questions.length }}</span>
    </div>
    <div class="question-scroll">
      <fieldset class="question" :disabled="submitting">
        <legend class="sr-only">{{ question.question }}</legend>
        <div class="question-copy">
          <span class="question-header">{{ question.header }}</span>
          <span v-if="question.multi_select" class="multiple">{{ t("userInput.multiple") }}</span>
          <h3 ref="heading" tabindex="-1">{{ question.question }}</h3>
        </div>
        <div class="options" :role="question.multi_select ? 'group' : 'radiogroup'" :aria-label="question.question">
          <label v-for="(option, index) in question.options" :key="option.label" class="option" :class="{ selected: isSelected(question, option.label) }" :for="`${uid}-${request.request_id}-${question.id}-${index}`">
            <input
              :id="`${uid}-${request.request_id}-${question.id}-${index}`"
              :name="`${uid}-${request.request_id}-${question.id}`"
              :type="question.multi_select ? 'checkbox' : 'radio'"
              :checked="isSelected(question, option.label)"
              :disabled="submitting"
              @change="choose(question, option.label)"
            />
            <span class="option-copy"><strong>{{ option.label }}</strong><span>{{ option.description }}</span></span>
            <span class="choice-indicator" aria-hidden="true" />
          </label>
          <label class="option custom-option" :class="{ selected: currentDraft(question).other }" :for="`${uid}-${request.request_id}-${question.id}-other`">
            <input
              :id="`${uid}-${request.request_id}-${question.id}-other`"
              :name="`${uid}-${request.request_id}-${question.id}`"
              :type="question.multi_select ? 'checkbox' : 'radio'"
              :checked="currentDraft(question).other"
              :disabled="submitting"
              @change="chooseOther(question)"
            />
            <span class="option-copy"><strong>{{ t("userInput.other") }}</strong><span>{{ t("userInput.otherHint") }}</span></span>
            <span class="choice-indicator" aria-hidden="true" />
          </label>
        </div>
        <textarea
          v-if="currentDraft(question).other"
          ref="customInput"
          v-model="currentDraft(question).custom"
          class="custom-input"
          rows="2"
          :maxlength="2000"
          :placeholder="t('userInput.customPlaceholder')"
          :aria-label="t('userInput.customPlaceholder')"
          :disabled="submitting"
          @keydown.escape.prevent="currentDraft(question).other = false"
        />
        <p v-if="validationError" class="validation" role="alert">{{ validationError }}</p>
      </fieldset>
    </div>
    <div class="input-footer">
      <button type="button" class="skip" :disabled="submitting" @click="emit('skip', { requestId: request.request_id, revision: request.revision })">{{ t("userInput.skip") }}</button>
      <span class="privacy-hint">{{ request.async ? t("userInput.asyncHint") : t("userInput.notApproval") }}</span>
      <div class="steps">
        <button v-if="active > 0" type="button" :disabled="submitting" @click="active--">{{ t("userInput.back") }}</button>
        <button type="button" class="primary" :disabled="submitting" @click="continueOrSubmit">
          {{ submitting ? t("userInput.submitting") : active < request.questions.length - 1 ? t("userInput.next") : t("userInput.submit") }}
        </button>
      </div>
    </div>
    <p v-if="requestError" class="request-error" role="alert">{{ requestError }}</p>
  </section>
</template>

<style scoped lang="scss">
.user-input-bar { flex: 0 0 auto; display: flex; flex-direction: column; min-height: 0; max-height: min(64dvh, 560px); overflow: hidden; border: 1px solid color-mix(in srgb, var(--el-color-primary) 35%, var(--el-border-color)); border-radius: 14px; background: var(--el-bg-color); box-shadow: 0 8px 28px color-mix(in srgb, var(--el-color-primary) 8%, transparent); }
.input-heading { display: flex; align-items: center; gap: 8px; padding: 12px 15px 8px; font-size: 12px; }
.question-mark { display: grid; place-items: center; width: 19px; height: 19px; border-radius: 50%; background: var(--el-color-primary-light-9); color: var(--el-color-primary); font-weight: 700; }
.step-count { margin-left: auto; color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; }
.async-mode { margin-left: auto; padding: 2px 7px; border-radius: 999px; background: var(--el-color-primary-light-9); color: var(--el-color-primary); font-size: 10px; white-space: nowrap; }
.async-mode + .step-count { margin-left: 0; }
.user-input-bar.asynchronous { border-color: var(--el-border-color); box-shadow: var(--el-box-shadow-lighter); }
.question-scroll { min-height: 0; overflow-y: auto; overflow-x: hidden; overscroll-behavior: contain; padding: 0 15px; }
.question { min-width: 0; margin: 0; padding: 0; border: 0; }
.question-copy { margin-bottom: 10px; }
.question-header { display: inline-block; max-width: 100%; padding: 2px 7px; border-radius: 5px; background: var(--el-fill-color-light); color: var(--el-text-color-secondary); font-size: 10px; overflow-wrap: anywhere; }
.multiple { margin-left: 6px; color: var(--el-text-color-secondary); font-size: 10px; }
h3 { margin: 7px 0 0; font-size: 14px; line-height: 1.55; font-weight: 600; overflow-wrap: anywhere; outline: none; }
.options { display: grid; gap: 7px; }
.option { position: relative; display: flex; align-items: flex-start; gap: 10px; min-width: 0; padding: 10px 34px 10px 11px; border: 1px solid var(--el-border-color-light); border-radius: 9px; background: var(--el-bg-color); cursor: pointer; transition: border-color var(--pg-transition), background var(--pg-transition); }
.option:hover { border-color: var(--el-color-primary-light-5); }
.option.selected { border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.option:focus-within { outline: 2px solid var(--el-color-primary); outline-offset: 2px; }
.option input { position: absolute; width: 1px; height: 1px; opacity: 0; pointer-events: none; }
.option-copy { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.option-copy strong { font-size: 12px; line-height: 1.45; font-weight: 600; overflow-wrap: anywhere; }
.option-copy > span { color: var(--el-text-color-secondary); font-size: 11px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
.choice-indicator { position: absolute; right: 12px; top: 13px; width: 14px; height: 14px; border: 1px solid var(--el-border-color); border-radius: 50%; background: var(--el-bg-color); }
.selected .choice-indicator { border: 4px solid var(--el-color-primary); }
.custom-input { box-sizing: border-box; width: 100%; margin-top: 8px; padding: 9px 11px; resize: vertical; border: 1px solid var(--el-border-color); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-primary); font: 12px/1.55 inherit; }
.custom-input:focus { border-color: var(--el-color-primary); outline: 2px solid var(--el-color-primary-light-7); }
.input-footer { display: flex; align-items: center; gap: 10px; padding: 11px 15px 13px; }
.input-footer button { min-height: 32px; padding: 5px 13px; border: 1px solid var(--el-border-color); border-radius: 8px; background: var(--el-bg-color); color: var(--el-text-color-primary); font-size: 12px; cursor: pointer; }
.input-footer button:disabled { opacity: .55; cursor: wait; }
.input-footer .primary { border-color: var(--pg-solid); background: var(--pg-solid); color: var(--pg-on-solid); }
.input-footer .skip { border-color: transparent; color: var(--el-text-color-secondary); padding-inline: 0; }
.privacy-hint { min-width: 0; color: var(--el-text-color-secondary); font-size: 10px; overflow-wrap: anywhere; }
.steps { display: flex; gap: 7px; margin-left: auto; flex: 0 0 auto; }
.validation, .request-error { margin: 7px 0 0; color: var(--el-color-danger); font-size: 11px; }
.request-error { margin: 0; padding: 0 15px 11px; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 560px) { .privacy-hint { display: none; } .input-footer { gap: 8px; } .option { padding-block: 11px; } }
@media (prefers-reduced-motion: reduce) { .option { transition: none; } }
</style>
