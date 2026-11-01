package web

// Workflow authoring is an authenticated facade over the same draft, publish
// and Run stores used by the canvas. It never accepts an acting user or token.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

type workflowAuthoringError struct {
	Status      int
	Code        string
	Message     string
	Diagnostics []workflowAuthoringDiagnostic `json:"diagnostics,omitempty"`
}

func (e *workflowAuthoringError) Error() string { return e.Message }
func wfAuthoringErr(status int, code, message string) error {
	return &workflowAuthoringError{Status: status, Code: code, Message: message}
}
func decodeWFAuthoring(raw json.RawMessage, value any) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return wfAuthoringErr(400, "invalid_request", "请求为空或超过大小限制")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return wfAuthoringErr(400, "invalid_request", err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return wfAuthoringErr(400, "invalid_request", "请求包含多余内容")
	}
	// Dep's legacy custom decoder accepts unknown object fields. Authoring
	// refuses them, so a proposed data condition cannot silently disappear.
	var wire any
	if err := json.Unmarshal(raw, &wire); err != nil {
		return wfAuthoringErr(400, "invalid_request", "请求 JSON 无效")
	}
	var check func(any) error
	check = func(value any) error {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if key == "after" {
					if deps, ok := child.([]any); ok {
						for _, dep := range deps {
							if object, ok := dep.(map[string]any); ok {
								for field := range object {
									if field != "node" && field != "when" {
										return wfAuthoringErr(400, "unsupported_dependency_field", "依赖仅支持 node/when，不能忽略字段："+field)
									}
								}
							}
						}
					}
				}
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(wire)
}
func (s *Server) authoringAllowed(r *http.Request, wid, action string) error {
	if wid != "" && !validID(wid) {
		return wfAuthoringErr(400, "invalid_id", "workflow_id 无效")
	}
	allowed, err := s.workflowAllowed(r, wid, action)
	if err != nil {
		return wfAuthoringErr(503, "authorization_unavailable", "暂时无法确认工作流权限")
	}
	if !allowed {
		return wfAuthoringErr(403, "forbidden", "没有执行此工作流操作的权限")
	}
	if wid != "" {
		if _, ok := s.mgr.workflows.get(wid); !ok {
			return wfAuthoringErr(404, "not_found", "工作流不存在或不可用")
		}
	}
	return nil
}
func (s *Server) registerWorkflowAuthoringRoutes() {
	s.mux.HandleFunc("POST /api/manage/workflow-authoring/{action}", func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := decodeBody(r, &body); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": map[string]any{"code": "invalid_request", "message": err.Error()}})
			return
		}
		value, err := s.workflowAuthoringAction(r, r.PathValue("action"), body)
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			var e *workflowAuthoringError
			if !errors.As(err, &e) {
				e = &workflowAuthoringError{Status: 500, Code: "authoring_failed", Message: "工作流操作失败"}
			}
			writeJSON(w, e.Status, map[string]any{"ok": false, "error": map[string]any{"code": e.Code, "message": e.Message, "diagnostics": e.Diagnostics}})
			return
		}
		writeJSON(w, 200, value)
	})
}
func (s *Server) workflowAuthoringAction(r *http.Request, action string, body json.RawMessage) (any, error) {
	switch action {
	case "discover":
		return s.workflowAuthoringDiscover(r, body)
	case "read":
		return s.workflowAuthoringRead(r, body)
	case "validate":
		return s.workflowAuthoringCheck(r, body)
	case "edit":
		return s.workflowAuthoringEdit(r, body)
	case "publish":
		return s.workflowAuthoringPublish(r, body)
	case "test", "run":
		return s.workflowAuthoringRun(r, action, body)
	default:
		return nil, wfAuthoringErr(400, "unsupported_action", "不支持的工作流操作")
	}
}

type workflowAuthoringDiagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	NodeID    string `json:"node_id,omitempty"`
	FieldPath string `json:"field_path"`
	Message   string `json:"message"`
	FixHint   string `json:"fix_hint,omitempty"`
}
type workflowAuthoringValidation struct {
	Valid           bool                          `json:"valid"`
	ReadyForTest    bool                          `json:"ready_for_test"`
	DefinitionHash  string                        `json:"definition_hash"`
	InputKeys       []string                      `json:"input_keys"`
	InputsValidated bool                          `json:"inputs_validated"`
	Diagnostics     []workflowAuthoringDiagnostic `json:"diagnostics"`
}

