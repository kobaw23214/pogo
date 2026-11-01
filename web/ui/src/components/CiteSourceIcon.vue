<script setup lang="ts">
import { computed, inject, ref } from "vue";
import type { CiteSource } from "@/api/types";
import { citeFaviconKey, citeSourceFavicon } from "@/agent/citeFavicon";
import { citePresentation } from "@/agent/citePresentation";
import CiteTypeIcon from "./CiteTypeIcon.vue";
import "@/agent/citeFavicon.css";

const props = defineProps<{ source: CiteSource }>();
const cache = inject(citeFaviconKey, null);
const icon = computed(() => citeSourceFavicon(props.source, cache));
const loaded = ref("");
const failed = ref("");
function imageResult(event: Event, success: boolean) {
  const image = event.target as HTMLImageElement;
  if (image.getAttribute("src") !== icon.value) return;
  if (success) loaded.value = icon.value ?? "";
  else failed.value = icon.value ?? "";
}
</script>

<template>
  <span class="cite-source-icon" :class="{ 'has-favicon': icon && loaded === icon && failed !== icon }" aria-hidden="true">
    <CiteTypeIcon :type="citePresentation(source).iconType" />
    <img v-if="icon && failed !== icon" :key="icon" class="cite-favicon" :src="icon" alt="" decoding="async" @load="imageResult($event, true)" @error="imageResult($event, false)" />
  </span>
</template>
