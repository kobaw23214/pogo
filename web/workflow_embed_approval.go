package web

import (
	"errors"
	"sort"

	"github.com/yosukeno/pogo/workflow"
)

// Only the integration-secret-protected launch calls this adapter. A browser
// form can supply business data, never account identities or approval grants.
func (s *Server) workflowEmbedBusinessApprovers(p workflowPublication, supplied map[string][]string) (map[string][]string, error) {
	if len(supplied) == 0 {
		return nil, nil
	}
	if len(supplied) > workflow.MaxNodes {
		return nil, errors.New("业务审批分配节点过多")
	}
	version, err := s.mgr.workflows.readVersion(p.WorkflowID, p.VersionID)
	if err != nil {
		return nil, errors.New("审批分配引用的发布版本不可读")
	}
	nodes := map[string]workflow.Node{}
	for _, node := range version.Workflow.Nodes {
		nodes[node.ID] = node
	}
	mappings := map[string]string{}
	for _, mapping := range p.Mappings {
		mappings[mapping.Subject] = mapping.Username
	}
	out := map[string][]string{}
	for nodeID, subjects := range supplied {
		node, ok := nodes[nodeID]
		if !ok || node.ApprovalPolicy == nil || node.ApprovalPolicy.Assignment != "business" || (node.Kind != workflow.KindHuman && !node.Approval) {
			return nil, errors.New("只能为声明业务分配的审批节点提供审批人")
		}
		if len(subjects) == 0 || len(subjects) > 50 {
			return nil, errors.New("请为业务审批节点提供有效的审批人名单")
		}
		seen := map[string]bool{}
		for _, subject := range subjects {
			if !validWorkflowExternalSubject(subject) {
				return nil, errors.New("业务审批人标识无效")
			}
			username, known := mappings[subject]
			principal, valid := s.workflowEmbedLocalPrincipal(username)
			if !known || !valid {
				return nil, errors.New("业务审批人尚未映射到有效的普通平台账号")
			}
			role, err := s.workflowRole(principal)
			if err != nil || role == "disabled" {
				return nil, errors.New("业务审批人账号不可用")
			}
			if !seen[principal.ID] {
				seen[principal.ID] = true
				out[nodeID] = append(out[nodeID], principal.ID)
			}
		}
		sort.Strings(out[nodeID])
	}
	return out, nil
}
