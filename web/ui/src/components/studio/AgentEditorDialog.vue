<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from "vue";
import { ArrowLeft, ArrowRight, Check, Close, Document, MagicStick, Plus, Refresh, Search } from "@element-plus/icons-vue";
import AgentAvatar, { AVATAR_COLORS } from "@/components/AgentAvatar.vue";
import SkillToolsConfiguration from './SkillToolsConfiguration.vue';
import FieldHelp from './FieldHelp.vue';
import StarterIconPicker from './StarterIconPicker.vue';
import InlineComposer from '@/components/InlineComposer.vue';
import { starterCardComposerParts } from '@/components/starterCardDraft';
import { platformApi } from '@/api/platform';
import { appendGeneratedStarters, starterGenerationInput } from '@/agent/starterGeneration';
import { isRuntimeSkillID, skillChoices, skillStateLabel } from '@/agent/skillSelection';
import { legacyShortcutManagementNames } from '@/agent/shortcutToolNames';
import { boundNativeSkills, boundSkillState, canToggleNativeTool, defaultNativeTools, inheritedNativeTools, linkedNativeTools, nativeToolSkill, requiredBuiltinSkill, setSkillToolBinding, toggleSkillToolBinding } from '@/agent/skillToolBindings';
import type {
  AgentTemplate,
  PlatformAgent,
  PlatformAgentInput,
  PlatformCapabilities,
  PlatformModel,
  SkillSelection,
  SkillSelectionPlan,
} from "@/agent/platformTypes";
import {
  AGENT_FORM_LIMITS,
  emptyStarters,
  normalizeStarter,
  padStarters,
  starterToWire,
  CATEGORY_ORDER,
  buildAgentInput,
  categoryLabel,
  defaultModelId,
  initialCapabilities,
  initialCreateSkills,
  toggleId,
  toggleModelSelection,
  toolsetWarning,
  unavailableSelected,
  validateAgentForm,
  withTemplate,
  type AgentFormState,
} from "@/agent/studioPresentation";

/**
 * The create/edit surface is a real full-viewport dialog, not a drawer or a
 * narrow modal: defining an Agent is the Studio's one deep task, and it wants
 * the whole window — the form's group cards spread flat in two columns on
 * desktop, one column on a phone, with the header and the action footer pinned
 * while the middle scrolls.
 *
 * A native <dialog> with showModal(), so the focus trap, the inert page behind
 * and Esc handling are the browser's own; the dirty-close confirmation is ours
 * and intercepts both Esc and the close button.
 */
const props = defineProps<{
  models: PlatformModel[];
  /** Workspace starting points, curated server-side and passed down; the
   *  blank start is not among them — it is the dialog itself. */
  templates: AgentTemplate[];
  /** Skill + MCP catalog; null while loading or after a failed fetch. */
  capabilities: PlatformCapabilities | null;
  capabilitiesLoading?: boolean;
  /** Catalog failure from the last fetch, shown with an in-dialog retry. */
  capabilitiesError?: string;
  saving: boolean;
  /** Server-side failure from the last submit, shown inside the dialog. */
  error: string;
}>();

const emit = defineEmits<{
  create: [input: PlatformAgentInput, enterChat: boolean];
  update: [id: string, input: PlatformAgentInput, revision: number];
  createTemplate: [input: Omit<AgentTemplate, "id">];
  removeTemplate: [id: string];
  dismissError: [];
  retryCapabilities: [];
  closed: [];
}>();

const dlg = ref<HTMLDialogElement | null>(null);
const nameInput = ref<HTMLInputElement | null>(null);

const mode = ref<"create" | "edit">("create");
const editingId = ref<string | null>(null);
const attemptedSubmit = ref(false);
const confirmDiscard = ref(false);
const closing = ref(false);
/** Which template filled the current draft; null is the blank start. */
const templateId = ref<string | null>(null);

/* Template curation. The row stays one horizontal line however many
 * starting points there are: native horizontal scroll plus edge-fade and
 * arrow affordances, and the cards themselves carry the add/delete actions. */
const tplRow = ref<HTMLElement | null>(null);
const tplNameInput = ref<HTMLInputElement | null>(null);
const tplCanLeft = ref(false);
const tplCanRight = ref(false);
/** The trailing card flipped into its save-current-draft-as-template form. */
const tplAdding = ref(false);
const tplName = ref("");
const tplNameErr = ref(false);
/** Which card is showing its inline delete confirmation. */
const tplConfirmId = ref<string | null>(null);

function tplScrollState() {
  const el = tplRow.value;
  if (!el) return;
  tplCanLeft.value = el.scrollLeft > 2;
  tplCanRight.value = el.scrollLeft + el.clientWidth < el.scrollWidth - 2;
}

function tplScrollBy(dir: 1 | -1) {
  const el = tplRow.value;
  if (!el) return;
  el.scrollBy({ left: dir * Math.max(320, Math.round(el.clientWidth * 0.8)), behavior: "smooth" });
  // Smooth programmatic scrolling does not reliably deliver scroll events
  // (seen in embedded webviews, where rAF never fires either), so converge
  // the arrow/fade state by watching the position settle on a timer.
  let last = -1;
  let stable = 0;
  const started = performance.now();
  const tick = () => {
    tplScrollState();
    stable = el.scrollLeft === last ? stable + 1 : 0;
    last = el.scrollLeft;
    if (stable < 3 && performance.now() - started < 1000) setTimeout(tick, 60);
  };
  setTimeout(tick, 60);
}

function startTplAdd() {
  tplName.value = form.name.trim();
  tplNameErr.value = false;
  tplAdding.value = true;
  void nextTick(() => {
    const el = tplRow.value;
    if (el) el.scrollLeft = el.scrollWidth;
    tplNameInput.value?.focus();
  });
}

function cancelTplAdd() {
  tplAdding.value = false;
  tplName.value = "";
  tplNameErr.value = false;
}

/** A template captures the draft's identity and starters — never its
 *  capability grants, matching what applying one refills. */
function submitTemplate() {
  const name = tplName.value.trim();
  if (!name || props.saving) {
    tplNameErr.value = !name;
    return;
  }
  tplNameErr.value = false;
  emit("createTemplate", {
    name,
    description: form.description.trim(),
    instructions: form.instructions.trim(),
    icon: form.icon,
    color: form.color,
    category: form.category,
    starters: form.starters
      .filter((s) => s.prompt.trim())
      .map(starterToWire),
  });
}

function addStarter() {
  form.starters.push({ title: "", label: "", icon: "", prompt: "" });
}

function removeStarter(i: number) {
  form.starters.splice(i, 1);
  while (form.starters.length < 3) form.starters.push({ title: "", label: "", icon: "", prompt: "" });
}

function requestTemplateRemove(id: string) {
  tplConfirmId.value = tplConfirmId.value === id ? null : id;
}

function confirmTemplateRemove(id: string) {
  if (props.saving) return;
  // Deselect first: a failed delete leaves the card, but the draft it filled
  // stays untouched either way.
  if (templateId.value === id) templateId.value = null;
  emit("removeTemplate", id);
}

watch(
  () => props.templates.length,
  (now, was) => {
    // A grown list is a successful save: fold the add form back into its card.
    if (now > (was ?? 0)) cancelTplAdd();
    tplConfirmId.value = null;
    void nextTick(tplScrollState);
  },
);
/** Whether the editing Agent's capability selection was applied from the
 *  (possibly still loading) catalog. */
const capsApplied = ref(false);
const capabilityEdited = reactive({ skills: false, mcp: false, tools: false });

const form = reactive<AgentFormState>({
  name: "",
  description: "",
  instructions: "",
  model: "",
  models: [],
  skills: [],
  mcpServers: [],
  tools: [],
  skillToolBindings: undefined,
  skillsInherited: false,
  mcpInherited: false,
  toolsInherited: true,
  color: AVATAR_COLORS[0] as string,
  icon: "spark",
  category: "general",
  starters: emptyStarters(),
});

/** The form as it was when the dialog opened, for the dirty check. */
let baseline = "";
const snapshot = () => JSON.stringify(form);
const isDirty = computed(() => snapshot() !== baseline);

const errors = computed(() => validateAgentForm(form));
const isValid = computed(() => Object.keys(errors.value).length === 0);
const showErr = (key: string) => (attemptedSubmit.value ? errors.value[key] : undefined);

const platformWorkflowToolNames = new Set([
  "workflow_discover", "workflow_read", "workflow_validate", "workflow_edit",
  "workflow_test", "workflow_run", "workflow_publish",
]);
// Keep obsolete saved selections visible and removable, while never offering
// platform management capabilities as Agent grants (including stale catalogs).
const agentCapabilities = computed(() => {
  const catalog = props.capabilities;
  if (!catalog) return null;
  return {
    ...catalog,
    builtin_tools: catalog.builtin_tools?.filter(tool => !platformWorkflowToolNames.has(tool.name)),
    skills: catalog.skills.filter(skill => skill.name !== "workflow-authoring"),
  };
});

/** Capability catalogs must be present before a save: the editor always
 *  sends explicit arrays, so an unknown catalog could silently drop a
 *  legacy Agent's inheritance. */
const capabilitiesReady = computed(
  () => !!agentCapabilities.value && !props.capabilitiesLoading && !props.capabilitiesError,
);
const skillSelection = computed<SkillSelection>(() => ({ skill_ids: form.skills.filter(isRuntimeSkillID) }));
const runtimeSkillMode = computed(() => skillSelection.value.skill_ids.length > 0);
const runtimeSkillValidation = ref<{ pending: boolean; error: string; plan: SkillSelectionPlan | null }>({ pending: false, error: '', plan: null });
const canSubmit = computed(() => isValid.value && capabilitiesReady.value && !!props.models.length && (!runtimeSkillMode.value || (!runtimeSkillValidation.value.pending && !runtimeSkillValidation.value.error && !!runtimeSkillValidation.value.plan)));
const canPublish = computed(() => canSubmit.value && (!runtimeSkillMode.value || !!runtimeSkillValidation.value.plan?.ready));

