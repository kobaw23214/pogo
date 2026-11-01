<script setup lang="ts">
// The folder-upload door's glyph: a folder with an arrow rising out of it —
// the same reading as UploadIcon, scoped to trees. Stroke-based (Lucide
// folder-up shape) so it follows currentColor in the composer's quiet
// toolbar like every other icon there.
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    width="1em"
    height="1em"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M20 20V10a2 2 0 0 0-2-2h-7.5a2 2 0 0 1-1.7-.94L7.7 5.94A2 2 0 0 0 6 5H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
    <path d="M12 10v6" />
    <path d="m9 13 3-3 3 3" />
  </svg>
</template>
