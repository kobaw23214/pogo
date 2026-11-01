<script setup lang="ts">
import { computed, inject, ref } from "vue";
import { citeFaviconKey } from "@/agent/citeFavicon";
import { elementPageFavicon } from "@/agent/elementPageIcon";

const props = defineProps<{ url?: string; icon?: string }>();
const archive = inject(citeFaviconKey, null);
const image = computed(() => elementPageFavicon(props.url, props.icon, archive));
const failed = ref<string>();
function imageFailed(event: Event) {
  const source = (event.target as HTMLImageElement).getAttribute("src");
  if (source && source === image.value) failed.value = source;
}
</script>

<template>
  <img v-if="image && image !== failed" :key="image" class="element-page-icon" :src="image" alt="" @error="imageFailed" />
  <svg v-else class="element-page-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
    <circle cx="12" cy="12" r="10" />
    <path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
  </svg>
</template>

<style scoped>
.element-page-icon { width: 13px; height: 13px; flex: 0 0 auto; object-fit: contain; color: var(--el-text-color-secondary); }
</style>