const generationCount = ref(3);
const generating = ref(false);
const generationError = ref('');
const generationMessage = ref('');
// The card list has no total cap; the count is bounded per generation batch
// (the server's studioGenerateStarterLimit).
const generationMax = AGENT_FORM_LIMITS.starterGenerationBatch;
const generationReason = computed(() => {
  if (!form.models.length || !form.model || !form.models.includes(form.model)) return '请先在上方勾选至少一个模型，并设置默认模型。';
  if (!props.models.some(model => model.id === form.model && model.available)) return '当前默认模型不可用，请选择一个可用模型。';
  if (form.models.some(id => !props.models.some(model => model.id === id && model.available))) return '已勾选的模型中有不可用项，请先取消勾选或恢复模型配置。';
  if (!form.name.trim() && !form.description.trim() && !form.instructions.trim()) return '请先填写名称、简介或指令，让 AI 了解这个 Agent 的用途。';
  if (!capabilitiesReady.value) return '技能与工具清单加载完成后即可生成。';
  if (!Number.isInteger(generationCount.value) || generationCount.value < 1 || generationCount.value > generationMax) return `生成条数需为 1 到 ${generationMax}。`;
  return '';
});
const canGenerate = computed(() => !generationReason.value && !generating.value && !props.saving);
const generationContext = computed(() => JSON.stringify({
  input: starterGenerationInput(form, generationCount.value, editingId.value), starters: form.starters,
  modelAvailable: props.models.find(model => model.id === form.model)?.available,
  catalog: agentCapabilities.value,
}));
let generationEpoch = 0;
let generationController: AbortController | null = null;
function cancelGeneration(message = '已取消生成。') {
  generationEpoch++;
  generationController?.abort();
  generationController = null;
  generating.value = false;
  generationError.value = '';
  generationMessage.value = message;
}
watch(generationContext, () => {
  if (generating.value) cancelGeneration('信息已修改，已取消本次生成。可按新内容重新生成。');
}, { flush: 'sync' });
async function generateStarters() {
  if (!canGenerate.value) return;
  const epoch = ++generationEpoch;
  const context = generationContext.value;
  const controller = new AbortController();
  generationController = controller;
  generating.value = true;
  generationError.value = '';
  generationMessage.value = '';
  try {
    const result = await platformApi.generateStarters(starterGenerationInput(form, generationCount.value, editingId.value), controller.signal);
    if (epoch !== generationEpoch || controller.signal.aborted || context !== generationContext.value) return;
    const merged = appendGeneratedStarters(form.starters, result.starters);
    generating.value = false;
    generationController = null;
    form.starters = merged.starters;
    generationMessage.value = `已新增 ${merged.added} 条开场提示，原有内容已保留。可继续修改后保存。`;
  } catch (error) {
    if (epoch !== generationEpoch || controller.signal.aborted) return;
    generationError.value = error instanceof Error ? error.message : '生成暂时失败，请稍后重试。';
  } finally {
    if (epoch === generationEpoch) {
      generating.value = false;
      generationController = null;
    }
  }
}
onBeforeUnmount(() => cancelGeneration(''));

function formInput(status: 'ready' | 'draft'): PlatformAgentInput {
  return buildAgentInput(form, status);
}

/** Model rows: the available catalog, plus unavailable ids the saved Agent
 *  still selects — retained, visible, removable; never silently dropped. */
const modelRows = computed(() => {
  const known = new Set(props.models.map((m) => m.id));
  const rows = props.models.map((m) => ({
    id: m.id,
    provider: m.provider,
    available: m.available,
    selected: form.models.includes(m.id),
    locked: !m.available && !form.models.includes(m.id),
  }));
  const stale = unavailableSelected(form.models, [...known]).map((id) => ({
    id,
    provider: "",
    available: false,
    selected: true,
    locked: false,
  }));
  return [...rows, ...stale];
});

/** Skill/MCP rows share one shape so both groups render identically:
 *  selectable id, label, meta line, whether a new check is allowed, and
 *  whether this one is a stale retained id. */
interface CapRow {
  id: string;
  name: string;
  desc: string;
  meta: string;
  selected: boolean;
  /** Cannot gain a new selection (unavailable model / disabled server). */
  locked: boolean;
  /** Selected but absent from the catalog — removable, not re-checkable. */
  stale: boolean;
  /** The server itself is disabled — shown as a badge, not just meta text. */
  disabled: boolean;
  /** System built-in skill: permanently checked, not a grant to decide. */
  builtin: boolean;
  associatedSkill?: string;
  nativeTools?: string[];
  mixed?: boolean;
}

/** Required guides remain fixed; capability Skills are deliberately selectable. */
const builtinSkillIds = computed(() =>
  (agentCapabilities.value?.skills ?? []).filter(requiredBuiltinSkill).map((s) => s.id),
);
const linkedSkills = computed(() => boundNativeSkills(agentCapabilities.value));

/** The native-tool rows: the catalog's fixed set, protected entries always
 *  checked and locked — they are infrastructure, not a grant to decide. */
const allToolRows = computed<CapRow[]>(() => {
  const rows: CapRow[] = (agentCapabilities.value?.builtin_tools ?? []).map((t) => ({
    id: t.name,
    name: t.name,
    desc: t.description,
    meta: nativeToolSkill(t.name, agentCapabilities.value) ? `关联 ${nativeToolSkill(t.name, agentCapabilities.value)}` : t.protected ? "核心，始终保留" : t.read_only ? "只读" : "",
    selected: !!t.protected || form.tools.includes(t.name),
    locked: !canToggleNativeTool(t, agentCapabilities.value),
    stale: false,
    disabled: false,
    builtin: false,
    associatedSkill: nativeToolSkill(t.name, agentCapabilities.value),
  }));
  for (const skill of linkedSkills.value) {
    for (const name of skill.legacy_native_tools ?? []) {
      if (!form.tools.includes(name)) continue;
      rows.push({ id: name, name, desc: '保留原有兼容权限。切换关联 Skill 后，统一使用当前工具入口，不会自动扩大旧权限。', meta: `关联 ${skill.name} · 兼容授权`, selected: true, locked: true, stale: true, disabled: false, builtin: false, associatedSkill: skill.name });
    }
  }
  const legacy = legacyShortcutManagementNames.filter(name => form.tools.includes(name));
  for (const name of form.tools.filter(tool => platformWorkflowToolNames.has(tool))) {
    rows.push({ id: name, name, desc: "平台工作流在管理页面维护。此绑定已无操作权限，可取消勾选移除。", meta: "历史绑定", selected: true, locked: false, stale: true, disabled: true, builtin: false });
  }
  if (legacy.length && !form.tools.includes("shortcut_manage")) {
    const labels: Record<string, string> = { shortcut_create: "创建", shortcut_update: "修改", shortcut_folder_create: "创建文件夹", shortcut_folder_rename: "文件夹改名", shortcut_move: "移动" };
    rows.push({ id: "legacy_shortcut_management", name: "shortcut_manage (legacy)", desc: `仅允许：${legacy.map(name => labels[name]).join("、")}。保留此项不会增加权限；启用上方“shortcut_manage”才会开放全部管理操作。`, meta: "兼容授权", selected: true, locked: false, stale: false, disabled: false, builtin: false });
  }
  return rows;
});
const linkedToolRows = computed(() => allToolRows.value.filter(row => !!row.associatedSkill));
const generalToolRows = computed(() => allToolRows.value.filter(row => !row.associatedSkill));
const hasLegacySkillGrants = computed(() => linkedSkills.value.some(skill => boundSkillState(skill, form).mixed));

/** The self-lock note: shown, never blocking — a read-and-discuss Agent is a
 *  legitimate definition, just probably not the one being drafted. */
const toolWarning = computed(() => toolsetWarning(form, agentCapabilities.value ?? { skills: [] }));

function toggleTool(name: string) {
  if (nativeToolSkill(name, agentCapabilities.value)) return;
  if (allToolRows.value.find((r) => r.id === name)?.locked) return;
  if (platformWorkflowToolNames.has(name) && !form.tools.includes(name)) return;
  if (name === "legacy_shortcut_management") {
    form.tools = form.tools.filter(tool => !legacyShortcutManagementNames.some(legacy => legacy === tool));
  } else if (name === "shortcut_manage") {
    const enabled = form.tools.includes(name);
    form.tools = form.tools.filter(tool => tool !== name && !legacyShortcutManagementNames.some(legacy => legacy === tool));
    if (!enabled) form.tools.push(name);
  } else form.tools = toggleId(form.tools, name);
  capabilityEdited.tools = true;
  form.toolsInherited = false;
}

const skillQuery = ref("");
const mcpQuery = ref("");

const skillOptions = computed(() => skillChoices(agentCapabilities.value?.skills ?? [], form.skills));
const allSkillRows = computed<CapRow[]>(() => {
  const catalog = skillOptions.value.available;
  const rows: CapRow[] = catalog.map((s) => ({
    id: s.id,
    name: s.name,
    desc: s.description,
    meta: [s.package ? `${s.package_name} · v${s.package.version}` : '', skillStateLabel(s.state), s.hidden ? "仅手动调用" : "", s.builtin ? "系统内置" : "", s.native_tools?.length ? `${s.native_tools.length} 关联内置工具` : s.tool_count ? `${s.tool_count} 技能工具` : '操作指南'].filter(Boolean).join(" · "),
    selected: requiredBuiltinSkill(s) || form.skills.includes(s.id),
    locked: requiredBuiltinSkill(s) || (!form.skills.includes(s.id) && (s.state === 'revoked' || s.state === 'unavailable')),
    stale: false,
    disabled: s.state === 'revoked' || s.state === 'unavailable',
    builtin: !!s.builtin,
    nativeTools: linkedSkills.value.find(skill => skill.id === s.id)?.native_tools,
    mixed: linkedSkills.value.some(skill => skill.id === s.id) && boundSkillState(s, form).mixed,
  }));
  return rows;
});

const mcpStatusLabels: Record<string, string> = {
  connected: "已连接", connecting: "连接中", disconnected: "未连接", disabled: "已停用",
};
const allMcpRows = computed<CapRow[]>(() => {
  const catalog = agentCapabilities.value?.mcp_servers ?? [];
  const rows: CapRow[] = catalog.map((m) => ({
    id: m.id,
    name: m.name || m.id,
    desc: m.name && m.name !== m.id ? m.id : "",
    // A disabled server's status moves out of the meta line into a badge so
    // the row's whole look, not one word, says "cannot be granted here".
    meta: [m.transport, m.enabled ? mcpStatusLabels[m.status] || m.status : "", `${m.tool_count} 工具`].filter(Boolean).join(" · "),
    selected: form.mcpServers.includes(m.id),
    // A disabled server keeps an existing selection but cannot gain one.
    locked: !m.enabled && !form.mcpServers.includes(m.id),
    stale: false,
    disabled: !m.enabled,
    builtin: false,
  }));
  const stale = unavailableSelected(
    form.mcpServers,
    catalog.map((m) => m.id),
  ).map((id) => ({
    id, name: id, desc: "配置中已不存在的服务器", meta: "不可用",
    selected: true, locked: false, stale: true, disabled: false, builtin: false,
  }));
  return [...rows, ...stale];
});

