<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { continuesLine } from "@/agent/textTail";

/**
 * A single line that rolls: the newest text slides up in while the old slides
 * out, both in place — the "one-line status ticker" ChatGPT's status row,
 * ZCode's activity line and CLI spinners converged on.
 *
 * Two rates are deliberately decoupled: the caller may push new text at
 * stream rate, but the visible line changes at most every `interval` ms — a
 * faster cadence is unreadable flicker, and the trailing edge means the
 * newest text always lands. The default is one cadence everywhere (800ms):
 * per-site intervals drifted (320 here, 500 there) and read as different
 * components ticking at each other.
 *
 * Growth is not replacement. When the new text is the old one with more
 * appended — a streaming sentence getting longer — the line extends in place
 * with no motion; only a genuinely different line rolls. Without that rule a
 * live tail replays the slide every interval, which reads as the same line
 * jumping in place.
 *
 * Silent to screen readers by design (aria-live off): a line that changes many
 * times a minute would shred announcements. Milestones go through the separate
 * role="status" announcer instead.
 */
const props = withDefaults(defineProps<{ text: string; interval?: number }>(), { interval: 800 });

const shown = ref(props.text);
const rolls = ref(true);
let lastChange = 0;
let timer: number | undefined;

watch(
  () => props.text,
  (value) => {
    const elapsed = Date.now() - lastChange;
    window.clearTimeout(timer);
    timer = window.setTimeout(
      () => {
        rolls.value = !continuesLine(shown.value, value);
        shown.value = value;
        lastChange = Date.now();
      },
      Math.max(0, props.interval - elapsed),
    );
  },
);

onBeforeUnmount(() => window.clearTimeout(timer));
</script>

<template>
  <!-- Fixed one-line window; the leaving line is absolute so the two briefly
       coexist without layout shift (no out-in gap). The "swap" name styles
       nothing on purpose: a continuation replaces the text instantly, with no
       motion to read. -->
  <span class="rolling" role="marquee" :aria-label="text">
    <Transition :name="rolls ? 'roll' : 'swap'">
      <span :key="shown" class="line">{{ shown }}</span>
    </Transition>
  </span>
</template>

<style scoped lang="scss">
.rolling {
  position: relative;
  display: block;
  height: 1.5em;
  overflow: hidden;
  min-width: 0;
}

.line {
  display: block;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* The roll: new text from below, old text out through the top — vertical
   motion is what reads as "replaced by what came next". */
.roll-enter-active,
.roll-leave-active {
  transition:
    transform 0.22s cubic-bezier(0.2, 0, 0, 1),
    opacity 0.22s;
}

.roll-enter-from {
  transform: translateY(0.75em);
  opacity: 0;
}

.roll-leave-to {
  transform: translateY(-0.75em);
  opacity: 0;
}

.roll-leave-active {
  position: absolute;
  inset: 0;
}

/* Reduced motion: the swap becomes a plain crossfade — the information stays,
   the travel goes. */
@media (prefers-reduced-motion: reduce) {
  .roll-enter-active,
  .roll-leave-active {
    transition: opacity 0.15s;
  }

  .roll-enter-from,
  .roll-leave-to {
    transform: none;
  }
}
</style>
