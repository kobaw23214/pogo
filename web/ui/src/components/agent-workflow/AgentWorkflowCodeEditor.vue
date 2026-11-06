<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { Compartment, EditorState, Prec, Transaction } from '@codemirror/state';
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view';
import { bracketMatching, HighlightStyle, indentOnInput, indentUnit, syntaxHighlighting, syntaxTree } from '@codemirror/language';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, snippetCompletion, startCompletion, type Completion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete';
import { javascript } from '@codemirror/lang-javascript';
import { python } from '@codemirror/lang-python';
import { tags } from '@lezer/highlight';
import type { AgentWorkflowCodeBinding, AgentWorkflowCodeSpec } from '@/api/agentWorkflows';

const props = defineProps<{ modelValue: string; language: AgentWorkflowCodeSpec['language']; inputs: Record<string, AgentWorkflowCodeBinding>; disabled?: boolean; label: string }>();
const emit = defineEmits<{ 'update:modelValue': [string] }>();
const { t, locale } = useI18n();
const text = computed(() => ({
  view: t("uiCoverage.codeEditor.view"),
  edit: t("uiCoverage.codeEditor.edit"),
  done: t("uiCoverage.codeEditor.done"),
  readonly: t("uiCoverage.codeEditor.readonly"),
  copy: t("uiCoverage.codeEditor.copy"),
  copied: t("uiCoverage.codeEditor.copied"),
  copyError: t("uiCoverage.codeEditor.copyError"),
  help: t("uiCoverage.codeEditor.help"),
  viewing: t("uiCoverage.codeEditor.viewing"),
  editing: t("uiCoverage.codeEditor.editing"),
  suggest: t("uiCoverage.codeEditor.suggest"),
  input: t("uiCoverage.codeEditor.input"),
  lines: t("uiCoverage.codeEditor.lines"),
}));
const host = ref<HTMLElement | null>(null), editing = ref(false), copied = ref(false), copyError = ref(false);
const canEdit = computed(() => editing.value && !props.disabled);
const lineCount = computed(() => props.modelValue.split(/\r\n|\r|\n/).length);
const mode = new Compartment(), languageMode = new Compartment(), lineEnding = new Compartment(), accessibility = new Compartment();
let view: EditorView | undefined, syncing = false, copiedTimer: ReturnType<typeof setTimeout> | undefined;

