<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { EditorState, Plugin, TextSelection, type Transaction } from "prosemirror-state";
import { EditorView, type NodeView } from "prosemirror-view";
import { baseKeymap, splitBlock } from "prosemirror-commands";
import { closeHistory, history, redo, undo } from "prosemirror-history";
import { keymap } from "prosemirror-keymap";
import { useI18n } from "vue-i18n";
import { composerDocument, composerDocumentParts, composerTextSelection, freshComposerSlice, inlineComposerSchema } from "@/agent/inlineComposerModel";
import { completeInlineComposerSlash, inlineComposerSlashQuery } from "@/agent/inlineComposerSuggestions";
import { composerDisplayText, composerOccurrenceID, elementComposerPart, fileComposerPart, normalizeComposerParts, type ComposerElementPart, type ComposerFilePart, type ComposerPart, type ComposerReferencePart, type ComposerSelection } from "@/agent/composerParts";
import { parsePickedCached, type ElementPick } from "@/agent/elementPicker";
import { citeFaviconKey } from "@/agent/citeFavicon";
import { elementPageFavicon } from "@/agent/elementPageIcon";
import { fileIcon } from "./fileIcons";
import { starterIconFor } from "./starterIcons";
import { uiIconPaths } from "@/agent/uiIcons";
import { snapshotComposerElementIcons } from "@/agent/orderedInput";
import "./commandToken.css";
import "prosemirror-view/style/prosemirror.css";

const props = withDefaults(defineProps<{
  modelValue: ComposerPart[];
  placeholder?: string;
  disabled?: boolean;
  ariaLabel?: string;
  /** Forms edit multiline prompts; the main conversation submits on Enter. */
  submitOnEnter?: boolean;
}>(), { placeholder: "", disabled: false, ariaLabel: "", submitOnEnter: true });
const emit = defineEmits<{
  "update:modelValue": [parts: ComposerPart[]];
  submit: [];
  keydown: [event: KeyboardEvent];
  focus: [event: FocusEvent];
  blur: [event: FocusEvent];
  selectionChange: [selection: ComposerSelection];
  pasteFiles: [files: File[]];
  dropFiles: [files: File[]];
  activateReference: [part: ComposerReferencePart];
  compositionstart: [event: CompositionEvent];
  compositionend: [event: CompositionEvent];
}>();
const { locale } = useI18n();
const faviconArchive = inject(citeFaviconKey, null);
const host = ref<HTMLDivElement>();
const refreshReferences = new Set<() => void>();
let view: EditorView | undefined;
let pendingParts: ComposerPart[] | undefined;
let pendingMetadataOnly = false;
let mounted = true;
let iconSnapshotEpoch = 0;
const isChinese = () => locale.value.startsWith("zh");
const removeLabel = (part: ComposerReferencePart) => isChinese() ? `移除${part.label}` : `Remove ${part.label}`;