const filterRows = (rows: readonly CapRow[], q: string): CapRow[] => {
  const needle = q.trim().toLowerCase();
  if (!needle) return [...rows];
  return rows.filter((r) => `${r.name} ${r.desc} ${r.meta} ${r.id}`.toLowerCase().includes(needle));
};
const skillView = computed(() => filterRows(allSkillRows.value, skillQuery.value));
const mcpView = computed(() => filterRows(allMcpRows.value, mcpQuery.value));

// The parent hands the editing target to openEdit; we keep it for the
// draft/ready footer logic rather than re-deriving it from a list.
const editingAgent = ref<PlatformAgent | null>(null);

function toggleModel(id: string) {
  const selection = toggleModelSelection(
    {
      models: form.models,
      defaultModel: form.model,
      skills: form.skills,
      mcpServers: form.mcpServers,
      tools: form.tools,
      skillsInherited: form.skillsInherited,
      mcpInherited: form.mcpInherited,
      toolsInherited: form.toolsInherited,
    },
    id,
  );
  form.models = selection.models;
  form.model = selection.defaultModel;
}

function toggleSkill(id: string) {
  // System built-ins are not a grant to decide; the checkbox is disabled and
  // this guard keeps every other path honest too.
  if (builtinSkillIds.value.includes(id)) return;
  if (allSkillRows.value.find(r => r.id === id)?.locked) return;
  const linked = linkedSkills.value.find(skill => skill.id === id);
  if (linked) {
    const next = toggleSkillToolBinding(form, linked);
    form.skills = next.skills;
    form.tools = next.tools;
    form.skillToolBindings = next.skillToolBindings;
    capabilityEdited.tools = true;
    form.toolsInherited = false;
  } else form.skills = toggleId(form.skills, id);
  capabilityEdited.skills = true;
  form.skillsInherited = false;
}

// Built-ins ride every draft: they merge in as soon as the catalog names
// them (create mode), and absorbing the merge into the baseline keeps an
// untouched Agent from looking dirty merely because the system grew a skill.
watch(builtinSkillIds, () => {
  const missing = builtinSkillIds.value.filter((id) => !form.skills.includes(id));
  if (!missing.length) return;
  const wasDirty = isDirty.value;
  form.skills = [...form.skills, ...missing];
  if (!wasDirty) baseline = snapshot();
});

// General tools retain the existing defaults; linked tools follow selected Skills.
// A fresh draft whose catalog
// arrived late (tools still empty — protected rows are locked, so an empty
// list can only mean "not yet filled", never "user unchecked everything") or
// an inherited legacy Agent checks everything the moment the catalog names
// the tools, and the same baseline absorption keeps an untouched dialog clean.
watch(
  () => agentCapabilities.value?.builtin_tools,
  (list) => {
    if (!list || capabilityEdited.tools || (mode.value === 'edit' && !form.toolsInherited) || (!form.toolsInherited && form.tools.length > 0)) return;
    const wasDirty = isDirty.value;
    form.tools = mode.value === 'create' ? defaultNativeTools(agentCapabilities.value, form.skills) : inheritedNativeTools(agentCapabilities.value);
    if (mode.value === 'create') form.skillToolBindings = linkedSkills.value.map(skill => skill.name);
    if (!wasDirty) baseline = snapshot();
  },
);

function toggleMcp(id: string) {
  form.mcpServers = toggleId(form.mcpServers, id);
  capabilityEdited.mcp = true;
  form.mcpInherited = false;
}

function removeUnavailableSkill(id: string) {
  const linked = linkedSkills.value.find(skill => skill.id === id);
  if (linked) {
    const next = setSkillToolBinding(form, linked, false);
    form.skills = next.skills; form.tools = next.tools; form.skillToolBindings = next.skillToolBindings;
    capabilityEdited.tools = true; form.toolsInherited = false;
  } else form.skills = form.skills.filter(selected => selected !== id);
  capabilityEdited.skills = true;
  form.skillsInherited = false;
}

function resetForm() {
  cancelGeneration('');
  generationCount.value = 3;
  form.name = "";
  form.description = "";
  form.instructions = "";
  // A new Agent: one default model selected, only system Skills, no MCP servers —
  // capability grants are deliberate, never inherited by accident. The native
  // tool set is the exception: it starts at the full default and narrows by
  // deliberate unchecking, because a draft without write/edit/bash is rarely
  // the intent.
  form.models = [defaultModelId(props.models)].filter(Boolean);
  form.model = form.models[0] ?? "";
  form.skills = initialCreateSkills(agentCapabilities.value);
  form.mcpServers = [];
  // toolsInherited stays false for a fresh draft: the full set is a default
  // here, not an inheritance being reported. When the catalog has not arrived
  // yet the list is empty and the builtin_tools watch fills it on arrival.
  form.tools = defaultNativeTools(agentCapabilities.value, form.skills);
  form.skillToolBindings = linkedSkills.value.map(skill => skill.name);
  runtimeSkillValidation.value = { pending: false, error: '', plan: null };
  form.skillsInherited = false;
  form.mcpInherited = false;
  form.toolsInherited = false;
  skillQuery.value = "";
  mcpQuery.value = "";
  form.color = AVATAR_COLORS[0];
  form.icon = "spark";
  form.category = "general";
  form.starters = emptyStarters();
  attemptedSubmit.value = false;
  editingId.value = null;
  editingAgent.value = null;
  Object.assign(capabilityEdited, { skills: false, mcp: false, tools: false });
  templateId.value = null;
  confirmDiscard.value = false;
  capsApplied.value = false;
}

/** Templates refill identity and starters only. Capability choices — models,
 *  default, Skills, MCP — survive every template switch (withTemplate). */
function applyTemplate(tpl: AgentTemplate | null) {
  Object.assign(form, withTemplate({ ...form }, tpl));
  if (!form.models.length) {
    form.models = [defaultModelId(props.models)].filter(Boolean);
    form.model = form.models[0] ?? "";
  }
  templateId.value = tpl ? tpl.id : null;
}

async function present() {
  baseline = snapshot();
  closing.value = false;
  tplAdding.value = false;
  tplName.value = "";
  tplNameErr.value = false;
  tplConfirmId.value = null;
  dlg.value?.showModal();
  await nextTick();
  nameInput.value?.focus();
  void nextTick(tplScrollState);
}

function openCreate(_agents: readonly PlatformAgent[] = [], presetSkills: string[] = [], name = '') {
  resetForm();
  form.skills = initialCreateSkills(agentCapabilities.value, presetSkills);
  form.tools = defaultNativeTools(agentCapabilities.value, form.skills);
  if (name) form.name = name;
  mode.value = "create";
  void present();
}

function openEdit(agent: PlatformAgent, _agents: readonly PlatformAgent[] = []) {
  resetForm();
  // The draft and optimistic lock belong to the definition the user opened.
  // A later list refresh must not silently rebase an in-progress edit.
  editingAgent.value = JSON.parse(JSON.stringify(agent)) as PlatformAgent;
  form.name = agent.name;
  form.description = agent.description;
  form.instructions = agent.instructions;
  form.model = agent.model;
  // Model truth is already available from the Agent itself. Seed it now so an
  // async capability fetch (or a failed one) never renders the saved default
  // as unchecked. Skill/MCP inheritance still waits for the real catalog.
  const savedModels = [...new Set(agent.models ?? (agent.model ? [agent.model] : []))];
  form.models = savedModels;
  form.model = savedModels.includes(agent.model) ? agent.model : savedModels[0] ?? "";
  form.skills = agent.skills ? [...new Set(agent.skills)] : [];
  form.mcpServers = agent.mcp_servers ? [...new Set(agent.mcp_servers)] : [];
  form.tools = agent.tools ? [...new Set(agent.tools)] : [];
  form.skillToolBindings = agent.skill_tool_bindings === undefined ? undefined : [...new Set(agent.skill_tool_bindings)];
  form.skillsInherited = agent.skills === undefined;
  form.mcpInherited = agent.mcp_servers === undefined;
  form.toolsInherited = agent.tools === undefined;
  form.color = agent.color || AVATAR_COLORS[0];
  form.icon = agent.icon || "spark";
  form.category = agent.category || "general";
  form.starters = padStarters(agent.starters.map(normalizeStarter));
  mode.value = "edit";
  editingId.value = agent.id;
  applyAgentCapabilities();
  void present();
}

/** Fill the capability selection from the saved Agent once the catalog is
 *  in. Legacy Agents (omitted arrays) open with the full inherited catalog
 *  checked; explicit arrays — even stale ones — are honored as saved. */
function applyAgentCapabilities() {
  const agent = editingAgent.value;
  if (!agent || !agentCapabilities.value || capsApplied.value) return;
  // Applying the saved selection is not a user edit. If the catalog arrived
  // after present(), absorbing it into the baseline keeps an untouched legacy
  // Agent from looking dirty merely because inheritance was made visible.
  const wasDirty = isDirty.value;
	const sel = initialCapabilities(agent, agentCapabilities.value);
	// Models were seeded from the definition synchronously and do not depend on
	// this catalog. Only untouched capability groups may initialize late.
	if (!capabilityEdited.skills) { form.skills = sel.skills; form.skillsInherited = sel.skillsInherited; }
	if (!capabilityEdited.mcp) { form.mcpServers = sel.mcpServers; form.mcpInherited = sel.mcpInherited; }
	if (!capabilityEdited.tools) { form.tools = sel.tools; form.toolsInherited = sel.toolsInherited; form.skillToolBindings = sel.skillToolBindings; }
	// The saved definition predates the always-on built-ins at worst; the
	// union here keeps an explicit "no skills" Agent meaning "no extra
	// skills" rather than "no system skills".
	const missingBuiltins = builtinSkillIds.value.filter((id) => !form.skills.includes(id));
	if (missingBuiltins.length) form.skills = [...form.skills, ...missingBuiltins];
  form.tools = linkedNativeTools(form, agentCapabilities.value);
  capsApplied.value = true;
  if (!wasDirty) baseline = snapshot();
}

watch(
  () => agentCapabilities.value,
  () => {
    if (mode.value === "edit" && !capsApplied.value) applyAgentCapabilities();
  },
);

/** Close without a dirty check — the parent's creationSucceeded() after a
    successful save, or the confirmed discard. */
function close() {
  if (closing.value) return;
  cancelGeneration('');
  closing.value = true;
  // Let the exit transition play before the dialog actually closes.
  setTimeout(() => dlg.value?.close(), 140);
}

function onClosed() {
  // The native close event: state resets here so reopening is always clean.
  closing.value = false;
  confirmDiscard.value = false;
  emit("closed");
}

function requestClose() {
  if (props.saving || closing.value) return;
  if (isDirty.value) {
    confirmDiscard.value = true;
    return;
  }
  close();
}

