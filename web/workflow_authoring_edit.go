package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/workflow"
)

// Receipts and drafts share one atomic registry write. A crash cannot commit
// the edit while losing its request id, nor replay a create as another graph.
// This optional metadata is absent from legacy registries and version hashes.
type workflowAuthoringReceipt struct {
	ActorID     string                    `json:"actor_id"`
	AcceptedAt  string                    `json:"accepted_at"`
	Fingerprint string                    `json:"fingerprint"`
	Workflow    Workflow                  `json:"workflow"`
	Changes     []workflowAuthoringChange `json:"changes"`
}
type workflowAuthoringChange struct {
	Operation string `json:"operation"`
	NodeID    string `json:"node_id,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}
type workflowAuthoringOperation struct {
	Op          string                     `json:"op"`
	NodeID      string                     `json:"node_id,omitempty"`
	Node        *workflow.Node             `json:"node,omitempty"`
	Patch       map[string]json.RawMessage `json:"patch,omitempty"`
	From        string                     `json:"from,omitempty"`
	To          string                     `json:"to,omitempty"`
	When        string                     `json:"when,omitempty"`
	Name        *string                    `json:"name,omitempty"`
	Description *string                    `json:"description,omitempty"`
}
type workflowAuthoringEditRequest struct {
	Action           string                       `json:"action"`
	WorkflowID       string                       `json:"workflow_id,omitempty"`
	ExpectedRevision int                          `json:"expected_revision,omitempty"`
	RequestID        string                       `json:"request_id"`
	Name             string                       `json:"name,omitempty"`
	Description      string                       `json:"description,omitempty"`
	Nodes            []workflow.Node              `json:"nodes,omitempty"`
	Operations       []workflowAuthoringOperation `json:"operations,omitempty"`
}

func cloneAuthoringWorkflow(w Workflow) Workflow {
	raw, _ := json.Marshal(w)
	var copy Workflow
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func workflowAuthoringPatch(in workflowInput, ops []workflowAuthoringOperation) (workflowInput, []workflowAuthoringChange, error) {
	raw, _ := json.Marshal(in)
	var private workflowInput
	_ = json.Unmarshal(raw, &private)
	in = private
	changes := []workflowAuthoringChange{}
	if len(ops) == 0 || len(ops) > 100 {
		return in, nil, wfAuthoringErr(400, "invalid_operations", "局部修改需要 1–100 个操作")
	}
	find := func(id string) int {
		for i, n := range in.Nodes {
			if n.ID == id {
				return i
			}
		}
		return -1
	}
	for _, op := range ops {
		i := find(op.NodeID)
		switch op.Op {
		case "add_node":
			if op.Node == nil || op.Node.ID == "" {
				return in, nil, wfAuthoringErr(400, "node_required", "add_node 需要 node 及稳定的 node.id")
			}
			if op.NodeID != "" && op.NodeID != op.Node.ID {
				return in, nil, wfAuthoringErr(400, "node_id_mismatch", "node_id 与 node.id 不一致")
			}
			if find(op.Node.ID) >= 0 {
				return in, nil, wfAuthoringErr(409, "node_exists", "节点已存在："+op.Node.ID)
			}
			in.Nodes = append(in.Nodes, *op.Node)
			changes = append(changes, workflowAuthoringChange{Operation: op.Op, NodeID: op.Node.ID})
		case "update_node":
			if i < 0 {
				return in, nil, wfAuthoringErr(400, "node_not_found", "需要修改的节点不存在："+op.NodeID)
			}
			if len(op.Patch) == 0 {
				return in, nil, wfAuthoringErr(400, "patch_required", "update_node 需要非空 patch")
			}
			if _, ok := op.Patch["id"]; ok {
				return in, nil, wfAuthoringErr(400, "stable_node_id", "局部修改不能更改节点 ID")
			}
			raw, _ := json.Marshal(in.Nodes[i])
			var values map[string]json.RawMessage
			_ = json.Unmarshal(raw, &values)
			for key, value := range op.Patch {
				values[key] = value
			}
			raw, _ = json.Marshal(values)
			var node workflow.Node
			if err := decodeWFAuthoring(raw, &node); err != nil {
				return in, nil, err
			}
			in.Nodes[i] = node
			changes = append(changes, workflowAuthoringChange{Operation: op.Op, NodeID: op.NodeID})
		case "remove_node":
			if i < 0 {
				return in, nil, wfAuthoringErr(400, "node_not_found", "需要删除的节点不存在："+op.NodeID)
			}
			in.Nodes = append(in.Nodes[:i], in.Nodes[i+1:]...)
			for j := range in.Nodes {
				in.Nodes[j].After = slices.DeleteFunc(in.Nodes[j].After, func(d workflow.Dep) bool { return d.Node == op.NodeID })
			}
			changes = append(changes, workflowAuthoringChange{Operation: op.Op, NodeID: op.NodeID})
		case "connect", "disconnect":
			target := find(op.To)
			if find(op.From) < 0 || target < 0 {
				return in, nil, wfAuthoringErr(400, "edge_node_not_found", "连线的 from/to 节点不存在")
			}
			deps := in.Nodes[target].After
			index := -1
			for j, d := range deps {
				if d.Node == op.From {
					index = j
					break
				}
			}
			if op.Op == "connect" {
				when := op.When
				if when == "" {
					when = workflow.WhenSuccess
				}
				if index < 0 {
					deps = append(deps, workflow.Dep{Node: op.From, When: when})
				} else {
					deps[index].When = when
				}
			} else if index >= 0 {
				deps = append(deps[:index], deps[index+1:]...)
			}
			in.Nodes[target].After = deps
			changes = append(changes, workflowAuthoringChange{Operation: op.Op, From: op.From, To: op.To})
		case "set_metadata":
			if op.Name == nil && op.Description == nil {
				return in, nil, wfAuthoringErr(400, "metadata_required", "set_metadata 需要 name 或 description")
			}
			if op.Name != nil {
				in.Name = *op.Name
			}
			if op.Description != nil {
				in.Description = *op.Description
			}
			changes = append(changes, workflowAuthoringChange{Operation: op.Op})
		default:
			return in, nil, wfAuthoringErr(400, "unsupported_operation", "不支持的草稿操作："+op.Op)
		}
	}
	return in, changes, nil
}
func (s *workflowStore) authoringEdit(principal string, req workflowAuthoringEditRequest) (workflowAuthoringReceipt, bool, error) {
	raw, _ := json.Marshal(req)
	fingerprint := sha256.Sum256(raw)
	key := sha256.Sum256([]byte(principal + "\x00" + req.RequestID))
	digest, receiptKey := hex.EncodeToString(fingerprint[:]), hex.EncodeToString(key[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.data.AuthoringReceipts[receiptKey]; ok {
		if previous.Fingerprint != digest {
			return workflowAuthoringReceipt{}, false, wfAuthoringErr(409, "request_conflict", "同一 request_id 已用于不同的修改意图")
		}
		return previous, true, nil
	}
	if len(s.data.AuthoringReceipts) >= 1000 {
		return workflowAuthoringReceipt{}, false, wfAuthoringErr(409, "authoring_receipts_full", "草稿操作回执已达到容量上限，请由运维处理存储容量；不会丢弃幂等记录")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var w Workflow
	changes := []workflowAuthoringChange{}
	if req.Action == "create" {
		if len(s.data.Workflows) >= maxWorkflows {
			return workflowAuthoringReceipt{}, false, errors.New("工作流数量已达到当前上限")
		}
		var id [12]byte
		if _, err := rand.Read(id[:]); err != nil {
			return workflowAuthoringReceipt{}, false, err
		}
		w = Workflow{workflowInput: workflowInput{Name: req.Name, Description: req.Description, Nodes: req.Nodes}, ID: "wf_" + hex.EncodeToString(id[:]), Revision: 1, CreatedAt: now, UpdatedAt: now}
		changes = append(changes, workflowAuthoringChange{Operation: "create"})
	} else {
		old, ok := s.data.Workflows[req.WorkflowID]
		if !ok || old.Deleted {
			return workflowAuthoringReceipt{}, false, ErrNotFound
		}
		if req.ExpectedRevision != old.Revision {
			return workflowAuthoringReceipt{}, false, wfAuthoringErr(409, "revision_conflict", "草稿已更新，请重新读取后应用修改")
		}
		w = cloneAuthoringWorkflow(old)
		var err error
		w.workflowInput, changes, err = workflowAuthoringPatch(w.workflowInput, req.Operations)
		if err != nil {
			return workflowAuthoringReceipt{}, false, err
		}
		w.Revision++
		w.UpdatedAt = now
	}
	if err := validateWorkflowInput(&w.workflowInput); err != nil {
		return workflowAuthoringReceipt{}, false, wfAuthoringErr(400, "invalid_definition", err.Error())
	}
	if s.refs != nil {
		if err := s.refs(&w.workflowInput); err != nil {
			return workflowAuthoringReceipt{}, false, wfAuthoringErr(400, "invalid_reference", err.Error())
		}
	}
	receipt := workflowAuthoringReceipt{ActorID: principal, AcceptedAt: now, Fingerprint: digest, Workflow: cloneAuthoringWorkflow(w), Changes: changes}
	next := s.data
	next.Workflows = maps.Clone(s.data.Workflows)
	next.Workflows[w.ID] = w
	next.AuthoringReceipts = maps.Clone(s.data.AuthoringReceipts)
	if next.AuthoringReceipts == nil {
		next.AuthoringReceipts = map[string]workflowAuthoringReceipt{}
	}
	next.AuthoringReceipts[receiptKey] = receipt
	if err := s.save(next); err != nil {
		return workflowAuthoringReceipt{}, false, err
	}
	s.data = next
	return receipt, false, nil
}
func (s *Server) workflowAuthoringEdit(r *http.Request, raw json.RawMessage) (any, error) {
	var req workflowAuthoringEditRequest
	if err := decodeWFAuthoring(raw, &req); err != nil {
		return nil, err
	}
	if req.Action != "create" && req.Action != "patch" {
		return nil, wfAuthoringErr(400, "unsupported_action", "编辑操作必须是 create 或 patch")
	}
	if strings.TrimSpace(req.RequestID) != req.RequestID || req.RequestID == "" || len(req.RequestID) > 128 || strings.IndexFunc(req.RequestID, unicode.IsControl) >= 0 {
		return nil, wfAuthoringErr(400, "request_id_required", "需要 1–128 字符且无控制字符的 request_id")
	}
	if req.Action == "create" && (req.WorkflowID != "" || req.ExpectedRevision != 0 || len(req.Operations) > 0) {
		return nil, wfAuthoringErr(400, "invalid_create", "创建不能指定现有工作流或局部修改操作")
	}
	if req.Action == "patch" && (!validID(req.WorkflowID) || req.ExpectedRevision < 1 || req.Name != "" || req.Description != "" || req.Nodes != nil) {
		return nil, wfAuthoringErr(400, "invalid_patch", "局部修改需要 workflow_id、expected_revision 和 operations")
	}
	if err := s.authoringAllowed(r, req.WorkflowID, wfEdit); err != nil {
		return nil, err
	}
	receipt, reused, err := s.mgr.workflows.authoringEdit(s.workflowPrincipal(r).ID, req)
	if errors.Is(err, ErrNotFound) {
		return nil, wfAuthoringErr(404, "not_found", "工作流不存在或不可用")
	}
	if err != nil {
		return nil, err
	}
	// A replay still needs authority over the actual created resource, including
	// a later explicit revocation. Never return the saved receipt as a bypass.
	if err := s.authoringAllowed(r, receipt.Workflow.ID, wfEdit); err != nil {
		return nil, err
	}
	view := s.workflowViewFor(receipt.Workflow, s.workflowSchedules())
	caps := s.workflowCapabilities(r, receipt.Workflow.ID)
	view.Capabilities = &caps
	_, _, hash, _ := s.mgr.workflows.canonicalDraft(receipt.Workflow)
	validation := s.workflowAuthoringValidate(r, receipt.Workflow.workflowInput, nil)
	validation.DefinitionHash = hash
	return map[string]any{"ok": true, "workflow": view, "workflow_id": receipt.Workflow.ID, "revision": receipt.Workflow.Revision, "definition_hash": hash, "changes": receipt.Changes, "reused": reused, "validation": validation, "editor_url": fmt.Sprintf("/manage/workflows/%s", receipt.Workflow.ID)}, nil
}
