<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { PendingGate } from "@/api/types";
import { approvalUiKey } from "@/agent/approvalState";

const { t } = useI18n();

const props = defineProps<{
  gate: PendingGate;
  /** live is false when replaying history, where an undecided card is stale. */
  live: boolean;
  compact?: boolean;
  serverOffset?: number | null;
}>();

const emit = defineEmits<{
  decide: [{ allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [];
  thaw: [];
}>();
const approval = inject(approvalUiKey, undefined);

// The countdown is display only; the server's clock decides. It is computed from
// an absolute deadline rather than counted down from a duration, which is why a
// page reload still shows the right number.
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => { timer = setInterval(() => (now.value = Date.now()), 200); });
onBeforeUnmount(() => { if (timer !== undefined) clearInterval(timer); });

const remaining = computed(() => props.gate.frozen ? Math.max(0, props.gate.remaining_ms ?? 0) :
  Math.max(0, props.gate.deadline - (now.value + (props.serverOffset ?? approval?.serverOffset.value ?? 0))));
const seconds = computed(() => Math.ceil(remaining.value / 1000));
const live = computed(() => approval ? approval.canDecide && approval.activeGateIds.value.has(props.gate.gate_id) : props.live);
const expired = computed(() => !live.value || (!props.gate.frozen && props.gate.deadline > 0 && remaining.value === 0));
const dangerous = computed(() => (props.gate.danger?.length ?? 0) > 0);

const args = computed(() => (props.gate.args ?? {}) as Record<string, unknown>);
const command = computed(() => String(args.value.command ?? ""));
const isBash = computed(() => props.gate.tool === "bash");
const operationSummary = computed(() => {
  if (isBash.value) return command.value;
  return JSON.stringify(args.value);
});

const rewriting = ref(false);
const draft = ref("");
const remember = ref(false);
const localDeciding = ref(false);
const deciding = computed(() => approval ? !!approval.pending.value[props.gate.gate_id] : localDeciding.value);
const decisionError = computed(() => approval?.errors.value[props.gate.gate_id]);
const detailsOpen = ref(false);

watch(
  () => `${approval?.sessionId.value ?? ""}:${props.gate.gate_id}`,
  () => {
    rewriting.value = false;
    remember.value = false;
    localDeciding.value = false;
    detailsOpen.value = false;
    parseError.value = "";
  },
);

function startRewrite() {
  if (expired.value || deciding.value) return;
  parseError.value = "";
  draft.value = isBash.value ? command.value : JSON.stringify(args.value, null, 2);
  rewriting.value = true;
  // Editing takes time, and being judged as "did not answer" while typing would
  // be wrong. The server freezes its clock and reports the new deadline on thaw.
  emit("freeze");
}

function cancelRewrite() {
  rewriting.value = false;
  emit("thaw");
}

function submitRewrite() {
  if (expired.value || deciding.value) return;
  let next: unknown;
  if (isBash.value) {
    next = { ...args.value, command: draft.value };
  } else {
    try {
      next = JSON.parse(draft.value);
    } catch {
      parseError.value = t("gateCard.invalidJson");
      return;
    }
  }
  localDeciding.value = true;
  emit("decide", { allow: true, args: next });
}

const parseError = ref("");

function approve() {
  if (expired.value || deciding.value) return;
  localDeciding.value = true;
  // A danger match cannot be remembered; the server enforces that too, so the
  // checkbox being disabled is a hint rather than the control.
  emit("decide", {
    allow: true,
    remember: remember.value && !dangerous.value && !props.gate.origin ? (isBash.value ? "command" : "tool") : undefined,
  });
}

function reject() {
  if (expired.value || deciding.value) return;
  localDeciding.value = true;
  emit("decide", { allow: false, reason: t("gateCard.rejectReason") } as never);
}
</script>

<template>
  <div class="gate" :class="{ expired, dangerous, compact }" :data-gate-id="gate.gate_id">
    <div class="head">
      <span class="title">{{ expired ? t("gateCard.expired") : t("gateCard.confirmRequired") }}</span>
      <span class="tool">{{ gate.tool }}</span>
      <span v-if="!expired" class="clock">{{ gate.frozen ? t("gateCard.paused") : t("gateCard.remaining", { n: seconds }) }}</span>
    </div>

    <template v-if="rewriting">
      <textarea v-model="draft" class="editor" :rows="isBash ? 3 : 8" spellcheck="false" />
      <div v-if="parseError" class="parse-error">{{ parseError }}</div>
      <div class="actions">
        <button class="primary" :disabled="deciding || expired" @click="submitRewrite">{{ t("gateCard.runRewritten") }}</button>
        <button :disabled="deciding" @click="cancelRewrite">{{ t("gateCard.cancelRewrite") }}</button>
      </div>
      <div v-if="expired" class="stale">{{ t("gateCard.staleNote") }}</div>
    </template>

    <template v-else>
      <div v-if="compact" class="operation-summary">{{ operationSummary }}</div>
      <details v-if="compact" class="compact-details" @toggle="detailsOpen = ($event.target as HTMLDetailsElement).open">
        <summary>{{ t("gateCard.details") }}</summary>
        <pre class="args">{{ isBash ? command : JSON.stringify(args, null, 2) }}</pre>
      </details>
      <pre v-else class="args">{{ isBash ? command : JSON.stringify(args, null, 2) }}</pre>

      <div v-if="dangerous" class="danger">
        {{ t("gateCard.dangerHit", { patterns: gate.danger?.join("、") }) }}
        <span class="note">{{ t("gateCard.dangerNote") }}</span>
      </div>

      <div v-if="!expired" class="actions">
        <button class="primary" :disabled="deciding" @click="approve">{{ t("gateCard.approve") }}</button>
        <button :disabled="deciding" @click="startRewrite">{{ t("gateCard.rewrite") }}</button>
        <button class="danger-btn" :disabled="deciding" @click="reject">{{ t("gateCard.reject") }}</button>
      </div>
      <label v-if="!expired && !gate.origin && (!compact || detailsOpen)" class="remember" :class="{ off: dangerous }">
        <input v-model="remember" type="checkbox" :disabled="dangerous" />
        <span v-if="isBash">{{ t("gateCard.rememberCommand") }}</span>
        <span v-else>{{ t("gateCard.rememberTool", { tool: gate.tool }) }}</span>
        <span v-if="dangerous" class="note">{{ t("gateCard.rememberDangerNote") }}</span>
      </label>
      <div v-if="expired" class="stale">{{ t("gateCard.staleNote") }}</div>
    </template>
    <div v-if="decisionError" class="parse-error" role="alert">{{ decisionError }}</div>
  </div>
</template>

<style scoped lang="scss">
/* The run is stopped on this card, so it is the one thing in the transcript that
   is allowed to look like an interruption: a tinted card with a full border, not
   a row. */
.gate {
  border: 1px solid color-mix(in srgb, var(--el-color-warning) 45%, transparent);
  border-radius: 12px;
  padding: 12px 14px;
  background: var(--el-color-warning-light-9);
  box-shadow: var(--el-box-shadow-lighter);

  &.dangerous {
    border-color: color-mix(in srgb, var(--el-color-danger) 45%, transparent);
    background: var(--el-color-danger-light-9);
  }

  &.expired {
    border-color: var(--el-border-color);
    background: var(--el-fill-color-lighter);
    opacity: 0.75;
  }
}
.gate.compact { padding: 9px 11px; border-radius: 10px; box-shadow: none; }
.compact-details summary { cursor: pointer; font-size: 12px; color: var(--el-text-color-secondary); }
.compact-details .args { margin-top: 7px; max-height: min(20dvh, 160px); overflow: auto; }
.compact .head { margin-bottom: 5px; flex-wrap: wrap; }
.compact .actions { flex-wrap: wrap; margin-top: 7px; }
.compact .actions button { flex: 1 1 auto; }
.operation-summary { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.45 var(--pg-mono); margin: 4px 0 6px; }

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  margin-bottom: 8px;
}