/** Esc on a modal dialog fires cancel, which closes by default — prevent that
    so the dirty check runs first. */
function onCancel(e: Event) {
  e.preventDefault();
  if (confirmDiscard.value) {
    confirmDiscard.value = false;
    return;
  }
  requestClose();
}

function submitCreate(enterChat: boolean) {
  attemptedSubmit.value = true;
  if (!canSubmit.value || props.saving) return;
  if (enterChat && !canPublish.value) return;
  emit("create", formInput(enterChat ? "ready" : "draft"), enterChat);
}

function submitEdit(status?: "ready" | "draft") {
  attemptedSubmit.value = true;
  if (!canSubmit.value || props.saving || !editingId.value) return;
  const original = editingAgent.value;
  const targetStatus = status ?? original?.status ?? 'ready';
  if (targetStatus === 'ready' && !canPublish.value) return;
  if (original) emit("update", editingId.value, formInput(targetStatus), original.revision);
}

defineExpose({ openCreate, openEdit, close });

const editingIsDraft = computed(() => editingAgent.value?.status === "draft");
</script>

<template>
  <dialog
    ref="dlg"
    class="editor"
    :class="{ closing }"
    aria-labelledby="editor-title"
    @cancel="onCancel"
    @close="onClosed"
  >
    <div class="ed-shell">
      <header class="ed-head">
        <div class="ed-head-text">
          <h2 id="editor-title">{{ mode === "create" ? "创建 Agent" : `编辑 ${editingAgent?.name ?? "Agent"}` }}</h2>
          <p>{{ mode === "create" ? "从模板或空白开始，选择模型、MCP 与 Skill。" : "模型、技能和内置工具会同步到已有对话，执行中的任务结束后应用；指令与 MCP 授权仅对新对话生效。" }}</p>
        </div>
        <button type="button" class="iconbtn" aria-label="关闭" :disabled="saving" @click="requestClose">
          <el-icon aria-hidden="true"><Close /></el-icon>
        </button>
      </header>

      <div v-if="error" class="ed-error" role="alert">
        <span class="ed-error-msg">{{ error }}</span>
        <button type="button" class="iconbtn sm" aria-label="关闭提示" @click="emit('dismissError')">
          <el-icon aria-hidden="true"><Close /></el-icon>
        </button>
      </div>

      <div v-if="confirmDiscard" class="ed-discard" role="alertdialog" aria-label="放弃更改确认">
        <span>有未保存的更改，确定放弃吗？</span>
        <span class="ed-discard-actions">
          <button type="button" class="btn ghost sm" @click="confirmDiscard = false">继续编辑</button>
          <button type="button" class="btn danger sm" @click="close">放弃更改</button>
        </span>
      </div>

      <fieldset class="ed-body" :disabled="saving" :inert="saving" :aria-busy="saving">
        <!-- Starting points are choices inside the dialog, not a nav page:
             blank, one of the workspace templates prefilling the same form,
             or saving the current draft as a new template. The row is always
             one horizontal line — native scroll carries it, the arrows and
             edge fades just make that visible. -->
        <section v-if="mode === 'create'" class="ed-templates" aria-label="从模板开始">
          <span class="group-label"><el-icon aria-hidden="true"><MagicStick /></el-icon>从模板开始</span>
          <div
            class="tpl-scroll"
            :class="{ 'can-left': tplCanLeft, 'can-right': tplCanRight }"
          >
            <div
              ref="tplRow"
              class="tpl-row"
              role="group"
              aria-label="模板选择"
              @scroll.passive="tplScrollState"
            >
              <button
                type="button"
                class="tpl"
                :class="{ on: templateId === null }"
                :aria-pressed="templateId === null"
                @click="applyTemplate(null)"
              >
                <!-- A blank page, not a plus: the trailing card already owns
                     the "+" shape for saving the draft as a template. -->
                <span class="tpl-blank" aria-hidden="true"><el-icon><Document /></el-icon></span>
                <span class="tpl-name">空白 Agent</span>
                <span class="tpl-desc">从一个好名字和一段清晰的指令开始。</span>
              </button>
              <div
                v-for="t in templates"
                :key="t.id"
                class="tpl-wrap"
                :class="{ on: templateId === t.id }"
              >
                <button
                  type="button"
                  class="tpl"
                  :class="{ on: templateId === t.id }"
                  :aria-pressed="templateId === t.id"
                  @click="applyTemplate(t)"
                >
                  <AgentAvatar :icon="t.icon" :color="t.color" :size="34" />
                  <span class="tpl-name">{{ t.name }}</span>
                  <span class="tpl-desc">{{ t.description }}</span>
                </button>
                <button
                  v-if="tplConfirmId !== t.id"
                  type="button"
                  class="tpl-del"
                  aria-label="删除模板"
                  @click.stop="requestTemplateRemove(t.id)"
                >
                  <el-icon aria-hidden="true"><Close /></el-icon>
                </button>
                <div v-else class="tpl-confirm" role="alertdialog" aria-label="删除模板确认" @click.stop>
                  <span class="tpl-confirm-text">删除「{{ t.name }}」？</span>
                  <span class="tpl-confirm-actions">
                    <button type="button" class="btn ghost sm" @click="tplConfirmId = null">取消</button>
                    <button
                      type="button"
                      class="btn danger sm"
                      :disabled="saving"
                      @click="confirmTemplateRemove(t.id)"
                    >
                      删除
                    </button>
                  </span>
                </div>
              </div>
              <button v-if="!tplAdding" type="button" class="tpl tpl-add" @click="startTplAdd">
                <el-icon class="tpl-add-icon" aria-hidden="true"><Plus /></el-icon>
                <span class="tpl-name">存为模板</span>
                <span class="tpl-desc">把当前填写的身份与开场存成一个模板。</span>
              </button>
              <div v-else class="tpl tpl-add-form" @click.stop>
                <span class="field-label-row"><span class="tpl-name">模板名称</span><FieldHelp label="模板名称" text="为这个起点取一个易识别的名字。模板保存名称、简介、指令、外观和开场提示；模型、技能与工具选择不会随模板保存。" /></span>
                <input
                  ref="tplNameInput"
                  v-model="tplName"
                  class="tpl-add-input"
                  type="text"
                  maxlength="60"
                  placeholder="模板名称"
                  :aria-invalid="tplNameErr"
                  @keyup.enter="submitTemplate"
                  @keyup.esc="cancelTplAdd"
                />
                <span v-if="tplNameErr" class="tpl-add-err" role="alert">请填写模板名称</span>
                <span class="tpl-add-actions">
                  <button
                    type="button"
                    class="btn solid sm"
                    :disabled="saving"
                    @click="submitTemplate"
                  >
                    {{ saving ? "保存中…" : "保存" }}
                  </button>
                  <button type="button" class="btn ghost sm" :disabled="saving" @click="cancelTplAdd">
                    取消
                  </button>
                </span>
              </div>
            </div>
            <button
              v-if="tplCanLeft"
              type="button"
              class="tpl-nav left"
              aria-label="向左滚动模板"
              @click="tplScrollBy(-1)"
            >
              <el-icon aria-hidden="true"><ArrowLeft /></el-icon>
            </button>
            <button
              v-if="tplCanRight"
              type="button"
              class="tpl-nav right"
              aria-label="向右滚动模板"
              @click="tplScrollBy(1)"
            >
              <el-icon aria-hidden="true"><ArrowRight /></el-icon>
            </button>
          </div>
        </section>

        <form class="ed-form" autocomplete="off" @submit.prevent>
          <section class="group" aria-labelledby="eg-identity">
            <h3 id="eg-identity" class="group-title">身份</h3>

            <div class="field">
              <div class="field-label-row"><label class="flabel" for="mf-name">名称<span class="req" aria-hidden="true"> *</span></label><FieldHelp label="名称" text="这个 Agent 的名字，用于列表和对话中的识别，也会告诉模型它的身份。建议简短明确，例如“项目研究助手”。" /></div>
              <input
                id="mf-name"
                ref="nameInput"
                v-model="form.name"
                class="input"
                type="text"
                :maxlength="AGENT_FORM_LIMITS.name"
                placeholder="例如：研究助手"
                :aria-invalid="!!showErr('name')"
              />
              <div class="fmeta">
                <span v-if="showErr('name')" class="ferr" role="alert">{{ errors.name }}</span>
                <span class="fcount">{{ form.name.trim().length }}/{{ AGENT_FORM_LIMITS.name }}</span>
              </div>
            </div>

            <div class="field">
              <div class="field-label-row"><label class="flabel" for="mf-desc">简介</label><FieldHelp label="简介" text="向使用者说明这个 Agent 擅长什么，显示在 Agent 列表中。简介不作为每次对话的行为指令；长期规则请填写在“指令”中。" /></div>
              <textarea
                id="mf-desc"
                v-model="form.description"
                class="input"
                rows="2"
                :maxlength="AGENT_FORM_LIMITS.description"
                placeholder="一句话说明它擅长什么。"
                :aria-invalid="!!showErr('description')"
              />
              <div class="fmeta">
                <span v-if="showErr('description')" class="ferr" role="alert">{{ errors.description }}</span>
                <span class="fcount">{{ form.description.trim().length }}/{{ AGENT_FORM_LIMITS.description }}</span>
              </div>
            </div>

            <div class="field">
              <div class="field-label-row"><span class="flabel" id="mf-cat-label">分类</span><FieldHelp label="分类" text="帮助在列表中归类和查找 Agent，例如研究、写作或开发。分类不会改变模型的能力或工具权限。" /></div>
              <div class="chip-row" role="group" aria-labelledby="mf-cat-label">
                <button
                  v-for="c in CATEGORY_ORDER"
                  :key="c"
                  type="button"
                  class="chip"
                  :class="{ on: form.category === c }"
                  :aria-pressed="form.category === c"
                  @click="form.category = c"
                >
                  {{ categoryLabel(c) }}
                </button>
              </div>
            </div>
          </section>

          <section class="group" aria-labelledby="eg-behavior">
            <h3 id="eg-behavior" class="group-title">行为与模型</h3>

            <div class="field">
              <div class="field-label-row"><label class="flabel" for="mf-inst">指令</label><FieldHelp label="指令" text="规定这个 Agent 长期遵循的身份、工作步骤、约束和回答格式。每次模型请求都会带上这些规则。可以写“用中文回答、结论附来源”；工具与技能仍需在下方勾选。" /></div>
              <textarea
                id="mf-inst"
                v-model="form.instructions"
                class="input code"
                rows="8"
                :maxlength="AGENT_FORM_LIMITS.instructions"
                placeholder="告诉这个 Agent 它是谁、怎么工作、什么不要做。"
                :aria-invalid="!!showErr('instructions')"
              />
              <div class="fmeta">
                <span class="fhint">指令是它的行为准则，越具体越好。</span>
                <span v-if="showErr('instructions')" class="ferr" role="alert">{{ errors.instructions }}</span>
                <span class="fcount">{{ form.instructions.trim().length }}/{{ AGENT_FORM_LIMITS.instructions }}</span>
              </div>
            </div>

            <!-- Models: multi-select, exactly one default. The default opens
                 the conversation; the rest stay switchable in chat. -->
            <div class="field">
              <div class="field-label-row"><span class="flabel" id="mf-model-label">模型<span class="req" aria-hidden="true"> *</span></span><FieldHelp label="模型" text="勾选允许使用的模型，并选择默认模型。保存后同步到已有对话；正在执行的任务结束后应用。修改默认模型会切换到新默认，只修改可选列表时保留仍然允许的当前模型。技能和工具权限不会随之扩大。" /></div>
              <template v-if="modelRows.length">
                <div class="fmeta">
                  <span v-if="showErr('model')" class="ferr" role="alert">{{ errors.model }}</span>
                  <span v-else class="fhint">
                    已选 {{ form.models.length }} 个；默认模型开启对话，其余可在对话中切换。
                  </span>
                  <span class="fcount">{{ form.models.length }}/{{ modelRows.length }}</span>
                </div>
                <div class="field-label-row model-default-help"><span class="fhint">默认模型</span><FieldHelp label="默认模型" text="新对话和生成开场提示使用默认模型。更换默认模型也会同步到已有对话，正在执行的任务完成后切换；取消勾选它时会自动选择另一个已选模型。" /></div>
                <div class="caps-list" role="group" aria-labelledby="mf-model-label">
                  <div
                    v-for="m in modelRows"
                    :key="m.id"
                    class="cap-row model"
                    :class="{ on: m.selected, stale: !m.available }"
                  >
                    <label class="cap-check">
                      <input
                        type="checkbox"
                        :checked="m.selected"
                        :disabled="m.locked || saving"
                        @change="toggleModel(m.id)"
                      />
                      <span class="cap-name">{{ m.provider ? `${m.provider} · ${m.id}` : m.id }}</span>
                      <span v-if="!m.available" class="cap-tag bad">不可用</span>
                    </label>
                    <label class="cap-default" :class="{ on: form.model === m.id }">
                      <input
                        v-model="form.model"
                        type="radio"
                        name="agent-default-model"
                        :value="m.id"
                        :aria-label="`设 ${m.id} 为默认模型`"
                        :disabled="!m.selected || saving"
                      />
                      默认
                    </label>
                  </div>
                  </div>
                </template>
                <div v-else class="model-empty" role="note">
                还没有可用的模型。请先在「模型配置」中完成配置，再回来创建 Agent。
              </div>
            </div>
          </section>

          <section class="group" aria-labelledby="eg-capabilities">
            <h3 id="eg-capabilities" class="group-title">技能与工具<span class="opt">（可选）</span></h3>

            <!-- Catalog loading/error are explicit: a failed fetch is never
                 an authoritative empty list, and saving stays blocked so no
                 capability can be silently dropped. -->
            <div v-if="capabilitiesLoading" class="caps-state" role="status">
              <el-icon class="spin" aria-hidden="true"><Refresh /></el-icon>
              正在加载技能与 MCP 清单…
            </div>
            <div v-else-if="capabilitiesError" class="caps-state bad" role="alert">
              <span class="caps-state-msg">技能与 MCP 清单加载失败：{{ capabilitiesError }}</span>
              <button type="button" class="btn ghost sm" @click="emit('retryCapabilities')">
                <el-icon aria-hidden="true"><Refresh /></el-icon>
                重试
              </button>
            </div>

            <template v-else>
              <p v-if="mode === 'edit' && (form.skillsInherited || form.mcpInherited || form.toolsInherited)" class="caps-inherit" role="note">
                此 Agent 此前未单独设置技能、MCP 与内置工具，因此继承工作空间全部资源（已全部勾选）。保存时会把当前勾选固化为明确清单。
              </p>

              <!-- Linked native grants can only change through the Skill selection. -->
              <div v-if="linkedToolRows.length" class="caps-group" data-testid="skill-linked-native-tools">
                <div class="caps-head">
                  <span class="field-label-row"><span class="caps-title">内置工具 · Skill 关联</span><FieldHelp label="Skill 关联工具" text="这些工具随对应 Skill 一起启用或关闭，不能单独勾选。请在下方 Skills 中选择能力，保存后生效。工具调用仍遵循原有审批和用户权限。" /></span>
                  <span class="caps-count">已选 {{ linkedToolRows.filter(row => row.selected).length }}/{{ linkedToolRows.length }}</span>
                </div>
                <p class="caps-hint">在下方 Skills 中勾选或取消对应 Skill，这里的工具同步变化；保存前可以核对本次授权范围。</p>
                <p v-if="hasLegacySkillGrants" class="caps-inherit" role="note">部分旧配置的指南与工具权限尚未关联，当前保留原权限。切换对应 Skill 后，将统一启用或关闭整组工具。</p>
                <div class="caps-list" role="group" aria-label="Skill 关联内置工具">
                  <label v-for="row in linkedToolRows" :key="row.id" class="cap-row" :class="{ on: row.selected, stale: row.stale }">
                    <input type="checkbox" :checked="row.selected" disabled />
                    <span class="cap-main">
                      <span class="cap-name">{{ row.name }}<span class="cap-tag sys">随 Skill</span></span>
                      <span class="cap-desc">{{ row.desc }}</span>
                    </span>
                    <span class="cap-meta">{{ row.meta }}</span>
                  </label>
                </div>
              </div>

              <div class="caps-group" data-testid="general-native-tools">
                <div class="caps-head">
                  <span class="field-label-row"><span class="caps-title">内置工具 · 一般工具</span><FieldHelp label="一般内置工具" text="独立配置文件、命令和快捷方式等通用能力。核心只读与协作工具始终可用；其他工具可以取消勾选。写进指令不会自动启用工具。" /></span>
                  <span class="caps-count">已选 {{ generalToolRows.filter(row => row.selected).length }}/{{ generalToolRows.length }}</span>
                </div>
                <p class="caps-hint">通用工具独立选择。快捷方式删除与合并分别授权；文件写入不会因选择工作流 Skill 自动启用。Agent 对话只操作当前 Agent 的内部工作流。</p>
                <div class="caps-list" role="group" aria-label="一般内置工具选择">
                  <label
                    v-for="row in generalToolRows"
                    :key="row.id"
                    class="cap-row"
                    :class="{ on: row.selected, sys: row.locked, stale: row.stale }"
                  >
                    <input type="checkbox" :checked="row.selected" :disabled="row.locked" @change="toggleTool(row.id)" />
                    <span class="cap-main">
                      <span class="cap-name">
                        {{ row.name }}
                        <span v-if="row.locked" class="cap-tag sys">核心</span>
                        <span v-if="row.disabled" class="cap-tag off">已停用</span>
                      </span>
                      <span class="cap-desc">{{ row.desc }}</span>
                    </span>
                    <span class="cap-meta">{{ row.meta }}</span>
                  </label>
                </div>
                <p v-if="toolWarning" class="caps-warn" role="note">{{ toolWarning }}</p>
              </div>

              <!-- ── MCP：按服务器整体授权 ─────────────────────────── -->
              <div class="caps-group">
                <div class="caps-head">
                  <span class="field-label-row"><span class="caps-title">MCP 服务器</span><FieldHelp label="MCP 服务器" text="把工作空间已配置的外部工具连接授权给这个 Agent。勾选后可使用该连接提供的工具；没有连接也可以进行普通文字对话。停用的连接需先启用。" /></span>
                  <span class="caps-count">已选 {{ form.mcpServers.length }}/{{ allMcpRows.length }}</span>
                </div>
                <p class="caps-hint">勾选后可使用该连接提供的工具；已停用的连接需先在 MCP 管理中启用。</p>
                <div v-if="allMcpRows.length > 6" class="caps-search">
                  <el-icon aria-hidden="true"><Search /></el-icon>
                  <input v-model="mcpQuery" type="search" placeholder="搜索服务器" aria-label="搜索 MCP 服务器" />
                </div>
                <div v-if="!allMcpRows.length" class="caps-empty" role="note">
                  工作空间还没有 MCP 服务器。需要时先到「MCP 管理」添加并启用，再回来勾选。
                </div>
                <div v-else-if="!mcpView.length" class="caps-empty" role="note">没有匹配的服务器。</div>
                <div v-else class="caps-list" role="group" aria-label="MCP 服务器选择">
                  <label
                    v-for="row in mcpView"
                    :key="row.id"
                    class="cap-row"
                    :class="{ on: row.selected, stale: row.stale, locked: row.locked }"
                  >
                      <input type="checkbox" :checked="row.selected" :disabled="row.locked" @change="toggleMcp(row.id)" />
                      <span class="cap-main">
                        <span class="cap-name">
                          {{ row.name }}
                          <span v-if="row.disabled" class="cap-tag off">已停用</span>
                        </span>
                        <span v-if="row.stale" class="cap-tag bad">不可用</span>
                        <span v-if="row.desc" class="cap-desc">{{ row.desc }}</span>
                      </span>
                      <span class="cap-meta">{{ row.meta }}</span>
                  </label>
                </div>
              </div>

              <!-- ── Skills：技能目录 ────────────────────────────────── -->
              <div class="caps-group">
                <div class="caps-head">
                  <span class="field-label-row"><span class="caps-title">Skills</span><FieldHelp label="Skills" text="选择 Agent 可使用的工作方法。带关联内置工具的 Skill 可以勾选，相关工具会一起启用或关闭；其他必需系统指南始终保留。“仅手动调用”的技能需要主动发起。" /></span>
                  <span class="caps-count">已选 {{ allSkillRows.filter(row => row.selected).length }}/{{ allSkillRows.length }}</span>
                </div>
                <p class="caps-hint">勾选关联 Skill 时，同时授权下方列出的内置工具；取消勾选会关闭这些工具。文件、命令和外部服务等通用权限仍独立配置。</p>
                <div v-if="skillOptions.unavailable.length" class="unavailable-skills" role="note" aria-label="失效的 Skill 绑定">
                  <p>已有配置包含失效的 Skill，请移除后重新选择。</p>
                  <div v-for="skill in skillOptions.unavailable" :key="skill.id">
                    <span>{{ skill.name }} · {{ skill.reason }}</span>
                    <button type="button" :aria-label="`移除失效 Skill ${skill.name}`" @click="removeUnavailableSkill(skill.id)">移除</button>
                  </div>
                </div>
                <div v-if="allSkillRows.length > 6" class="caps-search">
                  <el-icon aria-hidden="true"><Search /></el-icon>
                  <input v-model="skillQuery" type="search" placeholder="搜索技能" aria-label="搜索技能" />
                </div>
                <div v-if="!allSkillRows.length" class="caps-empty" role="note">
                  还没有可管理的技能。需要时先到「Skill 管理」上传技能，再回来勾选。
                </div>
                <div v-else-if="!skillView.length" class="caps-empty" role="note">没有匹配的技能。</div>
                  <div v-else class="caps-list" role="group" aria-label="技能选择">
                    <label
                      v-for="row in skillView"
                      :key="row.id"
                      class="cap-row"
                      :class="{ on: row.selected, stale: row.stale, sys: row.builtin }"
                    >
                      <input type="checkbox" :checked="row.selected" :indeterminate.prop="!!row.mixed" :aria-checked="row.mixed ? 'mixed' : row.selected" :disabled="row.locked" @change="toggleSkill(row.id)" />
                      <span class="cap-main">
                        <span class="cap-name">
                          {{ row.name }}
                          <span v-if="row.builtin" class="cap-tag sys">系统</span>
                          <span v-if="row.mixed" class="cap-tag">旧配置待关联</span>
                        </span>
                        <span v-if="row.stale" class="cap-tag bad">不可用</span>
                        <span class="cap-desc">{{ row.desc }}</span>
                        <span v-if="row.nativeTools?.length" class="cap-desc">关联工具：{{ row.nativeTools.join('、') }}</span>
                      </span>
                      <span class="cap-meta">{{ row.meta }}</span>
                    </label>
                  </div>
              </div>
              <p v-if="runtimeSkillMode" class="caps-hint">勾选 Skill 后自动关联它包含的工具和数据；需要的 MCP 服务可同时选择。</p>
              <SkillToolsConfiguration v-if="agentCapabilities" :selection="skillSelection" :catalog="agentCapabilities" @validation="runtimeSkillValidation = $event" />
            </template>
          </section>

          <section class="group" aria-labelledby="eg-presence">
            <h3 id="eg-presence" class="group-title">开场提示与外观</h3>

            <div class="field">
              <div class="field-label-row"><span class="flabel">开场提示<span class="opt">（可选）</span></span><FieldHelp label="开场提示" text="显示在这个 Agent 的对话首页，帮助使用者快速开始。点击卡片会把完整问题填入输入框，确认发送后才交给 Agent；它不会自动执行，也不会作为长期指令。" /></div>
              <div class="starter-generation">
                <div class="starter-generation-controls">
                  <span class="field-label-row"><label class="fhint" for="starter-generation-count">生成条数</label><FieldHelp label="生成条数" :text="`一次生成 1 到 ${generationMax} 条。新提示会追加，保留你已填写的内容；开场提示总数不限。`" /></span>
                  <input id="starter-generation-count" v-model.number="generationCount" class="input starter-generation-count" type="number" min="1" :max="generationMax" step="1" :disabled="generating || saving" />
                  <button type="button" class="btn soft sm starter-generate-btn" :disabled="!canGenerate" :aria-busy="generating" aria-describedby="starter-generation-hint" @click="generateStarters">
                    <el-icon :class="{ spin: generating }" aria-hidden="true"><component :is="generating ? Refresh : MagicStick" /></el-icon>
                    {{ generating ? '生成中…' : 'AI 生成' }}
                  </button>
                  <button v-if="generating" type="button" class="btn ghost sm starter-generation-cancel" @click="cancelGeneration()">取消</button>
                </div>
                <p id="starter-generation-hint" class="fhint starter-generation-hint">{{ generationReason || `使用默认模型 ${form.model}，根据名称、简介、指令及已选技能与工具生成。` }}</p>
                <p v-if="generationMessage" class="starter-generation-state" role="status">{{ generationMessage }}</p>
                <p v-if="generationError" class="starter-generation-error" role="alert">{{ generationError }}</p>
              </div>
              <div class="starters">
                <!-- One row per opening card: the full question is required,
                     icon, title and label are the card's glyph, heading and
                     corner tag on the chat homepage — leave them empty for a
                     plain-text opening. -->
                <div v-for="(s, i) in form.starters" :key="i" class="starter-row">
                  <div class="starter-field starter-icon">
                    <span class="field-label-row starter-field-label"><span>图标</span><FieldHelp :label="`开场提示 ${i + 1} 图标`" text="显示在这条开场卡片上的图标。点击后在网格中选择；也可以选“无”以不显示图标。" /></span>
                    <StarterIconPicker v-model="s.icon" :label="`开场提示 ${i + 1} 图标`" />
                  </div>
                  <div class="starter-field starter-title">
                    <span class="field-label-row starter-field-label"><label :for="`starter-title-${i}`">标题</label><FieldHelp :label="`开场提示 ${i + 1} 标题`" text="卡片上的简短标题，例如“分析项目”。可选；不填写时会使用完整问题作为卡片内容。" /></span>
                    <input
                    :id="`starter-title-${i}`"
                    v-model="s.title"
                    class="input"
                    type="text"
                    :maxlength="AGENT_FORM_LIMITS.starterTitle"
                    :placeholder="`标题 ${i + 1}（可选）`"
                    :aria-label="`开场提示 ${i + 1} 标题`"
                    />
                  </div>
                  <div class="starter-field starter-label">
                    <span class="field-label-row starter-field-label"><label :for="`starter-label-${i}`">标签</label><FieldHelp :label="`开场提示 ${i + 1} 标签`" text="卡片右上角的短标签，例如“推荐”或“进阶”。仅用于展示，不会加到发送的问题中；可以留空。" /></span>
                    <input
                    :id="`starter-label-${i}`"
                    v-model="s.label"
                    class="input"
                    type="text"
                    :maxlength="AGENT_FORM_LIMITS.starterLabel"
                    :placeholder="`标签 ${i + 1}（可选）`"
                    :aria-label="`开场提示 ${i + 1} 标签`"
                    />
                  </div>
                  <button
                    v-if="form.starters.length > 3 || s.prompt.trim()"
                    type="button"
                    class="iconbtn sm starter-del"
                    :aria-label="`删除开场提示 ${i + 1}`"
                    @click="removeStarter(i)"
                  >
                    <el-icon aria-hidden="true"><Close /></el-icon>
                  </button>
                  <div class="starter-field starter-prompt">
                    <span class="field-label-row starter-field-label"><label :for="`starter-prompt-${i}`">完整问题</label><FieldHelp :label="`开场提示 ${i + 1} 完整问题`" text="点击这条开场提示时，填入对话输入框的完整内容。这是实际发送给 Agent 的问题；每条要保存的提示都需要填写。" /></span>
                    <template v-if="s.parts?.length">
                      <InlineComposer
                        :model-value="starterCardComposerParts(s)"
                        class="starter-structured-preview"
                        :disabled="true"
                        :aria-label="`开场提示 ${i + 1}，含引用的完整问题`"
                      />
                      <p class="starter-structured-hint">此卡片已保存文字和引用的排列顺序。请在对话页的「快捷卡片 → 编辑」中修改完整问题；这里可修改标题、标签和图标。</p>
                    </template>
                    <input
                    v-else
                    :id="`starter-prompt-${i}`"
                    v-model="s.prompt"
                    class="input"
                    type="text"
                    :maxlength="AGENT_FORM_LIMITS.starter"
                    :placeholder="['例如：帮我梳理这个问题的思路', '例如：把这份资料提炼成要点', '例如：对比两个方案并给出建议'][i] || '开场问题（点击后填入输入框的完整问题）'"
                    :aria-label="`开场提示 ${i + 1}`"
                    />
                  </div>
                </div>
                <button
                  type="button"
                  class="btn ghost sm starter-add"
                  @click="addStarter"
                >
                  <el-icon aria-hidden="true"><Plus /></el-icon>
                  添加开场提示
                </button>
              </div>
              <div class="fmeta"><span class="fhint">标题与标签会渲染成对话首页的卡片；留空则为普通文字问题。</span></div>
            </div>

            <div class="field">
              <div class="field-label-row"><span class="flabel" id="mf-color-label">颜色</span><FieldHelp label="颜色" text="设置 Agent 头像的主题颜色，方便在列表和对话中识别。颜色只影响外观。" /></div>
              <div class="swatch-row" role="group" aria-labelledby="mf-color-label">
                <button
                  v-for="c in AVATAR_COLORS"
                  :key="c"
                  type="button"
                  class="swatch"
                  :class="{ on: form.color === c }"
                  :style="{ background: c }"
                  :aria-pressed="form.color === c"
                  :aria-label="`选择颜色 ${c}`"
                  @click="form.color = c"
                >
                  <el-icon v-if="form.color === c" aria-hidden="true"><Check /></el-icon>
                </button>
              </div>
            </div>

            <div class="field">
              <div class="field-label-row"><span class="flabel" id="mf-icon-label">图标</span><FieldHelp label="图标" text="设置 Agent 的头像图标，显示在 Agent 列表和对话中。它与每条开场提示的卡片图标可以分别选择。" /></div>
              <StarterIconPicker v-model="form.icon" module="agents" :allow-none="false" :disabled="saving" label="Agent 头像图标" />
            </div>
        </section>
        </form>
      </fieldset>

      <footer class="ed-foot">
        <span class="foot-note">
          {{
            saving
              ? "正在保存，请稍候…"
              : capabilitiesReady
                ? mode === "edit"
                  ? "模型设置会同步到已有对话；其他配置用于新对话。"
                  : "草稿稍后可以再编辑并发布。"
                : "技能与 MCP 清单加载完成后才能保存。"
          }}
        </span>
        <div class="foot-actions">
          <button type="button" class="btn ghost" :disabled="saving" @click="requestClose">取消</button>
          <template v-if="mode === 'create'">
            <button type="button" class="btn soft" :disabled="saving || !canSubmit" @click="submitCreate(false)">
              保存草稿
            </button>
            <button type="button" class="btn solid" :disabled="saving || !canPublish" @click="submitCreate(true)">
              创建并进入对话
            </button>
          </template>
          <template v-else>
            <button
              v-if="editingIsDraft || !canPublish"
              type="button"
              class="btn soft"
              :disabled="saving || !canSubmit"
              @click="submitEdit('draft')"
            >
              保存草稿
            </button>
            <button
              type="button"
              class="btn solid"
              :disabled="saving || !canPublish"
              @click="submitEdit(editingIsDraft ? 'ready' : undefined)"
            >
              {{ editingIsDraft ? "发布 Agent" : "保存更改" }}
            </button>
          </template>
        </div>
      </footer>
    </div>
  </dialog>
