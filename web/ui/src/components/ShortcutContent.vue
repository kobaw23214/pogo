<script setup lang="ts">
import type { MessageInputPart } from "@/api/types";
import StructuredShortcutContent from "./StructuredShortcutContent.vue";
import AnswerText from "./AnswerText.vue";
import CodeBlock from "./CodeBlock.vue";
defineProps<{ content: string; language?: string; parts?: MessageInputPart[] }>();
</script>
<template><StructuredShortcutContent v-if="parts?.length" :parts="parts" :language="language" /><AnswerText v-else-if="language === 'markdown'" class="shortcut-markdown" :source="content" passive /><CodeBlock v-else :code="content" :lang="language || 'text'" uncollapsed preserve-trailing-line /></template>
<style scoped>.shortcut-markdown{min-width:0;max-width:100%;white-space:normal;overflow-wrap:anywhere}</style>
