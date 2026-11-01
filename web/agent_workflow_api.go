package web

// Private Agent procedures share no platform-workflow grants, publication or
// callback state. Both conversation tools and the panel enter this boundary.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
)

type agentWorkflowRequest struct {
	Action           string          `json:"action"`
	WorkflowID       string          `json:"workflow_id,omitempty"`
	RunID            string          `json:"run_id,omitempty"`
	StepID           string          `json:"step_id,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	SourceSessionID  string          `json:"source_session_id,omitempty"`
	ExpectedRevision int             `json:"expected_revision,omitempty"`
	ExpectedHash     string          `json:"expected_hash,omitempty"`
	VersionID        string          `json:"version_id,omitempty"`
	TargetRevision   int             `json:"target_revision,omitempty"`
	BeforeRevision   int             `json:"before_revision,omitempty"`
	Name             string          `json:"name,omitempty"`
	Description      string          `json:"description,omitempty"`
	Inputs           json.RawMessage `json:"inputs,omitempty"`
	Steps            []af.Step       `json:"steps,omitempty"`
	Graph            *af.Graph       `json:"graph,omitempty"`
	ToolApprovalMode string          `json:"tool_approval_mode,omitempty"`
	Operations       []af.Operation  `json:"operations,omitempty"`
	Definition       *af.Definition  `json:"definition,omitempty"`
	Files            []string        `json:"files,omitempty"`
	Feedback         string          `json:"feedback,omitempty"`
	NodeInputs       json.RawMessage `json:"node_inputs,omitempty"`
	ItemIndex        *int            `json:"item_index,omitempty"`
	IncludeDeleted   bool            `json:"include_deleted,omitempty"`
}
type agentWorkflowCaps struct {
	Read                    bool     `json:"read"`
	Edit                    bool     `json:"edit"`
	Enable                  bool     `json:"enable"`
	Run                     bool     `json:"run"`
	Confirm                 bool     `json:"confirm"`
	Tools                   []string `json:"tools"`
	MissingTools            []string `json:"missing_tools"`
	Reason                  string   `json:"reason,omitempty"`
	GraphVersion            int      `json:"graph_version"`
	StepKinds               []string `json:"step_kinds"`
	ConditionOperators      []string `json:"condition_operators"`
	MaxParallelNodes        int      `json:"max_parallel_nodes"`
	MaxSteps                int      `json:"max_steps"`
	MaxRepeatItems          int      `json:"max_repeat_items"`
	MaxCodeRetries          int      `json:"max_code_retries"`
	ToolApprovalModes       []string `json:"tool_approval_modes"`
	DefaultToolApprovalMode string   `json:"default_tool_approval_mode"`
}

func awCaps(a studioAgent, available bool) agentWorkflowCaps {
	c := agentWorkflowCaps{Read: true, Tools: []string{}, MissingTools: []string{}, GraphVersion: 1, ToolApprovalModes: []string{af.ToolApprovalStandard, af.ToolApprovalStrict, af.ToolApprovalAuto}, DefaultToolApprovalMode: af.ToolApprovalStandard,
		StepKinds:          []string{"agent", "code", "iteration", "loop", "confirm", "condition", "parallel", "join"},
		ConditionOperators: []string{"equals", "not_equals", "contains", "not_contains", "is_empty", "not_empty", "gt", "gte", "lt", "lte"}, MaxParallelNodes: awGraphConcurrency, MaxSteps: af.MaxSteps, MaxRepeatItems: af.MaxRepeatItems, MaxCodeRetries: af.MaxCodeRetries}
	for _, g := range []string{"read", "edit", "enable", "run"} {
		n := "agent_workflow_" + g
		if available && tools.BuiltinSelected(a.Tools, n) {
			c.Tools = append(c.Tools, n)
		} else {
			c.MissingTools = append(c.MissingTools, n)
		}
	}
	c.Edit = available && tools.BuiltinSelected(a.Tools, "agent_workflow_edit")
	c.Enable = available && tools.BuiltinSelected(a.Tools, "agent_workflow_enable")
	c.Run = available && tools.BuiltinSelected(a.Tools, "agent_workflow_run")
	c.Confirm = c.Run
	if !c.Edit || !c.Run {
		c.Reason = "当前 Agent 尚未启用完整的内部工作流能力；请由有权管理者在 Agent 配置中勾选 agent-workflows Skill。"
	}
	return c
}
func (s *Server) registerAgentWorkflowRoutes() {
	s.registerAgentWorkflowLayoutRoutes()
	s.registerAgentWorkflowActivityRoutes()
	s.mgr.mu.Lock()
	s.mgr.agentWorkflowHost = s
	s.mgr.mu.Unlock()
	s.mux.HandleFunc("GET /api/apps/{aid}/agent-workflows", func(w http.ResponseWriter, r *http.Request) {
		s.handleAgentWorkflow(w, r, "read", []byte(`{"action":"list"}`))
	})
	s.mux.HandleFunc("POST /api/apps/{aid}/agent-workflows/{group}", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, af.MaxRequestBytes))
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		s.handleAgentWorkflow(w, r, r.PathValue("group"), raw)
	})
}
func (s *Server) handleAgentWorkflow(w http.ResponseWriter, r *http.Request, group string, raw []byte) {
	owner, ok := s.taskCenterOwner(r)
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	value, err := s.agentWorkflowAction(r.Context(), owner, r.PathValue("aid"), "", group, raw)
	if err != nil {
		status := 400
		var denied *awHTTPError
		if errors.As(err, &denied) {
			status = denied.status
		} else if errors.Is(err, taskqueue.ErrNotFound) || errors.Is(err, ErrNotFound) {
			status = 404
		} else if errors.Is(err, taskqueue.ErrConflict) || errors.Is(err, af.ErrConflict) {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, value)
}

type awHTTPError struct {
	status  int
	message string
}

func (e *awHTTPError) Error() string { return e.message }
func awDenied(message string) error  { return &awHTTPError{403, message} }
func awDecode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}
func awFingerprint(a studioAgent) string {
	raw, _ := json.Marshal(struct {
		Instructions, Model string
		Models, Skills, MCP *[]string
		Tools               *[]string
		SkillBinding        json.RawMessage
		SkillToolBindings   []string `json:"SkillToolBindings,omitempty"`
	}{a.Instructions, a.Model, a.Models, a.Skills, a.MCPServers, a.Tools, a.SkillBinding, a.SkillToolBindings})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func awPublicRun(run af.Run) af.Run {
	run.OwnerID = ""
	run.AgentSnapshot = nil
	run.Workspace = ""
	run.Definition.OwnerID = ""
	return run
}
func awPublicDefinition(d af.Definition) af.Definition { d.OwnerID = ""; return d }
func (s *Server) agentWorkflowAction(ctx context.Context, owner *identity.Scope, aid, source, group string, raw []byte) (map[string]any, error) {
	if !validID(aid) {
		return nil, ErrNotFound
	}
	if active, _ := s.mgr.releaseMaintenance(); active && group != "read" {
		return nil, &awHTTPError{503, "服务维护中，请稍后操作"}
	}
	a, available := s.mgr.studio.get(aid)
	available = available && a.Status == "ready"
	capAgent := a
	capAgent.Tools = s.mgr.studioNativeSelection(a)
	caps := awCaps(capAgent, available)
	if group != "read" && (!available || group == "edit" && !caps.Edit || group == "enable" && !caps.Enable || (group == "run" || group == "confirm") && !caps.Run) {
		return nil, awDenied("当前 Agent 未授权此内部工作流操作")
	}
	if group != "read" && group != "edit" && group != "enable" && group != "run" && group != "confirm" {
		return nil, errors.New("未知内部工作流操作")
	}
	s.mgr.mu.Lock()
	valid := s.mgr.scheduleOwnerValid
	s.mgr.mu.Unlock()
	if valid == nil || !valid(owner) {
		return nil, awDenied("当前账号不可用")
	}
	var q agentWorkflowRequest
	if err := awDecode(raw, &q); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if mode, supplied := fields["tool_approval_mode"]; supplied {
		var value string
		if group != "edit" || q.Action != "create" || json.Unmarshal(mode, &value) != nil || value == "" || !af.ValidToolApprovalMode(value) {
			return nil, errors.New("tool_approval_mode仅用于创建，且仅支持standard、strict或auto；修改请使用set_metadata")
		}
	}
	if value, supplied := fields["operations"]; supplied {
		var operations []map[string]json.RawMessage
		if json.Unmarshal(value, &operations) == nil {
			for _, operation := range operations {
				if mode, supplied := operation["tool_approval_mode"]; supplied {
					var value, op string
					_ = json.Unmarshal(operation["op"], &op)
					if op != "set_metadata" || json.Unmarshal(mode, &value) != nil || value == "" || !af.ValidToolApprovalMode(value) {
						return nil, errors.New("tool_approval_mode仅能在set_metadata中设置standard、strict或auto")
					}
				}
			}
		}
	}
	if q.IncludeDeleted && (group != "read" || q.Action != "list") {
		return nil, errors.New("include_deleted仅用于读取流程列表")
	}
	if q.ItemIndex != nil && (group != "read" || q.Action != "artifact" || *q.ItemIndex < 0 || *q.ItemIndex >= af.MaxRepeatItems) {
		return nil, errors.New("item_index仅用于读取0–99号迭代项的真实产物")
	}
	if source != "" {
		if q.SourceSessionID != "" && q.SourceSessionID != source {
			return nil, awDenied("来源会话不匹配")
		}
		q.SourceSessionID = source
	}
	if q.SourceSessionID != "" && !s.mgr.backgroundTaskSessionOwned(q.SourceSessionID, aid, sessionResultOwnerKey(owner)) {
		return nil, ErrNotFound
	}
	if q.SourceSessionID != "" && s.mgr.agentWorkflowMarker(q.SourceSessionID) != nil {
		return nil, awDenied("执行会话不能再次创建或调用内部流程")
	}
	db, err := s.mgr.taskRuns.database(true)
	if err != nil {
		return nil, err
	}
	key := sessionResultOwnerKey(owner)
	out := map[string]any{"ok": true, "agent_id": aid, "capabilities": caps}
	switch group {
	case "read":
		switch q.Action {
		case "run_status":
			rows, e := db.AWActiveRunStatus(key, aid)
			if e != nil {
				return nil, e
			}
			out["run_status"] = rows
		case "resources":
			resources, e := s.agentWorkflowResources(ctx, owner, a, q.SourceSessionID)
			if e != nil {
				return nil, e
			}
			out["resources"] = resources
		case "capabilities":
			out["capabilities"] = caps
			out["code_runtimes"] = awCodeRuntimes()
		case "list":
			rows, e := db.AWList(key, aid, q.IncludeDeleted)
			if e != nil {
				return nil, e
			}
			for i := range rows {
				rows[i] = awPublicDefinition(rows[i])
			}
			out["workflows"] = rows
		case "get":
			d, e := db.AWGet(key, aid, q.WorkflowID)
			if e != nil {
				return nil, e
			}
			out["workflow"] = awPublicDefinition(d)
		case "versions":
			page, e := db.AWVersions(key, aid, q.WorkflowID, q.BeforeRevision)
			if e != nil {
				return nil, e
			}
			out["versions"], out["has_more"] = page.Versions, page.HasMore
			if page.NextBeforeRevision > 0 {
				out["next_before_revision"] = page.NextBeforeRevision
			}
			if page.EnabledVersion != nil {
				out["enabled_version"] = page.EnabledVersion
			}
		case "version":
			d, version, e := db.AWDefinitionVersion(key, aid, q.WorkflowID, q.VersionID, q.TargetRevision)
			if e != nil {
				return nil, e
			}
			out["workflow"], out["version"] = awPublicDefinition(d), version
		case "validate":
			var d af.Definition
			if q.Definition != nil {
				d = *q.Definition
				if d.OwnerID != "" || d.AgentID != "" || d.ID != "" {
					return nil, errors.New("候选定义不能指定归属身份")
				}
				if d.Revision == 0 {
					d.Revision = 1
				}
			} else {
				var e error
				d, e = db.AWGet(key, aid, q.WorkflowID)
				if e != nil {
					return nil, e
				}
				if q.ExpectedRevision > 0 && q.ExpectedRevision != d.Revision {
					return nil, taskqueue.ErrConflict
				}
			}
			ds := af.Validate(d)
			if e := s.validateAgentWorkflowResources(ctx, owner, a, d); e != nil {
				ds = append(ds, af.Diagnostic{Code: "invalid_resources", Severity: "error", Message: e.Error()})
			}
			if len(q.Inputs) > 0 {
				inputs, err := af.DecodeInputs(d, q.Inputs)
				if err != nil {
					return nil, err
				}
				if err := awInputs(d, inputs); err != nil {
					ds = append(ds, af.Diagnostic{Code: "invalid_inputs", Severity: "error", Message: err.Error()})
				}
			}
			out["diagnostics"] = ds
			counts := map[string]int{}
			for _, diagnostic := range ds {
				counts[diagnostic.Code]++
			}
			out["diagnostic_count"] = len(ds)
			out["diagnostic_counts"] = counts
			out["valid"] = len(ds) == 0
			out["definition_hash"] = af.Hash(d)
		case "runs":
			rows, e := db.AWRuns(key, aid, q.WorkflowID)
			if e != nil {
				return nil, e
			}
			for i := range rows {
				rows[i] = awPublicRun(rows[i])
			}
			out["runs"] = rows
		case "run", "artifact":
			run, e := db.AWRun(key, aid, q.WorkflowID, q.RunID)
			if e != nil {
				return nil, e
			}
			if q.Action == "run" {
				out["run"] = awPublicRun(run)
			} else {
				var path, expectedSHA string
				artifactStepID := q.StepID
				for _, st := range run.Steps {
					if st.StepID == q.StepID {
						if q.ItemIndex == nil {
							path, expectedSHA = st.Artifact, st.ArtifactSHA256
						} else if st.Repeat != nil && *q.ItemIndex >= 0 && *q.ItemIndex < len(st.Repeat.Items) {
							item := st.Repeat.Items[*q.ItemIndex]
							if item.Index == *q.ItemIndex {
								path, expectedSHA = item.Artifact, item.ArtifactSHA256
								artifactStepID = awRepeatArtifactID(st.StepID, item.Index)
							}
						}
					}
				}
				if path == "" {
					return nil, ErrNotFound
				}
				b, e := s.mgr.awReadArtifact(run.ID, artifactStepID)
				if e != nil {
					return nil, e
				}
				sum := sha256.Sum256(b)
				if expectedSHA == "" || hex.EncodeToString(sum[:]) != expectedSHA {
					return nil, errors.New("历史产物校验失败")
				}
				if !utf8.Valid(b) {
					return nil, errors.New("产物不是可预览文本")
				}
				out["text"] = string(b)
				out["path"] = path
			}
		default:
			return nil, errors.New("未知读取操作")
		}
	case "edit":
		if q.RequestID == "" {
			return nil, errors.New("编辑需要 request_id")
		}
		var inputs []af.Input
		if len(q.Inputs) > 0 && json.Unmarshal(q.Inputs, &inputs) != nil {
			return nil, errors.New("流程输入定义必须为参数列表")
		}
		d, reused, e := db.AWEdit(key, aid, af.EditRequest{Action: q.Action, WorkflowID: q.WorkflowID, RequestID: q.RequestID, Name: q.Name, Description: q.Description, ExpectedRevision: q.ExpectedRevision, Inputs: inputs, Steps: q.Steps, Graph: q.Graph, ToolApprovalMode: q.ToolApprovalMode, Operations: q.Operations, VersionID: q.VersionID, TargetRevision: q.TargetRevision})
		if e != nil {
			return nil, e
		}
		out["workflow"] = awPublicDefinition(d)
		out["reused"] = reused
	case "enable":
		if q.Action != "enable" && q.Action != "disable" {
			return nil, errors.New("仅支持启用或停用")
		}
		d, e := db.AWEnable(key, aid, q.WorkflowID, q.ExpectedRevision, q.ExpectedHash, q.Action == "enable")
		if e != nil {
			return nil, e
		}
		out["workflow"] = awPublicDefinition(d)
	case "confirm":
		if q.StepID == "" || q.RequestID == "" || len(q.RequestID) > 128 || len(q.Feedback) > 16384 {
			return nil, errors.New("确认请求缺少有效标识或意见过长")
		}
		current, e := db.AWRun(key, aid, q.WorkflowID, q.RunID)
		if e != nil {
			return nil, e
		}
		if e = s.mgr.checkAgentWorkflowResources(ctx, owner, a, current); e != nil {
			return nil, e
		}
		run, _, reused, e := db.AWConfirm(key, aid, q.WorkflowID, q.RunID, q.RequestID, q.Feedback, q.StepID)
		if e != nil {
			return nil, e
		}
		out["run"] = awPublicRun(run)
		out["reused"] = reused
		s.mgr.taskRuns.wake()
	case "run":
		if q.RequestID == "" || len(q.RequestID) > 128 {
			return nil, errors.New("运行控制需要有效 request_id")
		}
		if q.Action == "cancel" {
			run, e := db.AWCancel(key, aid, q.WorkflowID, q.RunID)
			if e != nil {
				return nil, e
			}
			segments, e := db.AWExecutions(key, aid, q.WorkflowID, q.RunID)
			if e != nil {
				return nil, e
			}
			for _, seg := range segments {
				if seg.Status == taskqueue.Queued || seg.Status == taskqueue.Running {
					_, _ = s.mgr.taskRuns.cancel(seg.ID, seg.Revision)
				}
			}
			out["run"] = awPublicRun(run)
			return out, nil
		}
		if q.Action == "test_node" {
			return s.agentWorkflowTestCodeNode(ctx, owner, a, db, q, out)
		}
		if q.Action != "trial" && q.Action != "start" && q.Action != "retry" {
			return nil, errors.New("未知执行操作")
		}
		d, e := db.AWGet(key, aid, q.WorkflowID)
		if e != nil {
			return nil, e
		}
		if d.Deleted {
			return nil, fmt.Errorf("%w: 流程已删除，只能查看历史", taskqueue.ErrConflict)
		}
		if d.Archived {
			return nil, errors.New("流程已归档")
		}
		trial := q.Action == "trial"
		var retry string
		var inputs map[string]string
		var nodeTest *af.NodeTest
		if q.Action == "retry" {
			old, e := db.AWRun(key, aid, q.WorkflowID, q.RunID)
			if e != nil {
				return nil, e
			}
			if old.Status != af.Failed && old.Status != af.Cancelled && old.Status != af.Interrupted {
				return nil, taskqueue.ErrConflict
			}
			d, inputs, trial, retry = old.Definition, old.Inputs, old.Trial, old.ID
			nodeTest = old.NodeTest
			q.Files = nil
		}
		if trial && q.Action != "retry" {
			if q.ExpectedRevision != d.Revision || q.ExpectedHash != af.Hash(d) {
				return nil, taskqueue.ErrConflict
			}
		} else if q.Action != "retry" {
			if d.EnabledVersion == "" || q.VersionID != "" && q.VersionID != d.EnabledVersion {
				return nil, taskqueue.ErrConflict
			}
			d, e = db.AWVersion(key, aid, d.ID, d.EnabledVersion)
			if e != nil {
				return nil, e
			}
		}
		if inputs == nil {
			inputs, e = af.DecodeInputs(d, q.Inputs)
			if e != nil {
				return nil, e
			}
		}
		if nodeTest == nil {
			inputs, e = af.NormalizeInputs(d, inputs)
		}
		if e != nil {
			return nil, e
		}
		if ds := af.Validate(d); len(ds) > 0 {
			return nil, errors.New(ds[0].Message)
		}
		run, e := s.prepareAgentWorkflowRun(ctx, owner, a, d, q.SourceSessionID, inputs, q.Files, trial, retry)
		if e != nil {
			return nil, e
		}
		if retry != "" {
			run.NodeTest = nodeTest
			old, e := db.AWRun(key, aid, q.WorkflowID, retry)
			if e != nil {
				return nil, e
			}
			if s.mgr.agentWorkflowFingerprint(a, owner) != old.AgentFingerprint {
				return nil, errors.New("Agent配置已改变，请重新试跑")
			}
			run.AgentSnapshot = old.AgentSnapshot
			run.AgentFingerprint = old.AgentFingerprint
			if e = s.mgr.checkAgentWorkflowResources(ctx, owner, a, old); e != nil {
				s.mgr.awRemoveWorkspace(owner, run.Workspace)
				return nil, e
			}
			if e = s.copyAgentWorkflowRetryFiles(owner, &run, old); e != nil {
				s.mgr.awRemoveWorkspace(owner, run.Workspace)
				return nil, e
			}
			if e = s.copyAgentWorkflowRetryResources(owner, &run, old); e != nil {
				s.mgr.awRemoveWorkspace(owner, run.Workspace)
				return nil, e
			}
		}
		accepted, _, reused, e := db.AWAdmit(run, q.RequestID)
		if e != nil || reused {
			s.mgr.awRemoveWorkspace(owner, run.Workspace)
		}
		if e != nil {
			return nil, e
		}
		out["run"] = awPublicRun(accepted)
		out["reused"] = reused
		s.mgr.taskRuns.wake()
	}
	return out, nil
}
func awInputs(d af.Definition, values map[string]string) error {
	return af.ValidateInputs(d, values)
}
func awReadFile(dir, relative string, limit int64) ([]byte, error) {
	if !af.SafePath(relative) {
		return nil, errors.New("文件路径不合法")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("文件须为大小受限的普通文件")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("文件过大")
	}
	return b, err
}
func (s *Server) prepareAgentWorkflowRun(ctx context.Context, owner *identity.Scope, a studioAgent, d af.Definition, source string, inputs map[string]string, files []string, trial bool, retry string) (af.Run, error) {
	if len(files) > 16 {
		return af.Run{}, errors.New("一次最多选择16份材料")
	}
	base := s.mgr.cfg.Cwd
	if owner != nil {
		base = s.mgr.UserRoot(*owner)
	}
	sourceDir := filepath.Join(base, "agents", a.ID)
	if source != "" {
		sess, e := s.mgr.Get(source)
		if e != nil {
			return af.Run{}, e
		}
		sourceDir = sess.cwd
	}
	id := "afr_" + rand.Text()
	workspace := filepath.Join(base, "agents", a.ID, "workflow-runs", id, "workspace")
	relative, err := s.mgr.awRelative(owner, filepath.Join(workspace, "inputs"))
	if err != nil {
		return af.Run{}, err
	}
	root, err := s.mgr.awRoot(owner)
	if err != nil {
		return af.Run{}, err
	}
	defer root.Close()
	if err = root.MkdirAll(relative, 0700); err != nil {
		return af.Run{}, err
	}
	success := false
	defer func() {
		if !success {
			s.mgr.awRemoveWorkspace(owner, workspace)
		}
	}()
	snapshot, _ := json.Marshal(awExecutionSnapshot{Agent: a, Owner: owner})
	run := af.Run{ID: id, OwnerID: sessionResultOwnerKey(owner), AgentID: a.ID, WorkflowID: d.ID, VersionID: af.Hash(d), Definition: d, AgentSnapshot: snapshot, AgentFingerprint: s.mgr.agentWorkflowFingerprint(a, owner), SourceSessionID: source, Workspace: workspace, Inputs: inputs, Trial: trial, RetryOf: retry, Status: af.Queued, CreatedAt: time.Now().UnixMilli(), Steps: []af.StepRun{}}
	total := 0
	for i, path := range files {
		if !af.SafePath(path) {
			return af.Run{}, errors.New("材料必须为当前工作目录内的相对路径")
		}
		if err := ctx.Err(); err != nil {
			return af.Run{}, err
		}
		data, err := s.mgr.awReadOwned(owner, filepath.Join(sourceDir, filepath.FromSlash(path)), 4<<20)
		if err != nil {
			return af.Run{}, fmt.Errorf("材料 %s: %w", path, err)
		}
		total += len(data)
		if total > 16<<20 {
			return af.Run{}, errors.New("材料总计不能超过16MiB")
		}
		dest := fmt.Sprintf("inputs/%02d-%s", i+1, filepath.Base(path))
		if err := s.mgr.awWriteOwned(owner, filepath.Join(workspace, filepath.FromSlash(dest)), data); err != nil {
			return af.Run{}, err
		}
		sum := sha256.Sum256(data)
		run.Files = append(run.Files, af.InputFile{Source: path, Path: dest, SHA256: hex.EncodeToString(sum[:])})
	}
	if retry == "" {
		if err = s.freezeAgentWorkflowResources(ctx, owner, a, &run); err != nil {
			return af.Run{}, err
		}
	}
	success = true
	return run, nil
}
func (s *Server) copyAgentWorkflowRetryFiles(owner *identity.Scope, run *af.Run, old af.Run) error {
	for _, f := range old.Files {
		data, e := s.mgr.awReadOwned(owner, filepath.Join(old.Workspace, filepath.FromSlash(f.Path)), 4<<20)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return errors.New("历史输入材料已改变，不能无提示重试")
		}
		if e = s.mgr.awWriteOwned(owner, filepath.Join(run.Workspace, filepath.FromSlash(f.Path)), data); e != nil {
			return e
		}
		run.Files = append(run.Files, f)
	}
	return nil
}
