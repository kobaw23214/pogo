package web

// Viewing a saved file never acquires this lease. It belongs only to the
// current writer; a status/list request cannot make an abandoned canvas live.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
)

const officeWriterLease = 90 * time.Second
const officeRecoveryDeadline = 90 * time.Second

type officeRecoveryCopy struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	Title              string `json:"title"`
	FileType           string `json:"fileType"`
	CreatedAt          string `json:"createdAt"`
	SourceSessionID    string `json:"sourceSessionId"`
	Generation         uint64 `json:"generation"`
	DirtyEpoch         uint64 `json:"dirtyEpoch"`
	PersistedEpoch     uint64 `json:"persistedEpoch"`
	BaseVersion        string `json:"baseVersion"`
	Hash               string `json:"hash,omitempty"`
	Bytes              int    `json:"bytes,omitempty"`
	Error              string `json:"error,omitempty"`
	DownloadURL        string `json:"downloadURL,omitempty"`
	EngineReceiptHash  string `json:"engineReceiptHash,omitempty"`
	Source             string `json:"source,omitempty"`
	Coverage           string `json:"coverage,omitempty"`
	ForgottenChecked   bool   `json:"forgottenChecked,omitempty"`
	ForgottenAttempts  int    `json:"forgottenAttempts,omitempty"`
	ForgottenLastAt    string `json:"forgottenLastAt,omitempty"`
	OriginalRecoveryID string `json:"originalRecoveryId,omitempty"`
}

func (d *officeDocumentSession) connectionStateLocked(now time.Time) string {
	if d.closed {
		return "closed"
	}
	if d.restarted || d.recoverySealed {
		return "disconnected"
	}
	if d.lastSeenAt.IsZero() {
		return "unconfirmed"
	}
	if now.Before(d.leaseExpiresAt) {
		return "online"
	}
	return "disconnected"
}

func (d *officeDocumentSession) touchWriterLocked() {
	if d.leaseEpoch == 0 {
		d.leaseEpoch = 1
	}
	d.lastSeenAt = time.Now().UTC()
	d.leaseExpiresAt = d.lastSeenAt.Add(officeWriterLease)
}

func officeRecoveryMatchesLocked(d *officeDocumentSession) bool {
	c := d.recoveryCopy
	return officeRecoveryTargetMatchesLocked(d, c) && c.State == "ready" && officeVersionValidHash(c.Hash) && c.Bytes > 0 && d.recoverySealed
}

func officeRecoveryTargetMatchesLocked(d *officeDocumentSession, c *officeRecoveryCopy) bool {
	return c != nil && c.SourceSessionID == d.id && c.Generation == d.generation && c.DirtyEpoch == d.dirtyEpoch && c.PersistedEpoch == d.persistedEpoch && c.BaseVersion == d.baseHash
}

func officeHandoffQuietLocked(d *officeDocumentSession) bool {
	return !d.closed && !d.saving && !d.exportBusy && !officePendingLocked(d) && d.freeze == nil && d.batchLease == "" && !d.replacementPending && d.journalError == ""
}

func officeCanTakeoverLocked(d *officeDocumentSession) bool {
	if !officeHandoffQuietLocked(d) || d.connectionStateLocked(time.Now()) == "online" || d.connectionStateLocked(time.Now()) == "unconfirmed" {
		return false
	}
	return !d.blocked && d.dirtyEpoch == d.persistedEpoch || officeRecoveryMatchesLocked(d)
}

func (s *Server) officePresenceRoutes() {
	s.mux.HandleFunc("GET /api/office/access", s.handleOfficeAccess)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/heartbeat", s.handleOfficeHeartbeat)
	s.mux.HandleFunc("POST /api/office/sessions/{oid}/recover", s.handleOfficeRecoverCopy)
	s.mux.HandleFunc("POST /api/office/takeover", s.handleOfficeTakeover)
	s.mux.HandleFunc("POST /api/office/reset", s.handleOfficeReset)
	s.mux.HandleFunc("POST /api/office/saved-copy", s.handleOfficeSavedCopy)
	s.mux.HandleFunc("GET /api/office/recovery-copies/{rid}", s.handleOfficeRecoveryDownload)
}

