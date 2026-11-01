<script setup lang="ts">
/**
 * The sidebar's "this conversation is working" marker: the same sonar grammar
 * as the live work header's pulse, scaled down to row size — a heartbeat, not
 * machinery. It draws in the accent colour on purpose: an idle row never
 * carries one, so colour is never the only thing setting it apart (the motion
 * and the shape both say it too).
 */
</script>

<template>
  <span class="sess-pulse" aria-hidden="true"><i /><i /></span>
</template>

<style scoped lang="scss">
.sess-pulse {
  position: relative;
  flex: 0 0 auto;
  width: 12px;
  height: 12px;
  color: var(--el-color-primary);

  &::before {
    content: "";
    position: absolute;
    inset: 3.5px;
    border-radius: 50%;
    background: currentcolor;
    animation: sp-core 2s ease-in-out infinite;
  }

  i {
    position: absolute;
    inset: 0;
    border: 1.5px solid currentcolor;
    border-radius: 50%;
    opacity: 0;
    animation: sp-sonar 2s cubic-bezier(0.22, 0.61, 0.36, 1) infinite;

    &:nth-child(2) {
      animation-delay: 1s;
    }
  }
}

@keyframes sp-core {
  0%,
  100% {
    transform: scale(1);
  }

  50% {
    transform: scale(0.7);
  }
}

@keyframes sp-sonar {
  0% {
    transform: scale(0.3);
    opacity: 0.85;
  }

  100% {
    transform: scale(1.05);
    opacity: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .sess-pulse::before {
    animation: none;
  }

  /* The rings are pure motion; without it only the steady core remains. */
  .sess-pulse i {
    display: none;
  }
}
</style>
