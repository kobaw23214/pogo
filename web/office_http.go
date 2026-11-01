package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	officeruntime "github.com/yosukeno/pogo/office-runtime"
)

type officeCapabilities struct {
	Editor                               string `json:"editor"`
	Read                                 bool   `json:"read"`
	WordReplace                          bool   `json:"wordReplace"`
	SheetApply                           bool   `json:"sheetApply"`
	SheetList                            bool   `json:"sheetList"`
	SelectionRead                        bool   `json:"selectionRead"`
	NativeUndo                           bool   `json:"nativeUndo"`
	NativeRedo                           bool   `json:"nativeRedo"`
	HistoryWithoutRead                   bool   `json:"historyWithoutRead"`
	ReadPreservesHistory                 bool   `json:"readPreservesHistory"`
	ReadPreservesSelection               bool   `json:"readPreservesSelection"`
	MutationPreconditionsPreserveHistory bool   `json:"mutationPreconditionsPreserveHistory"`
	SelectiveAgentUndo                   bool   `json:"selectiveAgentUndo"`
	HistoryScope                         string `json:"historyScope"`
	GroupedAcrossCommands                bool   `json:"groupedAcrossCommands"`
	Save                                 string `json:"save"`
	Deduplication                        string `json:"deduplication"`
	Blocked                              bool   `json:"blocked"`
	EngineExtension                      struct {
		ID            string `json:"id"`
		Version       int    `json:"version"`
		Required      bool   `json:"required"`
		Detected      bool   `json:"detected"`
		EngineVersion string `json:"engineVersion,omitempty"`
		EngineBuild   string `json:"engineBuild,omitempty"`
	} `json:"engineExtension"`
}

func (cap officeCapabilities) safe() bool {
	return cap.Read && cap.HistoryWithoutRead && cap.ReadPreservesHistory && cap.ReadPreservesSelection && cap.MutationPreconditionsPreserveHistory &&
		cap.EngineExtension.ID == "pogo-office-history" && cap.EngineExtension.Version == 1 && cap.EngineExtension.Required && cap.EngineExtension.Detected
}

func (s *Server) officeRoutes() {
	s.mux.HandleFunc(officeGatewayPrefix+"/", s.handleOfficeGateway)
	s.officeBatchRoutes()
	s.officeVersionRoutes()
	s.officePresenceRoutes()
	s.mux.HandleFunc("GET /api/office/config", s.handleOfficeConfig)
	s.mux.HandleFunc("GET /api/office/sessions", s.handleOfficeList)
	s.mux.HandleFunc("POST /api/office/sessions", s.handleOfficeOpen)
	s.mux.HandleFunc("GET /api/office/sessions/{oid}/config", s.handleOfficeEditorConfig)
	s.mux.HandleFunc("GET /api/office/sessions/{oid}/status", s.handleOfficeStatus)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/commands", s.handleOfficeCommand)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/save", s.handleOfficeSave)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/close", s.handleOfficeClose)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/release", s.handleOfficeBootstrapRelease)
	s.mux.HandleFunc("/office/bridge/{oid}/{cap}/{resource}", s.handleOfficeBridge)
	s.mux.HandleFunc("GET /office/files/{oid}/{cap}", s.handleOfficeFile)
	s.mux.HandleFunc("POST /office/callbacks/{oid}/{cap}", s.handleOfficeCallback)
}

func officeHTTPError(w http.ResponseWriter, err error) {
	status := 500
	var typed *officeError
	if errors.As(err, &typed) {
		status = typed.status
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, officeErrorReply(err))
}

func officeReadBody(r *http.Request, value any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, officeMaxJSON+1))
	if err != nil {
		return err
	}
	if len(data) > officeMaxJSON {
		return officeFail(413, "REQUEST_LIMIT", "Office 请求过大。")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	return officeDecodeJSON(data, value)
}

func (s *Server) officeAvailable(w http.ResponseWriter) bool {
	if s.office == nil {
		if s.officeConfigError {
			officeHTTPError(w, officeFail(503, "OFFICE_UNAVAILABLE", officeConfigErrorReason))
			return false
		}
		officeHTTPError(w, officeFail(503, "OFFICE_DISABLED", "当前部署尚未配置 Office 编辑服务。"))
		return false
	}
	return true
}