function currentSelection(): ComposerSelection {
  return view ? { from: view.state.selection.from, to: view.state.selection.to } : { from: 1, to: 1 };
}
function setSelection(selection: ComposerSelection) {
  if (!view) return;
  const maximum = view.state.doc.content.size;
  const from = Math.max(1, Math.min(selection.from, maximum - 1));
  const to = Math.max(1, Math.min(selection.to, maximum - 1));
  view.dispatch(view.state.tr.setSelection(TextSelection.between(view.state.doc.resolve(from), view.state.doc.resolve(to))));
}
function focus() { view?.focus(); }
function setTextSelection(from: number, to = from) {
  if (view) setSelection(composerTextSelection(view.state.doc, from, to));
}
function syncAttributes() {
  if (!view) return;
  view.dom.setAttribute("data-empty", String(view.state.doc.textContent.length === 0 && !composerDocumentParts(view.state.doc).some((part) => part.type !== "text")));
  view.dom.setAttribute("data-placeholder", props.placeholder);
  view.dom.setAttribute("aria-label", props.ariaLabel || props.placeholder || (isChinese() ? "消息，支持引用排列" : "Message with ordered references"));
  view.dom.setAttribute("aria-disabled", String(props.disabled));
  view.dom.querySelectorAll<HTMLButtonElement>(".inline-reference-remove").forEach((button) => { button.disabled = props.disabled; });
}
function insertReference(part: ComposerReferencePart) {
  if (!view || props.disabled) return;
  const atom = inlineComposerSchema.nodes.reference.create({ part: { ...part, id: composerOccurrenceID() } });
  view.dispatch(view.state.tr.replaceSelectionWith(atom).scrollIntoView());
  view.focus();
}
/** User edits to one snapshot are undoable without rebuilding other atoms. */
function replaceReference(part: ComposerReferencePart, expected: string): boolean {
  if (!view || props.disabled) return false;
  let position: number | undefined;
  view.state.doc.descendants((node, pos) => {
    if (node.type.name === "reference" && node.attrs.part.id === part.id && JSON.stringify(node.attrs.part) === expected) position = pos;
  });
  if (position === undefined) return false;
  view.dispatch(closeHistory(view.state.tr.setNodeMarkup(position, undefined, { part: JSON.parse(JSON.stringify(part)) })));
  return true;
}
function insertElement(value: ElementPick | ComposerElementPart) {
  insertReference("type" in value ? value : elementComposerPart(value));
}
function insertAttachment(value: Omit<ComposerFilePart, "type" | "id"> | ComposerFilePart) {
  insertReference("type" in value ? value : fileComposerPart(value));
}
function insertParts(parts: ComposerPart[]) {
  if (!view || props.disabled) return;
  const doc = composerDocument(parts.map(part => part.type === "text" ? { ...part } : { ...part, id: composerOccurrenceID() }));
  view.dispatch(view.state.tr.replaceSelection(doc.slice(1, doc.content.size - 1)).scrollIntoView());
  view.focus();
}
function insertText(text: string) {
  if (!view || props.disabled) return;
  const doc = composerDocument([{ type: "text", text }]);
  view.dispatch(view.state.tr.replaceSelection(doc.slice(1, doc.content.size - 1)).scrollIntoView());
  view.focus();
}
function getSlashQuery(): { text: string; from: number; to: number } | null {
  return view ? inlineComposerSlashQuery(view.state) : null;
}
function completeSlash(value: string, skillName?: string) {
  if (!view || props.disabled) return false;
  const tr = completeInlineComposerSlash(view.state, value, skillName);
  if (!tr) return false;
  view.dispatch(tr.scrollIntoView()); view.focus(); return true;
}
function sameStructure(left: ComposerPart[], right: ComposerPart[]): boolean {
  return left.length === right.length && left.every((part, index) => {
    const other = right[index];
    return part.type === "text" ? other.type === "text" && part.text === other.text : other.type === part.type && part.id === other.id;
  });
}
function applyExternal(parts: ComposerPart[]) {
  if (!view) return;
  const next = normalizeComposerParts(parts);
  const current = composerDocumentParts(view.state.doc);
  if (JSON.stringify(current) === JSON.stringify(next)) return;
  const metadataOnly = sameStructure(current, next);
  if (view.composing) { pendingParts = next; pendingMetadataOnly = metadataOnly; return; }
  if (metadataOnly) {
    // Upload metadata does not replace the document: earlier text undo and the
    // browser's live composition ranges must retain their original positions.
    const references = new Map(next.filter((part) => part.type !== "text").map((part) => [part.id, part]));
    let transaction = view.state.tr;
    view.state.doc.descendants((node, pos) => {
      if (node.type.name !== "reference") return;
      const part = references.get((node.attrs.part as ComposerReferencePart).id);
      if (part && JSON.stringify(part) !== JSON.stringify(node.attrs.part)) transaction = transaction.setNodeMarkup(pos, undefined, { part });
    });
    if (transaction.docChanged) view.dispatch(transaction.setMeta("addToHistory", false));
    return;
  }
  const selection = currentSelection();
  const transaction = view.state.tr.replaceWith(0, view.state.doc.content.size, composerDocument(next).content).setMeta("addToHistory", false);
  view.dispatch(transaction);
  setSelection(selection);
}
async function snapshotEditorIcons() {
  if (!view || props.disabled) return;
  const epoch = ++iconSnapshotEpoch;
  const before = composerDocumentParts(view.state.doc);
  const frozen = await snapshotComposerElementIcons(before, faviconArchive);
  if (!mounted || !view || epoch !== iconSnapshotEpoch) return;
  const snapshots = new Map(frozen.filter((part): part is ComposerElementPart => part.type === "element" && !!part.favicon).map(part => [part.id, part]));
  const next = composerDocumentParts(view.state.doc).map(part => {
    const saved = part.type === "element" ? snapshots.get(part.id) : undefined;
    return saved?.type === "element" && part.type === "element" && saved.text === part.text && !part.favicon ? { ...part, favicon: saved.favicon, ...(part.pick ? { pick: { ...part.pick, icon: saved.favicon } } : {}) } : part;
  });
  applyExternal(next);
}
function flushPendingParts() {
  if (!mounted || !view || !pendingParts) return;
  const pending = pendingParts;
  pendingParts = undefined;
  if (pendingMetadataOnly) {
    const references = new Map(pending.filter((part) => part.type !== "text").map((part) => [part.id, part]));
    applyExternal(composerDocumentParts(view.state.doc).map((part) => part.type === "text" ? part : references.get(part.id) ?? part));
  } else applyExternal(pending);
}

