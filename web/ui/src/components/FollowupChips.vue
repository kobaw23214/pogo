<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { StarterCard } from "@/api/types";

// Next-step chips at the tail of the conversation. Chips rather than cards on
// purpose: the empty state has the whole screen to introduce the agent, whereas
// this sits under an answer the user is still reading and must not compete with
// it. No icons, no descriptions — the chip shows the short title and leaves the
// full question to the hover preview, because a row that wraps to two lines is
// only readable when each entry stays short.
//
// The hover preview is a small card, not the stock dark tooltip: it previews
// the exact text a click sends, so it borrows the conversation's own surface —
// light, bordered, softly elevated — with an eyebrow label saying what the
// text is and what clicking does.
defineProps<{ chips: StarterCard[] }>();
const emit = defineEmits<{ pick: [StarterCard] }>();
const { t } = useI18n();
</script>

<template>
  <div class="followups">
    <el-tooltip
      v-for="(c, i) in chips"
      :key="`${c.title}-${i}`"
      :disabled="!c.prompt || c.prompt === c.title"
      placement="top"
      effect="light"
      :show-after="0"
      :hide-after="0"
      transition="el-zoom-in-top"
      popper-class="followup-tip"
    >
      <template #content>
        <div class="tip-eyebrow">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9L12 3z" />
            <path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8L19 15z" />
          </svg>
          <span>{{ t("followupChips.tipLabel") }}</span>
        </div>
        <p class="tip-prompt">{{ c.prompt }}</p>
      </template>
      <button class="chip" type="button" :aria-label="c.prompt ?? c.title" @click="emit('pick', c)">
        {{ c.title }}
      </button>
    </el-tooltip>
  </div>
</template>

<style scoped lang="scss">
.followups {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 2px 0 10px;
  // Two rows are the ceiling: more than that and the row stops being a hint and
  // starts being a menu competing with the answer above it.
  max-height: 64px;
  overflow: hidden;
}

.chip {
  padding: 4px 10px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 999px;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease;

  // A long title may not elbow its row-mates onto a third line; the hover
  // preview carries everything the ellipsis drops.
  max-width: 320px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;

  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}
</style>

<!-- Unscoped because the tooltip popper is teleported to <body>, out of the
     scoped style's reach. -->
<style lang="scss">
// The preview card: the conversation surface's own grammar (light, hairline
// border, soft elevation) rather than the stock dark bubble — it previews a
// message, so it is dressed like one.
.el-popper.is-light.followup-tip {
  max-width: 400px;
  padding: 9px 12px 10px;
  border-color: var(--el-border-color-lighter);
  border-radius: 12px;
  box-shadow: var(--el-box-shadow-light);

  .tip-eyebrow {
    display: flex;
    align-items: center;
    gap: 5px;
    font-size: 10.5px;
    letter-spacing: 0.04em;
    color: var(--el-text-color-secondary);

    svg {
      width: 11px;
      height: 11px;
      color: var(--el-color-primary);
    }
  }

  .tip-prompt {
    margin: 5px 0 0;
    font-size: 12.5px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
    white-space: normal;
    word-break: break-word;
    // Long questions clamp with an ellipsis instead of a scroll region — a
    // tooltip you must scroll has stopped being a preview.
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 6;
    overflow: hidden;
  }
}
</style>
