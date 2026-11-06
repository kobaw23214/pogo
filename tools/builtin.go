package tools

import "slices"

// The native tools as an admin-facing catalog: what the deployment has, what
// each one does, and which of them a per-Agent selection may withhold. The
// protected core is enforced in New, not only at the call sites that build an
// Options — a selection that names everything except read would otherwise be
// a session that cannot even read its own spill notes.
//
// The order here is the display order everywhere the catalog is served
// (/api/tools, the capabilities endpoint, the Agent editor's picker).

// BuiltinInfo is one native tool's catalog entry. ReadOnly says calling it
// cannot mutate the workspace or the outside world (browser fails that test:
// once its gate is armed, page operations change things). Protected marks
// the core a selection can never remove.
type BuiltinInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	Protected   bool   `json:"protected"`
	// SkillBinding identifies a host-defined native capability group. This is
	// catalog metadata, not a grant supplied by a Skill manifest or its name.
	SkillBinding string `json:"skill_binding,omitempty"`
	Legacy       bool   `json:"legacy,omitempty"`
	ContextOnly  bool   `json:"context_only,omitempty"`
}

var nativeSkillBindings = []struct {
	name   string
	tools  []string
	legacy []string
}{
	{name: "agent-workflows", tools: []string{AgentWorkflowReadName, AgentWorkflowEditName, AgentWorkflowEnableName, AgentWorkflowRunName}},
	{name: "background-tasks", tools: []string{BackgroundTaskName}, legacy: []string{WatchName, WatchTestName}},
	{name: "browser-use", tools: []string{"browser"}},
	{name: "skill-creator", tools: []string{SkillSaveName}},
}

// SkillBindingNames is the ordered, host-owned binding catalog. Callers must
// still verify that a selected Skill is the trusted embedded catalog entry.
func SkillBindingNames() []string {
	names := make([]string, 0, len(nativeSkillBindings))
	for _, binding := range nativeSkillBindings {
		names = append(names, binding.name)
	}
	return names
}

// SkillBindingTools returns the complete native grant for an explicitly
// selected binding. Generic file tools and protected readers stay independent.
func SkillBindingTools(name string) []string {
	for _, binding := range nativeSkillBindings {
		if binding.name == name {
			return slices.Clone(binding.tools)
		}
	}
	return nil
}

// SkillBindingLegacyTools names retained old permissions associated with a
// group. They may be preserved or revoked, never inferred as the full grant.
func SkillBindingLegacyTools(name string) []string {
	for _, binding := range nativeSkillBindings {
		if binding.name == name {
			return slices.Clone(binding.legacy)
		}
	}
	return nil
}

func SkillBindingForTool(name string) string {
	for _, binding := range nativeSkillBindings {
		if slices.Contains(binding.tools, name) {
			return binding.name
		}
	}
	return ""
}

// protectedBuiltin is the closable line drawn through the catalog: the
// read-only four are the ground every other feature stands on (skill reads,
// spill note recovery), todo and subagent are session coordination rather
// than capabilities, and analyze_image exists only where a vision model was
// configured — a deployment-level fact, not a per-Agent choice. What remains
// (write, edit, bash, browser, shortcut_manage) is the selectable half: verbs
// with real side effects an Agent definition may deliberately withhold.
// Shortcut reads, like information reads, still require a host-bound store.
var protectedBuiltin = map[string]bool{
	"read": true, "ls": true, "find": true, "grep": true,
	ToolResultReadName: true,
	ShortcutQueryName:  true,
	ShortcutSearchName: true, ShortcutReadName: true, ShortcutListName: true,
	SkillSearchName: true, SkillReadName: true,
	WatchDecideName: true,
	"todo":          true, "subagent": true, "analyze_image": true,
}

// IsProtectedBuiltin reports whether a per-Agent tool selection may withhold
// the named built-in. Unknown names are not protected — they are simply not
// built-ins, which the catalog answers.
func IsProtectedBuiltin(name string) bool { return protectedBuiltin[name] }

// The host's shortcut verbs cannot be supplied by a Skill or MCP projection,
// including when creation was withheld by the Agent's native-tool selection.
func hostShortcutName(name string) bool {
	switch name {
	case ShortcutQueryName, ShortcutManageName, ShortcutSearchName, ShortcutReadName, ShortcutCreateName, ShortcutListName, ShortcutFolderCreateName, ShortcutUpdateName, ShortcutFolderRenameName, ShortcutMoveName, ShortcutDeleteName, ShortcutMergeName:
		return true
	}
	return false
}

