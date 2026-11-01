<script setup lang="ts">
import { computed } from "vue";
import ActivityList from "./ActivityList.vue";
import type { TurnItem } from "@/agent/timeline";
import type { Exchange } from "@/agent/exchanges";
import type { OpenSheetRequest } from "./dockSheets";

const props = defineProps<{
  turn: TurnItem;
  runActive: boolean;
  skills?: { name: string; path: string }[];
  cwd?: string;
  todoPinned?: boolean;
  workFolded?: boolean;
  serverOffset?: number | null;
}>();
const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string]; thaw: [string];
  "open-sheet": [OpenSheetRequest];
  "open-file": [{ path: string; name: string }];
}>();

// Standalone panes retain the TurnCard API and share the main activity tree.
const exchange = computed<Exchange>(() => ({
  id: props.turn.id,
  turns: [props.turn], answer: props.turn.text ? props.turn : undefined,
  notices: [], finished: !props.runActive,
  stats: { calls: props.turn.calls.length, failed: 0, files: 0, tools: {} },
}));
</script>

<template>
  <ActivityList
    :exchange="exchange"
    :run-active="runActive"
    :work-folded="workFolded"
    :skills="skills"
    :cwd="cwd"
    :todo-pinned="todoPinned"
    :server-offset="serverOffset"
    @suggest="emit('suggest', $event)"
    @decide="emit('decide', $event)"
    @freeze="emit('freeze', $event)"
    @thaw="emit('thaw', $event)"
    @open-sheet="emit('open-sheet', $event)"
    @open-file="emit('open-file', $event)"
  />
</template>
