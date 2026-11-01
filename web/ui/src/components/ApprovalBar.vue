<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { PendingGate } from "@/api/types";
import GateCard from "./GateCard.vue";

const props = defineProps<{ gates: PendingGate[] }>();
const emit = defineEmits<{
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string]; thaw: [string];
}>();
const { t } = useI18n();
const selected = ref("");
watch(() => props.gates.map(g => g.gate_id), ids => {
  if (!ids.includes(selected.value)) selected.value = ids[0] ?? "";
}, { immediate: true });
const index = computed(() => Math.max(0, props.gates.findIndex(g => g.gate_id === selected.value)));
const gate = computed(() => props.gates[index.value]);
function move(by: number) { selected.value = props.gates[(index.value + by + props.gates.length) % props.gates.length]!.gate_id; }
</script>

<template>
  <section v-if="gate" class="approval-bar" role="region" :aria-label="t('gateCard.pendingApprovals')" data-testid="approval-bar">
    <div class="approval-heading">
      <span class="approval-dot" aria-hidden="true" />
      <strong>{{ t('gateCard.pendingApprovals') }}</strong>
      <span v-if="gate.origin" class="approval-origin" :title="gate.origin">{{ t('gateCard.delegatedOrigin') }} · {{ gate.origin }}</span>
      <nav v-if="gates.length > 1" class="approval-pager" :aria-label="t('gateCard.pendingApprovals')">
        <button type="button" :aria-label="t('gateCard.previous')" @click="move(-1)">‹</button>
        <span>{{ index + 1 }} / {{ gates.length }}</span>
        <button type="button" :aria-label="t('gateCard.next')" @click="move(1)">›</button>
      </nav>
    </div>
    <GateCard :key="gate.gate_id" :gate="gate" :live="true" compact
      @decide="emit('decide', { gateId: gate!.gate_id, ...$event })"
      @freeze="emit('freeze', gate!.gate_id)" @thaw="emit('thaw', gate!.gate_id)" />
  </section>
</template>

<style scoped lang="scss">
.approval-bar { flex: 0 0 auto; min-height: 0; max-height: min(30dvh, 260px); overflow-y: auto; overscroll-behavior: contain; padding: 8px; border: 1px solid var(--el-color-warning-light-5); border-radius: 12px; background: var(--el-bg-color); box-shadow: var(--el-box-shadow-lighter); }
.approval-heading { display: flex; align-items: center; gap: 7px; padding: 0 3px 7px; font-size: 12px; color: var(--el-text-color-primary); }
.approval-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--el-color-warning); flex: 0 0 auto; }
.approval-origin { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--el-text-color-secondary); font-size: 11px; }
.approval-pager { display: flex; align-items: center; gap: 6px; margin-left: auto; color: var(--el-text-color-secondary); font-variant-numeric: tabular-nums; white-space: nowrap; }
.approval-pager button { width: 24px; height: 23px; padding: 0; border: 1px solid var(--el-border-color); border-radius: 6px; color: inherit; background: var(--el-bg-color); cursor: pointer; }
</style>