</template>

<style scoped>
.unavailable-skills { margin: 12px 0; padding: 12px; border-radius: 8px; background: var(--el-color-warning-light-9); color: var(--el-text-color-regular); font-size: 12px; }
.unavailable-skills p { margin: 0 0 8px; }
.unavailable-skills > div { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.unavailable-skills span { overflow-wrap: anywhere; }
.unavailable-skills button { color: var(--el-color-primary); border: 0; background: transparent; padding: 5px; cursor: pointer; white-space: nowrap; }
/* The whole window is the dialog: no card-in-the-middle, no radius to round
   off — the page behind is inert under the backdrop and this is the Studio's
   one full-space task. */
.editor {
  width: 100vw;
  height: 100dvh;
  max-width: none;
  max-height: none;
  margin: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  background: var(--el-bg-color-page);
  color: var(--el-text-color-primary);
  font: inherit;
}
.editor::backdrop {
  background: var(--el-mask-color);
}
.editor[open] {
  animation: ed-in 0.18s ease;
}
.editor.closing {
  opacity: 0;
  transition: opacity 0.14s ease;
}
@keyframes ed-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

.ed-shell {
  height: 100%;
  display: flex;
  flex-direction: column;
}

.ed-head {
  flex: none;
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 20px 28px 16px;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.ed-head-text h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 700;
  letter-spacing: -0.01em;
}
.ed-head-text p {
  margin: 4px 0 0;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}

.ed-error {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 14px 28px 0;
  background: var(--el-color-danger-light-9);
  border: 1px solid var(--el-color-danger-light-7);
  color: var(--el-color-danger);
  border-radius: 10px;
  padding: 10px 14px;
  font-size: 13px;
}
.ed-error-msg {
  flex: 1;
  min-width: 0;
}

.ed-discard {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin: 14px 28px 0;
  background: var(--el-color-warning-light-9);
  border: 1px solid var(--el-color-warning-light-7);
  color: var(--el-text-color-primary);
  border-radius: 10px;
  padding: 10px 14px;
  font-size: 13px;
}
.ed-discard-actions {
  display: inline-flex;
  gap: 8px;
}

/* The one scrolling region. */
.ed-body {
  margin: 0;
  border: 0;
  min-inline-size: 0;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 20px 28px 32px;
}

.group-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: var(--el-text-color-secondary);
  margin-bottom: 10px;
}