func (s *Server) handleOfficeConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.officeAvailability(r.Context()))
}

func (s *Server) officeConversation(w http.ResponseWriter, r *http.Request, id string) (*Session, bool) {
	if !validID(id) || !s.ownsSession(r, id) {
		officeHTTPError(w, officeFail(404, "CONVERSATION_NOT_FOUND", "会话不存在。"))
		return nil, false
	}
	session, err := s.mgr.Get(id)
	if err != nil {
		officeHTTPError(w, officeFail(404, "CONVERSATION_NOT_FOUND", "会话不存在。"))
		return nil, false
	}
	session.mu.Lock()
	closing := session.closing
	session.mu.Unlock()
	if closing {
		officeHTTPError(w, officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。"))
		return nil, false
	}
	return session, true
}

func (s *Server) officeForRequest(w http.ResponseWriter, r *http.Request) (*officeDocumentSession, bool) {
	if !s.officeAvailable(w) {
		return nil, false
	}
	id := r.PathValue("oid")
	if !officeSessionID.MatchString(id) {
		officeHTTPError(w, officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。"))
		return nil, false
	}
	s.office.mu.Lock()
	document := s.office.sessions[id]
	s.office.mu.Unlock()
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	if document == nil || !officeSameOwner(document.owner, owner) || (!document.readOnly && !s.ownsSession(r, document.conversationID)) {
		officeHTTPError(w, officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。"))
		return nil, false
	}
	if document.readOnly {
		action := strings.TrimPrefix(r.URL.Path, "/api/office/sessions/"+document.id+"/")
		switch action {
		case "config", "status", "commands", "close", "heartbeat", "release":
		default:
			officeHTTPError(w, officeFail(403, "READ_ONLY_DOCUMENT", "信息库文档只读，不能进入编辑、恢复或保存流程。"))
			return nil, false
		}
	}
	if requested := r.URL.Query().Get("conversation_id"); requested != "" && requested != document.conversationID {
		officeHTTPError(w, officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。"))
		return nil, false
	}
	return document, true
}

func (s *Server) handleOfficeList(w http.ResponseWriter, r *http.Request) {
	if !s.officeAvailable(w) {
		return
	}
	session, ok := s.officeConversation(w, r, r.URL.Query().Get("conversation_id"))
	if !ok {
		return
	}
	sessions := s.office.list(session.ID, session.owner)
	if requestedPath := r.URL.Query().Get("path"); requestedPath != "" {
		absolute, panelPath, err := s.officeRequestedPath(r, session.owner, requestedPath)
		if err == nil {
			_, err = officePanelPath(session.cwd, absolute)
		}
		if err != nil {
			officeHTTPError(w, err)
			return
		}
		fileRef := officeFileRef(session.owner, panelPath)
		matches := make([]officeSessionView, 0, 1)
		for _, view := range sessions {
			if view.FileRef == fileRef {
				matches = append(matches, view)
			}
		}
		writeJSON(w, 200, map[string]any{"sessions": matches, "requestedPath": filepath.ToSlash(filepath.Clean(requestedPath)), "fileRef": fileRef})
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": sessions})
}

func (s *Server) handleOfficeOpen(w http.ResponseWriter, r *http.Request) {
	if !s.officeAvailable(w) {
		return
	}
	var body struct {
		Path           string `json:"path"`
		ConversationID string `json:"conversation_id"`
		InstanceID     string `json:"instanceId"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	if !officeOperationID.MatchString(body.InstanceID) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "请提供当前编辑器实例身份。"))
		return
	}
	session, ok := s.officeConversation(w, r, body.ConversationID)
	if !ok {
		return
	}
	absolute, panelPath, err := s.officeRequestedPath(r, session.owner, body.Path)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	session.mu.Lock()
	closing := session.closing
	session.mu.Unlock()
	if closing {
		officeHTTPError(w, officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。"))
		return
	}
	// If a config response was lost, retrying the same POST can recover the
	// same claim even while the engine is temporarily unavailable. A foreign
	// instance never gains the claim by waiting or replaying another path.
	claimed, err := s.office.recoverOpenClaim(session.ID, session.cwd, session.owner, absolute, body.InstanceID)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if claimed != nil {
		s.writeOfficeConfigRequest(w, r, claimed, body.InstanceID, releaseMaintenanceGrant(r), body.Path)
		return
	}
	if err := s.office.waitEditorReady(r.Context()); err != nil {
		officeHTTPError(w, err)
		return
	}
	journal := s.mgr.JournalFor(session.root).ForSession(session.ID)
	session.mu.Lock()
	if session.closing {
		session.mu.Unlock()
		officeHTTPError(w, officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。"))
		return
	}
	document, err := s.office.open(session.ID, session.cwd, session.owner, session.hub, absolute, panelPath, journal)
	session.mu.Unlock()
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	s.writeOfficeConfigRequest(w, r, document, body.InstanceID, releaseMaintenanceGrant(r), body.Path)
}

func (s *Server) writeOfficeConfig(w http.ResponseWriter, d *officeDocumentSession, instance string, grant [32]byte) {
	s.writeOfficeConfigPath(w, d, instance, grant, d.panelPath)
}

func (s *Server) writeOfficeConfigPath(w http.ResponseWriter, d *officeDocumentSession, instance string, grant [32]byte, requestedPath string) {
	s.writeOfficeConfigRequest(w, nil, d, instance, grant, requestedPath)
}

func (s *Server) writeOfficeConfigRequest(w http.ResponseWriter, r *http.Request, d *officeDocumentSession, instance string, grant [32]byte, requestedPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !officeOperationID.MatchString(instance) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "请提供当前编辑器实例身份。"))
		return
	}
	if d.closed {
		officeHTTPError(w, officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。"))
		return
	}
	if d.restarted || d.recoverySealed {
		officeHTTPError(w, officeFail(409, "EDITOR_FENCED", "原编辑连接已封存，请查看已保存版本或恢复副本。"))
		return
	}
	if d.instanceID != "" && d.instanceID != instance {
		officeHTTPError(w, officeFail(409, "EDITOR_ALREADY_CONNECTED", "该文档已在另一编辑器实例中打开。"))
		return
	}
	origin := d.browserOrigin
	if r != nil {
		if origin == "" {
			var allowed bool
			origin, allowed = officeRequestOrigin(r)
			if !allowed {
				officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "编辑器必须从当前 Pogo 页面打开。"))
				return
			}
		} else if !officeBoundOrigin(r, origin) {
			officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "该编辑器属于另一个 Pogo 页面来源，请回到原页面操作。"))
			return
		}
	}
	bridgeURL := "/office/bridge/" + d.id + "/" + d.bridgeToken + "/"
	config := map[string]any{
		"documentType": d.editor, "type": "desktop",
		"document": map[string]any{"key": d.key, "fileType": d.fileType, "title": d.title,
			"url":         s.office.cfg.fetchURL + "/office/files/" + d.id + "/" + d.fileToken,
			"permissions": map[string]bool{"edit": true, "download": true, "print": false, "review": true}},
		"editorConfig": map[string]any{"mode": "edit", "lang": "zh-CN",
			"user":          map[string]string{"id": "pogo-" + d.conversationID, "name": "Pogo 用户"},
			"callbackUrl":   s.office.cfg.fetchURL + "/office/callbacks/" + d.id + "/" + d.fileToken,
			"customization": map[string]any{"autosave": true, "forcesave": false, "plugins": true},
			"plugins":       map[string]any{"pluginsData": []string{bridgeURL + "config.json"}, "autostart": []string{"asc.{A6691D12-9A8F-4A33-91D4-548D3BB68D01}"}}},
	}
	if d.readOnly {
		config["document"].(map[string]any)["permissions"] = map[string]bool{"edit": false, "download": false, "print": false, "review": false, "copy": true, "comment": false, "fillForms": false}
		editorConfig := config["editorConfig"].(map[string]any)
		editorConfig["mode"] = "view"
		delete(editorConfig, "callbackUrl")
		editorConfig["customization"] = map[string]any{"autosave": false, "forcesave": false, "plugins": true, "compactHeader": true, "compactToolbar": true}
	}
	token, err := s.office.signJWT(config)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	// The caller has already passed ordinary account and conversation ownership
	// checks. Bind only after a valid config is ready; the digest is never sent
	// to the editor, and a normal (non-maintenance) request clears any old grant.
	if r != nil && s.office.gateway != nil {
		if err := s.office.gateway.issue(w, r, d); err != nil {
			officeHTTPError(w, err)
			return
		}
	}
	d.instanceID = instance
	d.browserOrigin = origin
	if d.bootstrapToken == "" {
		d.bootstrapToken = randomID(32)
	}
	d.releaseGrant = grant
	d.touchWriterLocked()
	if err := s.office.persistEditorLocked(d); err != nil {
		officeHTTPError(w, err)
		return
	}
	config["token"] = token
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	reply := map[string]any{"session": d.viewLocked(), "config": config, "browserURL": officeGatewayPrefix, "bridgeURL": bridgeURL, "bridgeToken": d.bridgeToken, "instanceOwned": true, "bootstrapToken": d.bootstrapToken, "requestedPath": filepath.ToSlash(filepath.Clean(requestedPath))}
	if d.informationSource != nil {
		reply["source"] = d.informationSource
	}
	writeJSON(w, 200, reply)
}

func (s *Server) handleOfficeEditorConfig(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if ok {
		requestedPath := r.URL.Query().Get("path")
		if requestedPath == "" {
			requestedPath = document.panelPath
		} else if document.readOnly {
			if requestedPath != document.panelPath {
				officeHTTPError(w, officeFail(400, "DOCUMENT_IDENTITY_MISMATCH", "文档身份不一致。"))
				return
			}
		} else {
			absolute, _, err := s.officeRequestedPath(r, document.owner, requestedPath)
			if err != nil || absolute != document.absolute {
				officeHTTPError(w, officeFail(400, "DOCUMENT_IDENTITY_MISMATCH", "文档路径与当前编辑会话不一致。"))
				return
			}
		}
		s.writeOfficeConfigRequest(w, r, document, r.URL.Query().Get("instanceId"), releaseMaintenanceGrant(r), requestedPath)
	}
}
func (s *Server) handleOfficeStatus(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if ok {
		if s.office.gateway != nil {
			s.office.gateway.renew(w, r, document)
		}
		w.Header().Set("Cache-Control", "no-store")
		if document.readOnly {
			document.mu.Lock()
			document.touchWriterLocked()
			document.mu.Unlock()
		}
		s.office.observeDisk(document)
		writeJSON(w, 200, map[string]any{"session": document.view()})
	}
}

func (s *Server) handleOfficeCommand(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		OperationID string          `json:"operationId"`
		Operation   string          `json:"operation"`
		Args        json.RawMessage `json:"args"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	result, err := s.office.commandAs(r.Context(), document, body.OperationID, body.Operation, body.Args, officeActivityUser)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func (s *Server) handleOfficeSave(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		OperationID string `json:"operationId,omitempty"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	view, err := s.office.saveAs(r.Context(), document, body.OperationID, officeActivityUser)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": view})
}

func (s *Server) handleOfficeClose(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var body struct{}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	if err := s.office.closeDocument(document); err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": document.view()})
}

func (s *Server) officeCapability(w http.ResponseWriter, r *http.Request, file bool, allowClosed bool) (*officeDocumentSession, bool) {
	if !s.officeAvailable(w) {
		return nil, false
	}
	s.office.mu.Lock()
	document := s.office.sessions[r.PathValue("oid")]
	s.office.mu.Unlock()
	if document == nil && file && allowClosed {
		// Retired writers never regain an editor or an active file lease. The
		// sealed identity authorizes only this signed, archival callback path.
		document, _ = s.office.loadCallbackIdentity(r.PathValue("oid"))
	}
	if document == nil {
		officeHTTPError(w, officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。"))
		return nil, false
	}
	document.mu.Lock()
	token := document.bridgeToken
	if file {
		token = document.fileToken
	}
	closed := document.closed
	document.mu.Unlock()
	if !officeEqual(r.PathValue("cap"), token) {
		officeHTTPError(w, officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。"))
		return nil, false
	}
	if closed && !allowClosed {
		officeHTTPError(w, officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。"))
		return nil, false
	}
	return document, true
}

func (s *Server) handleOfficeFile(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeCapability(w, r, true, false)
	if !ok {
		return
	}
	document.mu.Lock()
	if document.closed {
		document.mu.Unlock()
		officeHTTPError(w, officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。"))
		return
	}
	if document.restarted {
		document.mu.Unlock()
		officeHTTPError(w, officeFail(409, "EDITOR_FENCED", "原编辑连接已中断，不能重新使用旧画布。"))
		return
	}
	kind := document.fileType
	if document.readOnly {
		if document.informationSnapshot == nil {
			document.mu.Unlock()
			officeHTTPError(w, officeFail(409, "SESSION_CLOSED", "只读预览已关闭。"))
			return
		}
		reader, release, err := document.informationSnapshot.acquire()
		title := document.title
		document.mu.Unlock()
		if err != nil {
			officeHTTPError(w, err)
			return
		}
		defer release()
		officeFileHeaders(w, kind)
		http.ServeContent(w, r, title, time.Time{}, reader)
		return
	}
	data := append([]byte(nil), document.snapshot...)
	document.mu.Unlock()
	officeFileHeaders(w, kind)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Write(data)
}

func officeFileHeaders(w http.ResponseWriter, kind string) {
	mime := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if kind == "xlsx" {
		mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
}

func officeCanonicalRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "INVALID"
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
func (s *Server) handleOfficeCallback(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeCapability(w, r, true, true)
	if !ok {
		return
	}
	if document.readOnly {
		officeHTTPError(w, officeFail(403, "READ_ONLY_DOCUMENT", "只读信息库文档不接收保存回调。"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, officeMaxJSON+1))
	if err != nil || len(data) > officeMaxJSON {
		officeHTTPError(w, officeFail(400, "INVALID_CALLBACK", "保存回调无法读取。"))
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil {
		officeHTTPError(w, officeFail(400, "INVALID_CALLBACK", "保存回调不正确。"))
		return
	}
	var token string
	_ = json.Unmarshal(body["token"], &token)
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	claims, err := s.office.verifyJWT(token)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if raw := claims["payload"]; len(raw) > 0 {
		if json.Unmarshal(raw, &claims) != nil {
			officeHTTPError(w, officeFail(403, "INVALID_SIGNATURE", "保存回调与签名不一致。"))
			return
		}
	}
	for _, field := range []string{"key", "status", "url", "userdata"} {
		if officeCanonicalRaw(claims[field]) != officeCanonicalRaw(body[field]) {
			officeHTTPError(w, officeFail(403, "INVALID_SIGNATURE", "保存回调与签名不一致。"))
			return
		}
	}
	var key string
	_ = json.Unmarshal(body["key"], &key)
	if key != document.key {
		officeHTTPError(w, officeFail(403, "WRONG_DOCUMENT", "保存回调的文档身份不匹配。"))
		return
	}
	// A successful callback lets the engine delete its cached document. Keep a
	// durable isolated copy before acknowledging; it is never a canonical save.
	if err := s.office.acceptRecoveryCallback(r.Context(), document, body); err != nil {
		writeJSON(w, 200, map[string]int{"error": 1})
		return
	}
	writeJSON(w, 200, map[string]int{"error": 0})
}

func (s *Server) bridgeIdentity(d *officeDocumentSession, instance string) error {
	if d.closed {
		return officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。")
	}
	if d.restarted || d.recoverySealed {
		return officeFail(409, "EDITOR_FENCED", "编辑连接已封存，请保留原画布并查看恢复副本。")
	}
	if instance == "" || instance != d.instanceID {
		return officeFail(409, "EDITOR_INSTANCE_MISMATCH", "该编辑器实例不拥有当前文档操作队列。")
	}
	return nil
}

func officeSanitizeResult(data json.RawMessage) (json.RawMessage, error) {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return nil, officeFail(400, "INVALID_RECEIPT", "编辑器回执不正确。")
	}
	var clean func(any) any
	clean = func(value any) any {
		switch current := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, item := range current {
				switch strings.ToLower(key) {
				case "token", "jwttoken", "jwt", "config", "bridgetoken", "bridgeurl", "filetoken", "authorization", "secret":
					continue
				}
				out[key] = clean(item)
			}
			return out
		case []any:
			for index, item := range current {
				current[index] = clean(item)
			}
			return current
		default:
			return value
		}
	}
	return json.Marshal(clean(value))
}

func (s *Server) handleOfficeBridge(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeCapability(w, r, false, false)
	if !ok {
		return
	}
	resource := r.PathValue("resource")
	if document.readOnly {
		switch resource {
		case "config.json", "index.html", "plugin.js", "sdk.js", "scope.js", "next", "mounted", "detach", "ready", "state", "results":
		default:
			officeHTTPError(w, officeFail(403, "READ_ONLY_DOCUMENT", "只读信息库文档不允许修改、导出或保存。"))
			return
		}
	}
	document.mu.Lock()
	origin := document.browserOrigin
	document.mu.Unlock()
	allowed := officeBoundOrigin(r, origin)
	if origin == "" {
		_, allowed = officeRequestOrigin(r)
	}
	if !allowed {
		officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "该编辑器属于另一个 Pogo 页面来源。"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	switch resource {
	case "config.json", "index.html", "plugin.js", "sdk.js", "scope.js":
		if r.Method != http.MethodGet {
			officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "该资源仅支持读取。"))
			return
		}
		if resource == "scope.js" {
			document.mu.Lock()
			scope := map[string]string{"sessionId": document.id, "documentId": document.documentID, "token": document.bridgeToken, "editor": document.editor}
			document.mu.Unlock()
			data, _ := json.Marshal(scope)
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			fmt.Fprintf(w, "window.__pogoOfficeLabScope=%s;", data)
			return
		}
		data, err := officeruntime.Asset(resource)
		if err != nil {
			officeHTTPError(w, err)
			return
		}
		switch resource {
		case "config.json":
			var config map[string]any
			if json.Unmarshal(data, &config) != nil {
				officeHTTPError(w, errors.New("Office bridge manifest is invalid"))
				return
			}
			config["name"] = "Pogo Office"
			config["baseUrl"] = "/office/bridge/" + document.id + "/" + document.bridgeToken + "/"
			if variations, ok := config["variations"].([]any); ok && len(variations) > 0 {
				if variation, ok := variations[0].(map[string]any); ok {
					if document.readOnly {
						variation["isViewer"] = true
					}
					variation["url"] = "index.html"
					variation["description"] = "Pogo Office 文档操作"
				}
			}
			writeJSON(w, 200, config)
		case "index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
		default:
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Write(data)
		}
		return
	case "batch-freeze":
		s.handleOfficeFreeze(w, r, document)
		return
	case "reload-ready":
		s.handleOfficeReloadReady(w, r, document)
		return
	case "reconcile":
		s.handleOfficeReconcile(w, r, document)
		return
	case "next":
		if r.Method != http.MethodGet {
			officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "next 仅支持读取。"))
			return
		}
		document.mu.Lock()
		defer document.mu.Unlock()
		if err := s.bridgeIdentity(document, r.URL.Query().Get("instanceId")); err != nil {
			officeHTTPError(w, err)
			return
		}
		respond := func(command any) {
			reply := map[string]any{"command": command}
			if recovery := document.recoveryViewLocked(); recovery != nil {
				reply["recovery"] = recovery
			}
			if id := r.URL.Query().Get("inFlightId"); id != "" {
				if op := document.operations[id]; op != nil && op.dispatched {
					reply["flight"] = map[string]any{"operationId": id, "finished": op.finished, "readOnly": officeReadOnly(op.name), "reconciled": op.reconciled, "known": op.finished && !op.unknown, "succeeded": officeOperationSucceeded(op)}
				}
			}
			if value := r.URL.Query().Get("syncRevision"); value != "" && value != strconv.FormatUint(document.syncRevision, 10) {
				reply["session"] = document.viewProjectionLocked(false)
			}
			// Old editor clients do not consume activity. Only upgraded views
			// opt in; neither old nor up-to-date 150ms polls retransmit diffs.
			requestedVersion := r.URL.Query().Get("activityVersion")
			if requestedVersion != "" && requestedVersion != strconv.FormatUint(document.activityVersion, 10) {
				reply["activity"] = document.activityViewLocked()
			}
			writeJSON(w, 200, reply)
		}
		// The view still owns its previous command until it acknowledges the
		// host's completion. Keep control/recovery polling alive without handing
		// it a second command that the view would silently reject as busy.
		if id := r.URL.Query().Get("inFlightId"); id != "" {
			if op := document.operations[id]; op != nil && op.dispatched {
				respond(nil)
				return
			}
		}
		var chosen *officeOperation
		for _, op := range document.queue {
			if op.dispatched && !op.finished {
				respond(nil)
				return
			}
		}
		if document.ready {
			for _, op := range document.queue {
				if !op.dispatched && !op.finished {
					if document.blocked && !officeReadOnly(op.name) {
						continue
					}
					if officeOperationExpired(op) {
						s.office.failOperationLocked(document, op, officeFail(409, "NOT_DISPATCHED", "该操作已过期，未发送到编辑器。"))
						continue
					}
					if err := s.office.prepareDispatchLocked(document, op); err != nil {
						s.office.failOperationLocked(document, op, err)
						continue
					}
					chosen = op
					op.dispatched = true
					op.recoveryReadRevision = document.recoveryRevision
					document.startActivityLocked(op)
					break
				}
			}
		}
		if chosen == nil {
			respond(nil)
		} else {
			if err := s.office.persistEditorLocked(document); err != nil {
				s.office.failOperationLocked(document, chosen, err)
				officeHTTPError(w, err)
				return
			}
			actor := officeActivityAgent
			if item := document.activityLocked(chosen); item != nil {
				actor = item.Actor
			}
			respond(map[string]any{"operationId": chosen.id, "operation": chosen.name, "args": chosen.args, "actor": actor})
		}
		return
	}
	if r.Method != http.MethodPost {
		officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "该操作仅支持 POST。"))
		return
	}
	switch resource {
	case "mounted":
		var body struct {
			InstanceID string `json:"instanceId"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		document.mu.Lock()
		defer document.mu.Unlock()
		if err := s.bridgeIdentity(document, body.InstanceID); err != nil {
			officeHTTPError(w, err)
			return
		}
		document.canvasMounted = true
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.viewLocked()})
	case "detach":
		var body struct {
			InstanceID string `json:"instanceId"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		if !officeOperationID.MatchString(body.InstanceID) {
			officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "编辑器实例身份不正确。"))
			return
		}
		if err := s.office.closeDocumentInstance(document, body.InstanceID); err != nil {
			officeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.view()})
	case "ready":
		var body struct {
			InstanceID   string          `json:"instanceId"`
			Capabilities json.RawMessage `json:"capabilities"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		if !officeOperationID.MatchString(body.InstanceID) || len(body.Capabilities) > 16384 {
			officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "编辑器身份或能力不正确。"))
			return
		}
		var capabilities officeCapabilities
		if json.Unmarshal(body.Capabilities, &capabilities) != nil || capabilities.Editor != document.editor {
			officeHTTPError(w, officeFail(400, "WRONG_EDITOR", "编辑器类型不匹配。"))
			return
		}
		document.mu.Lock()
		defer document.mu.Unlock()
		if document.closed {
			officeHTTPError(w, officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。"))
			return
		}
		if document.restarted || document.recoverySealed {
			officeHTTPError(w, officeFail(409, "EDITOR_FENCED", "原编辑连接已封存，请保留画布。"))
			return
		}
		if document.instanceID != "" && document.instanceID != body.InstanceID {
			officeHTTPError(w, officeFail(409, "EDITOR_ALREADY_CONNECTED", "当前文档已绑定其他编辑器实例。"))
			return
		}
		if document.browserOrigin == "" {
			origin, allowed := officeRequestOrigin(r)
			if !allowed {
				officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "编辑器必须从当前 Pogo 页面打开。"))
				return
			}
			document.browserOrigin = origin
		}
		document.instanceID = body.InstanceID
		document.canvasMounted = true
		document.capabilities, _ = json.Marshal(capabilities)
		document.ready = capabilities.safe()
		document.touchWriterLocked()
		if !document.ready {
			document.lastError = "编辑服务尚未加载已验证的 Pogo 文档历史扩展，Agent 操作暂不可用。"
		} else {
			document.lastError = ""
		}
		if err := s.office.persistEditorLocked(document); err != nil {
			officeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.viewLocked()})
	case "state":
		var body struct {
			InstanceID string `json:"instanceId"`
			Dirty      bool   `json:"dirty"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		document.mu.Lock()
		defer document.mu.Unlock()
		if err := s.bridgeIdentity(document, body.InstanceID); err != nil {
			officeHTTPError(w, err)
			return
		}
		document.canvasMounted = true
		if body.Dirty {
			if document.readOnly {
				officeHTTPError(w, officeFail(403, "READ_ONLY_DOCUMENT", "只读文档不接受修改状态。"))
				return
			}
			document.dirtyEpoch++
			if !document.saving {
				document.saveState = "dirty"
			}
		}
		if err := s.office.persistEditorLocked(document); err != nil {
			officeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.viewLocked()})
	case "results":
		var body struct {
			InstanceID  string          `json:"instanceId"`
			OperationID string          `json:"operationId"`
			OK          *bool           `json:"ok"`
			Result      json.RawMessage `json:"result,omitempty"`
			Error       struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		document.mu.Lock()
		defer document.mu.Unlock()
		if err := s.bridgeIdentity(document, body.InstanceID); err != nil {
			officeHTTPError(w, err)
			return
		}
		op := document.operations[body.OperationID]
		if op == nil || !op.dispatched || op.name == "host.export" {
			officeHTTPError(w, officeFail(409, "INVALID_RECEIPT", "回执不对应已发送的操作。"))
			return
		}
		if op.finished {
			writeJSON(w, 200, map[string]any{"ok": true, "ignored": true})
			return
		}
		if officeOperationExpired(op) {
			s.office.expireLocked(document, op)
			writeJSON(w, 200, map[string]any{"ok": true, "ignored": true})
			return
		}
		if body.OK == nil {
			officeHTTPError(w, officeFail(400, "INVALID_RECEIPT", "回执必须有明确结果。"))
			return
		}
		if *body.OK {
			if op.name == "selection.read" {
				if err := officeSelectionMatchesLocked(document, op.args); err != nil {
					s.office.failOperationLocked(document, op, err)
					writeJSON(w, 200, map[string]any{"ok": true, "session": document.viewLocked()})
					return
				}
			}
			result, err := officeSanitizeResult(body.Result)
			if err != nil {
				officeHTTPError(w, err)
				return
			}
			if bytes.Contains(result, []byte(document.bridgeToken)) || bytes.Contains(result, []byte(document.fileToken)) {
				officeHTTPError(w, officeFail(400, "INVALID_RECEIPT", "回执包含不应传给 Agent 的编辑器凭据。"))
				return
			}
			if officeMutation(op.name) {
				document.dirtyEpoch++
				op.epoch = document.dirtyEpoch
				if !document.saving {
					document.saveState = "dirty"
				}
			}
			s.office.finishLocked(document, op, map[string]any{"ok": true, "operationId": op.id, "result": result})
			officeConfirmRecoveryReadLocked(document, op, result)
		} else {
			if len(body.Error.Code) > 128 || len(body.Error.Message) > 4096 || body.Error.Code == "" {
				officeHTTPError(w, officeFail(400, "INVALID_RECEIPT", "回执错误格式不正确。"))
				return
			}
			if strings.Contains(body.Error.Code+body.Error.Message, document.bridgeToken) || strings.Contains(body.Error.Code+body.Error.Message, document.fileToken) {
				officeHTTPError(w, officeFail(400, "INVALID_RECEIPT", "错误回执包含编辑器凭据。"))
				return
			}
			if !officeReadOnly(op.name) && (body.Error.Code == "OUTCOME_UNKNOWN" || body.Error.Code == "EDITOR_OUTCOME_UNKNOWN") {
				officeMarkUnknownLocked(document, op)
			}
			s.office.finishLocked(document, op, map[string]any{"ok": false, "operationId": op.id, "error": body.Error})
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.viewLocked()})
	case "export":
		var body struct {
			InstanceID  string `json:"instanceId"`
			OperationID string `json:"operationId"`
			FileType    string `json:"fileType"`
			URL         string `json:"url"`
		}
		if err := officeReadBody(r, &body); err != nil {
			officeHTTPError(w, err)
			return
		}
		if err := s.office.exportCurrent(r.Context(), document, body.InstanceID, body.OperationID, body.FileType, body.URL); err != nil {
			officeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "session": document.view()})
	default:
		officeHTTPError(w, officeFail(404, "UNKNOWN_BRIDGE_ACTION", "没有该编辑器桥操作。"))
	}
}
