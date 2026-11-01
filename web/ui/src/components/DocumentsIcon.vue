<script setup lang="ts">
// The session-files affordance: two document sheets, the front one folded and
// lined. It reads "documents collected here" — deliberately not a paperclip,
// which is the composer's attach action; the two must never be mistaken for
// each other. Stroke follows currentColor like every quiet-toolbar icon.
</script>

<template>
  <svg
    viewBox="0 0 1024 1024"
    width="1em"
    height="1em"
    fill="none"
    stroke="currentColor"
    stroke-width="56"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M 372 344 V 208 a 56 56 0 0 1 56-56 h 296 a 56 56 0 0 1 56 56 v 312 a 56 56 0 0 1-56 56 h-44" />
    <path d="M 300 412 H 496 L 676 592 V 812 a 56 56 0 0 1-56 56 H 300 a 56 56 0 0 1-56-56 V 468 a 56 56 0 0 1 56-56 Z" />
    <path d="M 496 412 V 536 a 56 56 0 0 0 56 56 h 124" stroke-width="48" />
    <path d="M 340 676 H 580" stroke-width="48" />
    <path d="M 340 772 H 512" stroke-width="48" />
  </svg>
</template>
