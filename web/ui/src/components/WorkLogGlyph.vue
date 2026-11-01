<script setup lang="ts">
import type { ToolGlyphKind } from "@/agent/toolPresentation";
/**
 * Shared semantic glyphs for tool headers and the work log's rail.
 *
 * Inline SVG rather than the Element Plus set: EP has no terminal and no
 * file-diff metaphor at all (Monitor and EditPen are the weak stand-ins), and
 * mixing two icon families in one rail reads as a mistake. These follow
 * the lucide stroke idiom (24×24 viewBox, stroke 2, round caps), which is also
 * what the EP icons roughly draw at, so the rail stays coherent.
 *
 * Colour is the row's category channel — set from the outside, on the node;
 * the glyph itself only ever draws currentColor. Shape is the other half of
 * the encoding: colour is never the only thing telling rows apart (WCAG 1.4.1).
 */
const props = defineProps<{
  kind: ToolGlyphKind;
}>();
</script>

<template>
  <svg
    class="glyph"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <!-- thinking: a lightbulb — "an idea took time" -->
    <template v-if="props.kind === 'thinking'">
      <path d="M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5" />
      <path d="M9 18h6" />
      <path d="M10 22h4" />
    </template>
    <!-- explore: a magnifier — the zero-ambiguity search metaphor -->
    <template v-else-if="props.kind === 'explore'">
      <circle cx="11" cy="11" r="8" />
      <path d="m21 21-4.3-4.3" />
    </template>
    <!-- command: a terminal prompt — >_ -->
    <template v-else-if="props.kind === 'command'">
      <polyline points="4 17 10 11 4 5" />
      <line x1="12" x2="20" y1="19" y2="19" />
    </template>
    <!-- change: a pencil — "this file was edited" -->
    <template v-else-if="props.kind === 'change'">
      <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
      <path d="m15 5 4 4" />
    </template>
    <template v-else-if="props.kind === 'read'">
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z" />
      <path d="M14 2v6h6M8 13h8M8 17h6" />
    </template>
    <template v-else-if="props.kind === 'list'">
      <path d="M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
      <path d="M7 11h10M7 15h7" />
    </template>
    <template v-else-if="props.kind === 'browser'">
      <rect x="2" y="3" width="20" height="18" rx="2" />
      <path d="M2 8h20M6 5.5h.01M10 5.5h.01M8 12l-3 3 3 3M16 12l3 3-3 3" />
    </template>
    <template v-else-if="props.kind === 'office'">
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8ZM14 2v6h6" />
      <path d="M8 12h8v7H8ZM8 15.5h8M12 12v7" />
    </template>
    <template v-else-if="props.kind === 'mcp'">
      <path d="M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0ZM12 17v5" />
    </template>
    <template v-else-if="props.kind === 'skill'">
      <path d="m12 2 2.7 6.8L22 12l-7.3 3.2L12 22l-2.7-6.8L2 12l7.3-3.2Z" />
    </template>
    <template v-else-if="props.kind === 'subagent'">
      <rect x="4" y="7" width="16" height="14" rx="3" />
      <path d="M12 3v4M9 16h6M1 12v5M23 12v5M8 11h.01M16 11h.01" />
    </template>
    <template v-else-if="props.kind === 'todo'">
      <path d="m3 6 2 2 3-4M11 6h10M3 13h5M11 13h10M3 20h5M11 20h10" />
    </template>
    <template v-else-if="props.kind === 'shortcut'">
      <path d="m10 13 4-4M8 16l-1 1a4 4 0 0 1-6-6l5-5a4 4 0 0 1 6 0M12 18a4 4 0 0 0 6 0l5-5a4 4 0 0 0-6-6l-1 1" transform="translate(0 -1)" />
    </template>
    <template v-else-if="props.kind === 'background'">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </template>
    <template v-else>
      <path d="m14 6 4-4 4 4-4 4M10 18l-4 4-4-4 4-4M8 16l8-8" />
    </template>
  </svg>
</template>

<style scoped>
.glyph {
  display: block;
  width: 100%;
  height: 100%;
}
</style>