// This is a description of the actual cut-down node executor, not a grant.
// In particular node.tools narrows native tools, not Skill or MCP selections.
type workflowAuthoringCapabilities struct {
	NativeTools           []string                      `json:"native_tools"`
	Skills                []string                      `json:"skills"`
	MCPServers            []string                      `json:"mcp_servers"`
	ExternalTools         []string                      `json:"external_tools"`
	RuntimeSkillSupported bool                          `json:"runtime_skill_supported"`
	ToolReviewSupported   bool                          `json:"tool_review_supported"`
	Diagnostics           []workflowAuthoringDiagnostic `json:"diagnostics"`
}

func (s *Server) workflowAuthoringCapabilities(r *http.Request, a studioAgent, node workflow.Node) workflowAuthoringCapabilities {
	out := workflowAuthoringCapabilities{NativeTools: []string{}, Skills: []string{}, MCPServers: []string{}, ExternalTools: []string{}, Diagnostics: []workflowAuthoringDiagnostic{}}
	add := func(code, severity, msg, hint string) {
		path := "nodes." + node.ID + ".capabilities"
		if node.ID == "" {
			path = "agents." + a.ID + ".capabilities"
		}
		out.Diagnostics = append(out.Diagnostics, workflowAuthoringDiagnostic{code, severity, node.ID, path, msg, hint})
	}
	v := s.mgr.skillViews()
	subset, mcpOnly := s.mgr.studioSelection(a)
	if subset != nil {
		v = s.mgr.filterSkillViews(v, subset)
		for _, sk := range subset {
			out.Skills = append(out.Skills, sk.Name)
		}
	} else {
		v = s.mgr.unregisterSynthTools(v)
		out.Skills = s.mgr.studioSkillNames(a)
	}
	principal := s.workflowPrincipal(r)
	projected, trusted := s.mgr.mcpProjectionFor(v.synthTools, principal.Scope, mcpOnly)
	if a.MCPServers != nil {
		out.MCPServers = append(out.MCPServers, (*a.MCPServers)...)
	} else {
		for name := range s.mgr.mcpServerConfigs() {
			out.MCPServers = append(out.MCPServers, name)
		}
	}
	selected := narrowWorkflowTools(s.mgr.studioNativeSelection(a), node.Tools)
	// Registry construction is side-effect free; use precisely the node's
	// native options, withholding session-only backends just like executor().
	reg := tools.New(tools.Options{Cwd: s.mgr.cfg.Cwd, Enabled: selected, Todo: true, ResultScope: "workflow-authoring-preview"})
	for _, t := range reg.All() {
		out.NativeTools = append(out.NativeTools, t.Name())
	}
	// MCP readOnlyHint is untrusted scheduling metadata and cannot waive
	// review. Manifest read-only hints additionally require a real sandbox;
	// this node executor has no bound approval/sandbox context, so no Synth
	// tool is certified by the authoring preflight in this first stage.
	for _, source := range []struct {
		tools    []tools.Tool
		approved []string
	}{{v.synthTools, nil}, {projected, trusted}} {
		for _, t := range source.tools {
			out.ExternalTools = append(out.ExternalTools, t.Name())
			if !slices.Contains(source.approved, t.Name()) {
				add("tool_review_unavailable", "error", "该节点包含尚未接入逐工具审批且未由宿主预授权的工具："+t.Name(), "由管理员明确配置受信任能力，或先补齐工作流逐工具审批；readOnlyHint 不构成授权")
			} else {
				add("preapproved_external_execution", "warning", "试跑可能调用宿主已预授权的真实外部工具："+t.Name(), "预授权沿用现有策略；实际试跑不是模拟")
			}
		}
	}
	if selected != nil {
		for _, name := range *selected {
			if !tools.BuiltinNameSet()[name] {
				add("unknown_native_tool", "error", "Agent/节点包含未知内置工具："+name, "从实际内置工具目录修正选择")
				continue
			}
			if !slices.Contains(out.NativeTools, name) {
				add("node_tool_unavailable", "error", "当前工作流执行器没有注册所选会话工具："+name, "移除此节点不支持的工具，或选择其他执行 Agent")
			}
		}
	}
	if len(a.SkillBinding) > 0 {
		add("runtime_skill_unavailable", "error", "该 Agent 绑定了尚未接入工作流的运行型 Skill", "选择当前工作流执行器支持的 Agent；不会静默忽略 Skill 绑定")
	}
	if slices.Contains(out.NativeTools, "bash") {
		add("shell_review_unavailable", "error", "工作流节点未接入普通会话的命令审批链，不能作为安全试跑能力", "在节点 tools 中明确收窄为所需的非 shell 工具")
	}
	if slices.Contains(out.NativeTools, "write") || slices.Contains(out.NativeTools, "edit") {
		add("workspace_write", "warning", "试跑会在独立 Run 工作目录写入文件", "检查生成文件；这不是无副作用模拟")
	}
	for _, sk := range subset {
		if sk.BrowserTask {
			add("browser_session_unavailable", "error", "工作流节点没有普通聊天的浏览器面板绑定", "使用不依赖浏览器会话的执行 Agent")
		}
	}
	if selected != nil && slices.Contains(*selected, "browser") {
		add("browser_session_unavailable", "error", "选择了 browser，但工作流执行器未注册浏览器会话链接", "移除此不支持的能力或选择其他 Agent")
	}
	slices.Sort(out.NativeTools)
	slices.Sort(out.Skills)
	slices.Sort(out.MCPServers)
	slices.Sort(out.ExternalTools)
	return out
}
func (s *Server) workflowAuthoringDiscover(r *http.Request, raw json.RawMessage) (any, error) {
	var req struct {
		Query      string `json:"query"`
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeWFAuthoring(raw, &req); err != nil {
		return nil, err
	}
	if err := s.authoringAllowed(r, req.WorkflowID, wfEdit); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(req.Query))
	agents := []any{}
	for _, a := range s.mgr.studio.list(s.adminRequest(r)) {
		if q != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Description+" "+a.ID), q) {
			continue
		}
		caps := s.workflowAuthoringCapabilities(r, a, workflow.Node{})
		ready := a.Status == "ready"
		for _, d := range caps.Diagnostics {
			if d.Severity == "error" {
				ready = false
			}
		}
		agents = append(agents, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "revision": a.Revision, "status": a.Status, "model": a.Model, "models": a.allowedModels(), "effective_capabilities": caps, "ready_for_test": ready})
	}
	models := []any{}
	for _, m := range config.Catalog() {
		_, err := config.Resolve(m.ID)
		models = append(models, map[string]any{"id": m.ID, "configured": err == nil})
	}
	subjects := []any{}
	for _, u := range s.workflowAccessSubjects() {
		if s.workflowApprovalPrincipalActive(u.PrincipalID) {
			subjects = append(subjects, map[string]any{"username": u.DisplayName, "display_name": u.DisplayName, "principal_id": u.PrincipalID})
		}
	}
	return map[string]any{"ok": true, "agents": agents, "models": models, "builtin_tools": tools.BuiltinCatalog(), "approval_subjects": subjects, "approval_assignment_modes": []string{"fixed", "business"}, "capabilities": map[string]any{"node_kinds": []string{"agent", "human"}, "dependency_conditions": []string{"success", "failed", "rejected", "always"}, "data_conditions": false, "loops": false, "node_tools_scope": "native_only", "runtime_skill_supported": false, "tool_review_supported": false}, "notes": []string{"资源目录不包含指令、凭据、连接地址或私有文件路径", "业务分配审批人需要受信任业务后端提供名单；不从提示词推断组织关系"}}, nil
}