function referenceView(initial: import("prosemirror-model").Node, editor: EditorView, getPos: () => number | undefined): NodeView {
  let part = initial.attrs.part as ComposerReferencePart;
  const dom = document.createElement("span");
  dom.contentEditable = "false";
  dom.draggable = true;
  const icon = document.createElement("span");
  icon.className = "inline-reference-icon";
  icon.setAttribute("aria-hidden", "true");
  const primary = document.createElement("span");
  const copy = document.createElement("span");
  copy.className = "command-token-copy";
  const type = document.createElement("span");
  type.className = "command-token-type";
  const page = document.createElement("span");
  page.className = "inline-reference-page";
  const separator = document.createElement("span");
  separator.className = "inline-reference-separator";
  separator.textContent = "·";
  const dataBadge = document.createElement("span");
  dataBadge.className = "inline-reference-data";
  dataBadge.setAttribute("aria-hidden", "true");
  dataBadge.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><path d="M18 20V10M12 20V4M6 20v-6"/></svg>';
  const label = document.createElement("span");
  const remove = document.createElement("button");
  remove.type = "button";
  remove.tabIndex = -1;
  remove.innerHTML = '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><path d="m6 6 8 8m0-8-8 8" /></svg>';
  copy.append(icon, type, dataBadge, page, separator, label);
  primary.append(copy);
  dom.append(primary, remove);
  const update = () => {
    const skill = part.type === "skill";
    dom.className = `inline-reference ${part.type}${skill ? " command-token-surface command-token-editable" : ""}${part.type === "file" && part.status ? ` ${part.status}` : ""}${props.disabled ? " disabled" : ""}`;
    primary.className = `inline-reference-primary${skill ? " command-token-primary" : ""}`;
    label.className = `inline-reference-label${skill ? " command-token-name" : ""}`;
    remove.className = `inline-reference-remove${skill ? " command-token-remove" : ""}`;
    dom.dataset.pogoComposerPart = JSON.stringify(part);
    dom.dataset.occurrenceId = part.id;
    type.hidden = !skill;
    type.textContent = skill ? (isChinese() ? "技能" : "Skill") : "";
    page.hidden = separator.hidden = true;
    dataBadge.hidden = true;
    icon.hidden = false;
    if (skill) icon.innerHTML = '<svg class="command-token-icon" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m12 3 9 5v8l-9 5-9-5V8l9-5Zm-9 5 9 5 9-5m-9 5v8M7.5 5.5l9 5"/></svg>';
    else if (part.type === "element") {
      const card = parsePickedCached(part.text).cards[0];
      const title = part.title || part.pick?.pageTitle || card?.title;
      const favicon = elementPageFavicon(part.pick?.pageURL || card?.url, part.favicon || part.pick?.icon, faviconArchive);
      const globe = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>';
      icon.innerHTML = globe;
      if (favicon) {
        const image = document.createElement("img");
        image.src = favicon; image.alt = "";
        image.addEventListener("error", () => { if (image.parentNode === icon) icon.innerHTML = globe; }, { once: true });
        icon.replaceChildren(image);
      }
      if (title && title !== part.label) {
        page.hidden = separator.hidden = false;
        page.textContent = title;
      }
      dataBadge.hidden = !part.pick?.hasData && !card?.data;
      dom.classList.toggle("has-data", !dataBadge.hidden);
    } else if (part.type === "office") {
      const glyph = fileIcon(part.office.title);
      icon.innerHTML = `<svg width="16" height="16" viewBox="0 0 ${glyph.width} ${glyph.height}">${glyph.body}</svg>`;
    } else if (part.type === "shortcut") {
      const glyph = starterIconFor(part.icon);
      icon.hidden = !glyph;
      icon.innerHTML = glyph ? `<svg width="16" height="16" viewBox="0 0 ${glyph.width} ${glyph.height}">${glyph.body}</svg>` : "";
    } else {
      const glyph = fileIcon(part.label);
      // SVG comes exclusively from the bundled, trusted icon set. File names
      // and page metadata are always text, never interpolated as markup.
      icon.innerHTML = `<svg width="22" height="22" viewBox="0 0 ${glyph.width} ${glyph.height}">${glyph.body}</svg>`;
    }
    label.textContent = part.label;
    dom.title = part.type === "element" ? [part.title, part.pick?.preview || part.label].filter(Boolean).join("\n") : part.type === "shortcut" ? part.title : part.type === "skill" ? `/skill:${part.name}` : part.type === "office" ? part.label : part.error || part.path || part.label;
    remove.setAttribute("aria-label", removeLabel(part));
    remove.title = removeLabel(part);
    remove.disabled = props.disabled;
  };
  update();
  refreshReferences.add(update);
  remove.addEventListener("mousedown", (event) => event.preventDefault());
  remove.addEventListener("click", (event) => {
    event.preventDefault(); event.stopPropagation();
    if (props.disabled) return;
    const pos = getPos();
    if (typeof pos === "number") editor.dispatch(editor.state.tr.delete(pos, pos + 1).scrollIntoView());
    editor.focus();
  });
  dom.addEventListener("click", (event) => {
    if (!props.disabled && !remove.contains(event.target as Node)) emit("activateReference", { ...part });
  });
  return {
    dom,
    update(node) {
      if (node.type.name !== "reference") return false;
      part = node.attrs.part as ComposerReferencePart;
      update(); return true;
    },
    stopEvent: (event) => remove.contains(event.target as Node),
    ignoreMutation: () => true,
    selectNode: () => dom.classList.add("ProseMirror-selectednode"),
    deselectNode: () => dom.classList.remove("ProseMirror-selectednode"),
    destroy: () => { refreshReferences.delete(update); },
  };
}

