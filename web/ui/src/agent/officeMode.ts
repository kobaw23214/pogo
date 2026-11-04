export type OfficeEditorMode = "view" | "edit";

export function withOfficeEditorMode(config: Record<string, unknown>, viewOnly: boolean): Record<string, unknown> {
  const editorConfig = (config.editorConfig ?? {}) as Record<string, unknown>;
  const customization = (editorConfig.customization ?? {}) as Record<string, unknown>;
  return {
    ...config,
    editorConfig: {
      ...editorConfig,
      mode: viewOnly ? "view" : "edit",
      customization: { ...customization, autosave: !viewOnly },
    },
  };
}