// Scoped Skill lifecycle verbs are host capabilities, not manifest or MCP
// tool names. A selected runtime reader is admitted only as its trusted type.
func hostWatchName(name string) bool {
	return name == WatchName || name == WatchTestName || name == WatchDecideName || name == BackgroundTaskName
}

func hostSkillAuthorName(name string) bool {
	return name == SkillSearchName || name == SkillReadName || name == SkillSaveName
}

func hostWorkflowName(name string) bool {
	if IsPlatformWorkflowTool(name) {
		return true
	}
	switch name {
	case AgentWorkflowReadName, AgentWorkflowEditName, AgentWorkflowEnableName, AgentWorkflowRunName:
		return true
	}
	return false
}

// IsPlatformWorkflowTool keeps retired platform verbs reserved so a Skill or
// MCP projection cannot reintroduce them into an Agent's tool registry.
func IsPlatformWorkflowTool(name string) bool {
	switch name {
	case WorkflowDiscoverName, WorkflowReadName, WorkflowValidateName, WorkflowEditName, WorkflowTestName, WorkflowRunName, WorkflowPublishName:
		return true
	}
	return false
}

// BuiltinCatalog lists the current visible native tool names in display
// order. BuiltinNameSet additionally accepts persisted legacy grant names. analyze_image closes the list: it is registered only when a
// vision model is configured, and its description says so rather than the
// catalog pretending the registration is unconditional.
func BuiltinCatalog() []BuiltinInfo {
	result := []BuiltinInfo{
		{Name: WebServiceName, Description: "启动与管理受管 Python/Node.js 网站，提供网站地址、重命名、自定义续期、日志、重启及停止；按已配置的运行环境提供"},
		{Name: "read", Description: "读取工作区文件内容", ReadOnly: true, Protected: true},
		{Name: "ls", Description: "列出目录内容", ReadOnly: true, Protected: true},
		{Name: "find", Description: "按名称模式查找文件", ReadOnly: true, Protected: true},
		{Name: "grep", Description: "在文件内容中搜索匹配行", ReadOnly: true, Protected: true},
		{Name: ToolResultReadName, Description: "按结果 ID 读取、筛选或统计已保存的工具输出；不会重复执行原操作", ReadOnly: true, Protected: true},
		{Name: ShortcutQueryName, Description: "查询快捷方式和可引用资料，读取混排快照或浏览目录", ReadOnly: true, Protected: true},
		{Name: ShortcutManageName, Description: "创建、修改、重命名和移动私有快捷方式及文件夹；删除、合并独立授权", ReadOnly: false, Protected: false},
		{Name: ShortcutDeleteName, Description: "删除选中的私有快捷方式或文件夹，不删除只读来源", ReadOnly: false, Protected: false},
		{Name: ShortcutMergeName, Description: "先预览完全相同的私有快捷方式，再按计划合并重复项", ReadOnly: false, Protected: false},
		{Name: SkillSearchName, Description: "查找当前 Agent 可读取的 Skill，查看版本、来源和启用状态", ReadOnly: true, Protected: true},
		{Name: SkillReadName, Description: "读取已授权的 Skill 文档与文件结构，不增加权限", ReadOnly: true, Protected: true},
		{Name: SkillSaveName, Description: "为当前 Agent 创建或更新文档型 Skill，保留版本并在安全时机生效", ReadOnly: false, Protected: false},
		{Name: AgentWorkflowReadName, Description: "读取当前 Agent 的私人内部流程、校验草稿和查看运行产物", ReadOnly: true},
		{Name: AgentWorkflowEditName, Description: "为当前 Agent 创建、局部修改或归档私人内部流程", ReadOnly: false},
		{Name: AgentWorkflowEnableName, Description: "按精确草稿版本启用或停用当前 Agent 的内部流程", ReadOnly: false},
		{Name: AgentWorkflowRunName, Description: "试跑、启动、取消或重试当前 Agent 的内部流程；人工确认由本人处理", ReadOnly: false},
		{Name: BackgroundTaskName, Description: "准备与试运行哨兵脚本，创建和管理当前 Agent 的后台任务，保存截止时间并交由宿主执行", ReadOnly: false},
		{Name: "write", Description: "创建或整体重写文件", ReadOnly: false, Protected: false},
		{Name: "edit", Description: "对已有文件做局部修改", ReadOnly: false, Protected: false},
		{Name: "bash", Description: "执行 shell 命令", ReadOnly: false, Protected: false},
		{Name: "browser", Description: "驱动浏览器面板操作页面；授权随 browser-task 技能的调用打开", ReadOnly: false, Protected: false},
		{Name: "todo", Description: "维护会话的任务清单", ReadOnly: true, Protected: true},
		{Name: "subagent", Description: "派生子代理承接委托任务", ReadOnly: false, Protected: true},
		{Name: "analyze_image", Description: "就已附加的图片向视觉模型提问；仅在配置了视觉模型时注册", ReadOnly: true, Protected: true},
	}
	for i := range result {
		result[i].SkillBinding = SkillBindingForTool(result[i].Name)
	}
	return result
}

