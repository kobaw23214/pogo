<script setup lang="ts">
import { computed } from "vue";

// The one place that knows settings can render two ways: the legacy
// fullscreen dialog (attrs spread onto the dialog box, teleported to body)
// or an inline section that fills a Studio module's content area. Everything
// inside — body and the named footer controls — is the caller's real
// implementation, passed through untouched, so the two modes cannot fork.
const props = defineProps<{
  modelValue: boolean;
  embedded?: boolean;
  title?: string;
  beforeClose?: (done: () => void) => void;
  footerVisible?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [boolean] }>();

const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit("update:modelValue", v),
});
</script>

<template>
  <el-dialog
    v-if="!embedded"
    v-model="visible"
    :title="title"
    fullscreen
    append-to-body
    :before-close="beforeClose"
    class="settings-dialog"
  >
    <slot />
    <template #footer><slot name="footer" /></template>
  </el-dialog>

  <!-- Inline Studio module content: same .settings-dialog name so every
       existing token-based rule in SettingsDialog.vue keeps applying, plus
       the chrome the dialog used to provide — a definite height and a floor
       under the same footer controls. No overlay, no title, no close X. -->
  <section v-else class="settings-dialog settings-dialog-embed">
    <div class="settings-dialog-embed-body"><slot /></div>
    <footer v-if="$slots.footer && footerVisible !== false" class="settings-dialog-embed-footer">
      <slot name="footer" />
    </footer>
  </section>
</template>

<style lang="scss">
/* Unscoped, matching SettingsDialog's sheet: the embedded branch renders
   inline (no teleport), but it deliberately reuses the .settings-dialog
   class so the shared descendant rules (split, nav, panes, sections) apply
   in both modes. Only the dialog-chrome equivalents live here. */
.settings-dialog-embed {
  height: 100%;
  min-height: 0;
  overflow: hidden;
  border-radius: 0;
  background: var(--el-bg-color);

  /* Mirrors .settings-dialog .el-dialog__body: the pane scrolls, not this
     box, and .settings-split's height: 100% resolves against it. */
  .settings-dialog-embed-body {
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  /* The same floor the dialog footer provided, so named footer controls
     (save buttons) stay reachable while the pane scrolls. Adjacent
     .el-button spacing comes from Element Plus's own sibling rule. */
  .settings-dialog-embed-footer {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    padding: 10px 16px;
    border-top: 1px solid var(--el-border-color-lighter);
    background: var(--el-bg-color);
  }
  /* Narrow screens need no override here: the media rules in
     SettingsDialog.vue reflow the split itself, exactly as inside the
     fullscreen dialog. */
}

/* Studio already has the workspace sidebar. Use a compact tab row for a
   module's subsections instead of placing a second sidebar beside it. */
.settings-dialog.settings-dialog-embed {
  .settings-split { flex-direction: column; gap: 0; }
  .settings-nav {
    flex: 0 0 auto;
    flex-direction: row;
    align-items: stretch;
    gap: 6px;
    padding: 0 20px;
    overflow-x: auto;
    overflow-y: hidden;
    border-bottom: 1px solid var(--el-border-color-lighter);

    .nav-group { display: none; }
    .nav-item {
      flex: 0 0 auto;
      margin: 0;
      padding: 12px 10px;
      border-radius: 0;
      border-bottom: 2px solid transparent;
      white-space: nowrap;
      background: transparent;
    }
    .nav-item.on {
      background: transparent;
      border-bottom-color: var(--el-color-primary);
      color: var(--el-text-color-primary);
    }
    /* Sheet-level actions pinned to the tab row's right end — this embedded
       page has no footer bar for the MCP-serve sheet, so its save/export
       buttons live here. Sticky, so a narrow window's horizontal nav scroll
       can never hide them. */
    .nav-actions {
      margin-left: auto;
      flex: 0 0 auto;
      display: flex;
      align-items: center;
      gap: 8px;
      position: sticky;
      right: 0;
      padding: 8px 0 8px 16px;
      background: var(--el-bg-color);
    }
  }
  .settings-pane { height: auto; min-height: 0; padding: 24px; }

  @media (max-width: 720px) {
    .settings-nav { padding: 0 12px; }
    .settings-pane { padding: 18px 14px; }
  }
}
</style>
