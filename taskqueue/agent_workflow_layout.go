package taskqueue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func initAgentWorkflowLayoutSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_workflow_layouts(
 owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,workflow_id TEXT NOT NULL REFERENCES agent_workflow_definitions(id),
 layout BLOB NOT NULL,PRIMARY KEY(owner_id,agent_id,workflow_id));
CREATE TABLE IF NOT EXISTS agent_workflow_layout_edits(
 owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,workflow_id TEXT NOT NULL REFERENCES agent_workflow_definitions(id),
 request_key TEXT NOT NULL,fingerprint TEXT NOT NULL,response BLOB NOT NULL,
 PRIMARY KEY(owner_id,agent_id,workflow_id,request_key));`)
	return err
}

func decodeAWLayout(raw []byte, wid string) (af.Layout, error) {
	var layout af.Layout
	if json.Unmarshal(raw, &layout) != nil || layout.WorkflowID != wid || layout.LayoutRevision < 1 || layout.DefinitionRevision < 1 {
		return layout, fmt.Errorf("%w: invalid internal workflow layout", ErrConflict)
	}
	if err := af.ValidateLayout(layout.Positions, layout.Viewport); err != nil {
		return layout, fmt.Errorf("%w: invalid stored layout: %v", ErrConflict, err)
	}
	return layout, nil
}

func (s *Store) AWLayout(owner, agent, wid string) (af.Layout, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Layout{}, err
	}
	// The joined read observes definition and layout in one SQLite snapshot.
	var definition, raw []byte
	err := s.db.QueryRow(`SELECT d.definition,l.layout FROM agent_workflow_definitions d
LEFT JOIN agent_workflow_layouts l ON l.owner_id=d.owner_id AND l.agent_id=d.agent_id AND l.workflow_id=d.id
WHERE d.id=? AND d.owner_id=? AND d.agent_id=?`, wid, owner, agent).Scan(&definition, &raw)
	if err == sql.ErrNoRows {
		return af.Layout{}, ErrNotFound
	}
	if err != nil {
		return af.Layout{}, err
	}
	var d af.Definition
	if json.Unmarshal(definition, &d) != nil || d.DefinitionHash != af.Hash(d) {
		return af.Layout{}, fmt.Errorf("%w: internal workflow integrity check", ErrConflict)
	}
	layout := af.Layout{WorkflowID: wid, Positions: map[string]af.Position{}}
	if raw != nil {
		layout, err = decodeAWLayout(raw, wid)
		if err != nil {
			return af.Layout{}, err
		}
	}
	return af.ProjectLayout(layout, d), nil
}

func (s *Store) AWSaveLayout(owner, agent, wid string, req af.LayoutSave) (af.Layout, bool, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Layout{}, false, err
	}
	if !validText(req.RequestID, 128, true) || strings.TrimSpace(req.RequestID) != req.RequestID || req.ExpectedLayoutRevision < 0 || req.ExpectedDefinitionRevision < 1 {
		return af.Layout{}, false, fmt.Errorf("%w: invalid layout revision or request ID", af.ErrInvalid)
	}
	if err := af.ValidateLayout(req.Positions, req.Viewport); err != nil {
		return af.Layout{}, false, err
	}
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > 64<<10 {
		return af.Layout{}, false, fmt.Errorf("%w: invalid layout request", af.ErrInvalid)
	}
	fingerprint := digest(raw, nil)
	tx, err := s.db.Begin()
	if err != nil {
		return af.Layout{}, false, err
	}
	defer tx.Rollback()
	d, err := awGet(tx, owner, agent, wid)
	if err != nil {
		return af.Layout{}, false, err
	}
	if d.Archived || d.Deleted {
		return af.Layout{}, false, fmt.Errorf("%w: archived workflows cannot change layout", ErrConflict)
	}
	var priorFingerprint string
	var priorResponse []byte
	err = tx.QueryRow(`SELECT fingerprint,response FROM agent_workflow_layout_edits
WHERE owner_id=? AND agent_id=? AND workflow_id=? AND request_key=?`, owner, agent, wid, req.RequestID).Scan(&priorFingerprint, &priorResponse)
	if err == nil {
		if priorFingerprint != fingerprint {
			return af.Layout{}, false, fmt.Errorf("%w: layout request ID was used for a different edit", ErrConflict)
		}
		layout, e := decodeAWLayout(priorResponse, wid)
		return layout, true, e
	}
	if err != sql.ErrNoRows {
		return af.Layout{}, false, err
	}
	if d.Revision != req.ExpectedDefinitionRevision {
		return af.Layout{}, false, fmt.Errorf("%w: 流程草稿已变化，请重新载入后保存布局", ErrConflict)
	}
	nodes := af.LayoutNodes(d)
	for id := range req.Positions {
		if !nodes[id] {
			return af.Layout{}, false, fmt.Errorf("%w: 布局包含不属于当前流程的节点", af.ErrInvalid)
		}
	}
	var previous []byte
	err = tx.QueryRow(`SELECT layout FROM agent_workflow_layouts WHERE owner_id=? AND agent_id=? AND workflow_id=?`, owner, agent, wid).Scan(&previous)
	var revision int64
	if err == nil {
		layout, e := decodeAWLayout(previous, wid)
		if e != nil {
			return af.Layout{}, false, e
		}
		revision = layout.LayoutRevision
	} else if err != sql.ErrNoRows {
		return af.Layout{}, false, err
	}
	if revision != req.ExpectedLayoutRevision || revision == math.MaxInt64 {
		return af.Layout{}, false, fmt.Errorf("%w: 画布布局已变化，请重新载入后保存", ErrConflict)
	}
	layout := af.Layout{WorkflowID: wid, LayoutRevision: revision + 1, DefinitionRevision: d.Revision, Positions: req.Positions, Viewport: req.Viewport}
	response, err := json.Marshal(layout)
	if err != nil {
		return af.Layout{}, false, err
	}
	_, err = tx.Exec(`INSERT INTO agent_workflow_layouts(owner_id,agent_id,workflow_id,layout) VALUES(?,?,?,?)
ON CONFLICT(owner_id,agent_id,workflow_id) DO UPDATE SET layout=excluded.layout`, owner, agent, wid, response)
	if err == nil {
		_, err = tx.Exec(`INSERT INTO agent_workflow_layout_edits(owner_id,agent_id,workflow_id,request_key,fingerprint,response) VALUES(?,?,?,?,?,?)`, owner, agent, wid, req.RequestID, fingerprint, response)
	}
	if err != nil {
		return af.Layout{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return af.Layout{}, false, err
	}
	return layout, false, nil
}
