<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

/**
 * The share-link dialog: the full URL and one-click copy.
 *
 * The URL shown is absolute (location.origin + the server's path): the point
 * is to paste it somewhere else. Links are snapshots that live in the server's
 * shares store — there is deliberately no revoke control here; retiring a link
 * is an operator act on shares.json, not a per-click API.
 */
const props = defineProps<{
  modelValue: boolean;
  /** The server's public path, e.g. "/share/abc…". */
  path?: string;
}>();

const emit = defineEmits<{
  "update:modelValue": [boolean];
}>();

const { t } = useI18n();
const copied = ref(false);
const urlInput = ref<HTMLInputElement | null>(null);

const open = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit("update:modelValue", v),
});

const url = computed(() => (props.path ? location.origin + props.path : ""));

watch(open, (v) => {
  if (v) copied.value = false;
});

async function copy() {
  if (!url.value) return;
  try {
    await navigator.clipboard.writeText(url.value);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1500);
  } catch {
    // Clipboard access can be denied; the input stays selectable as the fallback.
  }
}
</script>

<template>
  <el-dialog v-model="open" :title="t('shareDialog.title')" width="520px">
    <div class="share-body">
      <p class="hint">{{ t("shareDialog.hint") }}</p>
      <div class="row">
        <input ref="urlInput" class="url" :value="url" readonly @focus="urlInput?.select()" />
        <el-button type="primary" @click="copy">
          {{ copied ? t("common.copied") : t("common.copy") }}
        </el-button>
      </div>
      <p class="warn">{{ t("shareDialog.noLogin") }}</p>
    </div>
    <template #footer>
      <a v-if="url" class="open" :href="url" target="_blank" rel="noopener">{{
        t("shareDialog.open")
      }}</a>
    </template>
  </el-dialog>
</template>

<style scoped lang="scss">
.share-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.hint {
  margin: 0;
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.row {
  display: flex;
  gap: 8px;
}

.url {
  flex: 1;
  min-width: 0;
  font: 12.5px/1.5 var(--pg-mono);
  padding: 7px 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-primary);
}

.warn {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.open {
  font-size: 13px;
  color: var(--el-color-primary);
  text-underline-offset: 2px;
}
</style>