const colours = HighlightStyle.define([
  { tag: [tags.keyword, tags.modifier, tags.operatorKeyword], color: 'var(--pg-syn-keyword)' },
  { tag: [tags.string, tags.regexp], color: 'var(--pg-syn-string)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--pg-syn-number)' },
  { tag: [tags.typeName, tags.className], color: 'var(--pg-syn-type)' },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: 'var(--pg-syn-func)' },
  { tag: tags.comment, color: 'var(--pg-syn-comment)', fontStyle: 'italic' },
]);
const skin = EditorView.theme({
  '&': { color: 'var(--el-text-color-primary)', backgroundColor: 'var(--el-bg-color)', fontSize: '12px' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { overflow: 'auto', maxHeight: '440px', minHeight: '220px', fontFamily: 'ui-monospace, SFMono-Regular, Consolas, monospace', lineHeight: '1.7' },
  '.cm-content': { padding: '10px 0', caretColor: 'var(--el-color-primary)' },
  '.cm-line': { padding: '0 12px' },
  '.cm-gutters': { backgroundColor: 'var(--el-fill-color-extra-light)', color: 'var(--el-text-color-placeholder)', borderRight: '1px solid var(--el-border-color-lighter)' },
  '.cm-lineNumbers .cm-gutterElement': { minWidth: '24px', padding: '0 8px' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--el-fill-color-light)' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': { backgroundColor: 'var(--el-color-primary-light-8)' },
  '.cm-cursor': { borderLeftColor: 'var(--el-color-primary)' },
  '.cm-matchingBracket': { backgroundColor: 'var(--el-color-primary-light-9)', outline: '1px solid var(--el-color-primary-light-5)' },
  '.cm-tooltip': { border: '1px solid var(--el-border-color)', backgroundColor: 'var(--el-bg-color-overlay)', color: 'var(--el-text-color-primary)', borderRadius: '7px', boxShadow: 'var(--el-box-shadow-light)' },
  '.cm-tooltip-autocomplete': { maxWidth: 'min(420px, 80vw)' },
  '.cm-tooltip-autocomplete > ul > li': { padding: '5px 8px' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--el-color-primary-light-9)', color: 'var(--el-color-primary)' },
});
function editorMode() {
  return [EditorState.readOnly.of(!canEdit.value), EditorView.editable.of(canEdit.value), ...(canEdit.value ? [
    highlightActiveLine(), highlightActiveLineGutter(), autocompletion({ activateOnTyping: true, maxRenderedOptions: 30, defaultKeymap: false }),
    Prec.high(keymap.of([...completionKeymap, { key: 'Mod-Space', run: startCompletion }])),
  ] : [])];
}
function contentAttributes() {
  return EditorView.contentAttributes.of({ 'aria-label': props.label, 'aria-readonly': String(!canEdit.value), tabindex: '0', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' });
}
function separator(source: string) { return source.includes('\r\n') ? '\r\n' : source.includes('\r') && !source.includes('\n') ? '\r' : '\n'; }

function completeInputs(context: CompletionContext): CompletionResult | null {
  const bracket = context.matchBefore(/inputs\[(?:"[^"\n]*|'[^'\n]*)/);
  const keys = Object.entries(props.inputs);
  if (bracket) {
    const quote = bracket.text[7]!, prefix = bracket.text.slice(8);
    const closing = context.state.sliceDoc(context.pos, context.pos + 2);
    return {
      from: bracket.from, to: context.pos + (closing === `${quote}]` ? 2 : closing.startsWith(quote) ? 1 : 0), filter: false,
      options: keys.filter(([key]) => key.startsWith(prefix)).map(([key, binding]) => ({ label: `inputs[${JSON.stringify(key)}]`, type: 'property', detail: `${text.value.input} · ${binding.type}` })),
    };
  }
  const member = context.matchBefore(/inputs\.[$\w]*/);
  if (member && props.language === 'javascript') return {
    from: member.from + 7, validFor: /^[$\w]*$/,
    options: keys.filter(([key]) => /^[$A-Z_a-z][$\w]*$/.test(key)).map(([key, binding]) => ({ label: key, type: 'property', detail: `${text.value.input} · ${binding.type}` })),
  };
  if (/Comment|String/.test(syntaxTree(context.state).resolveInner(context.pos, -1).name)) return null;
  const word = context.matchBefore(/[$\w]*/);
  if (!word || !context.explicit && !word.text) return null;
  const main = props.language === 'python'
    ? snippetCompletion('def main(inputs):\n    return {"result": ${value}}', { label: 'main', type: 'function', detail: 'main(inputs)' })
    : snippetCompletion('function main(inputs) {\n  return { result: ${value} };\n}', { label: 'main', type: 'function', detail: 'main(inputs)' });
  const options: Completion[] = [main, { label: 'inputs', type: 'variable', detail: text.value.input }, ...keys.map(([key, binding]) => ({
    label: props.language === 'javascript' && /^[$A-Z_a-z][$\w]*$/.test(key) ? `inputs.${key}` : `inputs[${JSON.stringify(key)}]`,
    type: 'property', detail: `${text.value.input} · ${binding.type}`,
  }))];
  return { from: word.from, options, validFor: /^[$\w]*$/ };
}

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    lineNumbers(), drawSelection(), history(), indentOnInput(), bracketMatching(), closeBrackets(),
    EditorState.tabSize.of(4), indentUnit.of('    '),
    languageMode.of(props.language === 'python' ? python() : javascript()),
    EditorState.languageData.of(() => [{ autocomplete: completeInputs }]),
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap]),
    syntaxHighlighting(colours), skin, mode.of(editorMode()), accessibility.of(contentAttributes()), lineEnding.of(EditorState.lineSeparator.of(separator(props.modelValue))),
    EditorView.updateListener.of(update => {
      if (update.docChanged && canEdit.value && !syncing) emit('update:modelValue', update.state.sliceDoc());
    }),
  ] }) });
});
watch(() => props.modelValue, value => {
  copied.value = false; copyError.value = false;
  if (!view || value === view.state.sliceDoc()) return;
  syncing = true;
  try {
    if (view.state.lineBreak !== separator(value)) view.dispatch({ effects: lineEnding.reconfigure(EditorState.lineSeparator.of(separator(value))) });
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value }, annotations: Transaction.addToHistory.of(false) });
  }
  finally { syncing = false; }
});
watch(() => props.language, value => view?.dispatch({ effects: languageMode.reconfigure(value === 'python' ? python() : javascript()) }));
watch(() => props.disabled, disabled => { if (disabled) editing.value = false; });
watch([canEdit, () => props.label, locale], () => view?.dispatch({ effects: [mode.reconfigure(editorMode()), accessibility.reconfigure(contentAttributes())] }));
async function edit() { if (props.disabled) return; editing.value = true; await nextTick(); view?.focus(); }
function suggest() { if (canEdit.value && view) { view.focus(); startCompletion(view); } }
async function copy() {
  copied.value = false; copyError.value = false;
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(props.modelValue);
    else {
      const previous = document.activeElement as HTMLElement | null, area = document.createElement('textarea');
      area.value = props.modelValue; area.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0'; document.body.append(area);
      try { area.select(); if (!document.execCommand('copy')) throw new Error('clipboard'); }
      finally { area.remove(); previous?.focus(); }
    }
    copied.value = true; clearTimeout(copiedTimer); copiedTimer = setTimeout(() => { copied.value = false; }, 1500);
  } catch { copyError.value = true; }
}
onBeforeUnmount(() => { clearTimeout(copiedTimer); view?.destroy(); view = undefined; });
</script>