func (s *Server) handleOfficeHeartbeat(w http.ResponseWriter, r *http.Request) {
	d, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		InstanceID string `json:"instanceId"`
		LeaseEpoch uint64 `json:"leaseEpoch"`
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
	if body.LeaseEpoch == 0 || body.LeaseEpoch != d.leaseEpoch {
		officeHTTPError(w, officeFail(409, "EDITOR_LEASE_CHANGED", "编辑权已改变，请保留当前画布。"))
		return
	}
	d.touchWriterLocked()
	if err := s.office.persistEditorLocked(d); err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": d.viewLocked()})
}

func (s *Server) officeAccessDocument(r *http.Request, conversation, requestedPath string) (*Session, string, string, *officeDocumentSession, error) {
	if s.office == nil {
		return nil, "", "", nil, officeFail(503, "OFFICE_DISABLED", "当前部署尚未配置 Office 编辑服务。")
	}
	if !validID(conversation) || !s.ownsSession(r, conversation) {
		return nil, "", "", nil, officeFail(404, "CONVERSATION_NOT_FOUND", "会话不存在。")
	}
	session, err := s.mgr.Get(conversation)
	if err != nil {
		return nil, "", "", nil, officeFail(404, "CONVERSATION_NOT_FOUND", "会话不存在。")
	}
	absolute, panel, err := s.officeRequestedPath(r, session.owner, requestedPath)
	if err != nil {
		return nil, "", "", nil, err
	}
	if _, err = officePanelPath(session.cwd, absolute); err != nil {
		return nil, "", "", nil, err
	}
	s.office.mu.Lock()
	d := s.office.sessions[s.office.active[absolute]]
	badJournal := s.office.editorJournalError
	s.office.mu.Unlock()
	if badJournal {
		return nil, "", "", nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态记录需要核对，文件仍可只读查看。")
	}
	if d != nil && (!officeSameOwner(d.owner, session.owner) || !s.ownsSession(r, d.conversationID)) {
		return nil, "", "", nil, officeFail(409, "DOCUMENT_ALREADY_OPEN", "文档的编辑权当前不可用，仍可查看已保存版本。")
	}
	return session, absolute, panel, d, nil
}

func (s *Server) handleOfficeAccess(w http.ResponseWriter, r *http.Request) {
	if !s.officeAvailable(w) {
		return
	}
	session, absolute, panel, d, err := s.officeAccessDocument(r, r.URL.Query().Get("conversation_id"), r.URL.Query().Get("path"))
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	data, _, _, err := officeDiskRead(session.cwd, absolute)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	access := map[string]any{"state": "available", "canTakeover": false, "canRecover": false, "canReadForgotten": false, "canReset": false, "resetRequiresOnlineConfirmation": false, "leaseEpoch": uint64(0)}
	var writer *officeSessionView
	copies := []officeRecoveryCopy{}
	if d != nil {
		d.mu.Lock()
		s.office.expireRecoveryCopyLocked(d)
		value := d.viewLocked()
		writer = &value
		access["state"], access["canTakeover"], access["leaseEpoch"] = value.ConnectionState, officeCanTakeoverLocked(d), d.leaseEpoch
		access["canRecover"] = officeHandoffQuietLocked(d) && value.ConnectionState == "disconnected" && (d.dirtyEpoch > d.persistedEpoch || d.blocked) && (d.recoveryCopy == nil || !d.recoverySealed) && d.fileToken != ""
		access["canReadForgotten"] = officeCanReadForgottenLocked(d)
		access["canReset"] = officeCanResetLocked(d)
		access["resetRequiresOnlineConfirmation"] = officeResetNeedsOnlineConfirmationLocked(d)
		access["lastSeenAt"], access["leaseExpiresAt"] = value.LastSeenAt, value.LeaseExpiresAt
		if value.RecoveryCopy != nil {
			copies = append(copies, *value.RecoveryCopy)
			access["recovery"] = value.RecoveryCopy
		}
		d.mu.Unlock()
	}
	// Retired recovery copies remain discoverable after an edit-right handoff.
	archived, err := s.office.recoveryCopiesFor(session.owner, panel)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	for _, copy := range archived {
		found := false
		for _, existing := range copies {
			if copy.ID == existing.ID {
				found = true
			}
		}
		if !found {
			copies = append(copies, copy)
		}
	}
	for i := range copies {
		if copies[i].State == "ready" {
			copies[i].DownloadURL = officeRecoveryDownloadURL(copies[i].ID, session.ID, panel)
		}
	}
	if writer != nil && writer.RecoveryCopy != nil && writer.RecoveryCopy.State == "ready" {
		writer.RecoveryCopy.DownloadURL = officeRecoveryDownloadURL(writer.RecoveryCopy.ID, session.ID, panel)
		access["recovery"] = writer.RecoveryCopy
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"ok": true, "fileRef": officeFileRef(session.owner, panel), "path": panel, "diskVersion": officeHash(data), "writer": writer, "access": access, "recoveryCopies": copies})
}

