package web

// Draft trials are ordinary durable workflow Runs, attributed to the caller.
// They freeze a saved draft without changing the publication pointer or
// inheriting a production integration's callback subscription.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/workflow"
)

type workflowRunRequest struct {
	WorkflowID       string            `json:"workflow_id,omitempty"`
	ExpectedRevision int               `json:"expected_revision,omitempty"`
	ExpectedHash     string            `json:"expected_hash,omitempty"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	RequestID        string            `json:"request_id,omitempty"`
	VersionID        string            `json:"version_id,omitempty"`
}

func (s *Server) registerWorkflowTrialRoutes() {
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/test-run", s.handleWorkflowTrial)
}

// decodeWorkflowRunRequest preserves the existing empty-body action contract,
// but never accepts caller-supplied ownership, approval or integration fields.
func decodeWorkflowRunRequest(r *http.Request, target any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("运行请求必须是一个 JSON 对象")
	}
	return nil
}

// The initial string-input contract is derived from the existing immutable
// template references. The integration adapter may declare extra form fields;
// those remain governed by its own publication schema, unchanged here.
func validateWorkflowRunInputs(def workflow.Definition, inputs map[string]string) (map[string]string, error) {
	fields := make([]workflowPublicationInput, 0)
	for _, key := range def.InputKeys() {
		fields = append(fields, workflowPublicationInput{Key: key, Label: key, Required: true})
	}
	return validateWorkflowPublicationInputs(workflowPublication{InputFields: fields}, inputs)
}

func workflowDefinitionHash(def workflow.Definition) (string, error) {
	def.Normalize()
	raw, err := json.MarshalIndent(canonicalWorkflow{Name: def.Name, Description: def.Description, Revision: def.Revision, Nodes: def.Nodes}, "", "  ")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])[:12], nil
}

func workflowTrialIntent(requestID string) (string, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 256 || strings.TrimSpace(requestID) != requestID {
		return "", wfAuthoringErr(http.StatusBadRequest, "request_id_required", "试跑需要不超过 256 字节的 request_id")
	}
	digest := sha256.Sum256([]byte(requestID))
	return "draft-test:" + hex.EncodeToString(digest[:]), nil
}

func (s *Server) workflowRunLocalCaller(r *http.Request) error {
	p := s.workflowPrincipal(r)
	if p.Kind != identity.PrincipalLocal && p.Kind != identity.PrincipalOperator {
		return wfAuthoringErr(http.StatusForbidden, "local_account_required", "此运行入口需要本地账号；外部系统需使用单独配置的接入入口")
	}
	return nil
}

func (s *Server) workflowPublishedRun(r *http.Request, wid string, inputs map[string]string, key, expectedVersion string) (taskqueue.WorkflowRunMeta, error) {
	if err := s.workflowRunLocalCaller(r); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if err := s.authoringAllowed(r, wid, wfInvoke); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if _, ok := s.mgr.workflows.get(wid); !ok {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusNotFound, "workflow_not_found", "工作流不存在")
	}
	def, err := s.mgr.workflows.publishedDefinition(wid)
	if err != nil {
		if errors.Is(err, errWorkflowUnpublished) {
			return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusConflict, "workflow_unpublished", "工作流尚未发布，请先发布或使用草稿试跑")
		}
		return taskqueue.WorkflowRunMeta{}, err
	}
	version, err := workflowDefinitionHash(def)
	if err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if expectedVersion != "" && expectedVersion != version {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusConflict, "published_version_conflict", "已发布版本已改变，请重新读取后运行")
	}
	values, err := validateWorkflowRunInputs(def, inputs)
	if err != nil {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "invalid_inputs", err.Error())
	}
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	meta, err := s.mgr.admitWorkflowWithOptions(def, owner, "manual", wid, key, "", workflowAdmissionOptions{InitiatorID: s.workflowPrincipal(r).ID, VersionID: version, Inputs: values})
	if errors.Is(err, taskqueue.ErrConflict) {
		return meta, wfAuthoringErr(http.StatusConflict, "request_conflict", "运行请求标识已用于不同内容")
	}
	return meta, err
}

func (s *Server) workflowTrial(r *http.Request, wid string, expectedRevision int, expectedHash string, inputs map[string]string, requestID string) (taskqueue.WorkflowRunMeta, error) {
	if err := s.workflowRunLocalCaller(r); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	for _, action := range []string{wfEdit, wfInvoke} {
		if err := s.authoringAllowed(r, wid, action); err != nil {
			return taskqueue.WorkflowRunMeta{}, err
		}
	}
	key, err := workflowTrialIntent(requestID)
	if err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if expectedRevision < 1 || expectedHash == "" {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "draft_version_required", "试跑需要审阅过的 expected_revision 和 expected_hash")
	}
	// Read, compare and serialize the exact saved revision while holding the
	// draft lock. Decode creates a deep copy independent of future edits.
	s.mgr.workflows.mu.RLock()
	draft, ok := s.mgr.workflows.data.Workflows[wid]
	if !ok || draft.Deleted {
		s.mgr.workflows.mu.RUnlock()
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusNotFound, "workflow_not_found", "工作流不存在")
	}
	_, frozen, hash, err := s.mgr.workflows.canonicalDraft(draft)
	s.mgr.workflows.mu.RUnlock()
	if err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	if draft.Revision != expectedRevision || hash != expectedHash {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusConflict, "draft_version_conflict", "草稿已改变，请重新校验后试跑")
	}
	var canon canonicalWorkflow
	if err := json.Unmarshal(frozen, &canon); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	input := workflowInput{Name: canon.Name, Description: canon.Description, Nodes: canon.Nodes}
	if err := validateWorkflowInput(&input); err != nil {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "invalid_definition", err.Error())
	}
	if s.mgr.workflows.refs != nil {
		if err := s.mgr.workflows.refs(&input); err != nil {
			return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "invalid_resource", err.Error())
		}
	}
	def := workflow.Definition{ID: wid, Name: canon.Name, Description: canon.Description, Revision: canon.Revision, Nodes: canon.Nodes}
	if err := def.Validate(); err != nil {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "invalid_definition", err.Error())
	}
	validation := s.workflowAuthoringValidate(r, input, inputs)
	if !validation.Valid || !validation.ReadyForTest {
		return taskqueue.WorkflowRunMeta{}, &workflowAuthoringError{Status: http.StatusBadRequest, Code: "workflow_not_ready_for_test", Message: "草稿暂不可试跑，请先处理结构、输入或执行能力问题", Diagnostics: validation.Diagnostics}
	}
	if err := s.workflowTrialRuntimeSupport(r, def); err != nil {
		return taskqueue.WorkflowRunMeta{}, err
	}
	values, err := validateWorkflowRunInputs(def, inputs)
	if err != nil {
		return taskqueue.WorkflowRunMeta{}, wfAuthoringErr(http.StatusBadRequest, "invalid_inputs", err.Error())
	}
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	meta, err := s.mgr.admitWorkflowWithOptions(def, owner, "draft_test", wid, key, "", workflowAdmissionOptions{
		InitiatorID: s.workflowPrincipal(r).ID, VersionID: hash, Inputs: values, TestRun: true, DraftRevision: expectedRevision, DefinitionHash: hash,
	})
	if errors.Is(err, taskqueue.ErrConflict) {
		return meta, wfAuthoringErr(http.StatusConflict, "request_conflict", "试跑请求标识已用于不同内容")
	}
	return meta, err
}

// Runtime Skill bindings and host-only native backends are not wired by the
// workflow node executor. Refuse those draft tests explicitly, rather than
// claiming a sample proved capabilities the node could never call.
func (s *Server) workflowTrialRuntimeSupport(r *http.Request, def workflow.Definition) error {
	check := func(id string, narrow *[]string) error {
		a, ok := s.mgr.studio.get(id)
		if !ok || a.Status != "ready" {
			return wfAuthoringErr(http.StatusBadRequest, "agent_unavailable", "节点引用的 Agent 尚未就绪")
		}
		if len(a.SkillBinding) > 0 {
			return wfAuthoringErr(http.StatusBadRequest, "runtime_skill_unsupported", "当前工作流节点尚未接入所选 Skill 的完整运行环境，请选择不依赖运行型 Skill 的 Agent")
		}
		// Orchestrator candidates execute with their own capabilities. They
		// must pass the same real-execution boundary as the visible node.
		capabilities := s.workflowAuthoringCapabilities(r, a, workflow.Node{ID: id, Agent: id, Tools: narrow})
		for _, diagnostic := range capabilities.Diagnostics {
			if diagnostic.Severity == "error" {
				return wfAuthoringErr(http.StatusBadRequest, diagnostic.Code, diagnostic.Message)
			}
		}
		return nil
	}
	for _, node := range def.Nodes {
		if node.Kind == workflow.KindHuman {
			continue
		}
		if err := check(node.Agent, node.Tools); err != nil {
			return err
		}
		if node.Orchestrate != nil {
			for _, candidate := range node.Orchestrate.Candidates {
				if err := check(candidate.Agent, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Server) workflowTrialReceipt(meta taskqueue.WorkflowRunMeta) map[string]any {
	status := "queued"
	if rec, ok := s.mgr.wfruns.snapshot(meta.ID); ok {
		status = rec.Status
		if rec.Status == workflow.RunWorking {
			for _, state := range rec.Nodes {
				if state.Status == workflow.NodeWaitingHuman {
					status = "waiting_human"
				}
			}
		}
	}
	receipt := map[string]any{"id": meta.ID, "run_id": meta.ID, "workflow_id": meta.WorkflowID, "status": status,
		"test_run": meta.TestRun, "draft_revision": meta.DraftRevision, "definition_hash": meta.DefinitionHash, "version_id": meta.VersionID,
		"real_execution": true, "result_url": "/manage/workflows/" + meta.WorkflowID + "?run=" + meta.ID}
	receipt["run_url"] = receipt["result_url"]
	if active, _ := s.mgr.releaseMaintenance(); active && status == "queued" {
		receipt["waiting_reason"] = "maintenance"
	}
	return receipt
}

func (s *Server) workflowAuthoringRun(r *http.Request, action string, raw json.RawMessage) (any, error) {
	var request workflowRunRequest
	if err := decodeWFAuthoring(raw, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.WorkflowID) == "" || request.RequestID == "" {
		return nil, wfAuthoringErr(http.StatusBadRequest, "request_fields_required", "需要 workflow_id 和 request_id")
	}
	if len(request.RequestID) > 256 {
		return nil, wfAuthoringErr(http.StatusBadRequest, "request_id_too_long", "request_id 过长")
	}
	var meta taskqueue.WorkflowRunMeta
	var err error
	if action == "test" {
		if request.VersionID != "" {
			return nil, wfAuthoringErr(http.StatusBadRequest, "unexpected_version_id", "草稿试跑使用 expected_revision 和 expected_hash")
		}
		meta, err = s.workflowTrial(r, request.WorkflowID, request.ExpectedRevision, request.ExpectedHash, request.Inputs, request.RequestID)
	} else if action == "run" {
		if request.ExpectedRevision != 0 || request.ExpectedHash != "" {
			return nil, wfAuthoringErr(http.StatusBadRequest, "unexpected_draft_version", "正式运行使用已发布版本，可用 version_id 固定该版本")
		}
		meta, err = s.workflowPublishedRun(r, request.WorkflowID, request.Inputs, request.RequestID, request.VersionID)
	} else {
		return nil, wfAuthoringErr(http.StatusBadRequest, "unknown_action", "未知的运行操作")
	}
	if err != nil {
		return nil, err
	}
	return s.workflowTrialReceipt(meta), nil
}

func (s *Server) handleWorkflowTrial(w http.ResponseWriter, r *http.Request) {
	var request workflowRunRequest
	if err := decodeWorkflowRunRequest(r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if request.WorkflowID != "" && request.WorkflowID != r.PathValue("wid") || request.VersionID != "" {
		writeErr(w, http.StatusBadRequest, errors.New("试跑请求与当前工作流不一致"))
		return
	}
	if request.RequestID == "" {
		request.RequestID = r.Header.Get("Idempotency-Key")
	}
	meta, err := s.workflowTrial(r, r.PathValue("wid"), request.ExpectedRevision, request.ExpectedHash, request.Inputs, request.RequestID)
	if err != nil {
		status := http.StatusBadRequest
		var typed *workflowAuthoringError
		if errors.As(err, &typed) {
			status = typed.Status
			writeJSON(w, status, map[string]any{"error": typed.Message, "code": typed.Code, "diagnostics": typed.Diagnostics})
			return
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.workflowTrialReceipt(meta))
}

// A test continuation/retry must retain the same edit/invoke grant and runtime
// checks as its admission, including when it came from the task-center API.
// It does not authorize itself by trusting a browser's test_run marker.
func (s *Server) workflowTrialExecutionAllowed(meta taskqueue.WorkflowRunMeta, principal identity.Principal) bool {
	if !meta.TestRun {
		return true
	}
	r, _ := http.NewRequest(http.MethodPost, "/", nil)
	r = withWorkflowPrincipal(r, principal)
	for _, action := range []string{wfEdit, wfInvoke} {
		if allowed, err := s.workflowAllowed(r, meta.WorkflowID, action); err != nil || !allowed {
			return false
		}
	}
	var def workflow.Definition
	if json.Unmarshal(meta.Definition, &def) != nil || def.Revision != meta.DraftRevision {
		return false
	}
	hash, err := workflowDefinitionHash(def)
	if err != nil || hash != meta.DefinitionHash {
		return false
	}
	validation := s.workflowAuthoringValidate(r, workflowInput{Name: def.Name, Description: def.Description, Nodes: def.Nodes}, meta.Inputs)
	return validation.Valid && validation.ReadyForTest && s.workflowTrialRuntimeSupport(r, def) == nil
}

// Validate the actual captured capability snapshot immediately before each
// test node/candidate creates its Agent. A long-running prior node must not
// make a newly broadened Agent bypass the trial preflight.
func workflowTrialNodeExecutionError(run *wfRun, a studioAgent, node workflow.Node, registry *tools.Registry, synth, external []tools.Tool, approved []string) error {
	run.mu.Lock()
	isTest := run.record.TestRun
	run.mu.Unlock()
	if !isTest {
		return nil
	}
	if len(a.SkillBinding) > 0 {
		return errors.New("试跑节点的 Agent 已改为需要尚未接入工作流的运行型 Skill")
	}
	selected := narrowWorkflowTools(a.Tools, node.Tools)
	if selected == nil {
		return errors.New("试跑节点的 Agent 已改为继承包含命令执行的工具集，请重新校验")
	}
	actual := map[string]bool{}
	for _, capability := range registry.All() {
		actual[capability.Name()] = true
	}
	for _, name := range *selected {
		if !actual[name] {
			return errors.New("试跑节点当前没有注册所选工具，请重新校验：" + name)
		}
	}
	if actual["bash"] {
		return errors.New("试跑节点的 Agent 已改为需要尚未接入工作流的命令审批链")
	}
	// Synth readonly hints are supplied by a bundle author. In the ordinary
	// chat they only waive review under a real active command sandbox; this
	// cut-down workflow executor has neither that gate nor its binding. Phase
	// one therefore never admits a Synth execution, even if its name also
	// appears in an annotation-derived or caller-provided preapproval list.
	if len(synth) > 0 {
		return errors.New("试跑节点包含尚未接入沙箱与逐工具审批的 Skill 声明工具，请重新校验")
	}
	preapproved := map[string]bool{}
	for _, name := range approved {
		preapproved[name] = true
	}
	for _, capability := range external {
		if !preapproved[capability.Name()] {
			return errors.New("试跑节点的外部工具已改变且尚未接入逐工具审批，请重新校验：" + capability.Name())
		}
	}
	return nil
}