.title {
  font-weight: 600;
}

.tool {
  font-family: var(--pg-mono);
  color: var(--el-color-primary-dark-2);
}

.clock {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}

/* White, not the page's fill: this is the thing being approved, and on a tinted
   card the only way to make it the focus is to lift it out of the tint. */
.args,
.editor {
  width: 100%;
  box-sizing: border-box;
  margin: 0;
  padding: 9px 11px;
  border-radius: 9px;
  border: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
  font: 12px/1.55 var(--pg-mono);
  white-space: pre-wrap;
  word-break: break-all;
}

.editor {
  resize: vertical;
}

.danger {
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-color-danger);
}

.note {
  color: var(--el-text-color-secondary);
  font-size: 11px;
}

.parse-error {
  margin-top: 6px;
  font-size: 11px;
  color: var(--el-color-danger);
}

.actions {
  display: flex;
  gap: 8px;
  margin-top: 10px;

  button {
    border: 1px solid var(--el-border-color);
    background: var(--el-bg-color);
    border-radius: 999px;
    padding: 5px 14px;
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    transition:
      background var(--pg-transition),
      border-color var(--pg-transition);

    &:hover:not(:disabled) {
      border-color: var(--el-border-color-hover);
    }

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }

    /* The skin's solid, like Send: approving is the affirmative action, and it is
       the one button on a tinted card that must not read as part of the tint. */
    &.primary {
      background: var(--pg-solid);
      border-color: var(--pg-solid);
      color: var(--pg-on-solid);

      &:hover:not(:disabled) {
        background: var(--pg-solid-hover);
        border-color: var(--pg-solid-hover);
      }
    }

    &.danger-btn {
      color: var(--el-color-danger);
      border-color: color-mix(in srgb, var(--el-color-danger) 40%, transparent);

      &:hover:not(:disabled) {
        background: color-mix(in srgb, var(--el-color-danger) 8%, var(--el-bg-color));
        border-color: var(--el-color-danger);
      }
    }
  }
}

.remember {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  &.off {
    opacity: 0.6;
  }
}

.stale {
  margin-top: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
