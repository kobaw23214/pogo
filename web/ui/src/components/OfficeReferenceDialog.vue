<script setup lang="ts">
import type { OfficeReferenceSnapshot } from "@/api/types";
import OfficeReference from "./OfficeReference.vue";
const reference = defineModel<OfficeReferenceSnapshot | null>({ required: true });
function visibility(visible: boolean) { if (!visible) reference.value = null; }
</script>
<template><el-dialog :model-value="!!reference" :title="reference?.title" width="min(660px, calc(100vw - 32px))" append-to-body data-testid="office-reference-dialog" @update:model-value="visibility"><OfficeReference v-if="reference" :office="reference" details-only /></el-dialog></template>
