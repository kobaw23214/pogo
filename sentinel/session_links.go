package sentinel

import "encoding/json"

// SessionRecords is a trusted navigation/recovery view, including removed
// watches. Model-facing List and supervisor ListAll deliberately keep their
// existing active-definition semantics. Nothing here mutates saved work.
func (s *Store) SessionRecords() ([]Watch, error) {
	rows, err := s.db.Query(`SELECT spec,state,cursor,status,error,next_at,failures,(SELECT count(*) FROM sentinel_events WHERE watch_id=sentinel_watches.id AND settled=0) FROM sentinel_watches ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Watch{}
	for rows.Next() {
		var w Watch
		var spec, state, cursor []byte
		if err := rows.Scan(&spec, &state, &cursor, &w.Status, &w.Error, &w.NextAt, &w.Failures, &w.Pending); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(spec, &w.Spec); err != nil {
			return nil, err
		}
		w.State, w.Cursor = state, cursor
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkerSessionLinks retains historical batch identities after a watch is
// removed or its retained conversation pointer changes. Old reports must not
// reappear among ordinary chats merely because a task has ended.
func (s *Store) WorkerSessionLinks() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT session_id,watch_id FROM sentinel_batches WHERE session_id<>'' ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sessionID, watchID string
		if err := rows.Scan(&sessionID, &watchID); err != nil {
			return nil, err
		}
		out[sessionID] = watchID
	}
	return out, rows.Err()
}