.ed-templates {
  margin-bottom: 22px;
}

/* One horizontal line of cards, however many starting points there are: the
   row scrolls natively, the fades and arrows only make the overflow visible. */
.tpl-scroll {
  position: relative;
}
.tpl-scroll::before,
.tpl-scroll::after {
  content: "";
  position: absolute;
  top: 0;
  bottom: 6px;
  width: 30px;
  z-index: 1;
  pointer-events: none;
  opacity: 0;
  transition: opacity var(--pg-transition);
}
.tpl-scroll::before {
  left: 0;
  background: linear-gradient(to right, var(--el-bg-color-page), transparent);
}
.tpl-scroll::after {
  right: 0;
  background: linear-gradient(to left, var(--el-bg-color-page), transparent);
}
.tpl-scroll.can-left::before,
.tpl-scroll.can-right::after {
  opacity: 1;
}
.tpl-row {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: minmax(180px, 220px);
  gap: 10px;
  overflow-x: auto;
  scrollbar-width: none;
  padding: 2px 2px 8px;
}
.tpl-row::-webkit-scrollbar {
  display: none;
}
.tpl-nav {
  position: absolute;
  top: 42%;
  transform: translateY(-50%);
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 50%;
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  box-shadow: var(--el-box-shadow-light);
  cursor: pointer;
  z-index: 2;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.tpl-nav:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.tpl-nav .el-icon {
  font-size: 15px;
}
.tpl-nav.left {
  left: -10px;
}
.tpl-nav.right {
  right: -10px;
}

.tpl-wrap {
  position: relative;
  display: flex;
  min-width: 0;
}
.tpl-wrap > .tpl {
  flex: 1;
}
.tpl-wrap.on > .tpl {
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.tpl-del {
  position: absolute;
  top: 5px;
  right: 5px;
  z-index: 1;
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-placeholder);
  cursor: pointer;
  opacity: 0.45;
  transition: opacity var(--pg-transition), background var(--pg-transition), color var(--pg-transition);
}
.tpl-wrap:hover .tpl-del,
.tpl-wrap:focus-within .tpl-del,
.tpl-del:focus-visible {
  opacity: 1;
}
.tpl-del:hover {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.tpl-confirm {
  position: absolute;
  inset: 0;
  z-index: 2;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 9px;
  border: 1px solid var(--el-color-danger-light-5);
  border-radius: var(--pg-radius-card);
  background: var(--el-bg-color);
  padding: 10px;
  text-align: center;
}
.tpl-confirm-text {
  font-size: 12.5px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.tpl-confirm-actions {
  display: inline-flex;
  gap: 7px;
}

.tpl {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 7px;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
  border-radius: var(--pg-radius-card);
  padding: 13px 14px;
  font: inherit;
  text-align: left;
  cursor: pointer;
  color: var(--el-text-color-primary);
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.tpl:hover {
  border-color: var(--el-border-color);
  box-shadow: var(--el-box-shadow-lighter);
}
.tpl.on {
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.tpl-blank {
  width: 34px;
  height: 34px;
  display: grid;
  place-items: center;
  border: 1px dashed var(--el-border-color);
  border-radius: 10px;
  color: var(--el-text-color-secondary);
  font-size: 17px;
}
.tpl-blank .el-icon {
  font-size: 17px;
}
.tpl-name {
  font-size: 13.5px;
  font-weight: 600;
}
.tpl-desc {
  font-size: 12px;
  line-height: 1.55;
  color: var(--el-text-color-secondary);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* The trailing "save as template" card: a dashed twin of the others that
   flips into a name field capturing the current draft. */
.tpl-add {
  border-style: dashed;
}
.tpl-add-icon {
  width: 34px;
  height: 34px;
  display: grid;
  place-items: center;
  border: 1px dashed var(--el-border-color);
  border-radius: 10px;
  color: var(--el-text-color-secondary);
  font-size: 16px;
  box-sizing: border-box;
}
.tpl-add-form {
  cursor: default;
}
.tpl-add-form:hover {
  border-color: var(--el-border-color-lighter);
  box-shadow: none;
}
.tpl-add-input {
  width: 100%;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-bg-color);
  padding: 7px 10px;
  font: inherit;
  font-size: 12.8px;
  color: var(--el-text-color-primary);
  box-sizing: border-box;
}
.tpl-add-input:focus {
  outline: 0;
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.tpl-add-input[aria-invalid="true"] {
  border-color: var(--el-color-danger-light-3);
}
.tpl-add-err {
  font-size: 11.5px;
  color: var(--el-color-danger);
}
.tpl-add-actions {
  display: inline-flex;
  gap: 7px;
}

/* The form owns the whole width now that the preview is gone: its group cards
   spread flat in two columns on a wide window, one column on a narrow one. */
.ed-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 24px;
  min-width: 0;
}
.group {
  display: flex;
  flex-direction: column;
  gap: 16px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--pg-radius-card);
  padding: 18px 20px 20px;
}
.group-title {
  margin: 0;
  font-size: 13px;
  font-weight: 650;
  color: var(--el-text-color-primary);
}

.field {
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.flabel {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.field-label-row { display: inline-flex; align-items: center; gap: 7px; min-width: 0; }
.model-default-help { margin-top: 3px; }
.req {
  color: var(--el-color-danger);
}
.opt {
  font-weight: 400;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.input {
  width: 100%;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-bg-color);
  padding: 9px 12px;
  font: inherit;
  font-size: 13.5px;
  color: var(--el-text-color-primary);
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
  box-sizing: border-box;
}
.input::placeholder {
  color: var(--el-text-color-placeholder);
}
.input:focus {
  outline: 0;
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.input[aria-invalid="true"] {
  border-color: var(--el-color-danger-light-3);
}
textarea.input {
  resize: vertical;
  line-height: 1.6;
}
textarea.code {
  font-family: var(--pg-mono);
  font-size: 12.8px;
}
.select-wrap {
  position: relative;
  display: block;
}
select.input {
  appearance: none;
  padding-right: 34px;
}
.sel-caret {
  position: absolute;
  right: 12px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 13px;
  color: var(--el-text-color-secondary);
  pointer-events: none;
}
.fmeta {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-height: 16px;
}
.fhint {
  font-size: 11.8px;
  color: var(--el-text-color-secondary);
}
.ferr {
  font-size: 12px;
  color: var(--el-color-danger);
  font-weight: 500;
}
.fcount {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--el-text-color-placeholder);
  font-variant-numeric: tabular-nums;
}
.model-empty {
  border: 1px dashed var(--el-color-warning-light-5);
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
  border-radius: 8px;
  padding: 12px 14px;
  font-size: 13px;
}

/* Capability selectors: card rows, not dropdowns — every grant says what it
   is, whether it is available, and (for models) whether it is the default. */
.caps-group {
  display: flex;
  flex-direction: column;
  gap: 9px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-bg-color);
  padding: 12px 13px;
}
.caps-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.caps-title {
  font-size: 12.5px;
  font-weight: 650;
  color: var(--el-text-color-primary);
}
.caps-count {
  flex: none;
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}
.caps-hint {
  margin: 0;
  font-size: 11.8px;
  line-height: 1.55;
  color: var(--el-text-color-secondary);
}
.caps-search {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-bg-color);
  padding: 0 10px;
  transition: border-color var(--pg-transition), box-shadow var(--pg-transition);
}
.caps-search:focus-within {
  border-color: var(--el-color-primary);
  box-shadow: var(--pg-ring);
}
.caps-search .el-icon {
  flex: none;
  font-size: 14px;
  color: var(--el-text-color-placeholder);
}
.caps-search input {
  width: 100%;
  min-width: 0;
  height: 31px;
  border: 0;
  outline: 0;
  background: transparent;
  font: inherit;
  font-size: 12.8px;
  color: var(--el-text-color-primary);
}
.caps-search input::placeholder {
  color: var(--el-text-color-placeholder);
}
.caps-list {
  display: flex;
  flex-direction: column;
  gap: 7px;
  max-height: 302px;
  overflow-y: auto;
  padding-right: 2px;
}
.cap-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 9px;
  background: var(--el-bg-color);
  padding: 9px 11px;
  cursor: pointer;
  transition: border-color var(--pg-transition), background var(--pg-transition);
}
.cap-row:hover {
  border-color: var(--el-border-color);
}
.cap-row.on {
  border-color: var(--pg-accent-line);
  background: var(--pg-accent-wash);
}
/* A disabled MCP server reads as off-limits at a glance: the familiar
   cross-hatch of a disabled control, muted text, no hover tease. */
.cap-row.locked {
  cursor: not-allowed;
  background: repeating-linear-gradient(
    -45deg,
    var(--el-fill-color-light) 0 7px,
    var(--el-fill-color-lighter) 7px 14px
  );
  color: var(--el-text-color-secondary);
}
.cap-row.locked:hover {
  border-color: var(--el-border-color-lighter);
}
.cap-row.locked .cap-name {
  color: var(--el-text-color-secondary);
}
.cap-row.stale {
  border-style: dashed;
  border-color: var(--el-color-warning-light-5);
}
.cap-row > input[type="checkbox"] {
  width: 16px;
  height: 16px;
  margin: 0;
  flex: none;
  accent-color: var(--el-color-primary);
}
.cap-check input[type="checkbox"] {
  width: 16px;
  height: 16px;
  margin: 0;
  flex: none;
  accent-color: var(--el-color-primary);
}
.cap-row.model {
  grid-template-columns: minmax(0, 1fr) auto;
}
.cap-check {
  grid-column: 1 / 3;
  display: flex;
  align-items: center;
  gap: 9px;
  min-width: 0;
}
.cap-default {
  grid-column: 3;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  flex: none;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 999px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
  padding: 3px 9px;
  font-size: 11.5px;
  cursor: pointer;
}
.cap-default input {
  width: 13px;
  height: 13px;
  margin: 0;
  accent-color: var(--el-color-primary);
}
.cap-default.on {
  border-color: var(--pg-accent-line);
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  font-weight: 600;
}
.cap-default:has(input:disabled) {
  opacity: 0.55;
  cursor: not-allowed;
}
.cap-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.cap-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12.8px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.cap-desc {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.8px;
  color: var(--el-text-color-secondary);
}
.cap-meta {
  flex: none;
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}
.cap-tag.bad {
  flex: none;
  border-radius: 999px;
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
  padding: 1px 6px;
  font-size: 10.5px;
  font-weight: 600;
}
.cap-row.sys {
  cursor: default;
}
.cap-row.sys:hover {
  border-color: var(--pg-accent-line);
}
.cap-tag.sys {
  flex: none;
  border-radius: 999px;
  background: var(--pg-accent-wash);
  color: var(--el-color-primary);
  box-shadow: inset 0 0 0 1px var(--pg-accent-line);
  padding: 1px 6px;
  font-size: 10.5px;
  font-weight: 600;
}
.cap-tag.off {
  flex: none;
  border-radius: 999px;
  background: var(--el-fill-color-dark, var(--el-fill-color));
  color: var(--el-text-color-secondary);
  padding: 1px 6px;
  font-size: 10.5px;
  font-weight: 600;
}
.caps-empty {
  margin: 0;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
  padding: 12px 14px;
  font-size: 12.3px;
  line-height: 1.55;
}
.caps-state {
  display: flex;
  align-items: center;
  gap: 9px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 9px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
  padding: 12px 13px;
  font-size: 12.5px;
}
.caps-state.bad {
  border-color: var(--el-color-danger-light-7);
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.caps-state-msg {
  flex: 1;
  min-width: 0;
  word-break: break-word;
}
.caps-state.bad .btn.ghost {
  border-color: var(--el-color-danger-light-5);
  color: var(--el-color-danger);
}
.caps-inherit {
  margin: 0;
  border-left: 3px solid var(--pg-accent-line);
  background: var(--pg-accent-wash);
  color: var(--el-text-color-regular);
  border-radius: 0 8px 8px 0;
  padding: 10px 12px;
  font-size: 12.3px;
  line-height: 1.6;
}
.caps-warn {
  margin: 0;
  border-left: 3px solid var(--el-color-warning-light-5);
  background: var(--el-color-warning-light-9);
  color: var(--el-text-color-regular);
  border-radius: 0 8px 8px 0;
  padding: 10px 12px;
  font-size: 12.3px;
  line-height: 1.6;
}
.spin {
  animation: spin-capabilities 0.9s linear infinite;
}
@keyframes spin-capabilities {
  to {
    transform: rotate(360deg);
  }
}

.chip-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  border: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
  color: var(--el-text-color-regular);
  border-radius: 999px;
  padding: 5px 13px;
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition), border-color var(--pg-transition);
}
.chip:hover {
  border-color: var(--el-border-color-hover);
}
.chip.on {
  background: var(--pg-accent-wash);
  border-color: var(--pg-accent-line);
  color: var(--el-color-primary);
  font-weight: 600;
}

.swatch-row {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.swatch {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  border: 0;
  box-shadow: inset 0 0 0 1px rgb(0 0 0 / 0.08);
  cursor: pointer;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--el-color-white, #fff);
  transition: transform var(--pg-transition), box-shadow var(--pg-transition);
}
.swatch:hover {
  transform: scale(1.08);
}
.swatch.on {
  box-shadow: 0 0 0 2px var(--el-bg-color), 0 0 0 4px var(--el-color-primary);
}

.iconpick-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.iconpick {
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
  border-radius: 10px;
  padding: 5px;
  cursor: pointer;
  transition: border-color var(--pg-transition), background var(--pg-transition);
}
.iconpick:hover {
  border-color: var(--el-border-color-hover);
}
.iconpick.on {
  border-color: var(--el-color-primary);
  background: var(--pg-accent-wash);
}

.starters {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.starter-row {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr) minmax(0, 1fr) 28px;
  align-items: end;
  gap: 9px;
  padding: 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  background: var(--el-fill-color-extra-light);
}
.starter-field { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
.starter-field-label { font-size: 11px; color: var(--el-text-color-secondary); }
.starter-field .input { font-size: 12.5px; padding: 8px 9px; height: 34px; }
.starter-prompt { grid-column: 1 / -1; }
.starter-structured-preview { padding: 8px 9px; border: 1px solid var(--el-border-color-lighter); border-radius: 7px; font-size: 12.5px; opacity: 1; }
.starter-structured-preview :deep(.inline-composer-document) { min-height: 36px; max-height: 132px; }
.starter-structured-preview :deep(.inline-reference-remove) { display: none; }
.starter-structured-hint { margin: 3px 0 0; font-size: 11px; line-height: 1.6; color: var(--el-text-color-secondary); }
.starter-del {
  color: var(--el-text-color-placeholder);
  margin-bottom: 2px;
}
.starter-del:hover {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}
.starter-add {
  align-self: flex-start;
}
.starter-generation { padding: 12px; border-radius: 8px; border: 1px solid var(--el-border-color-lighter); }
.starter-generation-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.starter-generation-count { width: 68px; padding: 6px 8px; }
.starter-generation-hint { margin: 8px 0 0; line-height: 1.6; overflow-wrap: anywhere; }
.starter-generation-state { margin: 8px 0 0; font-size: 12px; line-height: 1.6; color: var(--el-color-success); }
.starter-generation-error { margin: 8px 0 0; font-size: 12px; line-height: 1.6; color: var(--el-color-danger); overflow-wrap: anywhere; }
@media (max-width: 900px) {
  .starter-row {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    position: relative;
  }
  .starter-icon,
  .starter-prompt {
    grid-column: 1 / -1;
  }
  .starter-del {
    position: absolute;
    right: 9px;
    top: 8px;
  }
}

.ed-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 13px 28px;
  background: var(--el-bg-color);
  border-top: 1px solid var(--el-border-color-lighter);
}
.foot-note {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex: 1;
  min-width: 0;
}
.foot-actions {
  display: flex;
  gap: 10px;
  flex: none;
}

/* Buttons: the affirmative action is the solid ink of the composer, ghosts and
   softs stay on fills — no gradient, no second accent. */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  padding: 9px 16px;
  font: inherit;
  font-size: 13px;
  font-weight: 550;
  cursor: pointer;
  border: 1px solid transparent;
  transition: background var(--pg-transition), border-color var(--pg-transition), color var(--pg-transition);
}
.btn.sm {
  padding: 6px 12px;
  font-size: 12.5px;
}
.btn.solid {
  background: var(--pg-solid);
  border-color: var(--pg-solid);
  color: var(--pg-on-solid);
}
.btn.solid:hover {
  background: var(--pg-solid-hover);
  border-color: var(--pg-solid-hover);
}
.btn.soft {
  background: var(--el-fill-color-light);
  border-color: var(--el-border-color-light);
  color: var(--el-text-color-primary);
}
.btn.soft:hover {
  background: var(--el-fill-color);
}
.btn.ghost {
  background: transparent;
  border-color: var(--el-border-color);
  color: var(--el-text-color-regular);
}
.btn.ghost:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}
.btn.danger {
  background: var(--el-color-danger);
  border-color: var(--el-color-danger);
  color: var(--el-color-white, #fff);
}
.btn.danger:hover {
  background: var(--el-color-danger-dark-2);
  border-color: var(--el-color-danger-dark-2);
}
.btn:disabled {
  opacity: 0.55;
  cursor: wait;
}

.iconbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  transition: background var(--pg-transition), color var(--pg-transition);
}
.iconbtn.sm {
  width: 28px;
  height: 28px;
}
.iconbtn:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}

