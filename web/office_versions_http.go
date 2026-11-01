package web

import (
	"net/http"
	"strconv"
)

func (s *Server) officeVersionRoutes() {
	s.mux.HandleFunc("GET /api/office/versions", s.handleOfficeVersions)
	s.mux.HandleFunc("POST /api/office/versions/restore", s.handleOfficeRestoreVersion)
}

func (s *Server) handleOfficeVersions(w http.ResponseWriter, r *http.Request) {
	l, ok := s.officeBatchLink(w, r, r.URL.Query().Get("conversation_id"))
	if !ok {
		return
	}
	absolute, _, err := s.officeRequestedPath(r, l.owner, r.URL.Query().Get("path"))
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	limit := 0
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil {
			officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "文档版本分页数量不正确。"))
			return
		}
	}
	page, err := s.office.listVersions(r.Context(), l, absolute, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, page)
}

func (s *Server) handleOfficeRestoreVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ConversationID  string `json:"conversationId"`
		Path            string `json:"path"`
		VersionID       string `json:"versionId"`
		ExpectedVersion string `json:"expectedVersion"`
		OperationID     string `json:"operationId"`
		RestoreSide     string `json:"restoreSide"`
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
	// The authenticated UI entry point supplies the actor. Requests cannot
	// attribute a restore to the agent or claim another document's ownership.
	reply, err := s.office.restoreVersionSide(r.Context(), l, absolute, body.VersionID, body.ExpectedVersion, body.OperationID, body.RestoreSide, officeActivityUser)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, reply)
}