onMounted(() => {
  if (!host.value) return;
  view = new EditorView(host.value, {
    state: EditorState.create({
      schema: inlineComposerSchema,
      doc: composerDocument(props.modelValue),
      plugins: [
        history(),
        keymap({ "Mod-z": undo, "Mod-y": redo, "Shift-Mod-z": redo, "Shift-Enter": splitBlock }),
        keymap(baseKeymap),
        new Plugin({
          appendTransaction(_transactions, _old, state) {
            const seen = new Set<string>();
            let transaction: Transaction | null = null;
            state.doc.descendants((node, pos) => {
              if (node.type.name !== "reference") return;
              const part = node.attrs.part as ComposerReferencePart;
              if (!part.id || seen.has(part.id)) {
                transaction ??= state.tr;
                transaction.setNodeMarkup(pos, undefined, { part: { ...part, id: composerOccurrenceID() } });
              } else seen.add(part.id);
            });
            return transaction ? (transaction as Transaction).setMeta("addToHistory", false) : null;
          },
        }),
      ],
    }),
    editable: () => !props.disabled,
    attributes: { role: "textbox", "aria-multiline": "true", class: "inline-composer-document" },
    nodeViews: { reference: referenceView },
    dispatchTransaction(transaction) {
      if (!view) return;
      const result = view.state.applyTransaction(transaction);
      view.updateState(result.state);
      syncAttributes();
      if (result.transactions.some((item) => item.docChanged)) emit("update:modelValue", composerDocumentParts(view.state.doc));
      if (transaction.selectionSet || transaction.docChanged) emit("selectionChange", currentSelection());
    },
    transformPasted: freshComposerSlice,
    handleKeyDown(editor, event) {
      emit("keydown", event);
      if (event.defaultPrevented) return true;
      if (props.disabled) return true;
      if (event.isComposing || editor.composing || event.keyCode === 229) return false;
      if (props.submitOnEnter && event.key === "Enter" && !event.shiftKey) {
        event.preventDefault(); emit("submit"); return true;
      }
      return false;
    },
    handlePaste(_editor, event) {
      const files = Array.from(event.clipboardData?.files ?? []);
      if (!files.length || props.disabled) return false;
      event.preventDefault(); event.stopPropagation(); emit("pasteFiles", files); return true;
    },
    handleDrop(editor, event) {
      const files = Array.from(event.dataTransfer?.files ?? []);
      if (!files.length || props.disabled) return false;
      event.preventDefault(); event.stopPropagation();
      const coordinates = editor.posAtCoords({ left: event.clientX, top: event.clientY });
      if (coordinates) setSelection({ from: coordinates.pos, to: coordinates.pos });
      emit("dropFiles", files); return true;
    },
    handleDOMEvents: {
      focus: (_editor, event) => { emit("focus", event); return false; },
      blur: (_editor, event) => { emit("blur", event); return false; },
      compositionstart: (_editor, event) => { emit("compositionstart", event); return false; },
      compositionend: (_editor, event) => {
        emit("compositionend", event);
        setTimeout(flushPendingParts, 30);
        return false;
      },
    },
  });
  syncAttributes();
  void snapshotEditorIcons();
});
watch(() => props.modelValue, parts => { applyExternal(parts); void snapshotEditorIcons(); }, { deep: true });
watch(uiIconPaths, () => refreshReferences.forEach(refresh => refresh()));
watch(() => [props.disabled, props.placeholder, props.ariaLabel, locale.value], () => {
  view?.setProps({ editable: () => !props.disabled });
  refreshReferences.forEach((refresh) => refresh());
  syncAttributes();
});
onBeforeUnmount(() => { mounted = false; view?.destroy(); view = undefined; });
defineExpose({
  focus, insertReference, replaceReference, insertElement, insertAttachment, insertParts, insertText, setSelection, setTextSelection, getSlashQuery, completeSlash,
  getSelection: currentSelection,
  getParts: () => view ? composerDocumentParts(view.state.doc) : normalizeComposerParts(props.modelValue),
  getText: () => composerDisplayText(view ? composerDocumentParts(view.state.doc) : props.modelValue),
});
</script>