<template>
  <section class="aw-code-editor" :class="{ editing: canEdit }" data-testid="workflow-code-editor">
    <header class="aw-code-editor-bar">
      <span class="aw-code-editor-language">{{ language === 'python' ? 'Python' : 'JavaScript' }}</span>
      <span class="aw-code-editor-mode">{{ disabled ? text.readonly : canEdit ? text.edit : text.view }}</span>
      <span class="aw-code-editor-actions">
        <button type="button" :aria-label="copied ? text.copied : text.copy" @click="copy">{{ copied ? text.copied : text.copy }}</button>
        <button v-if="!disabled && !canEdit" type="button" class="aw-code-editor-edit" @click="edit">{{ text.edit }}</button>
        <button v-else-if="canEdit" type="button" class="aw-code-editor-edit" @click="editing = false">{{ text.done }}</button>
      </span>
    </header>
    <div ref="host" class="aw-code-editor-host nowheel nodrag" data-testid="workflow-code-source" />
    <footer class="aw-code-editor-footer"><span>{{ lineCount }} {{ text.lines }}</span><button v-if="canEdit" type="button" @click="suggest">{{ text.suggest }} <kbd>⌃Space</kbd></button><span v-else>{{ text.viewing }}</span></footer>
    <p v-if="canEdit" class="aw-code-editor-help">{{ text.help }}<br>{{ text.editing }}</p>
    <span class="aw-code-editor-announcement" role="status">{{ copyError ? text.copyError : copied ? text.copied : '' }}</span>
  </section>
</template>

<style scoped>
.aw-code-editor{min-width:0;border:1px solid var(--el-border-color);border-radius:9px;overflow:hidden;background:var(--el-bg-color)}.aw-code-editor.editing{border-color:var(--el-color-primary-light-5)}.aw-code-editor:focus-within{box-shadow:0 0 0 2px var(--el-color-primary-light-9)}.aw-code-editor-bar{display:flex;align-items:center;gap:7px;padding:7px 9px;background:var(--el-fill-color-extra-light);border-bottom:1px solid var(--el-border-color-lighter)}.aw-code-editor-language{font-size:11px;font-weight:600;color:var(--el-text-color-regular)}.aw-code-editor-mode{font-size:10px;color:var(--el-text-color-secondary);padding:2px 5px;border-radius:4px;background:var(--el-fill-color)}.aw-code-editor.editing .aw-code-editor-mode{color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.aw-code-editor-actions{display:flex;gap:5px;align-items:center;margin-left:auto}.aw-code-editor button{width:auto;padding:4px 7px;border:1px solid var(--el-border-color);border-radius:5px;background:var(--el-bg-color);color:var(--el-text-color-regular);font:inherit;font-size:11px;white-space:nowrap;cursor:pointer}.aw-code-editor button:hover{border-color:var(--el-color-primary-light-5);color:var(--el-color-primary)}.aw-code-editor button:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:2px}.aw-code-editor .aw-code-editor-edit{color:var(--el-color-primary);border-color:var(--el-color-primary-light-7)}.aw-code-editor-host{min-width:0;max-width:100%;overflow:hidden}.aw-code-editor-footer{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:6px 9px;border-top:1px solid var(--el-border-color-lighter);color:var(--el-text-color-secondary);font-size:10px}.aw-code-editor-footer button{border:0;padding:0;background:transparent;font-size:10px}.aw-code-editor kbd{font:inherit;color:var(--el-text-color-placeholder);margin-left:5px}.aw-code-editor-help{padding:7px 9px;margin:0;border-top:1px solid var(--el-border-color-lighter);font-size:10px;line-height:1.7;color:var(--el-text-color-secondary)}.aw-code-editor-announcement:empty{display:none}.aw-code-editor-announcement:not(:empty){display:block;padding:6px 9px;font-size:11px;color:var(--el-text-color-secondary)}
</style>
