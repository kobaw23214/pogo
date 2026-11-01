<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{ icon: string }>();
// Editor controls must remain visible in offline deployments. Keep their small
// monochrome outlines local rather than requesting an external icon provider.
const paths: Record<string, string> = {
  'arrow-left': 'M19 12H5m7-7-7 7 7 7',
  'panels-top-left': 'M3 3h18v18H3V3Zm0 6h18M9 9v12',
  'panel-right-close': 'M3 3h18v18H3V3Zm12 0v18m-8-13 4 4-4 4',
  'folder-open': 'M3 7V4h6l3 3h9v3M3 7v13h16l3-10H7L3 20',
  'folder-search': 'M3 20V4h6l3 3h9v5M3 20h8m6-9a4 4 0 1 0 0 8 4 4 0 0 0 0-8Zm3 7 3 3',
  globe: 'M3 12h18M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18Zm0 0c-5 5-5 13 0 18 5-5 5-13 0-18Z',
  'table-2': 'M3 3h18v18H3V3Zm0 6h18M9 9v12',
  box: 'm12 3 9 5v8l-9 5-9-5V8l9-5Zm-9 5 9 5 9-5m-9 5v8M7.5 5.5l9 5',
  layers: 'm12 3 10 5-10 5L2 8l10-5Zm-10 9 10 5 10-5M2 16l10 5 10-5',
  'arrow-up-right': 'M6 18 18 6M6 6h12v12',
  search: 'M10.5 3a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15Zm5.5 13 5 5',
  x: 'm6 6 12 12M18 6 6 18',
  'refresh-cw': 'M20 7a8 8 0 0 0-14-2L3 8m0-5v5h5m-4 9a8 8 0 0 0 14 2l3-3m0 5v-5h-5',
  'chevron-up': 'm6 15 6-6 6 6',
  'chevron-down': 'm6 9 6 6 6-6',
  'chevron-left': 'm15 6-6 6 6 6',
  'chevron-right': 'm9 6 6 6-6 6',
  plus: 'M12 5v14M5 12h14',
  'mouse-pointer-2': 'm4 3 7 18 3-7 7-3L4 3Z',
  'pencil-line': 'm15 4 5 5M4 20l4-1L21 6l-4-4L4 15v5Zm9 0h8',
  eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12Zm10-3a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z',
  'message-square-plus': 'M21 15V3H3v18l6-6h12ZM12 6v6m-3-3h6',
  'shield-check': 'm12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6l9-4Zm-5 10 3 3 7-7',
};
const path = computed(() => paths[props.icon.replace(/^lucide:/, '')] || paths.layers);
</script>
<template><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="path" /></svg></template>
