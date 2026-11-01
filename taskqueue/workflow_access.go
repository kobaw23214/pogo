package taskqueue

import (
	"database/sql"
	"encoding/json"
	"sort"
)

type AccessRoleBinding struct {
	PrincipalID string `json:"principal_id"`
	Role        string `json:"role"`
}

type AccessGrant struct {
	PrincipalID string   `json:"principal_id"`
	NodeID      string   `json:"node_id,omitempty"`
	Actions     []string `json:"actions"`
}

func (s *Store) AccessRole(principalID string) (string, error) {
	if !validText(principalID, 512, true) {
		return "", ErrConflict
	}
	var role string
	err := s.db.QueryRow("SELECT role FROM workflow_space_roles WHERE space_id=? AND principal_id=?", DefaultSpaceID, principalID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

func (s *Store) SetAccessRole(principalID, role string) error {
	if !validText(principalID, 512, true) {
		return ErrConflict
	}
	switch role {
	case "", "space_admin", "builder", "user", "disabled":
	default:
		return ErrConflict
	}
	if role == "" {
		_, err := s.db.Exec("DELETE FROM workflow_space_roles WHERE space_id=? AND principal_id=?", DefaultSpaceID, principalID)
		return err
	}
	_, err := s.db.Exec("INSERT INTO workflow_space_roles(space_id,principal_id,role) VALUES(?,?,?) ON CONFLICT(space_id,principal_id) DO UPDATE SET role=excluded.role", DefaultSpaceID, principalID, role)
	return err
}

func (s *Store) ListAccessRoles() ([]AccessRoleBinding, error) {
	rows, err := s.db.Query("SELECT principal_id,role FROM workflow_space_roles WHERE space_id=? ORDER BY principal_id", DefaultSpaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessRoleBinding{}
	for rows.Next() {
		var item AccessRoleBinding
		if err := rows.Scan(&item.PrincipalID, &item.Role); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func encodedActions(actions []string) ([]byte, error) {
	if len(actions) > 64 {
		return nil, ErrConflict
	}
	unique := map[string]bool{}
	for _, action := range actions {
		if !validText(action, 128, true) {
			return nil, ErrConflict
		}
		unique[action] = true
	}
	ordered := make([]string, 0, len(unique))
	for action := range unique {
		ordered = append(ordered, action)
	}
	sort.Strings(ordered)
	return json.Marshal(ordered)
}

func decodedActions(raw []byte) ([]string, error) {
	var actions []string
	if err := json.Unmarshal(raw, &actions); err != nil {
		return nil, ErrConflict
	}
	if _, err := encodedActions(actions); err != nil {
		return nil, err
	}
	if actions == nil {
		actions = []string{}
	}
	return actions, nil
}

// WorkflowGrant distinguishes no override (nil) from an explicit empty grant.
// The host may retain legacy defaults only when no override exists.
func (s *Store) WorkflowGrant(workflowID, principalID string) ([]string, error) {
	if !validText(workflowID, 256, true) || !validText(principalID, 512, true) {
		return nil, ErrConflict
	}
	var raw []byte
	err := s.db.QueryRow("SELECT actions FROM workflow_resource_grants WHERE workflow_id=? AND principal_id=?", workflowID, principalID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodedActions(raw)
}

func (s *Store) SetWorkflowGrant(workflowID, principalID string, actions []string) error {
	if !validText(workflowID, 256, true) || !validText(principalID, 512, true) {
		return ErrConflict
	}
	raw, err := encodedActions(actions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO workflow_resource_grants(workflow_id,principal_id,actions) VALUES(?,?,?) ON CONFLICT(workflow_id,principal_id) DO UPDATE SET actions=excluded.actions", workflowID, principalID, raw)
	return err
}

// DeleteWorkflowGrant restores the host's default policy for this resource.
// SetWorkflowGrant(..., nil) instead creates an explicit empty grant.
func (s *Store) DeleteWorkflowGrant(workflowID, principalID string) error {
	if !validText(workflowID, 256, true) || !validText(principalID, 512, true) {
		return ErrConflict
	}
	_, err := s.db.Exec("DELETE FROM workflow_resource_grants WHERE workflow_id=? AND principal_id=?", workflowID, principalID)
	return err
}

func (s *Store) RunGrant(runID, principalID, nodeID string) ([]string, error) {
	if !validText(runID, 128, true) || !validText(principalID, 512, true) || !validText(nodeID, 64, false) {
		return nil, ErrConflict
	}
	var raw []byte
	err := s.db.QueryRow("SELECT actions FROM workflow_run_grants WHERE run_id=? AND principal_id=? AND node_id=?", runID, principalID, nodeID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodedActions(raw)
}

func (s *Store) SetRunGrant(runID, principalID, nodeID string, actions []string) error {
	if !validText(runID, 128, true) || !validText(principalID, 512, true) || !validText(nodeID, 64, false) {
		return ErrConflict
	}
	raw, err := encodedActions(actions)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		_, err := s.db.Exec("DELETE FROM workflow_run_grants WHERE run_id=? AND principal_id=? AND node_id=?", runID, principalID, nodeID)
		return err
	}
	_, err = s.db.Exec("INSERT INTO workflow_run_grants(run_id,principal_id,node_id,actions) VALUES(?,?,?,?) ON CONFLICT(run_id,principal_id,node_id) DO UPDATE SET actions=excluded.actions", runID, principalID, nodeID, raw)
	return err
}

func (s *Store) ListWorkflowGrants(workflowID string) ([]AccessGrant, error) {
	if !validText(workflowID, 256, true) {
		return nil, ErrConflict
	}
	rows, err := s.db.Query("SELECT principal_id,'' AS node_id,actions FROM workflow_resource_grants WHERE workflow_id=? ORDER BY principal_id", workflowID)
	if err != nil {
		return nil, err
	}
	return scanAccessGrants(rows)
}

func (s *Store) ListRunGrants(runID string) ([]AccessGrant, error) {
	if !validText(runID, 128, true) {
		return nil, ErrConflict
	}
	rows, err := s.db.Query("SELECT principal_id,node_id,actions FROM workflow_run_grants WHERE run_id=? ORDER BY principal_id,node_id", runID)
	if err != nil {
		return nil, err
	}
	return scanAccessGrants(rows)
}

func scanAccessGrants(rows *sql.Rows) ([]AccessGrant, error) {
	defer rows.Close()
	out := []AccessGrant{}
	for rows.Next() {
		var item AccessGrant
		var raw []byte
		if err := rows.Scan(&item.PrincipalID, &item.NodeID, &raw); err != nil {
			return nil, err
		}
		actions, err := decodedActions(raw)
		if err != nil {
			return nil, err
		}
		item.Actions = actions
		out = append(out, item)
	}
	return out, rows.Err()
}