func officeRecoveryDownloadURL(id, conversation, path string) string {
	return "/api/office/recovery-copies/" + id + "?" + url.Values{"conversation_id": {conversation}, "path": {path}}.Encode()
}

type officeTakeoverRequest struct {
	ConversationID     string `json:"conversationId"`
	Path               string `json:"path"`
	InstanceID         string `json:"instanceId"`
	ExpectedSessionID  string `json:"expectedSessionId"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
	ExpectedDirtyEpoch uint64 `json:"expectedDirtyEpoch"`
	ExpectedVersion    string `json:"expectedVersion"`
	ExpectedLeaseEpoch uint64 `json:"expectedLeaseEpoch"`
	RecoveryID         string `json:"recoveryId,omitempty"`
}

func officeTakeoverMatchesLocked(d *officeDocumentSession, p officeTakeoverRequest) bool {
	return d.id == p.ExpectedSessionID && d.generation == p.ExpectedGeneration && d.dirtyEpoch == p.ExpectedDirtyEpoch && d.leaseEpoch == p.ExpectedLeaseEpoch
}

func (s *Server) handleOfficeTakeover(w http.ResponseWriter, r *http.Request) {
	var p officeTakeoverRequest
	if err := officeReadBody(r, &p); err != nil {
		officeHTTPError(w, err)
		return
	}
	if !officeOperationID.MatchString(p.InstanceID) || !officeSessionID.MatchString(p.ExpectedSessionID) || !officeVersionValidHash(p.ExpectedVersion) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "编辑权接管的身份或版本不正确。"))
		return
	}
	session, absolute, panel, d, err := s.officeAccessDocument(r, p.ConversationID, p.Path)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if d == nil {
		officeHTTPError(w, officeFail(409, "EDITOR_LEASE_CHANGED", "文档编辑状态已改变，请重新查看。"))
		return
	}
	if _, ok := officeRequestOrigin(r); !ok {
		officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "请从当前 Pogo 页面接管。"))
		return
	}
	fingerprintBytes, _ := json.Marshal(p)
	fingerprint := officeHash(fingerprintBytes)
	d.mu.Lock()
	alreadyHandedOff := d.handoffFromSessionID == p.ExpectedSessionID && d.handoffFingerprint == fingerprint && d.instanceID == p.InstanceID && d.conversationID == p.ConversationID && !d.closed
	d.mu.Unlock()
	if alreadyHandedOff {
		s.writeOfficeConfigRequest(w, r, d, p.InstanceID, releaseMaintenanceGrant(r), p.Path)
		return
	}
	if err = s.office.waitEditorReady(r.Context()); err != nil {
		officeHTTPError(w, err)
		return
	}
	var fresh *officeDocumentSession
	err = tools.WithFileMutationLock(absolute, func() error {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.closing {
			return officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。")
		}
		s.office.mu.Lock()
		defer s.office.mu.Unlock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if s.office.active[absolute] != d.id || !officeTakeoverMatchesLocked(d, p) || !officeCanTakeoverLocked(d) {
			return officeFail(409, "EDITOR_LEASE_CHANGED", "编辑权已续期或状态已变化，未执行接管。")
		}
		data, _, _, e := officeDiskRead(session.cwd, absolute)
		if e != nil {
			return e
		}
		if officeHash(data) != p.ExpectedVersion {
			return officeFail(409, "FILE_CHANGED", "文件版本已变化，请重新查看。")
		}
		if d.dirtyEpoch > d.persistedEpoch || d.blocked {
			if !officeRecoveryMatchesLocked(d) || p.RecoveryID != d.recoveryCopy.ID {
				return officeFail(409, "RECOVERY_REQUIRED", "原编辑内容尚未保留到恢复副本，不能接管。")
			}
			if _, e = s.office.readRecoveryCopyLocked(d); e != nil {
				return e
			}
		}
		// Persist the retirement before issuing any new writer. Old keys/tokens
		// remain fenced even if creating the new editor fails halfway through.
		d.closed = true
		if e = s.office.persistEditorLocked(d); e != nil {
			d.closed = false
			return e
		}
		d.closed = false
		d.retirementAuthorized = true
		s.office.retireLocked(d)
		canonical, e := tools.Resolve(session.cwd, ".")
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(canonical, absolute)
		if e != nil {
			return e
		}
		journal := s.mgr.JournalFor(session.root).ForSession(session.ID)
		fresh, e = s.office.createDocumentLocked(session.ID, session.cwd, session.owner, session.hub, absolute, panel, journal, canonical, relative, d.fileType)
		if e == nil {
			fresh.handoffFromSessionID, fresh.handoffFingerprint, fresh.instanceID = p.ExpectedSessionID, fingerprint, p.InstanceID
			fresh.touchWriterLocked()
			e = s.office.persistEditorLocked(fresh)
		}
		return e
	})
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	s.writeOfficeConfigRequest(w, r, fresh, p.InstanceID, releaseMaintenanceGrant(r), p.Path)
}

type officeRecoverRequest struct {
	ExpectedGeneration uint64 `json:"expectedGeneration"`
	ExpectedDirtyEpoch uint64 `json:"expectedDirtyEpoch"`
	ExpectedLeaseEpoch uint64 `json:"expectedLeaseEpoch"`
	ExpectedVersion    string `json:"expectedVersion"`
	ReadForgotten      bool   `json:"readForgotten,omitempty"`
}

func (s *Server) handleOfficeRecoverCopy(w http.ResponseWriter, r *http.Request) {
	d, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	var p officeRecoverRequest
	if err := officeReadBody(r, &p); err != nil {
		officeHTTPError(w, err)
		return
	}
	d.mu.Lock()
	if p.ExpectedGeneration != d.generation || p.ExpectedDirtyEpoch != d.dirtyEpoch || p.ExpectedLeaseEpoch != d.leaseEpoch || p.ExpectedVersion != d.baseHash {
		d.mu.Unlock()
		officeHTTPError(w, officeFail(409, "EDITOR_LEASE_CHANGED", "恢复目标已变化，请重新查看。"))
		return
	}
	if d.recoveryCopy != nil && d.recoverySealed {
		s.office.expireRecoveryCopyLocked(d)
		if p.ReadForgotten && officeCanReadForgottenLocked(d) {
			id := d.recoveryCopy.ID
			d.mu.Unlock()
			_ = s.office.readForgottenRecovery(r.Context(), d, id)
			d.mu.Lock()
		}
		copy, view := *d.recoveryCopy, d.viewLocked()
		d.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "state": copy.State, "recoveryCopy": copy, "session": view})
		return
	}
	if p.ReadForgotten {
		d.mu.Unlock()
		officeHTTPError(w, officeFail(409, "RECOVERY_REQUIRED", "请先封存并核对原编辑状态，此操作不会启动新的保存。"))
		return
	}
	if !officeHandoffQuietLocked(d) || d.connectionStateLocked(time.Now()) != "disconnected" {
		d.mu.Unlock()
		officeHTTPError(w, officeFail(409, "EDITOR_STILL_CONNECTED", "编辑窗口仍在线或有操作待确认，请保留原画布。"))
		return
	}
	if d.fileToken == "" {
		d.mu.Unlock()
		officeHTTPError(w, officeFail(409, "RECOVERY_UNAVAILABLE", "旧编辑状态已保留，但旧版本没有可验证的恢复回调身份。"))
		return
	}
	copy := &officeRecoveryCopy{ID: officeNewID(), State: "pending", Title: "恢复副本-" + d.title, FileType: d.fileType, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceSessionID: d.id, Generation: d.generation, DirtyEpoch: d.dirtyEpoch, PersistedEpoch: d.persistedEpoch, BaseVersion: d.baseHash}
	d.recoveryCopy, d.recoverySealed = copy, true
	if err := s.office.persistEditorLocked(d); err != nil {
		d.mu.Unlock()
		officeHTTPError(w, err)
		return
	}
	key, recoveryID := d.key, copy.ID
	if s.office.gateway != nil {
		s.office.gateway.revokeDocument(key)
	}
	d.mu.Unlock()
	// The command is sent once. A lost acknowledgement is not permission to
	// repeat it: the signed callback may still complete this exact recovery ID.
	err := s.office.requestRecoveryForceSave(r.Context(), key, recoveryID)
	if officeForceSaveDefinitivelyUnavailable(err) {
		_ = s.office.readForgottenRecovery(r.Context(), d, recoveryID)
	}
	d.mu.Lock()
	if err != nil && d.recoveryCopy != nil && d.recoveryCopy.ID == recoveryID && d.recoveryCopy.State == "pending" {
		d.recoveryCopy.State = "unknown"
		d.recoveryCopy.Error = "恢复请求的结果尚未确认，原修改不会自动放弃。"
		_ = s.office.persistEditorLocked(d)
	}
	result, view := *d.recoveryCopy, d.viewLocked()
	d.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "state": result.State, "recoveryCopy": result, "session": view})
}

type officeRecoveryCommandReply struct {
	Error *int   `json:"error"`
	Key   string `json:"key"`
	URL   string `json:"url"`
}
type officeRecoveryCommandError struct{ code int }

func (e *officeRecoveryCommandError) Error() string {
	return "编辑服务未提供可确认的恢复快照。"
}
func officeForceSaveDefinitivelyUnavailable(err error) bool {
	var command *officeRecoveryCommandError
	return errors.As(err, &command) && (command.code == 1 || command.code == 4 || command.code == -404 || command.code == -405)
}

func (o *officeService) requestRecoveryForceSave(ctx context.Context, key, recoveryID string) error {
	_, err := o.recoveryCommand(ctx, "forcesave", key, recoveryID)
	return err
}

func (o *officeService) recoveryCommand(ctx context.Context, command, key, recoveryID string) (officeRecoveryCommandReply, error) {
	if command != "forcesave" && command != "getForgotten" {
		return officeRecoveryCommandReply{}, officeFail(400, "INVALID_ARGUMENT", "不支持此恢复命令。")
	}
	payload := map[string]any{"c": command, "key": key}
	if recoveryID != "" {
		payload["userdata"] = recoveryID
	}
	token, err := o.signJWT(payload)
	if err != nil {
		return officeRecoveryCommandReply{}, err
	}
	payload["token"] = token
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "POST", o.cfg.serviceURL+"/command?"+url.Values{"shardkey": {key}}.Encode(), bytes.NewReader(body))
	if err != nil {
		return officeRecoveryCommandReply{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := o.cfg.client.Do(r)
	if err != nil {
		return officeRecoveryCommandReply{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if response.StatusCode == 404 || response.StatusCode == 405 {
		return officeRecoveryCommandReply{}, &officeRecoveryCommandError{code: -response.StatusCode}
	}
	if err != nil || len(data) > 65536 || response.StatusCode != 200 {
		return officeRecoveryCommandReply{}, officeFail(502, "RECOVERY_UNCONFIRMED", "编辑服务尚未确认恢复请求。")
	}
	var result officeRecoveryCommandReply
	if json.Unmarshal(data, &result) != nil || result.Error == nil || (result.Key != "" && result.Key != key) {
		return result, officeFail(502, "RECOVERY_UNCONFIRMED", "编辑服务尚未确认恢复请求。")
	}
	if *result.Error != 0 {
		return result, &officeRecoveryCommandError{code: *result.Error}
	}
	return result, nil
}

func (o *officeService) expireRecoveryCopyLocked(d *officeDocumentSession) {
	if d.recoveryCopy == nil || d.recoveryCopy.State != "pending" {
		return
	}
	created, err := time.Parse(time.RFC3339Nano, d.recoveryCopy.CreatedAt)
	if err != nil || time.Since(created) > officeRecoveryDeadline {
		d.recoveryCopy.State = "unknown"
		d.recoveryCopy.Error = "恢复副本尚未确认，请保留原画布和已有文件。"
		_ = o.persistEditorLocked(d)
	}
}

// Only the explicitly correlated signed force-save callback enters this path.
// Ordinary autosave/final callbacks remain acknowledgements, never commits.
func (o *officeService) acceptRecoveryCallback(ctx context.Context, d *officeDocumentSession, body map[string]json.RawMessage) error {
	var id, key, address string
	var status int
	_ = json.Unmarshal(body["userdata"], &id)
	_ = json.Unmarshal(body["key"], &key)
	_ = json.Unmarshal(body["url"], &address)
	_ = json.Unmarshal(body["status"], &status)
	d.mu.Lock()
	copy := d.recoveryCopy
	receiptBytes, _ := json.Marshal([]any{key, status, address, id})
	receiptHash := officeHash(receiptBytes)
	if key != d.key {
		d.mu.Unlock()
		return officeFail(403, "WRONG_DOCUMENT", "恢复文件身份不匹配。")
	}
	if status != 2 && status != 6 && status != 7 {
		d.mu.Unlock()
		return nil
	}
	var source *url.URL
	if status == 2 || status == 6 {
		var err error
		source, err = o.exportURL(address, d.browserOrigin)
		if err != nil || !officeExportDocumentPath(source.Path, d.key) {
			d.mu.Unlock()
			return officeFail(403, "RECOVERY_DOCUMENT_REJECTED", "恢复文件不属于当前文档。")
		}
	}
	if id != "" && (copy == nil || copy.ID != id) {
		if status == 6 && source != nil {
			d.mu.Unlock()
			return o.archiveLateRecoveryCallback(ctx, d, id, receiptHash, source)
		}
		d.mu.Unlock()
		return officeFail(409, "RECOVERY_CHANGED", "恢复回执不属于当前恢复请求。")
	}
	if id == "" && (status == 2 || status == 6) {
		if copy != nil && copy.State == "ready" && copy.EngineReceiptHash == receiptHash {
			d.mu.Unlock()
			return nil
		}
		retryingSameReceipt := copy != nil && copy.EngineReceiptHash == receiptHash
		// Revoking the old gateway can make the engine finish the document
		// before the explicit force-save command is serviced. Its signed final
		// callback has no userdata, but belongs to this one sealed engine key.
		// Correlate only the exact unchanged recovery target, never an ordinary
		// status-6 checkpoint or a callback for an older dirty/base generation.
		sealedFinal := status == 2 && copy != nil && d.recoverySealed && copy.State != "ready"
		if sealedFinal && !officeRecoveryTargetMatchesLocked(d, copy) {
			d.mu.Unlock()
			return officeFail(409, "RECOVERY_CHANGED", "最终快照对应的恢复目标已变化，未释放原占用。")
		}
		if copy != nil && d.recoverySealed && copy.State != "ready" && !retryingSameReceipt && !sealedFinal {
			d.mu.Unlock()
			return officeFail(409, "RECOVERY_PENDING", "正在核对指定恢复请求，自动回执仍需保留。")
		}
		// Ordinary final-save/force-save callbacks are also durable recovery
		// evidence. They do not prove the canonical file has been committed.
		if !retryingSameReceipt && (!sealedFinal || copy.Hash != "") {
			copy = &officeRecoveryCopy{ID: officeNewID(), State: "pending", Title: "恢复副本-" + d.title, FileType: d.fileType, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceSessionID: d.id, Generation: d.generation, DirtyEpoch: d.dirtyEpoch, PersistedEpoch: d.persistedEpoch, BaseVersion: d.baseHash, EngineReceiptHash: receiptHash}
			d.recoveryCopy = copy
		}
		if status == 2 {
			d.recoverySealed = true
			if o.gateway != nil {
				o.gateway.revokeDocument(d.key)
			}
		}
		if err := o.persistEditorLocked(d); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	if copy == nil {
		d.mu.Unlock()
		return nil
	}
	if copy.State == "ready" {
		if copy.EngineReceiptHash != "" && copy.EngineReceiptHash != receiptHash {
			if status == 6 && id != "" && source != nil {
				d.mu.Unlock()
				return o.archiveLateRecoveryCallback(ctx, d, id, receiptHash, source)
			}
			d.mu.Unlock()
			return officeFail(409, "RECOVERY_RECEIPT_CHANGED", "恢复回执已改变，未覆盖既有副本。")
		}
		d.mu.Unlock()
		return nil
	}
	if status == 7 {
		copy.State = "failed"
		copy.Error = "编辑服务未能导出恢复副本，原修改尚未放弃。"
		err := o.persistEditorLocked(d)
		d.mu.Unlock()
		return err
	}
	if status != 6 && status != 2 {
		d.mu.Unlock()
		return nil
	}
	copy.EngineReceiptHash = receiptHash
	copy.Coverage = "engine-snapshot"
	if status == 2 {
		copy.Source = "final"
	} else {
		copy.Source = "forcesave"
	}
	if (id != "" && !d.recoverySealed) || copy.SourceSessionID != d.id || copy.Generation != d.generation || copy.DirtyEpoch != d.dirtyEpoch || copy.PersistedEpoch != d.persistedEpoch || copy.BaseVersion != d.baseHash {
		d.mu.Unlock()
		return officeFail(409, "RECOVERY_CHANGED", "恢复目标已变化，未接受旧快照。")
	}
	recoveryID, fileType := copy.ID, d.fileType
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", source.String(), nil)
	if err != nil {
		return err
	}
	response, err := o.cfg.client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return officeFail(502, "RECOVERY_FETCH_FAILED", "恢复副本尚未下载完成。")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, officeMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > officeMaxBytes {
		return officeFail(413, "DOCUMENT_LIMIT", "恢复副本超过当前文档大小上限。")
	}
	if err = officeCheckPackage(data, fileType); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.recoveryCopy == nil || d.recoveryCopy.ID != recoveryID || d.recoveryCopy.DirtyEpoch != d.dirtyEpoch || d.recoveryCopy.Generation != d.generation || d.recoveryCopy.BaseVersion != d.baseHash || d.recoveryCopy.EngineReceiptHash != receiptHash || (id != "" && !d.recoverySealed) {
		return officeFail(409, "RECOVERY_CHANGED", "恢复目标已变化，未接受旧快照。")
	}
	if d.recoveryCopy.State == "ready" {
		if d.recoveryCopy.Hash != officeHash(data) {
			return officeFail(409, "RECOVERY_STORAGE_CHANGED", "重复回执的文件字节不同，不会覆盖已保留副本。")
		}
		return nil
	}
	if err = o.storeRecoveryCopyLocked(d, data); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleOfficeRecoveryDownload(w http.ResponseWriter, r *http.Request) {
	if !officeSessionID.MatchString(r.PathValue("rid")) {
		officeHTTPError(w, officeFail(404, "RECOVERY_NOT_FOUND", "恢复副本不存在。"))
		return
	}
	session, _, panel, _, err := s.officeAccessDocument(r, r.URL.Query().Get("conversation_id"), r.URL.Query().Get("path"))
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	copy, data, err := s.office.readArchivedRecoveryCopy(session.owner, panel, r.PathValue("rid"))
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	mime := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if copy.FileType == "xlsx" {
		mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(copy.Title))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// Kept private to the operator's journal. No engine key, token, absolute host
// path or private storage path is present in the public recovery receipt.
func officeRecoveryKeyName(id, fileType string) string { return id + "." + strings.ToLower(fileType) }

// Creating an independently editable copy is always available, even when an
// abandoned dirty writer cannot be recovered. Its unconfirmed content and its
// occupancy are untouched; only the current saved version is copied.
func (s *Server) handleOfficeSavedCopy(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ConversationID  string `json:"conversationId"`
		Path            string `json:"path"`
		ExpectedVersion string `json:"expectedVersion"`
		OperationID     string `json:"operationId"`
	}
	if err := officeReadBody(r, &p); err != nil {
		officeHTTPError(w, err)
		return
	}
	if !officeOperationID.MatchString(p.OperationID) || !officeVersionValidHash(p.ExpectedVersion) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "副本操作身份或已保存版本不正确。"))
		return
	}
	session, source, panel, _, err := s.officeAccessDocument(r, p.ConversationID, p.Path)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if !isOfficeFileType(source) {
		officeHTTPError(w, officeFail(400, "UNSUPPORTED_DOCUMENT", "仅支持 Word 和 Excel 文档副本。"))
		return
	}
	identity, _ := json.Marshal([]any{session.owner, session.ID, source, p.ExpectedVersion, p.OperationID})
	nonce := officeHash(identity)[:20]
	ext := filepath.Ext(source)
	base := []rune(strings.TrimSuffix(filepath.Base(source), ext))
	if len(base) > 60 {
		base = base[:60]
	}
	targetName := string(base) + "-已保存副本-" + nonce + ext
	target := filepath.Join(filepath.Dir(source), targetName)
	var reused bool
	err = tools.WithFileMutationLock(source, func() error {
		return tools.WithFileMutationLock(target, func() error {
			session.mu.Lock()
			defer session.mu.Unlock()
			if session.closing {
				return officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。")
			}
			data, _, parentInfo, e := officeDiskRead(session.cwd, source)
			if e != nil {
				return e
			}
			if officeHash(data) != p.ExpectedVersion {
				return officeFail(409, "FILE_CHANGED", "已保存版本已更新，请重新查看后创建副本。")
			}
			parent, _, currentParent, e := officeOpenParent(session.cwd, source)
			if e != nil {
				return e
			}
			defer parent.Close()
			if !os.SameFile(parentInfo, currentParent) {
				return officeFail(409, "FILE_IDENTITY_CHANGED", "文档目录身份已改变。")
			}
			if existing, _, e := officeReadFile(parent, targetName); e == nil {
				if officeHash(existing) != p.ExpectedVersion {
					return officeFail(409, "COPY_ID_CONFLICT", "同一副本操作已存在不同内容，不会覆盖。")
				}
				reused = true
			} else {
				if !os.IsNotExist(e) {
					return e
				}
				temp := ".pogo-office-copy-" + randomID(16) + ".tmp"
				f, e := parent.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e != nil {
					return e
				}
				defer parent.Remove(temp)
				_, writeErr := f.Write(data)
				syncErr := f.Sync()
				closeErr := f.Close()
				if writeErr != nil {
					return writeErr
				}
				if syncErr != nil {
					return syncErr
				}
				if closeErr != nil {
					return closeErr
				}
				s.mgr.JournalFor(session.root).ForSession(session.ID).BeforeChange(target, nil, false)
				// Link publishes a complete inode without ever replacing an existing
				// path. A lost HTTP response is retried with the exact same nonce.
				if e = parent.Link(temp, targetName); e != nil {
					return e
				}
				if e = parent.Remove(temp); e != nil {
					return e
				}
				if directory, e := parent.Open("."); e == nil {
					e = directory.Sync()
					directory.Close()
					if e != nil {
						return e
					}
				} else {
					return e
				}
			}
			return s.office.registerCommandProtectedPath(session.cwd, session.owner, target)
		})
	})
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	copyPanel, err := officePanelPath(s.requestRoot(r), target)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": copyPanel, "title": targetName, "diskVersion": p.ExpectedVersion, "sourcePath": panel, "includesUnsavedChanges": false, "reused": reused})
}

func isOfficeFileType(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".docx" || ext == ".xlsx"
}