<template>
  <div ref="host" class="inline-composer" :class="{ disabled }" data-testid="inline-composer" />
</template>

<style scoped>
.inline-composer { position: relative; width: 100%; min-width: 0; font: inherit; color: inherit; }
.inline-composer :deep(.inline-composer-document) { position: relative; box-sizing: border-box; min-height: 90px; max-height: min(40vh, 360px); padding: 0; overflow-y: auto; overflow-wrap: anywhere; white-space: pre-wrap; outline: none; line-height: 1.7; font: inherit; color: inherit; }
.inline-composer :deep(.inline-composer-document[data-empty="true"]::before) { position: absolute; top: 0; left: 0; pointer-events: none; color: var(--text-muted, #8b8882); content: attr(data-placeholder); }
.inline-composer :deep(.inline-composer-document p) { margin: 0; min-height: 1.7em; }
.inline-composer :deep(.inline-reference) { display: inline-flex; align-items: center; box-sizing: border-box; max-width: min(calc(100% - 6px), 420px); margin: 2px 3px; vertical-align: middle; user-select: all; cursor: pointer; white-space: nowrap; }
.inline-composer :deep(.inline-reference-primary) { min-width: 0; }
.inline-composer :deep(.inline-reference:not(.skill)) { gap: 4px; padding: 2px 4px 2px 8px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; background: var(--el-fill-color-lighter); color: var(--el-text-color-regular); font-size: 12px; line-height: 20px; }
.inline-composer :deep(.inline-reference:not(.skill):hover) { background: var(--el-fill-color); border-color: var(--el-color-primary-light-5); }
.inline-composer :deep(.inline-reference:not(.skill) .command-token-copy) { gap: 4px; }
.inline-composer :deep(.inline-reference-label) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.inline-composer :deep(.inline-reference-page) { min-width: 0; max-width: 140px; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
.inline-composer :deep(.inline-reference-separator) { color: var(--el-text-color-secondary); }
.inline-composer :deep(.inline-reference-icon) { flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; }
.inline-composer :deep(.inline-reference-icon[hidden]) { display:none; }
.inline-composer :deep(.inline-reference-icon img) { display: block; width: 13px; height: 13px; object-fit: contain; }
.inline-composer :deep(.inline-reference.file) { padding-left: 2px; }
.inline-composer :deep(.inline-reference.file .inline-reference-icon) { margin: 4px; }
.inline-composer :deep(.inline-reference.element .inline-reference-icon) { color: var(--el-text-color-secondary); }
.inline-composer :deep(.inline-reference.element .inline-reference-label) { font-family: var(--el-font-family-mono, monospace); }
.inline-composer :deep(.inline-reference-data) { flex: none; color: var(--el-color-success); }
.inline-composer :deep(.inline-reference.has-data) { border-color: color-mix(in srgb, var(--el-color-success) 45%, var(--el-border-color-lighter)); }
.inline-composer :deep(.inline-reference:not(.skill) .inline-reference-remove) { flex: 0 0 auto; display: inline-grid; place-items: center; width: 18px; height: 18px; margin: 0; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--el-text-color-secondary); cursor: pointer; }
.inline-composer :deep(.inline-reference:not(.skill) .inline-reference-remove svg) { width: 16px; height: 16px; pointer-events: none; }
.inline-composer :deep(.inline-reference:not(.skill) .inline-reference-remove:hover) { background: var(--el-fill-color-dark); color: var(--el-text-color-primary); }
.inline-composer :deep(.inline-reference.ProseMirror-selectednode) { outline: 2px solid var(--command-token-color, var(--el-color-primary)); outline-offset: 1px; }
.inline-composer :deep(.inline-reference.uploading) { opacity: .65; }
.inline-composer :deep(.inline-reference.error) { color: var(--danger, #be4b42); border-color: currentColor; }
.inline-composer.disabled { opacity: .65; }
@media (pointer: coarse) { .inline-composer :deep(.inline-reference:not(.skill) .inline-reference-remove) { width: 28px; height: 28px; } }
.inline-reference.shortcut { border-color:color-mix(in srgb,var(--el-color-primary) 28%,transparent); background:color-mix(in srgb,var(--el-color-primary) 8%,transparent); color:var(--el-color-primary); }
</style>