func (s *Server) workflowAuthoringValidate(r *http.Request, in workflowInput, inputs map[string]string) workflowAuthoringValidation {
	result := workflowAuthoringValidation{Valid: true, ReadyForTest: true, InputKeys: []string{}, Diagnostics: []workflowAuthoringDiagnostic{}}
	add := func(code, severity, node, path, msg, hint string) {
		d := workflowAuthoringDiagnostic{code, severity, node, path, msg, hint}
		for _, old := range result.Diagnostics {
			if old.Code == code && old.FieldPath == path && old.Message == msg {
				return
			}
		}
		result.Diagnostics = append(result.Diagnostics, d)
	}
	cloned, _ := json.Marshal(in)
	var private workflowInput
	_ = json.Unmarshal(cloned, &private)
	in = private
	if in.Name == "" || utf8.RuneCountInString(strings.TrimSpace(in.Name)) > 60 {
		add("invalid_name", "error", "", "name", "名称需要 1–60 个字符", "填写工作流名称")
	}
	if utf8.RuneCountInString(in.Description) > 240 {
		add("invalid_description", "error", "", "description", "描述超过长度限制", "将描述缩短到 240 个字符以内")
	}
	def := workflow.Definition{ID: "wf_preview", Name: in.Name, Description: in.Description, Revision: 1, Nodes: in.Nodes}
	def.Normalize()
	if err := def.Validate(); err != nil {
		add("invalid_definition", "error", "", "nodes", err.Error(), "检查节点、依赖、模板及审批配置")
	}
	seen := map[string]bool{}
	for _, n := range def.Nodes {
		path := "nodes." + n.ID
		if seen[n.ID] {
			add("duplicate_node", "error", n.ID, path+".id", "节点 ID 重复", "为每个节点使用稳定且唯一的 ID")
		}
		seen[n.ID] = true
		if n.Kind == workflow.KindHuman {
			if n.Prompt == "" {
				add("missing_prompt", "error", n.ID, path+".prompt", "人工节点缺少审批说明", "补充 prompt")
			}
		} else {
			a, ok := s.mgr.studio.get(n.Agent)
			if !ok {
				add("agent_unavailable", "error", n.ID, path+".agent", "引用的 Agent 不存在或已删除", "从资源目录选择实际 Agent ID")
			} else {
				if a.Status != "ready" {
					add("agent_not_ready", "error", n.ID, path+".agent", "引用的 Agent 尚未就绪", "由有权管理者完成 Agent 配置")
				}
				c := s.workflowAuthoringCapabilities(r, a, n)
				result.Diagnostics = append(result.Diagnostics, c.Diagnostics...)
			}
			if n.Task == "" {
				add("missing_task", "error", n.ID, path+".task", "Agent 节点缺少任务说明", "填写本节点 task")
			}
		}
		if n.Orchestrate != nil {
			for _, candidate := range n.Orchestrate.Candidates {
				a, ok := s.mgr.studio.get(candidate.Agent)
				if !ok || a.Status != "ready" {
					add("candidate_unavailable", "error", n.ID, path+".orchestrate.candidates", "动态分派候选不存在或未就绪："+candidate.Agent, "选择就绪的实际 Agent")
					continue
				}
				caps := s.workflowAuthoringCapabilities(r, a, workflow.Node{ID: n.ID, Agent: candidate.Agent})
				for _, diagnostic := range caps.Diagnostics {
					diagnostic.FieldPath = path + ".orchestrate.candidates"
					result.Diagnostics = append(result.Diagnostics, diagnostic)
				}
			}
		}
		model := n.Model
		if model == "" && n.Kind != workflow.KindHuman {
			if a, ok := s.mgr.studio.get(n.Agent); ok {
				model = a.Model
			}
		}
		if model != "" {
			if _, err := config.Resolve(model); err != nil {
				add("model_unavailable", "error", n.ID, path+".model", "模型不可用或未配置", "从模型目录选择已配置模型")
			}
		}
		if n.Tools != nil {
			known := tools.BuiltinNameSet()
			for _, t := range *n.Tools {
				if !known[t] {
					add("unknown_native_tool", "error", n.ID, path+".tools", "未知内置工具："+t, "MCP/Skill 工具不应写入 node.tools")
				}
			}
		}
		if p := n.ApprovalPolicy; p != nil {
			if p.Assignment != "business" && len(p.Approvers) == 0 {
				add("approver_required", "error", n.ID, path+".approval_policy.approvers", "固定审批策略缺少候选账号", "从可用审批人目录选择至少一名账号")
			}
			if p.Assignment == "business" {
				add("business_approvers_required", "warning", n.ID, path+".approval_policy", "业务审批名单只能由受信任接入后端传入", "平台内试跑使用固定可用审批人，或通过独立业务接入测试")
			}
			for _, name := range p.Approvers {
				p, ok := s.workflowApprovalPrincipalForUsername(name)
				if !ok || !s.workflowApprovalPrincipalActive(p) {
					add("approver_unavailable", "error", n.ID, path+".approval_policy.approvers", "审批账号不存在或已停用："+name, "从可用审批账号目录重新选择")
				}
			}
		}
		for _, dep := range n.After {
			if !seen[dep.Node] {
				exists := false
				for _, up := range def.Nodes {
					if up.ID == dep.Node {
						exists = true
					}
				}
				if !exists {
					add("unknown_dependency", "error", n.ID, path+".after", "依赖节点不存在："+dep.Node, "先添加依赖节点或移除该边")
				}
			}
		}
	}
	result.InputKeys = def.InputKeys()
	if inputs == nil && len(result.InputKeys) > 0 {
		result.ReadyForTest = false
		add("sample_inputs_required", "warning", "", "inputs", "该流程需要样例输入才能试跑", "提供 input_keys 中每个参数的非空字符串值")
	}
	result.InputsValidated = inputs != nil
	if inputs != nil {
		if _, err := validateWorkflowRunInputs(def, inputs); err != nil {
			add("invalid_inputs", "error", "", "inputs", err.Error(), "仅提供已声明的字符串参数并遵守输入大小限制")
		}
		for _, key := range result.InputKeys {
			if strings.TrimSpace(inputs[key]) == "" {
				add("missing_input", "error", "", "inputs."+key, "缺少运行参数："+key, "提供非空样例值")
			}
		}
	}
	if err := s.mgr.validateWorkflowRefs(&in); err != nil {
		add("invalid_reference", "error", "", "nodes", err.Error(), "通过资源目录修正资源引用")
	}
	canonW := Workflow{workflowInput: in, Revision: 1}
	_, _, result.DefinitionHash, _ = s.mgr.workflows.canonicalDraft(canonW)
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			result.Valid = false
			result.ReadyForTest = false
		}
	}
	return result
}
func (s *Server) workflowApprovalPrincipalForUsername(name string) (string, bool) {
	for _, u := range s.workflowAccessSubjects() {
		if u.DisplayName == name {
			return u.PrincipalID, true
		}
	}
	return "", false
}
func (s *Server) workflowAuthoringCheck(r *http.Request, raw json.RawMessage) (any, error) {
	var req struct {
		WorkflowID       string            `json:"workflow_id"`
		Definition       *workflowInput    `json:"definition"`
		ExpectedRevision int               `json:"expected_revision"`
		Inputs           map[string]string `json:"inputs"`
	}
	if err := decodeWFAuthoring(raw, &req); err != nil {
		return nil, err
	}
	if err := s.authoringAllowed(r, req.WorkflowID, wfEdit); err != nil {
		return nil, err
	}
	if req.WorkflowID == "" && req.Definition == nil {
		return nil, wfAuthoringErr(400, "definition_required", "需要 workflow_id 或 definition")
	}
	if req.WorkflowID == "" && req.ExpectedRevision != 0 {
		return nil, wfAuthoringErr(400, "invalid_revision", "独立定义没有可比较的已保存 revision")
	}
	if req.WorkflowID != "" && req.Definition != nil && req.ExpectedRevision < 1 {
		return nil, wfAuthoringErr(400, "revision_required", "检查现有流程的替代定义需要 expected_revision")
	}
	var in workflowInput
	revision := 1
	if req.WorkflowID != "" {
		w, _ := s.mgr.workflows.get(req.WorkflowID)
		in = w.workflowInput
		revision = w.Revision
		if req.ExpectedRevision != 0 && revision != req.ExpectedRevision {
			return nil, wfAuthoringErr(409, "revision_conflict", "草稿已更新，请重新读取后校验")
		}
	}
	if req.Definition != nil {
		in = *req.Definition
	}
	result := s.workflowAuthoringValidate(r, in, req.Inputs)
	w := Workflow{workflowInput: in, Revision: revision}
	_, _, result.DefinitionHash, _ = s.mgr.workflows.canonicalDraft(w)
	return map[string]any{"ok": true, "validation": result, "valid": result.Valid, "ready_for_test": result.ReadyForTest, "definition_hash": result.DefinitionHash, "revision": revision, "diagnostics": result.Diagnostics, "input_keys": result.InputKeys}, nil
}