// BuiltinRuntimeCatalog describes callable compatibility/context tools as well
// as selectable tools. It is for a real registry-filtered session view, never
// the Agent permission picker: merely listing an entry grants nothing.
func BuiltinRuntimeCatalog() []BuiltinInfo {
	return append(BuiltinCatalog(),
		BuiltinInfo{Name: ShellTasksName, Description: "查看本会话受管 Shell 任务；随 bash 权限提供", ReadOnly: true, ContextOnly: true},
		BuiltinInfo{Name: ShellOutputName, Description: "分页读取本会话受管 Shell 输出；随 bash 权限提供", ReadOnly: true, ContextOnly: true},
		BuiltinInfo{Name: ShellStopName, Description: "停止本会话受管 Shell 任务；随 bash 权限提供", ContextOnly: true},
		BuiltinInfo{Name: WatchName, Description: "旧哨兵权限兼容入口；新配置通过 background_task 管理，保留原有权限范围", Legacy: true},
		BuiltinInfo{Name: WatchTestName, Description: "旧哨兵脚本试运行权限兼容入口；新配置使用 background_task 的 check", Legacy: true},
		BuiltinInfo{Name: WatchDecideName, Description: "仅在宿主交付的当前哨兵事件批次中记录处理决定，不能在普通对话调用", Protected: true, ContextOnly: true},
	)
}

// BuiltinNameSet is the catalog as a lookup set — the reference validation
// the web layer runs over a saved selection.
func BuiltinNameSet() map[string]bool {
	out := make(map[string]bool, len(BuiltinCatalog()))
	for _, info := range BuiltinCatalog() {
		out[info.Name] = true
	}
	// Accept persisted per-operation grants without showing them as separate
	// tools in the current catalog or escalating them into a full group grant.
	for _, name := range []string{ShortcutSearchName, ShortcutReadName, ShortcutListName, ShortcutCreateName, ShortcutFolderCreateName, ShortcutUpdateName, ShortcutFolderRenameName, ShortcutMoveName} {
		out[name] = true
	}
	// The consolidated background_task handles new authoring. Historical
	// watch/check grants stay separately valid, and decision is host-context
	// authority rather than an Agent-editor checkbox.
	for _, name := range []string{WatchName, WatchTestName, WatchDecideName} {
		out[name] = true
	}
	// Historical Agent definitions remain loadable/editable without granting
	// platform access. These names are absent from the selectable catalog and
	// BuiltinSelected always denies them, including legacy nil inheritance.
	for _, name := range []string{WorkflowDiscoverName, WorkflowReadName, WorkflowValidateName, WorkflowEditName, WorkflowTestName, WorkflowRunName, WorkflowPublishName} {
		out[name] = true
	}
	return out
}

// keeps reports whether a non-nil Enabled selection retains the named
// built-in. The protected core is kept whatever the selection says: it is
// infrastructure, and a definition that omits it reads as "nothing optional"
// rather than "remove the ground under the session".
func (o Options) keeps(name string) bool {
	return (name != WatchDecideName || o.SentinelEvent) && BuiltinSelected(o.Enabled, name)
}

// BuiltinSelected resolves current catalog entries and persisted legacy grants.
// A partial legacy management selection stays partial at dispatch time.
func BuiltinSelected(enabled *[]string, name string) bool {
	if hostShellJobName(name) {
		return BuiltinSelected(enabled, "bash")
	}
	if IsPlatformWorkflowTool(name) {
		return false
	}
	if enabled == nil || protectedBuiltin[name] {
		return true
	}
	if name == ShortcutManageName {
		return len(ShortcutManageActions(enabled)) > 0
	}
	for _, verb := range shortcutManageVerbs {
		if verb.legacy == name && slices.Contains(*enabled, ShortcutManageName) {
			return true
		}
	}
	return slices.Contains(*enabled, name)
}
