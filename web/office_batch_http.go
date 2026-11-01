package web

import (
	"encoding/json"
	"net/http"
)

func (s *Server) officeBatchRoutes() {
	s.mux.HandleFunc("POST /api/office/batches", s.handleOfficeBatchPrepare)
	s.mux.HandleFunc("GET /api/office/batches/{bid}", s.handleOfficeBatchStatus)
	s.mux.HandleFunc("POST /api/office/batches/{bid}/commit", s.handleOfficeBatchCommit)
	s.mux.HandleFunc("POST /api/office/batches/{bid}/abort", s.handleOfficeBatchAbort)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/reload", s.handleOfficeReload)
}
func (s *Server) officeBatchLink(w http.ResponseWriter, r *http.Request, id string) (*officeLink, bool) {
	if !s.officeAvailable(w) {
		return nil, false
	}
	session, ok := s.officeConversation(w, r, id)
	if !ok {
		return nil, false
	}
	return s.office.link(session.ID, session.cwd, session.owner, session.hub).(*officeLink), true
}
func (s *Server) handleOfficeBatchPrepare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ConversationID string `json:"conversationId"`
		Path           string `json:"path"`
		OperationID    string `json:"operationId"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	l, ok := s.officeBatchLink(w, r, body.ConversationID)
	if !ok {
		return
	}
	absolute, _, err := s.officeRequestedPath(r, l.owner, body.Path)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	reply, err := s.office.prepareBatch(r.Context(), l, absolute, body.OperationID, "batch", "")
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, reply)
}
func (s *Server) handleOfficeBatchStatus(w http.ResponseWriter, r *http.Request) {
	l, ok := s.officeBatchLink(w, r, r.URL.Query().Get("conversation_id"))
	if !ok {
		return
	}
	reply, err := l.executeBatch(r.Context(), officeToolRequest{Action: "batch_status", BatchID: r.PathValue("bid")})
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(reply)
}
func (s *Server) handleOfficeBatchCommit(w http.ResponseWriter, r *http.Request) {
	s.handleOfficeBatchEnd(w, r, true)
}
func (s *Server) handleOfficeBatchAbort(w http.ResponseWriter, r *http.Request) {
	s.handleOfficeBatchEnd(w, r, false)
}
func (s *Server) handleOfficeBatchEnd(w http.ResponseWriter, r *http.Request, commit bool) {
	var body struct {
		ConversationID string `json:"conversationId"`
		OperationID    string `json:"operationId"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	l, ok := s.officeBatchLink(w, r, body.ConversationID)
	if !ok {
		return
	}
	action := "batch_abort"
	if commit {
		action = "batch_commit"
	}
	reply, err := l.executeBatch(r.Context(), officeToolRequest{Action: action, BatchID: r.PathValue("bid"), OperationID: body.OperationID})
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}
func (s *Server) handleOfficeReload(w http.ResponseWriter, r *http.Request) {
	d, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		OperationID            string  `json:"operationId"`
		ExpectedVersion        string  `json:"expectedVersion"`
		DiscardChanges         bool    `json:"discardChanges"`
		InstanceID             string  `json:"instanceId"`
		ConfirmedCanvasVersion string  `json:"confirmedCanvasVersion"`
		ConfirmedDirtyEpoch    *uint64 `json:"confirmedDirtyEpoch"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	l := s.office.link(d.conversationID, d.cwd, d.owner, d.hub).(*officeLink)
	var discard *officeDiscardConfirmation
	if body.DiscardChanges {
		if body.ConfirmedDirtyEpoch == nil || body.InstanceID == "" || body.ConfirmedCanvasVersion == "" {
			officeHTTPError(w, officeFail(400, "DISCARD_CONFIRMATION_REQUIRED", "请明确确认要放弃的当前画布版本。"))
			return
		}
		discard = &officeDiscardConfirmation{InstanceID: body.InstanceID, CanvasVersion: body.ConfirmedCanvasVersion, DirtyEpoch: *body.ConfirmedDirtyEpoch}
	}
	reply, err := s.office.reloadDocumentConfirmed(r.Context(), l, d, body.OperationID, body.ExpectedVersion, discard)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, reply)
}
func (s *Server) handleOfficeFreeze(w http.ResponseWriter, r *http.Request, d *officeDocumentSession) {
	if r.Method != http.MethodPost {
		officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "冻结确认仅支持 POST。"))
		return
	}
	var body struct {
		InstanceID     string `json:"instanceId"`
		BatchID        string `json:"batchId"`
		CanvasVersion  string `json:"canvasVersion"`
		DirtyEpoch     uint64 `json:"dirtyEpoch"`
		PersistedEpoch uint64 `json:"persistedEpoch"`
		Frozen         bool   `json:"frozen"`
		Error          string `json:"error,omitempty"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := s.bridgeIdentity(d, body.InstanceID); err != nil {
		officeHTTPError(w, err)
		return
	}
	f := d.freeze
	if f == nil || f.id != body.BatchID || d.batchLease != body.BatchID {
		officeHTTPError(w, officeFail(409, "STALE_FREEZE", "冻结确认已过期。"))
		return
	}
	if !body.Frozen {
		if !f.acknowledged {
			f.acknowledged = true
			f.done <- officeFail(409, "FREEZE_REJECTED", "编辑器无法确认已保存并冻结，批处理未开始。")
			d.syncRevision++
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": d.viewProjectionLocked(false)})
		return
	}
	if body.CanvasVersion != d.baseHash || body.DirtyEpoch != d.dirtyEpoch || body.PersistedEpoch != d.persistedEpoch || !officeBatchCanvasSafe(d, f.discard) || d.blocked || d.saving || d.exportBusy || officePendingLocked(d) {
		officeHTTPError(w, officeFail(409, "FREEZE_STATE_CHANGED", "冻结期间编辑状态改变，请保留画布并重新核对。"))
		return
	}
	if !f.acknowledged {
		f.acknowledged = true
		f.phase = "frozen"
		d.syncRevision++
		f.done <- nil
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": d.viewProjectionLocked(false)})
}
func (s *Server) handleOfficeReloadReady(w http.ResponseWriter, r *http.Request, d *officeDocumentSession) {
	if r.Method != http.MethodPost {
		officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "版本确认仅支持 POST。"))
		return
	}
	var body struct {
		InstanceID      string `json:"instanceId"`
		CanvasVersion   string `json:"canvasVersion"`
		ReadOperationID string `json:"readOperationId"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	s.office.observeDisk(d)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := s.bridgeIdentity(d, body.InstanceID); err != nil {
		officeHTTPError(w, err)
		return
	}
	op := d.operations[body.ReadOperationID]
	valid := false
	if op != nil && op.finished && op.dispatched && (op.name == "word.read" || op.name == "sheet.read") {
		var result struct {
			OK          bool   `json:"ok"`
			OperationID string `json:"operationId"`
		}
		valid = json.Unmarshal(op.result, &result) == nil && result.OK && result.OperationID == op.id
	}
	if !valid || body.CanvasVersion != d.baseHash || d.externalChange || d.diskVersion != d.baseHash || !d.ready || d.blocked || d.closed {
		officeHTTPError(w, officeFail(409, "RELOAD_UNVERIFIED", "新画布尚未通过实际内容读取与磁盘版本确认。"))
		return
	}
	d.reloadConfirmed = true
	d.replacementPending = false
	d.syncRevision++
	writeJSON(w, 200, map[string]any{"ok": true, "session": d.viewProjectionLocked(false)})
}