// Reads reuse existing projection and permission handlers, including node-only
// reviewers and redacted historical Runs. No new owner shortcut is introduced.
func (s *Server) workflowAuthoringRead(r *http.Request, raw json.RawMessage) (any, error) {
	var req struct {
		Action     string `json:"action"`
		WorkflowID string `json:"workflow_id"`
		RunID      string `json:"run_id"`
		NodeID     string `json:"node_id"`
	}
	if err := decodeWFAuthoring(raw, &req); err != nil {
		return nil, err
	}
	if req.Action == "" {
		if req.WorkflowID == "" {
			req.Action = "list"
		} else {
			req.Action = "get"
		}
	}
	cloned := r.Clone(r.Context())
	cloned.Method = "GET"
	cloned.SetPathValue("wid", req.WorkflowID)
	cloned.SetPathValue("rid", req.RunID)
	cloned.SetPathValue("nid", req.NodeID)
	var handler http.HandlerFunc
	switch req.Action {
	case "list":
		handler = s.handleWorkflowList
	case "get":
		handler = s.handleWorkflowGet
	case "runs":
		handler = s.handleWorkflowRunList
	case "run":
		handler = s.handleWorkflowRunGet
	case "artifact":
		handler = s.handleWorkflowRunArtifact
	default:
		return nil, wfAuthoringErr(400, "unsupported_action", "不支持的读取操作")
	}
	if req.Action != "list" && !validID(req.WorkflowID) {
		return nil, wfAuthoringErr(400, "invalid_id", "需要有效 workflow_id")
	}
	if (req.Action == "run" || req.Action == "artifact") && !validID(req.RunID) {
		return nil, wfAuthoringErr(400, "invalid_id", "需要有效 run_id")
	}
	if req.Action == "artifact" && !validID(req.NodeID) {
		return nil, wfAuthoringErr(400, "invalid_id", "需要有效 node_id")
	}
	if req.Action == "artifact" {
		cloned.Header.Set("Range", "bytes=0-262144")
	}
	recorder := httptest.NewRecorder()
	handler(recorder, cloned)
	if req.Action == "artifact" && recorder.Code == http.StatusRequestedRangeNotSatisfiable && recorder.Header().Get("Content-Range") == "bytes */0" {
		return map[string]any{"ok": true, "content": "", "inline": true}, nil
	}
	if recorder.Code >= 400 {
		return nil, wfAuthoringErr(recorder.Code, "read_unavailable", "工作流或运行不存在、无权访问或暂不可用")
	}
	if req.Action == "artifact" {
		data := recorder.Body.Bytes()
		if len(data) > 256<<10 || !utf8.Valid(data) {
			return map[string]any{"ok": true, "artifact_url": fmt.Sprintf("/api/workflows/%s/runs/%s/nodes/%s/artifact", req.WorkflowID, req.RunID, req.NodeID), "inline": false}, nil
		}
		return map[string]any{"ok": true, "content": string(data), "inline": true}, nil
	}
	var value map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		return nil, err
	}
	value["ok"] = true
	return value, nil
}
func (s *Server) workflowAuthoringPublish(r *http.Request, raw json.RawMessage) (any, error) {
	var req struct {
		WorkflowID       string `json:"workflow_id"`
		ExpectedRevision int    `json:"expected_revision"`
		ExpectedHash     string `json:"expected_hash"`
		Name             string `json:"name"`
		Desc             string `json:"desc"`
	}
	if err := decodeWFAuthoring(raw, &req); err != nil {
		return nil, err
	}
	if err := s.authoringAllowed(r, req.WorkflowID, wfPublish); err != nil {
		return nil, err
	}
	if req.ExpectedRevision < 1 || req.ExpectedHash == "" {
		return nil, wfAuthoringErr(400, "publication_review_required", "发布需要审阅过的 expected_revision 和 expected_hash")
	}
	ptr, err := s.mgr.workflows.publishExact(req.WorkflowID, req.Name, req.Desc, req.ExpectedRevision, req.ExpectedHash)
	if errors.Is(err, errWorkflowDraftChanged) {
		return nil, wfAuthoringErr(409, "revision_conflict", "审阅后草稿发生变化，请重新读取并审阅")
	}
	if err != nil {
		return nil, wfAuthoringErr(400, "publish_invalid", err.Error())
	}
	return map[string]any{"ok": true, "publication": ptr, "version_id": ptr.VersionID, "workflow_id": req.WorkflowID, "definition_hash": ptr.VersionID}, nil
}
