<script setup lang="ts">
// The pogo mark: the wordmark itself — "Po" over "Go" — set as heavy
// geometric letterforms on a white tile, black on white after the Wails
// app-icon reference. A slight skew gives the italic lean; the letters are
// drawn as strokes and arcs rather than font glyphs so they render
// identically on every system. The colours are fixed (brand black/white),
// not skin variables: the tile carries its own background, so the mark sits
// equally well on a Nord sidebar or a light page.
withDefaults(defineProps<{ size?: number }>(), { size: 22 });
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 64 64"
    fill="none"
    role="img"
    aria-label="pogo"
    class="logo"
  >
    <rect x="2" y="2" width="60" height="60" rx="14" fill="#FFFFFF" />
    <g transform="translate(2.8 0) skewX(-6)" stroke="#111111" stroke-width="5.5" fill="none">
      <!-- Po -->
      <path d="M17.5 30.5V13.5" />
      <path d="M17.5 13.5H21A6.5 6.5 0 0 1 21 26.5H17.5" />
      <circle cx="40.25" cy="20.25" r="6" />
      <!-- Go -->
      <path d="M27.5 38.39A7 7 0 1 0 27.95 48.7H21.5" />
      <circle cx="40.5" cy="45.25" r="6" />
    </g>
  </svg>
</template>

<style scoped>
.logo {
  display: block;
  flex-shrink: 0;
}
</style>
