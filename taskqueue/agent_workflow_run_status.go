package taskqueue

// AWRunStatus contains only the metadata needed by the always-visible Agent
// workflow entry. It deliberately excludes the frozen definition, inputs,
// execution transcripts and artifacts from periodic status requests.
type AWRunStatus struct {
	ID              string `json:"id"`
	WorkflowID      string `json:"workflow_id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	CancelRequested bool   `json:"cancel_requested"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// AWActiveRunStatus reads every nonterminal Run in this exact owner/Agent
// scope. No historical record is rewritten or decoded into a full Run.
func (s *Store) AWActiveRunStatus(owner, agent string) ([]AWRunStatus, error) {
	if err := awScope(owner, agent); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,workflow_id,
 COALESCE(json_extract(CAST(record AS TEXT),'$.definition.name'),''),
 json_extract(CAST(record AS TEXT),'$.status'),
 COALESCE(json_extract(CAST(record AS TEXT),'$.cancel_requested'),0),created_at,
 COALESCE(json_extract(CAST(record AS TEXT),'$.updated_at'),created_at)
 FROM agent_workflow_runs WHERE owner_id=? AND agent_id=?
 AND json_extract(CAST(record AS TEXT),'$.status') IN ('queued','running','waiting_confirmation')
 ORDER BY created_at DESC,id DESC`, owner, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AWRunStatus{}
	for rows.Next() {
		var row AWRunStatus
		if err := rows.Scan(&row.ID, &row.WorkflowID, &row.Name, &row.Status, &row.CancelRequested, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
