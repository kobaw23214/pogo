<script lang="ts">
/** Vetted brand palette. Agent colors outside this set fall back to the first entry. */
export const AVATAR_COLORS = ['#5965e8', '#e8734a', '#2fa37c', '#d9a036', '#c65d8f', '#4a90c2'] as const;
/** Consistent glyph set for agents. Unknown names fall back to "spark". */
export const AVATAR_ICONS = ['spark', 'search', 'pen', 'code', 'compass', 'chart'] as const;
export type AvatarIcon = (typeof AVATAR_ICONS)[number];
</script>

<script setup lang="ts">
import { computed } from 'vue';
import { Icon } from '@iconify/vue';
import { starterIconFor } from './starterIcons';
const customGlyph = computed(() => props.icon.startsWith('ui_') ? starterIconFor(props.icon) : null);

const props = withDefaults(
  defineProps<{
    icon: string;
    color: string;
    size?: number;
  }>(),
  { size: 44 },
);

const resolvedColor = computed(() =>
  (AVATAR_COLORS as readonly string[]).includes(props.color) ? props.color : AVATAR_COLORS[0],
);
const resolvedIcon = computed<AvatarIcon>(() =>
  (AVATAR_ICONS as readonly string[]).includes(props.icon) ? (props.icon as AvatarIcon) : 'spark',
);
const glyph = computed(() => Math.max(12, Math.round(props.size * 0.56)));
const corner = computed(() => Math.max(8, Math.round(props.size * 0.3)));
const boxStyle = computed(() => ({
  width: `${props.size}px`,
  height: `${props.size}px`,
  color: resolvedColor.value,
  background: `${resolvedColor.value}14`,
  borderRadius: `${corner.value}px`,
}));
</script>

<template>
  <span class="agent-avatar" :style="boxStyle" aria-hidden="true">
    <Icon v-if="customGlyph" :icon="customGlyph" :width="glyph" :height="glyph" />
    <svg
      v-else
      :width="glyph"
      :height="glyph"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.8"
      stroke-linecap="round"
      stroke-linejoin="round"
    >
      <template v-if="resolvedIcon === 'spark'">
        <path d="M12 4.2c.55 3.9 2 5.35 5.9 5.9-3.9.55-5.35 2-5.9 5.9-.55-3.9-2-5.35-5.9-5.9 3.9-.55 5.35-2 5.9-5.9Z" />
        <circle cx="18.6" cy="5.6" r="1.15" fill="currentColor" stroke="none" />
      </template>
      <template v-else-if="resolvedIcon === 'search'">
        <circle cx="11" cy="11" r="6.2" />
        <path d="m15.7 15.7 4.1 4.1" />
      </template>
      <template v-else-if="resolvedIcon === 'pen'">
        <path d="M4.8 19.2 5.9 15 15.7 5.2a2.05 2.05 0 0 1 2.9 0l.2.2a2.05 2.05 0 0 1 0 2.9L9 18.1l-4.2 1.1Z" />
        <path d="m13.9 7 3.1 3.1" />
      </template>
      <template v-else-if="resolvedIcon === 'code'">
        <path d="m8.6 8.2-4 3.8 4 3.8" />
        <path d="m15.4 8.2 4 3.8-4 3.8" />
        <path d="M13.1 5.6 10.9 18.4" />
      </template>
      <template v-else-if="resolvedIcon === 'compass'">
        <circle cx="12" cy="12" r="8.1" />
        <path d="m14.9 9.1-1.6 4.2-4.2 1.6 1.6-4.2 4.2-1.6Z" />
      </template>
      <template v-else>
        <path d="M4.5 19.5h15" />
        <path d="M8 16.5v-4" />
        <path d="M12 16.5V8.5" />
        <path d="M16 16.5v-6.5" />
      </template>
    </svg>
  </span>
</template>

<style scoped>
.agent-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
}
.agent-avatar svg {
  display: block;
}
</style>
