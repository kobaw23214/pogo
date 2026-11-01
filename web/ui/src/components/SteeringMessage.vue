<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { UserItem } from "@/agent/timeline";
import OrderedMessageContent from "./OrderedMessageContent.vue";
defineProps<{ message: UserItem }>();
const emit = defineEmits<{ openFile: [file: { path: string; name: string }] }>();
const { t } = useI18n();
</script>

<template>
  <div class="steering-message" :data-mid="message.id" :data-message-id="message.id" data-testid="steering-message">
    <div class="steering-label">{{ t('conversationInbox.adopted') }}</div>
    <div class="steering-text"><OrderedMessageContent v-if="message.orderedContent" :blocks="message.orderedContent" @open-file="emit('openFile', $event)" /><template v-else>{{ message.text }}</template></div>
  </div>
</template>

<style scoped>
.steering-message { margin: 20px 0 16px; padding: 4px 0 4px 13px; border-left: 2px solid var(--el-border-color); }
.steering-label { margin-bottom: 5px; color: var(--el-text-color-secondary); font-size: 12px; }
.steering-text { white-space: pre-wrap; overflow-wrap: anywhere; color: var(--el-text-color-primary); line-height: 1.65; }
</style>