@media (max-width: 1100px) {
  .ed-form {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 640px) {
  .ed-head {
    padding: 14px 16px 12px;
  }
  .ed-body {
    padding: 14px 16px 24px;
  }
  .ed-error,
  .ed-discard {
    margin: 10px 16px 0;
  }
  .ed-foot {
    padding: 10px 16px;
    flex-wrap: wrap;
  }
  .foot-note {
    flex-basis: 100%;
    order: 2;
  }
  .foot-actions {
    width: 100%;
  }
  .foot-actions .btn {
    flex: 1;
    justify-content: center;
    min-height: 44px;
    padding: 10px 8px;
    font-size: 12px;
    white-space: nowrap;
  }
  .cap-row {
    grid-template-columns: auto minmax(0, 1fr);
    align-items: start;
  }
  .cap-meta {
    grid-column: 2;
    max-width: 100%;
    margin-top: 4px;
  }
  .cap-row.model {
    grid-template-columns: minmax(0, 1fr);
  }
  .cap-check {
    grid-column: auto;
  }
  .cap-default {
    grid-column: 1;
    justify-self: start;
    margin-top: 8px;
  }
  .cap-desc {
    white-space: normal;
  }
}

@media (prefers-reduced-motion: reduce) {
  .spin {
    animation: none;
  }
}
</style>
